// file: internal/audiobooks/purge_round12_test.go
// version: 1.0.0
// guid: 5b8e2c17-94a3-4d60-b1f8-3a07d6e9c254
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// SH-A.1. A root check whose stats did not answer leaves goroutines blocked in
// the stat. While any are outstanding no new check starts: asking again (a pass
// re-checks per book, a batch per book) would park another 51 threads each time
// on a mount that is hung. The first check started the root's stat and one
// sampled file's; ten asks later that is still all that was ever started.
func TestLibraryRootReady_HungMountIsNotAskedAgainWhileItsStatsAreOutstanding(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	started := hangRootStats(t)

	err := svc.libraryRootReady(config.AppConfig.RootDir)
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	assert.Contains(t, err.Error(), "did not answer")
	require.Eventually(t, func() bool { return started.Load() == 2 }, 5*time.Second, 5*time.Millisecond, "the root and the sampled file were stat-ed")
	assert.Equal(t, int64(2), rootStatsHung.Load(), "both are outstanding")

	for i := 0; i < 10; i++ {
		err := svc.libraryRootReady(config.AppConfig.RootDir)
		require.ErrorIs(t, err, ErrLibraryRootNotReady)
		assert.Contains(t, err.Error(), "still not answered")
	}
	assert.Equal(t, int32(2), started.Load(), "no further stat was started by the ten later checks")
}

// Once the hung stats answer, the library is asked again and a healthy one is
// ready: the refusal lasts exactly as long as the hang.
func TestLibraryRootReady_AnswersAgainOnceTheHungStatsReturn(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	prevStat, prevTimeout := rootStat, rootStatTimeout
	release := make(chan struct{})
	var started atomic.Int32
	rootStat = func(p string) (os.FileInfo, error) {
		started.Add(1)
		<-release
		return os.Stat(p)
	}
	rootStatTimeout = 50 * time.Millisecond
	t.Cleanup(func() { rootStat, rootStatTimeout = prevStat, prevTimeout })

	require.ErrorIs(t, svc.libraryRootReady(config.AppConfig.RootDir), ErrLibraryRootNotReady)
	require.Eventually(t, func() bool { return started.Load() == 2 }, 5*time.Second, 5*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool { return rootStatsHung.Load() == 0 }, 5*time.Second, 5*time.Millisecond, "the late answers take themselves off the count")
	require.NoError(t, svc.libraryRootReady(config.AppConfig.RootDir))
}

// failSampleAfterStore samples normally for its first `ok` questions and fails
// after: a mount lost in the middle of a pass.
type failSampleAfterStore struct {
	*database.PebbleStore
	calls atomic.Int64
	ok    int64
}

func (s *failSampleAfterStore) SampleLiveBookFilePathsUnder(root string, k int) ([]string, error) {
	if s.calls.Add(1) > s.ok {
		return nil, database.ErrLiveSampleUnavailable
	}
	return s.PebbleStore.SampleLiveBookFilePathsUnder(root, k)
}

// SH-A.2 / n-8. A root that stops being ready after the pass has started stops
// the pass with one reason, the way a lost ledger index does, instead of asking
// the library again and failing each remaining book with the same line.
func TestRunPurge_RootLostMidPassStopsThePassWithOneReason(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	const n = 400
	for i := 0; i < n; i++ {
		softDeleted(t, store, fmt.Sprintf("b%03d", i), "")
	}
	failing := &failSampleAfterStore{PebbleStore: store, ok: 1} // the pass's own start check is the one good answer
	svc.store = failing
	prevTTL := rootGuardTTL
	rootGuardTTL = time.Nanosecond // every decision looks again
	t.Cleanup(func() { rootGuardTTL = prevTTL })

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.True(t, res.StoppedEarly, "%+v", res)
	assert.Contains(t, res.StopReason, "purge stopped")
	assert.Contains(t, res.StopReason, "library root is not ready")
	assert.Zero(t, res.Purged, "no book is purged against a root that cannot be vouched for")
	assert.LessOrEqual(t, res.Failed, runtime.NumCPU(), "at most one in-flight book per worker may fail: %d failed of %d", res.Failed, n)
	assert.Less(t, res.Decided(), res.Attempted)
	assert.Less(t, failing.calls.Load(), int64(n/2), "the library is not asked again for every book")
}

// SH-A.3. "Purge now" on a book in the trash checks the library root BEFORE the
// merge lock: with the library not mounted it is refused at once while somebody
// else holds the lock (it does not wait for it), nothing is deleted, and the
// error says so. A healthy library is sampled once, and the purge under the
// lock reuses that answer.
func TestDeleteAudiobook_PurgeNowChecksTheRootBeforeTheMergeLock(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	counting := &countingSampleStore{PebbleStore: store}
	svc := NewAudiobookService(counting)
	svc.auditOverride = &fakeRecorder{}

	unlock, err := merge.TryLockMergeRMWWithin(time.Second)
	require.NoError(t, err)
	remount := unmountLibrary(t)
	began := time.Now()
	_, err = svc.DeleteAudiobook(context.Background(), "T", &DeleteAudiobookOptions{})
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	assert.Contains(t, err.Error(), "nothing deleted")
	assert.Less(t, time.Since(began), merge.InteractiveMergeLockWait, "refused before waiting for the merge lock")
	assert.False(t, bookGone(t, store, "T"))
	remount()
	unlock()

	counting.calls.Store(0)
	_, err = svc.DeleteAudiobook(context.Background(), "T", &DeleteAudiobookOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), counting.calls.Load(), "one sample for the whole action, taken before the lock")
	assert.True(t, bookGone(t, store, "T"))
	assert.False(t, fileExists(x), "the file went with the book")
}

// With delete_files off, a purge decides nothing about a file, so a library that
// is not mounted does not hold it up and no sample is taken.
func TestDeleteAudiobook_PurgeNowWithFilesKeptDoesNotCheckTheRoot(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "knightfall.m4b")
	writeFile(t, x)
	softDeleted(t, store, "U", x)
	counting := &countingSampleStore{PebbleStore: store}
	svc := NewAudiobookService(counting)
	svc.auditOverride = &fakeRecorder{}
	remount := unmountRoot(t, store)
	defer remount()

	_, err := svc.DeleteAudiobook(context.Background(), "U", &DeleteAudiobookOptions{})
	require.NoError(t, err)
	assert.Zero(t, counting.calls.Load())
	assert.True(t, bookGone(t, store, "U"))
}

// SH-A.3. The batch's session (BeginTrashPurges) checks the root outside the
// lock once and answers later checks from the guard; purgeLocked then does not
// sample. A failed first check is returned and nothing is purged.
func TestBeginTrashPurges_ChecksOnceOutsideTheLockAndPurgesUnderIt(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	files := map[string]string{"T": "hush.m4b", "U": "knightfall.m4b", "V": "dark-victory.m4b"}
	counting := &countingSampleStore{PebbleStore: store}
	svc := NewAudiobookService(counting)
	svc.auditOverride = &fakeRecorder{}
	for id, name := range files {
		p := filepath.Join(base, "lib", "Gotham", name)
		writeFile(t, p)
		softDeleted(t, store, id, p)
	}

	checkRoot, purgeLocked := svc.BeginTrashPurges()
	for id := range files {
		require.NoError(t, checkRoot())
		book, err := store.GetBookByID(id)
		require.NoError(t, err)
		unlock, err := merge.TryLockMergeRMWWithin(time.Second)
		require.NoError(t, err)
		err = purgeLocked(book)
		unlock()
		require.NoError(t, err, id)
		assert.True(t, bookGone(t, store, id))
	}
	assert.Equal(t, int32(1), counting.calls.Load(), "one sample for the whole run")
	for _, name := range files {
		assert.False(t, fileExists(filepath.Join(base, "lib", "Gotham", name)))
	}
}

func TestBeginTrashPurges_AFailedCheckIsReturnedAndNothingIsSampledWithFilesKept(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	withDeleteFiles(t)
	counting := &countingSampleStore{PebbleStore: store}
	svc := NewAudiobookService(counting)
	remount := unmountRoot(t, store)
	defer remount()

	checkRoot, _ := svc.BeginTrashPurges()
	require.ErrorIs(t, checkRoot(), ErrLibraryRootNotReady)

	config.AppConfig.PurgeSoftDeletedDeleteFiles = false
	counting.calls.Store(0)
	checkRoot, _ = svc.BeginTrashPurges()
	require.NoError(t, checkRoot(), "files are kept: the root does not matter")
	assert.Zero(t, counting.calls.Load())
}

// A book that holds listening state no live version can take is kept for THAT
// reason whatever its file is doing (undecidedOutcome reclassifies it), but a
// root that cannot be vouched for is not the book's reason: the pass still stops
// with it, instead of the reclassification swallowing the stop.
func TestRunPurge_RootLostOnABookWithListeningStateStillStopsThePass(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "T", "")
	purgeSeedProgress(t, store, "T")
	svc.store = &failSampleAfterStore{PebbleStore: store, ok: 1}
	prevTTL := rootGuardTTL
	rootGuardTTL = time.Nanosecond
	t.Cleanup(func() { rootGuardTTL = prevTTL })

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.True(t, res.StoppedEarly, "%+v", res)
	assert.Contains(t, res.StopReason, "library root is not ready")
	assert.Zero(t, res.Purged)
	assert.False(t, bookGone(t, store, "T"))
}
