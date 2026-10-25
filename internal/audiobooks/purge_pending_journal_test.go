// file: internal/audiobooks/purge_pending_journal_test.go
// version: 1.0.0
// guid: 2e8a4c71-6d09-4b35-9f1a-c7d3b05e8a62
// last-edited: 2026-10-25

package audiobooks

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// hookedPathStore is a PebbleStore whose path lookups can be intercepted: the
// seam the race and the transient-failure tests need.
type hookedPathStore struct {
	*database.PebbleStore
	// beforeLive runs at the start of every LiveBookIDsAtPath call (the first
	// path check planPurgeFile makes), then the real lookup answers.
	beforeLive func(path string)
	// allRowsErr, when it returns non-nil, fails BookIDsAtPath with that error.
	allRowsErr func(path string) error
}

func (h *hookedPathStore) LiveBookIDsAtPath(path string) ([]string, error) {
	if h.beforeLive != nil {
		h.beforeLive(path)
	}
	return h.PebbleStore.LiveBookIDsAtPath(path)
}

func (h *hookedPathStore) BookIDsAtPath(path string) ([]string, error) {
	if h.allRowsErr != nil {
		if err := h.allRowsErr(path); err != nil {
			return nil, err
		}
	}
	return h.PebbleStore.BookIDsAtPath(path)
}

// pendingJournal returns the pending-removal records in the store.
func pendingJournal(t *testing.T, store *database.PebbleStore) []database.KVPair {
	t.Helper()
	pairs, err := store.ScanPrefix(pendingFilePrefix)
	require.NoError(t, err)
	return pairs
}

// crashAfterRowAndTombstone runs the real per-book purge for each id with a
// delete_files pass's deferred removal, then stops: the process "dies" before
// removeDeferredFiles. What is left is what a crash leaves: rows and tombstones
// gone, files on disk, the journal.
func crashAfterRowAndTombstone(t *testing.T, svc *AudiobookService, store *database.PebbleStore, ids ...string) {
	t.Helper()
	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	env.files = &pendingFiles{}
	for _, id := range ids {
		row, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, row)
		out := svc.purgeOne(row, true, env)
		require.Equal(t, purgePurged, out.kind, "%s: %+v", id, out)
		require.True(t, bookGone(t, store, id))
	}
	require.NotEmpty(t, env.files.sorted(), "the pass had decided on removals")
}

// S1. A book trashed while the removal loop runs is restorable and wants its
// audio. The removal reads the store about each path when it gets to it, so a
// live book that is trashed after the removal began (here: between the pass's
// first read of the path and its check for other books) still keeps the file.
// A trash listing taken once at the start of the loop would have shown it live.
func TestRemoveDeferredFiles_BookTrashedMidLoopStillKeepsItsFile(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "race.m4b")
	writeFile(t, x)
	_, err := store.CreateBook(&database.Book{ID: "L", Title: "L", FilePath: x})
	require.NoError(t, err)

	hooked := &hookedPathStore{PebbleStore: store}
	svc = NewAudiobookService(hooked)
	var trashedL atomic.Bool
	hooked.beforeLive = func(string) {
		if trashedL.CompareAndSwap(false, true) {
			_, merr := store.ModifyBook("L", func(b *database.Book) error {
				yes := true
				b.MarkedForDeletion = &yes
				return nil
			})
			require.NoError(t, merr)
		}
	}

	files := &pendingFiles{}
	files.add("purged", purgeFileVerdict{remove: x})
	removed, errs := svc.removeDeferredFiles(files, nil)

	require.True(t, trashedL.Load(), "fixture: L was trashed inside the loop")
	assert.Zero(t, removed)
	assert.True(t, fileExists(x), "L is trashed now, and restorable: it keeps its audio")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "L")
}

// The same question in the single-book purge (DiscardProgressAndPurge): a
// trashed book that names the file keeps it, found by the per-path lookup and
// not by listing the trash.
func TestDiscardProgressAndPurge_FileNamedByAnotherTrashedBookStays(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	prev := config.AppConfig.PurgeSoftDeletedDeleteFiles
	config.AppConfig.PurgeSoftDeletedDeleteFiles = true
	t.Cleanup(func() { config.AppConfig.PurgeSoftDeletedDeleteFiles = prev })

	shared := filepath.Join(base, "lib", "Author", "shared.m4b")
	alone := filepath.Join(base, "lib", "Author", "alone.m4b")
	writeFile(t, shared)
	writeFile(t, alone)
	softDeleted(t, store, "mine", shared)
	softDeleted(t, store, "twin", shared)
	softDeleted(t, store, "solo", alone)

	res, err := svc.DiscardProgressAndPurge(context.Background(), "mine", "owner")
	require.NoError(t, err)
	assert.Zero(t, res.FilesDeleted)
	assert.True(t, fileExists(shared), "the trashed twin still names it")
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "twin")
	assert.False(t, bookGone(t, store, "twin"))

	res, err = svc.DiscardProgressAndPurge(context.Background(), "solo", "owner")
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesDeleted)
	assert.False(t, fileExists(alone), "nothing else names it")
}

// S2. A pass that dies after it deleted rows and tombstones and before it
// removed their files leaves the removals in the journal. A later pass carries
// them out, through the same checks: a file nothing names goes, a file a live
// book has taken since is kept.
func TestRunPurge_ReplaysRemovalsAnEarlierPassLeftPending(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	free := filepath.Join(base, "lib", "Author", "free.m4b")
	taken := filepath.Join(base, "lib", "Author", "taken.m4b")
	writeFile(t, free)
	writeFile(t, taken)
	softDeleted(t, store, "T1", free)
	softDeleted(t, store, "T2", taken)

	crashAfterRowAndTombstone(t, svc, store, "T1", "T2")
	assert.True(t, fileExists(free), "fixture: the crash left both files")
	assert.True(t, fileExists(taken))
	require.Len(t, pendingJournal(t, store), 2, "one record per purged book")
	for _, id := range []string{"T1", "T2"} {
		tomb, err := store.GetBookTombstone(id)
		require.NoError(t, err)
		require.Nil(t, tomb, "fixture: %s's tombstone is gone, so only the journal names its file", id)
	}

	// A rescan, or a user, gives a live book the second file before the next
	// pass runs.
	_, err := store.CreateBook(&database.Book{ID: "L", Title: "L", FilePath: taken})
	require.NoError(t, err)

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesDeleted, "%+v", res)
	assert.False(t, fileExists(free), "nothing names it: the removal the dead pass decided on is done")
	assert.True(t, fileExists(taken), "a live book names it now: the replay runs the same checks")
	require.Len(t, res.Errors, 1, "%+v", res)
	assert.Contains(t, res.Errors[0], "T2")
	assert.Empty(t, pendingJournal(t, store), "both records are decided, so both are cleared")
}

// A resumed purge op is refused when its preview is no longer in memory, and
// that refusal must not strand the files its predecessor left: the replay runs
// before it and needs no preview.
func TestRunPurge_RefusedForWantOfItsPreviewStillReplaysThePendingRemovals(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "resumed.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	crashAfterRowAndTombstone(t, svc, store, "T")

	// A new process: nothing is remembered.
	restarted := NewAudiobookService(store)
	bound := time.Now()
	res, err := restarted.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, TrashedBefore: &bound})
	require.ErrorIs(t, err, ErrPurgePreviewUnknown)
	require.NotNil(t, res)
	assert.True(t, res.StoppedEarly)
	assert.Equal(t, 1, res.FilesDeleted, "%+v", res)
	assert.False(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store))

	// And the same for a pass sent with no trashed_before at all.
	writeFile(t, x)
	softDeleted(t, store, "T2", x)
	crashAfterRowAndTombstone(t, restarted, store, "T2")
	res, err = restarted.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true})
	require.ErrorIs(t, err, ErrPurgePreviewRequired)
	assert.Equal(t, 1, res.FilesDeleted, "%+v", res)
	assert.False(t, fileExists(x))
}

// A preview never replays: it changes nothing.
func TestRunPurge_PreviewDoesNotReplayPendingRemovals(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "preview.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	crashAfterRowAndTombstone(t, svc, store, "T")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DryRun: true})
	require.NoError(t, err)
	assert.Zero(t, res.FilesDeleted)
	assert.True(t, fileExists(x))
	assert.Len(t, pendingJournal(t, store), 1)
}

// A removal that could not be decided (the store failed to answer, or the file
// could not be removed) stays journaled and is retried by the next pass; one
// that is decided for good is cleared.
func TestRunPurge_UndecidedRemovalStaysJournaledAndIsRetried(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "retry.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)

	hooked := &hookedPathStore{PebbleStore: store}
	svc := NewAudiobookService(hooked)
	crashAfterRowAndTombstone(t, svc, store, "T")

	failing := errors.New("simulated lookup failure")
	hooked.allRowsErr = func(string) error { return failing }
	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Zero(t, res.FilesDeleted)
	assert.True(t, fileExists(x), "a path that cannot be vouched for is not removed")
	require.Len(t, res.Errors, 1, "%+v", res)
	assert.Contains(t, res.Errors[0], "simulated lookup failure")
	assert.Len(t, pendingJournal(t, store), 1, "undecided: kept for the next pass")

	hooked.allRowsErr = nil
	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesDeleted, "%+v", res)
	assert.False(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store))
}

// A normal pass leaves no journal behind, and never journals a file its own
// bound preview said stays: a later replay has no preview to consult, so a
// record for such a file would remove what the owner was told would stay.
func TestRunPurge_JournalIsClearedAfterAPassAndNeverHoldsAFileThePreviewKept(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	listed := filepath.Join(base, "lib", "Author", "listed.m4b")
	unlisted := filepath.Join(base, "lib", "Author", "unlisted.m4b")
	writeFile(t, listed)
	writeFile(t, unlisted)
	softDeleted(t, store, "A", listed)
	softDeleted(t, store, "B", unlisted)
	_, err := store.CreateBook(&database.Book{ID: "L", Title: "L", FilePath: unlisted})
	require.NoError(t, err)

	pv := previewOnce(t, svc, nil)
	require.Equal(t, []string{listed}, pv.FilesToDelete, "a live book names the other file")

	// The live book goes away after the preview: the file is nobody's now,
	// but the preview did not list it.
	require.NoError(t, store.DeleteBook("L"))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true, TrashedBefore: pv.TrashedBefore})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Purged, "%+v", res)
	assert.Equal(t, 1, res.FilesDeleted, "%+v", res)
	assert.False(t, fileExists(listed))
	assert.True(t, fileExists(unlisted), "the preview said it stays")
	assert.Empty(t, pendingJournal(t, store), "nothing is left to replay: the unlisted file was never journaled")
}
