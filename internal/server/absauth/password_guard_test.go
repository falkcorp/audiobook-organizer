// file: internal/server/absauth/password_guard_test.go
// version: 2.0.0
// guid: b81d3e64-2a97-4c05-8f1e-d7a09c5e3b72
// last-edited: 2026-10-19

package absauth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const bcryptImportPath = "golang.org/x/crypto/bcrypt"

// bcryptBanned are the bcrypt entry points production code must not call. Stored
// hashes are argon2id or bcrypt depending on PasswordHashAlgo (the ABS sign-in
// rehashes bcrypt users to argon2id), so a direct compare silently rejects every
// rehashed user, and a direct hash writes a second, divergent hashing policy.
// HashPassword and VerifyPassword in password.go are the only allowed callers.
var bcryptBanned = map[string]bool{
	"CompareHashAndPassword": true,
	"GenerateFromPassword":   true,
}

// bcryptMisuse parses one Go source and returns "line: bcrypt.Func" for every call
// into golang.org/x/crypto/bcrypt of a banned function. It resolves the package by
// import path, not by the identifier "bcrypt", so an alias (bc "…/bcrypt") or a dot
// import is caught, and an unrelated local variable named bcrypt is not.
func bcryptMisuse(filename string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
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
			if id, ok := x.X.(*ast.Ident); ok && names[id.Name] && bcryptBanned[x.Sel.Name] {
				hits = append(hits, strconv.Itoa(fset.Position(x.Pos()).Line)+": bcrypt."+x.Sel.Name)
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && dotImport && bcryptBanned[id.Name] {
				hits = append(hits, strconv.Itoa(fset.Position(x.Pos()).Line)+": bcrypt."+id.Name)
			}
		}
		return true
	})
	return hits, nil
}

func TestBcryptMisuseDetection(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"plain verify", `package p
import "golang.org/x/crypto/bcrypt"
func f() { _ = bcrypt.CompareHashAndPassword(nil, nil) }`, []string{"3: bcrypt.CompareHashAndPassword"}},
		{"plain hash", `package p
import "golang.org/x/crypto/bcrypt"
func f() { bcrypt.GenerateFromPassword(nil, bcrypt.DefaultCost) }`, []string{"3: bcrypt.GenerateFromPassword"}},
		{"alias", `package p
import bc "golang.org/x/crypto/bcrypt"
func f() { _ = bc.CompareHashAndPassword(nil, nil) }`, []string{"3: bcrypt.CompareHashAndPassword"}},
		{"dot import", `package p
import . "golang.org/x/crypto/bcrypt"
func f() { _ = CompareHashAndPassword(nil, nil) }`, []string{"3: bcrypt.CompareHashAndPassword"}},
		{"unrelated local named bcrypt", `package p
type h struct{}
func (h) CompareHashAndPassword() {}
func f() { bcrypt := h{}; bcrypt.CompareHashAndPassword() }`, nil},
		{"alias of another package named bcrypt", `package p
import bcrypt "example.com/other/bcrypt"
func f() { _ = bcrypt.CompareHashAndPassword(nil, nil) }`, nil},
		{"bcrypt imported but only the cost constant used", `package p
import "golang.org/x/crypto/bcrypt"
var c = bcrypt.DefaultCost`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bcryptMisuse("x.go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNoDirectBcrypt fails when production code in this module calls
// bcrypt.CompareHashAndPassword or bcrypt.GenerateFromPassword outside
// password.go. It walks the root package, internal/, cmd/, pkg/ and tools/, skips
// testdata, hidden and underscore directories as the go tool does, and skips
// _test.go files.
func TestNoDirectBcrypt(t *testing.T) {
	// os.Getwd, not runtime.Caller: CI builds with -trimpath.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatalf("go.mod not found above %s", dir)
		}
		root = parent
	}

	allowed := filepath.Join(root, "internal", "server", "absauth", "password.go")
	scanned := 0
	check := func(path string) {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == allowed {
			return
		}
		scanned++
		hits, err := bcryptMisuse(path, nil)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return
		}
		rel, _ := filepath.Rel(root, path)
		for _, h := range hits {
			t.Errorf("%s:%s; use absauth.HashPassword / absauth.VerifyPassword", rel, h)
		}
	}

	rootEntries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rootEntries {
		if !e.IsDir() {
			check(filepath.Join(root, e.Name()))
		}
	}
	for _, top := range []string{"internal", "cmd", "pkg", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if path != filepath.Join(root, top) &&
					(name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
					return fs.SkipDir
				}
				return nil
			}
			check(path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d files; the walk is broken, not the code", scanned)
	}
}
