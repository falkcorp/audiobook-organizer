// file: internal/server/handlers/auth_saturation_test.go
// version: 1.0.0
// guid: 145af753-2faf-4792-8784-654bedb37b46
// last-edited: 2026-10-21

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
