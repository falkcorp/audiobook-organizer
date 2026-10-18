// file: internal/audiobooks/revert_merge_combine_test.go
// version: 1.1.0
// guid: a6c29e41-8f07-4b3d-9e15-d2b84f7c0a63
// last-edited: 2026-10-18

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The server's store (database.Store, the revert's store in production) is a
// merge.Store, so combineUndoerFor builds a merge service over it rather than
// refusing every pointer row.
var _ merge.Store = database.Store(nil)

// fakeCombineUndoer serves one journal and records the undos asked of it.
type fakeCombineUndoer struct {
	journals map[string]*merge.CombineJournal
	refuse   bool
	// warnings are returned on a successful undo's result.
	warnings []string
	// undoneByRival: the journal is undone by someone else after the revert
	// read it, so this undo is refused against a journal that is done.
	undoneByRival bool
	undone        []string
}

func (f *fakeCombineUndoer) GetCombineJournal(id string) (*merge.CombineJournal, error) {
	j, ok := f.journals[id]
	if !ok {
		return nil, merge.ErrCombineJournalNotFound
	}
	return j, nil
}

func (f *fakeCombineUndoer) UndoCombine(id string) (*merge.CombineUndoResult, error) {
	if f.undoneByRival {
		f.journals[id].Status = merge.CombineJournalUndone
		return nil, &merge.CombineUndoRefusedError{JournalID: id, Reasons: []string{"journal already undone"}}
	}
	if f.refuse {
		return nil, &merge.CombineUndoRefusedError{JournalID: id, Reasons: []string{"file f1 is no longer on survivor keep"}}
	}
	f.undone = append(f.undone, id)
	f.journals[id].Status = merge.CombineJournalUndone
	return &merge.CombineUndoResult{JournalID: id, Warnings: f.warnings}, nil
}

// The split-book fixer's apply journals a ledger decision and a pointer to
// the merge's combine journal. Reverting the operation undoes that journal;
// one already undone counts restored; a refused undo fails the row, changed
// since; a missing journal or no merge service refuses it. The ledger row is
// marked only when the pointer row was reverted.
func TestRevertOperation_MergeCombineJournal(t *testing.T) {
	rows := func() []*database.OperationChange {
		return []*database.OperationChange{
			{ID: "dec", OperationID: "op", BookID: "keep", ChangeType: undo.ChangeTypeRepairPlanRecord, FieldName: "row:split:1", NewValue: `{"row_id":"split:1"}`},
			{ID: "ptr", OperationID: "op", BookID: "keep", ChangeType: undo.ChangeTypeMergeCombineJournal, FieldName: undo.MergeCombineJournalField, NewValue: "J1"},
		}
	}
	keep := &database.Book{ID: "keep", Title: "Keep"}
	newFake := func(status string) *fakeCombineUndoer {
		return &fakeCombineUndoer{journals: map[string]*merge.CombineJournal{"J1": {ID: "J1", Status: status}}}
	}

	t.Run("undoes the journal", func(t *testing.T) {
		s := &ledgerStub{book: keep, changes: rows()}
		u := newFake(merge.CombineJournalApplied)
		rs := NewRevertService(s)
		rs.combineUndo = u
		res, err := rs.RevertOperation("op")
		require.NoError(t, err)
		assert.Equal(t, []string{"J1"}, u.undone)
		assert.Equal(t, 1, res.Restored)
		assert.ElementsMatch(t, []string{"ptr", "dec"}, s.marked)
	})
	t.Run("what the undo left as is is reported on the revert", func(t *testing.T) {
		s := &ledgerStub{book: keep, changes: rows()}
		u := newFake(merge.CombineJournalApplied)
		u.warnings = []string{"file row f9 (created by the combine for book gone) has no book to go back to; left on survivor keep"}
		rs := NewRevertService(s)
		rs.combineUndo = u
		res, err := rs.RevertOperation("op")
		require.NoError(t, err)
		assert.Equal(t, 1, res.Restored, "the combine is undone")
		assert.Equal(t, 1, res.PartiallyRestored)
		require.Len(t, res.Kept, 1)
		assert.Contains(t, res.Kept[0], "file row f9")
		assert.ElementsMatch(t, []string{"ptr", "dec"}, s.marked)
	})
	t.Run("a journal already undone is already restored", func(t *testing.T) {
		s := &ledgerStub{book: keep, changes: rows()}
		u := newFake(merge.CombineJournalUndone)
		rs := NewRevertService(s)
		rs.combineUndo = u
		res, err := rs.RevertOperation("op")
		require.NoError(t, err)
		assert.Empty(t, u.undone, "nothing is undone twice")
		assert.Equal(t, 1, res.AlreadyRestored)
		assert.ElementsMatch(t, []string{"ptr", "dec"}, s.marked)
	})
	t.Run("a refused undo is changed since", func(t *testing.T) {
		s := &ledgerStub{book: keep, changes: rows()}
		u := newFake(merge.CombineJournalApplied)
		u.refuse = true
		rs := NewRevertService(s)
		rs.combineUndo = u
		res, err := rs.RevertOperation("op")
		require.Error(t, err)
		assert.Equal(t, 1, res.Failed)
		assert.Equal(t, 1, res.ChangedSince)
		assert.Empty(t, s.marked)
	})
	t.Run("a refusal against a journal undone in the meantime is already restored", func(t *testing.T) {
		s := &ledgerStub{book: keep, changes: rows()}
		u := newFake(merge.CombineJournalApplied)
		u.undoneByRival = true
		rs := NewRevertService(s)
		rs.combineUndo = u
		res, err := rs.RevertOperation("op")
		require.NoError(t, err)
		assert.Equal(t, 1, res.AlreadyRestored)
		assert.Zero(t, res.Failed)
		assert.Zero(t, res.ChangedSince)
		assert.ElementsMatch(t, []string{"ptr", "dec"}, s.marked)
	})
	t.Run("a missing journal is refused", func(t *testing.T) {
		s := &ledgerStub{book: keep, changes: rows()}
		rs := NewRevertService(s)
		rs.combineUndo = &fakeCombineUndoer{journals: map[string]*merge.CombineJournal{}}
		res, err := rs.RevertOperation("op")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "combine journal J1 not found")
		assert.Equal(t, 1, res.Failed)
		assert.Empty(t, s.marked)
	})
	t.Run("a store with no merge service refuses", func(t *testing.T) {
		s := &ledgerStub{book: keep, changes: rows()}
		res, err := NewRevertService(s).RevertOperation("op")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no merge service")
		assert.Equal(t, 1, res.Failed)
		assert.Empty(t, s.marked)
	})
}
