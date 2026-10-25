// file: internal/batch/hard_delete_owned_files_test.go
// version: 1.1.0
// guid: 5b8e2d71-3c94-4a06-9f1d-7e2a60c4b839
// last-edited: 2026-10-25

package batch

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ownedFilesFixture is a version group with a listed book ("keep") and the
// books the tests hard-delete, plus one reader with 40% progress on each
// book in withState.
type ownedFilesFixture struct {
	s    *database.PebbleStore
	user *database.User
}

func newOwnedFilesFixture(t *testing.T) *ownedFilesFixture {
	t.Helper()
	s, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	u, err := s.CreateUser("listener", "listener@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	return &ownedFilesFixture{s: s, user: u}
}

// book creates a book; listed makes it the organized primary Audiobookshelf
// lists, group puts it in a version group.
func (f *ownedFilesFixture) book(t *testing.T, id string, listed bool, group string) {
	t.Helper()
	b := &database.Book{ID: id, Title: id, Format: "m4b"}
	if group != "" {
		b.VersionGroupID = &group
	}
	if listed {
		b.IsPrimaryVersion, b.LibraryState = new(true), new("organized")
	} else {
		b.IsPrimaryVersion, b.LibraryState = new(false), new("imported")
	}
	_, err := f.s.CreateBook(b)
	require.NoError(t, err)
}

func (f *ownedFilesFixture) ownFile(t *testing.T, bookID string) {
	t.Helper()
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{
		ID: ulid.Make().String(), BookID: bookID, FilePath: "/tmp/owned/" + bookID + ".m4b", Format: "m4b",
	}))
}

func (f *ownedFilesFixture) state(t *testing.T, bookID string) {
	t.Helper()
	require.NoError(t, f.s.SetUserBookState(&database.UserBookState{
		UserID: f.user.ID, BookID: bookID, Status: database.UserBookStatusInProgress,
		ProgressPct: 40, LastActivityAt: time.Now(),
	}))
}

func (f *ownedFilesFixture) stateOf(t *testing.T, bookID string) *database.UserBookState {
	t.Helper()
	st, err := f.s.GetUserBookState(f.user.ID, bookID)
	require.NoError(t, err)
	return st
}

func (f *ownedFilesFixture) exists(bookID string) bool {
	b, _ := f.s.GetBookByID(bookID)
	return b != nil
}

func hardDeleteOps(ids ...string) *BatchOperationsRequest {
	req := &BatchOperationsRequest{}
	for _, id := range ids {
		req.Operations = append(req.Operations, BatchOperationItem{ID: id, Action: "delete", HardDelete: true})
	}
	return req
}

func resultFor(t *testing.T, resp *BatchResponse, id string) BatchResult {
	t.Helper()
	for _, r := range resp.Results {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no result for %s in %+v", id, resp.Results)
	return BatchResult{}
}

// 1: a batch hard delete of a book that owns files carries nothing and
// deletes nothing. Before 2026-10-25 the carry ran first, moved the book's
// progress to the listed version, and DeleteBook then refused: the book stayed
// and its progress had left it.
func TestExecuteOperations_HardDeleteOfBookOwningFilesLeavesStateUntouched(t *testing.T) {
	f := newOwnedFilesFixture(t)
	f.book(t, "keep", true, "g1")
	f.book(t, "has-files", false, "g1")
	f.ownFile(t, "has-files")
	f.state(t, "has-files")

	resp := NewBatchService(f.s).ExecuteOperations(hardDeleteOps("has-files"))

	require.Equal(t, 0, resp.Success, "a skipped book is not a success: %+v", resp.Results)
	require.Equal(t, 1, resp.Failed)
	require.Equal(t, 1, resp.Total)
	r := resultFor(t, resp, "has-files")
	require.False(t, r.Success)
	require.Contains(t, r.Error, database.ErrBookOwnsFiles.Error())
	require.Contains(t, r.Error, "soft-delete it instead, or move its files to another book first")

	require.True(t, f.exists("has-files"), "the book that owns files is kept")
	src := f.stateOf(t, "has-files")
	require.NotNil(t, src, "the source keeps its state")
	require.Equal(t, 40, src.ProgressPct)
	require.Nil(t, f.stateOf(t, "keep"), "nothing was carried to the target")
}

// 2: in a mixed batch the file-less books are deleted (carrying their state)
// and the books that own files are skipped one by one, each with its reason.
func TestExecuteOperations_HardDeleteMixedBatchSkipsOnlyBooksOwningFiles(t *testing.T) {
	f := newOwnedFilesFixture(t)
	f.book(t, "keep", true, "g1")
	f.book(t, "with-files-grouped", false, "g1")
	f.book(t, "with-files-alone", false, "")
	f.book(t, "bare-with-state", false, "g1")
	f.book(t, "bare-plain", false, "")
	for _, id := range []string{"with-files-grouped", "with-files-alone"} {
		f.ownFile(t, id)
		f.state(t, id)
	}
	f.state(t, "bare-with-state")

	resp := NewBatchService(f.s).ExecuteOperations(hardDeleteOps(
		"with-files-grouped", "bare-with-state", "with-files-alone", "bare-plain"))

	require.Equal(t, 2, resp.Success, "%+v", resp.Results)
	require.Equal(t, 2, resp.Failed)
	require.Equal(t, 4, resp.Total)
	for _, id := range []string{"with-files-grouped", "with-files-alone"} {
		r := resultFor(t, resp, id)
		require.False(t, r.Success, id)
		require.Contains(t, r.Error, database.ErrBookOwnsFiles.Error(), id)
		require.True(t, f.exists(id), "%s is kept", id)
		st := f.stateOf(t, id)
		require.NotNil(t, st, "%s keeps its state", id)
		require.Equal(t, 40, st.ProgressPct)
	}
	for _, id := range []string{"bare-with-state", "bare-plain"} {
		require.True(t, resultFor(t, resp, id).Success, id)
		require.False(t, f.exists(id), "%s is deleted", id)
	}
	moved := f.stateOf(t, "keep")
	require.NotNil(t, moved, "the deleted book's state moved to the listed version")
	require.Equal(t, 40, moved.ProgressPct)
}

// gainsFileStore adds a book_file row to bookID right after its gainOnCall-th
// read of the book's files returns (1 = after the batch's first check and
// before the merge lock the carry runs under; 2 = after the check under that
// lock). The files are read through GetBookFilesIncludingRetired, the reader
// database.CheckBookOwnsNoFiles uses.
type gainsFileStore struct {
	*database.PebbleStore
	t          *testing.T
	bookID     string
	gainOnCall int

	mu    sync.Mutex
	calls int
}

func (g *gainsFileStore) GetBookFilesIncludingRetired(id string) ([]database.BookFile, error) {
	rows, err := g.PebbleStore.GetBookFilesIncludingRetired(id)
	if id == g.bookID {
		g.mu.Lock()
		g.calls++
		gain := g.calls == g.gainOnCall
		g.mu.Unlock()
		if gain {
			require.NoError(g.t, g.PebbleStore.CreateBookFile(&database.BookFile{
				ID: ulid.Make().String(), BookID: id, FilePath: "/tmp/owned/gained.m4b", Format: "m4b",
			}))
		}
	}
	return rows, err
}

// 3: a file gained after the first check is caught by the check under the
// merge lock, before the carry moves anything, and reported in exactly the
// words of the first check (not wrapped in the merge package's text).
func TestExecuteOperations_HardDeleteCatchesFileGainedBeforeTheLock(t *testing.T) {
	f := newOwnedFilesFixture(t)
	f.book(t, "keep", true, "g1")
	f.book(t, "gains-file", false, "g1")
	f.state(t, "gains-file")
	store := &gainsFileStore{PebbleStore: f.s, t: t, bookID: "gains-file", gainOnCall: 1}

	resp := NewBatchService(store).ExecuteOperations(hardDeleteOps("gains-file"))

	require.Equal(t, 0, resp.Success, "%+v", resp.Results)
	require.Equal(t, 1, resp.Failed)
	r := resultFor(t, resp, "gains-file")
	require.Equal(t, "hard delete gains-file: book still owns book_file rows (1 row(s)); soft-delete it instead, or move its files to another book first", r.Error)
	require.GreaterOrEqual(t, store.calls, 2, "the files were checked again under the merge lock")

	require.True(t, f.exists("gains-file"))
	src := f.stateOf(t, "gains-file")
	require.NotNil(t, src, "the source keeps its state")
	require.Equal(t, 40, src.ProgressPct)
	require.Nil(t, f.stateOf(t, "keep"), "nothing was carried to the target")
}

// 3b: the same race for a book with no state to carry (the no-carry branch of
// the merge helper): refused under the lock, in the same words.
func TestExecuteOperations_HardDeleteWithoutStateCatchesFileGainedBeforeTheLock(t *testing.T) {
	f := newOwnedFilesFixture(t)
	f.book(t, "gains-file", false, "")
	store := &gainsFileStore{PebbleStore: f.s, t: t, bookID: "gains-file", gainOnCall: 1}

	resp := NewBatchService(store).ExecuteOperations(hardDeleteOps("gains-file"))

	require.Equal(t, 0, resp.Success, "%+v", resp.Results)
	require.Equal(t, "hard delete gains-file: book still owns book_file rows (1 row(s)); soft-delete it instead, or move its files to another book first",
		resultFor(t, resp, "gains-file").Error)
	require.GreaterOrEqual(t, store.calls, 2, "the files were checked again under the merge lock")
	require.True(t, f.exists("gains-file"))
}

// 3c: a file that lands after even the check under the lock (a writer that is
// not a merge does not take it) is refused by DeleteBook AFTER the carry. The
// item says the state was moved, and where, instead of reading like a clean
// refusal.
func TestExecuteOperations_HardDeleteLateRefusalAfterCarrySaysStateMoved(t *testing.T) {
	f := newOwnedFilesFixture(t)
	f.book(t, "keep", true, "g1")
	f.book(t, "late-file", false, "g1")
	f.state(t, "late-file")
	store := &gainsFileStore{PebbleStore: f.s, t: t, bookID: "late-file", gainOnCall: 2}

	resp := NewBatchService(store).ExecuteOperations(hardDeleteOps("late-file"))

	require.Equal(t, 0, resp.Success, "%+v", resp.Results)
	require.Equal(t, 1, resp.Failed)
	r := resultFor(t, resp, "late-file")
	require.Contains(t, r.Error, "after its state moved to keep")
	require.Contains(t, r.Error, database.ErrBookOwnsFiles.Error())
	require.True(t, f.exists("late-file"), "the book is kept")
	moved := f.stateOf(t, "keep")
	require.NotNil(t, moved, "its state was moved before DeleteBook refused")
	require.Equal(t, 40, moved.ProgressPct)
}

// retire gives holderID one retired book_file row (re-keyed from a shell book
// into it), so the holder owns only a retired row.
func (f *ownedFilesFixture) retire(t *testing.T, holderID string) {
	t.Helper()
	f.book(t, "shell-"+holderID, false, "")
	row := &database.BookFile{ID: ulid.Make().String(), BookID: "shell-" + holderID, FilePath: "/tmp/owned/" + holderID + "-retired.m4b", Format: "m4b"}
	require.NoError(t, f.s.CreateBookFile(row))
	v, err := f.s.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: "shell-" + holderID, RetireOpID: "op-1",
	})
	require.NoError(t, err)
	require.NoError(t, f.s.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: "shell-" + holderID}}, holderID, v.ID))
}

// 4: DeleteBook refuses a book holding only retired rows, but GetBookFiles
// does not list them, so a check made with it let the carry run first. A book
// with only a retired row and listening state is refused with nothing carried,
// in words that do not tell the user to move files that cannot be moved.
func TestExecuteOperations_HardDeleteOfBookWithOnlyRetiredRowsLeavesStateUntouched(t *testing.T) {
	f := newOwnedFilesFixture(t)
	f.book(t, "keep", true, "g1")
	f.book(t, "holder", false, "g1")
	f.retire(t, "holder")
	f.state(t, "holder")
	live, err := f.s.GetBookFiles("holder")
	require.NoError(t, err)
	require.Empty(t, live, "the holder's only row is retired: GetBookFiles does not list it")

	resp := NewBatchService(f.s).ExecuteOperations(hardDeleteOps("holder"))

	require.Equal(t, 0, resp.Success, "%+v", resp.Results)
	require.Equal(t, 1, resp.Failed)
	r := resultFor(t, resp, "holder")
	require.Contains(t, r.Error, database.ErrBookOwnsFiles.Error())
	require.Contains(t, r.Error, "retired")
	require.Contains(t, r.Error, "cannot be moved")
	require.NotContains(t, r.Error, "move its files to another book first")
	require.True(t, f.exists("holder"))
	src := f.stateOf(t, "holder")
	require.NotNil(t, src, "the source keeps its state")
	require.Equal(t, 40, src.ProgressPct)
	require.Nil(t, f.stateOf(t, "keep"), "nothing was carried to the target")
}
