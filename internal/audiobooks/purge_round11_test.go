// file: internal/audiobooks/purge_round11_test.go
// version: 1.0.0
// guid: 9d4f7a52-3c1e-4b86-a0d5-6e21b8c7f390
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// manyFilesOneFolder journals nExisting files that are there and nGone that are
// not (all in one folder with no sibling that stays), and returns the pending
// set, the folder and the paths of the files that exist. The gone files sort
// AFTER the existing ones, so a folder removed as soon as the last existing file
// is unlinked would be missing under every one of them.
func manyFilesOneFolder(t *testing.T, base string, nExisting, nGone int) (files *pendingFiles, dir string, existing []string) {
	t.Helper()
	dir = filepath.Join(base, "lib", "Gotham", "Hush")
	files = &pendingFiles{}
	for i := range nExisting {
		p := filepath.Join(dir, fmt.Sprintf("a%02d.m4b", i))
		writeFile(t, p)
		existing = append(existing, p)
		files.add(fmt.Sprintf("E%02d", i), purgeFileVerdict{remove: p, id: identityAt(t, p)})
	}
	for i := range nGone {
		p := filepath.Join(dir, fmt.Sprintf("z%02d.m4b", i))
		files.add(fmt.Sprintf("G%02d", i), purgeFileVerdict{remove: p, id: fileIdentity{Known: true, Size: 1, MtimeNS: 1}})
	}
	return files, dir, existing
}

// SH-1. Several files in one folder, none kept by a sibling: every file that is
// there goes, every file that was already gone is quietly "gone" (its folder is
// there while the paths are decided), and only then is the emptied folder
// removed. A parent removed by one worker as soon as it was empty made a
// neighbour's already-gone file read as "its folder is missing too" and kept
// its record, depending on scheduling.
func TestRemoveDeferredFiles_FilesInOneFolderWithNoSiblingAreAllDecidedBeforeTheFolderGoes(t *testing.T) {
	svc, _, base := setupPurgeBoundary(t)
	files, dir, existing := manyFilesOneFolder(t, base, 30, 30)

	removed, errs := svc.removeDeferredFiles(context.Background(), files, nil)

	assert.Equal(t, len(existing), removed)
	assert.Empty(t, errs, "an already-gone file in a folder that was there is fine: %v", errs)
	for _, p := range existing {
		assert.False(t, fileExists(p), p)
	}
	_, err := os.Stat(dir)
	assert.True(t, errors.Is(err, os.ErrNotExist), "the emptied folder is removed once every path is decided: %v", err)
	assert.True(t, fileExists(config.AppConfig.RootDir), "and never the root")
	pairs, err := svc.readPendingFilesForTest()
	require.NoError(t, err)
	assert.Empty(t, pairs, "nothing is left recorded")
}

// readPendingFilesForTest is the journal as a pass would read it.
func (svc *AudiobookService) readPendingFilesForTest() ([]string, error) {
	files, corrupt, _, err := svc.readPendingFiles()
	if err != nil {
		return nil, err
	}
	out := append([]string{}, corrupt...)
	for _, f := range files.sorted() {
		out = append(out, f.path)
	}
	return out, nil
}

// SH-1. The same through the journal, the way a replay after a crash reads it.
func TestReplay_FilesInOneFolderWithNoSiblingAreAllDecidedBeforeTheFolderGoes(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	files, dir, existing := manyFilesOneFolder(t, base, 12, 12)
	for _, f := range files.sorted() {
		require.NoError(t, svc.journalPendingFile(f.bookIDs[0], purgeFileVerdict{remove: f.path, id: f.id}))
	}

	removed, errs := svc.replayPendingFileRemovals(context.Background())

	assert.Equal(t, len(existing), removed)
	assert.Empty(t, errs, "%v", errs)
	assert.Empty(t, pendingJournal(t, store))
	_, err := os.Stat(dir)
	assert.True(t, errors.Is(err, os.ErrNotExist), "%v", err)
}

// SH-1. Each record is forgotten right after its own unlink, not in one batch at
// the end: a store whose journal cannot be written reports a line for EVERY
// record it could not clear (a batch would report one), and the files that went
// are counted.
func TestRemoveDeferredFiles_ForgetsEachRecordAndReportsEachFailure(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	failing := &failingForgetStore{PebbleStore: store}
	failing.forgetErr.Store(true)
	svc := NewAudiobookService(failing)
	files := &pendingFiles{}
	for _, name := range []string{"hush.m4b", "knightfall.m4b", "dark-victory.m4b"} {
		p := filepath.Join(base, "lib", "Gotham", name)
		writeFile(t, p)
		files.add("B-"+name, purgeFileVerdict{remove: p, id: identityAt(t, p)})
	}

	removed, errs := svc.removeDeferredFiles(context.Background(), files, nil)

	assert.Equal(t, 3, removed)
	require.Len(t, errs, 3, "one line per record that could not be cleared: %v", errs)
	for _, line := range errs {
		assert.Contains(t, line, "could not be cleared")
		assert.Contains(t, line, "simulated journal failure")
	}
}

// SH-2. The aggregate line is for records with a missing folder only, and it
// does not hide the lines of other records.
func TestRemoveDeferredFiles_MissingFolderLineIsOneAndOtherLinesStay(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	libraryAppDirs(t)
	liveBookWithFile(t, store, "present", filepath.Join(base, "lib", "Arkham", "year-one.m4b"))
	files := &pendingFiles{}
	for _, name := range []string{"hush.m4b", "knightfall.m4b"} {
		files.add("M-"+name, purgeFileVerdict{remove: filepath.Join(base, "lib", "Gotham", name), id: fileIdentity{Known: true, Size: 1, MtimeNS: 1}})
	}
	changed := filepath.Join(base, "lib", "Arkham", "no-mans-land.m4b")
	writeFile(t, changed)
	files.add("C", purgeFileVerdict{remove: changed, id: fileIdentity{Known: true, Size: 99, MtimeNS: 1}})

	removed, errs := svc.removeDeferredFiles(context.Background(), files, nil)

	assert.Zero(t, removed)
	require.Len(t, errs, 2, "%v", errs)
	assert.Contains(t, errs[0]+errs[1], "2 pending file removal(s) are waiting for a folder that is missing")
	assert.Contains(t, errs[0]+errs[1], "is not the file this purge decided to remove")
	assert.True(t, fileExists(changed))
}

// SH-3 / SH-4. The root check is made on the library's own live files wherever
// they are in the keyspace: a library that is not mounted is found out even when
// a long run of other live books sorts around the files under the root.
func TestLibraryRootReady_UnmountedRootIsFoundOutBehindManyOtherBooks(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	for i := range 120 {
		id := fmt.Sprintf("elsewhere-%03d", i)
		p := filepath.Join(base, "elsewhere", fmt.Sprintf("%03d.m4b", i))
		_, err := store.CreateBook(&database.Book{ID: id, Title: batmanTitle(id), FilePath: p})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: id + "-f", BookID: id, FilePath: p, Format: "m4b"}))
	}
	remount := unmountRoot(t, store)
	err := svc.libraryRootReady(config.AppConfig.RootDir)
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	assert.Contains(t, err.Error(), "only 0 of 1")
	remount()
	require.NoError(t, svc.libraryRootReady(config.AppConfig.RootDir))
}

// SH-3. A library with no live file under the root at all, anywhere, has nothing
// to compare against and is ready, as before.
func TestLibraryRootReady_NoLiveFileUnderTheRootAnywhereIsReady(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	p := filepath.Join(base, "elsewhere", "year-one.m4b")
	_, err := store.CreateBook(&database.Book{ID: "outside", Title: batmanTitle("outside"), FilePath: p})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "outside-f", BookID: "outside", FilePath: p, Format: "m4b"}))
	require.NoError(t, svc.libraryRootReady(config.AppConfig.RootDir))
}

// n-4. The root check stats the way the purge's own decisions do: a symlink
// whose target is not there (a link into a mount that is not up) is not a file
// that is present.
func TestLibraryRootReady_SymlinkToAMissingTargetIsNotPresent(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	dir := filepath.Join(config.AppConfig.RootDir, "Gotham")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	link := filepath.Join(dir, "year-one.m4b")
	require.NoError(t, os.Symlink(filepath.Join(base, "not-mounted", "year-one.m4b"), link))
	_, err := store.CreateBook(&database.Book{ID: "linked", Title: batmanTitle("linked"), FilePath: link})
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "linked-f", BookID: "linked", FilePath: link, Format: "m4b"}))

	err = svc.libraryRootReady(config.AppConfig.RootDir)
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	assert.Contains(t, err.Error(), "only 0 of 1")
}

// n-3. A stat that never answers (a hung network mount) makes the root not
// ready within rootStatTimeout instead of hanging the caller.
func TestLibraryRootReady_HungStatFailsClosedWithinTheTimeout(t *testing.T) {
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
	t.Cleanup(func() {
		close(release)
		rootStat, rootStatTimeout = prevStat, prevTimeout
	})

	began := time.Now()
	err := svc.libraryRootReady(config.AppConfig.RootDir)
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	assert.Contains(t, err.Error(), "did not answer")
	assert.Less(t, time.Since(began), 5*time.Second)
	require.Eventually(t, func() bool { return started.Load() == 2 }, 5*time.Second, 5*time.Millisecond, "the root and the sampled file were both stat-ed")
}

// testClock is a clock a test moves by hand, safe to read from stat goroutines.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// n-2. A check that takes a long time is as old as when it began: the guard
// trusts it from its start, so a slow check does not buy extra trust.
func TestRootGuard_TrustsACheckFromWhenItBeganNotWhenItEnded(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	clock := &testClock{t: time.Now()}
	began := clock.now()

	prevStat := rootStat
	rootStat = func(p string) (os.FileInfo, error) {
		clock.advance(rootGuardTTL) // the check takes the whole TTL
		return os.Stat(p)
	}
	t.Cleanup(func() { rootStat = prevStat })

	g := newRootGuard(svc, config.AppConfig.RootDir, time.Time{})
	g.now = clock.now
	require.NoError(t, g.ready())
	g.mu.Lock()
	okAt := g.okAt
	g.mu.Unlock()
	assert.Equal(t, began, okAt, "trusted from the time the check began")
	assert.False(t, g.fresh(), "and so no longer fresh once it has taken the TTL")
}

// countingSampleStore counts the root check's samples.
type countingSampleStore struct {
	*database.PebbleStore
	calls atomic.Int32
}

func (s *countingSampleStore) SampleLiveBookFilePathsUnder(root string, k int) ([]string, error) {
	s.calls.Add(1)
	return s.PebbleStore.SampleLiveBookFilePathsUnder(root, k)
}

// SH-3. "Discard progress and purge" checks the library root once, BEFORE it
// takes the global merge lock, and the decisions under the lock reuse that
// answer: a library that is not mounted is refused while the lock is held by
// somebody else, and a healthy one is sampled once for the whole action.
func TestDiscardProgressAndPurge_RootIsCheckedBeforeTheMergeLockAndOnlyOnce(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	withDeleteFiles(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	purgeSeedProgress(t, store, "T")
	counting := &countingSampleStore{PebbleStore: store}
	svc := NewAudiobookService(counting)
	svc.auditOverride = &fakeRecorder{}

	// Somebody else holds the merge lock; the library is not mounted. The answer
	// is the root refusal, immediately, not a wait for the lock.
	unlock, err := merge.TryLockMergeRMWWithin(time.Second)
	require.NoError(t, err)
	remount := unmountLibrary(t)
	began := time.Now()
	_, err = svc.DiscardProgressAndPurge(context.Background(), "T", "owner")
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	assert.Contains(t, err.Error(), "nothing discarded")
	assert.Less(t, time.Since(began), merge.InteractiveMergeLockWait, "refused before waiting for the merge lock")
	assert.False(t, bookGone(t, store, "T"))
	remount()
	unlock()

	counting.calls.Store(0)
	_, err = svc.DiscardProgressAndPurge(context.Background(), "T", "owner")
	require.NoError(t, err)
	assert.Equal(t, int32(1), counting.calls.Load(), "one sample for the whole action, taken before the lock")
	assert.True(t, bookGone(t, store, "T"))
	assert.False(t, fileExists(x))
}

// SH-3. Workers that find the guard's check stale at the same moment share ONE
// check (singleflight): the library is not sampled once per worker.
func TestRootGuard_ConcurrentCallersShareOneCheck(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	counting := &countingSampleStore{PebbleStore: store}
	svc := NewAudiobookService(counting)

	prevStat := rootStat
	rootStat = func(p string) (os.FileInfo, error) {
		time.Sleep(150 * time.Millisecond) // wide enough that every caller arrives while the check runs
		return os.Stat(p)
	}
	t.Cleanup(func() { rootStat = prevStat })

	g := newRootGuard(svc, config.AppConfig.RootDir, time.Time{})
	var wg sync.WaitGroup
	errs := make([]error, 8)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = g.ready()
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, "caller %d", i)
	}
	assert.Equal(t, int32(1), counting.calls.Load(), "one sample for eight callers")
}
