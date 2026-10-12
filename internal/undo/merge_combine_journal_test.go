// file: internal/undo/merge_combine_journal_test.go
// version: 1.0.0
// guid: 8d14b6e2-7a3c-4f95-b0d7-2e9c51a4f6d8
// last-edited: 2026-10-12

package undo

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The classifier and the revert agree on the split-book fixer's pointer row:
// a row naming a journal id is restorable (the revert undoes that journal),
// a malformed one is labelled not restorable, and it is never ledger-only.
func TestMergeCombineJournalRows_Classified(t *testing.T) {
	ok := &database.OperationChange{ChangeType: ChangeTypeMergeCombineJournal, FieldName: MergeCombineJournalField, NewValue: "01JOURNALID"}
	require.True(t, IsRestorable(ok))
	require.False(t, IsLedgerOnly(ok))
	require.True(t, CountsTowardRevert(ok))
	for _, bad := range []*database.OperationChange{
		{ChangeType: ChangeTypeMergeCombineJournal, FieldName: MergeCombineJournalField},
		{ChangeType: ChangeTypeMergeCombineJournal, FieldName: "other", NewValue: "01JOURNALID"},
		{ChangeType: ChangeTypeMergeCombineJournal, FieldName: MergeCombineJournalField, NewValue: "merge:combine-journal:01J"},
	} {
		require.Equal(t, ChangeTypeMergeCombineJournal+":(no journal id)", NotRestorableLabel(bad), "%+v", bad)
	}
}
