// file: internal/server/handlers/diagnostics_dbhealth_census_test.go
// version: 1.1.0
// guid: 637c4ea3-5b82-4c17-88fb-4256b6233cd3
// last-edited: 2026-10-04

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fetchCachePad makes each row ~24 KB so a few dozen rows pass the census's
// 1 MiB unflushed-bytes gate and the census flushes (and so sees them).
var fetchCachePad = strings.Repeat("x", 24<<10)

func putFetchCacheRow(t *testing.T, p *database.PebbleStore, i int, cachedAt time.Time) {
	t.Helper()
	b, err := json.Marshal(database.CachedMetadataEntry{
		BookID: fmt.Sprintf("book%d", i), Source: "test", CachedAt: cachedAt,
		Extra: map[string]string{"pad": fetchCachePad},
	})
	require.NoError(t, err)
	require.NoError(t, p.SetRaw(fmt.Sprintf("metadata_fetch_cache:book%d:test", i), b))
}

func TestGetDBHealth_UsesCensusAndSkipsDecode(t *testing.T) {
	p := newCensusHandlerStore(t)
	for i := 0; i < 50; i++ {
		putFetchCacheRow(t, p, i, time.Now())
	}

	payload := dbHealthPayload(t, callDBHealth(t, p))

	pebble := payload["pebble"].(map[string]any)
	require.Equal(t, true, pebble["estimated"])
	mc := payload["metadata_cache"].(map[string]any)
	require.Equal(t, true, mc["estimated"])
	require.EqualValues(t, -1, mc["expired_entries"])
	require.Equal(t, false, mc["expired_entries_computed"])
	require.Greater(t, mc["total_entries"], float64(0))
}

func TestGetDBHealth_DeepCountsExpired(t *testing.T) {
	prev := config.AppConfig.MetadataFetchCacheTTLDays
	config.AppConfig.MetadataFetchCacheTTLDays = 30
	t.Cleanup(func() { config.AppConfig.MetadataFetchCacheTTLDays = prev })

	p := newCensusHandlerStore(t)
	old := time.Now().Add(-90 * 24 * time.Hour)
	for i := 0; i < 3; i++ {
		putFetchCacheRow(t, p, i, old)
	}
	for i := 3; i < 5; i++ {
		putFetchCacheRow(t, p, i, time.Now())
	}

	mc := dbHealthPayload(t, callDBHealthQuery(t, p, "deep=true"))["metadata_cache"].(map[string]any)
	require.EqualValues(t, 3, mc["expired_entries"])
	require.Equal(t, true, mc["expired_entries_computed"])
}

// The streaming counter must cross page boundaries (page size 1000).
func TestCountExpiredMetadataFetches_PagesThroughTheFamily(t *testing.T) {
	p := newCensusHandlerStore(t)
	old := time.Now().Add(-90 * 24 * time.Hour)
	for i := 0; i < 2500; i++ {
		putFetchCacheRow(t, p, i, old)
	}
	n, err := countExpiredMetadataFetches(context.Background(), p, time.Now().Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 2500, n)
}
