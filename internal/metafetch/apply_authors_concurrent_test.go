// file: internal/metafetch/apply_authors_concurrent_test.go
// version: 1.0.0
// guid: 5d2e9b41-7a3c-4f18-b6d0-2c8e4f1a9b73
// last-edited: 2026-09-14

package metafetch

import (
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/require"
)

// Two fill-only applies to the SAME book, each naming a different new author,
// must both land. With a caller-side GetBookAuthors -> merge -> SetBookAuthors
// both read [A, B], and the second write drops the first's author: a lost
// co-author. The add has to be one atomic read-merge-write in the store.
//
// Run against a real PebbleStore (the MockStore has no concurrency story) for
// many rounds so the interleaving is hit, and under -race in CI.
func TestApplyMetadataToBook_ConcurrentFillOnlyAppliesKeepBothAuthors(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(store)

	a, err := store.CreateAuthor("Author A")
	require.NoError(t, err)
	b, err := store.CreateAuthor("Author B")
	require.NoError(t, err)

	const rounds = 40
	lost := 0
	for r := 0; r < rounds; r++ {
		book, err := store.CreateBook(&database.Book{
			Title: fmt.Sprintf("Book %d", r), FilePath: fmt.Sprintf("/library/%d.m4b", r), Format: "m4b", AuthorID: &a.ID,
		})
		require.NoError(t, err)
		require.NoError(t, store.SetBookAuthors(book.ID, []database.BookAuthor{
			{BookID: book.ID, AuthorID: a.ID, Role: "author", Position: 0},
			{BookID: book.ID, AuthorID: b.ID, Role: "author", Position: 1},
		}))

		names := []string{fmt.Sprintf("Author C%d", r), fmt.Sprintf("Author D%d", r)}
		wantIDs := map[int]bool{a.ID: true, b.ID: true}
		for _, n := range names {
			au, err := store.CreateAuthor(n)
			require.NoError(t, err)
			wantIDs[au.ID] = true
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, len(names))
		for i, n := range names {
			wg.Add(1)
			go func(i int, n string) {
				defer wg.Done()
				working := *book
				<-start
				_, errs[i] = svc.ApplyMetadataToBook(&working, metadata.BookMetadata{Author: n})
			}(i, n)
		}
		close(start)
		wg.Wait()
		for _, e := range errs {
			require.NoError(t, e)
		}

		got, err := store.GetBookAuthors(book.ID)
		require.NoError(t, err)
		gotIDs := map[int]bool{}
		for _, ba := range got {
			gotIDs[ba.AuthorID] = true
		}
		for id := range wantIDs {
			if !gotIDs[id] {
				lost++
				t.Errorf("round %d: author %d lost; join=%+v", r, id, got)
			}
		}
	}
	require.Zero(t, lost, "concurrent fill-only applies lost %d author credit(s) in %d rounds", lost, rounds)
}
