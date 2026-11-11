// file: core/archtest/nodirectbcrypt.go
// version: 1.0.0
// guid: ae7ceb07-b3d4-4e4b-99c5-d51f210882b0
// last-edited: 2026-11-10

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const bcryptImportPath = "golang.org/x/crypto/bcrypt"

// NoDirectBcrypt fails t when a non-test Go file of the module rooted at dir
// calls bcrypt.CompareHashAndPassword or bcrypt.GenerateFromPassword, other
// than the files in allowed (slash paths relative to dir). Stored password
// hashes are argon2id or bcrypt, and core/auth/password verifies both and
// rehashes bcrypt to argon2id (05-authn §3.2), so a direct compare silently
// rejects every rehashed user and a direct hash writes a second, divergent
// hashing policy. It walks the module's directories (moduleSourceFiles), so a
// file behind any build tag is read too. An allowed entry that names no
// scanned file fails, so an exemption cannot outlive its file. Moved from
// ABO's absauth password_guard_test.go (F4-02) so both modules run one
// implementation.
func NoDirectBcrypt(t testing.TB, dir string, allowed []string) {
	t.Helper()
	root, err := resolveRoot(dir)
	if err != nil {
		t.Fatalf("NoDirectBcrypt: %v", err)
	}
	all, err := moduleSourceFiles(root, nil)
	if err != nil {
		t.Fatalf("NoDirectBcrypt: %v", err)
	}
	seen := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		seen[a] = false
	}
	var files []string
	for _, f := range all {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			t.Fatalf("NoDirectBcrypt: %v", err)
		}
		if _, ok := seen[filepath.ToSlash(rel)]; ok {
			seen[filepath.ToSlash(rel)] = true
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("NoDirectBcrypt: no non-test Go file under %s; the check would be vacuous", dir)
	}
	for _, a := range allowed {
		if !seen[a] {
			t.Errorf("NoDirectBcrypt: allowed file %s is not a scanned non-test file; drop the stale exemption", a)
		}
	}
	findings, err := NoDirectBcryptFindings(files)
	if err != nil {
		t.Fatalf("NoDirectBcrypt: %v", err)
	}
	if len(findings) > 0 {
		t.Errorf("%d direct bcrypt call(s); hash and verify through core/auth/password:\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// NoDirectBcryptFindings parses files and returns "file:line: bcrypt.Func"
// for every banned bcrypt call, sorted.
func NoDirectBcryptFindings(files []string) ([]string, error) {
	var out []string
	for _, f := range uniq(append([]string(nil), files...)) {
		hits, err := bcryptMisuse(f, nil)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			out = append(out, f+":"+h)
		}
	}
	sort.Strings(out)
	return out, nil
}

// bcryptBanned reports whether name is a bcrypt entry point production code
// must not call.
func bcryptBanned(name string) bool {
	return name == "CompareHashAndPassword" || name == "GenerateFromPassword"
}

// bcryptMisuse parses one Go source and returns "line: bcrypt.Func" for every
// call into golang.org/x/crypto/bcrypt of a banned function. It resolves the
// package by import path, not by the identifier "bcrypt", so an alias
// (bc "…/bcrypt") or a dot import is caught, and an unrelated local variable
// named bcrypt is not.
func bcryptMisuse(filename string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", filename, err)
	}
	names := map[string]bool{} // local names bound to the bcrypt package
	dotImport := false
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != bcryptImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			names["bcrypt"] = true
		case imp.Name.Name == ".":
			dotImport = true
		case imp.Name.Name != "_":
			names[imp.Name.Name] = true
		}
	}
	if len(names) == 0 && !dotImport {
		return nil, nil
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && names[id.Name] && bcryptBanned(x.Sel.Name) {
				hits = append(hits, strconv.Itoa(fset.Position(x.Pos()).Line)+": bcrypt."+x.Sel.Name)
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && dotImport && bcryptBanned(id.Name) {
				hits = append(hits, strconv.Itoa(fset.Position(x.Pos()).Line)+": bcrypt."+id.Name)
			}
		}
		return true
	})
	return hits, nil
}
