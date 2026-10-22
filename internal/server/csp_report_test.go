// file: internal/server/csp_report_test.go
// version: 1.3.0
// guid: b0cfe71e-4a49-4714-bc3b-9cbae5c214f5
// last-edited: 2026-10-22

package server

import (
	"context"
	"fmt"
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
	legacy := parseCSPReports([]byte(`{"csp-report":{"document-uri":"https://example.test/invite/` + cspTestToken + `?token=secret#frag","violated-directive":"script-src-elem","effective-directive":"script-src-elem","blocked-uri":"https://evil.example.com/x.js?q=1","source-file":"https://cdn.example.com/assets/app.js","line-number":12}}`))
	require.Equal(t, []cspViolation{{directive: "script-src-elem", blocked: "https://evil.example.com/x.js", page: "https://example.test/invite/...", source: "https://cdn.example.com/assets/app.js", line: "12"}}, legacy,
		"query, fragment and the secret path tail are dropped; a cross-origin URL keeps its path")

	modern := parseCSPReports([]byte(`[{"type":"csp-violation","url":"https://example.test/","body":{"effectiveDirective":"img-src","blockedURL":"inline","documentURL":"https://example.test/library","sourceFile":"","lineNumber":3}},{"type":"deprecation","body":{}}]`))
	require.Equal(t, []cspViolation{{directive: "img-src", blocked: "inline", page: "https://example.test/library", source: "-", line: "3"}}, modern,
		"keywords stay as they are and non-CSP reports are ignored")

	for _, junk := range []string{"", "not json", "{}", "[]", `"x"`, `{"csp-report":null}`, `[1,2]`} {
		require.Empty(t, parseCSPReports([]byte(junk)), "%q", junk)
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
		"deep same-origin path":             `{"csp-report":{"document-uri":"https://example.test/","source-file":"https://example.test/some/other/` + cspTestToken + `/deeper"}}`,
		"relative path that is not a route": `{"csp-report":{"document-uri":"/unknown/` + cspTestToken + `"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseCSPReports([]byte(body))
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
		require.Equal(t, want, cspLogURL(raw, host, false), "%q", raw)
	}
	// The document URL is always cut to its first segment, whatever its host.
	for raw, want := range map[string]string{
		"https://example.test/library/books/12": "https://example.test/library/...",
		"https://other.example/a/b":             "https://other.example/a/...",
		"https://example.test/library":          "https://example.test/library",
	} {
		require.Equal(t, want, cspLogURL(raw, "", true), "document %q", raw)
	}
}

// S2: which URLs are same-origin is decided against the REPORTED document's host,
// not the request's Host (a proxy may rewrite it), and the document URL itself is
// always cut to its first segment. The invite prefix matches without regard to
// case and across empty segments. Nothing here may leak the token, whatever Host
// the request arrived with.
func TestCSPReportHandler_SameOriginIsTheReportedDocumentsNotTheRequestHost(t *testing.T) {
	buf := captureSlog(t)
	r := cspReportRouter(newCSPReporter(rate.NewLimiter(rate.Inf, 1)))
	pages := []string{
		"https://public.example/invite/" + cspTestToken,
		"https://public.example//invite/" + cspTestToken,
		"https://public.example/INVITE/" + cspTestToken,
		"https://public.example/Invite/" + cspTestToken,
		"https://public.example/%69nvite/" + cspTestToken,
		"https://public.example/invite/" + cspTestToken + "/",
		"https://public.example/api/v1/auth/invite/" + cspTestToken,
		"https://public.example/api/v1/Auth//invite/" + cspTestToken,
		"https://public.example/some/unknown/" + cspTestToken,
	}
	for _, host := range []string{"public.example", "127.0.0.1:8484", "Public.Example", ""} {
		for _, page := range pages {
			for _, field := range []string{"documentURL", "sourceFile", "blockedURL"} {
				body := `[{"type":"csp-violation","body":{"documentURL":"https://public.example/","` + field + `":"` + page + `"}}]`
				req := httptest.NewRequest(http.MethodPost, cspReportPath, strings.NewReader(body))
				req.Host = host
				req.Header.Set("Content-Type", "application/reports+json")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Equal(t, http.StatusNoContent, w.Code)
			}
		}
	}
	msgs := buf.all()
	require.NotEmpty(t, msgs)
	for _, m := range msgs {
		require.NotContains(t, m, cspTestToken)
	}
}

func TestParseCSPReports_DocumentIsAlwaysCutAndSameOriginFollowsTheDocument(t *testing.T) {
	got := parseCSPReports([]byte(`{"csp-report":{"document-uri":"https://app.example/library/books/42?x=1","blocked-uri":"https://app.example/assets/a/b.js","source-file":"https://cdn.example/assets/a/b.js"}}`))
	require.Len(t, got, 1)
	require.Equal(t, "https://app.example/library/...", got[0].page, "the page is always a first segment")
	require.Equal(t, "https://app.example/assets/...", got[0].blocked, "same host as the reported document: cut")
	require.Equal(t, "https://cdn.example/assets/a/b.js", got[0].source, "another host keeps its path")
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
// is charged for every one of them; at most one burst's worth is charged and
// logged, and the rest are dropped and counted.
func TestCSPReportHandler_LimitCountsReportsNotRequests(t *testing.T) {
	buf := captureSlog(t)
	lim := rate.NewLimiter(rate.Every(time.Hour), 4)
	rep := newCSPReporter(lim)
	rep.newVisitor = func() *rate.Limiter { return rate.NewLimiter(rate.Inf, 1) }
	r := cspReportRouter(rep)
	one := `{"type":"csp-violation","body":{"effectiveDirective":"img-src","blockedURL":"inline"}}`
	logged := func() int {
		n := 0
		for _, m := range buf.all() {
			if strings.Contains(m, "CSP violation") {
				n++
			}
		}
		return n
	}
	batch := "[" + strings.TrimSuffix(strings.Repeat(one+",", 6), ",") + "]"
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/reports+json", batch))
	require.Equal(t, 4, logged(), "six reports against a burst of four: four are logged")
	require.Zero(t, rep.dropped.Load(), "the drop count was taken into the line that was let through")
	var note string
	for _, m := range buf.all() {
		if strings.Contains(m, "were dropped") {
			note = m
		}
	}
	require.Contains(t, note, "2 violation report(s) were dropped", "...the other two are reported as dropped")
	require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/reports+json", "["+one+"]"))
	require.Equal(t, 4, logged(), "the budget is spent: nothing more is logged")
	require.EqualValues(t, 1, rep.dropped.Load())
}

// S4: with the production limiters a report-to batch of 6 to 10 violations (what a
// page load with several blocked styles sends) is logged, not dropped for good.
func TestCSPReportHandler_FullBatchIsLoggedWithProductionLimits(t *testing.T) {
	for _, n := range []int{1, 5, 6, 8, 10} {
		buf := captureSlog(t)
		rep := newCSPReporter(rate.NewLimiter(rate.Every(2*time.Second), 10))
		r := cspReportRouter(rep)
		var items []string
		for i := 0; i < n; i++ {
			items = append(items, fmt.Sprintf(`{"type":"csp-violation","body":{"effectiveDirective":"style-src-elem","blockedURL":"inline","lineNumber":%d}}`, i))
		}
		require.Equal(t, http.StatusNoContent, cspReportPost(r, "application/reports+json", "["+strings.Join(items, ",")+"]"))
		logged := 0
		for _, m := range buf.all() {
			if strings.Contains(m, "CSP violation") {
				logged++
			}
		}
		require.Equal(t, n, logged, "a batch of %d is logged in full", n)
		require.Zero(t, rep.dropped.Load())
	}
}

// S4: a batch bigger than the burst is capped at it, logged to that size, and the
// excess counted, instead of being refused whole for ever.
func TestCSPReporter_BatchOverTheBurstIsCappedNotRefused(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rep := newCSPReporter(rate.NewLimiter(rate.Inf, 1))
	rep.newVisitor = func() *rate.Limiter { return rate.NewLimiter(rate.Every(time.Hour), 4) }
	rep.now = func() time.Time { return now }
	require.Equal(t, 4, rep.admit("198.51.100.1", 9), "capped at the visitor's burst")
	require.Equal(t, 0, rep.admit("198.51.100.1", 1), "and that spent it")
}

// S4: the service-wide limiter is asked first, and a request it refuses spends none
// of the visitor's tokens (and one the visitor's limiter refuses gives the service
// its tokens back).
func TestCSPReporter_RefusalSpendsNeitherBudget(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	svc := rate.NewLimiter(rate.Every(time.Hour), 10)
	rep := newCSPReporter(svc)
	rep.now = func() time.Time { return now }
	require.Equal(t, 10, rep.admit("someone-else", 10)) // the service budget is gone
	for i := 0; i < 20; i++ {
		require.Equal(t, 0, rep.admit("198.51.100.1", 5), "refused by the service limiter")
	}
	svc.SetLimit(rate.Inf)
	require.Equal(t, 10, rep.admit("198.51.100.1", 10), "the visitor still has its whole burst: the refusals cost it nothing")

	// The other way round: the visitor's refusal hands the service its tokens back.
	svc2 := rate.NewLimiter(rate.Every(time.Hour), 20)
	rep2 := newCSPReporter(svc2)
	rep2.now = func() time.Time { return now }
	require.Equal(t, 10, rep2.admit("hog", 10))
	require.InDelta(t, 10, svc2.TokensAt(now), 0.001)
	require.Equal(t, 0, rep2.admit("hog", 5), "the hog's own budget is spent")
	require.InDelta(t, 10, svc2.TokensAt(now), 0.001, "...and that attempt took nothing from the service")
}

// N9: one caller cannot spend the service's budget and leave another's reports
// unlogged.
func TestCSPReporter_PerVisitorBucket(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rep := newCSPReporter(rate.NewLimiter(rate.Inf, 1))
	rep.now = func() time.Time { return now }
	for i := 0; i < cspReportMaxPerRequest; i++ {
		require.Equal(t, 1, rep.admit("198.51.100.1", 1), "burst of %d for one visitor: %d", cspReportMaxPerRequest, i)
	}
	require.Equal(t, 0, rep.admit("198.51.100.1", 1), "one past the burst from the same visitor")
	require.Equal(t, 1, rep.admit("198.51.100.2", 1), "another visitor is unaffected")
	now = now.Add(11 * time.Second)
	require.Equal(t, 1, rep.admit("198.51.100.1", 1), "one more report every ten seconds")
}

func TestCSPReporter_VisitorTableIsBoundedAndSwept(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rep := newCSPReporter(rate.NewLimiter(rate.Inf, 1))
	rep.now = func() time.Time { return now }
	for i := 0; i < maxCSPVisitors+500; i++ {
		rep.admit("v"+strconv.Itoa(i), 1)
	}
	require.LessOrEqual(t, rep.visitors.Len(), maxCSPVisitors)
	now = now.Add(cspVisitorIdle + time.Second)
	rep.admit("fresh", 1)
	require.Equal(t, 1, rep.visitors.Len(), "idle visitors are swept")
}
