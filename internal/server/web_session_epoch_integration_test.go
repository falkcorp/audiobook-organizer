// file: internal/server/web_session_epoch_integration_test.go
// version: 1.1.0
// guid: bf3e46f8-d6da-4ac3-889b-0debee0221ae
// last-edited: 2026-10-30

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const webPW = "pw-pw-pw-pw-1"

// webRig is the web-UI session surface against the test server's REAL store:
// the real Login and ChangePassword handlers, the real auth middleware, the real
// reset-link and temp-login handlers.
type webRig struct {
	s     *Server
	store database.Store
	user  *database.User
	r     *gin.Engine
}

// newWebRig builds the router. handlerStore is what the account-security
// handlers (login, change-password) are constructed with; the auth middleware
// always reads the real store.
func newWebRig(t *testing.T, s *Server, handlerStore database.Store) *webRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	real := s.storeForWiring()
	u := createPasswordUser(t, s, "robin", webPW)
	ah := handlers.NewAuthHandler(handlerStore, true)

	r := gin.New()
	r.POST("/auth/login", ah.Login)
	r.GET("/auth/temp-login", s.consumeTempLoginToken)
	r.POST("/users/:id/reset-password", s.handleResetPassword)
	r.Use(servermiddleware.RequireAuth(real))
	r.GET("/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.PUT("/auth/me/password", ah.ChangePassword)
	return &webRig{s: s, store: real, user: u, r: r}
}

func (w *webRig) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	w.r.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == servermiddleware.SessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in the response: %d %s", rec.Code, rec.Body.String())
	return nil
}

// signIn is a real POST /auth/login and returns the session cookie it set.
func (w *webRig) signIn(t *testing.T, pw string) *http.Cookie {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"username": "robin", "password": pw})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := w.do(req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return sessionCookie(t, rec)
}

func (w *webRig) ping(c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.AddCookie(c)
	return w.do(req)
}

func (w *webRig) row(t *testing.T, c *http.Cookie) *database.Session {
	t.Helper()
	sess, err := w.store.GetSession(c.Value)
	require.NoError(t, err)
	require.NotNil(t, sess)
	return sess
}

// webAction is one account-security path. keepsCaller reports whether the
// session that made the request is meant to stay signed in.
type webAction struct {
	keepsCaller bool
	run         func(t *testing.T, w *webRig, caller *http.Cookie)
}

func webActions() map[string]webAction {
	return map[string]webAction{
		"self password change": {keepsCaller: true, run: func(t *testing.T, w *webRig, caller *http.Cookie) {
			raw, _ := json.Marshal(map[string]any{"current_password": webPW, "new_password": "brand-new-pw"})
			req := httptest.NewRequest(http.MethodPut, "/auth/me/password", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(caller)
			rec := w.do(req)
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		}},
		"admin password reset": {run: func(t *testing.T, w *webRig, _ *http.Cookie) {
			admin := &database.User{ID: "admin-1", Username: "admin", Status: "active"}
			c, rec := handlerCtx(http.MethodPut, "/api/v1/auth/me/password",
				map[string]any{"user_id": w.user.ID, "new_password": "brand-new-pw"}, admin, auth.PermUsersManage)
			handlers.NewAuthHandler(w.s.storeForWiring(), true).ChangePassword(c)
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		}},
		"reset-password link": {run: func(t *testing.T, w *webRig, _ *http.Cookie) {
			rec := w.do(httptest.NewRequest(http.MethodPost, "/users/"+w.user.ID+"/reset-password", nil))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		}},
		"deactivate": {run: func(t *testing.T, w *webRig, _ *http.Cookie) {
			c, rec := handlerCtx(http.MethodPost, "/api/v1/users/"+w.user.ID+"/deactivate", nil, nil)
			c.Params = gin.Params{{Key: "id", Value: w.user.ID}}
			handlers.NewUserHandler(w.s.storeForWiring()).DeactivateUser(c)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		}},
	}
}

// TestWebSessionsEndOnCredentialChange: a web session that existed before each
// account-security action stops authenticating, the cleanup revoke has marked
// its row, and only the self-service change leaves its own caller signed in.
func TestWebSessionsEndOnCredentialChange(t *testing.T) {
	for name, act := range webActions() {
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			w := newWebRig(t, s, s.storeForWiring())
			otherDevice := w.signIn(t, webPW)
			caller := w.signIn(t, webPW)
			require.Equal(t, http.StatusOK, w.ping(otherDevice).Code, "precondition: signed in")
			require.Equal(t, http.StatusOK, w.ping(caller).Code, "precondition: signed in")

			act.run(t, w, caller)

			// Cleanup ran in the same request: the dead rows are already revoked,
			// before anyone presented them.
			require.True(t, w.row(t, otherDevice).Revoked, "the other device's row must be revoked as cleanup")
			require.Equal(t, http.StatusUnauthorized, w.ping(otherDevice).Code, "the other device must be signed out")
			if act.keepsCaller {
				require.False(t, w.row(t, caller).Revoked, "the caller's own session must not be revoked")
				require.Equal(t, http.StatusOK, w.ping(caller).Code, "the caller stays signed in on their own password change")
			} else {
				require.Equal(t, http.StatusUnauthorized, w.ping(caller).Code, "%s must end every web session", name)
			}
		})
	}
}

// brokenCleanupStore is the real store whose session listing and revoke both
// fail, so only the epoch stamp can be ending anything.
type brokenCleanupStore struct {
	database.Store
	listCalls   int
	revokeCalls int
}

func (b *brokenCleanupStore) Unwrap() database.Store { return b.Store }

func (b *brokenCleanupStore) ListUserSessions(string) ([]database.Session, error) {
	b.listCalls++
	return nil, errors.New("disk full")
}

func (b *brokenCleanupStore) RevokeSession(string) error {
	b.revokeCalls++
	return errors.New("disk full")
}

// TestWebSessionEpochAloneEndsSessions: with the cleanup unable to list or revoke
// anything, every path still signs the other sessions out (the stamp does it, not
// the revoke), the request still succeeds, and the caller's own session is kept
// because its re-stamp is part of the password write, not of the cleanup.
func TestWebSessionEpochAloneEndsSessions(t *testing.T) {
	for name, act := range webActions() {
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			real := s.storeForWiring()
			broken := &brokenCleanupStore{Store: real}
			w := newWebRig(t, s, broken)
			otherDevice := w.signIn(t, webPW)
			caller := w.signIn(t, webPW)

			orig := s.store
			s.store = broken // the reset link and the direct handlers read s.store
			act.run(t, w, caller)
			s.store = orig

			require.Positive(t, broken.listCalls, "the cleanup was attempted")
			require.False(t, w.row(t, otherDevice).Revoked, "precondition: the cleanup did not revoke it")
			require.Equal(t, http.StatusUnauthorized, w.ping(otherDevice).Code, "the epoch alone must refuse the other device")
			if act.keepsCaller {
				require.Equal(t, http.StatusOK, w.ping(caller).Code, "the caller's re-stamp is atomic with the password, not cleanup")
			} else {
				require.Equal(t, http.StatusUnauthorized, w.ping(caller).Code)
			}
		})
	}
}

// racingWebLoginStore holds a web login between verifying the password and
// writing its session, and runs a credential change in that gap: the window a
// revoke can never reach, because the session does not exist yet when it runs.
type racingWebLoginStore struct {
	database.Store
	during func() // runs once, on the next login, then disarms
}

func (r *racingWebLoginStore) Unwrap() database.Store { return r.Store }

func (r *racingWebLoginStore) CreateSessionWithOrigin(userID, ip, ua string, ttl time.Duration, origin string, epoch int64) (*database.Session, error) {
	if d := r.during; d != nil {
		r.during = nil
		d()
	}
	return r.Store.CreateSessionWithOrigin(userID, ip, ua, ttl, origin, epoch)
}

// TestWebLoginRacingACredentialChangeIsRefused: a sign-in that verified the old
// password while the password was being changed (or the account reset or
// deactivated) gets a cookie, but the session never authenticates: it carries the
// epoch it verified against. Its row is NOT revoked (the cleanup ran before it
// existed), so the middleware's stamp check is the only thing refusing it.
func TestWebLoginRacingACredentialChangeIsRefused(t *testing.T) {
	for name, act := range webActions() {
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			racing := &racingWebLoginStore{Store: s.storeForWiring()}
			w := newWebRig(t, s, racing)
			caller := w.signIn(t, webPW) // before the race arms; not under test
			racing.during = func() { act.run(t, w, caller) }

			mid := w.signIn(t, webPW) // login verified pre-change, session written post-change

			require.False(t, w.row(t, mid).Revoked, "the cleanup ran before this session existed; only the stamp can refuse it")
			require.Equal(t, http.StatusUnauthorized, w.ping(mid).Code, "a session minted mid-change from the old credentials must be refused")
		})
	}
}

// freshLoginDuringCleanupStore creates a sign-in that verified AFTER the change
// (so it is stamped with the new epoch) at the moment the cleanup lists the
// user's sessions: a login completing between the epoch bump and the cleanup.
type freshLoginDuringCleanupStore struct {
	database.Store
	t       *testing.T
	created *database.Session
}

func (f *freshLoginDuringCleanupStore) Unwrap() database.Store { return f.Store }

func (f *freshLoginDuringCleanupStore) ListUserSessions(userID string) ([]database.Session, error) {
	u, err := f.Store.GetUserByID(userID)
	require.NoError(f.t, err)
	f.created, err = f.Store.CreateSessionWithOrigin(userID, "192.0.2.9", "later", time.Hour, database.SessionOriginPassword, u.CredentialEpoch)
	require.NoError(f.t, err)
	return f.Store.ListUserSessions(userID)
}

// TestCleanupDoesNotRevokeASignInThatFollowedTheChange: the cleanup revokes only
// sessions OLDER than the change, so a person who signs in with the new password
// in the instant between the epoch bump and the cleanup is not signed out by it.
func TestCleanupDoesNotRevokeASignInThatFollowedTheChange(t *testing.T) {
	for _, name := range []string{"admin password reset", "reset-password link", "deactivate"} {
		act := webActions()[name]
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupTestServer(t)
			defer cleanup()
			hook := &freshLoginDuringCleanupStore{Store: s.storeForWiring(), t: t}
			w := newWebRig(t, s, hook)
			old := w.signIn(t, webPW)

			orig := s.store
			s.store = hook
			act.run(t, w, old)
			s.store = orig

			require.NotNil(t, hook.created)
			require.False(t, w.row(t, &http.Cookie{Value: hook.created.ID}).Revoked, "a session stamped with the new epoch must survive the cleanup")
			require.True(t, w.row(t, old).Revoked, "the older session is still cleaned up")
			if name == "deactivate" {
				return // the account is locked: the survivor is refused by status, not by stamp
			}
			require.Equal(t, http.StatusOK, w.ping(&http.Cookie{Name: servermiddleware.SessionCookieName, Value: hook.created.ID}).Code)
		})
	}
}

// TestResetLinkCompletionYieldsACurrentSession: the user the admin reset signs in
// through the one-time link; that session is stamped with the epoch the reset
// produced, works, and survives the self-service password change they make next.
func TestResetLinkCompletionYieldsACurrentSession(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	w := newWebRig(t, s, s.storeForWiring())
	before := w.signIn(t, webPW)

	rec := w.do(httptest.NewRequest(http.MethodPost, "/users/"+w.user.ID+"/reset-password", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			LoginURL string `json:"login_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, http.StatusUnauthorized, w.ping(before).Code, "the reset ends the session from before it")

	link, err := url.Parse(body.Data.LoginURL)
	require.NoError(t, err)
	linkRec := w.do(httptest.NewRequest(http.MethodGet, link.RequestURI(), nil))
	require.Equal(t, http.StatusSeeOther, linkRec.Code)
	fresh := sessionCookie(t, linkRec)

	u, err := w.store.GetUserByID(w.user.ID)
	require.NoError(t, err)
	require.Equal(t, u.CredentialEpoch, w.row(t, fresh).CredentialEpoch, "the link's session carries the epoch current at redemption")
	require.Equal(t, http.StatusOK, w.ping(fresh).Code, "completing the reset link must sign the user in")

	webActions()["self password change"].run(t, w, fresh)
	require.Equal(t, http.StatusOK, w.ping(fresh).Code, "choosing their own password keeps them signed in")
}

// resetLink asks for a reset link for the rig's user and returns its request URI.
func (w *webRig) resetLink(t *testing.T) string {
	t.Helper()
	rec := w.do(httptest.NewRequest(http.MethodPost, "/users/"+w.user.ID+"/reset-password", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data struct {
			LoginURL string `json:"login_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	link, err := url.Parse(body.Data.LoginURL)
	require.NoError(t, err)
	return link.RequestURI()
}

// TestResetLinkDiesWhenCredentialsChangeAfterItWasMinted: a link carries the
// epoch it was minted under. A later password change or a later reset makes it
// worthless, so a link left in a chat log cannot sign anyone in after the
// account has moved on; the newest link still works.
func TestResetLinkDiesWhenCredentialsChangeAfterItWasMinted(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	w := newWebRig(t, s, s.storeForWiring())

	// A password change after minting.
	first := w.resetLink(t)
	caller := w.signIn(t, webPW)
	webActions()["self password change"].run(t, w, caller)
	rec := w.do(httptest.NewRequest(http.MethodGet, first, nil))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "/login?error=invalid_or_expired_token")
	for _, c := range rec.Result().Cookies() {
		require.NotEqual(t, servermiddleware.SessionCookieName, c.Name, "a refused link must not set a session cookie")
	}

	// A second reset after minting: only the newest link works.
	older := w.resetLink(t)
	newer := w.resetLink(t)
	rec = w.do(httptest.NewRequest(http.MethodGet, older, nil))
	require.Contains(t, rec.Header().Get("Location"), "/login?error=invalid_or_expired_token")
	rec = w.do(httptest.NewRequest(http.MethodGet, newer, nil))
	require.Equal(t, "/", rec.Header().Get("Location"))
	require.Equal(t, http.StatusOK, w.ping(sessionCookie(t, rec)).Code)
}

// TestWebLoginStampsTheEpochItVerified: the session row carries the user's
// CredentialEpoch at sign-in, not a default.
func TestWebLoginStampsTheEpochItVerified(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	w := newWebRig(t, s, s.storeForWiring())
	creds, ok := database.AsCapability[database.UserCredentialWriter](w.store)
	require.True(t, ok)
	_, err := creds.BumpUserCredentialEpoch(w.user.ID)
	require.NoError(t, err)
	_, err = creds.BumpUserCredentialEpoch(w.user.ID)
	require.NoError(t, err)

	c := w.signIn(t, webPW)

	require.EqualValues(t, 2, w.row(t, c).CredentialEpoch)
	require.Equal(t, http.StatusOK, w.ping(c).Code)
}

// TestReactivationDoesNotReviveWebSessions: unlocking an account must not bring
// back a web session from before it was locked, with or without the cleanup.
func TestReactivationDoesNotReviveWebSessions(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	broken := &brokenCleanupStore{Store: s.storeForWiring()}
	w := newWebRig(t, s, broken)
	old := w.signIn(t, webPW)
	uh := handlers.NewUserHandler(broken)

	c, rec := handlerCtx(http.MethodPost, "/deactivate", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: w.user.ID}}
	uh.DeactivateUser(c)
	require.Equal(t, http.StatusOK, rec.Code)
	c, rec = handlerCtx(http.MethodPost, "/reactivate", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: w.user.ID}}
	uh.ReactivateUser(c)
	require.Equal(t, http.StatusOK, rec.Code)

	require.False(t, w.row(t, old).Revoked, "precondition: the cleanup revoke failed")
	require.Equal(t, http.StatusUnauthorized, w.ping(old).Code, "the account is active again, but the old session stays dead")
	require.Equal(t, http.StatusOK, w.ping(w.signIn(t, webPW)).Code, "a fresh sign-in works")
}

// TestOpenStreamEndsOnACredentialChange: sessionStillStands, the live re-check an
// open event stream asks, answers false once the session's stamp is behind.
func TestOpenStreamEndsOnACredentialChange(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	config.AppConfig.EnableAuth = true // setupTestServer restores AppConfig on cleanup
	w := newWebRig(t, s, s.storeForWiring())
	stands := s.sessionStillStands()
	var got bool
	w.r.GET("/stands", func(c *gin.Context) { got = stands(c); c.Status(http.StatusOK) })
	check := func(c *http.Cookie) bool {
		req := httptest.NewRequest(http.MethodGet, "/stands", nil)
		req.AddCookie(c)
		require.Equal(t, http.StatusOK, w.do(req).Code)
		return got
	}

	c := w.signIn(t, webPW)
	require.True(t, check(c), "precondition: a current session stands")

	creds, ok := database.AsCapability[database.UserCredentialWriter](w.store)
	require.True(t, ok)
	_, err := creds.BumpUserCredentialEpoch(w.user.ID)
	require.NoError(t, err)

	// The middleware would refuse a new request on this session; the stream
	// opened before the change, so ask the re-check directly with the request's
	// bound identity from before the bump.
	req := httptest.NewRequest(http.MethodGet, "/stands", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	user := w.user
	sess := w.row(t, c)
	ctx.Set("auth_session", sess)
	ctx.Request = ctx.Request.WithContext(auth.WithUser(ctx.Request.Context(), user))
	require.False(t, stands(ctx), "an open stream must end when its session's stamp falls behind the user's epoch")
}
