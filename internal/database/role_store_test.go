// file: internal/database/role_store_test.go
// version: 1.0.1
// guid: 4d8a2e1f-5c3b-4f80-a9d6-2f7e0c1b9a48

package database

import (
	"path/filepath"
	"testing"
)

func TestRoleStore_CreateGetUpdate(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	r := &Role{ID: "admin", Name: "admin", Description: "Full access",
		Permissions: []string{"library.view", "library.edit_metadata", "users.manage"},
		IsSeed:      true}
	created, err := store.CreateRole(r)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID != "admin" {
		t.Fatalf("ID = %q, want admin", created.ID)
	}
	if created.Version != 1 {
		t.Errorf("Version = %d, want 1", created.Version)
	}

	got, err := store.GetRoleByID("admin")
	if err != nil || got == nil {
		t.Fatalf("GetRoleByID: %v, %v", got, err)
	}
	if got.Name != "admin" {
		t.Errorf("Name = %q, want admin", got.Name)
	}

	byName, err := store.GetRoleByName("Admin") // case-insensitive
	if err != nil || byName == nil {
		t.Fatalf("GetRoleByName: %v, %v", byName, err)
	}
	if byName.ID != "admin" {
		t.Errorf("byName.ID = %q, want admin", byName.ID)
	}

	got.Description = "updated"
	if err := store.UpdateRole(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := store.GetRoleByID("admin")
	if after.Description != "updated" {
		t.Errorf("Description = %q, want updated", after.Description)
	}
	if after.Version != 2 {
		t.Errorf("Version = %d, want 2 after update", after.Version)
	}
}

func TestRoleStore_DuplicateName(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.CreateRole(&Role{ID: "editor", Name: "editor"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := store.CreateRole(&Role{Name: "EDITOR"}); err == nil {
		t.Fatal("expected error on duplicate name (case-insensitive)")
	}
}

func TestRoleStore_SeedNotDeletable(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.CreateRole(&Role{ID: "admin", Name: "admin", IsSeed: true}); err != nil {
		t.Fatalf("create seed: %v", err)
	}
	if err := store.DeleteRole("admin"); err == nil {
		t.Fatal("expected error deleting seed role")
	}
}

func TestRoleStore_ListRoles(t *testing.T) {
	store, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateRole(&Role{ID: "admin", Name: "admin", IsSeed: true})
	_, _ = store.CreateRole(&Role{ID: "editor", Name: "editor", IsSeed: true})
	_, _ = store.CreateRole(&Role{ID: "viewer", Name: "viewer", IsSeed: true})

	roles, err := store.ListRoles()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(roles) != 3 {
		t.Fatalf("got %d roles, want 3", len(roles))
	}
}
