// file: internal/database/pebble_store_abssession_test.go
// version: 1.0.0
// guid: 5a3f8c21-9e04-4b7d-8c16-2f5b9d0e7a34
// last-edited: 2026-07-30

package database

import (
	"path/filepath"
	"testing"
	"time"
)

func newABSSessionStore(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sampleABSSession(id, userID string) *ABSSession {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &ABSSession{
		ID:               id,
		UserID:           userID,
		Seed:             "seed-" + id,
		Generation:       1,
		RefreshTokenHash: "hash-" + id + "-1",
		DeviceInfo:       `{"model":"quest"}`,
		UserAgent:        "AudioBooth/1.0",
		IP:               "10.0.0.5",
		CreatedAt:        now,
		LastUsedAt:       now,
		ExpiresAt:        now.Add(720 * time.Hour),
	}
}

func TestABSSession_CreateGetRoundTrip(t *testing.T) {
	store := newABSSessionStore(t)
	want := sampleABSSession("sess-a", "user-1")
	if err := store.CreateABSSession(want); err != nil {
		t.Fatalf("CreateABSSession: %v", err)
	}
	got, err := store.GetABSSession("sess-a")
	if err != nil {
		t.Fatalf("GetABSSession: %v", err)
	}
	if got == nil {
		t.Fatal("GetABSSession returned nil for a session that was just created")
	}
	if got.UserID != want.UserID || got.Seed != want.Seed || got.Generation != want.Generation {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if got.RefreshTokenHash != want.RefreshTokenHash {
		t.Fatalf("refresh hash mismatch: got %q want %q", got.RefreshTokenHash, want.RefreshTokenHash)
	}
	if got.DeviceInfo != want.DeviceInfo || got.UserAgent != want.UserAgent || got.IP != want.IP {
		t.Fatalf("device metadata lost: %+v", got)
	}
}

func TestABSSession_GetMissingReturnsNilNoError(t *testing.T) {
	store := newABSSessionStore(t)
	got, err := store.GetABSSession("nope")
	if err != nil {
		t.Fatalf("GetABSSession on missing id: unexpected error %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing session, got %+v", got)
	}
}

func TestABSSession_LookupByRefreshHash(t *testing.T) {
	store := newABSSessionStore(t)
	s := sampleABSSession("sess-b", "user-1")
	if err := store.CreateABSSession(s); err != nil {
		t.Fatalf("CreateABSSession: %v", err)
	}
	got, err := store.GetABSSessionByRefreshHash("hash-sess-b-1")
	if err != nil {
		t.Fatalf("GetABSSessionByRefreshHash: %v", err)
	}
	if got == nil || got.ID != "sess-b" {
		t.Fatalf("expected sess-b, got %+v", got)
	}

	// Rotation: the previous hash must remain resolvable (grace window), and the
	// new current hash must resolve too.
	s.PrevRefreshTokenHash = s.RefreshTokenHash
	s.Generation = 2
	s.RefreshTokenHash = "hash-sess-b-2"
	s.GraceUntil = time.Now().Add(10 * time.Minute)
	if err := store.UpdateABSSession(s); err != nil {
		t.Fatalf("UpdateABSSession: %v", err)
	}
	for _, h := range []string{"hash-sess-b-1", "hash-sess-b-2"} {
		got, err := store.GetABSSessionByRefreshHash(h)
		if err != nil {
			t.Fatalf("GetABSSessionByRefreshHash(%s): %v", h, err)
		}
		if got == nil || got.ID != "sess-b" {
			t.Fatalf("hash %s should resolve to sess-b, got %+v", h, got)
		}
	}
}

func TestABSSession_StaleTokenIndexIsNotResolvable(t *testing.T) {
	store := newABSSessionStore(t)
	s := sampleABSSession("sess-c", "user-1")
	if err := store.CreateABSSession(s); err != nil {
		t.Fatalf("CreateABSSession: %v", err)
	}
	// Two rotations: generation 1's hash is no longer current OR previous, so it
	// must stop resolving even though its index entry was written once.
	s.PrevRefreshTokenHash = s.RefreshTokenHash
	s.RefreshTokenHash = "hash-sess-c-2"
	s.Generation = 2
	if err := store.UpdateABSSession(s); err != nil {
		t.Fatalf("UpdateABSSession: %v", err)
	}
	s.PrevRefreshTokenHash = s.RefreshTokenHash
	s.RefreshTokenHash = "hash-sess-c-3"
	s.Generation = 3
	if err := store.UpdateABSSession(s); err != nil {
		t.Fatalf("UpdateABSSession: %v", err)
	}
	got, err := store.GetABSSessionByRefreshHash("hash-sess-c-1")
	if err != nil {
		t.Fatalf("GetABSSessionByRefreshHash: %v", err)
	}
	if got != nil {
		t.Fatalf("a hash that is neither current nor previous must not resolve, got %+v", got)
	}
}

func TestABSSession_ListForUserIsScopedToThatUser(t *testing.T) {
	store := newABSSessionStore(t)
	for _, tc := range []struct{ id, user string }{
		{"s1", "user-1"}, {"s2", "user-1"}, {"s3", "user-2"},
	} {
		if err := store.CreateABSSession(sampleABSSession(tc.id, tc.user)); err != nil {
			t.Fatalf("CreateABSSession(%s): %v", tc.id, err)
		}
	}
	got, err := store.ListABSSessionsForUser("user-1")
	if err != nil {
		t.Fatalf("ListABSSessionsForUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions for user-1, got %d (%+v)", len(got), got)
	}
	for _, s := range got {
		if s.UserID != "user-1" {
			t.Fatalf("session %s leaked from user %s", s.ID, s.UserID)
		}
	}
}

func TestABSSession_RevokeMarksRevokedAndKillsTokenLookup(t *testing.T) {
	store := newABSSessionStore(t)
	s := sampleABSSession("sess-d", "user-1")
	if err := store.CreateABSSession(s); err != nil {
		t.Fatalf("CreateABSSession: %v", err)
	}
	if err := store.RevokeABSSession("sess-d"); err != nil {
		t.Fatalf("RevokeABSSession: %v", err)
	}
	got, err := store.GetABSSession("sess-d")
	if err != nil {
		t.Fatalf("GetABSSession: %v", err)
	}
	if got == nil || !got.Revoked {
		t.Fatalf("expected revoked session, got %+v", got)
	}
}

func TestABSSession_RevokeAllForUser(t *testing.T) {
	store := newABSSessionStore(t)
	for _, tc := range []struct{ id, user string }{
		{"r1", "user-1"}, {"r2", "user-1"}, {"r3", "user-2"},
	} {
		if err := store.CreateABSSession(sampleABSSession(tc.id, tc.user)); err != nil {
			t.Fatalf("CreateABSSession(%s): %v", tc.id, err)
		}
	}
	n, err := store.RevokeAllABSSessionsForUser("user-1")
	if err != nil {
		t.Fatalf("RevokeAllABSSessionsForUser: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 revocations, got %d", n)
	}
	other, err := store.GetABSSession("r3")
	if err != nil {
		t.Fatalf("GetABSSession(r3): %v", err)
	}
	if other == nil || other.Revoked {
		t.Fatalf("another user's session must not be revoked: %+v", other)
	}
}

func TestABSSession_DeleteExpiredRemovesRecordAndIndexes(t *testing.T) {
	store := newABSSessionStore(t)
	live := sampleABSSession("live", "user-1")
	dead := sampleABSSession("dead", "user-1")
	dead.ExpiresAt = time.Now().Add(-time.Hour)
	for _, s := range []*ABSSession{live, dead} {
		if err := store.CreateABSSession(s); err != nil {
			t.Fatalf("CreateABSSession(%s): %v", s.ID, err)
		}
	}
	n, err := store.DeleteExpiredABSSessions(time.Now())
	if err != nil {
		t.Fatalf("DeleteExpiredABSSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 deletion, got %d", n)
	}
	if got, _ := store.GetABSSession("dead"); got != nil {
		t.Fatalf("expired session still present: %+v", got)
	}
	if got, _ := store.GetABSSessionByRefreshHash("hash-dead-1"); got != nil {
		t.Fatalf("expired session's token index still resolves: %+v", got)
	}
	if got, _ := store.GetABSSession("live"); got == nil {
		t.Fatal("live session was deleted")
	}
	remaining, err := store.ListABSSessionsForUser("user-1")
	if err != nil {
		t.Fatalf("ListABSSessionsForUser: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "live" {
		t.Fatalf("per-user index not cleaned up: %+v", remaining)
	}
}

// TestABSSession_DoesNotCollideWithBrowserSessions guards the additive-auth
// constraint: the new abs_sess: keyspace must be invisible to the existing
// sess: browser-session keyspace and vice versa.
func TestABSSession_DoesNotCollideWithBrowserSessions(t *testing.T) {
	store := newABSSessionStore(t)
	browser, err := store.CreateSession("user-1", "10.0.0.1", "curl", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.CreateABSSession(sampleABSSession(browser.ID, "user-1")); err != nil {
		t.Fatalf("CreateABSSession with a colliding id: %v", err)
	}
	got, err := store.GetSession(browser.ID)
	if err != nil || got == nil {
		t.Fatalf("browser session lost: %v %+v", err, got)
	}
	if got.IP != "10.0.0.1" || got.UserAgent != "curl" {
		t.Fatalf("browser session overwritten by ABS session: %+v", got)
	}
	sessions, err := store.ListUserSessions("user-1")
	if err != nil {
		t.Fatalf("ListUserSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ABS sessions leaked into ListUserSessions: %+v", sessions)
	}
}
