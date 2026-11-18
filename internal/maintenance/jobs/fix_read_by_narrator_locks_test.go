// file: internal/maintenance/jobs/fix_read_by_narrator_locks_test.go
// version: 1.0.0
// guid: 5f1c7e0a-93d4-4b6e-8a21-c47d9e3b6f58
// last-edited: 2026-11-18

package jobs_test

import (
	"context"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/require"
)

// readByStore holds four books whose title is "read by <narrator>": a title-locked
// one, a narrator-locked one, an unlocked one and one whose lock read fails.
func readByStore(t *testing.T) (*database.MockStore, *userLockFixture) {
	t.Helper()
	authorID := 2
	stored := map[string]database.Book{}
	cores := make([]database.BookCore, 0, 4)
	for _, id := range []string{"bk-title-locked", "bk-narr-locked", "bk-open", "bk-bad"} {
		a := authorID
		b := database.Book{ID: id, Title: "read by Alfred Pennyworth", AuthorID: &a, FilePath: "/library/Batman/Year One/chapter.mp3"}
		stored[id] = b
		cores = append(cores, b.Core())
	}
	fx := newUserLockFixture()
	fx.locked["bk-title-locked"] = []string{database.FieldKeyTitle}
	fx.locked["bk-narr-locked"] = []string{database.FieldKeyNarrator}
	fx.unreadable["bk-bad"] = true
	st := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) { return cores, nil },
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: "Year One"}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b := stored[id]
			return &b, nil
		},
	}
	fx.install(st)
	return st, fx
}

// The fix rewrites the title and derives the narrator from it, so a user lock on
// either column holds the whole write: neither column changes.
func TestFixReadByNarratorJob_UserLockHoldsTheWholeFix(t *testing.T) {
	st, fx := readByStore(t)
	j, err := maintenance.Get("fix-read-by-narrator")
	require.NoError(t, err)
	rep := &noopReporter{}
	require.NoError(t, j.Run(context.Background(), st, rep, false))

	require.False(t, fx.wrote("bk-title-locked"), "a user-locked title must not be rewritten")
	require.False(t, fx.wrote("bk-narr-locked"), "a user-locked narrator holds the title rewrite too")
	require.True(t, logged(rep, "locked", "bk-title-locked"), "%v", rep.logs)
	require.True(t, logged(rep, "locked", "bk-narr-locked"), "%v", rep.logs)

	// Control: the unlocked sibling is fixed in the same run.
	require.True(t, fx.wrote("bk-open"))
	got := fx.written("bk-open")
	require.Equal(t, "Year One", got.Title)
	require.NotNil(t, got.Narrator)
	require.Equal(t, "Alfred Pennyworth", *got.Narrator)
}

func TestFixReadByNarratorJob_UnreadableLocksWriteNothingForThatBook(t *testing.T) {
	st, fx := readByStore(t)
	j, err := maintenance.Get("fix-read-by-narrator")
	require.NoError(t, err)
	require.NoError(t, j.Run(context.Background(), st, &noopReporter{}, false))

	require.False(t, fx.wrote("bk-bad"), "a book whose locks cannot be read must not be written")
	require.True(t, fx.wrote("bk-open"), "one unreadable book does not stop the run")
}
