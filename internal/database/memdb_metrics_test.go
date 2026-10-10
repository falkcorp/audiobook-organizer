// file: internal/database/memdb_metrics_test.go
// version: 1.0.0
// guid: c27e5a90-4d13-4b68-8f0e-9a1b6d3c5e74
// last-edited: 2026-10-09

package database

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func newWarmPebbleForMemdbTest(t *testing.T) *PebbleStore {
	t.Helper()
	p, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	p.WaitForWarmup()
	require.True(t, p.IsMemReady())
	return p
}

func TestWarmupStatus_ReadyAndDurationAfterWarmup(t *testing.T) {
	p := newWarmPebbleForMemdbTest(t)
	ready, done, ms := p.WarmupStatus()
	require.True(t, ready)
	require.True(t, done)
	require.GreaterOrEqual(t, ms, int64(0))
	require.NoError(t, p.WaitForWarmupCtx(context.Background()))
}

// A canceled ctx must not hide a finished warmup (callers probe with one), and
// must end a wait on an unfinished one.
func TestWaitForWarmupCtx_FinishedBeatsCanceledAndCancelEndsWait(t *testing.T) {
	p := newWarmPebbleForMemdbTest(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for range 50 {
		require.NoError(t, p.WaitForWarmupCtx(canceled))
	}

	unfinished := &PebbleStore{warmupDone: make(chan struct{})}
	ready, done, ms := unfinished.WarmupStatus()
	require.False(t, ready)
	require.False(t, done)
	require.Zero(t, ms)
	require.ErrorIs(t, unfinished.WaitForWarmupCtx(canceled), context.Canceled)

	ctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	require.ErrorIs(t, unfinished.WaitForWarmupCtx(ctx), context.DeadlineExceeded)

	// No warmup goroutine at all (nil channel): nothing to wait for.
	none := &PebbleStore{}
	_, done, _ = none.WarmupStatus()
	require.True(t, done)
	require.NoError(t, none.WaitForWarmupCtx(canceled))
}

// The fallback counter counts only when the store wants memdb and it is not
// published, and it reaches /metrics as memdb_fallback_reads_total{site=...}.
func TestMemOrFallback_CountsOnlyUnreadyReadsAndShowsOnMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	require.NoError(t, err)
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	scrape := func() string {
		rec := httptest.NewRecorder()
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		return rec.Body.String()
	}
	line := func(site string) string {
		for _, l := range strings.Split(scrape(), "\n") {
			if strings.HasPrefix(l, "memdb_fallback_reads_total{") && strings.Contains(l, `site="`+site+`"`) {
				return l
			}
		}
		return ""
	}

	p := newWarmPebbleForMemdbTest(t)

	// Ready: served from memdb, not counted.
	_, err = p.GetAllAuthors()
	require.NoError(t, err)
	require.Empty(t, line("GetAllAuthors"))

	// Unpublished with UseMemDB on: a fallback read, counted once per call.
	m := p.mem()
	p.memPtr.Store(nil)
	_, err = p.GetAllAuthors()
	require.NoError(t, err)
	_, err = p.GetAllAuthors()
	require.NoError(t, err)
	got := line("GetAllAuthors")
	require.NotEmpty(t, got, "memdb_fallback_reads_total{site=\"GetAllAuthors\"} missing from scrape:\n%s", scrape())
	require.True(t, strings.HasSuffix(got, " 2"), "want 2 fallback reads, got line %q", got)
	t.Logf("scraped: %s", got)

	// UseMemDB=false is a deliberate Pebble store (tests), never a fallback.
	p.UseMemDB = false
	_, err = p.GetAllSeries()
	require.NoError(t, err)
	require.Empty(t, line("GetAllSeries"))
	p.UseMemDB = true
	p.memPtr.Store(m)
}
