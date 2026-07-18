// file: internal/metrics/metrics_test.go
// version: 1.1.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-07-18

package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRegister(t *testing.T) {
	// Register should be idempotent - calling it multiple times shouldn't panic
	Register()
	Register()
	Register()

	// If we reach here without panicking, the test passes
	t.Log("Register called multiple times successfully")
}

func TestIncOperationStarted(t *testing.T) {
	Register()

	// Test incrementing different operation types
	operations := []string{"scan", "organize", "metadata_fetch"}

	for _, op := range operations {
		IncOperationStarted(op)
		// If we reach here without panicking, the increment worked
		t.Logf("Incremented operation started for: %s", op)
	}
}

func TestIncOperationCompleted(t *testing.T) {
	Register()

	operations := []string{"scan", "organize", "metadata_fetch"}

	for _, op := range operations {
		IncOperationCompleted(op)
		t.Logf("Incremented operation completed for: %s", op)
	}
}

func TestIncOperationFailed(t *testing.T) {
	Register()

	operations := []string{"scan", "organize", "metadata_fetch"}

	for _, op := range operations {
		IncOperationFailed(op)
		t.Logf("Incremented operation failed for: %s", op)
	}
}

func TestIncOperationCanceled(t *testing.T) {
	Register()

	operations := []string{"scan", "organize", "metadata_fetch"}

	for _, op := range operations {
		IncOperationCanceled(op)
		t.Logf("Incremented operation canceled for: %s", op)
	}
}

func TestObserveOperationDuration(t *testing.T) {
	Register()

	tests := []struct {
		name     string
		opType   string
		duration time.Duration
	}{
		{
			name:     "Short duration",
			opType:   "scan",
			duration: 100 * time.Millisecond,
		},
		{
			name:     "Medium duration",
			opType:   "organize",
			duration: 2 * time.Second,
		},
		{
			name:     "Long duration",
			opType:   "metadata_fetch",
			duration: 10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ObserveOperationDuration(tt.opType, tt.duration)
			t.Logf("Observed duration %v for operation %s", tt.duration, tt.opType)
		})
	}
}

func TestSetBooks(t *testing.T) {
	Register()

	testValues := []int{0, 1, 100, 1000, 10000}

	for _, val := range testValues {
		SetBooks(val)
		t.Logf("Set books gauge to: %d", val)
	}
}

func TestSetFolders(t *testing.T) {
	Register()

	testValues := []int{0, 1, 5, 10, 50}

	for _, val := range testValues {
		SetFolders(val)
		t.Logf("Set folders gauge to: %d", val)
	}
}

func TestSetMemoryAlloc(t *testing.T) {
	Register()

	testValues := []uint64{
		0,
		1024,              // 1KB
		1024 * 1024,       // 1MB
		1024 * 1024 * 100, // 100MB
	}

	for _, val := range testValues {
		SetMemoryAlloc(val)
		t.Logf("Set memory alloc gauge to: %d bytes", val)
	}
}

func TestSetGoroutines(t *testing.T) {
	Register()

	testValues := []int{1, 10, 100, 1000}

	for _, val := range testValues {
		SetGoroutines(val)
		t.Logf("Set goroutines gauge to: %d", val)
	}
}

func TestMetricsIntegration(t *testing.T) {
	Register()

	// Simulate a complete operation lifecycle
	opType := "test_operation"

	// Start operation
	IncOperationStarted(opType)

	// Simulate work
	start := time.Now()
	time.Sleep(10 * time.Millisecond)

	// Complete operation
	duration := time.Since(start)
	ObserveOperationDuration(opType, duration)
	IncOperationCompleted(opType)

	// Update gauges
	SetBooks(42)
	SetFolders(5)
	SetMemoryAlloc(1024 * 1024 * 50) // 50MB
	SetGoroutines(10)

	t.Log("Successfully completed full metrics lifecycle test")
}

func TestMetricsWithMultipleOperationTypes(t *testing.T) {
	Register()

	// Simulate multiple concurrent operations
	operations := []string{"scan", "organize", "metadata_fetch", "backup"}

	for _, op := range operations {
		IncOperationStarted(op)

		// Simulate varying durations
		duration := time.Duration(len(op)) * 100 * time.Millisecond
		ObserveOperationDuration(op, duration)

		// Randomly succeed or fail (for test purposes, all succeed)
		IncOperationCompleted(op)
	}

	t.Logf("Successfully recorded %d different operation types", len(operations))
}

func TestOperationLifecycle_Success(t *testing.T) {
	Register()

	opType := "successful_operation"

	IncOperationStarted(opType)
	time.Sleep(5 * time.Millisecond)
	duration := 5 * time.Millisecond
	ObserveOperationDuration(opType, duration)
	IncOperationCompleted(opType)

	t.Log("Successfully completed operation lifecycle")
}

func TestOperationLifecycle_Failure(t *testing.T) {
	Register()

	opType := "failed_operation"

	IncOperationStarted(opType)
	time.Sleep(5 * time.Millisecond)
	duration := 5 * time.Millisecond
	ObserveOperationDuration(opType, duration)
	IncOperationFailed(opType)

	t.Log("Successfully recorded failed operation")
}

func TestOperationLifecycle_Canceled(t *testing.T) {
	Register()

	opType := "canceled_operation"

	IncOperationStarted(opType)
	time.Sleep(5 * time.Millisecond)
	duration := 5 * time.Millisecond
	ObserveOperationDuration(opType, duration)
	IncOperationCanceled(opType)

	t.Log("Successfully recorded canceled operation")
}

// b2uniqueOpLabels returns an op_id/op_type label pair unique to this test
// run, so parallel/table-driven tests never collide on the same GaugeVec
// label series (task-unique per B2 op-progress-metric work item).
func b2uniqueOpLabels(t *testing.T, suffix string) (opID, opType string) {
	t.Helper()
	return "b2-op-" + suffix, "b2.op_type." + suffix
}

// TestSetOpProgress_SetsGauges verifies UpdateProgress-style calls (via
// SetOpProgress) set both the items-processed and items-total gauges for the
// given op_id/op_type label pair (OPS-5 op-stall exporter, TODO #36).
func TestSetOpProgress_SetsGauges(t *testing.T) {
	Register()
	opID, opType := b2uniqueOpLabels(t, "setprogress")
	defer ClearOpProgress(opID, opType)

	SetOpProgress(opID, opType, 42, 100)

	if got := testutil.ToFloat64(opItemsProcessed.WithLabelValues(opID, opType)); got != 42 {
		t.Errorf("opItemsProcessed = %v, want 42", got)
	}
	if got := testutil.ToFloat64(opItemsTotal.WithLabelValues(opID, opType)); got != 100 {
		t.Errorf("opItemsTotal = %v, want 100", got)
	}

	// A subsequent call (simulating the next UpdateProgress tick) overwrites
	// rather than accumulates — these are gauges, not counters.
	SetOpProgress(opID, opType, 75, 100)
	if got := testutil.ToFloat64(opItemsProcessed.WithLabelValues(opID, opType)); got != 75 {
		t.Errorf("opItemsProcessed after second update = %v, want 75", got)
	}
}

// TestClearOpProgress_DeletesGaugeSeries verifies that ClearOpProgress —
// called by registry.publishOpTerminal on every terminal transition — removes
// the per-op label series entirely (not just zeroes it), so stale op_ids
// don't accumulate as unbounded cardinality once an op completes/fails/
// cancels (OPS-5, TODO #36).
func TestClearOpProgress_DeletesGaugeSeries(t *testing.T) {
	Register()
	opID, opType := b2uniqueOpLabels(t, "clearprogress")

	SetOpProgress(opID, opType, 10, 20)
	if got := testutil.ToFloat64(opItemsProcessed.WithLabelValues(opID, opType)); got != 10 {
		t.Fatalf("precondition: opItemsProcessed = %v, want 10", got)
	}

	ClearOpProgress(opID, opType)

	deletedProcessed := opItemsProcessed.DeleteLabelValues(opID, opType)
	deletedTotal := opItemsTotal.DeleteLabelValues(opID, opType)
	if deletedProcessed {
		t.Error("opItemsProcessed series still present after ClearOpProgress (Delete should have been a no-op on an already-deleted series)")
	}
	if deletedTotal {
		t.Error("opItemsTotal series still present after ClearOpProgress (Delete should have been a no-op on an already-deleted series)")
	}

	// A freshly re-observed value after clearing must start from the new
	// current/total, not any stale prior state (there should be none, since
	// the series was deleted rather than merely reset to 0).
	SetOpProgress(opID, opType, 1, 5)
	defer ClearOpProgress(opID, opType)
	if got := testutil.ToFloat64(opItemsProcessed.WithLabelValues(opID, opType)); got != 1 {
		t.Errorf("opItemsProcessed after re-observe = %v, want 1", got)
	}
}
