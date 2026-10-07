// file: internal/server/middleware/auth_method_test.go
// version: 1.4.0
// guid: 5e2b7c19-0d4a-4f86-b3e1-9a6c8d2f7041
// last-edited: 2026-10-07

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// authMethodStore serves one session per origin ("sess-<origin>", and
// "sess-" with no origin) and one API key.
func authMethodStore() *database.MockStore {
	return &database.MockStore{
		CountUsersFunc: func() (int, error) { return 1, nil },
		GetSessionFunc: func(id string) (*database.Session, error) {
			origin := id[len("sess-"):]
			return &database.Session{ID: id, UserID: "owner", ExpiresAt: time.Now().Add(time.Hour), Origin: origin}, nil
		},
		GetUserByIDFunc: func(id string) (*database.User, error) {
			return &database.User{ID: id, Username: id, Status: "active", Roles: []string{"admin"}}, nil
		},
		GetAPIKeyByHashFunc: func(string) (*database.APIKey, error) {
			exp := time.Now().Add(time.Hour)
			return &database.APIKey{ID: "k1", UserID: "owner", Status: "active", ExpiresAt: &exp}, nil
		},
		TouchAPIKeyLastUsedFunc: func(string, time.Time, string) error { return nil },
	}
}

// TestRequireAuth_RecordsAuthMethod: the method is the verifier that bound
// the user, not the transport, and only a session from the user's own
// sign-in is interactive. An API key presented any way (Bearer, cookie) is an
// API key; a temp-login or invite session, or one with no recorded origin,
// is delegated.
func TestRequireAuth_RecordsAuthMethod(t *testing.T) {
	t.Parallel()
	cookie := func(v string) func(*http.Request) {
		return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: v}) }
	}
	bearer := func(v string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+v) }
	}
	cases := []struct {
		name        string
		set         func(*http.Request)
		want        auth.Method
		interactive bool
	}{
		{"password session cookie", cookie("sess-password"), auth.MethodSession, true},
		{"oauth session bearer", bearer("sess-oauth"), auth.MethodSession, true},
		{"session without origin", cookie("sess-"), auth.MethodSessionDelegated, false},
		{"temp-login session", cookie("sess-temp_login"), auth.MethodSessionDelegated, false},
		{"invite session", cookie("sess-invite"), auth.MethodSessionDelegated, false},
		{"api key bearer", bearer("abk_secret"), auth.MethodAPIKey, false},
		{"api key in cookie", cookie("abk_secret"), auth.MethodAPIKey, false},
		{"api key bearer beside a password session cookie", func(r *http.Request) {
			bearer("abk_secret")(r)
			cookie("sess-password")(r)
		}, auth.MethodAPIKey, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			tc.set(req)
			resp, hit, c := executeAuthMiddleware(t, RequireAuth(authMethodStore()), req)
			require.True(t, hit, "status %d", resp.Code)
			got := CurrentAuthMethod(c)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.interactive, got.Interactive())
		})
	}
}

// TestRequireAuth_BootstrapModeIsNotInteractive: with no users the request
// passes unauthenticated, and no method is recorded.
func TestRequireAuth_BootstrapModeIsNotInteractive(t *testing.T) {
	t.Parallel()
	store := authMethodStore()
	store.CountUsersFunc = func() (int, error) { return 0, nil }
	_, hit, c := executeAuthMiddleware(t, RequireAuth(store), httptest.NewRequest(http.MethodGet, "/protected", nil))
	require.True(t, hit)
	assert.Equal(t, auth.MethodNone, CurrentAuthMethod(c))
	assert.False(t, CurrentAuthMethod(c).Interactive())
}

func TestAuthMethod_Interactive(t *testing.T) {
	t.Parallel()
	assert.True(t, auth.MethodSession.Interactive())
	assert.True(t, auth.MethodCFAccess.Interactive())
	assert.False(t, auth.MethodSessionDelegated.Interactive())
	assert.False(t, auth.MethodAPIKey.Interactive())
	assert.False(t, auth.MethodABS.Interactive())
	assert.False(t, auth.MethodNone.Interactive())
	assert.False(t, auth.Method("forged").Interactive())
}

// TestWithMethod_DowngradeOnly: once recorded, a method can be replaced only
// by a non-interactive one, so no later stage can turn an API-key request
// into a session.
func TestWithMethod_DowngradeOnly(t *testing.T) {
	t.Parallel()
	ctx := auth.WithMethod(context.Background(), auth.MethodAPIKey)
	assert.Equal(t, auth.MethodAPIKey, auth.MethodFromContext(auth.WithMethod(ctx, auth.MethodSession)))
	assert.Equal(t, auth.MethodAPIKey, auth.MethodFromContext(auth.WithMethod(ctx, auth.MethodCFAccess)))
	ctx = auth.WithMethod(context.Background(), auth.MethodCFAccess)
	assert.Equal(t, auth.MethodABS, auth.MethodFromContext(auth.WithMethod(ctx, auth.MethodABS)))
	assert.Equal(t, auth.MethodCFAccess, auth.MethodFromContext(auth.WithMethod(ctx, auth.MethodNone)))
}

// TestCloudflareAccess_HumanIsInteractive_ServiceTokenIsNot: a verified SSO
// assertion naming an allowlisted person records cf_access; a service-token
// assertion (valid, no email) binds nobody, so the request is decided by
// what RequireAuth verifies next (an API key here, or a 401).
func TestCloudflareAccess_HumanIsInteractive_ServiceTokenIsNot(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	cfStore := newFakeABSStore()
	cfStore.addUser(activeUser("owner", "owner"))
	verifier := &fakeCFVerifier{
		byToken: map[string]*oauth.IdentityClaims{
			"human": {Provider: oauth.ProviderCFAccess, Subject: "sub-owner", Email: "owner@example.com", EmailVerified: true},
		},
		nonIdentity: map[string]bool{"svc": true},
	}
	cf := &CFAccessAuthenticator{verifier: verifier, cfg: oauth.New(oauth.Config{AllowedEmails: []string{"owner@example.com"}}), store: cfStore}

	run := func(assertion, token string) (int, auth.Method) {
		r := gin.New()
		var got auth.Method
		r.Use(CloudflareAccessAuth(cf), RequireAuth(authMethodStore()))
		r.GET("/protected", func(c *gin.Context) {
			got = CurrentAuthMethod(c)
			c.Status(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		if assertion != "" {
			req.Header.Set(oauth.CFAccessHeader, assertion)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, got
	}

	code, m := run("human", "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, auth.MethodCFAccess, m)
	assert.True(t, m.Interactive())

	code, m = run("svc", "abk_secret")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, auth.MethodAPIKey, m, "a service token never makes the request interactive")
	assert.False(t, m.Interactive())

	code, _ = run("svc", "")
	assert.Equal(t, http.StatusUnauthorized, code, "a service token alone binds nobody")

	code, m = run("forged", "abk_secret")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, auth.MethodAPIKey, m)
}

// TestCloudflareAccess_APIKeyWins (owner decision 2026-10-07): a request that
// presents an API key is the key's request, recorded as api_key with every key
// restriction, even when it ALSO carries a verified Access assertion for an
// allowlisted person — in the bearer or in the session cookie.
func TestCloudflareAccess_APIKeyWins(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	cfStore := newFakeABSStore()
	cfStore.addUser(activeUser("owner", "owner"))
	verifier := &fakeCFVerifier{byToken: map[string]*oauth.IdentityClaims{
		"human": {Provider: oauth.ProviderCFAccess, Subject: "sub-owner", Email: "owner@example.com", EmailVerified: true},
	}}
	cf := &CFAccessAuthenticator{verifier: verifier, cfg: oauth.New(oauth.Config{AllowedEmails: []string{"owner@example.com"}}), store: cfStore}
	cases := map[string]func(r *http.Request){
		"bearer key":                func(r *http.Request) { r.Header.Set("Authorization", "Bearer abk_secret") },
		"key in the session cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "abk_secret"}) },
		"session bearer + key cookie": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer sess-password")
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "abk_secret"})
		},
		"access cookie + bearer key": func(r *http.Request) {
			r.Header.Del(oauth.CFAccessHeader)
			r.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: "human"})
			r.Header.Set("Authorization", "Bearer abk_secret")
		},
	}
	for name, add := range cases {
		t.Run(name, func(t *testing.T) {
			r := gin.New()
			var got auth.Method
			r.Use(CloudflareAccessAuth(cf), RequireAuth(authMethodStore()))
			r.GET("/protected", func(c *gin.Context) {
				got = CurrentAuthMethod(c)
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set(oauth.CFAccessHeader, "human")
			add(req)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, auth.MethodAPIKey, got)
		})
	}
}

// TestABSBind_IsNeverInteractive: every identity the ABS surface binds is
// recorded as abs, never as an interactive login.
func TestABSBind_IsNeverInteractive(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	(&ABSIdentityResolver{}).Bind(c, &ABSIdentity{User: activeUser("owner", "owner"), Mode: ABSModeJWT, SessionID: "s1"})
	assert.Equal(t, auth.MethodABS, CurrentAuthMethod(c))
	assert.False(t, CurrentAuthMethod(c).Interactive())
}

// TestRequireAuth_APIKeyWithoutExpiryRefused: /api/v1 refuses a key with no
// (or a zero) expiry instead of treating it as never expiring — the
// 2026-10-07 review's fail-open on a nil time.
func TestRequireAuth_APIKeyWithoutExpiryRefused(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var zero time.Time
	for name, exp := range map[string]*time.Time{"nil": nil, "zero": &zero} {
		t.Run(name, func(t *testing.T) {
			store := authMethodStore()
			store.GetAPIKeyByHashFunc = func(string) (*database.APIKey, error) {
				return &database.APIKey{ID: "k1", UserID: "owner", Status: "active", ExpiresAt: exp}, nil
			}
			r := gin.New()
			r.Use(RequireAuth(store))
			r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Authorization", "Bearer abk_secret")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "no expiry")
		})
	}
}

// TestOwnerProof_OnlyAVerifiedAccessJWT: the owner check reads the email of a
// VERIFIED Access JWT. The unsigned Cf-Access-Authenticated-User-Email header
// is never consulted, and a key riding along with the owner's assertion is
// the key's request.
func TestOwnerProof_OnlyAVerifiedAccessJWT(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	cfStore := newFakeABSStore()
	cfStore.addUser(activeUser("owner", "owner"))
	verifier := &fakeCFVerifier{byToken: map[string]*oauth.IdentityClaims{
		"owner-jwt": {Provider: oauth.ProviderCFAccess, Subject: "sub-owner", Email: "owner@example.com", EmailVerified: true},
		"other-jwt": {Provider: oauth.ProviderCFAccess, Subject: "sub-other", Email: "other@example.com", EmailVerified: true},
		// A different IdP account whose address uses U+212A KELVIN SIGN.
		"kelvin-jwt": {Provider: oauth.ProviderCFAccess, Subject: "sub-kelvin", Email: "\u212Aate@example.com", EmailVerified: true},
		"kate-jwt":   {Provider: oauth.ProviderCFAccess, Subject: "sub-kate", Email: "kate@example.com", EmailVerified: true},
	}}
	cf := &CFAccessAuthenticator{verifier: verifier,
		cfg: oauth.New(oauth.Config{AllowedEmails: []string{"owner@example.com", "other@example.com", "kate@example.com"}}), store: cfStore}
	ownerEmail := "owner@example.com"
	run := func(set func(r *http.Request)) string {
		r := gin.New()
		why := "unreached"
		r.Use(CloudflareAccessAuth(cf), RequireAuth(authMethodStore()))
		r.GET("/x", func(c *gin.Context) {
			why = auth.OwnerProofWhyNot(c.Request.Context(), ownerEmail, "books.example.com")
			c.Status(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		set(req)
		r.ServeHTTP(httptest.NewRecorder(), req)
		return why
	}
	assert.Empty(t, run(func(r *http.Request) { r.Header.Set(oauth.CFAccessHeader, "owner-jwt") }), "the owner's verified JWT")
	assert.NotEmpty(t, run(func(r *http.Request) { r.Header.Set(oauth.CFAccessHeader, "other-jwt") }), "another person's JWT")
	assert.NotEmpty(t, run(func(r *http.Request) {
		r.Header.Set("Cf-Access-Authenticated-User-Email", "owner@example.com")
		r.Header.Set("Authorization", "Bearer sess-password")
	}), "the unsigned email header with a password session")
	assert.NotEmpty(t, run(func(r *http.Request) {
		r.Header.Set(oauth.CFAccessHeader, "owner-jwt")
		r.Header.Set("Authorization", "Bearer abk_secret")
	}), "an API key riding along with the owner's JWT")

	// 2026-10-07 review: strings.EqualFold folded the Kelvin sign onto "k",
	// so this other account passed as the owner.
	ownerEmail = "kate@example.com"
	assert.Empty(t, run(func(r *http.Request) { r.Header.Set(oauth.CFAccessHeader, "kate-jwt") }), "the owner kate's verified JWT")
	assert.NotEmpty(t, run(func(r *http.Request) { r.Header.Set(oauth.CFAccessHeader, "kelvin-jwt") }), "a Kelvin-sign lookalike of the owner's email")
}
