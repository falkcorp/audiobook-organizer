// file: internal/metafetch/cache_stale_test.go
// version: 1.0.1
// guid: c4a81e27-96d3-4b05-8f7a-3e5d0b2c9a16
// last-edited: 2026-10-09

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// CandidateIdentityStale refuses every candidate of a row marked Stale with
// ErrCandidateRowStale, before the ASIN check; an unflagged row still gets the
// ASIN check.
func TestCandidateIdentityStale_RowFlag(t *testing.T) {
	asin := "B00NEWASIN"
	book := &database.Book{ID: "b1", ASIN: &asin}
	cand := &MetadataCandidate{Title: "Title 000123"}

	// Unflagged, nothing replaced: passes.
	require.NoError(t, CandidateIdentityStale(&MetadataCandidateCache{BookID: "b1"}, book, cand))

	// Flagged: refused with the row error, naming the book and the question.
	err := CandidateIdentityStale(&MetadataCandidateCache{BookID: "b1", Stale: true, StaleQuestionFP: "fp-synthetic"}, book, cand)
	require.ErrorIs(t, err, ErrCandidateRowStale)
	require.NotErrorIs(t, err, ErrCandidateASINReplaced)
	require.NotErrorIs(t, err, ErrStaleMetadataCache, "no transcription may lift a stale row")
	require.Contains(t, err.Error(), "b1")
	require.Contains(t, err.Error(), "fp-synthetic")

	// Flagged, even a candidate that carries the book's current ASIN.
	require.ErrorIs(t, CandidateIdentityStale(&MetadataCandidateCache{Stale: true}, book,
		&MetadataCandidate{ASIN: asin}), ErrCandidateRowStale)

	// ASIN replaced, not flagged: still the ASIN error.
	err = CandidateIdentityStale(&MetadataCandidateCache{FetchedForASIN: "B00OLDASIN"}, book, cand)
	require.ErrorIs(t, err, ErrCandidateASINReplaced)
	require.NotErrorIs(t, err, ErrCandidateRowStale)

	// Both: the row error wins.
	err = CandidateIdentityStale(&MetadataCandidateCache{Stale: true, FetchedForASIN: "B00OLDASIN"}, book, cand)
	require.ErrorIs(t, err, ErrCandidateRowStale)
	require.NotErrorIs(t, err, ErrCandidateASINReplaced)

	// nil inputs pass.
	require.NoError(t, CandidateIdentityStale(nil, book, cand))
	require.NoError(t, CandidateIdentityStale(&MetadataCandidateCache{Stale: true}, nil, cand))
	require.NoError(t, CandidateIdentityStale(&MetadataCandidateCache{Stale: true}, book, nil))
}

// A refetch writes a new row: a row marked Stale under the old inputs is
// replaced, not carried, so the flag is cleared by replacement.
func TestCacheSearchResponse_ReplacementClearsStale(t *testing.T) {
	mfs := preserveFixture(t)
	require.NoError(t, mfs.db.PutMetadataCache(&MetadataCandidateCache{
		BookID: preserveBookID, SourceHash: "hash-of-the-old-title", Stale: true, StaleQuestionFP: "fp-synthetic",
		Candidates: nil,
	}))

	got := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, "", "",
		&SearchMetadataResponse{Results: []MetadataCandidate{{Title: preserveQuery}}})
	require.False(t, got.Stale)
	stored, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Len(t, stored.Candidates, 1)
	require.False(t, stored.Stale, "the replacement row is not stale")
	require.Empty(t, stored.StaleQuestionFP)

	// An empty answer under changed inputs also writes a fresh row.
	require.NoError(t, mfs.db.PutMetadataCache(&MetadataCandidateCache{
		BookID: preserveBookID, SourceHash: "hash-of-another-title", Stale: true, StaleQuestionFP: "fp-synthetic",
	}))
	mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, "", "", &SearchMetadataResponse{})
	stored, err = mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.False(t, stored.Stale)
}

// A row marked Stale is re-asked by the batch fetch (the scheduled fetch
// selects it with Force=false) even though its hash and fingerprint still
// match the current question, and the replacement row is served as fresh.
// VouchedCachedRow does not vouch it, so its candidates are not carried.
func TestBatchVerdict_StaleRowIsReasked(t *testing.T) {
	f := newVerdictFixture(t)
	b, err := f.store.CreateBook(&database.Book{Title: "Title 000301", FilePath: "/lib/s/s.m4b"})
	require.NoError(t, err)
	src := &verdictSource{name: "Src", results: []metadata.BookMetadata{{Title: "Title 000301"}}}
	f.mfs.SetOverrideSources([]metadata.MetadataSource{src})
	f.batchFetch(b.ID)
	require.Equal(t, BatchVerdictFreshCandidates, f.batchFetch(b.ID), "fixture: the fresh row is served")
	book := f.book(b.ID)
	require.NotNil(t, f.mfs.VouchedCachedRow(book, book.Title), "fixture: the unflagged row is vouched")

	entry, err := f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	entry.Stale, entry.StaleQuestionFP = true, "fp-synthetic"
	require.NoError(t, f.store.PutMetadataCache(entry))

	require.Nil(t, f.mfs.VouchedCachedRow(book, book.Title), "a stale row is not vouched")
	// The search itself may be answered from the per-source fetch cache, so
	// the proof of a re-ask is the verdict and the replaced row.
	require.Equal(t, BatchVerdictNone, f.batchFetch(b.ID), "a stale row must be re-asked")
	entry, err = f.store.GetMetadataCache(b.ID)
	require.NoError(t, err)
	require.False(t, entry.Stale, "the refetch replaced the row")
	require.Equal(t, BatchVerdictFreshCandidates, f.batchFetch(b.ID))
}
