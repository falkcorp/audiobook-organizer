// file: internal/merge/service_test.go
// version: 1.1.0
// guid: 9b3d7e21-4a6c-4f08-8e15-7c2a9d4b6e30
// last-edited: 2026-07-13

package merge

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestStore(t *testing.T) database.Store {
	t.Helper()
	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})
	return store
}

func TestService_MergeBooks(t *testing.T) {
	store := setupTestStore(t)

	book1 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Test Book MP3",
		Format:   "mp3",
		FilePath: "/tmp/test1.mp3",
	}
	book2 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Test Book M4B",
		Format:   "m4b",
		FilePath: "/tmp/test2.m4b",
	}

	_, err := store.CreateBook(book1)
	require.NoError(t, err)
	_, err = store.CreateBook(book2)
	require.NoError(t, err)

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{book1.ID, book2.ID}, "")
	require.NoError(t, err)

	assert.Equal(t, 2, result.MergedCount)
	assert.NotEmpty(t, result.VersionGroupID)
	// M4B should be selected as primary since it's the preferred format
	assert.Equal(t, book2.ID, result.PrimaryID)

	// Verify books in database
	b1, err := store.GetBookByID(book1.ID)
	require.NoError(t, err)
	require.NotNil(t, b1.VersionGroupID)
	assert.Equal(t, result.VersionGroupID, *b1.VersionGroupID)
	require.NotNil(t, b1.IsPrimaryVersion)
	assert.False(t, *b1.IsPrimaryVersion)

	b2, err := store.GetBookByID(book2.ID)
	require.NoError(t, err)
	require.NotNil(t, b2.VersionGroupID)
	assert.Equal(t, result.VersionGroupID, *b2.VersionGroupID)
	require.NotNil(t, b2.IsPrimaryVersion)
	assert.True(t, *b2.IsPrimaryVersion)

	// Data-loss invariant: a merge must never leave the store inconsistent
	// (e.g. a book both live-primary and soft-deleted, or a survivor vanished).
	dbtest.AssertStoreInvariants(t, store)
}

func TestService_MergeBooks_ExplicitPrimary(t *testing.T) {
	store := setupTestStore(t)

	book1 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Book A",
		Format:   "mp3",
		FilePath: "/tmp/a.mp3",
	}
	book2 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Book B",
		Format:   "m4b",
		FilePath: "/tmp/b.m4b",
	}

	_, err := store.CreateBook(book1)
	require.NoError(t, err)
	_, err = store.CreateBook(book2)
	require.NoError(t, err)

	ms := NewService(store)
	// Force MP3 as primary even though M4B would normally win
	result, err := ms.MergeBooks([]string{book1.ID, book2.ID}, book1.ID)
	require.NoError(t, err)

	assert.Equal(t, book1.ID, result.PrimaryID)
}

func TestService_MergeBooks_TooFew(t *testing.T) {
	ms := NewService(nil)
	_, err := ms.MergeBooks([]string{"one"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 2")
}

// TestService_MergeBooks_SoftDeletesLosers verifies the
// post-2026-04-11 merge semantics: losers get soft-deleted
// (MarkedForDeletion=true) after merge.
func TestService_MergeBooks_SoftDeletesLosers(t *testing.T) {
	store := setupTestStore(t)

	book1 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Loser MP3",
		Format:   "mp3",
		FilePath: "/tmp/loser.mp3",
	}
	book2 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Winner M4B",
		Format:   "m4b",
		FilePath: "/tmp/winner.m4b",
	}

	_, err := store.CreateBook(book1)
	require.NoError(t, err)
	_, err = store.CreateBook(book2)
	require.NoError(t, err)

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{book1.ID, book2.ID}, "")
	require.NoError(t, err)
	require.Equal(t, book2.ID, result.PrimaryID, "M4B should auto-win")

	// Winner is NOT soft-deleted.
	winner, err := store.GetBookByID(book2.ID)
	require.NoError(t, err)
	require.NotNil(t, winner)
	require.NotNil(t, winner.IsPrimaryVersion)
	assert.True(t, *winner.IsPrimaryVersion)
	if winner.MarkedForDeletion != nil {
		assert.False(t, *winner.MarkedForDeletion, "winner must not be soft-deleted")
	}

	// Loser IS soft-deleted.
	loser, err := store.GetBookByID(book1.ID)
	require.NoError(t, err)
	require.NotNil(t, loser)
	require.NotNil(t, loser.IsPrimaryVersion)
	assert.False(t, *loser.IsPrimaryVersion)
	require.NotNil(t, loser.MarkedForDeletion, "loser must be soft-deleted")
	assert.True(t, *loser.MarkedForDeletion)
	require.NotNil(t, loser.MarkedForDeletionAt, "loser must have deletion timestamp")
	assert.WithinDuration(t, time.Now(), *loser.MarkedForDeletionAt, 5*time.Second)
	require.NotNil(t, loser.VersionGroupID)
	assert.Equal(t, result.VersionGroupID, *loser.VersionGroupID)
}

// TestService_MergeBooks_PrefersCuratedOverPristine verifies that a
// curated book wins the primary slot over a pristine duplicate.
func TestService_MergeBooks_PrefersCuratedOverPristine(t *testing.T) {
	store := setupTestStore(t)

	pristineM4B := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Foundation and Empire",
		Format:   "m4b",
		FilePath: "/mnt/bigdata/books/audiobook-organizer/asimov/foundation-and-empire.m4b",
	}
	highBitrate := 192
	pristineM4B.Bitrate = &highBitrate

	curatedMP3 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Foundation and Empire",
		Format:   "mp3",
		FilePath: "/mnt/bigdata/books/audiobook-organizer/asimov/foundation-and-empire.mp3",
	}
	lowBitrate := 64
	curatedMP3.Bitrate = &lowBitrate

	_, err := store.CreateBook(pristineM4B)
	require.NoError(t, err)
	_, err = store.CreateBook(curatedMP3)
	require.NoError(t, err)

	matched := "matched"
	curatedMP3.MetadataReviewStatus = &matched
	_, err = store.UpdateBook(curatedMP3.ID, curatedMP3)
	require.NoError(t, err)
	require.NoError(t, store.SetLastWrittenAt(curatedMP3.ID, time.Now()))

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{pristineM4B.ID, curatedMP3.ID}, "")
	require.NoError(t, err)

	assert.Equal(t, curatedMP3.ID, result.PrimaryID,
		"curated MP3 should beat pristine M4B — user's work is the strongest signal")
}

func TestBookCurationScore(t *testing.T) {
	matched := "matched"
	noMatch := "no_match"
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)

	cases := []struct {
		name string
		book *database.Book
		want int
	}{
		{"empty", &database.Book{}, 0},
		{"matched only", &database.Book{MetadataReviewStatus: &matched}, 1},
		{"no_match does not count", &database.Book{MetadataReviewStatus: &noMatch}, 0},
		{"last written only", &database.Book{LastWrittenAt: &now}, 1},
		{
			"metadata edited after create",
			&database.Book{CreatedAt: &earlier, MetadataUpdatedAt: &now},
			1,
		},
		{
			"metadata edited at same time as create does not count",
			&database.Book{CreatedAt: &now, MetadataUpdatedAt: &now},
			0,
		},
		{
			"fully curated",
			&database.Book{
				MetadataReviewStatus: &matched,
				LastWrittenAt:        &now,
				CreatedAt:            &earlier,
				MetadataUpdatedAt:    &now,
			},
			3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, BookCurationScore(tc.book))
		})
	}
}

func TestService_MergeBooks_PrefersOrganizedOverITunesGhost(t *testing.T) {
	store := setupTestStore(t)

	ghost := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Foundation and Empire",
		Format:   "m4b",
		FilePath: "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Isaac Asimov/Foundation and Empire.m4b",
	}
	bitrate := 128
	ghost.Bitrate = &bitrate

	organized := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Foundation and Empire",
		Format:   "mp3",
		FilePath: "/mnt/bigdata/books/audiobook-organizer/Isaac Asimov/Foundation and Empire/Foundation and Empire.mp3",
	}
	lowBitrate := 64
	organized.Bitrate = &lowBitrate

	_, err := store.CreateBook(ghost)
	require.NoError(t, err)
	_, err = store.CreateBook(organized)
	require.NoError(t, err)

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{ghost.ID, organized.ID}, "")
	require.NoError(t, err)

	assert.Equal(t, organized.ID, result.PrimaryID,
		"organized library path should beat iTunes ghost regardless of format")
}

func TestIsITunesGhostPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"itunes media absolute", "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/x.m4b", true},
		{"itunes media mixed case", "/mnt/bigdata/books/iTunes/iTunes Media/x.m4b", true},
		{"organized library", "/mnt/bigdata/books/audiobook-organizer/author/book.mp3", false},
		{"empty", "", false},
		{"relative", "itunes/iTunes Media/x.m4b", true},
		{"generic linux tmp", "/tmp/x.mp3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsITunesGhostPath(tc.path))
		})
	}
}

func TestService_MergeBooks_NotFound(t *testing.T) {
	store := setupTestStore(t)

	ms := NewService(store)
	_, err := ms.MergeBooks([]string{"nonexistent1", "nonexistent2"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
