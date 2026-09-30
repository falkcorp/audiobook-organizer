// file: internal/server/handlers/organize_scanlock_export_test.go
// version: 1.1.0
// guid: 6d1f3b82-9e47-4c05-a8b3-e2c07f5a9d14
// last-edited: 2026-09-30

package handlers

import (
	"errors"
	"time"
)

// SetOrganizeBookLockWaitForTest shortens the organize request's wait for a
// book the scanner holds and returns the restore func.
func SetOrganizeBookLockWaitForTest(d time.Duration) func() {
	old := organizeBookLockWait
	organizeBookLockWait = d
	return func() { organizeBookLockWait = old }
}

// SetOrganizeUnsettledRetryForTest shortens the queued organize's retries of a
// lock set that will not settle.
func SetOrganizeUnsettledRetryForTest(retries int, delay time.Duration) func() {
	oldN, oldD := organizeUnsettledRetries, organizeUnsettledDelay
	organizeUnsettledRetries, organizeUnsettledDelay = retries, delay
	return func() { organizeUnsettledRetries, organizeUnsettledDelay = oldN, oldD }
}

// IsOrganizeSetUnsettled reports whether err is the lock-set-never-settled
// refusal.
func IsOrganizeSetUnsettled(err error) bool { return errors.Is(err, errOrganizeSetUnsettled) }
