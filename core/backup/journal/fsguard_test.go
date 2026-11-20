// file: core/backup/journal/fsguard_test.go
// version: 1.0.1
// guid: 7c1e5a93-2b48-4d06-9f7a-3e8d1c6b0a54
// last-edited: 2026-11-19

package journal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// readRecorder is an FS over the real one that records the reads (Lstat and
// ReadFile), which take no label and so never reach the crash kit's recorder.
type readRecorder struct {
	FS
	reads []string
}

func (r *readRecorder) Lstat(path string) (fs.FileInfo, error) {
	r.reads = append(r.reads, "Lstat "+filepath.Base(path))
	return r.FS.Lstat(path)
}

func (r *readRecorder) ReadFile(path string) ([]byte, error) {
	r.reads = append(r.reads, "ReadFile "+filepath.Base(path))
	return r.FS.ReadFile(path)
}

// TestMoveFile_ReadsGoThroughTheFS: the checks MoveFile makes before it writes
// (the source exists, the destination is not a link, a destination already
// there is the verified copy) and the read-back of its copy go through the
// FS it was given, so over the in-memory filesystem they read the same
// filesystem the writes land in. A read that went to package os would read the
// host's disk instead, and the crash cells would pass over a filesystem the
// writes never touched.
func TestMoveFile_ReadsGoThroughTheFS(t *testing.T) {
	fx := newFixture(t)
	fx.built(t)
	dest := filepath.Join(t.TempDir(), "offsite", "gotham_revocations.journal")
	rec := &readRecorder{FS: OSFS()}

	must(t, MoveFile(fx.path, dest, fx.keys, WithFS(rec)))
	// The second move finds the destination already there and compares it.
	must(t, MoveFile(fx.path, dest, fx.keys, WithFS(rec)))

	base := filepath.Base(dest)
	for _, want := range []string{
		"Lstat " + filepath.Base(fx.path), // the source exists
		"Lstat " + base,                   // the destination is not a link
		"ReadFile " + base,                // a destination already there is compared
		"ReadFile " + base + ".moving",    // the copy is read back before it is renamed
	} {
		if !slices.Contains(rec.reads, want) {
			t.Errorf("MoveFile never made the read %q through the FS; reads: %q", want, rec.reads)
		}
	}
}

// osAllowed are the only names of package os the package may use outside
// fsys.go: types, mode bits, the not-exist sentinel and the open flags (O_*).
// Anything else is a function that reaches the filesystem (or a new one that
// might): outside fsys.go (osFS, the real backend) the package reaches the
// filesystem only through an FS, because a direct call would bypass the seam
// and, over the in-memory filesystem, read or write the host's disk.
var osAllowed = []string{
	"ErrNotExist", "File", "FileMode", "ModeDevice", "ModeNamedPipe", "ModeSocket", "ModeSymlink",
}

// TestNoDirectOSFilesystemCalls: no file of the package but fsys.go uses a
// name of package os outside osAllowed. It parses the sources, so a call added to
// a new path (the read the kit cannot see) fails here and not at a boot that
// never ran the new path.
func TestNoDirectOSFilesystemCalls(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "fsys.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" && !slices.Contains(osAllowed, sel.Sel.Name) && !strings.HasPrefix(sel.Sel.Name, "O_") {
				t.Errorf("%s: os.%s is not an allowed name of package os here; go through the FS (fsys.go is the one file that calls os)", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if checked < 8 {
		t.Fatalf("parsed %d source files; the guard is not looking at the package", checked)
	}
}
