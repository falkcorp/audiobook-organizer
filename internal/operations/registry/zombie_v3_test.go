// file: internal/operations/registry/zombie_v3_test.go
// version: 1.1.1
// guid: b2d29b34-41c4-40f1-93e1-3d616e6251bc
// last-edited: 2026-10-11

package registry_test

// The registry against a real Pebble store, whose v2 methods stage the opv3
// projection (05-PR4): the zombie is v3 "stopping" while its goroutine lives,
// the enqueue dedupe skips it, the boot sweep maps a stopping run by its stop
// reason, and a requeue records its successor. The fakes embed NopOpsV3 and
// cannot exercise any of this.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

func newV3Store(t *testing.T) *database.PebbleStore {
	t.Helper()
	p, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// newV3Registry is a registry on a real store with a short abandon grace and
// a watchdog that stays out of the way. Shutdown runs at cleanup before the
// store closes (cleanups run last-in first-out).
func newV3Registry(t *testing.T, store database.OpsStore, release ...func()) *registry.Registry {
	t.Helper()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		AbandonGrace:     50 * time.Millisecond,
		WatchdogInterval: time.Hour,
		SweepInterval:    time.Hour,
		AbandonedCap:     100,
	})
	t.Cleanup(func() {
		for _, rel := range release {
			rel()
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(sctx)
	})
	return r
}

func v2Status(t *testing.T, store *database.PebbleStore, id string) string {
	t.Helper()
	row, err := store.GetOperationV2(id)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2(%s): %v %v", id, row, err)
	}
	return row.Status
}

func awaitV2(t *testing.T, store *database.PebbleStore, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if row, err := store.GetOperationV2(id); err == nil && row != nil && row.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("op %s: status %q, want %q after 5s", id, v2Status(t, store, id), want)
}

func runV3(t *testing.T, store *database.PebbleStore, id string) *database.RunRecord {
	t.Helper()
	rec, err := store.GetRunV3(id)
	if err != nil || rec == nil {
		t.Fatalf("GetRunV3(%s): %v %v", id, rec, err)
	}
	return rec
}

func eventNames(t *testing.T, store *database.PebbleStore, id string) string {
	t.Helper()
	evts, err := store.ListRunEventsV3(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range evts {
		names = append(names, e.Event)
	}
	return strings.Join(names, " ")
}

func awaitZombieCount(t *testing.T, r *registry.Registry, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.ZombieCount() != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := r.ZombieCount(); n != want {
		t.Fatalf("ZombieCount = %d, want %d", n, want)
	}
}

// TestZombieIsStoppingNotTerminal: a canceled run whose goroutine outlives the
// grace is v3 stopping{user, zombie} while the goroutine lives, its v2 row
// reads "running", and only when the goroutine returns does it become
// canceled (goroutine_returned).
func TestZombieIsStoppingNotTerminal(t *testing.T) {
	store := newV3Store(t)
	s := newStubbornRun()
	r := newV3Registry(t, store, s.releaseRun)
	def := makeValidDef("test.v3-zombie")
	def.Run = s.run
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	id, err := r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	if err := r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	awaitZombieCount(t, r, 1)
	rec := runV3(t, store, id)
	if rec.State != state.V3Stopping || !rec.Zombie || rec.StopReason != state.StopUser {
		t.Fatalf("zombie in v3 = %s zombie=%v reason=%q, want stopping{user, zombie}", rec.State, rec.Zombie, rec.StopReason)
	}
	if got := v2Status(t, store, id); got != "running" {
		t.Fatalf("zombie in v2 = %q, want running (stopping mirrors as running)", got)
	}
	if ev := eventNames(t, store, id); ev != "enqueue dispatch stop_requested" {
		t.Fatalf("events while stopping = %q", ev)
	}
	// A second cancel of a run already stopping as canceled writes nothing:
	// the run ends when its goroutine returns, not when it is asked again.
	if err := r.Cancel(id); err != nil {
		t.Fatalf("cancel of a stopping run: %v", err)
	}
	if got := v2Status(t, store, id); got != "running" {
		t.Fatalf("after a second cancel the row is %q, want running until the goroutine returns", got)
	}
	if ev := eventNames(t, store, id); ev != "enqueue dispatch stop_requested" {
		t.Fatalf("a second cancel recorded events: %q", ev)
	}
	s.releaseRun()
	// Status first, release second: by the time the handle is gone (the
	// zombie count drops) the row must already be terminal, or a new run of
	// the key could start beside a row that still reads running.
	deadline := time.Now().Add(5 * time.Second)
	for r.ZombieCount() != 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := v2Status(t, store, id); got != "canceled" {
		t.Fatalf("row = %q when the zombie's handle was released, want canceled already", got)
	}
	if rec := runV3(t, store, id); rec.State != state.V3Canceled || rec.Zombie || rec.StopReason != state.StopUser {
		t.Fatalf("after the goroutine returned: %s zombie=%v reason=%q, want canceled", rec.State, rec.Zombie, rec.StopReason)
	}
	if ev := eventNames(t, store, id); ev != "enqueue dispatch stop_requested goroutine_returned" {
		t.Fatalf("events = %q", ev)
	}
}

// TestEnqueueDedupeSkipsStopping is the C-3 regression for v3: a stopping
// run's row reads "running" and it has a live (zombie) handle, so only the
// isZombie skip keeps the dedupe from handing its id to every new enqueue.
func TestEnqueueDedupeSkipsStopping(t *testing.T) {
	store := newV3Store(t)
	s := newStubbornRun()
	r := newV3Registry(t, store, s.releaseRun)
	def := makeValidDef("test.v3-dedupe")
	def.ConcurrencyKey = "v3.key"
	def.Run = s.run
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	first, err := r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	_ = r.Cancel(first)
	awaitZombieCount(t, r, 1)
	if rec := runV3(t, store, first); rec.State != state.V3Stopping {
		t.Fatalf("first run is %s, want stopping", rec.State)
	}
	second, err := r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("enqueue returned the stopping run's id %s: the op type is silently disabled (C-3)", first)
	}
	for range 10 {
		r.DispatchCycleForTest(t.Context())
		time.Sleep(2 * time.Millisecond)
	}
	if got := v2Status(t, store, second); got != "queued" {
		t.Fatalf("second run is %q while the stopping run holds the key, want queued", got)
	}
	s.releaseRun()
	awaitV2(t, store, second, "completed")
}

// seedStoppingRun leaves a run the way a crashed process does: v2 "running",
// v3 stopping{reason} (or running when reason is "").
func seedStoppingRun(t *testing.T, store *database.PebbleStore, defID, reason string) string {
	t.Helper()
	id := ulid.Make().String()
	now := time.Now().UTC()
	if err := store.InsertOperationV2(database.OperationV2Row{ID: id, DefID: defID, Plugin: "test", Status: "queued", Params: "{}", QueuedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOperationV2Status(id, "running", &now, nil, nil); err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		rec := runV3(t, store, id)
		if _, err := store.TransitionRunV3(database.TransitionRequest{ID: id, ExpectState: rec.State, ExpectAttempt: rec.Attempt,
			ExpectFenceEpoch: rec.FenceEpoch, Event: state.EvStopRequested, StopReason: reason, Zombie: true}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// TestBootSweepStoppingBecomesInterruptedCrash: at boot a v3 running run, and
// a stopping one stopped for a quiesce or a shutdown, take boot_sweep to
// interrupted{crash} and are then resumed by their ResumePolicy exactly as a
// "running" row always was. A run stopped by a user cancel or a timeout keeps
// the outcome its stop decided and is not resumed (05-PR4's deviation from
// the spec's single boot_sweep row; see bootSweepV3).
func TestBootSweepStoppingBecomesInterruptedCrash(t *testing.T) {
	cases := []struct {
		reason       string
		wantV3       state.V3
		wantV2       string
		wantRuns     int32
		wantEventEnd string
	}{
		{"", state.V3Succeeded, "completed", 1, "boot_sweep resume dispatch return_nil"},
		{state.StopQuiesce, state.V3Succeeded, "completed", 1, "stop_requested boot_sweep resume dispatch return_nil"},
		{state.StopShutdown, state.V3Succeeded, "completed", 1, "stop_requested boot_sweep resume dispatch return_nil"},
		{state.StopUser, state.V3Canceled, "canceled", 0, "stop_requested goroutine_returned"},
		{state.StopTimeout, state.V3TimedOut, "canceled", 0, "stop_requested goroutine_returned"},
	}
	for _, c := range cases {
		t.Run("stop="+c.reason, func(t *testing.T) {
			store := newV3Store(t)
			def := makeValidDef("test.v3-boot")
			def.ResumePolicy = registry.ResumeRestart
			id := seedStoppingRun(t, store, def.ID, c.reason)
			var runs atomic.Int32
			def.Run = func(context.Context, json.RawMessage, registry.Reporter) error { runs.Add(1); return nil }
			r := newV3Registry(t, store)
			if err := r.RegisterOp(def); err != nil {
				t.Fatal(err)
			}
			r.Start(t.Context())
			awaitV2(t, store, id, c.wantV2)
			// Give a resumed run time to finish its v3 write.
			deadline := time.Now().Add(5 * time.Second)
			for runV3(t, store, id).State != c.wantV3 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			rec := runV3(t, store, id)
			if rec.State != c.wantV3 {
				t.Fatalf("v3 state = %s, want %s", rec.State, c.wantV3)
			}
			if got := runs.Load(); got != c.wantRuns {
				t.Fatalf("Run called %d times, want %d", got, c.wantRuns)
			}
			if ev := eventNames(t, store, id); !strings.HasSuffix(ev, c.wantEventEnd) {
				t.Fatalf("events = %q, want ...%q", ev, c.wantEventEnd)
			}
			if c.wantRuns == 1 {
				evts, _ := store.ListRunEventsV3(id, 0)
				for _, e := range evts {
					if e.Event == string(state.EvBootSweep) && (e.To != state.V3Interrupted || e.Reason != state.WhyCrash) {
						t.Fatalf("boot_sweep went to %s{%s}, want interrupted{crash}", e.To, e.Reason)
					}
				}
			}
		})
	}
}

// TestResumeRequeueSetsSuccessor: ResumeRequeue retires the original as
// dropped with its replacement's id as SuccessorID (native decide_drop after
// the boot sweep), and the mirror is the row v2 always wrote. A queued row at
// boot has no v3 move to dropped, so it takes the plain v2 write and records
// no successor.
func TestResumeRequeueSetsSuccessor(t *testing.T) {
	store := newV3Store(t)
	def := makeValidDef("test.v3-requeue")
	def.ResumePolicy = registry.ResumeRequeue
	running := seedStoppingRun(t, store, def.ID, "")
	queuedID := ulid.Make().String()
	if err := store.InsertOperationV2(database.OperationV2Row{ID: queuedID, DefID: def.ID, Plugin: "test", Status: "queued",
		Params: `{"n":2}`, QueuedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	r := newV3Registry(t, store)
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())

	rec := runV3(t, store, running)
	if rec.State != state.V3Dropped || rec.SuccessorID == "" {
		t.Fatalf("retired run = %s successor %q, want dropped with a successor", rec.State, rec.SuccessorID)
	}
	row, err := store.GetOperationV2(running)
	if err != nil || row == nil || row.Status != "interrupted_dropped" || row.ErrorMessage == nil || *row.ErrorMessage != state.RequeuedMessage {
		t.Fatalf("retired v2 row = %+v %v, want interrupted_dropped %q", row, err, state.RequeuedMessage)
	}
	if ev := eventNames(t, store, running); !strings.HasSuffix(ev, "boot_sweep decide_drop") {
		t.Fatalf("events = %q", ev)
	}
	awaitV2(t, store, rec.SuccessorID, "completed")

	q := runV3(t, store, queuedID)
	if q.State != state.V3Dropped || q.SuccessorID != "" {
		t.Fatalf("queued row retired as %s successor %q, want dropped with no successor (v2 fallback)", q.State, q.SuccessorID)
	}
	if got := v2Status(t, store, queuedID); got != "interrupted_dropped" {
		t.Fatalf("queued row v2 status = %q", got)
	}
}

// TestZombieTimeoutIsTimedOut: a run whose deadline expired and whose
// goroutine outlived the grace is stopping{timeout} while it lives, and
// timed_out (v2 "canceled", message starting "timeout:") when it returns --
// the same outcome the boot sweep gives it after a crash. Mutation checks:
// putting "abandoned: ..." before the reason in the message records canceled;
// mapping a deadline to the "user" stop reason fails the stopping check.
func TestZombieTimeoutIsTimedOut(t *testing.T) {
	store := newV3Store(t)
	s := newStubbornRun()
	r := newV3Registry(t, store, s.releaseRun)
	def := makeValidDef("test.v3-timeout")
	def.Timeout = 100 * time.Millisecond
	def.Run = s.run
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	id, err := r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	awaitZombieCount(t, r, 1)
	if rec := runV3(t, store, id); rec.State != state.V3Stopping || rec.StopReason != state.StopTimeout {
		t.Fatalf("timed-out zombie in v3 = %s{%s}, want stopping{timeout}", rec.State, rec.StopReason)
	}
	s.releaseRun()
	awaitV2(t, store, id, "canceled")
	row, err := store.GetOperationV2(id)
	if err != nil || row == nil || row.ErrorMessage == nil || !strings.HasPrefix(*row.ErrorMessage, state.TimeoutPrefix) {
		t.Fatalf("v2 row = %+v %v, want canceled with a %q message", row, err, state.TimeoutPrefix)
	}
	if rec := runV3(t, store, id); rec.State != state.V3TimedOut {
		t.Fatalf("after the goroutine returned: %s, want timed_out", rec.State)
	}
}

// TestRetryNotBlockedByStoppingRun: a stopping run (zombie) reads "running"
// with a live handle until its goroutine returns; Retry of ANOTHER row of the
// same def must not be refused because of it (the C-3 trap on the Retry
// path). The retried row is queued and Gate 3 holds it until the key frees.
func TestRetryNotBlockedByStoppingRun(t *testing.T) {
	store := newV3Store(t)
	s := newStubbornRun()
	r := newV3Registry(t, store, s.releaseRun)
	def := makeValidDef("test.v3-retry")
	def.ConcurrencyKey = "v3.retry-key"
	def.ResumePolicy = registry.ResumeRestart
	def.Run = s.run
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	first, err := r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	if err := r.Cancel(first); err != nil {
		t.Fatal(err)
	}
	awaitZombieCount(t, r, 1)
	other := ulid.Make().String()
	now := time.Now().UTC()
	msg := "quiesced earlier"
	if err := store.InsertOperationV2(database.OperationV2Row{ID: other, DefID: def.ID, Plugin: "test", Status: "interrupted_quiesced",
		Params: "{}", QueuedAt: now, CompletedAt: &now, ErrorMessage: &msg}); err != nil {
		t.Fatal(err)
	}
	if err := r.RetryInterrupted(t.Context(), other, "tester"); err != nil {
		t.Fatalf("retry of another row while a run of the def is stopping: %v, want nil", err)
	}
	for range 10 {
		r.DispatchCycleForTest(t.Context())
		time.Sleep(2 * time.Millisecond)
	}
	if got := v2Status(t, store, other); got != "queued" {
		t.Fatalf("retried row is %q while the stopping run holds the key, want queued", got)
	}
	s.releaseRun()
	awaitV2(t, store, other, "completed")
}

// TestZombieExitNotifiesDependents: the dependency scheduler hears about a
// zombie when its goroutine returns, not at abandonment: a dependent stays
// waiting_deps while the stopped run's goroutine still writes, and is failed
// once it ends canceled.
func TestZombieExitNotifiesDependents(t *testing.T) {
	store := newV3Store(t)
	s := newStubbornRun()
	r := newV3Registry(t, store, s.releaseRun)
	defA := makeValidDef("test.v3-dep-a")
	defA.Run = s.run
	defB := makeValidDef("test.v3-dep-b")
	defB.Requires = []registry.Requirement{{Kind: registry.ReqOpCompleted, OpType: defA.ID}}
	for _, d := range []registry.OperationDef{defA, defB} {
		if err := r.RegisterOp(d); err != nil {
			t.Fatal(err)
		}
	}
	r.SetDepsScheduler(registry.NewDepsScheduler(r, &pebbleSchedulerStore{PebbleStore: store}))
	reqs, _ := json.Marshal(defB.Requires)
	waiting := ulid.Make().String()
	if err := store.InsertOperationV2(database.OperationV2Row{ID: waiting, DefID: defB.ID, Plugin: "test", Status: "waiting_deps",
		QueuedAt: time.Now().UTC(), SubjectType: "book", SubjectID: "book-42", Requirements: string(reqs), Params: `{"book_id":"book-42"}`}); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	a, err := r.EnqueueOp(t.Context(), defA.ID, map[string]any{"book_id": "book-42"})
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	if err := r.Cancel(a); err != nil {
		t.Fatal(err)
	}
	awaitZombieCount(t, r, 1)
	time.Sleep(50 * time.Millisecond)
	if got := v2Status(t, store, waiting); got != "waiting_deps" {
		t.Fatalf("dependent is %q while its prerequisite's goroutine still runs, want waiting_deps", got)
	}
	s.releaseRun()
	awaitV2(t, store, a, "canceled")
	awaitV2(t, store, waiting, "failed")
}

// transitionFailingStore is a real store whose TransitionRunV3 fails with err
// for event (every event when ""), so a test can drive the registry's v3
// fallbacks.
type transitionFailingStore struct {
	*database.PebbleStore
	event state.Event
	err   error
}

func (s *transitionFailingStore) TransitionRunV3(req database.TransitionRequest) (*database.RunRecord, error) {
	if s.event == "" || req.Event == s.event {
		return nil, s.err
	}
	return s.PebbleStore.TransitionRunV3(req)
}

func opTerminalCount(b *recordingBus, opID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, ev := range b.events {
		if p, ok := ev.payload.(map[string]any); ok && ev.name == "op.terminal" && p["op_id"] == opID {
			n++
		}
	}
	return n
}

// TestBootSweepFallbacks: a stopping{user} run at boot whose native boot
// write fails. With ErrStaleTransition the record does not describe the row
// any more and the row decides (ResumeRestart: it runs again). With any other
// error the record is current and the stop's outcome is kept through the v2
// write: canceled, not run, op.terminal published.
func TestBootSweepFallbacks(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantV2   string
		wantRuns int32
		wantTerm int
	}{
		{"stale record: the row decides", fmt.Errorf("wrapped: %w", database.ErrStaleTransition), "completed", 1, 1},
		{"store error: the stop stands", errors.New("injected store failure"), "canceled", 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newV3Store(t)
			def := makeValidDef("test.v3-boot-fallback")
			def.ResumePolicy = registry.ResumeRestart
			id := seedStoppingRun(t, store, def.ID, state.StopUser)
			var runs atomic.Int32
			def.Run = func(context.Context, json.RawMessage, registry.Reporter) error { runs.Add(1); return nil }
			r := newV3Registry(t, &transitionFailingStore{PebbleStore: store, err: c.err})
			bus := &recordingBus{}
			r.SetBus(bus)
			if err := r.RegisterOp(def); err != nil {
				t.Fatal(err)
			}
			r.Start(t.Context())
			awaitV2(t, store, id, c.wantV2)
			time.Sleep(20 * time.Millisecond)
			if got := runs.Load(); got != c.wantRuns {
				t.Fatalf("Run called %d times, want %d", got, c.wantRuns)
			}
			if n := opTerminalCount(bus, id); n < c.wantTerm {
				t.Fatalf("op.terminal published %d times, want at least %d", n, c.wantTerm)
			}
		})
	}
}

// TestResumeRequeueStaleRecordFallsBackToV2: when the native retire is
// refused as stale, the requeue still retires the original the v2 way
// (interrupted_dropped, the requeue message) and inserts the replacement; no
// successor is recorded because the record could not be trusted.
func TestResumeRequeueStaleRecordFallsBackToV2(t *testing.T) {
	store := newV3Store(t)
	def := makeValidDef("test.v3-requeue-stale")
	def.ResumePolicy = registry.ResumeRequeue
	running := seedStoppingRun(t, store, def.ID, "")
	r := newV3Registry(t, &transitionFailingStore{PebbleStore: store, event: state.EvDecideDrop,
		err: fmt.Errorf("wrapped: %w", database.ErrStaleTransition)})
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	row, err := store.GetOperationV2(running)
	if err != nil || row == nil || row.Status != "interrupted_dropped" || row.ErrorMessage == nil || *row.ErrorMessage != state.RequeuedMessage {
		t.Fatalf("retired v2 row = %+v %v, want interrupted_dropped %q", row, err, state.RequeuedMessage)
	}
	if rec := runV3(t, store, running); rec.State != state.V3Dropped || rec.SuccessorID != "" {
		t.Fatalf("retired run = %s successor %q, want dropped with no successor", rec.State, rec.SuccessorID)
	}
	rows, err := store.ListOperationsV2Since(time.Time{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	replacements := 0
	for _, o := range rows {
		if o.DefID == def.ID && o.ID != running {
			replacements++
		}
	}
	if replacements != 1 {
		t.Fatalf("%d replacement rows, want 1", replacements)
	}
}

// TestShutdownZombieRecordsStoppingShutdown: a run Shutdown stops whose
// goroutine outlives the grace is recorded stopping{shutdown} (the shutdown
// branch of executeRun) before Shutdown's timeout stamp ends it interrupted,
// so its audit trail says why it stopped. Mutation check: drop markStopping
// from the shutdown branch and the stop_requested event is missing.
func TestShutdownZombieRecordsStoppingShutdown(t *testing.T) {
	store := newV3Store(t)
	s := newStubbornRun()
	defer s.releaseRun()
	def := makeValidDef("test.v3-shutdown-zombie")
	def.ResumePolicy = registry.ResumeRestart
	def.Run = s.run
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		AbandonGrace:     50 * time.Millisecond,
		WatchdogInterval: time.Hour,
		SweepInterval:    time.Hour,
		AbandonedCap:     100,
	})
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	id, err := r.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	sctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := r.Shutdown(sctx); err == nil {
		t.Fatal("Shutdown returned nil; the zombie should have held the drain until the context expired")
	}
	evts, err := store.ListRunEventsV3(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	for _, e := range evts {
		if e.Event == string(state.EvStopRequested) && e.Reason == state.StopShutdown {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("events = %q, want a stop_requested{shutdown}", eventNames(t, store, id))
	}
	if rec := runV3(t, store, id); rec.State != state.V3Interrupted {
		t.Fatalf("after the shutdown stamp: %s, want interrupted", rec.State)
	}
}
