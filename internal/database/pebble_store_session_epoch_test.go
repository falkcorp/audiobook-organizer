// file: internal/database/pebble_store_session_epoch_test.go
// version: 1.0.0
// guid: 5cb82609-6ccf-4160-9e5e-6441a2b16a8a
// last-edited: 2026-10-30

package database

import (
	"sync"
	"testing"
	"time"
)

func mustSession(t *testing.T, p *PebbleStore, id string) *Session {
	t.Helper()
	s, err := p.GetSession(id)
	if err != nil || s == nil {
		t.Fatalf("GetSession(%s) = %v, %v", id, s, err)
	}
	return s
}

// The stamp is whatever the caller passes (the epoch of the record it verified),
// is persisted, and CredentialsCurrent compares it exactly with the user's.
func TestCreateSession_StampsTheEpochTheCallerVerified(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	s, err := p.CreateSession(u.ID, "192.0.2.1", "ua", time.Hour, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, p, s.ID).CredentialEpoch; got != 7 {
		t.Fatalf("stored stamp = %d, want 7", got)
	}
	so, err := p.CreateSessionWithOrigin(u.ID, "192.0.2.1", "ua", time.Hour, SessionOriginPassword, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, p, so.ID).CredentialEpoch; got != 3 {
		t.Fatalf("stored stamp (with origin) = %d, want 3", got)
	}
	user := &User{CredentialEpoch: 3}
	if !so.CredentialsCurrent(user) || s.CredentialsCurrent(user) {
		t.Fatal("CredentialsCurrent must be an exact match")
	}
	var nilSession *Session
	if nilSession.CredentialsCurrent(user) || so.CredentialsCurrent(nil) {
		t.Fatal("a nil session or user is never current")
	}
}

// A session created after the epoch moved, but stamped with the epoch the caller
// had verified before it (the login that raced a password change), keeps the OLD
// stamp: the store does not re-read the user and launder it.
func TestCreateSession_DoesNotReStampFromTheFreshUser(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	if _, err := p.SetUserPassword(u.ID, "", "bcrypt", "hash-new", ""); err != nil {
		t.Fatal(err)
	}
	s, err := p.CreateSession(u.ID, "192.0.2.1", "ua", time.Hour, u.CredentialEpoch) // the stale copy's epoch (0)
	if err != nil {
		t.Fatal(err)
	}
	if s.CredentialsCurrent(mustGetUser(t, p, u.ID)) {
		t.Fatal("a session stamped with the pre-change epoch must not be current after the change")
	}
}

// keepSessionID re-stamps the caller's own session in the same write as the
// password, and nothing else: another session of the same user stays stale.
func TestSetUserPassword_KeepsOnlyTheCallersSession(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	mine, _ := p.CreateSession(u.ID, "192.0.2.1", "mine", time.Hour, 0)
	other, _ := p.CreateSession(u.ID, "192.0.2.2", "other", time.Hour, 0)

	updated, err := p.SetUserPassword(u.ID, "", "bcrypt", "hash-new", mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CredentialEpoch != 1 {
		t.Fatalf("epoch = %d, want 1", updated.CredentialEpoch)
	}
	fresh := mustGetUser(t, p, u.ID)
	if !mustSession(t, p, mine.ID).CredentialsCurrent(fresh) {
		t.Error("the caller's own session must be current after their own password change")
	}
	if mustSession(t, p, other.ID).CredentialsCurrent(fresh) {
		t.Error("another session of the same user must be stale after the change")
	}
}

// The re-stamp never launders a session the old epoch had already killed, nor
// revives a revoked or expired one, nor touches another user's session.
func TestSetUserPassword_KeepSessionIsNotALaunderingPath(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	stranger, err := p.CreateUser("bruce", "bruce@example.com", "bcrypt", "hash-b", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := p.CreateSession(u.ID, "192.0.2.1", "stale", time.Hour, 0)
	if _, err := p.BumpUserCredentialEpoch(u.ID); err != nil { // stale is now one epoch behind
		t.Fatal(err)
	}
	revoked, _ := p.CreateSession(u.ID, "192.0.2.1", "revoked", time.Hour, 1)
	if err := p.RevokeSession(revoked.ID); err != nil {
		t.Fatal(err)
	}
	expired, _ := p.CreateSession(u.ID, "192.0.2.1", "expired", -time.Minute, 1)
	foreign, _ := p.CreateSession(stranger.ID, "192.0.2.3", "foreign", time.Hour, 0)

	for name, id := range map[string]string{"stale": stale.ID, "revoked": revoked.ID, "expired": expired.ID, "another user's": foreign.ID, "unknown": "no-such-session"} {
		before := mustGetUser(t, p, u.ID).CredentialEpoch
		if _, err := p.SetUserPassword(u.ID, "", "bcrypt", "hash-"+name, id); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fresh := mustGetUser(t, p, u.ID)
		if fresh.CredentialEpoch != before+1 {
			t.Fatalf("%s: epoch = %d, want %d (the password change itself must still land)", name, fresh.CredentialEpoch, before+1)
		}
		if name != "unknown" && mustSession(t, p, id).CredentialsCurrent(fresh) && id != foreign.ID {
			t.Errorf("%s session was re-stamped current", name)
		}
	}
	if got := mustSession(t, p, foreign.ID); got.CredentialEpoch != 0 {
		t.Errorf("another user's session was re-stamped to %d", got.CredentialEpoch)
	}
}

// The user row and the kept session are written by one batch (see
// mutateUserCredentials), so after the call they agree on the epoch. A reader in
// between cannot be staged from outside a Pebble batch; this pins the outcome.
func TestSetUserPassword_KeepSessionMovesWithTheUserRow(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	mine, _ := p.CreateSession(u.ID, "192.0.2.1", "mine", time.Hour, 0)
	if _, err := p.SetUserPassword(u.ID, "", "bcrypt", "hash-new", mine.ID); err != nil {
		t.Fatal(err)
	}
	// Whatever order they are read in, user and session agree.
	sess := mustSession(t, p, mine.ID)
	user := mustGetUser(t, p, u.ID)
	if sess.CredentialEpoch != user.CredentialEpoch {
		t.Fatalf("session stamp %d and user epoch %d must move together", sess.CredentialEpoch, user.CredentialEpoch)
	}
}

// RevokeSession and the keepSessionID re-stamp are both read-modify-write on the
// same sess: row. Interleaved without sessionWriteMu, the re-stamp writes back a
// copy read before the revoke and the session comes back to life. The revoke ran
// (it returned), so the row must end revoked every time.
func TestRevokeSession_IsNotLostToAConcurrentReStamp(t *testing.T) {
	p := newABSSessionStore(t)
	u := newEpochUser(t, p)
	for i := 0; i < 200; i++ {
		cur := mustGetUser(t, p, u.ID)
		s, err := p.CreateSession(u.ID, "192.0.2.1", "ua", time.Hour, cur.CredentialEpoch)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var revokeErr, setErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			revokeErr = p.RevokeSession(s.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, setErr = p.SetUserPassword(u.ID, "", "bcrypt", "hash-new", s.ID)
		}()
		close(start)
		wg.Wait()
		if revokeErr != nil || setErr != nil {
			t.Fatalf("iteration %d: revoke=%v set=%v", i, revokeErr, setErr)
		}
		if !mustSession(t, p, s.ID).Revoked {
			t.Fatalf("iteration %d: a revoked session was resurrected by the concurrent re-stamp", i)
		}
	}
}
