// file: internal/database/user_email_unique_test.go
// version: 1.2.0
// guid: 76d177ea-781f-4827-8e57-febd941f7965
// last-edited: 2026-10-19

package database

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func TestUpdateUser_RefusesAnEmailAnotherUserHasAnyCase(t *testing.T) {
	s := newInviteTestStore(t)
	mustUser(t, s, "alice", "viewer", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")

	bob.Email = "ALICE@Admins.Example.com"
	if err := s.UpdateUser(bob); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("UpdateUser to another user's email (other case) = %v, want ErrUserEmailTaken", err)
	}
	stored, _ := s.GetUserByID(bob.ID)
	if stored.Email != "bob@admins.example.com" {
		t.Fatalf("a refused update must not write: email = %q", stored.Email)
	}
	if u, _ := s.GetUserByVerifiedEmail("alice@admins.example.com"); u == nil || u.Username != "alice" {
		t.Fatalf("alice's verified email must still resolve to alice: %+v", u)
	}
}

// A self-service email change never makes an address verified, and gives up
// the verification of the old one.
func TestUpdateUser_EmailChangeIsNeverVerified(t *testing.T) {
	s := newInviteTestStore(t)
	alice := mustUser(t, s, "alice", "viewer", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")

	alice.Email = "Alice.New@example.com"
	if err := s.UpdateUser(alice); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.GetUserByVerifiedEmail("alice.new@example.com"); u != nil {
		t.Fatalf("a self-set email must not be verified: %+v", u)
	}
	if u, _ := s.GetUserByVerifiedEmail("alice@admins.example.com"); u != nil {
		t.Fatalf("the address alice gave up must no longer be verified for anyone: %+v", u)
	}
	if _, closer, err := s.db.Get([]byte(userEmailIndexPrefix + "alice@admins.example.com")); err == nil {
		closer.Close()
		t.Fatal("the old verified key must be deleted, not just ignored")
	}
	// The freed address can be taken by someone else, unverified.
	bob.Email = "alice@admins.example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatalf("a freed email must be usable: %v", err)
	}
	if u, _ := s.GetUserByVerifiedEmail("alice@admins.example.com"); u != nil {
		t.Fatalf("bob's self-set copy must not be verified: %+v", u)
	}
	// A case-only change of a verified email keeps it verified.
	carol := mustUser(t, s, "carol", "viewer", "active")
	carol.Email = "CAROL@admins.example.com"
	if err := s.UpdateUser(carol); err != nil {
		t.Fatalf("case-only change of own email: %v", err)
	}
	if u, _ := s.GetUserByVerifiedEmail("carol@admins.example.com"); u == nil || u.ID != carol.ID {
		t.Fatalf("a case-only change must keep the verification: %+v", u)
	}
}

func TestCreateUser_RefusesAnEmailSetThroughUpdateUser(t *testing.T) {
	s := newInviteTestStore(t)
	bob := mustUser(t, s, "bob", "viewer", "active")
	bob.Email = "Taken@example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("newcomer", "taken@example.com", "oauth", "", []string{"viewer"}, "active"); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("CreateUser with a self-set email of another user = %v, want ErrUserEmailTaken", err)
	}
}

func TestMarkUserEmailVerified_OnlyTheCurrentEmailAndNeverAnotherUsers(t *testing.T) {
	s := newInviteTestStore(t)
	alice := mustUser(t, s, "alice", "viewer", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")
	bob.Email = "bob.self@example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.MarkUserEmailVerified(bob.ID, "someone-else@example.com"); err != nil || ok {
		t.Fatalf("marking an address that is not bob's = %v, %v", ok, err)
	}
	if ok, err := s.MarkUserEmailVerified(bob.ID, "alice@admins.example.com"); err != nil || ok {
		t.Fatalf("marking alice's verified address for bob = %v, %v", ok, err)
	}
	if ok, err := s.MarkUserEmailVerified(bob.ID, "BOB.SELF@example.com"); err != nil || !ok {
		t.Fatalf("marking bob's own current email = %v, %v", ok, err)
	}
	if u, _ := s.GetUserByVerifiedEmail("bob.self@example.com"); u == nil || u.ID != bob.ID {
		t.Fatalf("bob's email must now be verified: %+v", u)
	}
	if ok, _ := s.MarkUserEmailVerified(bob.ID, "bob.self@example.com"); ok {
		t.Fatal("a second mark must be a no-op")
	}
	if u, _ := s.GetUserByVerifiedEmail("alice@admins.example.com"); u == nil || u.ID != alice.ID {
		t.Fatal("alice's verification must be untouched")
	}
}

// setRawUserEmail rewrites a user row's email directly, the way a pre-fix
// UpdateUser did: no index change.
func setRawUserEmail(t *testing.T, s *PebbleStore, u *User, email string) {
	t.Helper()
	u.Email = email
	data, _ := json.Marshal(u)
	if err := s.db.Set([]byte("u:"+u.ID), data, pebble.Sync); err != nil {
		t.Fatal(err)
	}
}

// The open-time rebuild keeps only addresses with provenance (an invite grant
// or a linked identity recorded with that email) and drops every other key,
// including ones that merely match the user's current email.
func TestBackfillVerifiedEmailIndex(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "admin1", "admin", "active") // key from CreateUser, no provenance
	mover := mustUser(t, s, "mover", "viewer", "active")
	setRawUserEmail(t, s, mover, "mover.now@example.com") // stale key for its old address
	linked := mustUser(t, s, "linked", "viewer", "active")
	setRawUserEmail(t, s, linked, "linked@example.com")
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google", Subject: "sub-linked", Email: "Linked@example.com", UserID: linked.ID}); err != nil {
		t.Fatal(err)
	}
	deliveredInvite(t, s, admin.ID, "tok-prov", "invitee", "invitee@example.com")
	invitee, err := s.ConsumeInvite("tok-prov", "bcrypt", "h")
	if err != nil {
		t.Fatal(err)
	}
	// A squatter whose key matches its current email (as a pre-fix build
	// could leave it) but with no provenance.
	squatter := mustUser(t, s, "squatter", "viewer", "active")
	setRawUserEmail(t, s, squatter, "someone@example.com")
	if err := s.db.Set([]byte(userEmailIndexPrefix+"someone@example.com"), []byte(squatter.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}

	res, err := s.RerunVerifiedEmailBackfillForTest()
	if err != nil {
		t.Fatal(err)
	}
	// Dropped: admin1@, mover@ (stale), linked@admins (stale), squatter@admins
	// (stale), someone@ (no provenance). Kept: invitee@. Added: linked@.
	if res.Dropped != 5 || res.Kept != 1 || res.Added != 1 || res.Conflict != 0 {
		t.Fatalf("result = %+v, want 5 dropped, 1 kept, 1 added", res)
	}
	for _, addr := range []string{"admin1@admins.example.com", "someone@example.com", "mover.now@example.com"} {
		if u, _ := s.GetUserByVerifiedEmail(addr); u != nil {
			t.Fatalf("%s has no provenance and must not be verified: %+v", addr, u)
		}
	}
	if u, _ := s.GetUserByVerifiedEmail("linked@example.com"); u == nil || u.ID != linked.ID {
		t.Fatalf("an identity-proven email must be verified: %+v", u)
	}
	if u, _ := s.GetUserByVerifiedEmail("invitee@example.com"); u == nil || u.ID != invitee.ID {
		t.Fatalf("an invite-proven email must stay verified: %+v", u)
	}
	if id, _ := s.GetOAuthIdentityByProviderSubject("google", "sub-linked"); id == nil || id.UserID != linked.ID {
		t.Fatal("linked identities must be untouched")
	}
	if again, err := s.backfillVerifiedEmailIndex(); err != nil || !again.Skipped {
		t.Fatalf("second run = %+v, %v; want skipped by the sentinel", again, err)
	}
}

func TestCreateUserDisplacingUnverifiedEmail(t *testing.T) {
	s := newInviteTestStore(t)
	squatter := mustUser(t, s, "squatter", "viewer", "active")
	squatter.Email = "Owner.Real@example.com"
	if err := s.UpdateUser(squatter); err != nil {
		t.Fatal(err)
	}
	u, displaced, err := s.CreateUserDisplacingUnverifiedEmail("owner.real@example.com", "owner.real@example.com", "oauth", "", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(displaced) != 1 || displaced[0] != squatter.ID {
		t.Fatalf("displaced = %v, want the squatter", displaced)
	}
	if got, _ := s.GetUserByID(squatter.ID); got == nil || got.Email != "" || got.Status != "active" || got.PasswordHash != "h" {
		t.Fatalf("the squatter keeps its account and loses only the address: %+v", got)
	}
	if v, _ := s.GetUserByVerifiedEmail("owner.real@example.com"); v == nil || v.ID != u.ID {
		t.Fatalf("the new account must hold the address verified: %+v", v)
	}

	// A verified holder is never displaced.
	before, _ := s.CountUsers()
	if _, _, err := s.CreateUserDisplacingUnverifiedEmail("again", "OWNER.REAL@example.com", "oauth", "", []string{"viewer"}, "active"); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("displacing a verified holder = %v, want ErrUserEmailTaken", err)
	}
	if after, _ := s.CountUsers(); after != before {
		t.Fatal("a refused displacement must write nothing")
	}
	if v, _ := s.GetUserByVerifiedEmail("owner.real@example.com"); v == nil || v.ID != u.ID {
		t.Fatal("the verified holder must keep the address")
	}
}

// Rows written by UpdateUser before it maintained the index are not indexed
// under their current email; the uniqueness check must still see them.
func TestUpdateUser_SeesAnUnindexedEmail(t *testing.T) {
	s := newInviteTestStore(t)
	alice := mustUser(t, s, "alice", "viewer", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")
	alice.Email = "legacy@example.com"
	data, _ := json.Marshal(alice)
	if err := s.db.Set([]byte("u:"+alice.ID), data, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	bob.Email = "Legacy@Example.com"
	if err := s.UpdateUser(bob); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("UpdateUser to an unindexed email = %v, want ErrUserEmailTaken", err)
	}
}

func TestUpdateUser_RacingClaimsOfOneEmailLetOneWin(t *testing.T) {
	s := newInviteTestStore(t)
	const n = 8
	users := make([]*User, n)
	for i := range users {
		users[i] = mustUser(t, s, "racer"+string(rune('a'+i)), "viewer", "active")
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u.Email = "contested@example.com"
			errs[i] = s.UpdateUser(u)
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrUserEmailTaken):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d updates claimed the same email, want exactly 1", won)
	}
}

// An address set through the profile is now indexed, so an emailed invite for
// it can no longer be accepted into a second account.
func TestConsumeInvite_RefusesAnEmailSetThroughUpdateUser(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "admin1", "admin", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")
	deliveredInvite(t, s, admin.ID, "tok-profile-email", "guest", "guest@example.com")
	bob.Email = "Guest@example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeInvite("tok-profile-email", "bcrypt", "h"); !errors.Is(err, ErrInviteEmailTaken) {
		t.Fatalf("ConsumeInvite = %v, want ErrInviteEmailTaken", err)
	}
}
