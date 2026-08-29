// file: internal/pathutil/hidden_test.go
// version: 1.1.0
// guid: 9d4b1f38-6c02-4a75-8e3d-2f905c7ab146
// last-edited: 2026-08-29

package pathutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsHiddenName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{".backups", true},
		{".itunes-writeback", true},
		{".failed", true},
		{"books", false},
		{"", false},
		// Traversal elements are not hidden. Treating "." as hidden would make
		// a walk rooted at "." skip its own root.
		{".", false},
		{"..", false},
		// A dotted FILE name is still hidden by name; the dir-vs-file decision
		// belongs to the caller, not here.
		{".DS_Store", true},
		// Not hidden: the dot is not leading.
		{"book.1", false},
	} {
		if got := IsHiddenName(tc.name); got != tc.want {
			t.Errorf("IsHiddenName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestShouldSkipDir_SkipsHiddenChildrenButNeverTheRoot(t *testing.T) {
	root := "/srv/books"
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/srv/books", false},                     // the root itself
		{"/srv/books/abooks", false},              // ordinary content
		{"/srv/books/.backups", true},             // the case this exists for
		{"/srv/books/audiobook-organizer", false}, // the app's own dir is NOT hidden
		{"/srv/books/audiobook-organizer/.backups", true},
		{"/srv/books/.failed", true},
	} {
		if got := ShouldSkipDir(root, tc.path); got != tc.want {
			t.Errorf("ShouldSkipDir(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
		}
	}
}

// A root that is ITSELF hidden must still be walked.
//
// WalkDir yields the root as its first callback, so skipping it abandons the
// whole walk -- the failure mode would be a configured import path that
// silently scans nothing, which is the hardest kind of bug to notice because
// it looks exactly like an empty folder.
func TestShouldSkipDir_HiddenRootIsStillWalked(t *testing.T) {
	root := "/srv/.hidden-library"
	if ShouldSkipDir(root, root) {
		t.Fatal("the walk root was skipped; an explicitly configured hidden root must still be scanned")
	}
	if !ShouldSkipDir(root, "/srv/.hidden-library/.backups") {
		t.Error("a hidden child of a hidden root must still be skipped")
	}
	if ShouldSkipDir(root, "/srv/.hidden-library/abooks") {
		t.Error("ordinary content under a hidden root must be scanned")
	}
}

// Trailing separators must not change the verdict for the root.
func TestShouldSkipDir_RootWithTrailingSeparator(t *testing.T) {
	if ShouldSkipDir("/srv/books/", "/srv/books") {
		t.Error("a trailing separator on the root made the walk skip its own root")
	}
	if ShouldSkipDir("/srv/.x/", "/srv/.x") {
		t.Error("a trailing separator on a hidden root made the walk skip everything")
	}
}

// The behaviour that matters, end to end: a real walk must not descend into a
// hidden directory, and must still see everything else.
func TestShouldSkipDir_RealWalkSkipsHiddenSubtree(t *testing.T) {
	root := t.TempDir()
	mkdir := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("abooks")
	mkdir(".backups")
	mkdir(".backups/nested")
	write("abooks/real.m4b")
	write(".backups/archive.tar.gz")
	write(".backups/nested/another.tar.gz")

	var seen []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if ShouldSkipDir(root, path) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		seen = append(seen, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(seen) != 1 || seen[0] != filepath.Join("abooks", "real.m4b") {
		t.Fatalf("walk saw %v; want only abooks/real.m4b -- the .backups subtree must be invisible", seen)
	}
}

// The carve-out. Alternate versions are real library content that happens to
// live under a dot-name, so the skip rule must not hide it. Without this the
// alternates feature would ship into a scanner that cannot see it -- and a
// folder the scanner cannot see fails SILENTLY: the books just never appear.
func TestShouldSkipDir_AlternatesIsCarvedOut(t *testing.T) {
	root := "/srv/books"
	if ShouldSkipDir(root, "/srv/books/.alternates") {
		t.Error(".alternates was skipped; it is library content, not app state")
	}
	if ShouldSkipDir(root, "/srv/books/abooks/.alternates") {
		t.Error(".alternates was skipped when nested under a book folder")
	}
	// The carve-out is exact, not a prefix match: a neighbouring dot-name must
	// not inherit visibility just by starting the same way.
	if !ShouldSkipDir(root, "/srv/books/.alternates-backup") {
		t.Error(".alternates-backup was treated as carved out; the match must be exact")
	}
	// And the rule it lives inside still works.
	if !ShouldSkipDir(root, "/srv/books/.backups") {
		t.Error(".backups must still be skipped")
	}
}

func TestIsVisibleHiddenDir(t *testing.T) {
	if !IsVisibleHiddenDir(".alternates") {
		t.Error(".alternates must be visible")
	}
	for _, n := range []string{".backups", ".failed", ".itunes-writeback", "abooks", ""} {
		if IsVisibleHiddenDir(n) {
			t.Errorf("%q must not be carved out", n)
		}
	}
}
