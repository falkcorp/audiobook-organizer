// file: internal/undo/merge_sibling_journal_test.go
// version: 1.0.0
// guid: 4d7a1e93-6b20-4f85-a3c9-0e58b2d7f146
// last-edited: 2026-10-18

package undo

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

type pointerRowsStub struct {
	rows map[string]database.OperationChange
	err  error
}

func (s *pointerRowsStub) CreateOperationChange(c *database.OperationChange) error {
	if s.err != nil {
		return s.err
	}
	if s.rows == nil {
		s.rows = map[string]database.OperationChange{}
	}
	s.rows[c.ID] = *c
	return nil
}

// RecordMergeJournalPointer writes one restorable row under the op id and
// hands back a void that takes it back; it refuses every input that would
// leave a row the revert cannot follow.
func TestRecordMergeJournalPointer(t *testing.T) {
	for _, ct := range []string{ChangeTypeMergeCombineJournal, ChangeTypeMergeSiblingJournal} {
		w := &pointerRowsStub{}
		void, err := RecordMergeJournalPointer(w, "op-1", "keep", ct, "01J")
		require.NoError(t, err)
		require.Len(t, w.rows, 1)
		for _, r := range w.rows {
			require.Equal(t, "op-1", r.OperationID)
			require.Equal(t, "keep", r.BookID)
			require.Equal(t, ct, r.ChangeType)
			require.Equal(t, "01J", r.NewValue)
			require.True(t, IsRestorable(&r), "%+v", r)
		}
		require.NoError(t, void())
		require.Len(t, w.rows, 1, "the void rewrites the same row")
		for _, r := range w.rows {
			require.True(t, r.Voided)
			require.NotNil(t, r.RevertedAt)
		}
	}

	refused := []struct {
		name              string
		w                 JournalPointerWriter
		op, ct, journalID string
	}{
		{"no writer", nil, "op", ChangeTypeMergeCombineJournal, "01J"},
		{"no op id", &pointerRowsStub{}, "", ChangeTypeMergeCombineJournal, "01J"},
		{"not a pointer type", &pointerRowsStub{}, "op", ChangeTypeBookSoftDelete, "01J"},
		{"no journal id", &pointerRowsStub{}, "op", ChangeTypeMergeSiblingJournal, ""},
		{"a key, not an id", &pointerRowsStub{}, "op", ChangeTypeMergeSiblingJournal, "merge:sibling-journal:01J"},
		{"store error", &pointerRowsStub{err: errors.New("disk full")}, "op", ChangeTypeMergeCombineJournal, "01J"},
	}
	for _, tc := range refused {
		void, err := RecordMergeJournalPointer(tc.w, tc.op, "keep", tc.ct, tc.journalID)
		require.Error(t, err, tc.name)
		require.Nil(t, void, tc.name)
	}
}
