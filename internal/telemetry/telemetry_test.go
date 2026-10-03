// file: internal/telemetry/telemetry_test.go
// version: 2.1.0
// guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-10-03

package telemetry

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

func TestLoadConfig_MetricsOnWithoutEndpoint(t *testing.T) {
	cfg := LoadConfig("test", "")
	if !cfg.MetricsEnabled {
		t.Error("MetricsEnabled should be true with no endpoint")
	}
	if cfg.TracingEnabled {
		t.Error("TracingEnabled should be false with no endpoint")
	}
	cfg = LoadConfig("test", "tempo:4317")
	if !cfg.TracingEnabled || !cfg.MetricsEnabled {
		t.Errorf("with an endpoint both halves should be on; got metrics=%v tracing=%v", cfg.MetricsEnabled, cfg.TracingEnabled)
	}
}

func TestInitOTEL_NothingEnabled(t *testing.T) {
	shutdown, err := InitOTEL(context.Background(), &Config{ServiceName: "test"})
	if err != nil {
		t.Fatalf("InitOTEL with nothing enabled returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown function should not be nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown returned error: %v", err)
	}
}

// TestInitOTEL_NoEndpoint_StillInstallsMeterProvider is the regression pin for
// the split: the production configuration (no OTLP endpoint) must still get a
// real meter provider whose instruments reach the Prometheus scrape.
func TestInitOTEL_NoEndpoint_StillInstallsMeterProvider(t *testing.T) {
	otel.SetMeterProvider(noop.NewMeterProvider())
	cfg := LoadConfig("test", "")

	shutdown, err := InitOTEL(context.Background(), cfg)
	if err != nil {
		t.Fatalf("InitOTEL with no endpoint returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	if _, isNoop := otel.GetMeterProvider().(noop.MeterProvider); isNoop {
		t.Fatal("global meter provider is still the no-op: metrics were gated behind the trace endpoint")
	}

	// The OTel Prometheus exporter registers with the default registry that
	// /metrics serves; its target_info series is the scrape-visible proof.
	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "target_info") {
		t.Error("scrape has no target_info: the OTel Prometheus exporter is not registered")
	}
}

// TestInitOTEL_CalledTwice_SharesOneMeterProvider: the exporter registration
// is process-global, so a second init must reuse rather than re-register, and
// both shutdowns must succeed.
func TestInitOTEL_CalledTwice_SharesOneMeterProvider(t *testing.T) {
	cfg := LoadConfig("test", "")
	s1, err := InitOTEL(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first InitOTEL: %v", err)
	}
	s2, err := InitOTEL(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second InitOTEL: %v", err)
	}
	if err := s1(context.Background()); err != nil {
		t.Errorf("first shutdown: %v", err)
	}
	if err := s2(context.Background()); err != nil {
		t.Errorf("second shutdown: %v", err)
	}
}

// TestInitOTEL_UnusableEndpointIsNotFatal: a trace endpoint that cannot be
// used turns tracing off and nothing else. On 2026-10-03 the error was
// returned, cmd/root.go made it fatal, and prod crash-looped on
// OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:4317.
func TestInitOTEL_UnusableEndpointIsNotFatal(t *testing.T) {
	for _, ep := range []string{"invalid://endpoint", "no-port", "http://", "ftp://host:21", "dns:///"} {
		shutdown, err := InitOTEL(context.Background(), LoadConfig("test", ep))
		if err != nil {
			t.Fatalf("InitOTEL(%q) = %v; an unusable trace endpoint must not fail init", ep, err)
		}
		if err := shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown after %q: %v", ep, err)
		}
	}
}

func TestTraceEndpointOption_Forms(t *testing.T) {
	ok := []string{"127.0.0.1:4317", "localhost:4317", "[::1]:4317", "tempo:4317",
		"http://127.0.0.1:4317", "https://collector.example:4317", "dns:///tempo:4317", " http://127.0.0.1:4317 "}
	for _, ep := range ok {
		if _, err := traceEndpointOption(ep); err != nil {
			t.Errorf("traceEndpointOption(%q) = %v, want accepted", ep, err)
		}
	}
	bad := []string{"", "127.0.0.1", "http://127.0.0.1", "http://:4317", "ftp://host:21", "invalid://endpoint", "dns:///", ":4317"}
	for _, ep := range bad {
		if _, err := traceEndpointOption(ep); err == nil {
			t.Errorf("traceEndpointOption(%q) accepted, want an error", ep)
		}
	}
}

// traceSink is a plaintext OTLP/gRPC collector that counts the spans it gets.
type traceSink struct {
	coltracepb.UnimplementedTraceServiceServer
	spans atomic.Int64
}

func (s *traceSink) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			s.spans.Add(int64(len(ss.GetSpans())))
		}
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// TestInitTracing_SpansReachAPlaintextCollector proves both endpoint forms
// against a real gRPC listener, the way Tempo listens on :4317: the URL form
// needs nothing else, the bare form needs the SDK's insecure switch.
func TestInitTracing_SpansReachAPlaintextCollector(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &traceSink{}
	srv := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(srv, sink)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	addr := lis.Addr().String()

	for _, tc := range []struct {
		name, endpoint string
		insecureEnv    bool
	}{
		{"url form", "http://" + addr, false},
		{"bare host:port with the insecure switch", addr, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.insecureEnv {
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_INSECURE", "true")
			}
			before := sink.spans.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tp, err := initTracing(ctx, LoadConfig("test", tc.endpoint))
			if err != nil {
				t.Fatalf("initTracing(%q): %v", tc.endpoint, err)
			}
			_, span := tp.Tracer("test").Start(ctx, "op")
			span.End()
			if err := tp.ForceFlush(ctx); err != nil {
				t.Fatalf("flush to %q: %v", tc.endpoint, err)
			}
			if err := tp.Shutdown(ctx); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			if got := sink.spans.Load() - before; got != 1 {
				t.Fatalf("collector received %d spans from %q, want 1", got, tc.endpoint)
			}
		})
	}
}
