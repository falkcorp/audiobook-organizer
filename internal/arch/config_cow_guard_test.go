// file: internal/arch/config_cow_guard_test.go
// version: 1.0.0
// guid: 6f1d2c84-93be-4a57-8e0c-1b7a5d3f9e26
// last-edited: 2026-11-21

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
//     (ITunesConfig.Resolve, and r10's in-place Config.Normalize; Config.Validate
//     no longer does, see Normalized)
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
