// file: internal/database/collection_members_cap.go
// version: 1.0.0
// guid: 5d1c8e3a-92b4-4c70-a6f1-0b7e3d9a2c64
// last-edited: 2026-10-26

package database

import "slices"

// MaxMaterializedMembers is the most member ids a dynamic collection or smart
// playlist carries: in its stored MaterializedBookIDs, in a native API answer, and
// in what the ABS surface expands into library items.
//
// A query is evaluated over the whole index with no result window, so a broad one
// (read_status:unstarted on a 120,000-book library) answers with every book. Every
// consumer of that list is paid per member: the stored row is rewritten whole with
// a sync, the native answer is one JSON array, and the ABS surface builds a full
// library item for each. 10,000 is the window the evaluator used to impose
// silently (and wrongly, as the OLDEST 10,000 books); it is kept as an explicit
// bound that is reported, not hidden: MaterializedTotal on the row or answer holds
// the real count whenever the list was cut.
const MaxMaterializedMembers = 10_000

// CapMembers returns ids cut to MaxMaterializedMembers, and the number of ids it
// was given when it had to cut (0 when nothing was cut, so the zero value of
// MaterializedTotal means "the list is complete"). A list that is cut is a copy,
// so the full list's backing array is not kept alive by the stored row.
func CapMembers(ids []string) (kept []string, total int) {
	if len(ids) <= MaxMaterializedMembers {
		return ids, 0
	}
	return slices.Clone(ids[:MaxMaterializedMembers]), len(ids)
}
