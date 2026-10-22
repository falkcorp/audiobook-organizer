// file: internal/server/csp_report_test.go
// version: 1.0.0
// guid: b0cfe71e-4a49-4714-bc3b-9cbae5c214f5
// last-edited: 2026-10-21

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func cspReportRouter(rep *cspReporter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST(cspReportPath, rep.handle)
	return r
}

func cspReportPost(r *gin.Engine, contentType, body string) int {
	req := httptest.NewRequest(http.MethodPost, cspReportPath, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestParseCSPReports_BothWireFormats(t *testing.T) {
	legacy := parseCSPReports([]byte(`{"csp-report":{"document-uri":"https://example.test/invite/abcdef0123456789?token=secret#frag","violated-directive":"script-src-elem","effective-directive":"script-src-elem","blocked-uri":"https://evil.example.test/x.js?q=1","source-file":"https://example.test/assets/app.js","line-number":12}}`))
	require.Equal(t, []cspViolation{{directive: "script-src-elem", blocked: "https://evil.example.test/x.js", page: "https://example.test/invite/...", source: "https://example.test/assets/app.js", line: "12"}}, legacy,
		"query, fragment and the secret path tail are dropped")

	modern := parseCSPReports([]byte(`[{"type":"csp-violation","url":"https://example.test/","body":{"effectiveDirective":"img-src","blockedURL":"inline","documentURL":"https://example.test/library","sourceFile":"","lineNumber":3}},{"type":"deprecation","body":{}}]`))
	require.Equal(t, []cspViolation{{directive: "img-src", blocked: "inline", page: "https://example.test/library", source: "-", line: "3"}}, modern,
		"keywords stay as they are and non-CSP reports are ignored")

	for _, junk := range []string{"", "not json", "{}", "[]", `"x"`, `{"csp-report":null}`, `[1,2]`} {
		require.Empty(t, parseCSPReports([]byte(junk)), "%q", junk)
	}
}

func TestCSPLogField_StripsControlCharactersAndBoundsLength(t *testing.T) {
	require.Equal(t, "a b c", cspLogField("a\nb\rc"))
	require.Equal(t, "-", cspLogField(""))
	require.Len(t, cspLogField(strings.Repeat("x", 5000)), cspReportMaxField+3)
	require.Equal(t, "data:", cspLogURL("data:image/png;base64,AAAA", false), "no payload from a data: URI")
}

func TestCSPReportHandler_StatusCodes(t *testing.T) {
	r := cspReportRouter(newCSPReporter(rate.NewLimiter(rate.Inf, 1)))
	const ok = `{"csp-report":{"violated-directive":"img-src","blocked-uri":"inline"}}`
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/csp-report", ok))
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/reports+json; charset=utf-8", "[]"))
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/json", "not json"), "malformed bodies are accepted and ignored")
	require.Equal(t, http.StatusUnsupportedMediaType, cspReportPost(r, "text/plain", ok))
	require.Equal(t, http.StatusUnsupportedMediaType, cspReportPost(r, "", ok))
	require.Equal(t, http.StatusBadRequest, cspReportPost(r, "application/csp-report", strings.Repeat("a", cspReportMaxBody+1)), "over the body cap")
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/csp-report", strings.Repeat(" ", cspReportMaxBody-len(ok))+ok), "exactly at the cap is fine")
}

// Over the limit a report is dropped and counted, still answered 204, and the
// count is reported on the next one let through.
func TestCSPReportHandler_RateLimited(t *testing.T) {
	lim := rate.NewLimiter(rate.Every(time.Hour), 2)
	rep := newCSPReporter(lim)
	r := cspReportRouter(rep)
	const body = `{"csp-report":{"violated-directive":"img-src","blocked-uri":"inline"}}`
	for range 5 {
		require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/csp-report", body), "a caller cannot tell whether it was dropped")
	}
	require.EqualValues(t, 3, rep.dropped.Load(), "burst of 2 allowed, 3 dropped")
	lim.SetLimit(rate.Inf)
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/csp-report", body))
	require.Zero(t, rep.dropped.Load(), "the drop count was reported and reset")
}

// The real router serves the route without any login, ahead of the auth chain.
func TestCSPReportRoute_IsPublicOnTheRealRouter(t *testing.T) {
	fx := setupCredGuardServerWith(t, nil)
	req := httptest.NewRequest(http.MethodPost, cspReportPath, strings.NewReader(`{"csp-report":{"violated-directive":"img-src","blocked-uri":"inline"}}`))
	req.Header.Set("Content-Type", "application/csp-report")
	req.RemoteAddr = "192.0.2.5:4000"
	w := httptest.NewRecorder()
	fx.srv.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Contains(t, w.Header().Get("Content-Security-Policy"), "report-uri "+cspReportPath)
}
