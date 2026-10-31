// file: internal/database/memdb_warmup_hook.go
// version: 1.0.0
// guid: 91d0c2d8-c547-4e46-a204-632819f23e0d
// last-edited: 2026-10-31

package database

import "sync"

// warmupStartHook, when non-nil, runs at the start of the async memdb warmup
// goroutine, before it scans Pebble. It exists so tests can hold the store in
// the "memdb is not serving" window deterministically. Production never sets
// it. Guarded by warmupStartHookMu because store constructors in parallel tests
// read it while another test installs it.
var (
	warmupStartHookMu sync.RWMutex
	warmupStartHook   func()
)

func runWarmupStartHook() {
	warmupStartHookMu.RLock()
	h := warmupStartHook
	warmupStartHookMu.RUnlock()
	if h != nil {
		h()
	}
}

// SetWarmupStartHookForTest installs h to run at the start of every memdb
// warmup goroutine started from now on, and returns a func that restores the
// previous hook. A slow h models a CI agent so loaded that a test body runs
// before warmup has published: the state in which every fail-closed lookup
// (BookFilesAtPath, the retire and combine ownership checks, BookFilesWithHash)
// refuses with "memdb is not serving". The hook is process-global, so a test
// that installs one must not run in parallel with tests that open stores.
func SetWarmupStartHookForTest(h func()) (restore func()) {
	warmupStartHookMu.Lock()
	prev := warmupStartHook
	warmupStartHook = h
	warmupStartHookMu.Unlock()
	return func() {
		warmupStartHookMu.Lock()
		warmupStartHook = prev
		warmupStartHookMu.Unlock()
	}
}
