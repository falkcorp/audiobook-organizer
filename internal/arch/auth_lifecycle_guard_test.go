// file: internal/arch/auth_lifecycle_guard_test.go
// version: 0.1.0
// guid: 6c0e4b97-3a15-4d82-9f6b-e21a8d70c534
// last-edited: 2026-11-22

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

// Manifest.AuthLifecycle hands a module Deps.AuthLifecycle on an identity boot: the
// power to observe every account event (accepted invite, status change, e-mail
// change, delete, merge) after it commits, with ids and the minimal facts. It cannot
// veto a write and holds no store, but it is still a view of every account change,
// so it is books' alone, for the Cloudflare Access guest policy. The rule is read off
// the syntax of every non-test file: no composite literal sets the field to anything
// but false outside the one file that declares it for books. The same shape as the
// Manifest.AuthRefresh guard (auth_refresh_guard_test.go).

const (
	authLifecycleField = "AuthLifecycle"
	authLifecycleOwner = "internal/books/module.go"
)

// declarationProblems judges the non-test Go files (path to source) for a Manifest
// field only owner may set to something other than false.
func declarationProblems(files map[string][]byte, field, owner, lost string) []string {
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
			if !ok || key.Name != field {
				return true
			}
			if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "false" {
				return true
			}
			declared = true
			if p != owner {
				out = append(out, fmt.Sprintf("%s:%d: sets %s: only %s (books) may declare it", p, fset.Position(kv.Pos()).Line, field, owner))
			}
			return true
		})
		if p == owner {
			sawOwner = declared
		}
	}
	if !sawOwner {
		out = append(out, owner+" no longer declares "+field+": "+lost)
	}
	return out
}

func authLifecycleProblems(files map[string][]byte) []string {
	return declarationProblems(files, authLifecycleField, authLifecycleOwner,
		"books' guests would keep their Cloudflare entry after a disable, delete or merge, or the declaration moved and this guard guards nothing")
}

// TestAuthLifecycle_OnlyBooksDeclaresIt pins the rule over the real tree.
func TestAuthLifecycle_OnlyBooksDeclaresIt(t *testing.T) {
	if probs := authLifecycleProblems(productionGoFiles(t)); len(probs) > 0 {
		t.Fatalf("Manifest.AuthLifecycle guard found %d problem(s):\n%s", len(probs), strings.Join(probs, "\n"))
	}
}

// TestAuthLifecycleGuard_CatchesEachBypass: the guard fails closed on a second
// declarer and on books losing the declaration; a false value is not a declaration.
func TestAuthLifecycleGuard_CatchesEachBypass(t *testing.T) {
	books := []byte("package books\nvar m = module.Manifest{AuthLifecycle: true}\n")
	cases := map[string]struct {
		files map[string][]byte
		want  string
	}{
		"books alone is clean": {files: map[string][]byte{authLifecycleOwner: books}},
		"a second module declaring it": {
			files: map[string][]byte{authLifecycleOwner: books, "internal/plugins/x/x.go": []byte("package x\nvar m = module.Manifest{AuthLifecycle: true}\n")},
			want:  "only internal/books/module.go",
		},
		"a second module computing it": {
			files: map[string][]byte{authLifecycleOwner: books, "internal/plugins/x/x.go": []byte("package x\nvar m = module.Manifest{AuthLifecycle: on()}\n")},
			want:  "only internal/books/module.go",
		},
		"an explicit false is not a declaration": {
			files: map[string][]byte{authLifecycleOwner: books, "internal/plugins/x/x.go": []byte("package x\nvar m = module.Manifest{AuthLifecycle: false}\n")},
		},
		"books lost it": {
			files: map[string][]byte{authLifecycleOwner: []byte("package books\nvar m = module.Manifest{}\n")},
			want:  "no longer declares",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			probs := authLifecycleProblems(tc.files)
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
