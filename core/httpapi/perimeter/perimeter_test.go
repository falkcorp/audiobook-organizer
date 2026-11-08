// file: core/httpapi/perimeter/perimeter_test.go
// version: 1.0.0
// guid: 9328a91c-0e8b-4b68-b822-f6d8af5531f2
// last-edited: 2026-11-08

package perimeter_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/httpmw"
	"github.com/falkcorp/mediacore/rbac"
)

// gotham is the legacy mount's stand-in for a gin engine: it serves a few
// paths the way the legacy handler would.
type gotham struct{}

func (gotham) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/v1/cases/gone":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"gone"}`))
	case r.URL.Path == "/api/v1/cases/own-csp":
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		_, _ = w.Write([]byte("Oracle"))
	case strings.HasPrefix(r.URL.Path, "/api/v1/logs/"):
		// A body the handler gzipped itself (HEAD's log download).
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gzipped(strings.Repeat("Two-Face flipped the coin. ", 40)))
	case r.URL.Path == "/api/v1/events":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: Bat-Signal\n\n"))
	case strings.HasPrefix(r.URL.Path, "/api/v1/"):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"case":"` + strings.Repeat("The Long Halloween ", 20) + `"}`))
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Gotham</title>"))
	}
}

func gzipped(s string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	return b.Bytes()
}

const (
	legacyCSP = "script-src 'self'; connect-src 'self' https://wayne.example.test; default-src 'self'"
	buildID   = "batcomputer-0042"
)

type gadget struct {
	Name string `json:"name"`
}

type gadgetReq struct {
	ID string `path:"id"`
}

// fixture is a router with a native route, an infra probe and a legacy
// mount that owns "/", built behind the whole chain.
type fixture struct {
	router *httpapi.Router
	h      http.Handler
	reader *sdkmetric.ManualReader
	logs   *capture
}

type fixtureOpt func(*httpapi.Options, *httpapi.Deps)

func newFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	f := &fixture{reader: sdkmetric.NewManualReader(), logs: &capture{}}
	o := httpapi.Options{ServerName: "gotham-server"}
	d := httpapi.Deps{
		Log:           slog.New(f.logs),
		MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader)),
	}
	for _, opt := range opts {
		opt(&o, &d)
	}
	r := httpapi.NewRouter(o, d)
	r.AllowPublic("batcave", "getGadget")
	httpapi.Handle(r.ModuleGroup("batcave"), httpapi.Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public},
		func(_ context.Context, in *httpapi.In[gadgetReq]) (gadget, error) {
			if in.Req.ID != "batarang" {
				return gadget{}, httpapi.NotFound("gadget", in.Req.ID)
			}
			return gadget{Name: "batarang"}, nil
		})
	r.Infra("GET /livez", rbac.Public, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	if err := r.Claim("gcpd", "/api/v1/"); err != nil {
		t.Fatal(err)
	}
	routes := []httpapi.LegacyRoute{
		{Method: http.MethodGet, GinPath: "/api/v1/cases/:id", Perm: rbac.AnyAuthenticated, RateClass: httpapi.RateDefault},
		{Method: http.MethodPost, GinPath: "/api/v1/cases", Perm: rbac.AnyAuthenticated, RateClass: httpapi.RateDefault},
		{Method: http.MethodGet, GinPath: "/api/v1/health", Perm: rbac.Public, Public: true, RateClass: httpapi.RateExempt},
		{Method: http.MethodGet, GinPath: "/api/v1/logs/:id", Perm: rbac.AnyAuthenticated, RateClass: httpapi.RateDefault, NoCompress: true},
		{Method: http.MethodGet, GinPath: "/api/v1/events", Perm: rbac.AnyAuthenticated, RateClass: httpapi.RateExempt},
	}
	if err := r.MountLegacy(httpapi.LegacyMount{
		Owner: "gcpd", Handler: gotham{}, Auth: httpapi.LegacyAuthGin, ServesSPA: true,
		DefaultCSP: func(*http.Request) string { return legacyCSP }, CSPReport: "/api/v1/csp-report",
		BuildID: buildID,
		Routes: func(yield func(httpapi.LegacyRoute) bool) {
			for _, lr := range routes {
				if !yield(lr) {
					return
				}
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	f.router, f.h = r, h
	return f
}

// do serves one request; mutate adjusts it first.
func (f *fixture) do(method, target string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// capture is an slog handler that keeps every record.
type capture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }
func (c *capture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, r.Clone())
	return nil
}
func (c *capture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capture) WithGroup(string) slog.Handler      { return c }

// access returns the attributes of the access-log records.
func (c *capture) access() []map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]string
	for _, r := range c.recs {
		if r.Message != httpmw.AccessMessage {
			continue
		}
		m := map[string]string{"level": r.Level.String()}
		r.Attrs(func(a slog.Attr) bool {
			m[a.Key] = a.Value.String()
			return true
		})
		out = append(out, m)
	}
	return out
}

// TestChain_OrderGolden: the recorded stage order of a routed request equals
// 05-http-api §4.2's table, outermost first, with stage 8 (the Basic gate)
// absent because it stays in gin until F3.
func TestChain_OrderGolden(t *testing.T) {
	var got []int
	c, err := httpmw.New(httpmw.Deps{Trace: func(s int) { got = append(got, s) }}, httpmw.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	route := c.Route(httpmw.RouteSpec{Pattern: "/api/v1/cases/{id}", Class: "default", MaxBody: -1},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { got = append(got, 14) }))
	h := c.Outer(route)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/cases/7", nil))
	// §4.2: 0 peer allowlist, 1 request ID, 2 tracing and HTTP metrics, 3
	// recovery, 4 access log, 5 security headers, 6 CORS, 7 cross-origin
	// protection, [8 Basic gate: gin in R1], 9 compression, 10 rate limit,
	// 11 body limit, [12-13: off for the legacy mount in R1], 14 handler.
	want := []int{0, 1, 2, 3, 4, 5, 6, 7, 9, 10, 11, 14}
	if !slices.Equal(got, want) {
		t.Fatalf("stage order = %v, want %v", got, want)
	}
	if order := c.Order(); !slices.Equal(order, want[:len(want)-1]) {
		t.Fatalf("Order() = %v, want %v", order, want[:len(want)-1])
	}
}

// TestRequestID_GeneratedAndPropagated: the response header, the problem
// body and the access-log record carry the same ULID; a well-formed inbound
// X-Request-ID is kept as client_request_id, never as the ID. (The audit
// record joins at F4, when audit exists.)
func TestRequestID_GeneratedAndPropagated(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, "/api/batcave/v1/gadgets/joker-gas", func(r *http.Request) {
		r.Header.Set("X-Request-ID", "alfred-7")
	})
	id := rec.Header().Get("X-Request-ID")
	if len(id) != 26 || strings.Trim(id, "0123456789ABCDEFGHJKMNPQRSTVWXYZ") != "" {
		t.Fatalf("X-Request-ID = %q, want a 26-character ULID", id)
	}
	var p httpapi.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.RequestID != id {
		t.Fatalf("problem request_id = %q (%v), want %q", p.RequestID, err, id)
	}
	logs := f.logs.access()
	if len(logs) != 1 || logs[0]["request_id"] != id || logs[0]["client_request_id"] != "alfred-7" {
		t.Fatalf("access log = %v, want request_id %s and client_request_id alfred-7", logs, id)
	}
	// A malformed inbound ID is not kept.
	f.do(http.MethodGet, "/livez", func(r *http.Request) { r.Header.Set("X-Request-ID", "joker gas;rm") })
	if logs := f.logs.access(); logs[1]["client_request_id"] != "" {
		t.Fatalf("malformed inbound id kept: %v", logs[1])
	}
	if other := f.do(http.MethodGet, "/livez").Header().Get("X-Request-ID"); other == id || len(other) != 26 {
		t.Fatalf("second request ID = %q, want a fresh ULID", other)
	}
}

// TestCSP_DefaultPerOwner: the legacy mount's responses (its SPA page
// included) carry DefaultCSP and Reporting-Endpoints; a native route gets
// core's default policy; a handler-set CSP is kept (FE-4, FE2-1).
func TestCSP_DefaultPerOwner(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		target, csp, report string
	}{
		{"/library", legacyCSP, `csp="/api/v1/csp-report"`},
		{"/api/v1/cases/7", legacyCSP, `csp="/api/v1/csp-report"`},
		{"/api/v1/nope/at/all", legacyCSP, `csp="/api/v1/csp-report"`},
		{"/api/v1/cases/own-csp", "default-src 'none'; frame-ancestors 'none'", `csp="/api/v1/csp-report"`},
		{"/api/batcave/v1/gadgets/batarang", "default-src 'none'; frame-ancestors 'self'", ""},
		{"/livez", "default-src 'none'; frame-ancestors 'self'", ""},
	} {
		rec := f.do(http.MethodGet, c.target)
		if got := rec.Header().Get("Content-Security-Policy"); got != c.csp {
			t.Errorf("%s: CSP = %q, want %q", c.target, got, c.csp)
		}
		if got := rec.Header().Get("Reporting-Endpoints"); got != c.report {
			t.Errorf("%s: Reporting-Endpoints = %q, want %q", c.target, got, c.report)
		}
		for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "SAMEORIGIN",
			"Referrer-Policy": "strict-origin-when-cross-origin", "X-Xss-Protection": "0",
			"Permissions-Policy": "camera=(), microphone=(), geolocation=(), payment=(), usb=()"} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", c.target, k, got, v)
			}
		}
		if rec.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS over plain HTTP from an untrusted peer", c.target)
		}
	}
}

// TestPerimeter_XAppBuildOnEveryResponse: legacy, native, infra, 404, 405
// and a legacy 410 all carry X-App-Build, the "/" owner's build ID
// (FE2-5, OPS2-13).
func TestPerimeter_XAppBuildOnEveryResponse(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, "/api/v1/cases/7", 200},
		{http.MethodGet, "/library", 200},
		{http.MethodGet, "/api/batcave/v1/gadgets/batarang", 200},
		{http.MethodGet, "/livez", 200},
		{http.MethodGet, "/api/batcave/v1/nope", 404},
		{http.MethodGet, "/api/v1/nope/at/all", 404},
		{http.MethodPost, "/api/batcave/v1/gadgets/batarang", 405},
		{http.MethodGet, "/api/v1/cases/gone", 410},
		{http.MethodOptions, "/api/v1/cases/7", 204},
	} {
		rec := f.do(c.method, c.target)
		if rec.Code != c.status || rec.Header().Get("X-App-Build") != buildID {
			t.Errorf("%s %s = %d, X-App-Build %q; want %d, %q", c.method, c.target, rec.Code,
				rec.Header().Get("X-App-Build"), c.status, buildID)
		}
	}
	// No "/" owner bundle: "dev".
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	h, err := r.Build()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nowhere", nil))
	if got := rec.Header().Get("X-App-Build"); got != "dev" {
		t.Fatalf("X-App-Build without a bundle = %q, want dev", got)
	}
}

// TestRateLimit_ClientBuckets: a token bucket per client address and class
// (synctest time): the burst passes, the next is 429 rate_limited with
// Retry-After, RateLimit-Policy and RateLimit; another client and a
// RateExempt route are unaffected; a token comes back with time; disabled
// is off from the start.
func TestRateLimit_ClientBuckets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		if err := f.router.SetPerimeter(httpapi.Options{Limits: httpapi.LimitOptions{RateLimitEnabled: true, RateLimitPerMinute: 60}}); err != nil {
			t.Fatal(err)
		}
		from := func(peer string) func(*http.Request) { return func(r *http.Request) { r.RemoteAddr = peer } }
		robin, nightwing := from("192.0.2.10:5000"), from("192.0.2.11:5000")
		for i := range 12 { // burst max(60/5, 10)
			if rec := f.do(http.MethodGet, "/api/v1/cases/7", robin); rec.Code != 200 {
				t.Fatalf("request %d = %d, want 200 within the burst", i, rec.Code)
			}
		}
		rec := f.do(http.MethodGet, "/api/v1/cases/7", robin)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("over the burst = %d, want 429", rec.Code)
		}
		var p httpapi.Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		h := rec.Header()
		if p.Code != "rate_limited" || h.Get("Retry-After") != "1" || h.Get("RateLimit-Policy") != `"default";q=60;w=60` ||
			h.Get("RateLimit") != `"default";r=0;t=1` || h.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("429 = %q %v", p.Code, h)
		}
		if rec := f.do(http.MethodGet, "/api/v1/cases/7", nightwing); rec.Code != 200 {
			t.Fatalf("another client = %d, want its own bucket", rec.Code)
		}
		if rec := f.do(http.MethodGet, "/api/v1/health", robin); rec.Code != 200 {
			t.Fatalf("RateExempt route = %d, want never limited", rec.Code)
		}
		if rec := f.do(http.MethodGet, "/library", robin); rec.Code != 200 {
			t.Fatalf("the / owner = %d, want never limited", rec.Code)
		}
		time.Sleep(time.Second)
		if rec := f.do(http.MethodGet, "/api/v1/cases/7", robin); rec.Code != 200 {
			t.Fatalf("after a second = %d, want one token back", rec.Code)
		}
		if err := f.router.SetPerimeter(httpapi.Options{Limits: httpapi.LimitOptions{RateLimitEnabled: false, RateLimitPerMinute: 60}}); err != nil {
			t.Fatal(err)
		}
		for range 20 {
			if rec := f.do(http.MethodGet, "/api/v1/cases/7", robin); rec.Code != 200 {
				t.Fatalf("disabled limit = %d", rec.Code)
			}
		}
	})
}

// TestCSRF_CookieBodilessPOSTFromSiblingOriginRefused: a cookie-carrying
// POST with Sec-Fetch-Site: same-site is refused 403 cross_origin_rejected
// and logged as a security_event; same-origin and header-less clients pass;
// cached Basic credentials are no bypass (FE2-23).
func TestCSRF_CookieBodilessPOSTFromSiblingOriginRefused(t *testing.T) {
	f := newFixture(t)
	sibling := func(r *http.Request) {
		r.Header.Set("Cookie", "abo_session=penguin")
		r.Header.Set("Sec-Fetch-Site", "same-site")
		r.Header.Set("Origin", "https://iceberg.gotham.example.test")
	}
	rec := f.do(http.MethodPost, "/api/v1/cases", sibling)
	var p httpapi.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusForbidden || p.Code != "cross_origin_rejected" {
		t.Fatalf("sibling-origin POST = %d %q, want 403 cross_origin_rejected", rec.Code, p.Code)
	}
	if rec := f.do(http.MethodPost, "/api/v1/cases", sibling, func(r *http.Request) {
		r.SetBasicAuth("bruce", "alfred")
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST with Basic credentials = %d, want 403", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/api/v1/cases", func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}); rec.Code != 200 {
		t.Fatalf("same-origin POST = %d, want 200", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/api/v1/cases"); rec.Code != 200 {
		t.Fatalf("header-less client POST = %d, want 200", rec.Code)
	}
	var events int
	for _, l := range f.logs.access() {
		if l["security_event"] == "cross_origin_rejected" {
			events++
		}
	}
	if events != 2 {
		t.Fatalf("security_event records = %d, want 2", events)
	}
}

// TestCrossOrigin_TrustedProxyForwardedHost: over plain HTTP the Origin
// comparison decides; a trusted proxy's X-Forwarded-Host stands in for a
// rewritten Host, the same header from any other peer is ignored.
func TestCrossOrigin_TrustedProxyForwardedHost(t *testing.T) {
	f := newFixture(t)
	if err := f.router.SetPerimeter(httpapi.Options{TrustedProxies: []httpapi.TrustedPeer{{Addr: netip.MustParseAddr("192.0.2.53")}}}); err != nil {
		t.Fatal(err)
	}
	post := func(peer string) *httptest.ResponseRecorder {
		return f.do(http.MethodPost, "http://127.0.0.1:8484/api/v1/cases", func(r *http.Request) {
			r.RemoteAddr = peer
			r.Header.Set("Origin", "http://wayne.example.test")
			r.Header.Set("X-Forwarded-Host", "wayne.example.test")
		})
	}
	if rec := post("192.0.2.53:41000"); rec.Code != 200 {
		t.Fatalf("via the trusted proxy = %d, want 200", rec.Code)
	}
	if rec := post("192.0.2.99:41000"); rec.Code != http.StatusForbidden {
		t.Fatalf("X-Forwarded-Host from an untrusted peer = %d, want 403", rec.Code)
	}
}

// TestHandler_NoDoubleGzip: a NoCompress route's own gzip body passes once,
// a plain JSON answer is compressed once, and text/event-stream never is.
func TestHandler_NoDoubleGzip(t *testing.T) {
	f := newFixture(t)
	accept := func(r *http.Request) { r.Header.Set("Accept-Encoding", "gzip, deflate") }
	rec := f.do(http.MethodGet, "/api/v1/logs/9", accept)
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Vary") != "" {
		t.Fatalf("log download headers = %v", rec.Header())
	}
	if got := gunzip(t, rec.Body.Bytes()); !strings.HasPrefix(got, "Two-Face") {
		t.Fatalf("log download decompressed once = %q, want the text", got[:min(len(got), 40)])
	}
	rec = f.do(http.MethodGet, "/api/v1/cases/7", accept)
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Values("Vary")[0] != "Accept-Encoding" {
		t.Fatalf("JSON answer headers = %v", rec.Header())
	}
	if got := gunzip(t, rec.Body.Bytes()); !strings.HasPrefix(got, `{"case":"The Long Halloween`) {
		t.Fatalf("JSON decompressed = %q", got)
	}
	rec = f.do(http.MethodGet, "/api/v1/events", accept)
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "data: Bat-Signal\n\n" {
		t.Fatalf("event stream = %v %q, want it uncompressed", rec.Header(), rec.Body.String())
	}
	// An error answer is sent plain with Vary and ETag deleted (HEAD's
	// gin-contrib/gzip), CORS's Vary: Origin included.
	rec = f.do(http.MethodGet, "/api/v1/cases/gone", accept, func(r *http.Request) {
		r.Host = "gotham.example.test"
		r.Header.Set("Origin", "http://gotham.example.test")
	})
	if rec.Code != http.StatusGone || rec.Header().Get("Content-Encoding") != "" || len(rec.Header().Values("Vary")) != 0 ||
		rec.Body.String() != `{"error":"gone"}` {
		t.Fatalf("410 = %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// series is one recorded data point of http.server.request.duration.
type series map[string]string

// durations collects the request-duration points with their counts.
func (f *fixture) durations(t *testing.T) (map[string]uint64, []series) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := f.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	counts := map[string]uint64{}
	var all []series
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http.server.request.duration" {
				continue
			}
			if sm.Scope.Name != "httpapi" {
				t.Errorf("duration on meter %q, want httpapi", sm.Scope.Name)
			}
			h := m.Data.(metricdata.Histogram[float64])
			for _, dp := range h.DataPoints {
				s := series{}
				for _, kv := range dp.Attributes.ToSlice() {
					s[string(kv.Key)] = kv.Value.String()
					if (kv.Key == "server.port" || kv.Key == "http.response.status_code") && kv.Value.Type() != attribute.INT64 {
						t.Errorf("%s is %v, want an integer", kv.Key, kv.Value.Type())
					}
				}
				counts[s["http.request.method"]+" "+s["http.route"]+" "+s["http.response.status_code"]] += dp.Count
				all = append(all, s)
				if len(dp.Bounds) != 14 || dp.Bounds[0] != 0.005 || dp.Bounds[13] != 10 {
					t.Errorf("duration bounds = %v, want the semconv advice", dp.Bounds)
				}
			}
		}
	}
	return counts, all
}

// TestStage2_RecordsEveryRequest (core half; the real-engine half is ABO's,
// internal/server): a native request, a legacy {id} route, a 429, a stage 7
// refusal, a router 404, a legacy 404 and an SPA load are each recorded
// exactly once, with the route's mux pattern or, unrouted, none.
func TestStage2_RecordsEveryRequest(t *testing.T) {
	f := newFixture(t)
	if err := f.router.SetPerimeter(httpapi.Options{Limits: httpapi.LimitOptions{RateLimitEnabled: true, RateLimitPerMinute: 1, RateLimitBurst: 1}}); err != nil {
		t.Fatal(err)
	}
	// The native route shares the "default" class bucket: another client.
	f.do(http.MethodGet, "/api/batcave/v1/gadgets/batarang", func(r *http.Request) { r.RemoteAddr = "192.0.2.77:5000" })
	f.do(http.MethodGet, "/api/v1/cases/7")
	f.do(http.MethodGet, "/api/v1/cases/8") // 429: its route is recorded
	f.do(http.MethodPost, "/api/v1/cases", func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	f.do(http.MethodGet, "/api/batcave/v1/nope")
	f.do(http.MethodGet, "/api/v1/nope/at/all")
	f.do(http.MethodGet, "/library")
	f.do(http.MethodOptions, "/api/v1/cases/7")
	counts, _ := f.durations(t)
	want := map[string]uint64{
		"GET /api/batcave/v1/gadgets/{id} 200": 1,
		"GET /api/v1/cases/{id} 200":           1,
		"GET /api/v1/cases/{id} 429":           1,
		"POST  403":                            1,
		"GET  404":                             2,
		"GET  200":                             1,
		"OPTIONS  204":                         1,
	}
	if len(counts) != len(want) {
		t.Errorf("recorded %v, want %v", counts, want)
	}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("%q recorded %d times, want %d (all: %v)", k, counts[k], n, counts)
		}
	}
}

// TestStage2_ServerAddressIgnoresHost: two requests to one route with
// different Host headers produce one server.address, Options.ServerName,
// and server.port is the listener's port, never the Host's.
func TestStage2_ServerAddressIgnoresHost(t *testing.T) {
	f := newFixture(t)
	for _, host := range []string{"gotham.example.test:9999", "arkham.example.test:31337"} {
		f.do(http.MethodGet, "/livez", func(r *http.Request) {
			r.Host = host
			*r = *r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8484}))
		})
	}
	_, all := f.durations(t)
	if len(all) != 1 {
		t.Fatalf("series = %v, want one", all)
	}
	if all[0]["server.address"] != "gotham-server" || all[0]["server.port"] != "8484" {
		t.Fatalf("series = %v, want server.address gotham-server and server.port 8484", all[0])
	}
}
