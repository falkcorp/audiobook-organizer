// file: core/auth/oidc/idp_test.go
// version: 1.0.0
// guid: da23338d-077f-4650-8c03-f55456b48940
// last-edited: 2026-11-11

package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

// Batman-themed fixtures: the provider is the Gotham Signal service, the
// client is the cave, the people are the family.
const (
	testClientID     = "bat-cave-client"
	testClientSecret = "hush-Alfred-pennyworth-secret-0042"
)

// idpToken is what the fake IdP's next token response carries for a code.
type idpToken struct {
	Sub           string
	Email         string
	EmailVerified any // a bool, or something that is not (a string)
	Name          string
	Nonce         string
	Audience      string
	Issuer        string
	Expiry        time.Time
	SignWith      *rsa.PrivateKey // nil: the IdP's own key
	OmitIDToken   bool
}

// idpCode is one authorization code the IdP issued: the PKCE challenge it must
// be redeemed against and the token it yields.
type idpCode struct {
	challenge string
	tok       idpToken
}

// idp is an in-process OIDC provider: discovery, a JWKS, and a token endpoint
// that enforces PKCE the way a real one does. It records what it was sent.
type idp struct {
	t   testing.TB
	srv *httptest.Server
	key *rsa.PrivateKey

	mu         sync.Mutex
	codes      map[string]idpCode
	forms      []url.Values
	tokenCalls int
	failBody   string // when set, the token endpoint answers 400 with this body
	jwksDown   bool
}

func newIDP(t testing.TB) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{t: t, key: key, codes: map[string]idpCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize",
			"token_endpoint": p.srv.URL + "/token", "jwks_uri": p.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		down := p.jwksDown
		p.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", p.token)
	p.srv = httptest.NewTLSServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// issue registers a code the next token request may redeem, bound to the PKCE
// challenge of the authorization URL the flow produced.
func (p *idp) issue(code, authURL string, tok idpToken) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tok.Audience == "" {
		tok.Audience = testClientID
	}
	if tok.Issuer == "" {
		tok.Issuer = p.srv.URL
	}
	if tok.Expiry.IsZero() {
		tok.Expiry = time.Now().Add(time.Hour)
	}
	if tok.EmailVerified == nil {
		tok.EmailVerified = true
	}
	p.codes[code] = idpCode{challenge: queryParam(p.t, authURL, "code_challenge"), tok: tok}
}

func (p *idp) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p.mu.Lock()
	p.tokenCalls++
	p.forms = append(p.forms, r.PostForm)
	fail := p.failBody
	rec, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code")) // a code is single use at the provider
	p.mu.Unlock()
	if fail != "" {
		http.Error(w, fail, http.StatusBadRequest)
		return
	}
	if !ok || r.PostForm.Get("client_id") != testClientID && !basicIs(r, testClientID, testClientSecret) {
		writeErr(w, "invalid_grant")
		return
	}
	// PKCE at the provider: the verifier must hash to the challenge sent at
	// authorization time. This is what makes the verifier's presence testable.
	if CodeChallengeS256(r.PostForm.Get("code_verifier")) != rec.challenge {
		writeErr(w, "invalid_grant")
		return
	}
	resp := map[string]any{"access_token": "at-oidc-token", "token_type": "Bearer", "expires_in": 3600}
	if !rec.tok.OmitIDToken {
		resp["id_token"] = p.sign(rec.tok)
	}
	writeJSON(w, resp)
}

func basicIs(r *http.Request, id, secret string) bool {
	u, pw, ok := r.BasicAuth()
	if !ok {
		return false
	}
	u, _ = url.QueryUnescape(u)
	pw, _ = url.QueryUnescape(pw)
	return u == id && pw == secret
}

func writeErr(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// sign mints an id_token for tok.
func (p *idp) sign(tok idpToken) string {
	p.t.Helper()
	key := p.key
	if tok.SignWith != nil {
		key = tok.SignWith
	}
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		p.t.Fatal(err)
	}
	claims := map[string]any{
		"iss": tok.Issuer, "sub": tok.Sub, "aud": tok.Audience,
		"exp": tok.Expiry.Unix(), "iat": time.Now().Add(-time.Minute).Unix(),
		"nonce": tok.Nonce, "email": tok.Email, "email_verified": tok.EmailVerified, "name": tok.Name,
	}
	raw, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		p.t.Fatal(err)
	}
	return raw
}

func (p *idp) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokenCalls
}

func (p *idp) lastForm() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.forms) == 0 {
		return nil
	}
	return p.forms[len(p.forms)-1]
}

// ghEmail is one row of GitHub's /user/emails.
type ghEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// gh is an in-process GitHub: authorize/token endpoints (PKCE enforced) and
// the two API reads the provider makes.
type gh struct {
	t   testing.TB
	srv *httptest.Server

	mu         sync.Mutex
	codes      map[string]string // code -> PKCE challenge
	userID     int64
	userName   string
	emails     []ghEmail
	tokenCalls int
}

func newGH(t testing.TB) *gh {
	t.Helper()
	g := &gh{t: t, codes: map[string]string{}, userID: 1939, userName: "Bruce Wayne"}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		g.tokenCalls++
		challenge, ok := g.codes[r.PostForm.Get("code")]
		delete(g.codes, r.PostForm.Get("code"))
		g.mu.Unlock()
		if !ok || CodeChallengeS256(r.PostForm.Get("code_verifier")) != challenge {
			writeErr(w, "bad_verification_code")
			return
		}
		writeJSON(w, map[string]any{"access_token": "gho_cave_token", "token_type": "bearer"})
	})
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer gho_cave_token" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/user", authed(func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		writeJSON(w, map[string]any{"id": g.userID, "name": g.userName})
	}))
	mux.HandleFunc("/user/emails", authed(func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		writeJSON(w, g.emails)
	}))
	g.srv = httptest.NewTLSServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gh) issue(code, authURL string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.codes[code] = queryParam(g.t, authURL, "code_challenge")
}

func (g *gh) urls() githubURLs {
	return githubURLs{
		endpoint: oauth2.Endpoint{AuthURL: g.srv.URL + "/login/oauth/authorize", TokenURL: g.srv.URL + "/login/oauth/access_token"},
		api:      g.srv.URL,
	}
}
