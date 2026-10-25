// file: internal/batch/hard_delete_owned_files_test.go
// version: 1.0.1
// guid: 5b8e2d71-3c94-4a06-9f1d-7e2a60c4b839
// last-edited: 2026-10-25

package batch

import (
	"path/filepath"
	"strings"
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

// gainsFileStore adds a book_file row to bookID right after the FIRST
// GetBookFiles of it returns: a file that lands after the batch's first check
// and before the merge lock the carry runs under.
type gainsFileStore struct {
	*database.PebbleStore
	t      *testing.T
	bookID string

	mu    sync.Mutex
	calls int
}

func (g *gainsFileStore) GetBookFiles(id string) ([]database.BookFile, error) {
	rows, err := g.PebbleStore.GetBookFiles(id)
	if id == g.bookID {
		g.mu.Lock()
		g.calls++
		first := g.calls == 1
		g.mu.Unlock()
		if first {
			require.NoError(g.t, g.PebbleStore.CreateBookFile(&database.BookFile{
				ID: ulid.Make().String(), BookID: id, FilePath: "/tmp/owned/gained.m4b", Format: "m4b",
			}))
		}
	}
	return rows, err
}

// 3: a file gained after the first check is caught by the check under the
// merge lock, before the carry moves anything.
func TestExecuteOperations_HardDeleteCatchesFileGainedBeforeTheLock(t *testing.T) {
	f := newOwnedFilesFixture(t)
	f.book(t, "keep", true, "g1")
	f.book(t, "gains-file", false, "g1")
	f.state(t, "gains-file")
	store := &gainsFileStore{PebbleStore: f.s, t: t, bookID: "gains-file"}

	resp := NewBatchService(store).ExecuteOperations(hardDeleteOps("gains-file"))

	require.Equal(t, 0, resp.Success, "%+v", resp.Results)
	require.Equal(t, 1, resp.Failed)
	r := resultFor(t, resp, "gains-file")
	require.Contains(t, r.Error, database.ErrBookOwnsFiles.Error())
	require.True(t, strings.HasPrefix(r.Error, "hard delete gains-file: "), "same wording as the first check, not the carry's wrapper: %s", r.Error)
	require.Contains(t, r.Error, "soft-delete it instead, or move its files to another book first")
	require.GreaterOrEqual(t, store.calls, 2, "the files were checked again under the merge lock")

	require.True(t, f.exists("gains-file"))
	src := f.stateOf(t, "gains-file")
	require.NotNil(t, src, "the source keeps its state")
	require.Equal(t, 40, src.ProgressPct)
	require.Nil(t, f.stateOf(t, "keep"), "nothing was carried to the target")
}
