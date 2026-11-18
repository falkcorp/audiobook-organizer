// file: internal/maintenance/jobs/fix_author_narrator_swap_locks_test.go
// version: 1.0.0
// guid: 1070c36a-61ac-42e9-bea3-d242f8c8ec5f
// last-edited: 2026-11-18

package jobs_test

import (
	"context"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/require"
)

// swapStore holds three books whose narrator equals the author's name: bk-locked
// (author user-locked), bk-open (nothing locked) and bk-bad (lock read fails).
func swapStore(t *testing.T) (*database.MockStore, *userLockFixture) {
	t.Helper()
	authorID := 7
	name := "Bruce Wayne"
	stored := map[string]database.Book{}
	cores := make([]database.BookCore, 0, 3)
	for _, id := range []string{"bk-locked", "bk-open", "bk-bad"} {
		a, n := authorID, name
		b := database.Book{ID: id, Title: "Year One", AuthorID: &a, Narrator: &n}
		stored[id] = b
		cores = append(cores, b.Core())
	}
	fx := newUserLockFixture()
	fx.locked["bk-locked"] = []string{database.FieldKeyAuthorName}
	fx.unreadable["bk-bad"] = true
	st := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return cores, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: name}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b := stored[id]
			return &b, nil
		},
	}
	fx.install(st)
	return st, fx
}

func TestFixAuthorNarratorSwapJob_UserLockedAuthorIsNotCleared(t *testing.T) {
	st, fx := swapStore(t)
	j, err := maintenance.Get("fix-author-narrator-swap")
	require.NoError(t, err)
	rep := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), st, rep, false))

	require.False(t, fx.wrote("bk-locked"), "a user-locked author must not be cleared")
	require.True(t, logged(rep, "locked", "bk-locked"), "the held book is reported: %v", rep.logs)

	// Control: the unlocked sibling in the same run is still fixed, so the
	// assertion above is not satisfied by a job that writes nothing.
	require.True(t, fx.wrote("bk-open"))
	got := fx.written("bk-open")
	require.Nil(t, got.AuthorID)
}

func TestFixAuthorNarratorSwapJob_UnreadableLocksWriteNothingForThatBook(t *testing.T) {
	st, fx := swapStore(t)
	j, err := maintenance.Get("fix-author-narrator-swap")
	require.NoError(t, err)
	rep := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), st, rep, false))

	require.False(t, fx.wrote("bk-bad"), "a book whose locks cannot be read must not be written")
	require.True(t, logged(rep, "bk-bad"), "the failure is reported: %v", rep.logs)
	require.True(t, fx.wrote("bk-open"), "one unreadable book does not stop the run")
}
