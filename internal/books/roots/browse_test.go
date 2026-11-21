// file: internal/books/roots/browse_test.go
// version: 0.1.0
// guid: 1f4b8e36-6c2a-4d7e-8a90-5e3d2c1b0f44
// last-edited: 2026-11-20

package roots_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"

	"github.com/falkcorp/audiobook-organizer/internal/books/roots"
)

func mkdir(t *testing.T, base, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, rel), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAllowedPath_ResolvesUnderBooksRootsOnly(t *testing.T) {
	rg := newRig(t, nil)
	mkdir(t, rg.libDir, "Year One")
	p, err := roots.AllowedPath(rg.books, filepath.Join(rg.libDir, "Year One"))
	if err != nil || p != (library.Path{Root: "books-library", Rel: "Year One"}) {
		t.Fatalf("AllowedPath under the library = %+v, %v", p, err)
	}
	if p, err := roots.AllowedPath(rg.books, filepath.Join(rg.arkham, "Hush", "..", "Hush")); err != nil || p.Root != "import-1" || p.Rel != "Hush" {
		t.Errorf("AllowedPath is lexically cleaned: %+v, %v", p, err)
	}
	for name, abs := range map[string]string{
		"another module's root": filepath.Join(rg.tvDir, "Pilot"),
		"under no root":         "/etc/passwd",
		"empty":                 "",
		"relative":              "Year One",
		"relative with dots":    filepath.Join("..", filepath.Base(rg.libDir)),
	} {
		_, err := roots.AllowedPath(rg.books, abs)
		if name != "empty" && !errors.Is(err, roots.ErrPathNotAllowed) { // relative paths are refused, never resolved against the cwd
			t.Errorf("%s: AllowedPath = %v; want ErrPathNotAllowed", name, err)
		}
		if name == "empty" && err == nil {
			t.Errorf("%s: AllowedPath accepted it", name)
		}
	}
	// The refusal carries library's reason as well.
	if _, err := roots.AllowedPath(rg.books, "/etc/passwd"); !errors.Is(err, library.ErrNotUnderRoot) {
		t.Errorf("AllowedPath outside every root = %v; want library.ErrNotUnderRoot wrapped", err)
	}
}

func TestExclusion_CreateListRemoveThroughTheConfinedFS(t *testing.T) {
	rg := newRig(t, nil)
	mkdir(t, rg.libDir, "Year One/Book One")
	year := library.Path{Root: "books-library", Rel: "Year One"}
	book := library.Path{Root: "books-library", Rel: "Year One/Book One"}

	if ex, err := roots.Excluded(rg.books, year); err != nil || ex {
		t.Fatalf("Excluded before = %v, %v", ex, err)
	}
	if err := roots.CreateExclusion(rg.books, year); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rg.libDir, "Year One", roots.ExclusionMarker)); err != nil {
		t.Errorf("the marker is not on disk where HEAD put it: %v", err)
	}
	if err := roots.CreateExclusion(rg.books, year); err != nil {
		t.Errorf("CreateExclusion twice = %v; want idempotent", err)
	}
	got, err := roots.ExcludedAmong(context.Background(), rg.books, []library.Path{year, book})
	if err != nil || len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("ExcludedAmong = %v, %v; want [true false]", got, err)
	}
	if err := roots.RemoveExclusion(rg.books, year); err != nil {
		t.Fatal(err)
	}
	if err := roots.RemoveExclusion(rg.books, year); !errors.Is(err, roots.ErrExclusionNotFound) {
		t.Errorf("RemoveExclusion twice = %v; want ErrExclusionNotFound", err)
	}
	if ex, _ := roots.Excluded(rg.books, year); ex {
		t.Error("still excluded after RemoveExclusion")
	}
}

func TestExclusion_TheRootItselfAndRefusals(t *testing.T) {
	rg := newRig(t, nil)
	rootDir := library.Path{Root: "books-library"}
	if err := roots.CreateExclusion(rg.books, rootDir); err != nil {
		t.Fatalf("exclusion at the root: %v", err)
	}
	if ex, err := roots.Excluded(rg.books, rootDir); err != nil || !ex {
		t.Errorf("Excluded at the root = %v, %v", ex, err)
	}
	if err := os.WriteFile(filepath.Join(rg.libDir, "cover.jpg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := roots.CreateExclusion(rg.books, library.Path{Root: "books-library", Rel: "cover.jpg"}); !errors.Is(err, roots.ErrNotDirectory) {
		t.Errorf("exclusion on a file = %v; want ErrNotDirectory", err)
	}
	if err := roots.CreateExclusion(rg.books, library.Path{Root: "books-library", Rel: "Nope"}); err == nil {
		t.Error("exclusion on a missing directory succeeded")
	}
	// Another module's root is not readable by books at all.
	if err := roots.CreateExclusion(rg.books, library.Path{Root: "tv-library"}); !errors.Is(err, library.ErrNotReadable) {
		t.Errorf("exclusion in tv's root = %v; want ErrNotReadable", err)
	}
}

// probeFS is a RootFS whose Stat is the test's: it counts concurrent probes and
// fails the names it is told to.
type probeFS struct {
	library.RootFS
	cur, peak atomic.Int64
	calls     atomic.Int64
	excluded  map[string]bool
	failOn    string
}

func (p *probeFS) Stat(name string) (fs.FileInfo, error) {
	p.calls.Add(1)
	n := p.cur.Add(1)
	defer p.cur.Add(-1)
	for {
		old := p.peak.Load()
		if n <= old || p.peak.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	switch {
	case name == p.failOn:
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrPermission}
	case p.excluded[name]:
		return nil, nil
	}
	return nil, fs.ErrNotExist
}

type probeSource struct {
	roots.PathSource
	fs *probeFS
}

func (s probeSource) FS(library.RootID) (library.RootFS, error) { return s.fs, nil }

func TestExcludedAmong_OrderedBoundedAndStrict(t *testing.T) {
	pfs := &probeFS{excluded: map[string]bool{}}
	var dirs []library.Path
	var want []bool
	for i := range 60 {
		rel := fmt.Sprintf("Author %02d", i)
		dirs = append(dirs, library.Path{Root: "books-library", Rel: rel})
		ex := i%3 == 0
		want = append(want, ex)
		if ex {
			pfs.excluded[rel+"/"+roots.ExclusionMarker] = true
		}
	}
	src := probeSource{fs: pfs}
	got, err := roots.ExcludedAmong(context.Background(), src, dirs)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("result %d = %v; want %v (results must stay in input order)", i, got[i], want[i])
		}
	}
	if peak := pfs.peak.Load(); peak > 8 || peak < 2 {
		t.Errorf("peak concurrent probes = %d; want bounded by 8 and actually parallel", peak)
	}

	// A permission error is an error, never "not excluded".
	pfs.failOn = "Author 17/" + roots.ExclusionMarker
	if _, err := roots.ExcludedAmong(context.Background(), src, dirs); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("ExcludedAmong with an unreadable marker = %v; want the permission error", err)
	}
	if ex, err := roots.Excluded(probeSource{fs: pfs}, library.Path{Root: "books-library", Rel: "Author 17"}); err == nil || ex {
		t.Errorf("Excluded with an unreadable marker = %v, %v", ex, err)
	}
}

func TestExcludedAmong_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := probeSource{fs: &probeFS{excluded: map[string]bool{}}}
	if _, err := roots.ExcludedAmong(ctx, src, []library.Path{{Root: "books-library", Rel: "A"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("ExcludedAmong on a cancelled context = %v", err)
	}
}

// A dead share fails every probe; the pool must stop launching after the first
// failure rather than Stat every directory in the listing.
func TestExcludedAmong_AnErrorStopsLaunchingProbes(t *testing.T) {
	pfs := &probeFS{excluded: map[string]bool{}}
	var dirs []library.Path
	for i := range 2000 {
		dirs = append(dirs, library.Path{Root: "books-library", Rel: fmt.Sprintf("Author %04d", i)})
	}
	pfs.failOn = "Author 0000/" + roots.ExclusionMarker
	if _, err := roots.ExcludedAmong(context.Background(), probeSource{fs: pfs}, dirs); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("ExcludedAmong = %v; want the permission error", err)
	}
	if n := pfs.calls.Load(); n >= 200 {
		t.Errorf("%d of %d directories were probed after the first one failed; want the pool cancelled", n, len(dirs))
	}
}
