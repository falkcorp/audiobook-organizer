// file: internal/books/cleanstop_test.go
// version: 1.0.1
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

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The HNSW clean-stop marker on the served path (R1-15 follow-up, ported onto
// core storage): the kernel opens, prepares and migrates the store inside
// app.New; the first books migration step discards the marker before it
// writes, and Register takes what is left, under the store's lock, for the
// server's snapshot import (R1-15 round-9 review). A migration that ran, or
// a swap the slot held when Options ran (R1-25), voids it.

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

// TestCleanStopClaimWaitsForTheStoreLock (R1-15 round-9 review): the served
// boot takes the marker in Register, once the kernel holds the store's lock,
// not in Options before the open. A boot refused because another opener
// holds the store leaves the marker on disk, untouched, for that opener's
// run to settle; and a marker the holder writes as it lets go, after this
// boot's Options ran, is the one Register takes, so it cannot stay on disk
// vouching through a run that then writes the rows.
func TestCleanStopClaimWaitsForTheStoreLock(t *testing.T) {
	// hold opens the store at path with the rows the snapshot projects and
	// keeps it open: the opener this boot runs beside.
	hold := func(t *testing.T, path string) *database.PebbleStore {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		store, err := database.NewPebbleStore(path)
		require.NoError(t, err)
		seedCleanStopRows(t, store, path)
		return store
	}

	t.Run("a boot refused beside a holder leaves the marker", func(t *testing.T) {
		path := filepath.Join(tempDir(t), "gotham.pebble")
		holder := hold(t, path)
		t.Cleanup(func() { _ = holder.Close() })
		dir := database.HNSWSnapshotDir(path)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		plantCleanStop(t, dir)
		before, err := os.ReadFile(filepath.Join(dir, "hnsw.clean-stop"))
		require.NoError(t, err)

		edit, disk := onDisk(path)
		mod, opts, _ := composedOptions(t, false, "", edit, nil)
		disk(&opts)
		a, err := app.New(t.Context(), opts, mod)
		t.Cleanup(func() { Release(mod) })
		if a != nil {
			_ = a.Close(context.Background())
		}
		require.Error(t, err, "the boot opened a store another opener holds")
		after, rerr := os.ReadFile(filepath.Join(dir, "hnsw.clean-stop"))
		require.NoError(t, rerr, "a refused boot removed the marker")
		require.Equal(t, before, after, "a refused boot changed the marker")
	})

	t.Run("the marker the holder writes as it lets go is taken", func(t *testing.T) {
		path := filepath.Join(tempDir(t), "gotham.pebble")
		holder := hold(t, path)
		dir := database.HNSWSnapshotDir(path)
		require.NoError(t, os.MkdirAll(dir, 0o700))

		edit, disk := onDisk(path)
		c := bootComposedWith(t, false, "", func(cfg *config.Config) {
			edit(cfg)
			cfg.Embedding.Dimensions = 4
		}, nil, func(o *app.Options) {
			disk(o)
			// After Options, before app.New opens the store: the holder's
			// clean stop writes its marker, then lets the store go.
			plantCleanStop(t, dir)
			require.NoError(t, holder.Close())
		})
		cleanStopGone(t, dir, "after the boot")
		require.False(t, c.mod.migrated.Load(), "a books step ran in New: the case proves nothing")
		want := fmt.Sprintf("hnsw: loaded from on-disk snapshot in %s (reconcile_pending=false)", dir)
		require.Contains(t, c.newLog.String(), want, "the import did not get the holder's marker")
	})
}

// TestSwapBootVoidsTheCleanStopClaim (R1-25; the R1-23 todo on the P0a
// swap): a boot whose pre-boot swap applies a staged restore replaces the
// store and leaves hnsw/ beside it alone, so a marker on disk describes the
// rows from before the swap. Every stager discards the marker before it
// writes the intent; this marker is written after the stage (by a build
// from before the swap slot, run between the stage and this boot), so only
// the void Options reads from the slot keeps it from vouching. The control,
// the same marker with nothing staged, is trusted.
func TestSwapBootVoidsTheCleanStopClaim(t *testing.T) {
	for name, staged := range map[string]bool{"nothing staged": false, "a staged restore": true} {
		t.Run(name, func(t *testing.T) {
			h := newOfflineABO(t)
			store, err := database.NewPebbleStore(h.path)
			require.NoError(t, err)
			seedCleanStopRows(t, store, h.path)
			require.NoError(t, store.Close())
			if staged {
				info, err := backup.CreateBackup(h.path, "pebble", backup.BackupConfig{BackupDir: backup.ResolveDir("", h.path), MaxBackups: 5})
				require.NoError(t, err)
				restarts := 0
				_, err = h.liveStager(&restarts).StageRestore(context.Background(), filepath.Dir(info.Path), info.Filename, true, "user:alfred", nil)
				require.NoError(t, err)
				require.True(t, h.slot().Busy(), "nothing staged")
			}
			dir := database.HNSWSnapshotDir(h.path)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			plantCleanStop(t, dir)

			c := bootComposedWith(t, false, h.state, func(cfg *config.Config) {
				h.edit(cfg)
				cfg.Embedding.Dimensions = 4
			}, nil, h.disk)
			require.False(t, h.slot().Busy(), "the boot did not apply the staged restore")
			require.False(t, c.mod.migrated.Load(), "a books step ran in New: the case proves nothing")
			require.Equal(t, staged, c.mod.swapVoid != "", "the swap void: %q", c.mod.swapVoid)
			cleanStopGone(t, dir, "after the boot")
			want := fmt.Sprintf("hnsw: loaded from on-disk snapshot in %s (reconcile_pending=%t)", dir, staged)
			require.Contains(t, c.newLog.String(), want, "the server's snapshot import")
		})
	}
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

// TestBeforeMigrationDiscardsTheMarkerOnce: the observer books' manifest
// passes database.BooksSpec discards the marker durably on the first step
// of a boot and only marks the boot migrated on the later ones; a marker
// that may still vouch fails the step; without Options it discards nothing.
func TestBeforeMigrationDiscardsTheMarkerOnce(t *testing.T) {
	path := filepath.Join(tempDir(t), "gotham.pebble")
	dir := database.HNSWSnapshotDir(path)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	marker := filepath.Join(dir, "hnsw.clean-stop")

	unset := Module().(*booksModule)
	require.NoError(t, os.WriteFile(marker, nil, 0o600))
	require.NoError(t, unset.beforeMigration())
	require.True(t, unset.migrated.Load())
	_, err := os.Lstat(marker)
	require.NoError(t, err, "a module without Options discarded a marker")

	m := Module().(*booksModule)
	m.dbPath = path
	require.NoError(t, m.beforeMigration())
	require.True(t, m.migrated.Load())
	cleanStopGone(t, dir, "after the first step")
	require.Equal(t, database.HNSWCleanStopVoidMigrated, m.cleanStopVoid())

	require.NoError(t, os.WriteFile(marker, nil, 0o600))
	require.NoError(t, m.beforeMigration())
	_, err = os.Lstat(marker)
	require.NoError(t, err, "a later step discarded again")

	// A non-empty directory in the marker's place can be neither removed
	// nor emptied.
	require.NoError(t, os.Remove(marker))
	require.NoError(t, os.MkdirAll(filepath.Join(marker, "riddle"), 0o700))
	fresh := Module().(*booksModule)
	fresh.dbPath = path
	require.ErrorIs(t, fresh.beforeMigration(), database.ErrCleanStopNotDiscarded)
}

// TestServedBootRefusesAMigrationOverAMarkerThatMayStillVouch: on a HEAD
// store, whose books steps run inside app.New, a marker the first step
// cannot discard fails that step before it writes, and the boot with it:
// a legacy step (a store below LegacyCap) and a versioned one (a store at
// LegacyCap) alike.
func TestServedBootRefusesAMigrationOverAMarkerThatMayStillVouch(t *testing.T) {
	for name, version := range map[string]int{
		"a legacy step":    database.LegacyCap - 1,
		"a versioned step": database.LegacyCap,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(tempDir(t), "gotham.pebble")
			headStoreAtVersion(t, path, version, nil)
			marker := filepath.Join(database.HNSWSnapshotDir(path), "hnsw.clean-stop")
			require.NoError(t, os.MkdirAll(filepath.Join(marker, "riddle"), 0o700))

			edit, disk := onDisk(path)
			mod, opts, _ := composedOptions(t, false, "", edit, nil)
			disk(&opts)
			a, err := app.New(t.Context(), opts, mod)
			t.Cleanup(func() { Release(mod) })
			if a != nil {
				_ = a.Close(context.Background())
			}
			require.ErrorIs(t, err, database.ErrCleanStopNotDiscarded)
			require.True(t, mod.migrated.Load(), "no books step began: the case proves nothing")
			require.Nil(t, mod.store.Load(), "Register ran after a failed migration")
		})
	}
}

// TestSwapVoidReason: Options' read of the swap slot voids the clean-stop
// claim for a staged swap and, failing closed, for a slot it cannot read.
func TestSwapVoidReason(t *testing.T) {
	state := t.TempDir()
	require.Empty(t, swapVoidReason(state), "an empty slot")

	db := filepath.Join(t.TempDir(), "gotham.pebble")
	const id = "20261109T000000Z-oracle01"
	require.NoError(t, restorejournal.CreateIntent(state, restorejournal.Intent{
		ID: id, Kind: restorejournal.KindArchive, DBPath: db,
		Targets: []restorejournal.Target{{From: filepath.Join(restorejournal.StagingDir(db, id), "db"), To: db}},
	}))
	require.Contains(t, swapVoidReason(state), "pre-boot swap may replace the store")
	require.Contains(t, swapVoidReason(state), id)

	// A directory in pending.json's place: it exists, and it cannot be read
	// as an intent.
	unreadable := t.TempDir()
	require.NoError(t, os.MkdirAll(restorejournal.PendingPath(unreadable), 0o700))
	require.Contains(t, swapVoidReason(unreadable), "could not be read")
}
