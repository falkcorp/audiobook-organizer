// file: internal/reconcile/retired_callers_test.go
// version: 1.0.0
// guid: 3a9f6c52-d018-4e47-b2c9-7e15a8d40f36
// last-edited: 2026-10-23

package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// retiredHolderStore is the fake reconcile store plus the retired-row reader a
// production store has: GetBookFiles (inherited) skips retired rows, and
// GetBookFilesIncludingRetired returns them with the live ones.
type retiredHolderStore struct {
	*fakeReconcileStore
	retired map[string][]database.BookFile
}

func (s *retiredHolderStore) GetBookFilesIncludingRetired(bookID string) ([]database.BookFile, error) {
	live, _ := s.fakeReconcileStore.GetBookFiles(bookID)
	return append(append([]database.BookFile{}, live...), s.retired[bookID]...), nil
}

func (s *retiredHolderStore) GetRetiredBookFiles(holderID string) ([]database.BookFile, error) {
	return s.retired[holderID], nil
}

func (s *retiredHolderStore) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return nil, nil
}

func (s *retiredHolderStore) GetRetiredVersionsFromBook(string) ([]database.BookVersion, error) {
	return nil, nil
}

func (s *retiredHolderStore) GetBookFileByPIDIncludingRetired(string) (*database.BookFile, error) {
	return nil, nil
}

func (s *retiredHolderStore) GetBookFileByPathIncludingRetired(string) (*database.BookFile, error) {
	return nil, nil
}

var _ database.RetiredBookFileReader = (*retiredHolderStore)(nil)

// A duplicate library copy that holds ONLY retired rows reads as having no
// files through GetBookFiles. Without the include-retired read it would lose
// its file to os.Remove and then be refused by DeleteBook; it is kept, and
// counted as holding retired versions, in a dry run too.
func TestCleanupDuplicateVersionGroups_DuplicateHoldingOnlyRetiredRowsIsKept(t *testing.T) {
	for _, dry := range []bool{true, false} {
		root := t.TempDir()
		dupFile := filepath.Join(root, "b.m4b")
		if err := os.WriteFile(dupFile, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		base := newFakeStore()
		vg := "vg-1"
		addCore(base, database.BookCore{ID: "01", Title: "t", FilePath: filepath.Join(root, "a.m4b"), VersionGroupID: &vg})
		addCore(base, database.BookCore{ID: "02", Title: "t", FilePath: dupFile, VersionGroupID: &vg})
		addCore(base, database.BookCore{ID: "03", Title: "t", FilePath: "/src/a.m4b", VersionGroupID: &vg})
		s := &retiredHolderStore{fakeReconcileStore: base, retired: map[string][]database.BookFile{
			"02": {{ID: "r1", BookID: "02", FilePath: "/lib/old/shell.m4b", Retired: true, VersionID: "v1"}},
		}}

		res, err := CleanupDuplicateVersionGroups(s, root, dry)
		if err != nil {
			t.Fatal(err)
		}
		if res.DuplicatesRemoved != 0 || res.FilesDeleted != 0 || res.SkippedOwnsFiles != 1 || res.SkippedHoldsRetired != 1 {
			t.Errorf("dry=%v: result = %+v, want DuplicatesRemoved=0 FilesDeleted=0 SkippedOwnsFiles=1 SkippedHoldsRetired=1", dry, res)
		}
		if _, err := os.Stat(dupFile); err != nil {
			t.Errorf("dry=%v: the holder's file was removed from disk: %v", dry, err)
		}
		if _, ok := base.byID["02"]; !ok {
			t.Errorf("dry=%v: the duplicate holding retired rows was deleted", dry)
		}
	}
}

// A path held by a retired row is not a live candidate for the AcoustID collapse
// of ambiguous heal matches: its BookID is the holder, so merging "its book"
// would merge the holder into a book it already absorbed. Control: the same
// print on a live row reaches the merge path.
func TestResolveAmbiguousByDB_RetiredCandidateIsUnresolved(t *testing.T) {
	good := healTestFrames(400, 1)
	for name, retired := range map[string]bool{"retired": true, "control_live": false} {
		t.Run(name, func(t *testing.T) {
			var bookReads atomic.Int32
			m := &database.MockStore{}
			m.GetBookFileByPathFunc = func(p string) (*database.BookFile, error) {
				bookID := "A"
				if p == "/b.m4b" {
					bookID = "B"
				}
				return &database.BookFile{ID: "f" + bookID, BookID: bookID, FilePath: p, AcoustIDFingerprint: good,
					AcoustIDFPVersion: fingerprint.PrintEncodingVersion, Retired: retired && p == "/b.m4b"}, nil
			}
			m.GetBookByIDFunc = func(string) (*database.Book, error) { bookReads.Add(1); return nil, nil }
			src, merged := resolveAmbiguousByDB(context.Background(), m, "", []string{"/a.m4b", "/b.m4b"})
			if retired {
				if src != "" || merged != 0 || bookReads.Load() != 0 {
					t.Fatalf("retired candidate: resolved to %q (merged %d, book reads %d), want unresolved with no merge reads", src, merged, bookReads.Load())
				}
			} else if bookReads.Load() == 0 {
				t.Fatal("control: the live candidates never reached the merge path; fixture is vacuous")
			}
		})
	}
}

// The book-metadata layer scores a candidate through its row's BookID; a retired
// row's BookID is the holder, which was never scored against this track.
func TestResolveAmbiguousByBookMeta_RetiredCandidateIsSkipped(t *testing.T) {
	for name, retired := range map[string]bool{"retired": true, "control_live": false} {
		t.Run(name, func(t *testing.T) {
			var bookReads atomic.Int32
			m := &database.MockStore{}
			m.GetBookFileByPathFunc = func(p string) (*database.BookFile, error) {
				return &database.BookFile{ID: "f1", BookID: "A", FilePath: p, Retired: retired}, nil
			}
			m.GetBookByIDFunc = func(string) (*database.Book, error) { bookReads.Add(1); return nil, nil }
			got := resolveAmbiguousByBookMeta(m, iTunesTrack{Album: "Some Long Book Title", Artist: "An Author"}, []string{"/a.m4b"})
			if got != "" {
				t.Fatalf("resolved to %q, want unresolved", got)
			}
			if retired && bookReads.Load() != 0 {
				t.Fatal("a retired candidate's holder was scored")
			}
			if !retired && bookReads.Load() == 0 {
				t.Fatal("control: a live candidate was never scored; fixture is vacuous")
			}
		})
	}
}

// A PID only a retired row holds is not "the book's current file": the heal's
// not-found layer returns nothing even when the retired row's file is on disk.
func TestResolveNotFoundByPID_RetiredRowIsRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.m4b")
	if err := os.WriteFile(file, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, retired := range map[string]bool{"retired": true, "control_live": false} {
		t.Run(name, func(t *testing.T) {
			m := &database.MockStore{}
			m.GetBookFileByPIDFunc = func(string) (*database.BookFile, error) {
				return &database.BookFile{ID: "f1", BookID: "A", FilePath: file, Retired: retired}, nil
			}
			got := resolveNotFoundByPID(m, "PID-1")
			if retired && got != "" {
				t.Fatalf("a retired row resolved to %q, want \"\"", got)
			}
			if !retired && got != file {
				t.Fatalf("control: a live row resolved to %q, want %q", got, file)
			}
		})
	}
}
