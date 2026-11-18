// file: internal/reconcile/merge_dupe_stored_row_test.go
// version: 1.0.2
// guid: 2f7d4c8a-6e1b-4a93-b5d0-9c3e8f2a1d67
// last-edited: 2026-11-18

package reconcile

import (
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A1#15 (#3435 review): "fill where empty" must be decided on the STORED row.
// The hydrated copy of the winner has no narrator; by the time the merge
// writes, another writer has filled it. The dupe's narrator must not win.
func TestMergeDupeIntoStoredRow_FillsOnlyWhatIsEmptyOnTheStoredRow(t *testing.T) {
	var mu sync.Mutex
	concurrent := "Concurrent Narrator"
	stored := &database.Book{ID: "P", Title: "Winner", Narrator: &concurrent}
	m := &database.MockStore{}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		mu.Lock()
		defer mu.Unlock()
		if id != stored.ID {
			return nil, nil
		}
		cp := *stored
		return &cp, nil
	}
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		mu.Lock()
		defer mu.Unlock()
		cp := *b
		stored = &cp
		return b, nil
	}
	m.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) { return nil, nil }

	cached := &database.Book{ID: "P", Title: "Winner"} // hydrated before the concurrent write
	dupeNarrator, dupePublisher := "Dupe Narrator", "Dupe Press"
	dupe := &database.Book{ID: "D", Title: "Winner", Narrator: &dupeNarrator, Publisher: &dupePublisher}

	written, merged, err := mergeDupeIntoStoredRow(m, cached, dupe, "test")
	require.NoError(t, err)
	require.NotNil(t, written)
	require.NotNil(t, written.Narrator)
	assert.Equal(t, "Concurrent Narrator", *written.Narrator, "the narrator filled concurrently must survive the merge")
	require.NotNil(t, written.Publisher)
	assert.Equal(t, "Dupe Press", *written.Publisher, "a column empty on the stored row is filled from the dupe")
	assert.NotContains(t, merged, database.FieldKeyNarrator)
	assert.Contains(t, merged, "publisher")
}

// The dupe can name a series that was deleted (older builds left such links).
// The fill used to lay it on the winner and the store, which refuses a link to a
// series that has no row, failed the whole merge, so the pair was never merged.
// The winner now gets everything else and no series, and a series the dupe has
// that still exists is filled as before.
func TestMergeDupeIntoStoredRow_ADupeWithADeletedSeriesStillMergesTheRest(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()

	winner, err := store.CreateBook(&database.Book{Title: "Batman: Hush", FilePath: "/w/hush-a"})
	require.NoError(t, err)
	dupe, err := store.CreateBook(&database.Book{Title: "Batman: Hush", FilePath: "/w/hush-b"})
	require.NoError(t, err)
	press := "DC Press"
	gone := 4713
	_, err = store.SeedLegacyBookRowForTest(dupe.ID, func(b *database.Book) error { b.SeriesID = &gone; b.Publisher = &press; return nil })
	require.NoError(t, err)
	dupe, err = store.GetBookByID(dupe.ID)
	require.NoError(t, err)
	require.NotNil(t, dupe.SeriesID, "precondition: the dupe names the deleted series")

	written, merged, err := mergeDupeIntoStoredRow(store, winner, dupe, "test")
	require.NoError(t, err, "a dupe's deleted series must not fail the merge")
	require.NotNil(t, written)
	require.NotNil(t, written.Publisher)
	assert.Equal(t, "DC Press", *written.Publisher, "the rest of the dupe still merges")
	assert.Nil(t, written.SeriesID, "the winner gets no series rather than a link to a deleted one")
	assert.Contains(t, merged, "publisher")
	assert.NotContains(t, merged, "series_id", "a dropped fill is not reported as merged")

	live, err := store.CreateSeries("Hush", nil)
	require.NoError(t, err)
	other, err := store.CreateBook(&database.Book{Title: "Batman: Hush", FilePath: "/w/hush-c"})
	require.NoError(t, err)
	_, err = store.ModifyBook(dupe.ID, func(b *database.Book) error { b.SeriesID = &live.ID; return nil })
	require.NoError(t, err)
	dupe, err = store.GetBookByID(dupe.ID)
	require.NoError(t, err)
	written, merged, err = mergeDupeIntoStoredRow(store, other, dupe, "test")
	require.NoError(t, err)
	require.NotNil(t, written.SeriesID, "a series that exists is still filled")
	assert.Equal(t, live.ID, *written.SeriesID)
	assert.Contains(t, merged, "series_id")
}
