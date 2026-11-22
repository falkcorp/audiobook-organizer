// file: internal/arch/config_cow_guard_test.go
// version: 0.1.0
// guid: 9c4e1a7b-2d58-4f30-a6e9-5b8d3c1f7e02
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
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The copy-on-write config (docs/plans/platform-core/config-cow.md) publishes
// an immutable *config.Config from config.Get(). Go cannot make a pointer
// read-only, so these are the writes that compile and must not exist:
//
//   - assigning through it:         config.Get().X = v, cfg.X++ (cfg := config.Get())
//   - taking a field's address:     &config.Get().X
//   - mutating a slice or map in it: append/copy/clear/delete, sort.*, slices.Sort*
//   - calling a pointer-receiver method that changes its receiver
//     (ITunesConfig.Resolve and Config.Normalize; Config.Validate and Problems
//     no longer do, see Normalized)
//
// plus the test-only setters (SetForTest, ReplaceForTest) in production code.
// The check is syntactic and follows one level of aliasing (a local assigned
// from config.Get()); a write through a deeper alias is caught at run time by
// the seal (internal/config/cow.go) under go test.

// cowMutatingMethods are pointer-receiver config methods that change their
// receiver. Calling one on a snapshot writes into it.
var cowMutatingMethods = map[string]bool{"Resolve": true, "Normalize": true}

// cowTestOnly are the setters only tests (and internal/testutil) may call.
var cowTestOnly = map[string]bool{"SetForTest": true, "ReplaceForTest": true}

var cowSortMutators = map[string]map[string]bool{
	"sort": {"Strings": true, "Ints": true, "Float64s": true, "Slice": true, "SliceStable": true, "Sort": true, "Stable": true},
	"slices": {"Sort": true, "SortFunc": true, "SortStableFunc": true, "Reverse": true, "Delete": true, "DeleteFunc": true,
		"Insert": true, "Replace": true, "Compact": true, "CompactFunc": true, "Grow": true, "Clip": true},
}

func TestConfigCOWNoWritesThroughGet(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	var hits []string
	files := 0
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "testdata", "vendor", ".worktrees":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/config/") || strings.HasPrefix(rel, "internal/arch/") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			files++
			isTest := strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "internal/testutil/")
			for _, h := range cowViolations(fset, f, isTest) {
				hits = append(hits, rel+":"+h)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 500 {
		t.Fatalf("scanned only %d files; the walk is broken", files)
	}
	sort.Strings(hits)
	for _, h := range hits {
		t.Error(h)
	}
}

// cowViolations returns "line: what" for every forbidden use in f.
func cowViolations(fset *token.FileSet, f *ast.File, isTest bool) []string {
	local := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == modulePath+"/internal/config" {
			local = "config"
			if imp.Name != nil {
				local = imp.Name.Name
			}
		}
	}
	if local == "" || local == "_" || local == "." {
		return nil
	}
	var out []string
	report := func(n ast.Node, what string) {
		out = append(out, fmt.Sprintf("%d: %s", fset.Position(n.Pos()).Line, what))
	}
	isGetCall := func(e ast.Expr) bool {
		c, ok := e.(*ast.CallExpr)
		if !ok || len(c.Args) != 0 {
			return false
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Get" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == local
	}

	if !isTest {
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && cowTestOnly[sel.Sel.Name] {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
					report(n, local+"."+sel.Sel.Name+" is test-only; production code writes the config with Mutate/MutateChecked")
				}
			}
			return true
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Body != nil {
			out = append(out, cowFuncViolations(fset, fn.Body, isGetCall, nil)...)
			return false
		}
		if fl, ok := n.(*ast.FuncLit); ok {
			out = append(out, cowFuncViolations(fset, fl.Body, isGetCall, nil)...)
			return false
		}
		return true
	})
	return out
}

// cowFuncViolations checks one function body. aliases are the locals assigned
// directly from config.Get() in it or in an enclosing function (a closure sees
// its parent's locals).
func cowFuncViolations(fset *token.FileSet, body *ast.BlockStmt, isGetCall func(ast.Expr) bool, inherited map[string]bool) []string {
	aliases := map[string]bool{}
	for k := range inherited {
		aliases[k] = true
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) == len(s.Rhs) {
				for i, r := range s.Rhs {
					if id, ok := s.Lhs[i].(*ast.Ident); ok && isGetCall(r) {
						aliases[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			if len(s.Names) == len(s.Values) {
				for i, v := range s.Values {
					if isGetCall(v) {
						aliases[s.Names[i].Name] = true
					}
				}
			}
		}
		return true
	})
	// rooted reports whether e is an access chain into a snapshot, and how many
	// selectors/indexes deep (0: the snapshot pointer itself).
	rooted := func(e ast.Expr) (bool, int) {
		depth := 0
		for {
			if isGetCall(e) {
				return true, depth
			}
			switch x := e.(type) {
			case *ast.Ident:
				return aliases[x.Name], depth
			case *ast.SelectorExpr:
				e = x.X
			case *ast.IndexExpr:
				e = x.X
			case *ast.ParenExpr:
				e = x.X
				continue
			case *ast.StarExpr:
				e = x.X
			default:
				return false, 0
			}
			depth++
		}
	}
	var out []string
	report := func(n ast.Node, what string) {
		out = append(out, fmt.Sprintf("%d: %s", fset.Position(n.Pos()).Line, what))
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.FuncLit:
			out = append(out, cowFuncViolations(fset, s.Body, isGetCall, aliases)...)
			return false
		case *ast.AssignStmt:
			for _, l := range s.Lhs {
				if ok, d := rooted(l); ok && d > 0 {
					report(s, "assignment into a config.Get() snapshot; use Mutate (or SetForTest in a test)")
				}
			}
		case *ast.IncDecStmt:
			if ok, d := rooted(s.X); ok && d > 0 {
				report(s, "increment of a config.Get() snapshot field; use Mutate (or SetForTest in a test)")
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{s.Key, s.Value} {
				if e == nil || s.Tok != token.ASSIGN {
					continue
				}
				if ok, d := rooted(e); ok && d > 0 {
					report(s, "range assigns into a config.Get() snapshot")
				}
			}
		case *ast.UnaryExpr:
			if s.Op == token.AND {
				if ok, d := rooted(s.X); ok && d > 0 {
					report(s, "address of a config.Get() snapshot field; copy the value, or take a config.Source")
				}
			}
		case *ast.CallExpr:
			if sel, ok := s.Fun.(*ast.SelectorExpr); ok {
				if cowMutatingMethods[sel.Sel.Name] {
					if ok, _ := rooted(sel.X); ok {
						report(s, sel.Sel.Name+" changes its receiver; call it on a Clone()")
					}
				}
				if id, ok := sel.X.(*ast.Ident); ok && cowSortMutators[id.Name][sel.Sel.Name] && len(s.Args) > 0 {
					if ok, d := rooted(s.Args[0]); ok && d > 0 {
						report(s, id.Name+"."+sel.Sel.Name+" reorders a config.Get() snapshot slice in place; slices.Clone it first")
					}
				}
			}
			if id, ok := s.Fun.(*ast.Ident); ok && len(s.Args) > 0 {
				switch id.Name {
				case "append", "copy", "clear", "delete":
					if ok, d := rooted(s.Args[0]); ok && d > 0 {
						report(s, id.Name+" writes into a config.Get() snapshot slice or map; copy it first")
					}
				}
			}
		}
		return true
	})
	return out
}

// TestConfigCOWGuardCatchesTheWrites is the guard's positive control: every
// forbidden shape in a fixture is reported, and the allowed reads are not.
func TestConfigCOWGuardCatchesTheWrites(t *testing.T) {
	src := `package gotham

import (
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func bad() {
	config.Get().RootDir = "/tmp/arkham"
	cfg := config.Get()
	cfg.ScanWorkers++
	_ = &cfg.SupportedExtensions
	sort.Strings(config.Get().SupportedExtensions)
	_ = append(cfg.SupportedExtensions, ".m4b")
	delete(cfg.Dedup.Signals.Confidence, "isbn")
	config.Get().ITunes.Resolve()
	config.SetForTest(func(c *config.Config) { c.RootDir = "/tmp/batcave" })
	go func() { config.Get().Dedup.Signals.Confidence["asin"] = 1 }()
	go func() { cfg.RootDir = "/tmp/ace-chemicals" }()
}

func good() string {
	cfg := config.Get()
	exts := append([]string(nil), cfg.SupportedExtensions...)
	sort.Strings(exts)
	local := *config.Get()
	local.RootDir = "/tmp/wayne-manor"
	n := config.Get().Normalized()
	n.RootDir = "/tmp/gcpd"
	return cfg.RootDir + local.RootDir + n.RootDir
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "gotham.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := cowViolations(fset, f, false)
	var lines []string
	for _, g := range got {
		lines = append(lines, strings.SplitN(g, ":", 2)[0])
	}
	want := []string{"10", "12", "13", "14", "15", "16", "17", "18", "19", "20"}
	sort.Strings(lines)
	sort.Strings(want)
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Fatalf("guard reported lines %v, want %v:\n%s", lines, want, strings.Join(got, "\n"))
	}
	if v := cowViolations(fset, f, true); len(v) != len(want)-1 {
		t.Fatalf("in a test file SetForTest is allowed and the rest still reported; got %d: %v", len(v), v)
	}
}

// ── the transitional mirror: test writers before production readers ─────────
//
// While config.AppConfig is the transitional mirror (internal/config/cow.go), a
// test that assigns it directly is seen by AppConfig readers at once and by
// config.Get() readers only at the next publish. So the sweep converts every
// test writer (to config.SetForTest / ReplaceForTest) BEFORE any production
// reader moves to config.Get(): TestConfigCOWTestWritersRatchet keeps the
// direct test writes from growing back while they are converted, and
// TestConfigCOWReadersWaitForTestWriters refuses the first production Get()
// until none is left. internal/config itself is out of scope: it owns the
// mirror and its tests exercise it on purpose.

// cowTestWriteCeiling is, per package directory, the number of direct
// config.AppConfig writes in its _test.go files on 2026-11-22 (assignment and
// increment targets rooted at config.AppConfig, and &config.AppConfig[.X]
// hand-offs, which a callee can write through). The ceilings may only go
// DOWN: the test does not fail when a count falls, so a conversion slice need
// not edit this map (and parallel slices do not collide on it). Never add an
// entry and never raise one.
// CONFIG_COW_PRINT=1 go test ./internal/arch -run TestConfigCOWTestWritersRatchet -v
// prints the current map.
var cowTestWriteCeiling = map[string]int{
	"cmd":                                 94,
	"internal/appdirs":                    4,
	"internal/audiobooks":                 47,
	"internal/batch":                      22,
	"internal/books":                      4,
	"internal/catalog":                    4,
	"internal/decisions":                  2,
	"internal/dedup":                      44,
	"internal/deluge":                     2,
	"internal/importer":                   4,
	"internal/itunes":                     2,
	"internal/itunes/service":             6,
	"internal/maintenance/jobs":           56,
	"internal/merge":                      21,
	"internal/metabatch":                  4,
	"internal/metadata":                   12,
	"internal/metafetch":                  138,
	"internal/organizer":                  76,
	"internal/plugins/acoustid":           28,
	"internal/plugins/dedup":              35,
	"internal/plugins/deluge":             31,
	"internal/plugins/maintenance":        97,
	"internal/plugins/metafetch":          6,
	"internal/reconcile":                  40,
	"internal/remux":                      20,
	"internal/scanner":                    273,
	"internal/scheduler":                  78,
	"internal/server":                     410,
	"internal/server/handlers":            56,
	"internal/server/handlers/abs":        6,
	"internal/server/handlers/aibackends": 4,
	"internal/server/handlers/audiobooks": 2,
	"internal/server/handlers/dedup":      2,
	"internal/server/handlers/metadata":   2,
	"internal/server/handlers/system":     22,
	"internal/transcribe":                 5,
	"internal/versions":                   2,
}

// cowTestWrites counts the direct config.AppConfig writes in f.
func cowTestWrites(f *ast.File) int {
	local := configLocalName(f)
	if local == "" {
		return 0
	}
	isMirror := func(e ast.Expr) bool {
		for {
			switch x := e.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && id.Name == local && x.Sel.Name == "AppConfig" {
					return true
				}
				e = x.X
			case *ast.IndexExpr:
				e = x.X
			case *ast.ParenExpr:
				e = x.X
			case *ast.StarExpr:
				e = x.X
			default:
				return false
			}
		}
	}
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		switch s := node.(type) {
		case *ast.AssignStmt:
			for _, l := range s.Lhs {
				if isMirror(l) {
					n++
				}
			}
		case *ast.IncDecStmt:
			if isMirror(s.X) {
				n++
			}
		case *ast.UnaryExpr:
			if s.Op == token.AND && isMirror(s.X) {
				n++
			}
		}
		return true
	})
	return n
}

// cowScan walks cmd/, internal/ and main.go once and returns the direct test
// writes per package directory and the production config.Get() uses
// ("file:line"), both outside internal/config.
func cowScan(t *testing.T) (writes map[string]int, gets []string, files int) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	writes = map[string]int{}
	fset := token.NewFileSet()
	visit := func(path string) error {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/config/") || !strings.HasSuffix(rel, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		files++
		dir := filepath.ToSlash(filepath.Dir(rel))
		if strings.HasSuffix(rel, "_test.go") {
			if n := cowTestWrites(f); n > 0 {
				writes[dir] += n
			}
			return nil
		}
		local := configLocalName(f)
		if local == "" {
			return nil
		}
		ast.Inspect(f, func(node ast.Node) bool {
			if c, ok := node.(*ast.CallExpr); ok && len(c.Args) == 0 {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Get" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
						gets = append(gets, fmt.Sprintf("%s:%d", rel, fset.Position(c.Pos()).Line))
					}
				}
			}
			return true
		})
		return nil
	}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "testdata", "vendor", ".worktrees":
					return filepath.SkipDir
				}
				return nil
			}
			return visit(path)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := visit(filepath.Join(root, "main.go")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return writes, gets, files
}

func TestConfigCOWTestWritersRatchet(t *testing.T) {
	writes, _, files := cowScan(t)
	if files < 500 {
		t.Fatalf("scanned only %d files; the walk is broken", files)
	}
	if os.Getenv("CONFIG_COW_PRINT") != "" {
		dirs := make([]string, 0, len(writes))
		for d := range writes {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			t.Logf("\t%q: %d,", d, writes[d])
		}
	}
	for _, v := range cowRatchetViolations(writes, cowTestWriteCeiling) {
		t.Error(v)
	}
}

// cowRatchetViolations compares the counted writes with the ceilings.
func cowRatchetViolations(writes, ceilings map[string]int) []string {
	var out []string
	for dir, n := range writes {
		ceiling, ok := ceilings[dir]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s: %d direct config.AppConfig writes in tests, and the package had none on 2026-11-22. "+
				"Use config.SetForTest / config.ReplaceForTest (internal/config/cow.go)", dir, n))
		case n > ceiling:
			out = append(out, fmt.Sprintf("%s: direct config.AppConfig writes in tests rose to %d (ceiling %d). "+
				"Use config.SetForTest / config.ReplaceForTest (internal/config/cow.go)", dir, n, ceiling))
		}
	}
	sort.Strings(out)
	return out
}

func TestConfigCOWReadersWaitForTestWriters(t *testing.T) {
	writes, gets, _ := cowScan(t)
	if msg := cowOrderViolation(writes, gets); msg != "" {
		t.Fatal(msg)
	}
}

// cowOrderViolation is the refusal TestConfigCOWReadersWaitForTestWriters
// reports, or "" when production config.Get() may exist.
func cowOrderViolation(writes map[string]int, gets []string) string {
	left := 0
	for _, n := range writes {
		left += n
	}
	if left == 0 || len(gets) == 0 {
		return ""
	}
	sorted := append([]string(nil), gets...)
	sort.Strings(sorted)
	return fmt.Sprintf("%d production config.Get() uses (first: %s) while %d direct config.AppConfig writes remain in tests. "+
		"A test's direct write reaches Get() only at the next publish, so a converted reader can see a stale config: "+
		"convert the test writers first (.claude/notes/pc-tools/config-cow-slices.md, wave 1)", len(sorted), sorted[0], left)
}

// TestConfigCOWOrderAndRatchetDecide is the positive control for the two
// checks above, which pass vacuously on a tree with no production Get().
func TestConfigCOWOrderAndRatchetDecide(t *testing.T) {
	if cowOrderViolation(map[string]int{"internal/gotham": 1}, []string{"internal/arkham/a.go:3"}) == "" {
		t.Error("a production Get() with a direct test write left was not refused")
	}
	if cowOrderViolation(map[string]int{}, []string{"internal/arkham/a.go:3"}) != "" {
		t.Error("a production Get() with no direct test write left was refused")
	}
	if cowOrderViolation(map[string]int{"internal/gotham": 4}, nil) != "" {
		t.Error("direct test writes with no production Get() were refused")
	}
	got := cowRatchetViolations(
		map[string]int{"internal/gotham": 3, "internal/arkham": 2, "internal/blackgate": 1},
		map[string]int{"internal/gotham": 2, "internal/arkham": 2})
	if len(got) != 2 || !strings.HasPrefix(got[0], "internal/blackgate:") || !strings.HasPrefix(got[1], "internal/gotham:") {
		t.Errorf("ratchet: want a new package and a raised one refused, a held one passed; got %q", got)
	}
}

// TestAppConfigRatchetCountsGet: the AppConfig ratchet must count config.Get,
// or the sweep's rewrite would silently empty it.
func TestAppConfigRatchetCountsGet(t *testing.T) {
	src := `package gotham

import "github.com/falkcorp/audiobook-organizer/internal/config"

func read() string { return config.Get().RootDir + config.AppConfig.RootDir + config.Snapshot().RootDir }
`
	f, err := parser.ParseFile(token.NewFileSet(), "gotham.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAccessorUses(f); got != 3 {
		t.Fatalf("countAccessorUses = %d, want 3 (Get, AppConfig, Snapshot)", got)
	}
}

// TestConfigCOWTestWritesCountsTheShapes is the ratchet's positive control.
func TestConfigCOWTestWritesCountsTheShapes(t *testing.T) {
	src := `package gotham

import cfgpkg "github.com/falkcorp/audiobook-organizer/internal/config"

func setUp() {
	cfgpkg.AppConfig.RootDir = "/tmp/arkham"
	cfgpkg.AppConfig.ScanWorkers++
	cfgpkg.AppConfig = cfgpkg.Config{}
	cfgpkg.AppConfig.Dedup.Signals.Confidence["isbn"] = cfgpkg.DedupKindConfidence{}
	keep(&cfgpkg.AppConfig)
	keep2(&cfgpkg.AppConfig.Tools)
	_ = cfgpkg.AppConfig.RootDir
	cfgpkg.SetForTest(func(c *cfgpkg.Config) { c.RootDir = "/tmp/batcave" })
}
`
	f, err := parser.ParseFile(token.NewFileSet(), "gotham_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := cowTestWrites(f); got != 6 {
		t.Fatalf("counted %d direct writes, want 6", got)
	}
}
