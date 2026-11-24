// file: internal/arch/server_no_exit_guard_test.go
// version: 0.1.0
// guid: 7e3a9c1d-5b28-4f64-a0d7-c91e46b2f853
// last-edited: 2026-11-22

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
	"strconv"
	"strings"
	"testing"
)

// serverNoExitRoot is the tree whose non-test files must never end the process.
// internal/server is a library the books module builds (NewServer), so an exit
// from inside it kills the whole process with no unwind of the kernel: an ABS
// misconfiguration did exactly that, silently, until NewServer was made to
// return its error (F4-08 B10w). The process is ended by the composition root
// (cmd), which reads the error it is handed.
const serverNoExitRoot = "internal/server"

// exitOffence is one reference to a call that ends the process.
type exitOffence struct {
	file string
	line int
	what string
}

func (o exitOffence) String() string { return fmt.Sprintf("%s:%d %s", o.file, o.line, o.what) }

// exitingSelectors are the package-qualified references that end the process:
// the import path and the selector name. A reference counts whether or not it
// is called (`exit := os.Exit` is as much an exit as `os.Exit(1)`).
var exitingSelectors = map[string][]string{
	"os":      {"Exit"},
	"syscall": {"Exit"},
	"log":     {"Fatal", "Fatalf", "Fatalln"},
}

// fatalMethods are method names that, on a logger, log and exit. A file that
// imports "testing" is a test helper kept outside a _test.go file, and a
// testing.TB's Fatal ends only the test, so such a file is exempt from this
// half (the package-qualified half still applies to it).
var fatalMethods = []string{"Fatal", "Fatalf", "Fatalln", "Fatalw"}

// exitOffences parses src as the Go file named name and returns every
// reference in code (comments are not code) that ends the process.
func exitOffences(name, src string) ([]exitOffence, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	// local name -> import path, for the packages exitingSelectors names. An
	// aliased import (stdos "os") is the same package under another name.
	locals := map[string]string{}
	importsTesting := false
	for _, imp := range f.Imports {
		path, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			return nil, uerr
		}
		if path == "testing" {
			importsTesting = true
		}
		if _, ok := exitingSelectors[path]; !ok {
			continue
		}
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		locals[local] = path
	}
	var out []exitOffence
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pos := fset.Position(sel.Pos())
		if id, ok := sel.X.(*ast.Ident); ok {
			if path, ok := locals[id.Name]; ok && slices.Contains(exitingSelectors[path], sel.Sel.Name) {
				out = append(out, exitOffence{name, pos.Line, id.Name + "." + sel.Sel.Name + " (package " + path + ")"})
				return true
			}
		}
		if !importsTesting && slices.Contains(fatalMethods, sel.Sel.Name) {
			out = append(out, exitOffence{name, pos.Line, "a ." + sel.Sel.Name + " call: a logger's Fatal exits the process"})
		}
		return true
	})
	return out, nil
}

// TestServerNeverExitsTheProcess pins that no non-test file under
// internal/server ends the process: no os.Exit, no syscall.Exit, no log.Fatal*
// and no logger's Fatal*. A refusal is a returned error; a request-time failure
// is a logged 5xx. If this fails, the file:line below is an exit that kills the
// process from inside a library (see serverNoExitRoot); make it a returned
// error with the cause instead of adding it to a list.
func TestServerNeverExitsTheProcess(t *testing.T) {
	root := repoRoot(t)
	var offences []exitOffence
	files := 0
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(serverNoExitRoot)), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		found, perr := exitOffences(filepath.ToSlash(rel), string(raw))
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		files++
		offences = append(offences, found...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 100 {
		t.Fatalf("scanned %d non-test files under %s: the walk is not reaching the server tree, so a pass would prove nothing", files, serverNoExitRoot)
	}
	for _, o := range offences {
		t.Errorf("%s", o)
	}
	if len(offences) > 0 {
		t.Errorf("%d reference(s) under %s end the process; return an error naming the cause instead (NewServer returns one)", len(offences), serverNoExitRoot)
	}
}

// TestExitOffences_Detector pins the detector itself: each form that ends the
// process is found, and the code that only talks about one is not. Without it
// the guard above could go blind (an unrecognised alias, a comment mistaken for
// code) and still pass.
func TestExitOffences_Detector(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"os.Exit", "package p\nimport \"os\"\nfunc f() { os.Exit(1) }\n", 1},
		{"aliased os", "package p\nimport stdos \"os\"\nfunc f() { stdos.Exit(1) }\n", 1},
		{"os.Exit as a value", "package p\nimport \"os\"\nvar exit = os.Exit\n", 1},
		{"syscall.Exit", "package p\nimport \"syscall\"\nfunc f() { syscall.Exit(2) }\n", 1},
		{"log.Fatalf", "package p\nimport \"log\"\nfunc f() { log.Fatalf(\"x\") }\n", 1},
		{"logger Fatal", "package p\ntype l struct{}\nfunc (l) Fatal(string) {}\nfunc f(x l) { x.Fatal(\"x\") }\n", 1},
		{"two in one file", "package p\nimport \"os\"\nfunc f() { os.Exit(1); os.Exit(2) }\n", 2},
		{"comment only", "package p\n// os.Exit(1) and log.Fatal(x) in prose\nfunc f() {}\n", 0},
		{"string only", "package p\nvar s = \"os.Exit(1)\"\n", 0},
		{"os without Exit", "package p\nimport \"os\"\nfunc f() { _, _ = os.Stat(\"x\") }\n", 0},
		{"a local named os", "package p\ntype o struct{}\nfunc (o) Exit() {}\nfunc f() { var os o; os.Exit() }\n", 0},
		{"t.Fatal in a test helper file", "package p\nimport \"testing\"\nfunc f(t *testing.T) { t.Fatal(\"x\") }\n", 0},
		{"os.Exit in a test helper file still counts", "package p\nimport (\"os\"; \"testing\")\nfunc f(t *testing.T) { os.Exit(1) }\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exitOffences("x.go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Fatalf("exitOffences found %d (%v), want %d", len(got), got, tc.want)
			}
		})
	}
}
