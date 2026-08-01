// file: internal/server/auth_reset_password_test.go
// version: 1.0.0
// guid: 0e7c53a9-1d84-4b26-97f5-2a6b8fc04d13
// last-edited: 2026-08-01

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// resetPasswordRouter wires the handler WITHOUT the users.manage permission
// middleware. Authorization is enforced at the route (wire_library_routes.go)
// and covered elsewhere; these tests are about whether the token this endpoint
// returns actually works, which is the part that was broken.
func resetPasswordRouter(s *Server) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/users/:id/reset-password", s.handleResetPassword)
	r.GET("/auth/temp-login", s.consumeTempLoginToken)
	return r
}

// THE REGRESSION. The old implementation minted a database.Invite with
// RoleID: "", which CreateInvite rejects outright — so this endpoint returned
// 500 for every user, every time.
func TestResetPassword_ReturnsUsableToken(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	user, err := s.Store().CreateUser("resetme", "resetme@example.com", "bcrypt", "hash", []string{"viewer"}, "active")
	require.NoError(t, err)

	r := resetPasswordRouter(s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+user.ID+"/reset-password", nil))

	require.Equal(t, http.StatusOK, w.Code, "reset-password must not 500 (body: %s)", w.Body.String())

	var resp struct {
		Data struct {
			Token    string `json:"token"`
			LoginURL string `json:"login_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data.Token, "a reset with no token is useless to the admin")
	require.Contains(t, resp.Data.LoginURL, resp.Data.Token)
}

// Beyond "does not 500": the token must actually be redeemable. The old
// invite-based implementation could never satisfy this even with a valid
// RoleID, because ConsumeInvite creates a NEW user and refuses a token whose
// username already exists. Fixing only the 500 would have produced a token that
// dead-ends at redemption, which is a worse failure than a loud error.
func TestResetPassword_TokenRedeemsToASession(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	user, err := s.Store().CreateUser("redeemer", "redeemer@example.com", "bcrypt", "hash", []string{"viewer"}, "active")
	require.NoError(t, err)

	r := resetPasswordRouter(s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+user.ID+"/reset-password", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	redeem := httptest.NewRecorder()
	r.ServeHTTP(redeem, httptest.NewRequest(http.MethodGet, "/auth/temp-login?token="+resp.Data.Token, nil))

	require.Equal(t, http.StatusSeeOther, redeem.Code)
	require.Equal(t, "/", redeem.Header().Get("Location"),
		"a redeemable token lands on the app root; a rejected one redirects to /login with an error")

	var sessionCookie string
	for _, ck := range redeem.Result().Cookies() {
		if strings.Contains(strings.ToLower(ck.Name), "session") {
			sessionCookie = ck.Value
		}
	}
	require.NotEmpty(t, sessionCookie, "redeeming must actually establish a session")
}

// Single-use is a security property of the underlying token, and routing
// reset-password through it must not weaken that.
func TestResetPassword_TokenIsSingleUse(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	user, err := s.Store().CreateUser("onceonly", "onceonly@example.com", "bcrypt", "hash", []string{"viewer"}, "active")
	require.NoError(t, err)

	r := resetPasswordRouter(s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+user.ID+"/reset-password", nil))
	var resp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	first := httptest.NewRecorder()
	r.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/auth/temp-login?token="+resp.Data.Token, nil))
	require.Equal(t, "/", first.Header().Get("Location"))

	second := httptest.NewRecorder()
	r.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/auth/temp-login?token="+resp.Data.Token, nil))
	require.Contains(t, second.Header().Get("Location"), "invalid_or_expired_token",
		"a reset link must not be replayable")
}

// An inactive account must not be revivable through a reset link. The guard
// lives in consumeTempLoginToken; this pins that reset-password inherits it.
func TestResetPassword_InactiveUserCannotRedeem(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	user, err := s.Store().CreateUser("disabled", "disabled@example.com", "bcrypt", "hash", []string{"viewer"}, "suspended")
	require.NoError(t, err)

	r := resetPasswordRouter(s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+user.ID+"/reset-password", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	redeem := httptest.NewRecorder()
	r.ServeHTTP(redeem, httptest.NewRequest(http.MethodGet, "/auth/temp-login?token="+resp.Data.Token, nil))
	require.Contains(t, redeem.Header().Get("Location"), "account_inactive")
}

func TestResetPassword_UnknownUserIs404(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	r := resetPasswordRouter(s)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/does-not-exist/reset-password", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}
