// file: core/archtest/inviterule.go
// version: 0.1.0
// guid: 14248b62-16ec-42ba-8985-469562724292
// last-edited: 2026-11-22

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// isLegacyInviteMethod names the auth.InviteAdmin methods that answer the
// legacy surface's reach (auth.mayAdministerInvites): core:users.manage through
// any method, a key or the auth-disabled principal included.
func isLegacyInviteMethod(name string) bool {
	return name == "AdminInvites" || name == "AdminDeleteInvite"
}

// InviteRuleLegacyOnly fails t when a non-test Go file of the module rooted at
// dir names auth.InviteAdmin's AdminInvites or AdminDeleteInvite: a selector
// <x>.AdminInvites or <x>.AdminDeleteInvite, called or taken as a value.
//
// The two methods carry HEAD's reach for books' GET and DELETE
// /api/v1/users/invites (F4-08 B10y): no interactive leg and no user, so an
// API key holding core:users.manage and the auth-disabled principal pass. That
// reach is the legacy surface's only. A core-native route lists through
// Service.Invites behind its own route guards and deletes through
// Service.DeleteInvite (core:users.manage through an interactive method, a
// user), and a core package that reached for these two would widen it
// silently. auth.mayAdministerInvites is unexported, so only these two
// methods expose it outside package auth, where they are defined and not
// called. The shape is syntactic (no type check): any selector with either
// name is caught, which is the point.
func InviteRuleLegacyOnly(t testing.TB, dir string) {
	t.Helper()
	root, err := resolveRoot(dir)
	if err != nil {
		t.Fatalf("InviteRuleLegacyOnly: %v", err)
	}
	files, err := moduleSourceFiles(root, nil)
	if err != nil {
		t.Fatalf("InviteRuleLegacyOnly: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("InviteRuleLegacyOnly: no non-test Go file under %s; the check would be vacuous", dir)
	}
	var findings []string
	for _, f := range files {
		hits, err := legacyInviteRefs(f, nil)
		if err != nil {
			t.Fatalf("InviteRuleLegacyOnly: %v", err)
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			rel = f
		}
		for _, h := range hits {
			findings = append(findings, filepath.ToSlash(rel)+":"+h)
		}
	}
	sort.Strings(findings)
	if len(findings) > 0 {
		t.Errorf("%d reference(s) to the legacy-only invite methods in core; a core route lists through Service.Invites and deletes through Service.DeleteInvite, which keep core's own rule:\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// legacyInviteRefs parses one file (src when non-nil, else the file at path)
// and returns "line: .<Name>" for every selector naming a legacy invite method.
func legacyInviteRefs(path string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && isLegacyInviteMethod(sel.Sel.Name) {
			out = append(out, fmt.Sprintf("%d: .%s", fset.Position(sel.Pos()).Line, sel.Sel.Name))
		}
		return true
	})
	return out, nil
}
