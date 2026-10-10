// file: internal/telemetry/views.go
// version: 1.0.0
// guid: 9154168b-56f2-4d06-bb53-f0b09d8954fa
// last-edited: 2026-10-09

package telemetry

import (
	"maps"
	"slices"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
)

// histogramBuckets declares the bucket boundaries of every OTel histogram the
// binary exports, keyed by instrument name. A histogram must never ship with
// the SDK's default boundaries: they stop at 10000, which would flatten the
// 86400-second tail the overnight dashboard reads from operation durations.
//
// The lists for the families that exist today are copied from the live
// client_golang constructors, so a family migrated to OTel keeps byte-identical
// `le` labels (TestViewsMatchLiveBuckets in internal/telemetry/contract
// compares them against a real scrape). The two exponential lists are
// generated with the same prometheus.ExponentialBuckets call the constructors
// use, so the float64 values are bit-identical rather than retyped decimals.
//
// The http.server.* entries copy the boundaries otelgin produced before this
// table existed (the semconv advice for request duration, the SDK default for
// body sizes), so declaring them changes nothing on /metrics. A view overrides
// an instrument's bucket advice, so an otelgin upgrade that changes its
// defaults is pinned here, not passed through; change these lists on purpose.
var histogramBuckets = map[string][]float64{
	// internal/metrics/metrics.go operationDuration.
	"audiobook_organizer.operation.duration": {0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400, 43200, 86400},
	// internal/metrics/metrics.go cacheGetDuration: 500ns up to ~130ms.
	"audiobook_organizer.cache.get.duration": prometheus.ExponentialBuckets(0.0000005, 4, 10),
	// internal/metrics/pipeline_metrics.go reviewIndexRequestSeconds. Spec 11
	// §3.4 wrote this key as "...review_index.request.duration", which would
	// export review_index_request_duration_seconds and rename the
	// dashboard-pinned review_index_request_seconds family on migration; the
	// key below exports the existing name.
	"audiobook_organizer.review_index.request": {0.1, 0.25, 0.5, 1, 2, 3, 5, 10, 20, 30, 60, 120},
	// internal/metrics/pipeline_metrics.go fixerDurationSeconds.
	"audiobook_organizer.fixer.duration": {0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200},
	// internal/aidispatch/metrics.go slotWaitSeconds: 1ms up to ~262s.
	"ai_dispatch.slot_wait": prometheus.ExponentialBuckets(0.001, 4, 10),

	// Declared ahead of the instruments that will use them (spec 11 §3.4, §3.9).
	"audiobook_organizer.deluge.rpc.duration": {0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	"audiobook_organizer.ops.schedule_lag":    {1, 5, 15, 60, 300, 900, 3600, 21600, 86400},
	"ai.request.duration":                     {0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300},

	// otelgin (server.go router.Use(otelgin.Middleware(...))).
	"http.server.request.duration":   {0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10},
	"http.server.request.body.size":  {0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000},
	"http.server.response.body.size": {0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000},
}

// HistogramBuckets returns a copy of the views table: instrument name to
// bucket boundaries.
func HistogramBuckets() map[string][]float64 {
	out := make(map[string][]float64, len(histogramBuckets))
	for k, v := range histogramBuckets {
		out[k] = slices.Clone(v)
	}
	return out
}

// Views returns one meter-provider option per histogramBuckets entry, in a
// stable (sorted) order, each an explicit-bucket view matched by instrument
// name.
func Views() []metric.Option {
	names := slices.Sorted(maps.Keys(histogramBuckets))
	opts := make([]metric.Option, 0, len(names))
	for _, name := range names {
		opts = append(opts, metric.WithView(metric.NewView(
			metric.Instrument{Name: name, Kind: metric.InstrumentKindHistogram},
			metric.Stream{Aggregation: metric.AggregationExplicitBucketHistogram{
				Boundaries: slices.Clone(histogramBuckets[name]),
			}},
		)))
	}
	return opts
}
