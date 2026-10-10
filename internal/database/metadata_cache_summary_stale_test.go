// file: internal/database/metadata_cache_summary_stale_test.go
// version: 1.0.0
// guid: 5e2f8a14-b7c3-4d90-a6e1-0f9b3d7c2a58
// last-edited: 2026-10-09

package database

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func staleSummaryOf(t *testing.T, s *PebbleStore, bookID string) MetadataCacheSummary {
	t.Helper()
	sums, err := s.ListMetadataCacheKeys()
	require.NoError(t, err)
	for _, sm := range sums {
		if sm.BookID == bookID {
			return sm
		}
	}
	t.Fatalf("no summary for %s", bookID)
	return MetadataCacheSummary{}
}

// Stale survives PutMetadataCache -> ListMetadataCacheKeys on the first call
// (the full scan) and on a later one (the incremental refresh of a changed
// row), in both directions.
func TestListMetadataCacheKeys_CarriesStale(t *testing.T) {
	s := newKeepCacheStore(t)
	cand := []json.RawMessage{json.RawMessage(`{"title":"Title 000123"}`)}
	put := func(id string, stale bool) {
		t.Helper()
		require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id, FetchedAt: time.Now(),
			SourceHash: "h", Candidates: cand, Stale: stale, StaleQuestionFP: map[bool]string{true: "fp-synthetic"}[stale]}))
	}
	put("flagged", true)
	put("plain", false)

	// Full scan.
	require.True(t, staleSummaryOf(t, s, "flagged").Stale)
	require.False(t, staleSummaryOf(t, s, "plain").Stale)
	require.Equal(t, 1, staleSummaryOf(t, s, "flagged").CandidateCount)

	// Incremental refresh: flip both and add a new flagged row.
	put("flagged", false)
	put("plain", true)
	put("later", true)
	require.False(t, staleSummaryOf(t, s, "flagged").Stale)
	require.True(t, staleSummaryOf(t, s, "plain").Stale)
	require.True(t, staleSummaryOf(t, s, "later").Stale)
}
