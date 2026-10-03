// file: internal/telemetry/telemetry_test.go
// version: 2.0.0
// guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-10-03

package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"
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

func TestInitOTEL_WithInvalidEndpoint(t *testing.T) {
	cfg := LoadConfig("test", "invalid://endpoint")

	// Should fail due to invalid endpoint format
	_, err := InitOTEL(context.Background(), cfg)
	if err == nil {
		t.Fatal("InitOTEL with invalid endpoint should return error")
	}
}
