// file: core/archtest/nosystemprincipal.go
// version: 1.0.1
// guid: 52d501bd-71ec-4932-95f3-f768c7011020
// last-edited: 2026-11-10

package archtest

import (
	"fmt"
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

// systemPrincipalCallers returns the packages whose non-test files may
// import authz: auth (AuthDisabled), the tasks executor (ForSystem, 05-tasks
// §5T.3) and kernel (Compile injected as rbac.Deps.Compile, and both
// privileged constructors), exactly, not their sub-packages.
func systemPrincipalCallers() map[string]bool {
	return map[string]bool{
		mediacorePath + "/auth":              true,
		mediacorePath + "/internal/executor": true,
		mediacorePath + "/kernel":            true,
	}
}

// NoSystemPrincipalOutsideKernel fails t when core breaks 05-rbac §12's
// guard (SEC-26, COR4-12):
//
//   - (a) a non-test file outside auth, the tasks executor
//     (core/internal/executor) and kernel imports authz at all. Counting the
//     import, not calls, covers every alias, function value and dot import,
//     and keeps Compile in scope too: handed a role set holding core:admin
//     and Interactive, it mints an interactive admin checker as surely as
//     the privileged constructors bypass roles;
//   - (b) rbac or any of its sub-packages imports authz, from a test file
//     included;
//   - (c) a non-test file outside authz itself carries a //go:linkname
//     directive naming authz, which reaches its functions without an import.
//
// It loads core, the module above the working directory, over the whole
// tag-set union.
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
		t.Errorf("%d import(s) or linkname(s) of %s outside auth, internal/executor and kernel, or an rbac import of it (05-rbac §3.4, SEC-26):\n  %s",
			len(findings), authzPath, strings.Join(findings, "\n  "))
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
		if p.ImportPath == authzPath {
			continue
		}
		for _, f := range p.GoFiles {
			scanned++
			found, err := authzUses(f, !allowed[p.ImportPath])
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	if scanned == 0 {
		return nil, fmt.Errorf("archtest: no non-test Go file outside authz under %s; the guard would be vacuous", dir)
	}
	sort.Strings(out)
	return uniq(out), nil
}

// authzUses parses one file and reports every //go:linkname directive naming
// authz and, when imports is set, every import of authz (blank and dot
// imports included).
func authzUses(file string, imports bool) ([]string, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", file, err)
	}
	var out []string
	if imports {
		for _, imp := range af.Imports {
			// The parser only accepts a well-formed quoted import path.
			if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == authzPath {
				out = append(out, fmt.Sprintf("%s: import of %s outside auth, internal/executor and kernel", fset.Position(imp.Pos()), authzPath))
			}
		}
	}
	for _, cg := range af.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:linkname ")
			if !ok {
				continue
			}
			for field := range strings.FieldsSeq(rest) {
				if field == authzPath || strings.HasPrefix(field, authzPath+".") {
					out = append(out, fmt.Sprintf("%s: //go:linkname names %s", fset.Position(c.Pos()), authzPath))
					break
				}
			}
		}
	}
	return out, nil
}
