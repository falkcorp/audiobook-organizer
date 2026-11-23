// file: internal/books/cleanstop_test.go
// version: 1.0.2
// guid: 90934ec3-bdf7-42d6-b6fd-7867e2638f55
// last-edited: 2026-11-09

package books

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
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
	plantCleanStopAs(t, dir, 4, "")
}

// plantCleanStopAs is plantCleanStop for a snapshot of dims-wide vectors whose
// marker records model: the pair a store's own settings give the boot, which
// the boot compares the marker with (a mismatch reconciles instead of
// importing as current).
func plantCleanStopAs(t *testing.T, dir string, dims int, model string) {
	t.Helper()
	vec := make([]float32, dims)
	vec[0] = 1
	s := database.NewHNSWEmbeddingStore(dims)
	require.NoError(t, s.Upsert(context.Background(), "book", "joker", vec, nil))
	s.SetComplete(true)
	require.NoError(t, s.ExportWithTruth(dir, cleanStopTruth))
	marker := fmt.Sprintf(`{"version":1,"model":%q}`, model) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hnsw.clean-stop"), []byte(marker), 0o644))
}

// seedCleanStopRows writes the book row and the embedding row the snapshot
// projects, so the server's import finds the Pebble count it recorded.
func seedCleanStopRows(t *testing.T, store *database.PebbleStore, path string) {
	t.Helper()
	seedCleanStopRowsDims(t, store, path, 4)
}

// seedCleanStopRowsDims is seedCleanStopRows with dims-wide vectors.
func seedCleanStopRowsDims(t *testing.T, store *database.PebbleStore, path string, dims int) {
	t.Helper()
	_, err := store.CreateBook(&database.Book{ID: "joker", Title: "Hush", FilePath: filepath.Join(filepath.Dir(path), "library", "Hush.m4b")})
	require.NoError(t, err)
	vec := make([]float32, dims)
	vec[0] = 1
	require.NoError(t, database.NewEmbeddingStore(store.DB()).Upsert(database.Embedding{
		EntityType: "book", EntityID: "joker", Vector: vec}))
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
//
// The store is the frozen one R2 leaves (testdata/r2store, copyR2Store): a
// store a build with books' upgrade floor opens at all.
func TestCleanStopClaimWaitsForTheStoreLock(t *testing.T) {
	// hold opens the store at path with the rows the snapshot projects and
	// keeps it open: the opener this boot runs beside.
	hold := func(t *testing.T, path string) *database.PebbleStore {
		t.Helper()
		store, err := database.OpenStandalone(path, StandaloneOptions(database.StandaloneOptions{}))
		require.NoError(t, err)
		seedCleanStopRowsDims(t, store, path, r2StoreEmbeddingDims)
		return store
	}

	t.Run("a boot refused beside a holder leaves the marker", func(t *testing.T) {
		work := copyR2Store(t)
		path := r2StoreDB(work)
		holder := hold(t, path)
		t.Cleanup(func() { _ = holder.Close() })
		dir := database.HNSWSnapshotDir(path)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		plantCleanStop(t, dir)
		before, err := os.ReadFile(filepath.Join(dir, "hnsw.clean-stop"))
		require.NoError(t, err)

		edit, boot := r2StoreBootConfig(t, work)
		mod, opts, _ := composedOptions(t, false, r2StoreState(work), edit, nil)
		boot(&opts)
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
		work := copyR2Store(t)
		path := r2StoreDB(work)
		holder := hold(t, path)
		dir := database.HNSWSnapshotDir(path)
		require.NoError(t, os.MkdirAll(dir, 0o700))

		edit, boot := r2StoreBootConfig(t, work)
		c := bootComposedWith(t, false, r2StoreState(work), edit, nil, func(o *app.Options) {
			boot(o)
			// After Options, before app.New opens the store: the holder's
			// clean stop writes its marker, then lets the store go.
			plantCleanStopAs(t, dir, r2StoreEmbeddingDims, r2StoreEmbeddingModel)
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
			// The harness booted serve over this path, so the store carries the
			// flip's level: a standalone open plans it (StandaloneSpec).
			store, err := database.OpenStandalone(h.path, StandaloneOptions(database.StandaloneOptions{}))
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
// the frozen R2 store (copyR2Store) with a snapshot and its marker beside it,
// and reads what the server's own snapshot import, inside Register, made of it.
// No books step runs at this build's boot (the floor refuses every store with
// one pending), so the boot keeps the marker for the import, which loads the
// snapshot as fresh. The case a boot that migrates voids the marker, and the
// import reconciles, is TestServedBootRefusesAMigrationOverAMarkerThatMayStillVouch's
// and TestBeforeMigrationDiscardsTheMarkerOnce's: no store this build accepts
// has a step to run, so there is no served boot of it left to test here.
func TestServedBootHandsTheCleanStopMarkerToTheImport(t *testing.T) {
	work := copyR2Store(t)
	path := r2StoreDB(work)
	store, err := database.OpenStandalone(path, StandaloneOptions(database.StandaloneOptions{}))
	require.NoError(t, err)
	seedCleanStopRowsDims(t, store, path, r2StoreEmbeddingDims)
	require.NoError(t, store.Close())
	dir := database.HNSWSnapshotDir(path)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	// The R2 store's own settings give the embedding width and model; the
	// marker must record them to be the current one.
	edit, disk := r2StoreBootConfig(t, work)
	plantCleanStopAs(t, dir, r2StoreEmbeddingDims, r2StoreEmbeddingModel)
	c := bootComposedWith(t, false, r2StoreState(work), edit, nil, disk)
	cleanStopGone(t, dir, "after the boot")
	require.False(t, c.mod.migrated.Load(), "a books step ran in New: the case proves nothing")

	want := fmt.Sprintf("hnsw: loaded from on-disk snapshot in %s (reconcile_pending=false)", dir)
	require.Contains(t, c.newLog.String(), want, "the server's snapshot import")
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

// pendingStep is the module with one more versioned step registered after
// books' last, which the R2 store does not hold: the pending step a release
// after R3 will add. Its Up runs the observer books' manifest passes its own
// steps (beforeMigration), so it meets the clean-stop marker as a real step does.
type pendingStep struct {
	module.Module
	observe func() error
}

func (p pendingStep) Manifest() module.Manifest {
	m := p.Module.Manifest()
	m.Storage.Migrations = append(slices.Clone(m.Storage.Migrations), storage.Migration{
		Version: booksLevelStepVersion + 1, Name: "test.pending-step", Compat: storage.Additive,
		Up: func(context.Context, *storage.MigrationTx) error { return p.observe() },
	})
	return m
}

// TestServedBootRefusesAMigrationOverAMarkerThatMayStillVouch: on the R2 store
// with one versioned step pending (a release after R3's step, pendingStep),
// whose books step runs inside app.New, a marker the first step cannot
// discard fails that step before it writes, and the boot with it. The steps
// books itself had pending at R1 are gone (the floor refuses those stores), so
// the step is the test's own; the observer it runs is books' real one.
func TestServedBootRefusesAMigrationOverAMarkerThatMayStillVouch(t *testing.T) {
	work := copyR2Store(t)
	path := r2StoreDB(work)
	marker := filepath.Join(database.HNSWSnapshotDir(path), "hnsw.clean-stop")
	require.NoError(t, os.MkdirAll(filepath.Join(marker, "riddle"), 0o700))

	edit, disk := r2StoreBootConfig(t, work)
	mod, opts, _ := composedOptions(t, false, r2StoreState(work), edit, nil)
	disk(&opts)
	a, err := app.New(t.Context(), opts, pendingStep{Module: mod, observe: mod.beforeMigration})
	t.Cleanup(func() { Release(mod) })
	if a != nil {
		_ = a.Close(context.Background())
	}
	require.ErrorIs(t, err, database.ErrCleanStopNotDiscarded)
	require.True(t, mod.migrated.Load(), "no books step began: the case proves nothing")
	require.Nil(t, mod.store.Load(), "Register ran after a failed migration")
}

// TestServedBootRefusesAClaimOfAMarkerThatMayStillVouch (R1-25 review S-1):
// on a store at this build's schema no books step runs, so Register's take
// is the one that meets the marker; a marker it can neither remove nor
// empty fails the boot, as it fails the first migration step above, rather
// than staying on disk vouching for the snapshot through a run that writes
// the rows. Register kept the store before the take, so Release closes it.
// TestServedBootHandsTheCleanStopMarkerToTheImport is the control: the same
// store with a marker that can be taken boots.
func TestServedBootRefusesAClaimOfAMarkerThatMayStillVouch(t *testing.T) {
	work := copyR2Store(t)
	path := r2StoreDB(work)
	marker := filepath.Join(database.HNSWSnapshotDir(path), "hnsw.clean-stop")
	require.NoError(t, os.MkdirAll(filepath.Join(marker, "riddle"), 0o700))

	edit, boot := r2StoreBootConfig(t, work)
	mod, opts, _ := composedOptions(t, false, r2StoreState(work), edit, nil)
	boot(&opts)
	a, err := app.New(t.Context(), opts, mod)
	t.Cleanup(func() { Release(mod) })
	if a != nil {
		_ = a.Close(context.Background())
	}
	require.ErrorIs(t, err, database.ErrCleanStopNotDiscarded)
	require.False(t, mod.migrated.Load(), "a books step ran: the first step refused, not Register's take")
	require.NotNil(t, mod.store.Load(), "Register's take did not run, or ran before the store was kept for Release")
	_, err = os.Lstat(filepath.Join(marker, "riddle"))
	require.NoError(t, err, "the marker that may still vouch left the disk")
}

// TestSwapVoidReason: Options' read of the swap slot voids the clean-stop
// claim for a staged swap, for one in flight and, failing closed, for a slot
// it cannot read.
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

	// A swap in flight (R1-25 review N-1): pending.json was renamed to
	// restore/<id>/inflight.json, and this boot's P0a finishes it.
	inflight := t.TempDir()
	const flying = "20261109T000100Z-oracle02"
	journal := restorejournal.JournalDir(inflight, flying)
	require.NoError(t, os.MkdirAll(journal, 0o700))
	intent, err := json.Marshal(restorejournal.Intent{ID: flying, Kind: restorejournal.KindArchive, DBPath: db})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(journal, "inflight.json"), intent, 0o600))
	require.Contains(t, swapVoidReason(inflight), "pre-boot swap may replace the store")
	require.Contains(t, swapVoidReason(inflight), restorejournal.TokenRestoreInflight)
	require.Contains(t, swapVoidReason(inflight), flying)

	// A directory in pending.json's place: it exists, and it cannot be read
	// as an intent.
	unreadable := t.TempDir()
	require.NoError(t, os.MkdirAll(restorejournal.PendingPath(unreadable), 0o700))
	require.Contains(t, swapVoidReason(unreadable), "could not be read")
}
