// file: core/kernel/catalog_test.go
// version: 1.0.0
// guid: e65f18d1-2a0c-4e65-b360-dfbb1e69009a
// last-edited: 2026-11-10

package kernel

import (
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// TestKernel_DeclaresCoreScopesWithNoSubsystemWired is the kernel half of
// 05-rbac §12's "a kernel with no optional core subsystem wired declares every
// core:* scope" (05-rbac §5 item 3, IMPL-4). declareCoreCatalog takes no
// subsystem, so the catalog it returns cannot depend on one.
func TestKernel_DeclaresCoreScopesWithNoSubsystemWired(t *testing.T) {
	c, err := declareCoreCatalog()
	if err != nil {
		t.Fatalf("declareCoreCatalog: %v", err)
	}

	var got []rbac.PermissionSpec
	for s := range c.Namespace("core") {
		got = append(got, s)
	}
	want := rbac.CoreScopes()
	if len(want) == 0 {
		t.Fatal("rbac.CoreScopes is empty")
	}
	byName := func(a, b rbac.PermissionSpec) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	}
	slices.SortFunc(want, byName)
	slices.SortFunc(got, byName)
	if len(got) != len(want) {
		t.Fatalf("core namespace holds %d scopes, want %d", len(got), len(want))
	}
	for i := range want {
		if !specEqual(got[i], want[i]) {
			t.Errorf("core scope %s differs from rbac.CoreScopes:\n got  %+v\n want %+v", want[i].Name, got[i], want[i])
		}
	}

	// The kernel declares core's scopes and nothing else.
	n := 0
	for range c.All() {
		n++
	}
	if n != len(want) {
		t.Errorf("catalog holds %d scopes, want only core's %d", n, len(want))
	}
}

// TestKernel_DeclaresFreshCatalogPerCall: nothing is shared between calls.
func TestKernel_DeclaresFreshCatalogPerCall(t *testing.T) {
	a, err := declareCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	b, err := declareCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("declareCoreCatalog returned the same *Catalog twice")
	}
}

func specEqual(a, b rbac.PermissionSpec) bool {
	return a.Name == b.Name && a.Title == b.Title && a.Description == b.Description &&
		a.Class == b.Class && a.Sensitive == b.Sensitive && a.AuthRequired == b.AuthRequired &&
		a.Confining == b.Confining && a.Warn == b.Warn && a.Deprecated == b.Deprecated &&
		slices.Equal(a.DefaultGrants, b.DefaultGrants) && slices.Equal(a.Implies, b.Implies)
}
