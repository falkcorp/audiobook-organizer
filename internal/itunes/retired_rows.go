// file: internal/itunes/retired_rows.go
// version: 1.0.0
// guid: 7d41c0a2-93e8-4b5f-a6d1-2c8e5b09f713
// last-edited: 2026-10-23

package itunes

import "github.com/falkcorp/audiobook-organizer/internal/database"

// splitRetiredRows reads every book_file row, retired ones included, and
// returns the live rows and the retired rows separately.
//
// A retired row (a version the retire-versions repair moved onto its holder)
// keeps its iTunes persistent ID on purpose: the ID is the provenance the undo
// of the retire needs, and the track it names in the iTunes library still
// exists. The PID audits therefore need to SEE retired rows, but must not mix
// them into the live PID groups: a live row on the holder can legitimately
// carry the same PID (the PID index points at the live row), so counting the
// retired row as a second owner would report a duplicate that is not one, and a
// repair plan built from that group would clear the retired row's PID.
//
// store is `any` because the audits narrow it to their own small interfaces;
// database.AllBookFilesCoreIncludingRetired resolves the retired reader
// through that and falls back to the plain getter for stores that have never
// retired a row.
func splitRetiredRows(store any) (live, retired []database.BookFileCore, err error) {
	all, err := database.AllBookFilesCoreIncludingRetired(store)
	if err != nil {
		return nil, nil, err
	}
	live = make([]database.BookFileCore, 0, len(all))
	for i := range all {
		if all[i].Retired {
			retired = append(retired, all[i])
			continue
		}
		live = append(live, all[i])
	}
	return live, retired, nil
}
