// file: internal/server/handlers/organize_scanlock_export_test.go
// version: 1.0.0
// guid: 6d1f3b82-9e47-4c05-a8b3-e2c07f5a9d14
// last-edited: 2026-09-30

package handlers

import "time"

// SetOrganizeBookLockWaitForTest shortens the organize request's wait for a
// book the scanner holds and returns the restore func.
func SetOrganizeBookLockWaitForTest(d time.Duration) func() {
	old := organizeBookLockWait
	organizeBookLockWait = d
	return func() { organizeBookLockWait = old }
}
