// file: internal/books/identity_invite_session_boot_test.go
// version: 0.1.0
// guid: 7a1c9e35-82d4-4b6f-a0e3-5f2d8c41b967
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// F4-08 B10ttl: session lifetimes under core identity are HEAD's. An accepted
// invite opens a 30-day session (origin/main handlers/user.go:380); a remember-me
// sign-in 7 days (handlers/auth.go rememberMeSessionTTL) and a plain one 24 hours.
// Core gave the first the remember-me lifetime, which narrowed it to 7 days;
// identityAuthOptions sets Options.InviteSessionTTL so it stays 30. The lifetimes
// are read from the sessions a real boot opens, as the response says and as the
// cookie's own lifetime says (the browser drops the cookie at MaxAge). The clock
// is the real one, so the check allows a minute of drift.

// sessionLifetime is how long the session rec opened lives: the
// response's expires_at, and the cookie's Max-Age, which must agree to the second.
func sessionLifetime(t *testing.T, rec *httptest.ResponseRecorder, issuedAfter time.Time) time.Duration {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.False(t, body.ExpiresAt.IsZero(), rec.Body.String())
	ck := sessionCookie(t, rec)
	life := time.Until(body.ExpiresAt) + time.Since(issuedAfter)
	require.InDelta(t, life.Seconds(), float64(ck.MaxAge), 65, "the cookie outlives or undercuts the session it carries")
	return life
}

func TestIdentityBoot_SessionLifetimesAreHEADs(t *testing.T) {
	r := bootAccess(t, nil, nil)
	admin := newCoreAdmin(t, r)

	const username = "dick-grayson"
	rec := ownerReq(t, r, http.MethodPost, "/api/core/v1/users/invites", map[string]string{"username": username, "role": "viewer"}, admin.cookie, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Link string `json:"link"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created), rec.Body.String())
	_, token, ok := strings.Cut(created.Link, "#token=")
	require.True(t, ok, "invite link has no token: %q", created.Link)

	near := func(t *testing.T, got, want time.Duration) {
		t.Helper()
		require.InDelta(t, want.Seconds(), got.Seconds(), 60, "a session lives %v, HEAD's is %v", got, want)
	}

	t.Run("an accepted invite opens a 30-day session", func(t *testing.T) {
		start := time.Now()
		acc := ownerReq(t, r, http.MethodPost, "/api/core/v1/auth/invites/accept", map[string]string{"token": token, "password": ownerPass}, nil, "")
		near(t, sessionLifetime(t, acc, start), 30*24*time.Hour)
	})
	t.Run("a remember-me sign-in is still 7 days", func(t *testing.T) {
		start := time.Now()
		in := ownerReq(t, r, http.MethodPost, "/api/core/v1/auth/login", map[string]any{"username": username, "password": ownerPass, "remember": true}, nil, "")
		near(t, sessionLifetime(t, in, start), 7*24*time.Hour)
	})
	t.Run("a plain sign-in is still 24 hours", func(t *testing.T) {
		start := time.Now()
		in := ownerReq(t, r, http.MethodPost, "/api/core/v1/auth/login", map[string]any{"username": username, "password": ownerPass}, nil, "")
		near(t, sessionLifetime(t, in, start), 24*time.Hour)
	})
}
