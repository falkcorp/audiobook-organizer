// file: internal/database/session_origin_test.go
// version: 1.0.0
// guid: 3c8e1a57-4b92-4d06-a7f3-6e2b9d0c5f18
// last-edited: 2026-10-06

package database

import (
	"testing"
	"time"
)

// TestSessionOrigin_RoundTrip: the origin a session was issued with is
// stored and read back, and only a password or OAuth sign-in is an
// interactive login; CreateSession (no origin) never is.
func TestSessionOrigin_RoundTrip(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	for _, tc := range []struct {
		origin      string
		interactive bool
	}{
		{SessionOriginPassword, true},
		{SessionOriginOAuth, true},
		{"temp_login", false},
		{"", false},
	} {
		s, err := store.CreateSessionWithOrigin("user-1", "192.0.2.1", "ua", time.Hour, tc.origin)
		if err != nil {
			t.Fatalf("create %q: %v", tc.origin, err)
		}
		got, err := store.GetSession(s.ID)
		if err != nil || got == nil {
			t.Fatalf("read %q: %v", tc.origin, err)
		}
		if got.Origin != tc.origin || got.InteractiveLogin() != tc.interactive {
			t.Errorf("origin %q: read origin %q interactive %v, want interactive %v", tc.origin, got.Origin, got.InteractiveLogin(), tc.interactive)
		}
	}
	plain, err := store.CreateSession("user-1", "192.0.2.1", "ua", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetSession(plain.ID); got == nil || got.InteractiveLogin() {
		t.Errorf("a session created without an origin must not be an interactive login: %+v", got)
	}
}
