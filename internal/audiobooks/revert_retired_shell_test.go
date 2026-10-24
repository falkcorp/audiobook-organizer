// file: internal/audiobooks/revert_retired_shell_test.go
// version: 1.0.0
// guid: 8a4d1f60-e2b7-4c93-a5d8-0f6b3c7e9124
// last-edited: 2026-10-24

package audiobooks

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A soft-delete row's revert brings the book back from the trash; for a retired
// shell that would be an empty book beside the audio retired under another
// book, so it is refused, under the merge lock, before any write. An ordinary
// trashed book in the same operation still restores (the control that makes the
// shell assertions able to fail).
func TestRevertBookSoftDelete_RefusesARetiredShell(t *testing.T) {
	_, store, shell, plain := retiredShellFixture(t)
	rs := NewRevertService(store)

	err := rs.revertBookSoftDelete(&database.OperationChange{OperationID: "op-sd", BookID: shell,
		ChangeType: undo.ChangeTypeBookSoftDelete}, undo.SoftDeleteStamps{})
	require.Error(t, err)
	require.True(t, errors.Is(err, database.ErrBookIsRetiredShell), "got %v", err)
	require.True(t, database.IsInTrash(mustBook(t, store, shell)), "a refused revert leaves the shell in the trash")

	require.NoError(t, rs.revertBookSoftDelete(&database.OperationChange{OperationID: "op-sd", BookID: plain,
		ChangeType: undo.ChangeTypeBookSoftDelete}, undo.SoftDeleteStamps{}))
	require.False(t, database.IsInTrash(mustBook(t, store, plain)), "control: an ordinary trashed book restores")
}

// A store that cannot read retired versions refuses the restore (fail closed)
// rather than treating the shell as an ordinary trashed book.
func TestRevertBookSoftDelete_UnreadableRetiredRowsFailClosed(t *testing.T) {
	_, store, shell, _ := retiredShellFixture(t)
	rs := NewRevertService(failingRetiredReads{store})

	err := rs.revertBookSoftDelete(&database.OperationChange{OperationID: "op-sd", BookID: shell,
		ChangeType: undo.ChangeTypeBookSoftDelete}, undo.SoftDeleteStamps{})
	require.Error(t, err)
	require.False(t, errors.Is(err, database.ErrBookIsRetiredShell))
	require.True(t, database.IsInTrash(mustBook(t, store, shell)))
}

// The preflight predicts the same refusal, so no Undo is offered for a row the
// revert would refuse.
func TestPreflightUndo_SoftDeleteOfARetiredShellIsRefused(t *testing.T) {
	_, store, shell, plain := retiredShellFixture(t)
	for _, id := range []string{shell, plain} {
		require.NoError(t, store.CreateOperationChange(&database.OperationChange{
			ID: "sd-" + id, OperationID: "op-pre", BookID: id, ChangeType: undo.ChangeTypeBookSoftDelete}))
	}

	report, err := preflightUndoConflicts(store, "op-pre")
	require.NoError(t, err)
	require.Len(t, report.CheckFailed, 1)
	require.Equal(t, shell, report.CheckFailed[0].BookID)
	require.Equal(t, undo.ReasonBookIsRetiredShell, report.CheckFailed[0].Reason)
}
