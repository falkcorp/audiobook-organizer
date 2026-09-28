// file: internal/database/embedding_store_after_id_test.go
// version: 1.0.0
// guid: 3f0d6a52-8c1e-4b7a-9e25-6d4c1b8a7f90
// last-edited: 2026-09-28

package database

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedAfterIDFixture writes n candidates cycling through statuses and layers so
// a pending+exact filter matches roughly a third of them, scattered by ID.
func seedAfterIDFixture(t *testing.T, s *EmbeddingStore, n int) {
	t.Helper()
	statuses := []string{"pending", "merged", "pending", "dismissed"}
	layers := []string{"exact", "embedding", "exact"}
	for i := range n {
		require.NoError(t, s.UpsertCandidate(DedupCandidate{
			EntityType: "book",
			EntityAID:  fmt.Sprintf("a%03d", i),
			EntityBID:  fmt.Sprintf("b%03d", i),
			Layer:      layers[i%len(layers)],
			Status:     statuses[i%len(statuses)],
			Similarity: new(float64(i%5) / 5),
		}))
	}
}

// drainAfterID pages through ListCandidatesAfterID with the given page size,
// asserting strictly ascending IDs, and returns every row seen.
func drainAfterID(t *testing.T, s *EmbeddingStore, f CandidateFilter, page int) []DedupCandidate {
	t.Helper()
	var all []DedupCandidate
	var cursor int64
	for calls := 0; ; calls++ {
		require.Less(t, calls, 1000, "keyset scan did not terminate")
		rows, err := s.ListCandidatesAfterID(f, cursor, page)
		require.NoError(t, err)
		for _, c := range rows {
			require.Greater(t, c.ID, cursor, "rows must be strictly ascending by ID")
			cursor = c.ID
			all = append(all, c)
		}
		if len(rows) < page {
			return all
		}
	}
}

// The keyset scan must return exactly the rows ListCandidates returns for the
// same filter — no skips, no duplicates — on BOTH read paths (full dedup:r:
// scan with the index flag unset, and the dedup:s: status index with it set).
func TestListCandidatesAfterID_MatchesListCandidatesOnBothPaths(t *testing.T) {
	store := newTestEmbeddingStore(t)
	seedAfterIDFixture(t, store, 37)
	filter := CandidateFilter{EntityType: "book", Status: "pending", Layer: "exact"}

	want, total, err := store.ListCandidates(CandidateFilter{EntityType: "book", Status: "pending", Layer: "exact", Limit: 1000})
	require.NoError(t, err)
	require.Equal(t, total, len(want))
	require.Greater(t, total, 8, "fixture must span several pages")
	wantIDs := map[int64]bool{}
	for _, c := range want {
		wantIDs[c.ID] = true
	}

	check := func(name string) {
		for _, page := range []int{1, 3, 4, 1000} {
			got := drainAfterID(t, store, filter, page)
			require.Len(t, got, len(want), "%s page=%d: row count", name, page)
			for _, c := range got {
				require.True(t, wantIDs[c.ID], "%s page=%d: unexpected row %d", name, page, c.ID)
				require.Equal(t, "pending", c.Status)
				require.Equal(t, "exact", c.Layer)
			}
		}
	}

	require.False(t, store.IsCandidateStatusIndexBuilt())
	check("full-scan path")

	// UpsertCandidate maintains the dedup:s: rows inline, so flipping the flag
	// exercises the indexed path over the same data (not an empty index).
	require.NotEmpty(t, statusIndexRows(t, store))
	require.NoError(t, store.SetCandidateStatusIndexBuilt())
	check("status-index path")
}

// A row inserted after the scan started gets a higher ID, so a keyset scan
// picks it up at the end instead of shifting later pages.
func TestListCandidatesAfterID_RowInsertedMidScanIsSeenOnce(t *testing.T) {
	store := newTestEmbeddingStore(t)
	seedAfterIDFixture(t, store, 12)
	filter := CandidateFilter{EntityType: "book", Status: "pending", Layer: "exact"}

	first, err := store.ListCandidatesAfterID(filter, 0, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)

	require.NoError(t, store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "late-a", EntityBID: "late-b", Layer: "exact", Status: "pending",
	}))

	rest := drainAfterID(t, store, filter, 2)
	var late int
	for _, c := range rest {
		if c.EntityAID == "late-a" {
			late++
		}
	}
	require.Equal(t, 1, late, "row inserted mid-scan must be seen exactly once")
}

func TestListCandidatesAfterID_RejectsNonPositiveLimit(t *testing.T) {
	store := newTestEmbeddingStore(t)
	_, err := store.ListCandidatesAfterID(CandidateFilter{}, 0, 0)
	require.Error(t, err)
}
