// file: internal/audiobooks/purge_file_decision_test.go
// version: 1.0.0
// guid: 0b7d3e58-9c14-4a62-b5f1-8e2a6d4c1f97
// last-edited: 2026-10-25

package audiobooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// B-2. The file is decided BEFORE the row is deleted. A stat that fails for a
// reason that may pass (here: an unreadable parent directory) keeps the whole
// book in the trash and writes no journal record, so nothing is orphaned; the
// next pass, with the directory readable, purges the book and removes the file.
func TestRunPurge_FileThatCannotBeDecidedKeepsTheBookInTheTrash(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the stat cannot be made to fail")
	}
	svc, store, base := setupPurgeBoundary(t)
	dir := filepath.Join(base, "lib", "Author")
	x := filepath.Join(dir, "undecided.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)

	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.Failed, "%+v", res)
	require.Len(t, res.Errors, 1, "%+v", res)
	assert.Contains(t, res.Errors[0], "kept in the trash")
	assert.False(t, bookGone(t, store, "T"), "the row is still there: nothing was decided, so nothing was deleted")
	assert.Empty(t, pendingJournal(t, store), "no row was deleted, so there is nothing to journal")

	require.NoError(t, os.Chmod(dir, 0o755))
	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.True(t, bookGone(t, store, "T"))
	assert.False(t, fileExists(x))
	assert.Empty(t, res.Errors, "%+v", res)
}

// A lookup that fails the same way (the store cannot say whether a book_file row
// names the path) also keeps the book: purgePathStillReferenced runs in the
// decision, ahead of the delete.
func TestRunPurge_PathLookupFailureKeepsTheBookInTheTrash(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "lookup.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)

	hooked := &hookedPathStore{PebbleStore: store}
	svc := NewAudiobookService(hooked)
	var failing = true
	hooked.beforeLive = func(string) {}
	hooked.liveErr = func(string) error {
		if failing {
			return errors.New("simulated live lookup failure")
		}
		return nil
	}
	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Zero(t, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.Failed, "%+v", res)
	assert.False(t, bookGone(t, store, "T"))
	assert.True(t, fileExists(x))

	failing = false
	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Purged, "%+v", res)
	assert.False(t, fileExists(x))
}

// failingDeleteStore fails DeleteBook, and reports how many journal records
// existed at the moment it was asked to delete.
type failingDeleteStore struct {
	*database.PebbleStore
	journalAtDelete int
}

func (f *failingDeleteStore) DeleteBook(id string) error {
	pairs, _ := f.PebbleStore.ScanPrefix(pendingFilePrefix)
	f.journalAtDelete = len(pairs)
	return errors.New("simulated delete failure")
}

// The journal record of a file's removal is written BEFORE the row is deleted
// (closing the window in which a crash leaves a file with no row and no record),
// and taken back when the delete does not happen, so a later pass that deletes
// files never removes the file of a book that is still there.
func TestPurgeDeleteRow_JournalsBeforeTheDeleteAndForgetsWhenItFails(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "journaled.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	row, err := store.GetBookByID("T")
	require.NoError(t, err)

	failing := &failingDeleteStore{PebbleStore: store}
	svc := NewAudiobookService(failing)
	plan := &filePlan{path: x, v: purgeFileVerdict{remove: x, id: identityAt(t, x)}, journal: true}
	require.Error(t, svc.purgeDeleteRow(row, plan))

	assert.Equal(t, 1, failing.journalAtDelete, "the record was already written when the row delete was attempted")
	assert.Empty(t, pendingJournal(t, store), "the delete failed, so the record was taken back")
	assert.False(t, bookGone(t, store, "T"))
}

// A row whose path changed after the pass decided about its file was not
// decided: it stays in the trash for the next pass.
func TestPlanStillFor_RowWithAnotherPathIsRefused(t *testing.T) {
	var none *filePlan
	assert.NoError(t, none.stillFor(&database.Book{FilePath: "/anywhere"}))
	p := &filePlan{path: "/lib/a.m4b"}
	assert.NoError(t, p.stillFor(&database.Book{FilePath: "/lib/a.m4b"}))
	assert.ErrorContains(t, p.stillFor(&database.Book{FilePath: "/lib/b.m4b"}), "file path changed")
}

// n-a. ENOTDIR means the path cannot exist (a parent is a regular file), so the
// file is gone and the record is cleared quietly, as the base always read it.
func TestRunPurge_ReplayOfAPathUnderARegularFileClearsTheRecord(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	parent := filepath.Join(base, "lib", "Author", "notadir.m4b")
	writeFile(t, parent)
	child := filepath.Join(parent, "child.m4b")
	require.NoError(t, svc.journalPendingFile("T", purgeFileVerdict{remove: child, id: identityAt(t, parent)}))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Empty(t, res.Errors, "%+v", res)
	assert.Zero(t, res.FilesReplayed)
	assert.Empty(t, pendingJournal(t, store), "gone: nothing to retry")
	assert.True(t, fileExists(parent), "the regular file it sits under is not touched")
}

// n-a. ELOOP will not resolve by trying again: the record is retried a bounded
// number of passes, then cleared with a line that says it gave up.
func TestRunPurge_ReplayOfAnUnresolvablePathGivesUpAfterBoundedAttempts(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	dir := filepath.Join(base, "lib", "Author")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	a, b := filepath.Join(dir, "loopa"), filepath.Join(dir, "loopb")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))
	target := filepath.Join(a, "x.m4b")
	require.NoError(t, svc.journalPendingFile("T", purgeFileVerdict{remove: target, id: fileIdentity{Known: true, Size: 1, MtimeNS: 1}}))

	for pass := 1; pass < maxUnresolvableAttempts; pass++ {
		res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
		require.NoError(t, err)
		require.Len(t, res.Errors, 1, "pass %d: %+v", pass, res)
		assert.NotContains(t, res.Errors[0], "gave up", "pass %d", pass)
		pairs := pendingJournal(t, store)
		require.Len(t, pairs, 1, "pass %d: the record is kept", pass)
	}
	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	require.Len(t, res.Errors, 1, "%+v", res)
	assert.Contains(t, res.Errors[0], "gave up")
	assert.Empty(t, pendingJournal(t, store), "cleared after the last attempt")
}

// B-4 (backend half). With the trash empty, the journal an earlier delete-files
// pass left is still visible to a preview (files_pending_from_earlier_pass) and a
// run bound to that preview, with delete_files, drains it: the dialog's "finish
// removing" button is exactly that run.
func TestRunPurge_EmptyTrashRunDrainsTheJournalOfAnEarlierPass(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "left.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	crashAfterRowAndTombstone(t, svc, store, "T")
	require.True(t, fileExists(x))

	pv := previewOnce(t, svc, nil)
	assert.Zero(t, pv.Attempted, "the trash is empty")
	assert.Equal(t, 1, pv.FilesPendingFromEarlierPass, "%+v", pv)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
	assert.False(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store))
}
