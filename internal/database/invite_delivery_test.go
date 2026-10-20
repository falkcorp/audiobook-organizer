// file: internal/database/invite_delivery_test.go
// version: 1.1.0
// guid: 2a8c5e71-4b9d-4f03-a6e2-7d1c9b3f5e86
// last-edited: 2026-10-19

package database

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func newInviteTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustInvite(t *testing.T, s *PebbleStore, token, username, email string) *Invite {
	t.Helper()
	return mustInviteBy(t, s, "", token, username, email)
}

func mustInviteBy(t *testing.T, s *PebbleStore, creatorID, token, username, email string) *Invite {
	t.Helper()
	inv, err := s.CreateInvite(&Invite{Token: token, Username: username, Email: email, RoleID: "viewer",
		CreatedByUserID: creatorID, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func mustUser(t *testing.T, s *PebbleStore, username, role, status string) *User {
	t.Helper()
	u, err := s.CreateUser(username, username+"@admins.example.com", "bcrypt", "h", []string{role}, status)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// deliveredInvite creates an invite by creatorID and marks it emailed to email.
func deliveredInvite(t *testing.T, s *PebbleStore, creatorID, token, username, email string) {
	t.Helper()
	inv := mustInviteBy(t, s, creatorID, token, username, email)
	if _, err := s.MarkInviteEmailSent(token, inv.Email, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeInvite_EmailGrantAndUserEmail(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "boss", "admin", "active")
	inv := mustInviteBy(t, s, admin.ID, "tok1", "guest", "  Guest@Example.COM ")
	if inv.Email != "guest@example.com" {
		t.Fatalf("invite email not normalized: %q", inv.Email)
	}
	if _, err := s.MarkInviteEmailSent("tok1", "Guest@example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	u, err := s.ConsumeInvite("tok1", "bcrypt", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "guest@example.com" {
		t.Fatalf("user email = %q", u.Email)
	}
	if byEmail, _ := s.GetUserByEmail("guest@example.com"); byEmail == nil || byEmail.ID != u.ID {
		t.Fatal("email index does not point at the new user")
	}
	if g, err := s.GetInviteEmailGrant("GUEST@example.com"); err != nil || g == nil || g.UserID != u.ID || g.CFAdded {
		t.Fatalf("grant = %+v (%v), want one for %q", g, err, u.ID)
	}
	got, _ := s.GetInvite("tok1")
	if got.UsedAt == nil || got.AcceptedUserID != u.ID || got.EmailBinding != InviteEmailBound || got.SSOGrant != InviteSSOGranted {
		t.Fatalf("invite outcome not recorded: %+v", got)
	}
	if g, _ := s.GetInviteEmailGrant("other@example.com"); g != nil {
		t.Fatal("grant for an email nobody was invited with")
	}
}

// A link the admin copied by hand proves nothing about the address.
func TestConsumeInvite_ManualLinkBindsNoEmail(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "boss", "admin", "active")
	mustInviteBy(t, s, admin.ID, "tok", "guest", "guest@example.com")
	u, err := s.ConsumeInvite("tok", "bcrypt", "h")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "" {
		t.Fatalf("user email = %q, want none for a never-emailed invite", u.Email)
	}
	if byEmail, _ := s.GetUserByEmail("guest@example.com"); byEmail != nil {
		t.Fatal("email index written for an unproven address")
	}
	if g, _ := s.GetInviteEmailGrant("guest@example.com"); g != nil {
		t.Fatal("SSO grant written for an unproven address")
	}
	if inv, _ := s.GetInvite("tok"); inv.EmailBinding != InviteEmailNotEmailed || inv.SSOGrant != "" {
		t.Fatalf("outcome = %+v", inv)
	}
}

func TestConsumeInvite_SentToOtherAddressBindsNoEmail(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "boss", "admin", "active")
	mustInviteBy(t, s, admin.ID, "tok", "guest", "guest@example.com")
	if _, err := s.MarkInviteEmailSent("tok", "someone-else@example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	u, err := s.ConsumeInvite("tok", "bcrypt", "h")
	if err != nil || u.Email != "" {
		t.Fatalf("user email = %q (%v), want none", u.Email, err)
	}
}

func TestConsumeInvite_NonAdminCreatorGetsNoGrant(t *testing.T) {
	for _, tc := range []struct{ name, role, status string }{
		{"editor creator", "editor", "active"},
		{"locked admin creator", "admin", "locked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newInviteTestStore(t)
			creator := mustUser(t, s, "creator", tc.role, tc.status)
			deliveredInvite(t, s, creator.ID, "tok", "guest", "guest@example.com")
			u, err := s.ConsumeInvite("tok", "bcrypt", "h")
			if err != nil {
				t.Fatal(err)
			}
			if u.Email != "guest@example.com" {
				t.Fatalf("a delivered email is still the user's email: %q", u.Email)
			}
			if g, _ := s.GetInviteEmailGrant("guest@example.com"); g != nil {
				t.Fatal("a non-admin creator's invite must not grant SSO")
			}
			if inv, _ := s.GetInvite("tok"); inv.SSOGrant != InviteSSOCreatorNotAdmin {
				t.Fatalf("SSOGrant = %q", inv.SSOGrant)
			}
		})
	}
	s := newInviteTestStore(t)
	deliveredInvite(t, s, "", "tok", "guest", "guest@example.com") // no recorded creator
	if _, err := s.ConsumeInvite("tok", "bcrypt", "h"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetInviteEmailGrant("guest@example.com"); g != nil {
		t.Fatal("an invite with no recorded creator must not grant SSO")
	}
}

func TestInviteEmailGrant_CFAddedAndDeleteOnlyForItsUser(t *testing.T) {
	s := newInviteTestStore(t)
	admin := mustUser(t, s, "boss", "admin", "active")
	deliveredInvite(t, s, admin.ID, "tok", "guest", "guest@example.com")
	u, err := s.ConsumeInvite("tok", "bcrypt", "h")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetInviteEmailGrantCFAdded("guest@example.com", "someone-else"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetInviteEmailGrant("guest@example.com"); g.CFAdded {
		t.Fatal("CFAdded set for a different user id")
	}
	if err := s.SetInviteEmailGrantCFAdded("guest@example.com", u.ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetInviteEmailGrant("guest@example.com"); !g.CFAdded {
		t.Fatal("CFAdded not recorded")
	}
	if g, err := s.DeleteInviteEmailGrant("guest@example.com", "someone-else"); err != nil || g != nil {
		t.Fatalf("delete for another user removed the grant: %+v %v", g, err)
	}
	g, err := s.DeleteInviteEmailGrant("GUEST@example.com", u.ID)
	if err != nil || g == nil || !g.CFAdded {
		t.Fatalf("delete returned %+v %v", g, err)
	}
	if g, _ := s.GetInviteEmailGrant("guest@example.com"); g != nil {
		t.Fatal("grant still present after delete")
	}
	if g, err := s.DeleteInviteEmailGrant("guest@example.com", u.ID); err != nil || g != nil {
		t.Fatal("second delete must be a no-op")
	}
}

func TestConsumeInvite_NoEmailWritesNoGrant(t *testing.T) {
	s := newInviteTestStore(t)
	mustInvite(t, s, "tok1", "guest", "")
	u, err := s.ConsumeInvite("tok1", "bcrypt", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "" {
		t.Fatalf("user email = %q, want empty", u.Email)
	}
}

func TestInvite_EmailOwnedByExistingUserIsRefused(t *testing.T) {
	s := newInviteTestStore(t)
	if _, err := s.CreateUser("owner", "taken@example.com", "bcrypt", "h", []string{"admin"}, "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateInvite(&Invite{Token: "t", Username: "g", Email: "Taken@example.com", RoleID: "viewer"}); !errors.Is(err, ErrInviteEmailTaken) {
		t.Fatalf("CreateInvite err = %v, want ErrInviteEmailTaken", err)
	}
	// An invite created before the email was taken must not repoint the index.
	deliveredInvite(t, s, "", "t2", "g2", "later@example.com")
	owner2, err := s.CreateUser("second", "later@example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeInvite("t2", "bcrypt", "h"); !errors.Is(err, ErrInviteEmailTaken) {
		t.Fatalf("ConsumeInvite err = %v, want ErrInviteEmailTaken", err)
	}
	if u, _ := s.GetUserByEmail("later@example.com"); u == nil || u.ID != owner2.ID {
		t.Fatal("email index was repointed")
	}
	if inv, _ := s.GetInvite("t2"); inv.UsedAt != nil {
		t.Fatal("a refused accept consumed the invite")
	}
}

func TestConsumeInvite_ConcurrentAcceptsAreSingleUse(t *testing.T) {
	s := newInviteTestStore(t)
	deliveredInvite(t, s, "", "tok", "racer", "racer@example.com")
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ConsumeInvite("tok", "bcrypt", "h"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d accepts succeeded, want exactly 1", wins)
	}
}

func TestMarkInviteEmailSent_RefusesConsumedInvite(t *testing.T) {
	s := newInviteTestStore(t)
	mustInvite(t, s, "tok", "guest", "guest@example.com")
	at := time.Now()
	inv, err := s.MarkInviteEmailSent("tok", "Guest@example.com", at)
	if err != nil || inv.EmailSentAt == nil || !inv.EmailSentAt.Equal(at) || inv.EmailSentTo != "guest@example.com" {
		t.Fatalf("mark sent: %+v %v", inv, err)
	}
	if _, err := s.ConsumeInvite("tok", "bcrypt", "h"); err != nil {
		t.Fatal(err)
	}
	// A resend that finishes after the accept must not resurrect the invite.
	if _, err := s.MarkInviteEmailSent("tok", "guest@example.com", time.Now()); !errors.Is(err, ErrInviteNotPending) {
		t.Fatalf("err = %v, want ErrInviteNotPending", err)
	}
	got, _ := s.GetInvite("tok")
	if got.UsedAt == nil {
		t.Fatal("consumed invite was un-consumed")
	}
	if active, _ := s.ListActiveInvites(); len(active) != 0 {
		t.Fatalf("consumed invite listed as active: %+v", active)
	}
	if _, err := s.MarkInviteEmailSent("missing", "x@example.com", time.Now()); !errors.Is(err, ErrInviteNotPending) {
		t.Fatalf("missing invite: err = %v", err)
	}
}

func TestRecordInviteGuestPolicy_AndListAccepted(t *testing.T) {
	s := newInviteTestStore(t)
	for i := range 3 {
		mustInvite(t, s, fmt.Sprintf("t%d", i), fmt.Sprintf("u%d", i), fmt.Sprintf("u%d@example.com", i))
	}
	before := time.Now().Add(-time.Second)
	if _, err := s.ConsumeInvite("t0", "bcrypt", "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeInvite("t1", "bcrypt", "h"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInviteGuestPolicy("t1", InviteGuestPolicyFailed, "HTTP 403", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListAcceptedInvitesSince(before)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("accepted = %d, want 2", len(got))
	}
	var t1 *Invite
	for i := range got {
		if got[i].Token == "t1" {
			t1 = &got[i]
		}
	}
	if t1 == nil || t1.GuestPolicyStatus != InviteGuestPolicyFailed || t1.GuestPolicyDetail != "HTTP 403" || t1.GuestPolicyAt == nil {
		t.Fatalf("guest policy outcome not recorded: %+v", t1)
	}
	if later, _ := s.ListAcceptedInvitesSince(time.Now().Add(time.Hour)); len(later) != 0 {
		t.Fatal("since filter ignored")
	}
}
