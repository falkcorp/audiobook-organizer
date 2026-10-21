// file: internal/database/user_email_unique_test.go
// version: 2.7.0
// guid: 76d177ea-781f-4827-8e57-febd941f7965
// last-edited: 2026-10-20

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// A self-service change onto an address another account holds proven is
// accepted (refusing it would be an oracle for which addresses have verified
// accounts), stamped "self", and proves nothing: the verified lookup and an
// SSO link by email still reach the proven holder.
func TestUpdateUser_AcceptsAProvenHoldersAddressUnproven(t *testing.T) {
	s := newInviteTestStore(t)
	alice := mustUser(t, s, "alice", "viewer", "active") // setup: proven
	bob := mustUser(t, s, "bob", "viewer", "active")

	bob.Email = "ALICE@Admins.Example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatalf("UpdateUser onto another account's proven address = %v, want nil", err)
	}
	if got, _ := s.GetUserByID(bob.ID); got.EmailSource != EmailSourceSelf {
		t.Fatalf("bob's copy has source %q, want self", got.EmailSource)
	}
	if u, _ := s.GetUserByVerifiedEmail("alice@admins.example.com"); u == nil || u.ID != alice.ID {
		t.Fatalf("the verified lookup = %+v; want alice", u)
	}
	if keyFor(t, s, "alice@admins.example.com") != alice.ID {
		t.Fatal("bob's copy must not move alice's key")
	}
	id := &OAuthIdentity{Provider: "google", Subject: "g-alice", Email: "alice@admins.example.com"}
	if u, err := s.LinkIdentityToVerifiedEmailHolder(id, "alice@admins.example.com"); err != nil || u == nil || u.ID != alice.ID {
		t.Fatalf("SSO link by email = %+v, %v; want alice", u, err)
	}
}

// A self-set address is never proven, whatever older records the account
// has: a linked identity recorded with the address, or an invite grant for
// it, says the account controlled it once, not that it does now.
func TestSelfSetAddressIsNeverProvenByOlderRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record func(t *testing.T, s *PebbleStore, x *User)
	}{
		{"linked identity", func(t *testing.T, s *PebbleStore, x *User) {
			if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "github", Subject: "gh-x", Email: "Addr@example.com", UserID: x.ID}); err != nil {
				t.Fatal(err)
			}
		}},
		{"invite grant", func(t *testing.T, s *PebbleStore, x *User) {
			g, _ := json.Marshal(InviteEmailGrant{UserID: x.ID, InviteToken: "tok"})
			if err := s.db.Set([]byte(inviteEmailGrantPrefix+"addr@example.com"), g, pebble.Sync); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newInviteTestStore(t)
			x := mustUser(t, s, "x", "viewer", "active")
			tc.record(t, s, x)
			x.Email = "addr@example.com"
			if err := s.UpdateUser(x); err != nil {
				t.Fatal(err)
			}
			if u, _ := s.GetUserByVerifiedEmail("addr@example.com"); u != nil {
				t.Fatalf("a self-set address counted as proven: %+v", u)
			}
			// So another account can still be given the address, verified.
			y := mustUser(t, s, "y", "viewer", "active")
			if _, err := s.SetUserEmailAsAdmin(y.ID, "addr@example.com"); err != nil {
				t.Fatalf("x's self-set copy must not block a proven holder: %v", err)
			}
			// And x cannot be made a second proven holder by a stale key.
			if u, _ := s.GetUserByVerifiedEmail("addr@example.com"); u == nil || u.ID != y.ID {
				t.Fatalf("lookup = %+v; want y", u)
			}
		})
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

func TestCreateUser_OnlyAProvenHolderBlocksTheAddress(t *testing.T) {
	s := newInviteTestStore(t)
	bob := mustUser(t, s, "bob", "viewer", "active")
	bob.Email = "Taken@example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser("newcomer", "taken@example.com", "oauth", "", []string{"viewer"}, "active")
	if err != nil {
		t.Fatalf("CreateUser with an address another user only typed in = %v, want nil", err)
	}
	if got, _ := s.GetUserByVerifiedEmail("taken@example.com"); got == nil || got.ID != u.ID {
		t.Fatalf("the new, proven holder must resolve: %+v", got)
	}
	if _, err := s.CreateUser("third", "TAKEN@example.com", "oauth", "", []string{"viewer"}, "active"); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("CreateUser with an address another account holds proven = %v, want ErrUserEmailTaken", err)
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

// keyFor returns the user id the idx:user:email cache key names, or "".
func keyFor(t *testing.T, s *PebbleStore, lower string) string {
	t.Helper()
	v, closer, err := s.db.Get([]byte(userEmailIndexPrefix + lower))
	if err == pebble.ErrNotFound {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	return string(v)
}

// putUsedInvite writes a used invite row the way an older build left it.
func putUsedInvite(t *testing.T, s *PebbleStore, inv Invite) {
	t.Helper()
	now := time.Now()
	inv.UsedAt = &now
	data, _ := json.Marshal(inv)
	if err := s.db.Set([]byte("invite:"+inv.Token), data, pebble.Sync); err != nil {
		t.Fatal(err)
	}
}

// B1: rows an older build wrote have no EmailSource. Reconcile stamps each
// from its history, and only the trusted stamps are verified. A viewer who
// typed a listed address into their profile before deploy is "self" and is
// never verified for it, whatever the operator lists.
func TestReconcileVerifiedEmails_StampsLegacyRows(t *testing.T) {
	s := newInviteTestStore(t)
	// Created by CreateUser on an older build: key names it.
	setup := mustUser(t, s, "setup", "admin", "active")
	setRawUserEmailSource(t, s, setup, setup.Email, "")
	// Typed into the profile on an older build: no key for it.
	squatter := mustUser(t, s, "squatter", "viewer", "active")
	setRawUserEmailSource(t, s, squatter, "owner.real@example.com", "")
	// An admin who changed their own email through the profile.
	selfAdmin := mustUser(t, s, "selfadmin", "admin", "active")
	setRawUserEmailSource(t, s, selfAdmin, "boss.new@example.com", "")
	// Accepted from a copied invite link on an older build, which wrote the
	// key for whatever address the invite named.
	linkGuest := mustUser(t, s, "linkguest", "viewer", "active")
	setRawUserEmailSource(t, s, linkGuest, "linkguest@example.com", "")
	if err := s.db.Set([]byte(userEmailIndexPrefix+"linkguest@example.com"), []byte(linkGuest.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	putUsedInvite(t, s, Invite{Token: "old-link", Username: "LinkGuest", Email: "linkguest@example.com", RoleID: "viewer"})
	// Accepted from a delivered invite, then rewritten by a rollback build.
	bound := mustUser(t, s, "bound", "viewer", "active")
	setRawUserEmailSource(t, s, bound, "bound@example.com", "")
	putUsedInvite(t, s, Invite{Token: "bound-tok", Username: "bound", Email: "bound@example.com", RoleID: "viewer", AcceptedUserID: bound.ID, EmailBinding: InviteEmailBound, SSOGrant: InviteSSOGranted})
	// Linked to an identity provider with this address.
	linked := mustUser(t, s, "linked", "viewer", "active")
	setRawUserEmailSource(t, s, linked, "linked@example.com", "")
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google", Subject: "g-linked", Email: "Linked@example.com", UserID: linked.ID}); err != nil {
		t.Fatal(err)
	}
	// An older build lowercased the address without trimming it.
	padded := mustUser(t, s, "padded", "viewer", "active")
	setRawUserEmailSource(t, s, padded, " Padded@example.com", "")
	if err := s.db.Set([]byte(userEmailIndexPrefix+" padded@example.com"), []byte(padded.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}

	res, err := s.ReconcileVerifiedEmails()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		setup.ID: EmailSourceSetup, squatter.ID: EmailSourceSelf, selfAdmin.ID: EmailSourceSelf,
		linkGuest.ID: EmailSourceInviteLink, bound.ID: EmailSourceInvite, linked.ID: EmailSourceIdP,
		padded.ID: EmailSourceSetup,
	}
	for id, src := range want {
		if got, _ := s.GetUserByID(id); got.EmailSource != src {
			t.Errorf("user %s (%s): source %q, want %q", got.Username, id, got.EmailSource, src)
		}
	}
	if len(res.StampedSelfAdmins) != 1 || res.StampedSelfAdmins[0] != selfAdmin.ID {
		t.Fatalf("StampedSelfAdmins = %v, want the self-set admin", res.StampedSelfAdmins)
	}
	for addr, id := range map[string]string{
		setup.Email: setup.ID, "bound@example.com": bound.ID, "linked@example.com": linked.ID, "padded@example.com": padded.ID,
	} {
		if u, _ := s.GetUserByVerifiedEmail(addr); u == nil || u.ID != id {
			t.Errorf("%s must be verified for %s: %+v", addr, id, u)
		}
	}
	for _, addr := range []string{"owner.real@example.com", "boss.new@example.com", "linkguest@example.com"} {
		if u, _ := s.GetUserByVerifiedEmail(addr); u != nil {
			t.Errorf("%s must not be verified: %+v", addr, u)
		}
		if id := keyFor(t, s, addr); id != "" {
			t.Errorf("%s must have no cache key (a rollback build would trust it), names %s", addr, id)
		}
	}
	// The squatter is displaced by the real owner's verified login.
	if _, displaced, err := s.CreateUserDisplacingUnverifiedEmail("owner.real", "owner.real@example.com", "oauth", "", []string{"viewer"}, "active"); err != nil || len(displaced) != 1 || displaced[0] != squatter.ID {
		t.Fatalf("displacement = %v, %v; want the squatter", displaced, err)
	}

	// Idempotent: a second run stamps nothing.
	res, err = s.ReconcileVerifiedEmails()
	if err != nil || len(res.Stamped) != 0 {
		t.Fatalf("second run = %+v, %v; want nothing stamped", res, err)
	}
	// A rollback build rewrites a row without the field; the next open
	// stamps it again from the cache key that build left alone.
	setRawUserEmailSource(t, s, setup, setup.Email, "")
	if _, err := s.ReconcileVerifiedEmails(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUserByID(setup.ID); got.EmailSource != EmailSourceSetup {
		t.Fatalf("restamped source = %q, want setup", got.EmailSource)
	}
}

// A legacy bound invite counts as "invite" only on the admin path: granted
// SSO, or a creator who is an admin now. Otherwise it proved nothing.
func TestReconcileVerifiedEmails_BoundInviteNeedsTheAdminPath(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "boss", "admin", "active")
	editor := mustUser(t, s, "editor1", "editor", "active")
	cases := []struct {
		name, creator, grant, want string
	}{
		{"granted", editor.ID, InviteSSOGranted, EmailSourceInvite},
		{"creator is an admin now", admin.ID, "", EmailSourceInvite},
		{"non-admin creator, no grant", editor.ID, InviteSSOCreatorNotAdmin, EmailSourceInviteLink},
		{"unknown creator", "01GONECREATOR", "", EmailSourceInviteLink},
	}
	for i, tc := range cases {
		u := mustUser(t, s, fmt.Sprintf("guest%d", i), "viewer", "active")
		addr := fmt.Sprintf("guest%d@example.com", i)
		setRawUserEmailSource(t, s, u, addr, "")
		putUsedInvite(t, s, Invite{Token: fmt.Sprintf("tok%d", i), Username: u.Username, Email: addr, RoleID: "viewer",
			CreatedByUserID: tc.creator, AcceptedUserID: u.ID, EmailBinding: InviteEmailBound, SSOGrant: tc.grant})
		cases[i].name = u.ID + " " + tc.name
		defer func(id, want, name string) {
			if got, _ := s.GetUserByID(id); got.EmailSource != want {
				t.Errorf("%s: source %q, want %q", name, got.EmailSource, want)
			}
		}(u.ID, tc.want, tc.name)
	}
	if _, err := s.ReconcileVerifiedEmails(); err != nil {
		t.Fatal(err)
	}
}

// The released build's only key writer is CreateUser, so a key from it
// stamps "setup": an older build's setup or bootstrap admin keeps its
// address verified across the upgrade.
func TestReconcileVerifiedEmails_KeyFromCreateUserIsSetup(t *testing.T) {
	s := newInviteTestStore(t)
	u, err := s.CreateUser("admin", "admin@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	setRawUserEmailSource(t, s, u, u.Email, "") // as the older build left it
	if keyFor(t, s, "admin@example.com") != u.ID {
		t.Fatal("precondition: CreateUser wrote the key")
	}
	if _, err := s.ReconcileVerifiedEmails(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUserByID(u.ID); got.EmailSource != EmailSourceSetup {
		t.Fatalf("source %q, want setup", got.EmailSource)
	}
	if v, _ := s.GetUserByVerifiedEmail("admin@example.com"); v == nil || v.ID != u.ID {
		t.Fatalf("still verified after the upgrade: %+v", v)
	}
}

// Reconcile rewrites the cache from live provenance: stale and squatted keys
// go, missing ones come back, a key naming an unreadable row stays, and an
// address proven for two accounts gets none.
func TestReconcileVerifiedEmails_RewritesTheCache(t *testing.T) {
	s := newInviteTestStore(t)
	proven := mustUser(t, s, "proven", "viewer", "active")
	if err := s.db.Delete([]byte(userEmailIndexPrefix+proven.Email), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	squatter := mustUser(t, s, "squatter", "viewer", "active")
	squatter.Email = "someone@example.com"
	if err := s.UpdateUser(squatter); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte(userEmailIndexPrefix+"someone@example.com"), []byte(squatter.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte("u:01UNREADABLEROW"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte(userEmailIndexPrefix+"broken@example.com"), []byte("01UNREADABLEROW"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	a := mustUser(t, s, "twin-a", "viewer", "active")
	b := mustUser(t, s, "twin-b", "viewer", "active")
	setRawUserEmailSource(t, s, a, "shared@example.com", EmailSourceSetup)
	setRawUserEmailSource(t, s, b, "shared@example.com", EmailSourceSetup)

	res, err := s.ReconcileVerifiedEmails()
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflict != 1 {
		t.Fatalf("result = %+v, want 1 conflict", res)
	}
	if keyFor(t, s, proven.Email) != proven.ID {
		t.Fatal("a proven address's missing key must be written")
	}
	if keyFor(t, s, "someone@example.com") != "" {
		t.Fatal("a squatted key must be dropped")
	}
	if keyFor(t, s, "broken@example.com") != "01UNREADABLEROW" {
		t.Fatal("a key naming an unreadable row is left alone")
	}
	if keyFor(t, s, "shared@example.com") != "" {
		t.Fatal("an address proven for two accounts gets no key")
	}
	if u, _ := s.GetUserByVerifiedEmail("shared@example.com"); u != nil {
		t.Fatalf("a conflicted address must not be verified: %+v", u)
	}
}

// The lookup is computed live and repairs the cache key it disagrees with;
// an unreadable row does not block it.
func TestGetUserByVerifiedEmail_LiveAndRepairsTheCache(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "admin1", "admin", "active")
	if err := s.db.Delete([]byte(userEmailIndexPrefix+admin.Email), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte("u:01UNREADABLEROW"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if u, err := s.GetUserByVerifiedEmail(admin.Email); err != nil || u == nil || u.ID != admin.ID {
		t.Fatalf("lookup = %+v, %v; want the admin, computed without the key", u, err)
	}
	if keyFor(t, s, admin.Email) != admin.ID {
		t.Fatal("the lookup must write the missing key back")
	}
	squatter := mustUser(t, s, "squatter", "viewer", "active")
	squatter.Email = "planted@example.com"
	if err := s.UpdateUser(squatter); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte(userEmailIndexPrefix+"planted@example.com"), []byte(squatter.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.GetUserByVerifiedEmail("planted@example.com"); u != nil {
		t.Fatalf("a planted key must not verify a self-set address: %+v", u)
	}
	if keyFor(t, s, "planted@example.com") != "" {
		t.Fatal("the lookup must delete a key that names an unproven holder")
	}
	// Status does not decide proof, so nothing sticks on reactivation.
	if _, err := s.SetUserStatus(admin.ID, "locked"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetUserStatus(admin.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.GetUserByVerifiedEmail(admin.Email); u == nil || u.ID != admin.ID {
		t.Fatalf("after deactivate and reactivate: %+v", u)
	}
}

// S4: a repeat login whose address is already IdP-verified takes no lock.
func TestMarkUserEmailVerified_FastPathTakesNoLock(t *testing.T) {
	s := newInviteTestStore(t)
	u := mustUser(t, s, "repeat", "viewer", "active")
	if ok, err := s.MarkUserEmailVerified(u.ID, u.Email); err != nil || !ok {
		t.Fatalf("first mark = %v, %v", ok, err)
	}
	s.inviteMu.Lock()
	done := make(chan bool, 1)
	go func() {
		ok, _ := s.MarkUserEmailVerified(u.ID, u.Email)
		done <- ok
	}()
	select {
	case ok := <-done:
		s.inviteMu.Unlock()
		if ok {
			t.Fatal("a repeat mark must report nothing written")
		}
	case <-time.After(2 * time.Second):
		s.inviteMu.Unlock()
		t.Fatal("a repeat mark of an IdP-verified address waited for inviteMu")
	}
}

// NIT3: an account that typed in an address another account holds proven is
// checked on every Cloudflare Access request. That no-op is decided from the
// cache key and the holder's own row, without inviteMu, userWriteMu or a scan
// of every user.
func TestMarkUserEmailVerified_ProvenElsewhereTakesNoLock(t *testing.T) {
	s := newInviteTestStore(t)
	holder := mustUser(t, s, "holder", "viewer", "active") // setup: proven
	typist := mustUser(t, s, "typist", "viewer", "active")
	setRawUserEmailSource(t, s, typist, holder.Email, EmailSourceSelf)
	locked := 0
	markEmailVerifiedLockedHook = func() { locked++ }
	t.Cleanup(func() { markEmailVerifiedLockedHook = nil })

	s.inviteMu.Lock()
	done := make(chan bool, 1)
	go func() {
		ok, _ := s.MarkUserEmailVerified(typist.ID, holder.Email)
		done <- ok
	}()
	select {
	case ok := <-done:
		s.inviteMu.Unlock()
		if ok {
			t.Fatal("another account's proven address must not be marked")
		}
	case <-time.After(2 * time.Second):
		s.inviteMu.Unlock()
		t.Fatal("the mark waited for inviteMu although another account holds the address proven")
	}
	if locked != 0 {
		t.Fatalf("locked path ran %d time(s); want the fast path", locked)
	}
	// A stale key (its holder no longer has the address) is only a hint: the
	// locked path decides, and the typist's address is then verified.
	setRawUserEmailSource(t, s, holder, "moved@example.com", EmailSourceSetup)
	if ok, err := s.MarkUserEmailVerified(typist.ID, typist.Email); err != nil || !ok {
		t.Fatalf("mark after the holder moved = %v, %v; want verified", ok, err)
	}
	if locked != 1 {
		t.Fatalf("locked path ran %d time(s); want 1", locked)
	}
}

// The admin action sets a verified address (source admin) and moves the key;
// clearing removes both; another account's address is refused.
func TestSetUserEmailAsAdmin(t *testing.T) {
	s := newInviteTestStore(t)
	u := mustUser(t, s, "member", "viewer", "active")
	old := u.Email
	other := mustUser(t, s, "other", "viewer", "active")
	prev, err := s.SetUserEmailAsAdmin(u.ID, " Member.New@example.com ")
	if err != nil || prev != old {
		t.Fatalf("SetUserEmailAsAdmin = %q, %v", prev, err)
	}
	got, _ := s.GetUserByID(u.ID)
	if got.Email != "Member.New@example.com" || got.EmailSource != EmailSourceAdmin {
		t.Fatalf("row = %q / %q, want the trimmed address with source admin", got.Email, got.EmailSource)
	}
	if keyFor(t, s, "member.new@example.com") != u.ID || keyFor(t, s, old) != "" {
		t.Fatal("the key must move from the old address to the new one")
	}
	if v, _ := s.GetUserByVerifiedEmail("member.new@example.com"); v == nil || v.ID != u.ID {
		t.Fatalf("an admin-set address is verified: %+v", v)
	}
	if _, err := s.SetUserEmailAsAdmin(u.ID, other.Email); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("another account's address = %v, want ErrUserEmailTaken", err)
	}
	if _, err := s.SetUserEmailAsAdmin(u.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUserByID(u.ID); got.Email != "" || got.EmailSource != "" || keyFor(t, s, "member.new@example.com") != "" {
		t.Fatalf("clearing must remove the address, source and key: %+v", got)
	}
	if _, err := s.SetUserEmailAsAdmin("01NOSUCHUSER", "x@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown id = %v, want ErrUserNotFound", err)
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

// A holder with provenance is never displaced, even without a cache key: a
// linked identity, a trusted EmailSource or an invite grant each refuse.
func TestCreateUserDisplacingUnverifiedEmail_RefusesAProvenHolder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prove func(t *testing.T, s *PebbleStore, holder *User)
	}{
		{"setup", func(t *testing.T, s *PebbleStore, holder *User) {
			setRawUserEmailSource(t, s, holder, "shared@example.com", EmailSourceSetup)
		}},
		{"admin", func(t *testing.T, s *PebbleStore, holder *User) {
			setRawUserEmailSource(t, s, holder, "shared@example.com", EmailSourceAdmin)
		}},
		{"invite", func(t *testing.T, s *PebbleStore, holder *User) {
			setRawUserEmailSource(t, s, holder, "shared@example.com", EmailSourceInvite)
		}},
		{"idp", func(t *testing.T, s *PebbleStore, holder *User) {
			setRawUserEmailSource(t, s, holder, "shared@example.com", EmailSourceIdP)
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

// A self-set holder is displaced by the real owner's IdP login, and the
// sources move with the address.
func TestCreateUserDisplacingUnverifiedEmail_SourcesMoveWithTheAddress(t *testing.T) {
	s := newInviteTestStore(t)
	squatter := mustUser(t, s, "squatter", "viewer", "active")
	squatter.Email = "guest@example.com" // typed in before the real guest registered
	if err := s.UpdateUser(squatter); err != nil {
		t.Fatal(err)
	}
	u, displaced, err := s.CreateUserDisplacingUnverifiedEmail("guest@example.com", "guest@example.com", "oauth", "", []string{"viewer"}, "active")
	if err != nil {
		t.Fatalf("a self-set holder must not block: %v", err)
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

// The proven check scans the rows: a holder whose cache key is missing still
// blocks an admin from making the address proven for someone else.
func TestSetUserEmailAsAdmin_SeesAnUnindexedProvenHolder(t *testing.T) {
	s := newInviteTestStore(t)
	alice := mustUser(t, s, "alice", "viewer", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")
	setRawUserEmailSource(t, s, alice, "legacy@example.com", EmailSourceSetup)
	if _, err := s.SetUserEmailAsAdmin(bob.ID, "Legacy@Example.com"); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("SetUserEmailAsAdmin to an unindexed proven address = %v, want ErrUserEmailTaken", err)
	}
}

func TestSetUserEmailAsAdmin_RacingClaimsOfOneEmailLetOneWin(t *testing.T) {
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
			_, errs[i] = s.SetUserEmailAsAdmin(u.ID, "contested@example.com")
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
		t.Fatalf("%d admin sets made the same email proven, want exactly 1", won)
	}
}

// SF1: re-setting an address a user already holds is still checked: it would
// make the address proven for this user while another account holds it
// proven, and every later SSO login with it would be denied as a conflict.
func TestSetUserEmailAsAdmin_UnchangedAddressStillChecked(t *testing.T) {
	s := newInviteTestStore(t)
	owner := mustUser(t, s, "owner", "admin", "active") // owner@admins.example.com, setup
	dup := mustUser(t, s, "dup", "viewer", "active")
	// A self-set duplicate as prod has one (UpdateUser now refuses to make it).
	setRawUserEmailSource(t, s, dup, "owner@admins.example.com", EmailSourceSelf)
	if _, err := s.SetUserEmailAsAdmin(dup.ID, "owner@admins.example.com"); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("admin re-set of a duplicate's unchanged address = %v, want ErrUserEmailTaken", err)
	}
	if got, _ := s.GetUserByID(dup.ID); got.EmailSource != EmailSourceSelf {
		t.Fatalf("a refused set must not write: source %q", got.EmailSource)
	}
	if u, _ := s.GetUserByVerifiedEmail("owner@admins.example.com"); u == nil || u.ID != owner.ID {
		t.Fatalf("the owner must still be the one verified holder: %+v", u)
	}
	// The owner re-setting its own address is fine.
	if _, err := s.SetUserEmailAsAdmin(owner.ID, "owner@admins.example.com"); err != nil {
		t.Fatalf("re-setting the proven holder's own address: %v", err)
	}
}

// SF1: MarkUserEmailVerified checks the other holders' proof, not the cache
// key, so two accounts cannot take turns pointing the key at themselves.
func TestMarkUserEmailVerified_NoPingPongWithAnotherProvenHolder(t *testing.T) {
	s := newInviteTestStore(t)
	owner := mustUser(t, s, "owner", "admin", "active")
	dup := mustUser(t, s, "dup", "viewer", "active")
	setRawUserEmailSource(t, s, dup, "owner@admins.example.com", EmailSourceSelf)
	// A stale key naming dup (as a rollback build could leave it).
	if err := s.db.Set([]byte(userEmailIndexPrefix+"owner@admins.example.com"), []byte(dup.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.MarkUserEmailVerified(dup.ID, "owner@admins.example.com"); err != nil || ok {
		t.Fatalf("mark for the duplicate = %v, %v; want a no-op", ok, err)
	}
	if got, _ := s.GetUserByID(dup.ID); got.EmailSource == EmailSourceIdP {
		t.Fatal("the duplicate must not become idp-verified")
	}
	if u, _ := s.GetUserByVerifiedEmail("owner@admins.example.com"); u == nil || u.ID != owner.ID {
		t.Fatalf("lookup = %+v; want the owner", u)
	}
	if keyFor(t, s, "owner@admins.example.com") != owner.ID {
		t.Fatal("the lookup must have pointed the key back at the owner")
	}
}

// SF2: an account that only typed an address in does not block an invite to
// it; one that holds it proven does.
func TestCreateInvite_OnlyAProvenHolderBlocks(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "admin1", "admin", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")
	bob.Email = "guest@example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatal(err)
	}
	mustInviteBy(t, s, admin.ID, "tok-ok", "guest", "guest@example.com")
	if _, err := s.CreateInvite(&Invite{Token: "tok-no", Username: "guest2", RoleID: "viewer", Email: "Admin1@admins.example.com"}); !errors.Is(err, ErrInviteEmailTaken) {
		t.Fatalf("invite to a proven address = %v, want ErrInviteEmailTaken", err)
	}
}

// S2: an admin's invitation emailed to an address proves the inbox, as an
// IdP-verified login does: an account that only typed the address into its
// profile loses it to the accept, recorded on the invite.
func TestConsumeInvite_DisplacesAnEmailSetThroughUpdateUser(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "admin1", "admin", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")
	deliveredInvite(t, s, admin.ID, "tok-profile-email", "guest", "guest@example.com")
	bob.Email = "Guest@example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatal(err)
	}
	guest, err := s.ConsumeInvite("tok-profile-email", "bcrypt", "h")
	if err != nil {
		t.Fatalf("ConsumeInvite = %v", err)
	}
	if got, _ := s.GetUserByID(bob.ID); got.Email != "" || got.Status != "active" {
		t.Fatalf("bob keeps his account and loses only the address: %+v", got)
	}
	inv, _ := s.GetInvite("tok-profile-email")
	if len(inv.DisplacedUserIDs) != 1 || inv.DisplacedUserIDs[0] != bob.ID {
		t.Fatalf("DisplacedUserIDs = %v, want bob", inv.DisplacedUserIDs)
	}
	if v, _ := s.GetUserByVerifiedEmail("guest@example.com"); v == nil || v.ID != guest.ID {
		t.Fatalf("the guest holds the address verified: %+v", v)
	}
}

// A holder with the address proven, or an unreadable row, refuses the accept
// and nothing is written.
func TestConsumeInvite_RefusesWhenAHolderIsProvenOrARowUnreadable(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, s *PebbleStore, holder *User){
		"proven holder": func(t *testing.T, s *PebbleStore, holder *User) {
			setRawUserEmailSource(t, s, holder, "guest@example.com", EmailSourceIdP)
		},
		"unreadable row": func(t *testing.T, s *PebbleStore, _ *User) {
			if err := s.db.Set([]byte("u:01UNREADABLEROW"), []byte("{not json"), pebble.Sync); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newInviteTestStore(t)
			admin := mustUser(t, s, "admin1", "admin", "active")
			holder := mustUser(t, s, "holder", "viewer", "active")
			deliveredInvite(t, s, admin.ID, "tok-x", "guest", "guest@example.com")
			holder.Email = "guest@example.com"
			if err := s.UpdateUser(holder); err != nil {
				t.Fatal(err)
			}
			setup(t, s, holder)
			if _, err := s.ConsumeInvite("tok-x", "bcrypt", "h"); !errors.Is(err, ErrInviteEmailTaken) {
				t.Fatalf("ConsumeInvite = %v, want ErrInviteEmailTaken", err)
			}
			if got, _ := s.GetUserByID(holder.ID); got.Email != "guest@example.com" {
				t.Fatal("nothing may be cleared on a refused accept")
			}
			if inv, _ := s.GetInvite("tok-x"); inv.UsedAt != nil {
				t.Fatal("a refused accept must leave the invite unused")
			}
		})
	}
}

// Nit 3: an invitation emailed by someone without the admin role binds
// nothing: no email on the account, no cache key, no grant.
func TestConsumeInvite_NonAdminCreatorBindsNothing(t *testing.T) {
	s := newInviteTestStore(t)
	editor := mustUser(t, s, "editor1", "editor", "active")
	deliveredInvite(t, s, editor.ID, "tok-editor", "guest", "guest@example.com")
	u, err := s.ConsumeInvite("tok-editor", "bcrypt", "h")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "" || u.EmailSource != "" {
		t.Fatalf("a non-admin's invite must not bind the address: %+v", u)
	}
	if keyFor(t, s, "guest@example.com") != "" {
		t.Fatal("a non-admin's invite must not write the cache key")
	}
	if g, _ := s.GetInviteEmailGrant("guest@example.com"); g != nil {
		t.Fatal("a non-admin's invite must not grant SSO")
	}
	inv, _ := s.GetInvite("tok-editor")
	if inv.EmailBinding != InviteEmailCreatorNotAdmin || inv.SSOGrant != InviteSSOCreatorNotAdmin {
		t.Fatalf("binding/grant = %q/%q", inv.EmailBinding, inv.SSOGrant)
	}
}

// NIT6: compatibility spellings are the same address everywhere. A Kelvin
// sign (U+212A) renders as K and folds to k, so "Kate@" must find, and be
// blocked by, the verified "kate@".
func TestEmails_CompatibilityCharactersAreNormalized(t *testing.T) {
	s := newInviteTestStore(t)
	kate, err := s.CreateUser("kate", "kate@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	other := mustUser(t, s, "other", "viewer", "active")
	if u, _ := s.GetUserByVerifiedEmail("Kate@example.com"); u == nil || u.ID != kate.ID {
		t.Fatalf("Kelvin-sign lookup = %+v; want kate", u)
	}
	if _, err := s.SetUserEmailAsAdmin(other.ID, "Kate@example.com"); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("admin set of the Kelvin spelling = %v, want ErrUserEmailTaken", err)
	}
	if _, err := s.CreateUser("kate2", "ｋate@example.com", "oauth", "", []string{"viewer"}, "active"); !errors.Is(err, ErrUserEmailTaken) {
		t.Fatalf("CreateUser with the fullwidth spelling = %v, want ErrUserEmailTaken", err)
	}
	inv, err := s.CreateInvite(&Invite{Token: "tok-kelvin", Username: "g", RoleID: "viewer", Email: " Roſe@Example.com "})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Email != "rose@example.com" {
		t.Fatalf("invite email stored as %q, want the normalized form", inv.Email)
	}
}

// Stamping never makes a second proven holder. A legacy row whose identity
// was recorded with an address another account already holds proven is
// stamped "self"; between two legacy rows the setup key outranks an identity.
func TestReconcile_StampingDemotesDuplicateProof(t *testing.T) {
	s := newInviteTestStore(t)
	owner := mustUser(t, s, "owner", "admin", "active") // setup, not legacy
	late := mustUser(t, s, "late", "viewer", "active")
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "github", Subject: "gh-late", Email: owner.Email, UserID: late.ID}); err != nil {
		t.Fatal(err)
	}
	setRawUserEmailSource(t, s, late, owner.Email, "") // legacy: identity points to idp

	keyed, err := s.CreateUser("keyed", "both@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	setRawUserEmailSource(t, s, keyed, "both@example.com", "") // legacy: key points to setup
	ided := mustUser(t, s, "ided", "viewer", "active")
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "github", Subject: "gh-ided", Email: "both@example.com", UserID: ided.ID}); err != nil {
		t.Fatal(err)
	}
	setRawUserEmailSource(t, s, ided, "both@example.com", "") // legacy: identity points to idp

	res, err := s.ReconcileVerifiedEmails()
	if err != nil {
		t.Fatal(err)
	}
	// Each demoted row is reported with the account that holds its address
	// verified, and the boot warning names that holder as the one not to clear.
	wantGroups := []DowngradedDuplicate{
		{HolderID: owner.ID, Demoted: []string{late.ID}},
		{HolderID: keyed.ID, Demoted: []string{ided.ID}},
	}
	slices.SortFunc(wantGroups, func(a, b DowngradedDuplicate) int { return strings.Compare(a.Demoted[0], b.Demoted[0]) })
	if len(res.DowngradedDuplicates) != len(wantGroups) {
		t.Fatalf("DowngradedDuplicates = %+v, want %+v", res.DowngradedDuplicates, wantGroups)
	}
	for i, g := range res.DowngradedDuplicates {
		if g.HolderID != wantGroups[i].HolderID || !slices.Equal(g.Demoted, wantGroups[i].Demoted) {
			t.Fatalf("DowngradedDuplicates = %+v, want %+v", res.DowngradedDuplicates, wantGroups)
		}
		msg := downgradedDuplicateWarning(g)
		if !strings.Contains(msg, "Do NOT clear the email of account "+g.HolderID+" (the verified holder)") || !strings.Contains(msg, g.Demoted[0]) {
			t.Fatalf("warning does not name the holder %s and the demoted %v: %s", g.HolderID, g.Demoted, msg)
		}
		if strings.Contains(msg, "merge the accounts") || strings.Contains(msg, "@") {
			t.Fatalf("warning implies a merge tool or logs an address: %s", msg)
		}
	}
	for id, want := range map[string]string{late.ID: EmailSourceSelf, ided.ID: EmailSourceSelf, keyed.ID: EmailSourceSetup} {
		if got, _ := s.GetUserByID(id); got.EmailSource != want {
			t.Fatalf("user %s stamped %q, want %q", id, got.EmailSource, want)
		}
	}
	if u, _ := s.GetUserByVerifiedEmail(owner.Email); u == nil || u.ID != owner.ID {
		t.Fatalf("owner lookup = %+v", u)
	}
	if u, _ := s.GetUserByVerifiedEmail("both@example.com"); u == nil || u.ID != keyed.ID {
		t.Fatalf("both@ lookup = %+v; want the setup-keyed account", u)
	}
}

// A legacy row is stamped with the BEST source it qualifies for. The owner's
// first account has the setup key and later linked an identity with the same
// address; a second account only linked an identity. Stopping at the first
// match stamped both "idp", a tie that demoted both, so the owner lost the
// address on every account. The setup key outranks the identity.
func TestReconcile_KeyAndIdentityOutranksIdentityOnly(t *testing.T) {
	s := newInviteTestStore(t)
	first, err := s.CreateUser("owner", "owner@example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google", Subject: "g-owner", Email: "owner@example.com", UserID: first.ID}); err != nil {
		t.Fatal(err)
	}
	setRawUserEmailSource(t, s, first, "owner@example.com", "") // legacy: key and identity
	second := mustUser(t, s, "owner-sso", "viewer", "active")
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "github", Subject: "gh-owner", Email: "owner@example.com", UserID: second.ID}); err != nil {
		t.Fatal(err)
	}
	setRawUserEmailSource(t, s, second, "owner@example.com", "") // legacy: identity only

	res, err := s.ReconcileVerifiedEmails()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{first.ID: EmailSourceSetup, second.ID: EmailSourceSelf} {
		if got, _ := s.GetUserByID(id); got.EmailSource != want {
			t.Fatalf("user %s stamped %q, want %q", id, got.EmailSource, want)
		}
	}
	if want := []DowngradedDuplicate{{HolderID: first.ID, Demoted: []string{second.ID}}}; len(res.DowngradedDuplicates) != 1 ||
		res.DowngradedDuplicates[0].HolderID != want[0].HolderID || !slices.Equal(res.DowngradedDuplicates[0].Demoted, want[0].Demoted) {
		t.Fatalf("DowngradedDuplicates = %+v, want only the identity-only account, held by the setup account: %+v", res.DowngradedDuplicates, want)
	}
	if u, _ := s.GetUserByVerifiedEmail("owner@example.com"); u == nil || u.ID != first.ID {
		t.Fatalf("verified lookup = %+v; want the account with the setup key", u)
	}
}

// Two legacy rows tie for the best source. The tie goes to the row the
// pre-existing idx:user:email key names, the account main's SSO lookup
// returned, so the owner keeps the same account as before the update. With
// no key naming either, both are demoted.
func TestReconcile_TieGoesToTheRowTheKeyNames(t *testing.T) {
	s := newInviteTestStore(t)
	// Accepted from a copied link on an older build, which wrote the key for
	// the invite's address; then linked an identity with it: "idp" (the key
	// is the accept's write, not CreateUser's, so it is not "setup").
	keyed := mustUser(t, s, "keyed", "viewer", "active")
	setRawUserEmailSource(t, s, keyed, "tie@example.com", "")
	if err := s.db.Set([]byte(userEmailIndexPrefix+"tie@example.com"), []byte(keyed.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	putUsedInvite(t, s, Invite{Token: "tie-link", Username: "keyed", Email: "tie@example.com", RoleID: "viewer", AcceptedUserID: keyed.ID})
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google", Subject: "g-keyed", Email: "tie@example.com", UserID: keyed.ID}); err != nil {
		t.Fatal(err)
	}
	other := mustUser(t, s, "other", "viewer", "active")
	setRawUserEmailSource(t, s, other, "tie@example.com", "")
	if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "github", Subject: "gh-other", Email: "tie@example.com", UserID: other.ID}); err != nil {
		t.Fatal(err)
	}
	// A second address where nothing breaks the tie.
	a := mustUser(t, s, "a", "viewer", "active")
	setRawUserEmailSource(t, s, a, "nokey@example.com", "")
	b := mustUser(t, s, "b", "viewer", "active")
	setRawUserEmailSource(t, s, b, "nokey@example.com", "")
	for _, u := range []*User{a, b} {
		if _, err := s.CreateOAuthIdentity(&OAuthIdentity{Provider: "google", Subject: "g-" + u.Username, Email: "nokey@example.com", UserID: u.ID}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.ReconcileVerifiedEmails(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		keyed.ID: EmailSourceIdP, other.ID: EmailSourceSelf,
		a.ID: EmailSourceSelf, b.ID: EmailSourceSelf,
	} {
		if got, _ := s.GetUserByID(id); got.EmailSource != want {
			t.Fatalf("user %s stamped %q, want %q", got.Username, got.EmailSource, want)
		}
	}
	if u, _ := s.GetUserByVerifiedEmail("tie@example.com"); u == nil || u.ID != keyed.ID {
		t.Fatalf("tie@ lookup = %+v; want the account the key named", u)
	}
	if u, _ := s.GetUserByVerifiedEmail("nokey@example.com"); u != nil {
		t.Fatalf("nokey@ lookup = %+v; want nobody (an unbroken tie demotes both)", u)
	}
}

// A self-set admin whose address another account holds verified is reported
// with that holder's id, so the boot warning can name both accounts and say
// which one must not be cleared.
func TestReconcile_SelfAdminConflictNamesTheVerifiedHolder(t *testing.T) {
	s := newInviteTestStore(t)
	holder := mustUser(t, s, "ssoholder", "viewer", "active")
	setRawUserEmailSource(t, s, holder, "shared@example.com", EmailSourceIdP)
	admin := mustUser(t, s, "selfadmin", "admin", "active")
	setRawUserEmailSource(t, s, admin, "shared@example.com", "")

	res, err := s.ReconcileVerifiedEmails()
	if err != nil {
		t.Fatal(err)
	}
	want := []SelfAdminConflict{{AdminID: admin.ID, HolderID: holder.ID}}
	if !slices.Equal(res.SelfAdminsVerifiedElsewhere, want) {
		t.Fatalf("SelfAdminsVerifiedElsewhere = %+v, want %+v", res.SelfAdminsVerifiedElsewhere, want)
	}
}

// With no verified holder (a tie the key breaks for nobody), the warning says
// so, names every demoted account, and implies no merge tool.
func TestDowngradedDuplicateWarning_NoHolder(t *testing.T) {
	msg := downgradedDuplicateWarning(DowngradedDuplicate{Demoted: []string{"u-a", "u-b"}})
	for _, want := range []string{"u-a", "u-b", "no account holds it verified", "until they are merged"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("warning lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "merge the accounts") || strings.Contains(msg, "clear the duplicate") {
		t.Fatalf("warning keeps the old advice: %s", msg)
	}
}
