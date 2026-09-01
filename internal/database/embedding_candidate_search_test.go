// file: internal/database/embedding_candidate_search_test.go
// version: 1.0.0
// guid: 7c1f4a9e-2b83-4d15-9e6a-0f8c3d7b45a2
// last-edited: 2026-09-01

package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSearchCandidates writes a fixed corpus used by the search tests. The
// entity IDs are deliberately human-readable so a test can assert on which
// rows came back rather than just how many.
func seedSearchCandidates(t *testing.T) *EmbeddingStore {
	t.Helper()
	store := newTestEmbeddingStore(t)
	for _, c := range []DedupCandidate{
		{EntityType: "book", EntityAID: "alpha-a", EntityBID: "alpha-b",
			Layer: "embedding", Band: "CERTAIN", Similarity: floatPtr(0.99), Status: "pending"},
		{EntityType: "book", EntityAID: "beta-a", EntityBID: "beta-b",
			Layer: "fingerprint", Band: "HIGH", Similarity: floatPtr(0.90), Status: "pending"},
		{EntityType: "book", EntityAID: "gamma-a", EntityBID: "gamma-b",
			Layer: "fingerprint", Band: "MEDIUM", Similarity: floatPtr(0.80), Status: "pending"},
		{EntityType: "book", EntityAID: "delta-a", EntityBID: "delta-b",
			Layer: "embedding", Band: "REVIEW", Similarity: floatPtr(0.70), Status: "pending"},
	} {
		require.NoError(t, store.UpsertCandidate(c))
	}
	return store
}

// TestListCandidatesSearchMatchesRowLocalFields covers the half of the search
// union that lives ON the candidate row. This is the half the design most
// naturally drops: an implementation that only resolves book IDs would return
// zero rows here while looking completely correct for title searches.
func TestListCandidatesSearchMatchesRowLocalFields(t *testing.T) {
	store := seedSearchCandidates(t)

	cases := []struct {
		name    string
		needle  string
		wantIDs []string
	}{
		{"band", "certain", []string{"alpha-a"}},
		{"band is case-insensitive", "CERTAIN", []string{"alpha-a"}},
		{"band substring", "medi", []string{"gamma-a"}},
		{"layer", "fingerprint", []string{"beta-a", "gamma-a"}},
		{"entity id", "delta-b", []string{"delta-a"}},
		{"needle is trimmed", "  certain  ", []string{"alpha-a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := store.ListCandidates(CandidateFilter{
				EntityType: "book", Status: "pending", Search: tc.needle,
			})
			require.NoError(t, err)
			assert.Equal(t, len(tc.wantIDs), total, "total")
			var ids []string
			for _, c := range got {
				ids = append(ids, c.EntityAID)
			}
			assert.ElementsMatch(t, tc.wantIDs, ids)
		})
	}
}

// TestListCandidatesSearchMatchesJoinedBookIDs covers the other half: the
// needle hit a book's title/author/path, which the caller resolved to a set of
// entity IDs. Neither "certain" nor any row-local text appears here, so a
// match proves the ID set alone carried it.
func TestListCandidatesSearchMatchesJoinedBookIDs(t *testing.T) {
	store := seedSearchCandidates(t)

	got, total, err := store.ListCandidates(CandidateFilter{
		EntityType: "book", Status: "pending",
		Search: "norse mythology", // matches no row-local field
		SearchEntityIDs: map[string]struct{}{
			"gamma-b": {}, // B side, to prove both sides are checked
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, got, 1)
	assert.Equal(t, "gamma-a", got[0].EntityAID)
}

// TestListCandidatesSearchIsUnionNotIntersection is the test that fails if the
// two halves are AND'd. Each needle here matches exactly one half.
func TestListCandidatesSearchIsUnionNotIntersection(t *testing.T) {
	store := seedSearchCandidates(t)

	got, total, err := store.ListCandidates(CandidateFilter{
		EntityType: "book", Status: "pending",
		Search:          "certain",                         // row-local: alpha only
		SearchEntityIDs: map[string]struct{}{"beta-a": {}}, // joined book: beta only
	})
	require.NoError(t, err)
	assert.Equal(t, 2, total, "union of both halves, not their intersection")
	var ids []string
	for _, c := range got {
		ids = append(ids, c.EntityAID)
	}
	assert.ElementsMatch(t, []string{"alpha-a", "beta-a"}, ids)
}

// TestListCandidatesSearchTotalCountsEveryMatchNotThePage pins the property
// that motivated putting search at scan level. Limit is set BELOW the match
// count on purpose: a Limit at or above it cannot observe the bug, because
// every match fits inside the page window either way.
func TestListCandidatesSearchTotalCountsEveryMatchNotThePage(t *testing.T) {
	store := seedSearchCandidates(t)

	got, total, err := store.ListCandidates(CandidateFilter{
		EntityType: "book", Status: "pending",
		Search: "-a", // every seeded candidate's A-side id ends in "-a"
		Limit:  2,
	})
	require.NoError(t, err)
	assert.Equal(t, 4, total, "total must count all matches in the library")
	assert.Len(t, got, 2, "page must respect Limit")
}

// TestListCandidatesSearchNarrowsWithinOtherFilters proves search AND's with
// the other clauses rather than escaping them -- a candidate the Band filter
// excluded must not be re-admitted by a search hit.
func TestListCandidatesSearchNarrowsWithinOtherFilters(t *testing.T) {
	store := seedSearchCandidates(t)

	got, total, err := store.ListCandidates(CandidateFilter{
		EntityType: "book", Status: "pending",
		Band:   "HIGH",        // beta only
		Search: "fingerprint", // beta AND gamma
	})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "search must not re-admit rows the Band filter excluded")
	require.Len(t, got, 1)
	assert.Equal(t, "beta-a", got[0].EntityAID)
}

// TestListCandidatesSearchControls are the instrument checks: a bogus needle
// must return nothing, and an empty needle must not filter at all. Without the
// second, a search that silently matched nothing would look identical to one
// that correctly matched a narrow set.
func TestListCandidatesSearchControls(t *testing.T) {
	store := seedSearchCandidates(t)

	_, total, err := store.ListCandidates(CandidateFilter{
		EntityType: "book", Status: "pending", Search: "zzz-no-such-needle",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, total, "bogus needle must match nothing")

	_, total, err = store.ListCandidates(CandidateFilter{
		EntityType: "book", Status: "pending", Search: "",
	})
	require.NoError(t, err)
	assert.Equal(t, 4, total, "empty needle must not filter")

	_, total, err = store.ListCandidates(CandidateFilter{
		EntityType: "book", Status: "pending", Search: "   ",
	})
	require.NoError(t, err)
	assert.Equal(t, 4, total, "whitespace-only needle must not filter")
}
