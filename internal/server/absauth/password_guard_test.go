// file: internal/server/absauth/password_guard_test.go
// version: 1.0.0
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
	"strings"
	"testing"
)

// TestNoDirectBcryptVerification fails when production code under internal/ or cmd/
// verifies a password with bcrypt.CompareHashAndPassword instead of VerifyPassword.
// Stored hashes are argon2id or bcrypt depending on PasswordHashAlgo (the ABS
// sign-in rehashes bcrypt users to argon2id), so a direct bcrypt compare silently
// rejects every rehashed user. VerifyPassword itself is the only allowed caller.
func TestNoDirectBcryptVerification(t *testing.T) {
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
	for _, top := range []string{"internal", "cmd"} {
		walkErr := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == allowed {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "CompareHashAndPassword" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "bcrypt" {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s: bcrypt.CompareHashAndPassword called directly; use absauth.VerifyPassword", rel)
				}
				return true
			})
			return nil
		})
		if walkErr != nil {
			t.Fatal(walkErr)
		}
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d files; the walk is broken, not the code", scanned)
	}
}
