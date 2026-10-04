// file: internal/server/handlers/cache_census_test.go
// version: 1.0.0
// guid: c670ed15-3169-4e19-8d06-5680b094bc8d
// last-edited: 2026-10-04

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

func metadataFetchStat(t *testing.T, h *CacheHandler) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/cache/stats", nil)
	h.HandleCacheStats(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var envelope map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	data := envelope
	if d, ok := envelope["data"].(map[string]any); ok {
		data = d
	}
	for _, raw := range data["caches"].([]any) {
		if m := raw.(map[string]any); m["name"] == "metadata_fetch" {
			return m
		}
	}
	t.Fatal("no metadata_fetch cache in the response")
	return nil
}

func TestHandleCacheStats_MetadataFetchSizeFromCensus(t *testing.T) {
	metrics.Register()
	metrics.RecordCacheSet("metadata_fetch") // makes the cache appear in the gather
	p := newCensusHandlerStore(t)
	for i := 0; i < 50; i++ {
		putFetchCacheRow(t, p, i, time.Now())
	}

	stat := metadataFetchStat(t, NewCacheHandler(nil, p))
	require.Equal(t, true, stat["size_estimated"])
	require.Greater(t, stat["size"], float64(0))
}

// A store without a census keeps the exact CountPrefix path and does not claim
// an estimate.
func TestHandleCacheStats_FallsBackToCountPrefix(t *testing.T) {
	metrics.Register()
	metrics.RecordCacheSet("metadata_fetch")
	stat := metadataFetchStat(t, NewCacheHandler(nil, countOnlyStore{n: 17}))
	require.EqualValues(t, 17, stat["size"])
	_, present := stat["size_estimated"]
	require.False(t, present, "size_estimated is omitted when false")
}

type countOnlyStore struct{ n int64 }

func (s countOnlyStore) CountPrefix(string) (int64, error) { return s.n, nil }
