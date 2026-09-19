// file: internal/merge/combine_progress_partial_test.go
// version: 1.0.0
// guid: 1e5a9c7d-2b48-4f36-8d0e-b7c4f2a9e613
// last-edited: 2026-09-19

package merge

import (
	"fmt"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// corruptUserPositionsStore makes one user's position rows undecodable, the
// way a corrupt upos: row reads since the store stopped skipping them.
type corruptUserPositionsStore struct {
	database.Store
	userID string
}

func (s corruptUserPositionsStore) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	if userID == s.userID {
		return nil, fmt.Errorf("%w: planted for user %s", database.ErrUserPositionUndecodable, userID)
	}
	return s.Store.ListUserPositionsForBook(userID, bookID)
}

// One user's corrupt position row must not cost every OTHER user their undo:
// readable users are journaled and followed; the unreadable user is skipped
// (rows left in place) and never moved unjournaled.
func TestCombine_OneUsersCorruptPositionsDoNotBlockOthersJournal(t *testing.T) {
	store := setupTestStore(t)
	surv, abs := ulid.Make().String(), ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: surv, Title: "Survivor", Format: "m4b", FilePath: "/tmp/pp/s.m4b"})
	require.NoError(t, err)
	_, err = store.CreateBook(&database.Book{ID: abs, Title: "Absorbed", Format: "mp3", FilePath: "/tmp/pp/a.mp3"})
	require.NoError(t, err)
	userA, err := store.CreateUser("reader-a", "a@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	userB, err := store.CreateUser("reader-b", "b@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	for _, u := range []string{userA.ID, userB.ID} {
		require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: u, BookID: abs, Status: "in_progress", ProgressPct: 40}))
		require.NoError(t, store.SetUserPosition(u, abs, "seg-a", 1234))
	}
	preB := fmt.Sprint(progressView(t, store, userB.ID, abs))

	ms := NewService(corruptUserPositionsStore{Store: store, userID: userA.ID})
	ms.SetSyncFollower(database.AsSyncIdentityStore(store))
	res, err := ms.CombineBooks([]string{surv, abs}, surv, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.JournalID)

	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.Len(t, j.Absorbed, 1)
	var journaled []string
	for _, p := range j.Absorbed[0].Progress {
		journaled = append(journaled, p.UserID)
	}
	assert.Equal(t, []string{userB.ID}, journaled, "B journaled, A (unreadable) not")
	assert.NotEmpty(t, j.Warnings, "the skipped user is reported")

	_, _, bSurv := progressView(t, store, userB.ID, surv)
	assert.Equal(t, []string{"seg-a@1234"}, bSurv, "B's progress followed onto the survivor")
	aPos, err := store.ListUserPositionsForBook(userA.ID, abs)
	require.NoError(t, err)
	assert.Len(t, aPos, 1, "A's rows left in place on the absorbed book")
	aSurv, err := store.ListUserPositionsForBook(userA.ID, surv)
	require.NoError(t, err)
	assert.Empty(t, aSurv, "A's progress never moved unjournaled")

	_, err = ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, preB, fmt.Sprint(progressView(t, store, userB.ID, abs)), "undo restores B")
	aPos, err = store.ListUserPositionsForBook(userA.ID, abs)
	require.NoError(t, err)
	assert.Len(t, aPos, 1, "A untouched by undo")
}
