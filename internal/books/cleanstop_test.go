// file: internal/books/cleanstop_test.go
// version: 1.0.0
// guid: 90934ec3-bdf7-42d6-b6fd-7867e2638f55
// last-edited: 2026-11-09

package books

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The HNSW clean-stop marker on the served path (R1-15 follow-up, ported onto
// core storage): the kernel opens, prepares and migrates the store inside
// app.New, so books' Options takes the marker before New, and Register hands
// it to the store it builds for the server's snapshot import. A books
// migration that ran inside New voids it.

// cleanStopTruth is the Pebble truth count the tests' snapshots record: the
// one embedding row the stores hold.
func cleanStopTruth(string) (int, error) { return 1, nil }

// plantCleanStop writes a snapshot of one book to dir, as a clean stop leaves
// it, and the marker that vouches for it. The marker records the model the
// composed server classifies rows against ("": the tests configure no
// embedding client).
func plantCleanStop(t *testing.T, dir string) {
	t.Helper()
	s := database.NewHNSWEmbeddingStore(4)
	require.NoError(t, s.Upsert(context.Background(), "book", "joker", []float32{1, 0, 0, 0}, nil))
	s.SetComplete(true)
	require.NoError(t, s.ExportWithTruth(dir, cleanStopTruth))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hnsw.clean-stop"), []byte(`{"version":1,"model":""}`+"\n"), 0o644))
}

// seedCleanStopRows writes the book row and the embedding row the snapshot
// projects, so the server's import finds the Pebble count it recorded.
func seedCleanStopRows(t *testing.T, store *database.PebbleStore, path string) {
	t.Helper()
	_, err := store.CreateBook(&database.Book{ID: "joker", Title: "Hush", FilePath: filepath.Join(filepath.Dir(path), "library", "Hush.m4b")})
	require.NoError(t, err)
	require.NoError(t, database.NewEmbeddingStore(store.DB()).Upsert(database.Embedding{
		EntityType: "book", EntityID: "joker", Vector: []float32{1, 0, 0, 0}}))
}

// cleanStopGone fails the test unless dir's marker has left the disk.
func cleanStopGone(t *testing.T, dir, when string) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, "hnsw.clean-stop"))
	require.ErrorIs(t, err, fs.ErrNotExist, "the marker is still on disk %s", when)
}

// TestOptionsTakesTheCleanStopMarkerBeforeTheOpen: the marker is gone once
// Options returns, before app.New writes anything, so a migration that
// crashes inside New cannot leave it vouching.
func TestOptionsTakesTheCleanStopMarkerBeforeTheOpen(t *testing.T) {
	path := filepath.Join(tempDir(t), "gotham.pebble")
	dir := database.HNSWSnapshotDir(path)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	plantCleanStop(t, dir)
	edit, _ := onDisk(path)
	mod, _, _ := composedOptions(t, false, "", edit, nil)
	cleanStopGone(t, dir, "after Options")
	require.NotNil(t, mod.cleanStop, "Options took no claim")
}

// TestServedBootHandsTheCleanStopMarkerToTheImport boots the composition over
// a store on disk with a snapshot and its marker beside it, and reads what
// the server's own snapshot import, inside Register, made of it. A store the
// boot does not migrate keeps the marker for the import, which loads the
// snapshot as fresh; a HEAD store, whose books steps run inside New, voids
// it, and the import reconciles.
func TestServedBootHandsTheCleanStopMarkerToTheImport(t *testing.T) {
	for name, tc := range map[string]struct {
		head      bool
		reconcile bool
	}{
		"a store at this build's schema": {head: false, reconcile: false},
		"a HEAD store the boot migrates": {head: true, reconcile: true},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(tempDir(t), "gotham.pebble")
			if tc.head {
				headStoreAt(t, path, func(store *database.PebbleStore) { seedCleanStopRows(t, store, path) })
			} else {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				store, err := database.NewPebbleStore(path)
				require.NoError(t, err)
				seedCleanStopRows(t, store, path)
				require.NoError(t, store.Close())
			}
			dir := database.HNSWSnapshotDir(path)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			plantCleanStop(t, dir)

			edit, disk := onDisk(path)
			c := bootComposedWith(t, false, "", func(cfg *config.Config) {
				edit(cfg)
				cfg.Embedding.Dimensions = 4
			}, nil, disk)
			cleanStopGone(t, dir, "after the boot")
			require.Equal(t, tc.head, c.mod.migrated.Load(), "whether a books step ran in New: the case proves nothing")

			want := fmt.Sprintf("hnsw: loaded from on-disk snapshot in %s (reconcile_pending=%t)", dir, tc.reconcile)
			require.Contains(t, c.newLog.String(), want, "the server's snapshot import")
		})
	}
}
