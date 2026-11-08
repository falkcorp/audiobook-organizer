// file: internal/metrics/metricstest/metricstest.go
// version: 1.0.0
// guid: 11a35ff3-f156-477c-91ae-cbcbfc969642
// last-edited: 2026-11-07

// Package metricstest gives tests a registry holding internal/metrics'
// families. Production registers them once on the App's Provider
// (books.Attach, R1-15) and nothing registers on Prometheus' default registry
// any more, so a test that reads a family gathers this registry instead of
// prometheus.DefaultGatherer.
package metricstest

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// Registry returns a fresh registry with every internal/metrics family on
// it. The collectors are the package's values, so the samples are the ones
// the code under test records through internal/metrics' helpers; two
// registries from two calls serve the same series.
func Registry(tb testing.TB) *prometheus.Registry {
	tb.Helper()
	reg := prometheus.NewRegistry()
	if err := metrics.Register(reg); err != nil {
		tb.Fatalf("metricstest: register internal/metrics on a fresh registry: %v", err)
	}
	return reg
}
