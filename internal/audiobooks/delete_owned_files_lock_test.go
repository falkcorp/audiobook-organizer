// file: internal/audiobooks/delete_owned_files_lock_test.go
// version: 1.1.0
// guid: c41e7a92-6d05-4b38-8e1f-2a9d5b073c64
// last-edited: 2026-10-25

package audiobooks

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// gainsFileStore adds a book_file row to bookID right after chosen reads of
// the book's files return. database.CheckBookOwnsNoFiles (the hard delete's
// checks, the purge's check under the merge lock) reads
// GetBookFilesIncludingRetired; the purge's first check, outside the lock,
// reads GetBookFiles. A trigger of 0 never fires; N fires after the Nth call.
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
		BookID: id, FilePath: "/x/" + id + "-gained.m4b", Format: "m4b",
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
	_, err := store.CreateBook(&database.Book{ID: shell, Title: shell, Format: "m4b"})
	require.NoError(t, err)
	row := &database.BookFile{ID: "retired-row-" + holderID, BookID: shell, FilePath: "/x/" + holderID + "-retired.m4b", Format: "m4b"}
	require.NoError(t, store.CreateBookFile(row))
	v, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell, RetireOpID: "op-1",
	})
	require.NoError(t, err)
	require.NoError(t, store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell}}, holderID, v.ID))
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
	_, err := store.CreateBook(&database.Book{ID: "plain", Title: "plain", Format: "m4b", FileHash: &hash})
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
	wrapped := &gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterFilesCall: 1}
	svc := NewAudiobookService(wrapped)

	res, err := svc.DeleteAudiobook(context.Background(), "dup", &DeleteAudiobookOptions{})

	require.Nil(t, res)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.NotErrorIs(t, err, merge.ErrStateMovedBookKept)
	require.Contains(t, err.Error(), "nothing moved")
	require.False(t, bookGone(t, store, "dup"), "the book stays in the trash")
	require.Equal(t, 40, userPct(t, store, u.ID, "dup"), "the source keeps its progress")
	require.Zero(t, userPct(t, store, u.ID, "keep"), "nothing was carried to the listed copy")
	require.GreaterOrEqual(t, wrapped.inclRC, 1, "the files were checked under the merge lock")
}

// The nightly purge shares the path: the same race leaves the state where it
// was and counts the book as skipped for owning files.
func TestPurgeSoftDeletedBooks_FileGainedBeforeTheLockMovesNothing(t *testing.T) {
	svc0, store, _ := setupPurgeBoundary(t)
	_ = svc0
	groupBook(t, store, "keep", "g1", false, true)
	groupBook(t, store, "dup", "g1", true, false)
	u := purgeSeedProgress(t, store, "dup")
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterFilesCall: 1})

	res, err := svc.PurgeSoftDeletedBooks(context.Background(), false, nil)

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
	svc := NewAudiobookService(&gainsFileStore{PebbleStore: store, t: t, bookID: "dup", afterInclRetCall: 1})

	res, err := svc.DeleteAudiobook(context.Background(), "dup", &DeleteAudiobookOptions{})

	require.Nil(t, res)
	require.ErrorIs(t, err, merge.ErrStateMovedBookKept)
	require.ErrorIs(t, err, database.ErrBookOwnsFiles)
	require.Contains(t, err.Error(), "listening state moved to keep")
	require.False(t, bookGone(t, store, "dup"))
	require.Equal(t, 40, userPct(t, store, u.ID, "keep"), "the state moved before DeleteBook refused")
}
