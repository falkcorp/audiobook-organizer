// file: internal/database/memdb_metrics_test.go
// version: 1.0.4
// guid: c27e5a90-4d13-4b68-8f0e-9a1b6d3c5e74
// last-edited: 2026-10-10

package database

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
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
// published, and it reaches /metrics as audiobook_organizer_memdb_fallback_reads_total{site=...}.
func TestMemOrFallback_CountsOnlyUnreadyReadsAndShowsOnMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	require.NoError(t, err)
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	scrape := func() string {
		rec := httptest.NewRecorder()
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		return rec.Body.String()
	}
	line := func(site, outcome string) string {
		for _, l := range strings.Split(scrape(), "\n") {
			if strings.HasPrefix(l, "audiobook_organizer_memdb_fallback_reads_total{") && strings.Contains(l, `site="`+site+`"`) &&
				strings.Contains(l, `outcome="`+outcome+`"`) {
				return l
			}
		}
		return ""
	}

	p := newWarmPebbleForMemdbTest(t)
	// A private provider: the test never touches the process-global one, so it
	// is repeatable (-count=N) and cannot leak into another test.
	p.SetMeterProvider(mp)

	// Ready: served from memdb, not counted.
	_, err = p.GetAllAuthors()
	require.NoError(t, err)
	require.Empty(t, line("GetAllAuthors", "fallback"))

	// Unpublished with UseMemDB on: a fallback read, counted once per call.
	m := p.mem()
	p.memPtr.Store(nil)
	_, err = p.GetAllAuthors()
	require.NoError(t, err)
	_, err = p.GetAllAuthors()
	require.NoError(t, err)
	got := line("GetAllAuthors", "fallback")
	require.NotEmpty(t, got, "audiobook_organizer_memdb_fallback_reads_total{site=\"GetAllAuthors\"} missing from scrape:\n%s", scrape())
	require.True(t, strings.HasSuffix(got, " 2"), "want 2 fallback reads, got line %q", got)
	t.Logf("scraped: %s", got)

	// A read with no Pebble path is counted as refused, not as a fallback.
	_, err = p.GetBooksByMetadataSourceHashInMemory("h")
	require.ErrorIs(t, err, ErrMemDBNotReady)
	require.NotEmpty(t, line("GetBooksByMetadataSourceHashInMemory", "refused"))
	require.Empty(t, line("GetBooksByMetadataSourceHashInMemory", "fallback"))

	// UseMemDB=false is a deliberate Pebble store (tests), never a fallback.
	p.UseMemDB = false
	_, err = p.GetAllSeries()
	require.NoError(t, err)
	require.Empty(t, line("GetAllSeries", "fallback"))
	p.UseMemDB = true
	p.memPtr.Store(m)
}

// Every read that chooses between memdb and Pebble must go through
// memOrFallback / memOrRefuse, or its unmet memdb reads are invisible on
// audiobook_organizer_memdb_fallback_reads_total. This is an AST check, so it holds for every shape
// the package has used, including the multi-statement ones a line regex misses:
//
//	if p.UseMemDB && p.mem() != nil { ... }
//	if m := p.mem(); p.UseMemDB && m != nil { ... }
//	if s.UseMemDB { if m := s.mem(); m != nil { ... } }
//	if !p.UseMemDB { return err }; m := p.mem(); if m == nil { return err }
//
// The rule: a function that both reads the UseMemDB flag and touches .mem,
// .memPtr or .IsMemReady (called or as a method value) is a hand-rolled guard. Only the helpers themselves may.
func TestNoRawMemdbReadGuards(t *testing.T) {
	allowed := map[string]bool{"memOr": true}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || allowed[fd.Name.Name] {
				continue
			}
			var readsFlag, callsMem bool
			var memPos token.Pos
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				// Any selector of the memdb accessors counts, called or not: a
				// method value (f := p.mem) or a direct memPtr / IsMemReady
				// check is the same hand-rolled guard.
				if x, ok := n.(*ast.SelectorExpr); ok {
					switch x.Sel.Name {
					case "UseMemDB":
						readsFlag = true
					case "mem", "memPtr", "IsMemReady":
						callsMem = true
						memPos = x.Pos()
					}
				}
				return true
			})
			if readsFlag && callsMem {
				t.Errorf("%s: %s reads UseMemDB and uses .mem/.memPtr/.IsMemReady directly: use p.memOrFallback(site) (or memOrRefuse when there is no Pebble path) so the unmet read is counted",
					fset.Position(memPos), fd.Name.Name)
			}
		}
	}
}
