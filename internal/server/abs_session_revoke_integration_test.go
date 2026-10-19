// file: internal/server/abs_session_revoke_integration_test.go
// version: 1.3.0
// guid: d943df61-c54b-4621-8c73-dd144fc38af8
// last-edited: 2026-10-19

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
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
	absStore, ok := database.AsCapability[abshandler.Store](s.Ops())
	require.True(t, ok)
	return signInABSPhoneVia(t, s, absStore, username, password)
}

// signInABSPhoneVia is signInABSPhone with the ABS handler's store supplied, so a
// test can interpose on the login's own writes.
func signInABSPhoneVia(t *testing.T, s *Server, absStore abshandler.Store, username, password string) *absPhone {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: "0123456789abcdef0123456789abcdef", AuthModes: "jwt"})
	require.NoError(t, err)
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
			rehasher, ok := database.AsCapability[database.UserPasswordRehasher](s.Ops())
			require.True(t, ok)
			wrote, err := rehasher.RehashUserPassword(u.ID, fresh.PasswordHash, "bcrypt", string(hash))
			require.NoError(t, err)
			require.True(t, wrote)
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

// credentialActions are the four account-security paths. Each must end every
// ABS session of the user, and none may depend on the revoke-all to do it.
func credentialActions() map[string]func(t *testing.T, s *Server, store database.Store, u *database.User) {
	return map[string]func(t *testing.T, s *Server, store database.Store, u *database.User){
		"admin password reset": func(t *testing.T, s *Server, store database.Store, u *database.User) {
			admin := &database.User{ID: "admin-1", Username: "admin", Status: "active"}
			c, w := handlerCtx(http.MethodPut, "/api/v1/auth/me/password",
				map[string]any{"user_id": u.ID, "new_password": "brand-new-pw"}, admin, auth.PermUsersManage)
			handlers.NewAuthHandler(store, true).ChangePassword(c)
			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		},
		"reset-password link": func(t *testing.T, s *Server, store database.Store, u *database.User) {
			orig := s.store
			s.store = store
			defer func() { s.store = orig }()
			w := httptest.NewRecorder()
			resetPasswordRouter(s).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/reset-password", nil))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		},
		"deactivate": func(t *testing.T, s *Server, store database.Store, u *database.User) {
			c, w := handlerCtx(http.MethodPost, "/api/v1/users/"+u.ID+"/deactivate", nil, nil)
			c.Params = gin.Params{{Key: "id", Value: u.ID}}
			handlers.NewUserHandler(store).DeactivateUser(c)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		},
	}
}

// TestReactivationCannotReviveABSSessionsWhenRevokeFails: with every cleanup
// revoke failing, a session from before the deactivation (an unstamped legacy one
// here, epoch 0) is still refused after the account is reactivated, because both
// status changes bump the credential epoch.
func TestReactivationCannotReviveABSSessionsWhenRevokeFails(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	const pw = "pw-pw-pw-pw-1"
	u := createPasswordUser(t, s, "phoneuser", pw)
	phone := signInABSPhone(t, s, "phoneuser", pw)
	store := &noRevokeStore{Store: s.storeForWiring()}
	uh := handlers.NewUserHandler(store)

	c, w := handlerCtx(http.MethodPost, "/deactivate", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: u.ID}}
	uh.DeactivateUser(c)
	require.Equal(t, http.StatusOK, w.Code)
	c, w = handlerCtx(http.MethodPost, "/reactivate", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: u.ID}}
	uh.ReactivateUser(c)
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, 2, store.revokes, "both cleanup revokes were attempted and failed")
	phone.requireSignedOut(t, "after reactivation with failed revokes")
	fresh := signInABSPhone(t, s, "phoneuser", pw)
	require.True(t, fresh.bearerWorks(t), "the reactivated account signs in normally")
}

// noRevokeStore is the real store with a revoke-all that silently does nothing,
// so a test can show the credential epoch alone ends the sessions.
type noRevokeStore struct {
	database.Store
	revokes int
}

func (n *noRevokeStore) Unwrap() database.Store { return n.Store }

func (n *noRevokeStore) RevokeAllABSSessionsForUser(string) (int, error) {
	n.revokes++
	return 0, errors.New("disk full")
}

// TestCredentialEpochAloneEndsABSSessions: with the cleanup revoke failing, every
// account-security path still signs the phones out, because the epoch bump that
// lands with the change is what every refresh and bearer request checks.
func TestCredentialEpochAloneEndsABSSessions(t *testing.T) {
	const pw = "pw-pw-pw-pw-1"
	for name, act := range credentialActions() {
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			u := createPasswordUser(t, s, "phoneuser", pw)
			phone := signInABSPhone(t, s, "phoneuser", pw)
			require.True(t, phone.bearerWorks(t), "precondition: signed in")

			store := &noRevokeStore{Store: s.storeForWiring()}
			act(t, s, store, u)

			require.Equal(t, 1, store.revokes, "the cleanup revoke is still attempted")
			phone.requireSignedOut(t, name)
		})
	}
}

// racingLoginStore holds an ABS login between verifying the password and writing
// its session, and runs a credential change in that gap: the window a revoke can
// never reach, since the session does not exist yet when the revoke runs.
type racingLoginStore struct {
	abshandler.Store
	during func()
	once   sync.Once
}

func (r *racingLoginStore) CreateABSSession(sess *database.ABSSession) error {
	r.once.Do(r.during)
	return r.Store.CreateABSSession(sess)
}

// TestLoginRacingACredentialChangeIsRefused: a phone that signed in with the old
// password while the password was being changed (or the account reset or
// deactivated) gets a session, but it never works: it carries the old epoch. The
// row is not revoked (the revoke ran before it existed), so the epoch check is
// the only thing refusing it.
func TestLoginRacingACredentialChangeIsRefused(t *testing.T) {
	const pw = "pw-pw-pw-pw-1"
	for name, act := range credentialActions() {
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			u := createPasswordUser(t, s, "phoneuser", pw)
			absStore, ok := database.AsCapability[abshandler.Store](s.Ops())
			require.True(t, ok)
			racing := &racingLoginStore{Store: absStore, during: func() { act(t, s, s.storeForWiring(), u) }}

			phone := signInABSPhoneVia(t, s, racing, "phoneuser", pw)

			phone.requireSignedOut(t, name+" racing a login")
			lister, ok := database.AsCapability[interface {
				ListABSSessionsForUser(string) ([]database.ABSSession, error)
			}](s.Ops())
			require.True(t, ok)
			sessions, err := lister.ListABSSessionsForUser(u.ID)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			require.False(t, sessions[0].Revoked, "the revoke ran before this session existed; only the epoch can refuse it")
		})
	}
}

// failingUserWriteStore fails the reset link's epoch write.
type failingUserWriteStore struct {
	database.Store
}

func (f *failingUserWriteStore) Unwrap() database.Store { return f.Store }

func (f *failingUserWriteStore) BumpUserCredentialEpoch(string) (*database.User, error) {
	return nil, errors.New("disk full")
}

// The rest of database.UserCredentialWriter, so AsCapability resolves this
// wrapper's (failing) ops instead of looking through to the real store's.
func (f *failingUserWriteStore) SetUserPassword(string, string, string, string) (*database.User, error) {
	return nil, errors.New("disk full")
}

func (f *failingUserWriteStore) SetUserStatus(string, string) (*database.User, error) {
	return nil, errors.New("disk full")
}

// An admin reset whose epoch write fails must not hand out a reset link: the
// admin would believe the account was secured while the old sessions still work.
func TestResetPassword_EpochWriteFailureIssuesNoLink(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	const pw = "pw-pw-pw-pw-1"
	createPasswordUser(t, s, "resetfail", pw)
	phone := signInABSPhone(t, s, "resetfail", pw)
	user, err := s.storeForWiring().GetUserByUsername("resetfail")
	require.NoError(t, err)

	orig := s.store
	s.store = &failingUserWriteStore{Store: orig}
	w := httptest.NewRecorder()
	resetPasswordRouter(s).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+user.ID+"/reset-password", nil))
	s.store = orig

	require.Equal(t, http.StatusInternalServerError, w.Code, "body: %s", w.Body.String())
	require.NotContains(t, w.Body.String(), "token", "no reset link may be issued when the epoch write failed")
	require.True(t, phone.bearerWorks(t), "nothing changed, so the phone is still signed in")
}
