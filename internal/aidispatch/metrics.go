// file: internal/aidispatch/metrics.go
// version: 2.0.0
// guid: a8bcb9cf-e760-4b64-b4b9-35d8b18de82d
// last-edited: 2026-10-10

package aidispatch

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// The five ai_dispatch_* families are OTel instruments from
// telemetry.Meter("aidispatch"); the Prometheus exporter turns the dotted
// names into the series names dashboards already read (ai_dispatch_requests_total,
// ai_dispatch_inflight, ai_dispatch_failover_total, ai_dispatch_no_capable_total,
// ai_dispatch_slot_wait_seconds). "ai_dispatch" is the sanctioned unprefixed
// name (spec 11 D67). The slot-wait buckets come from the view in
// internal/telemetry/views.go, never from here.
//
// The instruments are created lazily, on the first Call. Production calls Call
// only from the routed sites (internal/ai pool_routing.go) while
// ai_endpoints_routing is on. An OTel instrument exports nothing until it
// records, so with the switch off /metrics gains no ai_dispatch_ series.
var (
	metricsOnce sync.Once

	requests  metric.Int64Counter
	inflight  metric.Int64UpDownCounter
	failover  metric.Int64Counter
	noCapable metric.Int64Counter
	slotWait  metric.Float64Histogram
	metricsMP metric.MeterProvider // nil: the global provider; tests inject a private one
)

func ensureMetrics() {
	metricsOnce.Do(func() {
		mp := metricsMP
		if mp == nil {
			mp = otel.GetMeterProvider()
		}
		createInstruments(telemetry.MeterFrom(mp, "aidispatch"))
	})
}

// createInstruments builds the five instruments. The metric package's
// constructors return a no-op instrument alongside any error, so a failure
// here degrades to missing series, never a panic or a nil dereference.
func createInstruments(m metric.Meter) {
	requests, _ = m.Int64Counter("ai_dispatch.requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("AI dispatch attempts by capability, endpoint and outcome class."))
	inflight, _ = m.Int64UpDownCounter("ai_dispatch.inflight",
		metric.WithUnit("{request}"),
		metric.WithDescription("AI dispatch requests currently holding an endpoint slot."))
	failover, _ = m.Int64Counter("ai_dispatch.failover",
		metric.WithUnit("{request}"),
		metric.WithDescription("AI dispatch failovers away from an endpoint, by capability, endpoint and class."))
	noCapable, _ = m.Int64Counter("ai_dispatch.no_capable",
		metric.WithUnit("{request}"),
		metric.WithDescription("AI dispatch calls refused because no endpoint was capable."))
	slotWait, _ = m.Float64Histogram("ai_dispatch.slot_wait",
		metric.WithUnit("s"),
		metric.WithDescription("Time spent waiting for an endpoint in-flight slot."))
}

// RegisterMetrics creates the five instruments now instead of on the first
// Call. Production never calls it (creation stays lazy, as the comment above
// says); the /metrics series contract test in internal/telemetry/contract
// does, so it can pin the families without depending on Call being the first
// thing to run.
func RegisterMetrics() { ensureMetrics() }
