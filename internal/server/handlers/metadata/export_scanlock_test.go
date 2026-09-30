// file: internal/server/handlers/metadata/export_scanlock_test.go
// version: 1.1.0
// guid: ac7f623d-69e8-43c0-adce-9afb215338e6
// last-edited: 2026-09-30

package metadatahandler

import "time"

// SetRequestBookLockWaitForTest shortens the request's wait for a book the
// scanner holds and returns the restore func.
func SetRequestBookLockWaitForTest(d time.Duration) func() {
	old := requestBookLockWait
	requestBookLockWait = d
	return func() { requestBookLockWait = old }
}

// SetQueuedWaitBeatForTest shortens a queued apply's wait beat and returns
// the restore func.
func SetQueuedWaitBeatForTest(d time.Duration) func() {
	old := queuedWaitBeat
	queuedWaitBeat = d
	return func() { queuedWaitBeat = old }
}
