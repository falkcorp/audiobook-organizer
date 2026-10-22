// file: internal/server/csp_report_test.go
// version: 1.2.0
// guid: b0cfe71e-4a49-4714-bc3b-9cbae5c214f5
// last-edited: 2026-10-22

package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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

// slogMessages records the message of every record the default slog logger gets
// (logger.New writes through it), unformatted by any handler.
type slogMessages struct {
	mu   sync.Mutex
	msgs []string
}

func (m *slogMessages) Enabled(context.Context, slog.Level) bool { return true }
func (m *slogMessages) Handle(_ context.Context, r slog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, r.Message)
	return nil
}
func (m *slogMessages) WithAttrs([]slog.Attr) slog.Handler { return m }
func (m *slogMessages) WithGroup(string) slog.Handler      { return m }

func (m *slogMessages) all() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.msgs...)
}

func captureSlog(t *testing.T) *slogMessages {
	t.Helper()
	m := &slogMessages{}
	prev := slog.Default()
	slog.SetDefault(slog.New(m))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return m
}

const cspTestToken = "abcdef0123456789SECRETTOKEN"

func TestParseCSPReports_BothWireFormats(t *testing.T) {
	// A DIFFERENT host than the page's, so a same-origin source is told apart from
	// a cross-origin one.
	legacy := parseCSPReports([]byte(`{"csp-report":{"document-uri":"https://example.test/invite/`+cspTestToken+`?token=secret#frag","violated-directive":"script-src-elem","effective-directive":"script-src-elem","blocked-uri":"https://evil.example.com/x.js?q=1","source-file":"https://cdn.example.com/assets/app.js","line-number":12}}`), "example.test")
	require.Equal(t, []cspViolation{{directive: "script-src-elem", blocked: "https://evil.example.com/x.js", page: "https://example.test/invite/...", source: "https://cdn.example.com/assets/app.js", line: "12"}}, legacy,
		"query, fragment and the secret path tail are dropped; a cross-origin URL keeps its path")

	modern := parseCSPReports([]byte(`[{"type":"csp-violation","url":"https://example.test/","body":{"effectiveDirective":"img-src","blockedURL":"inline","documentURL":"https://example.test/library","sourceFile":"","lineNumber":3}},{"type":"deprecation","body":{}}]`), "example.test")
	require.Equal(t, []cspViolation{{directive: "img-src", blocked: "inline", page: "https://example.test/library", source: "-", line: "3"}}, modern,
		"keywords stay as they are and non-CSP reports are ignored")

	for _, junk := range []string{"", "not json", "{}", "[]", `"x"`, `{"csp-report":null}`, `[1,2]`} {
		require.Empty(t, parseCSPReports([]byte(junk), "example.test"), "%q", junk)
	}
}

// B-1: the invite token lives in the path of /invite/<token>. Every URL field,
// with or without a scheme, must be reduced the same way, so it never reaches the
// log through the source file, the blocked URI or a relative document URI.
func TestParseCSPReports_NoURLFieldCarriesAnInviteToken(t *testing.T) {
	invite := "/invite/" + cspTestToken
	cases := map[string]string{
		"legacy source-file, same origin":   `{"csp-report":{"source-file":"https://example.test` + invite + `","blocked-uri":"inline"}}`,
		"legacy source-file, with query":    `{"csp-report":{"source-file":"https://example.test` + invite + `?x=1#y"}}`,
		"report-to sourceFile":              `[{"type":"csp-violation","body":{"sourceFile":"https://example.test` + invite + `"}}]`,
		"legacy blocked-uri, same origin":   `{"csp-report":{"blocked-uri":"https://example.test` + invite + `"}}`,
		"report-to blockedURL":              `[{"type":"csp-violation","body":{"blockedURL":"https://example.test` + invite + `"}}]`,
		"relative document-uri":             `{"csp-report":{"document-uri":"` + invite + `"}}`,
		"report-to relative documentURL":    `[{"type":"csp-violation","body":{"documentURL":"` + invite + `"}}]`,
		"schemeless document-uri":           `{"csp-report":{"document-uri":"example.test` + invite + `"}}`,
		"schemeless blocked-uri":            `{"csp-report":{"blocked-uri":"example.test` + invite + `"}}`,
		"scheme-relative source-file":       `{"csp-report":{"source-file":"//example.test` + invite + `"}}`,
		"upper-case host":                   `{"csp-report":{"source-file":"https://EXAMPLE.test` + invite + `"}}`,
		"deep same-origin path":             `{"csp-report":{"source-file":"https://example.test/some/other/` + cspTestToken + `/deeper"}}`,
		"relative path that is not a route": `{"csp-report":{"document-uri":"/unknown/` + cspTestToken + `"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseCSPReports([]byte(body), "example.test")
			require.Len(t, got, 1)
			require.NotContains(t, strings.Join([]string{got[0].directive, got[0].blocked, got[0].page, got[0].source, got[0].line}, "|"), cspTestToken)
		})
	}
}

func TestCSPLogURL_Reduction(t *testing.T) {
	const host = "example.test"
	for raw, want := range map[string]string{
		"":                           "",
		"inline":                     "inline",
		"eval":                       "eval",
		"self":                       "self",
		"data:image/png;base64,AAAA": "data:",
		"blob:https://example.test/" + cspTestToken: "blob:",
		"https://example.test/":                     "https://example.test/",
		"https://example.test/assets/app.js":        "https://example.test/assets/...",
		"https://example.test/library?q=1#x":        "https://example.test/library",
		"https://cdn.example.com/a/b/c.js?q=1":      "https://cdn.example.com/a/b/c.js",
		"/invite/" + cspTestToken:                   "/invite/...",
		"example.test/invite/" + cspTestToken:       "example.test/...",
	} {
		require.Equal(t, want, cspLogURL(raw, host), "%q", raw)
	}
}

// The log line itself, as written: no token anywhere, and every value quoted.
func TestCSPReportHandler_LogLineCarriesNoToken(t *testing.T) {
	buf := captureSlog(t)
	r := cspReportRouter(newCSPReporter(rate.NewLimiter(rate.Inf, 1)))
	body := `{"csp-report":{"document-uri":"/invite/` + cspTestToken + `","blocked-uri":"https://example.test/invite/` + cspTestToken + `","source-file":"example.test/invite/` + cspTestToken + `","violated-directive":"script-src"}}`
	req := httptest.NewRequest(http.MethodPost, cspReportPath, strings.NewReader(body))
	req.Host = "example.test"
	req.Header.Set("Content-Type", "application/csp-report")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	msgs := buf.all()
	require.Len(t, msgs, 1)
	require.Contains(t, msgs[0], "CSP violation")
	require.NotContains(t, msgs[0], cspTestToken)
}

// N2: values are attacker-controlled text. Quoting means a quote or an embedded
// newline cannot end the field or forge another line.
func TestCSPReportHandler_ValuesAreQuoted(t *testing.T) {
	buf := captureSlog(t)
	r := cspReportRouter(newCSPReporter(rate.NewLimiter(rate.Inf, 1)))
	body := `{"csp-report":{"violated-directive":"img-src\" source=\"forged","blocked-uri":"inline\nWARN forged line"}}`
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/csp-report", body))
	msgs := buf.all()
	require.Len(t, msgs, 1)
	msg := msgs[0]
	require.NotContains(t, msg, "\n", "an embedded newline cannot forge a second line")
	// The quote in the value is escaped, so it cannot close the field and open a
	// forged source= of its own.
	require.Contains(t, msg, `directive="img-src\" source=\"forged"`)
	require.Contains(t, msg, `blocked="inline WARN forged line"`)
}

func TestCSPLogField_StripsControlCharactersAndBoundsLength(t *testing.T) {
	require.Equal(t, "a b c", cspLogField("a\nb\rc"))
	require.Equal(t, "-", cspLogField(""))
	require.Len(t, cspLogField(strings.Repeat("x", 5000)), cspReportMaxField+3)
}

// N1: the cut lands on a rune boundary, never inside a multi-byte character.
func TestCSPLogField_TruncatesOnARuneBoundary(t *testing.T) {
	for pad := 0; pad < 4; pad++ {
		in := strings.Repeat("a", pad) + strings.Repeat("\u00e9", 300) // two bytes per rune
		got := cspLogField(in)
		require.True(t, utf8.ValidString(got), "pad %d: %q", pad, got)
		require.True(t, strings.HasSuffix(got, "..."))
		require.LessOrEqual(t, len(got), cspReportMaxField+3)
	}
	require.True(t, utf8.ValidString(cspLogField(strings.Repeat("\U0001F600", 100))))
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
	rep.newVisitor = func() *rate.Limiter { return rate.NewLimiter(rate.Inf, 1) } // this test is about the service limit
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

// S-2: the limit counts REPORTS, not requests. One request carrying many reports
// is charged for every one of them.
func TestCSPReportHandler_LimitCountsReportsNotRequests(t *testing.T) {
	lim := rate.NewLimiter(rate.Every(time.Hour), 4)
	rep := newCSPReporter(lim)
	rep.newVisitor = func() *rate.Limiter { return rate.NewLimiter(rate.Inf, 1) }
	r := cspReportRouter(rep)
	one := `{"type":"csp-violation","body":{"effectiveDirective":"img-src","blockedURL":"inline"}}`
	batch := "[" + strings.TrimSuffix(strings.Repeat(one+",", 6), ",") + "]"
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/reports+json", batch))
	require.EqualValues(t, 6, rep.dropped.Load(), "six reports against a burst of four: all six are dropped, not one request")
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/reports+json", "["+one+","+one+"]"))
	require.Zero(t, rep.dropped.Load(), "two reports fit in the burst of four, were logged, and the six dropped ones were reported with them")
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/reports+json", "["+one+","+one+","+one+"]"))
	require.EqualValues(t, 3, rep.dropped.Load(), "three more do not fit: only two tokens were left")
}

// A request carries at most cspReportMaxPerRequest reports into the limiter.
func TestParseCSPReports_BoundedPerRequest(t *testing.T) {
	one := `{"type":"csp-violation","body":{"effectiveDirective":"img-src"}}`
	batch := "[" + strings.TrimSuffix(strings.Repeat(one+",", 50), ",") + "]"
	require.Len(t, parseCSPReports([]byte(batch), "example.test"), cspReportMaxPerRequest)
}

// N9: one caller cannot spend the service's budget and leave another's reports
// unlogged.
func TestCSPReporter_PerVisitorBucket(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rep := newCSPReporter(rate.NewLimiter(rate.Inf, 1))
	rep.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		require.True(t, rep.allow("198.51.100.1", 1), "burst of five for one visitor: %d", i)
	}
	require.False(t, rep.allow("198.51.100.1", 1), "sixth report from the same visitor")
	require.True(t, rep.allow("198.51.100.2", 1), "another visitor is unaffected")
	require.False(t, rep.allow("198.51.100.2", 6), "a batch bigger than the burst is refused whole")
	now = now.Add(11 * time.Second)
	require.True(t, rep.allow("198.51.100.1", 1), "one more report every ten seconds")
}

func TestCSPReporter_VisitorTableIsBoundedAndSwept(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rep := newCSPReporter(rate.NewLimiter(rate.Inf, 1))
	rep.now = func() time.Time { return now }
	for i := 0; i < maxCSPVisitors+500; i++ {
		rep.allow("v"+strconv.Itoa(i), 1)
	}
	require.LessOrEqual(t, rep.visitors.Len(), maxCSPVisitors)
	now = now.Add(cspVisitorIdle + time.Second)
	rep.allow("fresh", 1)
	require.Equal(t, 1, rep.visitors.Len(), "idle visitors are swept")
}
