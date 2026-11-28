// file: core/archtest/systemstart_minters.go
// version: 1.0.0
// guid: 91c4e7a3-2b68-4d05-a3f9-0e7d5b1c8a46
// last-edited: 2026-11-28

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

// executorPath is the package that mints the start-origin handles.
const executorPath = mediacorePath + "/internal/executor"

// systemStartMinters are the exported functions of the executor that hand out a
// start with an origin (what lets a kind's Perms.SystemStart apply) or answer the
// rule's question for P7: the scheduler's and the outbox's handles and the
// check. Only the kernel, which wires each handle to its one consumer, may call
// them.
var systemStartMinters = map[string]bool{
	"ScheduleStager":   true,
	"OutboxRuns":       true,
	"CheckSystemStart": true,
}

// systemStartMinterExempt reports the packages whose non-test files may use the
// minters: the executor itself and the kernel.
func systemStartMinterExempt(importPath string) bool {
	rel, ok := Rel(mediacorePath, importPath)
	if !ok {
		return false
	}
	return rel == "internal/executor" || rel == "kernel"
}

// SystemStartMinterCallers fails t when a non-test file outside core/kernel and
// core/internal/executor names one of the executor's origin minters
// (ScheduleStager, OutboxRuns, CheckSystemStart). A start carries an origin only
// through a handle those functions return, and the origin is what lets one
// module's system actor start its own kind past Start (Permissions.SystemStart):
// a second holder of a handle is a second way to that authority.
//
// The check is syntactic: in a file that imports the executor, any selector
// through the import's name, any dot-imported use, and any //go:linkname that
// names a minter counts. Test files are exempt.
//
// It loads core, the module above the working directory, over the whole
// tag-set union.
func SystemStartMinterCallers(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("SystemStartMinterCallers: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("SystemStartMinterCallers: %v", err)
	}
	findings, err := SystemStartMinterFindings(root)
	if err != nil {
		t.Fatalf("SystemStartMinterCallers: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d use(s) of the executor's start-origin minters outside core/kernel and test files (a handle that carries an origin has one holder):\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// SystemStartMinterFindings loads the core module at dir over TagSets() and
// returns one line per use SystemStartMinterCallers reports, sorted. It returns
// an error when the load fails, when the executor is not among the loaded
// packages, or when no non-test file was scanned: each would make the guard
// pass without looking.
func SystemStartMinterFindings(dir string) ([]string, error) {
	pkgs, err := Load(LoadOptions{Dir: dir, TagSets: TagSets()})
	if err != nil {
		return nil, err
	}
	if _, ok := pkgs[executorPath]; !ok {
		return nil, fmt.Errorf("archtest: %s is not among the %d loaded packages; the guard would be vacuous", executorPath, len(pkgs))
	}
	var out []string
	scanned := 0
	for _, p := range pkgs {
		if systemStartMinterExempt(p.ImportPath) {
			continue
		}
		for _, f := range p.GoFiles {
			scanned++
			found, err := systemStartMinterRefs(f)
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

func systemStartMinterRefs(file string) ([]string, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", file, err)
	}
	names := map[string]bool{}
	dot := false
	for _, imp := range af.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != executorPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["executor"] = true
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	var out []string
	report := func(pos token.Pos, name string) {
		out = append(out, fmt.Sprintf("%s: %s.%s", fset.Position(pos), executorPath, name))
	}
	if len(names) > 0 || dot {
		sels := map[*ast.Ident]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			if n, ok := n.(*ast.SelectorExpr); ok {
				sels[n.Sel] = true
				if x, ok := n.X.(*ast.Ident); ok && names[x.Name] && systemStartMinters[n.Sel.Name] {
					report(n.Pos(), n.Sel.Name)
				}
			}
			return true
		})
		if dot {
			ast.Inspect(af, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && systemStartMinters[id.Name] && !sels[id] {
					report(id.Pos(), id.Name)
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
				if name, ok := strings.CutPrefix(field, executorPath+"."); ok && systemStartMinters[name] {
					report(c.Pos(), name)
					break
				}
			}
		}
	}
	return out, nil
}
