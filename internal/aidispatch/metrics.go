// file: internal/aidispatch/metrics.go
// version: 1.0.0
// guid: a8bcb9cf-e760-4b64-b4b9-35d8b18de82d
// last-edited: 2026-09-13

package aidispatch

import (
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are registered lazily, on the first Call. Nothing in production
// calls Call until a later PR moves a call site onto the dispatcher, so this
// PR adds no series to /metrics.
var (
	metricsOnce sync.Once

	requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_dispatch_requests_total",
		Help: "AI dispatch attempts by capability, endpoint and outcome class.",
	}, []string{"capability", "endpoint", "outcome"})

	inflightGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ai_dispatch_inflight",
		Help: "AI dispatch requests currently holding an endpoint slot.",
	}, []string{"endpoint"})

	failoverTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_dispatch_failover_total",
		Help: "AI dispatch failovers away from an endpoint, by capability, endpoint and class.",
	}, []string{"capability", "endpoint", "class"})

	noCapableTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_dispatch_no_capable_total",
		Help: "AI dispatch calls refused because no endpoint was capable.",
	}, []string{"capability"})

	slotWaitSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ai_dispatch_slot_wait_seconds",
		Help:    "Time spent waiting for an endpoint in-flight slot.",
		Buckets: prometheus.ExponentialBuckets(0.001, 4, 10),
	}, []string{"endpoint"})
)

func ensureMetrics() {
	metricsOnce.Do(func() {
		for _, c := range []prometheus.Collector{requestsTotal, inflightGauge, failoverTotal, noCapableTotal, slotWaitSeconds} {
			if err := prometheus.Register(c); err != nil {
				var already prometheus.AlreadyRegisteredError
				if !errors.As(err, &already) {
					panic(err)
				}
			}
		}
	})
}
