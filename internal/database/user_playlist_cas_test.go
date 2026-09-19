// file: internal/database/user_playlist_cas_test.go
// version: 1.0.0
// guid: ea68480d-5643-4659-be0a-53f661c7cd00
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestPlaylist(t *testing.T, store Store, name, owner string) *UserPlaylist {
	t.Helper()
	pl, err := store.CreateUserPlaylist(&UserPlaylist{Name: name, Type: UserPlaylistTypeStatic, CreatedByUserID: owner})
	require.NoError(t, err)
	return pl
}

// A write based on a stale read must be refused, not silently win.
func TestUpdateUserPlaylist_StaleVersionConflicts(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	pl := newTestPlaylist(t, store, "Mine", "u1")

	a, err := store.GetUserPlaylist(pl.ID)
	require.NoError(t, err)
	b, err := store.GetUserPlaylist(pl.ID)
	require.NoError(t, err)

	a.BookIDs = []string{"book-a"}
	require.NoError(t, store.UpdateUserPlaylist(a))
	b.Name = "Renamed"
	err = store.UpdateUserPlaylist(b)
	require.True(t, errors.Is(err, ErrUserPlaylistVersionConflict), "stale write must conflict, got %v", err)

	got, err := store.GetUserPlaylist(pl.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"book-a"}, got.BookIDs, "the first writer's change was lost")
}

// The review's scenario: an ABS batch add and a native rename racing on one
// playlist must BOTH land. Driven through the retry helper both surfaces use.
func TestUpdateUserPlaylistWithRetry_ConcurrentEditsCompose(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	pl := newTestPlaylist(t, store, "Race", "u1")

	const adders = 6
	var wg sync.WaitGroup
	errs := make(chan error, adders+1)
	for i := range adders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := UpdateUserPlaylistWithRetry(store, pl.ID, func(p *UserPlaylist) error {
				p.BookIDs = append(p.BookIDs, fmt.Sprintf("book-%02d", i))
				return nil
			})
			errs <- err
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := UpdateUserPlaylistWithRetry(store, pl.ID, func(p *UserPlaylist) error {
			p.Name = "Race renamed"
			return nil
		})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		// With a bounded retry a pathological storm could give up; with 7
		// writers and a process-wide write lock each retry wins quickly.
		require.NoError(t, err)
	}

	got, err := store.GetUserPlaylist(pl.ID)
	require.NoError(t, err)
	require.Equal(t, "Race renamed", got.Name)
	require.Len(t, got.BookIDs, adders, "an add was erased by a concurrent whole-record write: %v", got.BookIDs)
	for i := range adders {
		require.True(t, slices.Contains(got.BookIDs, fmt.Sprintf("book-%02d", i)))
	}
}

// The name index is global: a rename onto a taken name must be refused (it
// used to repoint the index and orphan the other playlist), with a message that
// names neither the other playlist nor its owner.
func TestUpdateUserPlaylist_RenameOntoTakenNameConflicts(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	theirs := newTestPlaylist(t, store, "Bedtime", "other-user")
	mine := newTestPlaylist(t, store, "Commute", "u1")

	mine.Name = "bedtime" // normalized collision
	err := store.UpdateUserPlaylist(mine)
	require.True(t, errors.Is(err, ErrUserPlaylistNameInUse), "got %v", err)
	require.NotContains(t, err.Error(), "other-user")
	require.NotContains(t, err.Error(), theirs.ID)

	byName, err := store.GetUserPlaylistByName("Bedtime")
	require.NoError(t, err)
	require.NotNil(t, byName)
	require.Equal(t, theirs.ID, byName.ID, "the other playlist's name index entry was repointed")

	_, err = store.CreateUserPlaylist(&UserPlaylist{Name: "BEDTIME", Type: UserPlaylistTypeStatic, CreatedByUserID: "u1"})
	require.True(t, errors.Is(err, ErrUserPlaylistNameInUse), "create collision got %v", err)
}
