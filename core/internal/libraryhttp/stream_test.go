// file: core/internal/libraryhttp/stream_test.go
// version: 1.1.0
// guid: 0c47be12-6d3a-4e18-b0f2-83a51d9ce7a4
// last-edited: 2026-11-11

package libraryhttp

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/library"
)

// fakeDirEntry is a directory entry of a given type.
type fakeDirEntry struct {
	name string
	typ  fs.FileMode
}

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return e.typ.IsDir() }
func (e fakeDirEntry) Type() fs.FileMode          { return e.typ }
func (e fakeDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("Info is never asked") }

// fakeDir is a directory that hands its entries out chunk by chunk and counts
// what was asked of it, so a test can prove the listing stops reading.
type fakeDir struct {
	ents     []fs.DirEntry
	next     int
	calls    int
	returned int
	failAt   int // when > 0, the call with this number (1-based) fails
	err      error
}

func (d *fakeDir) Stat() (fs.FileInfo, error) { return nil, errors.New("not used") }
func (d *fakeDir) Read([]byte) (int, error)   { return 0, errors.New("not used") }
func (d *fakeDir) Close() error               { return nil }

func (d *fakeDir) ReadDir(n int) ([]fs.DirEntry, error) {
	d.calls++
	if n <= 0 {
		panic("readDirs must read in chunks, never the whole directory at once")
	}
	if d.failAt > 0 && d.calls == d.failAt {
		return nil, d.err
	}
	if d.next >= len(d.ents) {
		return nil, io.EOF
	}
	end := min(d.next+n, len(d.ents))
	out := d.ents[d.next:end]
	d.next = end
	d.returned += len(out)
	return out, nil
}

func dirs(names ...string) []fs.DirEntry {
	out := make([]fs.DirEntry, len(names))
	for i, n := range names {
		out[i] = fakeDirEntry{n, fs.ModeDir}
	}
	return out
}

func entryNames(es []dirEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}

func plainSpec() library.RootSpec {
	return library.RootSpec{ID: "books-library", AppDir: ".mediacore", Quarantine: ".mediacore/quarantine"}
}

// A directory far larger than the cap is not read whole: the read stops at the
// chunk in which one entry more than the cap was kept.
func TestReadDirsStopsReadingAtTheCap(t *testing.T) {
	var all []fs.DirEntry
	for i := range 100_000 {
		all = append(all, fakeDirEntry{fmt.Sprintf("gargoyle-%06d", i), fs.ModeDir})
	}
	d := &fakeDir{ents: all}
	got, truncated, err := readDirs(context.Background(), d, plainSpec(), "", 3, maxExamined)
	if err != nil || !truncated || len(got) != 3 {
		t.Fatalf("readDirs = %d entries, truncated %v, %v", len(got), truncated, err)
	}
	if d.calls != 1 || d.returned != dirReadChunk {
		t.Errorf("read %d entries in %d calls, want one chunk of %d", d.returned, d.calls, dirReadChunk)
	}
}

// Entries the picker does not show do not count toward the cap, and the read
// goes on until enough shown ones have been kept: here the fourth shown entry is
// in the third chunk.
func TestReadDirsCountsOnlyShownEntries(t *testing.T) {
	var all []fs.DirEntry
	for i := range 3 * dirReadChunk {
		switch {
		case i == 10, i == dirReadChunk+10, i == 2*dirReadChunk+10, i == 2*dirReadChunk+20:
			all = append(all, fakeDirEntry{fmt.Sprintf("shown-%d", i), fs.ModeDir})
		case i%3 == 0:
			all = append(all, fakeDirEntry{fmt.Sprintf("file-%d.m4b", i), 0})
		case i%3 == 1:
			all = append(all, fakeDirEntry{fmt.Sprintf("link-%d", i), fs.ModeSymlink})
		default:
			all = append(all, fakeDirEntry{fmt.Sprintf(".hidden-%d", i), fs.ModeDir})
		}
	}
	d := &fakeDir{ents: all}
	got, truncated, err := readDirs(context.Background(), d, plainSpec(), "Gotham", 3, maxExamined)
	if err != nil || !truncated || len(got) != 3 {
		t.Fatalf("readDirs = %v, truncated %v, %v", entryNames(got), truncated, err)
	}
	if d.calls != 3 {
		t.Errorf("calls = %d, want 3 (the fourth shown entry is in the third chunk)", d.calls)
	}
	if got[0].Name != "shown-10" || got[0].Rel != "Gotham/shown-10" || !got[0].IsDir {
		t.Errorf("first entry = %+v", got[0])
	}
}

// Exactly at the cap is complete, not truncated; the read goes to the end of the
// directory to know that. Entries come back sorted whatever the disk's order.
func TestReadDirsExactFitAndSortedOutput(t *testing.T) {
	d := &fakeDir{ents: dirs("Zatanna", "Arkham", "Metropolis", "Gotham")}
	got, truncated, err := readDirs(context.Background(), d, plainSpec(), "", 4, maxExamined)
	if err != nil || truncated {
		t.Fatalf("at the cap: truncated %v, %v", truncated, err)
	}
	if want := []string{"Arkham", "Gotham", "Metropolis", "Zatanna"}; !slices.Equal(entryNames(got), want) {
		t.Errorf("names = %v, want %v", entryNames(got), want)
	}
	if d.returned != 4 || d.calls != 2 {
		t.Errorf("read %d entries in %d calls, want all four and the EOF call", d.returned, d.calls)
	}
	// One over the cap truncates, and the kept ones are still sorted: the first
	// three in the disk's order.
	d = &fakeDir{ents: dirs("Zatanna", "Arkham", "Metropolis", "Gotham")}
	got, truncated, err = readDirs(context.Background(), d, plainSpec(), "", 3, maxExamined)
	if err != nil || !truncated {
		t.Fatalf("over the cap: truncated %v, %v", truncated, err)
	}
	if want := []string{"Arkham", "Metropolis", "Zatanna"}; !slices.Equal(entryNames(got), want) {
		t.Errorf("names = %v, want the first three in disk order, sorted: %v", entryNames(got), want)
	}
}

// The root's own areas and dot directories are never listed, by any spelling
// of the case, and an empty directory lists as an empty array.
func TestReadDirsHidesTheAreas(t *testing.T) {
	sp := library.RootSpec{ID: "books-nested", AppDir: "state/app", Quarantine: "state/held"}
	d := &fakeDir{ents: dirs("app", "APP", "held", "kept", ".git")}
	got, truncated, err := readDirs(context.Background(), d, sp, "state", 10, maxExamined)
	if err != nil || truncated {
		t.Fatal(truncated, err)
	}
	if want := []string{"kept"}; !slices.Equal(entryNames(got), want) {
		t.Errorf("names under the state directory = %v, want %v", entryNames(got), want)
	}
	empty, truncated, err := readDirs(context.Background(), &fakeDir{}, plainSpec(), "", 5, maxExamined)
	if err != nil || truncated || empty == nil || len(empty) != 0 {
		t.Errorf("an empty directory = %#v, truncated %v, %v; want an empty, non-nil slice", empty, truncated, err)
	}
}

// A read error after some entries fails the listing: a partial listing is never
// served as a complete one.
func TestReadDirsPropagatesAReadError(t *testing.T) {
	boom := errors.New("input/output error")
	var all []fs.DirEntry
	for i := range 2 * dirReadChunk {
		all = append(all, fakeDirEntry{fmt.Sprintf("d-%d", i), fs.ModeDir})
	}
	got, truncated, err := readDirs(context.Background(), &fakeDir{ents: all, failAt: 2, err: boom}, plainSpec(), "", maxEntries, maxExamined)
	if !errors.Is(err, boom) || got != nil || truncated {
		t.Errorf("readDirs = %v, %v, %v; want the read error and nothing else", got, truncated, err)
	}
}

// A request-driven read never takes core's unguarded view. The picker reads a
// directory through Root.GuardedFS and nothing in this package calls Root.FS;
// core.CoreOwner appears once, as the registry filter's constant. A call to FS
// or a stray CoreOwner is a request reading as core: this scan fails on either.
func TestRequestReadsNeverTakeAnUnguardedView(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources found: %v", err)
	}
	fset := token.NewFileSet()
	var guarded, coreOwner int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "FS":
						t.Errorf("%s: a call to .FS: a request must read through GuardedFS", fset.Position(x.Pos()))
					case "GuardedFS":
						guarded++
					}
				}
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "library" && x.Sel.Name == "CoreOwner" {
					coreOwner++
				}
			}
			return true
		})
	}
	if guarded != 1 {
		t.Errorf("GuardedFS is called %d times, want once (the browse handler)", guarded)
	}
	if coreOwner != 1 {
		t.Errorf("library.CoreOwner appears %d times, want once (registryCaller)", coreOwner)
	}
}

type plainFile struct{}

func (plainFile) Name() string               { return "Batman Begins.m4b" }
func (plainFile) IsDir() bool                { return false }
func (plainFile) Type() fs.FileMode          { return 0 }
func (plainFile) Info() (fs.FileInfo, error) { return nil, io.EOF }

// The review probe: a directory of millions of plain files keeps nothing, so
// the keep-count cap never fired and the whole directory was read.
func TestReadDirsStopsAfterExaminingTheCap(t *testing.T) {
	d := &fakeFiles{n: 2_000_000}
	entries, truncated, err := readDirs(context.Background(), d, library.RootSpec{}, "", maxEntries, maxExamined)
	if err != nil || !truncated || len(entries) != 0 {
		t.Fatalf("readDirs = %d entries, truncated %v, %v", len(entries), truncated, err)
	}
	if d.served > maxExamined+dirReadChunk {
		t.Errorf("read %d entries of %d; the examination cap is %d", d.served, d.n, maxExamined)
	}
	// A directory just under the cap is read whole and is not truncated.
	d = &fakeFiles{n: maxExamined}
	if _, truncated, err := readDirs(context.Background(), d, library.RootSpec{}, "", maxEntries, maxExamined); err != nil || truncated {
		t.Errorf("a directory of exactly the cap: truncated %v, %v", truncated, err)
	}
}

// A client that has gone stops the read at the next chunk.
func TestReadDirsStopsWhenTheRequestIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &fakeFiles{n: 1_000_000, onRead: func(served int) {
		if served >= 4*dirReadChunk {
			cancel()
		}
	}}
	_, _, err := readDirs(ctx, d, library.RootSpec{}, "", maxEntries, maxExamined)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("readDirs = %v, want context.Canceled", err)
	}
	if d.served > 5*dirReadChunk {
		t.Errorf("read %d entries after the request was cancelled", d.served)
	}
}

type fakeFiles struct {
	n, served int
	onRead    func(served int)
}

func (f *fakeFiles) Stat() (fs.FileInfo, error) { return nil, io.EOF }
func (f *fakeFiles) Read([]byte) (int, error)   { return 0, io.EOF }
func (f *fakeFiles) Close() error               { return nil }
func (f *fakeFiles) ReadDir(n int) ([]fs.DirEntry, error) {
	if f.served >= f.n {
		return nil, io.EOF
	}
	k := min(n, f.n-f.served)
	f.served += k
	if f.onRead != nil {
		f.onRead(f.served)
	}
	out := make([]fs.DirEntry, k)
	for i := range out {
		out[i] = plainFile{}
	}
	return out, nil
}
