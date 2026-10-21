// file: internal/server/handlers/abs/openid_flow_test.go
// version: 1.9.0
// guid: 4f0b6c2e-9a71-4d38-b5e2-7c1d93a8f046
// last-edited: 2026-10-20

package abs_test

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
func newOIDCHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()
	t.Setenv(abshandler.OIDCRedirectURIsEnvVar, "")
	// Each test gets its own code store: it is package-level, so caps and
	// codes would otherwise carry over from earlier tests.
	abshandler.IsolateOIDCCodesForTest(t)
	h := newHarness(t, "cf,jwt", []string{"owner@example.com", "second@example.com"}, opts...)
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
	// Hop 2 is opened in a real browser, which always sends these; hop 1 and
	// the rest are the app's URLSession and do not.
	if strings.HasPrefix(path, abshandler.OIDCBrowserPath) {
		headers["Sec-Fetch-Mode"] = "navigate"
		headers["Sec-Fetch-Dest"] = "document"
	}
	w, _ := h.do(t, request{method: http.MethodGet, path: path, headers: headers})
	return w
}

// getWithCookie is a hop-2 browser navigation that also carries a
// CF_Authorization cookie, as Safari's shared jar does.
func (h *harness) getWithCookie(t *testing.T, path, assertion, cfCookie string) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{
		"Sec-Fetch-Mode": "navigate",
		"Sec-Fetch-Dest": "document",
		"Cookie":         "CF_Authorization=" + cfCookie,
	}
	if assertion != "" {
		headers[oauth.CFAccessHeader] = assertion
	}
	w, _ := h.do(t, request{method: http.MethodGet, path: path, headers: headers})
	return w
}

const wantExpireCFCookie = "CF_Authorization=; Path=/; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Secure; HttpOnly"

var bounceMetaRe = regexp.MustCompile(`<meta http-equiv="refresh" content="0;url=([^"]+)">`)

// assertHop2Bounce checks a hop-2 recovery bounce page and returns its target
// as a request URI: 200, no-store, CSP, no script, a visible Continue link, a
// meta refresh to hop 2 on this origin at reauth=wantStage, and the
// CF_Authorization expiry exactly when wantExpire.
func assertHop2Bounce(t *testing.T, w *httptest.ResponseRecorder, wantStage string, wantExpire bool) string {
	t.Helper()
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" {
		t.Fatalf("status %d Location %q body %q; want a 200 bounce page, not a redirect", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'none'") {
		t.Fatalf("Content-Security-Policy = %q, want default-src 'none'", csp)
	}
	body := w.Body.String()
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Fatalf("bounce page must carry no script:\n%s", body)
	}
	cookies := w.Result().Header.Values("Set-Cookie")
	if wantExpire {
		if len(cookies) != 1 || cookies[0] != wantExpireCFCookie {
			t.Fatalf("Set-Cookie = %q, want exactly %q", cookies, wantExpireCFCookie)
		}
	} else if len(cookies) != 0 {
		t.Fatalf("Set-Cookie = %q, want none (a person's cookie must survive)", cookies)
	}
	m := bounceMetaRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no meta refresh in bounce page:\n%s", body)
	}
	target := html.UnescapeString(m[1])
	if !strings.Contains(body, `>Continue</a>`) || !strings.Contains(body, `href="`+html.EscapeString(target)+`"`) {
		t.Fatalf("bounce page needs a visible Continue link to the same target:\n%s", body)
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host != "example.com" || u.Path != abshandler.OIDCBrowserPath {
		t.Fatalf("bounce target %q must be hop 2 on this origin", target)
	}
	if got := u.Query().Get("reauth"); got != wantStage {
		t.Fatalf("bounce target %q has reauth=%q, want %q", target, got, wantStage)
	}
	return u.RequestURI()
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
	// The client's scheme appears only as the redirect_uri echo the official
	// app requires, never as the Location's own scheme (checked above); and no
	// code is ever minted at hop 1.
	q := u.Query()
	if q.Get("code") != "" || q.Get("error") != "" {
		t.Fatalf("Location %q must carry no code and no error", loc)
	}
	if req := q.Get("req"); len(req) != 43 {
		t.Fatalf("req id %q must be a 43-char random id", req)
	}
	// The official app requires all four and rebuilds the URL from them,
	// dropping req: state must therefore be the request id.
	if q.Get("state") != q.Get("req") {
		t.Fatalf("Location %q: state must be the request id", loc)
	}
	if q.Get("redirect_uri") != "audiobooth://oauth" || q.Get("client_id") == "" || q.Get("scope") != "openid" {
		t.Fatalf("Location %q must echo the allow-listed redirect_uri, a client_id and scope=openid", loc)
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
	return appCallbackFor(t, w, "audiobooth://oauth")
}

func appCallbackFor(t *testing.T, w *httptest.ResponseRecorder, redirectURI string) url.Values {
	t.Helper()
	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther {
		t.Fatalf("hop 2 status = %d, want 302/303; body %q", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, redirectURI+"?") {
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
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") || !strings.Contains(w.Body.String(), "sign in again") {
		t.Fatalf("a dead link should render the message page; Content-Type %q body %q", ct, w.Body.String())
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
	// X-Frame-Options is the legacy control; CSP frame-ancestors is the one
	// current browsers apply, and it wins where both are present.
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("Content-Security-Policy = %q, want frame-ancestors 'none'", csp)
	}
	// no-referrer would make browsers send `Origin: null` on the form POST,
	// which the Origin check refuses: every real Allow would fail.
	if rp := w.Header().Get("Referrer-Policy"); rp != "same-origin" {
		t.Fatalf("Referrer-Policy = %q, want same-origin", rp)
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

// postConsent submits the consent form as a browser would: from the page's own
// origin, so it carries `Origin: https://example.com` (what WebKit sends under
// Referrer-Policy same-origin). extra overrides headers; an empty value removes
// one, and a "Host" entry sets the request's Host.
func (h *harness) postConsent(t *testing.T, req, csrf, decision, assertion string, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("req", req)
	form.Set("csrf", csrf)
	form.Set("decision", decision)
	r := httptest.NewRequest(http.MethodPost, abshandler.OIDCBrowserPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://example.com")
	if assertion != "" {
		r.Header.Set(oauth.CFAccessHeader, assertion)
	}
	for k, v := range extra {
		switch {
		case k == "Host":
			r.Host = v
		case v == "":
			r.Header.Del(k)
		default:
			r.Header.Set(k, v)
		}
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
		// client_id is parked, echoed into the hop-2 URL and logged.
		{"client_id too long", base(func(q url.Values) { q.Set("client_id", strings.Repeat("c", 129)) }), "client_id is too long"},
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

// Neither app sends state on hop 1. The callback then carries the request id
// as state: it is the state the official app saw in hop 1's Location and
// compares against, and AudioBooth passes state through without comparing
// it. The request is consumed by then, so the id is dead.
func TestOpenIDFlow_NoStateMeansRequestIDAsState(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	req, csrf, _ := h.consentPage(t, browser, oidcPersonAssertion)
	q := appCallback(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, nil))
	if q.Get("code") == "" || q.Get("state") != req {
		t.Fatalf("params %v: want a code and state = the request id %q", q, req)
	}
	assertDeadLink(t, h.get(t, browser, oidcPersonAssertion))

	// Deny is after the take too, so it echoes the id the same way.
	browser = h.hop1(t, s256(oidcVerifier), "")
	req, csrf, _ = h.consentPage(t, browser, oidcPersonAssertion)
	q = appCallback(t, h.postConsent(t, req, csrf, "deny", oidcPersonAssertion, nil))
	if q.Get("error") != "access_denied" || q.Get("state") != req {
		t.Fatalf("deny params %v: want access_denied and state = the request id", q)
	}
}

// The hop-2 GET's error redirects leave the request parked, so they must NOT
// hand its still-live id to whatever app owns the callback scheme: with no
// client state they carry no state at all.
func TestOpenIDHop2_GetErrorNeverEchoesTheLiveRequestID(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	for _, assertion := range []string{"", oidcStrangerAssert} {
		w := h.get(t, browser, assertion)
		q := appCallback(t, w)
		if q.Get("error") != "access_denied" {
			t.Fatalf("assertion %q: params %v: want access_denied", assertion, q)
		}
		if _, has := q["state"]; has {
			t.Fatalf("assertion %q: Location %q echoes a state for a live request", assertion, w.Header().Get("Location"))
		}
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

// With strict_service_token_auth off, iOS 17.4+ loads hop 2 with the service
// token AND Safari's shared jar holds a service-token CF_Authorization cookie.
// A plain 302 cannot shed a cookie, so hop 2 answers each service-token
// navigation with a static page that expires the cookie and meta-refreshes to
// the next stage; the reload reaches Cloudflare's identity login and comes back
// as a person.
func TestOpenIDHop2_PoisonedCookieIsExpiredThenPersonCompletes(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "s1")

	next := assertHop2Bounce(t, h.getWithCookie(t, browser, oidcServiceAssertion, oidcServiceAssertion), "1", true)

	q := h.allow(t, next, oidcPersonAssertion)
	if q.Get("code") == "" || q.Get("state") != "s1" {
		t.Fatalf("after the bounce a person must get a code; params %v", q)
	}
}

// A cookie that is a PERSON's must never be deleted just because the first
// load carried the app's service-token headers.
func TestOpenIDHop2_PersonCookieSurvivesFirstBounce(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	next := assertHop2Bounce(t, h.getWithCookie(t, browser, oidcServiceAssertion, oidcPersonAssertion), "1", false)
	if q := h.allow(t, next, oidcPersonAssertion); q.Get("code") == "" {
		t.Fatalf("params %v: want a code after the bounce", q)
	}
}

// No cookie at all (the headers alone made it a service token): bounce, nothing
// to expire.
func TestOpenIDHop2_ServiceTokenWithoutCookieBouncesWithoutExpiry(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	assertHop2Bounce(t, h.get(t, browser, oidcServiceAssertion), "1", false)
}

// load 1 -> reauth=1 (expire) -> reauth=2 (deny), and a denied hop 2 leaves the
// parked request alone so the app can start over.
func TestOpenIDHop2_RecoveryStagesThenDeny(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "s2")

	// Stage 0: the cookie is a person's, so it is kept.
	r1 := assertHop2Bounce(t, h.getWithCookie(t, browser, oidcServiceAssertion, oidcPersonAssertion), "1", false)
	// Stage 1 still arrives as a service token (the cookie alone): it goes now,
	// whatever it was.
	r2 := assertHop2Bounce(t, h.getWithCookie(t, r1, oidcServiceAssertion, oidcServiceAssertion), "2", true)
	// Stage 2 is the end of the road: deny through the app's callback.
	w := h.getWithCookie(t, r2, oidcServiceAssertion, oidcServiceAssertion)
	if w.Body.Len() != 0 && strings.Contains(w.Body.String(), "http-equiv") {
		t.Fatalf("reauth=2 must not bounce again:\n%s", w.Body.String())
	}
	q := appCallback(t, w)
	if q.Get("error") != "access_denied" || q.Get("code") != "" || q.Get("state") != "s2" {
		t.Fatalf("params %v: want access_denied with state s2 and no code", q)
	}
	h.consentPage(t, browser, oidcPersonAssertion)
}

// A service token with an EMPTY common_name is still a service token: the bounce
// is gated on "verified, no email", not on the log label.
func TestOpenIDHop2_ServiceTokenWithoutCommonNameStillBounces(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	assertHop2Bounce(t, h.get(t, browser, oidcNoCNService), "1", false)
}

// A junk reauth value can only deny, never loop.
func TestOpenIDHop2_JunkReauthDenies(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	for _, v := range []string{"3", "abc", "-1", "9999"} {
		w := h.get(t, browser+"&reauth="+v, oidcServiceAssertion)
		if q := appCallback(t, w); q.Get("error") != "access_denied" {
			t.Fatalf("reauth=%s: params %v, want access_denied", v, q)
		}
	}
}

// Hop 2 is reached only by a browser, so it does NOT require Sec-Fetch headers
// (nobody has confirmed the sign-in sheet sends them): a service-token load
// without them bounces like any other.
func TestOpenIDHop2_ServiceTokenBouncesWithoutSecFetchHeaders(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	w, _ := h.do(t, request{method: http.MethodGet, path: browser,
		headers: map[string]string{oauth.CFAccessHeader: oidcServiceAssertion}})
	next := assertHop2Bounce(t, w, "1", false)
	if q := h.allow(t, next, oidcPersonAssertion); q.Get("code") == "" {
		t.Fatalf("params %v: want a code after the bounce", q)
	}
}

// What still keeps a hop-2 load out of the bounce: an API key, AppleCoreMedia,
// and a non-service-token assertion (none at all).
func TestOpenIDHop2_ExcludedRequestsAreNotBounced(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	for name, hdrs := range map[string]map[string]string{
		"api key":        {oauth.CFAccessHeader: oidcServiceAssertion, "Authorization": "Bearer abk_0123456789"},
		"AppleCoreMedia": {oauth.CFAccessHeader: oidcServiceAssertion, "User-Agent": "AppleCoreMedia/1.0.0.21A329 (iPhone; U; CPU OS 17_0 like Mac OS X; en_us)"},
		"no assertion":   {},
	} {
		w, _ := h.do(t, request{method: http.MethodGet, path: browser, headers: hdrs})
		if q := appCallback(t, w); q.Get("error") != "access_denied" {
			t.Fatalf("%s: params %v, want access_denied and no bounce", name, q)
		}
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
	evil := browser + "&redirect_uri=" + url.QueryEscape("https://evil.example/cb")
	q := h.allow(t, evil, oidcPersonAssertion)
	if q.Get("state") != "real-state" || q.Get("code") == "" {
		t.Fatalf("params %v: want the stored state and a code", q)
	}

	// The error branches redirect too, and must use the stored target as well.
	browser = h.hop1(t, s256(oidcVerifier), "real-state")
	evil = browser + "&redirect_uri=" + url.QueryEscape("https://evil.example/cb")
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
	abshandler.IsolateOIDCCodesForTest(t)
	if _, err := abshandler.MintAppAuthorizationCode(&database.User{ID: "u1"}, "e@example.com", s256(oidcVerifier), "https://evil.example/cb"); err == nil {
		t.Fatal("minting for an unregistered redirect_uri must fail")
	}
	if _, err := abshandler.MintAppAuthorizationCode(&database.User{ID: "u1"}, "e@example.com", s256(oidcVerifier), "audiobooth://oauth"); err != nil {
		t.Fatalf("minting for the registered redirect_uri: %v", err)
	}
	if _, err := abshandler.MintAppAuthorizationCode(&database.User{ID: "u1"}, "e@example.com", "not-an-s256-challenge", "audiobooth://oauth"); err == nil {
		t.Fatal("minting with a challenge that is not S256-shaped must fail")
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
	// `Origin: null` is what sandboxed frames and no-referrer documents send;
	// it proves nothing about where the form came from.
	assertRefused403(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, map[string]string{"Origin": "null"}))
	// Same host, wrong scheme or port: a different origin.
	assertRefused403(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, map[string]string{"Origin": "http://example.com"}))
	assertRefused403(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, map[string]string{"Origin": "https://example.com:8443"}))
	// A suffix or prefix of our host is a different host.
	assertRefused403(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, map[string]string{"Origin": "https://example.com.evil.example"}))
	// A service token is not a person: there is no one to bind the consent to.
	assertRefused403(t, h.postConsent(t, req, csrf, "allow", oidcServiceAssertion, nil))

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

// ── origin handling ────────────────────────────────────────────────────────

// Host and Origin are compared as normalised origins: a Host with a different
// case and an explicit default port is the same origin as the browser's
// `Origin: https://example.com`, and the hop-1 Location drops the :443.
func TestOpenIDConsent_HostWithDefaultPortMatchesOrigin(t *testing.T) {
	h := newOIDCHarness(t)
	r := httptest.NewRequest(http.MethodGet, authorizePath(s256(oidcVerifier), ""), nil)
	r.Host = "Example.COM:443"
	r.Header.Set(oauth.CFAccessHeader, oidcServiceAssertion)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, r)
	browser := assertBrowserHopLocation(t, w).RequestURI()

	req, csrf, _ := h.consentPage(t, browser, oidcPersonAssertion)
	w = h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, map[string]string{"Host": "Example.COM:443"})
	if q := appCallback(t, w); q.Get("code") == "" {
		t.Fatalf("Host %q and Origin https://example.com are one origin; params %v", "Example.COM:443", q)
	}
}

// With no Origin header (not a browser form post) the CSRF token alone
// carries the check; that path must keep working.
func TestOpenIDConsent_NoOriginHeaderStillNeedsOnlyTheToken(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	req, csrf, _ := h.consentPage(t, browser, oidcPersonAssertion)
	if q := appCallback(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, map[string]string{"Origin": ""})); q.Get("code") == "" {
		t.Fatal("a POST without Origin but with a valid token must mint")
	}
}

// A configured public URL wins over the request Host — reduced to its origin
// (path dropped, host lowercased, :443 dropped) — and the consent POST must
// then come from that origin.
func TestOpenIDHop1_ConfiguredPublicOriginWins(t *testing.T) {
	h := newOIDCHarness(t, func(o *abshandler.Options) {
		o.PublicOrigin = func() string { return "https://Books.Example.net:443/abs/" }
	})
	w := h.get(t, authorizePath(s256(oidcVerifier), ""), oidcServiceAssertion)
	loc, err := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || err != nil {
		t.Fatalf("status %d Location %q", w.Code, w.Header().Get("Location"))
	}
	if loc.Scheme != "https" || loc.Host != "books.example.net" || loc.Path != abshandler.OIDCBrowserPath {
		t.Fatalf("Location %q must be hop 2 on the configured origin https://books.example.net", loc)
	}
	req, csrf, _ := h.consentPage(t, loc.RequestURI(), oidcPersonAssertion)
	// The page lives on the configured origin, so that is the Origin a
	// browser sends; the request's own Host (example.com) is not it.
	assertRefused403(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, nil))
	q := appCallback(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion,
		map[string]string{"Origin": "https://books.example.net"}))
	if q.Get("code") == "" {
		t.Fatal("a POST from the configured origin must mint")
	}
}

// A configured URL that is not https (or has no host) is ignored, and the
// request Host is used instead.
func TestOpenIDHop1_UnusablePublicOriginFallsBackToHost(t *testing.T) {
	for _, configured := range []string{"http://books.example.net", "books.example.net", "https://", "https://u:p@books.example.net"} {
		h := newOIDCHarness(t, func(o *abshandler.Options) {
			o.PublicOrigin = func() string { return configured }
		})
		assertBrowserHopLocation(t, h.get(t, authorizePath(s256(oidcVerifier), ""), oidcServiceAssertion))
	}
}

// ── the official Audiobookshelf app ────────────────────────────────────────

// officialAppAuthorizePath is audiobookshelf-app's exact hop-1 request
// (ServerConnectForm.vue oauthRequest): no scope, no state.
func officialAppAuthorizePath(challenge string) string {
	return "/auth/openid?code_challenge=" + challenge + "&code_challenge_method=S256" +
		"&redirect_uri=" + url.QueryEscape("audiobookshelf://oauth") +
		"&client_id=" + url.QueryEscape("Audiobookshelf-App") + "&response_type=code"
}

// officialAppRebuild is clickLoginWithOpenId's URL rebuild: it refuses a
// Location missing client_id, scope, state or redirect_uri, keeps host +
// pathname plus those four and its own challenge, and drops everything else —
// including req.
func officialAppRebuild(t *testing.T, location, challenge string) (rebuilt, state string) {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Location %q: %v", location, err)
	}
	q := u.Query()
	clientID, scope, state, redirectURI := q.Get("client_id"), q.Get("scope"), q.Get("state"), q.Get("redirect_uri")
	if clientID == "" || scope == "" || state == "" || redirectURI == "" {
		t.Fatalf("the official app rejects Location %q: client_id, scope, state or redirect_uri missing", location)
	}
	if u.Scheme != "https" {
		t.Fatalf("the official app requires an https Location, got %q", location)
	}
	rebuilt = u.Path + "?response_type=code" +
		"&client_id=" + url.QueryEscape(clientID) + "&scope=" + url.QueryEscape(scope) +
		"&state=" + url.QueryEscape(state) + "&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&code_challenge=" + url.QueryEscape(challenge) + "&code_challenge_method=S256"
	return rebuilt, state
}

// The official app completes the whole flow through its URL rebuild, and the
// callback's state equals the state it saw in hop 1's Location.
func TestOpenIDOfficialApp_RebuiltURLCompletesFlow(t *testing.T) {
	h := newOIDCHarness(t)
	t.Setenv(abshandler.OIDCRedirectURIsEnvVar, "audiobookshelf://oauth")
	challenge := s256(oidcVerifier)

	w := h.get(t, officialAppAuthorizePath(challenge), oidcServiceAssertion)
	if w.Code != http.StatusFound {
		t.Fatalf("hop 1 = %d, want 302: %q", w.Code, w.Body.String())
	}
	rebuilt, sawState := officialAppRebuild(t, w.Header().Get("Location"), challenge)
	if strings.Contains(rebuilt, "req=") {
		t.Fatalf("the simulated rebuild must drop req: %q", rebuilt)
	}

	req, csrf, body := h.consentPage(t, rebuilt, oidcPersonAssertion)
	if !strings.Contains(body, "Audiobookshelf") {
		t.Fatal("the consent page should name the official app from its callback scheme")
	}
	q := appCallbackFor(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, nil), "audiobookshelf://oauth")
	if q.Get("state") != sawState {
		t.Fatalf("callback state %q != the state %q the app saw in hop 1's Location (it would refuse the login)", q.Get("state"), sawState)
	}
	// oauthExchangeCodeForToken: state, code and code_verifier, no redirect_uri.
	cb := "/auth/openid/callback?state=" + url.QueryEscape(q.Get("state")) +
		"&code=" + url.QueryEscape(q.Get("code")) + "&code_verifier=" + url.QueryEscape(oidcVerifier)
	if w := h.get(t, cb, oidcServiceAssertion); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "owner@example.com") {
		t.Fatalf("callback = %d: %s", w.Code, w.Body.String())
	}
}

// A client state, when sent, wins over the request id in the callback.
func TestOpenIDFlow_ClientStateWinsOverRequestID(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "mine")
	req, csrf, _ := h.consentPage(t, browser, oidcPersonAssertion)
	q := appCallback(t, h.postConsent(t, req, csrf, "allow", oidcPersonAssertion, nil))
	if q.Get("state") != "mine" || q.Get("state") == req {
		t.Fatalf("params %v: want the client's own state", q)
	}
}

// req and state must name the same request; a second copy of either, or two
// that disagree, is ambiguous and answered as a dead link.
func TestOpenIDHop2_AmbiguousRequestIDIsDead(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "")
	other := h.hop1(t, s256(oidcVerifier), "")
	u, _ := url.Parse(other)
	otherID := u.Query().Get("req")

	b, _ := url.Parse(browser)
	id := b.Query().Get("req")
	for _, path := range []string{
		abshandler.OIDCBrowserPath + "?req=" + id + "&state=" + otherID,
		abshandler.OIDCBrowserPath + "?req=" + id + "&req=" + otherID,
		abshandler.OIDCBrowserPath + "?state=" + id + "&state=" + otherID,
		browser + "&state=evil",
	} {
		assertDeadLink(t, h.get(t, path, oidcPersonAssertion))
	}
	// Either alone works.
	h.consentPage(t, abshandler.OIDCBrowserPath+"?req="+id, oidcPersonAssertion)
	h.consentPage(t, abshandler.OIDCBrowserPath+"?state="+id, oidcPersonAssertion)
}

// A code minted before the account's credentials or status changed (password
// change, reset, deactivation: anything that bumps CredentialEpoch) is refused at
// the callback, and no session is created from it.
func TestOpenIDFlow_CodeFromBeforeACredentialChangeIsRefused(t *testing.T) {
	h := newOIDCHarness(t)
	browser := h.hop1(t, s256(oidcVerifier), "client-state-1")
	code := h.allow(t, browser, oidcPersonAssertion).Get("code")
	if code == "" {
		t.Fatal("precondition: hop 2 delivers a code")
	}
	h.bumpAllCredentialEpochs()
	sessionsBefore := h.sessionCount()

	w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("callback after a credential change = %d, want 401: %s", w.Code, w.Body.String())
	}
	if got := h.sessionCount(); got != sessionsBefore {
		t.Fatalf("a refused code created a session (%d -> %d)", sessionsBefore, got)
	}
}

// A code minted before a deactivation is dead (401 credentials-changed), not
// suspended (403 account-inactive): the epoch is checked before the status, in
// the same order as ResolveBearer. A deactivation through SetUserStatus both
// locks the row and bumps the epoch.
func TestOpenIDFlow_CodeFromBeforeADeactivationIsCredentialsChanged(t *testing.T) {
	h := newOIDCHarness(t)
	code := h.allow(t, h.hop1(t, s256(oidcVerifier), "client-state-1"), oidcPersonAssertion).Get("code")
	if code == "" {
		t.Fatal("precondition: hop 2 delivers a code")
	}
	h.store.mu.Lock()
	for _, u := range h.store.users {
		u.Status = "locked"
		u.CredentialEpoch++
	}
	h.store.mu.Unlock()

	w := h.redeem(t, code, oidcVerifier, "&state=client-state-1")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "sign in again") {
		t.Fatalf("callback after a deactivation = %d %s, want 401 credentials-changed", w.Code, w.Body.String())
	}
}

// bumpAllCredentialEpochs simulates a password change, reset or status change
// for every seeded user.
func (h *harness) bumpAllCredentialEpochs() {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for _, u := range h.store.users {
		u.CredentialEpoch++
	}
}

func (h *harness) sessionCount() int {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	return len(h.store.sessions)
}

// A code minted for a user who already changed their password at some point
// (epoch above 0) carries that epoch and redeems normally.
func TestOpenIDFlow_CodeCarriesTheCurrentEpoch(t *testing.T) {
	h := newOIDCHarness(t)
	// The first sign-in creates the user (JIT); then its epoch moves twice.
	first := h.allow(t, h.hop1(t, s256(oidcVerifier), "s0"), oidcPersonAssertion).Get("code")
	if w := h.redeem(t, first, oidcVerifier, "&state=s0"); w.Code != http.StatusOK {
		t.Fatalf("precondition: first sign-in = %d: %s", w.Code, w.Body.String())
	}
	h.bumpAllCredentialEpochs()
	h.bumpAllCredentialEpochs()
	browser := h.hop1(t, s256(oidcVerifier), "client-state-1")
	code := h.allow(t, browser, oidcPersonAssertion).Get("code")
	if w := h.redeem(t, code, oidcVerifier, "&state=client-state-1"); w.Code != http.StatusOK {
		t.Fatalf("callback at an unchanged non-zero epoch = %d, want 200: %s", w.Code, w.Body.String())
	}
}
