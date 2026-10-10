// file: internal/database/ai_scan_store_concurrency_test.go
// version: 1.1.0
// guid: 83c719e8-e868-4863-bff1-ed611c785418
// last-edited: 2026-10-10

package database

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// overlapping runs fn on n goroutines released together, so their calls
// overlap instead of running one after another.
func overlapping(n int, fn func(i int)) {
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(n)
	for i := range n {
		go func() {
			defer done.Done()
			start.Wait()
			fn(i)
		}()
	}
	start.Done()
	done.Wait()
}

func fourResults(scanID int) []ScanResult {
	out := make([]ScanResult, 4)
	for i := range out {
		out[i] = ScanResult{
			ScanID:    scanID,
			Agreement: "full_only",
			Suggestion: ScanSuggestion{
				Action:        "rename",
				CanonicalName: fmt.Sprintf("Author %d", i),
				AuthorIDs:     []int{i + 1},
			},
		}
	}
	return out
}

// Overlapping replaces must leave one set of results. Each call used to list
// the scan's existing rows, queue their deletes and commit its own fresh rows;
// two calls that both listed before either committed each deleted nothing the
// other wrote, and the scan kept every result twice. Cross-validation, the
// pipeline's writer, calls ReplaceScanResults.
func TestReplaceScanResultsOverlapLeavesOneSet(t *testing.T) {
	s := newCASStore(t)
	scan, err := s.CreateScan("batch", nil, 4)
	require.NoError(t, err)

	for round := range 10 {
		errs := make([]error, 8)
		overlapping(len(errs), func(i int) {
			errs[i] = s.ReplaceScanResults(scan.ID, fourResults(scan.ID))
		})
		for _, e := range errs {
			require.NoError(t, e)
		}
		got, err := s.GetScanResults(scan.ID)
		require.NoError(t, err)
		require.Len(t, got, 4, "round %d: overlapping replaces left more than one set", round)
	}
}

// ReplaceScanResultsIfUnapplied already holds applyMu when it replaces, so it
// must use the unlocked replace: calling the locked ReplaceScanResults there
// would deadlock (sync.Mutex is not re-entrant) and this test would hang. It
// also checks that the two writers exclude each other. Before the fix the
// serialized IfUnapplied calls usually listed after the plain ones committed
// and so deleted their rows, which can hide the overlap; the plain-only test
// above is the one that proves it.
func TestReplaceScanResultsOverlapWithIfUnapplied(t *testing.T) {
	s := newCASStore(t)
	scan, err := s.CreateScan("batch", nil, 4)
	require.NoError(t, err)

	for round := range 10 {
		errs := make([]error, 8)
		overlapping(len(errs), func(i int) {
			if i%2 == 0 {
				errs[i] = s.ReplaceScanResults(scan.ID, fourResults(scan.ID))
				return
			}
			_, errs[i] = s.ReplaceScanResultsIfUnapplied(scan.ID, fourResults(scan.ID))
		})
		for _, e := range errs {
			require.NoError(t, e)
		}
		got, err := s.GetScanResults(scan.ID)
		require.NoError(t, err)
		require.Len(t, got, 4, "round %d: overlapping replaces left more than one set", round)
	}
}

// Concurrent nextID calls must hand out distinct IDs. The counter is a read
// followed by a separate write, so two callers that both read before either
// wrote returned the same ID.
func TestNextIDUniqueUnderConcurrency(t *testing.T) {
	s := newCASStore(t)
	const workers, perWorker = 8, 25
	ids := make([][]int, workers)
	errs := make([]error, workers)
	overlapping(workers, func(i int) {
		for range perWorker {
			id, err := s.nextID("scan_result")
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = append(ids[i], id)
		}
	})
	seen := make(map[int]bool, workers*perWorker)
	for i := range workers {
		require.NoError(t, errs[i])
		for _, id := range ids[i] {
			require.False(t, seen[id], "nextID returned %d twice", id)
			seen[id] = true
		}
	}
	require.Len(t, seen, workers*perWorker)
}

// A save racing a replace must never leave a row the replace should have
// deleted. With SaveScanResult and the replace serialized, the save lands
// either before the replace (and is deleted by it) or after it (and has a
// higher ID than every replace row, since IDs are handed out in order). A
// save that landed inside the replace's list-then-commit window instead got an
// ID below the replace's IDs and survived next to a full set. One save against
// one replace per round: with more replaces the later ones usually delete the
// stray row and hide the overlap.
func TestSaveScanResultOverlapWithReplace(t *testing.T) {
	s := newCASStore(t)
	scan, err := s.CreateScan("batch", nil, 4)
	require.NoError(t, err)

	for round := range 50 {
		errs := make([]error, 2)
		overlapping(len(errs), func(i int) {
			if i == 0 {
				errs[i] = s.SaveScanResult(&ScanResult{
					ScanID:     scan.ID,
					Agreement:  "full_only",
					Suggestion: ScanSuggestion{Action: "rename", CanonicalName: "saved"},
				})
				return
			}
			errs[i] = s.ReplaceScanResults(scan.ID, fourResults(scan.ID))
		})
		for _, e := range errs {
			require.NoError(t, e)
		}
		got, err := s.GetScanResults(scan.ID)
		require.NoError(t, err)

		var saved *ScanResult
		maxReplaceID := 0
		replaced := 0
		for i := range got {
			if got[i].Suggestion.CanonicalName == "saved" {
				saved = &got[i]
				continue
			}
			replaced++
			maxReplaceID = max(maxReplaceID, got[i].ID)
		}
		require.Equal(t, 4, replaced, "round %d: want exactly one replace set", round)
		if saved != nil {
			require.Greater(t, saved.ID, maxReplaceID,
				"round %d: the saved row survived a replace that ran after it", round)
		}
		// Start the next round from an empty scan.
		require.NoError(t, s.ReplaceScanResults(scan.ID, nil))
	}
}
