// file: internal/telemetry/metric_env_isolation_test.go
// version: 1.0.0
// guid: c3a71e58-04bd-4f92-9e6a-5d18b2f7a0c4
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// headerSink records the incoming gRPC metadata of every Export next to the
// request.
type headerSink struct {
	metricSink
	mu2  sync.Mutex
	meta []metadata.MD
}

func (s *headerSink) Export(ctx context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu2.Lock()
	s.meta = append(s.meta, md)
	s.mu2.Unlock()
	return s.metricSink.Export(ctx, req)
}

func (s *headerSink) metas() []metadata.MD {
	s.mu2.Lock()
	defer s.mu2.Unlock()
	return append([]metadata.MD(nil), s.meta...)
}

func startHeaderSink(t *testing.T) (*headerSink, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &headerSink{}
	srv := grpc.NewServer()
	colmetricpb.RegisterMetricsServiceServer(srv, sink)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return sink, lis.Addr().String()
}

// pushOnce builds a provider for cfg, bumps a counter and shuts down, which
// flushes the periodic reader exactly once.
func pushOnce(t *testing.T, cfg *Config) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mp, status, err := newMeterProvider(ctx, cfg, privateProm())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled {
		t.Fatalf("status = %+v, want the OTLP reader on", status)
	}
	c, err := mp.Meter("env-isolation").Int64Counter("env_isolation_things")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(ctx, 7)
	_ = mp.Shutdown(ctx) // the flush error (TLS mismatch) is the point of one test
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
}

// A bare host:port with insecure=false is TLS. The generic
// OTEL_EXPORTER_OTLP_INSECURE=true / http:// endpoint must not downgrade it:
// against a plaintext collector nothing may be exported.
func TestOTLPMetrics_EnvCannotDowngradeTLS(t *testing.T) {
	setHostileTraceEnv(t)
	sink, addr := startHeaderSink(t)

	pushOnce(t, LoadConfig("test", "", WithMetricsOTLP(addr, time.Minute, false)))

	if n := len(sink.snapshot()); n != 0 {
		t.Fatalf("collector received %d plaintext exports from a TLS-configured endpoint; the generic OTLP environment downgraded the transport", n)
	}
}

// With plaintext configured, the trace collector's headers must not be sent
// and temporality stays cumulative (D66).
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
			sink, addr := startHeaderSink(t)
			ep, insecure := endpoint(addr)

			pushOnce(t, LoadConfig("test", "", WithMetricsOTLP(ep, time.Minute, insecure)))

			reqs := sink.snapshot()
			if len(reqs) == 0 {
				t.Fatal("plaintext collector got no export: the certificate environment overrode the explicit plaintext transport")
			}
			for _, md := range sink.metas() {
				if v := md.Get("authorization"); len(v) != 0 {
					t.Errorf("authorization header %q reached the metric collector", v)
				}
			}
			var seen bool
			for _, req := range reqs {
				for _, rm := range req.GetResourceMetrics() {
					for _, sm := range rm.GetScopeMetrics() {
						for _, m := range sm.GetMetrics() {
							if m.GetName() != "env_isolation_things" {
								continue
							}
							seen = true
							if got := m.GetSum().GetAggregationTemporality(); got != metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
								t.Errorf("temporality = %v, want CUMULATIVE (OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta must be ignored)", got)
							}
						}
					}
				}
			}
			if !seen {
				t.Error("the counter never reached the collector")
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

func TestRateLimitedErrorHandler(t *testing.T) {
	type line struct {
		msg   string
		attrs []any
	}
	var lines []line
	now := time.Unix(1_000, 0)
	h := newRateLimitedErrorHandler(10*time.Minute, func() time.Time { return now },
		func(_ slog.Level, msg string, attrs ...any) { lines = append(lines, line{msg, attrs}) })

	h.Handle(nil) // ignored
	h.Handle(errors.New("export failed: dial https://user:pw@collector.example.invalid:4317"))
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		h.Handle(errors.New("again"))
	}
	if len(lines) != 1 {
		t.Fatalf("logged %d lines inside the interval, want 1", len(lines))
	}
	got := strings.Join(func() (out []string) {
		for _, a := range lines[0].attrs {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return
	}(), " ")
	if strings.Contains(got, "user:pw") {
		t.Errorf("userinfo leaked into the log line: %s", got)
	}

	now = now.Add(10 * time.Minute)
	h.Handle(errors.New("later"))
	if len(lines) != 2 {
		t.Fatalf("logged %d lines after the interval, want 2", len(lines))
	}
	if lines[1].attrs[2] != "suppressed_since_last" || lines[1].attrs[3] != 5 {
		t.Errorf("second line attrs = %v, want suppressed_since_last=5", lines[1].attrs)
	}
}

func TestInitSummary_BothHalvesOffKeepsBothMessages(t *testing.T) {
	cfg := LoadConfig("test", "https://u:secret@tempo.example.invalid:4317")
	level, msg, attrs := initSummary(cfg, false, errors.New("trace boom https://u:secret@tempo.example.invalid:4317"),
		otlpStatus{Err: errors.New("endpoint \"nohostport\" is neither a URL nor host:port")})
	if level != slog.LevelError {
		t.Errorf("level = %v, want error", level)
	}
	if !strings.Contains(msg, "tracing OFF") || !strings.Contains(msg, "OTLP metric push OFF") {
		t.Errorf("msg = %q, want both clauses", msg)
	}
	flat := strings.Join(func() (out []string) {
		for _, a := range attrs {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return
	}(), " ")
	for _, want := range []string{"tracing_error", "otlp_metrics_error"} {
		if !strings.Contains(flat, want) {
			t.Errorf("attrs lack %s: %s", want, flat)
		}
	}
	if strings.Contains(flat, "secret") {
		t.Errorf("userinfo not redacted: %s", flat)
	}
}

func TestRedactUserinfo(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:p@h:4317":                     "https://h:4317",
		"endpoint \"http://u@h:1\" is bad":       "endpoint \"http://h:1\" is bad",
		"h:4317":                                 "h:4317",
		"dns:///h:4317":                          "dns:///h:4317",
		"parse \"http://a:b@h:1\": invalid port": "parse \"http://h:1\": invalid port",
	} {
		if got := redactUserinfo(in); got != want {
			t.Errorf("redactUserinfo(%q) = %q, want %q", in, got, want)
		}
	}
}
