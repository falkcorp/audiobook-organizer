// file: internal/merge/finish_carry_test.go
// version: 1.0.0
// guid: 0c6e2f4a-9b1d-4e73-a5c8-7d3f1b2e9a60
// last-edited: 2026-09-13
//
// The real mergeUserProgressFor and writeProgress against a real PebbleStore:
// a carried or restored finish keeps its FinishedAt, and a carried finish
// brings the loser's iTunes play-count mark with it.

package merge

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeUserProgressFor_CarriedFinishKeepsStampAndPlayCountMark(t *testing.T) {
	store := setupTestStore(t)
	loserMark := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	loser, err := store.CreateBook(&database.Book{Title: "Loser", FilePath: "/tmp/loser.m4b", Format: "m4b", ITunesPlayCountBumpedAt: &loserMark})
	require.NoError(t, err)
	winner, err := store.CreateBook(&database.Book{Title: "Winner", FilePath: "/tmp/winner.m4b", Format: "m4b"})
	require.NoError(t, err)

	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: "u1", BookID: loser.ID, Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: time.Now()}))
	loserState, err := store.GetUserBookState("u1", loser.ID)
	require.NoError(t, err)
	require.NotNil(t, loserState.FinishedAt)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: "u1", BookID: winner.ID, Status: database.UserBookStatusInProgress, ProgressPct: 10, LastActivityAt: time.Now()}))

	time.Sleep(5 * time.Millisecond)
	require.NoError(t, mergeUserProgressFor(store, "u1", loser.ID, winner.ID))

	got, err := store.GetUserBookState("u1", winner.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, database.UserBookStatusFinished, got.Status)
	require.NotNil(t, got.FinishedAt)
	assert.True(t, got.FinishedAt.Equal(*loserState.FinishedAt), "carried FinishedAt = %v, want the loser's %v", got.FinishedAt, loserState.FinishedAt)

	w, err := store.GetBookByID(winner.ID)
	require.NoError(t, err)
	require.NotNil(t, w.ITunesPlayCountBumpedAt, "the loser's play-count mark was not carried")
	assert.True(t, w.ITunesPlayCountBumpedAt.Equal(loserMark))
}

func TestMergeUserProgressFor_KeepsWinnersLaterPlayCountMark(t *testing.T) {
	store := setupTestStore(t)
	earlier := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	later := earlier.Add(time.Hour)
	loser, err := store.CreateBook(&database.Book{Title: "Loser", FilePath: "/tmp/loser2.m4b", Format: "m4b", ITunesPlayCountBumpedAt: &earlier})
	require.NoError(t, err)
	winner, err := store.CreateBook(&database.Book{Title: "Winner", FilePath: "/tmp/winner2.m4b", Format: "m4b", ITunesPlayCountBumpedAt: &later})
	require.NoError(t, err)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: "u1", BookID: loser.ID, Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: time.Now()}))

	require.NoError(t, mergeUserProgressFor(store, "u1", loser.ID, winner.ID))

	w, err := store.GetBookByID(winner.ID)
	require.NoError(t, err)
	require.NotNil(t, w.ITunesPlayCountBumpedAt)
	assert.True(t, w.ITunesPlayCountBumpedAt.Equal(later), "an earlier loser mark must not move the winner's back")
}

func TestWriteProgress_RestoreKeepsFinishedAt(t *testing.T) {
	store := setupTestStore(t)
	book, err := store.CreateBook(&database.Book{Title: "Restored", FilePath: "/tmp/restored.m4b", Format: "m4b"})
	require.NoError(t, err)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: "u1", BookID: book.ID, Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: time.Now()}))
	snapshot, err := store.GetUserBookState("u1", book.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.FinishedAt)

	// Drain (nil snapshot over the current row), then restore the snapshot.
	require.NoError(t, writeProgress(store, "u1", book.ID, nil, nil, snapshot))
	drained, err := store.GetUserBookState("u1", book.ID)
	require.NoError(t, err)
	require.NotNil(t, drained)
	require.Nil(t, drained.FinishedAt, "a drained row has no finish")

	time.Sleep(5 * time.Millisecond)
	require.NoError(t, writeProgress(store, "u1", book.ID, snapshot, nil, drained))
	got, err := store.GetUserBookState("u1", book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.FinishedAt)
	assert.True(t, got.FinishedAt.Equal(*snapshot.FinishedAt), "restored FinishedAt = %v, want the snapshot's %v", got.FinishedAt, snapshot.FinishedAt)
}
