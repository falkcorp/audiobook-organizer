// file: internal/database/ai_scan_store_test.go
// version: 1.4.0
// guid: b8c4d0e2-5f6a-7b8c-9d0e-1f2a3b4c5d6e

package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAIScanStore(t *testing.T) {
	tmpdir := t.TempDir()
	store, err := NewAIScanStore(tmpdir + "/ai_scans.db")
	require.NoError(t, err)
	require.NotNil(t, store)
	defer store.Close()
}

func TestAIScanStore_NextID(t *testing.T) {
	tmpdir := t.TempDir()
	store, err := NewAIScanStore(tmpdir + "/ai_scans.db")
	require.NoError(t, err)
	defer store.Close()

	// First ID should be 1
	id1, err := store.nextID("scan")
	require.NoError(t, err)
	require.Equal(t, 1, id1)

	// Second ID should be 2
	id2, err := store.nextID("scan")
	require.NoError(t, err)
	require.Equal(t, 2, id2)

	// scan_result counter should be independent
	rid1, err := store.nextID("scan_result")
	require.NoError(t, err)
	require.Equal(t, 1, rid1)
}

func TestAIScanStore_ReopenPreservesCounters(t *testing.T) {
	tmpdir := t.TempDir()
	dbPath := tmpdir + "/ai_scans.db"

	// Open, increment, close
	store, err := NewAIScanStore(dbPath)
	require.NoError(t, err)
	_, err = store.nextID("scan")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	// Reopen — counter should continue from 2
	store2, err := NewAIScanStore(dbPath)
	require.NoError(t, err)
	defer store2.Close()

	id, err := store2.nextID("scan")
	require.NoError(t, err)
	require.Equal(t, 2, id)
}

func TestAIScanStoreCRUD(t *testing.T) {
	store, err := NewAIScanStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()

	// Create scan
	scan, err := store.CreateScan("batch", map[string]string{"groups": "gpt-5-mini", "full": "o4-mini"}, 4826)
	require.NoError(t, err)
	require.Equal(t, "pending", scan.Status)
	require.Equal(t, 4826, scan.AuthorCount)

	// Update status
	err = store.UpdateScanStatus(scan.ID, "scanning")
	require.NoError(t, err)

	// Get scan
	got, err := store.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "scanning", got.Status)

	// Create phase
	phase, err := store.CreatePhase(scan.ID, "groups_scan", "gpt-5-mini")
	require.NoError(t, err)
	require.Equal(t, "pending", phase.Status)

	// Update phase with batch ID
	err = store.UpdatePhaseStatus(scan.ID, "groups_scan", "submitted", "batch_abc123")
	require.NoError(t, err)

	// Get phases for scan
	phases, err := store.GetPhases(scan.ID)
	require.NoError(t, err)
	require.Len(t, phases, 1)
	require.Equal(t, "batch_abc123", phases[0].BatchID)

	// Save scan results
	result := &ScanResult{
		ScanID:    scan.ID,
		Agreement: "agreed",
		Suggestion: ScanSuggestion{
			Action:        "merge",
			CanonicalName: "J. N. Chaney",
			Confidence:    "high",
		},
	}
	err = store.SaveScanResult(result)
	require.NoError(t, err)

	// Get results
	results, err := store.GetScanResults(scan.ID)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "agreed", results[0].Agreement)

	// Mark result applied
	err = store.MarkResultApplied(scan.ID, results[0].ID)
	require.NoError(t, err)
	results, _ = store.GetScanResults(scan.ID)
	require.True(t, results[0].Applied)

	// List scans
	scans, err := store.ListScans()
	require.NoError(t, err)
	require.Len(t, scans, 1)

	// Delete scan
	err = store.DeleteScan(scan.ID)
	require.NoError(t, err)
	scans, _ = store.ListScans()
	require.Empty(t, scans)
}

func TestAIScanStore_Optimize(t *testing.T) {
	tmpdir := t.TempDir()
	store, err := NewAIScanStore(tmpdir + "/ai_scans.db")
	require.NoError(t, err)
	defer store.Close()
	err = store.Optimize(context.Background())
	assert.NoError(t, err)
}

func TestNewAIScanStoreFromDB(t *testing.T) {
	// Verify shared-DB mode: all operations work, Close is a no-op.
	tmpdir := t.TempDir()
	ps, err := NewPebbleStore(tmpdir + "/main.pebble")
	require.NoError(t, err)
	defer ps.Close()

	store, err := NewAIScanStoreFromDB(ps.DB())
	require.NoError(t, err)
	require.NotNil(t, store)

	// Round-trip a scan.
	scan, err := store.CreateScan("batch", map[string]string{"groups": "gpt-4o-mini"}, 5)
	require.NoError(t, err)
	require.Equal(t, 1, scan.ID)

	got, err := store.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, scan.ID, got.ID)
	require.Equal(t, "pending", got.Status)

	// Close must be a no-op (ps.Close() still works after this).
	require.NoError(t, store.Close())
	require.NoError(t, store.Optimize(context.Background()))

	// Verify main DB is still alive.
	_, err = store.ListScans()
	require.NoError(t, err)
}
