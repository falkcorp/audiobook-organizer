// file: internal/database/memdb_metrics.go
// version: 1.0.0
// guid: 5b0f6c1e-2a47-4d8e-9c35-7e1a4b9d2f60
// last-edited: 2026-10-09

package database

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// memdbFallbackReads counts reads that wanted the in-memory query layer
// (UseMemDB is on) but found it not yet published and fell back to the slow
// Pebble path. The OTel instrument name has no suffix; the Prometheus exporter
// appends "_total", so /metrics exposes memdb_fallback_reads_total{site=...}.
//
// The instrument is created lazily through the global meter provider because
// telemetry.InitOTEL installs that provider after the first store may already
// exist. The global meter hands out delegating instruments that begin
// recording as soon as the real provider is installed.
var (
	memdbFallbackOnce    sync.Once
	memdbFallbackCounter metric.Int64Counter
)

func memdbFallbackReads() metric.Int64Counter {
	memdbFallbackOnce.Do(func() {
		c, err := otel.Meter("audiobook-organizer/database").Int64Counter(
			"memdb_fallback_reads",
			metric.WithDescription("Reads that fell back to the Pebble path because the memdb was not yet published."),
		)
		if err == nil {
			memdbFallbackCounter = c
		}
	})
	return memdbFallbackCounter
}

// recordMemdbFallback counts one fallback read attributed to site (the calling
// method name; a small fixed set, so label cardinality stays bounded).
func recordMemdbFallback(site string) {
	if c := memdbFallbackReads(); c != nil {
		c.Add(context.Background(), 1, metric.WithAttributes(attribute.String("site", site)))
	}
}
