// file: internal/telemetry/metric_otlp_test.go
// version: 1.0.0
// guid: 4c9e2a7d-1b63-4f08-a5d2-7e3b9c0f6a18
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
)

// metricSink is a plaintext OTLP/gRPC metrics collector that keeps what it gets.
type metricSink struct {
	colmetricpb.UnimplementedMetricsServiceServer
	mu   sync.Mutex
	reqs []*colmetricpb.ExportMetricsServiceRequest
}

func (s *metricSink) Export(_ context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

func (s *metricSink) snapshot() []*colmetricpb.ExportMetricsServiceRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*colmetricpb.ExportMetricsServiceRequest(nil), s.reqs...)
}

func privateProm() prometheus.Option {
	return prometheus.WithRegisterer(promclient.NewRegistry())
}

func TestNewMeterProvider_OTLPOffByDefault(t *testing.T) {
	mp, status, err := newMeterProvider(context.Background(), LoadConfig("test", "tempo:4317"), privateProm())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	if status.Enabled || status.Err != nil || status.Readers != 1 {
		t.Fatalf("status = %+v, want Enabled=false Err=nil Readers=1", status)
	}
}

func TestMetricsEndpointNeverFallsBackToTraceEndpoint(t *testing.T) {
	cfg := LoadConfig("test", "http://192.0.2.10:4317") // trace endpoint set, metrics empty
	mp, status, err := newMeterProvider(context.Background(), cfg, privateProm())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	if status.Enabled || status.Readers != 1 {
		t.Fatalf("status = %+v: the trace endpoint must never feed the metric reader", status)
	}
}

func TestNewMeterProvider_OTLPOnAddsPeriodicReader(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &metricSink{}
	srv := grpc.NewServer()
	colmetricpb.RegisterMetricsServiceServer(srv, sink)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	cfg := LoadConfig("test-svc", "",
		WithMetricsOTLP("http://"+lis.Addr().String(), 5*time.Second, false),
		WithEnvironment("staging"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mp, status, err := newMeterProvider(ctx, cfg, privateProm())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.Err != nil || status.Readers != 2 {
		t.Fatalf("status = %+v, want Enabled=true Err=nil Readers=2", status)
	}
	c, err := mp.Meter("otlp-test").Int64Counter("otlp_test_things")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(ctx, 3)
	// Shutdown flushes the periodic reader once, so the test need not wait a
	// full interval.
	if err := mp.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	var found bool
	for _, req := range sink.snapshot() {
		for _, rm := range req.GetResourceMetrics() {
			attrs := map[string]string{}
			for _, kv := range rm.GetResource().GetAttributes() {
				attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
			}
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					if m.GetName() != "otlp_test_things" {
						continue
					}
					found = true
					sum := m.GetSum()
					if sum.GetAggregationTemporality() != metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
						t.Errorf("temporality = %v, want cumulative", sum.GetAggregationTemporality())
					}
					if pts := sum.GetDataPoints(); len(pts) != 1 || pts[0].GetAsInt() != 3 {
						t.Errorf("data points = %v, want one point of 3", pts)
					}
					if attrs["service.name"] != "test-svc" || attrs["service.version"] == "" || attrs["deployment.environment"] != "staging" {
						t.Errorf("resource attrs = %v", attrs)
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("collector never received the counter; got %d requests", len(sink.snapshot()))
	}
}

// TestInitOTEL_BadMetricEndpointIsNotFatal: an unusable metrics endpoint costs
// the OTLP copy only; init succeeds and /metrics keeps serving.
func TestInitOTEL_BadMetricEndpointIsNotFatal(t *testing.T) {
	for _, ep := range []string{"://x", "ftp://h:1", "nohostport", "http://host"} {
		cfg := LoadConfig("test", "", WithMetricsOTLP(ep, time.Minute, false))
		shutdown, err := InitOTEL(context.Background(), cfg)
		if err != nil {
			t.Fatalf("InitOTEL(%q) = %v; a bad metrics endpoint must not fail init", ep, err)
		}
		if shutdown == nil {
			t.Fatalf("InitOTEL(%q) returned a nil shutdown", ep)
		}
		// The shutdown is deliberately not called: InitOTEL shares one
		// process-wide provider, and shutting it down here would starve the
		// later tests that scrape it (TestInitOTEL_NoEndpoint_StillInstallsMeterProvider).

		// Same endpoint through the pure builder: the failure is reported,
		// not returned, and the Prometheus reader still serves.
		reg := promclient.NewRegistry()
		mp, status, err := newMeterProvider(context.Background(), cfg, prometheus.WithRegisterer(reg))
		if err != nil {
			t.Fatalf("newMeterProvider(%q) = %v", ep, err)
		}
		if status.Enabled || status.Err == nil || status.Readers != 1 {
			t.Errorf("status for %q = %+v, want Enabled=false with Err and Readers=1", ep, status)
		}
		c, _ := mp.Meter("bad-ep").Int64Counter("bad_ep_things")
		c.Add(context.Background(), 1)
		rec := httptest.NewRecorder()
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if !strings.Contains(rec.Body.String(), "bad_ep_things") {
			t.Errorf("/metrics lost the instrument after bad endpoint %q", ep)
		}
		_ = mp.Shutdown(context.Background())
	}
}

func TestInitOTEL_UnreachableCollectorDoesNotBlockStart(t *testing.T) {
	// Grab a port, then close it so nothing listens there.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	cfg := LoadConfig("test", "", WithMetricsOTLP("http://"+addr, time.Minute, false))
	start := time.Now()
	mp, status, err := newMeterProvider(context.Background(), cfg, privateProm())
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("start took %v with an unreachable collector, want < 1s", took)
	}
	if !status.Enabled {
		t.Errorf("status = %+v: a dial failure must not be known at start", status)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = mp.Shutdown(shutdownCtx) // the final flush fails; irrelevant to the start path
}

func TestMetricEndpointOption_Forms(t *testing.T) {
	ok := []string{"127.0.0.1:4317", "localhost:4317", "[::1]:4317", "tempo:4317",
		"http://127.0.0.1:4317", "https://collector.example:4317", "dns:///tempo:4317", " http://127.0.0.1:4317 "}
	for _, ep := range ok {
		tgt, err := parseOTLPEndpoint(ep)
		if err != nil {
			t.Errorf("parseOTLPEndpoint(%q) = %v, want accepted", ep, err)
			continue
		}
		for _, insecure := range []bool{false, true} {
			opts := metricEndpointOption(tgt, insecure)
			want := 1
			if tgt.Bare && insecure {
				want = 2 // WithEndpoint + WithInsecure
			}
			if len(opts) != want {
				t.Errorf("metricEndpointOption(%q, insecure=%v) gave %d options, want %d", ep, insecure, len(opts), want)
			}
		}
	}
	for _, ep := range []string{"", "127.0.0.1", "http://127.0.0.1", "ftp://host:21", "dns:///", ":4317"} {
		if _, err := parseOTLPEndpoint(ep); err == nil {
			t.Errorf("parseOTLPEndpoint(%q) accepted, want an error", ep)
		}
	}
}

func TestInterval_ParsingAndClamp(t *testing.T) {
	for in, want := range map[string]time.Duration{"60s": 60 * time.Second, "": 0, "abc": 0, "-5s": 0, "0s": 0, "2m": 2 * time.Minute} {
		if got := ParseMetricsInterval(in); got != want {
			t.Errorf("ParseMetricsInterval(%q) = %v, want %v", in, got, want)
		}
	}
	for _, tc := range []struct {
		in   time.Duration
		want time.Duration
		note bool
	}{
		{0, 60 * time.Second, true},
		{time.Second, 5 * time.Second, true},
		{5 * time.Second, 5 * time.Second, false},
		{30 * time.Second, 30 * time.Second, false},
		{time.Hour, time.Hour, false},
		{2 * time.Hour, time.Hour, true},
	} {
		got, note := clampInterval(tc.in)
		if got != tc.want || (note != "") != tc.note {
			t.Errorf("clampInterval(%v) = %v, %q; want %v, note=%v", tc.in, got, note, tc.want, tc.note)
		}
	}
}

func TestNewResource_EnvironmentDefaultsToProd(t *testing.T) {
	if got := resourceAttrs(NewResourceWithEnvironment("svc", ""))["deployment.environment"]; got != "prod" {
		t.Errorf("empty environment = %q, want prod", got)
	}
	if got := resourceAttrs(NewResourceWithEnvironment("svc", "dev"))["deployment.environment"]; got != "dev" {
		t.Errorf("environment = %q, want dev", got)
	}
}
