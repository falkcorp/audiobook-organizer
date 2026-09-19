// file: internal/aiscan/external_results_test.go
// version: 1.0.0
// guid: f4ecec1d-b082-499e-8c29-8fe9a8f99043
// last-edited: 2026-09-19

package aiscan

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/stretchr/testify/require"
)

// TestRecordFullScanResultsIsIdempotentPerSource: maintenance.ai-dedup-batch's
// results land as a completed AI scan, so they are reviewed and applied from
// the same AI review queue as ai.author-scan's. aijobs may replay a callback
// whose completion mark was lost; the replay must not create a second scan.
func TestRecordFullScanResultsIsIdempotentPerSource(t *testing.T) {
	store := newScanStore(t)
	sugg := []ai.AuthorDiscoverySuggestion{
		{AuthorIDs: []int{1, 2}, Action: "merge", CanonicalName: "A. B. Smith", Confidence: "high"},
		{AuthorIDs: []int{3}, Action: "rename", CanonicalName: "C", Confidence: "low"},
	}

	id1, err := RecordFullScanResults(store, "run-1", sugg)
	require.NoError(t, err)
	id2, err := RecordFullScanResults(store, "run-1", sugg)
	require.NoError(t, err)
	require.Equal(t, id1, id2, "a replay reuses the scan")

	scans, err := store.ListScans()
	require.NoError(t, err)
	require.Len(t, scans, 1)
	require.Equal(t, "complete", scans[0].Status)
	require.Len(t, resultKeys(t, store, id1), 2)

	id3, err := RecordFullScanResults(store, "run-2", sugg)
	require.NoError(t, err)
	require.NotEqual(t, id1, id3, "a different run is a different scan")
}
