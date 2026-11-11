// file: core/tasks/taskstest/parity/kit.go
// version: 1.1.0
// guid: f9960b87-6628-4c89-97f3-68902d08625c
// last-edited: 2026-11-11

package parity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// storePath is the pebble path on the in-memory file system. The store is
// opened with storage.Open/Prepare/Migrate over a MemFS, never
// coretest.NewStore: core/tasks is in the kernel's import graph.
const storePath = "/batcave/parity.pebble"

// runBugsEnv, when set to 1, runs the cases marked as executor bugs instead
// of skipping them, so a fix can be checked against its oracle.
const runBugsEnv = "PARITY_RUN_BUGS"

// Params is the parameter shape of every kit definition (HEAD's test defs
// took a JSON object the same way).
type Params struct {
	Case   string `json:"case,omitempty"`
	JobID  string `json:"job_id,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
	Idx    int    `json:"idx,omitempty"`
}

// Kit is one store and the executors booted over it. A case gets a fresh
// Kit; Boot may be called again over the same store (a restart).
type Kit struct {
	T *testing.T

	fs     *vfs.MemFS
	raw    tasks.Stores // unhooked: assertions and planting read and write here
	ks     storage.Keyspace
	hooks  *Hooks
	wrap   tasks.Stores // raw with Runs decorated by hooks: what executors get
	closeD func() error
	closed bool
	seq    atomic.Int64
	execs  []tasks.Executor
	probes []*Probe

	mu    sync.Mutex
	terms []Terminal

	X tasks.Executor
}

// Terminal is one OnTerminal call: the run and the state it was entering.
type Terminal struct {
	ID    tasks.RunID
	State tasks.RunState
}

// NewKit returns a Kit; the store opens on first use.
func NewKit(t *testing.T) *Kit {
	t.Helper()
	return &Kit{T: t, hooks: &Hooks{}}
}

// Sub runs f with a fresh Kit (its own store) inline. testing.T.Run is not
// allowed inside a synctest bubble, so a sub-scenario is a labelled step of
// its case, not a subtest; a failure in it fails the case.
func (k *Kit) Sub(name string, f func(t *testing.T, k *Kit)) {
	k.T.Helper()
	k.T.Logf("sub-scenario %s", name)
	f(k.T, NewKit(k.T))
}

// KnownBug skips t as an executor bug unless PARITY_RUN_BUGS=1.
func KnownBug(t *testing.T, why string) {
	t.Helper()
	if os.Getenv(runBugsEnv) == "1" {
		return
	}
	t.Skip("executor bug: " + why)
}

// Hooks returns the store decorator's hook table.
func (k *Kit) Hooks() *Hooks { return k.hooks }

// Raw is the undecorated Stores over the kit's store.
func (k *Kit) Raw() tasks.Stores { k.open(); return k.raw }

func (k *Kit) open() {
	if k.fs != nil {
		return
	}
	t := k.T
	t.Helper()
	k.fs = vfs.NewCrashableMem()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            storePath,
		Owners:          []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:           "parity+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(k.fs),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("boot: %v", errors.Join(err, o.Close()))
	}
	k.closeD = db.Close
	// One cleanup, in the order that is safe: stop every executor, free every
	// body that waits on its probe, let those goroutines finish (the clock is
	// fake inside a bubble, so the sleep costs nothing), then close the store.
	t.Cleanup(func() {
		for _, x := range k.execs {
			if err := x.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown at cleanup: %v", err)
			}
		}
		for _, p := range k.probes {
			p.Free()
		}
		time.Sleep(time.Second)
		if k.closed {
			return
		}
		k.closed = true
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ks, ok := db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatalf("owner %s has no keyspace", taskstore.Owner)
	}
	k.ks = ks
	k.raw = tasks.NativeStores(ks)
	k.wrap = k.raw
	k.wrap.Runs = &hookedRuns{RunStore: k.raw.Runs, h: k.hooks}
}

// CloseStore closes the store now (the closed-store cases).
func (k *Kit) CloseStore() {
	k.T.Helper()
	k.open()
	if k.closed {
		return
	}
	k.closed = true
	if err := k.closeD(); err != nil {
		k.T.Errorf("close: %v", err)
	}
}

// Opt edits the executor Options before New.
type Opt func(*executor.Options)

// executorConfig is the executor's Config (cases name it without importing the executor).
type executorConfig = executor.Config

func isShutdown(err error) bool { return errors.Is(err, tasks.ErrShutdown) }

// Cfg edits the Config.
func Cfg(f func(*executor.Config)) Opt { return func(o *executor.Options) { f(&o.Config) } }

// testConfig is the kit's clock set: the dispatcher ticks at 100ms, the
// watchdog is out of the way, and every progress and checkpoint write lands.
func testConfig() executor.Config {
	return executor.Config{
		DispatchTick: 100 * time.Millisecond, WatchdogInterval: time.Hour,
		ProgressInterval: time.Nanosecond, MinCheckpointInterval: time.Nanosecond,
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Boot builds an executor over the kit's store with defs and runs it (the
// resume sweep, dispatcher and watchdog). It becomes k.X. A second Boot is a
// restart: the caller shut the first executor down.
func (k *Kit) Boot(defs []tasks.Definition, opts ...Opt) tasks.Executor {
	return k.boot(defs, true, opts)
}

// BootNoRun builds the executor without calling Run: no sweep, no
// dispatcher (the "registry not started" cases).
func (k *Kit) BootNoRun(defs []tasks.Definition, opts ...Opt) tasks.Executor {
	return k.boot(defs, false, opts)
}

func (k *Kit) boot(defs []tasks.Definition, run bool, edits []Opt) tasks.Executor {
	t := k.T
	t.Helper()
	k.open()
	nb := k.raw.Runs.(interface{ NewBatch() *storage.Batch }).NewBatch
	o := executor.Options{
		Stores: k.wrap, NewBatch: nb, Keyspace: &k.ks, Definitions: defs, InstrumentsOff: true,
		Config: testConfig(), Logger: quietLogger(),
		OnTerminal: func(_ context.Context, _ *storage.Batch, v tasks.RunView) error {
			k.mu.Lock()
			k.terms = append(k.terms, Terminal{v.ID, v.State})
			k.mu.Unlock()
			return nil
		},
	}
	for _, e := range edits {
		e(&o)
	}
	x, err := executor.New(o)
	if err != nil {
		t.Fatalf("executor.New: %v", err)
	}
	k.execs = append(k.execs, x)
	k.X = x
	if run {
		if err := x.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	return x
}

// Terminals lists the terminal transitions OnTerminal saw for id, in order.
// An attempt whose commit conflicted and re-staged may appear more than
// once, so assertions use "last" and "every", never "exactly".
func (k *Kit) Terminals(id tasks.RunID) []tasks.RunState {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []tasks.RunState
	for _, tm := range k.terms {
		if tm.ID == id {
			out = append(out, tm.State)
		}
	}
	return out
}

// --- run access ---

// Start starts kind live with params (nil: none) and returns the run ID.
func (k *Kit) Start(kind tasks.Kind, params any) tasks.RunID {
	k.T.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			k.T.Fatalf("params: %v", err)
		}
		raw = b
	}
	ref, err := k.X.Start(context.Background(), kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: raw})
	if err != nil {
		k.T.Fatalf("start %s: %v", kind, err)
	}
	return ref.ID
}

// Get reads a run from the raw store.
func (k *Kit) Get(id tasks.RunID) tasks.RunRecord {
	k.T.Helper()
	k.open()
	r, err := k.raw.Runs.Get(context.Background(), id)
	if err != nil {
		k.T.Fatalf("get %s: %v", id, err)
	}
	return r
}

// List reads runs (every state when none is named) from the raw store.
func (k *Kit) List(kinds []tasks.Kind, states ...tasks.RunState) []tasks.RunRecord {
	k.T.Helper()
	k.open()
	var out []tasks.RunRecord
	q := tasks.RunQuery{Kinds: kinds, States: states, Limit: 1000}
	for r, err := range k.raw.Runs.List(context.Background(), q) {
		if err != nil {
			k.T.Fatalf("list: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// Unfinished is the resume sweep's input.
func (k *Kit) Unfinished() []tasks.RunRecord {
	k.T.Helper()
	k.open()
	var out []tasks.RunRecord
	for r, err := range k.raw.Runs.Unfinished(context.Background()) {
		if err != nil {
			k.T.Fatalf("unfinished: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// Events is the run's transition trail.
func (k *Kit) Events(id tasks.RunID) []tasks.RunEvent {
	k.T.Helper()
	k.open()
	var out []tasks.RunEvent
	for p, err := range k.raw.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageEvents, Limit: 1000}) {
		if err != nil {
			k.T.Fatalf("events: %v", err)
		}
		out = append(out, p.Event)
	}
	return out
}

// WaitState polls until the run is in one of states (30s of the test's
// clock; inside a bubble that clock is fake).
func (k *Kit) WaitState(id tasks.RunID, states ...tasks.RunState) tasks.RunRecord {
	k.T.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := k.Get(id)
		if slices.Contains(states, r.State) {
			return r
		}
		if time.Now().After(deadline) {
			k.T.Fatalf("run %s is %s (reason %q, err %q), want one of %v", id, r.State, r.Reason, r.Error, states)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Stays asserts the run keeps state for d.
func (k *Kit) Stays(id tasks.RunID, state tasks.RunState, d time.Duration) {
	k.T.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if r := k.Get(id); r.State != state {
			k.T.Fatalf("run %s left %s for %s (reason %q)", id, state, r.State, r.Reason)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Quiet lets d of the test's clock pass (dispatch ticks included).
func (k *Kit) Quiet(d time.Duration) { time.Sleep(d) }

// Plant inserts a run directly into the store, as HEAD's insertRunningOp did:
// a row left behind by a process that died. A queued or waiting_deps row is
// a fresh run (attempt 0); any other state carries attempt 1 and fence 1.
func (k *Kit) Plant(kind tasks.Kind, state tasks.RunState, edits ...func(*tasks.RunRecord)) tasks.RunID {
	k.T.Helper()
	k.open()
	ctx := context.Background()
	id, err := k.raw.Runs.NewID(ctx)
	if err != nil {
		k.T.Fatalf("new id: %v", err)
	}
	now := time.Now()
	rec := tasks.RunRecord{
		ID: id, Kind: kind, Mode: tasks.ModeLive, State: state, Trigger: tasks.TriggerManual,
		Actor:    tasks.Actor{UserID: "bruce", AuthMethod: "session"},
		Created:  now.Add(time.Duration(k.seq.Add(1)) * time.Microsecond),
		Priority: tasks.PriorityNormal, Params: json.RawMessage(`{}`),
	}
	if state != tasks.StateQueued && state != tasks.StateWaitingDeps {
		rec.Attempt, rec.FenceEpoch, rec.Started = 1, 1, now
	}
	if state.Terminal() {
		rec.Finished = now
	}
	for _, e := range edits {
		e(&rec)
	}
	if err := k.write(func(b *storage.Batch) error { return k.raw.Runs.Insert(ctx, b, rec) }); err != nil {
		k.T.Fatalf("plant %s: %v", id, err)
	}
	return id
}

// WithParams sets a planted run's params.
func WithParams(p any) func(*tasks.RunRecord) {
	return func(r *tasks.RunRecord) {
		b, err := json.Marshal(p)
		if err != nil {
			panic(err)
		}
		r.Params = b
	}
}

// PlantCheckpoint stores raw JSON under name (a value the executor did not
// wrap, as a cutover import leaves; the executor reads it as is).
func (k *Kit) PlantCheckpoint(id tasks.RunID, name, rawJSON string) {
	k.T.Helper()
	k.open()
	if err := k.write(func(b *storage.Batch) error {
		return k.raw.Runs.SaveCheckpoint(context.Background(), b, id, name, []byte(rawJSON))
	}); err != nil {
		k.T.Fatalf("plant checkpoint: %v", err)
	}
}

// PlantStandDown stores a stand-down marker that names quiesced runs: the
// next boot leaves them interrupted_quiesced (its holder died mid-work).
func (k *Kit) PlantStandDown(key tasks.Key, quiesced ...tasks.RunID) {
	k.T.Helper()
	k.open()
	m := tasks.StandDown{Key: key, Holder: "dead-holder", Quiesced: quiesced, Expires: time.Now().Add(time.Hour)}
	if err := k.write(func(b *storage.Batch) error { return k.raw.StandDown.Set(context.Background(), b, key, &m) }); err != nil {
		k.T.Fatalf("plant stand-down: %v", err)
	}
}

// RawMove makes a store-level transition of id (what a racing actor's
// committed write looks like), from a fresh read. It returns an error
// instead of failing so a hook goroutine can use it.
func (k *Kit) RawMove(id tasks.RunID, edit func(*tasks.RunRecord)) error {
	k.open()
	ctx := context.Background()
	return k.write(func(b *storage.Batch) error {
		cur, err := k.raw.Runs.Get(ctx, id)
		if err != nil {
			return err
		}
		next := cur
		edit(&next)
		next.Seq.Event = cur.Seq.Event + 1
		if next.State.Terminal() && next.Finished.IsZero() {
			next.Finished = time.Now()
		}
		if err := k.raw.Runs.Transition(ctx, b, id, tasks.Expect{State: cur.State, Attempt: cur.Attempt, FenceEpoch: cur.FenceEpoch}, next); err != nil {
			return err
		}
		return k.raw.Runs.AppendEvent(ctx, b, id, next.Seq.Event, tasks.RunEvent{From: cur.State, To: next.State, Event: "raw", Reason: next.Reason, At: time.Now()})
	})
}

// Requeue is RawMove to queued: a resume's re-queue of a quiesced run.
func (k *Kit) Requeue(id tasks.RunID) error {
	return k.RawMove(id, func(r *tasks.RunRecord) { r.State, r.Reason = tasks.StateQueued, "" })
}

func (k *Kit) write(stage func(b *storage.Batch) error) error {
	open := k.raw.Runs.(interface{ NewBatch() *storage.Batch }).NewBatch
	return storage.RetryOnConflict(context.Background(), 16, func(ctx context.Context) (err error) {
		b := open()
		defer func() { err = errors.Join(err, b.Close()) }()
		if err := stage(b); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
}

// --- the store decorator ---

// TransitionEvent is what a transition hook sees.
type TransitionEvent struct {
	ID   tasks.RunID
	Want tasks.Expect
	Next tasks.RunRecord
}

type oneShot[E any] struct {
	match func(E) bool
	fn    func(E)
	fired bool
}

// Hooks is the decorator's table of one-shot hooks and always-on observers.
// A hook is matched on what the call says (run, target state, reason, query),
// never on a call count, and fires at most once.
type Hooks struct {
	mu     sync.Mutex
	before []*oneShot[TransitionEvent]
	after  []*oneShot[TransitionEvent]
	queued []func()
	lists  []*oneShot[tasks.RunQuery]
	claims []*claimHook
	onCkpt func(id tasks.RunID, name string, v []byte)
	onProg func(id tasks.RunID, done, total int, current string)
}

func fire[E any](h *Hooks, hs []*oneShot[E], e E) {
	h.mu.Lock()
	var run *oneShot[E]
	for _, x := range hs {
		if !x.fired && x.match(e) {
			x.fired, run = true, x
			break
		}
	}
	h.mu.Unlock()
	if run != nil {
		run.fn(e)
	}
}

// BeforeTransition runs fn once, before the first Transition that match
// accepts reaches the store (the run is then re-read: the writer's read is
// stale, as in HEAD's stale-snapshot hooks).
func (h *Hooks) BeforeTransition(match func(TransitionEvent) bool, fn func(TransitionEvent)) {
	h.mu.Lock()
	h.before = append(h.before, &oneShot[TransitionEvent]{match: match, fn: fn})
	h.mu.Unlock()
}

// AfterTransition runs fn once, after the staging of the first Transition
// that match accepts (before its batch commits).
func (h *Hooks) AfterTransition(match func(TransitionEvent) bool, fn func(TransitionEvent)) {
	h.mu.Lock()
	h.after = append(h.after, &oneShot[TransitionEvent]{match: match, fn: fn})
	h.mu.Unlock()
}

// claimHook is one AfterClaimCommitted hook.
type claimHook struct {
	id      tasks.RunID
	attempt int // the claim's attempt once its Transition is staged; 0 before
	fn      func()
	fired   bool
}

// AfterClaimCommitted runs fn once, inside the first checkpoint read of run
// id made after the run's claim (queued -> running) committed, before that
// read returns. The executor reads the run's resume state there, after the
// claim's commit and before the attempt goes live, so fn runs while the run
// is stored running and its body has not started: HEAD's pickup gate, where
// a worker holds a registered handle for a run it has not started. fn may
// park to hold the claim in that window; a case that parks must free it on
// every path, or the cleanup's Shutdown waits on the parked dispatcher. A
// case asserts that fn ran, so an executor that reads nothing in the window
// fails the case instead of passing it vacuously.
func (h *Hooks) AfterClaimCommitted(id tasks.RunID, fn func()) {
	h.mu.Lock()
	h.claims = append(h.claims, &claimHook{id: id, fn: fn})
	h.mu.Unlock()
}

// OnQueued runs fn once, inside the next Queued call (the dispatcher's
// listing of the queue), before the listing is returned.
func (h *Hooks) OnQueued(fn func()) {
	h.mu.Lock()
	h.queued = append(h.queued, fn)
	h.mu.Unlock()
}

// OnList runs fn once, inside the first List call match accepts.
func (h *Hooks) OnList(match func(tasks.RunQuery) bool, fn func()) {
	h.mu.Lock()
	h.lists = append(h.lists, &oneShot[tasks.RunQuery]{match: match, fn: func(tasks.RunQuery) { fn() }})
	h.mu.Unlock()
}

// ObserveCheckpoints reports every checkpoint write (the stored envelope).
func (h *Hooks) ObserveCheckpoints(fn func(id tasks.RunID, name string, v []byte)) {
	h.mu.Lock()
	h.onCkpt = fn
	h.mu.Unlock()
}

// ObserveProgress reports every progress write.
func (h *Hooks) ObserveProgress(fn func(id tasks.RunID, done, total int, current string)) {
	h.mu.Lock()
	h.onProg = fn
	h.mu.Unlock()
}

// hookedRuns decorates tasks.RunStore; every other method is the embedded
// store's.
type hookedRuns struct {
	tasks.RunStore
	h *Hooks
}

func (r *hookedRuns) Transition(ctx context.Context, b *storage.Batch, id tasks.RunID, want tasks.Expect, next tasks.RunRecord) error {
	ev := TransitionEvent{ID: id, Want: want, Next: next}
	fire(r.h, r.snapshot(true), ev)
	err := r.RunStore.Transition(ctx, b, id, want, next)
	if err == nil && want.State == tasks.StateQueued && next.State == tasks.StateRunning {
		r.h.mu.Lock()
		for _, c := range r.h.claims {
			if c.id == id && !c.fired {
				c.attempt = next.Attempt
			}
		}
		r.h.mu.Unlock()
	}
	fire(r.h, r.snapshot(false), ev)
	return err
}

// Checkpoint fires an armed AfterClaimCommitted hook of id once the run's
// stored record shows its claim committed (the claim's attempt is stored).
func (r *hookedRuns) Checkpoint(ctx context.Context, id tasks.RunID, name string) ([]byte, bool, error) {
	r.h.mu.Lock()
	var armed *claimHook
	for _, c := range r.h.claims {
		if c.id == id && c.attempt > 0 && !c.fired {
			armed = c
			break
		}
	}
	r.h.mu.Unlock()
	if armed != nil {
		if rec, err := r.RunStore.Get(ctx, id); err == nil && rec.Attempt == armed.attempt {
			r.h.mu.Lock()
			run := !armed.fired
			armed.fired = true
			r.h.mu.Unlock()
			if run {
				armed.fn()
			}
		}
	}
	return r.RunStore.Checkpoint(ctx, id, name)
}

func (r *hookedRuns) snapshot(before bool) []*oneShot[TransitionEvent] {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	if before {
		return append([]*oneShot[TransitionEvent](nil), r.h.before...)
	}
	return append([]*oneShot[TransitionEvent](nil), r.h.after...)
}

func (r *hookedRuns) Queued(ctx context.Context) iter.Seq2[tasks.RunRecord, error] {
	r.h.mu.Lock()
	var fn func()
	if len(r.h.queued) > 0 {
		fn, r.h.queued = r.h.queued[0], r.h.queued[1:]
	}
	r.h.mu.Unlock()
	if fn != nil {
		fn()
	}
	return r.RunStore.Queued(ctx)
}

func (r *hookedRuns) List(ctx context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunRecord, error] {
	r.h.mu.Lock()
	lists := append([]*oneShot[tasks.RunQuery](nil), r.h.lists...)
	r.h.mu.Unlock()
	fire(r.h, lists, q)
	return r.RunStore.List(ctx, q)
}

func (r *hookedRuns) SaveCheckpoint(ctx context.Context, b *storage.Batch, id tasks.RunID, name string, v []byte) error {
	r.h.mu.Lock()
	obs := r.h.onCkpt
	r.h.mu.Unlock()
	if obs != nil {
		obs(id, name, v)
	}
	return r.RunStore.SaveCheckpoint(ctx, b, id, name, v)
}

func (r *hookedRuns) Progress(ctx context.Context, b *storage.Batch, id tasks.RunID, done, total int, current string) error {
	r.h.mu.Lock()
	obs := r.h.onProg
	r.h.mu.Unlock()
	if obs != nil {
		obs(id, done, total, current)
	}
	return r.RunStore.Progress(ctx, b, id, done, total, current)
}

// --- Batman test definitions, shaped like HEAD's test defs ---

// Probe is one kit definition and what its body saw.
type Probe struct {
	Kind    tasks.Kind
	free    sync.Once
	Calls   atomic.Int32
	Exited  atomic.Bool
	Started chan tasks.RunID // one value per body start
	Release chan struct{}    // closed by the case to free a body that waits on it
	Seen    chan Params      // the decoded params of each call
	Resumed chan bool        // rc.Resumed() of each call
	Cause   chan error       // context.Cause of the run context when a cooperative body ended

	body func(p *Probe, rc *tasks.RC, in Params) (string, error)
}

// NewProbe is a definition for kind whose body returns at once.
func NewProbe(kind string) *Probe {
	return &Probe{
		Kind: tasks.Kind(kind), Started: make(chan tasks.RunID, 64), Release: make(chan struct{}),
		Seen: make(chan Params, 64), Resumed: make(chan bool, 64), Cause: make(chan error, 64),
	}
}

// Free releases every body waiting on Release (idempotent).
func (p *Probe) Free() { p.free.Do(func() { close(p.Release) }) }

// Probe is NewProbe whose Release the kit closes at cleanup, so a case that
// leaves a stubborn body running never strands a goroutine.
func (k *Kit) Probe(kind string) *Probe {
	p := NewProbe(kind)
	k.probes = append(k.probes, p)
	return p
}

func send[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
	}
}

// Blocks makes the body cooperative: it signals Started, then waits for its
// run context and returns the cause.
func (p *Probe) Blocks() *Probe {
	p.body = func(p *Probe, rc *tasks.RC, _ Params) (string, error) {
		send(p.Started, rc.RunID())
		<-rc.Context().Done()
		send(p.Cause, context.Cause(rc.Context()))
		return "", context.Cause(rc.Context())
	}
	return p
}

// Stubborn makes the body ignore its context: it signals Started and waits
// for Release, then returns the context's error (nil if it never ended).
func (p *Probe) Stubborn() *Probe {
	p.body = func(p *Probe, rc *tasks.RC, _ Params) (string, error) {
		send(p.Started, rc.RunID())
		<-p.Release
		p.Exited.Store(true)
		return "", rc.Context().Err()
	}
	return p
}

// WaitsRelease makes the body wait for Release or its context, then return
// nil (a run that finishes when the case lets it).
func (p *Probe) WaitsRelease() *Probe {
	p.body = func(p *Probe, rc *tasks.RC, _ Params) (string, error) {
		send(p.Started, rc.RunID())
		select {
		case <-p.Release:
			return "case closed", nil
		case <-rc.Context().Done():
			send(p.Cause, context.Cause(rc.Context()))
			return "", context.Cause(rc.Context())
		}
	}
	return p
}

// Body installs a custom body (it runs after the probe counted the call).
func (p *Probe) Body(f func(p *Probe, rc *tasks.RC, in Params) (string, error)) *Probe {
	p.body = f
	return p
}

func (p *Probe) run(rc *tasks.RC, in Params) (string, error) {
	p.Calls.Add(1)
	send(p.Seen, in)
	send(p.Resumed, rc.Resumed())
	if p.body == nil {
		return "case closed", nil
	}
	return p.body(p, rc, in)
}

// DefOpt edits a definition's Common.
type DefOpt func(*tasks.Common)

// Resume sets the resume policy (the default is ResumeContinue).
func Resume(pol tasks.ResumePolicy) DefOpt { return func(c *tasks.Common) { c.Resume = pol } }

// Timeout sets the attempt timeout.
func Timeout(d time.Duration) DefOpt { return func(c *tasks.Common) { c.Timeout = d } }

// ProgressTimeout sets the watchdog gap.
func ProgressTimeout(d time.Duration) DefOpt { return func(c *tasks.Common) { c.ProgressTimeout = d } }

// Grace sets a cooperative cancel grace.
func Grace(d time.Duration) DefOpt {
	return func(c *tasks.Common) { c.Cancel = tasks.Cooperative(d) }
}

// Yields lets a stand-down quiesce the run (requires ResumeContinue).
func Yields() DefOpt { return func(c *tasks.Common) { c.Yields = true } }

// Key sets the concurrency key.
func Key(k string) DefOpt { return func(c *tasks.Common) { c.ConcurrencyKey = tasks.Key(k) } }

// NotWhile makes the run wait while any of kinds runs.
func NotWhile(kinds ...string) DefOpt {
	return func(c *tasks.Common) {
		for _, k := range kinds {
			c.NotWhile = append(c.NotWhile, tasks.Kind(k))
		}
	}
}

// MergeQueued makes the kind fold a start into a queued run (HEAD's
// MergeQueuedParams): the params of the two are merged by keeping the
// existing run's.
func MergeQueued() DefOpt {
	return func(c *tasks.Common) {
		c.Dedupe.Merge = func(existing, _ json.RawMessage) (json.RawMessage, bool, error) { return existing, true, nil }
	}
}

// resourceOf is the resource a kind writes: its own, so two kinds of one
// module never serialise on a shared write set.
func resourceOf(kind tasks.Kind) tasks.Resource {
	m, name, _ := strings.Cut(string(kind), ".")
	return tasks.Res(m, strings.ReplaceAll(name, ".", "-"))
}

func baseCommon(kind tasks.Kind, edits []DefOpt) tasks.Common {
	m := kind.Module()
	c := tasks.Common{
		Title:   "Batcave " + string(kind),
		Effects: tasks.Writes(resourceOf(kind)),
		Perms:   tasks.Permissions{Start: rbac.Need(rbac.Permission(m + ":run"))},
		Resume:  tasks.ResumeContinue,
	}
	for _, e := range edits {
		e(&c)
	}
	return c
}

// Def builds the Task definition.
func (p *Probe) Def(edits ...DefOpt) tasks.Definition {
	return tasks.Task(p.Kind, tasks.TaskSpec[Params, string]{Common: baseCommon(p.Kind, edits), Run: p.run})
}

// Defs collects definitions.
func Defs(ds ...tasks.Definition) []tasks.Definition { return ds }

// WaitStarted waits for the next body start and returns its run.
func (k *Kit) WaitStarted(p *Probe) tasks.RunID {
	k.T.Helper()
	select {
	case id := <-p.Started:
		return id
	case <-time.After(30 * time.Second):
		k.T.Fatalf("%s: no run started", p.Kind)
		return ""
	}
}

// NoStart asserts no body of p started in d.
func (k *Kit) NoStart(p *Probe, d time.Duration) {
	k.T.Helper()
	select {
	case id := <-p.Started:
		k.T.Fatalf("%s: run %s started, want none", p.Kind, id)
	case <-time.After(d):
	}
}

// Holder is a definition that takes a stand-down hold on key and keeps it
// until its Probe is released: HEAD's AcquireScanStandDown holder.
func (k *Kit) Holder(kind, key string) (*Probe, *atomic.Bool) {
	p := k.Probe(kind)
	held := &atomic.Bool{}
	p.Body(func(p *Probe, rc *tasks.RC, _ Params) (string, error) {
		hold, err := rc.StandDown(tasks.Key(key))
		if err != nil {
			return "", err
		}
		defer hold.Release()
		held.Store(true)
		send(p.Started, rc.RunID())
		select {
		case <-p.Release:
			return "stood down", nil
		case <-rc.Context().Done():
			return "", context.Cause(rc.Context())
		}
	})
	return p, held
}

// GoShutdown runs Shutdown on a goroutine and lets a tick pass so it has
// flipped shutting-down before the caller (a hook on the dispatcher's
// goroutine) returns. The result arrives on the returned channel.
func (k *Kit) GoShutdown(ctx context.Context) <-chan error {
	x := k.X
	ch := make(chan error, 1)
	go func() { ch <- x.Shutdown(ctx) }()
	time.Sleep(50 * time.Millisecond)
	return ch
}

// --- callers ---

// grants is a caller's checker: exactly the permissions it names.
type grants map[rbac.Permission]bool

func (g grants) Has(p rbac.Permission) bool { return g[p] }

func (g grants) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindAllOf:
		for _, p := range ps {
			if !g[p] {
				return false
			}
		}
		return len(ps) > 0
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, g.Has)
	}
	return false
}

func (grants) Confined() bool { return false }

func (g grants) Concrete() []rbac.Permission {
	var out []rbac.Permission
	for p := range g {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

type callerKey struct{}

type callerID struct {
	actor tasks.Actor
	who   rbac.Checker
}

// AsUser is a request context of a signed-in user holding perms. It only
// means something to an executor booted WithPrincipal.
func AsUser(user string, perms ...rbac.Permission) context.Context {
	g := grants{}
	for _, p := range perms {
		g[p] = true
	}
	return context.WithValue(context.Background(), callerKey{}, callerID{tasks.Actor{UserID: user, AuthMethod: "session"}, g})
}

// WithPrincipal makes the executor read callers from AsUser's contexts; any
// other context is a kernel-internal call with no identity.
func WithPrincipal() Opt {
	return func(o *executor.Options) {
		o.Principal = func(ctx context.Context) (tasks.Actor, rbac.Checker, bool) {
			c, ok := ctx.Value(callerKey{}).(callerID)
			return c.actor, c.who, ok
		}
	}
}

// Box carries a value from a hook (another goroutine) to the case.
type Box[T any] struct {
	mu  sync.Mutex
	v   T
	set bool
}

// Set stores v.
func (b *Box[T]) Set(v T) { b.mu.Lock(); b.v, b.set = v, true; b.mu.Unlock() }

// Get returns the stored value and whether Set was called.
func (b *Box[T]) Get() (T, bool) { b.mu.Lock(); defer b.mu.Unlock(); return b.v, b.set }
