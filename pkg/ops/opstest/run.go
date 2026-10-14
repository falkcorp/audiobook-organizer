// file: pkg/ops/opstest/run.go
// version: 1.2.0
// guid: 9a4f6b2d-1c83-4e57-b0d9-3e8a5f7c2b61
// last-edited: 2026-10-14

package opstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// minWorkers is the fewest workers a run ever gets (owner decision D28:
// parallel by default; the harness proves the invariants concurrently).
const minWorkers = 4

const (
	// registryWorkers is the registry's run pool: room for a run, the run
	// queued behind its key and a replacement worker after an abandonment.
	registryWorkers = 4
	// runLimit bounds how long the harness waits for a run to end. It is a
	// deadline that turns a hang into a failure, never an ordering device.
	runLimit = 2 * time.Minute
	// abandonGrace is the registry's abandon grace in an AbandonAt run: the
	// abandoned item ignores its context until the run is a zombie, so the
	// grace only sets how soon that is.
	abandonGrace = 10 * time.Millisecond
	// standDownLease is the scan stand-down lease TTL; LoseLeaseAt moves the
	// stand-down clock past it.
	standDownLease = time.Minute
	// commitStep is the runner-clock step per reading when every completion
	// must commit at once: above the ledger's production commit throttle
	// (2s), well under the stuck-worker budget per item.
	commitStep = 5 * time.Second
)

// RunOpt configures one World.Run or Outcome.Resume.
type RunOpt func(*runCfg)

type runCfg struct {
	live    bool
	seed    *int64
	workers int
	chunk   int

	cancelAt, crashAt, abandonAt, loseLeaseAt, shutdownAt int
	crashMid                                              *[2]int
	insert                                                *insertSpec

	chunker     func(keys, parts []string, size int) (order, starts []int)
	leasePolicy func(done func(chunk int) bool, chunk int) int
	newSink     func() ProgressSink

	err error // an invalid option, reported by Run
}

type insertSpec struct{ item any }

func newRunCfg(opts []RunOpt) runCfg {
	c := runCfg{workers: max(runtime.GOMAXPROCS(0), minWorkers), cancelAt: -1, crashAt: -1, abandonAt: -1, loseLeaseAt: -1,
		shutdownAt: -1}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// carry is what a Resume (or AbandonAt's second run) keeps of the attempt
// it follows: the mode, the sizes and the runner mutations, never a fault.
func (c runCfg) carry() []RunOpt {
	return []RunOpt{func(n *runCfg) {
		n.live, n.seed, n.workers, n.chunk = c.live, c.seed, c.workers, c.chunk
		n.chunker, n.leasePolicy, n.newSink = c.chunker, c.leasePolicy, c.newSink
	}}
}

func (c runCfg) itemFaults() bool {
	return c.cancelAt >= 0 || c.crashAt >= 0 || c.crashMid != nil || c.abandonAt >= 0 || c.loseLeaseAt >= 0 ||
		c.shutdownAt >= 0 || c.insert != nil
}

// Live runs in Live mode (the run's params get "dry_run": false, as the API
// sends them). The default is Preview, as in production.
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

	harness bool // made by the harness itself (InsertBelowCursor), not by a def
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
	// Committed: the item returned and its completion is durable: its
	// chunk, or its key range, is in the attempt's last committed ledger;
	// for a DirtySet source, the runner deleted its dirty entry; for any
	// other Unordered source, the attempt ended without a crash.
	Committed bool
}

// ChunkEvent is one chunk lease of an attempt.
type ChunkEvent struct {
	Worker int
	Chunk  int
	Keys   []string
	Done   bool // handed to the ledger complete
}

// AbandonReport is what AbandonAt observed of the second run it queued while
// the abandoned item was still running.
type AbandonReport struct {
	// SecondQueued: the second run was queued under its own op id (not
	// deduplicated onto the abandoned run) and a whole dispatch cycle saw it
	// while the abandoned goroutine still held the key.
	SecondQueued bool
	// SecondStartedWhileHeld: the dispatcher claimed or started it before
	// the abandoned goroutine returned.
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
	Err     error // the run's error, as the registry recorded it (nil on success)
	// Crashed: a fault stopped the world (CrashAt, CrashMidChunk,
	// InsertBelowCursor, or ShutdownAt's graceful stop); Resume continues.
	Crashed bool
	// Summary is the Summary the runner handed Finish (Batch and Fixer).
	Summary ops.Summary
	// Ledger is the chunk ledger as last committed in this attempt (the
	// terminal transition deletes the stored one); on a crash, the stored
	// ledger the crash left (nil when none was written).
	Ledger *database.Ledger
	// UncommittedAtCrash is how many chunks a crashed attempt had handed to
	// the ledger that its last commit did not record: folded completions the
	// production commit throttle had not committed yet, lost with the crash.
	UncommittedAtCrash int
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
	// StartSeq and EndSeq bound the attempt in the World's commit sequence:
	// from the registry starting it to its terminal transition (or its
	// crash). FenceRevokedAt is the sequence number of the fence revoke or
	// the stand-down lease loss (0: neither).
	StartSeq, EndSeq, FenceRevokedAt int64
	// LateWrites are Writer calls that started after the fence was revoked
	// and still succeeded.
	LateWrites []string
	// DirtyCleared are the DirtySet entries (prefix+id) the runner deleted
	// in this attempt, sorted.
	DirtyCleared []string
	// Inserted is the book id InsertBelowCursor inserted ("" when none).
	Inserted string
	Abandon  *AbandonReport
	// Notes say which faults did not apply and why.
	Notes []string

	spans  []span
	w      *World
	def    ops.Definition
	params json.RawMessage // the run's params as enqueued (mode included)
	cfg    runCfg
}

// Run runs def once with params (any JSON-encodable value, or a
// json.RawMessage) on a fresh registry over the World and returns the
// outcome of its first attempt.
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
	w.mu.Lock()
	pending := len(w.crashed)
	w.mu.Unlock()
	if pending > 0 {
		w.t.Fatalf("opstest: Run while a crashed run waits for Resume: Resume it or Reset the World first")
	}
	c := compileRunnable(w.t, def)
	runParams, err := withMode(raw, cfg.live && !c.Common.Effects.IsReadOnly())
	if err != nil {
		w.t.Fatalf("%v", err)
	}
	g := w.newGeneration(def, cfg)
	a := g.newAttempt(def, c, runParams, cfg)
	g.expectAttempt(a)
	g.start()
	opID, err := g.reg.EnqueueOp(context.Background(), c.ID, runParams)
	if err != nil {
		g.shutdown()
		w.t.Fatalf("opstest: enqueue %s: %v", c.ID, err)
	}
	if err := g.bind(opID, a); err != nil {
		g.shutdown()
		w.t.Fatalf("%v", err)
	}
	return a.finish()
}

// Resume boots a new registry over the store the crash left (snapshot,
// ledger, marks, intents, writes), as a process start after a crash does:
// the registry's own boot sweep moves the run to interrupted and resumes it
// under the same op id per the def's policy (ResumeAsk: the harness answers
// Retry, as an operator would; ResumeDrop: the run is dropped). It keeps
// this attempt's mode, sizes and runner mutations; opts add to them (a fault
// for the resumed attempt).
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
	c := compileRunnable(w.t, o.def)
	g := w.newGeneration(o.def, cfg)
	a := g.newAttempt(o.def, c, o.params, cfg)
	if err := g.bind(o.OpID, a); err != nil {
		w.t.Fatalf("%v", err)
	}
	w.mu.Lock()
	delete(w.crashed, o.OpID)
	w.mu.Unlock()
	g.start() // the boot sweep: interrupted, then resumed, asked or dropped
	switch c.Resume {
	case ops.ResumeAsk:
		if err := g.reg.RetryInterrupted(context.Background(), o.OpID, "opstest"); err != nil {
			g.shutdown()
			w.t.Fatalf("opstest: retry %s after the boot asked: %v", o.OpID, err)
		}
	case ops.ResumeContinue, ops.ResumeFromZero:
	default:
		g.shutdown()
		out := a.outcome()
		out.Notes = append(out.Notes, "the def's resume policy is ResumeDrop: the boot dropped the run")
		return out
	}
	return a.finish()
}

func compileRunnable(t testing.TB, def ops.Definition) ops.Compiled {
	t.Helper()
	c := ops.Compile(def)
	if len(c.Problems) > 0 {
		t.Fatalf("opstest: %s does not compile: %v", c.ID, c.Problems)
	}
	if c.OpenBatch == nil && c.RunTask == nil {
		t.Fatalf("opstest: %s is a %s definition; the harness runs tasks, batches and fixers", c.ID, c.Kind)
	}
	return c
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

// withMode is params as the API sends a Live run: the object with
// "dry_run": false (an omitted mode is Preview, opmode.ParseDryRun). The def
// ignores the field: params decode with unknown fields ignored.
func withMode(raw json.RawMessage, live bool) (json.RawMessage, error) {
	if !live {
		return raw, nil
	}
	m := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("opstest: a Live run's params must be a JSON object: %w", err)
		}
	}
	m["dry_run"] = json.RawMessage("false")
	return json.Marshal(m)
}

// ── one generation's registry ───────────────────────────────────────────────

// newGeneration starts a new process lifetime of the World: a generation
// whose gate the run's writes pass and a registry over its store, with def
// registered. start begins it.
func (w *World) newGeneration(def ops.Definition, cfg runCfg) *generation {
	w.t.Helper()
	g := newGeneration(w)
	w.cur.Store(g)
	opts := registry.Options{
		Bus: termBus{g: g}, RunnerHooksForTest: g.hooksFor,
		WatchdogInterval: time.Hour, SweepInterval: time.Hour,
		ScanStandDownLease: standDownLease, ScanStandDownNow: g.standDownNow,
	}
	if cfg.abandonAt >= 0 {
		opts.AbandonGrace = abandonGrace
	}
	// The registry's own lines (starting run, run finished, progress) go
	// nowhere: a Conformance run starts hundreds of registries. A writer to
	// t.Logf would be unsafe, as registry goroutines can outlive the test.
	g.reg = registry.NewWithOptions(&worldStore{w: w, g: g}, slog.New(slog.DiscardHandler), registryWorkers, opts)
	if !g.reg.HasRunStateV3() {
		w.t.Fatalf("opstest: the registry did not resolve the v3 run state from the World's store")
	}
	if err := registry.Register(g.reg, "opstest", def); err != nil {
		w.t.Fatalf("opstest: register %s: %v", def.ID(), err)
	}
	return g
}

func (g *generation) start() {
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.reg.Start(ctx)
}

// shutdown stops the generation's registry. After a crash every write it
// tries is refused, so it changes nothing in the store.
func (g *generation) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), runLimit)
	defer cancel()
	if err := g.reg.Shutdown(ctx); err != nil && !g.isDead() {
		g.w.t.Errorf("opstest: registry shutdown: %v", err)
	}
	if g.cancel != nil {
		g.cancel()
	}
}

// standDownNow is the registry's stand-down clock: the wall clock, moved
// forward by LoseLeaseAt.
func (g *generation) standDownNow() time.Time {
	return time.Now().Add(time.Duration(g.sdSkew.Load()))
}

// newAttempt makes the attempt a run of def in g will be.
func (g *generation) newAttempt(def ops.Definition, c ops.Compiled, params json.RawMessage, cfg runCfg) *attempt {
	a := &attempt{w: g.w, g: g, def: def, c: c, params: params, cfg: cfg, itemAt: map[int]int{},
		current: map[int]int{}, chunkLen: map[int]int{}, ended: make(chan struct{}), zombie: make(chan struct{})}
	a.cond = sync.NewCond(&a.mu)
	if cfg.newSink != nil {
		a.sink = cfg.newSink()
	} else {
		a.sink = &recordingSink{}
	}
	a.step = time.Millisecond
	if cfg.crashMid != nil || cfg.insert != nil {
		a.step = commitStep
	}
	a.createdSeq = g.w.log.next()
	if c.Kind == ops.KindTask && cfg.itemFaults() {
		a.note("item faults do not apply to a Task: it has no items")
	}
	return a
}

// expectAttempt queues a for the next op id the registry runs that no
// attempt is bound to: EnqueueOp can start the run before it returns the id.
func (g *generation) expectAttempt(a *attempt) {
	g.mu.Lock()
	g.expect = append(g.expect, a)
	g.mu.Unlock()
}

// bind ties opID to a (a may already be bound to it, by hooksFor).
func (g *generation) bind(opID string, a *attempt) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if cur, ok := g.attempts[opID]; ok {
		if cur != a {
			return fmt.Errorf("opstest: op %s is bound to another attempt (the registry ran a run it did not expect)", opID)
		}
		return nil
	}
	if i := slices.Index(g.expect, a); i >= 0 {
		g.expect = slices.Delete(g.expect, i, i+1)
	}
	a.opID = opID
	g.attempts[opID] = a
	return nil
}

// hooksFor is Options.RunnerHooksForTest: the hooks of the attempt bound to
// opID, binding the next expected attempt to an op id seen first here.
func (g *generation) hooksFor(opID string) *registry.RunnerHooks {
	g.mu.Lock()
	a, ok := g.attempts[opID]
	if !ok {
		if len(g.expect) > 0 {
			a, g.expect = g.expect[0], g.expect[1:]
		} else {
			a = &attempt{w: g.w, g: g, ended: make(chan struct{}), zombie: make(chan struct{}), itemAt: map[int]int{},
				current: map[int]int{}, chunkLen: map[int]int{}, sink: &recordingSink{}, step: time.Millisecond}
			a.cond = sync.NewCond(&a.mu)
			g.w.t.Errorf("opstest: the registry ran op %s, which no run of the harness enqueued", opID)
		}
		a.opID = opID
		g.attempts[opID] = a
	}
	g.mu.Unlock()
	return a.runnerHooks()
}

// waitListed waits until a whole dispatch cycle has seen opID queued: a
// cycle's queue read listed it and a later cycle began (the dispatcher runs
// its cycles one after another, so that one is over).
func (g *generation) waitListed(opID string) bool {
	expired := false
	timer := time.AfterFunc(runLimit, func() {
		g.mu.Lock()
		expired = true
		g.mu.Unlock()
		g.cond.Broadcast()
	})
	defer timer.Stop()
	g.mu.Lock()
	defer g.mu.Unlock()
	for !expired {
		if n, ok := g.listed[opID]; ok && g.cycles > n {
			return true
		}
		g.cond.Wait()
	}
	return false
}

// ── attempt ─────────────────────────────────────────────────────────────────

// attempt is the live state of one run attempt: its faults, its hooks and
// what they observed.
type attempt struct {
	w      *World
	g      *generation
	opID   string
	cfg    runCfg
	def    ops.Definition
	c      ops.Compiled
	params json.RawMessage

	hooksOnce sync.Once
	hooks     *registry.RunnerHooks
	sink      ProgressSink
	sched     *scheduler
	writer    *opswriter.Writer
	tracked   *trackedWriter
	step      time.Duration // the runner clock's step per reading
	facts     *Outcome      // the job's facts (Order, sizes, keys), copied into the Outcome

	starts           atomic.Int64
	crashed          atomic.Bool
	crashAfterCommit atomic.Bool

	createdSeq       int64
	startSeq, endSeq int64 // guarded by mu
	ended            chan struct{}
	endOnce          sync.Once
	zombie           chan struct{}
	zombieOnce       sync.Once

	mu        sync.Mutex
	cond      *sync.Cond
	summary   ops.Summary
	items     []ItemEvent
	itemAt    map[int]int // start index -> position in items
	current   map[int]int // worker -> start index of its current item
	chunks    []ChunkEvent
	chunkLen  map[int]int
	workers   int
	order     ops.SourceOrder
	dirty     string // a DirtySet source's prefix ("" for any other source)
	exited    int
	delivered int
	firstDone *int // ChunksDone of the attempt's first ledger commit
	lastDone  int
	committed *database.Ledger
	revokedAt int64
	inserted  string
	notes     []string
	spans     []span
	late      []string
	marks     map[string]int // row id -> marks the store took (guarded by mu)
	second    *AbandonReport
	secondAt  *attempt
}

func (a *attempt) note(format string, args ...any) {
	a.mu.Lock()
	a.notes = append(a.notes, fmt.Sprintf(format, args...))
	a.mu.Unlock()
}

func (a *attempt) addSpan(s span) {
	a.mu.Lock()
	a.spans = append(a.spans, s)
	a.mu.Unlock()
}

// markTaken counts a row mark the store accepted.
func (a *attempt) markTaken(rowID string) {
	a.mu.Lock()
	if a.marks == nil {
		a.marks = map[string]int{}
	}
	a.marks[rowID]++
	a.mu.Unlock()
}

func (a *attempt) marksTaken(rowID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.marks[rowID]
}

func (a *attempt) lateWrite(what string) {
	a.mu.Lock()
	a.late = append(a.late, what)
	a.mu.Unlock()
}

// runnerHooks builds the attempt's hooks once; the registry asks for them
// when it starts the attempt, which opens the attempt's window.
func (a *attempt) runnerHooks() *registry.RunnerHooks {
	a.hooksOnce.Do(func() {
		seq := a.w.log.next()
		a.mu.Lock()
		a.startSeq = seq
		a.mu.Unlock()
		a.hooks = &registry.RunnerHooks{
			Job: a.describe, WrapWriter: a.wrapWriter, Now: a.now, Summary: a.setSummary,
			Abandoned:  func() { a.zombieOnce.Do(func() { close(a.zombie) }) },
			WorkerExit: a.workerExit, ChunkLeased: a.chunkLeased, ItemStart: a.itemStart, ItemDone: a.itemDone,
			ChunkDone: a.chunkDone, BeforeLedgerCommit: a.beforeCommit,
			Progress: func(p database.ProgressSnapshot) { a.sink.Published(toOps(p)) },
			Chunker:  a.cfg.chunker, LeasePolicy: a.cfg.leasePolicy,
		}
		if a.cfg.seed != nil {
			a.hooks.WorkerReady = func(w int) { a.schedGate(w) }
		}
	})
	return a.hooks
}

func (a *attempt) now() time.Time { return a.w.clock.now(a.step) }

func (a *attempt) setSummary(s ops.Summary) {
	a.mu.Lock()
	a.summary = s
	a.mu.Unlock()
}

func (a *attempt) wrapWriter(w ops.Writer) ops.Writer {
	if pw, ok := w.(*opswriter.Writer); ok {
		a.writer = pw
	}
	a.tracked = &trackedWriter{Writer: w, a: a}
	return a.tracked
}

// describe is the Job hook: it records the job's facts and applies the size
// overrides.
func (a *attempt) describe(job *ops.BatchJob) {
	f := &Outcome{Order: job.Order, ResumePolicy: job.Resume, DefChunkSize: job.ChunkSize, Partitioned: job.Partitioned}
	if job.Items != nil {
		n := job.Items.Len()
		f.SourceKeys, f.SourceParts = make([]string, n), make([]string, n)
		for i := range n {
			f.SourceKeys[i], f.SourceParts[i] = job.Items.Key(i), job.Items.Partition(i)
		}
	}
	job.Workers = a.cfg.workers
	if a.cfg.chunk > 0 {
		job.ChunkSize = a.cfg.chunk
	}
	f.ChunkSize, f.Workers = job.ChunkSize, job.Workers
	a.mu.Lock()
	a.facts = f
	a.workers, a.order = job.Workers, job.Order
	if job.Dirty != nil {
		a.dirty = job.Dirty.Prefix
	}
	if a.cfg.seed != nil {
		a.sched = newScheduler(*a.cfg.seed, job.Workers)
	}
	a.mu.Unlock()
	if a.cfg.insert != nil && job.Order != ops.AppendOnly {
		a.note("InsertBelowCursor not tested: the source claims %s, not append_only", job.Order)
	}
}

func (a *attempt) schedGate(w int) {
	a.mu.Lock()
	s := a.sched
	a.mu.Unlock()
	if s != nil {
		s.gate(w)
	}
}

// end is the registry's op.terminal for the attempt (termBus): it closes the
// attempt's window. A crashed attempt ended at its crash.
func (a *attempt) end(string) {
	if a.crashed.Load() {
		return
	}
	a.close()
}

// close stamps the end of the attempt's window, once.
func (a *attempt) close() {
	a.endOnce.Do(func() {
		seq := a.w.log.next()
		a.mu.Lock()
		if a.startSeq == 0 {
			a.startSeq = a.createdSeq
		}
		a.endSeq = seq
		from := a.startSeq
		a.mu.Unlock()
		a.w.log.addWindow(from, seq)
		close(a.ended)
	})
}

// finish waits for the attempt (and AbandonAt's second run) to end, stops
// the generation's registry and returns the outcome.
func (a *attempt) finish() *Outcome {
	a.w.t.Helper()
	ok := a.await()
	var second *attempt
	a.mu.Lock()
	second = a.secondAt
	a.mu.Unlock()
	if ok && second != nil {
		ok = second.await()
	}
	a.g.shutdown()
	if !ok {
		a.w.t.Fatalf("opstest: run %s (%s) did not end within %s", a.opID, a.c.ID, runLimit)
	}
	o := a.outcome()
	if second != nil {
		a.mu.Lock()
		rep := a.second
		a.mu.Unlock()
		rep.Second = second.outcome()
		o.Abandon = rep
	}
	return o
}

func (a *attempt) await() bool {
	select {
	case <-a.ended:
		return true
	case <-time.After(runLimit):
		return false
	}
}

// outcome reads what the attempt left in the store and what its hooks saw.
func (a *attempt) outcome() *Outcome {
	w := a.w
	o := &Outcome{OpID: a.opID, Kind: a.c.Kind, Crashed: a.crashed.Load(), w: w, def: a.def, params: a.params,
		cfg: a.cfg}
	st := w.st()
	if rec, err := st.GetRunV3(a.opID); err != nil || rec == nil {
		w.t.Errorf("opstest: read the run record of %s: %v", a.opID, err)
	} else {
		o.State, o.Attempt = rec.State, rec.Attempt
	}
	if row, err := st.GetOperationV2(a.opID); err == nil && row != nil {
		if row.ResultData != nil {
			o.Result = json.RawMessage(*row.ResultData)
		}
		if row.ErrorMessage != nil && *row.ErrorMessage != "" && !o.Crashed && o.State != state.V3Succeeded {
			o.Err = errors.New(*row.ErrorMessage)
		}
	}
	if in, err := st.ListIntentsV3(a.opID); err == nil {
		o.Intents = in
	} else {
		w.t.Errorf("opstest: list intents of %s: %v", a.opID, err)
	}
	a.mu.Lock()
	if a.facts != nil {
		f := a.facts
		o.Order, o.ResumePolicy, o.ChunkSize, o.DefChunkSize = f.Order, f.ResumePolicy, f.ChunkSize, f.DefChunkSize
		o.Workers, o.Partitioned, o.SourceKeys, o.SourceParts = f.Workers, f.Partitioned, f.SourceKeys, f.SourceParts
	}
	o.Summary = a.summary
	if a.committed != nil {
		l := *a.committed
		o.Ledger = &l
	}
	o.StartSeq, o.EndSeq, o.FenceRevokedAt = a.startSeq, a.endSeq, a.revokedAt
	if o.StartSeq == 0 {
		o.StartSeq = a.createdSeq
	}
	o.Items = slices.Clone(a.items)
	o.Chunks = slices.Clone(a.chunks)
	o.Inserted = a.inserted
	o.Notes = slices.Clone(a.notes)
	o.LateWrites = slices.Clone(a.late)
	o.spans = slices.Clone(a.spans)
	recorded := recordedChunks(a.committed)
	delivered := a.delivered
	firstDone := a.firstDone
	dirty := a.dirty
	a.mu.Unlock()
	if o.Crashed {
		if l, err := st.GetLedgerV3(a.opID); err == nil {
			o.Ledger = l
		}
		committedChunks := 0
		if o.Ledger != nil && firstDone != nil {
			committedChunks = o.Ledger.ChunksDone - *firstDone
		}
		o.UncommittedAtCrash = max(delivered-committedChunks, 0)
	}
	if o.EndSeq == 0 {
		o.EndSeq = w.log.next() // a dropped run never started
	}
	o.Writes, o.History = w.log.window(o.StartSeq, o.EndSeq, o.Attempt)
	var cleared map[string]bool
	if dirty != "" {
		cleared = w.log.clearedIn(o.StartSeq, o.EndSeq)
		o.DirtyCleared = slices.Sorted(maps.Keys(cleared))
	}
	sort.Slice(o.Items, func(i, j int) bool { return o.Items[i].Start < o.Items[j].Start })
	for i := range o.Items {
		it := &o.Items[i]
		switch {
		case !it.Done:
		case dirty != "":
			it.Committed = cleared[dirty+it.Key]
		case o.Order == ops.Snapshot:
			it.Committed = recorded[it.Chunk]
		case o.Order == ops.AppendOnly:
			it.Committed = o.Ledger != nil && inRange(o.Ledger.Ranges, it.Key)
		default:
			it.Committed = !o.Crashed
		}
	}
	o.Progress = a.sink.Snapshots()
	if !a.c.Common.Effects.IsReadOnly() && !a.cfg.live {
		o.Plan = planOf(a.c, o.Result, a.writer)
	}
	return o
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

// ── hooks ───────────────────────────────────────────────────────────────────

func (a *attempt) workerExit(w int) {
	a.mu.Lock()
	s := a.sched
	a.exited++
	a.mu.Unlock()
	if s != nil {
		s.exit(w)
	}
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

// observeCommit sees every ledger commit of this attempt that landed.
func (a *attempt) observeCommit(l database.Ledger) {
	a.mu.Lock()
	if a.firstDone == nil {
		n := l.ChunksDone
		a.firstDone = &n
	}
	a.lastDone = l.ChunksDone
	c := l
	c.Ranges = slices.Clone(l.Ranges)
	c.Done = slices.Clone(l.Done)
	a.committed = &c
	a.mu.Unlock()
	a.cond.Broadcast()
	if a.crashAfterCommit.CompareAndSwap(true, false) {
		a.crash() // InsertBelowCursor's final-commit form: the ledger landed, then the world stopped
	}
}

func (a *attempt) itemStart(it registry.ItemInfo, irc *ops.ItemRC) {
	a.schedGate(it.Worker)
	n := int(a.starts.Add(1) - 1)
	a.mu.Lock()
	a.itemAt[n] = len(a.items)
	a.items = append(a.items, ItemEvent{Start: n, Worker: it.Worker, Chunk: it.Chunk, Index: it.Index, Key: it.Key,
		Partition: it.Partition})
	a.current[it.Worker] = n
	a.mu.Unlock()
	a.sink.ItemStarted(it.Chunk, it.Index)
	if n == a.cfg.cancelAt {
		a.cancelRun()
	}
	if n == a.cfg.abandonAt {
		a.abandon(irc)
	}
	if n == a.cfg.loseLeaseAt {
		a.loseLease()
	}
	if n == a.cfg.shutdownAt {
		a.shutdownRegistry(irc)
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
	size := a.chunkLen[it.Chunk]
	order := a.order
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
	if a.cfg.insert != nil && order == ops.AppendOnly && a.insertBelowCommitted() {
		a.crash()
		return true
	}
	return false
}
