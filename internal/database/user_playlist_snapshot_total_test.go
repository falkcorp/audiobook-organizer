// file: internal/database/user_playlist_snapshot_total_test.go
// version: 1.0.0
// guid: 7a3c9e51-2b84-4d06-8f1a-c5e0d4b69a72
// last-edited: 2026-10-26

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A cut snapshot (a static playlist made from a smart one that matched more than
// MaxMaterializedMembers books) keeps the real count in MaterializedTotal. The count
// describes the books the snapshot was made from, so the store drops it when the
// books it holds change, by any route, and keeps it when they do not.
func TestUpdateUserPlaylist_ASnapshotTotalEndsWhenItsBooksChange(t *testing.T) {
	ids := make([]string, 5)
	for i := range ids {
		ids[i] = fmt.Sprintf("s%05d", i)
	}
	cut := func(t *testing.T, store Store) *UserPlaylist {
		t.Helper()
		pl, err := store.CreateUserPlaylist(&UserPlaylist{
			Name: "Knightfall", Type: UserPlaylistTypeStatic, CreatedByUserID: "u1",
			BookIDs: ids, MaterializedTotal: 12000,
		})
		require.NoError(t, err)
		return pl
	}
	update := func(t *testing.T, store Store, id string, mutate func(*UserPlaylist)) *UserPlaylist {
		t.Helper()
		pl, err := store.GetUserPlaylist(id)
		require.NoError(t, err)
		mutate(pl)
		require.NoError(t, store.UpdateUserPlaylist(pl))
		got, err := store.GetUserPlaylist(id)
		require.NoError(t, err)
		return got
	}

	t.Run("a rename and a reorder keep the total", func(t *testing.T) {
		store, cleanup := setupPebbleTestDB(t)
		defer cleanup()
		pl := cut(t, store)
		got := update(t, store, pl.ID, func(p *UserPlaylist) { p.Name = "Hush" })
		assert.Equal(t, 12000, got.MaterializedTotal)
		got = update(t, store, pl.ID, func(p *UserPlaylist) { p.BookIDs = []string{ids[4], ids[3], ids[2], ids[1], ids[0]} })
		assert.Equal(t, 12000, got.MaterializedTotal, "the same books in another order")
	})

	t.Run("an add ends it", func(t *testing.T) {
		store, cleanup := setupPebbleTestDB(t)
		defer cleanup()
		pl := cut(t, store)
		got := update(t, store, pl.ID, func(p *UserPlaylist) { p.BookIDs = append(p.BookIDs, "s99999") })
		assert.Zero(t, got.MaterializedTotal)
	})

	t.Run("a remove ends it", func(t *testing.T) {
		store, cleanup := setupPebbleTestDB(t)
		defer cleanup()
		pl := cut(t, store)
		got := update(t, store, pl.ID, func(p *UserPlaylist) { p.BookIDs = p.BookIDs[1:] })
		assert.Zero(t, got.MaterializedTotal)
	})

	t.Run("an add and a remove together, which keep the length, end it", func(t *testing.T) {
		store, cleanup := setupPebbleTestDB(t)
		defer cleanup()
		pl := cut(t, store)
		got := update(t, store, pl.ID, func(p *UserPlaylist) {
			p.BookIDs = append(append([]string{}, p.BookIDs[1:]...), "s99999")
		})
		require.Len(t, got.BookIDs, len(ids), "the length did not change")
		assert.Zero(t, got.MaterializedTotal)
	})
}

func TestSameBookSet(t *testing.T) {
	assert.True(t, sameBookSet(nil, nil))
	assert.True(t, sameBookSet([]string{"a", "b"}, []string{"b", "a"}))
	assert.False(t, sameBookSet([]string{"a", "b"}, []string{"a", "c"}))
	assert.False(t, sameBookSet([]string{"a", "a"}, []string{"a", "b"}), "a repeated id is not a different one")
	assert.False(t, sameBookSet([]string{"a"}, []string{"a", "b"}))
}
