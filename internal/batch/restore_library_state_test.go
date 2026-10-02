// file: internal/batch/restore_library_state_test.go
// version: 1.1.0
// guid: 5c054407-02a8-465f-bb5a-c509387c20b3
// last-edited: 2026-10-01

package batch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// trashedLikeDeleteAudiobook seeds an organized book inside the library root
// that DeleteAudiobook's soft delete trashed: label "deleted", and (as every
// row trashed before 2026-10-01) no recorded pre-trash state. It is also a
// merge loser of a live survivor.
func trashedLikeDeleteAudiobook(t *testing.T) (*vptest.Fixture, string) {
	t.Helper()
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	survivor := f.Book(t, vptest.Spec{ID: "survivor", Primary: "nil"})
	id := f.Book(t, vptest.Spec{ID: "b", Primary: "nil"})
	_, err := f.S.ModifyBook(id, func(b *database.Book) error {
		v, now, deleted := true, time.Now(), "deleted"
		b.MarkedForDeletion, b.MarkedForDeletionAt, b.LibraryState = &v, &now, &deleted
		b.MergedIntoBookID = &survivor
		return nil
	})
	require.NoError(t, err)
	return f, id
}

func requireRestoredVisible(t *testing.T, f *vptest.Fixture, id string) {
	t.Helper()
	b, err := f.S.GetBookByID(id)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted())
	require.NotNil(t, b.LibraryState)
	require.Equal(t, "organized", *b.LibraryState,
		"a restored book must not keep the trash label (DeleteAudiobook then refuses to delete it again, and ABS never lists it)")
	require.Nil(t, b.MergedIntoBookID)
	require.True(t, database.ABSLibraryFilter().Matches(b))
}

// The bulk restore cleared only the trash bits, so a book DeleteAudiobook had
// labelled "deleted" came back live but still labelled "deleted".
func TestBatchRestore_UsesTheSharedRestoreRule(t *testing.T) {
	f, id := trashedLikeDeleteAudiobook(t)
	resp := NewBatchService(f.S).ExecuteOperations(&BatchOperationsRequest{
		Operations: []BatchOperationItem{{ID: id, Action: "restore"}}})
	require.Equal(t, 1, resp.Success)
	requireRestoredVisible(t, f, id)
}

// marked_for_deletion=false through a batch update is a restore too.
func TestBatchUpdate_UnmarkDeletionUsesTheSharedRestoreRule(t *testing.T) {
	f, id := trashedLikeDeleteAudiobook(t)
	resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{id}, Updates: map[string]any{"marked_for_deletion": false}})
	require.Equal(t, 1, resp.Success)
	requireRestoredVisible(t, f, id)
}

// liveGroupWithNilSibling seeds a group whose primary was never trashed and a
// nil-flag sibling (the member Incumbent names when the primary is left out).
// There is no library root, so the hand-off after a batch edit holds the group
// and a yield written by the edit is what stays.
func liveGroupWithNilSibling(t *testing.T) (*vptest.Fixture, string, string) {
	t.Helper()
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	prim := f.Book(t, vptest.Spec{ID: "prim", Group: "g", Primary: "true"})
	sib := f.Book(t, vptest.Spec{ID: "sib", Group: "g", Primary: "nil"})
	config.AppConfig.RootDir = ""
	return f, prim, sib
}

// Adversarial review of #3649, finding 1: the "restore" action skipped only a
// row with an explicit false flag, so a live row with a nil flag fell through
// to the restore and its yield: a never-trashed primary was demoted.
func TestBatchRestore_NeverTrashedPrimaryIsLeftAlone(t *testing.T) {
	f, prim, sib := liveGroupWithNilSibling(t)
	before, err := f.S.GetBookByID(prim)
	require.NoError(t, err)
	require.Nil(t, before.MarkedForDeletion, "precondition: a nil deletion flag")

	resp := NewBatchService(f.S).ExecuteOperations(&BatchOperationsRequest{
		Operations: []BatchOperationItem{{ID: prim, Action: "restore"}}})
	require.Equal(t, 1, resp.Success)

	require.Equal(t, "true", f.Flag(t, prim), "a never-trashed primary keeps its flag")
	require.Equal(t, "nil", f.Flag(t, sib))
	after, err := f.S.GetBookByID(prim)
	require.NoError(t, err)
	require.Nil(t, after.MarkedForDeletion, "a restore of a live row writes nothing")
}

// The same through a batch update: marked_for_deletion=false on a live
// primary is not a restore, so it does not yield the flag.
func TestBatchUpdate_UnmarkDeletionOnLivePrimaryKeepsItsFlag(t *testing.T) {
	f, prim, _ := liveGroupWithNilSibling(t)
	resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{prim}, Updates: map[string]any{"marked_for_deletion": false}})
	require.Equal(t, 1, resp.Success)
	require.Equal(t, "true", f.Flag(t, prim), "a never-trashed primary keeps its flag")

	resp = NewBatchService(f.S).ExecuteOperations(&BatchOperationsRequest{
		Operations: []BatchOperationItem{{ID: prim, Action: "update", Updates: map[string]any{"marked_for_deletion": false}}}})
	require.Equal(t, 1, resp.Success)
	require.Equal(t, "true", f.Flag(t, prim), "a never-trashed primary keeps its flag")
}

// Adversarial review of #3649, finding 6: a batch update writing
// library_state="deleted" overwrote the state without recording it, so a
// later restore fell back to the file-location guess.
func TestBatchUpdate_DeletedLabelRecordsThePreTrashState(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	// "imported" inside the root: the fallback would wrongly say organized.
	id := f.Book(t, vptest.Spec{ID: "b", Primary: "nil", State: "imported"})

	resp := NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
		IDs: []string{id}, Updates: map[string]any{"library_state": "deleted", "marked_for_deletion": true}})
	require.Equal(t, 1, resp.Success)
	b, err := f.S.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b.PreTrashLibraryState, "the overwritten state is recorded")
	require.Equal(t, "imported", *b.PreTrashLibraryState)

	resp = NewBatchService(f.S).ExecuteOperations(&BatchOperationsRequest{
		Operations: []BatchOperationItem{{ID: id, Action: "restore"}}})
	require.Equal(t, 1, resp.Success)
	b, err = f.S.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, "imported", *b.LibraryState, "the recorded state comes back, not the fallback's guess")
}
