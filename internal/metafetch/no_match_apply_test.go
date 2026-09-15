// file: internal/metafetch/no_match_apply_test.go
// version: 1.0.0
// guid: 5b0e7c3a-91d4-4f62-8a1e-3c7d2f90b6a4
// last-edited: 2026-09-14

package metafetch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// noMatchFixture is stampFixture with the book marked "no match" by its owner.
func noMatchFixture() (*database.MockStore, **database.Book) {
	store, book, updated := stampFixture(false)
	nm := "no_match"
	book.MetadataReviewStatus = &nm
	return store, updated
}

// An automatic apply (FillOnly: nobody picked this candidate) on a book the
// owner marked "no match" writes nothing and says why, so a bulk caller can
// report the book skipped rather than failed.
func TestApplyCandidate_AutomaticApplySkipsNoMatchBook(t *testing.T) {
	store, updated := noMatchFixture()
	resp, err := NewService(store).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, nil, ApplyOptions{FillOnly: true})
	require.Error(t, err, "an automatic apply must not write onto a rejected book")
	require.True(t, errors.Is(err, ErrMarkedNoMatch), "the refusal is typed: %v", err)
	require.Nil(t, resp)
	require.Nil(t, *updated, "nothing was committed")
}

// A person picking a candidate for a book they marked "no match" is the owner
// overriding their own mark: the apply goes through and records the match,
// which replaces the no_match status (the single-book dialog's behaviour).
func TestApplyCandidate_HandPickedApplyOverridesNoMatch(t *testing.T) {
	store, updated := noMatchFixture()
	resp, err := NewService(store).ApplyMetadataCandidate("stamp-1", stampCandidate, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	got := *updated
	require.NotNil(t, got)
	require.NotNil(t, got.MetadataReviewStatus)
	require.Equal(t, "matched", *got.MetadataReviewStatus, "the owner's pick clears the no-match mark")
	require.Equal(t, "Candidate Title", got.Title)
}

// FetchMetadataForBookByTitle searches AND applies; the production-company
// resolvers call it in a loop, so it must refuse a rejected book the way
// FetchMetadataForBook does.
func TestFetchMetadataForBookByTitle_RefusesNoMatchBook(t *testing.T) {
	store, updated := noMatchFixture()
	_, err := NewService(store).FetchMetadataForBookByTitle("stamp-1")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMarkedNoMatch), "the refusal is typed: %v", err)
	require.Nil(t, *updated, "nothing was committed")
}
