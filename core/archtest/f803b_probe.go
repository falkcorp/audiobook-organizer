// file: core/archtest/f803b_probe.go
// version: 1.0.0
// guid: b2d6f0a8-4c17-4e93-9a5b-7d1e3c8f2a60
// last-edited: 2026-11-15

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// f803bProbeNote ends every finding of the probe pin.
const f803bProbeNote = "the F8-03b refusal's probe must be testing.Testing and nothing else; see seams.d/F8-03.md"

// f803bProbe pins the run-time refusal of diagnostics (round 10, N-3): the
// refusal is only as good as the probe that says "this is a test binary", and
// every in-process test passes the probe in, so a probe that always says "test
// binary" survives them all. The pin is syntactic, over the non-test files of
// diagnostics (and the files no tag set builds), and it reads names, so a rename
// fails the build instead of passing: every pinned symbol must be found.
//
//   - every call of newExporter ends with the argument testing.Testing (the
//     package "testing" under whatever name the file imports it);
//   - every call of redesignRefusal passes RedesignPendingF8_03b first and, second,
//     the parameter isTestBinary (inside newExporter), a method value testBinary,
//     or testing.Testing;
//   - isTestBinary is never assigned, and its only composite-literal value is the
//     identifier isTestBinary (newExporter's parameter);
//   - newExporter, redesignRefusal and testBinary are only ever called, never
//     passed around as values;
//   - the method testBinary has no function literal and returns only
//     recv.isTestBinary() or testing.Testing(), the last statement being
//     return testing.Testing().
//
// What it does not see: a different package passing its own value into an
// Exporter (the field is unexported), and the linker flag that makes
// testing.Testing true (F803bNoLinkerOverride looks for that in the repo's build
// paths). F8-03b deletes this file with the refusal.
func f803bProbe(files []string) ([]string, error) {
	var (
		out                                          []string
		newExporterCalls, refusalCalls, testBinaries int
	)
	const (
		fnNew     = "newExporter"
		fnRefuse  = "redesignRefusal"
		fnTestBin = "testBinary"
		field     = "isTestBinary"
	)
	for _, file := range files {
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %v", file, err)
		}
		testingNames := map[string]bool{}
		for _, imp := range af.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == "testing" {
				switch {
				case imp.Name == nil:
					testingNames["testing"] = true
				case imp.Name.Name != "_" && imp.Name.Name != ".":
					testingNames[imp.Name.Name] = true
				}
			}
		}
		// isTesting reports whether e is the selector testing.Testing.
		isTesting := func(e ast.Expr) bool {
			sel, ok := e.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Testing" {
				return false
			}
			id, ok := sel.X.(*ast.Ident)
			return ok && testingNames[id.Name]
		}
		report := func(pos token.Pos, what string) {
			out = append(out, fmt.Sprintf("%s: %s: %s", fset.Position(pos), what, f803bProbeNote))
		}
		// allowed holds the identifiers that name a pinned function in a place
		// the pin has judged: a declaration, the callee of a call, or the method
		// value handed to redesignRefusal.
		allowed := map[*ast.Ident]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				allowed[x.Name] = true
				if x.Name.Name == fnTestBin && x.Recv != nil {
					testBinaries++
					f803bProbeTestBinary(x, isTesting, field, report)
				}
			case *ast.CallExpr:
				callee, _ := x.Fun.(*ast.Ident)
				switch {
				case callee != nil && callee.Name == fnNew:
					newExporterCalls++
					allowed[callee] = true
					if len(x.Args) == 0 || !isTesting(x.Args[len(x.Args)-1]) {
						report(x.Pos(), fnNew+" must be called with testing.Testing as its last argument")
					}
				case callee != nil && callee.Name == fnRefuse:
					refusalCalls++
					allowed[callee] = true
					if len(x.Args) != 2 {
						report(x.Pos(), fnRefuse+" must be called with two arguments")
						break
					}
					if id, ok := x.Args[0].(*ast.Ident); !ok || id.Name != f803bMarker {
						report(x.Args[0].Pos(), fnRefuse+" must be called with "+f803bMarker+" as its first argument")
					}
					switch p := x.Args[1].(type) {
					case *ast.Ident:
						if p.Name != field {
							report(p.Pos(), fnRefuse+" must be given the probe "+field+", the method value "+fnTestBin+" or testing.Testing")
						}
					case *ast.SelectorExpr:
						if p.Sel.Name == fnTestBin {
							allowed[p.Sel] = true
						} else if !isTesting(p) {
							report(p.Pos(), fnRefuse+" must be given the probe "+field+", the method value "+fnTestBin+" or testing.Testing")
						}
					default:
						report(x.Args[1].Pos(), fnRefuse+" must be given the probe "+field+", the method value "+fnTestBin+" or testing.Testing")
					}
				}
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == fnTestBin {
					allowed[sel.Sel] = true
				}
			case *ast.AssignStmt:
				for _, l := range x.Lhs {
					if f803bNamed(l, field) {
						report(l.Pos(), field+" is assigned: it is set only by newExporter's composite literal")
					}
				}
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok && id.Name == field {
					if v, ok := x.Value.(*ast.Ident); !ok || v.Name != field {
						report(x.Value.Pos(), field+" may only be set to newExporter's parameter "+field)
					}
				}
			}
			return true
		})
		ast.Inspect(af, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && !allowed[id] && (id.Name == fnNew || id.Name == fnRefuse || id.Name == fnTestBin) {
				report(id.Pos(), id.Name+" is used other than by calling it: a function value could carry a different probe")
			}
			return true
		})
	}
	if newExporterCalls == 0 || refusalCalls < 2 || testBinaries != 1 {
		out = append(out, fmt.Sprintf("archtest: the probe pin found %d call(s) of newExporter, %d of redesignRefusal and %d method(s) testBinary in %s (want at least 1, at least 2 and exactly 1): if F8-03b renamed or removed them, remove the F8-03b gate (core/archtest/f803b_*.go) with them; otherwise the pin would be vacuous: %s",
			newExporterCalls, refusalCalls, testBinaries, diagnosticsPath, f803bProbeNote))
	}
	sort.Strings(out)
	return uniq(out), nil
}

// f803bProbeTestBinary pins the body of the method testBinary.
func f803bProbeTestBinary(fd *ast.FuncDecl, isTesting func(ast.Expr) bool, field string, report func(token.Pos, string)) {
	if fd.Body == nil || len(fd.Body.List) == 0 {
		report(fd.Pos(), "testBinary has no body")
		return
	}
	recv := ""
	if len(fd.Recv.List) == 1 && len(fd.Recv.List[0].Names) == 1 {
		recv = fd.Recv.List[0].Names[0].Name
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			report(x.Pos(), "testBinary holds a function literal")
		case *ast.ReturnStmt:
			if len(x.Results) != 1 {
				report(x.Pos(), "testBinary must return one call")
				return true
			}
			call, ok := x.Results[0].(*ast.CallExpr)
			if !ok || len(call.Args) != 0 {
				report(x.Pos(), "testBinary must return recv."+field+"() or testing.Testing()")
				return true
			}
			sel, isSel := call.Fun.(*ast.SelectorExpr)
			viaField := isSel && sel.Sel.Name == field && recv != "" && f803bNamed(sel.X, recv)
			if !viaField && !isTesting(call.Fun) {
				report(x.Pos(), "testBinary must return recv."+field+"() or testing.Testing()")
			}
		}
		return true
	})
	last, ok := fd.Body.List[len(fd.Body.List)-1].(*ast.ReturnStmt)
	if !ok || len(last.Results) != 1 {
		report(fd.Body.Pos(), "testBinary must end with return testing.Testing()")
		return
	}
	if call, ok := last.Results[0].(*ast.CallExpr); !ok || !isTesting(call.Fun) {
		report(last.Pos(), "testBinary must end with return testing.Testing()")
	}
}

// f803bNamed reports whether e is the identifier name or a selector ending in it.
func f803bNamed(e ast.Expr, name string) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == name
	case *ast.SelectorExpr:
		return x.Sel.Name == name
	}
	return false
}

// f803bTestBinaryFlag is the linker symbol the go command sets to make
// testing.Testing true. It is assembled so this file does not contain it.
const f803bTestBinaryFlag = "testing." + "testBinary"

// F803bNoLinkerOverride fails t when a build path of the repo names the linker
// symbol that makes testing.Testing true (-ldflags "-X testing.testBinary=1"),
// which would turn the run-time refusal of diagnostics off in a production
// binary. It reads the build files of the repo above core (the module itself
// when core is not under a larger repo): Makefiles, Dockerfiles, shell, Python,
// YAML, TOML, JSON and Go sources, skipping git, node_modules, dist and
// this guard's own files (f803b_probe*.go, which name the symbol). Known limit: a flag assembled at run time from pieces
// (a variable, a concatenation) is not seen, and a person with a shell can still
// pass it; the guard is for a build path that does it by accident or by copy.
func F803bNoLinkerOverride(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("F803bNoLinkerOverride: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("F803bNoLinkerOverride: %v", err)
	}
	findings, scanned, err := f803bLinkerFindings(f803bRepoRoot(root))
	if err != nil {
		t.Fatalf("F803bNoLinkerOverride: %v", err)
	}
	if scanned == 0 {
		t.Fatalf("F803bNoLinkerOverride: no build file under %s; the guard would be vacuous", f803bRepoRoot(root))
	}
	if len(findings) > 0 {
		t.Errorf("%d build path(s) set %s, which turns the F8-03b refusal off:\n  %s", len(findings), f803bTestBinaryFlag, strings.Join(findings, "\n  "))
	}
}

// f803bRepoRoot is the directory F803bNoLinkerOverride scans: the parent of the
// core module when core sits in a repo (the parent has its own go.mod), else the
// module itself.
func f803bRepoRoot(coreRoot string) string {
	parent := filepath.Dir(coreRoot)
	if filepath.Base(coreRoot) == "core" {
		if _, err := os.Stat(filepath.Join(parent, "go.mod")); err == nil {
			return parent
		}
	}
	return coreRoot
}

// f803bLinkerFindings walks root for build files that contain the linker symbol
// and returns one line per file with the line number, plus how many files it
// read.
func f803bLinkerFindings(root string) ([]string, int, error) {
	var (
		out     []string
		scanned int
	)
	skipDir := []string{".git", "node_modules", "dist", ".worktrees"}
	ext := []string{".go", ".sh", ".bash", ".py", ".yml", ".yaml", ".toml", ".json", ".mk", ".env"}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if slices.Contains(skipDir, name) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, "f803b_probe") {
			return nil
		}
		build := strings.HasPrefix(name, "Makefile") || strings.HasPrefix(name, "Dockerfile") || name == ".envrc" || slices.Contains(ext, filepath.Ext(name))
		if !build || !d.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, f803bTestBinaryFlag) {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				out = append(out, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), i+1))
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, scanned, err
}
