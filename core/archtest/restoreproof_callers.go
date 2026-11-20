// file: core/archtest/restoreproof_callers.go
// version: 0.1.0
// guid: 6fb0d703-b07a-4817-b7d0-9745f2256666
// last-edited: 2026-11-20

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

// backupPath declares WithRestoreProof; backuphttpPath is the one package whose
// non-test files may call it.
const (
	backupPath       = mediacorePath + "/backup"
	backuphttpPath   = mediacorePath + "/internal/backuphttp"
	restoreProofFunc = "WithRestoreProof"
)

// RestoreProofCallers fails t when a non-test file outside
// core/internal/backuphttp references backup.WithRestoreProof, backup's own
// files included. The proof is what lets StageRestore and RevertLastRestore
// run: 05-backup §5B.3.4 makes the restore a credential-change route with
// owner proof, and backuphttp mints the proof only behind that route's
// Interactive and Owner guards (§5B.3.8). Any other caller, a task body in
// backup among them, would be a way around those guards. Test files are
// exempt. It loads core, the module above the working directory, over the
// whole tag-set union.
func RestoreProofCallers(t testing.TB) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("RestoreProofCallers: %v", err)
	}
	root, err := ModuleRoot(wd)
	if err != nil {
		t.Fatalf("RestoreProofCallers: %v", err)
	}
	findings, err := RestoreProofCallerFindings(root)
	if err != nil {
		t.Fatalf("RestoreProofCallers: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d use(s) of %s.%s outside core/internal/backuphttp and test files (the owner proof is minted behind the restore routes' guards only):\n  %s",
			len(findings), backupPath, restoreProofFunc, strings.Join(findings, "\n  "))
	}
}

// RestoreProofCallerFindings loads the core module at dir over TagSets() and
// returns one line per use RestoreProofCallers reports, sorted. It returns an
// error when the load fails, when backup is not among the loaded packages, or
// when no non-test file was scanned: each would make the guard pass without
// looking.
func RestoreProofCallerFindings(dir string) ([]string, error) {
	pkgs, err := Load(LoadOptions{Dir: dir, TagSets: TagSets()})
	if err != nil {
		return nil, err
	}
	if _, ok := pkgs[backupPath]; !ok {
		return nil, fmt.Errorf("archtest: %s is not among the %d loaded packages; the guard would be vacuous", backupPath, len(pkgs))
	}
	var out []string
	scanned := 0
	for _, p := range pkgs {
		if p.ImportPath == backuphttpPath {
			continue
		}
		for _, f := range p.GoFiles {
			scanned++
			found, err := restoreProofRefs(f, p.ImportPath == backupPath)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	if scanned == 0 {
		return nil, fmt.Errorf("archtest: no non-test Go file outside backuphttp under %s; the guard would be vacuous", dir)
	}
	sort.Strings(out)
	return uniq(out), nil
}

// restoreProofRefs parses one file and reports every selector through an
// import of backup that names WithRestoreProof, every dot-imported use, every
// //go:linkname naming it and, in backup's own files (self), every use other
// than its declaration.
func restoreProofRefs(file string, self bool) ([]string, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", file, err)
	}
	names := map[string]bool{}
	bare := self // an unqualified identifier names the function
	for _, imp := range af.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != backupPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["backup"] = true
		case imp.Name.Name == ".":
			bare = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	var out []string
	report := func(pos token.Pos) {
		out = append(out, fmt.Sprintf("%s: %s.%s", fset.Position(pos), backupPath, restoreProofFunc))
	}
	skip := map[*ast.Ident]bool{} // selector fields and the declaration's name
	ast.Inspect(af, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			skip[n.Sel] = true
			if x, ok := n.X.(*ast.Ident); ok && names[x.Name] && n.Sel.Name == restoreProofFunc {
				report(n.Pos())
			}
		case *ast.FuncDecl:
			skip[n.Name] = true
		case *ast.Ident:
			if bare && n.Name == restoreProofFunc && !skip[n] {
				report(n.Pos())
			}
		}
		return true
	})
	for _, cg := range af.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:linkname ")
			if ok && strings.Contains(" "+rest+" ", " "+backupPath+"."+restoreProofFunc+" ") {
				report(c.Pos())
			}
		}
	}
	return out, nil
}
