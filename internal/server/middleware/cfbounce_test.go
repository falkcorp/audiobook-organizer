// file: internal/server/middleware/cfbounce_test.go
// version: 1.1.0
// guid: e5c1a7d3-2f94-4b60-8d17-a3b9c6e04f28
// last-edited: 2026-10-20

package middleware

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

const (
	bounceSvcHeader = "svc-token-header"
	bounceSvcCookie = "svc-token-cookie"
	bouncePerson    = "person-token"
	bounceHost      = "books.example.com"

	wantExpire = "CF_Authorization=; Path=/; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Secure; HttpOnly"
)

// bounceTestVerifier: bounceSvcHeader and bounceSvcCookie verify as service
// tokens (valid, no email); bouncePerson verifies as a person; anything else is
// forged.
func bounceTestVerifier() *fakeCFVerifier {
	return &fakeCFVerifier{
		byToken: map[string]*oauth.IdentityClaims{
			bouncePerson: {Provider: oauth.ProviderCFAccess, Subject: "cf|p", Email: "owner@example.com", EmailVerified: true},
		},
		nonIdentity: map[string]bool{bounceSvcHeader: true, bounceSvcCookie: true},
	}
}

// bounceEligible is a stand-in for the server's SPA path test: pages only.
func bounceEligible(p string) bool {
	for _, pre := range []string{"/api", "/auth/", "/ping", "/status", "/login", "/abs/", "/assets/"} {
		if strings.HasPrefix(p, pre) {
			return false
		}
	}
	return !strings.Contains(p, ".")
}

type bounceReq struct {
	method  string
	target  string
	headers map[string]string
	cookies map[string]string
}

var navHeaders = map[string]string{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}

func with(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func runBounce(t *testing.T, r bounceReq) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	reached := false
	router.Use(CFServiceTokenRecovery(NewCFServiceTokenBouncer(bounceTestVerifier()), bounceEligible))
	router.NoRoute(func(c *gin.Context) { reached = true; c.String(http.StatusOK, "spa") })
	method := r.method
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, r.target, nil)
	req.Host = bounceHost
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	for k, v := range r.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w, reached
}

func assertRecoveryHeaders(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want default-src 'none'", csp)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "<script") {
		t.Errorf("page must carry no script:\n%s", w.Body.String())
	}
}

func TestCFServiceTokenRecovery_Bounce(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		cookies    map[string]string
		wantExpire bool
		wantURL    string
	}{
		{"poisoned cookie is expired", "/library?x=1", map[string]string{"CF_Authorization": bounceSvcCookie}, true, "/library?cfreset=1&x=1"},
		{"person cookie is kept", "/library", map[string]string{"CF_Authorization": bouncePerson}, false, "/library?cfreset=1"},
		{"no cookie at all", "/", nil, false, "/?cfreset=1"},
		{"unverifiable cookie is kept", "/settings", map[string]string{"CF_Authorization": "forged"}, false, "/settings?cfreset=1"},
		{"other cf cookie is not CF_Authorization", "/", map[string]string{"cf_clearance": bounceSvcCookie}, false, "/?cfreset=1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, reached := runBounce(t, bounceReq{
				target: tc.target, cookies: tc.cookies,
				headers: with(navHeaders, "Cf-Access-Jwt-Assertion", bounceSvcHeader),
			})
			if reached {
				t.Fatal("handler reached; a service-token navigation must be bounced")
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			assertRecoveryHeaders(t, w)
			cookies := w.Result().Header.Values("Set-Cookie")
			if tc.wantExpire {
				if len(cookies) != 1 || cookies[0] != wantExpire {
					t.Fatalf("Set-Cookie = %q, want exactly %q", cookies, wantExpire)
				}
			} else if len(cookies) != 0 {
				t.Fatalf("Set-Cookie = %q, want none", cookies)
			}
			body := w.Body.String()
			// Attribute values are HTML-escaped (& -> &amp;); a browser decodes them.
			escURL := html.EscapeString(tc.wantURL)
			wantMeta := `<meta http-equiv="refresh" content="0;url=` + escURL + `">`
			if !strings.Contains(body, wantMeta) {
				t.Fatalf("body lacks %q:\n%s", wantMeta, body)
			}
			if !strings.Contains(body, `<a href="`+escURL+`">Continue</a>`) {
				t.Fatalf("body lacks a visible Continue link:\n%s", body)
			}
			if w.Header().Get("Location") != "" {
				t.Fatalf("a bounce must not be a redirect, got Location %q", w.Header().Get("Location"))
			}
		})
	}
}

func TestCFServiceTokenRecovery_LoopGuard(t *testing.T) {
	w, reached := runBounce(t, bounceReq{
		target:  "/library?cfreset=1",
		cookies: map[string]string{"CF_Authorization": bounceSvcCookie},
		headers: with(navHeaders, "Cf-Access-Jwt-Assertion", bounceSvcHeader),
	})
	if reached {
		t.Fatal("handler reached")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 error page", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("loop guard must not redirect, got %q", loc)
	}
	if got := w.Result().Header.Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("loop guard must not set cookies, got %q", got)
	}
	body := w.Body.String()
	if strings.Contains(body, "http-equiv") {
		t.Fatalf("error page must not refresh:\n%s", body)
	}
	for _, want := range []string{
		"Cloudflare signed this browser in with a device token instead of your account.",
		`<a href="https://books.example.com/cdn-cgi/access/logout">`,
		", then reload.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("error page lacks %q:\n%s", want, body)
		}
	}
	assertRecoveryHeaders(t, w)
}

func TestCFServiceTokenRecovery_LoopGuardHostIsEscapedAndValidated(t *testing.T) {
	if got := cfLogoutURL(`evil"><script>`); got != "/cdn-cgi/access/logout" {
		t.Fatalf("implausible host must fall back to a relative link, got %q", got)
	}
	if got := cfLogoutURL("books.example.com:8443"); got != "https://books.example.com:8443/cdn-cgi/access/logout" {
		t.Fatalf("got %q", got)
	}
}

func TestCFServiceTokenRecovery_PersonWithMarkerGetsCleanURL(t *testing.T) {
	tests := []struct{ target, want string }{
		{"/library?cfreset=1", "/library"},
		{"/library?a=1&cfreset=1&b=2", "/library?a=1&b=2"},
		{"/?cfreset=1", "/"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			w, reached := runBounce(t, bounceReq{
				target:  tc.target,
				headers: with(navHeaders, "Cf-Access-Jwt-Assertion", bouncePerson),
			})
			if reached {
				t.Fatal("handler reached")
			}
			if w.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302", w.Code)
			}
			if got := w.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCFServiceTokenRecovery_PersonWithoutMarkerPassesThrough(t *testing.T) {
	w, reached := runBounce(t, bounceReq{
		target:  "/library",
		headers: with(navHeaders, "Cf-Access-Jwt-Assertion", bouncePerson),
	})
	if !reached || w.Code != http.StatusOK || w.Body.String() != "spa" {
		t.Fatalf("reached=%v code=%d body=%q: a person navigation must reach the SPA untouched", reached, w.Code, w.Body.String())
	}
}

// Everything that is not a top-level browser navigation to a page route is left
// strictly alone, even though it carries a service-token assertion.
func TestCFServiceTokenRecovery_UntouchedRequests(t *testing.T) {
	svc := "Cf-Access-Jwt-Assertion"
	tests := []struct {
		name string
		req  bounceReq
	}{
		{"no Sec-Fetch headers", bounceReq{target: "/library", headers: map[string]string{svc: bounceSvcHeader}}},
		{"api path", bounceReq{target: "/api/v1/auth/me", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"api key bearer", bounceReq{target: "/library", headers: with(navHeaders, svc, bounceSvcHeader, "Authorization", "Bearer abk_0123456789")}},
		{"api key cookie", bounceReq{target: "/library", headers: with(navHeaders, svc, bounceSvcHeader), cookies: map[string]string{SessionCookieName: "abk_0123456789"}}},
		{"AppleCoreMedia", bounceReq{target: "/library", headers: with(navHeaders, svc, bounceSvcHeader, "User-Agent", "AppleCoreMedia/1.0.0.21A329 (iPhone; U; CPU OS 17_0 like Mac OS X; en_us)")}},
		{"fetch cors", bounceReq{target: "/library", headers: map[string]string{svc: bounceSvcHeader, "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}}},
		{"fetch same-origin", bounceReq{target: "/library", headers: map[string]string{svc: bounceSvcHeader, "Sec-Fetch-Mode": "same-origin", "Sec-Fetch-Dest": "empty"}}},
		{"navigate but iframe dest", bounceReq{target: "/library", headers: map[string]string{svc: bounceSvcHeader, "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}}},
		{"POST", bounceReq{method: http.MethodPost, target: "/library", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"HEAD", bounceReq{method: http.MethodHead, target: "/library", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"abs ping", bounceReq{target: "/ping", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"abs status", bounceReq{target: "/status", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"abs login", bounceReq{target: "/login", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"abs prefix", bounceReq{target: "/abs/anything", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"openid hop 1", bounceReq{target: "/auth/openid?client_id=AudioBooth", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"openid hop 2", bounceReq{target: "/auth/openid/browser?req=x", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"openid callback", bounceReq{target: "/auth/openid/callback?code=x", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"static asset", bounceReq{target: "/assets/index-abc.js", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"favicon", bounceReq{target: "/favicon.ico", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"scheme-relative path is never emitted", bounceReq{target: "//evil.example/x", headers: with(navHeaders, svc, bounceSvcHeader)}},
		{"no assertion header", bounceReq{target: "/library", headers: navHeaders}},
		{"forged assertion", bounceReq{target: "/library", headers: with(navHeaders, svc, "forged")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, reached := runBounce(t, tc.req)
			if !reached {
				t.Fatalf("request was intercepted (status %d, body %q); it must pass through untouched", w.Code, w.Body.String())
			}
			if w.Body.String() != "spa" || len(w.Result().Header.Values("Set-Cookie")) != 0 || w.Header().Get("Location") != "" {
				t.Fatalf("response was altered: code=%d body=%q headers=%v", w.Code, w.Body.String(), w.Header())
			}
		})
	}
}

func TestCFServiceTokenRecovery_NilBouncerIsPassThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CFServiceTokenRecovery(NewCFServiceTokenBouncer(nil), bounceEligible))
	router.NoRoute(func(c *gin.Context) { c.String(http.StatusOK, "spa") })
	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	for k, v := range with(navHeaders, "Cf-Access-Jwt-Assertion", bounceSvcHeader) {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Body.String() != "spa" {
		t.Fatalf("nil verifier must pass through, got %q", w.Body.String())
	}
}

func TestCFRecoveryURL(t *testing.T) {
	tests := []struct {
		in     string
		marker bool
		want   string
		ok     bool
	}{
		{"/a/b?x=1", true, "/a/b?cfreset=1&x=1", true},
		{"/a/b?cfreset=1&x=1", false, "/a/b?x=1", true},
		{"/", true, "/?cfreset=1", true},
		{"/ok", true, "/ok?cfreset=1", true},
		{"/q?next=%22%3E%3Cscript%3E", true, "/q?cfreset=1&next=%22%3E%3Cscript%3E", true},
	}
	// url.Parse reads "//evil.example/p" as a HOST; a request line can still carry
	// it as a PATH, so build that one by hand.
	if got, ok := CFRecoveryURL(&url.URL{Path: "//evil.example/p"}, true); ok || got != "" {
		t.Errorf("scheme-relative path must be refused, got %q, %v", got, ok)
	}
	for _, tc := range tests {
		u, err := url.Parse(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := CFRecoveryURL(u, tc.marker)
		if got != tc.want || ok != tc.ok {
			t.Errorf("CFRecoveryURL(%q, %v) = %q, %v; want %q, %v", tc.in, tc.marker, got, ok, tc.want, tc.ok)
		}
	}
}

// The target is escaped into the page, so a crafted query cannot break out of the
// attribute.
func TestWriteCFBouncePage_EscapesTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	WriteCFBouncePage(c, `/x?a="><script>alert(1)</script>`, false)
	if strings.Contains(w.Body.String(), "<script") || !strings.Contains(w.Body.String(), "&#34;&gt;&lt;script&gt;") {
		t.Fatalf("target was not escaped:\n%s", w.Body.String())
	}
}

func TestCFLogName_TruncatesAndSanitizes(t *testing.T) {
	if got := CFLogName("0123456789abcdef.access"); got != "01234567..." {
		t.Fatalf("got %q", got)
	}
	if got := CFLogName("short"); got != "short" {
		t.Fatalf("got %q", got)
	}
}

// The web UI requires Sec-Fetch headers, but says so (SkippedNoSecFetch, logged by
// the middleware) when it passes over a page-looking request only for lack of them.
func TestInspect_ReportsSkipForMissingSecFetch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := NewCFServiceTokenBouncer(bounceTestVerifier())
	tests := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"html accept, no sec-fetch", map[string]string{"Accept": "text/html,application/xhtml+xml", "Cf-Access-Jwt-Assertion": bounceSvcHeader}, true},
		{"json accept", map[string]string{"Accept": "application/json", "Cf-Access-Jwt-Assertion": bounceSvcHeader}, false},
		{"cors fetch", map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "cors", "Cf-Access-Jwt-Assertion": bounceSvcHeader}, false},
		{"person assertion", map[string]string{"Accept": "text/html", "Cf-Access-Jwt-Assertion": bouncePerson}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/library", nil)
			for k, v := range tc.headers {
				c.Request.Header.Set(k, v)
			}
			ins := b.Inspect(c)
			if ins.SkippedNoSecFetch != tc.want || ins.Kind != CFNavSkip {
				t.Fatalf("got kind=%v skipped=%v, want skip with skipped=%v", ins.Kind, ins.SkippedNoSecFetch, tc.want)
			}
		})
	}
}

// InspectBrowserHop needs no Sec-Fetch headers, but keeps the other exclusions.
func TestInspectBrowserHop_NoSecFetchRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := NewCFServiceTokenBouncer(bounceTestVerifier())
	tests := []struct {
		name    string
		method  string
		headers map[string]string
		want    CFNavKind
	}{
		{"no sec-fetch", http.MethodGet, map[string]string{"Cf-Access-Jwt-Assertion": bounceSvcHeader}, CFNavServiceToken},
		{"with sec-fetch", http.MethodGet, with(navHeaders, "Cf-Access-Jwt-Assertion", bounceSvcHeader), CFNavServiceToken},
		{"api key", http.MethodGet, map[string]string{"Cf-Access-Jwt-Assertion": bounceSvcHeader, "Authorization": "Bearer abk_0123456789"}, CFNavSkip},
		{"AppleCoreMedia", http.MethodGet, map[string]string{"Cf-Access-Jwt-Assertion": bounceSvcHeader, "User-Agent": "AppleCoreMedia/1.0"}, CFNavSkip},
		{"POST", http.MethodPost, map[string]string{"Cf-Access-Jwt-Assertion": bounceSvcHeader}, CFNavSkip},
		{"person", http.MethodGet, map[string]string{"Cf-Access-Jwt-Assertion": bouncePerson}, CFNavPerson},
		{"no assertion", http.MethodGet, nil, CFNavSkip},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(tc.method, "/auth/openid/browser", nil)
			for k, v := range tc.headers {
				c.Request.Header.Set(k, v)
			}
			if got := b.InspectBrowserHop(c).Kind; got != tc.want {
				t.Fatalf("kind = %v, want %v", got, tc.want)
			}
		})
	}
}
