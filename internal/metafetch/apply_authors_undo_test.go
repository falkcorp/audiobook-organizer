// file: internal/metafetch/apply_authors_undo_test.go
// version: 1.0.0
// guid: 8b4f2c61-3e9a-4d07-a5c2-6f1d9e8b3a47
// last-edited: 2026-09-14

package metafetch

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// interleavingStore is a real PebbleStore that runs onLookup once, the first
// time the named author is looked up. The lookup happens inside an apply after
// the apply has read the book but before it writes the author credit, so the
// hook lands a second apply's commit in exactly that window.
type interleavingStore struct {
	*database.PebbleStore
	mu       sync.Mutex
	trigger  string
	onLookup func()
}

func (s *interleavingStore) GetAuthorByName(name string) (*database.Author, error) {
	s.mu.Lock()
	hook := s.onLookup
	if hook != nil && strings.EqualFold(name, s.trigger) {
		s.onLookup = nil
	} else {
		hook = nil
	}
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return s.PebbleStore.GetAuthorByName(name)
}

func authorIDSet(t *testing.T, store database.Store, bookID string) map[int]bool {
	t.Helper()
	got, err := store.GetBookAuthors(bookID)
	require.NoError(t, err)
	out := map[int]bool{}
	for _, ba := range got {
		out[ba.AuthorID] = true
	}
	return out
}

// coAuthoredBook creates a book credited to [A, B] and returns it with the
// ids of A and B.
func coAuthoredBook(t *testing.T, store *database.PebbleStore, n int) (*database.Book, int, int) {
	t.Helper()
	a, err := store.CreateAuthor("Author A")
	require.NoError(t, err)
	b, err := store.CreateAuthor("Author B")
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{
		Title: fmt.Sprintf("Shared Book %d", n), FilePath: fmt.Sprintf("/library/undo-%d.m4b", n), Format: "m4b", AuthorID: &a.ID,
	})
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{
		{BookID: book.ID, AuthorID: a.ID, Role: "author", Position: 0},
		{BookID: book.ID, AuthorID: b.ID, Role: "author", Position: 1},
	}))
	return book, a.ID, b.ID
}

// The reviewer's repro (#3410): the book starts [A, B]. Apply2 reads the book,
// then apply1 lands and adds C ([A, B, C]), then apply2 adds D ([A, B, C, D]).
// Undoing apply2 must remove only D. Recording apply2's "before" credits from a
// read taken outside the lock stored [A, B], and undo wrote that list back
// wholesale, deleting C, which apply2 never touched.
func TestUndoLastApply_KeepsAnotherAppliesAuthorCredit(t *testing.T) {
	pebble, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pebble.Close() })
	store := &interleavingStore{PebbleStore: pebble, trigger: "Author D"}
	svc := NewService(store)

	book, aID, bID := coAuthoredBook(t, pebble, 0)
	store.onLookup = func() {
		_, err := svc.ApplyMetadataCandidate(book.ID, MetadataCandidate{Title: book.Title, Author: "Author C", Source: "audible"}, nil)
		require.NoError(t, err, "apply1")
	}
	_, err = svc.ApplyMetadataCandidate(book.ID, MetadataCandidate{Title: book.Title, Author: "Author D", Source: "audible"}, nil)
	require.NoError(t, err, "apply2")

	c, err := pebble.GetAuthorByName("Author C")
	require.NoError(t, err)
	d, err := pebble.GetAuthorByName("Author D")
	require.NoError(t, err)
	require.Equal(t, map[int]bool{aID: true, bID: true, c.ID: true, d.ID: true}, authorIDSet(t, pebble, book.ID), "both applies must land first")

	_, err = svc.UndoLastApply(book.ID)
	require.NoError(t, err, "undo apply2")
	require.Equal(t, map[int]bool{aID: true, bID: true, c.ID: true}, authorIDSet(t, pebble, book.ID),
		"undoing apply2 must remove only D; C belongs to apply1")
}

// Undo races a concurrent credit write on the same book: undoing an apply that
// added D while someone else adds E must remove D and keep E. A wholesale
// SetBookAuthors(previous) could land after E and delete it. Many rounds, and
// run under -race.
func TestUndoLastApply_ConcurrentCreditAddSurvives(t *testing.T) {
	pebble, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pebble.Close() })
	svc := NewService(pebble)

	const rounds = 30
	lost := 0
	for r := 0; r < rounds; r++ {
		book, aID, bID := coAuthoredBook(t, pebble, 100+r)
		_, err := svc.ApplyMetadataCandidate(book.ID, MetadataCandidate{Title: book.Title, Author: fmt.Sprintf("Author D%d", r), Source: "audible"}, nil)
		require.NoError(t, err)
		e, err := pebble.CreateAuthor(fmt.Sprintf("Author E%d", r))
		require.NoError(t, err)

		start := make(chan struct{})
		var wg sync.WaitGroup
		var undoErr, addErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, undoErr = svc.UndoLastApply(book.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, addErr = pebble.ModifyBookAuthors(book.ID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
				return append(cur, database.BookAuthor{BookID: book.ID, AuthorID: e.ID, Role: "author", Position: 99}), nil
			})
		}()
		close(start)
		wg.Wait()
		require.NoError(t, addErr)
		require.NoError(t, undoErr)

		got := authorIDSet(t, pebble, book.ID)
		for _, id := range []int{aID, bID, e.ID} {
			if !got[id] {
				lost++
				t.Errorf("round %d: credit %d lost after undo; join=%v", r, id, got)
			}
		}
	}
	require.Zero(t, lost, "undo lost %d credit(s) in %d rounds", lost, rounds)
}
