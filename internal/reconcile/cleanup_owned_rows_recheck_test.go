// file: internal/reconcile/cleanup_owned_rows_recheck_test.go
// version: 1.0.0
// guid: 5f1c8a36-92d4-4b70-a3e8-6d07b4c91f25
// last-edited: 2026-10-29

package reconcile

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retireAfterFirstReadStore keys a retired row under bookID right after the
// first time its files are read including retired ones: the cleanup's own count
// has passed by then, and a retire (which takes the merge lock the removal runs
// under) lands before that lock. The row's path is NOT the duplicate's, so the
// path lookup that guards os.Remove cannot see it: only a second count of the
// duplicate's rows can.
type retireAfterFirstReadStore struct {
	*database.PebbleStore
	t      *testing.T
	bookID string

	mu    sync.Mutex
	calls int
}

func (r *retireAfterFirstReadStore) GetBookFilesIncludingRetired(id string) ([]database.BookFile, error) {
	rows, err := r.PebbleStore.GetBookFilesIncludingRetired(id)
	if id == r.bookID {
		r.mu.Lock()
		r.calls++
		first := r.calls == 1
		r.mu.Unlock()
		if first {
			r.retireOne()
		}
	}
	return rows, err
}

func (r *retireAfterFirstReadStore) retireOne() {
	r.t.Helper()
	if _, err := r.PebbleStore.CreateBook(&database.Book{ID: "shell", Title: "t", FilePath: "/elsewhere/shell"}); err != nil {
		r.t.Error(err)
		return
	}
	now := time.Now()
	v, err := r.PebbleStore.CreateBookVersion(&database.BookVersion{
		BookID: r.bookID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: "shell", RetireOpID: "op-invented", RetiredAt: &now,
	})
	if err != nil {
		r.t.Error(err)
		return
	}
	row := &database.BookFile{BookID: "shell", FilePath: "/elsewhere/shell/a.m4b"}
	if err := r.PebbleStore.CreateBookFile(row); err != nil {
		r.t.Error(err)
		return
	}
	if err := r.PebbleStore.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: "shell"}}, r.bookID, v.ID); err != nil {
		r.t.Error(err)
	}
}

// A duplicate that passes the cleanup's count of its rows and then has a
// retired version keyed under it before the merge lock is taken keeps its file
// and its book row. Without the count made again under the lock, os.Remove
// deleted the file first and DeleteBook then refused the book (it counts every
// row key), leaving a book whose retired version names audio that was gone.
func TestCleanupDuplicateVersionGroups_RowsGainedBeforeTheLockKeepTheDuplicateAndItsFile(t *testing.T) {
	f := newCleanupFixture(t)
	w := &retireAfterFirstReadStore{PebbleStore: f.store, t: t, bookID: "02"}

	res, err := CleanupDuplicateVersionGroups(w, f.root, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.calls < 2 {
		t.Fatalf("the duplicate's rows were read %d time(s), want at least 2: the count under the merge lock never ran", w.calls)
	}
	if res.DuplicatesRemoved != 0 || res.FilesDeleted != 0 || res.SkippedOwnsFiles != 1 || res.SkippedHoldsRetired != 1 || res.WriteErrors != 0 {
		t.Errorf("result = %+v, want nothing removed, one duplicate skipped as holding retired versions, no write error", res)
	}
	f.requireFile(true)
	f.requireBookGone("02", false)
}

// refuseDeleteStore refuses DeleteBook of bookID the way the store does for a
// book_file row that a writer which does not take the merge lock added after
// every check had passed.
type refuseDeleteStore struct {
	*database.PebbleStore
	bookID string
}

func (r *refuseDeleteStore) DeleteBook(id string) error {
	if id == r.bookID {
		return fmt.Errorf("delete book %s: %w (1 row(s))", id, database.ErrBookOwnsFiles)
	}
	return r.PebbleStore.DeleteBook(id)
}

// The row delete comes before the file removal: when DeleteBook still refuses
// after every check has passed, the duplicate's audio is still on disk. The old
// order removed the file first and then found the book could not be deleted.
func TestCleanupDuplicateVersionGroups_DeleteRefusedAfterTheChecksKeepsTheFile(t *testing.T) {
	f := newCleanupFixture(t)
	w := &refuseDeleteStore{PebbleStore: f.store, bookID: "02"}

	res, err := CleanupDuplicateVersionGroups(w, f.root, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.DuplicatesRemoved != 0 || res.FilesDeleted != 0 || res.SkippedOwnsFiles != 1 || res.WriteErrors != 0 {
		t.Errorf("result = %+v, want nothing removed and one duplicate skipped as owning files", res)
	}
	f.requireFile(true)
	f.requireBookGone("02", false)
}
