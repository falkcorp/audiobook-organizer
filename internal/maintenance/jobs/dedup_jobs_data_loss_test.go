// file: internal/maintenance/jobs/dedup_jobs_data_loss_test.go
// version: 1.1.0
// guid: 5e2b8c47-91d3-4f60-a7c8-2d4e6f1a9b30
// last-edited: 2026-09-13

package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// Regression tests for the dedup-books and fix-version-groups data-loss
// findings. They run against a real PebbleStore where the bug lives in the
// store's semantics (UpsertBookFile keeping the stored BookID; rows deleted
// and recreated), because a mock would model the bug away.

func ddRealStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func ddMustBook(t *testing.T, s *database.PebbleStore, b *database.Book) *database.Book {
	t.Helper()
	created, err := s.CreateBook(b)
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	return created
}

func ddMustFile(t *testing.T, s *database.PebbleStore, f *database.BookFile) *database.BookFile {
	t.Helper()
	if f.FileSize == 0 {
		f.FileSize = 50 << 20
	}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	files, err := s.GetBookFiles(f.BookID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	for i := range files {
		if files[i].FilePath == f.FilePath {
			return &files[i]
		}
	}
	t.Fatalf("created file %s not found", f.FilePath)
	return nil
}

func ddMustGet(t *testing.T, s *database.PebbleStore, id string) *database.Book {
	t.Helper()
	b, err := s.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID(%s): %v", id, err)
	}
	return b
}

// On main the dup's rows were "reassigned" with f.BookID = keeper.ID +
// UpsertBookFile, which keeps the STORED BookID: nothing moved, and the dup was
// soft-deleted still owning the file and still naming the keeper's path.
func TestDDMergeDuplicateBook_MovesFilesAndClearsDupPath(t *testing.T) {
	s := ddRealStore(t)
	const shared = "/lib/Author/Book/book.m4b"
	keeper := ddMustBook(t, s, &database.Book{Title: "Book", FilePath: shared})
	dup := ddMustBook(t, s, &database.Book{Title: "Book", FilePath: shared})
	f := ddMustFile(t, s, &database.BookFile{BookID: dup.ID, FilePath: shared, ITunesPersistentID: "PID-1"})

	if err := ddMergeDuplicateBook(s, keeper, dup, false, nil); err != nil {
		t.Fatalf("ddMergeDuplicateBook: %v", err)
	}

	dupFiles, _ := s.GetBookFiles(dup.ID)
	if len(dupFiles) != 0 {
		t.Fatalf("dup still owns %d file row(s) after the merge", len(dupFiles))
	}
	keeperFiles, _ := s.GetBookFiles(keeper.ID)
	if len(keeperFiles) != 1 || keeperFiles[0].ID != f.ID {
		t.Fatalf("keeper files = %+v, want the dup's row %s moved over", keeperFiles, f.ID)
	}
	if keeperFiles[0].ITunesPersistentID != "PID-1" {
		t.Errorf("moved row lost its iTunes PID: %q", keeperFiles[0].ITunesPersistentID)
	}
	got := ddMustGet(t, s, dup.ID)
	if !got.IsSoftDeleted() {
		t.Error("dup must be soft-deleted after a successful merge")
	}
	if got.FilePath != "" {
		t.Errorf("dup FilePath = %q, want cleared: a purge with delete-files would remove the keeper's audio", got.FilePath)
	}
}

// A failed move is a failed pair: the dup is not soft-deleted.
func TestDDMergeDuplicateBook_MoveFailureLeavesDupLive(t *testing.T) {
	boom := errors.New("pebble: batch commit failed")
	writes := map[string]*database.Book{}
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) { return &database.Book{ID: id}, nil },
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			cp := *b
			writes[id] = &cp
			return b, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			if bookID == "dup" {
				return []database.BookFile{{ID: "f1", BookID: "dup", FilePath: "/lib/x.m4b"}}, nil
			}
			return nil, nil
		},
		MoveBookFilesToBookFunc: func([]string, string, string) error { return boom },
	}
	err := ddMergeDuplicateBook(store, &database.Book{ID: "keep"}, &database.Book{ID: "dup"}, false, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the move failure", err)
	}
	if d := writes["dup"]; d != nil && d.MarkedForDeletion != nil && *d.MarkedForDeletion {
		t.Fatal("dup was soft-deleted although its files never moved")
	}
}

// books/itunes/** is never mutated: the pair is refused before any write,
// in dry-run as in apply.
func TestDDMergeDuplicateBook_RefusesITunesLibrary(t *testing.T) {
	s := ddRealStore(t)
	const p = "/lib/books/itunes/Author/Book/book.m4b"
	keeper := ddMustBook(t, s, &database.Book{Title: "Book", FilePath: p})
	dup := ddMustBook(t, s, &database.Book{Title: "Book", FilePath: p})
	ddMustFile(t, s, &database.BookFile{BookID: dup.ID, FilePath: p})

	for _, dry := range []bool{true, false} {
		err := ddMergeDuplicateBook(s, keeper, dup, dry, nil)
		if !errors.Is(err, merge.ErrITunesProtected) {
			t.Fatalf("dryRun=%v: err = %v, want ErrITunesProtected", dry, err)
		}
	}
	if got := ddMustGet(t, s, dup.ID); got.IsSoftDeleted() || got.FilePath != p {
		t.Fatalf("iTunes-protected dup was mutated: %+v", got)
	}
	if files, _ := s.GetBookFiles(dup.ID); len(files) != 1 {
		t.Fatalf("iTunes-protected dup's rows moved: %d left", len(files))
	}
}

// The explicit primary is kept even when a non-primary has richer metadata.
func TestDDPickKeeperIdx_PrefersPrimary(t *testing.T) {
	yes, no := true, false
	author := 7
	books := []database.Book{
		{ID: "rich", AuthorID: &author, IsPrimaryVersion: &no},
		{ID: "prim", IsPrimaryVersion: &yes},
	}
	if got := books[ddPickKeeperIdx(books)].ID; got != "prim" {
		t.Fatalf("keeper = %s, want the version group's primary", got)
	}
}

// Retiring a group's primary hands the primary to the keeper first, so the
// group is never left with none.
func TestDDMergeDuplicateBook_PrimaryDupHandsOffToKeeper(t *testing.T) {
	s := ddRealStore(t)
	vg := "vg-handoff"
	yes, no := true, false
	keeper := ddMustBook(t, s, &database.Book{Title: "Book", FilePath: "/lib/A/k.m4b"})
	dup := ddMustBook(t, s, &database.Book{Title: "Book", FilePath: "/lib/A/d.m4b"})
	for id, prim := range map[string]*bool{keeper.ID: &no, dup.ID: &yes} {
		if _, err := s.ModifyBook(id, func(b *database.Book) error {
			b.VersionGroupID = &vg
			b.IsPrimaryVersion = prim
			return nil
		}); err != nil {
			t.Fatalf("seed group: %v", err)
		}
	}
	keeper, dup = ddMustGet(t, s, keeper.ID), ddMustGet(t, s, dup.ID)

	if err := ddMergeDuplicateBook(s, keeper, dup, false, nil); err != nil {
		t.Fatalf("ddMergeDuplicateBook: %v", err)
	}
	members, err := s.GetBooksByVersionGroup(vg)
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup: %v", err)
	}
	livePrimaries := 0
	for _, m := range members {
		if !m.IsSoftDeleted() && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			livePrimaries++
		}
	}
	if livePrimaries != 1 {
		t.Fatalf("group has %d live primaries after retiring its primary, want 1", livePrimaries)
	}
}

// ddJobReporter is a do-nothing maintenance.ProgressReporter.
type ddJobReporter struct{}

func (ddJobReporter) SetTotal(int)                {}
func (ddJobReporter) Increment()                  {}
func (ddJobReporter) Log(string, string, *string) {}

func ddWriteAudio(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(ddAudioBytes), 0o644); err != nil {
		t.Fatal(err)
	}
}

const ddAudioBytes = "not really audio"

// On main, fixing an author-directory path deleted every book_file row of the
// book and recreated them from a disk scan: new IDs, and the iTunes PID,
// duration, fingerprint and transcript of every row gone. Rows must be
// repointed in place instead.
func TestFixVersionGroups_AuthorDirRepointsRowsInPlace(t *testing.T) {
	s := ddRealStore(t)
	authorDir := filepath.Join(t.TempDir(), "Some Author")
	ddWriteAudio(t, filepath.Join(authorDir, "Alpha Chronicle", "01.mp3"))
	ddWriteAudio(t, filepath.Join(authorDir, "Beta Saga", "02.mp3"))

	book := ddMustBook(t, s, &database.Book{Title: "Alpha Chronicle", FilePath: authorDir})
	// The row predates the folder split: it names the file by its old flat
	// path, which no longer exists, and records the size of the moved file.
	old := ddMustFile(t, s, &database.BookFile{
		BookID: book.ID, FilePath: filepath.Join(authorDir, "01.mp3"),
		ITunesPersistentID: "PID-KEEP", Duration: 3600, FileSize: int64(len(ddAudioBytes)),
	})

	j := &fixVersionGroupsJob{}
	if err := j.Run(context.Background(), s, ddJobReporter{}, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := ddMustGet(t, s, book.ID)
	wantDir := filepath.Join(authorDir, "Alpha Chronicle")
	if got.FilePath != wantDir {
		t.Fatalf("book path = %q, want %q", got.FilePath, wantDir)
	}
	files, err := s.GetBookFiles(book.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("book has %d rows, want 1: %+v", len(files), files)
	}
	f := files[0]
	if f.ID != old.ID {
		t.Errorf("row ID changed %s -> %s: the row was recreated, not repointed", old.ID, f.ID)
	}
	if f.FilePath != filepath.Join(wantDir, "01.mp3") {
		t.Errorf("row path = %q, want it repointed into the book's folder", f.FilePath)
	}
	if f.ITunesPersistentID != "PID-KEEP" || f.Duration != 3600 {
		t.Errorf("per-file fields lost: pid=%q duration=%d", f.ITunesPersistentID, f.Duration)
	}
}
