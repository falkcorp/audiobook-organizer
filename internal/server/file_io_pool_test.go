// file: internal/server/file_io_pool_test.go
// version: 1.0.1
// guid: 9d4e2a8f-1b6c-4f70-a3d1-7e5b9c2f8a04
// last-edited: 2026-09-02

package server

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestParsePebbleKey_ModernFormat(t *testing.T) {
	bookID, opType, ok := parsePebbleKey("pending_file_op:abc123:apply_metadata")
	if !ok || bookID != "abc123" || opType != "apply_metadata" {
		t.Fatalf("got (%q, %q, %v), want (\"abc123\", \"apply_metadata\", true)", bookID, opType, ok)
	}
}

func TestParsePebbleKey_LegacyFormat(t *testing.T) {
	// Legacy keys (pre-2.2.0) had no opType — should default to apply_metadata.
	bookID, opType, ok := parsePebbleKey("pending_file_op:abc123")
	if !ok || bookID != "abc123" || opType != "apply_metadata" {
		t.Fatalf("got (%q, %q, %v), want (\"abc123\", \"apply_metadata\", true)", bookID, opType, ok)
	}
}

func TestParsePebbleKey_UnrelatedKey(t *testing.T) {
	if _, _, ok := parsePebbleKey("operation:xyz"); ok {
		t.Fatal("parsePebbleKey accepted a non-pending_file_op key")
	}
}

func TestPendingKey_Composite(t *testing.T) {
	if got := pendingKey("book1", "tag_writeback"); got != "book1:tag_writeback" {
		t.Fatalf("pendingKey = %q, want \"book1:tag_writeback\"", got)
	}
}

func TestRecoveryDispatch_RegisterAndLookup(t *testing.T) {
	const opType = "test_op_type_recovery_dispatch"
	defer func() {
		recoveryDispatchMu.Lock()
		delete(recoveryDispatch, opType)
		recoveryDispatchMu.Unlock()
	}()

	var called atomic.Int32
	RegisterFileOpRecovery(opType, func(string) {
		called.Add(1)
	})

	fn, ok := lookupFileOpRecovery(opType)
	if !ok {
		t.Fatal("lookupFileOpRecovery returned !ok for registered op type")
	}
	fn("book123")
	if called.Load() != 1 {
		t.Fatalf("recovery handler not invoked, called = %d", called.Load())
	}

	if _, ok := lookupFileOpRecovery("unregistered_op_type"); ok {
		t.Fatal("lookupFileOpRecovery returned ok for unregistered op type")
	}
}

func TestPendingJobs_DistinctOpTypesPerBook(t *testing.T) {
	// Two different op types for the same book should both be tracked
	// — this is what motivated the per-op key schema change.
	pool := NewFileIOPool(2)
	defer pool.Stop()

	done := make(chan struct{}, 2)
	pool.SubmitTyped("book1", "op_a", func() { done <- struct{}{} })
	pool.SubmitTyped("book1", "op_b", func() { done <- struct{}{} })

	deadline := time.After(2 * time.Second)
	for range 2 {
		select {
		case <-done:
		case <-deadline:
			t.Fatal("timed out waiting for both jobs to run")
		}
	}
}
