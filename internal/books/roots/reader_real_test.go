// file: internal/books/roots/reader_real_test.go
// version: 0.1.0
// guid: 0a9c1d77-52d4-4e7c-8f3b-1c6e5a4d9b02
// last-edited: 2026-11-20

package roots_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"

	"github.com/falkcorp/audiobook-organizer/internal/books/roots"
)

// The reader over a real library.Manager and Scope: what books reads is what
// library registered, narrowed to books' own roots.
func TestReader_OverARealScope(t *testing.T) {
	ctx := context.Background()
	rg := newRig(t, nil)
	scans := roots.NewScanStore(newMemRows())
	r := roots.NewReader(rg.books, scans)

	if got := r.RootDir(); got != rg.libDir {
		t.Errorf("RootDir = %q; want the registered path %q", got, rg.libDir)
	}
	lib, err := r.LibraryRoot()
	if err != nil || lib.Name != "Wayne Manor Library" || lib.Disabled != nil {
		t.Fatalf("LibraryRoot = %+v, %v", lib, err)
	}
	if err := scans.RecordScan(ctx, "import-1", 4, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := r.ImportRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Arkham intake" || got[1].Name != "Crime Alley" {
		t.Fatalf("ImportRoots = %+v; want books' two import roots and not tv's root", got)
	}
	if got[0].BookCount != 4 || !got[0].Enabled || got[1].Enabled {
		t.Errorf("scan state: %+v / %+v (import-2 has no row and watch off)", got[0], got[1])
	}
	if got[0].Path != rg.arkham {
		t.Errorf("import-1 path = %q; want %q", got[0].Path, rg.arkham)
	}
	if ids, err := r.WatchRoots(ctx); err != nil || len(ids) != 1 || ids[0] != "import-1" {
		t.Errorf("WatchRoots = %v, %v; want [import-1]", ids, err)
	}
	if n, err := r.NextImportN(ctx); err != nil || n != 3 {
		t.Errorf("NextImportN = %d, %v; want 3", n, err)
	}
	// tv's root is not books' to read.
	if _, err := r.ImportRoot(ctx, "tv-library"); !errors.Is(err, roots.ErrNoSuchRoot) {
		t.Errorf("ImportRoot(tv-library) = %v", err)
	}
}

func TestReader_NoLibraryRootOnAFreshInstall(t *testing.T) {
	rg := newRig(t, func(specs []library.RootSpec) { specs[0].ID = "someone-elses" })
	r := roots.NewReader(rg.books, roots.NewScanStore(newMemRows()))
	if _, err := r.LibraryRoot(); !errors.Is(err, roots.ErrNoLibraryRoot) {
		t.Errorf("LibraryRoot = %v; want ErrNoLibraryRoot", err)
	}
	if r.RootDir() != "" {
		t.Errorf("RootDir = %q; want empty", r.RootDir())
	}
}

func TestReader_ADisabledRootIsReportedNotHidden(t *testing.T) {
	rg := newRig(t, func(specs []library.RootSpec) { specs[1].Path = filepath.Join(os.TempDir(), "no-such-gotham-dir") })
	r := roots.NewReader(rg.books, roots.NewScanStore(newMemRows()))
	ir, err := r.ImportRoot(context.Background(), "import-1")
	if err != nil {
		t.Fatal(err)
	}
	if ir.Disabled == nil {
		t.Errorf("a root at a missing directory is not reported disabled: %+v", ir)
	}
	if ids, _ := r.WatchRoots(context.Background()); len(ids) != 0 {
		t.Errorf("WatchRoots includes a disabled root: %v", ids)
	}
}
