// file: internal/server/handlers/cache.go
// version: 2.3.0
// guid: c9d0e1f2-a3b4-5678-cdef-678901234567
// last-edited: 2026-08-11

package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
)

// CacheStatsResponse represents the JSON response for GET /api/v1/cache/stats.
type CacheStatsResponse struct {
	Caches      []CacheStat `json:"caches"`
	GeneratedAt string      `json:"generated_at"`
}

// CacheStat represents metrics for a single cache.
type CacheStat struct {
	Name              string            `json:"name"`
	Hits              int64             `json:"hits"`
	Misses            map[string]int64  `json:"misses"`
	Sets              int64             `json:"sets"`
	Invalidations     map[string]int64  `json:"invalidations"`
	Evictions         map[string]int64  `json:"evictions"`
	Size              int64             `json:"size"`
	HitRate           *float64          `json:"hit_rate,omitempty"`
	GetDurationMetric GetDurationMetric `json:"get_duration_seconds"`
}

// GetDurationMetric represents count and sum of cache get durations.
type GetDurationMetric struct {
	Count int64   `json:"count"`
	Sum   float64 `json:"sum"`
}

// MarshalJSON ensures proper JSON marshaling of CacheStatsResponse.
func (resp CacheStatsResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Caches      []CacheStat `json:"caches"`
		GeneratedAt string      `json:"generated_at"`
	}{
		Caches:      resp.Caches,
		GeneratedAt: resp.GeneratedAt,
	})
}

// CacheMetricsStore is the narrow interface CacheHandler requires for history persistence.
type CacheMetricsStore interface {
	GetCacheStatsHistory(cacheName string, since time.Time, limit int) ([]database.CacheStatsSnapshot, error)
}

// CacheMetadataStore is the narrow interface CacheHandler requires for DB-backed cache counts.
// CountPrefix("metadata_fetch_cache:") is equivalent to database.CountCachedMetadataFetches.
type CacheMetadataStore interface {
	CountPrefix(prefix string) (int64, error)
}

// CacheHandler handles all cache-related HTTP endpoints.
type CacheHandler struct {
	metricsStore  CacheMetricsStore
	metadataStore CacheMetadataStore
}

// NewCacheHandler creates a new CacheHandler.
// metadataStore may be nil if DB-backed cache size patching is not needed.
func NewCacheHandler(metricsStore CacheMetricsStore, metadataStore CacheMetadataStore) *CacheHandler {
	return &CacheHandler{metricsStore: metricsStore, metadataStore: metadataStore}
}

// HandleCacheStats returns aggregated cache metrics from Prometheus default registry.
// GET /api/v1/cache/stats (requires PermLibraryView — SEC-7)
func (h *CacheHandler) HandleCacheStats(c *gin.Context) {
	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to gather metrics")
		return
	}

	stats := aggregateCacheMetrics(metrics)

	// Patch DB-backed caches that have no in-memory size gauge.
	// metadata_fetch lives in PebbleDB; count its keys via prefix scan.
	if h.metadataStore != nil {
		if n, err := h.metadataStore.CountPrefix("metadata_fetch_cache:"); err == nil {
			for i := range stats {
				if stats[i].Name == "metadata_fetch" {
					stats[i].Size = n
					break
				}
			}
		}
	}

	resp := CacheStatsResponse{
		Caches:      stats,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	httputil.RespondWithOK(c, resp)
}

// HandleCacheKeysIntrospection returns key names for a specific cache.
// GET /api/v1/cache/stats/keys?cache=<name> (admin-gated)
func (h *CacheHandler) HandleCacheKeysIntrospection(c *gin.Context) {
	cacheName := c.Query("cache")
	if cacheName == "" {
		httputil.RespondWithBadRequest(c, "cache parameter required")
		return
	}

	// Check if it's a cache we can introspect
	cacheInst, ok := cache.Lookup(cacheName)
	if !ok {
		// Cache not found — check if it's a known non-introspectable name
		if IsNonIntrospectableCache(cacheName) {
			httputil.RespondWithBadRequest(c, "not introspectable: "+cacheName)
			return
		}
		// Unknown cache
		httputil.RespondWithBadRequest(c, "cache not found: "+cacheName)
		return
	}

	keys := cacheInst.Keys()
	httputil.RespondWithOK(c, struct {
		Cache string   `json:"cache"`
		Keys  []string `json:"keys"`
		Count int      `json:"count"`
	}{Cache: cacheName, Keys: keys, Count: len(keys)})
}

// CacheInvalidateRequest is the optional body of POST /api/v1/cache/invalidate.
// An absent or empty Cache field means "every registered cache".
type CacheInvalidateRequest struct {
	Cache string `json:"cache"`
}

// CacheInvalidateResponse reports what was actually dropped, per cache.
//
// Dropped is returned rather than a bare 200 because the operator reaching for
// this endpoint is trying to confirm that a specific stale entry is gone; "OK"
// does not distinguish "cleared 2000 entries" from "cleared a cache that was
// already empty because you named the wrong one".
type CacheInvalidateResponse struct {
	Dropped map[string]int `json:"dropped"`
	Total   int            `json:"total"`
	Skipped []string       `json:"skipped,omitempty"`
}

// HandleCacheInvalidate empties one or all in-memory caches.
// POST /api/v1/cache/invalidate (admin-gated)
//
// This is the operator lever for cache staleness. Before it existed the only
// remedy was a process restart, which costs roughly ten minutes of unusable
// library while memdb warms back up.
//
// Generation keying on the library-list caches does not make this redundant:
// the counter only advances on the store's three book-level writes, so batch
// writes, direct memdb writes and index-only edits still bypass it entirely.
// This endpoint is what covers those.
func (h *CacheHandler) HandleCacheInvalidate(c *gin.Context) {
	var req CacheInvalidateRequest
	// A completely absent body is valid and means "all caches"; only a
	// malformed one is an error.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	if req.Cache != "" {
		dropped, found, invalidatable := cache.InvalidateByName(req.Cache)
		if !found {
			if IsNonIntrospectableCache(req.Cache) {
				httputil.RespondWithBadRequest(c, "not invalidatable: "+req.Cache)
				return
			}
			httputil.RespondWithBadRequest(c, "cache not found: "+req.Cache)
			return
		}
		if !invalidatable {
			httputil.RespondWithBadRequest(c, "not invalidatable: "+req.Cache)
			return
		}
		slog.Info("cache invalidated by operator", "cache", req.Cache, "dropped", dropped)
		httputil.RespondWithOK(c, CacheInvalidateResponse{
			Dropped: map[string]int{req.Cache: dropped},
			Total:   dropped,
		})
		return
	}

	dropped, skipped := cache.InvalidateAllRegistered()
	total := 0
	for _, n := range dropped {
		total += n
	}
	slog.Info("all caches invalidated by operator",
		"caches", len(dropped), "dropped", total, "skipped", skipped)
	httputil.RespondWithOK(c, CacheInvalidateResponse{
		Dropped: dropped,
		Total:   total,
		Skipped: skipped,
	})
}

// HandleCacheStatsHistory returns persisted snapshots for one or all caches.
// GET /api/v1/cache/stats/history?cache=<name>&since=<RFC3339>&limit=<int>
//
// `since` defaults to 24h ago; `limit` defaults to 0 (no cap).
func (h *CacheHandler) HandleCacheStatsHistory(c *gin.Context) {
	if h.metricsStore == nil {
		httputil.RespondWithServiceUnavailable(c, "metrics store not initialized")
		return
	}
	cacheName := c.Query("cache")
	since := time.Now().Add(-24 * time.Hour)
	if raw := c.Query("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httputil.RespondWithBadRequest(c, "since must be RFC3339")
			return
		}
		since = t
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httputil.RespondWithBadRequest(c, "limit must be a non-negative integer")
			return
		}
		limit = n
	}
	snaps, err := h.metricsStore.GetCacheStatsHistory(cacheName, since, limit)
	if err != nil {
		httputil.InternalError(c, "failed to get cache stats history", err)
		return
	}
	httputil.RespondWithOK(c, struct {
		Cache     string `json:"cache"`
		Since     string `json:"since"`
		Snapshots any    `json:"snapshots"`
	}{Cache: cacheName, Since: since.UTC().Format(time.RFC3339), Snapshots: snaps})
}

// AggregateCacheMetrics is the exported wrapper around aggregateCacheMetrics,
// used by server-level helpers (e.g. runCacheStatsSnapshotter) outside this package.
func AggregateCacheMetrics(mfs []*io_prometheus_client.MetricFamily) []CacheStat {
	return aggregateCacheMetrics(mfs)
}

// aggregateCacheMetrics extracts all audiobook_organizer_cache_* metrics from Prometheus
// and builds a CacheStat for each unique cache name.
func aggregateCacheMetrics(mfs []*io_prometheus_client.MetricFamily) []CacheStat {
	// Map from cache name to its aggregated stats
	statMap := make(map[string]*CacheStat)

	for _, mf := range mfs {
		// Only process cache metrics
		if mf.Name == nil || *mf.Name == "" {
			continue
		}
		metricName := *mf.Name

		switch metricName {
		case "audiobook_organizer_cache_hits_total":
			processCounterMetric(mf, statMap, func(stat *CacheStat, value int64) {
				stat.Hits = value
			})

		case "audiobook_organizer_cache_misses_total":
			processCounterMetricWithReason(mf, statMap, func(stat *CacheStat, reason string, value int64) {
				if stat.Misses == nil {
					stat.Misses = make(map[string]int64)
				}
				stat.Misses[reason] = value
			})

		case "audiobook_organizer_cache_sets_total":
			processCounterMetric(mf, statMap, func(stat *CacheStat, value int64) {
				stat.Sets = value
			})

		case "audiobook_organizer_cache_invalidations_total":
			processCounterMetricWithReason(mf, statMap, func(stat *CacheStat, scope string, value int64) {
				if stat.Invalidations == nil {
					stat.Invalidations = make(map[string]int64)
				}
				stat.Invalidations[scope] = value
			})

		case "audiobook_organizer_cache_evictions_total":
			processCounterMetricWithReason(mf, statMap, func(stat *CacheStat, reason string, value int64) {
				if stat.Evictions == nil {
					stat.Evictions = make(map[string]int64)
				}
				stat.Evictions[reason] = value
			})

		case "audiobook_organizer_cache_size":
			processGaugeMetric(mf, statMap, func(stat *CacheStat, value float64) {
				stat.Size = int64(value)
			})

		case "audiobook_organizer_cache_get_duration_seconds":
			processHistogramMetric(mf, statMap, func(stat *CacheStat, count int64, sum float64) {
				stat.GetDurationMetric = GetDurationMetric{Count: count, Sum: sum}
			})
		}
	}

	// Convert to sorted slice and compute hit rates
	result := make([]CacheStat, 0, len(statMap))
	for _, stat := range statMap {
		// Compute hit_rate = hits / (hits + sum(misses))
		totalMisses := int64(0)
		if stat.Misses != nil {
			for _, count := range stat.Misses {
				totalMisses += count
			}
		}
		denominator := stat.Hits + totalMisses
		if denominator > 0 {
			rate := float64(stat.Hits) / float64(denominator)
			stat.HitRate = &rate
		}

		result = append(result, *stat)
	}

	// Sort by cache name for stable output
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].Name > result[j].Name {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	return result
}

// processCounterMetric extracts counter metrics with a single {cache} label.
func processCounterMetric(mf *io_prometheus_client.MetricFamily,
	statMap map[string]*CacheStat,
	fn func(*CacheStat, int64)) {
	if mf.Metric == nil {
		return
	}
	for _, m := range mf.Metric {
		cacheName := getLabelValue(m.Label, "cache")
		if cacheName == "" {
			continue
		}
		if _, ok := statMap[cacheName]; !ok {
			statMap[cacheName] = &CacheStat{Name: cacheName}
		}
		if m.Counter != nil && m.Counter.Value != nil {
			fn(statMap[cacheName], int64(*m.Counter.Value))
		}
	}
}

// processCounterMetricWithReason extracts counter metrics with {cache} and {reason/scope} labels.
func processCounterMetricWithReason(mf *io_prometheus_client.MetricFamily,
	statMap map[string]*CacheStat,
	fn func(*CacheStat, string, int64)) {
	if mf.Metric == nil {
		return
	}
	for _, m := range mf.Metric {
		cacheName := getLabelValue(m.Label, "cache")
		if cacheName == "" {
			continue
		}
		// Try both "reason" and "scope" label names
		reason := getLabelValue(m.Label, "reason")
		if reason == "" {
			reason = getLabelValue(m.Label, "scope")
		}
		if reason == "" {
			continue
		}
		if _, ok := statMap[cacheName]; !ok {
			statMap[cacheName] = &CacheStat{Name: cacheName}
		}
		if m.Counter != nil && m.Counter.Value != nil {
			fn(statMap[cacheName], reason, int64(*m.Counter.Value))
		}
	}
}

// processGaugeMetric extracts gauge metrics with a single {cache} label.
func processGaugeMetric(mf *io_prometheus_client.MetricFamily,
	statMap map[string]*CacheStat,
	fn func(*CacheStat, float64)) {
	if mf.Metric == nil {
		return
	}
	for _, m := range mf.Metric {
		cacheName := getLabelValue(m.Label, "cache")
		if cacheName == "" {
			continue
		}
		if _, ok := statMap[cacheName]; !ok {
			statMap[cacheName] = &CacheStat{Name: cacheName}
		}
		if m.Gauge != nil && m.Gauge.Value != nil {
			fn(statMap[cacheName], *m.Gauge.Value)
		}
	}
}

// processHistogramMetric extracts histogram bucket data, returning count and sum.
func processHistogramMetric(mf *io_prometheus_client.MetricFamily,
	statMap map[string]*CacheStat,
	fn func(*CacheStat, int64, float64)) {
	if mf.Metric == nil {
		return
	}
	for _, m := range mf.Metric {
		cacheName := getLabelValue(m.Label, "cache")
		if cacheName == "" {
			continue
		}
		if _, ok := statMap[cacheName]; !ok {
			statMap[cacheName] = &CacheStat{Name: cacheName}
		}
		if m.Histogram != nil {
			var count int64
			var sum float64
			if m.Histogram.SampleCount != nil {
				count = int64(*m.Histogram.SampleCount)
			}
			if m.Histogram.SampleSum != nil {
				sum = *m.Histogram.SampleSum
			}
			fn(statMap[cacheName], count, sum)
		}
	}
}

// getLabelValue finds a label value by name in a list of label pairs.
func getLabelValue(labels []*io_prometheus_client.LabelPair, name string) string {
	for _, lp := range labels {
		if lp.Name != nil && *lp.Name == name && lp.Value != nil {
			return *lp.Value
		}
	}
	return ""
}

// IsNonIntrospectableCache returns true if the cache name is a known non-introspectable cache
// (i.e., backed by a database or external service, not an in-memory *Cache[T]).
func IsNonIntrospectableCache(name string) bool {
	nonIntrospectable := map[string]bool{
		"metadata_fetch": true,
		"embedding":      true,
		// Add other DB-backed or external caches here
	}
	return nonIntrospectable[name]
}
