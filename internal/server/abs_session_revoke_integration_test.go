// file: internal/server/abs_session_revoke_integration_test.go
// version: 1.0.0
// guid: d943df61-c54b-4621-8c73-dd144fc38af8
// last-edited: 2026-10-19

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// absPhone is one signed-in ABS client against the test server's REAL store,
// resolved through the same AsCapability chain wireABSRoutes uses.
type absPhone struct {
	router   *gin.Engine
	resolver *servermiddleware.ABSIdentityResolver
	access   string
	refresh  string
}

func signInABSPhone(t *testing.T, s *Server, username, password string) *absPhone {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: "0123456789abcdef0123456789abcdef", AuthModes: "jwt"})
	require.NoError(t, err)
	absStore, ok := database.AsCapability[abshandler.Store](s.Ops())
	require.True(t, ok)
	idStore, ok := database.AsCapability[servermiddleware.ABSIdentityStore](s.Ops())
	require.True(t, ok)
	resolver := servermiddleware.NewABSIdentityResolver(cfg, nil, nil, idStore)
	h, err := abshandler.New(abshandler.Options{Config: cfg, Store: absStore, Resolver: resolver})
	require.NoError(t, err)
	h.SetSleep(func(time.Duration) {})
	r := gin.New()
	h.Register(r)

	p := &absPhone{router: r, resolver: resolver}
	raw, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-return-tokens", "true")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		User struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	p.access, p.refresh = body.User.AccessToken, body.User.RefreshToken
	require.NotEmpty(t, p.access)
	require.NotEmpty(t, p.refresh)
	return p
}

// bearerWorks reports whether the phone's access token still authenticates.
func (p *absPhone) bearerWorks(t *testing.T) bool {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	c.Request.Header.Set("Authorization", "Bearer "+p.access)
	id, e := p.resolver.ResolveBearer(c)
	return e == nil && id != nil
}

// refreshCode is the status of POST /auth/refresh with the phone's refresh token.
func (p *absPhone) refreshCode(t *testing.T) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("x-refresh-token", p.refresh)
	w := httptest.NewRecorder()
	p.router.ServeHTTP(w, req)
	return w.Code
}

func (p *absPhone) requireSignedOut(t *testing.T, why string) {
	t.Helper()
	require.False(t, p.bearerWorks(t), "%s: the ABS access token must stop working", why)
	require.Equal(t, http.StatusUnauthorized, p.refreshCode(t), "%s: the ABS refresh token must be dead", why)
}

func createPasswordUser(t *testing.T, s *Server, username, password string) *database.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	u, err := s.storeForWiring().CreateUser(username, username+"@example.com", "bcrypt", string(hash), []string{"viewer"}, "active")
	require.NoError(t, err)
	return u
}

func handlerCtx(method, path string, body any, caller *database.User, perms ...auth.Permission) (*gin.Context, *httptest.ResponseRecorder) {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	if caller != nil {
		c.Set("auth_user", caller)
		ctx := auth.WithUser(c.Request.Context(), caller)
		ctx = auth.WithPermissions(ctx, perms)
		ctx = auth.WithMethod(ctx, auth.MethodSession)
		c.Request = c.Request.WithContext(ctx)
	}
	return c, w
}

// TestAccountSecurityPathsRevokeABSSessions: each account-security action signs
// the user out of every ABS client, end to end against the real store. With
// sliding expiry and no absolute cap, a session that survived one of these
// would be good indefinitely.
func TestAccountSecurityPathsRevokeABSSessions(t *testing.T) {
	const pw = "pw-pw-pw-pw-1"
	cases := map[string]func(t *testing.T, s *Server, u *database.User){
		"self password change": func(t *testing.T, s *Server, u *database.User) {
			// ABS password login rehashes bcrypt to argon2id, and ChangePassword
			// verifies the current password with bcrypt only, so after an ABS
			// sign-in the user can no longer change their own password (a
			// separate, pre-existing bug, filed in todo.d). Put a bcrypt hash
			// back so this case exercises the revoke rather than that bug.
			fresh, err := s.storeForWiring().GetUserByID(u.ID)
			require.NoError(t, err)
			hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
			require.NoError(t, err)
			fresh.PasswordHash, fresh.PasswordHashAlgo = string(hash), "bcrypt"
			require.NoError(t, s.storeForWiring().UpdateUser(fresh))
			c, w := handlerCtx(http.MethodPut, "/api/v1/auth/me/password",
				map[string]any{"current_password": pw, "new_password": "brand-new-pw"}, u)
			handlers.NewAuthHandler(s.storeForWiring(), true).ChangePassword(c)
			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		},
		"admin password reset": func(t *testing.T, s *Server, u *database.User) {
			admin := &database.User{ID: "admin-1", Username: "admin", Status: "active"}
			c, w := handlerCtx(http.MethodPut, "/api/v1/auth/me/password",
				map[string]any{"user_id": u.ID, "new_password": "brand-new-pw"}, admin, auth.PermUsersManage)
			handlers.NewAuthHandler(s.storeForWiring(), true).ChangePassword(c)
			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		},
		"reset-password link": func(t *testing.T, s *Server, u *database.User) {
			w := httptest.NewRecorder()
			resetPasswordRouter(s).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/reset-password", nil))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		},
		"deactivate": func(t *testing.T, s *Server, u *database.User) {
			c, w := handlerCtx(http.MethodPost, "/api/v1/users/"+u.ID+"/deactivate", nil, nil)
			c.Params = gin.Params{{Key: "id", Value: u.ID}}
			handlers.NewUserHandler(s.storeForWiring()).DeactivateUser(c)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		},
	}
	for name, act := range cases {
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			u := createPasswordUser(t, s, "phoneuser", pw)
			phoneA := signInABSPhone(t, s, "phoneuser", pw)
			phoneB := signInABSPhone(t, s, "phoneuser", pw)
			require.True(t, phoneA.bearerWorks(t), "precondition: signed in")

			act(t, s, u)

			phoneA.requireSignedOut(t, name)
			phoneB.requireSignedOut(t, name)
			lister, ok := database.AsCapability[interface {
				ListABSSessionsForUser(string) ([]database.ABSSession, error)
			}](s.Ops())
			require.True(t, ok)
			sessions, err := lister.ListABSSessionsForUser(u.ID)
			require.NoError(t, err)
			require.Len(t, sessions, 2)
			for _, sess := range sessions {
				require.True(t, sess.Revoked, "%s: session %s must be revoked", name, sess.ID)
			}
		})
	}
}

// TestReactivationDoesNotReviveABSSessions: unlocking an account must not bring
// back the sessions it had before it was locked.
func TestReactivationDoesNotReviveABSSessions(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	const pw = "pw-pw-pw-pw-1"
	u := createPasswordUser(t, s, "phoneuser", pw)
	phone := signInABSPhone(t, s, "phoneuser", pw)
	uh := handlers.NewUserHandler(s.storeForWiring())

	c, w := handlerCtx(http.MethodPost, "/deactivate", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: u.ID}}
	uh.DeactivateUser(c)
	require.Equal(t, http.StatusOK, w.Code)

	c, w = handlerCtx(http.MethodPost, "/reactivate", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: u.ID}}
	uh.ReactivateUser(c)
	require.Equal(t, http.StatusOK, w.Code)

	phone.requireSignedOut(t, "after reactivation")
	// The account itself works again: a fresh sign-in succeeds.
	fresh := signInABSPhone(t, s, "phoneuser", pw)
	require.True(t, fresh.bearerWorks(t))
}

// failingABSRevokeStore is the real store with an ABS keyspace whose revoke
// always fails, so the reset-link path's fail-closed branch can be exercised.
type failingABSRevokeStore struct {
	database.Store
	calls int
}

func (f *failingABSRevokeStore) RevokeAllABSSessionsForUser(string) (int, error) {
	f.calls++
	return 0, errors.New("disk full")
}

// An admin reset that cannot sign the user's phones out must not hand out a
// reset link: the admin would believe the account was secured when the old
// sessions are still valid.
func TestResetPassword_RevokeFailureIssuesNoLink(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()

	user, err := s.storeForWiring().CreateUser("resetfail", "resetfail@example.com", "bcrypt", "hash", []string{"viewer"}, "active")
	require.NoError(t, err)

	orig := s.store
	failing := &failingABSRevokeStore{Store: orig}
	s.store = failing
	defer func() { s.store = orig }()

	w := httptest.NewRecorder()
	resetPasswordRouter(s).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+user.ID+"/reset-password", nil))

	require.Equal(t, http.StatusInternalServerError, w.Code, "body: %s", w.Body.String())
	require.Equal(t, 1, failing.calls, "the reset must try to revoke the user's ABS sessions")
	require.NotContains(t, w.Body.String(), "token", "no reset link may be issued when the revoke failed")
}
