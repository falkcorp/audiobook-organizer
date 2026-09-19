// file: internal/auth/seed_test.go
// version: 1.1.0
// guid: 8c4d1e2f-5b3a-4f60-b9c7-2d8e0f1b9a56
// last-edited: 2026-09-19

package auth

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestSeedRoles_FirstRun(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	created, updated, err := SeedRoles(store)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if created != 4 {
		t.Errorf("created = %d, want 4 (admin, editor, viewer, fp-worker)", created)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0", updated)
	}

	// Verify each role's permission set.
	admin, _ := store.GetRoleByID(SeedRoleAdmin)
	if admin == nil {
		t.Fatal("admin role not created")
	}
	if len(admin.Permissions) != len(All()) {
		t.Errorf("admin has %d perms, want %d (All)", len(admin.Permissions), len(All()))
	}
	if !admin.IsSeed {
		t.Error("admin should have IsSeed=true")
	}

	viewer, _ := store.GetRoleByID(SeedRoleViewer)
	if viewer == nil {
		t.Fatal("viewer role not created")
	}
	hasView := false
	hasEdit := false
	for _, p := range viewer.Permissions {
		if p == PermLibraryView {
			hasView = true
		}
		if p == PermLibraryEditMetadata {
			hasEdit = true
		}
	}
	if !hasView {
		t.Error("viewer should include library.view")
	}
	if hasEdit {
		t.Error("viewer should NOT include library.edit_metadata")
	}
}

func TestSeedRoles_Idempotent(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, _, err := SeedRoles(store); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	created, updated, err := SeedRoles(store)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if created != 0 {
		t.Errorf("second seed created = %d, want 0 (idempotent)", created)
	}
	if updated != 0 {
		t.Errorf("second seed updated = %d, want 0 (permissions unchanged)", updated)
	}
}

func TestSeedRoles_UpgradeAddsNewPermission(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Simulate an admin from a prior release: missing one permission.
	_, err = store.CreateRole(&database.Role{
		ID:          SeedRoleAdmin,
		Name:        "admin",
		Description: "(old)",
		Permissions: []string{PermLibraryView}, // only one — stale
		IsSeed:      true,
	})
	if err != nil {
		t.Fatalf("create stale admin: %v", err)
	}

	created, updated, err := SeedRoles(store)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if created != 3 {
		t.Errorf("created = %d, want 3 (editor + viewer + fp-worker)", created)
	}
	if updated != 1 {
		t.Errorf("updated = %d, want 1 (stale admin refreshed)", updated)
	}

	admin, _ := store.GetRoleByID(SeedRoleAdmin)
	if len(admin.Permissions) != len(All()) {
		t.Errorf("admin still has %d perms after re-seed, want %d", len(admin.Permissions), len(All()))
	}
}

// TestSeedRoles_FPWorkerHoldsOnlyTheWorkerPermission: the service user for a
// remote fingerprint worker need not be an admin.
func TestSeedRoles_FPWorkerHoldsOnlyTheWorkerPermission(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := SeedRoles(store); err != nil {
		t.Fatal(err)
	}
	r, err := store.GetRoleByID(SeedRoleFPWorker)
	if err != nil || r == nil {
		t.Fatalf("fp-worker role: %v, %v", r, err)
	}
	if len(r.Permissions) != 1 || r.Permissions[0] != PermFingerprintWorker {
		t.Fatalf("fp-worker permissions = %v, want only %s", r.Permissions, PermFingerprintWorker)
	}
}
