// file: internal/database/scan_fail_count_errors_test.go
// version: 1.0.1
// guid: e3b2b2ee-6483-4490-92fa-10a0ad4059f8
// last-edited: 2026-10-03

package database

import (
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// TestPebbleScanFailCount_ErrorsAreReturned: GetScanFailCount used to answer
// (0, nil) for every error, so an unreadable counter looked like a file that
// had never failed and auto-quarantine silently stopped. A missing counter is
// still (0, nil); a corrupt one is an error; and IncrScanFailCount must not
// paper over it by restarting the count at 1.
func TestPebbleScanFailCount_ErrorsAreReturned(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	n, err := store.GetScanFailCount("absent")
	require.NoError(t, err, "a missing counter is not an error")
	require.Zero(t, n)

	require.NoError(t, store.db.Set([]byte("scan_fail:corrupt"), []byte("not-a-number"), pebble.Sync))
	_, err = store.GetScanFailCount("corrupt")
	require.Error(t, err, "an unparseable counter must not read as 0")
	_, err = store.IncrScanFailCount("corrupt")
	require.Error(t, err, "an unreadable counter must not be restarted at 1")

	n, err = store.IncrScanFailCount("fresh")
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
