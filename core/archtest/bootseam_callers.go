// file: core/archtest/bootseam_callers.go
// version: 1.0.0
// guid: 6832cffd-0756-4534-b5fd-be37a33bd05b
// last-edited: 2026-11-12

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// bootseamPath is the package that declares Seams, With and Seams.Built.
const bootseamPath = mediacorePath + "/internal/bootseam"

// bootseamWith and bootseamBuilt are the guarded names: the function that
// puts a Seams in a boot's context, and the Seams field app.New calls with
// the kernel it built.
const (
	bootseamWith  = "With"
	bootseamBuilt = "Built"
)

// bootseamExempt reports the packages whose non-test files may use the hook:
// bootseam itself, the composition root that calls Built (app), and the
// test-support packages that put a Seams in a boot's context (coretest and its
// sub-packages, crashmatrix among them).
func bootseamExempt(importPath string) bool {
	rel, ok := Rel(mediacorePath, importPath)
	if !ok {
		return false
	}
	return rel == "internal/bootseam" || rel == "app" || rel == "coretest" || strings.HasPrefix(rel, "coretest/")
}

// BootseamHookCallers fails t when a non-test file outside core/app,
// core/coretest and core/internal/bootseam calls bootseam.With or references
// Seams.Built. Seams.Built hands the kernel itself to its holder, and With is
// how a holder gets a Seams into a boot: together they are a way to the
// kernel's raw task seams (the executor with no principal, a run's record
// with no permission rule), which app.App deliberately does not offer (F2-16
// review). A boot in production carries no Seams, so nothing else has a
// reason to use them. Test files are exempt.
//
// The check is syntactic, so it over-approximates in one place: in a file that
// imports bootseam, any selector or composite-literal key named Built counts,
// whatever its receiver's type.
//
// It loads core, the module above the working directory, over the whole
// tag-set union.
func BootseamHookCallers(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("BootseamHookCallers: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("BootseamHookCallers: %v", err)
	}
	findings, err := BootseamHookCallerFindings(root)
	if err != nil {
		t.Fatalf("BootseamHookCallers: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d use(s) of %s.%s or Seams.%s outside core/app, core/coretest and test files (the kernel's raw task seams are coretest's alone):\n  %s",
			len(findings), bootseamPath, bootseamWith, bootseamBuilt, strings.Join(findings, "\n  "))
	}
}

// BootseamHookCallerFindings loads the core module at dir over TagSets() and
// returns one line per use BootseamHookCallers reports, sorted. It returns an
// error when the load fails, when bootseam is not among the loaded packages,
// or when no non-test file was scanned: each would make the guard pass without
// looking.
func BootseamHookCallerFindings(dir string) ([]string, error) {
	pkgs, err := Load(LoadOptions{Dir: dir, TagSets: TagSets()})
	if err != nil {
		return nil, err
	}
	if _, ok := pkgs[bootseamPath]; !ok {
		return nil, fmt.Errorf("archtest: %s is not among the %d loaded packages; the guard would be vacuous", bootseamPath, len(pkgs))
	}
	var out []string
	scanned := 0
	for _, p := range pkgs {
		if bootseamExempt(p.ImportPath) {
			continue
		}
		for _, f := range p.GoFiles {
			scanned++
			found, err := bootseamRefs(f)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	if scanned == 0 {
		return nil, fmt.Errorf("archtest: no non-test Go file outside the exempt packages under %s; the guard would be vacuous", dir)
	}
	sort.Strings(out)
	return uniq(out), nil
}

// bootseamRefs parses one file and reports, when it imports bootseam, every
// selector through the import's name that names With, every dot-imported use
// of With, any selector or composite-literal key named Built, and a
// //go:linkname naming bootseam.With.
func bootseamRefs(file string) ([]string, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", file, err)
	}
	names := map[string]bool{}
	dot := false
	for _, imp := range af.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != bootseamPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["bootseam"] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	var out []string
	report := func(pos token.Pos, what string) {
		out = append(out, fmt.Sprintf("%s: %s", fset.Position(pos), what))
	}
	if len(names) > 0 || dot {
		sels := map[*ast.Ident]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				sels[n.Sel] = true
				if x, ok := n.X.(*ast.Ident); ok && names[x.Name] && n.Sel.Name == bootseamWith {
					report(n.Pos(), bootseamPath+"."+bootseamWith)
				}
				if n.Sel.Name == bootseamBuilt {
					report(n.Pos(), "Seams."+bootseamBuilt)
				}
			case *ast.KeyValueExpr:
				if id, ok := n.Key.(*ast.Ident); ok && id.Name == bootseamBuilt {
					report(id.Pos(), "Seams."+bootseamBuilt)
				}
			}
			return true
		})
		if dot {
			ast.Inspect(af, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == bootseamWith && !sels[id] {
					report(id.Pos(), bootseamPath+"."+bootseamWith)
				}
				return true
			})
		}
	}
	for _, cg := range af.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:linkname ")
			if !ok {
				continue
			}
			for field := range strings.FieldsSeq(rest) {
				if field == bootseamPath+"."+bootseamWith {
					report(c.Pos(), bootseamPath+"."+bootseamWith)
					break
				}
			}
		}
	}
	return out, nil
}
