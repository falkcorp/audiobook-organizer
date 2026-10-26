// file: internal/batch/action_grants_test.go
// version: 1.0.1
// guid: 7b0c5f3e-91a4-4d2b-8c6e-2f1a9d4e5b73
// last-edited: 2026-10-26

package batch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func opsFor(id, action string, hard bool, updates map[string]any, caller *TrashGrants) *BatchOperationsRequest {
	return &BatchOperationsRequest{
		Operations: []BatchOperationItem{{ID: id, Action: action, HardDelete: hard, Updates: updates}},
		Caller:     caller,
	}
}

// The delete (soft and hard) and restore actions of ExecuteOperations are
// judged by the service from the request's Caller, not only by the handler: a
// request with no Caller, or without the action's permission, changes nothing,
// and the matching permission is accepted.
func TestExecuteOperations_DeleteAndRestoreActionsNeedTheirGrant(t *testing.T) {
	t.Run("soft delete needs library.delete", func(t *testing.T) {
		for name, caller := range map[string]*TrashGrants{"no caller": nil, "restore only": {Restore: true}} {
			f := vptest.New(t)
			id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})
			resp := NewBatchService(f.S).ExecuteOperations(opsFor(id, "delete", false, nil, caller))
			require.Equal(t, 1, resp.Failed, "%s: %+v", name, resp.Results)
			require.Contains(t, resp.Results[0].Error, "library.delete", name)
			require.False(t, stillInTrash(t, f, id), name)
		}
		f := vptest.New(t)
		id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})
		resp := NewBatchService(f.S).ExecuteOperations(opsFor(id, "delete", false, nil, &TrashGrants{Trash: true}))
		require.Equal(t, 1, resp.Success, "%+v", resp.Results)
		require.True(t, stillInTrash(t, f, id))
	})

	t.Run("hard delete needs library.delete", func(t *testing.T) {
		for name, caller := range map[string]*TrashGrants{"no caller": nil, "restore only": {Restore: true}} {
			f := newOwnedFilesFixture(t)
			f.book(t, "plain", false, "")
			resp := NewBatchService(f.s).ExecuteOperations(opsFor("plain", "delete", true, nil, caller))
			require.Equal(t, 1, resp.Failed, "%s: %+v", name, resp.Results)
			require.Contains(t, resp.Results[0].Error, "library.delete", name)
			require.True(t, f.exists("plain"), name)
		}
		f := newOwnedFilesFixture(t)
		f.book(t, "plain", false, "")
		resp := NewBatchService(f.s).ExecuteOperations(opsFor("plain", "delete", true, nil, &TrashGrants{Trash: true}))
		require.Equal(t, 1, resp.Success, "%+v", resp.Results)
		require.False(t, f.exists("plain"))
	})

	t.Run("restore needs library.organize", func(t *testing.T) {
		for name, caller := range map[string]*TrashGrants{"no caller": nil, "delete only": {Trash: true}} {
			f, id := trashedLikeDeleteAudiobook(t)
			resp := NewBatchService(f.S).ExecuteOperations(opsFor(id, "restore", false, nil, caller))
			require.Equal(t, 1, resp.Failed, "%s: %+v", name, resp.Results)
			require.Contains(t, resp.Results[0].Error, "library.organize", name)
			require.True(t, stillInTrash(t, f, id), name)
		}
		f, id := trashedLikeDeleteAudiobook(t)
		resp := NewBatchService(f.S).ExecuteOperations(opsFor(id, "restore", false, nil, &TrashGrants{Restore: true}))
		require.Equal(t, 1, resp.Success, "%+v", resp.Results)
		require.False(t, stillInTrash(t, f, id))
	})
}

// A restore payload (marked_for_deletion=false) that also sets library_state to
// "deleted" on a live book is judged in the trash direction too, on the row as
// locked: the restore branch used to judge only the restore direction, so a
// caller holding library.organize alone could trash a book this way.
func TestRestorePayload_AlsoJudgesTheTrashDirection(t *testing.T) {
	updates := map[string]any{"marked_for_deletion": false, "library_state": "deleted"}
	f := vptest.New(t)
	id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})

	resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{id}, Updates: updates, Caller: &TrashGrants{Restore: true}})
	require.Equal(t, 1, resp.Failed, "%+v", resp.Results)
	require.Contains(t, resp.Results[0].Error, "library.delete")
	require.False(t, stillInTrash(t, f, id))

	resp = NewBatchService(f.S).ExecuteOperations(opsFor(id, "update", false, updates, &TrashGrants{Restore: true}))
	require.Equal(t, 1, resp.Failed, "%+v", resp.Results)
	require.Contains(t, resp.Results[0].Error, "library.delete")
	require.False(t, stillInTrash(t, f, id))

	resp = NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{id}, Updates: updates, Caller: &TrashGrants{Trash: true}})
	require.Equal(t, 1, resp.Success, "%+v", resp.Results)
	require.True(t, stillInTrash(t, f, id))
}
