// file: internal/scanner/hooks.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-08-18

package scanner

import "sync"

// ScanHooks provides optional callbacks for scan-time side effects.
// All methods must be safe for concurrent use. A nil ScanHooks value
// means no hooks fire — callers must nil-check before calling.
type ScanHooks interface {
	OnBookScanned(bookID, title string)
	OnImportDedup(bookID string)
}

// scanHooks is written by SetScanHooks and read from scan worker
// goroutines, so it needs a lock rather than being a bare global.
//
// The race is real and was observed in CI (TestAddImportPathFallbackScan,
// 2026-08-18): a test's deferred cleanup calls SetScanHooks(nil) while a
// scan started by an ASYNC operation is still running and reaching
// saveBookToDatabase. The two goroutines outlive each other by design --
// the operations registry runs the scan in the background -- so the write
// and the read genuinely overlap. An earlier fix addressed the panic this
// produced ("pebble: closed") by clearing hooks before closing the store;
// that ordering fix left the unsynchronised access itself in place.
var (
	scanHooksMu sync.RWMutex
	scanHooks   ScanHooks
)

// SetScanHooks installs (or clears) the hook implementation used by
// the scanner's save-to-database path. Pass nil to disable hooks.
func SetScanHooks(hooks ScanHooks) {
	scanHooksMu.Lock()
	defer scanHooksMu.Unlock()
	scanHooks = hooks
}

// currentScanHooks returns the installed hooks, or nil if none are set.
//
// Callers must use the returned value rather than reading the global again:
// a nil check followed by separate reads can see hooks disappear between
// the check and the call, which is exactly what teardown does.
func currentScanHooks() ScanHooks {
	scanHooksMu.RLock()
	defer scanHooksMu.RUnlock()
	return scanHooks
}
