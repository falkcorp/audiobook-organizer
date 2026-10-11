// file: internal/server/handlers/system/export_test.go
// version: 1.0.0
// guid: b4f7ca11-6cd9-4a62-8f99-234a5b638214
// last-edited: 2026-10-11

package system

// SetAfterOLCloseHook installs f to run in deleteOLDataLocked between the
// Open Library store's close and the removal of its data directory, and
// returns a function that removes it. For tests of the lock the reset holds
// across that window.
func SetAfterOLCloseHook(f func()) (restore func()) {
	afterOLCloseHook.Store(&f)
	return func() { afterOLCloseHook.Store(nil) }
}
