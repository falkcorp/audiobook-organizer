// file: internal/metafetch/apply_keeps_candidate_cache_test.go
// version: 1.0.0
// guid: a538ab02-6cfc-4650-9912-228a382db99d
// last-edited: 2026-10-05

package metafetch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// An apply that leaves the book's title and author as they were -- here it
// fills the empty ASIN with the candidate's own -- keeps the cached candidate
// it applied. Before 2026-10-05 the store dropped the row on the ASIN write
// (and the apply handlers dropped it again afterwards), so a book the
// auto-apply did not mark applied was left with neither a status nor a
// candidate. Real store: the bug was in the store, so a mock cannot see it.
func TestApplyMetadataCandidate_ASINFillKeepsTheAppliedCandidate(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st)
	author, err := st.CreateAuthor("Jane Writer")
	require.NoError(t, err)
	book, err := st.CreateBook(&database.Book{Title: "The Quiet Book", FilePath: "/l/q.m4b", Format: "m4b", AuthorID: &author.ID})
	require.NoError(t, err)

	cand := MetadataCandidate{Title: "The Quiet Book", Author: "Jane Writer", ASIN: "B00QUIETBK", Source: "audible", Score: 0.95}
	raw, err := json.Marshal(cand)
	require.NoError(t, err)
	require.NoError(t, st.PutMetadataCache(&database.MetadataCandidateCache{
		BookID: book.ID, FetchedAt: time.Now(), Candidates: []json.RawMessage{raw},
		SourceHash: BatchSourceHash(book.ID, book.Title, "Jane Writer"),
	}))

	_, err = svc.ApplyMetadataCandidate(book.ID, cand, []string{"asin"})
	require.NoError(t, err)

	got, err := st.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ASIN)
	require.Equal(t, "B00QUIETBK", *got.ASIN)
	entry, err := st.GetMetadataCache(book.ID)
	require.NoError(t, err)
	require.NotNil(t, entry, "the apply deleted the candidate it applied")
	require.Len(t, entry.Candidates, 1)

	// The kept row still passes the identity gate: the ASIN is no search input.
	live, err := database.LiveBookAuthorNames(st, got)
	require.NoError(t, err)
	require.NoError(t, svc.ValidateCachedIdentityForBook(entry, got, live))
}
