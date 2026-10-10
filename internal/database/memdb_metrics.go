// file: internal/database/memdb_metrics.go
// version: 1.0.3
// guid: 5b0f6c1e-2a47-4d8e-9c35-7e1a4b9d2f60
// last-edited: 2026-10-10

package database

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// Values of the `outcome` attribute on audiobook_organizer_memdb_fallback_reads_total.
const (
	// memdbOutcomeFallback: the read was served from Pebble instead (slower).
	memdbOutcomeFallback = "fallback"
	// memdbOutcomeRefused: the read has no Pebble path and returned an error.
	memdbOutcomeRefused = "refused"
)

// newMemdbFallbackCounter builds the fallback-read counter on mp. It counts
// reads that wanted the in-memory query layer (UseMemDB is on) but found it not
// yet published. Attributes: site (the method) and outcome, "fallback" when the
// read was served from Pebble and "refused" when it had no Pebble path and
// returned an error. The OTel instrument name has no suffix; the Prometheus
// exporter appends "_total", so /metrics exposes
// audiobook_organizer_memdb_fallback_reads_total{site=...,outcome=...}.
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
	c, err := telemetry.MeterFrom(mp, "database").Int64Counter(
		"audiobook_organizer.memdb.fallback_reads",
		metric.WithDescription("Reads that wanted the in-memory layer before it was published: outcome=fallback were served from Pebble, outcome=refused returned an error."),
	)
	if err != nil {
		return nil
	}
	return c
}

// SetMeterProvider rebuilds this store's metric instruments on mp. Call it
// before the store serves reads (the field is not synchronised).
func (p *PebbleStore) SetMeterProvider(mp metric.MeterProvider) {
	p.memdbFallback = newMemdbFallbackCounter(mp)
}

// initMetrics wires the store's instruments to the process-global provider.
func (p *PebbleStore) initMetrics() { p.SetMeterProvider(otel.GetMeterProvider()) }

// recordMemdbFallback counts one unmet memdb read attributed to site (the
// calling method name) and outcome; both are small fixed sets, so label
// cardinality stays bounded.
func (p *PebbleStore) recordMemdbFallback(site, outcome string) {
	if p.memdbFallback != nil {
		p.memdbFallback.Add(context.Background(), 1, metric.WithAttributes(telemetry.Site.String(site), telemetry.Outcome.String(outcome)))
	}
}
