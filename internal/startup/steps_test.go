// file: internal/startup/steps_test.go
// version: 1.1.0
// guid: 225d8029-438d-4ff7-9896-03d376746519
// last-edited: 2026-10-10

package startup

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestRun_FatalReturnsErrorAndRecordsNoToken(t *testing.T) {
	st := New().SetErrorLog(t.Logf)
	boom := errors.New("boom")
	if err := st.Run("initializeStore", func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("fatal step: got %v, want %v", err, boom)
	}
	if got := st.Tokens(); len(got) != 0 {
		t.Fatalf("fatal step recorded tokens %v", got)
	}
}

func TestRun_DegradedReturnsNilAndRecordsToken(t *testing.T) {
	st := New().SetErrorLog(t.Logf)
	if err := st.Run("InitOTEL", func() error { return errors.New("exporter") }); err != nil {
		t.Fatalf("degraded step returned %v, want nil", err)
	}
	if got, want := st.Tokens(), []string{TokenMetricsDisabled}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
}

// A degraded failure is never swallowed: it reaches the error log with the
// step name, its token and the cause.
func TestRun_DegradedFailureIsLogged(t *testing.T) {
	var got []string
	st := New().SetErrorLog(func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) })
	_ = st.Run("searchindex", func() error { return errors.New("bleve closed") })
	if len(got) != 1 || !strings.Contains(got[0], "searchindex") ||
		!strings.Contains(got[0], TokenSearchIndex) || !strings.Contains(got[0], "bleve closed") {
		t.Fatalf("error log = %q", got)
	}
	_ = st.Run("initializeStore", func() error { return errors.New("fatal") })
	if len(got) != 1 {
		t.Fatalf("a fatal step logged through the degraded path: %q", got)
	}
}

func TestRun_SuccessRecordsNothing(t *testing.T) {
	st := New().SetErrorLog(t.Logf)
	for _, step := range Table {
		if err := st.Run(step.Name, func() error { return nil }); err != nil {
			t.Fatalf("%s: %v", step.Name, err)
		}
	}
	if got := st.Tokens(); len(got) != 0 {
		t.Fatalf("tokens after clean run = %v", got)
	}
}

func TestRun_SameTokenTwiceAppearsOnce(t *testing.T) {
	st := New().SetErrorLog(t.Logf)
	fail := func() error { return errors.New("x") }
	_ = st.Run("searchindex", fail)
	_ = st.Run("searchindex", fail)
	_ = st.Run("InitOTEL", fail)
	if got, want := st.Tokens(), []string{TokenMetricsDisabled, TokenSearchIndex}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens = %v, want %v (sorted, deduplicated)", got, want)
	}
}

func TestClear_RemovesTokenAfterRetrySucceeds(t *testing.T) {
	st := New().SetErrorLog(t.Logf)
	_ = st.Run("searchindex", func() error { return errors.New("closed") })
	st.Clear(TokenSearchIndex)
	if got := st.Tokens(); len(got) != 0 {
		t.Fatalf("tokens after Clear = %v", got)
	}
}

func TestRun_UnknownStepFailsClosed(t *testing.T) {
	st := New().SetErrorLog(t.Logf)
	ran := false
	err := st.Run("no-such-step", func() error { ran = true; return nil })
	if err == nil || ran {
		t.Fatalf("unknown step: err=%v ran=%v, want an error and fn not run", err, ran)
	}
}

func TestTokens_NilSteps(t *testing.T) {
	var st *Steps
	if got := st.Tokens(); got != nil {
		t.Fatalf("nil Steps tokens = %v", got)
	}
}

// TestRun_ConcurrentRecordAndRead is the -race check: /health reads Tokens
// while startup may still be recording.
func TestRun_ConcurrentRecordAndRead(t *testing.T) {
	st := New().SetErrorLog(t.Logf)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = st.Run("searchindex", func() error { return errors.New("x") })
			st.Clear(TokenSearchIndex)
		}()
		go func() {
			defer wg.Done()
			_ = st.Tokens()
		}()
	}
	wg.Wait()
}

func TestTable_RowsAreWellFormed(t *testing.T) {
	seenName := map[string]bool{}
	seenToken := map[string]bool{}
	for _, step := range Table {
		if step.Name == "" || step.Reason == "" {
			t.Errorf("row %+v: name and reason are required", step)
		}
		if seenName[step.Name] {
			t.Errorf("duplicate row %q", step.Name)
		}
		seenName[step.Name] = true
		switch step.Class {
		case Fatal:
			if step.Token != "" {
				t.Errorf("fatal row %q has token %q; only degraded rows report one", step.Name, step.Token)
			}
		case Degraded:
			if step.Token == "" {
				t.Errorf("degraded row %q has no token", step.Name)
			}
			if seenToken[step.Token] {
				t.Errorf("token %q used twice", step.Token)
			}
			seenToken[step.Token] = true
		default:
			t.Errorf("row %q has class %v", step.Name, step.Class)
		}
	}
}

// --- AST scan: every Run call names a Table row, every row is run ---

const startupImportPath = "github.com/falkcorp/audiobook-organizer/internal/startup"

type runSite struct {
	pos  string
	name string // empty when the argument is not a string literal
}

// scanPackage finds the (*Steps).Run calls in one package's files. There is
// no type information (go/types needs an importer this module does not
// carry), so a receiver counts as a *Steps when its final name was declared
// in this package with type *startup.Steps, or assigned from startup.New()
// (optionally chained through SetErrorLog)
// or from a function of this package whose one result is *startup.Steps.
// The filter is per PACKAGE, not per file: a Steps field or function can be
// declared in one file and its Run called in another.
//
// foreign holds the EXPORTED field and function names that other scanned
// packages declare with type *startup.Steps (see exportedStepsNames), so a
// value reached across a package boundary -- cmd calling
// cfg.StartupSteps.Run, where StartupSteps is declared in package server --
// is recognised too, even in a package that never imports startup. Matching
// is by final name only, so an unrelated field that happens to share such a
// name over-reports (a spurious failure, never a missed site).
//
// Remaining blind spot: method values (r := steps.Run; r("x", f)) and Steps
// passed through an interface. A site missed that way still fails closed at
// runtime, because Run rejects a name that is not a Table row.
func scanPackage(fset *token.FileSet, files []*ast.File, foreign map[string]bool) []runSite {
	aliases := importAliases(files)
	if len(aliases) == 0 && len(foreign) == 0 {
		return nil
	}
	isStepsType := func(e ast.Expr) bool { return isStepsTypeExpr(e, aliases) }
	// startup.New(), or a chained setter on one (startup.New().SetErrorLog(f)).
	var isNewCall func(e ast.Expr) bool
	isNewCall = func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if sel.Sel.Name == "SetErrorLog" {
			return isNewCall(sel.X)
		}
		if sel.Sel.Name != "New" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && aliases[id.Name]
	}
	lastName := func(e ast.Expr) string {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			return x.Sel.Name
		}
		return ""
	}
	// Functions and methods of this package that return a *startup.Steps
	// (e.g. the server's startupStepsFor): a variable assigned from one of
	// them is a Steps value too.
	returnsSteps := map[string]bool{}
	for n := range foreign {
		returnsSteps[n] = true
	}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
				continue
			}
			if isStepsType(fd.Type.Results.List[0].Type) {
				returnsSteps[fd.Name.Name] = true
			}
		}
	}
	isNewCall = func(prev func(ast.Expr) bool) func(ast.Expr) bool {
		return func(e ast.Expr) bool {
			if prev(e) {
				return true
			}
			call, ok := e.(*ast.CallExpr)
			return ok && returnsSteps[lastName(call.Fun)]
		}
	}(isNewCall)

	names := map[string]bool{}
	for n := range foreign {
		names[n] = true
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Field:
				if isStepsType(x.Type) {
					for _, id := range x.Names {
						names[id.Name] = true
					}
				}
			case *ast.ValueSpec:
				if x.Type != nil && isStepsType(x.Type) {
					for _, id := range x.Names {
						names[id.Name] = true
					}
				}
				for i, v := range x.Values {
					if isNewCall(v) && i < len(x.Names) {
						names[x.Names[i].Name] = true
					}
				}
			case *ast.AssignStmt:
				for i, v := range x.Rhs {
					if isNewCall(v) && i < len(x.Lhs) {
						if n := lastName(x.Lhs[i]); n != "" {
							names[n] = true
						}
					}
				}
			}
			return true
		})
	}

	var sites []runSite
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Run" || !names[lastName(sel.X)] {
				return true
			}
			s := runSite{pos: fset.Position(call.Pos()).String()}
			if len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s.name, _ = strconv.Unquote(lit.Value)
				}
			}
			sites = append(sites, s)
			return true
		})
	}
	return sites
}

// importAliases returns the names under which files import internal/startup.
func importAliases(files []*ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, f := range files {
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == startupImportPath {
				alias := "startup"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				aliases[alias] = true
			}
		}
	}
	return aliases
}

// isStepsTypeExpr reports whether e is *<alias>.Steps for an import alias of
// internal/startup.
func isStepsTypeExpr(e ast.Expr, aliases map[string]bool) bool {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Steps" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && aliases[id.Name]
}

// exportedStepsNames returns the exported struct-field names declared with
// type *startup.Steps and the exported functions/methods whose one result is
// *startup.Steps, in one package. scanRepo unions these across packages and
// hands them to scanPackage as foreign names.
func exportedStepsNames(files []*ast.File) map[string]bool {
	aliases := importAliases(files)
	out := map[string]bool{}
	if len(aliases) == 0 {
		return out
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Field:
				if isStepsTypeExpr(x.Type, aliases) {
					for _, id := range x.Names {
						if id.IsExported() {
							out[id.Name] = true
						}
					}
				}
			case *ast.FuncDecl:
				if x.Name.IsExported() && x.Type.Results != nil && len(x.Type.Results.List) == 1 &&
					isStepsTypeExpr(x.Type.Results.List[0].Type, aliases) {
					out[x.Name.Name] = true
				}
			}
			return true
		})
	}
	return out
}

// foreignFor returns the exported Steps names of every package except own.
func foreignFor(own string, exported map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	for dir, names := range exported {
		if dir == own {
			continue
		}
		for n := range names {
			out[n] = true
		}
	}
	return out
}

// checkSites returns one message per violation: a non-literal name, a name
// missing from table, or a table row no site runs.
func checkSites(sites []runSite, table []Step) []string {
	var problems []string
	rows := map[string]bool{}
	for _, st := range table {
		rows[st.Name] = true
	}
	used := map[string]bool{}
	for _, s := range sites {
		if s.name == "" {
			problems = append(problems, fmt.Sprintf("%s: step names must be literals", s.pos))
			continue
		}
		if !rows[s.name] {
			problems = append(problems, fmt.Sprintf("%s: step %q is missing from startup.Table", s.pos, s.name))
		}
		used[s.name] = true
	}
	for _, st := range table {
		if !used[st.Name] {
			problems = append(problems, fmt.Sprintf("startup.Table row %q is never run (dead row)", st.Name))
		}
	}
	sort.Strings(problems)
	return problems
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
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func scanRepo(t *testing.T) []runSite {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	byDir := map[string][]*ast.File{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "vendor", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], f)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	exported := map[string]map[string]bool{}
	for dir, files := range byDir {
		exported[dir] = exportedStepsNames(files)
	}
	var sites []runSite
	for dir, files := range byDir {
		sites = append(sites, scanPackage(fset, files, foreignFor(dir, exported))...)
	}
	return sites
}

// TestTable_MatchesRunCallSites fails on a Run call whose name is not a
// literal or not a row (missing row), and on a row no Run call names (dead
// row).
func TestTable_MatchesRunCallSites(t *testing.T) {
	sites := scanRepo(t)
	// Positive control: every row is run at least once, so a working scanner
	// finds at least len(Table) sites. Fewer means the scanner lost them.
	if len(sites) < len(Table) {
		t.Fatalf("scanner found %d Run sites, fewer than the %d table rows: scanner broken or call sites removed", len(sites), len(Table))
	}
	for _, p := range checkSites(sites, Table) {
		t.Error(p)
	}
}

// TestScanPackage_Controls proves the scanner and checker reject what they
// claim to, on in-memory sources, so a green repo scan means something.
func TestScanPackage_Controls(t *testing.T) {
	const src = `package p

import "github.com/falkcorp/audiobook-organizer/internal/startup"

type srv struct{ steps *startup.Steps }

type other struct{}

func (other) Run(string, func() error) error { return nil }

func stepsOf(s *srv) *startup.Steps { return s.steps }

func f(s *srv, name string) {
	st := startup.New()
	_ = st.Run("initializeStore", nil)
	via := stepsOf(s)
	_ = via.Run("initializeStore", nil)
	chained := startup.New().SetErrorLog(nil)
	_ = chained.Run("initializeStore", nil)
	_ = s.steps.Run(name, nil)
	_ = s.steps.Run("not-a-row", nil)
	var o other
	_ = o.Run("ignored: not a Steps value", nil)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	sites := scanPackage(fset, []*ast.File{f}, nil)
	if len(sites) != 5 {
		t.Fatalf("found %d sites, want 5 (the other.Run call must not count): %+v", len(sites), sites)
	}
	table := []Step{
		{Name: "initializeStore", Class: Fatal, Reason: "r"},
		{Name: "dead", Class: Fatal, Reason: "r"},
	}
	got := strings.Join(checkSites(sites, table), "\n")
	for _, want := range []string{"step names must be literals", `step "not-a-row" is missing`, `row "dead" is never run`} {
		if !strings.Contains(got, want) {
			t.Errorf("checker output lacks %q:\n%s", want, got)
		}
	}

	// A package that never imports startup yields no sites at all.
	const noImport = `package q
func g(x interface{ Run(string, func() error) error }) { _ = x.Run("initializeStore", nil) }
`
	g, err := parser.ParseFile(fset, "q.go", noImport, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := scanPackage(fset, []*ast.File{g}, nil); len(got) != 0 {
		t.Fatalf("package without the import produced sites %+v", got)
	}
}

// TestScanPackage_CrossPackageControl: package a exports a *startup.Steps
// field and a getter; package b reaches them through a value of a's type
// WITHOUT importing startup (the cmd -> cfg.StartupSteps shape). Both Run
// calls must be found once a's exported names are seeded as foreign.
func TestScanPackage_CrossPackageControl(t *testing.T) {
	const srcA = `package a

import "github.com/falkcorp/audiobook-organizer/internal/startup"

type Config struct{ StartupSteps *startup.Steps }

func (c *Config) Runner() *startup.Steps { return c.StartupSteps }
`
	const srcB = `package b

import "example.com/a"

func f(cfg *a.Config) {
	_ = cfg.StartupSteps.Run("not-a-row", nil)
	r := cfg.Runner()
	_ = r.Run("also-not-a-row", nil)
}
`
	fset := token.NewFileSet()
	fa, err := parser.ParseFile(fset, "a.go", srcA, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := parser.ParseFile(fset, "b.go", srcB, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	exported := map[string]map[string]bool{
		"a": exportedStepsNames([]*ast.File{fa}),
		"b": exportedStepsNames([]*ast.File{fb}),
	}
	if !exported["a"]["StartupSteps"] || !exported["a"]["Runner"] {
		t.Fatalf("exported names of a = %v, want StartupSteps and Runner", exported["a"])
	}
	sites := scanPackage(fset, []*ast.File{fb}, foreignFor("b", exported))
	if len(sites) != 2 {
		t.Fatalf("found %d cross-package sites, want 2: %+v", len(sites), sites)
	}
	got := strings.Join(checkSites(sites, []Step{{Name: "initializeStore", Class: Fatal, Reason: "r"}}), "\n")
	for _, want := range []string{`step "not-a-row" is missing`, `step "also-not-a-row" is missing`} {
		if !strings.Contains(got, want) {
			t.Errorf("checker output lacks %q:\n%s", want, got)
		}
	}
	// Without the foreign seeding the same package yields nothing: this is
	// the blind spot the seeding closes.
	if got := scanPackage(fset, []*ast.File{fb}, nil); len(got) != 0 {
		t.Fatalf("unseeded scan of b found %+v; the control no longer proves the seeding", got)
	}
}
