// file: internal/merge/legacy_guard_parity_test.go
// version: 1.0.1
// guid: 5e0c7a41-3b92-4d68-a1f4-8c2d6b9e0f73
// last-edited: 2026-10-17

package merge

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin, against the live merge.Service path, the two refusals the
// retired dedup.MergeBooks path (guardKeeperAudioRoute and retireMergedLoser)
// made before it was deleted. They exist so that deleting the legacy code
// cannot silently lose either guard.

// seedPathBook seeds a book whose audio is path, with one book_file row.
func seedPathBook(t *testing.T, store database.Store, title, path string) *database.Book {
	t.Helper()
	b := &database.Book{ID: ulid.Make().String(), Title: title, Format: "mp3", FilePath: path}
	_, err := store.CreateBook(b)
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		ID: ulid.Make().String(), BookID: b.ID, FilePath: path, Format: "mp3",
	}))
	return b
}

// Legacy guardKeeperAudioRoute: a keeper with no audio route (no book_file
// rows and an empty FilePath) while a loser has audio is refused with the
// typed FilelessPrimaryError, before any write. Overlaps with
// TestMergeBooks_ExplicitFilelessPrimary_Refused (service_guards_test.go),
// which covers the same refusal from the generic F1 angle.
func TestLegacyParity_FilelessKeeperRefused(t *testing.T) {
	store := setupTestStore(t)
	keeper := seedGuardBook(t, store, "Parity Ghost Keeper", "m4b", false)
	loser := seedGuardBook(t, store, "Parity Audio Loser", "mp3", true)

	res, err := NewService(store).MergeBooks([]string{keeper.ID, loser.ID}, keeper.ID)
	require.Error(t, err)
	assert.Nil(t, res)

	var fe *FilelessPrimaryError
	require.True(t, errors.As(err, &fe), "want *FilelessPrimaryError, got %T: %v", err, err)
	assert.True(t, IsRefusal(err), "a fileless-primary refusal must satisfy IsRefusal")
	assert.Equal(t, keeper.ID, fe.PrimaryID)
	assert.Equal(t, []string{loser.ID}, fe.FileBearing)

	for _, id := range []string{keeper.ID, loser.ID} {
		assert.False(t, softDeleted(t, store, id), "refused merge must not retire %s", id)
	}
}

// Legacy retireMergedLoser shared-path rule: a loser whose audio path equals,
// or lies inside, the keeper's (or the keeper's lies inside the loser's) must
// not be retired, because a purge with file deletion would remove audio the
// keeper reaches. The live path enforces this behind
// MergeOptions.RefuseSharedAudioPaths, the option the iTunes heal sets.
func TestLegacyParity_SharedAudioPathLoserNotRetired(t *testing.T) {
	cases := []struct {
		name                 string
		keeperPath, lossPath string
	}{
		{"equal path", "/lib/parity/shared.mp3", "/lib/parity/shared.mp3"},
		{"loser inside keeper directory", "/lib/parity/dir", "/lib/parity/dir/part1.mp3"},
		{"keeper inside loser directory", "/lib/parity/dir2/part1.mp3", "/lib/parity/dir2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := setupTestStore(t)
			keeper := seedPathBook(t, store, "Parity Keeper", tc.keeperPath)
			loser := seedPathBook(t, store, "Parity Loser", tc.lossPath)

			res, err := NewService(store).MergeBooksWithOptions(
				[]string{keeper.ID, loser.ID}, keeper.ID, MergeOptions{RefuseSharedAudioPaths: true})
			require.Error(t, err)
			assert.Nil(t, res)

			var se *SharedAudioPathError
			require.True(t, errors.As(err, &se), "want *SharedAudioPathError, got %T: %v", err, err)
			assert.Equal(t, loser.ID, se.LoserID)
			assert.Equal(t, keeper.ID, se.SurvivorID)

			// Nothing was retired: neither row is soft-deleted, so the keeper's
			// audio is not on the purge clock.
			for _, id := range []string{keeper.ID, loser.ID} {
				assert.False(t, softDeleted(t, store, id), "refused merge must not retire %s", id)
			}
		})
	}
}

// Control: with disjoint paths the same options merge normally, so the
// refusal above is the shared-path rule and not a blanket failure. The second
// case is a sibling directory whose name merely starts with the keeper's
// directory name; it is not "inside" it and must not be refused.
func TestLegacyParity_DisjointPathsStillMerge(t *testing.T) {
	cases := []struct {
		name                 string
		keeperPath, lossPath string
	}{
		{"unrelated directories", "/lib/parity/a/one.mp3", "/lib/parity/b/two.mp3"},
		{"sibling with shared name prefix", "/lib/parity/dir/one.mp3", "/lib/parity/dir2/two.mp3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := setupTestStore(t)
			keeper := seedPathBook(t, store, "Parity Keeper", tc.keeperPath)
			loser := seedPathBook(t, store, "Parity Loser", tc.lossPath)

			res, err := NewService(store).MergeBooksWithOptions(
				[]string{keeper.ID, loser.ID}, keeper.ID, MergeOptions{RefuseSharedAudioPaths: true})
			require.NoError(t, err)
			assert.Equal(t, keeper.ID, res.PrimaryID)
			assert.True(t, softDeleted(t, store, loser.ID))
			assert.False(t, softDeleted(t, store, keeper.ID))
		})
	}
}
