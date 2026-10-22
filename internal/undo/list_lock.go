// file: internal/undo/list_lock.go
// version: 1.0.0
// guid: 5c1e7a94-3b60-4d2f-9a85-e0f4b7c23d16
// last-edited: 2026-10-22

package undo

import (
	"hash/fnv"
	"sync"
)

// listLocks serialise everything that reads a survivor's journal to decide a
// list write and then makes it: the carry of a retire into a collection or
// playlist (journal the marker, write the list) and the revert of one of those
// rows (read the journals, decide, write the list, stamp the marker). Each
// decides from the markers already journaled and writes the list the markers
// describe, so two of them interleaving on one list can each act on a journal
// the other is about to change: two reverts both see the other's marker still
// standing and both leave the survivor listed, or a revert reads the journal
// between an apply's marker and its write.
//
// The lock is keyed by the list (kind and id), not by the survivor: the
// revert's order graph reads the journals of every survivor that ever shared
// the list, so what must hold still is the list. A fixed set of mutexes: two
// lists sharing one only wait for each other.
//
// Lock order, outermost first: the operation's own lock and the merge lock (the
// caller's), then LockList, then the list store's own lock (the collection or
// playlist write). Nothing under LockList takes an operation or merge lock, and
// a holder never takes a second LockList.
var listLocks [64]sync.Mutex

// LockList takes the lock of the list (kind is UserContentCollection or
// UserContentPlaylist, id the list's id) and returns the function that
// releases it.
func LockList(kind, listID string) (unlock func()) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(listID))
	mu := &listLocks[h.Sum32()%uint32(len(listLocks))]
	mu.Lock()
	return mu.Unlock
}
