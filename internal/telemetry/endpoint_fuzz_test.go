// file: internal/telemetry/endpoint_fuzz_test.go
// version: 2.0.0
// guid: 3f8a1c64-9d27-4e50-b6a3-0c71e5d9f284
// last-edited: 2026-11-07

package telemetry

// The endpoint parser, its table, seed corpus and fuzz target moved to core
// (core/metrics/endpoint_fuzz_test.go, R1-10). What stays is ABO's half of the
// no-leak invariant: core's lines reach the process logger through the
// façade's emitHandler, and third-party error text through the redaction
// backstop of the process-wide error handler.

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/metrics"
)

// sentinel can never appear in an accepted endpoint: '_' is not valid in a
// host, a port, a scheme or an IPv6 address.
const sentinel = "SENTINEL_SECRET"

// TestFacade_EndpointSecretsNeverLogged puts the sentinel in each slot of the
// endpoint (userinfo, path, query, fragment, port, scheme, IPv6 brackets),
// builds both halves from it through ABO's options and asserts nothing the
// process logger receives carries it.
func TestFacade_EndpointSecretsNeverLogged(t *testing.T) {
	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	S := sentinel
	for _, in := range []string{
		"https://user:" + S + "@collector:4317", "dns:///user:" + S + "@192.0.2.1:4317",
		"https://collector:4317/" + S, "https://collector:4317?tok=" + S, "https://collector:4317#token=" + S,
		"collector:" + S, S + "://collector:4317", "[" + S + "]:4317",
	} {
		cfg := LoadConfig("t", in, WithMetricsOTLP(in, time.Minute, true))
		o := metricsProviderOptions(cfg)
		o.Tracing = &metrics.OTLPTracing{Endpoint: cfg.ExporterEndpoint}
		p, err := metrics.New(context.Background(), o)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = p.Shutdown(ctx)
		level, msg, attrs := initSummary(true, false, false, true, true)
		emit(context.Background(), level, msg, attrs...)
	}
	out := buf.String()
	if strings.Contains(out, S) {
		t.Fatalf("sentinel reached the process logger: %s", out)
	}
	if !strings.Contains(out, "not a valid OTLP/gRPC endpoint") {
		t.Errorf("core's refusal never reached the process logger through emit: %s", out)
	}
}

// The backstop for third-party error text only has to handle URL-shaped
// tokens: the sentinel in the userinfo, path, query and fragment slots.
func TestRedactEndpointSecrets_SentinelSlots(t *testing.T) {
	S := sentinel
	for _, in := range []string{
		"dial https://u:" + S + "@collector:4317: refused",
		"dial dns:///u:" + S + "@collector:4317: refused",
		"dial https://collector:4317/" + S + ": refused",
		"dial https://collector:4317?k=" + S + ": refused",
		"dial https://collector:4317#" + S + ": refused",
		"dial https://u:pa/" + S + "@collector:4317?k=" + S + "#" + S,
	} {
		if got := redactEndpointSecrets(in); strings.Contains(got, S) {
			t.Errorf("redactEndpointSecrets(%q) = %q, sentinel survived", in, got)
		}
	}
}
