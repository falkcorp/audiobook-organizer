// file: internal/database/apikey_invite_test.go
// version: 1.2.0
// guid: 5d1e8a2f-4c3b-4f70-a9d6-2e7f0c1b9a48
// last-edited: 2026-07-03

package database

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAPIKey_Lifecycle(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	key, err := store.CreateAPIKey(&APIKey{UserID: "u1", Name: "deploy-script"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if key.ID == "" {
		t.Fatal("ID should be auto-assigned")
	}

	got, err := store.GetAPIKey(key.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v / %v", got, err)
	}
	if got.Name != "deploy-script" {
		t.Errorf("Name = %q", got.Name)
	}

	// List by user.
	list, err := store.ListAPIKeysForUser("u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("list returned %d, want 1", len(list))
	}

	// Revoke sets RevokedAt.
	if err := store.RevokeAPIKey(key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, _ = store.GetAPIKey(key.ID)
	if got.RevokedAt == nil {
		t.Error("RevokedAt should be non-nil after RevokeAPIKey")
	}

	// Last-used touch with IP.
	touched := time.Now()
	if err := store.TouchAPIKeyLastUsed(key.ID, touched, "127.0.0.1"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, _ = store.GetAPIKey(key.ID)
	if got.LastUsedAt == nil || got.LastUsedAt.Unix() != touched.Unix() {
		t.Errorf("LastUsedAt not updated")
	}
	if got.LastUsedIP != "127.0.0.1" {
		t.Errorf("LastUsedIP = %q, want 127.0.0.1", got.LastUsedIP)
	}
	if got.UseCount != 1 {
		t.Errorf("UseCount = %d, want 1", got.UseCount)
	}
}

// SetAPIKeyExpiry round-trips through PebbleStore (SEC-1/PROC-6 rotation
// grace window): it updates only ExpiresAt, leaving Status untouched so the
// key keeps working until the new expiry via the existing middleware check.
func TestAPIKey_SetAPIKeyExpiry_RoundTrips(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	key, err := store.CreateAPIKey(&APIKey{UserID: "u1", Name: "rotated-key", Status: "active"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if key.ExpiresAt != nil {
		t.Fatalf("expected nil ExpiresAt on creation, got %v", key.ExpiresAt)
	}

	grace := time.Now().Add(1 * time.Hour)
	if err := store.SetAPIKeyExpiry(key.ID, grace); err != nil {
		t.Fatalf("SetAPIKeyExpiry: %v", err)
	}

	got, err := store.GetAPIKey(key.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v / %v", got, err)
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt should be non-nil after SetAPIKeyExpiry")
	}
	if got.ExpiresAt.Unix() != grace.Unix() {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, grace)
	}
	// Status must be untouched — SetAPIKeyExpiry is not a revoke.
	if got.Status != "active" {
		t.Errorf("Status = %q, want %q (SetAPIKeyExpiry must not revoke)", got.Status, "active")
	}

	// Unknown ID is a silent no-op (mirrors SetAPIKeyStatus's nil-key contract).
	if err := store.SetAPIKeyExpiry("does-not-exist", grace); err != nil {
		t.Errorf("SetAPIKeyExpiry on unknown id: %v, want nil error", err)
	}
}

func TestInvite_CreateAndConsume(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Seed role so invite's RoleID reference is valid-looking.
	_, _ = store.CreateRole(&Role{ID: "viewer", Name: "viewer", IsSeed: true})

	inv, err := store.CreateInvite(&Invite{
		Token: "tok123", Username: "bob", RoleID: "viewer",
		CreatedByUserID: "admin-1",
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if inv.ExpiresAt.Before(time.Now()) {
		t.Error("invite expires_at should default to future (7d)")
	}

	// Duplicate username → error.
	if _, err := store.CreateInvite(&Invite{Token: "tok456", Username: "BOB", RoleID: "viewer"}); err == nil {
		t.Error("expected duplicate-username error")
	}

	// ListActiveInvites returns the single valid one.
	active, err := store.ListActiveInvites()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("active = %d, want 1", len(active))
	}

	// Consume creates the user + marks invite used + drops pending index.
	user, err := store.ConsumeInvite("tok123", "bcrypt", "hash123")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if user == nil || user.Username != "bob" {
		t.Fatalf("user not created: %+v", user)
	}
	if len(user.Roles) != 1 || user.Roles[0] != "viewer" {
		t.Errorf("roles = %v, want [viewer]", user.Roles)
	}

	// Consuming again → error (already used).
	if _, err := store.ConsumeInvite("tok123", "bcrypt", "hash123"); err == nil {
		t.Error("expected already-used error on second consume")
	}

	// After consumption, the pending-username index should be gone,
	// so a new invite for the same username should succeed.
	if _, err := store.CreateInvite(&Invite{Token: "tok789", Username: "bob", RoleID: "viewer"}); err != nil {
		// But the USER itself exists now, so ConsumeInvite for this new
		// token should fail on username-taken; create step itself is
		// allowed to set up the invite.
		t.Fatalf("unexpected error creating new invite after consume: %v", err)
	}
}

func TestInvite_ExpiresRejected(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	past := time.Now().Add(-1 * time.Hour)
	if _, err := store.CreateInvite(&Invite{
		Token: "old", Username: "carol", RoleID: "viewer",
		CreatedAt: past.Add(-10 * time.Minute), ExpiresAt: past,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.ConsumeInvite("old", "bcrypt", "hash"); err == nil {
		t.Error("expected expired error")
	}
}
