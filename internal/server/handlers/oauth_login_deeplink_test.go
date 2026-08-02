// file: internal/server/handlers/oauth_login_deeplink_test.go
// version: 1.0.0
// guid: 5e04b7c2-1a68-4d39-b0f5-93c7e2016d84
// last-edited: 2026-08-02

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// The web provider flow can hand a registered native client back to its own URL
// scheme instead of the SPA root. The tests below split into two groups that matter
// for different reasons:
//
//   - the deep link WORKS, and the code it issues is bound to the CLIENT's PKCE
//     challenge (not the server's own IdP verifier — conflating the two exchanges is
//     the mistake this flow invites);
//   - the deep link CANNOT BREAK ORDINARY WEB LOGIN, which is the security property
//     behind the both-params gate: /auth/oauth/:provider/start is unauthenticated, so
//     if one stray query parameter could 400 it, anyone could break login for everyone
//     by getting a parameter appended to a shared link.

const testDeepLink = "audiobooth://oauth"

// stubProvider is an IdP that always succeeds, so the tests exercise OUR half of the
// handshake rather than a network.
type stubProvider struct {
	email        string
	lastVerifier string
}

func (p *stubProvider) Name() string { return "google" }

func (p *stubProvider) AuthCodeURL(state, challenge, redirectURI string) string {
	return "https://idp.example/authorize?state=" + url.QueryEscape(state) +
		"&code_challenge=" + url.QueryEscape(challenge)
}

func (p *stubProvider) Exchange(_ context.Context, _, verifier, _ string) (*oauth.IdentityClaims, error) {
	p.lastVerifier = verifier
	return &oauth.IdentityClaims{
		Provider: "google", Subject: "sub-1", Email: p.email, EmailVerified: true,
	}, nil
}

type deepLinkHarness struct {
	router   *gin.Engine
	provider *stubProvider
	codec    *oauth.StateCodec
}

func newDeepLinkHarness(t *testing.T) *deepLinkHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)

	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "oauth-db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := oauth.New(oauth.Config{
		Enabled:            true,
		GoogleClientID:     "cid",
		GoogleClientSecret: "secret",
		RedirectBaseURL:    "https://books.example.com",
		AllowedEmails:      []string{"owner@example.com"},
		DefaultRole:        "viewer",
	})
	codec, err := oauth.NewStateCodec(10 * time.Minute)
	if err != nil {
		t.Fatalf("NewStateCodec: %v", err)
	}
	provider := &stubProvider{email: "owner@example.com"}
	h := NewOAuthHandler(store, cfg, codec, map[string]oauth.Provider{"google": provider})

	r := gin.New()
	r.GET("/auth/oauth/:provider/start", h.Start)
	r.GET("/auth/oauth/:provider/callback", h.Callback)
	return &deepLinkHarness{router: r, provider: provider, codec: codec}
}

// start issues a Start request and returns the response plus the state cookie.
func (h *deepLinkHarness) start(t *testing.T, query string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/oauth/google/start"+query, http.NoBody))
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == oauthStateCookie && ck.Value != "" {
			return rec, ck
		}
	}
	return rec, nil
}

// callback completes the handshake using the state cookie from start.
func (h *deepLinkHarness) callback(t *testing.T, ck *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := h.codec.Decode(ck.Value)
	if err != nil {
		t.Fatalf("decode state: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet,
		"/auth/oauth/google/callback?code=idp-code&state="+url.QueryEscape(payload.State), http.NoBody)
	req.AddCookie(ck)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ── the deep link works ─────────────────────────────────────────────────────

// TestDeepLink_ReturnsCodeToTheRegisteredScheme is the whole point: before this
// change the custom scheme was silently discarded and the caller landed on the web
// SPA root, which surfaced as "it logged me into the website" rather than as a
// failure.
func TestDeepLink_ReturnsCodeToTheRegisteredScheme(t *testing.T) {
	h := newDeepLinkHarness(t)
	appChallenge := challengeFor("app-verifier-app-verifier-app-verifier")

	_, ck := h.start(t, "?redirect_uri="+url.QueryEscape(testDeepLink)+
		"&code_challenge="+appChallenge+"&code_challenge_method=S256&state=app-state")
	if ck == nil {
		t.Fatal("Start did not set the state cookie")
	}

	rec := h.callback(t, ck)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, testDeepLink+"?") {
		t.Fatalf("Location = %q, want a redirect to %q — the custom scheme must survive verbatim", loc, testDeepLink)
	}
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if parsed.Query().Get("code") == "" {
		t.Fatalf("Location %q carries no code", loc)
	}
	if got := parsed.Query().Get("state"); got != "app-state" {
		t.Fatalf("state = %q, want %q — the client's own state must round-trip", got, "app-state")
	}
}

// 🔴 TestDeepLink_KeepsTheTwoPKCEExchangesSeparate. There are two independent PKCE
// handshakes here: server<->IdP (our verifier) and app<->server (the client's
// challenge). Conflating them either breaks the upstream token exchange or issues a
// code with no client-side proof of possession.
func TestDeepLink_KeepsTheTwoPKCEExchangesSeparate(t *testing.T) {
	h := newDeepLinkHarness(t)
	appVerifier := "app-verifier-app-verifier-app-verifier"
	appChallenge := challengeFor(appVerifier)

	_, ck := h.start(t, "?redirect_uri="+url.QueryEscape(testDeepLink)+
		"&code_challenge="+appChallenge+"&code_challenge_method=S256")
	if ck == nil {
		t.Fatal("Start did not set the state cookie")
	}
	payload, err := h.codec.Decode(ck.Value)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if payload.Verifier == "" {
		t.Fatal("the server's own IdP verifier must still be minted")
	}
	if payload.Verifier == appVerifier || payload.Verifier == appChallenge {
		t.Fatal("the server's IdP verifier was replaced by the client's PKCE material — " +
			"this breaks the upstream token exchange")
	}
	if payload.AppChallenge != appChallenge {
		t.Fatalf("AppChallenge = %q, want the client's challenge %q", payload.AppChallenge, appChallenge)
	}

	h.callback(t, ck)
	// The upstream exchange must have received OUR verifier, not the client's.
	if h.provider.lastVerifier != payload.Verifier {
		t.Fatalf("IdP exchange used verifier %q, want the server's own %q",
			h.provider.lastVerifier, payload.Verifier)
	}
}

// ── the deep link cannot break ordinary web login ───────────────────────────

// 🔴 TestStart_StrayParamDoesNotBreakWebLogin is the gate's reason for existing.
// /auth/oauth/:provider/start is UNAUTHENTICATED, so if a bare redirect_uri (or a
// bare code_challenge) could 400 it, anyone could disable login for every user by
// getting one query parameter appended to a shared link.
func TestStart_StrayParamDoesNotBreakWebLogin(t *testing.T) {
	for name, query := range map[string]string{
		"bare redirect_uri":   "?redirect_uri=" + url.QueryEscape("https://evil.example"),
		"bare callback":       "?callback=" + url.QueryEscape("https://evil.example"),
		"bare code_challenge": "?code_challenge=" + challengeFor("x"),
		"redirect_uri with return": "?redirect_uri=" + url.QueryEscape("https://evil.example") +
			"&return=%2Flibrary",
	} {
		t.Run(name, func(t *testing.T) {
			h := newDeepLinkHarness(t)
			rec, ck := h.start(t, query)
			if rec.Code != http.StatusFound {
				t.Fatalf("Start = %d, want 302 to the IdP — a stray parameter must not break web login", rec.Code)
			}
			if ck == nil {
				t.Fatal("Start did not set the state cookie")
			}
			payload, err := h.codec.Decode(ck.Value)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if payload.AppRedirectURI != "" {
				t.Fatalf("AppRedirectURI = %q, want empty — the deep-link path must not engage "+
					"without BOTH redirect_uri and code_challenge", payload.AppRedirectURI)
			}
		})
	}
}

// TestStart_UnregisteredDeepLinkIsRejectedInline covers the open-redirect guard:
// once both parameters are present the caller has unambiguously asked for the native
// flow, so an unregistered target is a hard 400 — answered INLINE, never by
// redirecting to the rejected URI.
func TestStart_UnregisteredDeepLinkIsRejectedInline(t *testing.T) {
	h := newDeepLinkHarness(t)
	rec, _ := h.start(t, "?redirect_uri="+url.QueryEscape("https://evil.example")+
		"&code_challenge="+challengeFor("v")+"&code_challenge_method=S256")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Start = %d, want 400 for an unregistered redirect_uri", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("rejected request redirected to %q — reporting the rejection by navigating to "+
			"the unvalidated target is still an open redirect", loc)
	}
}

// TestStart_PlainPKCEIsRejected: on a custom URL scheme another installed app can
// register the same scheme and observe the redirect, so a "plain" challenge protects
// nothing.
func TestStart_PlainPKCEIsRejected(t *testing.T) {
	h := newDeepLinkHarness(t)
	rec, _ := h.start(t, "?redirect_uri="+url.QueryEscape(testDeepLink)+
		"&code_challenge=abc&code_challenge_method=plain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Start = %d, want 400 for code_challenge_method=plain", rec.Code)
	}
}

// TestWebLogin_StillLandsOnTheSPA is the regression guard for the untouched path:
// a plain web login must still end at the sanitized same-site return path and must
// still receive a browser session cookie.
func TestWebLogin_StillLandsOnTheSPA(t *testing.T) {
	h := newDeepLinkHarness(t)
	_, ck := h.start(t, "?return=%2Flibrary")
	if ck == nil {
		t.Fatal("Start did not set the state cookie")
	}

	rec := h.callback(t, ck)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/library" {
		t.Fatalf("Location = %q, want %q", loc, "/library")
	}
	var sawSession bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == servermiddleware.SessionCookieName && c.Value != "" {
			sawSession = true
		}
	}
	if !sawSession {
		t.Fatal("web login did not set a session cookie")
	}
}

// TestDeepLink_DoesNotMintABrowserSession: the caller is an
// ASWebAuthenticationSession whose cookie jar is discarded when it closes, so a
// browser session issued here would be an unusable row created on every app login.
func TestDeepLink_DoesNotMintABrowserSession(t *testing.T) {
	h := newDeepLinkHarness(t)
	_, ck := h.start(t, "?redirect_uri="+url.QueryEscape(testDeepLink)+
		"&code_challenge="+challengeFor("v")+"&code_challenge_method=S256")
	if ck == nil {
		t.Fatal("Start did not set the state cookie")
	}

	rec := h.callback(t, ck)
	for _, c := range rec.Result().Cookies() {
		if c.Name == servermiddleware.SessionCookieName && c.Value != "" {
			t.Fatal("the deep-link path issued a browser session cookie the client can never use")
		}
	}
}
