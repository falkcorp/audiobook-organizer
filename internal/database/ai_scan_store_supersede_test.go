// file: internal/database/ai_scan_store_supersede_test.go
// version: 1.0.0
// guid: 314ce945-5e47-40d3-87e0-a6a94d5f5c03
// last-edited: 2026-09-19

package database

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// F5: superseding and applying must be one decision. Either the apply lands
// first and the scan is not superseded, or the scan is superseded first and
// the apply is refused — never a superseded scan with an applied result.
func TestSupersedeAndApplyAreExclusive(t *testing.T) {
	s, err := NewAIScanStore(filepath.Join(t.TempDir(), "aiscan.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	for range 20 {
		old, err := s.CreateScan("batch", nil, 0)
		require.NoError(t, err)
		require.NoError(t, s.ReplaceScanResults(old.ID, []ScanResult{{Agreement: "full_only"}}))
		require.NoError(t, s.UpdateScanStatus(old.ID, "complete"))
		rs, err := s.GetScanResults(old.ID)
		require.NoError(t, err)

		var wg sync.WaitGroup
		var superseded bool
		var applyErr error
		wg.Add(2)
		go func() { defer wg.Done(); superseded, _ = s.SupersedeIfUnapplied(old.ID, old.ID+1000) }()
		go func() { defer wg.Done(); applyErr = s.MarkResultApplied(old.ID, rs[0].ID) }()
		wg.Wait()

		after, err := s.GetScanResults(old.ID)
		require.NoError(t, err)
		got, err := s.GetScan(old.ID)
		require.NoError(t, err)
		if superseded {
			require.Equal(t, "superseded", got.Status)
			require.Equal(t, old.ID+1000, got.SupersededBy)
			require.False(t, after[0].Applied, "a superseded scan must hold no applied result")
			var se *ScanSupersededError
			require.True(t, errors.As(applyErr, &se), "the apply must be refused, got %v", applyErr)
			require.Equal(t, old.ID+1000, se.By)
		} else {
			require.NoError(t, applyErr)
			require.True(t, after[0].Applied)
			require.Equal(t, "complete", got.Status, "a scan with an applied result is never superseded")
		}
	}
}

func TestSupersedeIfUnappliedSupersedesAnUnreviewedScan(t *testing.T) {
	s, err := NewAIScanStore(filepath.Join(t.TempDir(), "aiscan.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	old, err := s.CreateScan("batch", nil, 0)
	require.NoError(t, err)
	require.NoError(t, s.UpdateScanStatus(old.ID, "complete"))
	ok, err := s.SupersedeIfUnapplied(old.ID, 99)
	require.NoError(t, err)
	require.True(t, ok)
}
