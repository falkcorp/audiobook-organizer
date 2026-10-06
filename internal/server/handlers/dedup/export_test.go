// file: internal/server/handlers/dedup/export_test.go
// version: 1.0.0
// guid: 028a211d-147e-4bdf-bfdc-55af33f7b2b8
// last-edited: 2026-10-06

package deduphandler

// SetRestoreBulkLabelForTest replaces the bulk-dismiss revert's label-restore
// step, so a test can make it fail after the status step has run.
func SetRestoreBulkLabelForTest(h *Handler, f func(candidateID int64) (bool, error)) {
	h.restoreBulkLabel = f
}
