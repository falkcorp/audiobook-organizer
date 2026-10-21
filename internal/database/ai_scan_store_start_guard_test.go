// file: internal/database/ai_scan_store_start_guard_test.go
// version: 1.0.1
// guid: 7d2a9e41-3b85-4c60-bf17-9e4a1c6d8f23
// last-edited: 2026-10-20

package database

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func newGuardStore(t *testing.T) *AIScanStore {
	t.Helper()
	store, err := NewAIScanStore(t.TempDir() + "/ai_scans.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Two starts that race produce one scan and one refusal, never two scans. The
// run under -race also proves the check and the create share one lock.
func TestCreateScanIfNoneActive_ConcurrentStartsCreateExactlyOne(t *testing.T) {
	store := newGuardStore(t)
	const starters = 8
	var created, refused atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range starters {
		wg.Go(func() {
			<-start
			_, err := store.CreateScanIfNoneActive("realtime", nil, 3)
			var running *ScanAlreadyRunningError
			switch {
			case err == nil:
				created.Add(1)
			case errors.As(err, &running):
				refused.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 1, created.Load())
	require.EqualValues(t, starters-1, refused.Load())
	scans, err := store.ListScans()
	require.NoError(t, err)
	require.Len(t, scans, 1)
}

func TestCreateScanIfNoneActive_RefusalNamesTheRunningScanAndClearsOnceItEnds(t *testing.T) {
	store := newGuardStore(t)
	first, err := store.CreateScanIfNoneActive("realtime", nil, 1)
	require.NoError(t, err)

	for _, status := range []string{"pending", "scanning", "enriching", "cross_validating"} {
		require.NoError(t, store.UpdateScanStatus(first.ID, status))
		_, err = store.CreateScanIfNoneActive("batch", nil, 1)
		var running *ScanAlreadyRunningError
		require.ErrorAs(t, err, &running, status)
		require.Equal(t, first.ID, running.ScanID)
		require.Contains(t, err.Error(), "already running")
	}

	for _, status := range []string{"complete", "failed", "canceled", "superseded"} {
		require.NoError(t, store.UpdateScanStatus(first.ID, status))
		next, err := store.CreateScanIfNoneActive("realtime", nil, 1)
		require.NoError(t, err, status)
		// Finish the new scan so the loop's next status starts from a clean slate.
		require.NoError(t, store.UpdateScanStatus(next.ID, "complete"))
	}
}

// The nightly recording creates a scan whatever is running: it has already paid
// for those results.
func TestCreateScanTagged_IsNeverRefusedByARunningScan(t *testing.T) {
	store := newGuardStore(t)
	_, err := store.CreateScanIfNoneActive("realtime", nil, 1)
	require.NoError(t, err)
	tagged, err := store.CreateScanTagged("batch", nil, 0, "ai-dedup-batch:night-1")
	require.NoError(t, err)
	require.Equal(t, "ai-dedup-batch:night-1", tagged.OperationID)
}

func TestSetResultKeepIDs_FillsOnlyEmptyKeepsAndNeverTouchesApplied(t *testing.T) {
	store := newGuardStore(t)
	scan, err := store.CreateScan("realtime", nil, 2)
	require.NoError(t, err)
	a := &ScanResult{ScanID: scan.ID, Suggestion: ScanSuggestion{Action: "merge"}}
	b := &ScanResult{ScanID: scan.ID, Suggestion: ScanSuggestion{Action: "merge", KeepID: 7}}
	require.NoError(t, store.SaveScanResult(a))
	require.NoError(t, store.SaveScanResult(b))
	require.NoError(t, store.MarkResultApplied(scan.ID, a.ID))

	require.NoError(t, store.SetResultKeepIDs(scan.ID, map[int]int{a.ID: 3, b.ID: 99, 12345: 1}))

	got, err := store.GetScanResults(scan.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, 3, got[0].Suggestion.KeepID)
	require.True(t, got[0].Applied, "the apply flag survives the backfill")
	require.Equal(t, 7, got[1].Suggestion.KeepID, "an existing keep is not overwritten")
}
