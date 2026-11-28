// file: core/archtest/corescopes_test.go
// version: 1.0.0
// guid: 7e09cfc0-e477-42c8-bd49-659d38447f0f
// last-edited: 2026-11-28

package archtest

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestForeignScopeLiteralDetection plants one violation in every form the scan
// claims to cover, and the neighbours it must leave alone, so a scan that
// stopped matching a form fails here instead of passing over core.
func TestForeignScopeLiteralDetection(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"Need", "package p\nvar r = rbac.Need(\"books:edit\")", []string{`2: "books:edit"`}},
		{"AllOf, the foreign scope second", "package p\nvar r = rbac.AllOf(\"core:users.view\", \"books:edit\")", []string{`2: "books:edit"`}},
		{"AnyOf", "package p\nvar r = rbac.AnyOf(\"core:users.view\", \"gotham:patrol\")", []string{`2: "gotham:patrol"`}},
		{"ParsePermission", "package p\nvar _, err = rbac.ParsePermission(\"gotham:patrol\")", []string{`2: "gotham:patrol"`}},
		{"ParsePattern", "package p\nvar _, err = rbac.ParsePattern(\"gotham:*\")", []string{`2: "gotham:*"`}},
		{"a conversion", "package p\nvar p = rbac.Permission(\"gotham:patrol\")", []string{`2: "gotham:patrol"`}},
		{"a typed const", "package p\nconst P rbac.Permission = \"gotham:patrol\"", []string{`2: "gotham:patrol"`}},
		{"a typed var", "package p\nvar P rbac.Permission = \"gotham:patrol\"", []string{`2: "gotham:patrol"`}},
		{"a const converted", "package p\nconst P = rbac.Permission(\"gotham:patrol\")", []string{`2: "gotham:patrol"`}},
		{"a slice literal", "package p\nvar ps = []rbac.Permission{\"core:users.view\", \"gotham:patrol\"}", []string{`2: "gotham:patrol"`}},
		{"a typed var of a slice literal", "package p\nvar ps []rbac.Permission = []rbac.Permission{\"gotham:patrol\"}", []string{`2: "gotham:patrol"`}},
		{"a map literal's value", "package p\nvar m = map[string]rbac.Permission{\"gotham:key\": \"gotham:patrol\"}", []string{`2: "gotham:patrol"`}},
		{"a spec", "package p\nvar s = rbac.PermissionSpec{Name: \"gotham:patrol\", Title: \"gotham:title\"}", []string{`2: "gotham:patrol"`}},
		{"a spec's implies", "package p\nvar s = rbac.PermissionSpec{Name: \"core:x.y\", Implies: []rbac.Permission{\"gotham:patrol\"}}", []string{`2: "gotham:patrol"`}},
		{"inside a route literal", "package p\nvar r = httpapi.Route{Perm: rbac.Need(\"gotham:patrol\")}", []string{`2: "gotham:patrol"`}},
		{"bare names inside package rbac", "package rbac\nvar r = Need(\"gotham:patrol\")\nvar p = Permission(\"gotham:cave\")\nconst c Permission = \"gotham:signal\"",
			[]string{`2: "gotham:patrol"`, `3: "gotham:cave"`, `4: "gotham:signal"`}},
		// What it must leave alone.
		{"core scopes", "package p\nvar r = rbac.AllOf(\"core:users.view\", \"core:admin\")\nconst P rbac.Permission = \"core:system.manage\"", nil},
		{"a bare Need outside rbac is that package's own", "package p\nvar r = Need(\"gotham:patrol\")\nvar p = Permission(\"gotham:cave\")", nil},
		{"a string that is not a scope", "package p\nvar s = fmt.Sprintf(\"gotham:%s\", x)\nvar t = rbac.Need(name)", nil},
		{"a wildcard with no namespace", "package p\nvar p = rbac.Permission(\"*\")", nil},
		{"a map key", "package p\nvar m = map[string]rbac.Permission{\"gotham:key\": \"core:users.view\"}", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := foreignScopeLiterals("x.go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCoreScopesOnlyFixtureModule runs the whole check over a module with a
// planted violation in a core package, a violation in each exempt place, and a
// clean package: only the first is reported.
func TestCoreScopesOnlyFixtureModule(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.mod":              "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"kernel/ok.go":        "package kernel\n\nvar r = rbac.Need(\"core:system.manage\")\n",
		"kernel/bad.go":       "package kernel\n\nvar r = rbac.Need(\"books:integrations.manage\")\n",
		"kernel/bad_test.go":  "package kernel\n\nvar r = rbac.Need(\"books:in_test_files_is_fine\")\n",
		"examples/hello/p.go": "package hello\n\nconst P rbac.Permission = \"hello:admin\"\n",
		"coretest/h.go":       "package coretest\n\nvar r = rbac.Need(\"gotham:fixture\")\n",
		"rbac/rbactest/s.go":  "package rbactest\n\nvar r = rbac.Need(\"gotham:fixture\")\n",
	})
	rec := &recordingTB{TB: t}
	CoreScopesOnly(rec, root)
	if len(rec.errors) != 1 {
		t.Fatalf("%d errors %v, want exactly one", len(rec.errors), rec.errors)
	}
	msg := rec.errors[0]
	if !strings.Contains(msg, "kernel/bad.go:3") || !strings.Contains(msg, "books:integrations.manage") ||
		strings.Contains(msg, "ok.go") || strings.Contains(msg, "bad_test.go") || strings.Contains(msg, "hello") ||
		strings.Contains(msg, "coretest") || strings.Contains(msg, "rbactest") {
		t.Fatalf("report = %q, want only kernel/bad.go:3", msg)
	}
}

// TestCoreScopesOnlyEmptyModuleIsNotAPass: a module with no Go file is a fatal
// error, so a scan that read nothing cannot pass.
func TestCoreScopesOnlyEmptyModuleIsNotAPass(t *testing.T) {
	root := writeModule(t, map[string]string{"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n"})
	rec := &recordingTB{TB: t}
	func() {
		defer func() { _ = recover() }() // recordingTB.Fatalf stops the goroutine's work by panicking
		CoreScopesOnly(rec, root)
	}()
	if len(rec.fatals) != 1 || !strings.Contains(rec.fatals[0], "vacuous") {
		t.Fatalf("fatals = %v, want one vacuous-check failure", rec.fatals)
	}
}

// TestCoreScopesOnlyOverCore: no non-test core code names a scope outside the
// core namespace (W6): the app's scope on a core route is the app's
// RouteCoRequirements, never core's literal.
func TestCoreScopesOnlyOverCore(t *testing.T) {
	CoreScopesOnly(t, coreRoot(t))
}

// recordingTB records Errorf and Fatalf instead of failing the test, so a
// fixture can assert what the check reported.
type recordingTB struct {
	testing.TB
	errors []string
	fatals []string
}

func (r *recordingTB) Helper() {}
func (r *recordingTB) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
	panic("recordingTB: fatal")
}
