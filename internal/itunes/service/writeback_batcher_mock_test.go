// file: internal/itunes/service/writeback_batcher_mock_test.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-10-07
//
// White-box unit tests for WriteBackBatcher: NewWriteBackBatcher, Enqueue,
// EnqueueAdd, EnqueueRemove, UpdateConfig, flush (no-op path), and Stop.
// These tests exercise pure logic — no real ITL files are created or read.
//
// We use database.MockStore as the WriteBackStore because MockStore already
// satisfies database.Store (a superset of WriteBackStore), so there is no
// need to hand-roll a separate stub that would drift from the real interfaces.

package itunesservice

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newMockStore returns a zero-value database.MockStore whose unconfigured
// methods all return nil/zero silently, which is the correct posture for a
// batcher test that never exercises actual DB reads.
func newMockStore() *database.MockStore {
	return &database.MockStore{}
}

// disabledFlushCfg returns a config where AutoWriteBack is enabled (so Enqueue
// proceeds) but ITLWriteBackEnabled / LibraryWritePath are not set, so flush()
// will exit early without attempting any file I/O.
func disabledFlushCfg() WriteBackBatcherConfig {
	return WriteBackBatcherConfig{
		AutoWriteBack:       true,
		ITLWriteBackEnabled: false,
		LibraryWritePath:    "",
	}
}

// autoOffCfg returns a config where AutoWriteBack is false, so Enqueue/EnqueueAdd/
// EnqueueRemove are all no-ops.
func autoOffCfg() WriteBackBatcherConfig {
	return WriteBackBatcherConfig{
		AutoWriteBack:       false,
		ITLWriteBackEnabled: true,
		LibraryWritePath:    "/fake/path.itl",
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestNewWriteBackBatcher_Defaults verifies that NewWriteBackBatcher returns a
// non-nil batcher, HasPendingBook returns false for any ID on a fresh batcher,
// and Stop is safe to call immediately (no panic, no deadlock).
func TestNewWriteBackBatcher_Defaults(t *testing.T) {
	b := NewWriteBackBatcher(50*time.Millisecond, disabledFlushCfg(), newMockStore())
	if b == nil {
		t.Fatal("NewWriteBackBatcher returned nil")
	}

	if b.HasPendingBook("any-id") {
		t.Error("expected HasPendingBook to return false on a fresh batcher")
	}

	// Stop must not panic even when nothing has been enqueued.
	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_Enqueue_SetsPending checks that Enqueue causes
// HasPendingBook to return true for the enqueued ID.
func TestWriteBackBatcher_Enqueue_SetsPending(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), newMockStore())

	b.Enqueue("book-abc")

	if !b.HasPendingBook("book-abc") {
		t.Error("expected HasPendingBook(\"book-abc\") == true after Enqueue")
	}
	// Other IDs must not be affected.
	if b.HasPendingBook("other-book") {
		t.Error("expected HasPendingBook(\"other-book\") == false")
	}

	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_Enqueue_Idempotent verifies that enqueueing the same
// bookID twice results in exactly one entry in pendingBooks (the underlying
// map is naturally idempotent). HasPendingBook must still return true.
func TestWriteBackBatcher_Enqueue_Idempotent(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), newMockStore())

	b.Enqueue("dup-id")
	b.Enqueue("dup-id")

	if !b.HasPendingBook("dup-id") {
		t.Error("expected HasPendingBook(\"dup-id\") == true")
	}

	// White-box: inspect the map length directly (same package).
	b.mu.Lock()
	count := len(b.pendingBooks)
	b.mu.Unlock()
	if count != 1 {
		t.Errorf("expected exactly 1 pending book entry, got %d", count)
	}

	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_EnqueueAdd_SetsPending verifies that EnqueueAdd stores
// the track in pendingAdds and hasPending() reports true.
func TestWriteBackBatcher_EnqueueAdd_SetsPending(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), newMockStore())

	track := itunes.ITLNewTrack{Name: "Chapter 1", Location: "/fake/chapter1.m4b"}
	b.EnqueueAdd(track)

	b.mu.Lock()
	pending := b.hasPending()
	adds := len(b.pendingAdds)
	b.mu.Unlock()

	if !pending {
		t.Error("expected hasPending() == true after EnqueueAdd")
	}
	if adds != 1 {
		t.Errorf("expected 1 pending add, got %d", adds)
	}

	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_EnqueueRemove_SetsPending verifies that EnqueueRemove
// stores the PID (lowercased) in pendingRemoves and hasPending() returns true.
func TestWriteBackBatcher_EnqueueRemove_SetsPending(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), newMockStore())

	b.EnqueueRemove("AABBCCDD11223344")

	b.mu.Lock()
	pending := b.hasPending()
	_, found := b.pendingRemoves["aabbccdd11223344"]
	b.mu.Unlock()

	if !pending {
		t.Error("expected hasPending() == true after EnqueueRemove")
	}
	if !found {
		t.Error("expected lowercased PID in pendingRemoves")
	}

	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_UpdateConfig verifies that UpdateConfig updates the
// internal config fields without panicking, and that the helpers
// flushEnabled() / autoWriteBackEnabled() reflect the new values immediately.
func TestWriteBackBatcher_UpdateConfig(t *testing.T) {
	// Start with everything off.
	b := NewWriteBackBatcher(10*time.Second, WriteBackBatcherConfig{
		AutoWriteBack:       false,
		ITLWriteBackEnabled: false,
		LibraryWritePath:    "",
	}, newMockStore())

	if b.autoWriteBackEnabled() {
		t.Error("expected autoWriteBackEnabled == false initially")
	}
	if itlOn, path := b.flushEnabled(); itlOn || path != "" {
		t.Errorf("expected flushEnabled == (false, \"\") initially, got (%v, %q)", itlOn, path)
	}

	// Hot-reload: enable everything.
	b.UpdateConfig(WriteBackBatcherConfig{
		AutoWriteBack:       true,
		ITLWriteBackEnabled: true,
		LibraryWritePath:    "/some/path.itl",
	})

	if !b.autoWriteBackEnabled() {
		t.Error("expected autoWriteBackEnabled == true after UpdateConfig")
	}

	itlEnabled, path := b.flushEnabled()
	if !itlEnabled {
		t.Error("expected itlWriteBackEnabled == true after UpdateConfig")
	}
	if path != "/some/path.itl" {
		t.Errorf("expected libraryWritePath == /some/path.itl, got %q", path)
	}

	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_FlushSkipsWhenDisabled verifies that when
// ITLWriteBackEnabled is false or LibraryWritePath is empty, flush() attempts
// no file I/O and KEEPS the batch queued (v6.0.0: nothing is dropped; before,
// the batch was cleared and lost), without arming a timer that would spin.
func TestWriteBackBatcher_FlushSkipsWhenDisabled(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), newMockStore())

	// AutoWriteBack is true in disabledFlushCfg, so Enqueue proceeds.
	b.Enqueue("book-1")

	if !b.HasPendingBook("book-1") {
		t.Fatal("book-1 should be pending before flush")
	}

	// Call flush() directly. The store is non-nil, but flushEnabled() returns
	// (false, "") so flush keeps the batch and logs a warning.
	b.mu.Lock()
	b.stopTimerLocked() // isolate: only the flush below may act
	b.mu.Unlock()
	b.flush()

	if !b.HasPendingBook("book-1") {
		t.Error("write-back disabled: the batch must stay queued, not be dropped")
	}
	b.mu.Lock()
	armed := b.timer != nil
	b.mu.Unlock()
	if armed {
		t.Error("write-back disabled: no retry timer may be armed (it would spin)")
	}

	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_AutoWriteBack verifies autoWriteBackEnabled returns
// the correct value and that Enqueue is a no-op when AutoWriteBack is false.
func TestWriteBackBatcher_AutoWriteBack(t *testing.T) {
	// AutoWriteBack == true → autoWriteBackEnabled should be true.
	b := NewWriteBackBatcher(10*time.Second, WriteBackBatcherConfig{
		AutoWriteBack:       true,
		ITLWriteBackEnabled: true,
		LibraryWritePath:    "/fake/path.itl",
	}, newMockStore())
	if !b.autoWriteBackEnabled() {
		t.Error("expected autoWriteBackEnabled == true when AutoWriteBack=true")
	}
	_ = b.Stop(context.Background())

	// AutoWriteBack == false → autoWriteBackEnabled should be false, and
	// Enqueue / EnqueueAdd / EnqueueRemove should all be no-ops.
	bOff := NewWriteBackBatcher(10*time.Second, autoOffCfg(), newMockStore())
	if bOff.autoWriteBackEnabled() {
		t.Error("expected autoWriteBackEnabled == false when AutoWriteBack=false")
	}

	bOff.Enqueue("should-not-appear")
	if bOff.HasPendingBook("should-not-appear") {
		t.Error("Enqueue should be a no-op when autoWriteBackEnabled is false")
	}

	bOff.EnqueueAdd(itunes.ITLNewTrack{Name: "Track", Location: "/fake/track.m4b"})
	bOff.EnqueueRemove("DEADBEEF")

	bOff.mu.Lock()
	pending := bOff.hasPending()
	bOff.mu.Unlock()
	if pending {
		t.Error("expected no pending operations when AutoWriteBack=false")
	}

	_ = bOff.Stop(context.Background())
}

// TestWriteBackBatcher_Stop_CancelsTimer verifies that Stop can be called
// multiple times without panicking, and that Enqueue after Stop is a no-op
// (the stopped flag is set).
func TestWriteBackBatcher_Stop_CancelsTimer(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), newMockStore())

	// Enqueue something so the timer is armed.
	b.Enqueue("some-book")

	// First Stop.
	_ = b.Stop(context.Background())

	// Second and third Stop must not panic.
	_ = b.Stop(context.Background())
	_ = b.Stop(context.Background())

	// After Stop, further Enqueue calls must be no-ops (b.stopped == true).
	b.Enqueue("after-stop")
	if b.HasPendingBook("after-stop") {
		t.Error("Enqueue after Stop should be a no-op")
	}
}

// TestWriteBackBatcher_EnqueueWhenAutoDisabled is an explicit table-style
// check that all three enqueue methods are no-ops when AutoWriteBack=false.
func TestWriteBackBatcher_EnqueueWhenAutoDisabled(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, autoOffCfg(), newMockStore())

	b.Enqueue("book-1")
	b.EnqueueAdd(itunes.ITLNewTrack{Name: "Track", Location: "/fake/track.m4b"})
	b.EnqueueRemove("DEADBEEF")

	b.mu.Lock()
	pending := b.hasPending()
	b.mu.Unlock()

	if pending {
		t.Error("expected no pending operations when AutoWriteBack=false")
	}

	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_HasPendingBook_KeptWhenDisabled verifies that a flush
// with write-back disabled keeps every queued book (v6.0.0; it used to clear
// and lose them).
func TestWriteBackBatcher_HasPendingBook_KeptWhenDisabled(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), newMockStore())

	ids := []string{"a", "b", "c", "d"}
	for _, id := range ids {
		b.Enqueue(id)
	}
	for _, id := range ids {
		if !b.HasPendingBook(id) {
			t.Errorf("expected %q pending before flush", id)
		}
	}

	b.flush() // exits early because ITLWriteBackEnabled=false

	for _, id := range ids {
		if !b.HasPendingBook(id) {
			t.Errorf("expected %q still queued after a disabled flush", id)
		}
	}

	_ = b.Stop(context.Background())
}

// TestWriteBackBatcher_FlushNoPendingIsNoop verifies that calling flush()
// when there is nothing pending returns immediately without side effects
// (no panic, pending state remains empty, firstEnqueue stays zero).
func TestWriteBackBatcher_FlushNoPendingIsNoop(t *testing.T) {
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), newMockStore())

	// flush() with nothing pending should exit immediately at hasPending() check.
	b.flush()

	// Pending state must remain empty.
	b.mu.Lock()
	pending := b.hasPending()
	zeroed := b.firstEnqueue.IsZero()
	b.mu.Unlock()

	if pending {
		t.Error("expected no pending operations after no-op flush")
	}
	if !zeroed {
		t.Error("expected firstEnqueue to remain zero after no-op flush")
	}

	_ = b.Stop(context.Background())
}
