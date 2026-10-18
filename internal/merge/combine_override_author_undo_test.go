// file: internal/merge/combine_override_author_undo_test.go
// version: 1.0.0
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

const createdAuthorName = "Brand New Writer"

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

func requireAuthorLive(t *testing.T, store *database.PebbleStore, id int, live bool) {
	t.Helper()
	got, err := store.GetAuthorByID(id)
	require.NoError(t, err)
	if live {
		require.NotNil(t, got)
		require.Equal(t, id, got.ID)
		return
	}
	require.True(t, got == nil || got.ID != id, "author %d is gone", id)
}

// An undo removes the author the combine created once nothing credits it, and
// running the removal again is a no-op.
func TestUndoCombine_RemovesAuthorTheCombineCreated(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	ms, keep, a, res := combineCreatingAuthor(t, store)

	undo, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Empty(t, undo.Warnings, "an uncredited created author is removed without a warning")

	got, err := store.GetAuthorByName(createdAuthorName)
	require.NoError(t, err)
	require.Nil(t, got, "the author the combine created is removed")
	requireAuthorLive(t, store, a.ID, false)
	authors, err := store.GetBookAuthors(keep)
	require.NoError(t, err)
	require.Empty(t, authors, "the survivor is back to the credits it had")
	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.Nil(t, b.AuthorID)

	kept, err := ms.removeUncreditedAuthor(a.ID)
	require.NoError(t, err)
	require.Empty(t, kept, "a row already gone is not an error")
}

// A book that credits the author after the combine keeps it: through the
// junction, through the legacy AuthorID alone, or as a narrator. The warning
// names the author and why it stayed, and the credit is untouched.
func TestUndoCombine_KeepsCreatedAuthorStillCredited(t *testing.T) {
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
			joined := strings.Join(undo.Warnings, "\n")
			require.Contains(t, joined, "was kept")
			require.Contains(t, joined, "still credited")

			requireAuthorLive(t, store, a.ID, true)
			if tc.junction {
				links, err := store.GetBookAuthors(later)
				require.NoError(t, err)
				require.Len(t, links, 1)
				require.Equal(t, a.ID, links[0].AuthorID)
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

// An alias added to the author after the combine is somebody's work that
// DeleteAuthor would cascade away: the author is kept.
func TestUndoCombine_KeepsCreatedAuthorWithAnAlias(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	ms, _, a, res := combineCreatingAuthor(t, store)
	_, err := store.CreateAuthorAlias(a.ID, "B. New Writer", "pen_name")
	require.NoError(t, err)

	undo, err := ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(undo.Warnings, "\n"), "alias")
	requireAuthorLive(t, store, a.ID, true)
	aliases, err := store.GetAuthorAliases(a.ID)
	require.NoError(t, err)
	require.Len(t, aliases, 1)
}

// A journal from before AuthorCreated existed, and an override that linked an
// author that already existed, both delete nothing.
func TestUndoCombine_DeletesNoAuthorItDidNotCreate(t *testing.T) {
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
		requireAuthorLive(t, store, a.ID, true)
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
		requireAuthorLive(t, store, existing.ID, true)
	})
}

// An author merged into another is gone under its own id (GetAuthorByID
// answers with the canonical row): removing it touches neither the canonical
// author nor its credits.
func TestRemoveUncreditedAuthor_MergedAwayAuthorIsLeftAlone(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	merged, err := store.CreateAuthor("Merged Away Writer")
	require.NoError(t, err)
	canonical, err := store.CreateAuthor("Canonical Writer")
	require.NoError(t, err)
	later := creditLaterBook(t, store, canonical, true, true)
	_, err = store.MergeDeleteAuthor(merged.ID, canonical.ID)
	require.NoError(t, err)

	kept, err := NewService(store).removeUncreditedAuthor(merged.ID)
	require.NoError(t, err)
	require.Empty(t, kept)
	requireAuthorLive(t, store, canonical.ID, true)
	links, err := store.GetBookAuthors(later)
	require.NoError(t, err)
	require.Len(t, links, 1, "the canonical author's credit survives")
}

// deleteFailStore fails DeleteAuthor.
type deleteFailStore struct {
	*database.PebbleStore
}

func (deleteFailStore) DeleteAuthor(int) error { return errors.New("injected DeleteAuthor failure") }

// A removal that fails does not fail the undo: the survivor is restored, and
// the warning says the row was left.
func TestUndoCombine_AuthorDeleteFailureIsAWarning(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	_, keep, a, res := combineCreatingAuthor(t, store)

	undo, err := NewService(deleteFailStore{store}).UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(undo.Warnings, "\n"), "left in place")
	requireAuthorLive(t, store, a.ID, true)
	authors, err := store.GetBookAuthors(keep)
	require.NoError(t, err)
	require.Empty(t, authors, "the survivor is restored all the same")
}

// Gap 1: when the journal write that carries the new author's id fails, the
// author row just created is taken back, and the override is skipped.
func TestCombineOverride_FailedAuthorIDJournalLeavesNoAuthor(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	ws := &overrideJournalStore{PebbleStore: store, failAuthorIDJournal: true}
	ms := NewService(ws)
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: createdAuthorName}, CombineOptions{})
	require.NoError(t, err)

	a, err := store.GetAuthorByName(createdAuthorName)
	require.NoError(t, err)
	require.Nil(t, a, "the author created before the failed journal write is removed again")
	b, err := store.GetBookByID(keep)
	require.NoError(t, err)
	require.Nil(t, b.AuthorID, "the override was skipped")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	if j.Override != nil {
		require.Nil(t, j.Override.AuthorIDAfter)
		require.False(t, j.Override.AuthorCreated)
	}
}

// The same orphan after the id was journaled: a failed link write drops the
// override, and the author the combine created goes with it.
func TestCombineOverride_FailedLinkRemovesTheCreatedAuthor(t *testing.T) {
	real := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, real)
	ms := NewService(&b3FaultStore{Store: real, failSetBookAuthors: true})
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: createdAuthorName}, CombineOptions{})
	require.NoError(t, err)

	a, err := real.GetAuthorByName(createdAuthorName)
	require.NoError(t, err)
	require.Nil(t, a, "a dropped override leaves no author row behind")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.NotNil(t, j.Override)
	require.Nil(t, j.Override.AuthorIDAfter)
	require.False(t, j.Override.AuthorCreated)
}
