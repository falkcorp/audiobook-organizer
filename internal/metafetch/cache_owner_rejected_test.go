// file: internal/metafetch/cache_owner_rejected_test.go
// version: 1.0.0
// guid: 136b04d6-60b7-4d76-b95d-cb923cdc36b6
// last-edited: 2026-10-10

package metafetch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func cachedTitles(t *testing.T, rows []json.RawMessage) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var c MetadataCandidate
		require.NoError(t, json.Unmarshal(r, &c))
		out = append(out, c.Title)
	}
	return out
}

// rankRejectedX is metabatch.MergeRanker's shape with "Rejected X"
// owner-rejected (its MergeRankOwnerRejected tier is 3).
func rankRejectedX(c MetadataCandidate) int {
	if c.Title == "Rejected X" {
		return 3
	}
	return 0
}

// A chain search that REPLACES the row (no merge, no carried fallback
// candidates) ranks its answer with the caller's MergeRank like the merge
// paths do, so an owner-rejected candidate is never stored in slot 0
// (regression, 2026-10-10).
func TestCacheSearchResponse_PlainReplaceHonoursMergeRank(t *testing.T) {
	mfs := preserveFixture(t)
	seedGoodEntry(t, mfs) // a previous row for the same inputs, replaced below
	resp := &SearchMetadataResponse{
		Results: []MetadataCandidate{
			{Title: "Rejected X", Source: "Audible", Score: 0.97},
			{Title: "Good Y", Source: "Audible", Score: 0.93},
		},
		mergeRank: rankRejectedX,
	}
	entry := mfs.cacheSearchResponse(preserveBookID, preserveQuery, preserveAuthor, preserveNarrator, preserveSeries, resp)
	require.Equal(t, []string{"Good Y", "Rejected X"}, cachedTitles(t, entry.Candidates))
	stored, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.Equal(t, []string{"Good Y", "Rejected X"}, cachedTitles(t, stored.Candidates))
	// The response the caller holds keeps the search's own order.
	require.Equal(t, "Rejected X", resp.Results[0].Title)
}

// RerankCachedCandidates only re-orders: every candidate (an undecodable one
// included) survives, the row's other fields are written back as read, and a
// row already in order is not written again.
func TestRerankCachedCandidates_ReordersWithoutDropping(t *testing.T) {
	mfs := preserveFixture(t)
	enc := func(c MetadataCandidate) json.RawMessage { b, _ := json.Marshal(c); return b }
	bad := json.RawMessage(`"not a candidate"`)
	stamp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, mfs.db.PutMetadataCache(&MetadataCandidateCache{
		BookID:            preserveBookID,
		Candidates:        []json.RawMessage{enc(MetadataCandidate{Title: "Rejected X", Source: "Audible", Score: 0.97}), bad, enc(MetadataCandidate{Title: "Good Y", Source: "Audible", Score: 0.93})},
		FetchedAt:         stamp,
		SourceHash:        "hash-1",
		SearchFingerprint: "fp-1",
	}))

	out, err := mfs.RerankCachedCandidates(preserveBookID, rankRejectedX)
	require.NoError(t, err)
	require.Equal(t, RerankReordered, out)
	row, err := mfs.db.GetMetadataCache(preserveBookID)
	require.NoError(t, err)
	require.Len(t, row.Candidates, 3)
	require.Equal(t, []string{"Good Y", "Rejected X"}, cachedTitles(t, row.Candidates[:2]))
	require.JSONEq(t, string(bad), string(row.Candidates[2]))
	require.True(t, row.FetchedAt.Equal(stamp))
	require.Equal(t, "hash-1", row.SourceHash)
	require.Equal(t, "fp-1", row.SearchFingerprint)

	again, err := mfs.RerankCachedCandidates(preserveBookID, rankRejectedX)
	require.NoError(t, err)
	require.Equal(t, RerankUnchanged, again)

	none, err := mfs.RerankCachedCandidates("no-such-book", rankRejectedX)
	require.NoError(t, err)
	require.Equal(t, RerankNoRow, none)
}

func TestParseRejectedCandidateKey(t *testing.T) {
	id, suffix, ok := ParseRejectedCandidateKey(RejectedCandidateStoreKey("b-1", "Audible", "A: Title|With Bar"))
	require.True(t, ok)
	require.Equal(t, "b-1", id)
	require.Equal(t, "Audible|A: Title|With Bar", suffix)
	for _, bad := range []string{"other:b-1:x|y", "rejected_candidate:b-1", "rejected_candidate::x|y", "rejected_candidate:b-1:"} {
		_, _, ok := ParseRejectedCandidateKey(bad)
		require.False(t, ok, bad)
	}
}
