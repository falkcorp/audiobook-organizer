// file: internal/database/ai_scan_store_artifacts_test.go
// version: 1.0.0
// guid: ce18be7e-ff47-499b-a710-905aa2a76b43
// last-edited: 2026-09-19

package database

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAIScanStorePhaseArtifactsAndReplaceResults(t *testing.T) {
	s, err := NewAIScanStore(filepath.Join(t.TempDir(), "aiscan.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	scan, err := s.CreateScan("realtime", nil, 3)
	require.NoError(t, err)
	_, err = s.CreatePhase(scan.ID, "full_scan", "m")
	require.NoError(t, err)

	// Artifacts: overwrite by name, scoped by phase, and invisible to GetPhases.
	require.NoError(t, s.SavePhaseArtifact(scan.ID, "full_scan", "chunk:000000", json.RawMessage(`[1]`)))
	require.NoError(t, s.SavePhaseArtifact(scan.ID, "full_scan", "chunk:000000", json.RawMessage(`[2]`)))
	require.NoError(t, s.SavePhaseArtifact(scan.ID, "groups_scan", "group_author_ids", json.RawMessage(`[[1,2]]`)))
	arts, err := s.GetPhaseArtifacts(scan.ID, "full_scan")
	require.NoError(t, err)
	require.Equal(t, map[string]json.RawMessage{"chunk:000000": json.RawMessage(`[2]`)}, arts)
	phases, err := s.GetPhases(scan.ID)
	require.NoError(t, err)
	require.Len(t, phases, 1, "artifacts must not be read back as phases")

	// ReplaceScanResults: a second run leaves one set, not two.
	mk := func() []ScanResult {
		return []ScanResult{{Agreement: "full_only"}, {Agreement: "agreed"}}
	}
	require.NoError(t, s.ReplaceScanResults(scan.ID, mk()))
	require.NoError(t, s.ReplaceScanResults(scan.ID, mk()))
	rs, err := s.GetScanResults(scan.ID)
	require.NoError(t, err)
	require.Len(t, rs, 2)

	// DeleteScan removes artifacts too.
	require.NoError(t, s.DeleteScan(scan.ID))
	arts, err = s.GetPhaseArtifacts(scan.ID, "full_scan")
	require.NoError(t, err)
	require.Empty(t, arts)
}
