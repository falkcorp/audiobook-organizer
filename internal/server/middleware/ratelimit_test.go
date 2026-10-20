// file: internal/server/middleware/ratelimit_test.go
// version: 1.4.0
// guid: b31f3de0-b0bc-4cbf-8448-7309df38f7c0
// last-edited: 2026-10-20

package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a mutex-guarded manual clock injected via IPRateLimiter.now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestLimiter returns a limiter driven by a fake clock with the sweeper
// NOT started, so tests control eviction explicitly.
func newTestLimiter(t *testing.T, rpm, burst int) (*IPRateLimiter, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	r := NewIPRateLimiter(rpm, burst)
	r.now = clock.Now
	return r, clock
}

// v6 returns a distinct documentation-range IPv6 address for i.
func v6(i int) string {
	return fmt.Sprintf("2001:db8::%x:%x", i>>16, i&0xffff)
}

func TestNewIPRateLimiter_Defaults(t *testing.T) {
	t.Parallel()

	limiter := NewIPRateLimiter(0, 0)
	assert.Equal(t, 1, limiter.requestsPerMin)
	assert.Equal(t, 1, limiter.burst)
	assert.Equal(t, defaultIdleTTL, limiter.idleTTL)
	assert.Equal(t, defaultSweepInterval, limiter.sweepInterval)
	assert.Equal(t, defaultMaxEntries, limiter.maxEntries)
}

func TestIPRateLimiter_Middleware(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(NewIPRateLimiter(1, 1).Middleware())
	router.GET("/limited", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req1 := httptest.NewRequest(http.MethodGet, "/limited", nil)
	req1.RemoteAddr = "192.0.2.1:1234"
	resp1 := httptest.NewRecorder()
	router.ServeHTTP(resp1, req1)
	assert.Equal(t, http.StatusOK, resp1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/limited", nil)
	req2.RemoteAddr = "192.0.2.1:1234"
	resp2 := httptest.NewRecorder()
	router.ServeHTTP(resp2, req2)
	assert.Equal(t, http.StatusTooManyRequests, resp2.Code)
	assert.Contains(t, resp2.Body.String(), "rate limit exceeded")

	// Different IP should have its own bucket.
	req3 := httptest.NewRequest(http.MethodGet, "/limited", nil)
	req3.RemoteAddr = "198.51.100.3:4321"
	resp3 := httptest.NewRecorder()
	router.ServeHTTP(resp3, req3)
	assert.Equal(t, http.StatusOK, resp3.Code)
}

// TestIPRateLimiter_RequestPathDoesNotSweep is the SV-04 regression test.
// Pre-fix, limiterForIP walked the entire map deleting idle entries on every
// call, so after 10k stale entries a single request would have shrunk the
// map to one entry. Post-fix the request path must leave the stale entries
// alone (the sweeper owns eviction), must not allocate on a hit, and must
// not bump the sweep counter.
func TestIPRateLimiter_RequestPathDoesNotSweep(t *testing.T) {
	// Not t.Parallel(): testing.AllocsPerRun panics inside a parallel test.

	const n = 10_000
	r, clock := newTestLimiter(t, 60, 10)
	r.maxEntries = n + 10

	for i := range n {
		r.limiterForIP(v6(i))
	}
	require.Equal(t, n, r.Len())

	// Everything above is now idle past the TTL.
	clock.Advance(defaultIdleTTL + time.Minute)

	hot := "192.0.2.77"
	r.limiterForIP(hot) // insert
	assert.Equal(t, n+1, r.Len(), "a request must not evict idle entries on the request path")
	assert.Equal(t, int64(0), r.SweepCount(), "a request must not trigger a sweep")

	// A hit on an existing, fresh entry is a map lookup + LRU move: zero
	// allocations regardless of how many other IPs are tracked.
	allocs := testing.AllocsPerRun(200, func() {
		r.limiterForIP(hot)
	})
	assert.Equal(t, float64(0), allocs, "hit path must not allocate (no per-request iteration or copying)")
	assert.Equal(t, n+1, r.Len())
	assert.Equal(t, int64(0), r.SweepCount())
}

// TestIPRateLimiter_SweepEvictsStaleKeepsFresh drives the fake clock so that
// one batch of IPs is idle past the TTL and a second batch is fresh, then
// runs a sweep and checks only the stale batch is gone.
func TestIPRateLimiter_SweepEvictsStaleKeepsFresh(t *testing.T) {
	t.Parallel()

	r, clock := newTestLimiter(t, 60, 10)

	stale := []string{"192.0.2.10", "192.0.2.11", "2001:db8::10"}
	fresh := []string{"192.0.2.20", "2001:db8::20"}

	for _, ip := range stale {
		r.limiterForIP(ip)
	}
	clock.Advance(defaultIdleTTL + time.Second)
	for _, ip := range fresh {
		r.limiterForIP(ip)
	}
	require.Equal(t, len(stale)+len(fresh), r.Len())

	evicted := r.sweep()
	assert.Equal(t, len(stale), evicted)
	assert.Equal(t, len(fresh), r.Len())
	assert.Equal(t, int64(1), r.SweepCount())

	r.mu.Lock()
	for _, ip := range stale {
		_, ok := r.entries[ip]
		assert.False(t, ok, "stale %s should be evicted", ip)
	}
	for _, ip := range fresh {
		_, ok := r.entries[ip]
		assert.True(t, ok, "fresh %s should survive", ip)
	}
	r.mu.Unlock()

	// A touched-then-idle entry moves to the front of the LRU, so a later
	// sweep must still find it by TTL, not by insertion order.
	r.limiterForIP("192.0.2.20")
	clock.Advance(defaultIdleTTL + time.Second)
	assert.Equal(t, 2, r.sweep())
	assert.Equal(t, 0, r.Len())
}

// TestIPRateLimiter_CapHolds checks the map never exceeds maxEntries, that
// the least-recently-seen entry is the one evicted, and that touching an
// entry protects it from eviction.
func TestIPRateLimiter_CapHolds(t *testing.T) {
	t.Parallel()

	const capN = 100
	r, _ := newTestLimiter(t, 60, 10)
	r.maxEntries = capN

	for i := range capN {
		r.limiterForIP(v6(i))
	}
	require.Equal(t, capN, r.Len())

	// Touch the oldest so it becomes the most recently seen.
	r.limiterForIP(v6(0))

	// Insert 50 more: the map must stay at the cap, v6(1..50) (the oldest
	// untouched) must be gone, v6(0) and the newest must survive.
	for i := capN; i < capN+50; i++ {
		r.limiterForIP(v6(i))
		assert.LessOrEqual(t, r.Len(), capN)
	}
	assert.Equal(t, capN, r.Len())

	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.entries[v6(0)]
	assert.True(t, ok, "touched entry must survive")
	for i := 1; i <= 50; i++ {
		_, ok := r.entries[v6(i)]
		assert.False(t, ok, "least-recently-seen %s must be evicted", v6(i))
	}
	for i := 51; i < capN+50; i++ {
		_, ok := r.entries[v6(i)]
		assert.True(t, ok, "%s must survive", v6(i))
	}
	assert.Equal(t, r.lru.Len(), len(r.entries), "map and LRU must stay in sync")
}

// TestIPRateLimiter_IdleEntryGetsFreshBucketOnLookup preserves the pre-fix
// semantics: an IP returning after idleTTL always sees a full bucket, even
// if the sweeper has not reached its entry yet.
func TestIPRateLimiter_IdleEntryGetsFreshBucketOnLookup(t *testing.T) {
	t.Parallel()

	r, clock := newTestLimiter(t, 1, 1)
	ip := "192.0.2.5"
	require.True(t, r.limiterForIP(ip).Allow())
	require.False(t, r.limiterForIP(ip).Allow(), "burst of 1 exhausted")

	clock.Advance(defaultIdleTTL + time.Second)
	// No sweep has run; the entry is still present but stale.
	assert.Equal(t, 1, r.Len())
	assert.True(t, r.limiterForIP(ip).Allow(), "idle IP must get a fresh bucket")
	assert.Equal(t, 1, r.Len())
}

// TestIPRateLimiter_StartStopsOnContextCancel wires the sweeper to a context
// the way the server does with bgCtx and checks cancellation ends the
// goroutine.
func TestIPRateLimiter_StartStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	r, clock := newTestLimiter(t, 60, 10)
	r.sweepInterval = 5 * time.Millisecond
	r.limiterForIP("192.0.2.30")
	clock.Advance(defaultIdleTTL + time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	r.Start(ctx) // second Start is a no-op, not a second goroutine

	// Wait for both together: sweep() releases r.mu after evicting and only
	// then bumps the sweep counter, so Len()==0 can be observed while
	// SweepCount() is still 0. Checking them in sequence flaked in CI.
	assert.Eventually(t, func() bool { return r.Len() == 0 && r.SweepCount() >= 1 }, 2*time.Second, time.Millisecond,
		"background sweeper should evict the idle entry and record the sweep")

	cancel()
	select {
	case <-r.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("sweeper goroutine did not exit after context cancel")
	}
}

// TestIPRateLimiter_ConcurrentRequests exercises the critical section under
// -race with many goroutines and a running sweeper.
func TestIPRateLimiter_ConcurrentRequests(t *testing.T) {
	t.Parallel()

	r := NewIPRateLimiter(6000, 100)
	r.maxEntries = 500
	r.sweepInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	defer func() {
		cancel()
		<-r.doneCh
	}()

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 2000 {
				r.limiterForIP(v6(g*1000 + i%700)).Allow()
			}
		}(g)
	}
	wg.Wait()
	assert.LessOrEqual(t, r.Len(), 500)
}

// Len reports the number of IPs currently tracked. Test-only: the tests assert
// on the table size, and nothing in the server reads it.
func (r *IPRateLimiter) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lru.Len()
}

// SweepCount reports how many sweeps have completed. Test-only: the tests use
// it to prove the request path never sweeps and that the background sweeper ran.
func (r *IPRateLimiter) SweepCount() int64 {
	return r.sweeps.Load()
}

// ReserveKey takes tokens atomically: concurrent callers never get more than
// the bucket holds.
func TestIPRateLimiter_ReserveKeyIsAtomic(t *testing.T) {
	r := NewIPRateLimiter(1, 5)
	var got atomic.Int64
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := r.ReserveKey("k"); ok {
				got.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := got.Load(); n > 6 {
		t.Fatalf("%d concurrent reservations succeeded on a burst of 5", n)
	}
}

// ReserveKeys gives back every token it took when a later step refuses, and
// drops the entries it created for the refused request.
func TestReserveKeys_RefusalCancelsEverything(t *testing.T) {
	a := NewIPRateLimiter(1, 3)
	b := NewIPRateLimiter(1, 1)
	if !ReserveKeys(KeyStep{a, "x"}, KeyStep{b, "y"}) {
		t.Fatal("first request must pass")
	}
	for range 5 {
		if ReserveKeys(KeyStep{a, "x"}, KeyStep{b, "y"}) {
			t.Fatal("b is empty; the request must be refused")
		}
	}
	// a's bucket lost only the admitted request's token.
	n := 0
	for range 5 {
		if a.AllowKey("x") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("a allowed %d more, want 2 (refused requests must have spent nothing)", n)
	}
	// A refused request leaves no new key behind.
	if ReserveKeys(KeyStep{a, "fresh"}, KeyStep{b, "y"}) {
		t.Fatal("b is still empty")
	}
	a.mu.Lock()
	_, tracked := a.entries["fresh"]
	a.mu.Unlock()
	if tracked {
		t.Fatal("a refused request left a new entry behind")
	}
}
