// file: core/internal/executor/executor.go
// version: 1.0.0
// guid: a39876a5-3dce-4f6d-9003-f07fdeb8a28d
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// retryAttempts bounds WriteRuns' RetryOnConflict (5T.7: bounded, jittered).
const retryAttempts = 8

// moveAttempts bounds how often a move re-reads after its staging read
// raced another commit (ErrRunConflict from Transition while the edit's
// own fresh read still accepts the run).
const moveAttempts = 3

// def is one registered definition with its shape defaults resolved.
type def struct {
	kind     tasks.Kind
	module   string
	shape    tasks.Shape
	c        tasks.Common
	par      tasks.Parallelism
	key      tasks.Key
	timeout  time.Duration
	progTO   time.Duration
	grace    time.Duration
	never    bool // Uninterruptible
	readOnly bool
	writes   []entity.Ref

	decode func(json.RawMessage) (any, error)
	task   *tasks.TaskSpec[any, json.RawMessage]
	batch  *tasks.BatchSpec[any, any, json.RawMessage]
	fixer  *tasks.FixerSpec[any, any]
}

// erased is the hidden method core/tasks' Definition implementation
// offers this package (core/tasks/erased.go).
type erased interface {
	ExecutorRunners() (decode func(json.RawMessage) (any, error),
		task *tasks.TaskSpec[any, json.RawMessage], batch *tasks.BatchSpec[any, any, json.RawMessage], fixer *tasks.FixerSpec[any, any])
}

// Shape default timeouts (5T.3.6; HEAD pkg/ops/definition.go's).
const (
	defaultTaskTimeout  = 30 * time.Minute
	defaultBatchTimeout = 24 * time.Hour
	defaultFixerTimeout = 6 * time.Hour
)

func newDef(d tasks.Definition) (*def, error) {
	if probs := tasks.Problems(d); len(probs) > 0 {
		return nil, errors.Join(probs...)
	}
	r, ok := d.(erased)
	if !ok {
		return nil, fmt.Errorf("executor: %s: definition offers no runners", d.Kind())
	}
	c := tasks.CommonOf(d)
	x := &def{kind: d.Kind(), module: d.Kind().Module(), shape: d.Shape(), c: c, par: tasks.ParallelismOf(d)}
	x.decode, x.task, x.batch, x.fixer = r.ExecutorRunners()
	if x.task == nil && x.batch == nil && x.fixer == nil {
		return nil, fmt.Errorf("executor: %s: definition has no body", x.kind)
	}
	x.key = c.ConcurrencyKey
	if x.key == "" {
		x.key = tasks.Key(x.kind)
	}
	// 5T.3.4: a module may use keys in its own namespace and in core.
	if m := x.key.Module(); m != x.module && m != "core" {
		return nil, fmt.Errorf("executor: %s: ConcurrencyKey %q is outside the module's namespace and core", x.kind, x.key)
	}
	x.timeout = c.Timeout
	if x.timeout == 0 {
		switch x.shape {
		case tasks.ShapeBatch:
			x.timeout = defaultBatchTimeout
		case tasks.ShapeFixer:
			x.timeout = defaultFixerTimeout
		default:
			x.timeout = defaultTaskTimeout
		}
	}
	x.progTO = c.ProgressTimeout
	if x.progTO == 0 {
		x.progTO = max(x.timeout/6, time.Minute)
	}
	switch {
	case c.Cancel.IsUninterruptible():
		x.never, x.grace = true, c.Cancel.Budget()
	case c.Cancel.Grace() > 0:
		x.grace = c.Cancel.Grace()
	default:
		x.grace = tasks.DefaultCancelGrace
	}
	x.readOnly = c.Effects.IsReadOnly()
	x.writes = c.Effects.WriteSet()
	return x, nil
}

// executor is tasks.Executor. Every field is per instance (R5).
type executor struct {
	opts     Options
	cfg      atomic.Pointer[Config]
	log      *slog.Logger
	now      func() time.Time
	runs     tasks.RunStore
	newBatch func() *storage.Batch
	idem     *taskstore.Store
	defs     map[tasks.Kind]*def
	heldMods map[string]bool
	heldKind map[tasks.Kind]string
	starts   map[string][]tasks.Kind // module -> TaskStarts
	lanes    map[tasks.LaneID]*lane
	cpu      *cpuBudget
	refs     *refTable
	ins      *instruments

	startMu sync.Mutex // serialises dedupe-folding starts (taken before WriteRuns)

	mu        sync.Mutex
	running   map[tasks.RunID]*attempt
	zombies   map[string]int
	paused    bool
	unpaused  chan struct{} // closed while not paused
	holds     map[tasks.Key]*standHold
	bootQuiet map[tasks.RunID]bool // quiesced by a marker that survived a crash: not resumed this boot
	waiting   map[string]bool      // Ready sets with a live waiter
	deferred  map[tasks.RunID]string
	heldCount map[string]int64 // unfinished held runs by module, as of the resume sweep
	termCh    chan struct{}    // closed and replaced on every terminal transition
	shutting  bool
	runCalled bool
	shutDone  bool
	bgCtx     context.Context
	bgStop    context.CancelFunc
	bg        sync.WaitGroup
	wake      chan struct{}
}

// New builds an executor. It registers opts.Definitions (a definition with
// problems, a duplicate kind, or a ConcurrencyKey outside its module's
// namespace and core fails it) and creates the instruments unless
// InstrumentsOff. It starts nothing: Run does.
func New(opts Options) (tasks.Executor, error) {
	if err := opts.fill(); err != nil {
		return nil, err
	}
	cfg := opts.Config.withDefaults()
	e := &executor{
		opts: opts, log: opts.Logger, now: opts.Now, runs: opts.Stores.Runs, newBatch: opts.NewBatch,
		defs: map[tasks.Kind]*def{}, heldMods: map[string]bool{}, heldKind: map[tasks.Kind]string{},
		starts: map[string][]tasks.Kind{}, lanes: map[tasks.LaneID]*lane{},
		cpu: newCPUBudget(cfg.CPUSlots), refs: newRefTable(),
		running: map[tasks.RunID]*attempt{}, zombies: map[string]int{}, holds: map[tasks.Key]*standHold{},
		bootQuiet: map[tasks.RunID]bool{}, waiting: map[string]bool{}, deferred: map[tasks.RunID]string{},
		heldCount: map[string]int64{},
		termCh:    make(chan struct{}), unpaused: make(chan struct{}), wake: make(chan struct{}, 1),
	}
	close(e.unpaused)
	e.cfg.Store(&cfg)
	e.bgCtx, e.bgStop = context.WithCancel(context.Background())
	if opts.Keyspace != nil {
		e.idem = taskstore.New(*opts.Keyspace)
	}
	for _, d := range opts.Definitions {
		if d == nil {
			return nil, errors.New("executor: nil definition")
		}
		x, err := newDef(d)
		if err != nil {
			return nil, err
		}
		if _, dup := e.defs[x.kind]; dup {
			return nil, fmt.Errorf("executor: kind %s registered twice", x.kind)
		}
		e.defs[x.kind] = x
	}
	for _, m := range opts.Manifests {
		e.starts[m.Module] = slices.Clone(m.TaskStarts)
		if !m.Enabled {
			e.heldMods[m.Module] = true
			for _, k := range m.TaskLedger {
				e.heldKind[k] = m.Module
			}
		}
	}
	for _, l := range opts.Lanes {
		e.lanes[l.ID] = newLane(l)
	}
	if !opts.InstrumentsOff {
		ins, err := newInstruments(opts.Metrics, e)
		if err != nil {
			return nil, err
		}
		e.ins = ins
	}
	return e, nil
}

func (e *executor) config() Config { return *e.cfg.Load() }

// held reports whether kind's runs are held: its module is disabled, or a
// disabled manifest's TaskLedger lists it (5T.3.11).
func (e *executor) held(kind tasks.Kind) bool {
	if _, ok := e.heldKind[kind]; ok {
		return true
	}
	return e.heldMods[kind.Module()]
}

// view is a record's read model with Held computed (never stored).
func (e *executor) view(r tasks.RunRecord) tasks.RunView {
	v := r.RunView
	v.Held = e.held(r.Kind)
	return v
}

// stageKey marks a WriteRuns stage's context (nested detection).
type stageKey struct{}

// WriteRuns implements tasks.Stager/Executor.WriteRuns (5T.3.10, 5T.7): a
// fresh batch per attempt, stage, then Commit(storage.Sync), all under
// storage.RetryOnConflict. Nested inside another stage it returns
// tasks.ErrWriteRunsNested and runs nothing.
func (e *executor) WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error {
	if ctx.Value(stageKey{}) != nil {
		return tasks.ErrWriteRunsNested
	}
	if open == nil || stage == nil {
		return errors.New("executor: WriteRuns needs open and stage")
	}
	sctx := context.WithValue(ctx, stageKey{}, true)
	return storage.RetryOnConflict(ctx, retryAttempts, func(ctx context.Context) error {
		b := open()
		if b == nil {
			return errors.New("executor: WriteRuns: open returned a nil batch")
		}
		defer func() { _ = b.Close() }() // idempotent; a no-op after Commit
		if err := stage(sctx, b); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
}

// write is WriteRuns over the executor's own batch opener.
func (e *executor) write(ctx context.Context, stage func(ctx context.Context, b *storage.Batch) error) error {
	return e.WriteRuns(ctx, e.newBatch, stage)
}

// editFunc decides a move from the run's fresh read: the next record and
// the event naming it, or an error that refuses the move.
type editFunc func(cur tasks.RunRecord) (next tasks.RunRecord, ev tasks.RunEvent, err error)

// move is the one CAS transition path (5T.7): inside WriteRuns it re-reads
// the run, lets edit decide from that read, stages Transition with the read
// as want, appends the event, stages extra and, for a transition into a
// terminal state, Options.OnTerminal in the same batch. A storage conflict
// re-stages everything from a fresh read; a refused edit or a lost want
// (ErrRunConflict) stops.
func (e *executor) move(ctx context.Context, id tasks.RunID, edit editFunc,
	extra func(ctx context.Context, b *storage.Batch, next tasks.RunRecord) error) (tasks.RunRecord, error) {
	var out, prev tasks.RunRecord
	var err error
	for range moveAttempts {
		err = e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
			cur, err := e.runs.Get(ctx, id)
			if err != nil {
				return err
			}
			next, ev, err := edit(cur)
			if err != nil {
				return err
			}
			next.Seq.Event = cur.Seq.Event + 1
			ev.From, ev.To, ev.Attempt, ev.FenceEpoch = cur.State, next.State, next.Attempt, next.FenceEpoch
			if ev.At.IsZero() {
				ev.At = e.now()
			}
			want := tasks.Expect{State: cur.State, Attempt: cur.Attempt, FenceEpoch: cur.FenceEpoch}
			if err := e.runs.Transition(ctx, b, id, want, next); err != nil {
				return err
			}
			if err := e.runs.AppendEvent(ctx, b, id, next.Seq.Event, ev); err != nil {
				return err
			}
			if extra != nil {
				if err := extra(ctx, b, next); err != nil {
					return err
				}
			}
			if next.State.Terminal() && !cur.State.Terminal() && e.opts.OnTerminal != nil {
				if err := e.opts.OnTerminal(ctx, b, e.view(next)); err != nil {
					return fmt.Errorf("executor: OnTerminal: %w", err)
				}
			}
			prev, out = cur, next
			return nil
		})
		var r refusal
		if !errors.Is(err, tasks.ErrRunConflict) || errors.As(err, &r) {
			break
		}
	}
	if err != nil {
		return tasks.RunRecord{}, err
	}
	e.moved(prev, out)
	return out, nil
}

// errStale is an edit's refusal: the fresh read is not a run this move
// applies to. It wraps tasks.ErrRunConflict.
func errStale(cur tasks.RunRecord, why string) error {
	return refusal{fmt.Errorf("%w: run %s is %s (attempt %d, fence %d): %s", tasks.ErrRunConflict, cur.ID, cur.State, cur.Attempt, cur.FenceEpoch, why)}
}

// refusal is an edit's own refusal; move does not re-read after one, since
// the fresh read already said no.
type refusal struct{ error }

func (r refusal) Unwrap() error { return r.error }

// moved runs after a committed transition: terminal broadcast, metrics,
// dispatcher wake.
func (e *executor) moved(prev, next tasks.RunRecord) {
	if next.State.Terminal() && !prev.State.Terminal() {
		e.mu.Lock()
		close(e.termCh)
		e.termCh = make(chan struct{})
		delete(e.deferred, next.ID)
		e.mu.Unlock()
		e.ins.runEnded(next)
	}
	e.wakeDispatcher()
}

// termSignal is closed at the next terminal transition.
func (e *executor) termSignal() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.termCh
}

func (e *executor) wakeDispatcher() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
