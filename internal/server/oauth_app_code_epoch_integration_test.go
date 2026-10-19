// file: internal/server/oauth_app_code_epoch_integration_test.go
// version: 1.0.0
// guid: 73e84ab1-70b7-4eb5-b510-17c02d35d030
// last-edited: 2026-10-19

package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// appCodeIdP is an IdP that always verifies owner@example.com, so the test runs
// our half of the web OAuth handshake rather than a network.
type appCodeIdP struct{}

func (appCodeIdP) Name() string { return "google" }
func (appCodeIdP) AuthCodeURL(state, _, _ string) string {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state)
}
func (appCodeIdP) Exchange(context.Context, string, string, string) (*oauth.IdentityClaims, error) {
	return &oauth.IdentityClaims{Provider: "google", Subject: "sub-owner", Email: "owner@example.com", EmailVerified: true}, nil
}

const appCodeDeepLink = "audiobooth://oauth"

// appCodeRig wires the web OAuth handler (which mints app codes through
// abs.MintAppAuthorizationCode, oauth_login.go) and the ABS handler (which
// redeems them at /auth/openid/callback) onto one router over the test server's
// REAL store.
type appCodeRig struct {
	router *gin.Engine
	codec  *oauth.StateCodec
}

func newAppCodeRig(t *testing.T, s *Server) *appCodeRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv(abshandler.OIDCRedirectURIsEnvVar, "")
	codec, err := oauth.NewStateCodec(10 * time.Minute)
	require.NoError(t, err)
	oauthCfg := oauth.New(oauth.Config{
		Enabled: true, GoogleClientID: "cid", GoogleClientSecret: "secret",
		RedirectBaseURL: "https://books.example.com", AllowedEmails: []string{"owner@example.com"}, DefaultRole: "viewer",
	})
	oh := handlers.NewOAuthHandler(s.storeForWiring(), oauthCfg, codec, map[string]oauth.Provider{"google": appCodeIdP{}})

	absCfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: "0123456789abcdef0123456789abcdef", AuthModes: "jwt"})
	require.NoError(t, err)
	absStore, ok := database.AsCapability[abshandler.Store](s.Ops())
	require.True(t, ok)
	idStore, ok := database.AsCapability[servermiddleware.ABSIdentityStore](s.Ops())
	require.True(t, ok)
	ah, err := abshandler.New(abshandler.Options{Config: absCfg, Store: absStore,
		Resolver: servermiddleware.NewABSIdentityResolver(absCfg, nil, nil, idStore)})
	require.NoError(t, err)

	r := gin.New()
	r.GET("/auth/oauth/:provider/start", oh.Start)
	r.GET("/auth/oauth/:provider/callback", oh.Callback)
	ah.Register(r)
	return &appCodeRig{router: r, codec: codec}
}

// mint runs the web OAuth deep-link flow and returns the app code it hands back.
func (g *appCodeRig) mint(t *testing.T, verifier string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	w := httptest.NewRecorder()
	g.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/oauth/google/start?redirect_uri="+
		url.QueryEscape(appCodeDeepLink)+"&code_challenge="+challenge+"&code_challenge_method=S256", http.NoBody))
	var ck *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "oauth_state" && c.Value != "" {
			ck = c
		}
	}
	require.NotNil(t, ck, "Start set no state cookie: %d %s", w.Code, w.Body.String())
	payload, err := g.codec.Decode(ck.Value)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/auth/oauth/google/callback?code=idp-code&state="+url.QueryEscape(payload.State), http.NoBody)
	req.AddCookie(ck)
	w = httptest.NewRecorder()
	g.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusFound, w.Code, w.Body.String())
	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(loc.String(), appCodeDeepLink+"?"), "Location %q", loc)
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)
	return code
}

func (g *appCodeRig) redeem(code, verifier string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	g.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/openid/callback?code="+url.QueryEscape(code)+
		"&code_verifier="+url.QueryEscape(verifier), http.NoBody))
	return w
}

// The web OAuth app-code path (oauth_login.go → abs.MintAppAuthorizationCode)
// stamps the RESOLVED user's credential epoch on the code, and the exchange
// refuses a code minted before a credential change.
func TestOAuthAppCode_BoundToCredentialEpoch(t *testing.T) {
	verifier := strings.Repeat("v", 50)

	t.Run("stamps the resolved user's epoch", func(t *testing.T) {
		s, cleanup := setupTestServer(t)
		defer cleanup()
		u := createPasswordUser(t, s, "owner", "pw-pw-pw-pw-1") // owner@example.com: ResolveUser links to it
		creds := handlers.ResolveUserCredentialWriter(s.storeForWiring())
		require.NotNil(t, creds)
		// A non-zero epoch BEFORE minting: a code stamped from anything other
		// than the resolved record (a bare &User{ID}) would carry 0 and be refused.
		for range 2 {
			_, err := creds.BumpUserCredentialEpoch(u.ID)
			require.NoError(t, err)
		}
		g := newAppCodeRig(t, s)
		w := g.redeem(g.mint(t, verifier), verifier)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	t.Run("refuses a code minted before a credential change", func(t *testing.T) {
		s, cleanup := setupTestServer(t)
		defer cleanup()
		u := createPasswordUser(t, s, "owner", "pw-pw-pw-pw-1")
		creds := handlers.ResolveUserCredentialWriter(s.storeForWiring())
		require.NotNil(t, creds)
		g := newAppCodeRig(t, s)
		code := g.mint(t, verifier)
		_, err := creds.BumpUserCredentialEpoch(u.ID)
		require.NoError(t, err)
		w := g.redeem(code, verifier)
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "sign in again")
	})
}
