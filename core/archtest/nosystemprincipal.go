// file: core/archtest/nosystemprincipal.go
// version: 1.0.0
// guid: 52d501bd-71ec-4932-95f3-f768c7011020
// last-edited: 2026-11-10

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

// authzPath is the package that holds the privileged checker constructors.
const authzPath = mediacorePath + "/internal/authz"

// mediacorePath is the core module path the guard is written against.
const mediacorePath = "github.com/falkcorp/mediacore"

// privilegedConstructors returns the authz functions that build a checker
// bypassing role resolution (05-rbac §3.4, SEC-26), a fresh map on every call
// (R5).
func privilegedConstructors() map[string]bool {
	return map[string]bool{"AuthDisabled": true, "ForSystem": true}
}

// systemPrincipalCallers returns the packages whose non-test files may name
// the privileged constructors: auth, the tasks executor (05-tasks §5T.3) and
// kernel, exactly, not their sub-packages, plus authz itself.
func systemPrincipalCallers() map[string]bool {
	return map[string]bool{
		mediacorePath + "/auth":              true,
		mediacorePath + "/internal/executor": true,
		mediacorePath + "/kernel":            true,
		authzPath:                            true,
	}
}

// NoSystemPrincipalOutsideKernel fails t when core breaks 05-rbac §12's
// guard (SEC-26, COR4-12): (a) a non-test file outside auth, the tasks
// executor (core/internal/executor) and kernel names authz.AuthDisabled or
// authz.ForSystem, called or not (a function value escapes as surely as a
// call), or dot-imports authz, which hides such a call; or (b) rbac or any of
// its sub-packages imports authz, from a test file included. It loads core,
// the module above the working directory, over the whole tag-set union.
func NoSystemPrincipalOutsideKernel(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("NoSystemPrincipalOutsideKernel: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("NoSystemPrincipalOutsideKernel: %v", err)
	}
	findings, err := NoSystemPrincipalFindings(root)
	if err != nil {
		t.Fatalf("NoSystemPrincipalOutsideKernel: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d use(s) of a privileged checker outside auth, internal/executor and kernel, or an rbac import of authz (05-rbac §3.4, SEC-26):\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// NoSystemPrincipalFindings loads the core module at dir over TagSets() and
// returns one line per breach of NoSystemPrincipalOutsideKernel, sorted. It
// returns an error when the load fails, when authz is not among the loaded
// packages (dir is not core, or authz is gone), or when no non-test file was
// scanned: each would make the guard pass without looking.
func NoSystemPrincipalFindings(dir string) ([]string, error) {
	pkgs, err := Load(LoadOptions{Dir: dir, TagSets: TagSets()})
	if err != nil {
		return nil, err
	}
	if _, ok := pkgs[authzPath]; !ok {
		return nil, fmt.Errorf("archtest: %s is not among the %d loaded packages; the guard would be vacuous", authzPath, len(pkgs))
	}
	var out []string
	allowed := systemPrincipalCallers()
	scanned := 0
	for _, p := range pkgs {
		if p.ImportPath == mediacorePath+"/rbac" || strings.HasPrefix(p.ImportPath, mediacorePath+"/rbac/") {
			for _, imp := range append(append([]string(nil), p.Imports...), p.TestImports...) {
				if imp == authzPath {
					out = append(out, fmt.Sprintf("%s imports %s; rbac reaches authz only through the injected Deps.Compile (COR4-12)", p.ImportPath, authzPath))
				}
			}
		}
		if allowed[p.ImportPath] {
			continue
		}
		for _, f := range p.GoFiles {
			scanned++
			found, err := privilegedUses(f)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	if scanned == 0 {
		return nil, fmt.Errorf("archtest: no non-test Go file outside the allowed callers under %s; the guard would be vacuous", dir)
	}
	sort.Strings(out)
	return uniq(out), nil
}

// privilegedUses parses one file and reports every selector naming a
// privileged constructor through an import of authz, and a dot import of
// authz. A blank import names nothing and is not reported.
func privilegedUses(file string) ([]string, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", file, err)
	}
	var out []string
	aliases := map[string]bool{}
	for _, imp := range af.Imports {
		// The parser only accepts a well-formed quoted import path.
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != authzPath {
			continue
		}
		switch {
		case imp.Name == nil:
			aliases["authz"] = true
		case imp.Name.Name == ".":
			out = append(out, fmt.Sprintf("%s: dot import of %s hides calls to its privileged constructors", fset.Position(imp.Pos()), authzPath))
		case imp.Name.Name != "_":
			aliases[imp.Name.Name] = true
		}
	}
	if len(aliases) == 0 {
		return out, nil
	}
	ctors := privilegedConstructors()
	ast.Inspect(af, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !ctors[sel.Sel.Name] {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && aliases[id.Name] {
			out = append(out, fmt.Sprintf("%s: authz.%s outside auth, internal/executor and kernel", fset.Position(sel.Pos()), sel.Sel.Name))
		}
		return true
	})
	return out, nil
}
