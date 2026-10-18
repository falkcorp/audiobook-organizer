// file: internal/undo/merge_sibling_journal_test.go
// version: 1.0.1
// guid: 4d7a1e93-6b20-4f85-a3c9-0e58b2d7f146
// last-edited: 2026-10-18

package undo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// The classifier and the revert agree on a merge's sibling-journal pointer:
// a row naming a journal id is restorable, a malformed one is labelled not
// restorable, and it is never ledger-only.
func TestMergeSiblingJournalRows_Classified(t *testing.T) {
	ok := &database.OperationChange{ChangeType: ChangeTypeMergeSiblingJournal, FieldName: MergeSiblingJournalField, NewValue: "01JOURNALID"}
	require.True(t, IsRestorable(ok))
	require.False(t, IsLedgerOnly(ok))
	require.True(t, CountsTowardRevert(ok))
	for _, bad := range []*database.OperationChange{
		{ChangeType: ChangeTypeMergeSiblingJournal, FieldName: MergeSiblingJournalField},
		{ChangeType: ChangeTypeMergeSiblingJournal, FieldName: "other", NewValue: "01JOURNALID"},
		{ChangeType: ChangeTypeMergeSiblingJournal, FieldName: MergeSiblingJournalField, NewValue: "merge:sibling-journal:01J"},
	} {
		require.Equal(t, ChangeTypeMergeSiblingJournal+":(no journal id)", NotRestorableLabel(bad), "%+v", bad)
	}
}

// The prune keep-list (database cannot import undo, so it spells the types
// out) names both merge journal pointers: each is the only link from an
// operation to an undo record that is itself never pruned.
func TestMergeJournalPointers_KeptByPrune(t *testing.T) {
	require.True(t, database.OpChangeTypesKeptByPrune[ChangeTypeMergeCombineJournal])
	require.True(t, database.OpChangeTypesKeptByPrune[ChangeTypeMergeSiblingJournal])
}

// A pointer merge.RecordJournalPointer writes is one this package classifies
// restorable, for both journal kinds.
func TestMergeJournalPointers_WrittenRowsRestorable(t *testing.T) {
	for _, ct := range []string{ChangeTypeMergeCombineJournal, ChangeTypeMergeSiblingJournal} {
		w := &rowsStub{}
		_, err := merge.RecordJournalPointer(w, "op-1", "keep", ct, "01J")
		require.NoError(t, err)
		require.Len(t, w.rows, 1)
		require.True(t, IsRestorable(w.rows[0]), "%+v", w.rows[0])
	}
}

type rowsStub struct{ rows []*database.OperationChange }

func (s *rowsStub) CreateOperationChange(c *database.OperationChange) error {
	s.rows = append(s.rows, c)
	return nil
}
