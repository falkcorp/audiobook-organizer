// file: internal/server/handlers/diagnostics_dbhealth_followups_test.go
// version: 1.2.0
// guid: de17f8c2-593a-4eea-9ce9-4dac637b04e8
// last-edited: 2026-10-04

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// The embedding section reads the census emb:v: family; it must not walk the
// vectors, and it reports an error bound beside the estimate.
func TestGetDBHealth_EmbeddingsFromCensus(t *testing.T) {
	p := newCensusHandlerStore(t)
	pad := []byte(strings.Repeat("x", 24<<10))
	for i := 0; i < 60; i++ {
		require.NoError(t, p.SetRaw(fmt.Sprintf("emb:v:book:%d", i), pad))
	}
	// The embedding store sits on its OWN empty DB, so a walk through it would
	// count 0 vectors; a positive vector_count can only come from the census
	// of the main store.
	other := newCensusHandlerStore(t)
	emb := database.NewEmbeddingStore(other.DB())
	walked, err := emb.CountByType("book")
	require.NoError(t, err)
	require.Zero(t, walked)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/db-health", nil)
	NewDiagnosticsHandler(p, nil, emb, nil, nil).GetDBHealth(c)
	require.Equal(t, http.StatusOK, w.Code)

	payload := dbHealthPayload(t, w.Body.Bytes())
	e := payload["embeddings"].(map[string]any)
	require.Equal(t, true, e["estimated"])
	require.Greater(t, e["vector_count"], float64(0))
	require.Contains(t, e, "error_bound_keys")
	require.Contains(t, payload["pebble"].(map[string]any), "error_bound_keys")
	require.Contains(t, payload["metadata_cache"].(map[string]any), "error_bound_keys")
}

// countingStore records CountPrefix calls.
type countingStore struct{ calls int }

func (s *countingStore) CountPrefix(string) (int64, error) { s.calls++; return 9, nil }

// A cancelled request must not start the full-walk fallback count.
func TestHandleCacheStats_FallbackCountSkippedOnCancelledContext(t *testing.T) {
	metrics.Register()
	metrics.RecordCacheSet("metadata_fetch")
	cs := &countingStore{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/cache/stats", nil).WithContext(ctx)
	NewCacheHandler(nil, cs).HandleCacheStats(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Zero(t, cs.calls)
}

// censusMetaStub serves a fixed census family with an error bound.
type censusMetaStub struct{ countingStore }

func (*censusMetaStub) DBCensus(context.Context, database.CensusOptions) (*database.DBCensus, error) {
	return &database.DBCensus{Families: []database.FamilyCensus{
		{Prefix: "metadata_fetch_cache:", Keys: 5, ErrorBoundKeys: 17},
	}}, nil
}

// The census path reports its error bound beside the size.
func TestHandleCacheStats_CensusSizeCarriesErrorBound(t *testing.T) {
	metrics.Register()
	metrics.RecordCacheSet("metadata_fetch")
	stub := &censusMetaStub{}
	stat := metadataFetchStat(t, NewCacheHandler(nil, stub))
	require.Equal(t, true, stat["size_estimated"])
	require.EqualValues(t, 5, stat["size"])
	require.EqualValues(t, 17, stat["size_error_bound_keys"])
	require.Zero(t, stub.calls, "census path must not run the CountPrefix walk")
}

// walkSpyStore counts deep walks (first-page reads) and can fail or stall.
type walkSpyStore struct {
	database.RawKVStore
	walks   atomic.Int32
	delay   time.Duration
	err     error
	forever bool
}

func (s *walkSpyStore) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	if after == "" {
		s.walks.Add(1)
		time.Sleep(s.delay)
	}
	if s.err != nil {
		return nil, "", s.err
	}
	if s.forever {
		return nil, "more", nil
	}
	return nil, "", nil
}

func TestExpiredCounter_ConcurrentCallsShareOneWalk(t *testing.T) {
	spy := &walkSpyStore{delay: 300 * time.Millisecond}
	var ec expiredCounter
	var wg sync.WaitGroup
	// require.* calls t.FailNow, which must run on the test goroutine; the
	// workers record their errors and the test asserts after wg.Wait().
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ec.count(context.Background(), spy, 30)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "caller %d", i)
	}
	require.EqualValues(t, 1, spy.walks.Load())

	// A finished count is served from the cache for a while: no second walk.
	_, err := ec.count(context.Background(), spy, 30)
	require.NoError(t, err)
	require.EqualValues(t, 1, spy.walks.Load())

	// A different TTL is a different question.
	_, err = ec.count(context.Background(), spy, 7)
	require.NoError(t, err)
	require.EqualValues(t, 2, spy.walks.Load())
}

// ctx is checked between pages.
func TestCountExpiredMetadataFetches_StopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spy := &walkSpyStore{forever: true}
	_, pages, err := countExpiredMetadataFetches(ctx, spy, time.Now())
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, spy.walks.Load())
	require.Zero(t, pages)
}

// cancelAfterFirstPageStore cancels the walk's context while serving page 1
// and always reports another page, so only the between-pages ctx check can
// stop the walk.
type cancelAfterFirstPageStore struct {
	database.RawKVStore
	cancel context.CancelFunc
	walks  atomic.Int32
	reads  atomic.Int32
}

func (s *cancelAfterFirstPageStore) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	if after == "" {
		s.walks.Add(1)
	}
	if s.reads.Add(1) == 1 {
		s.cancel()
	}
	return nil, "more", nil
}

// A cancel that lands mid-walk stops it at the next page boundary.
func TestCountExpiredMetadataFetches_ChecksBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &cancelAfterFirstPageStore{cancel: cancel}
	_, pages, err := countExpiredMetadataFetches(ctx, spy, time.Now())
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, spy.walks.Load())
	require.EqualValues(t, 1, spy.reads.Load(), "no page may be read after the cancel")
	require.Equal(t, 1, pages)
}

// A walk that hits the ceiling says so, with the pages it got through, and
// still matches context.DeadlineExceeded.
func TestExpiredCounter_TimeoutReportsPagesWalked(t *testing.T) {
	spy := &walkSpyStore{forever: true}
	ec := expiredCounter{timeout: 50 * time.Millisecond}
	res, err := ec.count(context.Background(), spy, 30)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "timed out after 50ms, ")
	require.Contains(t, err.Error(), fmt.Sprintf("%d pages walked", res.Pages))
	require.NotContains(t, err.Error(), "context deadline exceeded")
	require.Positive(t, res.Pages)
	require.GreaterOrEqual(t, res.Elapsed, 50*time.Millisecond)
}

func TestShortDuration(t *testing.T) {
	require.Equal(t, "2m", shortDuration(2*time.Minute))
	require.Equal(t, "1m30s", shortDuration(90*time.Second))
	require.Equal(t, "1h", shortDuration(time.Hour))
	require.Equal(t, "50ms", shortDuration(50*time.Millisecond))
}

// A completed deep count reports how long its walk took and how many pages it
// read.
func TestGetDBHealth_DeepReportsWalkMeasurements(t *testing.T) {
	prev := config.AppConfig.MetadataFetchCacheTTLDays
	config.AppConfig.MetadataFetchCacheTTLDays = 30
	t.Cleanup(func() { config.AppConfig.MetadataFetchCacheTTLDays = prev })

	spy := &walkSpyStore{delay: 20 * time.Millisecond}
	store := deepSpyStore{spy: spy}
	mc := dbHealthPayload(t, callDBHealthQuery(t, store, "deep=true"))["metadata_cache"].(map[string]any)
	require.Equal(t, true, mc["expired_entries_computed"])
	require.EqualValues(t, 1, mc["expired_entries_pages_walked"])
	require.GreaterOrEqual(t, mc["expired_entries_elapsed_ms"], float64(20))
}

// A caller that disconnects gets its own ctx error, and the shared walk still
// finishes for the cache.
func TestExpiredCounter_CallerCancelDoesNotAbortSharedWalk(t *testing.T) {
	spy := &walkSpyStore{delay: 200 * time.Millisecond}
	var ec expiredCounter
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ec.count(ctx, spy, 30)
	require.ErrorIs(t, err, context.Canceled)

	require.Eventually(t, func() bool {
		ec.mu.Lock()
		defer ec.mu.Unlock()
		return ec.last.valid
	}, 5*time.Second, 20*time.Millisecond)
}

// A failed deep count reaches the client as expired_entries_error.
func TestGetDBHealth_DeepFailureReturnsError(t *testing.T) {
	prev := config.AppConfig.MetadataFetchCacheTTLDays
	config.AppConfig.MetadataFetchCacheTTLDays = 30
	t.Cleanup(func() { config.AppConfig.MetadataFetchCacheTTLDays = prev })

	spy := &walkSpyStore{err: errors.New("disk on fire")}
	store := deepSpyStore{spy: spy}
	mc := dbHealthPayload(t, callDBHealthQuery(t, store, "deep=true"))["metadata_cache"].(map[string]any)
	require.Equal(t, false, mc["expired_entries_computed"])
	require.Contains(t, mc["expired_entries_error"], "disk on fire")
	require.EqualValues(t, -1, mc["expired_entries"])
}

// deepSpyStore is the db-health stub with ScanPrefixPage routed to a spy.
type deepSpyStore struct {
	dbHealthStoreStub
	spy *walkSpyStore
}

func (s deepSpyStore) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	return s.spy.ScanPrefixPage(prefix, after, limit)
}
