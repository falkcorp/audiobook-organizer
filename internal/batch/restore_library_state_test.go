// file: internal/batch/restore_library_state_test.go
// version: 1.0.0
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
