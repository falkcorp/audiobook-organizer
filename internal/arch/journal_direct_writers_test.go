// file: internal/arch/journal_direct_writers_test.go
// version: 1.0.0
// guid: 5b2f9a6e-1d84-4c37-a0e3-7c91d2f4b685
// last-edited: 2026-10-30

package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A journal row (database.OperationChange) is created through
// database.RecordOperationChange(s), which refuses a change type nobody has
// classified as user or library data (database.CheckChangeTypeClassified).
// This guard keeps it the only way: a non-test file that selects
// CreateOperationChange or CreateOperationChanges, as a call or as a method
// value, bypasses the check, so it must be one of the files below.

// journalWriteMethods are the store methods that create a journal row.
var journalWriteMethods = map[string]bool{
	"CreateOperationChange":  true,
	"CreateOperationChanges": true,
}

// journalHelperFile holds RecordOperationChange(s), the checked path. The scan
// must see it call the store, or the scan is not reading what it thinks it is.
const journalHelperFile = "internal/database/change_type_class.go"

// journalStoreDir is the package that implements and wraps the store methods
// (the Pebble store, the in-memory store, decorators, the helper): it is where
// the method lives, not a writer of rows.
const journalStoreDir = "internal/database"

// journalDirectAllowed lists the files outside journalStoreDir that may select
// a journal write method, each with why. Adding a file here needs the same
// review as adding a change type to the classification table.
var journalDirectAllowed = map[string]string{
	"internal/operations/opswriter/writer.go": "records the op-history rows AFTER the write committed, so an " +
		"unclassified type must be logged and counted (History.failed), not refused; its types are the opswriter's " +
		"own (book_file_modify, book_authors_modify, effect:<name>)",
	"pkg/ops/opstest/world.go": "the in-memory test world forwards the store method it implements; it creates no row of its own",
}

// scanJournalDirectWrites returns, for every non-test Go file under root that
// selects a journal write method, the file's slash path relative to root and
// the number of selections. It reads the directory tree, not go list's file
// set, so a file behind a build tag or another GOOS, a cgo file and the root
// main package are all scanned. Generated mocks are skipped: they define the
// methods, they do not call them. An interface's method spec and a method
// declaration are not selections; a call or a method value is.
func scanJournalDirectWrites(t *testing.T, root string) (hits map[string]int, files int) {
	t.Helper()
	hits = map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "vendor", "web", ".standards", "mocks":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		files++
		n := 0
		ast.Inspect(f, func(node ast.Node) bool {
			if sel, ok := node.(*ast.SelectorExpr); ok && journalWriteMethods[sel.Sel.Name] {
				n++
			}
			return true
		})
		if n > 0 {
			r, _ := filepath.Rel(root, path)
			hits[filepath.ToSlash(r)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits, files
}

// TestJournalRowsAreCreatedThroughTheClassificationCheck: no non-test file
// outside internal/database and journalDirectAllowed selects a journal write
// method. A new journal writer calls database.RecordOperationChange (or goes
// through repairs.Writer, which does), so the classification check runs for it.
func TestJournalRowsAreCreatedThroughTheClassificationCheck(t *testing.T) {
	root := repoRoot(t)
	hits, files := scanJournalDirectWrites(t, root)
	if files < 500 {
		t.Fatalf("the scan is broken, not the code: it read %d Go files", files)
	}
	if hits[journalHelperFile] == 0 {
		t.Fatalf("the scan is broken, not the code: %s, which holds the checked path, was not seen calling the store",
			journalHelperFile)
	}
	var offenders []string
	for file, n := range hits {
		if strings.HasPrefix(file, journalStoreDir+"/") {
			continue
		}
		if _, ok := journalDirectAllowed[file]; ok {
			continue
		}
		offenders = append(offenders, fmt.Sprintf("%s (%d selections)", file, n))
	}
	slices.Sort(offenders)
	for _, o := range offenders {
		t.Errorf("%s creates a journal row without the classification check: call database.RecordOperationChange(store, row) "+
			"(or database.RecordOperationChanges for a batch); an unclassified change type is refused there, "+
			"and a new type is classified in internal/database/change_type_class.go", o)
	}
	for file := range journalDirectAllowed {
		if hits[file] == 0 {
			t.Errorf("journalDirectAllowed names %s, which no longer selects a journal write method: remove the entry", file)
		}
	}
}

// TestJournalDirectWriteScanSeesWhatItShould: the scan reports a call, a
// method value and a batch call in a file behind a build tag, ignores a test
// file, a generated mock, an interface method spec, a method declaration and a
// comment, and so proves the guard above can fail.
func TestJournalDirectWriteScanSeesWhatItShould(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("w/call.go", "package w\n\nfunc f(s interface{ CreateOperationChange(int) error }) error { return s.CreateOperationChange(1) }\n")
	write("w/value.go", "package w\n\nvar fn = (*T).CreateOperationChanges\n\ntype T struct{}\n\nfunc (*T) CreateOperationChanges([]int) error { return nil }\n")
	write("w/tagged.go", "//go:build never\n\npackage w\n\nfunc g(s T) { s.CreateOperationChange(2) }\n")
	write("w/iface.go", "package w\n\n// CreateOperationChange is only named here.\ntype I interface{ CreateOperationChange(int) error }\n\n"+
		"func (T) CreateOperationChange(int) error { return nil }\n")
	write("w/call_test.go", "package w\n\nfunc h(s T) { s.CreateOperationChange(3) }\n")
	write("mocks/mock.go", "package mocks\n\nfunc m(s interface{ CreateOperationChange(int) error }) { s.CreateOperationChange(4) }\n")

	hits, files := scanJournalDirectWrites(t, dir)
	if files != 4 {
		t.Errorf("read %d files, want 4 (call.go, value.go, tagged.go, iface.go)", files)
	}
	want := map[string]int{"w/call.go": 1, "w/value.go": 1, "w/tagged.go": 1}
	if len(hits) != len(want) {
		t.Fatalf("reported %v, want %v", hits, want)
	}
	for file, n := range want {
		if hits[file] != n {
			t.Errorf("reported %v, want %v", hits, want)
		}
	}
}
