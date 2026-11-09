// file: internal/database/hnsw_snapshot_regular_files_test.go
// version: 1.0.0
// guid: 8f403a07-63a7-48c4-b34c-fda21d4e77b8
// last-edited: 2026-11-09

package database_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// moveOutAndLink moves dir/name to a sibling directory, the restore's
// database directory in the review's shape, and leaves a relative symlink
// to it at dir/name: a snapshot file that points out of the snapshot
// directory, at a file a restore can write.
func moveOutAndLink(t *testing.T, dir, name string) {
	t.Helper()
	out := filepath.Join(filepath.Dir(dir), "audiobooks.pebble")
	require.NoError(t, os.MkdirAll(out, 0o755))
	require.NoError(t, os.Rename(filepath.Join(dir, name), filepath.Join(out, name)))
	if err := os.Symlink(filepath.Join("..", "audiobooks.pebble", name), filepath.Join(dir, name)); err != nil {
		t.Skipf("the filesystem does not make symlinks: %v", err)
	}
}

// exportedSnapshot exports a complete four-book store with its truth
// sidecar to base/hnsw and returns that directory.
func exportedSnapshot(t *testing.T) string {
	t.Helper()
	src := newExportTestStore(t, 4)
	src.SetComplete(true)
	dir := filepath.Join(t.TempDir(), "hnsw")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, src.ExportWithTruth(dir, func(string) (int, error) { return 4, nil }))
	return dir
}

// TestImport_RefusesASnapshotFileThatIsNotRegular (R1-15 round-7
// verification review, S1-r7): with hnsw/hnsw-book.bin a symlink to a file
// in the database directory, a restore writes that file and the import
// loaded its bytes as the graph. The import reads only regular files in
// the snapshot directory: a graph or meta file that is a symlink fails the
// import (the index is rebuilt from Pebble), and a truth sidecar that is
// one leaves the snapshot unconfirmed, so it is reconciled.
func TestImport_RefusesASnapshotFileThatIsNotRegular(t *testing.T) {
	for _, name := range []string{"hnsw-book.bin", "hnsw-book.meta.json"} {
		t.Run(name, func(t *testing.T) {
			dir := exportedSnapshot(t)
			moveOutAndLink(t, dir, name)
			err := newBoundImportStore(t).Import(dir)
			require.Error(t, err, "the import loaded %s through a symlink", name)
			assert.Contains(t, err.Error(), "not a regular file")
		})
	}
	t.Run("hnsw-book.truth.json", func(t *testing.T) {
		dir := exportedSnapshot(t)
		moveOutAndLink(t, dir, "hnsw-book.truth.json")
		imp := newBoundImportStore(t)
		require.NoError(t, imp.ImportCountChecked(dir, func(string) (int, error) { return 4, nil }))
		assert.True(t, imp.NeedsReconcile(), "a truth sidecar read through a symlink confirmed the snapshot")
	})
	t.Run("regular files import", func(t *testing.T) {
		dir := exportedSnapshot(t)
		imp := newBoundImportStore(t)
		require.NoError(t, imp.ImportCountChecked(dir, func(string) (int, error) { return 4, nil }))
		assert.False(t, imp.NeedsReconcile())
		_, err := os.Lstat(filepath.Join(dir, "hnsw-book.bin"))
		assert.False(t, errors.Is(err, os.ErrNotExist))
	})
}
