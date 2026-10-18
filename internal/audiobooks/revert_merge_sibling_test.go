// file: internal/audiobooks/revert_merge_sibling_test.go
// version: 1.0.0
// guid: 2f9b6d14-8e37-4c05-b1a2-5d7c0e93f468
// last-edited: 2026-10-18

package audiobooks

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// fakeSiblingUndoer serves sibling journals and records the undos asked of it.
type fakeSiblingUndoer struct {
	journals      map[string]*merge.SiblingMoveJournal
	refuse        bool
	undoneByRival bool
	undone        []string
}

func (f *fakeSiblingUndoer) GetSiblingMoveJournal(id string) (*merge.SiblingMoveJournal, error) {
	j, ok := f.journals[id]
	if !ok {
		return nil, merge.ErrSiblingJournalNotFound
	}
	return j, nil
}

func (f *fakeSiblingUndoer) UndoSiblingMove(id string) (*merge.SiblingUndoResult, error) {
	if f.undoneByRival {
		f.journals[id].Status = merge.SiblingJournalUndone
		return nil, fmt.Errorf("%w: journal %s is already undone", merge.ErrSiblingUndoRefused, id)
	}
	if f.refuse {
		return nil, fmt.Errorf("%w: a newer merge moved sibling s1 into the same group", merge.ErrSiblingUndoRefused)
	}
	f.undone = append(f.undone, id)
	f.journals[id].Status = merge.SiblingJournalUndone
	return &merge.SiblingUndoResult{JournalID: id, Status: merge.SiblingJournalUndone}, nil
}

// A merge's sibling-journal pointer row: reverting the operation undoes that
// journal; a journal already undone or aborted counts restored; a refused undo
// fails the row, changed since (unless a rival finished it meanwhile); a
// missing journal or no merge service refuses it.
func TestRevertOperation_MergeSiblingJournal(t *testing.T) {
	rows := func() []*database.OperationChange {
		return []*database.OperationChange{
			{ID: "ptr", OperationID: "op", BookID: "keep", ChangeType: undo.ChangeTypeMergeSiblingJournal, FieldName: undo.MergeSiblingJournalField, NewValue: "S1"},
		}
	}
	keep := &database.Book{ID: "keep", Title: "Keep"}
	newFake := func(status string) *fakeSiblingUndoer {
		return &fakeSiblingUndoer{journals: map[string]*merge.SiblingMoveJournal{"S1": {ID: "S1", Status: status}}}
	}
	run := func(u *fakeSiblingUndoer) (*ledgerStub, *RevertResult, error) {
		s := &ledgerStub{book: keep, changes: rows()}
		rs := NewRevertService(s)
		if u != nil {
			rs.siblingUndo = u
		}
		res, err := rs.RevertOperation("op")
		return s, res, err
	}

	t.Run("undoes the journal", func(t *testing.T) {
		u := newFake(merge.SiblingJournalApplied)
		s, res, err := run(u)
		require.NoError(t, err)
		assert.Equal(t, []string{"S1"}, u.undone)
		assert.Equal(t, 1, res.Restored)
		assert.Equal(t, 1, res.RestoredTypes[undo.ChangeTypeMergeSiblingJournal])
		assert.Equal(t, []string{"ptr"}, s.marked)
	})
	t.Run("a pending journal is undone too", func(t *testing.T) {
		u := newFake(merge.SiblingJournalPending)
		_, res, err := run(u)
		require.NoError(t, err)
		assert.Equal(t, []string{"S1"}, u.undone)
		assert.Equal(t, 1, res.Restored)
	})
	for _, status := range []string{merge.SiblingJournalUndone, merge.SiblingJournalAborted} {
		t.Run("a "+status+" journal is already restored", func(t *testing.T) {
			u := newFake(status)
			s, res, err := run(u)
			require.NoError(t, err)
			assert.Empty(t, u.undone)
			assert.Equal(t, 1, res.AlreadyRestored)
			assert.Equal(t, []string{"ptr"}, s.marked)
		})
	}
	t.Run("a refused undo is changed since", func(t *testing.T) {
		u := newFake(merge.SiblingJournalApplied)
		u.refuse = true
		s, res, err := run(u)
		require.Error(t, err)
		assert.Equal(t, 1, res.Failed)
		assert.Equal(t, 1, res.ChangedSince)
		assert.Empty(t, s.marked)
	})
	t.Run("a refusal against a journal undone meanwhile is already restored", func(t *testing.T) {
		u := newFake(merge.SiblingJournalApplied)
		u.undoneByRival = true
		s, res, err := run(u)
		require.NoError(t, err)
		assert.Equal(t, 1, res.AlreadyRestored)
		assert.Zero(t, res.Failed)
		assert.Equal(t, []string{"ptr"}, s.marked)
	})
	t.Run("a missing journal is refused", func(t *testing.T) {
		s, res, err := run(&fakeSiblingUndoer{journals: map[string]*merge.SiblingMoveJournal{}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sibling-move journal S1 not found")
		assert.Equal(t, 1, res.Failed)
		assert.Empty(t, s.marked)
	})
	t.Run("a store with no merge service refuses", func(t *testing.T) {
		s, res, err := run(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no merge service")
		assert.Equal(t, 1, res.Failed)
		assert.Empty(t, s.marked)
	})
}
