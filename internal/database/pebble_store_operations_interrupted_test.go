// file: internal/database/pebble_store_operations_interrupted_test.go
// version: 1.0.0
// guid: 7f2a4c81-93de-4b06-a15f-8c3d206e4b77
// last-edited: 2026-08-17

package database

import "testing"

// The startup sweep (server_lifecycle.go resumeInterruptedOperations) can only
// resume what GetInterruptedOperations returns. It used to enumerate
// running/queued/interrupted, while the registry mints a whole interrupted_*
// family — so a library.scan killed by a deploy sat at interrupted_quiesced,
// was invisible to the sweep, and never came back.
//
// This pins the PREFIX rule rather than a list, so a future seventh variant is
// picked up without anyone editing this test.
func TestInterruptedStatusMatching(t *testing.T) {
	resumable := []string{
		"running",
		"queued",
		"interrupted",
		"interrupted_quiesced",
		"interrupted_dropped",
		"interrupted_restart",
		"interrupted_ask",
		"interrupted_something_invented_later",
	}
	for _, st := range resumable {
		if !isResumableOpStatus(st) {
			t.Errorf("status %q must be picked up by the resume sweep", st)
		}
	}

	terminal := []string{"completed", "failed", "canceled", "cancelled", "pending", ""}
	for _, st := range terminal {
		if isResumableOpStatus(st) {
			t.Errorf("status %q must NOT be treated as resumable", st)
		}
	}
}
