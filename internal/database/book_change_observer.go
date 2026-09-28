// file: internal/database/book_change_observer.go
// version: 1.0.0
// guid: 6a0e4c29-7f13-4b58-9d26-1c8b5e3f7a40
// last-edited: 2026-09-27

package database

import "sync/atomic"

// BookChangeKind names what happened to a book row.
type BookChangeKind string

const (
	BookChangeCreated BookChangeKind = "created"
	BookChangeUpdated BookChangeKind = "updated"
	BookChangeDeleted BookChangeKind = "deleted"
)

// BookChangeObserver is told about every committed book row write.
//
// It is called from the SAME three points that bump the library generation
// (createBook, updateBookLockedMode, DeleteBook in pebble_store.go) — the
// single funnel every book write goes through, which is why the list caches
// already key on it. It must be cheap and non-blocking: it runs on the
// writer's goroutine, inside the book's write path. The server installs a
// coalescer (realtime.BookChangeCoalescer) that only records the id.
type BookChangeObserver func(kind BookChangeKind, bookID string)

var bookChangeObserver atomic.Pointer[BookChangeObserver]

// SetBookChangeObserver installs (or, with nil, removes) the process-wide
// book change observer.
func SetBookChangeObserver(fn BookChangeObserver) {
	if fn == nil {
		bookChangeObserver.Store(nil)
		return
	}
	bookChangeObserver.Store(&fn)
}

// notifyBookChanged reports a committed book write to the observer, if any.
func notifyBookChanged(kind BookChangeKind, bookID string) {
	if bookID == "" {
		return
	}
	if fn := bookChangeObserver.Load(); fn != nil {
		(*fn)(kind, bookID)
	}
}
