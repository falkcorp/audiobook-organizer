// file: core/metrics/otlp_test.go
// version: 1.0.0
// guid: f3c6edc8-8ed7-4463-9a07-89749eda8af6
// last-edited: 2026-11-07

package metrics

// The OTLP push and trace export tests, moved from internal/telemetry with
// the code they test (metric_otlp_test.go, metric_env_isolation_test.go,
// telemetry_test.go): they drive real OTLP/gRPC listeners through New.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// metricSink is a plaintext OTLP/gRPC metrics collector that keeps what it
// gets, with the incoming metadata of every export.
type metricSink struct {
	colmetricpb.UnimplementedMetricsServiceServer
	mu   sync.Mutex
	reqs []*colmetricpb.ExportMetricsServiceRequest
	meta []metadata.MD
}

func (s *metricSink) Export(ctx context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.meta = append(s.meta, md)
	s.mu.Unlock()
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

func (s *metricSink) snapshot() ([]*colmetricpb.ExportMetricsServiceRequest, []metadata.MD) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs), slices.Clone(s.meta)
}

func startMetricSink(t *testing.T) (*metricSink, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &metricSink{}
	srv := grpc.NewServer()
	colmetricpb.RegisterMetricsServiceServer(srv, sink)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return sink, lis.Addr().String()
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

// pushOnce builds a Provider with the OTLP push o, records a counter and a
// histogram and shuts down, which flushes the periodic reader exactly once.
func pushOnce(t *testing.T, o *OTLPMetrics) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := New(ctx, Options{AppID: "test", OTLP: o})
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Degraded(); len(d) != 0 {
		t.Fatalf("Degraded = %v, want the OTLP reader on", d)
	}
	m := p.MeterProvider().Meter("env-isolation")
	c, err := m.Int64Counter("env_isolation_things")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(ctx, 7)
	hist, err := m.Float64Histogram("env_isolation_latency")
	if err != nil {
		t.Fatal(err)
	}
	hist.Record(ctx, 0.25)
	_ = p.Shutdown(ctx) // the flush error (TLS mismatch) is the point of one test
}

func TestOTLPMetrics_PushReachesCollector(t *testing.T) {
	sink, addr := startMetricSink(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := New(ctx, Options{AppID: "a", ServiceName: "test-svc", ServiceVersion: "v1", Environment: "staging",
		OTLP: &OTLPMetrics{Endpoint: "http://" + addr, Interval: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.MeterProvider().Meter("otlp-test").Int64Counter("otlp_test_things")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(ctx, 3)
	// Shutdown flushes the periodic reader once, so the test need not wait a
	// full interval.
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	reqs, _ := sink.snapshot()
	var found bool
	for _, req := range reqs {
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
					if attrs["service.name"] != "test-svc" || attrs["service.version"] != "v1" || attrs["deployment.environment"] != "staging" {
						t.Errorf("resource attrs = %v", attrs)
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("collector never received the counter; got %d requests", len(reqs))
	}
}

func TestOTLPMetrics_UnreachableCollectorDoesNotBlockStart(t *testing.T) {
	// Grab a port, then close it so nothing listens there.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	start := time.Now()
	p, err := New(context.Background(), Options{AppID: "a", Prometheus: true, OTLP: &OTLPMetrics{Endpoint: "http://" + addr}})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("start took %v with an unreachable collector, want < 1s", took)
	}
	if d := p.Degraded(); len(d) != 0 {
		t.Errorf("Degraded = %v: a dial failure must not be known at start", d)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.Shutdown(shutdownCtx) // the final flush fails; irrelevant to the start path
}

// setHostileTraceEnv is the environment of a deployment that ships traces to a
// TLS collector with a bearer token and asks for delta metrics: none of it may
// reach the metric push.
func setHostileTraceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://trace-collector.example.invalid:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer trace-secret")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "authorization=Bearer metrics-secret")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", "delta")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION", "base2_exponential_bucket_histogram")
}

// A bare host:port with Insecure=false is TLS. The generic
// OTEL_EXPORTER_OTLP_INSECURE=true / http:// endpoint must not downgrade it:
// against a plaintext collector nothing may be exported.
func TestOTLPMetrics_EnvCannotDowngradeTLS(t *testing.T) {
	setHostileTraceEnv(t)
	sink, addr := startMetricSink(t)

	pushOnce(t, &OTLPMetrics{Endpoint: addr, Interval: time.Minute})

	if reqs, _ := sink.snapshot(); len(reqs) != 0 {
		t.Fatalf("collector received %d plaintext exports from a TLS-configured endpoint; the generic OTLP environment downgraded the transport", len(reqs))
	}
}

// With plaintext configured, the trace collector's headers must not be sent
// and temporality stays cumulative.
func TestOTLPMetrics_EnvCannotLeakHeadersOrFlipTemporality(t *testing.T) {
	checkPlaintextPush(t, false)
}

// OTEL_EXPORTER_OTLP_CERTIFICATE installs credentials, which the exporter
// prefers over its insecure flag: explicit plaintext must still win.
func TestOTLPMetrics_EnvCertificateCannotOverrideInsecure(t *testing.T) {
	checkPlaintextPush(t, true)
}

func checkPlaintextPush(t *testing.T, withCertEnv bool) {
	for name, endpoint := range map[string]func(addr string) (string, bool){
		"http-url":      func(a string) (string, bool) { return "http://" + a, false },
		"bare-insecure": func(a string) (string, bool) { return a, true },
		"dns-insecure":  func(a string) (string, bool) { return "dns:///" + a, true },
	} {
		t.Run(name, func(t *testing.T) {
			setHostileTraceEnv(t)
			if withCertEnv {
				t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", writeSelfSignedPEM(t))
			}
			sink, addr := startMetricSink(t)
			ep, insecure := endpoint(addr)

			pushOnce(t, &OTLPMetrics{Endpoint: ep, Insecure: insecure, Interval: time.Minute})

			reqs, metas := sink.snapshot()
			if len(reqs) == 0 {
				t.Fatal("plaintext collector got no export: the certificate environment overrode the explicit plaintext transport")
			}
			for _, md := range metas {
				if v := md.Get("authorization"); len(v) != 0 {
					t.Errorf("authorization header %q reached the metric collector", v)
				}
			}
			var seen, histSeen bool
			for _, req := range reqs {
				for _, rm := range req.GetResourceMetrics() {
					for _, sm := range rm.GetScopeMetrics() {
						for _, m := range sm.GetMetrics() {
							switch m.GetName() {
							case "env_isolation_latency":
								histSeen = true
								if m.GetExponentialHistogram() != nil || m.GetHistogram() == nil {
									t.Errorf("histogram exported as %T, want the explicit-bucket default (OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION must be ignored)", m.GetData())
								}
							case "env_isolation_things":
								seen = true
								if got := m.GetSum().GetAggregationTemporality(); got != metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
									t.Errorf("temporality = %v, want CUMULATIVE (OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta must be ignored)", got)
								}
							}
						}
					}
				}
			}
			if !seen || !histSeen {
				t.Errorf("collector missed an instrument: counter=%v histogram=%v", seen, histSeen)
			}
		})
	}
}

func writeSelfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.invalid"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOTLPMetrics_UserinfoNeverLogged: an endpoint with userinfo is refused,
// and neither the Provider's log lines nor its shutdown error carry the
// secret.
func TestOTLPMetrics_UserinfoNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := New(ctx, Options{AppID: "a", Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		OTLP:    &OTLPMetrics{Endpoint: "dns:///user:secret@192.0.2.1:4317", Insecure: true},
		Tracing: &OTLPTracing{Endpoint: "https://user:secret@192.0.2.1:4317"}})
	if err != nil {
		t.Fatal(err)
	}
	if sdErr := p.Shutdown(ctx); sdErr != nil && strings.Contains(sdErr.Error(), "secret") {
		t.Errorf("shutdown error leaks userinfo: %v", sdErr)
	}
	if strings.Contains(logs.String(), "secret") {
		t.Errorf("log output leaks userinfo: %s", logs.String())
	}
}

// TestTracing_SpansReachAPlaintextCollector proves the endpoint forms against
// a real gRPC listener, the way Tempo listens on :4317: the URL form needs
// nothing else, the bare form needs the SDK's insecure switch or Insecure.
func TestTracing_SpansReachAPlaintextCollector(t *testing.T) {
	sink, addr := startTraceServer(t)
	for _, tc := range []struct {
		name        string
		o           OTLPTracing
		insecureEnv bool
	}{
		{"url form", OTLPTracing{Endpoint: "http://" + addr}, false},
		{"bare host:port with the insecure switch", OTLPTracing{Endpoint: addr}, true},
		{"bare host:port with Insecure", OTLPTracing{Endpoint: addr, Insecure: true}, false},
		{"ratio sampler at 1", OTLPTracing{Endpoint: "http://" + addr, SampleRatio: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.insecureEnv {
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_INSECURE", "true")
			}
			before := sink.spans.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			o := tc.o
			p, err := New(ctx, Options{AppID: "a", Tracing: &o})
			if err != nil {
				t.Fatal(err)
			}
			if d := p.Degraded(); len(d) != 0 {
				t.Fatalf("Degraded = %v", d)
			}
			_, span := p.TracerProvider().Tracer("test").Start(ctx, "op")
			span.End()
			if err := p.Shutdown(ctx); err != nil {
				t.Fatalf("shutdown (flush) to %q: %v", o.Endpoint, err)
			}
			if got := sink.spans.Load() - before; got != 1 {
				t.Fatalf("collector received %d spans from %q, want 1", got, o.Endpoint)
			}
		})
	}
}

func TestTraceAndMetricOptions_Forms(t *testing.T) {
	ok := []string{"127.0.0.1:4317", "localhost:4317", "[::1]:4317", "tempo:4317",
		"http://127.0.0.1:4317", "https://collector.example.invalid:4317", "dns:///tempo:4317", " http://127.0.0.1:4317 "}
	for _, ep := range ok {
		tgt, err := parseOTLPEndpoint(keyMetricsEndpoint, ep)
		if err != nil {
			t.Errorf("%q: %v, want accepted", ep, err)
			continue
		}
		for _, insecure := range []bool{false, true} {
			want := 2 // endpoint + TLS credentials
			if metricPlaintext(tgt, insecure) {
				want = 3 // endpoint + WithInsecure + insecure credentials
			}
			if got := len(metricOptions(tgt, insecure)); got != want {
				t.Errorf("metricOptions(%q, insecure=%v) gave %d options, want %d", ep, insecure, got, want)
			}
			if len(traceOptions(tgt, insecure)) == 0 {
				t.Errorf("traceOptions(%q, %v) is empty", ep, insecure)
			}
		}
	}
	for _, ep := range []string{"", "127.0.0.1", "http://127.0.0.1", "http://:4317", "ftp://host:21", "invalid://endpoint", "dns:///", ":4317"} {
		if _, err := parseOTLPEndpoint(keyTraceEndpoint, ep); err == nil {
			t.Errorf("%q accepted, want an error", ep)
		}
	}
}

func TestInterval_Clamp(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want time.Duration
		note bool
	}{
		{0, 60 * time.Second, true},
		{-time.Second, 60 * time.Second, true},
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

func TestMetricPlaintext_OneRule(t *testing.T) {
	for _, tc := range []struct {
		ep       string
		insecure bool
		want     bool
	}{
		{"http://192.0.2.10:4317", false, true},
		{"http://192.0.2.10:4317", true, true},
		{"https://collector.example.invalid:4317", false, false},
		{"https://collector.example.invalid:4317", true, false},
		{"192.0.2.10:4317", false, false},
		{"192.0.2.10:4317", true, true},
		{"dns:///collector.example.invalid:4317", false, false},
		{"dns:///collector.example.invalid:4317", true, true},
	} {
		tgt, err := parseOTLPEndpoint(keyMetricsEndpoint, tc.ep)
		if err != nil {
			t.Fatal(err)
		}
		if got := metricPlaintext(tgt, tc.insecure); got != tc.want {
			t.Errorf("metricPlaintext(%q, insecure=%v) = %v, want %v", tc.ep, tc.insecure, got, tc.want)
		}
	}
}
