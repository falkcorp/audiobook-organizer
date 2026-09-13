// file: internal/reconcile/path_containment_boundary_test.go
// version: 1.0.0
// guid: 71a4c9e2-3f8b-4d06-a5c1-e92b7d04f6a3
// last-edited: 2026-09-12

package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Every reconcile pass that splits books into "in the library" and "not in the
// library" must treat a sibling directory ("/lib2" next to "/lib") as outside.

func addCore(s *fakeReconcileStore, b database.BookCore) {
	s.books = append(s.books, b)
	full := &database.Book{ID: b.ID, Title: b.Title, FilePath: b.FilePath, VersionGroupID: b.VersionGroupID}
	s.byID[b.ID] = full
}

func TestCleanupDuplicateVersionGroups_SiblingIsNotALibraryCopy(t *testing.T) {
	s := newFakeStore()
	vg := "vg-1"
	addCore(s, database.BookCore{ID: "01", Title: "t", FilePath: "/lib/a.m4b", VersionGroupID: &vg})
	addCore(s, database.BookCore{ID: "02", Title: "t", FilePath: "/lib2/a.m4b", VersionGroupID: &vg})
	addCore(s, database.BookCore{ID: "03", Title: "t", FilePath: "/src/a.m4b", VersionGroupID: &vg})

	res, err := CleanupDuplicateVersionGroups(s, "/lib", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.GroupsCleaned != 0 || res.DuplicatesRemoved != 0 {
		t.Errorf("cleaned=%d removed=%d, want 0/0: only one member is inside /lib", res.GroupsCleaned, res.DuplicatesRemoved)
	}

	// Control: two members really inside the root are pruned to one.
	s2 := newFakeStore()
	addCore(s2, database.BookCore{ID: "01", Title: "t", FilePath: "/lib/a.m4b", VersionGroupID: &vg})
	addCore(s2, database.BookCore{ID: "02", Title: "t", FilePath: "/lib/b.m4b", VersionGroupID: &vg})
	addCore(s2, database.BookCore{ID: "03", Title: "t", FilePath: "/src/a.m4b", VersionGroupID: &vg})
	res, err = CleanupDuplicateVersionGroups(s2, "/lib", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.GroupsCleaned != 1 || res.DuplicatesRemoved != 1 {
		t.Errorf("control: cleaned=%d removed=%d, want 1/1", res.GroupsCleaned, res.DuplicatesRemoved)
	}
}

func TestFindBrokenSegmentBooks_SiblingOfRootIsChecked(t *testing.T) {
	prev := config.AppConfig.RootDir
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	base := t.TempDir()
	root := filepath.Join(base, "lib")
	bookDir := filepath.Join(base, "lib2", "Book")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	present := filepath.Join(bookDir, "01.mp3")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	config.AppConfig.RootDir = root

	s := newFakeStore()
	addCore(s, database.BookCore{ID: "b1", Title: "Book", FilePath: bookDir})
	s.files["b1"] = []database.BookFile{
		{ID: "f1", BookID: "b1", FilePath: present},
		{ID: "f2", BookID: "b1", FilePath: filepath.Join(bookDir, "02.mp3")},
	}

	res, err := FindBrokenSegmentBooks(s, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenBooks != 1 {
		t.Errorf("BrokenBooks = %d, want 1: %s is outside %s and must be checked", res.BrokenBooks, bookDir, root)
	}
}

func TestMergeNoVGDuplicates_SiblingIsNotALibraryOrphan(t *testing.T) {
	s := newFakeStore()
	addCore(s, database.BookCore{ID: "in", Title: "In Root", FilePath: "/lib/a.m4b"})
	addCore(s, database.BookCore{ID: "sib", Title: "Sibling", FilePath: "/lib2/b.m4b"})

	res, err := MergeNoVGDuplicates(s, "/lib", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalNoVG != 1 {
		t.Errorf("TotalNoVG = %d, want 1: /lib2/b.m4b is not in /lib", res.TotalNoVG)
	}
}

func TestAssignOrphanVGs_SiblingIsNotInLibrary(t *testing.T) {
	s := newFakeStore()
	addCore(s, database.BookCore{ID: "sib", Title: "Sibling", FilePath: "/lib2/b.m4b"})
	addCore(s, database.BookCore{ID: "in", Title: "In Root", FilePath: "/lib/a.m4b"})

	res, err := AssignOrphanVGs(s, "/lib")
	if err != nil {
		t.Fatal(err)
	}
	if res.NotInLibrary != 1 || res.Assigned != 1 {
		t.Errorf("NotInLibrary=%d Assigned=%d, want 1/1", res.NotInLibrary, res.Assigned)
	}
	if _, touched := s.updated["sib"]; touched {
		t.Error("sibling book outside /lib was given a version group")
	}
}
