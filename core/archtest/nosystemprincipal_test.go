// file: core/archtest/nosystemprincipal_test.go
// version: 1.0.0
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
// declares the two privileged constructors and Compile, and the three
// packages allowed to call them. extra adds or replaces files.
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

func System() { _ = az.ForSystem(nil); f := az.AuthDisabled; _ = f }
`,
		"internal/executor/executor.go": `package executor

import "github.com/falkcorp/mediacore/internal/authz"

func Run() { _ = authz.ForSystem([]string{"gotham:patrol"}) }
`,
		// A forbidden package may call Compile, and its tests may call the
		// privileged constructors: neither is reported.
		"httpapi/httpapi.go": `package httpapi

import "github.com/falkcorp/mediacore/internal/authz"

func Serve() { _ = authz.Compile() }
`,
		"httpapi/httpapi_test.go": `package httpapi

import (
	"testing"

	"github.com/falkcorp/mediacore/internal/authz"
)

func TestOff(t *testing.T) { _ = authz.ForSystem(nil); _ = authz.AuthDisabled() }
`,
		// A blank import names nothing.
		"events/events.go": "package events\n\nimport _ \"github.com/falkcorp/mediacore/internal/authz\"\n",
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

// TestNoSystemPrincipal_AllowedTree: the allowed callers, a Compile call in a
// forbidden package, a test file of a forbidden package and a blank import
// pass.
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
		// default name, a call of each constructor
		"httpapi/serve.go": `package httpapi

import "github.com/falkcorp/mediacore/internal/authz"

func Open() { _ = authz.AuthDisabled(); _ = authz.ForSystem(nil) }
`,
		// a renamed import and a reference that is not a call
		"tasks/tasks.go": `package tasks

import gordon "github.com/falkcorp/mediacore/internal/authz"

var _ = gordon.ForSystem

func Start() { f := gordon.AuthDisabled; _ = f }
`,
		// a dot import
		"outbox/outbox.go": `package outbox

import . "github.com/falkcorp/mediacore/internal/authz"

func Drain() { _ = ForSystem(nil) }
`,
		// a call seen only under the integration tag set
		"library/library_integration.go": `//go:build integration

package library

import "github.com/falkcorp/mediacore/internal/authz"

func Scan() { _ = authz.ForSystem(nil) }
`,
		"library/library.go": "package library\n",
		// a sub-package of an allowed caller is not allowed
		"auth/oidc/oidc.go": `package oidc

import "github.com/falkcorp/mediacore/internal/authz"

func Login() { _ = authz.AuthDisabled() }
`,
		// rbac's tree importing authz: a sub-package, and rbac's external test
		"rbac/rbacx/rbacx.go": "package rbacx\n\nimport _ \"github.com/falkcorp/mediacore/internal/authz\"\n",
		"rbac/rbac_test.go": `package rbac_test

import (
	"testing"

	_ "github.com/falkcorp/mediacore/internal/authz"
)

func TestJoker(t *testing.T) {}
`,
	})
	got, err := NoSystemPrincipalFindings(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"httpapi/serve.go":               2,
		"tasks/tasks.go":                 2,
		"outbox/outbox.go":               1,
		"library/library_integration.go": 1,
		"auth/oidc/oidc.go":              1,
		"import rbac/rbacx":              1,
		"import rbac":                    1,
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

// TestNoSystemPrincipal_Vacuous: a tree without authz, and one whose only
// files are allowed callers, are errors, not passes.
func TestNoSystemPrincipal_Vacuous(t *testing.T) {
	noAuthz := writeModule(t, map[string]string{
		"go.mod":     "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"auth/a.go":  "package auth\n",
		"other/o.go": "package other\n",
	})
	if _, err := NoSystemPrincipalFindings(noAuthz); err == nil || !strings.Contains(err.Error(), "vacuous") {
		t.Errorf("a tree without authz: err = %v, want a vacuity error", err)
	}
	onlyAllowed := writeModule(t, map[string]string{
		"go.mod":                  "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"internal/authz/authz.go": "package authz\n\nfunc ForSystem() {}\n",
		"kernel/kernel.go":        "package kernel\n\nimport \"github.com/falkcorp/mediacore/internal/authz\"\n\nfunc K() { authz.ForSystem() }\n",
	})
	if _, err := NoSystemPrincipalFindings(onlyAllowed); err == nil || !strings.Contains(err.Error(), "vacuous") {
		t.Errorf("a tree of allowed callers only: err = %v, want a vacuity error", err)
	}
}

// TestNoSystemPrincipalOutsideKernel runs the guard over the real core
// module.
func TestNoSystemPrincipalOutsideKernel(t *testing.T) {
	NoSystemPrincipalOutsideKernel(t)
}
