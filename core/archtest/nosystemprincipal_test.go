// file: core/archtest/nosystemprincipal_test.go
// version: 1.0.1
// guid: defaa454-c7bf-4aa1-b9d2-821d1bdcccb9
// last-edited: 2026-11-10

package archtest

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

// systemPrincipalTree is a fixture core module: an rbac leaf, an authz that
// declares Compile and the two privileged constructors, and the three
// packages allowed to import it. extra adds or replaces files.
func systemPrincipalTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod":       "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"rbac/rbac.go": "package rbac\n\ntype Checker interface{ Has(string) bool }\n",
		"internal/authz/authz.go": `package authz

import "github.com/falkcorp/mediacore/rbac"

func Compile() rbac.Checker          { return nil }
func AuthDisabled() rbac.Checker     { return nil }
func ForSystem(p []string) rbac.Checker { return nil }
`,
		"auth/auth.go": `package auth

import "github.com/falkcorp/mediacore/internal/authz"

func Off() { _ = authz.AuthDisabled() }
`,
		"kernel/kernel.go": `package kernel

import az "github.com/falkcorp/mediacore/internal/authz"

func System() { _ = az.ForSystem(nil); _ = az.Compile(); f := az.AuthDisabled; _ = f }
`,
		"internal/executor/executor.go": `package executor

import "github.com/falkcorp/mediacore/internal/authz"

func Run() { _ = authz.ForSystem([]string{"gotham:patrol"}) }
`,
		// A forbidden package's tests may import authz: test files are not
		// scanned by rule (a).
		"httpapi/httpapi.go": "package httpapi\n\nfunc Serve() {}\n",
		"httpapi/httpapi_test.go": `package httpapi

import (
	"testing"

	"github.com/falkcorp/mediacore/internal/authz"
)

func TestOff(t *testing.T) { _ = authz.ForSystem(nil); _ = authz.AuthDisabled(); _ = authz.Compile() }
`,
	}
	maps.Copy(files, extra)
	return writeModule(t, files)
}

// findingsByFile counts findings per module-relative file (or per importing
// package for an import finding).
func findingsByFile(root string, findings []string) map[string]int {
	// go list reports the resolved directory (macOS's /var is /private/var).
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	out := map[string]int{}
	for _, f := range findings {
		if pkg, _, ok := strings.Cut(f, " imports "); ok && !strings.Contains(pkg, ":") {
			out["import "+strings.TrimPrefix(pkg, mediacorePath+"/")]++
			continue
		}
		file, _, _ := strings.Cut(f, ":")
		if rel, err := filepath.Rel(root, file); err == nil {
			file = filepath.ToSlash(rel)
		}
		out[file]++
	}
	return out
}

// TestNoSystemPrincipal_AllowedTree: the three allowed importers (one of
// them calling Compile and taking a function value) and a forbidden
// package's test file pass.
func TestNoSystemPrincipal_AllowedTree(t *testing.T) {
	root := systemPrincipalTree(t, nil)
	got, err := NoSystemPrincipalFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the allowed tree reported findings:\n  %s", strings.Join(got, "\n  "))
	}
}

// TestNoSystemPrincipal_ForbiddenTree: every forbidden shape is reported,
// with an exact count per file.
func TestNoSystemPrincipal_ForbiddenTree(t *testing.T) {
	root := systemPrincipalTree(t, map[string]string{
		// an import used only for Compile is still an import
		"httpapi/httpapi.go": `package httpapi

import "github.com/falkcorp/mediacore/internal/authz"

func Serve() { _ = authz.Compile() }
`,
		// a renamed import taking function values
		"tasks/tasks.go": `package tasks

import gordon "github.com/falkcorp/mediacore/internal/authz"

var _ = gordon.ForSystem

func Start() { f := gordon.AuthDisabled; _ = f }
`,
		// a dot import and a blank import
		"outbox/outbox.go": `package outbox

import . "github.com/falkcorp/mediacore/internal/authz"

func Drain() { _ = ForSystem(nil) }
`,
		"events/events.go": "package events\n\nimport _ \"github.com/falkcorp/mediacore/internal/authz\"\n",
		// an import seen only under the integration tag set
		"library/library_integration.go": `//go:build integration

package library

import "github.com/falkcorp/mediacore/internal/authz"

func Scan() { _ = authz.ForSystem(nil) }
`,
		"library/library.go": "package library\n",
		// a sub-package of an allowed importer is not allowed
		"auth/oidc/oidc.go": `package oidc

import "github.com/falkcorp/mediacore/internal/authz"

func Login() { _ = authz.AuthDisabled() }
`,
		// rbac's tree importing authz: a sub-package (also a forbidden
		// importer) and rbac's external test
		"rbac/rbacx/rbacx.go": "package rbacx\n\nimport _ \"github.com/falkcorp/mediacore/internal/authz\"\n",
		"rbac/rbac_test.go": `package rbac_test

import (
	"testing"

	_ "github.com/falkcorp/mediacore/internal/authz"
)

func TestJoker(t *testing.T) {}
`,
		// a linkname reaches authz without an import, in a forbidden package
		// and in an allowed one alike
		"metrics/link.go": `package metrics

import _ "unsafe"

//go:linkname forSystem github.com/falkcorp/mediacore/internal/authz.ForSystem
func forSystem(p []string) any
`,
		"kernel/link.go": `package kernel

import _ "unsafe"

//go:linkname compile github.com/falkcorp/mediacore/internal/authz.Compile
func compile() any
`,
	})
	got, err := NoSystemPrincipalFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"httpapi/httpapi.go":             1,
		"tasks/tasks.go":                 1,
		"outbox/outbox.go":               1,
		"events/events.go":               1,
		"library/library_integration.go": 1,
		"auth/oidc/oidc.go":              1,
		"rbac/rbacx/rbacx.go":            1,
		"import rbac/rbacx":              1,
		"import rbac":                    1,
		"metrics/link.go":                1,
		"kernel/link.go":                 1,
	}
	count := findingsByFile(root, got)
	for k, n := range want {
		if count[k] != n {
			t.Errorf("%s: %d findings, want %d", k, count[k], n)
		}
	}
	for k, n := range count {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected finding(s) in %s: %d", k, n)
		}
	}
	if t.Failed() {
		t.Logf("findings:\n  %s", strings.Join(got, "\n  "))
	}
}

// TestNoSystemPrincipal_Vacuous: a tree without authz, and one with no
// non-test file outside authz, are errors, not passes.
func TestNoSystemPrincipal_Vacuous(t *testing.T) {
	noAuthz := writeModule(t, map[string]string{
		"go.mod":     "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"auth/a.go":  "package auth\n",
		"other/o.go": "package other\n",
	})
	if _, err := NoSystemPrincipalFindings(noAuthz); err == nil || !strings.Contains(err.Error(), "vacuous") {
		t.Errorf("a tree without authz: err = %v, want a vacuity error", err)
	}
	onlyAuthz := writeModule(t, map[string]string{
		"go.mod":                  "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"internal/authz/authz.go": "package authz\n\nfunc ForSystem() {}\n",
	})
	if _, err := NoSystemPrincipalFindings(onlyAuthz); err == nil || !strings.Contains(err.Error(), "vacuous") {
		t.Errorf("a tree of authz only: err = %v, want a vacuity error", err)
	}
}

// TestNoSystemPrincipalOutsideKernel runs the guard over the real core
// module.
func TestNoSystemPrincipalOutsideKernel(t *testing.T) {
	NoSystemPrincipalOutsideKernel(t)
}
