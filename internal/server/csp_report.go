// file: internal/server/csp_report.go
// version: 1.1.0
// guid: d896d6d8-b883-482d-8e34-d41a4abd52df
// last-edited: 2026-10-22

package server

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Where a browser reports Content-Security-Policy violations (csp.go points
// report-uri and report-to at it). The route is unauthenticated on purpose: the
// browser sends it with no identity beyond what the page already has, a blocked
// page may be one the visitor is not signed in on, and it has no side effect
// except one log line.
const (
	cspReportPath  = "/api/v1/csp-report"
	cspReportGroup = "csp"

	// cspReportMaxBody bounds what is read from a report. A real one is well
	// under 2 KiB.
	cspReportMaxBody = 8 << 10
	// cspReportMaxField bounds each value written to the log.
	cspReportMaxField = 200
)

var cspReportLog = logger.New("server.csp")

// cspReporter logs violation reports behind two rate limits, so an anonymous
// caller cannot fill the log or burn CPU with them: one for the whole service and
// one per visitor key, so a single caller cannot spend the service's budget and
// leave a real browser's reports unlogged. The limits count REPORTS, not
// requests: a request carries up to cspReportMaxPerRequest of them. Reports over
// a limit are dropped and counted; the count is added to the next line that is
// let through.
type cspReporter struct {
	limiter *rate.Limiter
	dropped atomic.Int64

	// visitors are the per-visitor limiters, keyed on loginThrottleKey (the
	// tunnel visitor where one can be believed, else the peer), bounded and swept
	// from the cold end.
	mu         sync.Mutex
	visitors   *absauth.LRU[*cspVisitor]
	newVisitor func() *rate.Limiter
	now        func() time.Time
}

type cspVisitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

const (
	// cspReportMaxPerRequest bounds the reports read from one request.
	cspReportMaxPerRequest = 10
	// maxCSPVisitors bounds the per-visitor limiter table.
	maxCSPVisitors = 2000
	// cspVisitorIdle is how long a visitor's limiter is kept after its last report.
	cspVisitorIdle = 10 * time.Minute
)

// newCSPReporter takes the service-wide limiter. The per-visitor limiter allows a
// burst of 5 reports and one more every 10 seconds.
func newCSPReporter(limiter *rate.Limiter) *cspReporter {
	return &cspReporter{
		limiter:    limiter,
		visitors:   absauth.NewLRU[*cspVisitor](maxCSPVisitors),
		newVisitor: func() *rate.Limiter { return rate.NewLimiter(rate.Every(10*time.Second), 5) },
		now:        time.Now,
	}
}

// The service-wide limit: a burst of 10 reports and one more every two seconds
// (30 a minute sustained): enough to see every distinct violation of a page
// load, far less than a flood.
var defaultCSPReporter = newCSPReporter(rate.NewLimiter(rate.Every(2*time.Second), 10))

// allow charges n reports to the visitor's limiter and then the service's, and
// reports whether they may be logged.
func (r *cspReporter) allow(visitor string, n int) bool {
	if visitor == "" {
		visitor = "?"
	}
	now := r.now()
	r.mu.Lock()
	r.visitors.Sweep(func(v *cspVisitor) bool { return now.Sub(v.lastSeen) > cspVisitorIdle })
	v, ok := r.visitors.Get(visitor)
	if !ok {
		v = &cspVisitor{limiter: r.newVisitor()}
	}
	v.lastSeen = now
	r.visitors.Put(visitor, v)
	r.mu.Unlock()
	return v.limiter.AllowN(now, n) && r.limiter.AllowN(now, n)
}

// handle answers 204 for any well-formed request, logged or not (a browser does
// not retry, and a caller learns nothing about whether it was rate limited), 415
// for a content type a browser never sends, and 400 for a body over the cap.
func (r *cspReporter) handle(c *gin.Context) {
	mt, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	switch mt {
	case "application/csp-report", "application/reports+json", "application/json":
	default:
		c.AbortWithStatus(http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, cspReportMaxBody+1))
	if err != nil || len(body) > cspReportMaxBody {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	reports := parseCSPReports(body, c.Request.Host)
	if len(reports) == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	if !r.allow(loginThrottleKey(c), len(reports)) {
		r.dropped.Add(int64(len(reports)))
		c.Status(http.StatusNoContent)
		return
	}
	if n := r.dropped.Swap(0); n > 0 {
		cspReportLog.Warn("CSP: %d violation report(s) were dropped while rate limited", n)
	}
	for _, v := range reports {
		// %q: every value is attacker-controlled text.
		cspReportLog.Warn("CSP violation: directive=%q blocked=%q page=%q source=%q line=%q",
			v.directive, v.blocked, v.page, v.source, v.line)
	}
	c.Status(http.StatusNoContent)
}

// cspViolation is one report reduced to the fields worth logging, each already
// sanitised by cspLogField.
type cspViolation struct{ directive, blocked, page, source, line string }

// parseCSPReports reads both wire formats: report-uri's
// {"csp-report": {...}} and report-to's [{"type": "csp-violation", "body": {...}}].
// Anything else yields nothing. host is the request's Host, which decides which
// reported URLs are same-origin (cspLogURL).
func parseCSPReports(body []byte, host string) []cspViolation {
	var legacy struct {
		Report map[string]any `json:"csp-report"`
	}
	if json.Unmarshal(body, &legacy) == nil && legacy.Report != nil {
		return []cspViolation{cspViolationFrom(legacy.Report, host, "violated-directive", "blocked-uri", "document-uri", "source-file", "line-number", "effective-directive")}
	}
	var batch []struct {
		Type string         `json:"type"`
		Body map[string]any `json:"body"`
	}
	if json.Unmarshal(body, &batch) != nil {
		return nil
	}
	var out []cspViolation
	for _, e := range batch {
		if e.Type != "csp-violation" || e.Body == nil {
			continue
		}
		if len(out) >= cspReportMaxPerRequest {
			break
		}
		out = append(out, cspViolationFrom(e.Body, host, "effectiveDirective", "blockedURL", "documentURL", "sourceFile", "lineNumber", ""))
	}
	return out
}

func cspViolationFrom(m map[string]any, host, directive, blocked, page, source, line, directiveAlt string) cspViolation {
	str := func(k string) string {
		if k == "" {
			return ""
		}
		switch v := m[k].(type) {
		case string:
			return v
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
		return ""
	}
	d := str(directive)
	if alt := str(directiveAlt); alt != "" {
		d = alt
	}
	return cspViolation{
		directive: cspLogField(d),
		blocked:   cspLogField(cspLogURL(str(blocked), host)),
		page:      cspLogField(cspLogURL(str(page), host)),
		source:    cspLogField(cspLogURL(str(source), host)),
		line:      cspLogField(str(line)),
	}
}

// cspLogURL is the ONE reduction every reported URL (the page, the source file,
// the blocked resource), with or without a scheme, goes through before it is
// logged:
//
//   - the query and fragment are dropped (they can carry tokens);
//   - a keyword with no path (inline, eval, self, data) is kept as it is;
//   - a URL with a scheme but no host (data:, blob:, about:) is reduced to its
//     scheme, so no payload is logged;
//   - the path goes through servermiddleware.RedactLogPath (invite tokens live in
//     the path: /invite/<token>);
//   - a SAME-ORIGIN path (host equal to the request's, or no host at all, as in a
//     relative or schemeless URL) is then cut to its first segment, because the
//     redaction knows only the routes it was taught and the app's own pages put
//     secrets in the path. A cross-origin URL keeps its redacted path, which is
//     what makes a blocked third-party script identifiable.
func cspLogURL(raw, requestHost string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" || !strings.Contains(raw, "/") && !strings.Contains(raw, ":") {
		return raw // empty, or a keyword
	}
	u, err := url.Parse(raw)
	if err != nil {
		return firstSegment(raw)
	}
	if u.Host == "" && u.Scheme != "" { // data:, blob:, about: ...
		return u.Scheme + ":"
	}
	redacted := servermiddleware.RedactLogPath(u.Path)
	if u.Host == "" {
		return firstSegment(redacted)
	}
	if strings.EqualFold(u.Host, requestHost) {
		scheme := u.Scheme
		if scheme == "" {
			scheme = "https"
		}
		return scheme + "://" + u.Host + firstSegment(redacted)
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + u.Host + redacted
}

// firstSegment cuts a path to its first segment followed by /... when more
// follows: /invite/abc/def becomes /invite/..., a schemeless relative path
// example.test/invite/abc becomes example.test/...
func firstSegment(p string) string {
	lead := ""
	rest := p
	if strings.HasPrefix(rest, "/") {
		lead, rest = "/", rest[1:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		return lead + rest[:i] + "/..."
	}
	return lead + rest
}

// cspLogField strips anything that could forge or break a log line and bounds
// the length, cutting on a rune boundary.
func cspLogField(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) > cspReportMaxField {
		n := cspReportMaxField
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n] + "..."
	}
	if s == "" {
		return "-"
	}
	return s
}
