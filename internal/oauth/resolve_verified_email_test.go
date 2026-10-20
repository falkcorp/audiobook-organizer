// file: internal/oauth/resolve_verified_email_test.go
// version: 1.1.0
// guid: b7d1909a-d742-41fb-b8cf-25bdae5ca124
// last-edited: 2026-10-19

package oauth

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newResolvePebble(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("resolve")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func verifiedClaims(email, subject string) IdentityClaims {
	return IdentityClaims{Provider: ProviderGoogle, Subject: subject, Email: email, EmailVerified: true}
}

// A self-set profile email must never capture another person's SSO login.
// The verified login wins: the squatting account keeps its account but loses
// the address, a new account is created for the login, and the admin hook is
// told which accounts lost it.
func TestResolveUser_VerifiedLoginTakesBackASelfSetEmail(t *testing.T) {
	s := newResolvePebble(t)
	attacker, err := s.CreateUser("attacker", "attacker@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	attacker.Email = "victim@example.com"
	if err := s.UpdateUser(attacker); err != nil {
		t.Fatal(err)
	}
	var hookNew string
	var hookDisplaced []string
	cfg := New(Config{AllowedEmails: []string{"victim@example.com"}, DefaultRole: "viewer",
		OnEmailDisplaced: func(newID string, displaced []string, _ string) { hookNew, hookDisplaced = newID, displaced }})

	user, err := cfg.ResolveUser(s, verifiedClaims("Victim@example.com", "g-victim"))
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == attacker.ID {
		t.Fatal("the login must never resolve to the account that typed the address in")
	}
	if id, _ := s.GetOAuthIdentityByProviderSubject(ProviderGoogle, "g-victim"); id == nil || id.UserID != user.ID {
		t.Fatalf("the identity must be linked to the new account: %+v", id)
	}
	if got, _ := s.GetUserByID(attacker.ID); got == nil || got.Email != "" || got.Status != "active" {
		t.Fatalf("the squatter keeps its account and loses the address: %+v", got)
	}
	if v, _ := s.GetUserByVerifiedEmail("victim@example.com"); v == nil || v.ID != user.ID {
		t.Fatalf("the address must now be the new account's verified email: %+v", v)
	}
	if hookNew != user.ID || len(hookDisplaced) != 1 || hookDisplaced[0] != attacker.ID {
		t.Fatalf("OnEmailDisplaced(%q, %v); want the new account and the squatter", hookNew, hookDisplaced)
	}
}

// Without a store that can take the address back, an unverified holder still
// blocks linking: the login is denied, nothing is linked.
func TestResolveUser_UnverifiedHolderWithoutDisplacerIsDenied(t *testing.T) {
	store := newResolverStore()
	store.CreateUserFunc = func(string, string, string, string, []string, string) (*database.User, error) {
		return nil, database.ErrUserEmailTaken
	}
	linked := false
	store.CreateOAuthIdentityFunc = func(*database.OAuthIdentity) (*database.OAuthIdentity, error) {
		linked = true
		return nil, nil
	}
	cfg := New(Config{AllowedEmails: []string{"victim@example.com"}, DefaultRole: "viewer"})
	if _, err := cfg.ResolveUser(store, verifiedClaims("victim@example.com", "g-1")); !errors.Is(err, ErrEmailHeldUnverified) || !errors.Is(err, ErrEmailNotAllowed) {
		t.Fatalf("err = %v, want ErrEmailHeldUnverified (an ErrEmailNotAllowed)", err)
	}
	if linked {
		t.Fatal("nothing may be linked")
	}
}

// Someone who already signs in through SSO resolves by their (provider,
// subject) link, so the provenance-only rebuild, which drops their
// unprovable email key, does not lock them out.
func TestResolveUser_SSOLinkedUserStillSignsInAfterRebuild(t *testing.T) {
	s := newResolvePebble(t)
	owner, err := s.CreateUser("owner", "owner@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	// Linked long ago, when the IdP reported a different address.
	if _, err := s.CreateOAuthIdentity(&database.OAuthIdentity{Provider: ProviderGoogle, Subject: "g-owner", Email: "owner.old@example.com", UserID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RerunVerifiedEmailBackfillForTest(); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetUserByVerifiedEmail("owner@example.com"); v != nil {
		t.Fatalf("precondition: the rebuild must have dropped the unprovable key: %+v", v)
	}
	cfg := New(Config{AllowedEmails: []string{"owner@example.com"}, DefaultRole: "viewer"})
	user, err := cfg.ResolveUser(s, verifiedClaims("owner@example.com", "g-owner"))
	if err != nil || user == nil || user.ID != owner.ID {
		t.Fatalf("ResolveUser = %+v, %v; want the linked account", user, err)
	}
	if v, _ := s.GetUserByVerifiedEmail("owner@example.com"); v == nil || v.ID != owner.ID {
		t.Fatalf("the login re-verifies the matching current email: %+v", v)
	}
}

// An address bound by an invite that was emailed to it is verified, so the
// invitee's first SSO login links to their account.
func TestResolveUser_InviteBoundEmailLinks(t *testing.T) {
	s := newResolvePebble(t)
	admin, err := s.CreateUser("boss", "boss@admins.example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInvite(&database.Invite{Token: "tok-sso", Username: "guest", Email: "guest@example.com",
		RoleID: "viewer", CreatedByUserID: admin.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkInviteEmailSent("tok-sso", "guest@example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	guest, err := s.ConsumeInvite("tok-sso", "bcrypt", "h")
	if err != nil {
		t.Fatal(err)
	}
	cfg := New(Config{AllowedEmails: []string{"guest@example.com"}, DefaultRole: "viewer"})
	user, err := cfg.ResolveUser(s, verifiedClaims("guest@example.com", "g-guest"))
	if err != nil || user == nil || user.ID != guest.ID {
		t.Fatalf("ResolveUser = %+v, %v; want the invitee", user, err)
	}
	if id, _ := s.GetOAuthIdentityByProviderSubject(ProviderGoogle, "g-guest"); id == nil || id.UserID != guest.ID {
		t.Fatal("the identity must be linked to the invitee")
	}
}

// An account's email set by CreateUser (setup, bootstrap, an admin path) is
// verified, so the owner's first SSO login links to it.
func TestResolveUser_AdminCreatedEmailLinks(t *testing.T) {
	s := newResolvePebble(t)
	owner, err := s.CreateUser("owner", "owner@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	cfg := New(Config{AllowedEmails: []string{"owner@example.com"}, DefaultRole: "viewer"})
	user, err := cfg.ResolveUser(s, verifiedClaims("owner@example.com", "g-owner"))
	if err != nil || user == nil || user.ID != owner.ID {
		t.Fatalf("ResolveUser = %+v, %v; want the existing account", user, err)
	}
}

// A login through an already-linked identity whose IdP-verified email equals
// the account's current (self-set) email makes that email verified, so a
// second provider can then link by it.
func TestResolveUser_LinkedIdentityVerifiesMatchingEmail(t *testing.T) {
	s := newResolvePebble(t)
	u, err := s.CreateUser("pat", "pat@admins.example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	u.Email = "pat@example.com"
	if err := s.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOAuthIdentity(&database.OAuthIdentity{Provider: ProviderGoogle, Subject: "g-pat", Email: "pat@example.com", UserID: u.ID}); err != nil {
		t.Fatal(err)
	}
	cfg := New(Config{AllowedEmails: []string{"pat@example.com"}, DefaultRole: "viewer"})
	if got, err := cfg.ResolveUser(s, verifiedClaims("pat@example.com", "g-pat")); err != nil || got.ID != u.ID {
		t.Fatalf("linked login = %+v, %v", got, err)
	}
	if v, _ := s.GetUserByVerifiedEmail("pat@example.com"); v == nil || v.ID != u.ID {
		t.Fatalf("the IdP-verified email must now be verified: %+v", v)
	}
	gh := IdentityClaims{Provider: ProviderGitHub, Subject: "gh-pat", Email: "pat@example.com", EmailVerified: true}
	if got, err := cfg.ResolveUser(s, gh); err != nil || got.ID != u.ID {
		t.Fatalf("second provider = %+v, %v; want the same account", got, err)
	}
}
