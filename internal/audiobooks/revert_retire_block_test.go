// file: internal/audiobooks/revert_retire_block_test.go
// version: 1.0.0
// guid: c6f2a8d3-9e14-4b07-8a35-1d7b0e5f4c92
// last-edited: 2026-10-24

package audiobooks

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// retireOpFixture is a retired shell and its holder with the rows a retire
// Apply journals: the id move, the merged-into stamp, and (when withRetire)
// the retire row itself.
type retireOpFixture struct {
	s             *database.PebbleStore
	shell, holder *database.Book
	opID          string
	changes       []*database.OperationChange
}

func newRetireOpFixture(t *testing.T, withRetire bool) *retireOpFixture {
	t.Helper()
	s := newRevertPebble(t)
	holder, err := s.CreateBook(&database.Book{Title: "Holder", FilePath: "/lib/holder"})
	require.NoError(t, err)
	holderID := holder.ID
	shell, err := s.CreateBook(&database.Book{Title: "Shell", FilePath: "/lib/shell", MergedIntoBookID: &holderID})
	require.NoError(t, err)
	require.NoError(t, s.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "asin", ExternalID: "B000TEST01", BookID: holder.ID}))

	f := &retireOpFixture{s: s, shell: shell, holder: holder, opID: "op-retire-block"}
	now := time.Now()
	add := func(c *database.OperationChange) {
		c.ID = c.ChangeType + "-" + c.BookID
		c.OperationID = f.opID
		c.CreatedAt = now
		f.changes = append(f.changes, c)
		require.NoError(t, s.CreateOperationChange(c))
	}
	if withRetire {
		add(&database.OperationChange{BookID: shell.ID, ChangeType: "book_file_retire",
			FieldName: "book_file:f1", OldValue: shell.ID, NewValue: holder.ID})
	}
	add(&database.OperationChange{BookID: holder.ID, ChangeType: undo.ChangeTypeExternalIDReassign,
		FieldName: "external_id:asin/B000TEST01", OldValue: shell.ID, NewValue: holder.ID})
	add(&database.OperationChange{BookID: shell.ID, ChangeType: undo.ChangeTypeBookMergedInto,
		OldValue: "", NewValue: holder.ID})
	return f
}

func (f *retireOpFixture) assertUntouched(t *testing.T) {
	t.Helper()
	owner, err := f.s.GetBookByExternalID("asin", "B000TEST01")
	require.NoError(t, err)
	require.Equal(t, f.holder.ID, owner, "the external id stays on the holder")
	got, err := f.s.GetBookByID(f.shell.ID)
	require.NoError(t, err)
	require.NotNil(t, got.MergedIntoBookID, "the merged-into link stays")
	rows, err := f.s.GetOperationChanges(f.opID)
	require.NoError(t, err)
	for _, c := range rows {
		require.Nil(t, c.RevertedAt, "no row of the operation is marked reverted: %s", c.ChangeType)
	}
}

// An operation holding a retire row the undo cannot read (this fixture's
// book_file_retire row has no decodable places) is refused as a whole: the id
// move and merged-into rows beside it are reversible on their own, and
// reversing only those would leave the shell looking live and empty.
func TestRevertOperation_RefusesAWholeRetireOperation(t *testing.T) {
	f := newRetireOpFixture(t, true)

	res, err := NewRevertService(f.s).RevertOperation(f.opID)
	require.Error(t, err)
	require.Nil(t, res)
	var blocked *undo.RetireRevertBlockedError
	require.True(t, errors.As(err, &blocked), "got %v", err)
	require.Equal(t, map[string]int{"book_file_retire": 1}, blocked.Types)
	f.assertUntouched(t)
}

// The control: the same two reversible rows without the retire row revert, so
// the refusal above is the retire row's doing and the assertions can fail.
func TestRevertOperation_ControlWithoutRetireRowReverts(t *testing.T) {
	f := newRetireOpFixture(t, false)

	res, err := NewRevertService(f.s).RevertOperation(f.opID)
	require.NoError(t, err)
	require.Equal(t, 2, res.Restored)
	owner, err := f.s.GetBookByExternalID("asin", "B000TEST01")
	require.NoError(t, err)
	require.Equal(t, f.shell.ID, owner, "the id moved back")
}

// A well-formed retire row is reversible (the retire undo cases), so it does
// not block: the refusal is only for a row the undo cannot read.
func TestRetireRevertBlock_WellFormedRetireRowDoesNotBlock(t *testing.T) {
	row := &database.OperationChange{
		BookID: "holder", ChangeType: undo.ChangeTypeBookFileRetire, FieldName: "book_file:f1",
		OldValue: undo.RetirePlace{BookID: "shell"}.Encode(),
		NewValue: undo.RetirePlace{BookID: "holder", VersionID: "rv-1"}.Encode(),
	}
	require.Empty(t, undo.NotRestorableLabel(row), "precondition: part 5 reads this row")
	require.Nil(t, undo.RetireRevertBlock("op", []*database.OperationChange{row}))
	require.True(t, undo.OperationRevertible([]*database.OperationChange{row}))
}

// A voided retire row never happened and does not block.
func TestRetireRevertBlock_IgnoresVoidedRows(t *testing.T) {
	rows := []*database.OperationChange{{ChangeType: "book_file_retire", Voided: true}}
	require.Nil(t, undo.RetireRevertBlock("op", rows))
	require.True(t, undo.OperationRevertible([]*database.OperationChange{
		{ChangeType: undo.ChangeTypeBookMergedInto, NewValue: "x"}}))
}

// The preflight and the offer of an Undo agree with the revert: no Safe row, no
// Undo.
func TestRetireOperation_PreflightAndRevertibleAgree(t *testing.T) {
	f := newRetireOpFixture(t, true)

	require.False(t, undo.OperationRevertible(f.changes))
	report, err := undo.PreflightUndoConflicts(f.s, f.opID)
	require.NoError(t, err)
	require.Zero(t, report.Safe)
	require.Len(t, report.CheckFailed, 2)
	for _, it := range report.CheckFailed {
		require.Equal(t, undo.ReasonRetireOpNotRevertible, it.Reason)
	}
}
