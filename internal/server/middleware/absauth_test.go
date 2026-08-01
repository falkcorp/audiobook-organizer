// file: internal/server/middleware/absauth_test.go
// version: 1.1.0
// guid: b41e6d09-8a37-4f52-9c10-25d7b8e0f346
// last-edited: 2026-08-01

package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/gin-gonic/gin"
)

const absTestSecret = "0123456789abcdef0123456789abcdef"

// ── fakes ───────────────────────────────────────────────────────────────────

type fakeCFVerifier struct {
	// byToken maps a raw assertion to the identity it verifies to. Anything not in
	// the map fails verification, which is what a forged/malformed header looks like.
	byToken map[string]*oauth.IdentityClaims
	// nonIdentity holds assertions that are cryptographically VALID but carry no
	// email claim -- the shape Cloudflare mints for a service token. Distinct from
	// "not in byToken", which means the token did not verify at all. nil by default,
	// so existing tests are unaffected.
	nonIdentity map[string]bool
	calls       int
	mu          sync.Mutex
}

func (f *fakeCFVerifier) Verify(_ context.Context, raw string) (*oauth.IdentityClaims, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.nonIdentity[raw] {
		// Mirrors the real verifier: signature/issuer/aud all PASSED, there is simply
		// no person named in the token.
		return nil, fmt.Errorf("%w (sub=%q)", oauth.ErrNonIdentityAssertion, "svc-token")
	}
	if c, ok := f.byToken[raw]; ok {
		return c, nil
	}
	return nil, errors.New("cfaccess: verify jwt: signature is invalid")
}

type fakeABSStore struct {
	mu         sync.Mutex
	users      map[string]*database.User
	byUsername map[string]*database.User
	byEmail    map[string]*database.User
	identities map[string]*database.OAuthIdentity
	sessions   map[string]*database.ABSSession
	roles      map[string]*database.Role
	createErr  error
	sessionErr error
	nextID     int
}

func newFakeABSStore() *fakeABSStore {
	return &fakeABSStore{
		users:      map[string]*database.User{},
		byUsername: map[string]*database.User{},
		byEmail:    map[string]*database.User{},
		identities: map[string]*database.OAuthIdentity{},
		sessions:   map[string]*database.ABSSession{},
		roles:      map[string]*database.Role{},
	}
}

func (f *fakeABSStore) addUser(u *database.User) *database.User {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
	f.byUsername[strings.ToLower(u.Username)] = u
	if u.Email != "" {
		f.byEmail[strings.ToLower(u.Email)] = u
	}
	return u
}

func (f *fakeABSStore) addSession(s *database.ABSSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.ID] = s
}

func (f *fakeABSStore) GetUserByID(id string) (*database.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[id], nil
}

func (f *fakeABSStore) GetUserByUsername(u string) (*database.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byUsername[strings.ToLower(u)], nil
}

func (f *fakeABSStore) GetUserByEmail(e string) (*database.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byEmail[strings.ToLower(e)], nil
}

func (f *fakeABSStore) CreateUser(username, email, algo, hash string, roles []string, status string) (*database.User, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.mu.Lock()
	f.nextID++
	id := fmt.Sprintf("jit-%d", f.nextID)
	f.mu.Unlock()
	return f.addUser(&database.User{
		ID: id, Username: username, Email: email,
		PasswordHashAlgo: algo, PasswordHash: hash, Roles: roles, Status: status,
		CreatedAt: time.Now(),
	}), nil
}

func (f *fakeABSStore) GetOAuthIdentityByProviderSubject(provider, subject string) (*database.OAuthIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identities[provider+"|"+subject], nil
}

func (f *fakeABSStore) CreateOAuthIdentity(i *database.OAuthIdentity) (*database.OAuthIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i.ID = i.Provider + "|" + i.Subject
	f.identities[i.ID] = i
	return i, nil
}

func (f *fakeABSStore) GetRoleByID(id string) (*database.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.roles[id], nil
}

func (f *fakeABSStore) GetABSSession(id string) (*database.ABSSession, error) {
	if f.sessionErr != nil {
		return nil, f.sessionErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[id], nil
}

// ── harness ─────────────────────────────────────────────────────────────────

type absHarness struct {
	router   *gin.Engine
	store    *fakeABSStore
	cfg      *absauth.Config
	verifier *fakeCFVerifier
	// resolver is exposed so tests can call ResolveCFAssertion directly. That is
	// the exact entry point POST /login and POST /auth/refresh use, and its
	// (nil, nil) return is what "fall through to the password check" means.
	resolver *ABSIdentityResolver
}

func newABSHarness(t *testing.T, modes string, allowed []string) *absHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: absTestSecret, AuthModes: modes})
	if err != nil {
		t.Fatalf("absauth.Load: %v", err)
	}
	store := newFakeABSStore()
	verifier := &fakeCFVerifier{byToken: map[string]*oauth.IdentityClaims{}}
	oauthCfg := oauth.New(oauth.Config{AllowedEmails: allowed, DefaultRole: "viewer"})

	resolver := NewABSIdentityResolver(cfg, verifier, oauthCfg, store)
	r := gin.New()
	r.GET("/api/me", ABSRequireAuth(resolver), func(c *gin.Context) {
		u, _ := CurrentUser(c)
		c.JSON(http.StatusOK, gin.H{
			"user": u.ID, "mode": ABSAuthMode(c), "sid": ABSSessionID(c),
		})
	})
	return &absHarness{router: r, store: store, cfg: cfg, verifier: verifier, resolver: resolver}
}

func (h *absHarness) do(method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

func (h *absHarness) mintAccess(t *testing.T, userID, sessionID string) string {
	t.Helper()
	tok, _, err := h.cfg.MintAccessToken(userID, sessionID, time.Now())
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	return tok
}

func liveABSSession(id, userID string) *database.ABSSession {
	now := time.Now()
	return &database.ABSSession{ID: id, UserID: userID, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)}
}

func activeUser(id, username string) *database.User {
	return &database.User{ID: id, Username: username, Email: username + "@example.com", Status: "active"}
}

// ── MANDATED SECURITY TEST: no fail-open on the ABS surface ─────────────────

// TestABSAuth_InvalidAssertionIsHard401_NotFailOpen is the mandated security test
// (spec §3.0.1 final bullet). The existing CloudflareAccessAuth middleware on
// /api/v1 is FAIL-OPEN by design: an unverifiable assertion simply falls through.
// On the ABS surface that behaviour would be an authentication bypass, because there
// is no second gate behind it. A malformed, forged, expired or wrong-AUD assertion
// must be a hard 401.
func TestABSAuth_InvalidAssertionIsHard401_NotFailOpen(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.store.addUser(activeUser("u1", "owner"))

	for _, assertion := range []string{
		"not-a-jwt",
		"eyJhbGciOiJub25lIn0.eyJlbWFpbCI6Im93bmVyQGV4YW1wbGUuY29tIn0.",
		"a.b.c",
		"   ",
	} {
		w := h.do(http.MethodGet, "/api/me", map[string]string{oauth.CFAccessHeader: assertion})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("assertion %q: got %d, want 401 — a fail-open path here is an authentication bypass (body: %s)",
				assertion, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"user"`) {
			t.Fatalf("assertion %q resolved a user: %s", assertion, w.Body.String())
		}
	}
}

// TestABSAuth_InvalidAssertionDoesNotFallThroughToValidBearer proves the assertion
// check is a hard gate rather than a preference: a request carrying a BAD assertion
// alongside a perfectly good bearer token must still be rejected. Anything else lets
// an attacker probe the CF path for free.
func TestABSAuth_InvalidAssertionDoesNotFallThroughToValidBearer(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))
	bearer := h.mintAccess(t, "u1", "s1")

	w := h.do(http.MethodGet, "/api/me", map[string]string{
		oauth.CFAccessHeader: "forged",
		"Authorization":      "Bearer " + bearer,
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: an invalid assertion must not silently fall through to the bearer path", w.Code)
	}
}

// TestABSAuth_SpoofableEmailHeaderIsNeverTrusted: Cf-Access-Authenticated-User-Email
// is unsigned and trivially forged on a direct-to-origin request.
func TestABSAuth_SpoofableEmailHeaderIsNeverTrusted(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.store.addUser(activeUser("u1", "owner"))

	w := h.do(http.MethodGet, "/api/me", map[string]string{
		"Cf-Access-Authenticated-User-Email": "owner@example.com",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: the unsigned email header must never authenticate", w.Code)
	}
	if h.verifier.calls != 0 {
		t.Fatal("the email header must not even reach the verifier")
	}
}

// TestABSAuth_NonAllowlistedEmailIs403AndNeverProvisions pins the fail-closed JIT
// rule: verified is NOT authorized.
func TestABSAuth_NonAllowlistedEmailIs403AndNeverProvisions(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.verifier.byToken["good-stranger"] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|stranger",
		Email: "stranger@example.com", EmailVerified: true,
	}

	w := h.do(http.MethodGet, "/api/me", map[string]string{oauth.CFAccessHeader: "good-stranger"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 for a verified-but-not-allowlisted email", w.Code)
	}
	if len(h.store.users) != 0 {
		t.Fatalf("an unlisted email must NEVER be auto-provisioned, got users %+v", h.store.users)
	}
}

// ── Mode C/A: verified assertion + JIT provisioning ─────────────────────────

func TestABSAuth_VerifiedAssertionJITProvisionsAllowlistedUser(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.verifier.byToken["good"] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|owner",
		Email: "owner@example.com", EmailVerified: true,
	}

	w := h.do(http.MethodGet, "/api/me", map[string]string{oauth.CFAccessHeader: "good"})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"mode":"cf"`) {
		t.Fatalf("expected mode cf, got %s", w.Body.String())
	}
	if len(h.store.users) != 1 {
		t.Fatalf("expected exactly one JIT-provisioned user, got %d", len(h.store.users))
	}
	for _, u := range h.store.users {
		// A CF-identified user has no password credential and must not be loginable
		// by password.
		if ok, _ := absauth.VerifyPassword(u.PasswordHashAlgo, u.PasswordHash, ""); ok {
			t.Fatal("a JIT-provisioned user must not authenticate with an empty password")
		}
	}

	// Second request must reuse the same user, not mint another.
	w2 := h.do(http.MethodGet, "/api/me", map[string]string{oauth.CFAccessHeader: "good"})
	if w2.Code != http.StatusOK {
		t.Fatalf("second request got %d", w2.Code)
	}
	if len(h.store.users) != 1 {
		t.Fatalf("JIT provisioning must be idempotent, got %d users", len(h.store.users))
	}
}

func TestABSAuth_AssertionWinsOverBearer(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", []string{"owner@example.com"})
	cfUser := h.store.addUser(activeUser("cf-user", "owner"))
	h.store.addUser(activeUser("jwt-user", "someoneelse"))
	h.store.addSession(liveABSSession("s1", "jwt-user"))
	h.verifier.byToken["good"] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|owner",
		Email: cfUser.Email, EmailVerified: true,
	}

	w := h.do(http.MethodGet, "/api/me", map[string]string{
		oauth.CFAccessHeader: "good",
		"Authorization":      "Bearer " + h.mintAccess(t, "jwt-user", "s1"),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"user":"cf-user"`) {
		t.Fatalf("the verified assertion must win over the bearer (§3.0.1 order): %s", w.Body.String())
	}
}

// ── Mode B: our own bearer JWT ──────────────────────────────────────────────

func TestABSAuth_BearerJWTResolves(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))

	w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + h.mintAccess(t, "u1", "s1")})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"mode":"jwt"`) || !strings.Contains(body, `"sid":"s1"`) {
		t.Fatalf("unexpected body %s", body)
	}
}

// TestABSAuth_TokenQueryParamOnGET is mandatory, not optional: §1.7.2 verified that
// Absorb appends ?token= to cover, author-image and file URLs, and CarPlay does too.
func TestABSAuth_TokenQueryParamOnGET(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))

	w := h.do(http.MethodGet, "/api/me?token="+h.mintAccess(t, "u1", "s1"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("?token= on a GET must authenticate, got %d: %s", w.Code, w.Body.String())
	}
}

func TestABSAuth_NoCredentialIs401(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	if w := h.do(http.MethodGet, "/api/me", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
}

func TestABSAuth_RejectsGarbageAndTamperedBearer(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))
	good := h.mintAccess(t, "u1", "s1")
	tampered := good[:len(good)-4] + "AAAA"

	for name, tok := range map[string]string{
		"garbage":     "nonsense",
		"tampered":    tampered,
		"api key":     "abk_deadbeef",
		"empty":       "",
		"prefix only": "Bearer",
	} {
		w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + tok})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s token got %d, want 401", name, w.Code)
		}
	}
}

// TestABSAuth_RevokedSessionStopsWorking: the access JWT is still cryptographically
// valid, so revocation MUST be enforced against the abs_sess record.
func TestABSAuth_RevokedSessionStopsWorking(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	h.store.addUser(activeUser("u1", "owner"))
	s := liveABSSession("s1", "u1")
	h.store.addSession(s)
	tok := h.mintAccess(t, "u1", "s1")
	if w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + tok}); w.Code != 200 {
		t.Fatalf("precondition failed: %d", w.Code)
	}
	s.Revoked = true
	if w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + tok}); w.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked session must stop authenticating even with a valid JWT, got %d", w.Code)
	}
}

func TestABSAuth_ExpiredSessionStopsWorking(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	h.store.addUser(activeUser("u1", "owner"))
	s := liveABSSession("s1", "u1")
	s.ExpiresAt = time.Now().Add(-time.Minute)
	h.store.addSession(s)
	w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + h.mintAccess(t, "u1", "s1")})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401 for an expired session", w.Code)
	}
}

func TestABSAuth_UnknownSessionIs401(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	h.store.addUser(activeUser("u1", "owner"))
	w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + h.mintAccess(t, "u1", "ghost")})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401 for a token naming a session that does not exist", w.Code)
	}
}

func TestABSAuth_InactiveUserIs403(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	u := activeUser("u1", "owner")
	u.Status = "suspended"
	h.store.addUser(u)
	h.store.addSession(liveABSSession("s1", "u1"))
	w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + h.mintAccess(t, "u1", "s1")})
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d want 403 for a suspended user", w.Code)
	}
}

// TestABSAuth_StoreErrorIs500NotUnauthorized: §1.7.3 item 3 — a transient failure
// must never be reported as a dead credential.
func TestABSAuth_StoreErrorIs500NotUnauthorized(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))
	h.store.sessionErr = errors.New("pebble: temporarily unavailable")

	w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + h.mintAccess(t, "u1", "s1")})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500: a transient store error must not be reported as an invalid credential", w.Code)
	}
}

// ── ABS_AUTH_MODES gating ───────────────────────────────────────────────────

func TestABSAuth_CFOnlyModeRejectsBearer(t *testing.T) {
	h := newABSHarness(t, "cf", []string{"owner@example.com"})
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))
	w := h.do(http.MethodGet, "/api/me", map[string]string{"Authorization": "Bearer " + h.mintAccess(t, "u1", "s1")})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("ABS_AUTH_MODES=cf must reject our own bearer token, got %d", w.Code)
	}
}

func TestABSAuth_JWTOnlyModeIgnoresAssertion(t *testing.T) {
	h := newABSHarness(t, "jwt", []string{"owner@example.com"})
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))
	h.verifier.byToken["good"] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|owner",
		Email: "owner@example.com", EmailVerified: true,
	}
	// With cf disabled the assertion is not consulted at all, so a valid bearer wins
	// and a lone assertion is a 401.
	w := h.do(http.MethodGet, "/api/me", map[string]string{
		oauth.CFAccessHeader: "good",
		"Authorization":      "Bearer " + h.mintAccess(t, "u1", "s1"),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if h.verifier.calls != 0 {
		t.Fatal("cf resolver is disabled; the verifier must not be called")
	}
	if w := h.do(http.MethodGet, "/api/me", map[string]string{oauth.CFAccessHeader: "good"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("assertion-only request in jwt-only mode should 401, got %d", w.Code)
	}
}

// TestABSAuth_NilResolverDeniesEverything guards against a wiring mistake turning
// into an open door.
func TestABSAuth_NilResolverDeniesEverything(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/me", ABSRequireAuth(nil), func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a nil resolver must deny, got %d", w.Code)
	}
}

// TestABSAuth_ResponseBodyIsJSONNotHTML pins §1.8.6 / §1.7.3 item 11: under /api/ a
// 200 with an HTML body is fatal to both clients' decoders, and an error body should
// still be JSON for hygiene.
func TestABSAuth_ResponseBodyIsJSONNotHTML(t *testing.T) {
	h := newABSHarness(t, "cf,jwt", nil)
	w := h.do(http.MethodGet, "/api/me", nil)
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type %q must be JSON", ct)
	}
	if strings.Contains(w.Body.String(), "<html") || strings.Contains(w.Body.String(), "<!DOCTYPE") {
		t.Fatalf("body must never be HTML under /api/: %s", w.Body.String())
	}
	if w.Body.Len() == 0 {
		t.Fatal("an empty body is fatal to these decoders")
	}
}
