// file: internal/reconcile/cleanup_owns_files_test.go
// version: 1.0.0
// guid: 0e41a60d-a159-4c2a-9e4d-ff27e052715f
// last-edited: 2026-09-19

package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// CleanupDuplicateVersionGroups used to os.Remove a duplicate library copy's
// file and then DeleteBook it. DeleteBook never deletes book_file rows, so the
// duplicate's rows were orphaned; with DeleteBook now refusing such a book
// (database.ErrBookOwnsFiles) the same order would delete the audio and keep
// a book whose rows name a missing file. A duplicate that owns rows must be
// left entirely alone — file, rows and book — and counted.
func TestCleanupDuplicateVersionGroups_DuplicateOwningFilesIsKept(t *testing.T) {
	root := t.TempDir()
	dupFile := filepath.Join(root, "b.m4b")
	if err := os.WriteFile(dupFile, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newFakeStore()
	vg := "vg-1"
	addCore(s, database.BookCore{ID: "01", Title: "t", FilePath: filepath.Join(root, "a.m4b"), VersionGroupID: &vg})
	addCore(s, database.BookCore{ID: "02", Title: "t", FilePath: dupFile, VersionGroupID: &vg})
	addCore(s, database.BookCore{ID: "03", Title: "t", FilePath: "/src/a.m4b", VersionGroupID: &vg})
	s.files["02"] = []database.BookFile{{ID: "f2", BookID: "02", FilePath: dupFile}}

	res, err := CleanupDuplicateVersionGroups(s, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.DuplicatesRemoved != 0 || res.FilesDeleted != 0 || res.SkippedOwnsFiles != 1 {
		t.Errorf("result = %+v, want DuplicatesRemoved=0 FilesDeleted=0 SkippedOwnsFiles=1", res)
	}
	if _, err := os.Stat(dupFile); err != nil {
		t.Errorf("duplicate's file removed from disk: %v", err)
	}
	if _, ok := s.byID["02"]; !ok {
		t.Error("duplicate owning a book_file row was deleted")
	}
}
