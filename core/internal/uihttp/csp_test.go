// file: core/internal/uihttp/csp_test.go
// version: 1.0.0
// guid: 1f6b8d03-9a24-4c57-8e1b-3d7a0c5e9f46
// last-edited: 2026-11-10

package uihttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

const cspFamily = "mediacore_ui_csp_reports_total"

// cspServer builds a router holding only the Public CSP route, which builds
// without an auth service, so a request runs the whole core chain.
func cspServer(t testing.TB, a *api) http.Handler {
	t.Helper()
	r := newRouter()
	httpapi.HandleRaw(r.CoreGroup(), cspRoute(), httpapi.RawDoc{Status: http.StatusNoContent}, http.HandlerFunc(a.reportCSP))
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h
}

const cspPath = "/api/core/v1/ui/csp-report"

// TestCSPReport_BothWireFormats: report-uri's single object and the Reporting
// API's array are read, each report counted under its directive label, a
// legacy report preferring effective-directive, and the answer is 204.
func TestCSPReport_BothWireFormats(t *testing.T) {
	p, sc := uiScope(t)
	a, err := newAPI(Deps{Scope: sc})
	if err != nil {
		t.Fatal(err)
	}
	h := cspServer(t, a)

	legacy := `{"csp-report":{"document-uri":"https://wayne.example.test/invite/SECRET","blocked-uri":"https://joker.example.test/x.js?token=SECRET","violated-directive":"script-src 'self'","effective-directive":"script-src-elem"}}`
	if rec := post(h, cspPath, "application/csp-report", legacy); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("legacy report: %d %q, want an empty 204", rec.Code, rec.Body)
	}
	// Without effective-directive the first word of violated-directive counts.
	if rec := post(h, cspPath, "application/csp-report", `{"csp-report":{"violated-directive":"IMG-SRC https: data:"}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("violated-directive only: %d", rec.Code)
	}
	batch := `[{"type":"csp-violation","body":{"effectiveDirective":"connect-src","blockedURL":"https://riddler.example.test/"}},
		{"type":"deprecation","body":{"effectiveDirective":"style-src"}},
		{"type":"csp-violation","body":{"effectiveDirective":"connect-src"}},
		{"type":"csp-violation","body":"not an object"},
		"not an entry",
		{"type":"csp-violation","body":{"effectiveDirective":"frame-ancestors"}}]`
	if rec := post(h, cspPath, "application/reports+json", batch); rec.Code != http.StatusNoContent {
		t.Fatalf("batch: %d", rec.Code)
	}

	g := p.Gatherer()
	for d, want := range map[string]string{"script-src-elem": "1", "img-src": "1", "connect-src": "2", "frame-ancestors": "1", "style-src": "0", "script-src": "0"} {
		if got, ok := sample(t, g, cspFamily, map[string]string{"directive": d}); !ok || got != want {
			t.Errorf("directive %s = %q, %v, want %s", d, got, ok, want)
		}
	}
}

// TestCSPReport_StatusCases: 415 for a content type a browser never sends (or
// none), 413 for a body over the 8 KiB cap, and 204, counting nothing, for a
// body that is not a report, malformed JSON or an empty object.
func TestCSPReport_StatusCases(t *testing.T) {
	p, sc := uiScope(t)
	a, err := newAPI(Deps{Scope: sc})
	if err != nil {
		t.Fatal(err)
	}
	h := cspServer(t, a)
	ok := `{"csp-report":{"effective-directive":"img-src"}}`
	for name, c := range map[string]struct {
		ct, body string
		status   int
		code     string
	}{
		"text/plain":     {"text/plain", ok, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		"form":           {"application/x-www-form-urlencoded", ok, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		"no type":        {"", ok, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		"bad type":       {"application/", ok, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		"too large":      {"application/csp-report", `{"csp-report":{"x":"` + strings.Repeat("a", cspMaxBody) + `"}}`, http.StatusRequestEntityTooLarge, "body_too_large"},
		"malformed":      {"application/csp-report", `{"csp-report":`, http.StatusNoContent, ""},
		"not a report":   {"application/json", `{"hello":"gotham"}`, http.StatusNoContent, ""},
		"scalar":         {"application/json", `42`, http.StatusNoContent, ""},
		"empty report":   {"application/csp-report", `{"csp-report":{}}`, http.StatusNoContent, ""},
		"charset suffix": {"application/csp-report; charset=utf-8", ok, http.StatusNoContent, ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := post(h, cspPath, c.ct, c.body)
			if rec.Code != c.status {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body, c.status)
			}
			if c.code != "" && !strings.Contains(rec.Body.String(), `"`+c.code+`"`) {
				t.Errorf("body %s lacks code %s", rec.Body, c.code)
			}
		})
	}
	// "empty report" counts one "other"; the three that are not reports count
	// none; "charset suffix" counts one img-src.
	g := p.Gatherer()
	for d, want := range map[string]string{"other": "1", "img-src": "1"} {
		if got, _ := sample(t, g, cspFamily, map[string]string{"directive": d}); got != want {
			t.Errorf("directive %s = %s, want %s", d, got, want)
		}
	}
}

// TestCSPReport_ReadsAtMostTenReports: a batch longer than the per-request cap
// counts the first ten.
func TestCSPReport_ReadsAtMostTenReports(t *testing.T) {
	var entries []map[string]any
	for range 25 {
		entries = append(entries, map[string]any{"type": "csp-violation", "body": map[string]any{"effectiveDirective": "img-src"}})
	}
	b, _ := json.Marshal(entries)
	if got := reportedDirectives(b); len(got) != maxReportsPerRequest {
		t.Errorf("read %d reports, want %d", len(got), maxReportsPerRequest)
	}
	if got := reportedDirectives([]byte(`[]`)); got != nil {
		t.Errorf("an empty batch read %v", got)
	}
}

// TestCSPReport_HandlerBoundsItsOwnBody: called without the chain (as the dark
// tests do) the handler still refuses an oversize body.
func TestCSPReport_HandlerBoundsItsOwnBody(t *testing.T) {
	a, _ := newAPI(Deps{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, cspPath, strings.NewReader(strings.Repeat("a", cspMaxBody+1)))
	req.Header.Set("Content-Type", "application/csp-report")
	a.reportCSP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversize body = %d, want 413", rec.Code)
	}
}

// failingBody is a request body whose read fails.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, fmt.Errorf("connection reset") }

func TestCSPReport_UnreadableBodyIs400(t *testing.T) {
	a, _ := newAPI(Deps{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, cspPath, failingBody{})
	req.Header.Set("Content-Type", "application/csp-report")
	a.reportCSP(rec, req)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "reset") {
		t.Errorf("an unreadable body = %d %s, want 400 without the cause", rec.Code, rec.Body)
	}
}

// TestCSPReport_AttackerInputNeverReachesALabelOrALog is the cardinality test
// of the unauthenticated route: thousands of reports whose directive is a
// distinct attacker-chosen string (and URLs naming secrets) leave the family at
// its seeded series count, no series carries a submitted string, the SDK's
// overflow series never appears, and the log carries no submitted text.
func TestCSPReport_AttackerInputNeverReachesALabelOrALog(t *testing.T) {
	p, sc := uiScope(t)
	log, lines := capture()
	a, err := newAPI(Deps{Scope: sc, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	h := cspServer(t, a)
	g := p.Gatherer()
	before := len(seriesOf(t, g, cspFamily))
	if want := len(cspDirectives()) + 1; before != want {
		t.Fatalf("seeded %d csp series, want %d (every directive plus other)", before, want)
	}
	for i := range 3000 {
		d := fmt.Sprintf("evil-%d-<script>alert(1)</script>", i)
		body := fmt.Sprintf(`{"csp-report":{"effective-directive":%q,"blocked-uri":"https://joker-%d.example.test/SECRET%d"}}`, d, i, i)
		if rec := post(h, cspPath, "application/csp-report", body); rec.Code != http.StatusNoContent && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("report %d = %d", i, rec.Code)
		}
		batch := fmt.Sprintf(`[{"type":"csp-violation","body":{"effectiveDirective":"%s-src %d"}}]`, strings.Repeat("Z", i%50), i)
		a.logReports(reportedDirectives([]byte(batch)))
		for _, d := range reportedDirectives([]byte(batch)) {
			a.metrics.recordCSP(t.Context(), d)
		}
	}
	series := seriesOf(t, g, cspFamily)
	if len(series) != before {
		t.Errorf("%d csp series after the attack, want %d", len(series), before)
	}
	allowed := map[string]bool{otherLabel: true}
	for _, d := range cspDirectives() {
		allowed[d] = true
	}
	for _, s := range series {
		if !allowed[s["directive"]] {
			t.Errorf("series with a submitted directive %q", s["directive"])
		}
	}
	metricstest.AssertNoOverflow(t, p)
	for _, leak := range []string{"evil", "script", "SECRET", "joker", "ZZZ"} {
		if strings.Contains(lines.String(), leak) {
			t.Errorf("the log carries submitted text %q", leak)
		}
	}
}

// TestCSPReport_LogLineCarriesOnlyLabels: one line per request with the count
// and the distinct directive labels.
func TestCSPReport_LogLineCarriesOnlyLabels(t *testing.T) {
	log, lines := capture()
	a, _ := newAPI(Deps{Log: log})
	a.logReports([]string{"img-src", "script-src", "img-src"})
	got := lines.String()
	for _, want := range []string{"csp violation reports", "count=3", "directives=img-src,script-src", "dropped_since_last=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("log line %q lacks %q", got, want)
		}
	}
	lines.buf.Reset()
	a.logReports(nil)
	if lines.String() != "" {
		t.Errorf("no reports logged %q", lines)
	}
}

// TestLogBudget: a burst of 30 lines, then drops that are counted and reported
// on the next line that gets through, and one more line per two seconds.
func TestLogBudget(t *testing.T) {
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	b := newLogBudget()
	b.now = func() time.Time { return now }
	for i := range logBurst {
		if ok, d := b.take(); !ok || d != 0 {
			t.Fatalf("line %d refused inside the burst (%v, %d)", i, ok, d)
		}
	}
	for range 5 {
		if ok, _ := b.take(); ok {
			t.Fatal("a line got through with the budget spent")
		}
	}
	now = now.Add(logInterval)
	ok, dropped := b.take()
	if !ok || dropped != 5 {
		t.Errorf("after one interval: %v, dropped %d, want true and 5", ok, dropped)
	}
	if ok, _ := b.take(); ok {
		t.Error("two lines in one interval")
	}
	now = now.Add(time.Hour)
	for range logBurst {
		if ok, _ := b.take(); !ok {
			t.Fatal("the budget did not refill to the burst")
		}
	}
	if ok, _ := b.take(); ok {
		t.Error("the refill exceeded the burst")
	}
}

func TestLabels(t *testing.T) {
	for in, want := range map[string]string{
		"script-src": "script-src", "SCRIPT-SRC 'self'": "script-src", "img-src\thttps:": "img-src",
		"": otherLabel, "   ": otherLabel, "javascript:alert(1)": otherLabel, "script-src-bogus": otherLabel,
	} {
		if got := directiveLabel(in); got != want {
			t.Errorf("directiveLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := kindLabel("ui-missing"); got != "ui-missing" {
		t.Errorf("kindLabel(ui-missing) = %q", got)
	}
	if got := kindLabel("UI-MISSING"); got != otherLabel {
		t.Errorf("kindLabel is case sensitive, got %q", got)
	}
	mods := []string{"batman"}
	if moduleLabel(mods, "batman") != "batman" || moduleLabel(mods, "robin") != otherLabel || moduleLabel(mods, "") != otherLabel {
		t.Error("moduleLabel does not map through the registered IDs")
	}
	if !reflect.DeepEqual(append(diagnosticKinds(), "x")[:7], diagnosticKinds()) {
		t.Error("diagnosticKinds is not a fresh slice")
	}
}
