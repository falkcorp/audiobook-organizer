// file: internal/server/pebble_metrics_sources.go
// version: 1.0.0
// guid: 790014b3-eca8-4d71-8304-cc119aa559bd
// last-edited: 2026-10-04

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// registerPebbleMetricsSources wires the pebble_* Prometheus series to this
// server's stores. The sources are resolved on every scrape, not captured
// here: the OpenLibrary store is closed and re-opened by the delete and
// factory-reset handlers, and the main store may sit behind the search-index
// decorator (database.AsCapability walks it; a bare assertion fails in prod).
// The AI-scan store is deliberately not exported: it shares the main DB.
func (s *Server) registerPebbleMetricsSources() {
	metrics.SetPebbleSource(metrics.PebbleStoreMain, func() (metrics.PebbleSample, bool) {
		sampler, ok := database.AsCapability[database.PebbleMetricsSampler](s.Ops())
		if !ok {
			return metrics.PebbleSample{}, false
		}
		return sampler.PebbleMetricsSample()
	})
	metrics.SetPebbleSource(metrics.PebbleStoreOpenLibrary, func() (metrics.PebbleSample, bool) {
		if s.olService == nil {
			return metrics.PebbleSample{}, false
		}
		return s.olService.PebbleMetricsSample()
	})
}
