// file: internal/database/path_containment_boundary_test.go
// version: 1.0.0
// guid: 5c0e7a41-9b2d-4f63-8e15-2a7d4c9b6f08
// last-edited: 2026-09-12

package database

import (
	"path/filepath"
	"sort"
	"testing"
)

// These tests pin the separator-boundary rule for every "is this book under
// that root / import path" check in the store: a sibling directory whose name
// merely starts with the root ("/lib2" against "/lib") is OUTSIDE it. Each
// fixture pairs a sibling with a real child so a regression to a bare
// strings.HasPrefix changes the count.

func newBoundaryPebble(t *testing.T) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.UseMemDB = false
	return p
}

func boundaryBook(t *testing.T, p *PebbleStore, id, path string, size int64, sourceImport string) {
	t.Helper()
	b := &Book{ID: id, Title: id, FilePath: path, FileSize: &size}
	if sourceImport != "" {
		b.SourceImportPath = &sourceImport
	}
	if _, err := p.CreateBook(b); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func TestGetBookCountsAndSizesByLocation_SiblingIsNotInRoot(t *testing.T) {
	p := newBoundaryPebble(t)
	boundaryBook(t, p, "in-root", "/lib/a.m4b", 10, "")
	boundaryBook(t, p, "sibling", "/lib2/b.m4b", 20, "")

	lib, imp, err := p.GetBookCountsByLocation("/lib")
	if err != nil {
		t.Fatal(err)
	}
	if lib != 1 || imp != 1 {
		t.Errorf("GetBookCountsByLocation(/lib) = (%d, %d), want (1, 1): /lib2 is not inside /lib", lib, imp)
	}

	libSize, impSize, err := p.GetBookSizesByLocation("/lib")
	if err != nil {
		t.Fatal(err)
	}
	if libSize != 10 || impSize != 20 {
		t.Errorf("GetBookSizesByLocation(/lib) = (%d, %d), want (10, 20)", libSize, impSize)
	}

	// Trailing-separator root: same answer, as under the old check.
	lib, imp, _ = p.GetBookCountsByLocation("/lib/")
	if lib != 1 || imp != 1 {
		t.Errorf("GetBookCountsByLocation(/lib/) = (%d, %d), want (1, 1)", lib, imp)
	}
}

func TestCountBooksByPathPrefix_Pebble_SiblingNotCounted(t *testing.T) {
	p := newBoundaryPebble(t)
	boundaryBook(t, p, "src-child", "/elsewhere/a.m4b", 1, "/imp")
	boundaryBook(t, p, "src-sibling", "/elsewhere/b.m4b", 1, "/imp2")
	boundaryBook(t, p, "path-child", "/imp/c.m4b", 1, "")
	boundaryBook(t, p, "path-sibling", "/imp2/d.m4b", 1, "")

	got, err := p.CountBooksByPathPrefix("/imp")
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("CountBooksByPathPrefix(/imp) = %d, want 2 (SourceImportPath /imp and FilePath /imp/c.m4b only)", got)
	}
}

func TestCountBooksByPathPrefix_MemDB_SiblingNotCounted(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	imp, imp2 := "/imp", "/imp2"
	seedMemStore(t, m, []Book{
		{ID: "src-child", Title: "a", FilePath: "/elsewhere/a.m4b", SourceImportPath: &imp},
		{ID: "src-sibling", Title: "b", FilePath: "/elsewhere/b.m4b", SourceImportPath: &imp2},
		{ID: "path-child", Title: "c", FilePath: "/imp/c.m4b"},
		{ID: "path-sibling", Title: "d", FilePath: "/imp2/d.m4b"},
	}, nil, nil, nil)

	got, err := m.CountBooksByPathPrefix("/imp")
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("memdb CountBooksByPathPrefix(/imp) = %d, want 2", got)
	}
}

func TestComputeLibraryStats_MemDB_SiblingIsUnorganizedAndNotInImportPath(t *testing.T) {
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	s10, s20, s40 := int64(10), int64(20), int64(40)
	seedMemStore(t, m, []Book{
		{ID: "in-root", Title: "a", FilePath: "/lib/a.m4b", FileSize: &s10},
		{ID: "root-sibling", Title: "b", FilePath: "/lib2/b.m4b", FileSize: &s20},
		{ID: "imp-sibling", Title: "c", FilePath: "/imp2/c.m4b", FileSize: &s40},
	}, nil, nil, nil)
	ip := ImportPath{ID: 7, Path: "/imp"}

	stats, err := m.ComputeLibraryStats("/lib", []ImportPath{ip})
	if err != nil {
		t.Fatal(err)
	}
	if stats.OrganizedBooks != 1 || stats.UnorganizedBooks != 2 {
		t.Errorf("organized/unorganized = %d/%d, want 1/2", stats.OrganizedBooks, stats.UnorganizedBooks)
	}
	if n := stats.BooksByImportPath[ip.ID]; n != 0 {
		t.Errorf("BooksByImportPath[/imp] = %d, want 0: /imp2/c.m4b is not under /imp", n)
	}
}

func TestComputeLibraryStats_Pebble_SiblingIsUnorganizedAndNotInImportPath(t *testing.T) {
	p := newBoundaryPebble(t)
	p.SetRootDir("/lib")
	ip, err := p.CreateImportPath("/imp", "imp")
	if err != nil {
		t.Fatalf("create import path: %v", err)
	}
	boundaryBook(t, p, "in-root", "/lib/a.m4b", 10, "")
	boundaryBook(t, p, "root-sibling", "/lib2/b.m4b", 20, "")
	boundaryBook(t, p, "imp-child", "/imp/c.m4b", 30, "")
	boundaryBook(t, p, "imp-sibling", "/imp2/d.m4b", 40, "")

	stats, err := p.computeLibraryStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.OrganizedBooks != 1 || stats.UnorganizedBooks != 3 {
		t.Errorf("organized/unorganized = %d/%d, want 1/3", stats.OrganizedBooks, stats.UnorganizedBooks)
	}
	if n := stats.BooksByImportPath[ip.ID]; n != 1 {
		t.Errorf("BooksByImportPath[/imp] = %d, want 1 (only /imp/c.m4b)", n)
	}
}

func TestQuickQueryInImportPath_SiblingExcluded(t *testing.T) {
	p := newBoundaryPebble(t)
	if _, err := p.CreateImportPath("/imp", "imp"); err != nil {
		t.Fatalf("create import path: %v", err)
	}
	boundaryBook(t, p, "imp-child", "/imp/a.m4b", 1, "")
	boundaryBook(t, p, "imp-sibling", "/imp2/b.m4b", 1, "")

	n, err := p.computeQuickQueryCount("in_import_path")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("computeQuickQueryCount(in_import_path) = %d, want 1", n)
	}
	ids, err := p.GetAllBookIDsForQuickQuery("in_import_path")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(ids)
	if len(ids) != 1 || ids[0] != "imp-child" {
		t.Errorf("GetAllBookIDsForQuickQuery(in_import_path) = %v, want [imp-child]", ids)
	}
}
