// file: internal/database/pebble_store_auth_epoch_test.go
// version: 1.0.0
// guid: da259f0d-609e-4d78-8f4f-d1d0071a7291
// last-edited: 2026-10-19

package database

import (
	"errors"
	"sync"
	"testing"
)

func newEpochUser(t *testing.T, p *PebbleStore) *User {
	t.Helper()
	u, err := p.CreateUser("epoch", "epoch@example.com", "bcrypt", "hash-old", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func mustGetUser(t *testing.T, p *PebbleStore, id string) *User {
	t.Helper()
	u, err := p.GetUserByID(id)
	if err != nil || u == nil {
		t.Fatalf("GetUserByID(%s) = %v, %v", id, u, err)
	}
	return u
}

// UpdateUser enforces the credential epoch: a password change must bump it by
// exactly one, and no write may lower it or skip ahead.
func TestUpdateUser_CredentialEpochRules(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	if u.CredentialEpoch != 0 {
		t.Fatalf("a new user starts at epoch 0, got %d", u.CredentialEpoch)
	}

	noBump := mustGetUser(t, p, u.ID)
	noBump.PasswordHash = "hash-new"
	if err := p.UpdateUser(noBump); !errors.Is(err, ErrUserStale) {
		t.Fatalf("a password change without an epoch bump must be refused, got %v", err)
	}

	skip := mustGetUser(t, p, u.ID)
	skip.CredentialEpoch = 2
	if err := p.UpdateUser(skip); !errors.Is(err, ErrUserStale) {
		t.Fatalf("an epoch jump of two must be refused, got %v", err)
	}

	stale := mustGetUser(t, p, u.ID) // read before the change below
	change := mustGetUser(t, p, u.ID)
	change.PasswordHash = "hash-new"
	change.CredentialEpoch++
	if err := p.UpdateUser(change); err != nil {
		t.Fatalf("a password change with its bump must land: %v", err)
	}

	// A read-modify-write built from the copy read before the change (a role edit,
	// say) would put the old hash and epoch back.
	stale.Roles = []string{"admin"}
	if err := p.UpdateUser(stale); !errors.Is(err, ErrUserStale) {
		t.Fatalf("a stale copy must not overwrite a credential change, got %v", err)
	}
	got := mustGetUser(t, p, u.ID)
	if got.PasswordHash != "hash-new" || got.CredentialEpoch != 1 {
		t.Fatalf("stored = %q epoch %d, want the change to stand", got.PasswordHash, got.CredentialEpoch)
	}

	// An ordinary edit from a fresh copy is fine.
	got.Roles = []string{"admin"}
	if err := p.UpdateUser(got); err != nil {
		t.Fatalf("a fresh non-credential edit must land: %v", err)
	}
}

// Two password changes racing from the same read: exactly one wins.
func TestUpdateUser_ConcurrentPasswordChangesOneWins(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	a, b := mustGetUser(t, p, u.ID), mustGetUser(t, p, u.ID)
	a.PasswordHash, b.PasswordHash = "hash-a", "hash-b"
	a.CredentialEpoch++
	b.CredentialEpoch++
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, cand := range []*User{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = p.UpdateUser(cand) }()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrUserStale) {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d of 2 racing password changes landed, want exactly 1", ok)
	}
}

// The login rehash must not clobber a password change that landed after the
// login verified the old password, nor a concurrent deactivation.
func TestRehashUserPassword_IsConditional(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	verified := mustGetUser(t, p, u.ID) // what the login checked the password against

	change := mustGetUser(t, p, u.ID)
	change.PasswordHash = "hash-new"
	change.CredentialEpoch++
	if err := p.UpdateUser(change); err != nil {
		t.Fatal(err)
	}
	wrote, err := p.RehashUserPassword(u.ID, verified.PasswordHash, "argon2id", "argon-of-old")
	if err != nil || wrote {
		t.Fatalf("rehash after a password change = (%v, %v), want (false, nil)", wrote, err)
	}
	if got := mustGetUser(t, p, u.ID); got.PasswordHash != "hash-new" || got.PasswordHashAlgo != "bcrypt" {
		t.Fatalf("rehash clobbered the new password: %q/%q", got.PasswordHashAlgo, got.PasswordHash)
	}

	// Same password, but the account was locked after the login read it: the
	// rehash lands on the fresh row and keeps the lock and the epoch.
	verified = mustGetUser(t, p, u.ID)
	lock := mustGetUser(t, p, u.ID)
	lock.Status = "locked"
	lock.CredentialEpoch++
	if err := p.UpdateUser(lock); err != nil {
		t.Fatal(err)
	}
	wrote, err = p.RehashUserPassword(u.ID, verified.PasswordHash, "argon2id", "argon-of-new")
	if err != nil || !wrote {
		t.Fatalf("rehash of an unchanged password = (%v, %v), want (true, nil)", wrote, err)
	}
	got := mustGetUser(t, p, u.ID)
	if got.Status != "locked" || got.CredentialEpoch != 2 || got.PasswordHash != "argon-of-new" {
		t.Fatalf("after rehash: status %q epoch %d hash %q; want locked, 2, argon-of-new", got.Status, got.CredentialEpoch, got.PasswordHash)
	}
}

// No session write may move the credential stamp it was created with.
func TestABSSession_UpdateCannotChangeCredentialEpoch(t *testing.T) {
	p := newABSSessionStore(t)
	s := sampleABSSession("s1", "u1")
	if err := p.CreateABSSession(s); err != nil {
		t.Fatal(err)
	}
	next, err := p.GetABSSession("s1")
	if err != nil {
		t.Fatal(err)
	}
	next.CredentialEpoch = 7
	if err := p.UpdateABSSession(next); !errors.Is(err, ErrABSSessionStale) {
		t.Fatalf("re-stamping a session must be refused, got %v", err)
	}
}

func TestABSSession_CredentialsCurrent(t *testing.T) {
	s := &ABSSession{CredentialEpoch: 1}
	for _, tc := range []struct {
		user int64
		want bool
	}{{0, false}, {1, true}, {2, false}} {
		if got := s.CredentialsCurrent(&User{CredentialEpoch: tc.user}); got != tc.want {
			t.Errorf("session epoch 1, user epoch %d: got %v want %v", tc.user, got, tc.want)
		}
	}
	if (&ABSSession{}).CredentialsCurrent(&User{}) != true {
		t.Error("a legacy session (no stamp) and a legacy user (no epoch) must match, or deploy signs everyone out")
	}
}
