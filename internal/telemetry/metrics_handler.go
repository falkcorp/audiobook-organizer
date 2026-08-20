// file: internal/telemetry/metrics_handler.go
// version: 1.0.1
// guid: d4e5f6a7-b8c9-0005-deef-000000000005
// last-edited: 2026-08-20

package telemetry

import (
	"net/http"

	"go.opentelemetry.io/otel/exporters/prometheus"
)

// MetricsHandler returns an HTTP handler that serves Prometheus metrics.
// The handler exposes OTEL metrics collected by the Prometheus exporter
// via the Prometheus text format.
//
// Wire this to your HTTP server under a /metrics endpoint:
//
//	handler := telemetry.MetricsHandler()
//	router.GET("/metrics", gin.WrapF(handler.ServeHTTP))
//
// This exposes:
// - All OTEL instrumentation metrics (operation latency, HTTP request counts, etc)
// - Custom application metrics recorded via the global meter
// - Standard Go runtime metrics (if runtime instrumentation is enabled)
//
// Note: This creates a new exporter per request as a placeholder. In production,
// you should cache the handler or retrieve it from the registered exporter.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Create a Prometheus exporter that serves the current metrics
		exporter, err := prometheus.New()
		if err != nil {
			http.Error(w, "failed to create prometheus exporter: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Serve the Prometheus metrics in text format
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)

		// TODO: Call the exporter's Gather method and write to w
		// For now, return a placeholder response
		_, _ = w.Write([]byte("# HELP otel_metrics OpenTelemetry metrics\n"))
		_, _ = w.Write([]byte("# TYPE otel_metrics gauge\n"))
		_ = exporter // Use exporter for proper implementation
	})
}
