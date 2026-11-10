// file: internal/server/file_io_pool_test.go
// version: 1.1.0
// guid: 9d4e2a8f-1b6c-4f70-a3d1-7e5b9c2f8a04
// last-edited: 2026-11-10

package server

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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

// kvFake is a pool store holding fixed pending rows.
type kvFake struct {
	rawKVWriter
	rows    []database.KVPair
	deleted atomic.Int32
}

func (k *kvFake) ScanPrefix(string) ([]database.KVPair, error) { return k.rows, nil }
func (k *kvFake) SetRaw(string, []byte) error                  { return nil }
func (k *kvFake) DeleteRaw(string) error                       { k.deleted.Add(1); return nil }

// TestRecoverInterruptedFileOps_EndsAtAStop (review MAJOR 1a): a stop that has
// begun ends the replay between jobs. The rows stay for the next start.
func TestRecoverInterruptedFileOps_EndsAtAStop(t *testing.T) {
	const opType = "test_op_recover_stop"
	t.Cleanup(func() {
		recoveryDispatchMu.Lock()
		delete(recoveryDispatch, opType)
		recoveryDispatchMu.Unlock()
	})
	var ran atomic.Int32
	RegisterFileOpRecovery(opType, func(string) { ran.Add(1) })

	rows := []database.KVPair{
		{Key: pendingFileOpPrefix + "batman:" + opType, Value: []byte(`{"book_id":"batman","op_type":"` + opType + `"}`)},
		{Key: pendingFileOpPrefix + "robin:" + opType, Value: []byte(`{"book_id":"robin","op_type":"` + opType + `"}`)},
	}
	pool := NewFileIOPool(1)
	t.Cleanup(pool.Stop)
	pool.SetStore(&kvFake{rows: rows})

	stop := make(chan struct{})
	close(stop)
	RecoverInterruptedFileOps(pool, stop)
	pool.Stop()
	if got := ran.Load(); got != 0 {
		t.Fatalf("recovery re-queued %d jobs after a stop; want 0", got)
	}

	// Not stopped: both rows replay.
	pool2 := NewFileIOPool(1)
	pool2.SetStore(&kvFake{rows: rows})
	RecoverInterruptedFileOps(pool2, nil)
	pool2.Stop()
	if got := ran.Load(); got != 2 {
		t.Fatalf("recovery without a stop ran %d jobs; want 2", got)
	}
}

// TestSubmitTyped_StopFreesABlockedOverflowWait: a job waiting for an overflow
// slot (backpressure) is dropped, not stuck, when the stop channel closes.
func TestSubmitTyped_StopFreesABlockedOverflowWait(t *testing.T) {
	pool := NewFileIOPool(1)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		pool.Stop()
	})
	// One job on the worker, 500 in the buffer, one on the overflow goroutine
	// (taking the only overflow slot): the next submit blocks on the slot.
	for i := range 502 {
		if !pool.SubmitTyped(fmt.Sprintf("alfred-%d", i), "hold", func() { <-release }) {
			t.Fatalf("submit %d refused", i)
		}
	}

	stop := make(chan struct{})
	result := make(chan bool, 1)
	go func() { result <- pool.submitTyped(stop, "joker", "hold", func() {}) }()
	select {
	case <-result:
		t.Fatal("submit returned while every slot was taken")
	case <-time.After(150 * time.Millisecond):
	}
	close(stop)
	select {
	case accepted := <-result:
		if accepted {
			t.Fatal("a submit freed by a stop reported accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stop did not free the blocked submit")
	}
}
