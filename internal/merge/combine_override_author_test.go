// file: internal/merge/combine_override_author_test.go
// version: 1.1.0
// guid: 1e933924-4b94-4b6c-9a57-613b0274c40c
// last-edited: 2026-10-18

package merge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// overrideJournalStore fails every combine-journal write that carries an
// override (failOverrideJournal), and checks, at the author-link write, that
// the journal on disk already names the author id being linked.
type overrideJournalStore struct {
	*database.PebbleStore
	failOverrideJournal bool
	// failAuthorIDJournal fails only the journal write that carries the new
	// author's id (the one after the author row is created); the plan write
	// before it, which names no id, succeeds.
	failAuthorIDJournal bool
	// watchLinks: at each single-author link write, record whether some
	// combine journal on disk already names that author as AuthorIDAfter.
	watchLinks bool
	idOnDisk   []bool
}

func (s *overrideJournalStore) SetRaw(key string, value []byte) error {
	if s.failOverrideJournal && strings.HasPrefix(key, combineJournalPrefix) && strings.Contains(string(value), `"override"`) {
		return errors.New("injected override journal failure")
	}
	if s.failAuthorIDJournal && strings.HasPrefix(key, combineJournalPrefix) && strings.Contains(string(value), `"author_id_after"`) {
		return errors.New("injected author id journal failure")
	}
	return s.PebbleStore.SetRaw(key, value)
}

func (s *overrideJournalStore) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	if s.watchLinks && len(authors) == 1 {
		found := false
		rows, err := s.PebbleStore.ScanPrefix(combineJournalPrefix)
		if err == nil {
			for _, r := range rows {
				var j CombineJournal
				if json.Unmarshal(r.Value, &j) == nil && j.Override != nil && j.Override.AuthorIDAfter != nil &&
					*j.Override.AuthorIDAfter == authors[0].AuthorID {
					found = true
				}
			}
		}
		s.idOnDisk = append(s.idOnDisk, found)
	}
	return s.PebbleStore.SetBookAuthors(bookID, authors)
}

func overrideBooks(t *testing.T, store *database.PebbleStore) (string, string) {
	t.Helper()
	keep, gone := ulid.Make().String(), ulid.Make().String()
	for _, id := range []string{keep, gone} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "B", Format: "mp3", FilePath: fmt.Sprintf("/tmp/override-author/%s.mp3", id)})
		require.NoError(t, err)
	}
	return keep, gone
}

// NIT 7 (#4064 round 3): an override whose plan cannot be journaled is
// skipped, and a new author it names is never created. The plan used to
// create the author first, leaving a row nothing links to and no undo removes.
func TestCombineOverride_SkippedOverrideCreatesNoAuthor(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	ws := &overrideJournalStore{PebbleStore: store, failOverrideJournal: true}
	res, err := NewService(ws).CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: "Brand New Writer"}, CombineOptions{})
	require.NoError(t, err)
	require.Contains(t, strings.Join(res.Warnings, "\n"), "metadata override not applied")
	a, err := store.GetAuthorByName("Brand New Writer")
	require.NoError(t, err)
	require.Nil(t, a, "a skipped override creates no author row")
}

// A new author is created after the plan is journaled, and its id reaches
// the journal on disk before any author link names it, so a crash between
// the two leaves nothing for the undo to miss.
func TestCombineOverride_NewAuthorIDJournaledBeforeItIsLinked(t *testing.T) {
	store := setupTestStore(t).(*database.PebbleStore)
	keep, gone := overrideBooks(t, store)
	ws := &overrideJournalStore{PebbleStore: store, watchLinks: true}
	ms := NewService(ws)
	res, err := ms.CombineBooksWithOptions([]string{keep, gone}, keep, &CombineOverride{Author: "Brand New Writer"}, CombineOptions{})
	require.NoError(t, err)
	require.Equal(t, []bool{true}, ws.idOnDisk, "the author link is written once, after its id is on disk")
	a, err := store.GetAuthorByName("Brand New Writer")
	require.NoError(t, err)
	require.NotNil(t, a, "the applied override created the author")
	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	require.NotNil(t, j.Override.AuthorIDAfter)
	require.Equal(t, a.ID, *j.Override.AuthorIDAfter)
}
