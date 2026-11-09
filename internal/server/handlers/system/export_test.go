// file: internal/server/handlers/system/export_test.go
// version: 1.0.2
// guid: b4f7ca11-6cd9-4a62-8f99-234a5b638214
// last-edited: 2026-11-09

package system

// SetBeforeRestoreExtractHook installs f to run in RestoreBackup just before
// the extraction's first write, and returns a function that removes it. For
// tests of what the restore has done by then.
func SetBeforeRestoreExtractHook(f func()) (restore func()) {
	beforeRestoreExtractHook.Store(&f)
	return func() { beforeRestoreExtractHook.Store(nil) }
}
