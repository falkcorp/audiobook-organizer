// file: internal/transcribe/gate_probe_cache_test.go
// version: 1.0.0
// guid: e4ebe594-0334-4c03-8714-f86dd60340ee
// last-edited: 2026-10-30

package transcribe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMain turns the gate's probe cache off for the package: the dispatcher
// tests run many dispatches against fake workers whose /health changes between
// them (and ports are reused between servers), and each expects its gate to see
// the answer of that moment. The tests below that are ABOUT the cache turn it on
// with enableProbeCache.
func TestMain(m *testing.M) {
	gateProbeTTL = 0
	os.Exit(m.Run())
}

// enableProbeCache turns the cache on with a fake clock the test steps, and
// leaves it off and empty afterwards.
func enableProbeCache(t *testing.T) *fakeGateClock {
	t.Helper()
	c := &fakeGateClock{now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	resetGateProbeCache()
	gateProbeTTL, gateProbeNow = 30*time.Second, c.Now
	t.Cleanup(func() {
		gateProbeTTL, gateProbeNow = 0, time.Now
		resetGateProbeCache()
	})
	return c
}

func resetGateProbeCache() {
	gateProbeCache.mu.Lock()
	defer gateProbeCache.mu.Unlock()
	clear(gateProbeCache.m)
}

type fakeGateClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeGateClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeGateClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// countingHealth serves /health with the given body and counts the requests.
// status other than 200 makes the probe fail.
func countingHealth(t *testing.T, body map[string]any, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if status != http.StatusOK {
			http.Error(rw, "down", status)
			return
		}
		_ = json.NewEncoder(rw).Encode(body)
	}))
	t.Cleanup(srv.Close)
	clearCooldown(srv.URL)
	t.Cleanup(func() { clearCooldown(srv.URL) })
	return srv, &hits
}

func gateCached(t *testing.T, url string) gatedEndpoint {
	t.Helper()
	kept, refused := gateEndpointsCached(context.Background(), []Endpoint{{URL: url}}, nil)
	if len(kept) != 1 {
		t.Fatalf("gate kept %d endpoints (refused %v), want 1", len(kept), refused)
	}
	return kept[0]
}

func TestGateProbeCache_SecondGateWithinTTLDoesNotProbe(t *testing.T) {
	enableProbeCache(t)
	srv, hits := countingHealth(t, pathHealth(nil), http.StatusOK)

	first := gateCached(t, srv.URL)
	second := gateCached(t, srv.URL)

	if got := hits.Load(); got != 1 {
		t.Fatalf("/health requests = %d after two cached gates, want 1", got)
	}
	if !second.Probed || second.Health.Model != first.Health.Model || !second.Health.PathPipeline {
		t.Errorf("cached gate lost the health answer: %+v", second.Health)
	}
}

func TestGateProbeCache_UncachedGateAlwaysProbes(t *testing.T) {
	enableProbeCache(t)
	srv, hits := countingHealth(t, pathHealth(nil), http.StatusOK)

	gateCached(t, srv.URL)
	// gateEndpoints is the re-probe primitive (regate, reprobe, the preflight):
	// it must look every time, cache warm or not.
	for range 2 {
		if kept, _ := gateEndpoints(context.Background(), []Endpoint{{URL: srv.URL}}, nil); len(kept) != 1 {
			t.Fatal("gateEndpoints refused a healthy endpoint")
		}
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("/health requests = %d, want 3 (one cached-gate probe + two uncached gates)", got)
	}
}

func TestGateProbeCache_ProbesAgainAfterTTL(t *testing.T) {
	clock := enableProbeCache(t)
	srv, hits := countingHealth(t, pathHealth(nil), http.StatusOK)

	gateCached(t, srv.URL)
	clock.Advance(gateProbeTTL - time.Second)
	gateCached(t, srv.URL)
	if got := hits.Load(); got != 1 {
		t.Fatalf("/health requests = %d just inside the TTL, want 1", got)
	}
	clock.Advance(2 * time.Second)
	gateCached(t, srv.URL)
	if got := hits.Load(); got != 2 {
		t.Fatalf("/health requests = %d just past the TTL, want 2", got)
	}
}

// A failure marked for the endpoint retires its cached answer at once, even
// when the cooldown it started has already been cleared (a round that outlasts
// it) and the TTL has not run out.
func TestGateProbeCache_FailureInvalidatesImmediately(t *testing.T) {
	enableProbeCache(t)
	srv, hits := countingHealth(t, pathHealth(nil), http.StatusOK)

	gateCached(t, srv.URL)
	markEndpointFailure(srv.URL)

	// In its cooldown: not served from the cache.
	gateCached(t, srv.URL)
	if got := hits.Load(); got != 2 {
		t.Fatalf("/health requests = %d while benched, want 2 (the cached answer must not be served)", got)
	}
	// Cooldown over: the answer cached while benched must not exist either, and
	// the first one is retired by the failure generation.
	clearCooldown(srv.URL)
	gateCached(t, srv.URL)
	if got := hits.Load(); got != 3 {
		t.Fatalf("/health requests = %d after the cooldown cleared, want 3", got)
	}
	gateCached(t, srv.URL)
	if got := hits.Load(); got != 3 {
		t.Fatalf("/health requests = %d on the next gate, want 3 (the fresh answer is cacheable again)", got)
	}
}

// A failure that lands while a probe is in flight must retire that probe's
// answer: it was read before the failure and says nothing about the endpoint
// after it.
func TestGateProbeCache_FailureDuringProbeIsNotCached(t *testing.T) {
	enableProbeCache(t)
	var (
		hits   atomic.Int64
		srvURL atomic.Value // the handler needs the server's own URL
	)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			markEndpointFailure(srvURL.Load().(string))
		}
		_ = json.NewEncoder(rw).Encode(pathHealth(nil))
	}))
	t.Cleanup(srv.Close)
	srvURL.Store(srv.URL)
	clearCooldown(srv.URL)
	t.Cleanup(func() { clearCooldown(srv.URL) })

	gateCached(t, srv.URL)
	clearCooldown(srv.URL)
	gateCached(t, srv.URL)
	if got := hits.Load(); got != 2 {
		t.Fatalf("/health requests = %d, want 2: the answer probed across a failure was cached", got)
	}
}

func TestGateProbeCache_UnhealthyAnswersAreNotCached(t *testing.T) {
	cases := map[string]struct {
		body   map[string]any
		status int
	}{
		"probe failed":  {nil, http.StatusInternalServerError},
		"decoder fault": {pathHealth(map[string]any{"child_start_error": "model file missing"}), http.StatusOK},
		"stuck worker":  {pathHealth(map[string]any{"busy_seconds": 900.0, "busy_limit_seconds": 60.0}), http.StatusOK},
		"lost mount":    {pathHealth(map[string]any{"path_roots": []map[string]any{{"prefix": "/srv/library", "readable": false}}}), http.StatusOK},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			enableProbeCache(t)
			srv, hits := countingHealth(t, tc.body, tc.status)
			for range 2 {
				gateEndpointsCached(context.Background(), []Endpoint{{URL: srv.URL}}, nil)
			}
			if got := hits.Load(); got != 2 {
				t.Fatalf("/health requests = %d, want 2: a state that clears by itself was remembered", got)
			}
		})
	}
}

// End to end through the path batch: the second batch of a run does not probe
// the worker again.
func TestTranscribeByPathBatch_ReusesGateProbeAcrossBatches(t *testing.T) {
	clock := enableProbeCache(t)
	w := newPathWorker(t, pathHealth(nil))
	useEndpoints(t, 0, w.srv.URL)

	run := func() {
		t.Helper()
		if _, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	run()
	afterFirst := w.healthHits.Load()
	if afterFirst == 0 {
		t.Fatal("the first batch never probed /health")
	}
	run()
	if got := w.healthHits.Load(); got != afterFirst {
		t.Fatalf("second batch probed /health %d more time(s), want 0", got-afterFirst)
	}
	clock.Advance(gateProbeTTL + time.Second)
	run()
	if got := w.healthHits.Load(); got == afterFirst {
		t.Fatal("a batch past the TTL did not probe /health")
	}
}
