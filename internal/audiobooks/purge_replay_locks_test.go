// file: internal/audiobooks/purge_replay_locks_test.go
// version: 1.3.0
// guid: 6371d518-2425-4a76-af03-de3aeb5ea7d3
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"errors"
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

// purgeNowInBackground runs "Purge now" for book the way its handler does (under
// a bounded hold of the merge lock) on its own goroutine, reporting through a
// channel so a failure never calls FailNow off the test goroutine.
func purgeNowInBackground(svc *AudiobookService, book *database.Book) <-chan error {
	done := make(chan error, 1)
	go func() {
		unlock, err := merge.TryLockMergeRMWWithin(time.Second)
		if err != nil {
			done <- err
			return
		}
		defer unlock()
		_, err = svc.purgeNowLocked(book, purgeFileMode{})
		done <- err
	}()
	return done
}

// requireWithin fails unless ch delivers within d.
func requireWithin[T any](t *testing.T, ch <-chan T, d time.Duration, failure string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("%s", failure)
		var zero T
		return zero
	}
}

// SH-2. A replay is long (a stat and a removal per record) and runs under its
// own lock (replayMu). The journal lock (pendingMu) is held exclusively only
// while the journal is read, so a single-book purge, which holds it shared
// between its journal write and its row delete, is not made to wait for a slow
// replay. Holding pendingMu through the removals would block it here.
func TestReplay_SingleBookPurgeIsNotBlockedBySlowReplay(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	slow := filepath.Join(base, "lib", "Gotham", "knightfall.m4b")
	other := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, slow)
	writeFile(t, other)
	softDeleted(t, store, "replayed", slow)
	softDeleted(t, store, "single", other)

	hooked := &hookedPathStore{PebbleStore: store}
	svc := NewAudiobookService(hooked)
	withDeleteFiles(t)
	crashAfterRowAndTombstone(t, svc, store, "replayed")

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	hooked.beforeLive = func(path string) {
		if path == slow {
			once.Do(func() { close(entered) })
			<-release
		}
	}

	type replayed struct {
		removed int
		errs    []string
	}
	replayDone := make(chan replayed, 1)
	go func() {
		n, errs := svc.replayPendingFileRemovals(context.Background())
		replayDone <- replayed{n, errs}
	}()
	requireWithin(t, entered, 5*time.Second, "fixture: the replay never reached its first removal")

	row, err := store.GetBookByID("single")
	require.NoError(t, err)
	singleDone := purgeNowInBackground(svc, row)
	err = requireWithin(t, singleDone, 5*time.Second, "a single-book purge is waiting on a slow replay (the journal lock is held through the removals)")
	require.NoError(t, err)
	assert.True(t, bookGone(t, store, "single"))
	assert.False(t, fileExists(other), "and it removed its own file meanwhile")
	assert.True(t, fileExists(slow), "the replay is still looking at its path")

	close(release)
	r := requireWithin(t, replayDone, 5*time.Second, "the replay never finished")
	assert.Equal(t, 1, r.removed, "%v", r.errs)
	assert.False(t, fileExists(slow))
	assert.Empty(t, pendingJournal(t, store))
}

// blockingDeleteStore stops DeleteBook of the named book until released.
type blockingDeleteStore struct {
	*database.PebbleStore
	id      string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingDeleteStore) DeleteBook(id string) error {
	if id == s.id {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	return s.PebbleStore.DeleteBook(id)
}

// n-3. A replay must not read the journal in the window between a single-book
// purge's journal write and its row delete: the record is there and the book is
// still in the trash, and a replay that read it then would keep the file (a
// book names its path) and clear the record, and the purge would delete the row
// with nothing left to remove the file. pendingMu is what orders them.
func TestReplay_WaitsForAPurgeBetweenItsJournalWriteAndItsRowDelete(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "no-mans-land.m4b")
	writeFile(t, x)
	// A sibling keeps the folder: the purge removes the folders its removal
	// empties, and a replay that then found the file gone with its folder gone too
	// would (rightly) report "cannot tell". The test is about the order, not that.
	writeFile(t, filepath.Join(base, "lib", "Gotham", "long-halloween.m4b"))
	softDeleted(t, store, "T", x)

	blocking := &blockingDeleteStore{PebbleStore: store, id: "T", entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewAudiobookService(blocking)
	withDeleteFiles(t)

	row, err := store.GetBookByID("T")
	require.NoError(t, err)
	singleDone := purgeNowInBackground(svc, row)
	requireWithin(t, blocking.entered, 5*time.Second, "fixture: the purge never reached its row delete")
	require.Len(t, pendingJournal(t, store), 1, "fixture: the record is written and the book is still in the trash")

	type replayed struct {
		removed int
		errs    []string
	}
	replayDone := make(chan replayed, 1)
	go func() {
		n, errs := svc.replayPendingFileRemovals(context.Background())
		replayDone <- replayed{n, errs}
	}()
	select {
	case r := <-replayDone:
		close(blocking.release)
		t.Fatalf("the replay read the journal between a purge's record and its row delete: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}

	close(blocking.release)
	require.NoError(t, requireWithin(t, singleDone, 5*time.Second, "the purge never finished"))
	r := requireWithin(t, replayDone, 5*time.Second, "the replay never finished")
	assert.Empty(t, r.errs)
	assert.False(t, fileExists(x), "the file is removed, by the purge or by the replay that waited")
	assert.Empty(t, pendingJournal(t, store))
}

// n-1. A replay honours its context: a cancelled one looks at no path, removes
// nothing, keeps every record and says so.
func TestReplay_CancelledContextLooksAtNothingAndKeepsEveryRecord(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "hush.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	crashAfterRowAndTombstone(t, svc, store, "T")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	removed, errs := svc.replayPendingFileRemovals(ctx)
	assert.Zero(t, removed)
	require.Len(t, errs, 1, "%v", errs)
	assert.Contains(t, errs[0], "not looked at")
	assert.True(t, fileExists(x))
	assert.Len(t, pendingJournal(t, store), 1)

	removed, errs = svc.replayPendingFileRemovals(context.Background())
	assert.Equal(t, 1, removed, "%v", errs)
	assert.False(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store))
}

// n-1. Stopping part way: the paths already decided are settled and the rest
// keep their records.
func TestReplay_ContextCancelledMidRunKeepsTheRestOfTheRecords(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	var paths []string
	for _, name := range []string{"hush.m4b", "knightfall.m4b", "year-one.m4b"} {
		p := filepath.Join(base, "lib", "Gotham", name)
		writeFile(t, p)
		paths = append(paths, p)
	}
	for i, p := range paths {
		softDeleted(t, store, string(rune('A'+i)), p)
	}
	hooked := &hookedPathStore{PebbleStore: store}
	svc := NewAudiobookService(hooked)
	crashAfterRowAndTombstone(t, svc, store, "A", "B", "C")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	hooked.beforeLive = func(string) {
		if calls.Add(1) == 1 {
			cancel()
		}
	}
	removed, errs := svc.replayPendingFileRemovals(ctx)
	left := len(pendingJournal(t, store))
	// The workers that had started finish; those that had not are not looked at.
	assert.Equal(t, 3-left, removed, "%v", errs)
	assert.NotZero(t, removed, "the path being decided when it was cancelled is finished")
	if left > 0 {
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[len(errs)-1], "not looked at")
	}
}

// failingForgetStore fails DeleteBook, and (when forgetErr is set) the batch
// delete the journal's forget uses.
type failingForgetStore struct {
	*database.PebbleStore
	deleteErr atomic.Bool
	forgetErr atomic.Bool
}

func (s *failingForgetStore) DeleteBook(id string) error {
	if s.deleteErr.Load() {
		return errors.New("simulated delete failure")
	}
	return s.PebbleStore.DeleteBook(id)
}

func (s *failingForgetStore) DeleteRawBatch(keys []string) error {
	if s.forgetErr.Load() {
		return errors.New("simulated journal failure")
	}
	return s.PebbleStore.DeleteRawBatch(keys)
}

// n-8. A delete that fails, and a journal record that cannot be taken back
// after it, is reported in the error, not dropped. The record outlives a book
// that is still in the trash; a later replay finds that book at the path, keeps
// the file and clears the record.
func TestPurgeDeleteRow_RecordThatCannotBeTakenBackIsReportedAndSettledByTheNextReplay(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "year-one.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	failing := &failingForgetStore{PebbleStore: store}
	svc := NewAudiobookService(failing)
	withDeleteFiles(t)
	failing.deleteErr.Store(true)
	failing.forgetErr.Store(true)

	row, err := store.GetBookByID("T")
	require.NoError(t, err)
	err = requireWithin(t, purgeNowInBackground(svc, row), 5*time.Second, "the purge never returned")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete DB record")
	assert.Contains(t, err.Error(), "could not be taken back either")
	assert.Contains(t, err.Error(), "simulated journal failure")
	assert.False(t, bookGone(t, store, "T"), "the book is still in the trash")
	assert.True(t, fileExists(x))
	require.Len(t, pendingJournal(t, store), 1, "the record could not be taken back")

	failing.deleteErr.Store(false)
	failing.forgetErr.Store(false)
	removed, errs := svc.replayPendingFileRemovals(context.Background())
	assert.Zero(t, removed, "a book still names the path: its file is kept")
	require.Len(t, errs, 1, "%v", errs)
	assert.Contains(t, errs[0], "is still the path of book(s) [T]")
	assert.True(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store), "and the record is cleared")
	assert.False(t, bookGone(t, store, "T"))
}

// The same failed delete, when the record CAN be taken back, says nothing about
// a record: there is none left.
func TestPurgeDeleteRow_FailedDeleteWithRecordTakenBackAddsNoNote(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Gotham", "year-one.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	failing := &failingForgetStore{PebbleStore: store}
	svc := NewAudiobookService(failing)
	withDeleteFiles(t)
	failing.deleteErr.Store(true)

	row, err := store.GetBookByID("T")
	require.NoError(t, err)
	err = requireWithin(t, purgeNowInBackground(svc, row), 5*time.Second, "the purge never returned")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "taken back")
	assert.Empty(t, pendingJournal(t, store))
}

// SH-1(c). ENOENT is "already gone" only when the file's folder is there. A
// file whose folder is missing too may be on a library that is not (fully)
// mounted, so the book stays in the trash and nothing is decided; once the
// folder is readable the same file is gone and the book purges.
func TestRunPurge_MissingFileInAMissingFolderIsNotDecidedGone(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	// The library holds the application's own folders and one book that is
	// there, so the root as a whole looks mounted.
	libraryAppDirs(t)
	liveBookWithFile(t, store, "present", filepath.Join(base, "lib", "Arkham", "year-one.m4b"))
	folder := filepath.Join(base, "lib", "Gotham")
	softDeleted(t, store, "T", filepath.Join(folder, "hush.m4b"))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "%+v", res)
	assert.False(t, bookGone(t, store, "T"), "kept in the trash")
	require.Len(t, res.Errors, 1, "%+v", res)
	assert.Contains(t, res.Errors[0], "its folder")
	assert.Contains(t, res.Errors[0], "missing too")

	writeFile(t, filepath.Join(folder, "knightfall.m4b")) // the folder is back, holding another book's file
	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.Empty(t, res.Errors, "%+v", res)
	assert.True(t, bookGone(t, store, "T"))
}

// SH-2. A journal record for a file whose folder is missing is kept for as long
// as the folder is missing, on every pass, with no cap: a cap turned "the library
// was not mounted for three passes" into a deleted record and a leftover file.
// The passes report ONE line for all such records (a count and an example), not
// one line per record, and the records go the moment the folder is readable.
func TestReplay_RecordsWhoseFolderIsMissingAreKeptWithOneLinePerPass(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	libraryAppDirs(t)
	liveBookWithFile(t, store, "present", filepath.Join(base, "lib", "Arkham", "year-one.m4b"))
	folder := filepath.Join(base, "lib", "Gotham")
	ids := []string{"T", "U", "V"}
	names := []string{"hush.m4b", "knightfall.m4b", "dark-victory.m4b"}
	for i, id := range ids {
		require.NoError(t, svc.journalPendingFile(id, purgeFileVerdict{remove: filepath.Join(folder, names[i]), id: fileIdentity{Known: true, Size: 1, MtimeNS: 1}}))
	}

	for pass := 1; pass <= 2*maxUnresolvableAttempts; pass++ {
		removed, errs := svc.replayPendingFileRemovals(context.Background())
		assert.Zero(t, removed, "pass %d", pass)
		require.Len(t, errs, 1, "pass %d: one line for three records, got %v", pass, errs)
		assert.Contains(t, errs[0], "3 pending file removal(s) are waiting for a folder that is missing", "pass %d", pass)
		assert.Contains(t, errs[0], folder, "pass %d: the line names an example folder", pass)
		assert.NotContains(t, errs[0], "gave up", "pass %d", pass)
		require.Len(t, pendingJournal(t, store), 3, "pass %d: every record is kept", pass)
	}

	writeFile(t, filepath.Join(folder, "no-mans-land.m4b")) // the folder is back, holding another book's file
	removed, errs := svc.replayPendingFileRemovals(context.Background())
	assert.Zero(t, removed, "the files were already gone")
	assert.Empty(t, errs, "%v", errs)
	assert.Empty(t, pendingJournal(t, store), "the folder is readable: the files are gone, the records clear")
}

// SH-1(c). ENOENT with the folder there is still "already gone": the record is
// cleared quietly, as before.
func TestReplay_MissingFileInAPresentFolderIsGone(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	folder := filepath.Join(base, "lib", "Gotham")
	writeFile(t, filepath.Join(folder, "knightfall.m4b")) // a folder that holds another book's file
	require.NoError(t, svc.journalPendingFile("T", purgeFileVerdict{remove: filepath.Join(folder, "hush.m4b"), id: fileIdentity{Known: true, Size: 1, MtimeNS: 1}}))

	removed, errs := svc.replayPendingFileRemovals(context.Background())
	assert.Zero(t, removed)
	assert.Empty(t, errs, "%v", errs)
	assert.Empty(t, pendingJournal(t, store))
}

// n-4. Mid-pass mount loss. decideFile asks the pass's guard, which re-checks the
// root once its last good check is older than rootGuardTTL: the books after the
// loss stay in the trash with the root-not-ready line instead of being decided
// from ENOENT.
func TestDecideFile_PassGuardChecksTheRootAgainAfterItsTTL(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	liveBookWithFile(t, store, "mounted-witness", filepath.Join(config.AppConfig.RootDir, "Gotham", "year-one.m4b"))
	softDeleted(t, store, "T", filepath.Join(base, "lib", "Gotham", "hush.m4b"))
	book, err := store.GetBookByID("T")
	require.NoError(t, err)

	now := time.Now()
	g := newRootGuard(svc, config.AppConfig.RootDir, now)
	g.now = func() time.Time { return now }
	env := purgeEnv{root: g}

	plan, out := svc.decideFile(book, env)
	require.Nil(t, out, "inside the TTL the pass trusts its start-of-pass check")
	require.NotNil(t, plan)

	// The mount is lost, then the guard's TTL passes.
	unmountLibrary(t)
	now = now.Add(rootGuardTTL + time.Second)
	plan, out = svc.decideFile(book, env)
	require.Nil(t, plan)
	require.NotNil(t, out)
	assert.Equal(t, purgeFailed, out.kind)
	require.Len(t, out.errs, 1)
	assert.Contains(t, out.errs[0], "library root is not ready")
}
