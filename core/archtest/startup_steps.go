// file: core/archtest/startup_steps.go
// version: 1.0.0
// guid: 3b6c0d2e-8a41-4f6a-9d57-6e1f0a7c2b94
// last-edited: 2026-11-13

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// healthPath is the package that declares Startup.
const healthPath = mediacorePath + "/health"

// StartupSteps fails t when the startup table and the code that runs it
// disagree (05-health §5H.6, I4): a Startup.Run call whose step name is a
// string literal missing from table, a name that is not a literal at all, or a
// table row no Run call names. table is the step names, e.g. the Name of each
// row of health.CoreStartupSteps() plus a module's Registrar.StartupSteps;
// pkgs are the module-relative directories to scan, each with its
// sub-packages ("kernel", "module").
//
// A Run call is recognised by its receiver: an identifier or field declared as
// *health.Startup, or assigned from health.NewStartup, in the same package.
// It is a syntactic scan, as internal/startup's is; a Startup reached through
// a helper's return value is not tracked, and the guard's own tests pin what
// it does see. Test files are not scanned.
func StartupSteps(t testing.TB, table []string, pkgs ...string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("StartupSteps: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("StartupSteps: %v", err)
	}
	findings, err := StartupStepFindings(root, table, pkgs...)
	if err != nil {
		t.Fatalf("StartupSteps: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d startup table finding(s) (05-health §5H.6: every Run step is a table row and every row is run):\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// StartupStepFindings loads the core module at dir and returns one line per
// disagreement StartupSteps reports, sorted. It returns an error when the load
// fails, when a pkgs entry matches no loaded package, or when no Run call was
// seen at all, each of which would make the guard pass without looking.
func StartupStepFindings(dir string, table []string, pkgs ...string) ([]string, error) {
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("archtest: StartupSteps needs at least one package to scan")
	}
	loaded, err := Load(LoadOptions{Dir: dir, TagSets: TagSets()})
	if err != nil {
		return nil, err
	}
	ran := map[string]bool{}
	var out []string
	sites := 0
	for _, want := range pkgs {
		matched := false
		for _, p := range loaded {
			rel, ok := Rel(mediacorePath, p.ImportPath)
			if !ok || (rel != want && !strings.HasPrefix(rel, want+"/")) {
				continue
			}
			matched = true
			found, err := startupRunSites(p.GoFiles)
			if err != nil {
				return nil, err
			}
			for _, s := range found {
				sites++
				switch {
				case s.name == "":
					out = append(out, fmt.Sprintf("%s: Startup.Run with a step name that is not a string literal", s.pos))
				case !slices.Contains(table, s.name):
					out = append(out, fmt.Sprintf("%s: Startup.Run(%q) is not a row of the startup table", s.pos, s.name))
				default:
					ran[s.name] = true
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf("archtest: no loaded package under %q; the guard would be vacuous", want)
		}
	}
	if sites == 0 {
		return nil, fmt.Errorf("archtest: no Startup.Run call under %s; the guard would be vacuous", strings.Join(pkgs, ", "))
	}
	for _, name := range table {
		if !ran[name] {
			out = append(out, fmt.Sprintf("table row %q is never run by any Startup.Run call", name))
		}
	}
	sort.Strings(out)
	return uniq(out), nil
}

// startupRunSite is one Startup.Run call. name is empty when the step name is
// not a string literal.
type startupRunSite struct {
	pos  string
	name string
}

// startupRunSites parses the files as one package and returns its Run calls on
// a *health.Startup, in source order per file.
func startupRunSites(files []string) ([]startupRunSite, error) {
	fset := token.NewFileSet()
	var parsed []*ast.File
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %v", f, err)
		}
		parsed = append(parsed, af)
	}
	tracked := map[string]bool{}
	for _, af := range parsed {
		aliases := healthAliases(af)
		if len(aliases) == 0 {
			continue
		}
		trackStartupNames(af, aliases, tracked)
	}
	var out []startupRunSite
	for _, af := range parsed {
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 3 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Run" || !tracked[receiverName(sel.X)] {
				return true
			}
			site := startupRunSite{pos: fset.Position(call.Pos()).String()}
			if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					site.name = s
				}
			}
			out = append(out, site)
			return true
		})
	}
	return out, nil
}

// healthAliases returns the names under which af imports the health package.
func healthAliases(af *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, imp := range af.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != healthPath {
			continue
		}
		switch {
		case imp.Name == nil:
			out["health"] = true
		case imp.Name.Name != "_" && imp.Name.Name != ".":
			out[imp.Name.Name] = true
		}
	}
	return out
}

// trackStartupNames adds to tracked every name af declares as a *health.Startup
// (a parameter, result, field or var) or assigns from health.NewStartup.
func trackStartupNames(af *ast.File, aliases map[string]bool, tracked map[string]bool) {
	isStartup := func(e ast.Expr) bool {
		if star, ok := e.(*ast.StarExpr); ok {
			e = star.X
		}
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Startup" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && aliases[id.Name]
	}
	ast.Inspect(af, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			if isStartup(x.Type) {
				for _, id := range x.Names {
					tracked[id.Name] = true
				}
			}
		case *ast.ValueSpec:
			if x.Type != nil && isStartup(x.Type) {
				for _, id := range x.Names {
					tracked[id.Name] = true
				}
			}
		case *ast.AssignStmt:
			if len(x.Rhs) != 1 || len(x.Lhs) == 0 {
				return true
			}
			call, ok := x.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "NewStartup" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] {
				if lhs, ok := x.Lhs[0].(*ast.Ident); ok {
					tracked[lhs.Name] = true
				}
			}
		}
		return true
	})
}

// receiverName is the name a Run receiver is tracked by: the identifier, or the
// last field of a selector chain (k.startup.Run tracks "startup").
func receiverName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.ParenExpr:
		return receiverName(x.X)
	}
	return ""
}
