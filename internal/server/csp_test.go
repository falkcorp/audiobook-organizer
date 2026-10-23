// file: internal/server/csp_test.go
// version: 1.2.0
// guid: a8dee6b8-6533-4d26-9f2c-34437fd9caa3
// last-edited: 2026-10-21

package server

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// restoreCSP puts the process-wide policy back after a test that changes it.
func restoreCSP(t *testing.T) {
	t.Helper()
	prev := cspHeader.Load()
	t.Cleanup(func() { cspHeader.Store(prev) })
}

func setAccessTeam(t *testing.T, team string) {
	t.Helper()
	prev := config.CFAccessTeam()
	config.Mutate(func(c *config.Config) { c.CFAccessTeamDomain = team })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.CFAccessTeamDomain = prev }) })
}

func cspDirective(policy, name string) string {
	for _, d := range strings.Split(policy, ";") {
		d = strings.TrimSpace(d)
		if d == name || strings.HasPrefix(d, name+" ") {
			return d
		}
	}
	return ""
}

func TestSecurityHeaders_SendCSPAndPermissionsPolicy(t *testing.T) {
	setAccessTeam(t, "")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(securityHeadersMiddleware())
	r.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "<html></html>") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := w.Header().Get("Content-Security-Policy")
	require.NotEmpty(t, csp, "the SPA shell must carry a Content-Security-Policy")
	require.Equal(t, "default-src 'self'", cspDirective(csp, "default-src"))
	require.Equal(t, "script-src 'self'", cspDirective(csp, "script-src"),
		"no inline script is allowed until a hash is installed, and never 'unsafe-inline'")
	require.NotContains(t, cspDirective(csp, "script-src"), "unsafe")
	require.Equal(t, "object-src 'none'", cspDirective(csp, "object-src"))
	require.Equal(t, "base-uri 'none'", cspDirective(csp, "base-uri"))
	require.Equal(t, "frame-ancestors 'self'", cspDirective(csp, "frame-ancestors"))
	require.Equal(t, "connect-src 'self'", cspDirective(csp, "connect-src"),
		"with no Access team configured nothing beyond the origin is allowed, and never a wildcard")
	require.Equal(t, "report-uri "+cspReportPath, cspDirective(csp, "report-uri"))
	require.Equal(t, "report-to "+cspReportGroup, cspDirective(csp, "report-to"))
	require.Equal(t, cspReportGroup+`="`+cspReportPath+`"`, w.Header().Get("Reporting-Endpoints"))
	require.Contains(t, cspDirective(csp, "img-src"), "https:", "metadata search shows provider cover URLs")
	require.Contains(t, cspDirective(csp, "img-src"), "data:")

	pp := w.Header().Get("Permissions-Policy")
	for _, f := range []string{"camera=()", "microphone=()", "geolocation=()", "payment=()", "usb=()"} {
		require.Contains(t, pp, f)
	}
	require.NotContains(t, pp, "clipboard", "the UI copies to the clipboard")
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}

// A handler that sets its own policy (the OIDC browser hop's page, the
// Cloudflare recovery page) keeps it: the middleware runs first.
func TestSecurityHeaders_HandlerCSPWins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const own = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'"
	r := gin.New()
	r.Use(securityHeadersMiddleware())
	r.GET("/own", func(c *gin.Context) {
		c.Header("Content-Security-Policy", own)
		c.String(http.StatusOK, "x")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/own", nil))
	require.Equal(t, []string{own}, w.Header().Values("Content-Security-Policy"),
		"exactly one policy, the handler's own")
}

// The hash is over the exact bytes between the tags and is recomputed here
// without the code under test's helper.
func TestInlineScriptHashSources(t *testing.T) {
	const body = "\n      (function () { document.documentElement.dataset.x = '1'; })();\n    "
	sum := sha256.Sum256([]byte(body))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	html := []byte("<!doctype html><html><head>" +
		"<script>" + body + "</script>" +
		"<script type=\"module\" crossorigin src=\"/assets/index-x.js\"></script>" +
		"<script src='/a.js'></script>" +
		"<script>   </script>" +
		"</head></html>")
	require.Equal(t, []string{want}, inlineScriptHashSources(html),
		"only the one inline script is hashed; external and empty ones are not")

	// An inline module script is hashed too, and a data-src attribute is not a src.
	two := inlineScriptHashSources([]byte("<script type=module>a()</script><script data-src=x>b()</script>"))
	require.Len(t, two, 2)
	require.Empty(t, inlineScriptHashSources([]byte("<html><body>no scripts</body></html>")))
}

func TestSetSPAInlineScripts_AllowsExactlyThoseHashes(t *testing.T) {
	restoreCSP(t)
	index := []byte("<html><head><script>boot()</script><script src=\"/assets/app.js\"></script></head></html>")
	require.Equal(t, 1, setSPAInlineScripts(index))
	sum := sha256.Sum256([]byte("boot()"))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	require.Equal(t, "script-src 'self' "+hash, cspDirective(contentSecurityPolicy(), "script-src"))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(securityHeadersMiddleware())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Contains(t, w.Header().Get("Content-Security-Policy"), hash,
		"the middleware serves the installed policy, not a copy taken at construction")

	// A different index replaces the hashes rather than adding to them.
	require.Equal(t, 0, setSPAInlineScripts([]byte("<html></html>")))
	require.Equal(t, "script-src 'self'", cspDirective(contentSecurityPolicy(), "script-src"))
}

// apiFetch recognises an expired Access session by following the edge's
// redirect to the team's login host, so connect-src names exactly that host,
// read from config on every response, and never a wildcard.
func TestCSP_ConnectSrcNamesOnlyTheConfiguredAccessTeam(t *testing.T) {
	restoreCSP(t)
	setAccessTeam(t, "team-x.cloudflareaccess.com")
	require.Equal(t, "connect-src 'self' https://team-x.cloudflareaccess.com", cspDirective(contentSecurityPolicy(), "connect-src"))
	require.NotContains(t, contentSecurityPolicy(), "*", "no wildcard anywhere in the policy")

	// A change takes effect without a restart, and an installed hash survives it.
	setSPAInlineScripts([]byte("<script>boot()</script>"))
	setAccessTeam(t, "  HTTPS://Other-Team.example.test/ ")
	require.Equal(t, "connect-src 'self' https://other-team.example.test", cspDirective(contentSecurityPolicy(), "connect-src"),
		"trimmed, lowercased, scheme and trailing slash tolerated")
	require.Contains(t, cspDirective(contentSecurityPolicy(), "script-src"), "'sha256-")
	setAccessTeam(t, "")
	require.Equal(t, "connect-src 'self'", cspDirective(contentSecurityPolicy(), "connect-src"), "unset: nothing added")
}

// A malformed team can neither widen nor corrupt the header.
func TestCSP_MalformedAccessTeamIsDropped(t *testing.T) {
	restoreCSP(t)
	for _, bad := range []string{
		"*.cloudflareaccess.com", "team.example.test; script-src *", "team.example.test other.test",
		"https://team.example.test/path", "team", "-bad.example.test", "team..example.test",
		"team.example.test:8443", "team.example.test'", "evil.test\r\nX-Injected: 1",
	} {
		setAccessTeam(t, bad)
		require.Equal(t, "connect-src 'self'", cspDirective(contentSecurityPolicy(), "connect-src"), "%q", bad)
		require.NotContains(t, contentSecurityPolicy(), "X-Injected")
	}
}
