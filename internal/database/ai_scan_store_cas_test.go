// file: internal/database/ai_scan_store_cas_test.go
// version: 1.0.0
// guid: 426ca6ae-254d-41d8-8305-b8e6d5779aaf
// last-edited: 2026-09-19

package database

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func newCASStore(t *testing.T) *AIScanStore {
	t.Helper()
	s, err := NewAIScanStore(filepath.Join(t.TempDir(), "aiscan.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// A batch id must never be written onto a phase an operator canceled while
// CreateBatch was in flight: that would leave a live paid batch attached to a
// dead scan, never canceled and never collected.
func TestTransitionPhaseRefusesCanceledPhaseOrScan(t *testing.T) {
	s := newCASStore(t)
	scan, err := s.CreateScan("batch", nil, 0)
	require.NoError(t, err)
	_, err = s.CreatePhase(scan.ID, "full_scan", "m")
	require.NoError(t, err)
	require.NoError(t, s.UpdatePhaseStatus(scan.ID, "full_scan", "submitting", ""))

	require.NoError(t, s.UpdatePhaseStatus(scan.ID, "full_scan", "canceled", ""))
	ok, err := s.TransitionPhase(scan.ID, "full_scan", []string{"submitting"}, "submitted", "batch_x")
	require.NoError(t, err)
	require.False(t, ok)
	p, err := s.GetPhase(scan.ID, "full_scan")
	require.NoError(t, err)
	require.Equal(t, "canceled", p.Status)
	require.Empty(t, p.BatchID)

	// Phase still submitting but the scan canceled: also refused.
	_, err = s.CreatePhase(scan.ID, "groups_scan", "m")
	require.NoError(t, err)
	require.NoError(t, s.UpdatePhaseStatus(scan.ID, "groups_scan", "submitting", ""))
	require.NoError(t, s.UpdateScanStatus(scan.ID, "canceled"))
	ok, err = s.TransitionPhase(scan.ID, "groups_scan", []string{"submitting"}, "submitted", "batch_y")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestTransitionPhaseAppliesFromExpectedState(t *testing.T) {
	s := newCASStore(t)
	scan, err := s.CreateScan("batch", nil, 0)
	require.NoError(t, err)
	require.NoError(t, s.UpdateScanStatus(scan.ID, "scanning"))
	_, err = s.CreatePhase(scan.ID, "full_scan", "m")
	require.NoError(t, err)
	require.NoError(t, s.UpdatePhaseStatus(scan.ID, "full_scan", "submitting", ""))
	ok, err := s.TransitionPhase(scan.ID, "full_scan", []string{"submitting"}, "submitted", "batch_x")
	require.NoError(t, err)
	require.True(t, ok)
	p, err := s.GetPhase(scan.ID, "full_scan")
	require.NoError(t, err)
	require.Equal(t, "batch_x", p.BatchID)
}

func TestCompleteScanIfActiveKeepsCanceled(t *testing.T) {
	s := newCASStore(t)
	scan, err := s.CreateScan("batch", nil, 0)
	require.NoError(t, err)
	require.NoError(t, s.UpdateScanStatus(scan.ID, "canceled"))
	ok, err := s.CompleteScanIfActive(scan.ID)
	require.NoError(t, err)
	require.False(t, ok)
	got, err := s.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "canceled", got.Status)
}

// A replay's anyApplied check and its replace must be one decision with the
// apply: either the apply lands first and the replace is skipped, or the
// replace lands first and the apply (of a now-gone result id) fails. Never an
// apply reported successful and then wiped by the replace.
func TestReplaceScanResultsIfUnappliedIsExclusiveWithApply(t *testing.T) {
	s := newCASStore(t)
	for range 30 {
		scan, err := s.CreateScan("batch", nil, 0)
		require.NoError(t, err)
		require.NoError(t, s.ReplaceScanResults(scan.ID, []ScanResult{{Agreement: "full_only"}}))
		rs, err := s.GetScanResults(scan.ID)
		require.NoError(t, err)

		var wg sync.WaitGroup
		var applyErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = s.ReplaceScanResultsIfUnapplied(scan.ID, []ScanResult{{Agreement: "full_only"}})
		}()
		go func() { defer wg.Done(); applyErr = s.MarkResultApplied(scan.ID, rs[0].ID) }()
		wg.Wait()
		if applyErr == nil {
			after, err := s.GetScanResults(scan.ID)
			require.NoError(t, err)
			require.Len(t, after, 1)
			require.True(t, after[0].Applied, "a successful apply was wiped by the replace")
		}
	}
}
