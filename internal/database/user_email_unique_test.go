// file: internal/database/user_email_unique_test.go
// version: 1.4.0
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

// setRawUserEmailSource rewrites a user row's email and EmailSource
// directly; source "" is a row from before EmailSource existed.
func setRawUserEmailSource(t *testing.T, s *PebbleStore, u *User, email, source string) {
	t.Helper()
	u.Email, u.EmailSource = email, source
	data, _ := json.Marshal(u)
	if err := s.db.Set([]byte("u:"+u.ID), data, pebble.Sync); err != nil {
		t.Fatal(err)
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

	res, err := s.RerunVerifiedEmailBackfillForTest(nil)
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
	// Once applied, an unchanged (empty) operator list changes nothing.
	s.SetOperatorTrustedEmails(func() []string { return nil })
	if err := s.EnsureVerifiedEmailIndex(); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.GetUserByVerifiedEmail("invitee@example.com"); u == nil || u.ID != invitee.ID {
		t.Fatal("an unchanged list must not touch the index")
	}
}

// The operator's trusted list (owner email, OAUTH_ALLOWED_EMAILS) proves an
// address for its one active holder; removing an address withdraws that
// proof before the next lookup; adding it back restores it.
func TestVerifiedEmailIndex_OperatorList(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "admin1", "admin", "active")
	listed := mustUser(t, s, "listed", "viewer", "active")
	setRawUserEmailSource(t, s, listed, "listed@example.com", "") // legacy row
	selfSet := mustUser(t, s, "selfset", "viewer", "active")
	selfSet.Email = "Squatted@example.com" // typed in through self-service
	if err := s.UpdateUser(selfSet); err != nil {
		t.Fatal(err)
	}
	list := []string{"ADMIN1@admins.example.com", " listed@example.com ", "squatted@example.com"}
	s.SetOperatorTrustedEmails(func() []string { return list })
	if err := s.EnsureVerifiedEmailIndex(); err != nil { // first run: full rebuild
		t.Fatal(err)
	}
	if u, _ := s.GetUserByVerifiedEmail("admin1@admins.example.com"); u == nil || u.ID != admin.ID {
		t.Fatalf("the owner-listed admin must be verified: %+v", u)
	}
	if u, _ := s.GetUserByVerifiedEmail("listed@example.com"); u == nil || u.ID != listed.ID {
		t.Fatalf("an allow-listed legacy holder must be verified: %+v", u)
	}
	if u, _ := s.GetUserByVerifiedEmail("squatted@example.com"); u != nil {
		t.Fatalf("the list must not vouch for an address its holder typed in: %+v", u)
	}

	list = []string{"admin1@admins.example.com", "squatted@example.com"} // the operator removes listed@
	if u, err := s.GetUserByVerifiedEmail("listed@example.com"); err != nil || u != nil {
		t.Fatalf("a removed address must stop resolving at once: %+v, %v", u, err)
	}
	if _, closer, err := s.db.Get([]byte(userEmailIndexPrefix + "listed@example.com")); err == nil {
		closer.Close()
		t.Fatal("the key must be deleted, so a rollback build does not trust it")
	}
	list = append(list, "listed@example.com") // and adds it back
	if u, _ := s.GetUserByVerifiedEmail("listed@example.com"); u == nil || u.ID != listed.ID {
		t.Fatalf("a re-added address must resolve again: %+v", u)
	}

	// An inactive holder is not proven by the list.
	gone := mustUser(t, s, "gone", "viewer", "inactive")
	setRawUserEmail(t, s, gone, "gone@example.com")
	list = append(list, "gone@example.com")
	if u, _ := s.GetUserByVerifiedEmail("gone@example.com"); u != nil {
		t.Fatalf("an inactive holder must not be verified by the list: %+v", u)
	}
}

// Two holders of one operator-listed address: a conflict, so no key, in both
// the full rebuild and the re-apply after a list change; and the displacement
// gate refuses.
func TestVerifiedEmailIndex_OperatorListedDuplicateIsAConflict(t *testing.T) {
	s := newInviteTestStore(t)
	a := mustUser(t, s, "twin-a", "viewer", "active")
	b := mustUser(t, s, "twin-b", "viewer", "active")
	setRawUserEmail(t, s, a, "shared@example.com") // legacy duplicate, as a pre-fix UpdateUser allowed
	setRawUserEmail(t, s, b, "shared@example.com")
	res, err := s.RerunVerifiedEmailBackfillForTest([]string{"shared@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflict != 1 {
		t.Fatalf("result = %+v, want 1 conflict", res)
	}
	if u, _ := s.GetUserByVerifiedEmail("shared@example.com"); u != nil {
		t.Fatalf("a conflicted address must not be verified: %+v", u)
	}

	s2 := newInviteTestStore(t)
	c := mustUser(t, s2, "twin-c", "viewer", "active")
	d := mustUser(t, s2, "twin-d", "viewer", "active")
	setRawUserEmail(t, s2, c, "dup@example.com")
	setRawUserEmail(t, s2, d, "dup@example.com")
	list := []string{}
	s2.SetOperatorTrustedEmails(func() []string { return list })
	if err := s2.EnsureVerifiedEmailIndex(); err != nil {
		t.Fatal(err)
	}
	list = []string{"dup@example.com"}
	if u, err := s2.GetUserByVerifiedEmail("dup@example.com"); err != nil || u != nil {
		t.Fatalf("a newly listed address with two holders must stay unverified: %+v, %v", u, err)
	}
	if _, _, err := s2.CreateUserDisplacingUnverifiedEmail("newcomer", "dup@example.com", "oauth", "", []string{"viewer"}, "active"); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("displacing operator-listed holders = %v, want ErrUserEmailTaken", err)
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

// A holder with provenance is never displaced, even without a verified key:
// a linked identity, the operator list or an invite grant each refuse.
func TestCreateUserDisplacingUnverifiedEmail_RefusesAProvenHolder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prove func(t *testing.T, s *PebbleStore, holder *User)
	}{
		{"linked identity", func(t *testing.T, s *PebbleStore, holder *User) {
			if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "github", Subject: "gh-holder", Email: "Shared@example.com", UserID: holder.ID}); err != nil {
				t.Fatal(err)
			}
		}},
		{"operator list, holder not self-set", func(t *testing.T, s *PebbleStore, holder *User) {
			setRawUserEmailSource(t, s, holder, "shared@example.com", EmailSourceSetup)
			s.SetOperatorTrustedEmails(func() []string { return []string{"shared@example.com"} })
		}},
		{"invite grant", func(t *testing.T, s *PebbleStore, holder *User) {
			g, _ := json.Marshal(InviteEmailGrant{UserID: holder.ID, InviteToken: "tok"})
			if err := s.db.Set([]byte(inviteEmailGrantPrefix+"shared@example.com"), g, pebble.Sync); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newInviteTestStore(t)
			holder := mustUser(t, s, "holder", "viewer", "active")
			holder.Email = "shared@example.com" // self-set: no verified key
			if err := s.UpdateUser(holder); err != nil {
				t.Fatal(err)
			}
			tc.prove(t, s, holder)
			before, _ := s.CountUsers()
			if _, _, err := s.CreateUserDisplacingUnverifiedEmail("newcomer", "shared@example.com", "oauth", "", []string{"viewer"}, "active"); !errors.Is(err, ErrUserEmailTaken) {
				t.Fatalf("displacing a proven holder = %v, want ErrUserEmailTaken", err)
			}
			if got, _ := s.GetUserByID(holder.ID); got.Email != "shared@example.com" {
				t.Fatalf("a proven holder must keep the address: %q", got.Email)
			}
			if after, _ := s.CountUsers(); after != before {
				t.Fatal("a refused displacement must create nothing")
			}
		})
	}
}

// A self-set holder of an operator-listed address is not proven by the list,
// so the real owner's IdP login displaces it.
func TestCreateUserDisplacingUnverifiedEmail_SelfSetListedHolderIsDisplaced(t *testing.T) {
	s := newInviteTestStore(t)
	squatter := mustUser(t, s, "squatter", "viewer", "active")
	squatter.Email = "guest@example.com" // typed in before the real guest registered
	if err := s.UpdateUser(squatter); err != nil {
		t.Fatal(err)
	}
	s.SetOperatorTrustedEmails(func() []string { return []string{"guest@example.com"} })
	u, displaced, err := s.CreateUserDisplacingUnverifiedEmail("guest@example.com", "guest@example.com", "oauth", "", []string{"viewer"}, "active")
	if err != nil {
		t.Fatalf("a self-set listed holder must not block: %v", err)
	}
	if len(displaced) != 1 || displaced[0] != squatter.ID {
		t.Fatalf("displaced = %v", displaced)
	}
	if got, _ := s.GetUserByID(squatter.ID); got.Email != "" || got.EmailSource != "" {
		t.Fatalf("the squatter loses the address and its source: %+v", got)
	}
	if got, _ := s.GetUserByID(u.ID); got.EmailSource != EmailSourceIdP {
		t.Fatalf("the new account's source = %q, want idp", got.EmailSource)
	}
}

// The store sets EmailSource on every write path, and a caller cannot forge it.
func TestEmailSource_SetOnEveryWritePath(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "admin1", "admin", "active")
	if admin.EmailSource != EmailSourceSetup {
		t.Fatalf("CreateUser source = %q, want setup", admin.EmailSource)
	}
	// Case-only change keeps the source; a forged value is ignored.
	admin.Email = "ADMIN1@admins.example.com"
	admin.EmailSource = EmailSourceIdP
	if err := s.UpdateUser(admin); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUserByID(admin.ID); got.EmailSource != EmailSourceSetup {
		t.Fatalf("case-only change with a forged source = %q, want setup", got.EmailSource)
	}
	// A real change is self-service, whatever the caller claims.
	admin.Email = "new@example.com"
	admin.EmailSource = EmailSourceSetup
	if err := s.UpdateUser(admin); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUserByID(admin.ID); got.EmailSource != EmailSourceSelf {
		t.Fatalf("email change source = %q, want self", got.EmailSource)
	}
	// An IdP login verifying it upgrades it.
	if ok, err := s.MarkUserEmailVerified(admin.ID, "new@example.com"); err != nil || !ok {
		t.Fatalf("MarkUserEmailVerified = %v, %v", ok, err)
	}
	if got, _ := s.GetUserByID(admin.ID); got.EmailSource != EmailSourceIdP {
		t.Fatalf("after IdP verification source = %q, want idp", got.EmailSource)
	}
	// Invite binding.
	deliveredInvite(t, s, admin.ID, "tok-src", "invitee", "invitee@example.com")
	inv, err := s.ConsumeInvite("tok-src", "bcrypt", "h")
	if err != nil {
		t.Fatal(err)
	}
	if inv.EmailSource != EmailSourceInvite {
		t.Fatalf("invite source = %q, want invite", inv.EmailSource)
	}
	// Clearing the email clears the source.
	inv.Email = ""
	if err := s.UpdateUser(inv); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUserByID(inv.ID); got.EmailSource != "" {
		t.Fatalf("cleared email source = %q, want empty", got.EmailSource)
	}
}

// Re-apply after a list change follows the rebuild: another holder with proof
// of its own, or a key already naming someone else, is a conflict and nothing
// is written.
func TestVerifiedEmailIndex_ReapplyConflicts(t *testing.T) {
	s := newInviteTestStore(t)
	active := mustUser(t, s, "active", "viewer", "active")
	setRawUserEmailSource(t, s, active, "both@example.com", "")
	inactive := mustUser(t, s, "inactive", "viewer", "inactive")
	setRawUserEmail(t, s, inactive, "both@example.com")
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google", Subject: "g-inactive", Email: "both@example.com", UserID: inactive.ID}); err != nil {
		t.Fatal(err)
	}
	keyed := mustUser(t, s, "keyed", "viewer", "active")
	other := mustUser(t, s, "other", "viewer", "active")
	setRawUserEmailSource(t, s, keyed, "keyed@example.com", "")
	if err := s.db.Set([]byte(userEmailIndexPrefix+"keyed@example.com"), []byte(other.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}

	list := []string{}
	s.SetOperatorTrustedEmails(func() []string { return list })
	if _, err := s.RerunVerifiedEmailBackfillForTest(nil); err != nil {
		t.Fatal(err)
	}
	// The rebuild dropped the misdirected key and gave both@ to the inactive
	// holder (its linked identity is proof). Put the first back and remove the
	// second so the re-apply path's own rules are what get tested.
	if err := s.db.Set([]byte(userEmailIndexPrefix+"keyed@example.com"), []byte(other.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Delete([]byte(userEmailIndexPrefix+"both@example.com"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	list = []string{"both@example.com", "keyed@example.com"}
	if err := s.EnsureVerifiedEmailIndex(); err != nil {
		t.Fatal(err)
	}
	if v, closer, err := s.db.Get([]byte(userEmailIndexPrefix + "both@example.com")); err == nil {
		owner := string(v)
		closer.Close()
		t.Fatalf("an address another (inactive) holder has proof for must get no key, got one for %s", owner)
	}
	v, closer, err := s.db.Get([]byte(userEmailIndexPrefix + "keyed@example.com"))
	if err != nil {
		t.Fatal("an existing key must not be removed by a conflict")
	}
	if string(v) != other.ID {
		t.Fatalf("an existing key must not be overwritten: now %s", string(v))
	}
	closer.Close()
}

// An unreadable user row might be a holder; the displacement fails closed.
func TestCreateUserDisplacingUnverifiedEmail_RefusesWhenARowIsUnreadable(t *testing.T) {
	s := newInviteTestStore(t)
	squatter := mustUser(t, s, "squatter", "viewer", "active")
	squatter.Email = "contested@example.com"
	if err := s.UpdateUser(squatter); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte("u:01UNREADABLEROW"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateUserDisplacingUnverifiedEmail("newcomer", "contested@example.com", "oauth", "", []string{"viewer"}, "active")
	if !errors.Is(err, ErrUserEmailTaken) || !errors.Is(err, ErrUndecodableUserRows) {
		t.Fatalf("err = %v, want ErrUserEmailTaken and ErrUndecodableUserRows", err)
	}
	if got, _ := s.GetUserByID(squatter.ID); got.Email != "contested@example.com" {
		t.Fatal("nothing may be cleared when a row is unreadable")
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
