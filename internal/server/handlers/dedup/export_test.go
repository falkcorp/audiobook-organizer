// file: internal/server/handlers/dedup/export_test.go
// version: 1.2.0
// guid: 028a211d-147e-4bdf-bfdc-55af33f7b2b8
// last-edited: 2026-10-12

package deduphandler

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunesguard"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// SetRestoreBulkLabelForTest replaces the bulk-dismiss revert's label-restore
// step, so a test can make it fail after the status step has run.
func SetRestoreBulkLabelForTest(h *Handler, f func(candidateID int64) (bool, error)) {
	h.restoreBulkLabel = f
}

// SetITunesFlagBudgetForTest replaces the per-page time bound on the
// include_books is_itunes pass and returns a func that restores it.
func SetITunesFlagBudgetForTest(d time.Duration) (restore func()) {
	old := iTunesFlagBudget
	iTunesFlagBudget = d
	return func() { iTunesFlagBudget = old }
}

// SetOwnershipWhyForTest replaces the ownership predicate the is_itunes pass
// calls (so a test can stand in one that hangs, as a dead mount would) and
// returns a func that restores it.
func SetOwnershipWhyForTest(f func(res *repairs.PathResolver, id, pid string, paths []string, rows []itunesguard.Row, exts []database.ExternalIDMapping, countPathRef bool) (string, bool)) (restore func()) {
	old := ownershipWhy
	ownershipWhy = f
	return func() { ownershipWhy = old }
}

// SetExportRowCapForTest lowers the export row cap and returns a func that
// restores it.
func SetExportRowCapForTest(n int) (restore func()) {
	old := exportRowCap
	exportRowCap = n
	return func() { exportRowCap = old }
}
