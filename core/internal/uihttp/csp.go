// file: core/internal/uihttp/csp.go
// version: 1.0.0
// guid: c3e8a1d5-7f20-4b69-9d34-0a5b6e2f8c71
// last-edited: 2026-11-10

package uihttp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
)

// maxReportsPerRequest bounds the reports read from one request: a Reporting
// API batch is a handful, and HEAD's handler (internal/server/csp_report.go)
// caps it at the same ten.
const maxReportsPerRequest = 10

// reportTypeCSP is the type of a Reporting API entry that is a CSP violation.
const reportTypeCSP = "csp-violation"

// reportCSP receives a browser's violation report. It reads both wire formats
// HEAD does, answers 415 for a content type a browser never sends, 413 for a
// body over the cap and 204 for every other request, whether or not it held a
// report, so a caller learns nothing about what was counted or logged. The
// route is Public (07 §7.6.2): a browser sends a report with no credential, and
// a page that was blocked may be one the visitor is not signed in on. What it
// reads is one directive name per report, matched against the closed list.
func (a *api) reportCSP(w http.ResponseWriter, r *http.Request) {
	if !cspContentType(r.Header.Get("Content-Type")) {
		httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type",
			Detail: "send application/csp-report or application/reports+json"}, a.log)
		return
	}
	// Stage 11 already limits the body to the route's MaxBody; this keeps the
	// handler safe on its own (the dark tests call it without the chain).
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cspMaxBody))
	if err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large",
				Detail: "a CSP report is at most 8 KiB"}, a.log)
			return
		}
		httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusBadRequest, Code: "bad_request", Detail: "the body could not be read"}, a.log)
		return
	}
	directives := reportedDirectives(body)
	for _, d := range directives {
		a.metrics.recordCSP(r.Context(), d)
	}
	a.logReports(directives)
	w.WriteHeader(http.StatusNoContent)
}

// cspContentType reports whether ct is one a browser sends a report with:
// report-uri's application/csp-report, the Reporting API's
// application/reports+json, and application/json as HEAD also accepts.
func cspContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch mt {
	case "application/csp-report", "application/reports+json", "application/json":
		return true
	}
	return false
}

// reportedDirectives reads body as either wire format and returns one
// directive label per violation report, at most maxReportsPerRequest. Anything
// that is neither shape yields nothing. Only the directive is taken from the
// document: no URL, source file or sample is read.
//
//	report-uri:     {"csp-report": {"effective-directive": "...", "violated-directive": "...", ...}}
//	Reporting API:  [{"type": "csp-violation", "body": {"effectiveDirective": "...", ...}}, ...]
func reportedDirectives(body []byte) []string {
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	switch v := doc.(type) {
	case map[string]any:
		rep, ok := v["csp-report"].(map[string]any)
		if !ok {
			return nil
		}
		name, _ := rep["effective-directive"].(string)
		if name == "" {
			name, _ = rep["violated-directive"].(string)
		}
		return []string{directiveLabel(name)}
	case []any:
		var out []string
		for _, e := range v {
			entry, ok := e.(map[string]any)
			if !ok || entry["type"] != reportTypeCSP {
				continue
			}
			rep, ok := entry["body"].(map[string]any)
			if !ok {
				continue
			}
			if len(out) == maxReportsPerRequest {
				break
			}
			name, _ := rep["effectiveDirective"].(string)
			out = append(out, directiveLabel(name))
		}
		return out
	}
	return nil
}

// logReports writes one line for a request's reports, naming only the distinct
// directive labels and the count, behind the budget below. Reports over the
// budget are dropped from the log (never from the metric) and counted, and the
// count is added to the next line that gets through.
func (a *api) logReports(directives []string) {
	if len(directives) == 0 {
		return
	}
	ok, dropped := a.budget.take()
	if !ok {
		return
	}
	seen := slices.Clone(directives)
	slices.Sort(seen)
	seen = slices.Compact(seen)
	a.log.Warn("csp violation reports", "count", len(directives), "directives", strings.Join(seen, ","), "dropped_since_last", dropped)
}

// logBudget is the service-wide limit on CSP log lines, HEAD's: a burst of 30
// and one more every two seconds, so an anonymous caller (stage 10 already
// limits each client address, at the RateAuth class) cannot fill the log from
// many addresses. It limits log lines only; the metric counts every report.
type logBudget struct {
	mu      sync.Mutex
	now     func() time.Time
	tokens  float64
	last    time.Time
	dropped int64
}

const (
	logBurst    = 30
	logInterval = 2 * time.Second
)

func newLogBudget() *logBudget {
	return &logBudget{now: time.Now, tokens: logBurst}
}

// take spends one line of budget. When it is spent, it counts a drop and
// reports false; when it is not, it reports true and the number of lines
// dropped since the last one that got through.
func (b *logBudget) take() (ok bool, dropped int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if !b.last.IsZero() {
		b.tokens = min(logBurst, b.tokens+float64(now.Sub(b.last))/float64(logInterval))
	}
	b.last = now
	if b.tokens < 1 {
		b.dropped++
		return false, 0
	}
	b.tokens--
	dropped, b.dropped = b.dropped, 0
	return true, dropped
}
