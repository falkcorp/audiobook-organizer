// file: internal/audiobooks/purge_root_and_single_book_test.go
// version: 1.1.0
// guid: c3e8a1d6-5f27-4b90-8d14-7a2b9e0f6c35
// last-edited: 2026-10-25

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// liveBookWithFile adds a live (not trashed) book with one book_file row whose
// file exists on disk, the evidence libraryRootReady samples to tell a mounted
// library from a mount point that is not up.
func liveBookWithFile(t *testing.T, store *database.PebbleStore, id, path string) {
	t.Helper()
	writeFile(t, path)
	_, err := store.CreateBook(&database.Book{ID: id, Title: id, FilePath: path})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: id + "-f", BookID: id, FilePath: path, Format: "m4b"}))
}

// libraryAppDirs puts the application's own folders inside the root, as on
// production, where the database, the cover cache and the backups live under
// root_dir: a root holding them says nothing about whether the books are there.
func libraryAppDirs(t *testing.T) {
	t.Helper()
	root := config.AppConfig.RootDir
	writeFile(t, filepath.Join(root, ".appdata", "audiobooks.pebble", "MANIFEST"))
	writeFile(t, filepath.Join(root, ".covers", "year-one.jpg"))
	writeFile(t, filepath.Join(root, ".backups", "backup-1.tar"))
}

// unmountRoot moves the library away and leaves a directory at its path that
// holds only the application's own folders (libraryAppDirs): what a library
// mount point looks like while the filesystem that belongs there is not up, on a
// host whose database lives inside it. It first adds a live book whose file is in
// the library, so there is something for libraryRootReady to find missing. The
// returned func puts the library back; it reports failures with t.Errorf, not
// require, because the late-mount test calls it from a goroutine (FailNow is only
// valid on the test's own goroutine).
func unmountRoot(t *testing.T, store *database.PebbleStore) (remount func()) {
	t.Helper()
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	return unmountLibrary(t)
}

// unmountLibrary is unmountRoot for a test that has added its live books itself.
func unmountLibrary(t *testing.T) (remount func()) {
	t.Helper()
	root := config.AppConfig.RootDir
	away := root + ".away"
	require.NoError(t, os.Rename(root, away))
	require.NoError(t, os.Mkdir(root, 0o755))
	libraryAppDirs(t)
	return func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remount: clear the mount point: %v", err)
			return
		}
		if err := os.Rename(away, root); err != nil {
			t.Errorf("remount: put the library back: %v", err)
		}
	}
}

func withDeleteFiles(t *testing.T) {
	t.Helper()
	prev := config.AppConfig.PurgeSoftDeletedDeleteFiles
	config.AppConfig.PurgeSoftDeletedDeleteFiles = true
	t.Cleanup(func() { config.AppConfig.PurgeSoftDeletedDeleteFiles = prev })
}

// S-2 / SH-1. A library root is "ready" when it is configured, exists, is a
// directory and most of a sample of the live book files the database says are
// under it can be seen. Whether the root holds anything is not evidence: the
// application's own folders (database, covers, backups) live inside it.
func TestLibraryRootReady(t *testing.T) {
	t.Run("not configured, missing or not a directory", func(t *testing.T) {
		svc, _, base := setupPurgeBoundary(t)
		regular := filepath.Join(base, "regular")
		writeFile(t, regular)
		assert.ErrorIs(t, svc.libraryRootReady(""), ErrLibraryRootNotReady)
		assert.ErrorIs(t, svc.libraryRootReady(filepath.Join(base, "missing")), ErrLibraryRootNotReady)
		assert.ErrorIs(t, svc.libraryRootReady(regular), ErrLibraryRootNotReady)
	})

	t.Run("no live books under the root is ready", func(t *testing.T) {
		svc, _, _ := setupPurgeBoundary(t)
		assert.NoError(t, svc.libraryRootReady(config.AppConfig.RootDir), "an empty library has nothing to protect")
	})

	t.Run("most sampled files present is ready", func(t *testing.T) {
		svc, store, base := setupPurgeBoundary(t)
		libraryAppDirs(t)
		for _, id := range []string{"hush", "knightfall", "year-one"} {
			liveBookWithFile(t, store, id, filepath.Join(base, "lib", "Gotham", id+".m4b"))
		}
		// One file lost on its own (deleted by hand) does not make a library
		// look unmounted.
		require.NoError(t, os.Remove(filepath.Join(base, "lib", "Gotham", "hush.m4b")))
		assert.NoError(t, svc.libraryRootReady(config.AppConfig.RootDir))
	})

	t.Run("most sampled files missing is not ready even though the root holds the app's own folders", func(t *testing.T) {
		svc, store, base := setupPurgeBoundary(t)
		for _, id := range []string{"hush", "knightfall", "year-one"} {
			liveBookWithFile(t, store, id, filepath.Join(base, "lib", "Gotham", id+".m4b"))
		}
		require.NoError(t, os.RemoveAll(filepath.Join(base, "lib", "Gotham")))
		libraryAppDirs(t)
		err := svc.libraryRootReady(config.AppConfig.RootDir)
		require.ErrorIs(t, err, ErrLibraryRootNotReady)
		assert.Contains(t, err.Error(), "only 0 of 3 sampled")
	})

	t.Run("an empty-looking mount is not ready once a live book is known", func(t *testing.T) {
		svc, store, _ := setupPurgeBoundary(t)
		unmountRoot(t, store)
		assert.ErrorIs(t, svc.libraryRootReady(config.AppConfig.RootDir), ErrLibraryRootNotReady)
	})

	t.Run("trashed and outside-root files are not evidence", func(t *testing.T) {
		svc, store, base := setupPurgeBoundary(t)
		// A trashed book and a file outside the root, both missing:
		// none is sampled, so the library has nothing live to compare against.
		softDeleted(t, store, "trashed", filepath.Join(base, "lib", "Gotham", "hush.m4b"))
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "trashed-f", BookID: "trashed", FilePath: filepath.Join(base, "lib", "Gotham", "hush.m4b"), Format: "m4b"}))
		_, err := store.CreateBook(&database.Book{ID: "elsewhere", Title: "elsewhere", FilePath: filepath.Join(base, "lib2", "Gotham", "long-halloween.m4b")})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "elsewhere-f", BookID: "elsewhere", FilePath: filepath.Join(base, "lib2", "Gotham", "long-halloween.m4b"), Format: "m4b"}))
		assert.NoError(t, svc.libraryRootReady(config.AppConfig.RootDir))
	})

	t.Run("a store that cannot be read fails closed", func(t *testing.T) {
		_, store, _ := setupPurgeBoundary(t)
		svc := NewAudiobookService(&failingSampleStore{PebbleStore: store})
		err := svc.libraryRootReady(config.AppConfig.RootDir)
		require.ErrorIs(t, err, ErrLibraryRootNotReady)
		assert.Contains(t, err.Error(), "cannot sample")
	})
}

// failingSampleStore fails the read libraryRootReady's sample starts with.
type failingSampleStore struct{ *database.PebbleStore }

func (s *failingSampleStore) CountPrimaryBooks() (int, error) {
	return 0, errors.New("batcave offline")
}

// The pass's guard trusts a good check for rootGuardTTL and then checks again,
// so a mount lost in the middle of a long pass stops it, and a failed check is
// never cached.
func TestRootGuard_RechecksAfterTheTTLAndNeverCachesAFailure(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	now := time.Now()
	g := newRootGuard(svc, config.AppConfig.RootDir)
	g.okAt = now
	g.now = func() time.Time { return now }

	remount := unmountRoot(t, store)
	require.NoError(t, g.ready(), "inside the TTL the last good check stands")

	now = now.Add(rootGuardTTL + time.Second)
	require.ErrorIs(t, g.ready(), ErrLibraryRootNotReady, "past the TTL the library is looked at again")
	require.ErrorIs(t, g.ready(), ErrLibraryRootNotReady, "and a failure is not cached")

	remount()
	require.NoError(t, g.ready())
	assert.Equal(t, now, g.okAt, "a good check is remembered again")
}

// S-2. A pass that deletes files, run while the library is not mounted, is
// refused whole: every "no file here" would read as "already gone". Nothing is
// purged, no journal record is cleared, a preview says so, and a pass that does
// not delete files is not held up. Once the library is back the records drain.
func TestRunPurge_DeleteFilesPassIsRefusedWhileTheRootIsNotReady(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	gone := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, gone)
	softDeleted(t, store, "T", gone)
	crashAfterRowAndTombstone(t, svc, store, "T")
	stays := filepath.Join(base, "lib", "Gotham", "knightfall.m4b")
	writeFile(t, stays)
	softDeleted(t, store, "U", stays)

	remount := unmountRoot(t, store)
	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	require.NotNil(t, res)
	assert.True(t, res.StoppedEarly, "%+v", res)
	assert.Zero(t, res.Purged)
	assert.False(t, bookGone(t, store, "U"), "the book stays in the trash")
	assert.Len(t, pendingJournal(t, store), 1, "the record is kept")

	pv, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true, DeleteFiles: true})
	require.NoError(t, err)
	require.NotEmpty(t, pv.Errors)
	assert.Contains(t, pv.Errors[len(pv.Errors)-1], "would be refused")

	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "a pass that deletes no file is not held up: %+v", res)
	assert.Len(t, pendingJournal(t, store), 1, "and it touches no record")

	remount()
	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
	assert.False(t, fileExists(gone))
	assert.True(t, fileExists(stays), "the second book's file was never decided for removal")
	assert.Empty(t, pendingJournal(t, store))
}

// S-2. The replay and the removal each check the root themselves, so a mount
// lost between the pass's start and its end, and a replay called from anywhere
// (the boot), keep every record.
func TestReplayAndRemoval_KeepEveryRecordWhileTheRootIsNotReady(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	crashAfterRowAndTombstone(t, svc, store, "T")

	remount := unmountRoot(t, store)
	removed, errs := svc.replayPendingFileRemovals(context.Background())
	assert.Zero(t, removed)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "every record kept")
	assert.Len(t, pendingJournal(t, store), 1)

	files := &pendingFiles{}
	files.add("T", purgeFileVerdict{remove: x, id: fileIdentity{Known: true, Size: 1, MtimeNS: 1}})
	removed, errs = svc.removeDeferredFiles(context.Background(), files, nil)
	assert.Zero(t, removed)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "records kept")
	assert.Len(t, pendingJournal(t, store), 1, "an unmounted root is not 'already gone': the record is not cleared")

	remount()
	removed, errs = svc.replayPendingFileRemovals(context.Background())
	assert.Equal(t, 1, removed, "%v", errs)
	assert.False(t, fileExists(x))
}

// S-2. The boot replay: removes what a crashed pass left, waits for a library
// that is mounted late, gives up with every record kept, and stops on shutdown.
func TestReplayPendingFileRemovalsOnBoot(t *testing.T) {
	setup := func(t *testing.T) (*AudiobookService, *database.PebbleStore, string) {
		svc, store, base := setupPurgeBoundary(t)
		x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
		writeFile(t, x)
		softDeleted(t, store, "T", x)
		crashAfterRowAndTombstone(t, svc, store, "T")
		return svc, store, x
	}

	t.Run("removes what a crashed pass left", func(t *testing.T) {
		svc, store, x := setup(t)
		removed, errs, err := svc.ReplayPendingFileRemovalsOnBoot(context.Background(), time.Millisecond, 3)
		require.NoError(t, err)
		assert.Equal(t, 1, removed, "%v", errs)
		assert.False(t, fileExists(x))
		assert.Empty(t, pendingJournal(t, store))
	})

	t.Run("nothing pending never looks at the root", func(t *testing.T) {
		svc, store, _ := setupPurgeBoundary(t)
		unmountRoot(t, store)
		removed, errs, err := svc.ReplayPendingFileRemovalsOnBoot(context.Background(), time.Millisecond, 1)
		require.NoError(t, err)
		assert.Zero(t, removed)
		assert.Empty(t, errs)
	})

	t.Run("waits for a library that is mounted late", func(t *testing.T) {
		svc, store, x := setup(t)
		remount := unmountRoot(t, store)
		// The test must not end (and its temp dir go) before the remount does,
		// even when an assertion below fails first.
		remounted := make(chan struct{})
		t.Cleanup(func() { <-remounted })
		go func() {
			defer close(remounted)
			time.Sleep(60 * time.Millisecond)
			remount()
		}()
		removed, errs, err := svc.ReplayPendingFileRemovalsOnBoot(context.Background(), 20*time.Millisecond, 100)
		require.NoError(t, err)
		assert.Equal(t, 1, removed, "%v", errs)
		assert.False(t, fileExists(x))
		assert.Empty(t, pendingJournal(t, store))
	})

	t.Run("gives up with every record kept", func(t *testing.T) {
		svc, store, _ := setup(t)
		unmountRoot(t, store)
		removed, _, err := svc.ReplayPendingFileRemovalsOnBoot(context.Background(), time.Millisecond, 3)
		require.ErrorIs(t, err, ErrLibraryRootNotReady)
		assert.Zero(t, removed)
		assert.Len(t, pendingJournal(t, store), 1)
	})

	t.Run("stops on shutdown", func(t *testing.T) {
		svc, store, _ := setup(t)
		unmountRoot(t, store)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()
		t.Cleanup(cancel)
		_, _, err := svc.ReplayPendingFileRemovalsOnBoot(ctx, time.Hour, 5)
		require.ErrorIs(t, err, context.Canceled)
		assert.Len(t, pendingJournal(t, store), 1)
	})
}

// S-2. The single-book paths decide their file per book, so they check the root
// themselves: nothing is discarded or purged while the library is not there.
func TestSingleBookPurges_AreRefusedWhileTheRootIsNotReady(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	withDeleteFiles(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	purgeSeedProgress(t, store, "T")
	unmountRoot(t, store)

	_, err := svc.DiscardProgressAndPurge(context.Background(), "T", "owner")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "library root is not ready")
	assert.False(t, bookGone(t, store, "T"))

	row, gerr := store.GetBookByID("T")
	require.NoError(t, gerr)
	_, err = purgeNowUnderHold(t, svc, row)
	require.Error(t, err)
	assert.False(t, bookGone(t, store, "T"))
}

// afterDeleteStore runs `after` right after a successful DeleteBook: the moment
// between a single-book purge's row delete and its file removal.
type afterDeleteStore struct {
	*database.PebbleStore
	after func()
}

func (s *afterDeleteStore) DeleteBook(id string) error {
	err := s.PebbleStore.DeleteBook(id)
	if err == nil && s.after != nil {
		s.after()
	}
	return err
}

// S-3. "Purge now" decided the file before it deleted the row, and the removal
// comes after. A scan that attaches the path to a live book as a track in
// between (a book_file row, which the all-rows lookup does not read) must keep
// the file: the removal plans again.
func TestPurgeNow_FileAttachedToALiveBookAfterTheDecisionStays(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	row, err := store.GetBookByID("T")
	require.NoError(t, err)

	wrapped := &afterDeleteStore{PebbleStore: store}
	svc := NewAudiobookService(wrapped)
	wrapped.after = func() {
		_, cerr := store.CreateBook(&database.Book{ID: "D", Title: "D", FilePath: filepath.Dir(x)})
		require.NoError(t, cerr)
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "D-f", BookID: "D", FilePath: x}))
	}
	res, err := purgeNowUnderHold(t, svc, row)
	require.NoError(t, err)
	assert.Zero(t, res["files_deleted"])
	assert.True(t, fileExists(x), "a live book's track now names the file")
	require.Contains(t, res, "warnings")
	assert.Contains(t, res["warnings"].([]string)[0], "book_file")
}

// S-3. The same window for a different file put at the path.
func TestPurgeNow_FileReplacedAfterTheDecisionStays(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	row, err := store.GetBookByID("T")
	require.NoError(t, err)

	wrapped := &afterDeleteStore{PebbleStore: store}
	svc := NewAudiobookService(wrapped)
	wrapped.after = func() {
		tmp := x + ".new"
		require.NoError(t, os.WriteFile(tmp, []byte("a different file"), 0o644))
		require.NoError(t, os.Rename(tmp, x))
	}
	res, err := purgeNowUnderHold(t, svc, row)
	require.NoError(t, err)
	assert.Zero(t, res["files_deleted"])
	got, rerr := os.ReadFile(x)
	require.NoError(t, rerr)
	assert.Equal(t, "a different file", string(got), "the replacement is nobody's decision to remove")
	require.Contains(t, res, "warnings")
	assert.Contains(t, res["warnings"].([]string)[0], "not the file this purge decided")
}

// S-4. "Discard progress and purge" decides its file before anything is
// discarded: an undecidable file refuses the action with the listening state and
// the book untouched.
func TestDiscardProgressAndPurge_UndecidableFileRefusesBeforeAnythingIsDiscarded(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	u := purgeSeedProgress(t, store, "T")

	hooked := &hookedPathStore{PebbleStore: store}
	hooked.liveErr = func(string) error { return errors.New("simulated lookup failure") }
	svc := NewAudiobookService(hooked)
	rec := &fakeRecorder{}
	svc.auditOverride = rec

	_, err := svc.DiscardProgressAndPurge(context.Background(), "T", "owner")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing discarded")
	assert.False(t, bookGone(t, store, "T"))
	assert.Equal(t, 40, userPct(t, store, u.ID, "T"), "the listening state is still there")
	assert.Empty(t, rec.entries)
	assert.Empty(t, pendingJournal(t, store))
	assert.True(t, fileExists(x))
}

// S-4. The removal is journaled BEFORE the row goes, in a single-book purge too,
// and taken back when the delete does not happen.
func TestDiscardProgressAndPurge_JournalsBeforeTheDelete(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)

	failing := &failingDeleteStore{PebbleStore: store}
	svc := NewAudiobookService(failing)
	svc.auditOverride = &fakeRecorder{}
	_, err := svc.DiscardProgressAndPurge(context.Background(), "T", "owner")
	require.Error(t, err)
	assert.Equal(t, 1, failing.journalAtDelete, "the record was written when the delete was attempted")
	assert.Empty(t, pendingJournal(t, store), "the delete failed: the record was taken back")
	assert.False(t, bookGone(t, store, "T"))
}

// S-4. "Purge now": a lookup that fails AFTER the row is gone keeps the journal
// record (it used to leave the file with no row and no record), and the next
// replay finishes the removal. A removal that succeeds leaves no record.
func TestPurgeNow_FailedLookupAfterTheDeleteKeepsTheRecord(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	row, err := store.GetBookByID("T")
	require.NoError(t, err)

	hooked := &hookedPathStore{PebbleStore: store}
	failing := true
	hooked.allRowsErr = func(string) error {
		if failing {
			return errors.New("simulated all-rows lookup failure")
		}
		return nil
	}
	svc := NewAudiobookService(hooked)
	res, err := purgeNowUnderHold(t, svc, row)
	require.NoError(t, err)
	assert.Zero(t, res["files_deleted"])
	assert.True(t, bookGone(t, store, "T"))
	assert.True(t, fileExists(x))
	assert.Len(t, pendingJournal(t, store), 1, "the removal was not decided: the record stays")

	failing = false
	removed, errs := svc.replayPendingFileRemovals(context.Background())
	assert.Equal(t, 1, removed, "%v", errs)
	assert.False(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store))

	// And a purge now that removes the file at once leaves nothing behind.
	y := filepath.Join(base, "lib", "Gotham", "knightfall.m4b")
	writeFile(t, y)
	softDeleted(t, store, "U", y)
	row, err = store.GetBookByID("U")
	require.NoError(t, err)
	res, err = purgeNowUnderHold(t, svc, row)
	require.NoError(t, err)
	assert.Equal(t, 1, res["files_deleted"])
	assert.Empty(t, pendingJournal(t, store), "removed: the record is cleared")
}

// S-4. A failed unlink after the delete keeps the record too.
func TestPurgeNow_FailedUnlinkKeepsTheRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the unlink cannot be made to fail")
	}
	svc, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	dir := filepath.Join(base, "lib", "Gotham")
	x := filepath.Join(dir, "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	row, err := store.GetBookByID("T")
	require.NoError(t, err)

	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	res, err := purgeNowUnderHold(t, svc, row)
	require.NoError(t, err)
	assert.Zero(t, res["files_deleted"])
	assert.True(t, fileExists(x))
	assert.Len(t, pendingJournal(t, store), 1, "the unlink failed: a later pass retries it")

	require.NoError(t, os.Chmod(dir, 0o755))
	removed, errs := svc.replayPendingFileRemovals(context.Background())
	assert.Equal(t, 1, removed, "%v", errs)
	assert.False(t, fileExists(x))
}

// n-1. Counting an attempt rewrites each record as it was written, keeping its
// own identity; records that disagreed about the file must not be made to agree.
func TestRecordUnresolvableAttempt_KeepsEachRecordsOwnIdentity(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	idA := fileIdentity{Known: true, HasInode: true, Ino: 1, Size: 1, MtimeNS: 1}
	idB := fileIdentity{Known: true, HasInode: true, Ino: 2, Size: 2, MtimeNS: 2}
	require.NoError(t, svc.journalPendingFile("A", purgeFileVerdict{remove: "/lib/Gotham/year-one.m4b", id: idA}))
	require.NoError(t, svc.journalPendingFile("B", purgeFileVerdict{remove: "/lib/Gotham/year-one.m4b", id: idB}))

	files, _, _, err := svc.readPendingFiles()
	require.NoError(t, err)
	pending := files.sorted()
	require.Len(t, pending, 1)
	require.True(t, pending[0].idConflict)
	require.NoError(t, svc.recordUnresolvableAttempt(pending[0]))

	for id, want := range map[string]fileIdentity{"A": idA, "B": idB} {
		raw, gerr := store.GetRaw(pendingFilePrefix + id)
		require.NoError(t, gerr)
		var rec pendingFileRecord
		require.NoError(t, json.Unmarshal(raw, &rec))
		assert.Equal(t, want, rec.Identity, "record %s keeps its own identity", id)
		assert.Equal(t, 1, rec.Attempts)
	}
	// A record that is not there is not invented.
	pending[0].bookIDs = []string{"A", "ghost"}
	require.NoError(t, svc.recordUnresolvableAttempt(pending[0]))
	raw, gerr := store.GetRaw(pendingFilePrefix + "ghost")
	require.NoError(t, gerr)
	assert.Nil(t, raw)
}

// n-2. A book held for its listening state is kept for THAT reason even when its
// file cannot be decided, and the preview says the same. A book with no state
// whose file cannot be decided is failed by both.
func TestRunPurge_UndecidableFileDoesNotHideWhyAHeldBookStays(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "held", x)
	purgeSeedProgress(t, store, "held")
	y := filepath.Join(base, "lib", "Gotham", "knightfall.m4b")
	writeFile(t, y)
	softDeleted(t, store, "free", y)

	hooked := &hookedPathStore{PebbleStore: store}
	hooked.liveErr = func(string) error { return errors.New("simulated lookup failure") }
	svc := NewAudiobookService(hooked)

	pv, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, pv.KeptHasProgress, "%+v", pv)
	assert.Equal(t, 1, pv.Failed, "the preview counts the undecidable file as failed: %+v", pv)
	assert.Zero(t, pv.Purgeable)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, pv.KeptHasProgress, res.KeptHasProgress, "run == preview: %+v", res)
	assert.Equal(t, pv.Failed, res.Failed, "run == preview: %+v", res)
	assert.Zero(t, res.Purged)

	// Without "also delete files" the file is nobody's business.
	pv2, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, 1, pv2.Purgeable, "%+v", pv2)
	assert.Zero(t, pv2.Failed)
}

// n-3. A path stored with a trailing separator over a regular file is not
// "already gone" (stat says ENOTDIR while the file is there): the book is purged,
// the file stays, and a line says why.
func TestRunPurge_TrailingSeparatorOverARegularFileIsKeptAndReported(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "slashy", x+string(os.PathSeparator))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.True(t, fileExists(x))
	require.Len(t, res.Errors, 1, "%+v", res)
	assert.Contains(t, res.Errors[0], "trailing path separator")
	assert.Empty(t, pendingJournal(t, store))
}

// n-4. A failure to resolve the root is the filesystem not answering (a lookup
// failure, kept in the trash), while a path that escapes the root is a definite
// refusal.
func TestPurgePathInsideRoot_TransientResolveFailureIsALookupFailure(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "loopa"), filepath.Join(dir, "loopb")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))
	err := purgePathInsideRoot(filepath.Join(a, "no-mans-land.m4b"), a)
	require.Error(t, err)
	assert.ErrorIs(t, err, errPathLookupFailed)

	root := filepath.Join(dir, "lib")
	require.NoError(t, os.MkdirAll(root, 0o755))
	outside := filepath.Join(dir, "elsewhere")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "jump")))
	err = purgePathInsideRoot(filepath.Join(root, "jump", "no-mans-land.m4b"), root)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errPathLookupFailed, "an escape is final, not transient")
}

// n-6. A path that cannot be resolved gets maxUnresolvableAttempts passes in all,
// counting the pass that journaled it: kept after the second, cleared at the third.
func TestRemoveDeferredFiles_UnresolvablePathFoundByTheJournalingPassGetsThreePasses(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	dir := filepath.Join(base, "lib", "Gotham")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	a, b := filepath.Join(dir, "loopa"), filepath.Join(dir, "loopb")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))
	target := filepath.Join(a, "no-mans-land.m4b")
	id := fileIdentity{Known: true, Size: 1, MtimeNS: 1}
	v := purgeFileVerdict{remove: target, id: id}
	require.NoError(t, svc.journalPendingFile("T", v))

	// Pass 1: the pass's own entry.
	files := &pendingFiles{}
	files.add("T", v)
	_, errs := svc.removeDeferredFiles(context.Background(), files, nil)
	require.Len(t, errs, 1)
	assert.NotContains(t, errs[0], "gave up")
	require.Len(t, pendingJournal(t, store), 1)

	// Pass 2: replayed from the journal.
	_, errs = svc.replayPendingFileRemovals(context.Background())
	require.Len(t, errs, 1)
	assert.NotContains(t, errs[0], "gave up")
	require.Len(t, pendingJournal(t, store), 1)

	// Pass 3: the last.
	_, errs = svc.replayPendingFileRemovals(context.Background())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "gave up")
	assert.Empty(t, pendingJournal(t, store))
}
