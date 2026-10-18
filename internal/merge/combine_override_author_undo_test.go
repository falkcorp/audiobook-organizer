// file: internal/merge/combine_override_author_undo_test.go
// version: 2.0.0
// guid: 7b2d94e1-5c63-4f08-a1d7-3e90c4b6a258
// last-edited: 2026-10-18

package merge

import (
	"errors"
	"strings"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The combine and its undo never delete an author (createdAuthorNote has the
// design decision). These tests pin that: every case asserts the author row
// the combine created is still there, and that the warning naming
// maintenance.purge-empty-authors appears exactly when no book credits it.

const createdAuthorName = "Brand New Writer"

// uncreditedNote is the text of the warning about a created author no book
// credits any more.
const uncreditedNote = "was created by this combine and is no longer credited on any book"

// combineCreatingAuthor combines two books with an override naming a new
// author and returns the service, the survivor, the author the combine
// created and the combine result.
func combineCreatingAuthor(t *testing.T, store *database.PebbleStore) (*Service, string, *database.Author, *CombineResult) {
	t.Helper()
	keep, gone := overrideBooks(t, store)
	ms := NewService(store)
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: createdAuthorName}, CombineOptions{})
	require.NoError(t, err)
	a, err := store.GetAuthorByName(createdAuthorName)
	require.NoError(t, err)
	require.NotNil(t, a, "the applied override created the author")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.NotNil(t, j.Override)
	require.True(t, j.Override.AuthorCreated, "the journal records that the combine created the author")
	require.Equal(t, a.ID, *j.Override.AuthorIDAfter)
	return ms, keep, a, res
}

// creditLaterBook adds a book after the combine that credits the author.
func creditLaterBook(t *testing.T, store *database.PebbleStore, a *database.Author, junction, scalar bool) string {
	t.Helper()
	id := ulid.Make().String()
	b := &database.Book{ID: id, Title: "Later", Format: "mp3", FilePath: "/tmp/override-author/later-" + id + ".mp3"}
	if scalar {
		aid := a.ID
		b.AuthorID = &aid
	}
	_, err := store.CreateBook(b)
	require.NoError(t, err)
	if junction {
		require.NoError(t, store.SetBookAuthors(id, []database.BookAuthor{{BookID: id, AuthorID: a.ID, Role: "author"}}))
	}
	return id
}

func requireAuthorLive(t *testing.T, store *database.PebbleStore, id int) {
	t.Helper()
	got, err := store.GetAuthorByID(id)
	require.NoError(t, err)
	require.NotNil(t, got, "author %d is never deleted by a combine or its undo", id)
	require.Equal(t, id, got.ID)
}

// An undo leaves the author the combine created in place and says that no
// book credits it any more and purge-empty-authors can remove it. Running the
// undo's note again answers the same.
func TestUndoCombine_LeavesTheAuthorTheCombineCreatedAndWarns(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	ms, keep, a, res := combineCreatingAuthor(t, store)

	undo, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	joined := strings.Join(undo.Warnings, "\n")
	require.Contains(t, joined, uncreditedNote)
	require.Contains(t, joined, "maintenance.purge-empty-authors")
	require.Contains(t, joined, createdAuthorName)

	requireAuthorLive(t, store, a.ID)
	authors, err := store.GetBookAuthors(keep)
	require.NoError(t, err)
	require.Empty(t, authors, "the survivor is back to the credits it had")
	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.Nil(t, b.AuthorID)

	require.Len(t, ms.createdAuthorNote(a.ID, createdAuthorName), 1, "the note is a read: asking again answers the same")
	requireAuthorLive(t, store, a.ID)
}

// A book that credits the author after the combine (through the junction,
// through the legacy AuthorID alone, or as a narrator) means it is in use:
// no purge note, and the credit is untouched.
func TestUndoCombine_CreatedAuthorStillCreditedGetsNoNote(t *testing.T) {
	cases := []struct {
		name             string
		junction, scalar bool
		role             string
	}{
		{"junction", true, false, "author"},
		{"legacy author id only", false, true, "author"},
		{"narrator credit", true, false, "narrator"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := setupTestStore(t).(*database.PebbleStore)
			ms, _, a, res := combineCreatingAuthor(t, store)
			later := creditLaterBook(t, store, a, tc.junction, tc.scalar)
			if tc.role != "author" {
				require.NoError(t, store.SetBookAuthors(later, []database.BookAuthor{{BookID: later, AuthorID: a.ID, Role: tc.role}}))
			}

			undo, err := ms.UndoCombine(res.JournalID)
			require.NoError(t, err)
			require.NotContains(t, strings.Join(undo.Warnings, "\n"), "purge-empty-authors")

			requireAuthorLive(t, store, a.ID)
			if tc.junction {
				links, err := store.GetBookAuthors(later)
				require.NoError(t, err)
				require.Len(t, links, 1)
				require.Equal(t, a.ID, links[0].AuthorID)
				require.Equal(t, tc.role, links[0].Role)
			}
			if tc.scalar {
				b, err := store.GetBookByID(later)
				require.NoError(t, err)
				require.NotNil(t, b.AuthorID)
				require.Equal(t, a.ID, *b.AuthorID)
			}
		})
	}
}

// A journal from before AuthorCreated existed, and an override that linked an
// author that already existed, get no note: the combine did not create them.
func TestUndoCombine_NoNoteForAnAuthorItDidNotCreate(t *testing.T) {
	t.Run("journal without the field", func(t *testing.T) {
		store := setupTestStore(t).(*database.PebbleStore)
		ms, _, a, res := combineCreatingAuthor(t, store)
		j, err := ms.GetCombineJournal(res.JournalID)
		require.NoError(t, err)
		j.Override.AuthorCreated = false
		require.NoError(t, ms.putCombineJournal(j))
		raw, err := store.GetRaw(combineJournalPrefix + res.JournalID)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "author_created", "the stored journal reads as one written before the field")

		undo, err := ms.UndoCombine(res.JournalID)
		require.NoError(t, err)
		require.Empty(t, undo.Warnings)
		requireAuthorLive(t, store, a.ID)
	})
	t.Run("author already existed", func(t *testing.T) {
		store := setupTestStore(t).(*database.PebbleStore)
		existing, err := store.CreateAuthor(createdAuthorName)
		require.NoError(t, err)
		keep, gone := overrideBooks(t, store)
		ms := NewService(store)
		res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: createdAuthorName}, CombineOptions{})
		require.NoError(t, err)
		j, err := ms.GetCombineJournal(res.JournalID)
		require.NoError(t, err)
		require.NotNil(t, j.Override)
		require.Equal(t, existing.ID, *j.Override.AuthorIDAfter)
		require.False(t, j.Override.AuthorCreated)

		undo, err := ms.UndoCombine(res.JournalID)
		require.NoError(t, err)
		require.Empty(t, undo.Warnings)
		requireAuthorLive(t, store, existing.ID)
	})
}

// creditCheckFailStore fails the credit lookup createdAuthorNote asks.
type creditCheckFailStore struct {
	*database.PebbleStore
}

func (creditCheckFailStore) GetBooksByAuthorIDForRelinkCore(int) ([]database.BookCore, error) {
	return nil, errors.New("injected credit lookup failure")
}

// A credit check that cannot be answered does not fail the undo: the survivor
// is restored, the author stays, and the warning says the check failed.
func TestUndoCombine_UncheckableCreditIsAWarning(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	_, keep, a, res := combineCreatingAuthor(t, store)

	undo, err := NewService(creditCheckFailStore{store}).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(undo.Warnings, "\n"), "could not be checked")
	requireAuthorLive(t, store, a.ID)
	authors, err := store.GetBookAuthors(keep)
	require.NoError(t, err)
	require.Empty(t, authors, "the survivor is restored all the same")
}

// requireDroppedOverrideNotes asserts the combine result names the dropped
// author override and the uncredited author it created.
func requireDroppedOverrideNotes(t *testing.T, warnings []string) {
	t.Helper()
	joined := strings.Join(warnings, "\n")
	require.Contains(t, joined, `author override "`+createdAuthorName+`" not applied`)
	require.Contains(t, joined, uncreditedNote)
	require.Contains(t, joined, "maintenance.purge-empty-authors")
}

// When the journal write that carries the new author's id fails, the override
// is skipped, the author row just created stays, and the combine result says
// both (round-4 N5: these were log lines only).
func TestCombineOverride_FailedAuthorIDJournalKeepsAuthorAndWarns(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	ws := &overrideJournalStore{PebbleStore: store, failAuthorIDJournal: true}
	ms := NewService(ws)
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: createdAuthorName}, CombineOptions{})
	require.NoError(t, err)
	requireDroppedOverrideNotes(t, res.Warnings)

	a, err := store.GetAuthorByName(createdAuthorName)
	require.NoError(t, err)
	require.NotNil(t, a, "the author created before the failed journal write is left for purge-empty-authors")
	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.Nil(t, b.AuthorID, "the override was skipped")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	if j.Override != nil {
		require.Nil(t, j.Override.AuthorIDAfter)
		require.False(t, j.Override.AuthorCreated)
	}
	require.Contains(t, strings.Join(j.Warnings, "\n"), uncreditedNote, "the journal keeps the warning too")
}

// The same after the id was journaled: a failed link write drops the
// override, the author the combine created stays, and the result says so.
func TestCombineOverride_FailedLinkKeepsAuthorAndWarns(t *testing.T) {
	real := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, real)
	ms := NewService(&b3FaultStore{Store: real, failSetBookAuthors: true})
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: createdAuthorName}, CombineOptions{})
	require.NoError(t, err)
	requireDroppedOverrideNotes(t, res.Warnings)

	a, err := real.GetAuthorByName(createdAuthorName)
	require.NoError(t, err)
	require.NotNil(t, a, "a dropped override leaves its author for purge-empty-authors")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.NotNil(t, j.Override)
	require.Nil(t, j.Override.AuthorIDAfter)
	require.False(t, j.Override.AuthorCreated)
}
