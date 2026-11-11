// file: internal/cfaccess/extension_test.go
// version: 1.0.1
// guid: b29f8914-e3a0-4c02-8652-2f64d5cf6c4b
// last-edited: 2026-11-11

package cfaccess

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	coreauth "github.com/falkcorp/mediacore/auth"
	coreoidc "github.com/falkcorp/mediacore/auth/oidc"
)

const (
	testAUD   = "gotham-signal-aud-7c1d"
	ownerMail = "bruce@wayne.example.test"
)

// access is an in-process Cloudflare Access team domain: a JWKS the test can
// rotate, break and restore, and a signer for tokens.
type access struct {
	t   testing.TB
	srv *httptest.Server

	mu        sync.Mutex
	keys      map[string]*rsa.PrivateKey // kid -> key served
	mode      string                     // "", "503", "garbage", "empty", "drop", "redirect", "huge", "nokid", "503keys"
	fetches   int
	elsewhere int           // requests to the redirect target
	gate      chan struct{} // when set, a certs request waits for it to close
	teamHost  string
	clk       *fakeClock // the verifier's key-cache clock
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newAccess(t testing.TB) *access {
	t.Helper()
	a := &access{t: t, keys: map[string]*rsa.PrivateKey{"k1": newKey(t)}, clk: &fakeClock{now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}}
	a.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			a.mu.Lock()
			a.elsewhere++
			a.mu.Unlock()
			http.Error(w, "the redirect target must never be requested", http.StatusTeapot)
			return
		}
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		a.mu.Lock()
		a.fetches++
		mode, gate := a.mode, a.gate
		var set jose.JSONWebKeySet
		for kid, k := range a.keys {
			id := kid
			if mode == "nokid" {
				id = ""
			}
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &k.PublicKey, KeyID: id, Algorithm: "RS256", Use: "sig"})
		}
		a.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		switch mode {
		case "redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "503keys":
			// An error page that happens to carry a valid key set is still an error.
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(set)
		case "huge":
			_, _ = w.Write([]byte(`{"keys":[],"pad":"`))
			_, _ = w.Write(bytes.Repeat([]byte("x"), 2<<20))
			_, _ = w.Write([]byte(`"}`))
		case "503":
			http.Error(w, "maintenance: secret-upstream-detail", http.StatusServiceUnavailable)
		case "garbage":
			_, _ = w.Write([]byte("<html>captive portal secret-upstream-detail</html>"))
		case "empty":
			_, _ = w.Write([]byte(`{"keys":[]}`))
		case "drop":
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(set)
		}
	}))
	t.Cleanup(a.srv.Close)
	a.teamHost = strings.TrimPrefix(a.srv.URL, "https://")
	return a
}

func newKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (a *access) setMode(m string) { a.mu.Lock(); a.mode = m; a.mu.Unlock() }

func (a *access) fetchCount() int { a.mu.Lock(); defer a.mu.Unlock(); return a.fetches }

func (a *access) elsewhereCount() int { a.mu.Lock(); defer a.mu.Unlock(); return a.elsewhere }

// hold makes certs requests wait until the returned function is called.
func (a *access) hold() (release func()) {
	g := make(chan struct{})
	a.mu.Lock()
	a.gate = g
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.gate = nil
			a.mu.Unlock()
			close(g)
		})
	}
}

func (a *access) verifier() *CFAccessVerifier {
	a.t.Helper()
	v, err := newCFAccessVerifier(context.Background(), a.teamHost, testAUD, verifierOptions{hc: a.srv.Client(), now: a.clk.Now})
	if err != nil {
		a.t.Fatal(err)
	}
	return v
}

// tokenSpec describes one Access JWT; zero values are a valid human login.
type tokenSpec struct {
	Kid, Sub, Email, Issuer, CommonName string
	AUD                                 []string
	Expiry                              time.Time
	Key                                 *rsa.PrivateKey
	Iat                                 time.Time // zero = a minute before the fake clock
	NoIat                               bool
}

func (a *access) token(s tokenSpec) string {
	a.t.Helper()
	if s.Kid == "" {
		s.Kid = "k1"
	}
	if s.Issuer == "" {
		s.Issuer = a.srv.URL
	}
	if s.AUD == nil {
		s.AUD = []string{testAUD}
	}
	if s.Expiry.IsZero() {
		s.Expiry = time.Now().Add(time.Hour)
	}
	key := s.Key
	if key == nil {
		a.mu.Lock()
		key = a.keys[s.Kid]
		a.mu.Unlock()
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: s.Kid}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		a.t.Fatal(err)
	}
	claims := map[string]any{"iss": s.Issuer, "aud": s.AUD, "sub": s.Sub, "exp": s.Expiry.Unix()}
	if !s.NoIat {
		if s.Iat.IsZero() {
			s.Iat = a.clk.Now().Add(-time.Minute)
		}
		claims["iat"] = s.Iat.Unix()
	}
	if s.Email != "" {
		claims["email"] = s.Email
	}
	if s.CommonName != "" {
		claims["common_name"] = s.CommonName
	}
	raw, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

func human(email string) tokenSpec { return tokenSpec{Sub: "cf-sub-" + email, Email: email} }

// resolverFake records the policy and claims it was asked about.
type resolverFake struct {
	mu    sync.Mutex
	calls []coreoidc.Claims
	pols  []coreoidc.Policy
	user  *coreauth.User
	err   error
}

func (r *resolverFake) Resolve(_ context.Context, p coreoidc.Policy, c coreoidc.Claims) (*coreauth.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls, r.pols = append(r.calls, c), append(r.pols, p)
	if r.err != nil {
		return nil, r.err
	}
	return r.user, nil
}

func (r *resolverFake) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }

type rig struct {
	acc   *access
	res   *resolverFake
	owner string
	ext   coreauth.Extension
	logs  *syncBuf
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newRig(t testing.TB) *rig {
	t.Helper()
	g := &rig{acc: newAccess(t), res: &resolverFake{user: &coreauth.User{ID: "01JBATCAVE0000000000000020"}}, owner: ownerMail, logs: &syncBuf{}}
	ext, err := Extension(ExtensionConfig{
		Verifier: g.acc.verifier(), Resolver: g.res,
		Policy:     coreoidc.Policy{Provider: "cfaccess", AllowedDomains: []string{"wayne.example.test"}, LinkExistingByEmail: true},
		OwnerEmail: func() string { return g.owner },
		Log:        slog.New(slog.NewTextHandler(g.logs, nil)),
		Clock:      g.acc.clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	g.ext = ext
	return g
}

func (g *rig) do(raw string, mod func(*http.Request)) (*coreauth.Assertion, error) {
	r := httptest.NewRequest(http.MethodGet, "https://signal.example.test/api/v1/books", nil)
	if raw != "" {
		r.Header.Set(CFAccessHeader, raw)
	}
	if mod != nil {
		mod(r)
	}
	return g.ext.Authenticator.Authenticate(r)
}

func TestExtension_Registration(t *testing.T) {
	g := newRig(t)
	if g.ext.Spec.Method != "ext:cfaccess" || !g.ext.Spec.Interactive || !g.ext.Spec.OwnerVerifier || g.ext.Spec.MayChangeCredentials || g.ext.Spec.AppHandoff != nil {
		t.Fatalf("Spec = %+v", g.ext.Spec)
	}
	if g.ext.Order <= coreauth.OrderAPIKey || g.ext.Order == coreauth.OrderSession || g.ext.Order >= coreauth.OrderSession {
		t.Fatalf("Order = %d: must run after the API key and before the session authenticator", g.ext.Order)
	}
	if g.ext.Authenticator.Name() != "cfaccess" {
		t.Fatalf("Name = %q", g.ext.Authenticator.Name())
	}
	if !g.ext.OwnerSet() {
		t.Fatal("OwnerSet is false with an owner configured")
	}
	g.owner = "  "
	if g.ext.OwnerSet() {
		t.Fatal("OwnerSet is true with a blank owner")
	}
	g.owner = ""
	if g.ext.OwnerSet() {
		t.Fatal("OwnerSet is true with no owner")
	}
}

func TestExtension_RefusesAnUnusableConfig(t *testing.T) {
	g := newRig(t)
	ok := ExtensionConfig{
		Verifier: g.acc.verifier(), Resolver: g.res, Policy: coreoidc.Policy{Provider: "cfaccess"},
		OwnerEmail: func() string { return "" },
	}
	if _, err := Extension(ok); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*ExtensionConfig){
		"verifier":     func(c *ExtensionConfig) { c.Verifier = nil },
		"resolver":     func(c *ExtensionConfig) { c.Resolver = nil },
		"owner":        func(c *ExtensionConfig) { c.OwnerEmail = nil },
		"wrong policy": func(c *ExtensionConfig) { c.Policy.Provider = "gotham" },
	} {
		c := ok
		mod(&c)
		if _, err := Extension(c); err == nil {
			t.Errorf("Extension without %s succeeded", name)
		}
	}
}

// TestCFAccess_PresentedInvalidIs401_AbsentPassesThrough: no Access token is no
// credential (the next method decides); a token that does not verify is a
// presented invalid credential, which stage 12 turns into a 401 that no weaker
// method can rescue.
func TestCFAccess_PresentedInvalidIs401_AbsentPassesThrough(t *testing.T) {
	g := newRig(t)
	if _, err := g.do("", nil); !errors.Is(err, coreauth.ErrNoCredential) {
		t.Fatalf("no token: %v, want ErrNoCredential", err)
	}
	if _, err := g.do("   ", nil); !errors.Is(err, coreauth.ErrNoCredential) {
		t.Fatalf("blank header: %v, want ErrNoCredential", err)
	}
	stranger := newKey(t)
	for name, raw := range map[string]string{
		"garbage":               "not-a-jwt",
		"three dots of nothing": "a.b.c",
		"wrong audience":        g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, AUD: []string{"arkham-aud"}}),
		"wrong issuer":          g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Issuer: "https://arkham.example.test"}),
		"expired":               g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Expiry: time.Now().Add(-time.Hour)}),
		"signed by another key": g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Key: stranger}),
		"unknown kid":           g.acc.token(tokenSpec{Sub: "s", Email: ownerMail, Kid: "k-ghost", Key: stranger}),
	} {
		t.Run(name, func(t *testing.T) {
			a, err := g.do(raw, nil)
			if !errors.Is(err, coreauth.ErrInvalidCredentials) || a != nil {
				t.Fatalf("got %+v, %v; want ErrInvalidCredentials and no assertion", a, err)
			}
			if errors.Is(err, coreauth.ErrExtensionUnavailable) || errors.Is(err, coreauth.ErrNoCredential) {
				t.Fatalf("a forged token must not open to the next method: %v", err)
			}
		})
	}
	if g.res.count() != 0 {
		t.Fatal("an invalid token reached the resolver")
	}
	// The header is the token when both are present: an invalid header is not
	// rescued by a valid cookie.
	good := g.acc.token(human("dick@wayne.example.test"))
	_, err := g.do("forged", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: good}) })
	if !errors.Is(err, coreauth.ErrInvalidCredentials) {
		t.Fatalf("invalid header + valid cookie: %v", err)
	}
}

func TestCFAccess_ValidTokenFromHeaderOrCookie(t *testing.T) {
	g := newRig(t)
	raw := g.acc.token(human("dick@wayne.example.test"))
	a, err := g.do(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.User != "01JBATCAVE0000000000000020" || a.Owner || a.Scopes != nil || a.AuthTime.IsZero() ||
		a.CredentialID != "cfaccess:cf-sub-dick@wayne.example.test" {
		t.Fatalf("assertion = %+v", a)
	}
	if strings.Contains(a.CredentialID, raw) {
		t.Fatal("the credential id carries the token")
	}
	c := g.res.calls[0]
	p := g.res.pols[0]
	if c.Provider != "cfaccess" || c.Subject != "cf-sub-dick@wayne.example.test" || c.Email != "dick@wayne.example.test" || !c.EmailVerified {
		t.Fatalf("claims = %+v", c)
	}
	if p.Provider != "cfaccess" || !p.LinkExistingByEmail || len(p.AllowedDomains) != 1 {
		t.Fatalf("policy = %+v", p)
	}
	// Cookie only.
	a, err = g.do("", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: raw}) })
	if err != nil || a.User == "" {
		t.Fatalf("cookie: %+v, %v", a, err)
	}
}

// TestCFAccess_JWKSOutageFallsThrough: when Cloudflare's keys cannot be
// fetched the token is neither accepted nor called forged: the extension
// reports ErrExtensionUnavailable and grants nothing, and stage 12 opens to
// the next method.
func TestCFAccess_JWKSOutageFallsThrough(t *testing.T) {
	for _, mode := range []string{"503", "garbage", "empty", "drop"} {
		t.Run(mode, func(t *testing.T) {
			g := newRig(t)
			g.acc.setMode(mode)
			a, err := g.do(g.acc.token(human(ownerMail)), nil)
			if !errors.Is(err, coreauth.ErrExtensionUnavailable) || a != nil {
				t.Fatalf("got %+v, %v; want ErrExtensionUnavailable and no assertion", a, err)
			}
			if errors.Is(err, coreauth.ErrInvalidCredentials) {
				t.Fatalf("an outage must not read as a forged token: %v", err)
			}
			if strings.Contains(err.Error(), g.acc.srv.URL) || strings.Contains(err.Error(), "secret-upstream-detail") {
				t.Fatalf("the error quotes the fetch: %v", err)
			}
			if g.res.count() != 0 {
				t.Fatal("an unverified token reached the resolver")
			}
			// Recovery: the first request after the fetch window refetches and verifies.
			g.acc.setMode("")
			g.acc.clk.advance(keysMissWindow + time.Second)
			if _, err := g.do(g.acc.token(human(ownerMail)), nil); err != nil {
				t.Fatalf("after the outage: %v", err)
			}
		})
	}
}

// A valid Access token that names no person is a service token: it identifies
// nobody, so it is no credential for this extension and the request goes on
// to the bearer it also carries.
func TestCFAccess_ServiceTokenIsNotAnIdentity(t *testing.T) {
	g := newRig(t)
	raw := g.acc.token(tokenSpec{Sub: "svc", CommonName: "friends-token-client-id"})
	a, err := g.do(raw, nil)
	if !errors.Is(err, coreauth.ErrNoCredential) || a != nil {
		t.Fatalf("got %+v, %v; want ErrNoCredential", a, err)
	}
	if g.res.count() != 0 {
		t.Fatal("a service token's common_name resolved a user")
	}
	// The common name travels in the verifier's typed error only.
	_, verr := g.ext.Authenticator.(*extension).c.Verifier.VerifyIssued(context.Background(), raw)
	if CFAssertionCommonName(verr) != "friends-token-client-id" {
		t.Fatalf("Verify error = %v", verr)
	}
}

// The unsigned convenience headers and forwarding headers are never identity.
func TestCFAccess_ForwardedHeadersAreNeverIdentity(t *testing.T) {
	g := newRig(t)
	spoof := func(r *http.Request) {
		r.Header.Set("Cf-Access-Authenticated-User-Email", ownerMail)
		r.Header.Set("X-Forwarded-Email", ownerMail)
		r.Header.Set("X-Forwarded-User", ownerMail)
		r.Header.Set("X-Forwarded-For", "192.0.2.7")
		r.Header.Set("X-Auth-Request-Email", ownerMail)
	}
	if a, err := g.do("", spoof); !errors.Is(err, coreauth.ErrNoCredential) || a != nil {
		t.Fatalf("spoofed headers alone: %+v, %v", a, err)
	}
	// With a valid token for someone else, the header claiming the owner
	// changes nothing: the resolver saw the JWT's identity and no owner proof.
	a, err := g.do(g.acc.token(human("dick@wayne.example.test")), spoof)
	if err != nil {
		t.Fatal(err)
	}
	if a.Owner || g.res.calls[0].Email != "dick@wayne.example.test" {
		t.Fatalf("assertion %+v, resolver saw %+v", a, g.res.calls)
	}
	if g.res.count() != 1 {
		t.Fatalf("resolver calls = %d", g.res.count())
	}
}

// TestOwnerGuard_RequiresOwnerAssurance: only a verified Access token whose
// e-mail is the configured owner's yields the owner assurance; the Assertion
// is the extension's whole statement, and it never carries scopes.
func TestOwnerGuard_RequiresOwnerAssurance(t *testing.T) {
	g := newRig(t)
	for _, tc := range []struct {
		name, email, owner string
		want               bool
	}{
		{"the owner", ownerMail, ownerMail, true},
		{"owner, case differs", "Bruce@Wayne.Example.Test", ownerMail, true},
		{"configured owner has padding", ownerMail, "  " + ownerMail + " ", true},
		{"another Access user", "dick@wayne.example.test", ownerMail, false},
		{"no owner configured", ownerMail, "", false},
		{"blank owner", ownerMail, "   ", false},
		{"prefix of the owner", "bruce@wayne.example.tes", ownerMail, false},
		{"owner as a sub-address", "bruce+x@wayne.example.test", ownerMail, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g.owner = tc.owner
			a, err := g.do(g.acc.token(human(tc.email)), nil)
			if err != nil {
				t.Fatal(err)
			}
			if a.Owner != tc.want {
				t.Fatalf("Owner = %v, want %v", a.Owner, tc.want)
			}
			if a.Scopes != nil {
				t.Fatalf("an extension assertion must not carry scopes: %v", a.Scopes)
			}
		})
	}
	t.Run("an owner_email change takes effect at once", func(t *testing.T) {
		g.owner = "alfred@wayne.example.test"
		a, _ := g.do(g.acc.token(human(ownerMail)), nil)
		if a.Owner {
			t.Fatal("the old owner kept the assurance")
		}
		g.owner = ownerMail
		a, _ = g.do(g.acc.token(human(ownerMail)), nil)
		if !a.Owner {
			t.Fatal("the new owner did not get it")
		}
	})
}

// TestOwnerGate_AccessWithoutOwnerFallsBack: an Access sign-in that is not the
// owner is an ordinary user (no owner assurance), so an owner-gated route
// refuses it and a core owner gate falls back; and the resolver's refusals map
// the way stage 12 expects.
func TestOwnerGate_AccessWithoutOwnerFallsBack(t *testing.T) {
	g := newRig(t)
	raw := g.acc.token(human("dick@wayne.example.test"))
	a, err := g.do(raw, nil)
	if err != nil || a.Owner || a.User == "" {
		t.Fatalf("a non-owner Access login: %+v, %v", a, err)
	}
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"not allowed falls through", coreoidc.ErrEmailNotAllowed, coreauth.ErrNoCredential},
		{"email in use falls through", coreauth.ErrEmailInUse, coreauth.ErrNoCredential},
		{"unverified falls through", coreoidc.ErrEmailNotVerified, coreauth.ErrNoCredential},
		{"inactive is refused as such", coreauth.ErrInactiveUser, coreauth.ErrInactiveUser},
		{"reset required is refused as such", coreauth.ErrResetRequired, coreauth.ErrResetRequired},
		{"store down passes through for a 503", coreauth.ErrStoreUnavailable, coreauth.ErrStoreUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g.res.err = tc.err
			a, err := g.do(raw, nil)
			if !errors.Is(err, tc.want) || a != nil {
				t.Fatalf("got %+v, %v; want %v", a, err, tc.want)
			}
			if tc.want == coreauth.ErrNoCredential && (errors.Is(err, coreauth.ErrInactiveUser) || errors.Is(err, coreauth.ErrStoreUnavailable)) {
				t.Fatalf("mapped to the wrong class: %v", err)
			}
		})
	}
}
