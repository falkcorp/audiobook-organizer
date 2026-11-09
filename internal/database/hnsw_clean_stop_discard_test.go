// file: internal/database/hnsw_clean_stop_discard_test.go
// version: 1.0.0
// guid: 96bcd074-61a0-4b72-a157-959dc6e608c3
// last-edited: 2026-11-09

package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDiscardHNSWCleanStop (seams.d/R1-15.md, the P0a swap row; R1-25): the
// offline swaps, the live restore and the command-line writes discard the
// HNSW clean-stop marker beside the store before they change it, so the
// next boot reconciles the vector graph. No marker (or no snapshot
// directory) is nothing to do; a marker that did not parse is discarded
// without an error; a symbolic link at the marker's name is discarded and
// what it names is left alone; a marker that can be neither removed nor
// emptied stops the caller (ErrCleanStopNotDiscarded).
func TestDiscardHNSWCleanStop(t *testing.T) {
	db := filepath.Join(t.TempDir(), "gotham.pebble")
	marker := filepath.Join(HNSWSnapshotDir(db), hnswCleanStopFile)
	require.NoError(t, DiscardHNSWCleanStop(db), "no snapshot directory")

	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o700))
	require.NoError(t, DiscardHNSWCleanStop(db), "no marker")
	require.NoError(t, os.WriteFile(marker, []byte(`{"model":"oracle"}`), 0o600))
	require.NoError(t, DiscardHNSWCleanStop(db), "a marker that did not parse")
	_, err := os.Lstat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "the marker survived")

	target := filepath.Join(t.TempDir(), "penguin")
	require.NoError(t, os.WriteFile(target, []byte("umbrella"), 0o600))
	require.NoError(t, os.Symlink(target, marker))
	require.NoError(t, DiscardHNSWCleanStop(db))
	_, err = os.Lstat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "the symbolic link survived")
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "umbrella", string(got), "the link's target changed")

	// A non-empty directory in the marker's place: no unlink removes it and
	// nothing can empty it.
	require.NoError(t, os.MkdirAll(filepath.Join(marker, "riddle"), 0o700))
	require.ErrorIs(t, DiscardHNSWCleanStop(db), ErrCleanStopNotDiscarded, "a marker that cannot be removed")
}
