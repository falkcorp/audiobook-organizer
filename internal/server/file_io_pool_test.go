// file: internal/server/file_io_pool_test.go
// version: 1.1.2
// guid: 9d4e2a8f-1b6c-4f70-a3d1-7e5b9c2f8a04
// last-edited: 2026-11-10

package server

import (
	"fmt"
	"slices"
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
	fillPoolForBackpressure(t, pool, release)

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

// fillPoolForBackpressure takes the worker, the 500-slot buffer and the only
// overflow slot of a 1-worker pool with jobs that block on release, so the
// next submit waits for an overflow slot.
//
// The first job is submitted alone and waited on until the worker is running
// it. Without that, the worker can still be starting when the buffer fills:
// the 501st job then takes the one overflow slot while the buffer still holds
// the first, and the 502nd blocks on the overflow semaphore forever, hanging
// the helper (seen as a 10-minute test timeout under -count=5).
func fillPoolForBackpressure(t *testing.T, pool *FileIOPool, release chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	if !pool.SubmitTyped("alfred-0", "hold", func() { close(started); <-release }) {
		t.Fatal("submit 0 refused")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the pool's worker never started the first job")
	}
	for i := 1; i < 502; i++ {
		if !pool.SubmitTyped(fmt.Sprintf("alfred-%d", i), "hold", func() { <-release }) {
			t.Fatalf("submit %d refused", i)
		}
	}
}

func pendingHas(pool *FileIOPool, bookID string) bool {
	return slices.Contains(pool.PendingBookIDs(), bookID)
}

// TestSubmitTyped_DropAtStopLeavesNoPendingEntry: a submit dropped by a stop
// must not leave the in-memory pending entry behind: no worker will ever run
// the job that would remove it, so the book would read as busy for the rest of
// the process. The persisted row stays, for the next start's replay.
func TestSubmitTyped_DropAtStopLeavesNoPendingEntry(t *testing.T) {
	kv := &kvFake{}
	pool := NewFileIOPool(1)
	pool.SetStore(kv)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		pool.Stop()
	})
	fillPoolForBackpressure(t, pool, release)

	stop := make(chan struct{})
	result := make(chan bool, 1)
	go func() { result <- pool.submitTyped(stop, "joker", "hold", func() {}) }()
	deadline := time.Now().Add(5 * time.Second)
	for !pendingHas(pool, "joker") { // tryQueue recorded it before blocking
		if time.Now().After(deadline) {
			t.Fatal("the blocked submit never recorded its pending entry")
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	if accepted := <-result; accepted {
		t.Fatal("a submit freed by a stop reported accepted")
	}
	if pendingHas(pool, "joker") {
		t.Fatal("a dropped submit left its in-memory pending entry behind")
	}
	if kv.deleted.Load() != 0 {
		t.Fatal("the persisted pending row was deleted; it must stay for the next start")
	}
}

// TestSubmitTyped_DropByAStoppedPoolLeavesNoPendingEntry is the same for the
// pool-stopped drop, taken when Stop runs while a submit waits for an overflow
// slot.
func TestSubmitTyped_DropByAStoppedPoolLeavesNoPendingEntry(t *testing.T) {
	pool := NewFileIOPool(1)
	release := make(chan struct{})
	fillPoolForBackpressure(t, pool, release)

	result := make(chan bool, 1)
	go func() { result <- pool.submitTyped(nil, "joker", "hold", func() {}) }()
	deadline := time.Now().Add(5 * time.Second)
	for !pendingHas(pool, "joker") {
		if time.Now().After(deadline) {
			t.Fatal("the blocked submit never recorded its pending entry")
		}
		time.Sleep(time.Millisecond)
	}
	stopped := make(chan struct{})
	go func() { pool.Stop(); close(stopped) }()
	// Stop sets stopped under the write lock, which the blocked submit does not
	// hold; wait for it, then free the overflow slot so the submit proceeds.
	for {
		pool.submitMu.RLock()
		isStopped := pool.stopped
		pool.submitMu.RUnlock()
		if isStopped {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if accepted := <-result; accepted {
		t.Fatal("a submit dropped by a stopped pool reported accepted")
	}
	<-stopped
	if pendingHas(pool, "joker") {
		t.Fatal("a submit dropped by a stopped pool left its in-memory pending entry behind")
	}
}
