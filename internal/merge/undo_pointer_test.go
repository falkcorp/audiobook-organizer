// file: internal/merge/undo_pointer_test.go
// version: 1.0.0
// guid: 5e8c2b47-9d16-4a30-b7f4-1c6a3e09d852
// last-edited: 2026-10-18

package merge

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

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

// RecordJournalPointer writes one row under the op id and hands back a void
// that takes it back; it refuses every input that would leave a row the
// revert cannot follow.
func TestRecordJournalPointer(t *testing.T) {
	for _, ct := range []string{CombineJournalPointerType, SiblingJournalPointerType} {
		w := &pointerRowsStub{}
		void, err := RecordJournalPointer(w, "op-1", "keep", ct, "01J")
		require.NoError(t, err)
		require.Len(t, w.rows, 1)
		for _, r := range w.rows {
			require.Equal(t, "op-1", r.OperationID)
			require.Equal(t, "keep", r.BookID)
			require.Equal(t, ct, r.ChangeType)
			require.Equal(t, JournalPointerField, r.FieldName)
			require.Equal(t, "01J", r.NewValue)
			require.False(t, r.Voided)
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
		{"no writer", nil, "op", CombineJournalPointerType, "01J"},
		{"no op id", &pointerRowsStub{}, "", CombineJournalPointerType, "01J"},
		{"not a pointer type", &pointerRowsStub{}, "op", "book_soft_delete", "01J"},
		{"no journal id", &pointerRowsStub{}, "op", SiblingJournalPointerType, ""},
		{"a key, not an id", &pointerRowsStub{}, "op", SiblingJournalPointerType, "merge:sibling-journal:01J"},
		{"store error", &pointerRowsStub{err: errors.New("disk full")}, "op", CombineJournalPointerType, "01J"},
	}
	for _, tc := range refused {
		void, err := RecordJournalPointer(tc.w, tc.op, "keep", tc.ct, tc.journalID)
		require.Error(t, err, tc.name)
		require.Nil(t, void, tc.name)
	}

	// A store with no undo ledger refuses through the capability lookup too.
	_, err := RecordJournalPointerIn(struct{}{}, "op", "keep", CombineJournalPointerType, "01J")
	require.Error(t, err)
}

func TestUndoOperationID(t *testing.T) {
	require.Equal(t, "op-7", UndoOperationID("op-7"))
	a, b := UndoOperationID(""), UndoOperationID("")
	require.NotEmpty(t, a)
	require.NotEqual(t, a, b, "each merge with no operation gets its own id")
}
