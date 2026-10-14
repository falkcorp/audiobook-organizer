// file: internal/server/library_list_warmer_test.go
// version: 1.1.0
// guid: 5b0c8f4e-2d7a-4c1e-9a63-7e1f0b3d8a52
// last-edited: 2026-10-14

package server

import (
	"context"
	"log/slog"
	"math"
	"runtime"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

// trickleTestBacklog returns n distinct list queries, each its own cache entry.
func trickleTestBacklog(n int) []qry {
	primary := true
	out := make([]qry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, qry{
			name:   "title-asc-primary",
			limit:  20,
			offset: i * 20,
			filters: audiobookspkg.ListFilters{
				IsPrimaryVersion: &primary,
				SortBy:           "title",
				SortOrder:        "asc",
			},
		})
	}
	return out
}

// trickleQueryCached reports whether the trickle published q's response.
func trickleQueryCached(s *Server, q qry) bool {
	key := s.libraryGeneration().Key("list:", buildListCacheRawQuery(q.limit, q.offset, q.filters))
	_, ok := s.listCache.Get(key)
	return ok
}

// forcedGCCycles reads the process-wide count of GC cycles started by
// runtime.GC() or debug.FreeOSMemory(). Organic cycles are not counted, so a
// background collection cannot flake a test built on it.
func forcedGCCycles(t *testing.T) uint64 {
	t.Helper()
	sample := []metrics.Sample{{Name: "/gc/cycles/forced:gc-cycles"}}
	metrics.Read(sample)
	require.Equal(t, metrics.KindUint64, sample[0].Value.Kind(),
		"the runtime must export /gc/cycles/forced:gc-cycles")
	return sample[0].Value.Uint64()
}

// noMemoryLimit is what debug.SetMemoryLimit(-1) reports when GOMEMLIMIT is
// unset.
const noMemoryLimit = int64(math.MaxInt64)

// fastTrickleEnv is the production env with the pacing collapsed to a
// millisecond and the live-heap read left real. The memory limit is scripted to
// "none" so the result does not depend on the GOMEMLIMIT of whoever runs the
// tests.
func fastTrickleEnv() trickleEnv {
	env := defaultTrickleEnv()
	env.interval = time.Millisecond
	env.memLimitBytes = func() int64 { return noMemoryLimit }
	return env
}

// TestTrickleWarmerForcesAtMostOneGC pins the 06-P13 fix: the trickle used to
// force a collection plus a scavenge after every query, which kept a 6GiB,
// 60M-object heap in the collector for ~30 minutes after each restart. It may
// now force exactly one, the baseline taken once memdb is published.
//
// Deliberately NOT parallel: the forced-cycle counter is process-wide, so any
// other test forcing a GC while this one runs would be counted against it.
func TestTrickleWarmerForcesAtMostOneGC(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	backlog := trickleTestBacklog(6)
	before := forcedGCCycles(t)
	server.runTrickle(backlog, fastTrickleEnv())
	forced := forcedGCCycles(t) - before

	// Exactly one: the baseline. Equal rather than LessOrEqual so that dropping
	// the baseline collection (and with it the settled-heap reading) fails too.
	assert.Equal(t, uint64(1), forced,
		"%d queries must not force a collection each; only the baseline may", len(backlog))

	// Anti-over-suppression: removing the forced collections must not have
	// switched the warmer off. Every query ran and published its response.
	for _, q := range backlog {
		assert.True(t, trickleQueryCached(server, q),
			"trickle query at offset %d never ran", q.offset)
	}
}

// TestTrickleHeapGuardUsesLiveHeapMetric drives the trickle loop with a
// scripted live-heap reading. The first reading is the baseline; every later
// one is a tick's guard reading.
func TestTrickleHeapGuardUsesLiveHeapMetric(t *testing.T) {
	const (
		baselineMB = uint64(1000)
		deltaMB    = uint64(100)
		queries    = 5
	)
	// limit is the scripted process memory limit in bytes; zero means none set.
	// The warmers clamp their ceiling to 80% of it, so 1000MiB gives an 800MB cap.
	const limit1000MiB = int64(1000) << 20
	tests := []struct {
		name         string
		baseline     func() (uint64, bool)
		tick         func() (uint64, bool)
		deltaMB      uint64 // zero: the table default
		limitBytes   int64  // zero: no memory limit
		wantAllRun   bool
		wantNoneRuns bool
	}{
		{
			name:       "no GC has completed yet: zero live bytes skips the ceiling check",
			baseline:   func() (uint64, bool) { return 0, false },
			tick:       func() (uint64, bool) { return 0, false },
			wantAllRun: true,
		},
		{
			// A zero baseline must not become a ceiling of 0+delta that every
			// later real reading then exceeds.
			name:       "no baseline, later readings are huge: the guard stays off",
			baseline:   func() (uint64, bool) { return 0, false },
			tick:       func() (uint64, bool) { return baselineMB * 50, true },
			wantAllRun: true,
		},
		{
			name:       "live growth under the delta continues",
			baseline:   func() (uint64, bool) { return baselineMB, true },
			tick:       func() (uint64, bool) { return baselineMB + deltaMB - 1, true },
			wantAllRun: true,
		},
		{
			name:       "live growth exactly at the ceiling continues",
			baseline:   func() (uint64, bool) { return baselineMB, true },
			tick:       func() (uint64, bool) { return baselineMB + deltaMB, true },
			wantAllRun: true,
		},
		{
			name:         "live growth above the delta stops the trickle",
			baseline:     func() (uint64, bool) { return baselineMB, true },
			tick:         func() (uint64, bool) { return baselineMB + deltaMB + 1, true },
			wantNoneRuns: true,
		},
		{
			name:       "a zero reading mid-run is not growth",
			baseline:   func() (uint64, bool) { return baselineMB, true },
			tick:       func() (uint64, bool) { return 0, false },
			wantAllRun: true,
		},
		{
			// Production's shape: baseline + a 4GB delta lands far above the
			// memory limit, so without the clamp this would run.
			name:         "clamp active: live heap above the memory-limit cap stops the trickle though under baseline+delta",
			baseline:     func() (uint64, bool) { return 700, true },
			tick:         func() (uint64, bool) { return 801, true },
			deltaMB:      4096,
			limitBytes:   limit1000MiB,
			wantNoneRuns: true,
		},
		{
			name:       "clamp active: live heap exactly at the memory-limit cap continues",
			baseline:   func() (uint64, bool) { return 700, true },
			tick:       func() (uint64, bool) { return 800, true },
			deltaMB:    4096,
			limitBytes: limit1000MiB,
			wantAllRun: true,
		},
		{
			name:         "clamp active: a baseline already above the cap holds every query back",
			baseline:     func() (uint64, bool) { return 900, true },
			tick:         func() (uint64, bool) { return 900, true },
			deltaMB:      4096,
			limitBytes:   limit1000MiB,
			wantNoneRuns: true,
		},
		{
			name:         "limit looser than the delta: the delta is the binding ceiling",
			baseline:     func() (uint64, bool) { return baselineMB, true },
			tick:         func() (uint64, bool) { return baselineMB + deltaMB + 1, true },
			limitBytes:   int64(100_000) << 20,
			wantNoneRuns: true,
		},
		{
			// No GOMEMLIMIT: nothing to clamp to. A huge settled heap must not
			// be mistaken for a tripped cap.
			name:       "no limit set: a huge baseline is not clamped",
			baseline:   func() (uint64, bool) { return 100_000, true },
			tick:       func() (uint64, bool) { return 100_000 + deltaMB, true },
			wantAllRun: true,
		},
		{
			name:         "no limit set: the delta still applies",
			baseline:     func() (uint64, bool) { return 100_000, true },
			tick:         func() (uint64, bool) { return 100_000 + deltaMB + 1, true },
			wantNoneRuns: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, cleanup := setupTestServer(t)
			defer cleanup()

			// A guard that wrongly holds every query back would never let the
			// backlog drain; fail on the assertions below instead of hanging.
			watchdog := time.AfterFunc(10*time.Second, server.bgCancel)
			defer watchdog.Stop()

			var calls atomic.Int64
			env := fastTrickleEnv()
			env.deltaMB = deltaMB
			if tc.deltaMB != 0 {
				env.deltaMB = tc.deltaMB
			}
			if tc.limitBytes != 0 {
				env.memLimitBytes = func() int64 { return tc.limitBytes }
			}
			env.liveHeapMB = func() (uint64, bool) {
				n := calls.Add(1)
				if n == 1 {
					return tc.baseline()
				}
				// A guard that trips never lets the backlog drain, so end the
				// run once it has had several chances to let a query through.
				if tc.wantNoneRuns && n >= 6 {
					server.bgCancel()
				}
				return tc.tick()
			}

			backlog := trickleTestBacklog(queries)
			server.runTrickle(backlog, env)

			ran := 0
			for _, q := range backlog {
				if trickleQueryCached(server, q) {
					ran++
				}
			}
			switch {
			case tc.wantAllRun:
				assert.Equal(t, queries, ran, "the guard must not hold back queries")
			case tc.wantNoneRuns:
				assert.Zero(t, ran, "a live heap above the ceiling must stop every query")
				assert.GreaterOrEqual(t, calls.Load(), int64(6),
					"the loop must have consulted the guard on several ticks, not exited early")
			}
		})
	}
}

// TestReadLiveHeapMBReportsAfterGC is the positive control for the metric the
// guards read: after a collection it is nonzero and usable.
func TestReadLiveHeapMBReportsAfterGC(t *testing.T) {
	// Hold something live so the reading cannot legitimately round to 0MB.
	// Not parallel: it forces a GC, and TestTrickleWarmerForcesAtMostOneGC
	// counts forced cycles process-wide.
	ballast := make([]byte, 8<<20)
	runtime.GC()
	mb, ok := readLiveHeapMB()
	require.True(t, ok, "/gc/heap/live:bytes must be readable after a GC cycle")
	assert.GreaterOrEqual(t, mb, uint64(8))
	assert.False(t, heapOverCeiling(mb, ok, mb+1))
	assert.True(t, heapOverCeiling(mb+2, ok, mb+1))
	assert.False(t, heapOverCeiling(mb+2, false, mb+1), "an unusable reading never trips the ceiling")
	_ = ballast[0]
}

// TestHeapCeilingMB pins the ceiling both guards share: min(baseline+delta,
// 80% of the memory limit), with no clamp when no limit is set.
func TestHeapCeilingMB(t *testing.T) {
	tests := []struct {
		name       string
		baseline   uint64
		delta      uint64
		limitBytes int64
		want       uint64
	}{
		{"no limit: baseline+delta", 5900, 4096, math.MaxInt64, 9996},
		{"production shape: clamped to 80% of 9GiB", 5900, 4096, 9 << 30, 7372},
		{"delta binds when the cap is looser", 1000, 100, 9 << 30, 1100},
		{"baseline above the cap yields a ceiling below the baseline", 8000, 4096, 9 << 30, 7372},
		{"a non-positive limit is not a limit", 1000, 100, 0, 1100},
		{"cap rounds down to a whole MB", 0, 4096, 10<<20 + 1, 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, heapCeilingMB(tc.baseline, tc.delta, tc.limitBytes))
		})
	}
}

// TestCurrentMemoryLimitBytesIsReadOnly: the production limit reader must not
// change the limit it reads.
func TestCurrentMemoryLimitBytesIsReadOnly(t *testing.T) {
	first := currentMemoryLimitBytes()
	second := currentMemoryLimitBytes()
	assert.Equal(t, first, second)
	assert.Positive(t, first)
}

// TestEagerHeapGuard drives the eager phase with a scripted live heap. The
// first reading is the baseline; the n-th later reading is the guard check
// before the (n-1)-th query (0-based), including the first.
func TestEagerHeapGuard(t *testing.T) {
	const (
		queries      = 3
		limit1000MiB = int64(1000) << 20 // 800MB cap
	)
	steady := func(mb uint64) func(n int64) (uint64, bool) {
		return func(int64) (uint64, bool) { return mb, true }
	}
	// from returns the reading after the baseline: low for the checks before
	// the first query, high from the check before the second one on.
	from := func(low, high uint64) func(n int64) (uint64, bool) {
		return func(n int64) (uint64, bool) {
			if n >= 3 {
				return high, true
			}
			return low, true
		}
	}
	tests := []struct {
		name        string
		reading     func(n int64) (uint64, bool) // n is 1-based; 1 is the baseline
		deltaMB     uint64
		limitBytes  int64 // zero: no limit
		wantRan     int
		wantSkipped int
	}{
		{
			name:    "steady heap: every query runs",
			reading: steady(1000), deltaMB: 100, wantRan: queries,
		},
		{
			name: "no baseline: the guard stays off whatever later readings say",
			reading: func(n int64) (uint64, bool) {
				if n == 1 {
					return 0, false
				}
				return 1 << 30, true
			},
			deltaMB: 100, wantRan: queries,
		},
		{
			// The pre-flight the old code had compared a second read of the same
			// GC cycle against baseline+delta and could never trip. With the
			// clamp, a settled heap already above the cap skips the phase before
			// it allocates anything.
			name:    "clamp active: a baseline already above the cap skips every query",
			reading: steady(900), deltaMB: 4096, limitBytes: limit1000MiB,
			wantRan: 0, wantSkipped: queries,
		},
		{
			name:    "clamp active: baseline under the cap runs",
			reading: steady(700), deltaMB: 4096, limitBytes: limit1000MiB, wantRan: queries,
		},
		{
			name:    "clamp active: growth past the cap mid-run skips the rest",
			reading: from(700, 801), deltaMB: 4096, limitBytes: limit1000MiB,
			wantRan: 1, wantSkipped: queries - 1,
		},
		{
			name:    "no limit: growth past baseline+delta mid-run skips the rest",
			reading: from(1000, 1101), deltaMB: 100, wantRan: 1, wantSkipped: queries - 1,
		},
		{
			name:    "no limit: growth exactly to the ceiling continues",
			reading: from(1000, 1100), deltaMB: 100, wantRan: queries,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, cleanup := setupTestServer(t)
			defer cleanup()

			var calls atomic.Int64
			env := heapGuardEnv{
				deltaMB:       tc.deltaMB,
				liveHeapMB:    func() (uint64, bool) { return tc.reading(calls.Add(1)) },
				memLimitBytes: func() int64 { return noMemoryLimit },
			}
			if tc.limitBytes != 0 {
				env.memLimitBytes = func() int64 { return tc.limitBytes }
			}

			backlog := trickleTestBacklog(queries)
			res, completed := server.runEager(server.bgCtx, backlog, env)
			require.True(t, completed)

			assert.Equal(t, tc.wantRan, res.hits)
			assert.Equal(t, tc.wantSkipped, res.skipped)
			ran := 0
			for _, q := range backlog {
				if trickleQueryCached(server, q) {
					ran++
				}
			}
			assert.Equal(t, tc.wantRan, ran, "cache entries published must match the queries that ran")
		})
	}
}

// TestEagerStopsOnShutdown: a canceled server aborts the eager phase and
// reports it, so the caller does not start the trickle against a closing store.
func TestEagerStopsOnShutdown(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	server.bgCancel()

	res, completed := server.runEager(server.bgCtx, trickleTestBacklog(2), fastTrickleEnv().heapGuardEnv)
	assert.False(t, completed)
	assert.Zero(t, res.hits)
}

// memdbProbeStore wraps a store and reports a scripted memdb warmup, counting
// the reads the entity warmers issue. It stands in for a PebbleStore whose
// warmup is still running (release not yet closed) or has finished.
type memdbProbeStore struct {
	database.Store
	release chan struct{} // closed when the scripted warmup finishes
	waits   atomic.Int64

	mu              sync.Mutex
	authorBookCount int
	seriesBookCount int
}

func (p *memdbProbeStore) WaitForWarmupCtx(ctx context.Context) error {
	p.waits.Add(1)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetAllAuthorBookCounts and GetAllSeriesBookCounts are the first read each
// warmer's ListXWithCounts makes after GetAll*: the probe point for "the warmer
// started reading".
func (p *memdbProbeStore) GetAllAuthorBookCounts() (map[int]int, error) {
	p.mu.Lock()
	p.authorBookCount++
	p.mu.Unlock()
	return p.Store.GetAllAuthorBookCounts()
}

func (p *memdbProbeStore) GetAllSeriesBookCounts() (map[int]int, error) {
	p.mu.Lock()
	p.seriesBookCount++
	p.mu.Unlock()
	return p.Store.GetAllSeriesBookCounts()
}

func (p *memdbProbeStore) reads() (authors, series int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authorBookCount, p.seriesBookCount
}

// TestEntityWarmersWaitForMemdb pins Step 5 of 06-P13: the authors and series
// warmers must not read while memdb is still loading, and must read exactly
// once when the warmup finished -- including when it finished WITHOUT
// publishing, which is not a reason to skip the warm.
func TestEntityWarmersWaitForMemdb(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	probe := &memdbProbeStore{Store: server.store, release: make(chan struct{})}
	server.store = probe
	server.authorSeriesService = audiobookspkg.NewAuthorSeriesService(probe)

	var wg sync.WaitGroup
	for _, warm := range []func(){server.warmAuthorsCache, server.warmSeriesCache} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			warm()
		}()
	}

	// Both warmers must have reached the wait, and stay parked there.
	require.Eventually(t, func() bool { return probe.waits.Load() == 2 },
		5*time.Second, 5*time.Millisecond, "both warmers must wait for the memdb warmup")
	time.Sleep(100 * time.Millisecond)
	authors, series := probe.reads()
	assert.Zero(t, authors, "authors warmer read the store while memdb was still loading")
	assert.Zero(t, series, "series warmer read the store while memdb was still loading")

	// Warmup finishes (here: without any memdb being published -- the probe
	// has none). The warmers must now run as before, once each.
	close(probe.release)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("warmers did not finish after the warmup was released")
	}
	authors, series = probe.reads()
	assert.Equal(t, 1, authors, "authors warmer must warm exactly once after the wait")
	assert.Equal(t, 1, series, "series warmer must warm exactly once after the wait")
	_, ok := server.authorsCache.Get("all")
	assert.True(t, ok, "authors cache must be populated after the wait")
	_, ok = server.seriesCache.Get("all")
	assert.True(t, ok, "series cache must be populated after the wait")
}

// TestEntityWarmersSkipWhenShutdownDuringWait: a canceled server ends the wait
// without warming, so a closing store is never read.
func TestEntityWarmersSkipWhenShutdownDuringWait(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	probe := &memdbProbeStore{Store: server.store, release: make(chan struct{})}
	server.store = probe
	server.authorSeriesService = audiobookspkg.NewAuthorSeriesService(probe)

	done := make(chan struct{})
	go func() {
		server.warmAuthorsCache()
		server.warmSeriesCache()
		close(done)
	}()
	require.Eventually(t, func() bool { return probe.waits.Load() >= 1 },
		5*time.Second, 5*time.Millisecond)
	server.bgCancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("warmers did not return after shutdown")
	}
	authors, series := probe.reads()
	assert.Zero(t, authors)
	assert.Zero(t, series)
}

// TestWaitForMemdbWarmupBoundHit covers the bound-hit branch: a warmup that
// never finishes must not hold the warmer forever. After the bound it logs a
// warning and reports true, so the caller warms from the Pebble fallback as it
// did before it waited at all. (Shutdown is the separate case above.)
func TestWaitForMemdbWarmupBoundHit(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	rec := logtest.CaptureRecords(t) // not parallel: swaps slog.Default

	probe := &memdbProbeStore{Store: server.store, release: make(chan struct{})} // never released
	server.store = probe

	const bound = 10 * time.Millisecond
	start := time.Now()
	proceed := server.waitForMemdbWarmupBounded("bound-test", bound)
	elapsed := time.Since(start)

	assert.True(t, proceed, "on the bound the caller warms anyway")
	assert.GreaterOrEqual(t, elapsed, bound, "it must actually have waited for the bound")
	assert.Less(t, elapsed, 5*time.Second, "and not for much longer than it")
	assert.Equal(t, int64(1), probe.waits.Load())
	assert.NoError(t, server.bgCtx.Err(), "the bound is not a shutdown")

	var warned bool
	for _, r := range rec.Records() {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, "bound-test warm-up: memdb warmup not finished after 10ms") {
			warned = true
		}
	}
	assert.True(t, warned, "the bound-hit must be logged at Warn")
}
