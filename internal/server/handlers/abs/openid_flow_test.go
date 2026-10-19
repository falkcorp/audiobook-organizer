// file: internal/server/handlers/abs/openid_flow_test.go
// version: 1.2.0
// guid: 4f0b6c2e-9a71-4d38-b5e2-7c1d93a8f046
// last-edited: 2026-10-19

package abs_test

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	abshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/abs"
)

// End-to-end tests of the two-hop SSO flow as AudioBooth actually drives it
// (see the file comment in openid.go):
//
//	hop 1  GET  /auth/openid           app URLSession, service token only
//	hop 2  GET  /auth/openid/browser   system browser, CF Access person → consent page
//	       POST /auth/openid/browser   the page's Allow → code
//	hop 3  GET  /auth/openid/callback  app URLSession, code + verifier
//
// The regression these pin: hop 1 used to 302 straight to audiobooth://oauth,
// and AudioBooth hands hop 1's Location to ASWebAuthenticationSession, which
// crashes on a custom-scheme URL.

const (
	oidcPersonAssertion  = "person-assertion"
	oidcServiceAssertion = "service-token-assertion"
	oidcStrangerAssert   = "stranger-assertion"
	oidcSecondPerson     = "second-person-assertion"
	oidcOtherService     = "other-service-token-assertion"
	oidcNoCNService      = "service-token-without-common-name"
	oidcVerifier         = "audiobooth-verifier-0123456789-abcdefghijklmnopqrstuvwxyz"
)

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// newOIDCHarness is CF mode with one allowlisted person and one service token.
// httptest requests arrive with Host "example.com".
func newOIDCHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv(abshandler.OIDCRedirectURIsEnvVar, "")
	h := newHarness(t, "cf,jwt", []string{"owner@example.com", "second@example.com"})
	// As server.go configures production: trust no forwarded headers, so
	// c.ClientIP() is the connection's address.
	if err := h.router.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	h.verifier.byToken[oidcPersonAssertion] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|owner",
		Email: "owner@example.com", EmailVerified: true,
	}
	h.verifier.byToken[oidcStrangerAssert] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|stranger",
		Email: "stranger@example.com", EmailVerified: true,
	}
	h.verifier.byToken[oidcSecondPerson] = &oauth.IdentityClaims{
		Provider: oauth.ProviderCFAccess, Subject: "cf|second",
		Email: "second@example.com", EmailVerified: true,
	}
	h.verifier.serviceTokens[oidcServiceAssertion] = "family"
	h.verifier.serviceTokens[oidcOtherService] = "friends"
	h.verifier.serviceTokens[oidcNoCNService] = ""
	return h
}

// authorizePath is AudioBooth's exact hop-1 URL (OIDCAuthenticationManager
// buildOIDCURL), plus an optional state it does not itself send.
func authorizePath(challenge, state string) string {
	q := url.Values{}
	q.Set("client_id", "AudioBooth")
	q.Set("response_type", "code")
	q.Set("scope", "openid")
	q.Set("redirect_uri", "audiobooth://oauth")
	q.Set("callback", "audiobooth://oauth")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if state != "" {
		q.Set("state", state)
	}
	return "/auth/openid?" + q.Encode()
}

// hop1From runs hop 1 with forged Cf-Connecting-Ip and X-Forwarded-For headers,
// which the origin cannot authenticate and must ignore.
func (h *harness) hop1From(t *testing.T, assertion, deviceIP string) *httptest.ResponseRecorder {
	t.Helper()
	w, _ := h.do(t, request{method: http.MethodGet, path: authorizePath(s256(oidcVerifier), ""), headers: map[string]string{
		oauth.CFAccessHeader: assertion, "Cf-Connecting-Ip": deviceIP, "X-Forwarded-For": deviceIP,
	}})
	return w
}

func (h *harness) get(t *testing.T, path, assertion string) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{}
	if assertion != "" {
		headers[oauth.CFAccessHeader] = assertion
	}
	w, _ := h.do(t, request{method: http.MethodGet, path: path, headers: headers})
	return w
}

// assertBrowserHopLocation is the core regression assertion: an ABSOLUTE https
// URL on this server's own origin, pointing at hop 2 with a well-formed req id.
// "Not audiobooth://" alone would pass on a relative URL, which crashes the app
// just the same (it runs URL(string:) and hands the result to
// ASWebAuthenticationSession).
func assertBrowserHopLocation(t *testing.T, w *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if w.Code != http.StatusFound {
		t.Fatalf("hop 1 status = %d, want 302; body %q", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location %q does not parse: %v", loc, err)
	}
	if !u.IsAbs() || u.Scheme != "https" || u.Host != "example.com" {
		t.Fatalf("Location %q must be an absolute https URL on this origin (example.com)", loc)
	}
	if u.Path != abshandler.OIDCBrowserPath {
		t.Fatalf("Location path = %q, want %q", u.Path, abshandler.OIDCBrowserPath)
	}
	if strings.Contains(loc, "audiobooth") || u.Query().Get("code") != "" {
		t.Fatalf("Location %q must carry neither the client scheme nor a code", loc)
	}
	if req := u.Query().Get("req"); len(req) != 43 {
		t.Fatalf("req id %q must be a 43-char random id", req)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	return u
}

// hop1 runs hop 1 the way the app does (service token only) and returns the
// browser-hop path to load next.
func (h *harness) hop1(t *testing.T, challenge, state string) string {
	t.Helper()
	u := assertBrowserHopLocation(t, h.get(t, authorizePath(challenge, state), oidcServiceAssertion))
	return u.RequestURI()
}

// appCallback parses a hop-2 Location that must target the client's callback.
func appCallback(t *testing.T, w *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther {
		t.Fatalf("hop 2 status = %d, want 302/303; body %q", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "audiobooth://oauth?") {
		t.Fatalf("hop 2 Location %q must target the stored redirect_uri", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location %q does not parse: %v", loc, err)
	}
	return u.Query()
}

func (h *harness) redeem(t *testing.T, code, verifier, extra string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/auth/openid/callback?code=" + url.QueryEscape(code) + "&code_verifier=" + url.QueryEscape(verifier) + extra
	// The app's callback carries only its service token.
	return h.get(t, path, oidcServiceAssertion)
}

// assertDeadLink: an unknown, expired or used req id is answered inline, never
// by redirecting anywhere — there is no trusted target for such an id.
func assertDeadLink(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %q", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("a dead link must not redirect; Location %q", loc)
	}
}

var (
	consentReqRe  = regexp.MustCompile(`name="req" value="([^"]+)"`)
	consentCSRFRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
)

// consentPage loads hop 2 and asserts it rendered the consent page — and
// minted nothing: no redirect, no code anywhere in the response.
func (h *harness) consentPage(t *testing.T, browser, assertion string) (req, csrf, body string) {
	t.Helper()
	w := h.get(t, browser, assertion)
	if w.Code != http.StatusOK {
		t.Fatalf("hop 2 GET = %d, want 200 consent page; Location %q body %q", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("hop 2 GET must not redirect; Location %q", loc)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if xfo := w.Header().Get("X-Frame-Options"); xfo != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY (a framable consent page can be clickjacked)", xfo)
	}
	body = w.Body.String()
	if strings.Contains(body, "code=") {
		t.Fatal("the consent page must not carry a code")
	}
	m1, m2 := consentReqRe.FindStringSubmatch(body), consentCSRFRe.FindStringSubmatch(body)
	if m1 == nil || m2 == nil {
		t.Fatalf("consent page lacks req/csrf fields: %s", body)
	}
	return m1[1], m2[1], body
}

// postConsent submits the consent form as a browser would.
func (h *harness) postConsent(t *testing.T, req, csrf, decision, assertion string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("req", req)
	form.Set("csrf", csrf)
	form.Set("decision", decision)
	r := httptest.NewRequest(http.MethodPost, abshandler.OIDCBrowserPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if assertion != "" {
		r.Header.Set(oauth.CFAccessHeader, assertion)
	}
	for k, v := range extra {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, r)
	return w
}

// allow runs hop 2 to completion for assertion: consent page, then Allow.
func (h *harness) allow(t *testing.T, browser, assertion string) url.Values {
	t.Helper()
	req, csrf, _ := h.consentPage(t, browser, assertion)
	return appCallback(t, h.postConsent(t, req, csrf, "allow", assertion, nil))
}

func assertRefused403(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusForbidden || w.Header().Get("Location") != "" {
		t.Fatalf("status %d Location %q; want 403 and no redirect", w.Code, w.Header().Get("Location"))
	}
}

// ── hop 1 ──────────────────────────────────────────────────────────────────

func TestOpenIDHop1_RedirectsToHTTPSBrowserHop(t *testing.T) {
	for _, state := range []string{"", "client-state-1"} {
		h := newOIDCHarness(t)
		w := h.get(t, authorizePath(s256(oidcVerifier), state), oidcServiceAssertion)
		assertBrowserHopLocation(t, w)
	}
}

// Hop 1 resolves no identity even when a person assertion is present. A
// regression that restored "mint on hop 1" would put audiobooth://…?code= in
// this Location, which is the crash.
func TestOpenIDHop1_PersonAssertionStillGoesToBrowserHop(t *testing.T) {
	h := newOIDCHarness(t)
	w := h.get(t, authorizePath(s256(oidcVerifier), "s"), oidcPersonAssertion)
	assertBrowserHopLocation(t, w)
	if n := len(h.store.users); n != 0 {
		t.Fatalf("hop 1 provisioned %d users; it must resolve no identity", n)
	}
}

func TestOpenIDHop1_ErrorsAre400PlainTextNeverRedirects(t *testing.T) {
	good := s256(oidcVerifier)
	base := func(mut func(q url.Values)) string {
		u, _ := url.Parse(authorizePath(good, ""))
		q := u.Query()
		mut(q)
		return "/auth/openid?" + q.Encode()
	}
	cases := []struct {
		name, path, wantBody string
	}{
		{"no redirect_uri", base(func(q url.Values) { q.Del("redirect_uri"); q.Del("callback") }), "redirect_uri is required"},
		// Real ABS's exact wording; AudioBooth matches it to show its setup hint.
		{"unregistered redirect_uri", base(func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") }), "Invalid redirect_uri"},
		{"plain method", base(func(q url.Values) { q.Set("code_challenge_method", "plain") }), ""},
		{"no challenge", base(func(q url.Values) { q.Del("code_challenge") }), ""},
		{"malformed challenge", base(func(q url.Values) { q.Set("code_challenge", "short") }), ""},
		{"state too long", base(func(q url.Values) { q.Set("state", strings.Repeat("s", 513)) }), "state is too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOIDCHarness(t)
			w := h.get(t, tc.path, oidcServiceAssertion)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %q", w.Code, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != "" {
				t.Fatalf("hop 1 must never redirect on error; Location %q", loc)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
				t.Fatalf("Content-Type = %q, want text/plain (the app shows the body as a string)", ct)
			}
			if tc.wantBody != "" && w.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", w.Body.String(), tc.wantBody)
			}
		})
	}
}

// A Host that is not a bare host[:port] is refused, not echoed into Location.
func TestOpenIDHop1_RejectsMalformedHost(t *testing.T) {
	h := newOIDCHarness(t)
	r := httptest.NewRequest(http.MethodGet, authorizePath(s256(oidcVerifier), ""), nil)
	r.Host = "evil.example/@x"
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Fatalf("status %d Location %q; want 400 and no Location", w.Code, w.Header().Get("Location"))
	}
}

// Hop 1 requires a VERIFIED Cloudflare credential: it is the quota key, and a
// request that did not come through Access could never complete at hop 2.
func TestOpenIDHop1_RequiresVerifiedCredential(t *testing.T) {
	for _, assertion := range []string{"", "forged-assertion"} {
		h := newOIDCHarness(t)
		w := h.get(t, authorizePath(s256(oidcVerifier), ""), assertion)
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
			t.Fatalf("assertion %q: status %d Location %q; want 400 and no Location", assertion, w.Code, w.Header().Get("Location"))
		}
	}
}

// Without cf in ABS_AUTH_MODES no browser could ever complete; say so up front.
func TestOpenIDHop1_SSOUnavailableWithoutCFMode(t *testing.T) {
	h := newHarness(t, "jwt", []string{"owner@example.com"})
	w := h.get(t, authorizePath(s256(oidcVerifier), ""), "")
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Fatalf("status %d Location %q; want 400 and no Location", w.Code, w.Header().Get("Location"))
	}
	if !strings.Contains(w.Body.String(), "not available") {
		t.Fatalf("body %q should say SSO is unavailable", w.Body.String())
	}
}

// ── hop 2 + hop 3: the full exchange ───────────────────────────────────────

func TestOpenIDFlow_EndToEndWithVerifierSucceeds(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "client-state-1")

	q := h.allow(t, browser, oidcPersonAssertion)
	if q.Get("error") != "" {
		t.Fatalf("unexpected error %q", q.Get("error"))
	}
	if q.Get("state") != "client-state-1" {
		t.Fatalf("state = %q, want the hop-1 state round-tripped", q.Get("state"))
	}
	code := q.Get("code")
	if code == "" {
		t.Fatal("hop 2 with a verified person must deliver a code")
	}

	w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
	if w.Code != http.StatusOK {
		t.Fatalf("callback = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "owner@example.com") {
		t.Fatalf("session must be for the browser-hop person: %s", w.Body.String())
	}
}

// AudioBooth sends no state on hop 1; the callback must then carry none either.
func TestOpenIDFlow_NoStateMeansNoStateInCallback(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	req, csrf, _ := h.consentPage(t, browser, oidcPersonAssertion)
	w := h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, nil)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "code=") || strings.Contains(loc, "state=") {
		t.Fatalf("Location %q must carry a code and omit an empty state", loc)
	}
}

// The code is bound to the hop-1 challenge: a wrong verifier fails, and the
// failed attempt burns the code, so the right verifier cannot follow.
func TestOpenIDFlow_WrongVerifierFails(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	code := h.allow(t, browser, oidcPersonAssertion).Get("code")

	if w := h.redeem(t, code, "a-different-verifier-entirely-0123456789", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong verifier = %d, want 401", w.Code)
	}
	if w := h.redeem(t, code, oidcVerifier, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("redeeming after a failed attempt = %d, want 401 (single use)", w.Code)
	}
}

func TestOpenIDFlow_CodeIsSingleUse(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	code := h.allow(t, browser, oidcPersonAssertion).Get("code")
	if w := h.redeem(t, code, oidcVerifier, ""); w.Code != http.StatusOK {
		t.Fatalf("first redemption = %d, want 200", w.Code)
	}
	if w := h.redeem(t, code, oidcVerifier, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("second redemption = %d, want 401", w.Code)
	}
}

func TestOpenIDFlow_ExpiredCodeRefused(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	code := h.allow(t, browser, oidcPersonAssertion).Get("code")
	h.handler.SetClock(func() time.Time { return time.Now().Add(61 * time.Second) })
	if w := h.redeem(t, code, oidcVerifier, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired code = %d, want 401", w.Code)
	}
}

// A client that sends redirect_uri at the exchange must send the exact value
// the code was minted for. (AudioBooth sends none, which is accepted.)
func TestOpenIDFlow_RedirectURIMismatchRefused(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	code := h.allow(t, browser, oidcPersonAssertion).Get("code")
	if w := h.redeem(t, code, oidcVerifier, "&redirect_uri="+url.QueryEscape("otherapp://cb")); w.Code != http.StatusUnauthorized {
		t.Fatalf("mismatched redirect_uri = %d, want 401", w.Code)
	}

	browser = h.hop1(t, s256(oidcVerifier), "")
	code = h.allow(t, browser, oidcPersonAssertion).Get("code")
	if w := h.redeem(t, code, oidcVerifier, "&redirect_uri="+url.QueryEscape("audiobooth://oauth")); w.Code != http.StatusOK {
		t.Fatalf("matching redirect_uri = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// ── hop 2 failure paths ────────────────────────────────────────────────────

// A browser with no identity of its own cannot complete anyone's request: it
// gets an error through the stored callback and no code. The GET does not
// consume the parked request (hop-2 GETs are side-effect free).
func TestOpenIDHop2_NoIdentityRedirectsErrorWithoutConsuming(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "s9")

	q := appCallback(t, h.get(t, browser, ""))
	if q.Get("error") != "access_denied" || q.Get("code") != "" {
		t.Fatalf("callback params %v: want error=access_denied and no code", q)
	}
	if q.Get("state") != "s9" {
		t.Fatalf("state = %q, want s9 round-tripped on the error too", q.Get("state"))
	}
	h.consentPage(t, browser, oidcPersonAssertion)
}

// iOS 17.4+ loads hop 2 with the service token. Hop 2 redirects once to itself
// (reauth=1) WITHOUT consuming, so the next load — which carries no service
// token — can be authenticated as the person.
func TestOpenIDHop2_ServiceTokenBouncesOnceThenCompletes(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "s1")

	w := h.get(t, browser, oidcServiceAssertion)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	bounce, err := url.Parse(w.Header().Get("Location"))
	if err != nil || bounce.Scheme != "https" || bounce.Host != "example.com" || bounce.Path != abshandler.OIDCBrowserPath {
		t.Fatalf("bounce Location %q must be hop 2 on this origin", w.Header().Get("Location"))
	}
	if bounce.Query().Get("reauth") != "1" {
		t.Fatalf("bounce %q must carry reauth=1", bounce)
	}

	q := h.allow(t, bounce.RequestURI(), oidcPersonAssertion)
	if q.Get("code") == "" || q.Get("state") != "s1" {
		t.Fatalf("after the bounce a person must get a code; params %v", q)
	}
}

// After the one bounce, a still person-less load is terminal.
func TestOpenIDHop2_ServiceTokenAfterBounceIsDenied(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	q := appCallback(t, h.get(t, browser+"&reauth=1", oidcServiceAssertion))
	if q.Get("error") != "access_denied" || q.Get("code") != "" {
		t.Fatalf("params %v: want access_denied and no code", q)
	}
}

// A verified identity that is not on the allowlist gets an error, no code, and
// no account.
func TestOpenIDHop2_NonAllowlistedIdentityDenied(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	q := appCallback(t, h.get(t, browser, oidcStrangerAssert))
	if q.Get("error") != "access_denied" || q.Get("code") != "" {
		t.Fatalf("params %v: want access_denied and no code", q)
	}
	if n := len(h.store.users); n != 0 {
		t.Fatalf("%d users provisioned for a non-allowlisted identity", n)
	}
}

func TestOpenIDHop2_ReplayedReqRefused(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	h.allow(t, browser, oidcPersonAssertion)
	assertDeadLink(t, h.get(t, browser, oidcPersonAssertion))
}

func TestOpenIDHop2_ExpiredReqRefused(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	h.handler.SetClock(func() time.Time { return time.Now().Add(5 * time.Minute) })
	assertDeadLink(t, h.get(t, browser, oidcPersonAssertion))
}

func TestOpenIDHop2_UnknownOrMalformedReqRefused(t *testing.T) {
	h := newOIDCHarness(t)
	for _, path := range []string{
		abshandler.OIDCBrowserPath,
		abshandler.OIDCBrowserPath + "?req=junk",
		abshandler.OIDCBrowserPath + "?req=" + s256("never-issued"),
	} {
		assertDeadLink(t, h.get(t, path, oidcPersonAssertion))
	}
	if n := len(h.store.users); n != 0 {
		t.Fatalf("a dead link provisioned %d users; the id is checked before identity", n)
	}
}

// redirect_uri and state come from the parked request, never from hop 2's own
// query string, so a crafted hop-2 link cannot steer the code elsewhere.
func TestOpenIDHop2_IgnoresQueryRedirectURIAndState(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "real-state")
	evil := browser + "&redirect_uri=" + url.QueryEscape("https://evil.example/cb") + "&state=evil"
	q := h.allow(t, evil, oidcPersonAssertion)
	if q.Get("state") != "real-state" || q.Get("code") == "" {
		t.Fatalf("params %v: want the stored state and a code", q)
	}

	// The error branches redirect too, and must use the stored target as well.
	browser = h.hop1(t, s256(oidcVerifier), "real-state")
	evil = browser + "&redirect_uri=" + url.QueryEscape("https://evil.example/cb") + "&state=evil"
	for _, assertion := range []string{"", oidcStrangerAssert} {
		q = appCallback(t, h.get(t, evil, assertion))
		if q.Get("error") != "access_denied" || q.Get("state") != "real-state" {
			t.Fatalf("assertion %q: params %v: want access_denied with the stored state", assertion, q)
		}
	}
}

// The web OAuth deep-link seam binds every code to an allow-listed redirect_uri.
func TestMintAppAuthorizationCode_RefusesUnregisteredRedirect(t *testing.T) {
	t.Setenv(abshandler.OIDCRedirectURIsEnvVar, "")
	if _, err := abshandler.MintAppAuthorizationCode("u1", "e@example.com", s256(oidcVerifier), "https://evil.example/cb"); err == nil {
		t.Fatal("minting for an unregistered redirect_uri must fail")
	}
	if _, err := abshandler.MintAppAuthorizationCode("u1", "e@example.com", s256(oidcVerifier), "audiobooth://oauth"); err != nil {
		t.Fatalf("minting for the registered redirect_uri: %v", err)
	}
}

// ── consent interstitial ───────────────────────────────────────────────────

// The consent page names the app (from the allow-listed redirect_uri), the
// callback scheme and the signed-in account. Loading it any number of times
// mints nothing and consumes nothing.
func TestOpenIDConsent_GetIsSideEffectFree(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "s")
	_, _, body := h.consentPage(t, browser, oidcPersonAssertion)
	for _, want := range []string{"AudioBooth", "owner@example.com", "audiobooth://", `method="post"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("consent page lacks %q", want)
		}
	}
	h.consentPage(t, browser, oidcPersonAssertion)
	req, csrf, _ := h.consentPage(t, browser, oidcPersonAssertion)
	if q := appCallback(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, nil)); q.Get("code") == "" {
		t.Fatal("after three GETs the request must still complete: GET consumed it")
	}
}

// The consent page is only for the parked request's own client name; a
// caller-supplied client_id is never shown as the app's name.
func TestOpenIDConsent_IgnoresCallerClientID(t *testing.T) {
	h := newOIDCHarness(t)
	u, _ := url.Parse(authorizePath(s256(oidcVerifier), ""))
	q := u.Query()
	q.Set("client_id", "Your Bank")
	w := h.get(t, "/auth/openid?"+q.Encode(), oidcServiceAssertion)
	browser := assertBrowserHopLocation(t, w).RequestURI()
	_, _, body := h.consentPage(t, browser, oidcPersonAssertion)
	if strings.Contains(body, "Your Bank") {
		t.Fatal("the consent page must not display the caller-supplied client_id")
	}
}

func TestOpenIDConsent_PostWithoutValidTokenRefused(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	req, csrf, _ := h.consentPage(t, browser, oidcPersonAssertion)

	assertRefused403(t, h.postConsent(t, req, "", "allow", oidcPersonAssertion, nil))
	assertRefused403(t, h.postConsent(t, req, csrf+"x", "allow", oidcPersonAssertion, nil))
	assertRefused403(t, h.postConsent(t, req, strings.ToUpper(csrf), "allow", oidcPersonAssertion, nil))
	// No identity on the POST: nothing to bind a token to.
	assertRefused403(t, h.postConsent(t, req, csrf, "allow", "", nil))
	// A cross-site form post.
	assertRefused403(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, map[string]string{"Origin": "https://evil.example"}))

	// None of the refusals consumed the request.
	w := h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, map[string]string{"Origin": "https://example.com"})
	if q := appCallback(t, w); q.Get("code") == "" {
		t.Fatal("a valid POST after refused ones must still mint: a refusal consumed the request")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("consent POST answered %d, want 303", w.Code)
	}
}

// A token is bound to (req id, user): it cannot be replayed onto another
// request, and another person cannot use the token minted for someone else.
func TestOpenIDConsent_TokenNotReplayableAcrossRequestsOrIdentities(t *testing.T) {
	h := newOIDCHarness(t)
	browserA := h.hop1(t, s256(oidcVerifier), "")
	browserB := h.hop1(t, s256(oidcVerifier), "")
	reqA, csrfA, _ := h.consentPage(t, browserA, oidcPersonAssertion)
	reqB, csrfB, _ := h.consentPage(t, browserB, oidcPersonAssertion)
	if csrfA == csrfB {
		t.Fatal("two requests must not share a consent token")
	}

	assertRefused403(t, h.postConsent(t, reqB, csrfA, "allow", oidcPersonAssertion, nil))
	assertRefused403(t, h.postConsent(t, reqA, csrfA, "allow", oidcSecondPerson, nil))

	_, csrfA2, _ := h.consentPage(t, browserA, oidcSecondPerson)
	if csrfA2 == csrfA {
		t.Fatal("two identities must not share a consent token for one request")
	}

	q := appCallback(t, h.postConsent(t, reqA, csrfA, "allow", oidcPersonAssertion, nil))
	code := q.Get("code")
	if code == "" {
		t.Fatal("the matching token must mint")
	}
	if w := h.redeem(t, code, oidcVerifier, ""); !strings.Contains(w.Body.String(), "owner@example.com") {
		t.Fatalf("the code must be for the person who allowed: %s", w.Body.String())
	}
	// And the used token is dead.
	assertDeadLink(t, h.postConsent(t, reqA, csrfA, "allow", oidcPersonAssertion, nil))
}

// Deny consumes the request and reports access_denied through the callback.
func TestOpenIDConsent_DenyReportsAccessDenied(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "st")
	req, csrf, _ := h.consentPage(t, browser, oidcPersonAssertion)
	q := appCallback(t, h.postConsent(t, req, csrf, "deny", oidcPersonAssertion, nil))
	if q.Get("error") != "access_denied" || q.Get("code") != "" || q.Get("state") != "st" {
		t.Fatalf("params %v: want access_denied, no code, state round-tripped", q)
	}
	assertDeadLink(t, h.get(t, browser, oidcPersonAssertion))
}

// ── quota and rate limit ───────────────────────────────────────────────────

// A person credential holds at most three parked requests; a fourth evicts
// that person's own oldest, and another credential's request is untouched.
func TestOpenIDHop1_PersonCapEvictsOwnOldest(t *testing.T) {
	h := newOIDCHarness(t)
	other := h.hop1(t, s256(oidcVerifier), "")
	var mine []string
	for range 4 {
		w := h.get(t, authorizePath(s256(oidcVerifier), ""), oidcPersonAssertion)
		mine = append(mine, assertBrowserHopLocation(t, w).RequestURI())
	}
	assertDeadLink(t, h.get(t, mine[0], oidcPersonAssertion))
	for _, b := range append(mine[1:], other) {
		h.consentPage(t, b, oidcPersonAssertion)
	}
}

// A service token is shared by a whole group of people and devices. Many
// concurrent sign-ins under ONE token must not evict each other while the
// token is below its (group-sized) cap.
func TestOpenIDHop1_ManyLoginsUnderOneServiceTokenCoexist(t *testing.T) {
	h := newOIDCHarness(t)
	var links []string
	for range 12 {
		links = append(links, h.hop1(t, s256(oidcVerifier), ""))
	}
	for i, b := range links {
		if w := h.get(t, b, oidcPersonAssertion); w.Code != http.StatusOK {
			t.Fatalf("sign-in %d was evicted by others on the same token (status %d)", i+1, w.Code)
		}
	}
}

// Rotating a forged Cf-Connecting-Ip buys no fresh rate-limit bucket: the 31st
// request under one token (burst 30) is refused whatever address it claims,
// and another token is unaffected.
func TestOpenIDHop1_SpoofedConnectingIPIgnored(t *testing.T) {
	h := newOIDCHarness(t)
	for i := range 30 {
		if w := h.hop1From(t, oidcServiceAssertion, fmt.Sprintf("203.0.113.%d", i+1)); w.Code != http.StatusFound {
			t.Fatalf("request %d = %d, want 302 within the burst", i+1, w.Code)
		}
	}
	w := h.hop1From(t, oidcServiceAssertion, "203.0.113.200")
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "Too many") {
		t.Fatalf("over the burst with a fresh forged address: status %d body %q; want 400 text", w.Code, w.Body.String())
	}
	assertBrowserHopLocation(t, h.hop1From(t, oidcOtherService, "203.0.113.200"))
}

// A service-token assertion with no common_name names no credential: it would
// put every such caller in one anonymous bucket, so it is refused like no
// credential at all.
func TestOpenIDHop1_EmptyCommonNameIsNotACredential(t *testing.T) {
	h := newOIDCHarness(t)
	w := h.get(t, authorizePath(s256(oidcVerifier), ""), oidcNoCNService)
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
		t.Fatalf("status %d Location %q; want 400 and no Location", w.Code, w.Header().Get("Location"))
	}
}

// A person credential is rate-limited tightly (burst 5), independently of the
// shared tokens arriving from the same proxy address.
func TestOpenIDHop1_RateLimitedPerPerson(t *testing.T) {
	h := newOIDCHarness(t)
	for i := range 5 {
		if w := h.get(t, authorizePath(s256(oidcVerifier), ""), oidcPersonAssertion); w.Code != http.StatusFound {
			t.Fatalf("request %d = %d, want 302 within the burst", i, w.Code)
		}
	}
	w := h.get(t, authorizePath(s256(oidcVerifier), ""), oidcPersonAssertion)
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "Too many") {
		t.Fatalf("over the burst: status %d body %q; want 400 text", w.Code, w.Body.String())
	}
	assertBrowserHopLocation(t, h.get(t, authorizePath(s256(oidcVerifier), ""), oidcServiceAssertion))
}
