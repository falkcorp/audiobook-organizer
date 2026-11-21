// file: core/internal/executor/publish_test.go
// version: 1.0.0
// guid: 8d8dc699-fbb7-4d23-93b1-69156bc2f71d
// last-edited: 2026-11-21

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 slice E6b: the executor publishes core.task.run-changed.v1,
// core.task.log-appended.v1 and core.task.finished.v1 after each run commit
// (publish.go). Fixtures are Batman's: kinds are Gotham's, runs are Wayne's.

// tape records what the executor's sink receives, in sink order. on, when
// set, runs on the drainer goroutine before the emission is recorded.
type tape struct {
	mu  sync.Mutex
	ems []emission
	on  func(emission)
}

func (p *tape) sink(em emission) {
	if p.on != nil {
		p.on(em)
	}
	p.mu.Lock()
	p.ems = append(p.ems, em)
	p.mu.Unlock()
}

func (p *tape) all() []emission {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.ems)
}

// of is the emissions of kind for run ("" = every run), in sink order.
func (p *tape) of(kind emitKind, run tasks.RunID) []emission {
	var out []emission
	for _, em := range p.all() {
		if em.kind == kind && (run == "" || em.run == run) {
			out = append(out, em)
		}
	}
	return out
}

func (p *tape) count() int { return len(p.all()) }

// tapeHarness boots stores, builds an executor whose emissions land on p and
// runs it unless noRun (a test that wants to set a seam first starts it).
func tapeHarness(t testing.TB, p *tape, defs []tasks.Definition, edit func(*Options), noRun bool) *harness {
	t.Helper()
	fs := vfs.NewCrashableMem()
	stores, ks := bootStores(t, fs)
	return onStores(t, fs, stores, ks, defs, func(o *Options) {
		o.sink = p.sink
		if edit != nil {
			edit(o)
		}
	}, !noRun)
}

// drained shuts the executor down, which waits for the publish queue to empty,
// so every emission the run made is on the tape.
func (h *harness) drained() {
	h.t.Helper()
	if err := h.x.Shutdown(context.Background()); err != nil {
		h.t.Fatalf("Shutdown: %v", err)
	}
}

// within fails the test when fn does not return in d.
func within(t testing.TB, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s", what, d)
	}
}

func failBody(rc *tasks.RC, _ noParams) (string, error) { return "", errors.New("Joker got away") }

// stateRank orders a run's states for the no-regression check.
func stateRank(s string) int {
	switch tasks.RunState(s) {
	case tasks.StateQueued:
		return 0
	case tasks.StateRunning:
		return 1
	case tasks.StateStopping:
		return 2
	default:
		return 3
	}
}

// TestExecutor_PublishesRunChangedAfterCommit (F5-18 E6b; names coined by the
// F5-18 amendment of 2026-11-20): one run-changed per committed change to a
// run, and never one before the commit that carries it.
func TestExecutor_PublishesRunChangedAfterCommit(t *testing.T) {
	t.Run("one per committed transition, each already in the store", func(t *testing.T) {
		p := &tape{}
		var stores tasks.Stores
		var bad atomic.Int64
		p.on = func(em emission) {
			// The sink runs after the commit: the store already holds the
			// transition (its event sequence is at least the emission's), and
			// a finished emission is for a run the store has ended.
			rec, err := stores.Runs.Get(context.Background(), em.run)
			switch {
			case err != nil:
				bad.Add(1)
				t.Errorf("emission %d for %s published before the run exists: %v", em.kind, em.run, err)
			case em.kind == emitRunChanged && rec.Seq.Event < em.seq:
				bad.Add(1)
				t.Errorf("run-changed for %s at event %d published while the store is at %d", em.run, em.seq, rec.Seq.Event)
			case em.kind == emitFinished && !rec.State.Terminal():
				bad.Add(1)
				t.Errorf("finished for %s published while the store has it %s", em.run, rec.State)
			}
		}
		fs := vfs.NewCrashableMem()
		var ks storage.Keyspace
		stores, ks = bootStores(t, fs)
		h := onStores(t, fs, stores, ks, []tasks.Definition{
			taskDef("gotham.patrol", okBody, readOnly("gotham.patrol")),
			taskDef("gotham.ambush", failBody, readOnly("gotham.ambush")),
		}, func(o *Options) { o.sink = p.sink }, true)
		var ids []tasks.RunID
		for i := 0; i < 20; i++ {
			ids = append(ids, h.start("gotham.patrol", tasks.StartRequest{}), h.start("gotham.ambush", tasks.StartRequest{}))
		}
		for _, id := range ids {
			h.waitState(id, tasks.StateSucceeded, tasks.StateFailed)
		}
		h.drained()
		for _, id := range ids {
			got := p.of(emitRunChanged, id)
			// One for the insert and one per stored transition: queued, then
			// running, then the terminal state.
			if want := 1 + len(h.eventsOf(id)); len(got) != want {
				t.Errorf("run %s: %d run-changed, want %d (the insert plus %d stored transitions)", id, len(got), want, len(h.eventsOf(id)))
			}
			var states []string
			for _, em := range got {
				states = append(states, em.view.State)
			}
			last := h.get(id).State
			if len(states) < 3 || states[0] != string(tasks.StateQueued) || states[len(states)-1] != string(last) {
				t.Errorf("run %s published states %v, want queued ... %s", id, states, last)
			}
		}
		if bad.Load() > 0 {
			t.Fatalf("%d emissions were published before their commit", bad.Load())
		}
	})

	t.Run("a staged insert publishes nothing until the batch commits", func(t *testing.T) {
		p := &tape{}
		h := tapeHarness(t, p, []tasks.Definition{taskDef("gotham.patrol", okBody, readOnly("gotham.patrol"))},
			func(o *Options) { o.Catalog = catalogWith(t, "gotham:run") }, true) // gotham's system actor may start its kinds
		stager := h.x.(tasks.Stager)
		staged, release := make(chan struct{}), make(chan struct{})
		var ref tasks.RunRef
		errc := make(chan error, 1)
		go func() {
			errc <- stager.WriteRuns(context.Background(), h.e.newBatch, func(ctx context.Context, b *storage.Batch) error {
				var err error
				ref, err = stager.StartIn(ctx, b, "gotham.patrol", tasks.StartRequest{Mode: tasks.ModeRequestLive})
				close(staged)
				<-release
				return err
			})
		}()
		<-staged
		// The run is staged and its emission collected. Give a premature
		// publish every chance to show.
		time.Sleep(150 * time.Millisecond)
		if n := p.count(); n != 0 {
			t.Fatalf("%d emissions published while the batch had not committed", n)
		}
		if _, err := h.stores.Runs.Get(context.Background(), ref.ID); err == nil {
			t.Fatal("the run exists before the commit")
		}
		close(release)
		if err := <-errc; err != nil {
			t.Fatalf("WriteRuns: %v", err)
		}
		h.drained()
		got := p.of(emitRunChanged, ref.ID)
		if len(got) == 0 || got[0].view.State != string(tasks.StateQueued) || got[0].view.ID != string(ref.ID) {
			t.Fatalf("after the commit run-changed = %+v, want the queued run %s first", got, ref.ID)
		}
	})

	t.Run("a progress tick publishes the progress, with the run's fresh state", func(t *testing.T) {
		p := &tape{}
		reported := make(chan struct{})
		proceed := make(chan struct{})
		h := tapeHarness(t, p, []tasks.Definition{taskDef("gotham.patrol", func(rc *tasks.RC, _ noParams) (string, error) {
			rc.Progress().Set(3, tasks.Known(10))
			close(reported)
			<-proceed
			return "done", nil
		}, readOnly("gotham.patrol"))}, nil, false)
		id := h.start("gotham.patrol", tasks.StartRequest{})
		<-reported
		eventually(t, "the tick to publish", func() bool {
			for _, em := range p.of(emitRunChanged, id) {
				if em.view.Done == 3 && em.view.Total == 10 && em.view.State == string(tasks.StateRunning) {
					return true
				}
			}
			return false
		})
		close(proceed)
		h.waitState(id, tasks.StateSucceeded)
	})
}

// TestExecutor_LogAppendedKeyedByRunID: every line a run logs is published on
// log-appended keyed by that run's ID, in the order the lines were stored,
// with the stored line's content, and never under another run's key.
func TestExecutor_LogAppendedKeyedByRunID(t *testing.T) {
	const lines = 15
	body := func(tag string) func(rc *tasks.RC, _ noParams) (string, error) {
		return func(rc *tasks.RC, _ noParams) (string, error) {
			for i := 1; i <= lines; i++ {
				rc.Progress().Note(fmt.Sprintf("%s %02d", tag, i))
			}
			return "logged", nil
		}
	}
	p := &tape{}
	h := tapeHarness(t, p, []tasks.Definition{
		taskDef("gotham.signal", body("signal"), readOnly("gotham.signal")),
		taskDef("gotham.cipher", body("cipher"), readOnly("gotham.cipher")),
	}, nil, false)
	a := h.start("gotham.signal", tasks.StartRequest{})
	b := h.start("gotham.cipher", tasks.StartRequest{})
	h.waitState(a, tasks.StateSucceeded)
	h.waitState(b, tasks.StateSucceeded)
	h.drained()
	for id, tag := range map[tasks.RunID]string{a: "signal", b: "cipher"} {
		got := p.of(emitLogAppended, id)
		if len(got) != lines {
			t.Fatalf("run %s: %d log-appended, want %d", id, len(got), lines)
		}
		var stored []string
		for e, err := range h.stores.Runs.Pages(context.Background(), id, tasks.PageQuery{Page: tasks.PageLog, Limit: 1000}) {
			if err != nil {
				t.Fatal(err)
			}
			stored = append(stored, e.Log.Message)
		}
		for i, em := range got {
			want := fmt.Sprintf("%s %02d", tag, i+1)
			if em.line.Message != want || em.line.Level != "info" || em.line.At.IsZero() {
				t.Errorf("run %s line %d = %+v, want %q at info with a time", id, i, em.line, want)
			}
			if !strings.HasPrefix(em.line.Message, tag) {
				t.Errorf("run %s published %q, another run's line", id, em.line.Message)
			}
		}
		if len(stored) < lines || !slices.Equal(stored[:lines], func() []string {
			var m []string
			for _, em := range got {
				m = append(m, em.line.Message)
			}
			return m
		}()) {
			t.Errorf("run %s: published lines differ from the stored log %v", id, stored)
		}
	}
}

// TestExecutor_FinishedOncePerTerminalRun: finished is published exactly once
// per run that ends (succeeded, failed or canceled), carries the terminal
// view the run-changed beside it carries, and is not published for a run that
// has not ended. A terminal batch that is staged more than once announces the
// end once: the cipher run's batch conflicts at commit and is re-staged, the
// riddle run's OnTerminal fails and its batch is re-staged without the hook
// (the notify_failed line is published too).
func TestExecutor_FinishedOncePerTerminalRun(t *testing.T) {
	p := &tape{}
	started := make(chan tasks.RunID, 4)
	var stores tasks.Stores
	var cipherCalls atomic.Int32
	fs := vfs.NewCrashableMem()
	var ks storage.Keyspace
	stores, ks = bootStores(t, fs)
	h := onStores(t, fs, stores, ks, []tasks.Definition{
		taskDef("gotham.patrol", okBody, readOnly("gotham.patrol")),
		taskDef("gotham.ambush", failBody, readOnly("gotham.ambush")),
		taskDef("gotham.stakeout", blockUntilCtx(started), readOnly("gotham.stakeout")),
		taskDef("gotham.cipher", okBody, readOnly("gotham.cipher")),
		taskDef("gotham.riddle", okBody, readOnly("gotham.riddle")),
	}, func(o *Options) {
		o.sink = p.sink
		o.OnTerminal = func(ctx context.Context, b *storage.Batch, v tasks.RunView) error {
			switch v.Kind {
			case "gotham.cipher":
				// The first staging of the terminal batch commits a competing
				// write to the row after the batch read it, so the commit
				// conflicts and the stage runs again.
				if cipherCalls.Add(1) == 1 {
					return competingWrite(stores, v.ID)
				}
			case "gotham.riddle":
				return errors.New("the Bat-Signal is out")
			}
			return nil
		}
	}, true)
	ok := h.start("gotham.patrol", tasks.StartRequest{})
	bad := h.start("gotham.ambush", tasks.StartRequest{})
	cipher := h.start("gotham.cipher", tasks.StartRequest{})
	riddle := h.start("gotham.riddle", tasks.StartRequest{})
	held := h.start("gotham.stakeout", tasks.StartRequest{})
	<-started
	for _, id := range []tasks.RunID{ok, cipher, riddle} {
		h.waitState(id, tasks.StateSucceeded)
	}
	h.waitState(bad, tasks.StateFailed)
	// The held run is still running: it has run-changed events and no finished.
	eventually(t, "the stakeout's running view", func() bool {
		for _, em := range p.of(emitRunChanged, held) {
			if em.view.State == string(tasks.StateRunning) {
				return true
			}
		}
		return false
	})
	if n := len(p.of(emitFinished, held)); n != 0 {
		t.Fatalf("finished published %d times for a run still running", n)
	}
	if err := h.x.Cancel(context.Background(), held); err != nil {
		t.Fatal(err)
	}
	h.waitState(held, tasks.StateCanceled)
	h.drained()
	if n := cipherCalls.Load(); n != 2 {
		t.Fatalf("OnTerminal ran %d times for the cipher run, want 2: the conflict never re-staged its terminal batch", n)
	}
	want := map[tasks.RunID]tasks.RunState{ok: tasks.StateSucceeded, bad: tasks.StateFailed, cipher: tasks.StateSucceeded, riddle: tasks.StateSucceeded, held: tasks.StateCanceled}
	for id, state := range want {
		fin := p.of(emitFinished, id)
		if len(fin) != 1 {
			t.Fatalf("run %s (%s): finished published %d times, want once", id, state, len(fin))
		}
		if fin[0].view.State != string(state) {
			t.Errorf("run %s finished as %q, want %q", id, fin[0].view.State, state)
		}
		terminal := 0
		for _, em := range p.of(emitRunChanged, id) {
			if stateRank(em.view.State) == 3 {
				terminal++
				if em.view != fin[0].view {
					t.Errorf("run %s: the terminal run-changed %+v differs from finished %+v", id, em.view, fin[0].view)
				}
			}
		}
		if terminal != 1 {
			t.Errorf("run %s: %d terminal run-changed, want 1", id, terminal)
		}
	}
	if lines := p.of(emitLogAppended, riddle); len(lines) != 1 || lines[0].line.Level != "error" || !strings.Contains(lines[0].line.Message, "notification was not sent") {
		t.Errorf("riddle run's log-appended = %+v, want the one notify_failed error line", lines)
	}
}

// competingWrite commits a progress write to the run's row from a batch of its
// own, so a batch that read the row earlier fails its commit with a conflict.
func competingWrite(stores tasks.Stores, id tasks.RunID) error {
	return storage.RetryOnConflict(context.Background(), 4, func(ctx context.Context) error {
		b := stores.Runs.(batchOpener).NewBatch()
		defer func() { _ = b.Close() }()
		if err := stores.Runs.Progress(ctx, b, id, 1, 2, "a competing write"); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
}

// TestExecutor_NoPublishWhenCommitFails: a stage that errors, a batch whose
// commit fails, and a commit conflict that re-stages the batch publish nothing
// for the attempt that did not commit; only the attempt that lands publishes.
func TestExecutor_NoPublishWhenCommitFails(t *testing.T) {
	newH := func(t *testing.T) (*harness, *tape, tasks.Stager) {
		p := &tape{}
		h := tapeHarness(t, p, []tasks.Definition{taskDef("gotham.patrol", okBody, readOnly("gotham.patrol"))},
			func(o *Options) { o.Catalog = catalogWith(t, "gotham:run") }, true)
		return h, p, h.x.(tasks.Stager)
	}
	stage := func(st tasks.Stager, refs *[]tasks.RunID, after func(b *storage.Batch) error) func(ctx context.Context, b *storage.Batch) error {
		return func(ctx context.Context, b *storage.Batch) error {
			ref, err := st.StartIn(ctx, b, "gotham.patrol", tasks.StartRequest{Mode: tasks.ModeRequestLive})
			if err != nil {
				return err
			}
			*refs = append(*refs, ref.ID)
			return after(b)
		}
	}
	t.Run("the commit itself fails", func(t *testing.T) {
		h, p, st := newH(t)
		var refs []tasks.RunID
		err := st.WriteRuns(context.Background(), h.e.newBatch, stage(st, &refs, func(b *storage.Batch) error {
			return b.Close() // Commit now returns storage.ErrBatchDone: the batch never lands
		}))
		if err == nil {
			t.Fatal("WriteRuns of a closed batch succeeded")
		}
		h.drained()
		if n := p.count(); n != 0 {
			t.Fatalf("%d emissions published for a batch whose commit failed", n)
		}
		if _, gerr := h.stores.Runs.Get(context.Background(), refs[0]); gerr == nil {
			t.Fatal("the run exists although its commit failed")
		}
	})
	t.Run("the stage fails after staging", func(t *testing.T) {
		h, p, st := newH(t)
		var refs []tasks.RunID
		boom := errors.New("Riddler's riddle")
		if err := st.WriteRuns(context.Background(), h.e.newBatch, stage(st, &refs, func(*storage.Batch) error { return boom })); !errors.Is(err, boom) {
			t.Fatalf("WriteRuns = %v, want %v", err, boom)
		}
		h.drained()
		if n := p.count(); n != 0 {
			t.Fatalf("%d emissions published for a stage that failed", n)
		}
	})
	t.Run("a conflict re-stages the batch and only the committed attempt publishes", func(t *testing.T) {
		h, p, st := newH(t)
		var refs []tasks.RunID
		calls := 0
		err := st.WriteRuns(context.Background(), h.e.newBatch, stage(st, &refs, func(*storage.Batch) error {
			if calls++; calls == 1 {
				return fmt.Errorf("first attempt: %w", storage.ErrConflict)
			}
			return nil
		}))
		if err != nil {
			t.Fatalf("WriteRuns: %v", err)
		}
		h.drained()
		if len(refs) != 2 {
			t.Fatalf("the stage ran %d times, want 2", len(refs))
		}
		if got := p.of(emitRunChanged, refs[0]); len(got) != 0 {
			t.Errorf("the discarded attempt's run %s published %d events", refs[0], len(got))
		}
		if got := p.of(emitRunChanged, refs[1]); len(got) != 1 {
			t.Errorf("the committed attempt's run %s published %d events, want 1", refs[1], len(got))
		}
		if n := p.count(); n != 1 {
			t.Errorf("%d emissions in all, want 1", n)
		}
	})
}

// TestExecutor_NilEventsIsANoOp: with no Events (nil, a nil bus, a zero scope)
// the executor builds no publisher and runs unchanged, and a bus that has not
// declared the topics refuses the publish without disturbing the run.
func TestExecutor_NilEventsIsANoOp(t *testing.T) {
	cases := map[string]events.Publisher{
		"nil":          nil,
		"nil bus":      (*events.Bus)(nil),
		"zero scope":   events.Scope{},
		"undeclared":   mustBus(t, false).Scope("core"),
		"wrong module": mustBus(t, true).Scope("gotham"),
	}
	for name, pub := range cases {
		t.Run(name, func(t *testing.T) {
			fs := vfs.NewCrashableMem()
			stores, ks := bootStores(t, fs)
			h := onStores(t, fs, stores, ks, []tasks.Definition{taskDef("gotham.patrol", func(rc *tasks.RC, _ noParams) (string, error) {
				rc.Progress().Note("on patrol")
				return "ok", nil
			}, readOnly("gotham.patrol"))}, func(o *Options) { o.Events = pub }, true)
			if name == "nil" && h.e.pub != nil {
				t.Fatal("a nil Events built a publisher")
			}
			id := h.start("gotham.patrol", tasks.StartRequest{})
			if r := h.waitState(id, tasks.StateSucceeded); resultOf(t, r) != "ok" {
				t.Fatalf("result %s", r.Result)
			}
			h.drained()
		})
	}
}

// mustBus builds a bus; declared declares the three task topics as core,
// subscribes to none and freezes it.
func mustBus(t testing.TB, declared bool) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if declared {
		if err := bus.Declare("core", tasks.RunChangedTopicSpec(), tasks.LogAppendedTopicSpec(), tasks.FinishedTopicSpec()); err != nil {
			t.Fatal(err)
		}
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close(context.Background()) })
	return bus
}

// TestExecutor_PublishesOnTheRealBus: through the real bus, as core's scope,
// the three topics carry the typed payloads under the run's key, and what a
// realtime subscriber gets after the topic's own Redact has no blocked_by.
func TestExecutor_PublishesOnTheRealBus(t *testing.T) {
	bus, err := events.NewBus(events.BusOptions{Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	specs := []events.TopicSpec{tasks.RunChangedTopicSpec(), tasks.LogAppendedTopicSpec(), tasks.FinishedTopicSpec()}
	if err := bus.Declare("core", specs...); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	got := map[string][]events.RawEnvelope{}
	for _, s := range specs {
		name := s.Name
		if err := bus.Subscribe("core", events.OnRaw(name, "core.executor-test."+name, func(_ context.Context, e events.RawEnvelope) {
			mu.Lock()
			got[name] = append(got[name], e)
			mu.Unlock()
		})); err != nil {
			t.Fatal(err)
		}
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close(context.Background()) })

	fs := vfs.NewCrashableMem()
	stores, ks := bootStores(t, fs)
	h := onStores(t, fs, stores, ks, []tasks.Definition{taskDef("gotham.patrol", func(rc *tasks.RC, _ noParams) (string, error) {
		rc.Progress().Note("Bat-Signal lit")
		return "ok", nil
	}, readOnly("gotham.patrol"))}, func(o *Options) { o.Events = bus.Scope("core") }, true)
	id := h.start("gotham.patrol", tasks.StartRequest{})
	h.waitState(id, tasks.StateSucceeded)
	h.drained()
	eventually(t, "the bus to deliver finished", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got[tasks.FinishedTopicName]) == 1 && len(got[tasks.LogAppendedTopicName]) == 1 && len(got[tasks.RunChangedTopicName]) >= 3
	})
	mu.Lock()
	defer mu.Unlock()
	for name, envs := range got {
		for _, e := range envs {
			if e.Key != string(id) {
				t.Errorf("%s envelope keyed %q, want the run %q", name, e.Key, id)
			}
		}
	}
	var fin tasks.RunPayload
	if err := json.Unmarshal(got[tasks.FinishedTopicName][0].Payload, &fin); err != nil || fin.State != string(tasks.StateSucceeded) || fin.ID != string(id) {
		t.Errorf("finished payload %s (%v), want the succeeded run %s", got[tasks.FinishedTopicName][0].Payload, err, id)
	}
	var line tasks.LogAppended
	if err := json.Unmarshal(got[tasks.LogAppendedTopicName][0].Payload, &line); err != nil || line.Message != "Bat-Signal lit" {
		t.Errorf("log-appended payload %s (%v)", got[tasks.LogAppendedTopicName][0].Payload, err)
	}
}

// TestExecutor_StreamPayloadHasBlockedByBlank: the stream never carries the
// blocking run's ID (F5-18i). A queued run that is deferred behind a holder
// and then committed (a merge into it) publishes its deferral with no blocker,
// though GET /runs names the blocker to a caller who may view it; and the
// topic's Redact, which the realtime stream applies per subscriber, blanks
// blocked_by even in a payload that has it.
func TestExecutor_StreamPayloadHasBlockedByBlank(t *testing.T) {
	p := &tape{}
	started := make(chan tasks.RunID, 1)
	merge := func(c *tasks.Common) {
		c.Dedupe = tasks.DedupePolicy{Merge: func(_, incoming json.RawMessage) (json.RawMessage, bool, error) { return incoming, true, nil }}
	}
	h := tapeHarness(t, p, []tasks.Definition{
		taskDef("gotham.evidence", blockUntilCtx(started)),
		taskDef("gotham.forgery", okBody, merge),
	}, nil, false)
	holder := h.start("gotham.evidence", tasks.StartRequest{})
	<-started
	waiter := h.start("gotham.forgery", tasks.StartRequest{})
	eventually(t, "the forgery to be deferred", func() bool { return h.viewOf(waiter).Deferred == deferWriteSet })

	// A caller who may view the blocker's kind is told who blocks the run; this
	// is what makes the blank on the stream a decision and not an absence.
	named := h.viewOf(waiter)
	if named.BlockedBy != holder {
		t.Fatalf("the REST view names %q as the blocker, want %q: the fixture does not block", named.BlockedBy, holder)
	}

	// Commit a change to the deferred queued run: a merge-fold into it.
	ref, err := h.x.Start(context.Background(), "gotham.forgery", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: json.RawMessage(`{"case":"second"}`)})
	if err != nil || ref.ID != waiter || !ref.Existing {
		t.Fatalf("second start = %+v, %v; want a fold into %s", ref, err, waiter)
	}
	var merged *emission
	eventually(t, "the merge to publish", func() bool {
		for _, em := range p.of(emitRunChanged, waiter) {
			if em.view.Deferred != "" {
				merged = &em
				return true
			}
		}
		return false
	})
	if merged.view.Deferred != deferWriteSet || merged.view.State != string(tasks.StateQueued) {
		t.Errorf("merge payload = %+v, want a queued run deferred by %q", merged.view, deferWriteSet)
	}
	if merged.view.BlockedBy != "" {
		t.Errorf("the stream payload names blocked_by %q; the stream must never carry it", merged.view.BlockedBy)
	}
	for _, em := range p.all() {
		if em.kind != emitLogAppended && em.view.BlockedBy != "" {
			t.Errorf("an emission for %s carries blocked_by %q", em.run, em.view.BlockedBy)
		}
	}

	// The topic's Redact over a payload that does have the blocker (the REST
	// view of a caller who may see it) blanks it for any subscriber, privileged
	// or not.
	raw, err := json.Marshal(tasks.NewRunPayload(named))
	if err != nil || !strings.Contains(string(raw), `"blocked_by":"`+string(holder)+`"`) {
		t.Fatalf("fixture payload %s (%v) lacks the blocker", raw, err)
	}
	for who, checker := range map[string]rbac.Checker{"admin": grants{rbac.CoreAdmin: true, rbac.Permission("core:users.view"): true}, "plain": grants{}, "nil": nil} {
		out := tasks.RunViewRedactor()(checker, raw)
		var back tasks.RunPayload
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("%s: redacted payload %s: %v", who, out, err)
		}
		if back.BlockedBy != "" {
			t.Errorf("%s: Redact left blocked_by %q on the stream payload", who, back.BlockedBy)
		}
		if back.Deferred != deferWriteSet {
			t.Errorf("%s: Redact dropped the deferral reason: %+v", who, back)
		}
	}
	if err := h.x.Cancel(context.Background(), holder); err != nil {
		t.Fatal(err)
	}
	h.waitState(waiter, tasks.StateSucceeded)
}

// TestExecutor_PerRunOrderUnderConcurrentWorkers: with many workers committing
// at once, and a cancel racing each run's own progress and log writes, every
// run's emissions reach the sink in the order they committed: states never
// step back, a terminal state is last, progress and event sequence never
// decrease, log lines keep their order, and finished follows the terminal
// run-changed. A seam widens the window between a commit and its append so a
// missing sequencer shows up here.
func TestExecutor_PerRunOrderUnderConcurrentWorkers(t *testing.T) {
	const runs, steps = 24, 12
	var defs []tasks.Definition
	for i := 0; i < runs; i++ {
		kind := fmt.Sprintf("gotham.patrol%02d", i)
		defs = append(defs, taskDef(kind, func(rc *tasks.RC, _ noParams) (string, error) {
			for s := 1; s <= steps; s++ {
				if rc.Canceled() {
					return "", context.Cause(rc.Context())
				}
				rc.Progress().Set(int64(s), tasks.Known(steps))
				rc.Progress().Note(fmt.Sprintf("step %02d", s))
				runtime.Gosched()
			}
			return "walked", nil
		}, readOnly(kind), func(c *tasks.Common) { c.Cancel = tasks.Cooperative(time.Minute) }))
	}
	p := &tape{}
	h := tapeHarness(t, p, defs, func(o *Options) { o.Config.Workers = 8 }, true)
	h.e.pub.betweenCommitAndAppend = func() {
		if rand.IntN(3) == 0 {
			time.Sleep(time.Duration(rand.IntN(400)) * time.Microsecond)
		} else {
			runtime.Gosched()
		}
	}
	if err := h.x.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var ids []tasks.RunID
	for i := 0; i < runs; i++ {
		ids = append(ids, h.start(fmt.Sprintf("gotham.patrol%02d", i), tasks.StartRequest{}))
	}
	var wg sync.WaitGroup
	for i, id := range ids {
		if i%2 == 0 {
			continue // half of the runs are canceled mid-flight, racing their own writes
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(rand.IntN(3000)) * time.Microsecond)
			_ = h.x.Cancel(context.Background(), id) // may land after the run ended: ErrRunActive-style refusals are fine
		}()
	}
	wg.Wait()
	for _, id := range ids {
		h.waitState(id, tasks.StateSucceeded, tasks.StateCanceled, tasks.StateFailed)
	}
	h.drained()
	for _, id := range ids {
		var rank, done int
		var seq uint64
		terminalAt := -1
		for i, em := range p.of(emitRunChanged, id) {
			r := stateRank(em.view.State)
			switch {
			case i == 0 && em.view.State != string(tasks.StateQueued):
				t.Errorf("run %s: first run-changed is %q, want queued", id, em.view.State)
			case r < rank:
				t.Errorf("run %s: state stepped back from rank %d to %q at emission %d", id, rank, em.view.State, i)
			case terminalAt >= 0:
				t.Errorf("run %s: run-changed %d (%q) after the terminal one", id, i, em.view.State)
			case em.view.Done < done:
				t.Errorf("run %s: progress stepped back from %d to %d at emission %d", id, done, em.view.Done, i)
			case em.seq < seq:
				t.Errorf("run %s: event sequence stepped back from %d to %d at emission %d", id, seq, em.seq, i)
			}
			rank, done, seq = max(rank, r), max(done, em.view.Done), max(seq, em.seq)
			if r == 3 && terminalAt < 0 {
				terminalAt = i
			}
		}
		if terminalAt < 0 {
			t.Errorf("run %s: no terminal run-changed", id)
		}
		if t.Failed() {
			for i, em := range p.of(emitRunChanged, id) {
				t.Logf("run %s emission %d: state %s done %d seq %d", id, i, em.view.State, em.view.Done, em.seq)
			}
			t.FailNow()
		}
		next := 1
		for _, em := range p.of(emitLogAppended, id) {
			if want := fmt.Sprintf("step %02d", next); em.line.Message != want {
				t.Errorf("run %s: log line %q, want %q", id, em.line.Message, want)
			}
			next++
		}
		// finished sits after the terminal run-changed in the sink's order.
		pos := map[emitKind]int{}
		for i, em := range p.all() {
			if em.run == id && ((em.kind == emitRunChanged && stateRank(em.view.State) == 3) || em.kind == emitFinished) {
				pos[em.kind] = i
			}
		}
		if len(p.of(emitFinished, id)) != 1 || pos[emitFinished] < pos[emitRunChanged] {
			t.Errorf("run %s: finished (%d, %d times) not after its terminal run-changed (%d)", id, pos[emitFinished], len(p.of(emitFinished, id)), pos[emitRunChanged])
		}
	}
}

// TestExecutor_SlowSubscriberDoesNotStallTheExecutor: a sink that does not
// return delays publication and nothing else. Starts return, runs dispatch,
// make progress and end, and ledger writes commit while the sink is stuck; the
// stuck events arrive, in order, once it returns.
func TestExecutor_SlowSubscriberDoesNotStallTheExecutor(t *testing.T) {
	p := &tape{}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	p.on = func(emission) { <-release }
	h := tapeHarness(t, p, []tasks.Definition{
		taskDef("gotham.patrol", func(rc *tasks.RC, _ noParams) (string, error) {
			rc.Progress().Note("on patrol")
			rc.Progress().Set(1, tasks.Known(1))
			return "ok", nil
		}, readOnly("gotham.patrol")),
		taskDef("gotham.ambush", okBody, readOnly("gotham.ambush")),
	}, nil, false)
	var ids []tasks.RunID
	within(t, 10*time.Second, "starting runs behind a stuck sink", func() {
		for i := 0; i < 6; i++ {
			ids = append(ids, h.start("gotham.patrol", tasks.StartRequest{}), h.start("gotham.ambush", tasks.StartRequest{}))
		}
	})
	within(t, 10*time.Second, "runs ending behind a stuck sink", func() {
		for _, id := range ids {
			h.waitState(id, tasks.StateSucceeded)
		}
	})
	if n := len(p.all()); n != 0 {
		t.Fatalf("%d emissions recorded while the sink was held", n)
	}
	unblock()
	h.drained()
	for _, id := range ids {
		if n := len(p.of(emitFinished, id)); n != 1 {
			t.Errorf("run %s: finished %d times after the sink returned, want 1", id, n)
		}
	}
}
