// file: internal/scanner/scan_history.go
// version: 1.0.0
// guid: 6c348220-2f70-479f-a3fc-458c64478032
// last-edited: 2026-10-06
//
// Records the scanner's own book writes in the metadata change history.

package scanner

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Sources of the scanner's history rows (change type database.ChangeTypeScan).
const (
	// scanHistorySource is the library scan's own write: the rescan merge,
	// a relink, a version link.
	scanHistorySource = "scan"
	// scanAIHistorySource is the queued AI parse filling empty fields.
	scanAIHistorySource = "scan.ai-parse"
)

// modifyBookRecorded is getStore().ModifyBook plus a change-history row for
// every Book field the write changed (database.RecordBookEditHistory, change
// type database.ChangeTypeScan). Until 2026-10-06 no scanner write recorded
// history, so a scan that re-titled 1,176 books left nothing in any book's
// history to show it. Every scanner ModifyBook goes through here.
//
// before and after are taken inside the callback, on the row as it stands
// at write time, so a concurrent writer's change is not attributed to the
// scan; only the last run of a retried callback counts. A callback that
// returns an error (database.ErrSkipBookWrite included) records nothing. A
// failed history write is logged, never returned: the row is written.
func modifyBookRecorded(id, source string, fn func(*database.Book) error) (*database.Book, error) {
	st := getStore()
	var before, after database.Book
	wrote := false
	res, err := st.ModifyBook(id, func(cur *database.Book) error {
		snapshot := *cur
		wrote = false
		if ferr := fn(cur); ferr != nil {
			return ferr
		}
		before, after, wrote = snapshot, *cur, true
		return nil
	})
	if err != nil || res == nil || !wrote {
		return res, err
	}
	recordScanHistory(st, &before, &after, source)
	return res, err
}

// recordScanHistory writes the history rows for one scanner write.
func recordScanHistory(st scannerStore, before, after *database.Book, source string) {
	if st == nil || before == nil || after == nil || after.ID == "" {
		return
	}
	n, err := database.RecordBookEditHistory(st, before, after, database.ChangeTypeScan, source, time.Now(), nil)
	if err != nil {
		defaultLog.Warn("scan: change history of book %s incomplete (%d row(s) recorded): %v", after.ID, n, err)
	}
}
