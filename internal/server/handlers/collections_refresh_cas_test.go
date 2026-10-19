// file: internal/server/handlers/collections_refresh_cas_test.go
// version: 1.0.0
// guid: 8c41e0b9-6d27-4a53-b1f8-2e9d7a35c640
// last-edited: 2026-10-18

package handlers_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// newRefreshHandler is a handler over store with an open index holding b1 to b3
// and the refresh fixtures' listening states.
func newRefreshHandler(t *testing.T, store handlers.CollectionStore) *handlers.CollectionHandler {
	t.Helper()
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.MarkRebuilt())
	require.NoError(t, idx.IndexBookBatch([]search.BookDocument{
		{BookID: "b1", Title: "Alpha"}, {BookID: "b2", Title: "Bravo"}, {BookID: "b3", Title: "Charlie"},
	}))
	return handlers.NewCollectionHandler(store, nil, &colFakeUsers{list: ownerUsers()},
		&colFakeBooks{states: refreshStates()}, func() *search.BleveIndex { return idx })
}

// editedAfterListFake edits every collection (a new query and the Version bump a
// real write makes) right after ListCollections hands the refresh its copy, the
// way a person saving the collection between the list and the refresh's write
// would. The edit goes straight into the fake's rows.
type editedAfterListFake struct{ *colFakeStore }

func (e editedAfterListFake) ListCollections(ct string, l, o int) ([]database.Collection, int, error) {
	cols, n, err := e.colFakeStore.ListCollections(ct, l, o)
	out := append([]database.Collection(nil), cols...)
	e.colFakeStore.mu.Lock()
	for i := range e.colFakeStore.cols {
		e.colFakeStore.cols[i].Query = "last_played:>-1d title:UserEdited"
		e.colFakeStore.cols[i].Version++
	}
	e.colFakeStore.mu.Unlock()
	return out, n, err
}

// TestRefreshRelativeDynamic_NeverOverwritesAConcurrentEdit is the test the
// compare-and-swap needs: the fake store enforces Version like the real one, so a
// refresh that re-read the row and copied its Version before writing (which turns
// the CAS into a blind overwrite) would clobber the edit and fail here.
func TestRefreshRelativeDynamic_NeverOverwritesAConcurrentEdit(t *testing.T) {
	store := refreshStore()
	h := newRefreshHandler(t, editedAfterListFake{store})

	res, err := h.RefreshRelativeDynamic(context.Background(), false, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Conflicts, "the two evaluable collections were edited under the refresh")
	assert.Zero(t, res.Refreshed)
	for _, c := range store.cols {
		if c.Type != database.CollectionTypeDynamic {
			continue
		}
		assert.Equal(t, "last_played:>-1d title:UserEdited", c.Query, "%s: the user's edit survived", c.ID)
		assert.Equal(t, []string{"b9"}, c.MaterializedBookIDs, "%s: members computed from the old query were not stored", c.ID)
	}
}

// editingPebble is the same race against the real store.
type editingPebble struct {
	*database.PebbleStore
	t *testing.T
}

func (e editingPebble) ListCollections(ct string, l, o int) ([]database.Collection, int, error) {
	cols, n, err := e.PebbleStore.ListCollections(ct, l, o)
	for i := range cols {
		cur, gerr := e.PebbleStore.GetCollection(cols[i].ID)
		require.NoError(e.t, gerr)
		cur.Query = "last_played:>-1d title:UserEdited"
		require.NoError(e.t, e.PebbleStore.UpdateCollection(cur))
	}
	return cols, n, err
}

func TestRefreshRelativeDynamic_RealStoreKeepsAConcurrentEdit(t *testing.T) {
	ps, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	_, err = ps.CreateCollection(&database.Collection{Name: "rel", Type: database.CollectionTypeDynamic,
		Query: "last_played:>-30d", CreatedByUserID: "alice", MaterializedBookIDs: []string{"b9"}})
	require.NoError(t, err)

	h := newRefreshHandler(t, editingPebble{ps, t})
	res, err := h.RefreshRelativeDynamic(context.Background(), false, nil)
	require.NoError(t, err)

	cols, _, err := ps.ListCollections("", 0, 0)
	require.NoError(t, err)
	require.Len(t, cols, 1)
	assert.Equal(t, "last_played:>-1d title:UserEdited", cols[0].Query, "the user's edit was clobbered")
	assert.Equal(t, 1, res.Conflicts)
}
