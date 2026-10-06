// file: internal/metafetch/cache_asin_replaced_test.go
// version: 1.0.0
// guid: 3e7a9d14-6b2c-4f58-a0d1-8c5e2b7f9a36
// last-edited: 2026-10-05

// A book's cached candidates survive an ASIN change (database
// candidateSearchIdentityChanged). The row records the ASIN its candidates
// were fetched for (FetchedForASIN), so a candidate with no ASIN of its own
// reads as stale once the book's ASIN is replaced: CandidateASINStale refuses
// it, and the batch fetch re-asks the row instead of serving it as fresh.
package metafetch

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The fetch records the book's ASIN on the row it writes; an empty refetch
// that keeps the earlier candidates keeps the earlier row's value, and a
// refetch that finds candidates records its own.
func TestCacheSearchResponse_RecordsFetchedForASIN(t *testing.T) {
	mfs := preserveFixture(t)
	first := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, "", "",
		&SearchMetadataResponse{Results: []MetadataCandidate{{Title: preserveQuery}}, BookASIN: "B00FETCHED"})
	require.Equal(t, "B00FETCHED", first.FetchedForASIN)

	carried := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, "", "",
		&SearchMetadataResponse{BookASIN: "B00NOWASIN"})
	require.Len(t, carried.Candidates, 1, "fixture: the empty search keeps the candidates")
	stored, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.Equal(t, "B00FETCHED", stored.FetchedForASIN, "carried candidates keep the ASIN they were fetched for")

	fresh := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, "", "",
		&SearchMetadataResponse{Results: []MetadataCandidate{{Title: "New"}}, BookASIN: "B00NOWASIN"})
	require.Equal(t, "B00NOWASIN", fresh.FetchedForASIN)
}

// A row written before FetchedForASIN existed takes the empty search's ASIN
// when it carries: the store stamps any replacement on the row, so an
// unstamped row was last vouched for the ASIN the book holds now.
func TestCacheSearchResponse_UnstampedCarryTakesTheSearchASIN(t *testing.T) {
	mfs := preserveFixture(t)
	mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, "", "",
		&SearchMetadataResponse{Results: []MetadataCandidate{{Title: preserveQuery}}})
	mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, "", "",
		&SearchMetadataResponse{BookASIN: "B00CURRENT"})
	stored, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.Equal(t, "B00CURRENT", stored.FetchedForASIN)
}

func TestCandidateASINStale(t *testing.T) {
	asin := func(s string) *string { return &s }
	cases := []struct {
		name  string
		stamp string
		book  *string
		cand  string
		stale bool
	}{
		{name: "no stamp", stamp: "", book: asin("B00NEWASIN"), cand: ""},
		{name: "stamp is the book's", stamp: "B00NEWASIN", book: asin("b00newasin"), cand: ""},
		{name: "replaced, ASIN-less candidate", stamp: "B00OLDASIN", book: asin("B00NEWASIN"), cand: "", stale: true},
		{name: "replaced, candidate carries the new ASIN", stamp: "B00OLDASIN", book: asin("B00NEWASIN"), cand: " b00newasin"},
		{name: "replaced, candidate carries the old ASIN", stamp: "B00OLDASIN", book: asin("B00NEWASIN"), cand: "B00OLDASIN", stale: true},
		{name: "cleared", stamp: "B00OLDASIN", book: nil, cand: "B00OLDASIN", stale: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := &MetadataCandidateCache{BookID: "b1", FetchedForASIN: tc.stamp}
			book := &database.Book{ID: "b1", ASIN: tc.book}
			err := CandidateASINStale(entry, book, &MetadataCandidate{ASIN: tc.cand})
			require.Equal(t, tc.stale, err != nil, "err = %v", err)
			if err != nil {
				require.ErrorIs(t, err, ErrCandidateASINReplaced)
				require.False(t, errors.Is(err, ErrStaleMetadataCache),
					"must not read as a stale query, which a transcription can explain")
			}
		})
	}
	require.NoError(t, CandidateASINStale(nil, &database.Book{}, &MetadataCandidate{}))
}

// The batch fetch re-asks a row whose book's ASIN was replaced even when
// nothing else about its questions changed (here neither ASIN looks like an
// Audible ASIN, so the search fingerprint is the same), and stops re-asking
// once the current questions came back empty.
func TestBatchVerdict_ReplacedASINIsReasked(t *testing.T) {
	f := newVerdictFixture(t)
	old := "OLD-ASIN-1"
	b, err := f.store.CreateBook(&database.Book{Title: "Rendezvous", FilePath: "/lib/r/r.m4b", ASIN: &old})
	require.NoError(t, err)
	src := &verdictSource{name: "Src", results: []metadata.BookMetadata{{Title: "Rendezvous"}}}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{src})
	f.batchFetch(b.ID)
	entry, err := f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.Equal(t, old, entry.FetchedForASIN)
	require.Equal(t, BatchVerdictFreshCandidates, f.batchFetch(b.ID), "fixture: the fresh row is served")

	_, err = f.store.ModifyBook(b.ID, func(cur *database.Book) error { s := "OLD-ASIN-2"; cur.ASIN = &s; return nil })
	require.NoError(t, err)
	calls := src.calls.Load()
	require.Equal(t, BatchVerdictNone, f.batchFetch(b.ID), "a replaced ASIN must be re-asked")
	require.Greater(t, src.calls.Load(), calls, "the provider was asked again")
	entry, err = f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.Equal(t, "OLD-ASIN-2", entry.FetchedForASIN, "the refetch records the new ASIN")
	require.Equal(t, BatchVerdictFreshCandidates, f.batchFetch(b.ID))

	// Replaced again, and this time the providers have nothing: the old
	// candidates are carried (still stamped with the ASIN they were fetched
	// for), and the next run does not repeat the empty search.
	_, err = f.store.ModifyBook(b.ID, func(cur *database.Book) error { s := "OLD-ASIN-3"; cur.ASIN = &s; return nil })
	require.NoError(t, err)
	src.results = nil
	require.Equal(t, BatchVerdictNone, f.batchFetch(b.ID))
	entry, err = f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.Len(t, entry.Candidates, 1, "fixture: the empty refetch keeps the candidates")
	require.Equal(t, "OLD-ASIN-2", entry.FetchedForASIN)
	calls = src.calls.Load()
	require.NotEqual(t, BatchVerdictNone, f.batchFetch(b.ID), "an answered empty search is not repeated every run")
	require.Equal(t, calls, src.calls.Load())
	book := f.book(b.ID)
	require.ErrorIs(t, CandidateASINStale(entry, book, &MetadataCandidate{Title: "Rendezvous"}), ErrCandidateASINReplaced,
		"the carried candidates still read as stale to the apply gate")
}
