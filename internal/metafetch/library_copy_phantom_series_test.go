// file: internal/metafetch/library_copy_phantom_series_test.go
// version: 1.0.0
// guid: 6b1e9d34-2c70-4a85-9f13-e47a0c58d296
// last-edited: 2026-11-18

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// An original that names a series which no longer exists (older builds left
// such links) must not stop the library copy from following its metadata. The
// store refuses a book write that links a missing series, and the sync used to
// lay the original's series_id on the copy, so the whole sync was refused: the
// apply reported success, the copy kept its old title and description, and
// every later sync failed the same way.

// seedPhantomSeries makes book name a series id that has no row, the way an
// older build left it.
func seedPhantomSeries(t *testing.T, store *database.PebbleStore, bookID string, phantom int) {
	t.Helper()
	_, err := store.SeedLegacyBookRowForTest(bookID, func(b *database.Book) error { b.SeriesID = &phantom; return nil })
	require.NoError(t, err)
}

func TestSyncMetadataToLibraryCopy_AnOriginalWithADeletedSeriesStillSyncsTheRest(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()

	original, err := store.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: "/w/yo-orig"})
	require.NoError(t, err)
	copyBook, err := store.CreateBook(&database.Book{Title: "stale title", FilePath: "/lib/yo-copy"})
	require.NoError(t, err)
	seedPhantomSeries(t, store, original.ID, 4711)
	desc := "Bruce Wayne returns to Gotham."
	_, err = store.ModifyBook(original.ID, func(b *database.Book) error { b.Description = &desc; return nil })
	require.NoError(t, err)
	fresh, err := store.GetBookByID(original.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh.SeriesID, "precondition: the original names the deleted series")

	svc := NewService(store)
	require.NoError(t, svc.syncMetadataToLibraryCopy(fresh, copyBook))

	got, err := store.GetBookByID(copyBook.ID)
	require.NoError(t, err)
	require.Equal(t, "Batman: Year One", got.Title, "the rest of the metadata must land")
	require.NotNil(t, got.Description)
	require.Equal(t, desc, *got.Description)
	require.Nil(t, got.SeriesID, "the copy gets no series rather than a link to a deleted one")
}

// A sync that is refused for another reason is returned, not swallowed.
func TestSyncMetadataToLibraryCopy_ReportsAVanishedCopy(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	original, err := store.CreateBook(&database.Book{Title: "Hush", FilePath: "/w/hush"})
	require.NoError(t, err)

	err = NewService(store).syncMetadataToLibraryCopy(original, &database.Book{ID: "no-such-copy"})
	require.Error(t, err)
}

// End to end: the copy ensureLibraryCopy creates from an original with a deleted
// series is synced on the very first sync, which used to be the first failure.
func TestEnsureLibraryCopy_AnOriginalWithADeletedSeriesGetsASyncedCopy(t *testing.T) {
	store, book, _, _ := libraryCopyFixture(t)
	seedPhantomSeries(t, store, book.ID, 4712)
	desc := "A long night in Arkham."
	_, err := store.ModifyBook(book.ID, func(b *database.Book) error { b.Description = &desc; return nil })
	require.NoError(t, err)
	orig, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, orig.SeriesID)

	copyBook := NewService(store).ensureLibraryCopy(orig)
	require.NotNil(t, copyBook)

	got, err := store.GetBookByID(copyBook.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Description, "the first sync after the copy was made must not be refused")
	require.Equal(t, desc, *got.Description)
	require.Nil(t, got.SeriesID)
}
