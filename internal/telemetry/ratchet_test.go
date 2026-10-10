// file: internal/telemetry/ratchet_test.go
// version: 1.0.0
// guid: 2b8e6f10-3c4d-4a7e-9f15-a0b1c2d3e4f5
// last-edited: 2026-10-10

package telemetry

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The client_golang ratchet: the number of client_golang metric families
// declared in non-test code may only fall. New families are OTel instruments
// (telemetry.Meter). The baseline lives in testdata/client_golang_baseline.txt
// and is lowered by hand in the PR that migrates a family; it is never raised.

const (
	promImportPath     = "github.com/prometheus/client_golang/prometheus"
	promautoImportPath = "github.com/prometheus/client_golang/prometheus/promauto"
	pebbleDescHelper   = "newPebbleDesc"
)

var ratchetConstructors = map[string]bool{
	"NewCounter": true, "NewCounterVec": true, "NewCounterFunc": true,
	"NewGauge": true, "NewGaugeVec": true, "NewGaugeFunc": true,
	"NewHistogram": true, "NewHistogramVec": true,
	"NewSummary": true, "NewSummaryVec": true,
	"NewUntyped": true,
}

// localImportName returns the identifier a file uses for the import path, or
// "" when it is not imported (or imported as _ or .).
func localImportName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		return filepath.Base(path)
	}
	return ""
}

// countFileFamilies counts the declared families in one parsed file.
func countFileFamilies(f *ast.File) int {
	prom := localImportName(f, promImportPath)
	promauto := localImportName(f, promautoImportPath)
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		// The one sanctioned NewDesc lives inside the helper; its callers are
		// counted instead.
		if fd, ok := node.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == pebbleDescHelper {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == pebbleDescHelper && len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					n++
				}
			}
		case *ast.SelectorExpr:
			switch x := fn.X.(type) {
			case *ast.Ident:
				if prom != "" && x.Name == prom && (ratchetConstructors[fn.Sel.Name] || fn.Sel.Name == "NewDesc") {
					n++
				} else if promauto != "" && x.Name == promauto && ratchetConstructors[fn.Sel.Name] {
					n++
				}
			case *ast.CallExpr: // promauto.With(reg).NewCounter(...)
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && promauto != "" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == promauto && sel.Sel.Name == "With" && ratchetConstructors[fn.Sel.Name] {
						n++
					}
				}
			}
		}
		return true
	})
	return n
}

// countClientGolangFamilies walks internal, pkg and cmd under root and returns
// the total and the per-file counts (relative, slash-separated paths).
func countClientGolangFamilies(root string) (int, map[string]int, error) {
	total, per, _, err := countClientGolangFamiliesScanned(root)
	return total, per, err
}

// countClientGolangFamiliesScanned is countClientGolangFamilies plus the number
// of .go files parsed, so a walk that silently matches nothing can be caught.
func countClientGolangFamiliesScanned(root string) (int, map[string]int, int, error) {
	per := map[string]int{}
	total, scanned := 0, 0
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "pkg", "cmd"} {
		dir := filepath.Join(root, top)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if path != dir && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules" || name == "web") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return fmt.Errorf("parse %s: %w", path, perr)
			}
			scanned++
			if c := countFileFamilies(f); c > 0 {
				rel, _ := filepath.Rel(root, path)
				per[filepath.ToSlash(rel)] = c
				total += c
			}
			return nil
		})
		if err != nil {
			return 0, nil, 0, err
		}
	}
	return total, per, scanned, nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test working directory")
		}
		dir = parent
	}
}

func readRatchetBaseline(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "client_golang_baseline.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<!--") {
			continue
		}
		n, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("baseline line %q is not a number: %v", line, err)
		}
		return n
	}
	t.Fatal("baseline file has no number")
	return 0
}

// ratchetRose reports whether count exceeds the baseline. The ratchet is
// one-way: a fall never fails (owner decision D47).
func ratchetRose(count, baseline int) bool { return count > baseline }

func TestClientGolangConstructorRatchet(t *testing.T) {
	baseline := readRatchetBaseline(t)
	count, per, err := countClientGolangFamilies(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("client_golang families: %d (baseline %d)", count, baseline)
	if ratchetRose(count, baseline) {
		files := make([]string, 0, len(per))
		for f := range per {
			files = append(files, f)
		}
		sort.Strings(files)
		var sb strings.Builder
		for _, f := range files {
			fmt.Fprintf(&sb, "\n  %s: %d", f, per[f])
		}
		t.Fatalf("client_golang families rose to %d, above the baseline %d. A new client_golang family is not allowed; use telemetry.Meter(...) (internal/telemetry/meter.go) and add the family to internal/telemetry/contract/testdata/series.golden. Files with families:%s", count, baseline, sb.String())
	}
	if count < baseline {
		t.Logf("count fell to %d: lower internal/telemetry/testdata/client_golang_baseline.txt to %d in this PR", count, count)
	}
}

func writeFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureTree(t *testing.T) string {
	root := t.TempDir()
	writeFixture(t, root, "internal/a/a.go", `package a
import prom "github.com/prometheus/client_golang/prometheus"
var c = prom.NewCounterVec(prom.CounterOpts{Name: "a_total"}, nil)
`)
	writeFixture(t, root, "internal/b/b.go", `package b
import "github.com/prometheus/client_golang/prometheus/promauto"
var g = promauto.NewGauge(promauto.Opts{})
var h = promauto.With(nil).NewHistogram(nil)
`)
	writeFixture(t, root, "pkg/c/c.go", `package c
import "github.com/prometheus/client_golang/prometheus"
func newPebbleDesc(name string) any { return prometheus.NewDesc(name, "", nil, nil) }
var d = newPebbleDesc("x")
var e = prometheus.NewDesc("y", "", nil, nil)
func f(n string) any { return newPebbleDesc(n) } // non-literal: not counted
`)
	// Ignored: test file, testdata dir, file without the import.
	writeFixture(t, root, "internal/a/a_test.go", `package a
import "github.com/prometheus/client_golang/prometheus"
var x = prometheus.NewCounter(prometheus.CounterOpts{})
`)
	writeFixture(t, root, "internal/a/testdata/t.go", `package t
import "github.com/prometheus/client_golang/prometheus"
var x = prometheus.NewCounter(prometheus.CounterOpts{})
`)
	writeFixture(t, root, "cmd/m/m.go", `package m
type fake struct{}
func (fake) NewCounter() {}
var prometheus fake
func init() { prometheus.NewCounter() }
`)
	return root
}

func TestRatchetCounter_FixtureTree(t *testing.T) {
	root := fixtureTree(t)
	got, per, err := countClientGolangFamilies(root)
	if err != nil {
		t.Fatal(err)
	}
	// a.go 1, b.go 2, c.go: literal helper call 1 + stray NewDesc 1.
	if got != 5 {
		t.Fatalf("fixture count = %d, want 5 (per file: %v)", got, per)
	}
	want := map[string]int{"internal/a/a.go": 1, "internal/b/b.go": 2, "pkg/c/c.go": 2}
	for f, n := range want {
		if per[f] != n {
			t.Errorf("%s: got %d, want %d", f, per[f], n)
		}
	}
}

func TestRatchet_FailsOnOneMore(t *testing.T) {
	root := fixtureTree(t)
	base, _, err := countClientGolangFamilies(root)
	if err != nil {
		t.Fatal(err)
	}
	if ratchetRose(base, base) {
		t.Fatal("equal count must not be a rise")
	}
	writeFixture(t, root, "internal/z/z.go", `package z
import "github.com/prometheus/client_golang/prometheus"
var z = prometheus.NewCounter(prometheus.CounterOpts{Name: "z_total"})
`)
	more, _, err := countClientGolangFamilies(root)
	if err != nil {
		t.Fatal(err)
	}
	if more != base+1 || !ratchetRose(more, base) {
		t.Fatalf("one added constructor: count %d (base %d), rose=%v", more, base, ratchetRose(more, base))
	}
	if ratchetRose(base-1, base) {
		t.Fatal("a fall must not be a rise")
	}
}

func TestRatchet_ScansRealTree(t *testing.T) {
	_, per, scanned, err := countClientGolangFamiliesScanned(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 300 {
		t.Fatalf("scanned only %d .go files; the walk is not reaching the tree", scanned)
	}
	if per["internal/metrics/pebble_collector.go"] != 25 {
		t.Errorf("pebble_collector.go count = %d, want 25", per["internal/metrics/pebble_collector.go"])
	}
}
