// file: internal/plugins/itunes/stub.go
// version: 1.0.0
// guid: 3d81f0a7-6c94-4e2b-b5d3-9f7a1c60e482
// last-edited: 2026-08-16

package itunes

import "fmt"

// errNotImplemented is what an unfinished op Run must return.
//
// Returning nil from a stub is not a placeholder, it is a false report: the
// registry marks the op "completed", the UI shows a green row, and the work
// silently did not happen. Three iTunes ops did exactly that between
// 2026-07-17 and 2026-08-16, and the only reason it was ever noticed is that
// they also collided with a real registration and logged a WARN. A stub with
// no collision -- itunes.position-sync -- produced no evidence at all.
//
// The 2026-07-17 response to the same symptom was to remove the cron schedules
// from itunes.sync and itunes.position-sync so they would stop burning green
// no-op rows every 10 and 30 minutes. That stopped the rows; it left the lie
// intact for anyone who triggered the op by hand.
//
// An op that cannot do its work must fail. Failing is recoverable -- someone
// sees it and fixes it. Succeeding falsely is not.
func errNotImplemented(defID string) error {
	return fmt.Errorf("%s: operation is not implemented — refusing to report success for work that did not happen", defID)
}
