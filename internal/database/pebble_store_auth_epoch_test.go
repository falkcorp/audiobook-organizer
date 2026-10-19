// file: internal/database/pebble_store_auth_epoch_test.go
// version: 1.1.0
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

// UpdateUser writes ordinary fields only: any change to the password, Status or
// CredentialEpoch is refused, so a whole-row write can neither change nor revert
// them.
func TestUpdateUser_RefusesProtectedFields(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	for name, mutate := range map[string]func(*User){
		"password hash": func(x *User) { x.PasswordHash = "hash-new" },
		"password algo": func(x *User) { x.PasswordHashAlgo = "argon2id" },
		"status":        func(x *User) { x.Status = "locked" },
		"epoch":         func(x *User) { x.CredentialEpoch++ },
	} {
		cp := mustGetUser(t, p, u.ID)
		mutate(cp)
		if err := p.UpdateUser(cp); !errors.Is(err, ErrUserProtectedField) {
			t.Errorf("%s change through UpdateUser = %v, want ErrUserProtectedField", name, err)
		}
	}
	fresh := mustGetUser(t, p, u.ID)
	fresh.Email = "new@example.com"
	if err := p.UpdateUser(fresh); err != nil {
		t.Fatalf("an ordinary edit from a fresh copy must land: %v", err)
	}
}

// A copy read before a password or status change cannot revert it.
func TestUpdateUser_StaleCopyCannotRevertStatusOrPassword(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	stale := mustGetUser(t, p, u.ID)
	if _, err := p.SetUserStatus(u.ID, "locked"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SetUserPassword(u.ID, "", "bcrypt", "hash-new"); err != nil {
		t.Fatal(err)
	}
	stale.Email = "edit@example.com" // an innocent edit on a copy from before both
	if err := p.UpdateUser(stale); !errors.Is(err, ErrUserProtectedField) {
		t.Fatalf("stale whole-row write = %v, want ErrUserProtectedField", err)
	}
	got := mustGetUser(t, p, u.ID)
	if got.Status != "locked" || got.PasswordHash != "hash-new" || got.CredentialEpoch != 2 {
		t.Fatalf("after stale write: status %q hash %q epoch %d; want locked, hash-new, 2", got.Status, got.PasswordHash, got.CredentialEpoch)
	}
}

// Two admin resets racing from the same starting row both land, at DISTINCT
// epochs, and a session stamped with any epoch before the last is refused.
func TestSetUserPassword_RacingChangesEndAtDistinctEpochs(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	var wg sync.WaitGroup
	epochs := make([]int64, 2)
	errs := make([]error, 2)
	for i, hash := range []string{"hash-a", "hash-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := p.SetUserPassword(u.ID, "", "bcrypt", hash)
			errs[i] = err
			if got != nil {
				epochs[i] = got.CredentialEpoch
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if epochs[0] == epochs[1] {
		t.Fatalf("both racing changes ended at epoch %d; each must bump", epochs[0])
	}
	final := mustGetUser(t, p, u.ID)
	if final.CredentialEpoch != 2 {
		t.Fatalf("final epoch %d, want 2", final.CredentialEpoch)
	}
	for _, stamp := range []int64{0, 1} {
		if (&ABSSession{CredentialEpoch: stamp}).CredentialsCurrent(final) {
			t.Fatalf("a session stamped with old epoch %d still passes", stamp)
		}
	}
}

// A self-service change is a compare-and-swap on the password it verified: of
// two racing from the same verified hash, exactly one lands.
func TestSetUserPassword_SelfServiceIsCompareAndSwap(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, hash := range []string{"hash-a", "hash-b"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = p.SetUserPassword(u.ID, "hash-old", "bcrypt", hash) }()
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
		t.Fatalf("%d of 2 racing self-service changes landed, want exactly 1", ok)
	}
	if got := mustGetUser(t, p, u.ID); got.CredentialEpoch != 1 {
		t.Fatalf("epoch %d after one landed change, want 1", got.CredentialEpoch)
	}
}

// SetUserStatus bumps the epoch on deactivation AND reactivation, and the reset
// link's epoch bump cannot revert a deactivation that landed after its read.
func TestSetUserStatus_BumpsBothWaysAndBumpKeepsStatus(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	locked, err := p.SetUserStatus(u.ID, "locked")
	if err != nil || locked.CredentialEpoch != 1 || locked.Status != "locked" {
		t.Fatalf("deactivate = %+v, %v; want locked at epoch 1", locked, err)
	}
	active, err := p.SetUserStatus(u.ID, "active")
	if err != nil || active.CredentialEpoch != 2 || active.Status != "active" {
		t.Fatalf("reactivate = %+v, %v; want active at epoch 2", active, err)
	}
	if _, err := p.SetUserStatus(u.ID, "locked"); err != nil {
		t.Fatal(err)
	}
	bumped, err := p.BumpUserCredentialEpoch(u.ID)
	if err != nil || bumped.Status != "locked" || bumped.CredentialEpoch != 4 {
		t.Fatalf("reset-link bump = %+v, %v; want still locked at epoch 4", bumped, err)
	}
	if _, err := p.SetUserStatus("nobody", "locked"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown user = %v, want ErrUserNotFound", err)
	}
}

// The login rehash must not clobber a password change that landed after the
// login verified the old password, nor a concurrent deactivation.
func TestRehashUserPassword_IsConditional(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	verified := mustGetUser(t, p, u.ID) // what the login checked the password against

	if _, err := p.SetUserPassword(u.ID, "", "bcrypt", "hash-new"); err != nil {
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
	if _, err := p.SetUserStatus(u.ID, "locked"); err != nil {
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
