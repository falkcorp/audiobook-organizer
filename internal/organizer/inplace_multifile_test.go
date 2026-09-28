// file: internal/organizer/inplace_multifile_test.go
// version: 1.1.0
// guid: 6c2a9e4d-7b13-4f58-a0d2-3e9b1c5f8a47
// last-edited: 2026-09-28

package organizer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// multiFileBook creates a two-file book whose path is its first file (the
// scanner's sub-group shape) under root/incoming/<title>.
func multiFileBook(t *testing.T, store *database.PebbleStore, root, title string) (b *database.Book, first, second string) {
	t.Helper()
	dir := filepath.Join(root, "incoming", title)
	first = filepath.Join(dir, "01.mp3")
	b = addInPlaceBook(t, store, "multi", title, first, filled(130, 4), nil, 0)
	second = filepath.Join(dir, "02.mp3")
	if err := os.WriteFile(second, filled(140, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "multi-2", BookID: b.ID, FilePath: second}); err != nil {
		t.Fatal(err)
	}
	return b, first, second
}

func targetDirFor(t *testing.T, svc *Service, b *database.Book) string {
	t.Helper()
	d, err := svc.newOrganizer().GenerateTargetDirPath(b)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// rowPaths returns book_file id -> path for book id.
func rowPaths(t *testing.T, store database.Store, id string) map[string]string {
	t.Helper()
	rows, err := store.GetBookFiles(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.ID] = r.FilePath
	}
	return out
}

func requireConflict(t *testing.T, err error, category string) {
	t.Helper()
	var c *DestinationConflictError
	if !errors.As(err, &c) || c.Category != category {
		t.Fatalf("err = %v, want a %s refusal", err, category)
	}
}

func requireUnmoved(t *testing.T, store database.Store, b *database.Book, first, second string) {
	t.Helper()
	mustContent(t, first, filled(130, 4))
	mustContent(t, second, filled(140, 5))
	want := map[string]string{"multi-f": first, "multi-2": second}
	got := rowPaths(t, store, b.ID)
	for id, p := range want {
		if got[id] != p {
			t.Fatalf("row %s at %q, want it unchanged at %q (rows %v)", id, got[id], p, got)
		}
	}
	bk, err := store.GetBookByID(b.ID)
	if err != nil || bk == nil {
		t.Fatalf("book lookup: %v", err)
	}
	if bk.FilePath != first {
		t.Fatalf("book path = %q, want it unchanged at %q", bk.FilePath, first)
	}
}

// TestReOrganizeInPlace_MultiFileRowOutsideRootIsRefused is BL-1: a book that
// a dedup merge left owning a file in the download folder must not pull that
// file into the library.
func TestReOrganizeInPlace_MultiFileRowOutsideRootIsRefused(t *testing.T) {
	svc, store, root := setupInPlace(t)
	b, first, _ := multiFileBook(t, store, root, "Merged")
	seeding := filepath.Join(t.TempDir(), "downloads", "Merged", "02.mp3")
	if err := os.MkdirAll(filepath.Dir(seeding), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seeding, filled(150, 9), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "multi-3", BookID: b.ID, FilePath: seeding}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	requireConflict(t, err, OutcomeOutsideLibraryRoot)
	mustContent(t, seeding, filled(150, 9))
	mustContent(t, first, filled(130, 4))
}

// TestReOrganizeInPlace_MultiFileMissingRowAtDestinationRefuses is S-1: a
// Missing row of another book at a destination is invisible to Lstat but
// would make two books claim one path.
func TestReOrganizeInPlace_MultiFileMissingRowAtDestinationRefuses(t *testing.T) {
	svc, store, root := setupInPlace(t)
	b, first, second := multiFileBook(t, store, root, "Claimed")
	target := targetDirFor(t, svc, b)
	ghost, err := store.CreateBook(&database.Book{ID: "ghost", Title: "Ghost", FilePath: filepath.Join(root, "elsewhere", "g.mp3")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "ghost-f", BookID: ghost.ID, FilePath: filepath.Join(target, "02.mp3"), Missing: true}); err != nil {
		t.Fatal(err)
	}

	_, err = svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	requireConflict(t, err, OutcomeMultiFileTargetOccupied)
	requireUnmoved(t, store, b, first, second)
}

// TestReOrganizeInPlace_MultiFileTargetDirSharedRefuses is S-2: the target
// folder already holds another book's file, so moving in would leave a folder
// neither book could be moved out of.
func TestReOrganizeInPlace_MultiFileTargetDirSharedRefuses(t *testing.T) {
	svc, store, root := setupInPlace(t)
	b, first, second := multiFileBook(t, store, root, "Shared")
	target := targetDirFor(t, svc, b)
	addInPlaceBook(t, store, "edition2", "Shared", filepath.Join(target, "Shared.m4b"), filled(90, 7), nil, 0)

	_, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	requireConflict(t, err, OutcomeTargetDirShared)
	requireUnmoved(t, store, b, first, second)
	mustContent(t, filepath.Join(target, "Shared.m4b"), filled(90, 7))
}

// TestReOrganizeInPlace_SubGroupsAlreadyInPlaceKeepTheirFirstFile is the other
// half of S-2: two sub-groups of one flat folder whose metadata names that
// same folder. Nothing moves; each is stamped organized and keeps its path on
// its own first file instead of both claiming the folder.
func TestReOrganizeInPlace_SubGroupsAlreadyInPlaceKeepTheirFirstFile(t *testing.T) {
	svc, store, _ := setupInPlace(t)
	probe := &database.Book{Title: "Bible", Author: &database.Author{Name: "Some Author"}}
	target := targetDirFor(t, svc, probe)
	mk := func(id, a, bname string, fill byte) (*database.Book, string) {
		p1, p2 := filepath.Join(target, a), filepath.Join(target, bname)
		bk := addInPlaceBook(t, store, id, "Bible", p1, filled(100, fill), nil, 0)
		if err := os.WriteFile(p2, filled(110, fill), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateBookFile(&database.BookFile{ID: id + "-2", BookID: bk.ID, FilePath: p2}); err != nil {
			t.Fatal(err)
		}
		return bk, p1
	}
	genesis, g1 := mk("genesis", "01 Genesis 001.mp3", "01 Genesis 002.mp3", 1)
	exodus, e1 := mk("exodus", "02 Exodus 001.mp3", "02 Exodus 002.mp3", 2)

	for _, tc := range []struct {
		b     *database.Book
		first string
	}{{genesis, g1}, {exodus, e1}} {
		landing, err := svc.OrganizeOneBook(svc.newOrganizer(), tc.b, &noopLogger{})
		if err != nil {
			t.Fatalf("OrganizeOneBook(%s): %v", tc.b.ID, err)
		}
		if landing.Path != tc.first || len(landing.FileMoves) != 0 {
			t.Fatalf("landing %+v, want the first file %q and no moves", landing, tc.first)
		}
		got := getInPlaceBook(t, store, tc.b.ID)
		if got.FilePath != tc.first || got.LibraryState == nil || *got.LibraryState != "organized" {
			t.Fatalf("book %s path=%q state=%v, want %q organized", tc.b.ID, got.FilePath, got.LibraryState, tc.first)
		}
	}
}

// TestReOrganizeInPlace_MultiFileRollbackOnSecondMoveFailure is S-7(a): the
// second file's move fails; the first file must be back where it was and no
// row may have changed. Removing the rollback leaves 01.mp3 in the target.
func TestReOrganizeInPlace_MultiFileRollbackOnSecondMoveFailure(t *testing.T) {
	svc, store, root := setupInPlace(t)
	b, first, second := multiFileBook(t, store, root, "Halfway")
	prev := multiFileMove
	t.Cleanup(func() { multiFileMove = prev })
	multiFileMove = func(src, dst string) error {
		if src == second {
			return errors.New("injected: disk went away")
		}
		return prev(src, dst)
	}

	_, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if err == nil {
		t.Fatal("OrganizeOneBook succeeded; want the injected failure")
	}
	requireUnmoved(t, store, b, first, second)
	if _, statErr := os.Lstat(filepath.Join(targetDirFor(t, svc, b), "01.mp3")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("01.mp3 still in the target after rollback (stat err %v)", statErr)
	}
}

// TestReOrganizeInPlace_MultiFileRollbackFailureIsAFailureNotASkip is S-3: the
// forward move fails with an "exists" error AND putting file 1 back fails. The
// book is split across two folders -- a failure (500, counted Failed), never a
// declined move.
func TestReOrganizeInPlace_MultiFileRollbackFailureIsAFailureNotASkip(t *testing.T) {
	svc, store, root := setupInPlace(t)
	b, first, second := multiFileBook(t, store, root, "Split")
	prev := multiFileMove
	t.Cleanup(func() { multiFileMove = prev })
	multiFileMove = func(src, dst string) error {
		if src == second || dst == first {
			return &os.LinkError{Op: "link", Old: src, New: dst, Err: fs.ErrExist}
		}
		return prev(src, dst)
	}

	stats := svc.organizeBooks(context.Background(), []database.Book{*b}, nil, &noopLogger{}, "")
	if stats.Failed != 1 || stats.Skipped != 0 || len(stats.Collisions) != 0 {
		t.Fatalf("stats = %+v, want one failure and no skip/collision", stats)
	}
	// Same failure through the single-book path the handler maps to 409/500.
	// file 1 is stuck in the target now; put it back so the book is whole.
	if err := os.Rename(filepath.Join(targetDirFor(t, svc, b), "01.mp3"), first); err != nil {
		t.Fatalf("test reset: %v", err)
	}
	_, err := svc.OrganizeOneBook(svc.newOrganizer(), getInPlaceBook(t, store, b.ID), &noopLogger{})
	var c *DestinationConflictError
	if errors.As(err, &c) {
		t.Fatalf("a failed rollback surfaced as a declined move (%s): %v", c.Category, err)
	}
}

// updateFailStore fails the Nth UpdateBookFile call.
type updateFailStore struct {
	*database.PebbleStore
	mu       sync.Mutex
	calls    int
	failCall int
}

func (s *updateFailStore) UpdateBookFile(id string, f *database.BookFile) error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n == s.failCall {
		return errors.New("injected UpdateBookFile failure")
	}
	return s.PebbleStore.UpdateBookFile(id, f)
}

// TestReOrganizeInPlace_MultiFileRowWriteFailureUndoesTheMove is S-4: the
// second row write fails after both files moved. Both files go back and the
// row already rewritten is restored; otherwise the next scan appends the
// moved files to the book a second time.
func TestReOrganizeInPlace_MultiFileRowWriteFailureUndoesTheMove(t *testing.T) {
	_, pebble, root := setupInPlace(t)
	store := &updateFailStore{PebbleStore: pebble, failCall: 2}
	svc := NewService(store)
	b, first, second := multiFileBook(t, pebble, root, "Rewritten")

	_, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if err == nil {
		t.Fatal("OrganizeOneBook succeeded; want the injected row-write failure")
	}
	var c *DestinationConflictError
	if errors.As(err, &c) {
		t.Fatalf("a failed write surfaced as a declined move: %v", err)
	}
	requireUnmoved(t, pebble, b, first, second)
}

// TestReOrganizeInPlace_MultiFileDuplicateRowsMoveOnce: two rows at one path
// move one file and both rows follow it.
func TestReOrganizeInPlace_MultiFileDuplicateRowsMoveOnce(t *testing.T) {
	svc, store, root := setupInPlace(t)
	b, _, second := multiFileBook(t, store, root, "Doubled")
	if err := store.CreateBookFile(&database.BookFile{ID: "multi-2b", BookID: b.ID, FilePath: second}); err != nil {
		t.Fatal(err)
	}
	landing, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if err != nil {
		t.Fatalf("OrganizeOneBook: %v", err)
	}
	if len(landing.FileMoves) != 3 {
		t.Fatalf("landing moves %+v, want 3 (one per row)", landing.FileMoves)
	}
	dst := filepath.Join(landing.Path, "02.mp3")
	mustContent(t, dst, filled(140, 5))
	got := rowPaths(t, store, b.ID)
	if got["multi-2"] != dst || got["multi-2b"] != dst {
		t.Fatalf("duplicate rows %v, want both at %q", got, dst)
	}
}

// failingWriter fails every CreateOperationChange and remembers the order of
// the attempts.
type failingWriter struct{ types []string }

func (w *failingWriter) CreateOperationChange(c *database.OperationChange) error {
	w.types = append(w.types, c.ChangeType)
	return errors.New("injected write failure")
}

// TestRecordInPlaceMove_SurfacesWriteErrorsAndRecordsPathFirst is S-6 and the
// ordering nit: every failed undo row is returned, and the book path update
// is written before the per-file moves so an undo (newest first) restores the
// path last.
func TestRecordInPlaceMove_SurfacesWriteErrorsAndRecordsPathFirst(t *testing.T) {
	w := &failingWriter{}
	landing := &Landing{Path: "/lib/A/B", MultiFile: true, FileMoves: []BookFileMove{
		{BookFileID: "f1", From: "/lib/x/01.mp3", To: "/lib/A/B/01.mp3"},
		{BookFileID: "f2", From: "/lib/x/02.mp3", To: "/lib/A/B/02.mp3"},
	}}
	err := RecordInPlaceMove(w, "book", landing, "/lib/x/01.mp3", "op")
	if err == nil {
		t.Fatal("RecordInPlaceMove swallowed the write failures")
	}
	if len(w.types) != 3 || w.types[0] != "book_path_update" {
		t.Fatalf("write order %v, want book_path_update first then 2 book_file_move", w.types)
	}
}

// TestLinkMoveExclusive_TakesBackTheNewNameWhenTheSourceStays: when the
// source name cannot be removed the move has failed, and the new name must
// not be left behind as a second link nobody tracks.
func TestLinkMoveExclusive_TakesBackTheNewNameWhenTheSourceStays(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src, dst := filepath.Join(srcDir, "a.mp3"), filepath.Join(dstDir, "a.mp3")
	if err := os.WriteFile(src, filled(10, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(srcDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(srcDir, 0o755) })

	if err := moveExclusive(src, dst); err == nil {
		t.Fatal("moveExclusive succeeded with an unremovable source")
	}
	mustContent(t, src, filled(10, 1))
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the new name was left behind (stat err %v)", err)
	}
}

// TestReOrganizeInPlaceRecorded_MultiFileMoveIsUndoable is the series-normalize
// half of S-6: its re-organizes go through ReOrganizeInPlaceRecorded, which
// commits the landing under the operation, so a multi-file move leaves the
// per-file undo rows ReOrganizeInPlace never wrote.
func TestReOrganizeInPlaceRecorded_MultiFileMoveIsUndoable(t *testing.T) {
	svc, store, root := setupInPlace(t)
	b, _, _ := multiFileBook(t, store, root, "Normalized")
	target := targetDirFor(t, svc, b)
	const opID = "op-series-normalize"
	path, err := svc.ReOrganizeInPlaceRecorded(b, opID, &noopLogger{})
	if err != nil {
		t.Fatalf("ReOrganizeInPlaceRecorded: %v", err)
	}
	if path != target {
		t.Fatalf("path %q, want the target folder %q", path, target)
	}
	changes, err := store.GetOperationChanges(opID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, c := range changes {
		counts[c.ChangeType]++
	}
	if counts["book_file_move"] != 2 || counts["book_path_update"] != 1 {
		t.Fatalf("recorded %v; want 2 book_file_move + 1 book_path_update", counts)
	}
	if changes[0].ChangeType != "book_path_update" {
		t.Fatalf("first stored change is %s; the path update must be first so an undo restores it last", changes[0].ChangeType)
	}
}
