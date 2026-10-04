// file: internal/metabatch/import_roots.go
// version: 1.0.0
// guid: 3c8e51a2-7d94-4b0f-a6e3-91f2c4d7b805
// last-edited: 2026-10-04
//
// The import-root set the search-title resolver consults (titleJudge.isRootDir):
// a folder that is a registered import path is never listed for sibling rows,
// because its other rows are other books and listing it reads a whole import
// tree.

package metabatch

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// importRootsTTL bounds how stale a cached import-root list may get over a
// long pass (a fetch op can run for hours): it is re-read at most once per
// window.
const importRootsTTL = time.Minute

// importRootsLoadWait bounds how long a caller with NO list yet waits for
// another caller's in-flight read. The read is one store call, normally
// milliseconds; a stuck store degrades the waiter to "not a root" (the old
// global's behaviour) instead of parking every worker of a pass behind it.
var importRootsLoadWait = 2 * time.Second

// ImportPathReader is the one read the resolver needs for import roots.
//
// The roots come from the SAME store the caller hands the resolver, never
// from a process-wide registration. Until 2026-10-04 they came from a
// package global the server set in NewServer (SetImportRootsSource); the
// global outlived the store it closed over, so a later caller resolved
// through a closed Pebble store and panicked "pebble: closed" (the
// TestApplyCachedCandidate_GateRefuses flake, #3718).
type ImportPathReader interface {
	GetAllImportPaths() ([]database.ImportPath, error)
}

// importRootLog rate-limits the Warn for an unreadable import-path list.
var importRootLog warnLimiter

// readImportRoots loads the cleaned import-path set from r. ok is false when
// the read failed; the failure is logged here at Warn (rate-limited).
func readImportRoots(r ImportPathReader) (roots map[string]bool, paths []database.ImportPath, ok bool) {
	if r == nil {
		return nil, nil, true
	}
	paths, err := r.GetAllImportPaths()
	if err != nil {
		importRootLog.warn("import paths unreadable; keeping the previous root list (none on a first read): err=%s suppressed_since_last=%d",
			logger.SanitizeLogValue(err.Error()))
		return nil, nil, false
	}
	return cleanRoots(paths), paths, true
}

func cleanRoots(paths []database.ImportPath) map[string]bool {
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		if c := strings.TrimSpace(p.Path); c != "" {
			out[filepath.Clean(c)] = true
		}
	}
	return out
}

// ImportRootsCache is a store-bound, TTL-bounded import-root list shared by
// every row of one pass or one apply call. It is itself an ImportPathReader,
// so a caller can put it in front of a store (server.withCachedImportPaths)
// and every resolver call through that reader shares one read.
//
//   - One read at a time (single-flight), made OUTSIDE the lock: a slow store
//     never holds the mutex.
//   - A caller with no list yet waits for the in-flight read, bounded by
//     importRootsLoadWait; a caller that already has a list uses it while a
//     refresh runs.
//   - The TTL window starts only on a SUCCESSFUL read. A failed read keeps the
//     previous list and the next caller tries again; a read that panics
//     releases its waiters and consumes nothing.
//
// Safe for concurrent use.
type ImportRootsCache struct {
	r ImportPathReader

	mu       sync.Mutex
	roots    map[string]bool
	paths    []database.ImportPath
	loaded   bool      // a read has succeeded at least once
	at       time.Time // when the last successful read finished
	inflight chan struct{}

	// waiting counts callers parked on an in-flight first read (tests).
	waiting atomic.Int32
}

// NewImportRootsCache binds a cache to r.
func NewImportRootsCache(r ImportPathReader) *ImportRootsCache {
	return &ImportRootsCache{r: r}
}

// GetAllImportPaths returns the cached import paths, reading r when the
// window has passed. On a cold failure (no successful read yet) it returns an
// error, so a caller that treats an unreadable list as "no roots" says so.
func (c *ImportRootsCache) GetAllImportPaths() ([]database.ImportPath, error) {
	_, paths, ok := c.get()
	if !ok {
		return nil, errImportRootsUnavailable
	}
	return paths, nil
}

// errImportRootsUnavailable: no successful read yet (the read failed, or
// another caller's read outlasted importRootsLoadWait).
var errImportRootsUnavailable = importRootsError("import paths unavailable: no successful read yet")

type importRootsError string

func (e importRootsError) Error() string { return string(e) }

// set returns the cleaned import-root set ("" roots on a cold failure).
func (c *ImportRootsCache) set() map[string]bool {
	roots, _, _ := c.get()
	return roots
}

func (c *ImportRootsCache) get() (roots map[string]bool, paths []database.ImportPath, ok bool) {
	if c == nil {
		return nil, nil, true
	}
	c.mu.Lock()
	if c.loaded && time.Since(c.at) < importRootsTTL {
		defer c.mu.Unlock()
		return c.roots, c.paths, true
	}
	if ch := c.inflight; ch != nil {
		if c.loaded {
			defer c.mu.Unlock()
			return c.roots, c.paths, true // the stale list while the refresh runs
		}
		c.mu.Unlock()
		c.waiting.Add(1)
		select {
		case <-ch:
		case <-time.After(importRootsLoadWait):
		}
		c.waiting.Add(-1)
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.roots, c.paths, c.loaded
	}
	ch := make(chan struct{})
	c.inflight = ch
	c.mu.Unlock()

	var fresh map[string]bool
	var freshPaths []database.ImportPath
	succeeded := false
	defer func() {
		// Runs on success, failure and panic alike: waiters are released and
		// the next caller can read again. Only a success starts the window;
		// otherwise the previous list (if any) is what every caller gets.
		c.mu.Lock()
		if succeeded {
			c.roots, c.paths, c.loaded, c.at = fresh, freshPaths, true, time.Now()
		}
		roots, paths, ok = c.roots, c.paths, c.loaded
		c.inflight = nil
		c.mu.Unlock()
		close(ch)
	}()
	fresh, freshPaths, succeeded = readImportRoots(c.r)
	return nil, nil, false // replaced by the deferred block
}
