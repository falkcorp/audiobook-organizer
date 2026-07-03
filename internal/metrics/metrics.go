// file: internal/metrics/metrics.go
// version: 1.3.0
// guid: 9f8e7d6c-5b4a-3210-9fed-cba876543210
// last-edited: 2026-07-03

package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	registerOnce sync.Once

	operationStarted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "operations_started_total",
		Help:      "Total number of operations started by type",
	}, []string{"type"})
	operationCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "operations_completed_total",
		Help:      "Total number of operations successfully completed by type",
	}, []string{"type"})
	operationFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "operations_failed_total",
		Help:      "Total number of operations failed by type",
	}, []string{"type"})
	operationCanceled = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "operations_canceled_total",
		Help:      "Total number of operations canceled by type",
	}, []string{"type"})
	operationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "audiobook_organizer",
		Name:      "operation_duration_seconds",
		Help:      "Histogram of operation durations in seconds by type",
		Buckets:   prometheus.ExponentialBuckets(0.05, 1.6, 10), // ~50ms up to several seconds/minutes
	}, []string{"type"})

	booksGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "books_total",
		Help:      "Current total number of books in library",
	})
	foldersGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "import_paths_total",
		Help:      "Current total number of enabled import paths",
	})
	memoryAllocGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "process_memory_alloc_bytes",
		Help:      "Current process memory allocation (runtime.Alloc)",
	})
	goroutinesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "process_goroutines",
		Help:      "Number of currently running goroutines",
	})

	// Cache metrics. The {cache} label is a small enum of cache instance names
	// (dashboard, dedup, list, book, ai_response, metadata_fetch, embedding, ...).
	// Never label by cache key — that would explode cardinality.
	cacheHits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "cache_hits_total",
		Help:      "Total cache hits per cache instance",
	}, []string{"cache"})
	cacheMisses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "cache_misses_total",
		Help:      "Total cache misses per cache instance, partitioned by reason (not_found|expired)",
	}, []string{"cache", "reason"})
	cacheSets = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "cache_sets_total",
		Help:      "Total cache writes per cache instance",
	}, []string{"cache"})
	cacheInvalidations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "cache_invalidations_total",
		Help:      "Total explicit cache invalidations per cache instance, partitioned by scope (key|all)",
	}, []string{"cache", "scope"})
	cacheEvictions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "cache_evictions_total",
		Help:      "Total cache evictions per cache instance, partitioned by reason (expired|capacity)",
	}, []string{"cache", "reason"})
	cacheSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "cache_size",
		Help:      "Current number of entries per cache instance (includes expired-but-not-yet-evicted)",
	}, []string{"cache"})
	cacheGetDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "audiobook_organizer",
		Name:      "cache_get_duration_seconds",
		Help:      "Histogram of cache Get latencies in seconds per cache instance",
		Buckets:   prometheus.ExponentialBuckets(0.0000005, 4, 10), // 500ns up to ~130ms
	}, []string{"cache"})

	// itunesLocationUnmappable counts iTunes writeback location values that could
	// NOT be normalized into a valid 0x0B/0x0D LocationPair and were therefore
	// SKIPPED (never written raw — CRIT-2). The {reason} label is a small enum
	// (url_unmappable|invalid_path), never the path itself (cardinality). A
	// nonzero value here is an actionable data-quality signal: stale URL-shaped or
	// staging-dir f.ITunesPath rows that writeback refused to touch.
	itunesLocationUnmappable = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "itunes_location_unmappable_total",
		Help:      "Total iTunes writeback location values skipped because they could not be normalized into a valid 0x0B/0x0D pair (CRIT-2)",
	}, []string{"reason"})

	// aiBackendAvailable exports the reachability of AI backends (e.g. Ollama)
	// so it can be alerted on (OPS-4). Values are 0/1, set at server-init time
	// from the same signal that already feeds EmbeddingClient.SetOllamaAvailable.
	aiBackendAvailable = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "ai_backend_available",
		Help:      "1 if the named AI backend was reachable/available at last check, else 0",
	}, []string{"backend"})
)

// Register initializes metrics with the global Prometheus registry (idempotent)
func Register() {
	registerOnce.Do(func() {
		prometheus.MustRegister(operationStarted, operationCompleted, operationFailed, operationCanceled, operationDuration,
			booksGauge, foldersGauge, memoryAllocGauge, goroutinesGauge,
			cacheHits, cacheMisses, cacheSets, cacheInvalidations, cacheEvictions, cacheSize, cacheGetDuration,
			itunesLocationUnmappable, aiBackendAvailable)
	})
}

// RecordITunesLocationUnmappable counts a writeback location value that could not
// be normalized into a valid 0x0B/0x0D pair and was skipped (CRIT-2 / TASK-006).
// reason is a small enum: "url_unmappable" or "invalid_path".
func RecordITunesLocationUnmappable(reason string) {
	itunesLocationUnmappable.WithLabelValues(reason).Inc()
}

// SetBackendAvailable records whether the named AI backend (e.g. "ollama")
// was reachable/available at last check. Purely additive to the existing
// in-memory EmbeddingClient.SetOllamaAvailable signal (OPS-4).
func SetBackendAvailable(backend string, ok bool) {
	v := 0.0
	if ok {
		v = 1.0
	}
	aiBackendAvailable.WithLabelValues(backend).Set(v)
}

// Operation lifecycle helpers
func IncOperationStarted(opType string)   { operationStarted.WithLabelValues(opType).Inc() }
func IncOperationCompleted(opType string) { operationCompleted.WithLabelValues(opType).Inc() }
func IncOperationFailed(opType string)    { operationFailed.WithLabelValues(opType).Inc() }
func IncOperationCanceled(opType string)  { operationCanceled.WithLabelValues(opType).Inc() }
func ObserveOperationDuration(opType string, d time.Duration) {
	operationDuration.WithLabelValues(opType).Observe(d.Seconds())
}

// Gauges
func SetBooks(n int)          { booksGauge.Set(float64(n)) }
func SetFolders(n int)        { foldersGauge.Set(float64(n)) }
func SetMemoryAlloc(b uint64) { memoryAllocGauge.Set(float64(b)) }
func SetGoroutines(n int)     { goroutinesGauge.Set(float64(n)) }

// Cache helpers
func RecordCacheHit(cache string)          { cacheHits.WithLabelValues(cache).Inc() }
func RecordCacheMiss(cache, reason string) { cacheMisses.WithLabelValues(cache, reason).Inc() }
func RecordCacheSet(cache string)          { cacheSets.WithLabelValues(cache).Inc() }
func RecordCacheInvalidation(cache, scope string) {
	cacheInvalidations.WithLabelValues(cache, scope).Inc()
}
func RecordCacheEviction(cache, reason string) { cacheEvictions.WithLabelValues(cache, reason).Inc() }
func SetCacheSize(cache string, n int)         { cacheSize.WithLabelValues(cache).Set(float64(n)) }
func ObserveCacheGetDuration(cache string, d time.Duration) {
	cacheGetDuration.WithLabelValues(cache).Observe(d.Seconds())
}
