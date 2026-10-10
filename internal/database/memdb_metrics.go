// file: internal/database/memdb_metrics.go
// version: 1.0.1
// guid: 5b0f6c1e-2a47-4d8e-9c35-7e1a4b9d2f60
// last-edited: 2026-10-10

package database

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// newMemdbFallbackCounter builds the fallback-read counter on mp. It counts
// reads that wanted the in-memory query layer (UseMemDB is on) but found it not
// yet published and fell back to the slow Pebble path. The OTel instrument name
// has no suffix; the Prometheus exporter appends "_total", so /metrics exposes
// memdb_fallback_reads_total{site=...}.
//
// Each store owns its instrument, built from an injected provider, so a test can
// pass a private provider and never touch the process-global one (the global
// delegate binds to the first SetMeterProvider only, which made a shared,
// once-built instrument unusable across tests). Production passes
// otel.GetMeterProvider(): telemetry.InitOTEL installs the real provider after
// the first store may already exist, and the global provider hands out
// delegating instruments that begin recording as soon as it is installed.
// Returns nil on error; recording on a nil counter is a no-op.
func newMemdbFallbackCounter(mp metric.MeterProvider) metric.Int64Counter {
	c, err := mp.Meter("audiobook-organizer/database").Int64Counter(
		"memdb_fallback_reads",
		metric.WithDescription("Reads that fell back to the Pebble path because the memdb was not yet published."),
	)
	if err != nil {
		return nil
	}
	return c
}

// setMeterProvider rebuilds this store's metric instruments on mp. Call it
// before the store serves reads (the field is not synchronised).
func (p *PebbleStore) setMeterProvider(mp metric.MeterProvider) {
	p.memdbFallback = newMemdbFallbackCounter(mp)
}

// initMetrics wires the store's instruments to the process-global provider.
func (p *PebbleStore) initMetrics() { p.setMeterProvider(otel.GetMeterProvider()) }

// recordMemdbFallback counts one fallback read attributed to site (the calling
// method name; a small fixed set, so label cardinality stays bounded).
func (p *PebbleStore) recordMemdbFallback(site string) {
	if p.memdbFallback != nil {
		p.memdbFallback.Add(context.Background(), 1, metric.WithAttributes(attribute.String("site", site)))
	}
}
