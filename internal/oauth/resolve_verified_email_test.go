// file: internal/oauth/resolve_verified_email_test.go
// version: 1.0.0
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

// A self-set profile email must never capture another person's SSO login:
// the login is denied, nothing is linked and no second account is made.
func TestResolveUser_SelfSetEmailIsNotLinked(t *testing.T) {
	s := newResolvePebble(t)
	attacker, err := s.CreateUser("attacker", "attacker@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	attacker.Email = "victim@example.com"
	if err := s.UpdateUser(attacker); err != nil {
		t.Fatal(err)
	}
	before, _ := s.CountUsers()
	cfg := New(Config{AllowedEmails: []string{"victim@example.com"}, DefaultRole: "viewer"})

	user, err := cfg.ResolveUser(s, verifiedClaims("Victim@example.com", "g-victim"))
	if !errors.Is(err, ErrEmailHeldUnverified) || !errors.Is(err, ErrEmailNotAllowed) {
		t.Fatalf("ResolveUser = %+v, %v; want ErrEmailHeldUnverified (an ErrEmailNotAllowed)", user, err)
	}
	if id, _ := s.GetOAuthIdentityByProviderSubject(ProviderGoogle, "g-victim"); id != nil {
		t.Fatalf("the identity must not be linked: %+v", id)
	}
	if after, _ := s.CountUsers(); after != before {
		t.Fatalf("users %d -> %d; no account may be created", before, after)
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
