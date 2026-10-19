// file: internal/database/pebble_store_auth_epoch_test.go
// version: 1.2.0
// guid: da259f0d-609e-4d78-8f4f-d1d0071a7291
// last-edited: 2026-10-19

package database

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
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

// interleave arms userWriteAfterReadHook so that the FIRST user write to reach
// its after-read point runs competitor in another goroutine and waits up to one
// second for it before writing. With userWriteMu held the competitor blocks on
// the lock, the wait times out and the first write lands first; without the
// lock the competitor finishes inside the window between the first write's read
// and its write. Only the first writer waits, so the outcome does not depend on
// scheduling. The returned func waits for the competitor.
func interleave(t *testing.T, competitor func()) func() {
	t.Helper()
	var armed atomic.Bool
	armed.Store(true)
	done := make(chan struct{})
	hook := func() {
		if !armed.CompareAndSwap(true, false) {
			return
		}
		go func() { defer close(done); competitor() }()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	userWriteAfterReadHook.Store(&hook)
	t.Cleanup(func() { userWriteAfterReadHook.Store(nil) })
	return func() { <-done }
}

// Two admin resets racing from the same starting row both land, at DISTINCT
// epochs, and a session stamped with any epoch before the last is refused. The
// second change is forced into the first one's read-to-write window.
func TestSetUserPassword_RacingChangesEndAtDistinctEpochs(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	var second *User
	var secondErr error
	wait := interleave(t, func() { second, secondErr = p.SetUserPassword(u.ID, "", "bcrypt", "hash-b") })
	first, err := p.SetUserPassword(u.ID, "", "bcrypt", "hash-a")
	wait()
	if err != nil || secondErr != nil {
		t.Fatalf("errors: %v / %v", err, secondErr)
	}
	if first.CredentialEpoch == second.CredentialEpoch {
		t.Fatalf("both racing changes ended at epoch %d; each must bump", first.CredentialEpoch)
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
// two from the same verified hash, the one forced into the other's window loses.
func TestSetUserPassword_SelfServiceIsCompareAndSwap(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	var secondErr error
	wait := interleave(t, func() { _, secondErr = p.SetUserPassword(u.ID, "hash-old", "bcrypt", "hash-b") })
	_, firstErr := p.SetUserPassword(u.ID, "hash-old", "bcrypt", "hash-a")
	wait()
	if firstErr != nil {
		t.Fatalf("the first change must land: %v", firstErr)
	}
	if !errors.Is(secondErr, ErrUserStale) {
		t.Fatalf("the second change verified a password that is gone: got %v, want ErrUserStale", secondErr)
	}
	got := mustGetUser(t, p, u.ID)
	if got.PasswordHash != "hash-a" || got.CredentialEpoch != 1 {
		t.Fatalf("stored %q at epoch %d, want hash-a at 1", got.PasswordHash, got.CredentialEpoch)
	}
}

// An ordinary UpdateUser cannot revert a status change that lands between its
// re-read and its write.
func TestUpdateUser_CannotRevertAConcurrentStatusChange(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	var lockErr error
	wait := interleave(t, func() { _, lockErr = p.SetUserStatus(u.ID, "locked") })
	edit := mustGetUser(t, p, u.ID)
	edit.Email = "edit@example.com"
	if err := p.UpdateUser(edit); err != nil {
		t.Fatalf("the edit read a current row and must land: %v", err)
	}
	wait()
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	got := mustGetUser(t, p, u.ID)
	if got.Status != "locked" || got.CredentialEpoch != 1 {
		t.Fatalf("after the race: status %q epoch %d; the deactivation was reverted", got.Status, got.CredentialEpoch)
	}
}

// The login rehash cannot overwrite a password change that lands between its
// re-read and its write.
func TestRehashUserPassword_CannotOverwriteAConcurrentChange(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	var changeErr error
	wait := interleave(t, func() { _, changeErr = p.SetUserPassword(u.ID, "", "bcrypt", "hash-new") })
	if _, err := p.RehashUserPassword(u.ID, "hash-old", "argon2id", "argon-of-old"); err != nil {
		t.Fatal(err)
	}
	wait()
	if changeErr != nil {
		t.Fatal(changeErr)
	}
	got := mustGetUser(t, p, u.ID)
	if got.PasswordHash != "hash-new" || got.CredentialEpoch != 1 {
		t.Fatalf("after the race: hash %q epoch %d; the rehash overwrote the new password", got.PasswordHash, got.CredentialEpoch)
	}
}

// UpdateUser never creates a row: users come from CreateUser, which also writes
// the username and email indexes.
func TestUpdateUser_UnknownUserIsNotFound(t *testing.T) {
	p := newABSSessionStore(t)
	if err := p.UpdateUser(&User{ID: "ghost", Username: "ghost"}); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("UpdateUser(unknown) = %v, want ErrUserNotFound", err)
	}
	if got, _ := p.GetUserByID("ghost"); got != nil {
		t.Fatalf("UpdateUser created an unindexed row: %+v", got)
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
