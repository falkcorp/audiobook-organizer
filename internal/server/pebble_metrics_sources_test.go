// file: internal/server/pebble_metrics_sources_test.go
// version: 1.1.0
// guid: 4af3bbc9-4f8d-4fe4-9da3-1c7942a13084
// last-edited: 2026-10-04

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMetricsScrape_ExportsPebbleSeriesForMainStore scrapes /metrics through a
// real NewServer (the production wiring path, not a hand-registered collector)
// and asserts the pebble_* series appear for the main store. It fails if
// metrics.Register() stops registering the collector or if NewServer stops
// calling registerPebbleMetricsSources.
func TestMetricsScrape_ExportsPebbleSeriesForMainStore(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	for _, want := range []string{
		`audiobook_organizer_pebble_block_cache_hits_total{store="main"}`,
		`audiobook_organizer_pebble_read_amplification{store="main"}`,
		`audiobook_organizer_pebble_l0_sublevels{store="main"}`,
		`audiobook_organizer_pebble_compaction_debt_bytes{store="main"}`,
		`audiobook_organizer_pebble_level_bytes_compacted_total{level="6",store="main"}`,
	} {
		require.Contains(t, body, want)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "audiobook_organizer_pebble_") && strings.Contains(line, "{store=\"main\"}") {
			t.Log(line)
		}
	}
}

func scrapeBody(t *testing.T, srv *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, w.Code)
	return w.Body.String()
}

// Shutdown must drop this server's closures from the global collector (so they
// stop pinning the Server), but a late shutdown of an OLD server must not
// remove the sources a newer server installed over it.
func TestUnregisterPebbleMetricsSources_OnlyRemovesOwn(t *testing.T) {
	oldSrv, cleanupOld := setupTestServer(t)
	defer cleanupOld()
	newSrv, cleanupNew := setupTestServer(t) // replaces oldSrv's sources
	defer cleanupNew()

	const series = `audiobook_organizer_pebble_read_amplification{store="main"}`

	oldSrv.unregisterPebbleMetricsSources()
	require.Contains(t, scrapeBody(t, newSrv), series, "old server's shutdown removed the newer server's source")

	newSrv.unregisterPebbleMetricsSources()
	require.NotContains(t, scrapeBody(t, newSrv), series, "unregister left the source installed")

	newSrv.unregisterPebbleMetricsSources() // idempotent
}
