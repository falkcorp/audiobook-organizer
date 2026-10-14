// file: internal/operations/registry/watchdog_test.go
// version: 1.4.0
// guid: 4d5e6f7a-8b9c-0123-def0-1234567890ab
// last-edited: 2026-10-14

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestWatchdog_StuckOpGetsStrike verifies that an op which never reports
// progress is classified separately from one that reported and then stalled.
func TestWatchdog_StuckOpGetsStrike(t *testing.T) {
	ctx := t.Context()

	// Use 50ms watchdog interval for fast test.
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 50 * time.Millisecond,
	})

	canceled := make(chan struct{})
	def := makeValidDef("test.wdog-stuck")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.wdog-stuck", nil)

	// Wait for the op to start running.
	awaitStatus(t, store, opID, "running", 3*time.Second)

	// Backdate last_progress_at to be stale (older than ProgressTimeout).
	stale := time.Now().UTC().Add(-200 * time.Millisecond)
	store.setLastProgressAt(opID, &stale)

	// Wait for watchdog to fire and cancel the op.
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("stuck op was not canceled within 3s")
	}

	// Give the worker time to write terminal status.
	awaitStatus(t, store, opID, "canceled", 3*time.Second)

	// A run that never calls UpdateProgress is an unwired run, not a run that
	// made progress and then got stuck. The watchdog cancels both, but their
	// remediation is different, so the persisted strike kind must remain
	// discriminating.
	if n := len(store.strikesOfKind(opID, "never_reported")); n == 0 {
		t.Error("expected at least 1 never_reported strike, got 0")
	}
}

// TestWatchdog_UncheckpointedOpGetsStrike verifies that a ResumeRestart op
// that hasn't checkpointed in ≥5 minutes accumulates an uncheckpointed strike.
func TestWatchdog_UncheckpointedOpGetsStrike(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 50 * time.Millisecond,
	})

	started := make(chan struct{})
	def := makeValidDef("test.wdog-uncheckpointed")
	def.ResumePolicy = registry.ResumeRestart
	def.MinCheckpointInterval = 100 * time.Millisecond
	// Backdating started_at (below) would otherwise also trip the stuck check's
	// StartedAt fallback (R-2) and cancel the op before the uncheckpointed
	// branch is reached; a large ProgressTimeout keeps the stuck path quiet.
	def.ProgressTimeout = 30 * time.Minute
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-runCtx.Done()
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.wdog-uncheckpointed", nil)

	<-started

	// Backdate started_at to trigger the uncheckpointed window (≥5 min in real
	// code; the watchdog uses defaultMinCheckpointTimeout which we override via
	// a very stale started_at).
	stale := time.Now().UTC().Add(-6 * time.Minute)
	store.setStartedAt(opID, &stale)
	// Also set the status to "running" explicitly (may already be, but ensure).
	_ = store.UpdateOperationV2Status(opID, "running", nil, nil, nil)

	// Wait up to 500ms for the watchdog to fire and write a strike.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n := len(store.strikesOfKind(opID, "uncheckpointed")); n > 0 {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("expected at least 1 uncheckpointed strike within 500ms, got 0")
}

// TestWatchdog_InMemoryClockPreventsFalseStuck verifies that the watchdog does
// NOT fire when the in-memory atomic clock is fresh, even if the DB
// last_progress_at row is stale — the scenario that occurs when
// UpdateOpProgressV2 is blocked behind PebbleDB L0 compaction (Scenario A).
//
// The op calls UpdateProgress in a loop (keeping the atomic fresh) while the
// test continuously backdates the DB row (simulating stuck DB writes).
func TestWatchdog_InMemoryClockPreventsFalseStuck(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Millisecond,
	})

	ready := make(chan struct{})
	def := makeValidDef("test.wdog-atomic-fresh")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		// Signal that we're inside Run and about to start the loop.
		close(ready)
		// Keep calling UpdateProgress every 20ms so the atomic stays fresh.
		// This simulates an op that is actively making progress but whose DB
		// writes are queued/blocked.
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-runCtx.Done():
				return runCtx.Err()
			case <-ticker.C:
				i++
				_ = rep.UpdateProgress(i, 1000, "processing")
			}
		}
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)

	opID, _ := r.EnqueueOp(ctx, "test.wdog-atomic-fresh", nil)
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("op never entered Run")
	}

	// For 300ms, continuously backdate the DB row to simulate stuck DB writes.
	// The op's UpdateProgress loop keeps the atomic fresh (every 20ms).
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		stale := time.Now().UTC().Add(-500 * time.Millisecond)
		store.setLastProgressAt(opID, &stale)
		time.Sleep(10 * time.Millisecond)
	}

	if strikes := store.strikesOfKind(opID, "stuck"); len(strikes) > 0 {
		t.Errorf("watchdog fired despite fresh in-memory progress clock (got %d stuck strikes)", len(strikes))
	}
}

// TestWatchdog_InfiniteRestartForceDrop verifies that an op with resume_count≥3
// and no high_water_progress advancement is force-dropped.
func TestWatchdog_InfiniteRestartForceDrop(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 50 * time.Millisecond,
	})

	def := makeValidDef("test.wdog-infinite-restart")
	def.ResumePolicy = registry.ResumeRestart
	def.Run = func(_ context.Context, _ json.RawMessage, _ registry.Reporter) error {
		return nil
	}
	_ = r.RegisterOp(def)

	// Enqueue and set resume_count=3 BEFORE starting the registry: enqueueing
	// after Start races the dispatcher — on a loaded runner the worker can pick
	// the op up and complete it before setResumeCount lands (observed in CI).
	// With the registry not yet started, the row is queued and the state is in
	// place before the first dispatch tick can reach checkInfiniteRestart.
	opID, _ := r.EnqueueOp(ctx, "test.wdog-infinite-restart", nil)
	store.setResumeCount(opID, 3)
	r.Start(ctx)

	// Wait for the op to be force-dropped (executeRun calls checkInfiniteRestart).
	awaitStatus(t, store, opID, "interrupted_dropped", 5*time.Second)

	// Verify infinite_restart strike was written.
	if n := len(store.strikesOfKind(opID, "infinite_restart")); n == 0 {
		t.Error("expected at least 1 infinite_restart strike, got 0")
	}
}

// strikeHookRecorder collects the kinds the watchdog strike hook was called
// with.
type strikeHookRecorder struct {
	mu    sync.Mutex
	kinds []string
}

func (h *strikeHookRecorder) hook(kind string) {
	h.mu.Lock()
	h.kinds = append(h.kinds, kind)
	h.mu.Unlock()
}

func (h *strikeHookRecorder) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.kinds)
}

// newHookedRegistry is newSteppedRegistry with the strike hook installed
// through SetWatchdogStrikeHook, the path production takes (the container
// builds the registry without Options).
func newHookedRegistry(store *fakeStore, clk *stepClock, h *strikeHookRecorder) *registry.Registry {
	r := newSteppedRegistry(store, clk)
	r.SetWatchdogStrikeHook(h.hook)
	return r
}

// Options.OnWatchdogStrike installs the same hook as the setter, and the setter
// with nil removes it. NotifyWatchdogStrikeForTest calls the hook the way the
// watchdog does on a strike.
func TestWatchdogStrikeHookOptionsAndNil(t *testing.T) {
	hooks := &strikeHookRecorder{}
	r := registry.NewWithOptions(newFakeStore(), slog.Default(), 2, registry.Options{
		WatchdogInterval: time.Hour,
		OnWatchdogStrike: hooks.hook,
	})
	r.NotifyWatchdogStrikeForTest("stuck")
	if got := hooks.got(); !slices.Equal(got, []string{"stuck"}) {
		t.Fatalf("hook from Options: calls = %v, want [stuck]", got)
	}
	r.SetWatchdogStrikeHook(nil)
	r.NotifyWatchdogStrikeForTest("stuck") // no hook: must be a no-op
	if got := hooks.got(); len(got) != 1 {
		t.Fatalf("hook called after being removed: %v", got)
	}
}

// A stuck strike (the op reported once, then went quiet) calls the hook once
// with "stuck".
func TestWatchdogStuckStrikeRequestsSnapshot(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	clk := newStepClock()
	hooks := &strikeHookRecorder{}
	r := newHookedRegistry(store, clk, hooks)

	touched := make(chan struct{})
	canceled := make(chan struct{})
	def := makeValidDef("test.wdog-hook-stuck")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, rep registry.Reporter) error {
		registry.TouchLiveness(rep)
		close(touched)
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	opID, _ := r.EnqueueOp(ctx, "test.wdog-hook-stuck", nil)

	recv(t, touched, "the single touch")
	clk.Advance(150 * time.Millisecond)
	r.WatchdogCycleForTest()
	if got := hooks.got(); !slices.Equal(got, []string{"stuck"}) {
		t.Fatalf("hook calls = %v, want [stuck]", got)
	}
	recv(t, canceled, "the watchdog to cancel the op")
	if n := len(store.strikesOfKind(opID, "stuck")); n != 1 {
		t.Fatalf("want 1 stuck strike, got %d", n)
	}
}

// A never_reported strike calls the hook once with "never_reported".
func TestWatchdogNeverReportedStrikeRequestsSnapshot(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	clk := newStepClock()
	hooks := &strikeHookRecorder{}
	r := newHookedRegistry(store, clk, hooks)

	started := make(chan struct{})
	canceled := make(chan struct{})
	def := makeValidDef("test.wdog-hook-never")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	opID, _ := r.EnqueueOp(ctx, "test.wdog-hook-never", nil)

	recv(t, started, "the op to start")
	awaitStatus(t, store, opID, "running", 10*time.Second)
	clk.Advance(150 * time.Millisecond)
	r.WatchdogCycleForTest()
	if got := hooks.got(); !slices.Equal(got, []string{"never_reported"}) {
		t.Fatalf("hook calls = %v, want [never_reported]", got)
	}
	recv(t, canceled, "the watchdog to cancel the op")
	if n := len(store.strikesOfKind(opID, "never_reported")); n != 1 {
		t.Fatalf("want 1 never_reported strike, got %d", n)
	}
}

// An uncheckpointed strike cancels nothing and must not call the hook. The
// strike itself is asserted, so "no hook call" cannot pass on a cycle that
// never reached the uncheckpointed branch.
func TestWatchdogUncheckpointedStrikeDoesNotRequestSnapshot(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	clk := newStepClock()
	hooks := &strikeHookRecorder{}
	r := newHookedRegistry(store, clk, hooks)

	started := make(chan struct{})
	def := makeValidDef("test.wdog-hook-uncheckpointed")
	def.ResumePolicy = registry.ResumeRestart
	def.MinCheckpointInterval = 100 * time.Millisecond
	def.ProgressTimeout = 30 * time.Minute // keep the stuck branch quiet
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-runCtx.Done()
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	opID, _ := r.EnqueueOp(ctx, "test.wdog-hook-uncheckpointed", nil)

	recv(t, started, "the op to start")
	awaitStatus(t, store, opID, "running", 10*time.Second)
	// StartedAt is stamped in real time; place it on the stepped clock, past
	// the 5m uncheckpointed floor.
	stale := clk.Now().Add(-6 * time.Minute)
	store.setStartedAt(opID, &stale)
	r.WatchdogCycleForTest()

	if n := len(store.strikesOfKind(opID, "uncheckpointed")); n != 1 {
		t.Fatalf("control: want 1 uncheckpointed strike, got %d", n)
	}
	if got := hooks.got(); len(got) != 0 {
		t.Fatalf("hook called for an uncheckpointed strike: %v", got)
	}
}

// SetWatchdogStrikeHook (the production path) installs the hook after
// construction, and a panicking hook does not take the watchdog down.
func TestWatchdogStrikeHookSetterAndPanicIsolation(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	clk := newStepClock()
	r := newSteppedRegistry(store, clk)
	calls := 0
	r.SetWatchdogStrikeHook(func(string) {
		calls++
		panic("hook failure")
	})

	started := make(chan struct{})
	canceled := make(chan struct{})
	def := makeValidDef("test.wdog-hook-panic")
	def.ResumePolicy = registry.ResumeDrop
	def.ProgressTimeout = 100 * time.Millisecond
	def.Run = func(runCtx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-runCtx.Done()
		close(canceled)
		return runCtx.Err()
	}
	_ = r.RegisterOp(def)
	r.Start(ctx)
	opID, _ := r.EnqueueOp(ctx, "test.wdog-hook-panic", nil)

	recv(t, started, "the op to start")
	awaitStatus(t, store, opID, "running", 10*time.Second)
	clk.Advance(150 * time.Millisecond)
	r.WatchdogCycleForTest() // must not panic
	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}
	// The strike's own consequence (cancel) still happened after the panic.
	recv(t, canceled, "the watchdog to cancel the op")
}
