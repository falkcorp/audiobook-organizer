// file: internal/itunes/service/writeback_batcher_stop_test.go
// version: 1.0.0
// guid: 9e3ff3be-ea09-4128-9948-ff447812da3d
// last-edited: 2026-09-10
//
// Regression tests for WriteBackBatcher shutdown and single-writer
// discipline (TODO.md "writeback_batcher.Stop() waits for nothing").
//
// Pre-fix, Stop() set b.stopped, stopped the debounce timer and called
// flush() once — it joined nothing. The three goroutines the batcher
// spawns (the EnqueueRemove tombstone marker, the resetTimer fast-path
// `go b.flush()`, and the time.AfterFunc debounce callback) could all
// still be running, mid-ITL-write, after Stop returned. flush() itself
// never re-checked b.stopped, and because b.mu is released before
// SafeWriteITL two flushes could read-modify-write the live library
// through the same .tmp at the same time.
//
// These tests use the package's existing fake ITL hooks and a t.TempDir
// library file. They never touch a real iTunes library.

package itunesservice

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// withStubPinLKG neutralizes the last-known-good pin hook so the tests below
// exercise the flush/write path without touching PinLastKnownGood's real file
// handling on a placeholder library.
func withStubPinLKG(t *testing.T) {
	t.Helper()
	prev := itlPinLKGFn
	itlPinLKGFn = func(string) error { return nil }
	t.Cleanup(func() { itlPinLKGFn = prev })
}

// writebackTestStore returns a MockStore wired for a single book that owns one
// iTunes persistent ID and no book files, which is the shortest path through
// flush() to a non-empty ITLOperationSet.
func writebackTestStore(bookID, pid string) *database.MockStore {
	pidCopy := pid
	return &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id != bookID {
				return &database.Book{ID: id, Title: "other-" + id, ITunesPersistentID: &pidCopy}, nil
			}
			return &database.Book{ID: bookID, Title: "Test Book", ITunesPersistentID: &pidCopy}, nil
		},
		GetBookFilesFunc:     func(string) ([]database.BookFile, error) { return nil, nil },
		MarkITunesSyncedFunc: func([]string) (int64, error) { return 1, nil },
	}
}

// enabledFlushCfg is a config whose flush path is fully armed against the
// given placeholder ITL path.
func enabledFlushCfg(itlPath string) WriteBackBatcherConfig {
	return WriteBackBatcherConfig{
		AutoWriteBack:       true,
		ITLWriteBackEnabled: true,
		LibraryWritePath:    itlPath,
	}
}

// TestStop_JoinsTombstoneGoroutine proves Stop waits for the EnqueueRemove
// external-id tombstone goroutine (writeback_batcher.go's `go func()` inside
// EnqueueRemove) instead of returning out from under it.
//
// Pre-fix this fails: Stop returns immediately and the MarkExternalIDRemoved
// call is still sleeping, so `done` is still false.
func TestStop_JoinsTombstoneGoroutine(t *testing.T) {
	var done atomic.Bool
	store := &database.MockStore{
		MarkExternalIDRemovedFunc: func(_, _ string) error {
			time.Sleep(150 * time.Millisecond)
			done.Store(true)
			return nil
		},
	}

	// ITL write-back stays disabled: this test is about the goroutine join,
	// not about the write path, so flush must not touch any file.
	b := NewWriteBackBatcher(10*time.Second, disabledFlushCfg(), store)
	b.EnqueueRemove("AABBCCDD11223344")

	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !done.Load() {
		t.Error("Stop returned while the EnqueueRemove tombstone goroutine was still running; Stop must join every goroutine the batcher started")
	}
}

// TestStop_JoinsInFlightTimerFlush proves Stop waits for a debounce-timer
// flush that is already inside the ITL writer.
//
// Pre-fix this fails: Stop stops the (already-fired) timer and returns while
// the flush goroutine is still mid-SafeWriteITL, so `finished` is false.
func TestStop_JoinsInFlightTimerFlush(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "original-content")

	var enterOnce sync.Once
	entered := make(chan struct{})
	var finished atomic.Bool

	withFakeITLHooks(t,
		func(string) error { return nil },
		func(in, out string, _ itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			enterOnce.Do(func() { close(entered) })
			// Hold the writer open long enough that a Stop which joins
			// nothing is observable.
			time.Sleep(150 * time.Millisecond)
			data, _ := os.ReadFile(in)
			if err := os.WriteFile(out, append(data, '!'), 0o644); err != nil {
				return nil, err
			}
			finished.Store(true)
			return &itunes.ITLWriteBackResult{UpdatedCount: 1, OutputPath: out}, nil
		},
	)
	withFakeParseITLHook(t, &itunes.ITLLibrary{}, nil)
	withStubPinLKG(t)

	b := NewWriteBackBatcher(15*time.Millisecond, enabledFlushCfg(itlPath), writebackTestStore("book-1", "aabbccdd11223344"))
	b.Enqueue("book-1")

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("debounce timer flush never reached the ITL writer")
	}

	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !finished.Load() {
		t.Error("Stop returned while a flush was still inside SafeWriteITL; Stop must join the debounce-timer flush goroutine")
	}
}

// TestFlush_SingleWriterAcrossConcurrentFlushes proves two concurrent flushes
// never overlap inside the ITL writer.
//
// Pre-fix this fails: b.mu is released before SafeWriteITL, so both flushes
// enter ApplyITLOperations at once (maxActive == 2) and read-modify-write the
// same library through the same .tmp.
func TestFlush_SingleWriterAcrossConcurrentFlushes(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "library.itl", "original-content")

	var active, maxActive, entries atomic.Int32
	reachedA := make(chan struct{})
	reachedB := make(chan struct{})
	releaseA := make(chan struct{})

	withFakeITLHooks(t,
		func(string) error { return nil },
		func(in, out string, _ itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			n := active.Add(1)
			for {
				m := maxActive.Load()
				if n <= m || maxActive.CompareAndSwap(m, n) {
					break
				}
			}
			switch entries.Add(1) {
			case 1:
				close(reachedA)
				<-releaseA
			case 2:
				close(reachedB)
			}
			data, _ := os.ReadFile(in)
			if err := os.WriteFile(out, append(data, '!'), 0o644); err != nil {
				active.Add(-1)
				return nil, err
			}
			active.Add(-1)
			return &itunes.ITLWriteBackResult{UpdatedCount: 1, OutputPath: out}, nil
		},
	)
	withFakeParseITLHook(t, &itunes.ITLLibrary{}, nil)
	withStubPinLKG(t)

	// A long debounce keeps the timer from firing a third flush of its own.
	b := NewWriteBackBatcher(10*time.Second, enabledFlushCfg(itlPath), writebackTestStore("book-a", "aabbccdd11223344"))

	var wg sync.WaitGroup
	b.Enqueue("book-a")
	wg.Add(1)
	go func() { defer wg.Done(); b.flush() }()

	select {
	case <-reachedA:
	case <-time.After(5 * time.Second):
		t.Fatal("first flush never reached the ITL writer")
	}

	// Queue a second batch and drive a second concurrent flush.
	b.Enqueue("book-b")
	wg.Add(1)
	go func() { defer wg.Done(); b.flush() }()

	overlapped := false
	select {
	case <-reachedB:
		overlapped = true
	case <-time.After(400 * time.Millisecond):
	}

	close(releaseA)
	wg.Wait()
	_ = b.Stop(context.Background())

	if overlapped {
		t.Error("second flush entered the ITL writer while the first was still inside it; SafeWriteITL must have a single writer")
	}
	if got := maxActive.Load(); got != 1 {
		t.Errorf("concurrent writers inside SafeWriteITL: maxActive = %d, want 1", got)
	}
}

// TestStop_DoesNotRearmAfterFailedDrain proves the final drain cannot schedule
// more work after Stop. A failing ParseITL sends flush down its reEnqueue
// path, which re-arms the debounce timer; with maxDelay left at zero that
// re-arm takes resetTimer's `go b.flush()` fast path, so pre-fix Stop spawns
// flushes that run after it returned (three parse attempts, up to
// maxFlushFailures). Post-fix resetTimer refuses to arm anything once stopped,
// so the drain parses exactly once.
func TestStop_DoesNotRearmAfterFailedDrain(t *testing.T) {
	var parseCalls atomic.Int32
	prevParse := parseITLFn
	parseITLFn = func(string) (*itunes.ITLLibrary, error) {
		parseCalls.Add(1)
		return nil, errors.New("synthetic parse failure")
	}
	t.Cleanup(func() { parseITLFn = prevParse })

	// Struct literal: maxDelay stays zero so resetTimer takes its
	// "accumulated past maxDelay" fast path (`go b.flush()`).
	b := &WriteBackBatcher{
		pendingBooks:        map[string]bool{"book-1": true},
		pendingRemoves:      map[string]bool{},
		delay:               10 * time.Second,
		autoWriteBack:       true,
		itlWriteBackEnabled: true,
		libraryWritePath:    "/nonexistent/library.itl",
		store:               writebackTestStore("book-1", "aabbccdd11223344"),
	}

	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Give any goroutine Stop failed to prevent a chance to run.
	time.Sleep(200 * time.Millisecond)

	if got := parseCalls.Load(); got != 1 {
		t.Errorf("flushes ran after Stop returned: ParseITL called %d times, want 1", got)
	}
	b.mu.Lock()
	rearmed := b.timer != nil
	b.mu.Unlock()
	if rearmed {
		t.Error("Stop left a debounce timer armed; a flush would fire after shutdown")
	}
}
