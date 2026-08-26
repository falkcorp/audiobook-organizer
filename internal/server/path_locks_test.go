// file: internal/server/path_locks_test.go
// version: 1.0.0
// guid: 4c81b7d2-3e60-49aa-b5f1-8d7c6a204e39
// last-edited: 2026-08-15

package server

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestPathLocksExcludesSamePath is the instrument check for the write-back path
// lock: it must be RED if lock() is a no-op. `inside` counts concurrent holders,
// and `maxInside` records the peak; with a working lock the peak is 1, and with
// lock() stubbed to `return func(){}` the peak goes above 1 essentially every
// run at this level of contention.
func TestPathLocksExcludesSamePath(t *testing.T) {
	pl := newPathLocks()

	var inside, maxInside atomic.Int64
	var wg sync.WaitGroup

	const goroutines, iterations = 16, 50
	for range goroutines {
		wg.Go(func() {
			for range iterations {
				release := pl.lock("/library/Author/Book/book.m4b")
				n := inside.Add(1)
				for {
					m := maxInside.Load()
					if n <= m || maxInside.CompareAndSwap(m, n) {
						break
					}
				}
				inside.Add(-1)
				release()
			}
		})
	}
	wg.Wait()

	if got := maxInside.Load(); got != 1 {
		t.Fatalf("concurrent holders of one path: got peak %d, want 1", got)
	}
}

// TestPathLocksDistinctPathsDoNotBlock proves the lock is KEYED and not a single
// global mutex. Every goroutine takes a different path and they must all be able
// to hold their locks at the same time; a global lock would deadlock this on the
// barrier rather than merely slowing it down.
func TestPathLocksDistinctPathsDoNotBlock(t *testing.T) {
	pl := newPathLocks()

	const n = 8
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	var arrived atomic.Int64

	for i := range n {
		wg.Go(func() {
			release := pl.lock(filepath.Join("/library", "book", string(rune('a'+i))+".m4b"))
			defer release()
			// Every goroutine must reach here while all the others still hold
			// their own locks; the last one to arrive opens the barrier.
			if arrived.Add(1) == n {
				close(barrier)
			}
			<-barrier
		})
	}
	wg.Wait() // hangs (and the test times out) if the lock is not per-path
}

// TestPathLocksReleaseDrainsMap guards the refcount bookkeeping: a whole-library
// run takes and releases tens of thousands of paths, so an entry that is never
// deleted is an unbounded leak.
func TestPathLocksReleaseDrainsMap(t *testing.T) {
	pl := newPathLocks()

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			release := pl.lock(filepath.Join("/library", "b", string(rune('a'+i%26))+".m4b"))
			release()
			release() // double release must be a no-op, not a negative refcount
		})
	}
	wg.Wait()

	pl.mu.Lock()
	remaining := len(pl.m)
	pl.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("path lock map not drained: %d entries remain", remaining)
	}
}

// TestPathLocksEmptyPathIsNoOp covers the guard that keeps a book with no
// FilePath from serializing every such book behind one shared "" key.
func TestPathLocksEmptyPathIsNoOp(t *testing.T) {
	pl := newPathLocks()

	first := pl.lock("")
	second := pl.lock("") // would deadlock if "" took a real lock
	first()
	second()

	pl.mu.Lock()
	remaining := len(pl.m)
	pl.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("empty path created %d lock entries, want 0", remaining)
	}
}

// TestNormalizePathKeyCleans covers the reason lock() cleans at all: two spellings
// of one file must take the SAME lock, or the lock silently protects nothing.
func TestNormalizePathKeyCleans(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/a/b/x.m4b", "/a/b/x.m4b"},
		{"/a/./b/x.m4b", "/a/b/x.m4b"},
		{"/a/b//x.m4b", "/a/b/x.m4b"},
		{"/a/c/../b/x.m4b", "/a/b/x.m4b"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizePathKey(tc.in); got != tc.want {
			t.Errorf("normalizePathKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
