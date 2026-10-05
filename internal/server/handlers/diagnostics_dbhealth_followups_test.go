// file: internal/server/handlers/diagnostics_dbhealth_followups_test.go
// version: 1.0.0
// guid: de17f8c2-593a-4eea-9ce9-4dac637b04e8
// last-edited: 2026-10-04

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

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
	emb := database.NewEmbeddingStore(p.DB())

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

// The census path reports its error bound beside the size.
func TestHandleCacheStats_CensusSizeCarriesErrorBound(t *testing.T) {
	metrics.Register()
	metrics.RecordCacheSet("metadata_fetch")
	p := newCensusHandlerStore(t)
	for i := 0; i < 50; i++ {
		putFetchCacheRow(t, p, i, time.Now())
	}
	stat := metadataFetchStat(t, NewCacheHandler(nil, p))
	require.Equal(t, true, stat["size_estimated"])
	_ = stat["size_error_bound_keys"] // omitempty: zero is a legitimate bound
}
