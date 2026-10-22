// file: internal/server/handlers/auth_saturation_test.go
// version: 2.0.0
// guid: 145af753-2faf-4792-8784-654bedb37b46
// last-edited: 2026-10-22

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// While the aggregate login ceilings are spent the login handler delays an
// account with few failures of its own (never refusing it), and refuses one
// that has already failed SoftThreshold times, before any password work.
func TestLogin_SaturatedDelaysLowFailureAccountsAndRefusesSpentOnes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/auth.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.CreateUser("sat-user", "sat@example.test", "bcrypt", "x", []string{"viewer"}, "active")
	require.NoError(t, err)

	h := handlers.NewAuthHandler(store, true)
	var delays []time.Duration
	h.SetFailureDelay(func(d time.Duration) { delays = append(delays, d) })

	login := func(user string, saturated bool, remote string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body, _ := json.Marshal(map[string]string{"username": user, "password": "wrong"})
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.RemoteAddr = remote
		if saturated {
			absauth.MarkLoginSaturated(c)
		}
		h.Login(c)
		return w.Code
	}

	// Not saturated: no delay for the first failures.
	require.Equal(t, http.StatusUnauthorized, login("sat-user", false, "192.0.2.1:1"))
	require.Empty(t, delays)

	// Saturated, account has 1 failure: delayed by the policy, not refused.
	require.Equal(t, http.StatusUnauthorized, login("sat-user", true, "192.0.2.2:1"))
	want, refuse := absauth.SaturationPolicy(1)
	require.False(t, refuse)
	require.Equal(t, []time.Duration{want}, delays)

	// An unknown username is delayed like an untouched account.
	delays = nil
	require.Equal(t, http.StatusUnauthorized, login("nobody-here", true, "192.0.2.3:1"))
	base, _ := absauth.SaturationPolicy(0)
	require.Equal(t, []time.Duration{base}, delays)

	// Take the account to SoftThreshold failures, then it is refused while saturated.
	for i := 0; i < absauth.SoftThreshold; i++ {
		login("sat-user", false, "198.51.100.1:1")
	}
	delays = nil
	require.Equal(t, http.StatusTooManyRequests, login("sat-user", true, "192.0.2.4:1"))
	require.Empty(t, delays, "a refusal is not delayed first")
	// Not saturated, the same account is still only soft-delayed, never refused.
	require.Equal(t, http.StatusUnauthorized, login("sat-user", false, "192.0.2.5:1"))
}

// S-1: while saturated the answer must not reveal whether a name exists. The
// refusal is decided by the counter for the SUBMITTED name, so a name that does
// not exist is delayed and then refused exactly like one that does, and a refusal
// is recorded on the request as a failure for the ceiling.
func TestLogin_SaturatedAnswerDoesNotRevealWhetherTheNameExists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/auth.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.CreateUser("Real-User", "real@example.test", "bcrypt", "x", []string{"viewer"}, "active")
	require.NoError(t, err)

	h := handlers.NewAuthHandler(store, true)
	var delays []time.Duration
	h.SetFailureDelay(func(d time.Duration) { delays = append(delays, d) })
	login := func(user string, saturated bool, remote string) (int, bool) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body, _ := json.Marshal(map[string]string{"username": user, "password": "wrong"})
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.RemoteAddr = remote
		if saturated {
			absauth.MarkLoginSaturated(c)
		}
		h.Login(c)
		return w.Code, absauth.LoginSaturationRefused(c)
	}
	for i := 0; i < absauth.SoftThreshold; i++ {
		for _, name := range []string{"Real-User", "ghost-user"} {
			code, _ := login(name, false, "198.51.100.1:1")
			require.Equal(t, http.StatusUnauthorized, code)
		}
		// a different source each time so the per-source budget plays no part
		login("filler", false, "198.51.100.2:1")
	}
	// ... and the submitted name is normalised (case, spaces).
	for _, name := range []string{"real-user", "  GHOST-USER "} {
		delays = nil
		code, refused := login(name, true, "192.0.2.9:1")
		require.Equal(t, http.StatusTooManyRequests, code, name)
		require.True(t, refused, "%q: a saturation refusal is recorded for the ceiling", name)
		require.Empty(t, delays, "%q: a refusal is not delayed first", name)
	}
	// A name with failures below the threshold is delayed, real or not.
	for _, name := range []string{"Real-User-2", "ghost-2"} {
		delays = nil
		code, refused := login(name, true, "192.0.2.10:1")
		require.Equal(t, http.StatusUnauthorized, code, name)
		require.False(t, refused)
		require.Len(t, delays, 1, name)
	}
}

// B-2: a successful login must not wipe the source's failure budget. A caller
// holding one valid account logging in between rounds of guesses at other
// accounts still runs out of budget.
func TestLogin_SuccessDoesNotResetTheSourceBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/auth.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	algo, hash, err := absauth.HashPassword("own-password-123")
	require.NoError(t, err)
	_, err = store.CreateUser("own-account", "own@example.test", algo, hash, []string{"viewer"}, "active")
	require.NoError(t, err)

	h := handlers.NewAuthHandler(store, true)
	h.SetFailureDelay(func(time.Duration) {})
	login := func(user, password string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body, _ := json.Marshal(map[string]string{"username": user, "password": password})
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.RemoteAddr = "192.0.2.77:1"
		h.Login(c)
		return w.Code
	}
	blockedAfter := -1
	for round := 0; round < 50 && blockedAfter < 0; round++ {
		// guesses at OTHER accounts, then a login to the attacker's own account
		for g := 0; g < 3; g++ {
			if login("some-victim", "guess") == http.StatusTooManyRequests {
				blockedAfter = round
			}
		}
		if blockedAfter < 0 {
			switch code := login("own-account", "own-password-123"); code {
			case http.StatusOK:
			case http.StatusTooManyRequests:
				blockedAfter = round // the 15th guess spent the budget: even the own account is refused from here
			default:
				t.Fatalf("round %d: own login answered %d", round, code)
			}
		}
	}
	require.GreaterOrEqual(t, blockedAfter, 0, "the source must run out of budget even though it keeps logging in")
	require.LessOrEqual(t, blockedAfter, 6, "15 guesses is the budget; a success takes back none of them")
}

// Mistyping your own password and then getting in costs nothing.
func TestLogin_SuccessTakesBackOwnMistypes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/auth.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	algo, hash, err := absauth.HashPassword("own-password-123")
	require.NoError(t, err)
	_, err = store.CreateUser("own-account", "own@example.test", algo, hash, []string{"viewer"}, "active")
	require.NoError(t, err)
	h := handlers.NewAuthHandler(store, true)
	h.SetFailureDelay(func(time.Duration) {})
	login := func(password string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body, _ := json.Marshal(map[string]string{"username": "own-account", "password": password})
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.RemoteAddr = "192.0.2.78:1"
		h.Login(c)
		return w.Code
	}
	// Ten rounds of 4 mistypes then a success never exhausts the 15-failure budget.
	for round := 0; round < 10; round++ {
		for range 4 {
			require.Equal(t, http.StatusUnauthorized, login("typo"))
		}
		require.Equal(t, http.StatusOK, login("own-password-123"), "round %d", round)
	}
}
