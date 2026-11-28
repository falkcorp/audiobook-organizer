// file: internal/arch/auth_refresh_guard_test.go
// version: 0.1.0
// guid: e8b2f6a4-90d3-4c17-a5e8-3f7d1b46c092
// last-edited: 2026-11-21

package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// Manifest.AuthRefresh hands a module Deps.AuthRefresh on an identity boot: the
// power to re-derive the principal stage 12 bound (re-resolve the account after a
// sign-in step, keeping the credential's identity). It is books' alone, because
// books' ABS surface is the one flow that signs a user in below stage 12. A module
// that declares it without a reason is a module that can rewrite who a request is.
// The rule is read off the syntax of every non-test file: no composite literal sets
// the field to anything but false outside the one file that declares it for books.

const (
	authRefreshField = "AuthRefresh"
	authRefreshOwner = "internal/books/module.go"
)

// authRefreshProblems judges the non-test Go files (path to source).
func authRefreshProblems(files map[string][]byte) []string {
	var out []string
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	fset := token.NewFileSet()
	sawOwner := false
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, files[p], parser.SkipObjectResolution)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: unparsable: %v", p, err))
			continue
		}
		declared := false
		ast.Inspect(f, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != authRefreshField {
				return true
			}
			if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "false" {
				return true
			}
			declared = true
			if p != authRefreshOwner {
				out = append(out, fmt.Sprintf("%s:%d: sets %s: only %s (books) may declare it", p, fset.Position(kv.Pos()).Line, authRefreshField, authRefreshOwner))
			}
			return true
		})
		if p == authRefreshOwner {
			sawOwner = declared
		}
	}
	if !sawOwner {
		out = append(out, authRefreshOwner+" no longer declares "+authRefreshField+": books' ABS sign-in would run without Deps.AuthRefresh, or the declaration moved and this guard guards nothing")
	}
	return out
}

// TestAuthRefresh_OnlyBooksDeclaresIt pins the rule over the real tree.
func TestAuthRefresh_OnlyBooksDeclaresIt(t *testing.T) {
	if probs := authRefreshProblems(productionGoFiles(t)); len(probs) > 0 {
		t.Fatalf("Manifest.AuthRefresh guard found %d problem(s):\n%s", len(probs), strings.Join(probs, "\n"))
	}
}

// TestAuthRefreshGuard_CatchesEachBypass: the guard fails closed on a second
// declarer and on books losing the declaration; a false value is not a declaration.
func TestAuthRefreshGuard_CatchesEachBypass(t *testing.T) {
	books := []byte("package books\nvar m = module.Manifest{AuthRefresh: true}\n")
	cases := map[string]struct {
		files map[string][]byte
		want  string
	}{
		"books alone is clean": {files: map[string][]byte{authRefreshOwner: books}},
		"a second module declaring it": {
			files: map[string][]byte{authRefreshOwner: books, "internal/plugins/x/x.go": []byte("package x\nvar m = module.Manifest{AuthRefresh: true}\n")},
			want:  "only internal/books/module.go",
		},
		"a second module computing it": {
			files: map[string][]byte{authRefreshOwner: books, "internal/plugins/x/x.go": []byte("package x\nvar m = module.Manifest{AuthRefresh: on()}\n")},
			want:  "only internal/books/module.go",
		},
		"an explicit false is not a declaration": {
			files: map[string][]byte{authRefreshOwner: books, "internal/plugins/x/x.go": []byte("package x\nvar m = module.Manifest{AuthRefresh: false}\n")},
		},
		"books lost it": {
			files: map[string][]byte{authRefreshOwner: []byte("package books\nvar m = module.Manifest{}\n")},
			want:  "no longer declares",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probs := authRefreshProblems(tc.files)
			if tc.want == "" {
				if len(probs) != 0 {
					t.Fatalf("a clean tree was flagged: %v", probs)
				}
				return
			}
			if len(probs) == 0 {
				t.Fatalf("no problem reported, want one containing %q", tc.want)
			}
			found := false
			for _, p := range probs {
				if strings.Contains(p, tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("problems %v do not contain %q", probs, tc.want)
			}
		})
	}
}
