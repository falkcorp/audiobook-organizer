// file: internal/arch/plaindial_guard_test.go
// version: 1.0.0
// guid: 5b1e7c83-2d49-4f60-9a1c-e8d3b07f4a26
// last-edited: 2026-11-19

package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// plainDialSites are the only non-test files that may name mail.PlainDial: its
// declaration, and the invite flow's operator-relay sender. PlainDial is the
// unguarded SMTP dial; a host a non-admin can set must be dialled through the
// instance's guarded dial instead (docs/agent-tasks/platform-core/seams.d/
// F7-09-T2.md, obligation 3). The R2 change that moves the invite sender onto
// the notify/email instance removes the second entry rather than adding one.
var plainDialSites = []string{
	"core/integrations/notify/mail/mail.go",
	"internal/server/invite_guest_access.go",
}

// TestPlainDialHasOneCaller pins PlainDial's non-test references to
// plainDialSites, so a new unguarded dial cannot appear without this list (and
// a reviewer) seeing it.
func TestPlainDialHasOneCaller(t *testing.T) {
	root := repoRoot(t)
	skip := map[string]bool{
		".git": true, "node_modules": true, "web": true, ".worktrees": true,
		"third_party": true, ".standards": true, "vendor": true,
	}
	var found []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		named := false
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "PlainDial" {
				named = true
			}
			return !named
		})
		if named {
			rel, _ := filepath.Rel(root, path)
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(found)
	if !slices.Equal(found, plainDialSites) {
		t.Fatalf("non-test files naming PlainDial = %v, want exactly %v: PlainDial is the unguarded SMTP dial, "+
			"for a host only an operator controls; an instance's host goes through the guarded dial "+
			"(seams.d/F7-09-T2.md obligation 3)", found, plainDialSites)
	}
}
