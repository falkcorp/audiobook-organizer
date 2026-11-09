// file: internal/books/offline_swap_test.go
// version: 1.0.0
// guid: 0c7e4b92-3f1a-4d58-8e26-b9a5d17f4c30
// last-edited: 2026-11-08

package books

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRemoveHNSWCleanStop (seams.d/R1-15.md, the P0a swap row): the offline
// swaps' BeforeSwap removes the HNSW clean-stop marker beside the store
// being replaced, so the next boot reconciles the vector graph; no marker
// (or no snapshot directory) is nothing to do; a marker that cannot be
// removed stops the command, so nothing is staged with it in place.
func TestRemoveHNSWCleanStop(t *testing.T) {
	db := filepath.Join(t.TempDir(), "gotham.pebble")
	marker := filepath.Join(filepath.Dir(db), "hnsw", "hnsw.clean-stop")
	require.NoError(t, removeHNSWCleanStop(db), "no snapshot directory")

	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o700))
	require.NoError(t, removeHNSWCleanStop(db), "no marker")
	require.NoError(t, os.WriteFile(marker, []byte(`{"model":"oracle"}`), 0o600))
	require.NoError(t, removeHNSWCleanStop(db))
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "the marker survived")

	// A marker the unlink refuses: a non-empty directory in its place.
	require.NoError(t, os.MkdirAll(filepath.Join(marker, "riddle"), 0o700))
	require.Error(t, removeHNSWCleanStop(db), "a marker that cannot be removed")
}
