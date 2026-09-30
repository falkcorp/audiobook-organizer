// file: internal/server/file_io_pool_scanlock_test.go
// version: 1.0.0
// guid: 101aeb15-8f4a-4d7a-8573-0a0671cde076
// last-edited: 2026-09-30

package server

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
)

// A queued file job makes its book busy for the scanner until the job has
// run: the scanner must not read the old tags between the apply's database
// write and this job's tag write. Another apply of the book is not blocked.
func TestFileIOPool_SubmitMarksTheBookPendingForTheScanner(t *testing.T) {
	p := NewFileIOPool(1)
	defer p.Stop()

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	if !p.Submit("pool-book-1", func() {
		close(started)
		<-release
	}) {
		t.Fatal("job dropped")
	}
	<-started
	if _, ok := scanlock.Books.TryLockSetIdle([]string{"pool-book-1"}); ok {
		t.Fatal("the scanner could lock a book whose file job had not finished")
	}
	if h, ok := scanlock.Books.TryLockSet([]string{"pool-book-1"}); !ok {
		t.Fatal("another apply was blocked by the pending file job")
	} else {
		h.Release()
	}
	close(release)
	go func() {
		for {
			if h, ok := scanlock.Books.TryLockSetIdle([]string{"pool-book-1"}); ok {
				h.Release()
				close(finished)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the pending mark outlived the file job")
	}
}

// A job dropped because the pool is stopped clears its pending mark: the
// scanner must not treat the book as busy forever.
func TestFileIOPool_DroppedJobClearsPending(t *testing.T) {
	p := NewFileIOPool(1)
	p.Stop()
	if p.Submit("pool-book-2", func() {}) {
		t.Fatal("a stopped pool accepted a job")
	}
	h, ok := scanlock.Books.TryLockSetIdle([]string{"pool-book-2"})
	if !ok {
		t.Fatal("a dropped job left the book pending")
	}
	h.Release()
}
