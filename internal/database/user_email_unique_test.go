// file: internal/database/user_email_unique_test.go
// version: 1.0.0
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
	if u, _ := s.GetUserByEmail("alice@admins.example.com"); u == nil || u.Username != "alice" {
		t.Fatalf("the email index must still point at alice: %+v", u)
	}
}

func TestUpdateUser_MovesTheEmailIndex(t *testing.T) {
	s := newInviteTestStore(t)
	alice := mustUser(t, s, "alice", "viewer", "active")
	bob := mustUser(t, s, "bob", "viewer", "active")

	alice.Email = "Alice.New@example.com"
	if err := s.UpdateUser(alice); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.GetUserByEmail("alice.new@example.com"); u == nil || u.ID != alice.ID {
		t.Fatalf("new email must resolve to alice: %+v", u)
	}
	if u, _ := s.GetUserByEmail("alice@admins.example.com"); u != nil {
		t.Fatalf("old email must no longer resolve: %+v", u)
	}
	// The freed address can be taken by someone else.
	bob.Email = "alice@admins.example.com"
	if err := s.UpdateUser(bob); err != nil {
		t.Fatalf("a freed email must be usable: %v", err)
	}
	// A case-only change of one's own email is not a conflict.
	alice.Email = "ALICE.NEW@example.com"
	if err := s.UpdateUser(alice); err != nil {
		t.Fatalf("case-only change of own email: %v", err)
	}
	if u, _ := s.GetUserByEmail("alice.new@example.com"); u == nil || u.ID != alice.ID {
		t.Fatalf("case-only change must keep the index: %+v", u)
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
