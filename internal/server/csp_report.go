// file: internal/server/csp_report.go
// version: 1.3.0
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
// requests: a request carries up to cspReportMaxPerRequest of them, of which at
// most as many as both budgets hold are charged and logged. Reports over a limit are dropped
// and counted; the count is added to the next line that is let through.
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
// burst of cspReportMaxPerRequest reports, so one full report-to batch fits, and
// one more every 10 seconds.
func newCSPReporter(limiter *rate.Limiter) *cspReporter {
	return &cspReporter{
		limiter:    limiter,
		visitors:   absauth.NewLRU[*cspVisitor](maxCSPVisitors),
		newVisitor: func() *rate.Limiter { return rate.NewLimiter(rate.Every(10*time.Second), cspReportMaxPerRequest) },
		now:        time.Now,
	}
}

// The service-wide limit: a burst of 30 reports (three full batches, so one
// visitor's full batch can never use up the whole burst) and one more every two
// seconds (30 a minute sustained): enough to see every distinct violation of a
// page load, far less than a flood.
var defaultCSPReporter = newCSPReporter(rate.NewLimiter(rate.Every(2*time.Second), 30))

// admit decides how many of n reports from visitor may be logged: as many as
// BOTH budgets hold whole tokens for right now, up to n (a batch larger than the
// smaller burst is capped at it rather than refused, so a full report-to batch is
// not dropped forever), or none. The whole decision is made under r.mu, the only
// path to either limiter: the tokens are read and then charged with AllowN at the
// same instant, so concurrent requests cannot interleave a check and a charge, and
// a request that is refused charges nothing at all. (An earlier version reserved
// from the service limiter and handed the reservation back on refusal; the
// library's CancelAt does not restore tokens when another reservation was taken in
// between, so concurrent refusals left the service limiter in debt.)
func (r *cspReporter) admit(visitor string, n int) int {
	if n <= 0 {
		return 0
	}
	if visitor == "" {
		visitor = "?"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.visitors.Sweep(func(v *cspVisitor) bool { return now.Sub(v.lastSeen) > cspVisitorIdle })
	v, ok := r.visitors.Get(visitor)
	if !ok {
		v = &cspVisitor{limiter: r.newVisitor()}
	}
	v.lastSeen = now
	r.visitors.Put(visitor, v)
	k := min(n, tokensAvailable(r.limiter, now, n), tokensAvailable(v.limiter, now, n))
	if k <= 0 {
		return 0
	}
	r.limiter.AllowN(now, k)
	v.limiter.AllowN(now, k)
	return k
}

// tokensAvailable is how many whole tokens l holds at now (n when it has no limit),
// never more than its burst.
func tokensAvailable(l *rate.Limiter, now time.Time, n int) int {
	if l.Limit() == rate.Inf {
		return n
	}
	return max(0, int(l.TokensAt(now)))
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
	reports := parseCSPReports(body)
	if len(reports) == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	admitted := r.admit(loginThrottleKey(c), len(reports))
	if rest := len(reports) - admitted; rest > 0 {
		r.dropped.Add(int64(rest))
	}
	if admitted == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	reports = reports[:admitted]
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
// Anything else yields nothing. Which reported URLs are same-origin is decided
// against the REPORTED document's host (cspViolationFrom), never the request's
// Host, which a proxy may rewrite.
func parseCSPReports(body []byte) []cspViolation {
	var legacy struct {
		Report map[string]any `json:"csp-report"`
	}
	if json.Unmarshal(body, &legacy) == nil && legacy.Report != nil {
		return []cspViolation{cspViolationFrom(legacy.Report, "violated-directive", "blocked-uri", "document-uri", "source-file", "line-number", "effective-directive")}
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
		out = append(out, cspViolationFrom(e.Body, "effectiveDirective", "blockedURL", "documentURL", "sourceFile", "lineNumber", ""))
	}
	return out
}

func cspViolationFrom(m map[string]any, directive, blocked, page, source, line, directiveAlt string) cspViolation {
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
	docHost := cspURLHost(str(page))
	return cspViolation{
		directive: cspLogField(d),
		blocked:   cspLogField(cspLogURL(str(blocked), docHost)),
		page:      cspLogField(cspLogURL(str(page), docHost)),
		source:    cspLogField(cspLogURL(str(source), docHost)),
		line:      cspLogField(str(line)),
	}
}

// cspURLHost is the normalised host of a reported document URL ("" when it has
// none: a relative or schemeless URL, or one that does not parse).
func cspURLHost(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return cspHostName(u)
}

// cspHostName is the host of u reduced to what decides "the same site": lower
// case, no port (so :443 and :8443 on one name are not told apart, which can only
// cut more, never less) and no trailing dot (example.test. is example.test).
func cspHostName(u *url.URL) string {
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
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
//     the path: /invite/<token>), which matches without regard to case and skips
//     empty segments;
//   - the path is then cut to its first segment when the URL is SAME-SITE as the
//     reported document (same normalised host, see cspHostName; or no host at all,
//     as in a relative or schemeless URL), or when the document's host is not
//     known (nothing can be said to be third-party then), because the redaction
//     knows only the routes it was taught and the app's own pages put secrets in
//     the path. The document's own URL is always cut by this rule: its host is
//     docHost. docHost is the host of the reported document, not of the request: a
//     proxy may rewrite Host, and the browser's own report is the authority on
//     what origin the page was. A cross-origin URL keeps its redacted path, which
//     is what makes a blocked third-party script identifiable.
func cspLogURL(raw, docHost string) string {
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
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if docHost == "" || cspHostName(u) == docHost {
		return scheme + "://" + u.Host + firstSegment(redacted)
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
