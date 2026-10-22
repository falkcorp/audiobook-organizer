// file: internal/server/handlers/auth_login_timing_test.go
// version: 1.0.0
// guid: 2fc86d12-0fda-4c10-920b-0b83f4b2f534
// last-edited: 2026-10-22

package handlers_test

import (
	"errors"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/stretchr/testify/require"
)

func medianOf(ds []time.Duration) time.Duration {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

// S3: the response time of a failed login must not tell a caller whether the
// username exists. A name with no user row, and an account with no password
// credential (an OAuth user), spend the same argon2id verification a wrong
// password for a real account does. Before the fix the unknown name answered in
// microseconds and the real one after a full derivation.
func TestLogin_FailedLoginTimingDoesNotRevealWhetherTheNameExists(t *testing.T) {
	algo, hash, err := absauth.HashPassword("the-real-password")
	require.NoError(t, err)
	real := &database.User{ID: "u-real", Username: "real", PasswordHashAlgo: algo, PasswordHash: hash, Status: "active"}
	oauthOnly := &database.User{ID: "u-oauth", Username: "oauthy", PasswordHashAlgo: absauth.AlgoOAuth, Status: "active"}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("real").Return(real, nil).Maybe()
	store.EXPECT().GetUserByUsername("oauthy").Return(oauthOnly, nil).Maybe()
	store.EXPECT().GetUserByUsername("ghost").Return(nil, nil).Maybe()

	h := handlers.NewAuthHandler(store, true)
	h.SetFailureDelay(func(time.Duration) {})

	const samples = 5
	timeLogin := func(name string, i int) time.Duration {
		c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": name, "password": "wrong-password"})
		c.Request.RemoteAddr = "198.51.100." + string(rune('1'+i%9)) + ":4000"
		start := time.Now()
		h.Login(c)
		d := time.Since(start)
		require.Equal(t, http.StatusUnauthorized, w.Code, name)
		return d
	}
	var realD, ghostD, oauthD []time.Duration
	for i := 0; i < samples; i++ { // interleaved, so drift hits all three alike
		realD = append(realD, timeLogin("real", i))
		ghostD = append(ghostD, timeLogin("ghost", i))
		oauthD = append(oauthD, timeLogin("oauthy", i))
	}
	r, g, o := medianOf(realD), medianOf(ghostD), medianOf(oauthD)
	t.Logf("median: real=%v unknown=%v oauth-only=%v", r, g, o)
	require.GreaterOrEqual(t, int64(g), int64(r)/2, "an unknown name answered in %v, a real one in %v: timing gives the name away", g, r)
	require.GreaterOrEqual(t, int64(o), int64(r)/2, "an account without a password answered in %v, a real one in %v", o, r)
	require.LessOrEqual(t, int64(g), int64(r)*3, "and not wildly slower")
}

// N5: a store failure during the lookup is not a credential failure. It answers
// 500 and charges neither the source nor the name, so a flapping store cannot
// lock anybody out (the ABS login already behaved this way).
func TestLogin_StoreLookupErrorIsA500AndChargesNothing(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("alice").Return(nil, errors.New("pebble: closed")).Times(30)

	th := absauth.NewThrottle()
	th.SetSleep(func(time.Duration) {})
	h := handlers.NewAuthHandler(store, true)
	h.SetThrottle(th)
	for i := 0; i < 30; i++ { // past both the per-source and per-name budgets
		c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "alice", "password": "pw"})
		c.Request.RemoteAddr = "192.0.2.9:4000"
		h.Login(c)
		require.Equal(t, http.StatusInternalServerError, w.Code, "attempt %d", i)
	}
	require.Zero(t, th.AccountFailures(absauth.AccountKey("alice")), "nothing is charged to the name")
	require.False(t, th.IPBlocked("192.0.2.9"), "nothing is charged to the source")
}
