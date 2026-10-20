// file: internal/oauth/resolve_verified_email_test.go
// version: 2.1.0
// guid: b7d1909a-d742-41fb-b8cf-25bdae5ca124
// last-edited: 2026-10-20

package oauth

import (
	"encoding/json"
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

// Without a store that can take the address back, CreateUser decides: its
// ErrUserEmailTaken now means another account holds the address proven, so
// the login is denied as a conflict and nothing is linked.
func TestResolveUser_ProvenHolderWithoutDisplacerIsDenied(t *testing.T) {
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
	if _, err := cfg.ResolveUser(store, verifiedClaims("victim@example.com", "g-1")); !errors.Is(err, ErrEmailConflict) || !errors.Is(err, ErrEmailNotAllowed) {
		t.Fatalf("err = %v, want ErrEmailConflict (an ErrEmailNotAllowed)", err)
	}
	if linked {
		t.Fatal("nothing may be linked")
	}
}

// Someone who already signs in through SSO resolves by their (provider,
// subject) link, so an unverified current email (here typed into the
// profile) does not lock them out; the login then verifies it.
func TestResolveUser_SSOLinkedUserWithUnverifiedEmailStillSignsIn(t *testing.T) {
	s := newResolvePebble(t)
	owner, err := s.CreateUser("owner", "owner.old@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	// Linked long ago, when the IdP reported the old address.
	if _, err := s.CreateOAuthIdentity(&database.OAuthIdentity{Provider: ProviderGoogle, Subject: "g-owner", Email: "owner.old@example.com", UserID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	owner.Email = "owner@example.com"
	if err := s.UpdateUser(owner); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetUserByVerifiedEmail("owner@example.com"); v != nil {
		t.Fatalf("precondition: a profile-typed address is unverified: %+v", v)
	}
	cfg := New(Config{AllowedEmails: []string{"owner@example.com"}, DefaultRole: "viewer"})
	user, err := cfg.ResolveUser(s, verifiedClaims("owner@example.com", "g-owner"))
	if err != nil || user == nil || user.ID != owner.ID {
		t.Fatalf("ResolveUser = %+v, %v; want the linked account", user, err)
	}
	if v, _ := s.GetUserByVerifiedEmail("owner@example.com"); v == nil || v.ID != owner.ID || v.EmailSource != database.EmailSourceIdP {
		t.Fatalf("the login verifies the matching current email: %+v", v)
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

// An owner-email admin created at setup, never linked to SSO, has that first
// SSO login linked to the admin account, not displaced from it: CreateUser
// records the address as set up (verified). A row an older build wrote is
// stamped the same way at store open, from the key CreateUser wrote.
func TestResolveUser_OwnerEmailAdminFirstSSOLinksAndIsNotDisplaced(t *testing.T) {
	s := newResolvePebble(t)
	admin, err := s.CreateUser("admin", "owner@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileVerifiedEmails(); err != nil {
		t.Fatal(err)
	}
	before, _ := s.CountUsers()
	displacedCalled := false
	cfg := New(Config{AllowedEmails: []string{"owner@example.com"}, DefaultRole: "viewer",
		OnEmailDisplaced: func(string, []string, string) { displacedCalled = true }})
	user, err := cfg.ResolveUser(s, verifiedClaims("owner@example.com", "cf-owner"))
	if err != nil || user == nil || user.ID != admin.ID {
		t.Fatalf("ResolveUser = %+v, %v; want the admin account", user, err)
	}
	if got, _ := s.GetUserByID(admin.ID); got.Email != "owner@example.com" {
		t.Fatalf("the admin must keep its email: %q", got.Email)
	}
	if after, _ := s.CountUsers(); after != before || displacedCalled {
		t.Fatalf("no account may be created or displaced (users %d -> %d, displaced=%v)", before, after, displacedCalled)
	}
}

// B1: being on OAUTH_ALLOWED_EMAILS vouches for no one. Someone who typed a
// listed guest's address into their own profile before the guest ever signed
// in is displaced by the guest's real IdP login, which gets its own account.
func TestResolveUser_SelfSetListedAddressIsDisplacedByTheRealIdPLogin(t *testing.T) {
	s := newResolvePebble(t)
	squatter, err := s.CreateUser("squatter", "squatter@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	squatter.Email = "guest@example.com"
	if err := s.UpdateUser(squatter); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetUserByVerifiedEmail("guest@example.com"); v != nil {
		t.Fatalf("a self-set listed address must not be verified: %+v", v)
	}
	var displaced []string
	cfg := New(Config{AllowedEmails: []string{"guest@example.com"}, DefaultRole: "viewer",
		OnEmailDisplaced: func(_ string, ids []string, _ string) { displaced = ids }})
	user, err := cfg.ResolveUser(s, verifiedClaims("guest@example.com", "g-guest"))
	if err != nil || user == nil || user.ID == squatter.ID {
		t.Fatalf("ResolveUser = %+v, %v; want a new account, never the squatter", user, err)
	}
	if len(displaced) != 1 || displaced[0] != squatter.ID {
		t.Fatalf("displaced = %v, want the squatter", displaced)
	}
	if got, _ := s.GetUserByID(squatter.ID); got.Email != "" {
		t.Fatalf("the squatter must lose the address: %q", got.Email)
	}
	if v, _ := s.GetUserByVerifiedEmail("guest@example.com"); v == nil || v.ID != user.ID || v.EmailSource != database.EmailSourceIdP {
		t.Fatalf("the guest's account must hold it verified, source idp: %+v", v)
	}
}

// writeLegacyUserRow rewrites u's row the way a build from before
// email_source existed left it: the given email, no EmailSource, and no
// change to any index.
func writeLegacyUserRow(t *testing.T, s *database.PebbleStore, u *database.User, email string) {
	t.Helper()
	u.Email, u.EmailSource = email, ""
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRaw("u:"+u.ID, data); err != nil {
		t.Fatal(err)
	}
}

// NIT8: a legacy row that typed a guest's address into its profile on an
// older build has no EmailSource. Reconcile (run at every store open) stamps
// it "self", and the guest's first verified SSO login then takes the address
// back instead of linking to the squatter. A legacy row whose address was set
// when the account was created (the key CreateUser wrote) is stamped "setup"
// and is linked to.
func TestResolveUser_LegacySquatterRowAfterReconcile(t *testing.T) {
	s := newResolvePebble(t)
	squatter, err := s.CreateUser("squatter", "", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyUserRow(t, s, squatter, "guest@example.com")
	creator, err := s.CreateUser("creator", "creator@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyUserRow(t, s, creator, "creator@example.com")

	res, err := s.ReconcileVerifiedEmails()
	if err != nil {
		t.Fatal(err)
	}
	if res.Stamped[database.EmailSourceSelf] != 1 || res.Stamped[database.EmailSourceSetup] != 1 {
		t.Fatalf("stamped %v, want one self and one setup", res.Stamped)
	}

	var hookDisplaced []string
	cfg := New(Config{
		AllowedEmails: []string{"guest@example.com", "creator@example.com"}, DefaultRole: "viewer",
		OnEmailDisplaced: func(_ string, displaced []string, _ string) { hookDisplaced = displaced },
	})
	user, err := cfg.ResolveUser(s, verifiedClaims("Guest@Example.com", "g-guest"))
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == squatter.ID {
		t.Fatal("the guest's SSO login was linked to the legacy squatter")
	}
	if len(hookDisplaced) != 1 || hookDisplaced[0] != squatter.ID {
		t.Fatalf("displaced %v, want the squatter", hookDisplaced)
	}
	if got, _ := s.GetUserByID(squatter.ID); got.Email != "" {
		t.Fatalf("the squatter keeps its account and loses the address: %q", got.Email)
	}

	linked, err := cfg.ResolveUser(s, verifiedClaims("creator@example.com", "g-creator"))
	if err != nil {
		t.Fatal(err)
	}
	if linked.ID != creator.ID {
		t.Fatalf("the setup-stamped legacy account must be linked; got %s", linked.ID)
	}
}

// NIT4: step 4 decides and links in one locked store call. An account that
// gave its verified address up is not linked to, and nothing is written when
// no account holds the address proven.
func TestLinkIdentityToVerifiedEmailHolder(t *testing.T) {
	s := newResolvePebble(t)
	holder, err := s.CreateUser("holder", "held@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	id := &database.OAuthIdentity{Provider: ProviderGoogle, Subject: "g-1", Email: "held@example.com"}
	got, err := s.LinkIdentityToVerifiedEmailHolder(id, "HELD@example.com")
	if err != nil || got == nil || got.ID != holder.ID {
		t.Fatalf("link = %+v, %v; want the holder", got, err)
	}
	if linked, _ := s.GetOAuthIdentityByProviderSubject(ProviderGoogle, "g-1"); linked == nil || linked.UserID != holder.ID {
		t.Fatalf("identity = %+v; want it linked to the holder", linked)
	}

	holder.Email = "moved@example.com"
	if err := s.UpdateUser(holder); err != nil {
		t.Fatal(err)
	}
	id2 := &database.OAuthIdentity{Provider: ProviderGoogle, Subject: "g-2", Email: "held@example.com"}
	if got, err := s.LinkIdentityToVerifiedEmailHolder(id2, "held@example.com"); err != nil || got != nil {
		t.Fatalf("link after the holder moved = %+v, %v; want nil", got, err)
	}
	if linked, _ := s.GetOAuthIdentityByProviderSubject(ProviderGoogle, "g-2"); linked != nil {
		t.Fatalf("nothing may be written without a proven holder: %+v", linked)
	}
}
