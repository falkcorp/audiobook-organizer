// file: internal/audiobooks/purge_round13_test.go
// version: 1.0.0
// guid: 135fbf0d-bc38-4cf9-8ac6-52a1944113bb
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Round 13 SH-1. A book_file row whose path is the library root with a trailing
// separator used to make the sample empty, and an empty sample reads as "no live
// file under the root", so an unmounted root was ready. It must stay refused.
func TestLibraryRootReady_ARowThatIsTheRootDoesNotMakeAnUnmountedRootReady(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	remount := unmountRoot(t, store)
	defer remount()
	require.ErrorIs(t, svc.libraryRootReady(config.AppConfig.RootDir), ErrLibraryRootNotReady, "control: unmounted is refused")

	softDeleted(t, store, "Joker", "")
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "joker-f", BookID: "Joker", FilePath: config.AppConfig.RootDir + "/", Format: "m4b"}))
	require.ErrorIs(t, svc.libraryRootReady(config.AppConfig.RootDir), ErrLibraryRootNotReady,
		"a directory path stored as a file is not a reason to trust the mount")
}

// waitingRecord journals a removal under a folder that does not exist and marks
// it as waiting, the state the forget action is for.
func waitingRecord(t *testing.T, svc *AudiobookService, bookID, folder, name string) string {
	t.Helper()
	p := journalGone(t, svc, bookID, folder, name)
	require.NoError(t, svc.setWaitingForFolder(&pendingFile{bookIDs: []string{bookID}}, true))
	return p
}

// Round 13 SH-2. The list shows only the paths whose records ALL wait, in path
// order, and at most forgetMaxPaths of them with the true total.
func TestWaitingFileRecordPaths_ListsOnlyWaitingPathsInOrderAndCapsThem(t *testing.T) {
	svc, _, base := setupPurgeBoundary(t)
	lib := filepath.Join(base, "lib")
	waitingRecord(t, svc, "b-wayne", filepath.Join(lib, "Wayne"), "hush.m4b")
	waitingRecord(t, svc, "b-arkham", filepath.Join(lib, "Arkham"), "knightfall.m4b")
	journalGone(t, svc, "b-pending", filepath.Join(lib, "Gotham"), "year-one.m4b") // not marked

	l, err := svc.WaitingFileRecordPaths()
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(lib, "Arkham", "knightfall.m4b"), filepath.Join(lib, "Wayne", "hush.m4b")}, l.Paths)
	assert.Equal(t, 2, l.Total)
	assert.Equal(t, forgetMaxPaths, l.Limit)

	for i := 0; i < forgetMaxPaths; i++ {
		waitingRecord(t, svc, fmt.Sprintf("bulk%03d", i), filepath.Join(lib, "Bulk"), fmt.Sprintf("hush-%03d.m4b", i))
	}
	l, err = svc.WaitingFileRecordPaths()
	require.NoError(t, err)
	assert.Len(t, l.Paths, forgetMaxPaths)
	assert.Equal(t, forgetMaxPaths+2, l.Total, "the total counts what the list leaves out")
}

// The owner forgets what the dialog showed. A path that became waiting after the
// listing is not forgotten, and one that stopped waiting is counted, not touched.
func TestForgetWaitingFileRecords_ForgetsOnlyWhatWasShown(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	lib := filepath.Join(base, "lib")
	a := waitingRecord(t, svc, "A", filepath.Join(lib, "Wayne"), "hush.m4b")
	waitingRecord(t, svc, "B", filepath.Join(lib, "Arkham"), "knightfall.m4b")
	shown := listedWaiting(t, svc)
	require.Len(t, shown, 2)

	// After the listing: a new path becomes waiting, and one shown path is settled.
	waitingRecord(t, svc, "N", filepath.Join(lib, "Narrows"), "long-halloween.m4b")
	require.NoError(t, svc.forgetPendingFiles([]string{"B"}))

	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", shown)
	require.NoError(t, err)
	assert.Equal(t, []string{a}, res.ForgottenPaths)
	assert.Equal(t, 1, res.Records)
	assert.Equal(t, 1, res.NoLongerWaiting, "B was settled between the listing and the confirm")
	assert.Equal(t, []string{pendingFilePrefix + "N"}, journalKeys(t, store), "the path nobody was shown stays")
}

// A folder that holds files again is kept: the next purge decides it.
func TestForgetWaitingFileRecords_AFolderThatHoldsFilesAgainIsKept(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	lib := filepath.Join(base, "lib")
	waitingRecord(t, svc, "A", filepath.Join(lib, "Wayne"), "hush.m4b")
	back := waitingRecord(t, svc, "B", filepath.Join(lib, "Arkham"), "knightfall.m4b")
	shown := listedWaiting(t, svc)
	// The mount came back between the mark and the confirm.
	writeFile(t, filepath.Join(filepath.Dir(back), "sibling.m4b"))

	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", shown)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Records)
	require.Len(t, res.Kept, 1)
	assert.Equal(t, back, res.Kept[0].Path)
	assert.Contains(t, res.Kept[0].Reason, "its folder holds files again")
	assert.Equal(t, []string{pendingFilePrefix + "B"}, journalKeys(t, store))
	assert.True(t, fileExists(filepath.Join(filepath.Dir(back), "sibling.m4b")), "no file is touched")
}

// The audit row names EVERY forgotten path, not a sample of them.
func TestForgetWaitingFileRecords_TheAuditRowNamesEveryForgottenPath(t *testing.T) {
	svc, _, base := setupPurgeBoundary(t)
	rec := &fakeRecorder{}
	svc.auditOverride = rec
	var want []string
	for i := 0; i < 45; i++ {
		want = append(want, waitingRecord(t, svc, fmt.Sprintf("b%02d", i), filepath.Join(base, "lib", "Wayne"), fmt.Sprintf("hush-%02d.m4b", i)))
	}
	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", listedWaiting(t, svc))
	require.NoError(t, err)
	assert.Equal(t, 45, res.Paths)
	require.Len(t, rec.entries, 1)
	assert.ElementsMatch(t, want, rec.entries[0].Details["paths"])
	assert.Equal(t, 45, rec.entries[0].Details["paths_sent"])
}

func TestForgetWaitingFileRecords_RefusesNothingAndTooMany(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	waitingRecord(t, svc, "A", filepath.Join(base, "lib", "Wayne"), "hush.m4b")

	_, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", []string{"", ""})
	require.ErrorIs(t, err, ErrForgetNothingShown)

	var many []string
	for i := 0; i <= forgetMaxPaths; i++ {
		many = append(many, fmt.Sprintf("/lib/Gotham/Bruce Wayne/hush-%03d.m4b", i))
	}
	_, err = svc.ForgetWaitingFileRecords(context.Background(), "owner", many)
	require.ErrorIs(t, err, ErrForgetTooMany)
	assert.Len(t, journalKeys(t, store), 1, "nothing was forgotten")
}

// Every folder reads as missing while the library is not mounted, so the action
// refuses until the root check passes.
func TestForgetWaitingFileRecords_RefusesWhileTheLibraryRootIsNotReady(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	p := waitingRecord(t, svc, "A", filepath.Join(base, "lib", "Wayne"), "hush.m4b")
	shown := listedWaiting(t, svc)
	remount := unmountRoot(t, store)
	defer remount()

	_, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", []string{p})
	require.ErrorIs(t, err, ErrLibraryRootNotReady)
	assert.Equal(t, []string{p}, shown)
	assert.Len(t, journalKeys(t, store), 1)
}

// A purge removing files holds replayMu for its whole run; the action waits for
// it a bounded time and then says so, with nothing changed.
func TestForgetWaitingFileRecords_WaitsForAPurgeOnlyAWhile(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	p := waitingRecord(t, svc, "A", filepath.Join(base, "lib", "Wayne"), "hush.m4b")
	prev := forgetLockWait
	forgetLockWait = 50 * time.Millisecond
	defer func() { forgetLockWait = prev }()

	svc.replayMu.Lock()
	start := time.Now()
	_, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", []string{p})
	waited := time.Since(start)
	svc.replayMu.Unlock()
	require.ErrorIs(t, err, ErrPurgeBusy)
	assert.Less(t, waited, 5*time.Second)
	assert.Len(t, journalKeys(t, store), 1)

	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", []string{p})
	require.NoError(t, err, "the lock is free again")
	assert.Equal(t, 1, res.Records)
}

// A stat that does not answer keeps the record and is counted as hung: the
// action never waits on a dead mount.
func TestForgetWaitingFileRecords_ADeadMountKeepsTheRecordInsteadOfHanging(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	p := waitingRecord(t, svc, "A", filepath.Join(base, "lib", "Wayne"), "hush.m4b")
	shown := listedWaiting(t, svc)

	release := make(chan struct{})
	realStat := rootStat
	hang := func(name string) (os.FileInfo, error) {
		if name == p {
			<-release
		}
		return realStat(name)
	}
	rootStat = hang
	prevTimeout := rootStatTimeout
	rootStatTimeout = 100 * time.Millisecond
	t.Cleanup(func() { rootStat = realStat; rootStatTimeout = prevTimeout })

	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", shown)
	require.NoError(t, err)
	assert.Zero(t, res.Records)
	require.Len(t, res.Kept, 1)
	assert.Contains(t, res.Kept[0].Reason, "did not answer")
	assert.Len(t, journalKeys(t, store), 1)
	assert.Positive(t, rootStatsHung.Load(), "the blocked stat is counted until it returns")
	close(release)
	require.Eventually(t, func() bool { return rootStatsHung.Load() == 0 }, 5*time.Second, 10*time.Millisecond)
}

// A stat that fails for a reason other than "not there" (permission, I/O) is not
// evidence that the file is gone: the record is kept and the reason shown.
func TestForgetWaitingFileRecords_AStatThatFailsForAnotherReasonKeepsTheRecord(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	p := waitingRecord(t, svc, "A", filepath.Join(base, "lib", "Wayne"), "hush.m4b")
	shown := listedWaiting(t, svc)
	realStat := rootStat
	rootStat = func(name string) (os.FileInfo, error) {
		if name == p {
			return nil, fmt.Errorf("batcave offline")
		}
		return realStat(name)
	}
	t.Cleanup(func() { rootStat = realStat })

	res, err := svc.ForgetWaitingFileRecords(context.Background(), "owner", shown)
	require.NoError(t, err)
	assert.Zero(t, res.Records)
	require.Len(t, res.Kept, 1)
	assert.Contains(t, res.Kept[0].Reason, "cannot tell whether the file is there")
	assert.Len(t, journalKeys(t, store), 1)
}

// A request that is cancelled while it waits for a purge stops waiting at once,
// not after forgetLockWait.
func TestForgetWaitingFileRecords_ACancelledRequestStopsWaitingForAPurge(t *testing.T) {
	svc, _, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	p := waitingRecord(t, svc, "A", filepath.Join(base, "lib", "Wayne"), "hush.m4b")
	prev := forgetLockWait
	forgetLockWait = time.Hour
	defer func() { forgetLockWait = prev }()

	svc.replayMu.Lock()
	defer svc.replayMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := svc.ForgetWaitingFileRecords(ctx, "owner", []string{p})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second)
}

// N-7. A forget that lands between the read and the write of a record's
// waiting mark must stay forgotten: the mark's write used to put the record back.
func TestSetWaitingForFolder_ADeleteBetweenItsReadAndWriteStaysDeleted(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	journalGone(t, svc, "A", filepath.Join(base, "lib", "Wayne"), "hush.m4b")

	var once sync.Once
	var done chan struct{}
	journalReadHook = func() {
		once.Do(func() {
			done = make(chan struct{})
			go func() {
				defer close(done)
				_ = svc.forgetPendingFiles([]string{"A"})
			}()
			select { // a correct lock keeps the delete waiting, so this times out
			case <-done:
			case <-time.After(300 * time.Millisecond):
			}
		})
	}
	t.Cleanup(func() { journalReadHook = func() {} })

	require.NoError(t, svc.setWaitingForFolder(&pendingFile{bookIDs: []string{"A"}}, true))
	<-done
	assert.Empty(t, journalKeys(t, store), "the forgotten record was written back")
}

// N-1. A preview with delete_files says whether the library root check would
// pass, so the owner sees the refusal before choosing to delete files.
func TestRunPurge_PreviewReportsTheLibraryRootStatus(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	pv, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true, DeleteFiles: true})
	require.NoError(t, err)
	require.NotNil(t, pv.LibraryRoot)
	assert.True(t, pv.LibraryRoot.Ready, "%+v", pv.LibraryRoot)
	assert.Empty(t, pv.LibraryRoot.Problem)

	remount := unmountRoot(t, store)
	defer remount()
	pv, err = svc.RunPurge(context.Background(), PurgeOptions{DryRun: true, DeleteFiles: true})
	require.NoError(t, err)
	require.NotNil(t, pv.LibraryRoot)
	assert.False(t, pv.LibraryRoot.Ready)
	assert.Contains(t, pv.LibraryRoot.Problem, "library root")

	pv, err = svc.RunPurge(context.Background(), PurgeOptions{DryRun: true})
	require.NoError(t, err)
	require.NotNil(t, pv.LibraryRoot, "every preview carries it, so the dialog can show it before delete_files is chosen")
	assert.False(t, pv.LibraryRoot.Ready)
}
