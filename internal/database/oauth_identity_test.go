// file: internal/database/oauth_identity_test.go
// version: 1.0.0
// guid: 9f2c7b04-5a18-4d63-8e17-3b6a0c9e2d51

package database

import "testing"

func newOAuthTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOAuthIdentity_CreateAndLookup(t *testing.T) {
	s := newOAuthTestStore(t)

	created, err := s.CreateOAuthIdentity(&OAuthIdentity{
		Provider: "google", Subject: "g-123", Email: "owner@example.com", UserID: "u1",
	})
	if err != nil {
		t.Fatalf("CreateOAuthIdentity: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected an assigned ID")
	}

	got, err := s.GetOAuthIdentityByProviderSubject("google", "g-123")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil || got.UserID != "u1" || got.Email != "owner@example.com" {
		t.Fatalf("lookup returned %+v", got)
	}

	// A different provider with the same subject must not collide.
	if other, _ := s.GetOAuthIdentityByProviderSubject("github", "g-123"); other != nil {
		t.Fatal("cross-provider subject collision")
	}

	// Unknown subject → (nil, nil).
	if miss, err := s.GetOAuthIdentityByProviderSubject("google", "nope"); err != nil || miss != nil {
		t.Fatalf("unknown lookup = %+v, %v; want nil,nil", miss, err)
	}
}

func TestOAuthIdentity_ListForUser(t *testing.T) {
	s := newOAuthTestStore(t)
	_, _ = s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google", Subject: "g1", UserID: "u5"})
	_, _ = s.CreateOAuthIdentity(&OAuthIdentity{Provider: "github", Subject: "h1", UserID: "u5"})
	_, _ = s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google", Subject: "g2", UserID: "other"})

	list, err := s.GetOAuthIdentitiesForUser("u5")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 identities for u5, got %d", len(list))
	}
}

func TestOAuthIdentity_RequiresFields(t *testing.T) {
	s := newOAuthTestStore(t)
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google"}); err == nil {
		t.Error("missing subject/user_id must error")
	}
}
