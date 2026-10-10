// file: internal/telemetry/trace_tls_test.go
// version: 1.0.0
// guid: a4d2f7c8-6e13-4b95-9c0a-3e58d1b7f642
// last-edited: 2026-10-10

package telemetry

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// testPKI makes a CA (written to a PEM file) and a server certificate for
// 127.0.0.1 signed by it.
func testPKI(t *testing.T) (caFile string, serverCert tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca.example.invalid"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "collector.example.invalid"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return caFile, tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}
}

func startTraceServer(t *testing.T, opts ...grpc.ServerOption) (*traceSink, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &traceSink{}
	srv := grpc.NewServer(opts...)
	coltracepb.RegisterTraceServiceServer(srv, sink)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return sink, lis.Addr().String()
}

func exportOneSpan(t *testing.T, endpoint string) error {
	t.Helper()
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tp, err := initTracing(ctx, LoadConfig("test", endpoint))
	if err != nil {
		t.Fatalf("initTracing(%q): %v", endpoint, err)
	}
	_, span := tp.Tracer("test").Start(ctx, "op")
	span.End()
	ferr := tp.ForceFlush(ctx)
	_ = tp.Shutdown(ctx)
	return ferr
}

// An https trace endpoint honours OTEL_EXPORTER_OTLP_CERTIFICATE (a private
// CA), as it did before the transport was pinned, and the explicit
// dns:///host:port dial target still reaches the server. The metric exporter
// deliberately does not (see metricEndpointOption).
func TestTraceHTTPS_HonoursEnvCertificate(t *testing.T) {
	caFile, cert := testPKI(t)
	sink, addr := startTraceServer(t, grpc.Creds(credentials.NewServerTLSFromCert(&cert)))

	// Without the CA the system roots reject the server: proves the CA is
	// what makes the export work.
	if err := exportOneSpan(t, "https://"+addr); err == nil {
		t.Fatal("export to a private-CA server succeeded without the CA")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", caFile)
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true") // must not downgrade https
	before := sink.spans.Load()
	if err := exportOneSpan(t, "https://"+addr); err != nil {
		t.Fatalf("https export with OTEL_EXPORTER_OTLP_CERTIFICATE failed: %v", err)
	}
	if got := sink.spans.Load() - before; got != 1 {
		t.Fatalf("collector received %d spans, want 1", got)
	}
}

// http:// is really plaintext: a CERTIFICATE in the environment must not turn
// it into TLS.
func TestTraceHTTP_PlaintextSurvivesEnvCertificate(t *testing.T) {
	caFile, _ := testPKI(t)
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", caFile)
	sink, addr := startTraceServer(t)
	if err := exportOneSpan(t, "http://"+addr); err != nil {
		t.Fatalf("plaintext export with OTEL_EXPORTER_OTLP_CERTIFICATE set failed: %v", err)
	}
	if sink.spans.Load() != 1 {
		t.Fatalf("collector received %d spans, want 1", sink.spans.Load())
	}
}

// The trace options dial the explicit dns:///host:port target in every form
// (WithEndpoint after WithEndpointURL wins), so a host named like a resolver
// scheme is still a host.
func TestTraceOptions_DialExplicitDNSTarget(t *testing.T) {
	for _, ep := range []string{"unix:4317", "http://unix:4317", "https://dns:443", "dns:///passthrough:4317"} {
		tgt, err := parseOTLPEndpoint(keyTraceEndpoint, ep)
		if err != nil {
			t.Fatalf("%q: %v", ep, err)
		}
		if got := canonicalGRPCTarget(t, tgt.GRPCTarget()); got != tgt.GRPCTarget() {
			t.Errorf("%q: gRPC reads %q as %q", ep, tgt.GRPCTarget(), got)
		}
		if len(traceOptions(tgt)) == 0 {
			t.Errorf("%q: no trace options", ep)
		}
	}
}
