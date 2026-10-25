// file: internal/audiobooks/purge_pending_journal_test.go
// version: 1.3.0
// guid: 2e8a4c71-6d09-4b35-9f1a-c7d3b05e8a62
// last-edited: 2026-10-25

package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	// liveErr, when it returns non-nil, fails LiveBookIDsAtPath with that error.
	liveErr func(path string) error
}

func (h *hookedPathStore) LiveBookIDsAtPath(path string) ([]string, error) {
	if h.beforeLive != nil {
		h.beforeLive(path)
	}
	if h.liveErr != nil {
		if err := h.liveErr(path); err != nil {
			return nil, err
		}
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

// opaqueStore embeds the Store interface, so the all-rows path lookup (a
// capability, not a Store method) is hidden behind it, and it does not opt in to
// database.StoreUnwrapper. unwrappingStore is the same wrapper that does.
type opaqueStore struct{ database.Store }

type unwrappingStore struct{ opaqueStore }

func (u unwrappingStore) Unwrap() database.Store { return u.Store }

// The all-rows lookup is not on Store, so a store that cannot answer it must
// make the removal fail closed: the file stays, and the line says why.
func TestRemoveDeferredFiles_StoreWithoutAllRowsLookupKeepsTheFile(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "nocap.m4b")
	writeFile(t, x)

	svc := NewAudiobookService(opaqueStore{store})
	files := &pendingFiles{}
	files.add("purged", purgeFileVerdict{remove: x, id: identityAt(t, x)})
	removed, errs := svc.removeDeferredFiles(files, nil)

	assert.Zero(t, removed)
	assert.True(t, fileExists(x), "a path that cannot be vouched for is not a free path")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "BookRowPathReader")
}

// A decorator that opts in through StoreUnwrapper keeps the lookup reachable,
// so the same removal goes through when no book names the path.
func TestRemoveDeferredFiles_UnwrappingDecoratorStillReachesAllRowsLookup(t *testing.T) {
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "viawrap.m4b")
	writeFile(t, x)

	svc := NewAudiobookService(unwrappingStore{opaqueStore{store}})
	files := &pendingFiles{}
	files.add("purged", purgeFileVerdict{remove: x, id: identityAt(t, x)})
	removed, errs := svc.removeDeferredFiles(files, nil)

	assert.Empty(t, errs)
	assert.Equal(t, 1, removed)
	assert.False(t, fileExists(x))
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
	_, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "race.m4b")
	writeFile(t, x)
	_, err := store.CreateBook(&database.Book{ID: "L", Title: "L", FilePath: x})
	require.NoError(t, err)

	hooked := &hookedPathStore{PebbleStore: store}
	svc := NewAudiobookService(hooked)
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
	files.add("purged", purgeFileVerdict{remove: x, id: identityAt(t, x)})
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
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
	assert.False(t, fileExists(free), "nothing names it: the removal the dead pass decided on is done")
	assert.True(t, fileExists(taken), "a live book names it now: the replay runs the same checks")
	assert.Zero(t, res.FilesDeleted, "no book of THIS pass owned a file: the replayed removal is counted apart")
	require.Len(t, res.Errors, 1, "%+v", res)
	assert.Contains(t, res.Errors[0], "T2")
	assert.Contains(t, res.Errors[0], earlierPassLabel, "the line is about a book an earlier pass purged")
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
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
	assert.False(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store))

	// And the same for a pass sent with no trashed_before at all.
	writeFile(t, x)
	softDeleted(t, store, "T2", x)
	crashAfterRowAndTombstone(t, restarted, store, "T2")
	res, err = restarted.RunPurge(context.Background(), PurgeOptions{DeleteFiles: true})
	require.ErrorIs(t, err, ErrPurgePreviewRequired)
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
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
	assert.Zero(t, res.FilesReplayed)
	assert.True(t, fileExists(x))
	assert.Len(t, pendingJournal(t, store), 1)
	// What it does show: the file an earlier pass left waiting, which a purge
	// with delete_files would remove on top of the preview's own list.
	assert.Equal(t, 1, res.FilesPendingFromEarlierPass, "%+v", res)
	assert.Contains(t, res.Summary(), "an earlier pass left waiting")
}

// SR1. A purge run with "also delete files" off never removes a file from disk,
// whatever an earlier pass left in the journal. The records stay for a pass that
// does delete files.
func TestRunPurge_PassWithoutDeleteFilesNeverReplaysThePendingRemovals(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "pending.m4b")
	writeFile(t, x)
	softDeleted(t, store, "T", x)
	crashAfterRowAndTombstone(t, svc, store, "T")

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: false})
	require.NoError(t, err)
	assert.Zero(t, res.FilesReplayed, "%+v", res)
	assert.Zero(t, res.FilesDeleted, "%+v", res)
	assert.True(t, fileExists(x), "delete_files is off: nothing is removed from disk")
	assert.Empty(t, res.Errors, "%+v", res)
	assert.Len(t, pendingJournal(t, store), 1, "the record waits for a pass that deletes files")

	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
	assert.Zero(t, res.FilesDeleted, "%+v", res)
	assert.False(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store))
}

// SR2. The journal records the file as the pass saw it. A file that is not that
// one when the replay comes (replaced by another with the same name, or changed
// since) stays, and the record is cleared: nobody decided to remove it.
func TestRunPurge_ReplayKeepsAFileThatIsNotTheOneThePassDecidedOn(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, path string){
		"replaced by a file of another size": func(t *testing.T, path string) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.WriteFile(path, []byte("a different, longer recording"), 0o644))
		},
		"touched since": func(t *testing.T, path string) {
			later := time.Now().Add(time.Hour)
			require.NoError(t, os.Chtimes(path, later, later))
		},
		"replaced by a directory": func(t *testing.T, path string) {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0o755))
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc, store, base := setupPurgeBoundary(t)
			x := filepath.Join(base, "lib", "Author", "samename.m4b")
			writeFile(t, x)
			softDeleted(t, store, "T", x)
			crashAfterRowAndTombstone(t, svc, store, "T")
			change(t, x)

			res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
			require.NoError(t, err)
			assert.Zero(t, res.FilesReplayed, "%+v", res)
			_, statErr := os.Stat(x)
			assert.NoError(t, statErr, "it is not the file the pass decided on: it stays")
			require.Len(t, res.Errors, 1, "%+v", res)
			assert.Contains(t, res.Errors[0], "not the file this purge decided to remove")
			assert.Contains(t, res.Errors[0], earlierPassLabel)
			assert.Empty(t, pendingJournal(t, store), "decided for good: the record is cleared")
		})
	}
}

// A journal record with no identity (one a build that predates it wrote, or a
// damaged one) cannot be vouched for: its file is kept.
func TestRunPurge_ReplayKeepsTheFileOfARecordWithNoIdentity(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	x := filepath.Join(base, "lib", "Author", "noid.m4b")
	writeFile(t, x)
	raw, err := json.Marshal(pendingFileRecord{BookID: "T", Path: x, RecordedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.NoError(t, store.SetRaw(pendingFilePrefix+"T", raw))

	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Zero(t, res.FilesReplayed, "%+v", res)
	assert.True(t, fileExists(x))
	assert.Empty(t, pendingJournal(t, store))
}

// fileIdentity compares inode, size and modification time, and an
// unknown identity matches nothing.
func TestFileIdentity_Matches(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.m4b")
	b := filepath.Join(dir, "b.m4b")
	for _, p := range []string{a, b} {
		require.NoError(t, os.WriteFile(p, []byte("same bytes"), 0o644))
	}
	when := time.Unix(1_700_000_000, 0)
	require.NoError(t, os.Chtimes(a, when, when))
	require.NoError(t, os.Chtimes(b, when, when))
	ia, ib := statIdentity(t, a), statIdentity(t, b)

	assert.True(t, ia.matches(ia, false))
	if ia.HasInode {
		assert.False(t, ia.matches(ib, false), "same size and mtime, another file: the inode tells them apart")
	}
	later := ia
	later.MtimeNS++
	assert.False(t, ia.matches(later, false), "another mtime")
	longer := ia
	longer.Size++
	assert.False(t, ia.matches(longer, false), "another size")
	assert.False(t, fileIdentity{}.matches(fileIdentity{}, false), "unknown matches nothing")
	assert.False(t, ia.matches(fileIdentity{}, false))

	d1, d2 := filepath.Join(dir, "d1"), filepath.Join(dir, "d2")
	require.NoError(t, os.Mkdir(d1, 0o755))
	require.NoError(t, os.Mkdir(d2, 0o755))
	id1 := statIdentity(t, d1)
	grown := id1
	grown.Size += 4096
	grown.MtimeNS++
	assert.True(t, id1.matches(grown, true), "a directory is told by its inode, not by size or mtime")
	if id1.HasInode {
		assert.False(t, id1.matches(statIdentity(t, d2), true))
	}
}

// SR4. The journal holds only paths the pass's bound preview listed. The pass
// here dies after purging its books and before its end-of-pass removal, so the
// journal is all there is; the next pass has no preview. A path the preview said
// stays was never journaled, so it is still there afterwards, even though by
// then nothing names it.
func TestRunPurge_CrashedBoundPassJournalsOnlyThePathsItsPreviewListed(t *testing.T) {
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
	require.Equal(t, []string{listed}, pv.FilesToDelete, "a live book names the other file, so the preview keeps it")
	require.Equal(t, 2, pv.Purgeable, "both books are purgeable: only the second one's file stays")
	rec := svc.previewFor(*pv.TrashedBefore)
	require.NotNil(t, rec)

	// The live book goes away after the preview: the file is nobody's now.
	require.NoError(t, store.DeleteBook("L"))

	env, err := svc.newPurgeEnv()
	require.NoError(t, err)
	env.trashedBefore = pv.TrashedBefore
	env.preview = rec
	env.files = &pendingFiles{preview: rec}
	for _, id := range []string{"A", "B"} {
		row, gErr := store.GetBookByID(id)
		require.NoError(t, gErr)
		require.NotNil(t, row)
		out := svc.purgeOne(row, true, env)
		require.Equal(t, purgePurged, out.kind, "%s: %+v", id, out)
	}
	// The pass dies here: removeDeferredFiles never runs.

	pairs := pendingJournal(t, store)
	require.Len(t, pairs, 1, "only the book whose file the preview listed is journaled")
	var only pendingFileRecord
	require.NoError(t, json.Unmarshal(pairs[0].Value, &only))
	assert.Equal(t, listed, only.Path)

	restarted := NewAudiobookService(store)
	res, err := restarted.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
	assert.False(t, fileExists(listed))
	assert.True(t, fileExists(unlisted), "the preview said it stays: it was never journaled, so no replay removes it")
}

// SR5. Only ENOENT means "gone". A stat that fails any other way says nothing
// about the file: it stays, the line is reported, and the record is kept for the
// next pass. A file that really is gone clears its record quietly.
func TestRunPurge_StatFailureOtherThanNotFoundKeepsTheRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the stat cannot be made to fail")
	}
	svc, store, base := setupPurgeBoundary(t)
	dir := filepath.Join(base, "lib", "Author")
	x := filepath.Join(dir, "unreadable.m4b")
	gone := filepath.Join(dir, "gone.m4b")
	writeFile(t, x)
	writeFile(t, gone)
	softDeleted(t, store, "T", x)
	softDeleted(t, store, "G", gone)
	crashAfterRowAndTombstone(t, svc, store, "T", "G")
	require.NoError(t, os.Remove(gone))

	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	res, err := svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Zero(t, res.FilesReplayed, "%+v", res)
	require.Len(t, res.Errors, 2, "both stats fail on an unreadable directory, ENOENT included: %+v", res)
	for _, line := range res.Errors {
		assert.Contains(t, line, "cannot verify")
		assert.Contains(t, line, earlierPassLabel)
	}
	assert.Len(t, pendingJournal(t, store), 2, "undecided: both records wait for the next pass")

	require.NoError(t, os.Chmod(dir, 0o755))
	res, err = svc.RunPurge(context.Background(), PurgeOptions{Unattended: true, DeleteFiles: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
	assert.False(t, fileExists(x))
	assert.Empty(t, res.Errors, "a file that is not there is fine: %+v", res)
	assert.Empty(t, pendingJournal(t, store))
}

// SR7. The removal pool decides many paths at once. Every file goes, the empty
// parents go with them (the one directory the workers share is never left
// empty), the lines come back in path order, and the race detector sees no
// shared write.
func TestRemoveDeferredFiles_ManyPathsInOneDirectoryAreAllRemoved(t *testing.T) {
	svc, store, base := setupPurgeBoundary(t)
	dir := filepath.Join(base, "lib", "Author", "Series")
	var ids []string
	files := &pendingFiles{}
	for i := range 40 {
		id := fmt.Sprintf("T%02d", i)
		p := filepath.Join(dir, fmt.Sprintf("part%02d.m4b", i))
		writeFile(t, p)
		softDeleted(t, store, id, p)
		ids = append(ids, id)
		files.add(id, purgeFileVerdict{remove: p, id: identityAt(t, p)})
	}
	// One of them is named by a live book, so it stays and says so.
	keep := filepath.Join(dir, "part07.m4b")
	_, err := store.CreateBook(&database.Book{ID: "L", Title: "L", FilePath: keep})
	require.NoError(t, err)
	for _, id := range ids {
		require.NoError(t, store.DeleteBook(id))
	}

	removed, errs := svc.removeDeferredFiles(files, nil)
	assert.Equal(t, 39, removed)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "T07")
	assert.True(t, fileExists(keep))
	left, rdErr := os.ReadDir(dir)
	require.NoError(t, rdErr)
	assert.Len(t, left, 1, "only the kept file is left")

	// And when every file goes, the directory goes too.
	require.NoError(t, store.DeleteBook("L"))
	files = &pendingFiles{}
	files.add("T07", purgeFileVerdict{remove: keep, id: identityAt(t, keep)})
	removed, errs = svc.removeDeferredFiles(files, nil)
	assert.Equal(t, 1, removed)
	assert.Empty(t, errs)
	assert.NoDirExists(t, dir, "the last removal leaves no empty parent behind")
}

func statIdentity(t *testing.T, path string) fileIdentity {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return identityOf(info)
}

// identityAt is the identity a pass would have recorded for the file at path.
func identityAt(t *testing.T, path string) fileIdentity { return statIdentity(t, path) }

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
	assert.Equal(t, 1, res.FilesReplayed, "%+v", res)
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
