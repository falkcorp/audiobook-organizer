// file: internal/metrics/metrics.go
// version: 1.6.0
// guid: 9f8e7d6c-5b4a-3210-9fed-cba876543210
// last-edited: 2026-08-22

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
	// searchIndexDocsGauge is the counterpart to booksGauge that TODO L3433
	// asked for: books_total was already exported, but nothing exported the
	// search index's own document count, so a divergence between the two
	// (e.g. the 2026-08-14 incident where 67,824 indexed docs sat against
	// 63,871 live books) had no dashboard signal. This is a raw Bleve
	// DocCount(), NOT a book count: it can exceed books_total when stale or
	// soft-deleted documents remain indexed, which is exactly the condition
	// this gauge exists to make visible when graphed against books_total.
	searchIndexDocsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "search_index_docs_total",
		Help:      "Current document count in the Bleve search index (DocCount) — counts index documents, not live books; may diverge from books_total when stale or soft-deleted documents remain indexed",
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

	// opItemsProcessed/opItemsTotal export per-operation *progress* while an op
	// is still running (OPS-5 op-stall detection). Unlike the started/completed/
	// failed/canceled counters above (count of ops by type, only incremented at
	// lifecycle edges), these gauges reflect the in-flight ProgressReporter.
	// UpdateProgress(current, total, ...) state so a wedged-but-still-"running"
	// op (e.g. the 2026-07-05 dedup.full-scan 3hr hang, or the 9hr Pebble
	// write-stall freeze — both only noticed by a human watching the UI) can be
	// alerted on via rate(op_items_processed) == 0 for a sustained window.
	//
	// Labeled by {op_id, op_type} rather than just {op_type} because the alert
	// needs to identify the *specific* wedged run, not just its type. This is
	// bounded cardinality in practice: only currently in-flight ops have a
	// series at all — ClearOpProgress deletes both series the instant an op
	// reaches a terminal state (see registry.publishOpTerminal), so historical
	// op_ids never accumulate.
	opItemsProcessed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "op_items_processed",
		Help:      "Current items-processed count for an in-flight operation, by op_id and op_type. Deleted once the op reaches a terminal state.",
	}, []string{"op_id", "op_type"})
	opItemsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "op_items_total",
		Help:      "Total items expected for an in-flight operation (0 if not yet known), by op_id and op_type. Deleted once the op reaches a terminal state.",
	}, []string{"op_id", "op_type"})

	// maintenanceResumeParamsFallback counts interrupted maintenance jobs that
	// resumed WITHOUT the operator's saved params — falling back to the job's
	// advertised dry_run default (see resumeLegacyOp in internal/server).
	// runMaintenanceJob persists resolved params on every enqueue since #2419,
	// so once pre-#2419 rows age out, ANY fire means a SaveParams silently
	// failed — that is the alert condition (C511). {job_id} is bounded by the
	// maintenance job registry; {reason} is load_error|no_saved_params.
	maintenanceResumeParamsFallback = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "maintenance_resume_params_fallback_total",
		Help:      "Interrupted maintenance jobs resumed on the advertised dry_run default because saved params were missing (no_saved_params) or unreadable (load_error). Any fire post-#2419-aging means a params save failed.",
	}, []string{"job_id", "reason"})

	// absListeningStatsReadFailures counts listening-stats read failures in the ABS
	// handler. The read gracefully reports 0 total time instead of 5xx, so this counter
	// makes the silent failure observable (ABS-N6). No labels: this counts one specific
	// read path, not a family of dimensions, so a plain Counter (not a CounterVec) is
	// the right shape — see booksGauge/foldersGauge above for the same plain-vs-vec
	// convention applied to Gauge.
	absListeningStatsReadFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "abs_listening_stats_read_failures_total",
		Help:      "Total count of ABS listening-stats read failures reported as 0 instead of error",
	})
)

// Register initializes metrics with the global Prometheus registry (idempotent)
func Register() {
	registerOnce.Do(func() {
		prometheus.MustRegister(operationStarted, operationCompleted, operationFailed, operationCanceled, operationDuration,
			booksGauge, searchIndexDocsGauge, foldersGauge, memoryAllocGauge, goroutinesGauge,
			cacheHits, cacheMisses, cacheSets, cacheInvalidations, cacheEvictions, cacheSize, cacheGetDuration,
			itunesLocationUnmappable, aiBackendAvailable,
			opItemsProcessed, opItemsTotal,
			maintenanceResumeParamsFallback,
			absListeningStatsReadFailures)
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
func SetBooks(n int) { booksGauge.Set(float64(n)) }

// SetSearchIndexDocs mirrors SetBooks for the search index's own document
// count (TODO L3433). Takes uint64 to match BleveIndex.DocCount()'s return
// type directly, with no lossy int conversion at the call site.
func SetSearchIndexDocs(n uint64) { searchIndexDocsGauge.Set(float64(n)) }
func SetFolders(n int)            { foldersGauge.Set(float64(n)) }
func SetMemoryAlloc(b uint64)     { memoryAllocGauge.Set(float64(b)) }
func SetGoroutines(n int)         { goroutinesGauge.Set(float64(n)) }

// SetOpProgress records the current/total items-processed progress for an
// in-flight operation (OPS-5 op-stall detection). Call on every
// ProgressReporter.UpdateProgress. opType should be the op's def_id (stable
// type identifier), not a free-form message, to keep cardinality bounded.
func SetOpProgress(opID, opType string, current, total int) {
	opItemsProcessed.WithLabelValues(opID, opType).Set(float64(current))
	opItemsTotal.WithLabelValues(opID, opType).Set(float64(total))
}

// ClearOpProgress deletes the per-op progress gauge series for opID/opType.
// Call exactly once when an operation reaches a terminal state (completed,
// failed, canceled, or interrupted) — without this, op_items_processed and
// op_items_total would accumulate one label-series per historical op_id
// forever (unbounded cardinality growth). Safe to call even if no series was
// ever set for this opID (no-op).
func ClearOpProgress(opID, opType string) {
	opItemsProcessed.DeleteLabelValues(opID, opType)
	opItemsTotal.DeleteLabelValues(opID, opType)
}

// RecordMaintenanceResumeParamsFallback counts an interrupted maintenance job
// that resumed without the operator's saved params, on the advertised dry_run
// default instead (C511). reason is a small enum: "no_saved_params" (LoadParams
// found nothing) or "load_error" (LoadParams failed).
func RecordMaintenanceResumeParamsFallback(jobID, reason string) {
	maintenanceResumeParamsFallback.WithLabelValues(jobID, reason).Inc()
}

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

// IncABSListeningStatsReadFailures counts a listening-stats read failure in the
// ABS handler (ABS-N6). The read gracefully reports 0 total time instead of 5xx.
func IncABSListeningStatsReadFailures() {
	absListeningStatsReadFailures.Inc()
}
