// file: internal/operations/registry/zombie_v3_test.go
// version: 1.0.0
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
	"log/slog"
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
func newV3Registry(t *testing.T, store *database.PebbleStore, release ...func()) *registry.Registry {
	t.Helper()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		AbandonGrace:     50 * time.Millisecond,
		WatchdogInterval: time.Hour,
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
	s.releaseRun()
	awaitV2(t, store, id, "canceled")
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
