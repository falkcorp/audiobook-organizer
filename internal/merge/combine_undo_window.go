// file: internal/merge/combine_undo_window.go
// version: 1.0.0
// guid: 4a9d2e71-6c35-4f08-b1d7-8e3a5c20f946
// last-edited: 2026-10-22

package merge

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// combineJournalScanner is the one raw-key read UnreversedCombineShells needs.
// database.Store carries it (RawKVStore).
type combineJournalScanner interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
}

// UnreversedCombineShells maps every book a combine journal names as absorbed
// to the ids of the journals that name it, counting only journals that are
// NOT undone: applied, pending and undo_failed ones can all still be undone
// (UndoCombine accepts each), so the shell they name is still the thing an
// undo would restore. A caller that wants to retire or purge a trashed book
// must leave these alone, or it makes the combine silently non-undoable (the
// 2026-10-21 purge did that to 21 combines).
//
// It reads every journal once and decodes only the three fields it needs, so
// a journal's progress snapshots are skipped, not materialised. A row that
// cannot be decoded fails the whole read: which shells it named is unknown,
// and answering without it would fail OPEN, which is the direction that loses
// an undo.
//
// There is no per-book index of this yet. A caller that asks per book (a
// re-plan of one row) pays the whole scan; one that plans the whole trash
// asks once.
func UnreversedCombineShells(db combineJournalScanner) (map[string][]string, error) {
	rows, err := db.ScanPrefix(combineJournalPrefix)
	if err != nil {
		return nil, fmt.Errorf("list combine journals: %w", err)
	}
	out := map[string][]string{}
	for _, r := range rows {
		var j struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Absorbed []struct {
				BookID string `json:"book_id"`
			} `json:"absorbed"`
		}
		if err := json.Unmarshal(r.Value, &j); err != nil {
			return nil, fmt.Errorf("combine journal row %s is not readable, so which books it names is unknown: %w", r.Key, err)
		}
		if j.Status == CombineJournalUndone {
			continue
		}
		for _, a := range j.Absorbed {
			if a.BookID != "" {
				out[a.BookID] = append(out[a.BookID], j.ID)
			}
		}
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out, nil
}
