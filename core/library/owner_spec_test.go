// file: core/library/owner_spec_test.go
// version: 0.1.0
// guid: 3b0c4f60-7a52-4a0e-9f2b-0d54b1c7e2a8
// last-edited: 2026-11-22

package library

import (
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

func TestOwnerAndStorageSpec(t *testing.T) {
	if Owner != "core/library" {
		t.Fatalf("Owner = %q", Owner)
	}
	if Owner == storage.Owner(CoreOwner) {
		t.Fatal("the storage owner must not be the Scope caller identity")
	}
	s := StorageSpec()
	if !s.Keyspace || len(s.Serialized) != 0 {
		t.Fatalf("StorageSpec = %+v, want a bare core keyspace", s)
	}
}

func TestRoleRequirementValidate(t *testing.T) {
	cases := []struct {
		name string
		r    RoleRequirement
		bad  bool
	}{
		{"library", RoleRequirement{Role: RoleLibrary, Required: true, Why: "Alfred files the Batcave archive here"}, false},
		{"optional external", RoleRequirement{Role: RoleExternal, Why: "Read-only Gotham catalog"}, false},
		{"zero role", RoleRequirement{Why: "x"}, true},
		{"role past external", RoleRequirement{Role: RoleExternal + 1, Why: "x"}, true},
		{"empty why", RoleRequirement{Role: RoleImport, Required: true}, true},
		{"blank why", RoleRequirement{Role: RoleImport, Why: " \t"}, true},
	}
	for _, c := range cases {
		err := c.r.Validate()
		if c.bad != (err != nil) {
			t.Errorf("%s: Validate = %v, want bad=%v", c.name, err, c.bad)
		}
		if err != nil && !errors.Is(err, ErrBadRoleRequirement) {
			t.Errorf("%s: %v does not wrap ErrBadRoleRequirement", c.name, err)
		}
	}
}
