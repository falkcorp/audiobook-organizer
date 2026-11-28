// file: core/rbac/operator_test.go
// version: 0.1.0
// guid: 5a90c3e7-d1b2-4f68-8e34-72b6f0a1d9c5
// last-edited: 2026-11-28

package rbac

import (
	"slices"
	"testing"
)

// TestOperatorRequirement pins the operator surfaces' requirement: an AllOf of
// the seven core scopes of HEAD's settings.manage atom, each declared in core's
// catalog, and core:system.manage is only one of them.
func TestOperatorRequirement(t *testing.T) {
	req := OperatorRequirement()
	if req.Kind() != KindAllOf {
		t.Fatalf("OperatorRequirement kind = %v, want AllOf", req.Kind())
	}
	want := []Permission{
		"core:settings.manage", "core:tasks.manage", "core:system.view", "core:system.manage",
		"core:library.manage", "core:library.browse", "core:integrations.manage",
	}
	if got := req.Permissions(); !slices.Equal(got, want) {
		t.Fatalf("OperatorRequirement scopes = %v, want %v", got, want)
	}
	cat, err := NewCatalog(CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range want {
		if _, ok := cat.Lookup(p); !ok {
			t.Errorf("%s is not a core scope", p)
		}
	}
}
