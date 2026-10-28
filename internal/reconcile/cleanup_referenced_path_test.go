// file: internal/reconcile/cleanup_referenced_path_test.go
// version: 1.0.0
// guid: 7d3e0b9a-5c14-4f82-a6e7-2b9c41f8d035
// last-edited: 2026-10-28

package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// cleanupFixture is a version group of three copies: 01 (the oldest library copy, the
// copy kept), 02 (the duplicate under test, whose file is dupFile) and 03 (the
// original, outside the library, never a pruning candidate). Ids
// are invented. It runs against a REAL PebbleStore with memdb serving, because
// the path lookup under test (BookFilesAtPath, which counts retired rows) is
// the store's, not a fake's.
type cleanupFixture struct {
	t       *testing.T
	store   *database.PebbleStore
	root    string
	dupFile string
}

func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	ps, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("new pebble store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	root := t.TempDir()
	f := &cleanupFixture{t: t, store: ps, root: root, dupFile: filepath.Join(root, "shared", "b.m4b")}
	if err := os.MkdirAll(filepath.Dir(f.dupFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dupFile, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	vg := "vg-cleanup"
	for _, b := range []database.Book{
		{ID: "01", Title: "t", FilePath: filepath.Join(root, "a.m4b")},
		{ID: "02", Title: "t", FilePath: f.dupFile},
		{ID: "03", Title: "t", FilePath: "/src/c.m4b"}, // the original, outside the library
	} {
		b.VersionGroupID = &vg
		if _, err := ps.CreateBook(&b); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// retiredRowAtDupFile gives the path a retired row: a trashed shell's row,
// retired onto a holder book outside the group.
func (f *cleanupFixture) retiredRowAtDupFile() {
	f.t.Helper()
	for _, id := range []string{"holder", "shell"} {
		if _, err := f.store.CreateBook(&database.Book{ID: id, Title: id, FilePath: filepath.Join(f.root, id)}); err != nil {
			f.t.Fatal(err)
		}
	}
	now := time.Now()
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: "holder", Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: "shell", RetireOpID: "op-invented", RetiredAt: &now,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	row := &database.BookFile{BookID: "shell", FilePath: f.dupFile}
	if err := f.store.CreateBookFile(row); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: "shell"}}, "holder", v.ID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cleanupFixture) requireBookGone(id string, gone bool) {
	f.t.Helper()
	b, _ := f.store.GetBookByID(id)
	if gone && b != nil {
		f.t.Errorf("book %s still exists", id)
	}
	if !gone && b == nil {
		f.t.Errorf("book %s was removed", id)
	}
}

func (f *cleanupFixture) requireFile(present bool) {
	f.t.Helper()
	_, err := os.Stat(f.dupFile)
	if present && err != nil {
		f.t.Errorf("the duplicate's file is gone: %v", err)
	}
	if !present && err == nil {
		f.t.Error("the duplicate's file is still on disk")
	}
}

// S4 of the review of #4144: the duplicate cleanup removed the file and then the
// book row with no check that a book_file row names the path. A row that names
// it (a retired row keeps the path of the shell it was retired out of; another
// book's live row can too) owns that audio. The duplicate's row still goes, the
// file stays, and the result says so. The control is the same duplicate with no
// row at its path: its file is removed.
func TestCleanupDuplicateVersionGroups_FileANamedPathIsKept(t *testing.T) {
	t.Run("retired row at the path", func(t *testing.T) {
		f := newCleanupFixture(t)
		f.retiredRowAtDupFile()
		res, err := CleanupDuplicateVersionGroups(f.store, f.root, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.DuplicatesRemoved != 1 || res.FilesDeleted != 0 || res.FilesKeptReferenced != 1 || res.PathCheckErrors != 0 {
			t.Errorf("result = %+v, want DuplicatesRemoved=1 FilesDeleted=0 FilesKeptReferenced=1", res)
		}
		f.requireFile(true)
		f.requireBookGone("02", true)
	})
	t.Run("another book's live row at the path", func(t *testing.T) {
		f := newCleanupFixture(t)
		if _, err := f.store.CreateBook(&database.Book{ID: "other", Title: "other", FilePath: filepath.Join(f.root, "other")}); err != nil {
			t.Fatal(err)
		}
		if err := f.store.CreateBookFile(&database.BookFile{BookID: "other", FilePath: f.dupFile}); err != nil {
			t.Fatal(err)
		}
		res, err := CleanupDuplicateVersionGroups(f.store, f.root, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.DuplicatesRemoved != 1 || res.FilesDeleted != 0 || res.FilesKeptReferenced != 1 {
			t.Errorf("result = %+v, want DuplicatesRemoved=1 FilesDeleted=0 FilesKeptReferenced=1", res)
		}
		f.requireFile(true)
		f.requireBookGone("02", true)
	})
	t.Run("control: no row names the path", func(t *testing.T) {
		f := newCleanupFixture(t)
		res, err := CleanupDuplicateVersionGroups(f.store, f.root, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.DuplicatesRemoved != 1 || res.FilesDeleted != 1 || res.FilesKeptReferenced != 0 || res.PathCheckErrors != 0 {
			t.Errorf("result = %+v, want DuplicatesRemoved=1 FilesDeleted=1 FilesKeptReferenced=0", res)
		}
		f.requireFile(false)
		f.requireBookGone("02", true)
	})
}

// incompleteLookupStore is a store whose book_file path lookup is not complete
// (memdb warming up): BookFilesAtPath answers ErrBookFilesAtPathUnavailable.
type incompleteLookupStore struct {
	*database.PebbleStore
}

func (incompleteLookupStore) BookFilesAtPath(string) ([]database.BookFile, error) {
	return nil, database.ErrBookFilesAtPathUnavailable
}

// A lookup that cannot be answered is not "no row names it": the duplicate is
// kept with its file and counted, and a later pass asks again.
func TestCleanupDuplicateVersionGroups_UnanswerableLookupKeepsTheDuplicate(t *testing.T) {
	f := newCleanupFixture(t)
	s := incompleteLookupStore{f.store}
	if _, err := database.BookFileRowsAtPathStrict(s, f.dupFile); !errors.Is(err, database.ErrOwnershipLookupIncomplete) {
		t.Fatalf("setup: the wrapped lookup must be incomplete, got %v", err)
	}
	res, err := CleanupDuplicateVersionGroups(s, f.root, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.DuplicatesRemoved != 0 || res.FilesDeleted != 0 || res.PathCheckErrors != 1 {
		t.Errorf("result = %+v, want DuplicatesRemoved=0 FilesDeleted=0 PathCheckErrors=1", res)
	}
	f.requireFile(true)
	f.requireBookGone("02", false)
}

// writeStatefulDupFile puts audio at the stateful duplicate's path (the
// stateDupFixture duplicate is fileless on disk).
func writeStatefulDupFile(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, "Author", "dup.m4b")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The carry path (a duplicate a user has listening state on) takes the same
// check: the state still moves to the kept copy and the duplicate's row goes,
// but a file another book_file row names stays on disk.
func TestCleanupDuplicateVersionGroups_StatefulDuplicateKeepsAFileANamedPath(t *testing.T) {
	f, keep, dup, u := stateDupFixture(t, "organized")
	dupFile := writeStatefulDupFile(t, f.Root)
	if _, err := f.S.CreateBook(&database.Book{ID: "other", Title: "other", FilePath: filepath.Join(f.Root, "other")}); err != nil {
		t.Fatal(err)
	}
	if err := f.S.CreateBookFile(&database.BookFile{BookID: "other", FilePath: dupFile}); err != nil {
		t.Fatal(err)
	}

	res, err := CleanupDuplicateVersionGroups(f.S, f.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.StateCarried != 1 || res.DuplicatesRemoved != 1 || res.FilesDeleted != 0 || res.FilesKeptReferenced != 1 {
		t.Errorf("result = %+v, want StateCarried=1 DuplicatesRemoved=1 FilesDeleted=0 FilesKeptReferenced=1", res)
	}
	if _, err := os.Stat(dupFile); err != nil {
		t.Errorf("the file another row names was removed: %v", err)
	}
	if b, _ := f.S.GetBookByID(dup); b != nil {
		t.Error("the duplicate's book row was kept")
	}
	if st, _ := f.S.GetUserBookState(u.ID, keep); st == nil || st.ProgressPct != 35 {
		t.Errorf("the reader's state did not reach the kept copy: %+v", st)
	}
}

// With a lookup that cannot be answered the check refuses BEFORE the carry: the
// reader's state stays on the duplicate, nothing lands on the kept copy, and the
// duplicate and its file are kept for the next pass.
func TestCleanupDuplicateVersionGroups_StatefulDuplicateUnanswerableLookupCarriesNothing(t *testing.T) {
	f, keep, dup, u := stateDupFixture(t, "organized")
	dupFile := writeStatefulDupFile(t, f.Root)
	s := incompleteLookupStore{f.S}

	res, err := CleanupDuplicateVersionGroups(s, f.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.PathCheckErrors != 1 || res.StateCarried != 0 || res.DuplicatesRemoved != 0 || res.FilesDeleted != 0 {
		t.Errorf("result = %+v, want PathCheckErrors=1 StateCarried=0 DuplicatesRemoved=0 FilesDeleted=0", res)
	}
	if _, err := os.Stat(dupFile); err != nil {
		t.Errorf("the duplicate's file was removed: %v", err)
	}
	if b, _ := f.S.GetBookByID(dup); b == nil {
		t.Error("the duplicate was removed")
	}
	if st, _ := f.S.GetUserBookState(u.ID, dup); st == nil || st.ProgressPct != 35 {
		t.Errorf("the state left the duplicate: %+v", st)
	}
	if kst, _ := f.S.GetUserBookState(u.ID, keep); kst != nil {
		t.Errorf("state was carried onto the kept copy before the path check refused: %+v", kst)
	}
}
