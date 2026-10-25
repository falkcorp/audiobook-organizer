// file: internal/audiobooks/revert_retired_shell_receiver_test.go
// version: 1.0.0
// guid: 3d6b8e25-71c4-4a90-b2f8-5e1a9c0d7b43
// last-edited: 2026-10-25

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// retireRowFor is the book_file_retire row a retire Apply journals for one file
// of shell (BookID is the holder; OldValue names the shell the row left).
func retireRowFor(opID, shell, holder string) *database.OperationChange {
	return &database.OperationChange{ID: "retire-" + shell, OperationID: opID, BookID: holder,
		ChangeType: "book_file_retire", FieldName: "book_file:shell-f",
		OldValue: `{"book_id":"` + shell + `","version_id":""}`,
		NewValue: `{"book_id":"` + holder + `","version_id":"rv-` + shell + `"}`}
}

func idReassignRow(opID, from, to string) *database.OperationChange {
	return &database.OperationChange{ID: "ext-" + opID, OperationID: opID, BookID: from,
		ChangeType: undo.ChangeTypeExternalIDReassign, FieldName: "external_id:asin/B000TEST01",
		OldValue: from, NewValue: to}
}

func mergedIntoRow(opID, book, target string) *database.OperationChange {
	return &database.OperationChange{ID: "mi-" + opID, OperationID: opID, BookID: book,
		ChangeType: undo.ChangeTypeBookMergedInto, OldValue: "", NewValue: target}
}

// seedMovedID puts the asin on the holder, as a retire left it.
func seedMovedID(t *testing.T, store *database.PebbleStore, holder string) {
	t.Helper()
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "asin", ExternalID: "B000TEST01", BookID: holder}))
}

func ownerOf(t *testing.T, store *database.PebbleStore) string {
	t.Helper()
	owner, err := store.GetBookByExternalID("asin", "B000TEST01")
	require.NoError(t, err)
	return owner
}

// Handing an external id back to a retired shell is refused: its rows stay
// retired under the holder, so the shell would own the id and no file. The
// control, an ordinary trashed book, takes the id back, so the refusal is the
// shell check's doing.
func TestRevertExternalIDReassign_RefusesAnIDBackToARetiredShell(t *testing.T) {
	_, store, shell, plain := retiredShellFixture(t)
	rs := NewRevertService(store)
	seedMovedID(t, store, "holder")

	err := rs.revertExternalIDReassign(idReassignRow("op-tail", shell, "holder"), nil)
	require.Error(t, err)
	require.Equal(t, undo.ReasonBookIsRetiredShell, undo.RefusalReason(err), "got %v", err)
	require.Equal(t, "holder", ownerOf(t, store), "a refused revert leaves the id on the holder")

	require.NoError(t, rs.revertExternalIDReassign(idReassignRow("op-plain", plain, "holder"), nil))
	require.Equal(t, plain, ownerOf(t, store), "control: an ordinary trashed book takes the id back")
}

// N2: an OLDER operation's book_merged_into row (old "", new = the holder) still
// matches the book's current link when the retire's holder is that same book, so
// the currency check passes it; the shell check is what refuses clearing the
// link of a retired shell.
func TestRevertBookMergedInto_RefusesAnOlderRowOnARetiredShell(t *testing.T) {
	_, store, shell, plain := retiredShellFixture(t)
	rs := NewRevertService(store)
	holder := "holder"
	for _, id := range []string{shell, plain} {
		_, err := store.ModifyBook(id, func(b *database.Book) error { b.MergedIntoBookID = &holder; return nil })
		require.NoError(t, err)
	}

	err := rs.revertBookMergedInto(mergedIntoRow("op-older", shell, "holder"), nil)
	require.Error(t, err)
	require.Equal(t, undo.ReasonBookIsRetiredShell, undo.RefusalReason(err), "got %v", err)
	require.NotNil(t, mustBook(t, store, shell).MergedIntoBookID, "a refused revert leaves the link")

	require.NoError(t, rs.revertBookMergedInto(mergedIntoRow("op-plain", plain, "holder"), nil))
	require.Nil(t, mustBook(t, store, plain).MergedIntoBookID, "control: an ordinary trashed book's link clears")
}

// An operation that retired the shell itself (it holds a book_file_retire row
// naming it) is exempt: its revert un-retires the shell's rows and may put the
// ids and the link back. A plan that did not note such a row is not exempt, and
// neither is a voided row or one whose OldValue names no shell.
func TestRevertPlan_RetireRowOfTheSameOperationExemptsItsShell(t *testing.T) {
	_, store, shell, _ := retiredShellFixture(t)
	rs := NewRevertService(store)
	seedMovedID(t, store, "holder")
	holder := "holder"
	_, err := store.ModifyBook(shell, func(b *database.Book) error { b.MergedIntoBookID = &holder; return nil })
	require.NoError(t, err)
	idRow, miRow := idReassignRow("op-full", shell, "holder"), mergedIntoRow("op-full", shell, "holder")

	planOf := func(extra ...*database.OperationChange) *undo.RevertPlan {
		rows := []*database.OperationChange{idRow, miRow}
		plan, err := undo.PlanRevert(rows, nil)
		require.NoError(t, err)
		plan.NoteHandOffs(append(append([]*database.OperationChange{}, rows...), extra...))
		return plan
	}
	voided := retireRowFor("op-full", shell, "holder")
	voided.Voided = true
	garbled := retireRowFor("op-full", shell, "holder")
	garbled.OldValue = shell // not the JSON a retire journals
	other := retireRowFor("op-full", "some-other-shell", "holder")
	for name, plan := range map[string]*undo.RevertPlan{
		"no retire row": planOf(), "voided retire row": planOf(voided),
		"unparsable retire row": planOf(garbled), "another shell's retire row": planOf(other),
	} {
		err := rs.revertExternalIDReassign(idRow, plan)
		require.Equal(t, undo.ReasonBookIsRetiredShell, undo.RefusalReason(err), "%s: id: %v", name, err)
		err = rs.revertBookMergedInto(miRow, plan)
		require.Equal(t, undo.ReasonBookIsRetiredShell, undo.RefusalReason(err), "%s: link: %v", name, err)
	}
	require.Equal(t, "holder", ownerOf(t, store))

	exempt := planOf(retireRowFor("op-full", shell, "holder"))
	require.NoError(t, rs.revertExternalIDReassign(idRow, exempt))
	require.Equal(t, shell, ownerOf(t, store), "the retire's own operation hands the id back")
	require.NoError(t, rs.revertBookMergedInto(miRow, exempt))
	require.Nil(t, mustBook(t, store, shell).MergedIntoBookID)
}

// The preflight predicts the same refusals, so no Undo is offered for rows the
// revert would refuse, while an ordinary trashed book's rows stay Safe.
func TestPreflightUndo_IDAndLinkRowsOfARetiredShellAreRefused(t *testing.T) {
	_, store, shell, plain := retiredShellFixture(t)
	seedMovedID(t, store, "holder")
	holder := "holder"
	for _, id := range []string{shell, plain} {
		_, err := store.ModifyBook(id, func(b *database.Book) error { b.MergedIntoBookID = &holder; return nil })
		require.NoError(t, err)
	}
	for _, c := range []*database.OperationChange{
		idReassignRow("op-pre-shell", shell, "holder"), mergedIntoRow("op-pre-shell", shell, "holder"),
		idReassignRow("op-pre-plain", plain, "holder"), mergedIntoRow("op-pre-plain", plain, "holder"),
	} {
		c.ID += "-" + c.BookID
		require.NoError(t, store.CreateOperationChange(c))
	}

	report, err := undo.PreflightUndoConflicts(store, "op-pre-shell")
	require.NoError(t, err)
	require.Zero(t, report.Safe)
	require.Len(t, report.CheckFailed, 2)
	for _, it := range report.CheckFailed {
		require.Equal(t, undo.ReasonBookIsRetiredShell, it.Reason, "%+v", it)
	}

	report, err = undo.PreflightUndoConflicts(store, "op-pre-plain")
	require.NoError(t, err)
	require.Empty(t, report.CheckFailed, "control: an ordinary trashed book's rows are not refused")
	require.Equal(t, 2, report.Safe)
}
