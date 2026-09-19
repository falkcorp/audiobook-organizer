// file: internal/aiscan/external_results_test.go
// version: 1.1.0
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

// Finding 6: the source tag is written with the scan row, not after it. A
// crash between two writes left an untagged empty scan that no replay could
// find, and every replay created another.
func TestRecordFullScanResultsTagsScanAtomically(t *testing.T) {
	store := newScanStore(t)
	scan, err := store.CreateScanTagged("batch", map[string]string{"full": "batch"}, 0, externalSourcePrefix+"run-1")
	require.NoError(t, err)
	got, err := store.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, externalSourcePrefix+"run-1", got.OperationID)

	// A replay after a crash right after creation finishes THAT scan.
	id, err := RecordFullScanResults(store, "run-1", []ai.AuthorDiscoverySuggestion{{AuthorIDs: []int{1}, Action: "rename", CanonicalName: "A", Confidence: "high"}})
	require.NoError(t, err)
	require.Equal(t, scan.ID, id)
	scans, err := store.ListScans()
	require.NoError(t, err)
	require.Len(t, scans, 1)
}

// Finding 7: a replay must never replace results a user already applied
// (ReplaceScanResults drops the Applied flags and reassigns ids).
func TestRecordFullScanResultsReplayKeepsAppliedResults(t *testing.T) {
	store := newScanStore(t)
	sugg := []ai.AuthorDiscoverySuggestion{{AuthorIDs: []int{1, 2}, Action: "merge", CanonicalName: "A", Confidence: "high"}}
	id, err := RecordFullScanResults(store, "run-1", sugg)
	require.NoError(t, err)
	rs, err := store.GetScanResults(id)
	require.NoError(t, err)
	require.NoError(t, store.MarkResultApplied(id, rs[0].ID))
	// Crash state: the scan was not yet marked complete when the user applied.
	require.NoError(t, store.UpdateScanStatus(id, "scanning"))

	_, err = RecordFullScanResults(store, "run-1", sugg)
	require.NoError(t, err)
	after, err := store.GetScanResults(id)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.True(t, after[0].Applied, "the applied result survives the replay")
	require.Equal(t, rs[0].ID, after[0].ID)
}

// Finding 10: each nightly run records a near-identical scan. The previous
// run's scan, if nobody applied anything from it, is superseded so the review
// queue holds one current list instead of a pile.
func TestRecordFullScanResultsSupersedesUnreviewedPrevious(t *testing.T) {
	store := newScanStore(t)
	sugg := []ai.AuthorDiscoverySuggestion{{AuthorIDs: []int{1, 2}, Action: "merge", CanonicalName: "A", Confidence: "high"}}
	// A pipeline scan (ai.author-scan) nobody applied from: its OperationID is
	// the op's id, and it must never be superseded by an external run.
	pipelineScan, err := store.CreateScan("realtime", nil, 2)
	require.NoError(t, err)
	require.NoError(t, store.UpdateScanOperationID(pipelineScan.ID, "01J9ZOPERATIONULID000000000"))
	require.NoError(t, store.UpdateScanStatus(pipelineScan.ID, "complete"))

	first, err := RecordFullScanResults(store, "night-1", sugg)
	require.NoError(t, err)
	reviewed, err := RecordFullScanResults(store, "night-2", sugg)
	require.NoError(t, err)
	rs, err := store.GetScanResults(reviewed)
	require.NoError(t, err)
	require.NoError(t, store.MarkResultApplied(reviewed, rs[0].ID))
	latest, err := RecordFullScanResults(store, "night-3", sugg)
	require.NoError(t, err)

	status := func(id int) string {
		s, err := store.GetScan(id)
		require.NoError(t, err)
		return s.Status
	}
	require.Equal(t, "superseded", status(first), "unreviewed and replaced")
	require.Equal(t, "complete", status(reviewed), "a scan someone applied from is never superseded")
	require.Equal(t, "complete", status(latest))
	require.Equal(t, "complete", status(pipelineScan.ID), "only ai-dedup-batch scans are ever superseded")
}
