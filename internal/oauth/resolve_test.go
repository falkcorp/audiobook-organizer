// file: internal/oauth/resolve_test.go
// version: 1.1.0
// guid: 4c9e1b73-6a20-4d58-8f16-2b5a7c0e9d38

package oauth

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newResolverStore returns a MockStore wired so nothing exists yet; individual tests
// override the Func fields they care about.
func newResolverStore() *database.MockStore {
	return &database.MockStore{
		GetOAuthIdentityByProviderSubjectFunc: func(string, string) (*database.OAuthIdentity, error) { return nil, nil },
		GetUserByEmailFunc:                    func(string) (*database.User, error) { return nil, nil },
		GetUserByUsernameFunc:                 func(string) (*database.User, error) { return nil, nil },
		GetUserByIDFunc:                       func(string) (*database.User, error) { return nil, nil },
	}
}

// THE security-critical test: a validly-authenticated but non-allowlisted email is
// rejected and NEVER creates a user or an identity. Verified ≠ authorized.
func TestResolveUser_RejectsNonAllowlisted_NoWrites(t *testing.T) {
	created := false
	store := newResolverStore()
	store.CreateUserFunc = func(string, string, string, string, []string, string) (*database.User, error) {
		created = true
		return &database.User{ID: "u1"}, nil
	}
	store.CreateOAuthIdentityFunc = func(*database.OAuthIdentity) (*database.OAuthIdentity, error) {
		created = true
		return nil, nil
	}
	cfg := New(Config{AllowedEmails: []string{"owner@example.com"}, DefaultRole: "viewer"})

	_, err := cfg.ResolveUser(store, IdentityClaims{
		Provider: ProviderGoogle, Subject: "g-999", Email: "stranger@example.com", EmailVerified: true,
	})
	if !errors.Is(err, ErrEmailNotAllowed) {
		t.Fatalf("want ErrEmailNotAllowed, got %v", err)
	}
	if created {
		t.Fatal("a non-allowlisted login must NOT create a user or identity")
	}
}

// An unverified email is rejected even if it is on the allowlist.
func TestResolveUser_RejectsUnverified(t *testing.T) {
	cfg := New(Config{AllowedEmails: []string{"owner@example.com"}})
	_, err := cfg.ResolveUser(newResolverStore(), IdentityClaims{
		Provider: ProviderGitHub, Subject: "gh-1", Email: "owner@example.com", EmailVerified: false,
	})
	if !errors.Is(err, ErrEmailNotVerified) {
		t.Fatalf("want ErrEmailNotVerified, got %v", err)
	}
}

// An already-linked identity resolves to its user (no new user/identity).
func TestResolveUser_ExistingIdentity(t *testing.T) {
	store := newResolverStore()
	store.GetOAuthIdentityByProviderSubjectFunc = func(p, s string) (*database.OAuthIdentity, error) {
		return &database.OAuthIdentity{ID: "oi1", Provider: p, Subject: s, UserID: "u42"}, nil
	}
	store.GetUserByIDFunc = func(id string) (*database.User, error) {
		return &database.User{ID: id, Email: "owner@example.com"}, nil
	}
	store.CreateUserFunc = func(string, string, string, string, []string, string) (*database.User, error) {
		t.Fatal("must not create a user for an already-linked identity")
		return nil, nil
	}
	cfg := New(Config{AllowedEmails: []string{"owner@example.com"}})
	u, err := cfg.ResolveUser(store, IdentityClaims{
		Provider: ProviderGoogle, Subject: "g-1", Email: "owner@example.com", EmailVerified: true,
	})
	if err != nil || u.ID != "u42" {
		t.Fatalf("want user u42, got %v err=%v", u, err)
	}
}

// An allowlisted email matching an existing local user auto-links the new identity.
func TestResolveUser_LinksToExistingUserByEmail(t *testing.T) {
	linked := false
	store := newResolverStore()
	store.GetUserByEmailFunc = func(string) (*database.User, error) {
		return &database.User{ID: "u7", Email: "owner@example.com"}, nil
	}
	store.CreateOAuthIdentityFunc = func(oi *database.OAuthIdentity) (*database.OAuthIdentity, error) {
		linked = true
		if oi.UserID != "u7" {
			t.Errorf("linked to wrong user %s", oi.UserID)
		}
		return oi, nil
	}
	store.CreateUserFunc = func(string, string, string, string, []string, string) (*database.User, error) {
		t.Fatal("must link to existing user, not create a new one")
		return nil, nil
	}
	cfg := New(Config{AllowedEmails: []string{"owner@example.com"}})
	u, err := cfg.ResolveUser(store, IdentityClaims{
		Provider: ProviderGitHub, Subject: "gh-9", Email: "owner@example.com", EmailVerified: true,
	})
	if err != nil || u.ID != "u7" || !linked {
		t.Fatalf("want linked user u7, got %v err=%v linked=%v", u, err, linked)
	}
}

// A brand-new allowlisted email auto-creates a user (default role) + links the identity.
func TestResolveUser_AutoCreatesUser(t *testing.T) {
	var gotRoles []string
	var gotAlgo string
	store := newResolverStore()
	store.CreateUserFunc = func(username, email, algo, hash string, roles []string, status string) (*database.User, error) {
		gotRoles, gotAlgo = roles, algo
		if status != "active" {
			t.Errorf("new user status=%q, want active", status)
		}
		return &database.User{ID: "unew", Username: username, Email: email}, nil
	}
	linked := false
	store.CreateOAuthIdentityFunc = func(oi *database.OAuthIdentity) (*database.OAuthIdentity, error) {
		linked = true
		return oi, nil
	}
	cfg := New(Config{AllowedEmails: []string{"new@example.com"}, DefaultRole: "editor"})
	u, err := cfg.ResolveUser(store, IdentityClaims{
		Provider: ProviderGoogle, Subject: "g-new", Email: "new@example.com", EmailVerified: true,
	})
	if err != nil || u.ID != "unew" || !linked {
		t.Fatalf("want new user, got %v err=%v linked=%v", u, err, linked)
	}
	if len(gotRoles) != 1 || gotRoles[0] != "editor" {
		t.Errorf("new user roles=%v, want [editor] (default role)", gotRoles)
	}
	if gotAlgo != "oauth" {
		t.Errorf("new oauth user PasswordHashAlgo=%q, want oauth", gotAlgo)
	}
}

// An auto-created user is named by its FULL verified email, not the local part.
//
// Regression guard: the previous implementation cut the address at '@' and named this
// user "new", which collides across domains and matches nothing the owner ever typed.
// Reverting uniqueUsername to the local-part form fails this test on the '@' check.
func TestResolveUser_UsernameIsFullEmail(t *testing.T) {
	var gotUsername string
	store := newResolverStore()
	store.CreateUserFunc = func(username, email, algo, hash string, roles []string, status string) (*database.User, error) {
		gotUsername = username
		return &database.User{ID: "unew", Username: username, Email: email}, nil
	}
	cfg := New(Config{AllowedEmails: []string{"Owner.Name+abs@Example.com"}, DefaultRole: "admin"})
	if _, err := cfg.ResolveUser(store, IdentityClaims{
		Provider: ProviderCFAccess, Subject: "cf-1",
		Email: "Owner.Name+abs@Example.com", EmailVerified: true,
	}); err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	// Lower-cased, but otherwise the address intact — '@' and '+' must survive.
	if want := "owner.name+abs@example.com"; gotUsername != want {
		t.Errorf("username=%q, want %q", gotUsername, want)
	}
}

// A username collision that the email lookup did NOT catch (a hand-made local account
// with an empty email field holding the address as its username) must not silently bind
// the federated identity to that account — it gets a suffixed name instead.
func TestResolveUser_UsernameCollisionSuffixes(t *testing.T) {
	var gotUsername string
	store := newResolverStore()
	store.GetUserByUsernameFunc = func(u string) (*database.User, error) {
		if u == "taken@example.com" {
			return &database.User{ID: "pre-existing", Username: u}, nil
		}
		return nil, nil
	}
	store.CreateUserFunc = func(username, email, algo, hash string, roles []string, status string) (*database.User, error) {
		gotUsername = username
		return &database.User{ID: "unew", Username: username, Email: email}, nil
	}
	cfg := New(Config{AllowedEmails: []string{"taken@example.com"}, DefaultRole: "viewer"})
	if _, err := cfg.ResolveUser(store, IdentityClaims{
		Provider: ProviderGoogle, Subject: "g-2", Email: "taken@example.com", EmailVerified: true,
	}); err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if want := "taken@example.com1"; gotUsername != want {
		t.Errorf("colliding username=%q, want %q", gotUsername, want)
	}
}
