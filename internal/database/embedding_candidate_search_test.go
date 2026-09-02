// file: internal/database/embedding_candidate_search_test.go
// version: 1.1.0
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
	return seedSearchCandidatesIndexed(t, false)
}

// seedSearchCandidatesIndexed seeds the same corpus and optionally marks the
// candidate status index built.
//
// This distinction is the whole reason the suite runs twice. ListCandidates has
// TWO read paths: a full "dedup:r:" scan, and listCandidatesByStatusIndex,
// taken only when a Status filter is set AND the backfill flag is on. A bare
// test store has no flag, so every test written the obvious way exercises the
// full scan -- while PRODUCTION has run the backfill and the panel always sends
// status=pending, so production only ever takes the INDEXED path.
//
// Tested one way, a mutant that blanks f.Search/f.SearchEntityIDs inside
// listCandidatesByStatusIndex leaves the whole suite green while server-side
// search does nothing at all where it actually runs.
func seedSearchCandidatesIndexed(t *testing.T, indexed bool) *EmbeddingStore {
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
	if indexed {
		require.NoError(t, store.SetCandidateStatusIndexBuilt())
		require.True(t, store.IsCandidateStatusIndexBuilt(),
			"the flag must actually be on, or this sub-test silently retests the full scan")
	}
	return store
}

// forEachReadPath runs fn against both ListCandidates read paths. Any search
// assertion that does not run under this is only testing one of them.
func forEachReadPath(t *testing.T, fn func(t *testing.T, store *EmbeddingStore)) {
	t.Helper()
	for _, tc := range []struct {
		name    string
		indexed bool
	}{
		{"full scan", false},
		{"status index (the production path)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fn(t, seedSearchCandidatesIndexed(t, tc.indexed))
		})
	}
}

// TestListCandidatesSearchMatchesRowLocalFields covers the half of the search
// union that lives ON the candidate row. This is the half the design most
// naturally drops: an implementation that only resolves book IDs would return
// zero rows here while looking completely correct for title searches.
func TestListCandidatesSearchMatchesRowLocalFields(t *testing.T) {
	forEachReadPath(t, func(t *testing.T, store *EmbeddingStore) {
		cases := []struct {
			name    string
			needle  string
			wantIDs []string
		}{
			{"band", "certain", []string{"alpha-a"}},
			{"band is case-insensitive", "CERTAIN", []string{"alpha-a"}},
			{"band substring", "medi", []string{"gamma-a"}},
			{"layer", "fingerprint", []string{"beta-a", "gamma-a"}},
			{"entity id, full", "delta-b", []string{"delta-a"}},
			// Prefix, not substring: IDs are ULIDs, so a substring test would make
			// every short needle match most of the library on ID alone.
			{"entity id, prefix", "delta-", []string{"delta-a"}},
			{"entity id, mid-string does NOT match", "elta-b", nil},
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
	})
}

// TestListCandidatesSearchMatchesJoinedBookIDs covers the other half: the
// needle hit a book's title/author/path, which the caller resolved to a set of
// entity IDs. Neither "certain" nor any row-local text appears here, so a
// match proves the ID set alone carried it.
func TestListCandidatesSearchMatchesJoinedBookIDs(t *testing.T) {
	forEachReadPath(t, func(t *testing.T, store *EmbeddingStore) {

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
	})
}

// TestListCandidatesSearchIsUnionNotIntersection is the test that fails if the
// two halves are AND'd. Each needle here matches exactly one half.
func TestListCandidatesSearchIsUnionNotIntersection(t *testing.T) {
	forEachReadPath(t, func(t *testing.T, store *EmbeddingStore) {

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
	})
}

// TestListCandidatesSearchTotalCountsEveryMatchNotThePage pins the property
// that motivated putting search at scan level. Limit is set BELOW the match
// count on purpose: a Limit at or above it cannot observe the bug, because
// every match fits inside the page window either way.
func TestListCandidatesSearchTotalCountsEveryMatchNotThePage(t *testing.T) {
	forEachReadPath(t, func(t *testing.T, store *EmbeddingStore) {

		got, total, err := store.ListCandidates(CandidateFilter{
			EntityType: "book", Status: "pending",
			Search: "match-everything",
			SearchEntityIDs: map[string]struct{}{
				"alpha-a": {}, "beta-a": {}, "gamma-a": {}, "delta-a": {},
			},
			Limit: 2,
		})
		require.NoError(t, err)
		assert.Equal(t, 4, total, "total must count all matches in the library")
		assert.Len(t, got, 2, "page must respect Limit")
	})
}

// TestListCandidatesSearchNarrowsWithinOtherFilters proves search AND's with
// the other clauses rather than escaping them -- a candidate the Band filter
// excluded must not be re-admitted by a search hit.
func TestListCandidatesSearchNarrowsWithinOtherFilters(t *testing.T) {
	forEachReadPath(t, func(t *testing.T, store *EmbeddingStore) {

		got, total, err := store.ListCandidates(CandidateFilter{
			EntityType: "book", Status: "pending",
			Band:   "HIGH",        // beta only
			Search: "fingerprint", // beta AND gamma
		})
		require.NoError(t, err)
		assert.Equal(t, 1, total, "search must not re-admit rows the Band filter excluded")
		require.Len(t, got, 1)
		assert.Equal(t, "beta-a", got[0].EntityAID)
	})
}

// TestListCandidatesSearchControls are the instrument checks: a bogus needle
// must return nothing, and an empty needle must not filter at all. Without the
// second, a search that silently matched nothing would look identical to one
// that correctly matched a narrow set.
func TestListCandidatesSearchControls(t *testing.T) {
	forEachReadPath(t, func(t *testing.T, store *EmbeddingStore) {

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
	})
}

// TestListCandidatesSearchEntityIDsAloneStillFilters pins the reachability of
// the ID half. The clause guarding candidateMatchesSearch originally tested
// only f.Search, so a caller that set SearchEntityIDs WITHOUT a needle -- the
// natural shape for "restrict to this resolved set" -- got the entire
// unfiltered library back under a 200 and an honest-looking total. That is the
// fail-open direction, and no test could see it while every caller set both.
func TestListCandidatesSearchEntityIDsAloneStillFilters(t *testing.T) {
	forEachReadPath(t, func(t *testing.T, store *EmbeddingStore) {

		got, total, err := store.ListCandidates(CandidateFilter{
			EntityType: "book", Status: "pending",
			SearchEntityIDs: map[string]struct{}{"beta-b": {}},
		})
		require.NoError(t, err)
		assert.Equal(t, 1, total, "an ID set with no needle must still filter")
		require.Len(t, got, 1)
		assert.Equal(t, "beta-a", got[0].EntityAID)
	})
}
