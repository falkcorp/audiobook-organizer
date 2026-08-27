// file: internal/server/path_locks.go
// version: 1.1.0
// guid: 6e2a9c14-7d3b-4f58-9a01-2c8b4d5e6f70
// last-edited: 2026-08-27

package server

import (
	"path/filepath"
	"sync"
)

// pathLocks is a keyed mutex over filesystem paths. It exists so the write-back
// worker pools (runBulkWriteBack, metadata.batch-save) can run N books in
// parallel WITHOUT ever letting two workers write the same file concurrently.
//
// Why per-path and not one global lock: a global lock would serialize the whole
// pool and defeat the parallelism entirely. Why a lock at all — three real
// hazards, all of which are "different book IDs, same path on disk":
//
//  1. Version-group siblings. Two book rows in the same version group can
//     resolve to the same underlying file(s), so writing both at once means two
//     TagLib writers on one file.
//  2. Protected-path redirect. internal/metafetch's WriteBackMetadataForBook
//     (service_writeback.go:681-691) redirects a book in a protected path to its
//     library copy, so two DISTINCT book IDs can collapse onto one destination.
//  3. Backup-filename collision. The copy-on-write backup name is
//     filePath + ".bak-" + time.Now().Format("20060102-150405")
//     (internal/metafetch/service_files.go:65) — ONE-SECOND granularity. Two
//     writers touching the same file inside the same wall-clock second generate
//     the identical backup name and clobber each other's backup.
//
// Entries are refcounted and deleted on the last release, so a 60k-book run
// does not leave 60k map entries behind.
type pathLocks struct {
	mu sync.Mutex
	m  map[string]*pathLockEntry
}

// pathLockEntry is one path's lock plus the number of goroutines currently
// holding or waiting on it. refs is guarded by the parent pathLocks.mu, never
// by ch.
type pathLockEntry struct {
	ch   chan struct{}
	refs int
}

// newPathLocks returns an empty keyed-mutex table.
func newPathLocks() *pathLocks {
	return &pathLocks{m: make(map[string]*pathLockEntry)}
}

// writeBackPathLocks is the process-wide table shared by every write-back path.
// It is package-level rather than a *Server field on purpose: the guarantee
// needed is "no two goroutines in this process write one file at once", and two
// concurrently-running ops (a bulk write-back and a batch-save, say) must share
// one table or the lock is worthless across them. There is one Server per
// process, so this is not a scoping regression.
var writeBackPathLocks = newPathLocks()

// writeBackFileGate is the process-wide resource bound shared by every
// TagLib/disk write path. Unlike writeBackPathLocks, it controls aggregate
// filesystem pressure rather than just same-path races.
var writeBackFileGate = newFileWriteGate(maxWriteBackWorkers)

// normalizePathKey canonicalizes a path for use as a lock key. Cleaning matters:
// "/a/b/x.m4b" and "/a/./b/x.m4b" are the same file and must take the same lock.
// An empty path yields an empty key, which lock() treats as "nothing to guard".
func normalizePathKey(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// lock acquires the lock for path and returns the matching unlock func. The
// returned func is always non-nil and is safe to defer immediately, including
// when path is empty (in which case it is a no-op — there is no file to guard).
//
// Callers MUST compute the key from the path as it stands at write time. In
// runBulkWriteBack the rename pass runs first and can move the file, so the book
// is re-read after the rename before this is called; locking a pre-rename path
// protects nothing.
func (pl *pathLocks) lock(path string) func() {
	key := normalizePathKey(path)
	if key == "" {
		return func() {}
	}

	pl.mu.Lock()
	e, ok := pl.m[key]
	if !ok {
		e = &pathLockEntry{ch: make(chan struct{}, 1)}
		pl.m[key] = e
	}
	e.refs++
	pl.mu.Unlock()

	e.ch <- struct{}{} // blocks while another worker holds this path

	var once sync.Once
	return func() {
		once.Do(func() {
			<-e.ch
			pl.mu.Lock()
			e.refs--
			if e.refs == 0 {
				delete(pl.m, key)
			}
			pl.mu.Unlock()
		})
	}
}
