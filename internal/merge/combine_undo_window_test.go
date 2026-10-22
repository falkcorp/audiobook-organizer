// file: internal/merge/combine_undo_window_test.go
// version: 1.0.0
// guid: 7c1e5b94-2d08-4a63-9f17-b4e6a8d3c025
// last-edited: 2026-10-22

package merge

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeJournalScan struct {
	rows []database.KVPair
	err  error
}

func (f fakeJournalScan) ScanPrefix(prefix string) ([]database.KVPair, error) {
	if prefix != combineJournalPrefix {
		return nil, errors.New("unexpected prefix " + prefix)
	}
	return f.rows, f.err
}

func journalRow(t *testing.T, id, status string, absorbed ...string) database.KVPair {
	t.Helper()
	j := &CombineJournal{ID: id, Status: status, SurvivorID: "survivor"}
	for _, a := range absorbed {
		j.Absorbed = append(j.Absorbed, CombineAbsorbed{BookID: a})
	}
	data, err := json.Marshal(j)
	require.NoError(t, err)
	return database.KVPair{Key: combineJournalKey(id), Value: data}
}

// Applied, pending and undo_failed journals keep their absorbed books in the
// undo window; an undone one releases them.
func TestUnreversedCombineShells(t *testing.T) {
	got, err := UnreversedCombineShells(fakeJournalScan{rows: []database.KVPair{
		journalRow(t, "J1", CombineJournalApplied, "A", "B"),
		journalRow(t, "J2", CombineJournalUndone, "C"),
		journalRow(t, "J3", CombineJournalPending, "D"),
		journalRow(t, "J4", CombineJournalUndoFailed, "E", "B"),
		journalRow(t, "J5", CombineJournalApplied),
	}})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{
		"A": {"J1"}, "B": {"J1", "J4"}, "D": {"J3"}, "E": {"J4"},
	}, got)
}

// An unreadable journal row fails the read instead of being skipped.
func TestUnreversedCombineShells_FailsClosed(t *testing.T) {
	_, err := UnreversedCombineShells(fakeJournalScan{rows: []database.KVPair{
		{Key: combineJournalKey("J1"), Value: []byte("{not json")},
	}})
	require.ErrorContains(t, err, "not readable")
	_, err = UnreversedCombineShells(fakeJournalScan{err: errors.New("disk")})
	require.ErrorContains(t, err, "disk")
}
