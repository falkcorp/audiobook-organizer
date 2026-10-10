// file: internal/metafetch/cache_stale_test.go
// version: 1.0.0
// guid: c4a81e27-96d3-4b05-8f7a-3e5d0b2c9a16
// last-edited: 2026-10-09

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
