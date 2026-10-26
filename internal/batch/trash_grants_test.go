// file: internal/batch/trash_grants_test.go
// version: 1.2.0
// guid: 22c685fe-e807-4213-a039-f8dece0f9456
// last-edited: 2026-10-25

package batch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// labelTrashed seeds a book a trash path labelled "deleted" without setting the
// deletion flag (database.IsInTrash is true, IsSoftDeleted false).
func labelTrashed(t *testing.T) (*vptest.Fixture, string) {
	t.Helper()
	f := vptest.New(t)
	id := f.Book(t, vptest.Spec{ID: "lt", Primary: "nil", State: "deleted"})
	b, err := f.S.GetBookByID(id)
	require.NoError(t, err)
	require.True(t, database.IsInTrash(b))
	require.False(t, b.IsSoftDeleted(), "fixture: the row is in the trash by label only")
	return f, id
}

func stillInTrash(t *testing.T, f *vptest.Fixture, id string) bool {
	t.Helper()
	b, err := f.S.GetBookByID(id)
	require.NoError(t, err)
	return database.IsInTrash(b)
}

func libraryState(t *testing.T, f *vptest.Fixture, id string) string {
	t.Helper()
	b, err := f.S.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b.LibraryState)
	return *b.LibraryState
}

// A library_state edit that takes a book out of the trash is a restore, whatever
// spelling the request has and whichever route carries it: the row is judged
// after it is loaded, not by the request's shape.
func TestBatchUpdate_LibraryStateOnATrashedRowIsARestore(t *testing.T) {
	for _, state := range []string{"organized", "imported", "", " Organized "} {
		t.Run("without library.organize: "+state, func(t *testing.T) {
			f, id := labelTrashed(t)
			resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
				IDs: []string{id}, Updates: map[string]any{"library_state": state},
				Caller: &TrashGrants{Trash: true},
			})
			require.Equal(t, 1, resp.Failed)
			require.Equal(t, 0, resp.Success)
			require.Contains(t, resp.Results[0].Error, "library.organize")
			require.True(t, stillInTrash(t, f, id), "nothing is written")
			require.Equal(t, "deleted", libraryState(t, f, id))
		})
	}
	t.Run("with library.organize it goes through", func(t *testing.T) {
		f, id := labelTrashed(t)
		resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
			IDs: []string{id}, Updates: map[string]any{"library_state": "organized"},
			Caller: &TrashGrants{Restore: true},
		})
		require.Equal(t, 1, resp.Success, "%+v", resp.Results)
		require.False(t, stillInTrash(t, f, id))
		require.Equal(t, "organized", libraryState(t, f, id))
	})
	t.Run("a row that is not in the trash is a plain edit", func(t *testing.T) {
		f := vptest.New(t)
		id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})
		resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
			IDs: []string{id}, Updates: map[string]any{"library_state": "imported", "title": "T"},
			Caller: &TrashGrants{},
		})
		require.Equal(t, 1, resp.Success, "%+v", resp.Results)
		require.Equal(t, "imported", libraryState(t, f, id))
	})
	t.Run("no caller fails closed: it holds neither grant", func(t *testing.T) {
		f, id := labelTrashed(t)
		resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
			IDs: []string{id}, Updates: map[string]any{"library_state": "organized"},
		})
		require.Equal(t, 1, resp.Failed, "%+v", resp.Results)
		require.Contains(t, resp.Results[0].Error, "library.organize")
		require.True(t, stillInTrash(t, f, id), "nothing is written")
	})
}

// A request that reaches the service with no Caller at all is judged as a
// caller holding neither permission: trash-state changes are refused in both
// directions, and edits that leave the trash alone are not affected.
func TestNilCallerFailsClosed(t *testing.T) {
	t.Run("trashing", func(t *testing.T) {
		for _, updates := range []map[string]any{
			{"marked_for_deletion": true},
			{"library_state": "deleted"},
		} {
			f := vptest.New(t)
			id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})
			resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{id}, Updates: updates})
			require.Equal(t, 1, resp.Failed, "%v: %+v", updates, resp.Results)
			require.Contains(t, resp.Results[0].Error, "library.delete")
			require.False(t, stillInTrash(t, f, id))
		}
	})
	t.Run("restoring by clearing the deletion flag", func(t *testing.T) {
		f, id := trashedLikeDeleteAudiobook(t)
		resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
			IDs: []string{id}, Updates: map[string]any{"marked_for_deletion": false}})
		require.Equal(t, 1, resp.Failed, "%+v", resp.Results)
		require.Contains(t, resp.Results[0].Error, "library.organize")
		require.True(t, stillInTrash(t, f, id))

		resp2 := NewBatchService(f.S).ExecuteOperations(&BatchOperationsRequest{Operations: []BatchOperationItem{{
			ID: id, Action: "update", Updates: map[string]any{"marked_for_deletion": false}}}})
		require.Equal(t, 1, resp2.Failed, "%+v", resp2.Results)
		require.True(t, stillInTrash(t, f, id))
	})
	t.Run("operations route", func(t *testing.T) {
		f, id := labelTrashed(t)
		resp := NewBatchService(f.S).ExecuteOperations(&BatchOperationsRequest{Operations: []BatchOperationItem{{
			ID: id, Action: "update", Updates: map[string]any{"library_state": "organized"}}}})
		require.Equal(t, 1, resp.Failed, "%+v", resp.Results)
		require.True(t, stillInTrash(t, f, id))
	})
	t.Run("an edit that leaves the trash alone is unaffected", func(t *testing.T) {
		f := vptest.New(t)
		id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})
		resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
			IDs: []string{id}, Updates: map[string]any{"title": "The Long Halloween"}})
		require.Equal(t, 1, resp.Success, "%+v", resp.Results)
	})
}

// Clearing the deletion flag of a book trashed by the flag alone carries no
// library_state, so only the row's before/after comparison can see the restore.
func TestBatchUpdate_ClearingTheDeletionFlagNeedsOrganize(t *testing.T) {
	f, id := trashedLikeDeleteAudiobook(t)
	// Strip the label so the row is in the trash by its flag alone.
	_, err := f.S.ModifyBook(id, func(b *database.Book) error { b.LibraryState = nil; return nil })
	require.NoError(t, err)
	b, err := f.S.GetBookByID(id)
	require.NoError(t, err)
	require.True(t, b.IsSoftDeleted() && database.IsInTrash(b), "fixture: in the trash by the flag alone")

	resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{id}, Updates: map[string]any{"marked_for_deletion": false},
		Caller: &TrashGrants{Trash: true}})
	require.Equal(t, 1, resp.Failed, "%+v", resp.Results)
	require.Contains(t, resp.Results[0].Error, "library.organize")
	require.True(t, stillInTrash(t, f, id))

	resp = NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{id}, Updates: map[string]any{"marked_for_deletion": false},
		Caller: &TrashGrants{Restore: true}})
	require.Equal(t, 1, resp.Success, "%+v", resp.Results)
	require.False(t, stillInTrash(t, f, id))
}

// A trashed book whose deletion flag is set stays in the trash when only its
// label changes, but touching the label of a book in the trash is still the
// restore's business: a label set ahead of the restore would decide the state
// the restore keeps.
func TestBatchUpdate_LibraryStateOnAFlaggedTrashedRowNeedsOrganize(t *testing.T) {
	f, id := trashedLikeDeleteAudiobook(t)
	resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{id}, Updates: map[string]any{"library_state": "imported"},
		Caller: &TrashGrants{Trash: true},
	})
	require.Equal(t, 1, resp.Failed, "%+v", resp.Results)
	require.Equal(t, "deleted", libraryState(t, f, id))

	resp = NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{id}, Updates: map[string]any{"library_state": "imported"},
		Caller: &TrashGrants{Restore: true},
	})
	require.Equal(t, 1, resp.Success, "%+v", resp.Results)
}

func TestBatchOperations_UpdateLibraryStateOnATrashedRowIsARestore(t *testing.T) {
	ops := func(id string) *BatchOperationsRequest {
		return &BatchOperationsRequest{Operations: []BatchOperationItem{{
			ID: id, Action: "update", Updates: map[string]any{"library_state": "organized"}}}}
	}
	f, id := labelTrashed(t)
	req := ops(id)
	req.Caller = &TrashGrants{Trash: true}
	resp := NewBatchService(f.S).ExecuteOperations(req)
	require.Equal(t, 1, resp.Failed, "%+v", resp.Results)
	require.Contains(t, resp.Results[0].Error, "library.organize")
	require.True(t, stillInTrash(t, f, id))

	req = ops(id)
	req.Caller = &TrashGrants{Restore: true}
	resp = NewBatchService(f.S).ExecuteOperations(req)
	require.Equal(t, 1, resp.Success, "%+v", resp.Results)
	require.False(t, stillInTrash(t, f, id))
}

// The handler already asks for library.delete when a payload trashes books;
// the service judges the row too, so a request that reaches it without the
// grant trashes nothing.
func TestBatchUpdate_TrashingARowNeedsTheTrashGrant(t *testing.T) {
	for _, updates := range []map[string]any{
		{"marked_for_deletion": true},
		{"library_state": "deleted"},
	} {
		f := vptest.New(t)
		id := f.Book(t, vptest.Spec{ID: "live", Primary: "nil"})
		resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
			IDs: []string{id}, Updates: updates, Caller: &TrashGrants{Restore: true}})
		require.Equal(t, 1, resp.Failed, "%v: %+v", updates, resp.Results)
		require.Contains(t, resp.Results[0].Error, "library.delete")
		require.False(t, stillInTrash(t, f, id))

		resp = NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
			IDs: []string{id}, Updates: updates, Caller: &TrashGrants{Trash: true}})
		require.Equal(t, 1, resp.Success, "%v: %+v", updates, resp.Results)
		require.True(t, stillInTrash(t, f, id))
	}
}
