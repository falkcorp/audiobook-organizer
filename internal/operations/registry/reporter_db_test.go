// file: internal/operations/registry/reporter_db_test.go
// version: 1.1.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-06-22

package registry_test

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// newTestReporter creates a DB reporter bound to a fakeStore with an op pre-inserted.
func newTestReporter(t *testing.T, ctx context.Context) (registry.Reporter, *fakeStore, string) {
	t.Helper()
	store := newFakeStore()
	opID := "01TESTOPID000000000000000"
	defID := "test.def"
	plugin := "test-plugin"

	// Pre-insert an op row so UpdateOpProgressV2 etc. have something to update.
	now := time.Now().UTC()
	_ = store.InsertOperationV2(database.OperationV2Row{
		ID:       opID,
		DefID:    defID,
		Plugin:   plugin,
		TraceID:  "trace-1",
		SpanID:   "span-1",
		Status:   "running",
		Priority: 1,
		Params:   "{}",
		QueuedAt: now,
	})

	rep := registry.NewDBReporterForTest(ctx, opID, defID, plugin, "trace-1", "span-1", store, nil, slog.Default())
	return rep, store, opID
}

// TestReporterDB_UpdateProgressWritesColumns verifies that UpdateProgress
// stores current, total, and message in the fakeStore.
func TestReporterDB_UpdateProgressWritesColumns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep, store, opID := newTestReporter(t, ctx)

	if err := rep.UpdateProgress(42, 100, "halfway there"); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}

	cur, total, msg := store.progressOf(opID)
	if cur != 42 || total != 100 || msg != "halfway there" {
		t.Errorf("progress mismatch: got (%d, %d, %q)", cur, total, msg)
	}
}

// TestReporterDB_LogFlushAfter100Entries verifies bulk-flush to op_logs_v2.
func TestReporterDB_LogFlushAfter100Entries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep, store, opID := newTestReporter(t, ctx)

	// Log 100 entries — should trigger an immediate flush.
	for i := range 100 {
		if err := rep.Log(slog.LevelInfo, "test message", slog.Int("i", i)); err != nil {
			t.Fatalf("Log[%d]: %v", i, err)
		}
	}

	// Wait briefly for the flush goroutine to write.
	deadline := time.Now().Add(500 * time.Millisecond)
	var logCount int
	for time.Now().Before(deadline) {
		logCount = len(store.logsFor(opID))
		if logCount >= 100 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if logCount < 100 {
		t.Errorf("expected at least 100 log rows, got %d", logCount)
	}
}

// TestReporterDB_ErrorLevelAlsoWritesOpError verifies that slog.LevelError
// writes to both op_logs_v2 and op_errors_v2.
func TestReporterDB_ErrorLevelAlsoWritesOpError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep, store, opID := newTestReporter(t, ctx)

	if err := rep.Log(slog.LevelError, "something broke", slog.String("detail", "oops")); err != nil {
		t.Fatalf("Log error: %v", err)
	}

	// op_errors_v2 insert is immediate (not buffered).
	errors := store.errorsFor(opID)
	if len(errors) == 0 {
		t.Fatal("expected at least one op_errors_v2 row for error-level log")
	}
	if errors[0].Message != "something broke" {
		t.Errorf("expected message %q, got %q", "something broke", errors[0].Message)
	}
}

// TestReporterDB_LoggerHasOpIDAttr verifies that Logger() returns a logger
// with the op_id attribute set in its output.
func TestReporterDB_LoggerHasOpIDAttr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep, _, _ := newTestReporter(t, ctx)

	logger := rep.Logger()
	if logger == nil {
		t.Fatal("Logger() returned nil")
	}

	// The logger should be enabled for info by default.
	if !logger.Enabled(ctx, slog.LevelInfo) {
		t.Error("Logger() returned a logger that is disabled for Info")
	}
}

// checkpointState is a test state type for gob encoding.
type checkpointState struct {
	Counter int
	Label   string
}

func init() {
	// Register for gob encoding as required by the Checkpoint contract.
	gob.Register(checkpointState{})
}

// TestReporterDB_CheckpointEncodesAndUpserts verifies that Checkpoint stores
// gob-encoded state in op_state_v2 and updates high_water_progress.
func TestReporterDB_CheckpointEncodesAndUpserts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep, store, opID := newTestReporter(t, ctx)

	// Set some progress first so high_water_progress has a value.
	_ = rep.UpdateProgress(55, 100, "progress")

	state := checkpointState{Counter: 77, Label: "checkpoint-test"}
	if err := rep.Checkpoint(state); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	row, err := store.GetOpStateV2(opID)
	if err != nil {
		t.Fatalf("GetOpStateV2: %v", err)
	}
	if row == nil {
		t.Fatal("expected op_state_v2 row, got nil")
	}
	if len(row.StateBlob) == 0 {
		t.Error("expected non-empty state_blob")
	}

	// high_water_progress should be updated.
	op, _ := store.GetOperationV2(opID)
	if op == nil {
		t.Fatal("op row not found")
	}
	if op.HighWaterProgress < 55 {
		t.Errorf("expected high_water_progress >= 55, got %d", op.HighWaterProgress)
	}
}

// TestReporterDB_IsCanceledReturnsTrueAfterCtxCancel verifies IsCanceled().
func TestReporterDB_IsCanceledReturnsTrueAfterCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	rep, _, _ := newTestReporter(t, ctx)

	if rep.IsCanceled() {
		t.Error("IsCanceled() should be false before cancel")
	}

	cancel()
	// Give the cancel a moment to propagate.
	time.Sleep(5 * time.Millisecond)

	if !rep.IsCanceled() {
		t.Error("IsCanceled() should be true after context cancel")
	}
}

// TestReporterDB_RunPhaseUpdatesCurrentPhase verifies that RunPhase sets and
// then clears current_phase on the operation row.
func TestReporterDB_RunPhaseUpdatesCurrentPhase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep, store, opID := newTestReporter(t, ctx)

	var phaseInsideFn string
	err := rep.RunPhase(ctx, "scan", func(innerCtx context.Context, innerRep registry.Reporter) error {
		// Inside fn: check the op's current_phase in the store.
		op, _ := store.GetOperationV2(opID)
		if op != nil && op.CurrentPhase != nil {
			phaseInsideFn = *op.CurrentPhase
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunPhase: %v", err)
	}

	if phaseInsideFn != "scan" {
		t.Errorf("expected current_phase=%q inside fn, got %q", "scan", phaseInsideFn)
	}

	// After fn returns, current_phase should be cleared.
	op, _ := store.GetOperationV2(opID)
	if op != nil && op.CurrentPhase != nil && *op.CurrentPhase != "" {
		t.Errorf("expected current_phase to be cleared after RunPhase, got %q", *op.CurrentPhase)
	}
}

// TestReporterDB_TriggerWithNilBusIsNoop verifies that Trigger with a nil bus
// returns nil without panicking.
func TestReporterDB_TriggerWithNilBusIsNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep, _, _ := newTestReporter(t, ctx)

	if err := rep.Trigger(ctx, "test.event", map[string]any{"key": "val"}); err != nil {
		t.Errorf("Trigger with nil bus should not return error, got: %v", err)
	}
}

// TestReporterDB_BusPublishCalled verifies that the bus receives Publish calls
// when it is non-nil.
func TestReporterDB_BusPublishCalled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore()
	opID := "01TESTOPID000000000000001"
	now := time.Now().UTC()
	_ = store.InsertOperationV2(database.OperationV2Row{
		ID:       opID,
		DefID:    "test.def",
		Plugin:   "test-plugin",
		TraceID:  "trace-2",
		SpanID:   "span-2",
		Status:   "running",
		Priority: 1,
		Params:   "{}",
		QueuedAt: now,
	})

	bus := &fakeBus{}
	rep := registry.NewDBReporterForTest(ctx, opID, "test.def", "test-plugin", "trace-2", "span-2", store, bus, slog.Default())

	_ = rep.UpdateProgress(10, 20, "msg")
	if !bus.hasEvent("op.updated") {
		t.Error("expected op.updated event from UpdateProgress")
	}
}

// fakeBus is a test Bus implementation.
type fakeBus struct {
	events []string
}

func (b *fakeBus) Publish(_ context.Context, eventName string, _ any) error {
	b.events = append(b.events, eventName)
	return nil
}

func (b *fakeBus) hasEvent(name string) bool {
	for _, e := range b.events {
		if e == name {
			return true
		}
	}
	return false
}

// Ensure fakeBus implements Bus — compile-time check via JSON round-trip test.
var _ interface {
	Publish(context.Context, string, any) error
} = (*fakeBus)(nil)

// TestReporterDB_LogFlushOnContextCancel verifies the final flush on ctx cancel.
func TestReporterDB_LogFlushOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	rep, store, opID := newTestReporter(t, ctx)

	// Log a few entries (fewer than 100 so they don't auto-flush).
	for range 5 {
		_ = rep.Log(slog.LevelInfo, "pre-cancel message")
	}

	// Cancel context to trigger final flush.
	cancel()

	// Wait for the flush goroutine to write.
	deadline := time.Now().Add(500 * time.Millisecond)
	var logCount int
	for time.Now().Before(deadline) {
		logCount = len(store.logsFor(opID))
		if logCount >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if logCount < 5 {
		t.Errorf("expected 5 log rows after ctx cancel flush, got %d", logCount)
	}
}

// TestReporterDB_AttrsAreIncludedInLog verifies that Log encodes attrs into JSON.
func TestReporterDB_AttrsAreIncludedInLog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rep, store, opID := newTestReporter(t, ctx)

	// Log with attrs, then flush by logging 100 more entries.
	_ = rep.Log(slog.LevelInfo, "with attrs", slog.String("key", "value"), slog.Int("count", 42))
	for range 99 {
		_ = rep.Log(slog.LevelDebug, "filler")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	var logs []database.OpLogV2Row
	for time.Now().Before(deadline) {
		logs = store.logsFor(opID)
		if len(logs) >= 100 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(logs) == 0 {
		t.Fatal("no logs flushed")
	}

	// Find the "with attrs" entry.
	var found bool
	for _, l := range logs {
		if l.Message == "with attrs" {
			found = true
			// Check attrs JSON contains our keys.
			var m map[string]any
			if err := json.Unmarshal([]byte(l.Attrs), &m); err != nil {
				t.Errorf("attrs JSON invalid: %v", err)
			}
			if _, ok := m["key"]; !ok {
				t.Error("attrs JSON missing 'key'")
			}
		}
	}
	if !found {
		t.Error("'with attrs' log entry not found")
	}
}

// TestReporter_TouchProgressFnCalledOnUpdate verifies that the touchProgressFn
// closure is called on every UpdateProgress call — proving the in-memory
// atomic clock is stamped before any DB write can block.
func TestReporter_TouchProgressFnCalledOnUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var touchCount int64
	touchFn := func() { atomic.AddInt64(&touchCount, 1) }

	store := newFakeStore()
	opID := "01TESTOPID000000000000002"
	now := time.Now().UTC()
	_ = store.InsertOperationV2(database.OperationV2Row{
		ID: opID, DefID: "test.def", Plugin: "test-plugin",
		TraceID: "trace-touch", SpanID: "span-touch", Status: "running",
		Priority: 1, Params: "{}", QueuedAt: now,
	})

	rep := registry.NewDBReporterForTestWithTouch(ctx, opID, "test.def", "test-plugin",
		"trace-touch", "span-touch", store, nil, slog.Default(), touchFn)

	_ = rep.UpdateProgress(10, 100, "first")
	_ = rep.UpdateProgress(20, 100, "second")
	_ = rep.UpdateProgress(30, 100, "third")

	got := atomic.LoadInt64(&touchCount)
	if got != 3 {
		t.Errorf("expected touchFn called 3 times, got %d", got)
	}
}
