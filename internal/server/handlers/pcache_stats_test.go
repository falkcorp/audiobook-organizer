// file: internal/server/handlers/pcache_stats_test.go
// version: 1.0.0
// guid: 53fdb1b5-7be2-452b-a745-a14b47a44e70
// last-edited: 2026-10-11

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pcacheFixtureStore holds three question rows (one an Audible empty answer
// past its 14-day negative TTL), one product row and one pointer row.
func pcacheFixtureStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	prevMF, prevTTL := config.AppConfig.MetadataFetch, config.AppConfig.MetadataFetchCacheTTLDays
	t.Cleanup(func() {
		config.AppConfig.MetadataFetch, config.AppConfig.MetadataFetchCacheTTLDays = prevMF, prevTTL
	})
	config.AppConfig.MetadataFetch = config.DefaultMetadataFetchConfig()
	config.AppConfig.MetadataFetchCacheTTLDays = 180

	p := newCensusHandlerStore(t)
	now := time.Now()
	put := func(key string, empty bool, age time.Duration) {
		b, err := json.Marshal(database.PCacheEntry{Provider: "audible", AskedAt: now.Add(-age), Empty: empty, Results: json.RawMessage(`[]`)})
		require.NoError(t, err)
		require.NoError(t, p.SetRaw(key, b))
	}
	put("pcache:q:audible:search_title:a", false, time.Hour)
	put("pcache:q:audible:search_title:b", true, 24*time.Hour)
	put("pcache:q:audible:search_title:c", true, 20*24*time.Hour) // expired
	put("pcache:asin:audible:B0SYNTH001", false, time.Hour)
	require.NoError(t, database.RecordPCachePointer(p, "book-01", map[string][]string{"audible": {"pcache:q:audible:search_title:a"}}, now))
	return p
}

func providerAnswerStat(t *testing.T, h *CacheHandler, rawQuery string) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/cache/stats?"+rawQuery, nil)
	h.HandleCacheStats(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	data := envelope
	if d, ok := envelope["data"].(map[string]any); ok {
		data = d
	}
	for _, raw := range data["caches"].([]any) {
		if m := raw.(map[string]any); m["name"] == providerAnswerCacheName {
			return m
		}
	}
	t.Fatal("no provider_answer cache in the response")
	return nil
}

// The provider_answer row is present (even before any counter fires) with
// the family's row count; ?deep=true adds the expired-row count.
func TestHandleCacheStats_ProviderAnswerCounts(t *testing.T) {
	h := NewCacheHandler(nil, pcacheFixtureStore(t))
	stat := providerAnswerStat(t, h, "")
	_, hasExpired := stat["expired"]
	require.False(t, hasExpired, "the expired count needs ?deep=true")

	stat = providerAnswerStat(t, h, "deep=true")
	require.EqualValues(t, 1, stat["expired"], "one Audible empty answer is past its negative TTL")
}

func TestHandleCacheStats_ProviderAnswerSizeWithoutCensus(t *testing.T) {
	stat := providerAnswerStat(t, NewCacheHandler(nil, countOnlyStore{n: 17}), "deep=true")
	require.EqualValues(t, 17, stat["size"])
	require.NotEmpty(t, stat["expired_error"], "a store that cannot page says so instead of a count")
}

func TestGetDBHealth_ProviderAnswerCache(t *testing.T) {
	p := pcacheFixtureStore(t)
	pc := dbHealthPayload(t, callDBHealth(t, p))["provider_answer_cache"].(map[string]any)
	require.EqualValues(t, -1, pc["expired_entries"])
	require.Equal(t, false, pc["expired_entries_computed"])

	pc = dbHealthPayload(t, callDBHealthQuery(t, p, "deep=true"))["provider_answer_cache"].(map[string]any)
	require.Equal(t, true, pc["expired_entries_computed"])
	require.EqualValues(t, 1, pc["expired_entries"])
	require.EqualValues(t, 3, pc["questions"])
	require.EqualValues(t, 1, pc["products"])
	require.EqualValues(t, 1, pc["pointers"])
}
