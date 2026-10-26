// file: internal/audiobooks/delete_owned_files_lock_test.go
// version: 1.7.0
// guid: c41e7a92-6d05-4b38-8e1f-2a9d5b073c64
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// gainsFileStore adds a book_file row to bookID right after chosen reads of
// the book's files return. database.CheckBookOwnsNoFiles (every check: the
// hard delete's, the purge's up front and under the merge lock, the discard's
// two) reads GetBookFilesIncludingRetired, so afterInclRetCall counts those
// checks in order; afterFilesCall counts plain GetBookFiles reads, which no
// delete check makes any more. A trigger of 0 never fires; N fires after the
// Nth call.
type gainsFileStore struct {
	*database.PebbleStore
	t                  *testing.T
	bookID             string
	afterFilesCall     int
	afterInclRetCall   int
	mu                 sync.Mutex
	filesCalls, inclRC int
}

func (s *gainsFileStore) addFile(id string) {
	require.NoError(s.t, s.PebbleStore.CreateBookFile(&database.BookFile{
		BookID: id, FilePath: "/lib/Gotham/" + id + "-gained.m4b", Format: "m4b",
	}))
}

func (s *gainsFileStore) GetBookFiles(id string) ([]database.BookFile, error) {
	rows, err := s.PebbleStore.GetBookFiles(id)
	if id == s.bookID {
		s.mu.Lock()
		s.filesCalls++
		fire := s.filesCalls == s.afterFilesCall
		s.mu.Unlock()
		if fire {
			s.addFile(id)
		}
	}
	return rows, err
}

func (s *gainsFileStore) GetBookFilesIncludingRetired(id string) ([]database.BookFile, error) {
	rows, err := s.PebbleStore.GetBookFilesIncludingRetired(id)
	if id == s.bookID {
		s.mu.Lock()
		s.inclRC++
		fire := s.inclRC == s.afterInclRetCall
		s.mu.Unlock()
		if fire {
			s.addFile(id)
		}
	}
	return rows, err
}

// retireRowInto gives holderID one retired book_file row (re-keyed from a
// shell book), so the holder owns only a retired row.
func retireRowInto(t *testing.T, store *database.PebbleStore, holderID string) {
	t.Helper()
	shell := "shell-" + holderID
	_, err := store.CreateBook(&database.Book{ID: shell, Title: "The Dark Knight Returns", Format: "m4b"})
	require.NoError(t, err)
	row := &database.BookFile{ID: "retired-row-" + holderID, BookID: shell, FilePath: "/lib/Gotham/" + holderID + "-retired.m4b", Format: "m4b"}
	require.NoError(t, store.CreateBookFile(row))
	v, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell, RetireOpID: "op-1", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	require.NoError(t, store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell}}, holderID, v.ID))
}

// trashBook soft-deletes an existing book the way a trashed fixture is made:
// RetireBookFiles refuses a soft-deleted holder, so a trashed holder of only
// retired rows is built live and trashed afterwards.
func trashBook(t *testing.T, store *database.PebbleStore, id string) {
	t.Helper()
	yes := true
	now := time.Now().Add(-time.Hour)
	_, err := store.ModifyBook(id, func(b *database.Book) error {
		b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
		b.LibraryState = new("deleted")
		return nil
	})
	require.NoError(t, err)
}

// A file row that lands after the up-front check refuses the hard delete in
// the precheck under the merge lock, BEFORE the carry moves any state and
// before the hash is blocked. Without the re-check the progress moved to the
// listed copy and only then did DeleteBook refuse: the book stayed, its
// progress did not. The refusal is in exactly the words of the up-front check.
func TestDeleteAudiobook_HardDeleteFileGainedBeforeTheLockMovesNothing(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "copy", "g1", false, false)
	hash := "h-gains-file"
	_, err := store.ModifyBook("copy", func(b *database.Book) error { b.FileHash = &hash; return nil })
	require.NoError(t, err)
	u := purgeSeedProgress(t, store, "copy")
	wrapped := &gainsFileStore{PebbleStore: store, t: t, bookID: "copy", afterInclRetCall: 1}
	svc := NewAudiobookService(wrapped)

	res, err := svc.DeleteAudiobook(context.Background(), "copy", &DeleteAudiobookOptions{BlockHash: true})

	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.NotErrorIs(t, err, merge.ErrStateMovedBookKept)
	require.Nil(t, res)
	require.EqualError(t, err, "hard delete copy: book still owns book_file rows (1 row(s)); soft-delete it instead, or move its files to another book first")
	require.GreaterOrEqual(t, wrapped.inclRC, 2, "the files were checked again under the merge lock")
	require.False(t, bookGone(t, store, "copy"))
	require.Equal(t, 40, userPct(t, store, u.ID, "copy"), "the source keeps its progress")
	require.Zero(t, userPct(t, store, u.ID, "keep"), "nothing was carried to the listed copy")
	blocked, err := store.IsHashBlocked(hash)
	require.NoError(t, err)
	require.False(t, blocked, "a refused delete blocks no hash")
}

// The no-state branch of the merge helper takes the same precheck under the
// lock: a book with no listening state, BlockHash on, and a file that lands
// before the lock is refused BEFORE the hash is blocked. (Without the
// precheck the hash is blocked, inside del, and DeleteBook then refuses.)
func TestDeleteAudiobook_HardDeleteWithoutStateFileGainedBeforeTheLockBlocksNoHash(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	hash := "h-stateless"
	_, err := store.CreateBook(&database.Book{ID: "plain", Title: "Hush", Format: "m4b", FileHash: &hash})
	require.NoError(t, err)
	wrapped := &gainsFileStore{PebbleStore: store, t: t, bookID: "plain", afterInclRetCall: 1}
	svc := NewAudiobookService(wrapped)

	res, err := svc.DeleteAudiobook(context.Background(), "plain", &DeleteAudiobookOptions{BlockHash: true})

	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Nil(t, res)
	require.EqualError(t, err, "hard delete plain: book still owns book_file rows (1 row(s)); soft-delete it instead, or move its files to another book first")
	require.GreaterOrEqual(t, wrapped.inclRC, 2, "the files were checked again under the merge lock")
	require.False(t, bookGone(t, store, "plain"))
	blocked, err := store.IsHashBlocked(hash)
	require.NoError(t, err)
	require.False(t, blocked, "the hash is not blocked for a delete that was refused")
}

// A file that lands after even the check under the merge lock (a writer that
// is not a merge does not take it) is refused by DeleteBook AFTER the carry:
// the error says the state was moved, is told apart from "nothing deleted"
// (merge.ErrStateMovedBookKept), and the state is on the listed copy.
func TestDeleteAudiobook_HardDeleteLateRefusalAfterCarrySaysStateMoved(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "copy", "g1", false, false)
	u := purgeSeedProgress(t, store, "copy")
	wrapped := &gainsFileStore{PebbleStore: store, t: t, bookID: "copy", afterInclRetCall: 2}
	svc := NewAudiobookService(wrapped)

	res, err := svc.DeleteAudiobook(context.Background(), "copy", &DeleteAudiobookOptions{})

	require.Nil(t, res)
	require.ErrorIs(t, err, merge.ErrStateMovedBookKept)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "after its state moved to keep")
	require.Equal(t, 1, countOccurrences(err.Error(), "hard delete copy"), "no stacked prefixes: %s", err)
	require.False(t, bookGone(t, store, "copy"), "the book is kept")
	require.Equal(t, 40, userPct(t, store, u.ID, "keep"), "its state was moved before DeleteBook refused")
}

func countOccurrences(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}

// DeleteBook refuses a book holding only retired rows, but GetBookFiles does
// not list them: a check made with it let the carry run first. A holder with
// only a retired row and listening state is refused with nothing carried, in
// words that do not tell the user to move files that cannot be moved.
func TestDeleteAudiobook_HardDeleteOfBookWithOnlyRetiredRowsMovesNothing(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "holder", "g1", false, false)
	retireRowInto(t, store, "holder")
	u := purgeSeedProgress(t, store, "holder")
	live, err := store.GetBookFiles("holder")
	require.NoError(t, err)
	require.Empty(t, live, "the holder's only row is retired: GetBookFiles does not list it")

	res, err := NewAudiobookService(store).DeleteAudiobook(context.Background(), "holder", &DeleteAudiobookOptions{})

	require.Nil(t, res)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "retired")
	require.Contains(t, err.Error(), "cannot be moved")
	require.NotContains(t, err.Error(), "move its files to another book first")
	require.False(t, bookGone(t, store, "holder"))
	require.Equal(t, 40, userPct(t, store, u.ID, "holder"), "the source keeps its progress")
	require.Zero(t, userPct(t, store, u.ID, "keep"), "nothing was carried")
}

// S3: DELETE of a book in the trash ("Purge now", the nightly purge's path)
// carries its state to the listed copy under the merge lock. A file row that
// lands after the purge's first check (outside the lock) is caught by the
// check under the lock, BEFORE the carry: nothing moves, and the outcome is
// the plain owns-files refusal, not "state moved".
func TestPurgeNow_FileGainedBeforeTheLockMovesNothing(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	wrapped := &gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterInclRetCall: 1}
	svc := NewAudiobookService(wrapped)

	res, err := svc.DeleteAudiobook(context.Background(), "dup", &DeleteAudiobookOptions{})

	require.Nil(t, res)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.NotErrorIs(t, err, merge.ErrStateMovedBookKept)
	require.Contains(t, err.Error(), "nothing moved")
	require.False(t, bookGone(t, store, "dup"), "the book stays in the trash")
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"), "the source keeps its progress")
	require.Zero(t, userPct(t, store, u.ID, "keep"), "nothing was carried to the listed copy")
	require.GreaterOrEqual(t, wrapped.inclRC, 2, "the files were checked up front and again under the merge lock")
}

// The nightly purge shares the path: the same race leaves the state where it
// was and counts the book as skipped for owning files.
func TestPurgeSoftDeletedBooks_FileGainedBeforeTheLockMovesNothing(t *testing.T) {
	svc0, store, _ := setupPurgeBoundary(t)
	_ = svc0
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterInclRetCall: 1})

	res, err := svc.PurgeSoftDeletedUnattended(context.Background(), false, nil)

	require.NoError(t, err)
	require.Equal(t, []string{"dup"}, res.SkippedOwnsFilesIDs, "result %+v", res)
	require.Zero(t, res.CarriedToSibling)
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"))
	require.Zero(t, userPct(t, store, u.ID, "keep"))
}

// A file that lands after the purge's check under the lock is refused by
// DeleteBook after the carry: "Purge now" reports it as ErrStateMovedBookKept
// (the handler's STATE_MOVED_BOOK_KEPT), not as a refusal that changed nothing.
func TestPurgeNow_LateRefusalAfterCarrySaysStateMoved(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterInclRetCall: 2})

	res, err := svc.DeleteAudiobook(context.Background(), "dup", &DeleteAudiobookOptions{})

	require.Nil(t, res)
	require.ErrorIs(t, err, merge.ErrStateMovedBookKept)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "after its state moved to keep")
	require.False(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "keep"), "the state moved before DeleteBook refused")
}

// N1: a delete that fails after the carry for a reason other than a file row
// (here the tombstone write) also moved the state, so "Purge now" reports it
// as ErrStateMovedBookKept too (the handler answers 500 STATE_MOVED_BOOK_KEPT:
// not owns-files, so not 409).
type tombstoneFailsStore struct{ *database.PebbleStore }

func (s tombstoneFailsStore) CreateBookTombstone(*database.Book) error {
	return errors.New("synthetic tombstone failure")
}

func TestPurgeNow_NonFileFailureAfterCarryAlsoSaysStateMoved(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(tombstoneFailsStore{store})

	res, err := svc.DeleteAudiobook(context.Background(), "dup", &DeleteAudiobookOptions{})

	require.Nil(t, res)
	require.ErrorIs(t, err, merge.ErrStateMovedBookKept)
	require.NotErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "synthetic tombstone failure")
	require.False(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "keep"), "the state moved before the delete failed")
}

// N3/N4: the late-refusal text names the cause once, and the nightly purge
// counts a book refused after the carry once (an Errors line, not also
// "skipped: still owns book_file rows").
func TestPurgeSoftDeletedBooks_LateRefusalAfterCarryIsCountedOnce(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterInclRetCall: 2})

	res, err := svc.PurgeSoftDeletedUnattended(context.Background(), false, nil)

	require.NoError(t, err)
	require.Zero(t, res.SkippedOwnsFiles, "result %+v", res)
	require.Len(t, res.Errors, 1, "result %+v", res)
	require.Contains(t, res.Errors[0], "listening state moved to keep")
}

// N5: a nightly run whose only change was a carry followed by a refused delete
// purges nothing, but the sibling's progress moved, so cached lists are
// dropped just as they are after "Purge now".
func TestPurgeSoftDeletedBooks_LateRefusalAfterCarryInvalidatesTheListCache(t *testing.T) {
	prev := config.AppConfig.CacheInvalidateOnBookUpdate
	config.AppConfig.CacheInvalidateOnBookUpdate = true
	t.Cleanup(func() { config.AppConfig.CacheInvalidateOnBookUpdate = prev })

	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterInclRetCall: 2})
	svc.listCache.Set("probe", []database.Book{{ID: "cached"}})
	_, warm := svc.listCache.Get("probe")
	require.True(t, warm, "fixture: the probe entry is cached")

	res, err := svc.PurgeSoftDeletedUnattended(context.Background(), false, nil)

	require.NoError(t, err)
	require.Zero(t, res.Purged, "the delete refused, so nothing was purged: %+v", res)
	_, still := svc.listCache.Get("probe")
	require.False(t, still, "state moved to the sibling, so cached lists are dropped")
}

// The file-row refusal under the lock (nothing moved) is counted once too:
// skipped, with no Errors line.
func TestPurgeSoftDeletedBooks_RefusalUnderTheLockIsCountedOnce(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterInclRetCall: 1})

	res, err := svc.PurgeSoftDeletedUnattended(context.Background(), false, nil)

	require.NoError(t, err)
	require.Equal(t, 1, res.SkippedOwnsFiles, "result %+v", res)
	require.Empty(t, res.Errors, "result %+v", res)
}

// R2: a trashed book holding only retired rows (DeleteBook refuses it,
// GetBookFiles does not list the rows) with NO listening state: the nightly
// purge refuses it up front, before the tombstone snapshot, the external-ID
// tombstones or any delete, and counts it as skipped.
func TestPurgeSoftDeletedBooks_RetiredOnlyStatelessBookIsRefusedBeforeAnySideEffect(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "holder", "g1", false, false)
	retireRowInto(t, store, "holder")
	trashBook(t, store, "holder")
	live, err := store.GetBookFiles("holder")
	require.NoError(t, err)
	require.Empty(t, live, "the holder's only row is retired: GetBookFiles does not list it")
	svc := NewAudiobookService(store)

	res, err := svc.PurgeSoftDeletedUnattended(context.Background(), false, nil)

	require.NoError(t, err)
	require.Equal(t, []string{"holder"}, res.SkippedOwnsFilesIDs, "result %+v", res)
	require.Zero(t, res.Purged)
	require.Empty(t, res.Errors)
	require.False(t, bookGone(t, store, "holder"))
	tomb, err := store.GetBookTombstone("holder")
	require.NoError(t, err)
	require.Nil(t, tomb, "no tombstone snapshot was written for a book that was kept")
}

// S1: "Purge now" of a trashed book holding only retired rows says what the
// helper says (retired rows cannot be moved), once, and never advises
// soft-deleting a book that is already in the trash.
func TestPurgeNow_RetiredOnlyBookKeepsTheHelpersWording(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "holder", "g1", false, false)
	retireRowInto(t, store, "holder")
	trashBook(t, store, "holder")
	book, err := store.GetBookByID("holder")
	require.NoError(t, err)

	_, err = purgeNowUnderHold(t, NewAudiobookService(store), book)

	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "retired row(s)")
	require.Contains(t, err.Error(), "cannot be moved")
	require.NotContains(t, err.Error(), "soft-delete")
	require.Equal(t, 1, strings.Count(err.Error(), "still owns book_file rows"), "%v", err)
	require.False(t, bookGone(t, store, "holder"))
}

// R2: discard-progress-and-purge of a trashed book holding only retired rows
// is refused before any user's progress is erased, in the helper's wording for
// retired rows.
func TestDiscardProgressAndPurge_RetiredOnlyBookIsRefusedBeforeAnythingIsCleared(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	svc.auditOverride = &fakeRecorder{}
	groupBook(t, store, "holder", "g1", false, false)
	retireRowInto(t, store, "holder")
	trashBook(t, store, "holder")
	u := purgeSeedProgress(t, store, "holder")
	live, err := store.GetBookFiles("holder")
	require.NoError(t, err)
	require.Empty(t, live, "the holder's only row is retired: GetBookFiles does not list it")

	_, err = svc.DiscardProgressAndPurge(context.Background(), "holder", "owner")

	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "retired")
	require.Contains(t, err.Error(), "cannot be moved")
	require.False(t, bookGone(t, store, "holder"))
	require.Equal(t, 40, userPct(t, store, u.ID, "holder"), "no progress was erased")
	tomb, err := store.GetBookTombstone("holder")
	require.NoError(t, err)
	require.Nil(t, tomb)
}

// S4: a file row that lands AFTER both discard checks (the up-front one and
// the one under the merge lock) is refused by DeleteBook only once every
// user's progress has been erased. That is not "nothing changed": the book is
// kept without its progress, so the service writes the audit row, reports
// ErrDiscardedBookKept (which still wraps ErrBookOwnsFiles) and the handler
// maps it to its own code.
func TestDiscardProgressAndPurge_FileRowAfterClearingIsAuditedAsKept(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	u := purgeSeedProgress(t, store, "held")
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "held", afterInclRetCall: 2})
	rec := &fakeRecorder{}
	svc.auditOverride = rec

	_, err := svc.DiscardProgressAndPurge(context.Background(), "held", "owner")
	require.ErrorIs(t, err, merge.ErrDiscardedBookKept)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.False(t, bookGone(t, store, "held"), "the delete refused, so the book stays")
	require.Equal(t, 0, userPct(t, store, u.ID, "held"), "the progress was erased before the refusal")

	require.Len(t, rec.entries, 1, "the erase is audited even though the purge failed")
	e := rec.entries[0]
	require.Equal(t, activity.TypeTrashDiscardProgressPurge, e.Type)
	require.Equal(t, "error", e.Level)
	require.Equal(t, false, e.Details["purged"])
	require.Equal(t, 1, e.Details["users_cleared"])
	require.Contains(t, e.Details["error"], "was erased and the book was kept")
}

// A state row holding only a progress-reset marker counts no user, yet the
// discard deletes it, so a delete refused afterwards is audited as an erase,
// and the audit row carries the record count (a row with nothing in it at all
// counts the same: the safer side of "was anything erased").
func TestDiscardProgressAndPurge_ErasedMarkerRowIsAuditedWithItsRecordCount(t *testing.T) {
	_, store, _ := setupPurgeBoundary(t)
	softDeleted(t, store, "held", "")
	u, err := store.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	resetAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: "held", ProgressResetAt: &resetAt}))
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "held", afterInclRetCall: 2})
	rec := &fakeRecorder{}
	svc.auditOverride = rec

	_, err = svc.DiscardProgressAndPurge(context.Background(), "held", "owner")

	require.ErrorIs(t, err, merge.ErrDiscardedBookKept)
	require.Len(t, rec.entries, 1, "the erased marker row is audited")
	e := rec.entries[0]
	require.Equal(t, 0, e.Details["users_cleared"])
	require.Equal(t, 0, e.Details["bookmarks_cleared"])
	require.Equal(t, 1, e.Details["records_cleared"])
	require.Contains(t, e.Summary, "1 stored record(s)")
}

// N6: the state-moved refusal of "Purge now" returns before the success arms'
// cache refresh, but the sibling's progress did change, so the list cache is
// invalidated on that path too.
func TestPurgeNow_StateMovedRefusalInvalidatesTheListCache(t *testing.T) {
	prev := config.AppConfig.CacheInvalidateOnBookUpdate
	config.AppConfig.CacheInvalidateOnBookUpdate = true
	t.Cleanup(func() { config.AppConfig.CacheInvalidateOnBookUpdate = prev })

	_, store, _ := setupPurgeBoundary(t)
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterInclRetCall: 2})
	svc.listCache.Set("probe", []database.Book{{ID: "cached"}})
	_, warm := svc.listCache.Get("probe")
	require.True(t, warm, "fixture: the probe entry is cached")

	_, err := svc.DeleteAudiobook(context.Background(), "dup", &DeleteAudiobookOptions{})
	require.ErrorIs(t, err, merge.ErrStateMovedBookKept)

	_, still := svc.listCache.Get("probe")
	require.False(t, still, "the refusal moved state, so cached lists are dropped")
}
