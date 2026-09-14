// file: internal/metafetch/service_apply_match_stamp_test.go
// version: 1.0.0
// guid: dc28fc77-3a62-4866-bfd5-7fb3048d18a8
// last-edited: 2026-09-14

package metafetch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// stampFixture is a book titled "Old Title" and a store that records the
// committed row. With lockTitle the title carries a user override, so
// StripLockedFields blanks the candidate's title.
func stampFixture(lockTitle bool) (*database.MockStore, *database.Book, **database.Book) {
	book := &database.Book{ID: "stamp-1", Title: "Old Title"}
	var updated *database.Book
	store := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) {
			clone := *book
			return &clone, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			cp := *b
			updated = &cp
			return &cp, nil
		},
		GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) {
			if !lockTitle {
				return nil, nil
			}
			v := `"Curated Title"`
			return []database.MetadataFieldState{{Field: database.FieldKeyTitle, OverrideValue: &v}}, nil
		},
	}
	return store, book, &updated
}

var stampCandidate = MetadataCandidate{
	Title: "Candidate Title", Description: "A description.", CoverURL: "https://example.invalid/c.jpg",
	ASIN: "B0STAMP001", Source: "audible",
}

// requireStamped asserts the apply recorded the match: status, source, hash.
func requireStamped(t *testing.T, got *database.Book) {
	t.Helper()
	require.NotNil(t, got, "the apply committed a row")
	require.Equal(t, "Old Title", got.Title, "the candidate's title was not written")
	require.NotNil(t, got.MetadataReviewStatus, "a person picked this candidate: the match is recorded")
	require.Equal(t, "matched", *got.MetadataReviewStatus)
	require.NotNil(t, got.MetadataSource)
	require.Equal(t, "audible", *got.MetadataSource)
	require.NotNil(t, got.MetadataSourceHash)
}

// applyIgnoringHistory runs the apply; an owner-reviewed apply over a mock
// store with no history backend returns ErrApplyHistoryIncomplete with the
// committed response, which is not what these tests are about.
func applyIgnoringHistory(t *testing.T, store *database.MockStore, fields []string, opts ApplyOptions) {
	t.Helper()
	resp, err := NewService(store).ApplyMetadataCandidateWithOptions("stamp-1", stampCandidate, fields, opts)
	if err != nil && !errors.Is(err, ErrApplyHistoryIncomplete) {
		t.Fatalf("apply: %v", err)
	}
	require.NotNil(t, resp)
}

// The hand-picked dialog with the title deselected: applying only the cover
// and description is still the owner choosing this candidate, so the match is
// recorded although the book keeps its own title.
func TestApplyCandidate_HandPickedTitleDeselectedRecordsMatch(t *testing.T) {
	store, _, updated := stampFixture(false)
	resp, err := NewService(store).ApplyMetadataCandidate("stamp-1", stampCandidate, []string{"cover_url", "description"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	requireStamped(t, *updated)
}

// A locked title blanks the candidate's title on every apply. A person-picked
// apply (the single-book apply, a review-lane approval with the gate passed,
// or one whose approval lifted a gate refusal) still records the match, or
// the book would stay in the review lane forever.
func TestApplyCandidate_LockedTitleHumanAppliesRecordMatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts ApplyOptions
	}{
		{"hand-picked single-book apply", ApplyOptions{}},
		{"review-lane approval, gate passed", ApplyOptions{FillOnly: false}},
		{"review-lane approval, gate lifted", ApplyOptions{OwnerReviewed: true, GateOverride: "score_below_floor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, updated := stampFixture(true)
			applyIgnoringHistory(t, store, nil, tc.opts)
			requireStamped(t, *updated)
		})
	}
}

// An automatic (fill-only) apply that keeps a different title fills what it
// may but does not record the match: nobody chose this candidate, and the
// book does not hold its title.
func TestApplyCandidate_AutomaticKeptTitleRecordsNoMatch(t *testing.T) {
	store, _, updated := stampFixture(true)
	applyIgnoringHistory(t, store, nil, ApplyOptions{FillOnly: true})
	got := *updated
	require.NotNil(t, got, "the empty description and cover are still filled")
	require.Equal(t, "Old Title", got.Title)
	require.Nil(t, got.MetadataReviewStatus)
	require.Nil(t, got.MetadataSource)
	require.Nil(t, got.MetadataSourceHash)
}

// RefuseEmptyWrite must not report nothing-to-apply after the apply added an
// author credit: on a book credited to [A, B], a candidate naming C adds C to
// book_authors without changing any book column (AuthorID stays A).
func TestApplyCandidate_RefuseEmptyWriteCountsAddedCredit(t *testing.T) {
	store, book, writes := coAuthorFixture()
	cand := MetadataCandidate{Title: book.Title, Author: "C", Source: "audible"}
	_, err := NewService(store).ApplyMetadataCandidateWithOptions(book.ID, cand, []string{"author"},
		ApplyOptions{FillOnly: true, RefuseEmptyWrite: true})
	require.Equal(t, []int{1, 2, 3}, finalJoin(*writes), "the credit for C was written")
	require.NotErrorIs(t, err, ErrNothingToApply, "a written credit is a change")
	require.NoError(t, err)
}

// With the author already credited nothing changes, so the refusal stands.
func TestApplyCandidate_RefuseEmptyWriteUnchangedCreditStillRefuses(t *testing.T) {
	store, book, writes := coAuthorFixture()
	cand := MetadataCandidate{Title: book.Title, Author: "B", Source: "audible"}
	_, err := NewService(store).ApplyMetadataCandidateWithOptions(book.ID, cand, []string{"author"},
		ApplyOptions{FillOnly: true, RefuseEmptyWrite: true})
	require.ErrorIs(t, err, ErrNothingToApply)
	require.Empty(t, *writes)
}
