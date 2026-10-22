// file: internal/server/handlers/auth_test.go
// version: 1.7.0
// guid: d5e6f7a8-b9c0-1234-5678-90abcdef0123
// last-edited: 2026-10-22

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// hashOf matches a stored argon2id hash of plain, so a handler test proves the
// handler hashed the submitted password with the shared policy.
func hashOf(plain string) any {
	return mock.MatchedBy(func(hash string) bool {
		ok, _ := absauth.VerifyPassword(absauth.AlgoArgon2id, hash, plain)
		return ok
	})
}

func newAuthCtx(method, path string, body any) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c, w
}

// setAuthUser makes c a request from user's own signed-in session: the user
// on the gin context and MethodSession on the request context, as RequireAuth
// records them. Tests of another auth method override the method (asCaller).
func setAuthUser(c *gin.Context, user *database.User) {
	c.Set("auth_user", user)
	if c.Request != nil {
		c.Request = c.Request.WithContext(auth.WithMethod(c.Request.Context(), auth.MethodSession))
	}
}

func setAuthSession(c *gin.Context, session *database.Session) {
	c.Set("auth_session", session)
}

func testBcryptHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(hash)
}

func TestAuthHandler_GetStatus_NoUsers(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().CountUsers().Return(0, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("GET", "/auth/status", nil)
	h.GetStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, false, data["has_users"])
	assert.Equal(t, true, data["bootstrap_ready"])
	assert.Equal(t, false, data["requires_auth"])
}

func TestAuthHandler_GetStatus_HasUsers(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().CountUsers().Return(3, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("GET", "/auth/status", nil)
	h.GetStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, true, data["has_users"])
	assert.Equal(t, true, data["requires_auth"])
	assert.Equal(t, false, data["bootstrap_ready"])
}

func TestAuthHandler_GetStatus_AuthDisabled(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().CountUsers().Return(5, nil)

	h := handlers.NewAuthHandler(store, false) // enableAuth=false
	c, w := newAuthCtx("GET", "/auth/status", nil)
	h.GetStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, false, data["requires_auth"]) // auth disabled → no auth required
}

func TestAuthHandler_Login_Success(t *testing.T) {
	hash := testBcryptHash(t, "password123")
	user := &database.User{
		ID:           "user-1",
		Username:     "alice",
		PasswordHash: hash,
		Roles:        []string{"user"},
		Status:       "active",
		CreatedAt:    time.Now(),
	}
	session := &database.Session{
		ID:        "sess-abc",
		UserID:    "user-1",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("alice").Return(user, nil)
	// Login reports the user's effective permissions, resolved from the roles.
	store.EXPECT().GetRoleByID("user").Return(nil, nil)
	// A password sign-in records its origin: the session is an interactive login.
	store.EXPECT().CreateSessionWithOrigin("user-1", mock.Anything, mock.Anything, mock.Anything, database.SessionOriginPassword).Return(session, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{
		"username": "alice",
		"password": "password123",
	})
	h.Login(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.NotNil(t, data["user"])
	// Session token must NOT be exposed in the body (HttpOnly cookie only).
	assert.Nil(t, data["session"])
	assert.NotNil(t, data["expires_at"])
}

func TestAuthHandler_Login_WrongPassword(t *testing.T) {
	hash := testBcryptHash(t, "correctpassword")
	user := &database.User{ID: "user-1", Username: "alice", PasswordHash: hash}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("alice").Return(user, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{
		"username": "alice",
		"password": "wrongpassword",
	})
	h.Login(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuthHandler_Login_UserNotFound(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("nobody").Return(nil, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{
		"username": "nobody",
		"password": "anything",
	})
	h.Login(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// HIGH-3: a single source IP that exhausts its failed-login budget is throttled
// with 429. Uses unknown usernames so the throttle trips without any per-account
// soft delay (sleep-free, deterministic). The throttle check precedes the store
// lookup, so the over-budget request makes no GetUserByUsername call.
func TestAuthHandler_Login_PerIPThrottle(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("ghost").Return(nil, nil).Times(15)

	h := handlers.NewAuthHandler(store, true)
	// An unknown name is slowed like a real one once it has failed 5 times; keep
	// the test fast.
	h.SetFailureDelay(func(time.Duration) {})

	for i := range 15 {
		c, w := newAuthCtx("POST", "/auth/login", map[string]any{
			"username": "ghost", "password": "wrong",
		})
		h.Login(c)
		assert.Equal(t, http.StatusUnauthorized, w.Code, "attempt %d should be 401", i+1)
	}

	// 16th request from the same IP is over budget → 429, no store call.
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{
		"username": "ghost", "password": "wrong",
	})
	h.Login(c)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

// HIGH-3: the per-account counter is soft — it slows failed attempts but never
// hard-locks, so the legitimate user with the correct password still gets in
// after crossing the soft threshold (a third party can no longer lock them out).
func TestAuthHandler_Login_SoftCounterDoesNotLock(t *testing.T) {
	hash := testBcryptHash(t, "password123")
	user := &database.User{ID: "user-1", Username: "alice", PasswordHash: hash}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("alice").Return(user, nil).Times(8) // 7 wrong + 1 right
	// A password sign-in records its origin: the session is an interactive login.
	store.EXPECT().CreateSessionWithOrigin("user-1", mock.Anything, mock.Anything, mock.Anything, database.SessionOriginPassword).
		Return(&database.Session{ID: "sess-1", ExpiresAt: time.Now().Add(time.Hour)}, nil)

	h := handlers.NewAuthHandler(store, true)
	h.SetFailureDelay(func(time.Duration) {}) // no-op: keeps test fast and deterministic

	// 7 wrong attempts (past the soft threshold of 5) — adds small delays, no lock.
	for range 7 {
		c, _ := newAuthCtx("POST", "/auth/login", map[string]any{
			"username": "alice", "password": "wrong",
		})
		h.Login(c)
	}

	// Correct password still succeeds — the account is not hard-locked.
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{
		"username": "alice", "password": "password123",
	})
	h.Login(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

// HIGH-3: throttling is keyed on the source IP, so an attacker hammering from
// one IP cannot lock out a victim logging in from a different IP.
func TestAuthHandler_Login_DifferentIPNotThrottled(t *testing.T) {
	hash := testBcryptHash(t, "password123")
	user := &database.User{ID: "user-1", Username: "alice", PasswordHash: hash}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("ghost").Return(nil, nil).Times(15) // attacker probes
	store.EXPECT().GetUserByUsername("alice").Return(user, nil).Times(1) // victim
	// A password sign-in records its origin: the session is an interactive login.
	store.EXPECT().CreateSessionWithOrigin("user-1", mock.Anything, mock.Anything, mock.Anything, database.SessionOriginPassword).
		Return(&database.Session{ID: "sess-1", ExpiresAt: time.Now().Add(time.Hour)}, nil)

	h := handlers.NewAuthHandler(store, true)

	// Attacker burns the budget from one IP (unknown user → sleep-free).
	for range 15 {
		c, _ := newAuthCtx("POST", "/auth/login", map[string]any{
			"username": "ghost", "password": "wrong",
		})
		c.Request.RemoteAddr = "203.0.113.9:40000"
		h.Login(c)
	}

	// Victim logs in from a different IP with the correct password — not throttled.
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{
		"username": "alice", "password": "password123",
	})
	c.Request.RemoteAddr = "198.51.100.7:50000"
	h.Login(c)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuthHandler_SetupInitialAdmin_AlreadyExists(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().CountUsers().Return(1, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/setup", map[string]any{
		"username": "admin", "password": "password123",
	})
	h.SetupInitialAdmin(c)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestAuthHandler_SetupInitialAdmin_Success(t *testing.T) {
	created := &database.User{
		ID:        "user-1",
		Username:  "admin",
		Email:     "admin@local",
		Roles:     []string{"admin"},
		Status:    "active",
		CreatedAt: time.Now(),
	}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().CountUsers().Return(0, nil)
	store.EXPECT().CreateUser("admin", "admin@local", absauth.AlgoArgon2id, hashOf("password123"), []string{"admin"}, "active").Return(created, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/setup", map[string]any{
		"username": "admin", "password": "password123",
	})
	h.SetupInitialAdmin(c)

	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAuthHandler_Logout_RevokesSession(t *testing.T) {
	session := &database.Session{ID: "sess-1", UserID: "user-1"}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().RevokeSession("sess-1").Return(nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/logout", nil)
	setAuthSession(c, session)
	h.Logout(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuthHandler_Me_NotAuthenticated(t *testing.T) {
	store := handlersmocks.NewMockAuthStore(t)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("GET", "/auth/me", nil)
	// no user set in context
	h.Me(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuthHandler_Me_Authenticated(t *testing.T) {
	user := &database.User{
		ID:       "user-1",
		Username: "alice",
		Roles:    []string{"user"},
		Status:   "active",
	}
	store := handlersmocks.NewMockAuthStore(t)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("GET", "/auth/me", nil)
	setAuthUser(c, user)
	h.Me(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	userField := data["user"].(map[string]any)
	assert.Equal(t, "alice", userField["username"])
}

// ── permissions on login and /auth/me (07-F5a) ──────────────────────────────

// authUserPermissions decodes data.user.permissions; present reports whether the
// key was in the body at all.
func authUserPermissions(t *testing.T, w *httptest.ResponseRecorder) (perms []string, present bool, raw string) {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Data struct {
			User map[string]json.RawMessage `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	rawPerms, ok := resp.Data.User["permissions"]
	if ok {
		require.NoError(t, json.Unmarshal(rawPerms, &perms))
	}
	return perms, ok, w.Body.String()
}

func loginAs(t *testing.T, store *handlersmocks.MockAuthStore, roles ...string) *httptest.ResponseRecorder {
	t.Helper()
	user := &database.User{
		ID: "user-1", Username: "pat", PasswordHash: testBcryptHash(t, "password123"),
		Roles: roles, Status: "active", CreatedAt: time.Now(),
	}
	store.EXPECT().GetUserByUsername("pat").Return(user, nil)
	store.EXPECT().CreateSessionWithOrigin("user-1", mock.Anything, mock.Anything, mock.Anything, database.SessionOriginPassword).
		Return(&database.Session{ID: "sess-1", UserID: "user-1", ExpiresAt: time.Now().Add(time.Hour)}, nil)
	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "pat", "password": "password123"})
	h.Login(c)
	return w
}

// TestAuthHandler_Login_CarriesEffectivePermissions: Login answers before the
// auth middleware binds anything, so it resolves the roles itself. A curator
// holding collections.manage sees it; a plain viewer does not.
func TestAuthHandler_Login_CarriesEffectivePermissions(t *testing.T) {
	curator := &database.Role{ID: "curator", Name: "curator",
		Permissions: []string{auth.PermLibraryView, auth.PermCollectionsManage}}
	viewer := &database.Role{ID: "viewer", Name: "viewer",
		Permissions: []string{auth.PermLibraryView, auth.PermRequestsCreate}}

	t.Run("curator", func(t *testing.T) {
		store := handlersmocks.NewMockAuthStore(t)
		store.EXPECT().GetRoleByID("curator").Return(curator, nil)
		perms, present, body := authUserPermissions(t, loginAs(t, store, "curator"))
		require.True(t, present, "body: %s", body)
		assert.Equal(t, []string{auth.PermCollectionsManage, auth.PermLibraryView}, perms,
			"sorted, and exactly the role's set")
		assert.NotContains(t, body, "password_hash")
	})

	t.Run("plain user", func(t *testing.T) {
		store := handlersmocks.NewMockAuthStore(t)
		store.EXPECT().GetRoleByID("viewer").Return(viewer, nil)
		perms, _, _ := authUserPermissions(t, loginAs(t, store, "viewer"))
		assert.NotContains(t, perms, auth.PermCollectionsManage)
		assert.Contains(t, perms, auth.PermLibraryView)
	})

	t.Run("union across roles, unknown role skipped", func(t *testing.T) {
		store := handlersmocks.NewMockAuthStore(t)
		store.EXPECT().GetRoleByID("viewer").Return(viewer, nil)
		store.EXPECT().GetRoleByID("curator").Return(curator, nil)
		store.EXPECT().GetRoleByID("deleted-role").Return(nil, nil)
		perms, _, _ := authUserPermissions(t, loginAs(t, store, "viewer", "curator", "deleted-role"))
		assert.Equal(t,
			[]string{auth.PermCollectionsManage, auth.PermLibraryView, auth.PermRequestsCreate}, perms)
	})
}

// TestAuthHandler_Me_PermissionsAreTheRequestsEffectiveSet reads the request
// context, not the roles. The mock store has no GetRoleByID expectation, so a
// handler that re-walked the roles would fail the strict mock outright.
func TestAuthHandler_Me_PermissionsAreTheRequestsEffectiveSet(t *testing.T) {
	user := &database.User{ID: "user-1", Username: "pat", Roles: []string{"curator"}, Status: "active"}
	meWith := func(t *testing.T, perms []auth.Permission) *httptest.ResponseRecorder {
		store := handlersmocks.NewMockAuthStore(t)
		h := handlers.NewAuthHandler(store, true)
		c, w := newAuthCtx("GET", "/auth/me", nil)
		setAuthUser(c, user)
		c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), perms))
		h.Me(c)
		return w
	}

	t.Run("curator sees collections.manage", func(t *testing.T) {
		perms, present, body := authUserPermissions(t,
			meWith(t, []auth.Permission{auth.PermLibraryView, auth.PermCollectionsManage}))
		require.True(t, present, "body: %s", body)
		assert.Equal(t, []string{auth.PermCollectionsManage, auth.PermLibraryView}, perms)
	})

	t.Run("plain user does not", func(t *testing.T) {
		perms, _, _ := authUserPermissions(t, meWith(t, []auth.Permission{auth.PermLibraryView}))
		assert.Equal(t, []string{auth.PermLibraryView}, perms)
		assert.NotContains(t, perms, auth.PermCollectionsManage)
	})

	// An API key's scopes narrow the set the middleware binds. The user's role
	// still holds collections.manage, but this request cannot use it, so /auth/me
	// must not advertise it.
	t.Run("key-scoped request is narrowed", func(t *testing.T) {
		scoped := []auth.Permission{auth.PermLibraryView} // what the middleware left after intersecting
		perms, _, _ := authUserPermissions(t, meWith(t, scoped))
		assert.NotContains(t, perms, auth.PermCollectionsManage)
	})

	t.Run("no bound permissions leaves the key out, which a client reads as none", func(t *testing.T) {
		perms, present, _ := authUserPermissions(t, meWith(t, nil))
		assert.False(t, present)
		assert.Empty(t, perms)
	})
}
