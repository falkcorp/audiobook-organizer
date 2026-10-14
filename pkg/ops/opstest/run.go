// file: pkg/ops/opstest/run.go
// version: 1.0.0
// guid: 9a4f6b2d-1c83-4e57-b0d9-3e8a5f7c2b61
// last-edited: 2026-10-14

package opstest

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// minWorkers is the fewest workers a run ever gets (owner decision D28:
// parallel by default; the harness proves the invariants concurrently).
const minWorkers = 4

// RunOpt configures one World.Run or Outcome.Resume.
type RunOpt func(*runCfg)

type runCfg struct {
	live    bool
	seed    *int64
	workers int
	chunk   int

	cancelAt, crashAt, abandonAt, loseLeaseAt int
	crashMid                                  *[2]int
	insert                                    *insertSpec

	chunker     func(keys, parts []string, size int) (order, starts []int)
	leasePolicy func(done func(chunk int) bool, chunk int) int
	newSink     func() ProgressSink

	err error // an invalid option, reported by Run
}

type insertSpec struct{ item any }

func newRunCfg(opts []RunOpt) runCfg {
	c := runCfg{workers: max(runtime.GOMAXPROCS(0), minWorkers), cancelAt: -1, crashAt: -1, abandonAt: -1, loseLeaseAt: -1}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// carry is what a Resume keeps of the attempt it resumes: the mode, the
// sizes and the runner mutations, never a fault.
func (c runCfg) carry() []RunOpt {
	return []RunOpt{func(n *runCfg) {
		n.live, n.seed, n.workers, n.chunk = c.live, c.seed, c.workers, c.chunk
		n.chunker, n.leasePolicy, n.newSink = c.chunker, c.leasePolicy, c.newSink
	}}
}

// Live runs in Live mode. The default is Preview, as in production.
func Live() RunOpt { return func(c *runCfg) { c.live = true } }

// Seed makes the run's interleaving a function of n: which free worker takes
// the next chunk and the order concurrent items run in. Same seed, same
// Outcome.Writes order. A seeded run serializes its items (see scheduler), so
// it is for ordering tests, not for -race.
func Seed(n int64) RunOpt { return func(c *runCfg) { c.seed = &n } }

// Workers sets the worker count. The default is GOMAXPROCS, and never fewer
// than 4: a count below 4 is refused.
func Workers(n int) RunOpt {
	return func(c *runCfg) {
		if n < minWorkers {
			c.err = fmt.Errorf("opstest: Workers(%d): the harness never runs with fewer than %d workers (D28)", n, minWorkers)
			return
		}
		c.workers = n
	}
}

// WithChunker replaces the runner's chunking of a Snapshot source (a runner
// mutation, for tests of Conformance itself).
func WithChunker(fn func(keys, parts []string, size int) (order, starts []int)) RunOpt {
	return func(c *runCfg) { c.chunker = fn }
}

// WithLeasePolicy replaces the runner's choice of which chunks to lease (a
// runner mutation, for tests of Conformance itself). fn gets the ledger's
// completed set and a chunk index and returns the chunk to lease, or -1.
func WithLeasePolicy(fn func(done func(chunk int) bool, chunk int) int) RunOpt {
	return func(c *runCfg) { c.leasePolicy = fn }
}

// WithProgressSink replaces the progress recorder: newSink is called once per
// attempt and receives every progress publication and item start; what its
// Snapshots returns is the Outcome's Progress.
func WithProgressSink(newSink func() ProgressSink) RunOpt {
	return func(c *runCfg) { c.newSink = newSink }
}

// WriteRecord is one committed write, in commit order.
type WriteRecord struct {
	Seq     int64       `json:"seq"` // the World's commit sequence, shared with HistoryRow.Seq
	Attempt int         `json:"attempt"`
	Subject ops.Subject `json:"subject"`
	BookID  string      `json:"book_id,omitempty"` // the book the write belongs to, "" when none
	Op      string      `json:"op"`
	Changed []string    `json:"changed,omitempty"` // fields whose value changed
}

// HistoryRow is one recorded history row, in commit order.
type HistoryRow struct {
	Seq     int64  `json:"seq"`
	Attempt int    `json:"attempt"`
	BookID  string `json:"book_id"`
	Field   string `json:"field"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new,omitempty"`
	Kind    string `json:"kind"` // metadata:<change type> | journal:<change type>
}

// ItemEvent is one item start of an attempt.
type ItemEvent struct {
	Start     int    // global start index within the attempt (0 = the first item to start)
	Worker    int    //
	Chunk     int    //
	Index     int    // position inside the chunk
	Key       string //
	Partition string //
	Done      bool   // the item returned
	Err       string // its error, if any
}

// ChunkEvent is one chunk lease of an attempt.
type ChunkEvent struct {
	Worker int
	Chunk  int
	Keys   []string
	Done   bool // handed to the ledger complete
}

// AbandonReport is what AbandonAt observed of the second run it started while
// the abandoned item was still running.
type AbandonReport struct {
	// SecondQueued: the second run waited for the exclusive key.
	SecondQueued bool
	// SecondStartedWhileHeld: it started before the abandoned item returned.
	SecondStartedWhileHeld bool
	// Second is the second run's outcome.
	Second *Outcome
}

// Outcome is one attempt of a run.
type Outcome struct {
	State    state.V3
	Result   json.RawMessage
	Writes   []WriteRecord          // in commit order
	History  []HistoryRow           // in commit order
	Intents  []Intent               // unresolved at end
	Progress []ops.ProgressSnapshot // what the ProgressSink recorded
	Plan     []ops.Row              // Fixer preview rows / recorded intents

	OpID    string
	Attempt int
	Err     error // the run's error (nil on success)
	// Crashed: a fault stopped the world; Resume continues.
	Crashed bool
	// Summary is the Summary the runner handed Finish (Batch and Fixer).
	Summary ops.Summary
	// Ledger is the stored chunk ledger when the attempt ended, before any
	// terminal transition (nil when none was written).
	Ledger *database.Ledger
	// Items are the item starts, in start order; Chunks the leases.
	Items  []ItemEvent
	Chunks []ChunkEvent
	// Job facts: the source order, the resume policy the job ran with, the
	// chunk size in effect, the def's own chunk size, the worker count, and
	// whether the def partitions.
	Order        ops.SourceOrder
	ResumePolicy ops.ResumePolicy
	ChunkSize    int
	DefChunkSize int
	Workers      int
	Partitioned  bool
	Kind         ops.Kind
	// SourceKeys is an Items source's keys in source order, with their
	// partition keys (nil for a paged source).
	SourceKeys, SourceParts []string
	// StartSeq and EndSeq bound the attempt in the World's commit sequence;
	// FenceRevokedAt is the sequence number of the fence revoke (0: not
	// revoked).
	StartSeq, EndSeq, FenceRevokedAt int64
	// Inserted is the book id InsertBelowCursor inserted ("" when none).
	Inserted string
	Abandon  *AbandonReport
	// Notes say which faults did not apply and why.
	Notes []string

	w      *World
	def    ops.Definition
	params json.RawMessage
	cfg    runCfg
}

// Run runs def once with params (any JSON-encodable value, or a
// json.RawMessage) and returns the outcome of its first attempt.
func (w *World) Run(def ops.Definition, params any, opts ...RunOpt) *Outcome {
	w.t.Helper()
	raw, err := encodeParams(params)
	if err != nil {
		w.t.Fatalf("opstest: params do not encode: %v", err)
	}
	cfg := newRunCfg(opts)
	if cfg.err != nil {
		w.t.Fatalf("%v", cfg.err)
	}
	opID := fmt.Sprintf("opstest-%04d", w.opSeq.Add(1))
	o, err := w.attempt(def, raw, opID, false, cfg, nil, nil)
	if err != nil {
		w.t.Fatalf("%v", err)
	}
	return o
}

// Resume starts the next attempt of the same op id over the store the crash
// left (snapshot, ledger, marks, intents, writes), as a boot after a crash
// does: the run is swept to interrupted, then resumed per the def's policy.
// It keeps this attempt's mode, sizes and runner mutations; opts add to them
// (a fault for the resumed attempt).
func (o *Outcome) Resume(opts ...RunOpt) *Outcome {
	w := o.w
	w.t.Helper()
	if !o.Crashed {
		w.t.Fatalf("opstest: Resume: run %s was not stopped by a crash", o.OpID)
	}
	cfg := newRunCfg(append(o.cfg.carry(), opts...))
	if cfg.err != nil {
		w.t.Fatalf("%v", cfg.err)
	}
	if err := w.boot(); err != nil {
		w.t.Fatalf("%v", err)
	}
	c := ops.Compile(o.def)
	steps := []state.Event{state.EvResume}
	switch c.Resume {
	case ops.ResumeAsk:
		steps = []state.Event{state.EvAsk, state.EvDecideRetry}
	case ops.ResumeDrop:
		if _, err := w.transition(o.OpID, database.TransitionRequest{Event: state.EvDecideDrop}); err != nil {
			w.t.Fatalf("opstest: drop %s: %v", o.OpID, err)
		}
		return &Outcome{State: state.V3Dropped, OpID: o.OpID, w: w, def: o.def, params: o.params, cfg: cfg,
			Notes: []string{"the def's resume policy is ResumeDrop: the run was dropped, not resumed"}}
	}
	for _, ev := range steps {
		if _, err := w.transition(o.OpID, database.TransitionRequest{Event: ev}); err != nil {
			w.t.Fatalf("opstest: resume %s (%s): %v", o.OpID, ev, err)
		}
	}
	next, err := w.attempt(o.def, o.params, o.OpID, true, cfg, nil, nil)
	if err != nil {
		w.t.Fatalf("%v", err)
	}
	return next
}

func encodeParams(p any) (json.RawMessage, error) {
	switch v := p.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return v, nil
	default:
		return json.Marshal(v)
	}
}

// attempt is one run attempt: dispatch, run, settle the run record. queued
// and started, when set, are signaled when the attempt waits for its
// exclusive key and when it holds it (AbandonAt's second run). A harness
// failure is returned, never t.Fatal'd, because the second run of AbandonAt
// runs off the test goroutine.
func (w *World) attempt(def ops.Definition, params json.RawMessage, opID string, resumed bool, cfg runCfg,
	queued, started func()) (*Outcome, error) {
	c := ops.Compile(def)
	if len(c.Problems) > 0 {
		return nil, fmt.Errorf("opstest: %s does not compile: %v", c.ID, c.Problems)
	}
	if c.OpenBatch == nil && c.RunTask == nil {
		return nil, fmt.Errorf("opstest: %s is a %s definition; the harness runs tasks, batches and fixers", c.ID, c.Kind)
	}
	if err := w.boot(); err != nil {
		return nil, err
	}
	w.keys.acquire(c.Common.Exclusive, queued)
	keyHeld := true
	defer func() {
		if keyHeld {
			w.keys.release(c.Common.Exclusive)
		}
	}()
	if started != nil {
		started()
	}
	if !resumed {
		mode := modeString(cfg.live)
		if c.Common.Effects.IsReadOnly() {
			mode = ops.Live.String()
		}
		if err := w.st().InsertRunV3(database.RunRecord{SchemaVersion: 3, ID: opID, DefID: c.ID, Kind: c.Kind.String(),
			Mode: mode, Trigger: string(ops.TriggerManual), Params: params}); err != nil {
			return nil, fmt.Errorf("opstest: insert run %s: %w", opID, err)
		}
	}
	rec, err := w.transition(opID, database.TransitionRequest{Event: state.EvDispatch})
	if err != nil {
		return nil, fmt.Errorf("opstest: dispatch %s: %w", opID, err)
	}

	a := newAttempt(w, opID, rec.Attempt, cfg)
	a.def, a.params = def, params
	w.mu.Lock()
	w.attempts[opID] = a
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.attempts, opID)
		w.mu.Unlock()
	}()

	mode := ops.Preview
	if cfg.live || c.Common.Effects.IsReadOnly() {
		mode = ops.Live
	}
	// The run context as runV3 and executeRun build it: the refusal latch,
	// then the fence bound to the context's Done, then the Writer over both.
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	latch := &ops.RefusalLatch{}
	ctx := ops.WithRefusalLatch(base, latch)
	fence, revoke := opswriter.NewFence(rec.FenceEpoch, base.Done())
	ctx = opswriter.WithFence(ctx, fence)
	a.revoke, a.cancel = revoke, cancel
	env := ops.RunEnv{Context: ctx, OpID: opID, DefID: c.ID, Mode: mode, Trigger: ops.TriggerManual, Resumed: rec.Attempt > 1}
	if !c.Common.Effects.IsReadOnly() {
		a.writer = opswriter.New(opswriter.Config{OpID: opID, Mode: mode, Fence: fence.Check, RunLatch: latch,
			Intents: w.ops, Books: w.books, Files: w.books, Authors: w.books, Journal: w.journal,
			History: opswriter.NewBookHistory(w.meta, c.ID, "ops_v3", "op-"+opID+"-", nil)})
		a.tracked = &trackedWriter{Writer: a.writer, drain: &sync.RWMutex{}}
		env.Writer = a.tracked
	}
	env.Host = &worldHost{a: a}
	rc := ops.NewRC(env)

	o := &Outcome{OpID: opID, Attempt: rec.Attempt, Kind: c.Kind, w: w, def: def, params: params, cfg: cfg}
	o.StartSeq = w.log.next()
	w.log.setAttempt(rec.Attempt)

	var result json.RawMessage
	var runErr error
	switch {
	case c.OpenBatch != nil:
		job, err := c.OpenBatch(rc, params)
		if err != nil {
			runErr = err
			break
		}
		a.describe(o, job)
		result, runErr = a.runBatch(ctx, rc, job, o)
	default:
		if cfg.cancelAt >= 0 || cfg.crashAt >= 0 || cfg.crashMid != nil || cfg.abandonAt >= 0 || cfg.loseLeaseAt >= 0 || cfg.insert != nil {
			a.note("item faults do not apply to a Task: it has no items")
		}
		result, runErr = c.RunTask(rc, params)
	}

	if err := a.settle(o, c, result, runErr); err != nil {
		return nil, err
	}
	o.EndSeq = w.log.next()
	o.Writes, o.History = w.log.window(o.StartSeq, o.EndSeq)
	a.fill(o)
	if mode == ops.Preview {
		o.Plan = planOf(c, result, a.writer)
	}
	w.keys.release(c.Common.Exclusive)
	keyHeld = false
	if a.second != nil {
		rep := a.second
		if out := <-rep.done; out.err != nil {
			return nil, out.err
		} else {
			o.Abandon = &AbandonReport{SecondQueued: rep.queued.Load(), SecondStartedWhileHeld: rep.startedWhileHeld,
				Second: out.o}
		}
	}
	return o, nil
}

// settle reads what the attempt left and moves the run record as the
// registry would: a crash leaves it running for the next boot's sweep; a
// stop requested by a fault ends it through goroutine_returned; otherwise
// the run's return decides.
func (a *attempt) settle(o *Outcome, c ops.Compiled, result json.RawMessage, runErr error) error {
	w := a.w
	o.Err = runErr
	led, err := w.st().GetLedgerV3(a.opID)
	if err != nil {
		return fmt.Errorf("opstest: read the ledger of %s: %w", a.opID, err)
	}
	o.Ledger = led
	if in, err := w.st().ListIntentsV3(a.opID); err == nil {
		o.Intents = in
	} else {
		return fmt.Errorf("opstest: list intents of %s: %w", a.opID, err)
	}
	if a.crashed.Load() {
		o.Crashed = true
		o.State = state.V3Running
		w.mu.Lock()
		w.crashed[a.opID] = true
		w.mu.Unlock()
		return nil
	}
	rec, err := w.stateOf(a.opID)
	if err != nil {
		return err
	}
	req := database.TransitionRequest{}
	switch {
	case rec.State == state.V3Stopping:
		req.Event, req.StopReason = state.EvGoroutineReturned, rec.StopReason
		if runErr != nil {
			req.Error = runErr.Error()
		}
		req.Result = result
	case runErr == nil:
		req.Event, req.Result = state.EvReturnNil, result
	default:
		req.Event, req.Error = state.EvReturnErr, runErr.Error()
	}
	out, err := w.transition(a.opID, req)
	if err != nil {
		return fmt.Errorf("opstest: end %s (%s): %w", a.opID, req.Event, err)
	}
	o.State, o.Result = out.State, result
	return nil
}

// planOf is a preview's plan: a Fixer's planned rows, else the Writer's
// recorded intents as rows (subject as id, the write's op as class).
func planOf(c ops.Compiled, result json.RawMessage, w *opswriter.Writer) []ops.Row {
	if c.Kind == ops.KindFixer {
		var plan ops.FixerPlan
		if json.Unmarshal(result, &plan) == nil {
			return plan.Rows
		}
		return nil
	}
	if w == nil {
		return nil
	}
	lines, _ := w.Planned()
	rows := make([]ops.Row, 0, len(lines))
	for _, in := range lines {
		rows = append(rows, ops.Row{ID: in.Subject, Class: in.Kind, State: in.Planned})
	}
	return rows
}

// ── attempt ─────────────────────────────────────────────────────────────────

// attempt is the live state of one run attempt: its faults, its hooks and
// what they observed.
type attempt struct {
	w      *World
	opID   string
	num    int
	cfg    runCfg
	def    ops.Definition
	params json.RawMessage

	sink    ProgressSink
	sched   *scheduler
	sd      *standDown
	writer  *opswriter.Writer
	tracked *trackedWriter
	revoke  func()
	cancel  context.CancelFunc
	workers int
	order   ops.SourceOrder

	starts  atomic.Int64
	crashed atomic.Bool
	// crashAfterCommit stops the world once the next ledger commit lands.
	crashAfterCommit atomic.Bool

	mu        sync.Mutex
	cond      *sync.Cond
	items     []ItemEvent
	itemAt    map[int]int // start index -> position in items
	current   map[int]int // worker -> start index of its current item
	chunks    []ChunkEvent
	chunkLen  map[int]int
	completed []string // keys of items that returned, this attempt
	exited    int
	delivered int
	firstDone *int // ChunksDone of the attempt's first ledger commit
	lastDone  int
	committed *database.Ledger
	revokedAt int64
	stopped   bool
	inserted  string
	notes     []string
	second    *secondRun
}

type secondRun struct {
	queued           atomic.Bool
	startedWhileHeld bool
	done             chan secondResult
}

type secondResult struct {
	o   *Outcome
	err error
}

func newAttempt(w *World, opID string, num int, cfg runCfg) *attempt {
	a := &attempt{w: w, opID: opID, num: num, cfg: cfg, sd: &standDown{}, itemAt: map[int]int{}, current: map[int]int{},
		chunkLen: map[int]int{}}
	a.cond = sync.NewCond(&a.mu)
	if cfg.newSink != nil {
		a.sink = cfg.newSink()
	} else {
		a.sink = &recordingSink{}
	}
	return a
}

func (a *attempt) note(format string, args ...any) {
	a.mu.Lock()
	a.notes = append(a.notes, fmt.Sprintf(format, args...))
	a.mu.Unlock()
}

// describe records the job's facts and applies the size overrides.
func (a *attempt) describe(o *Outcome, job *ops.BatchJob) {
	o.Order, o.ResumePolicy, o.DefChunkSize, o.Partitioned = job.Order, job.Resume, job.ChunkSize, job.Partitioned
	if job.Items != nil {
		n := job.Items.Len()
		o.SourceKeys, o.SourceParts = make([]string, n), make([]string, n)
		for i := range n {
			o.SourceKeys[i], o.SourceParts[i] = job.Items.Key(i), job.Items.Partition(i)
		}
	}
	job.Workers = a.cfg.workers
	if a.cfg.chunk > 0 {
		job.ChunkSize = a.cfg.chunk
	}
	o.ChunkSize, o.Workers = job.ChunkSize, job.Workers
	a.workers, a.order = job.Workers, job.Order
	if a.cfg.seed != nil {
		a.sched = newScheduler(*a.cfg.seed, job.Workers)
	}
	if a.cfg.insert != nil && job.Order != ops.AppendOnly {
		a.note("InsertBelowCursor not tested: the source claims %s, not append_only", job.Order)
	}
}

func (a *attempt) runBatch(ctx context.Context, rc *ops.RC, job *ops.BatchJob, o *Outcome) (json.RawMessage, error) {
	h := &registry.RunnerHooks{
		WorkerExit: a.workerExit, ChunkLeased: a.chunkLeased, ItemStart: a.itemStart, ItemDone: a.itemDone,
		ChunkDone: a.chunkDone, BeforeLedgerCommit: a.beforeCommit,
		Progress: func(p database.ProgressSnapshot) { a.sink.Published(toOps(p)) },
		Chunker:  a.cfg.chunker, LeasePolicy: a.cfg.leasePolicy,
	}
	if a.sched != nil {
		h.WorkerReady = a.sched.gate
	}
	sum, result, err := registry.RunBatchForTest(ctx, registry.BatchRunForTest{OpID: a.opID, Job: job, RC: rc,
		Store: a.w.ops, Now: a.w.clock.now, Hooks: h})
	o.Summary = sum
	if a.crashed.Load() {
		return nil, nil
	}
	return result, err
}

// fill copies what the hooks observed into the outcome.
func (a *attempt) fill(o *Outcome) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o.Items = slices.Clone(a.items)
	sort.Slice(o.Items, func(i, j int) bool { return o.Items[i].Start < o.Items[j].Start })
	o.Chunks = slices.Clone(a.chunks)
	o.FenceRevokedAt = a.revokedAt
	o.Inserted = a.inserted
	o.Notes = append(o.Notes, a.notes...)
	o.Progress = a.sink.Snapshots()
}

// ── hooks ───────────────────────────────────────────────────────────────────

func (a *attempt) workerExit(w int) {
	if a.sched != nil {
		a.sched.exit(w)
	}
	a.mu.Lock()
	a.exited++
	a.mu.Unlock()
	a.cond.Broadcast()
}

func (a *attempt) chunkLeased(w, chunk int, keys []string) {
	a.mu.Lock()
	a.chunks = append(a.chunks, ChunkEvent{Worker: w, Chunk: chunk, Keys: slices.Clone(keys)})
	a.chunkLen[chunk] = len(keys)
	a.mu.Unlock()
}

func (a *attempt) chunkDone(w, chunk int) {
	a.mu.Lock()
	for i := len(a.chunks) - 1; i >= 0; i-- {
		if a.chunks[i].Chunk == chunk && a.chunks[i].Worker == w {
			a.chunks[i].Done = true
			break
		}
	}
	a.delivered++
	a.mu.Unlock()
	a.cond.Broadcast()
}

// observeCommit sees every ledger commit of this op that landed.
func (a *attempt) observeCommit(l database.Ledger) {
	a.mu.Lock()
	if a.firstDone == nil {
		n := l.ChunksDone
		a.firstDone = &n
	}
	a.lastDone = l.ChunksDone
	c := l
	c.Ranges = slices.Clone(l.Ranges)
	a.committed = &c
	a.mu.Unlock()
	a.cond.Broadcast()
	if a.crashAfterCommit.CompareAndSwap(true, false) {
		a.crash() // InsertBelowCursor's final-commit form: the ledger landed, then the world stopped
	}
}

func (a *attempt) itemStart(it registry.ItemInfo, irc *ops.ItemRC) {
	if a.sched != nil {
		a.sched.gate(it.Worker)
	}
	n := int(a.starts.Add(1) - 1)
	a.mu.Lock()
	a.itemAt[n] = len(a.items)
	a.items = append(a.items, ItemEvent{Start: n, Worker: it.Worker, Chunk: it.Chunk, Index: it.Index, Key: it.Key,
		Partition: it.Partition})
	a.current[it.Worker] = n
	a.mu.Unlock()
	a.sink.ItemStarted(it.Chunk, it.Index)
	if n == a.cfg.cancelAt {
		a.stop(state.StopUser, false)
	}
	if n == a.cfg.abandonAt {
		a.abandon(irc)
	}
	if n == a.cfg.loseLeaseAt {
		a.loseLease()
	}
}

func (a *attempt) itemDone(it registry.ItemInfo, err error) bool {
	a.mu.Lock()
	n := a.current[it.Worker]
	if p, ok := a.itemAt[n]; ok {
		a.items[p].Done = true
		if err != nil {
			a.items[p].Err = err.Error()
		}
	}
	a.completed = append(a.completed, it.Key)
	size := a.chunkLen[it.Chunk]
	a.mu.Unlock()
	if n == a.cfg.crashAt {
		a.crash()
		return true
	}
	if m := a.cfg.crashMid; m != nil && it.Chunk == m[0] && it.Index == m[1] {
		if m[1] >= size-1 {
			a.note("CrashMidChunk(%d, %d) not applied: item %d is the last of a %d-item chunk", m[0], m[1], m[1], size)
			return false
		}
		a.quiesce(it.Worker)
		a.crash()
		return true
	}
	if a.cfg.insert != nil && a.order == ops.AppendOnly && a.insertBelowCommitted() {
		a.crash()
		return true
	}
	return false
}
