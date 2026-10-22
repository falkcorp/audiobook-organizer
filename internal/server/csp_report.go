// file: internal/server/csp_report.go
// version: 1.0.0
// guid: d896d6d8-b883-482d-8e34-d41a4abd52df
// last-edited: 2026-10-21

package server

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
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

// cspReporter logs violation reports behind a rate limit, so an anonymous caller
// cannot fill the log or burn CPU with them. Reports over the limit are dropped
// and counted; the count is added to the next line that is let through.
type cspReporter struct {
	limiter *rate.Limiter
	dropped atomic.Int64
}

// newCSPReporter allows a burst of 10 reports and one more every two seconds
// (30 a minute sustained): enough to see every distinct violation of a page
// load, far less than a flood.
func newCSPReporter(limiter *rate.Limiter) *cspReporter { return &cspReporter{limiter: limiter} }

var defaultCSPReporter = newCSPReporter(rate.NewLimiter(rate.Every(2*time.Second), 10))

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
	// Decide before parsing, so a rejected report costs one limiter check.
	if !r.limiter.Allow() {
		r.dropped.Add(1)
		c.Status(http.StatusNoContent)
		return
	}
	reports := parseCSPReports(body)
	if len(reports) == 0 {
		c.Status(http.StatusNoContent)
		return
	}
	if n := r.dropped.Swap(0); n > 0 {
		cspReportLog.Warn("CSP: %d violation report(s) were dropped while rate limited", n)
	}
	for _, v := range reports {
		cspReportLog.Warn("CSP violation: directive=%s blocked=%s page=%s source=%s line=%s",
			v.directive, v.blocked, v.page, v.source, v.line)
	}
	c.Status(http.StatusNoContent)
}

// cspViolation is one report reduced to the fields worth logging, each already
// sanitised by cspLogField.
type cspViolation struct{ directive, blocked, page, source, line string }

// parseCSPReports reads both wire formats: report-uri's
// {"csp-report": {...}} and report-to's [{"type": "csp-violation", "body": {...}}].
// Anything else yields nothing.
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
	for i, e := range batch {
		if e.Type != "csp-violation" || e.Body == nil || i >= 10 {
			continue
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
	return cspViolation{
		directive: cspLogField(d),
		blocked:   cspLogField(cspLogURL(str(blocked), false)),
		page:      cspLogField(cspLogURL(str(page), true)),
		source:    cspLogField(cspLogURL(str(source), false)),
		line:      cspLogField(str(line)),
	}
}

// cspLogURL reduces a reported URL to something safe to keep in a log. The
// query and fragment are always dropped (they can carry tokens). For the page
// (firstSegmentOnly) the path is cut to its first segment as well, because
// pages such as /invite/<token> carry a secret in the path. A keyword such as
// inline, eval, data or self is returned unchanged.
func cspLogURL(raw string, firstSegmentOnly bool) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme == "" && u.Host == "") {
		if i := strings.IndexAny(raw, "?#"); i >= 0 {
			raw = raw[:i]
		}
		return raw
	}
	if u.Host == "" { // data:, blob:, about: ... keep only the scheme
		return u.Scheme + ":"
	}
	out := u.Scheme + "://" + u.Host
	p := u.Path
	if firstSegmentOnly {
		trimmed := strings.TrimPrefix(p, "/")
		if i := strings.Index(trimmed, "/"); i >= 0 {
			p = "/" + trimmed[:i] + "/..."
		}
	}
	return out + p
}

// cspLogField strips anything that could forge or break a log line and bounds
// the length.
func cspLogField(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) > cspReportMaxField {
		s = s[:cspReportMaxField] + "..."
	}
	if s == "" {
		return "-"
	}
	return s
}
