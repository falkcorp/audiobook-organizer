// file: internal/server/middleware/ratelimit.go
// version: 1.2.0
// guid: 1331705a-85cb-4158-92f5-5ce203d8a0e7
// last-edited: 2026-09-11

package middleware

import (
	"container/list"
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

const (
	// defaultIdleTTL is how long an IP may go unseen before its bucket is
	// discarded. Unchanged from the original implementation.
	defaultIdleTTL = 15 * time.Minute

	// defaultSweepInterval is how often the background sweeper evicts idle
	// entries. It is deliberately much shorter than defaultIdleTTL so an
	// idle entry lingers at most one interval past its TTL.
	defaultSweepInterval = time.Minute

	// defaultMaxEntries bounds the number of distinct IPs tracked at once.
	// When the cap is hit the least-recently-seen entry is evicted, so an
	// IP flood (or IPv6 rotation) cannot grow the map without limit. Each
	// entry is a few hundred bytes, so the default costs a few MB at most.
	// A single-user audiobook server seeing 10k distinct IPs inside the
	// idle TTL is already under attack; evicting the oldest bucket only
	// hands that IP a fresh (full) burst, which is what a brand-new IP gets
	// anyway.
	defaultMaxEntries = 10_000
)

// limiterEntry is one tracked IP. It lives in both r.entries (for O(1)
// lookup by IP) and r.lru (ordered most-recently-seen first, for O(1)
// eviction of the oldest entry).
type limiterEntry struct {
	ip       string
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiter is a lightweight per-IP token bucket limiter.
//
// The request path (limiterForIP) is a single map lookup plus an O(1) LRU
// move under a short critical section; it never iterates the map. Idle
// entries are evicted by a background sweeper (Start) that walks the LRU
// from the least-recently-seen end and stops at the first fresh entry, so
// each sweep costs O(evicted), not O(tracked). The map is additionally
// bounded by maxEntries, evicting least-recently-seen on insert. Before
// SV-04 every request swept the whole map under one mutex, so the
// abuse-mitigation control itself degraded as the number of distinct
// client IPs grew.
type IPRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*list.Element // ip -> element whose Value is *limiterEntry
	lru     *list.List               // front = most recently seen, back = least

	requestsPerMin int
	burst          int
	idleTTL        time.Duration
	sweepInterval  time.Duration
	maxEntries     int

	// now is injectable so tests can drive the clock deterministically.
	now func() time.Time

	// sweeps counts completed background/explicit sweeps; tests use it to
	// prove the request path never triggers one.
	sweeps atomic.Int64

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewIPRateLimiter builds a limiter allowing requestsPerMinute per client IP
// with the given burst. Values below 1 are clamped to 1. Call Start to run
// the idle-entry sweeper; without it the map is still bounded by the
// least-recently-seen cap, but idle entries are only reclaimed by that cap
// or by a stale-on-lookup refresh.
func NewIPRateLimiter(requestsPerMinute int, burst int) *IPRateLimiter {
	if requestsPerMinute < 1 {
		requestsPerMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &IPRateLimiter{
		entries:        make(map[string]*list.Element),
		lru:            list.New(),
		requestsPerMin: requestsPerMinute,
		burst:          burst,
		idleTTL:        defaultIdleTTL,
		sweepInterval:  defaultSweepInterval,
		maxEntries:     defaultMaxEntries,
		now:            time.Now,
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
	}
}

func (r *IPRateLimiter) newLimiter() *rate.Limiter {
	perSecond := float64(r.requestsPerMin) / 60.0
	return rate.NewLimiter(rate.Limit(perSecond), r.burst)
}

// limiterForIP returns the token bucket for ip, creating one on first sight.
// Cost is O(1): one map lookup and one list move/insert under r.mu. It
// never iterates r.entries.
func (r *IPRateLimiter) limiterForIP(ip string) *rate.Limiter {
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	if el, ok := r.entries[ip]; ok {
		entry := el.Value.(*limiterEntry)
		// Preserve the pre-SV-04 semantics exactly: an entry that outlived
		// idleTTL used to be deleted on the request path and recreated
		// fresh, so an IP returning after a long idle period always gets a
		// full bucket. The sweeper may not have reached it yet, so refresh
		// it here in O(1).
		if now.Sub(entry.lastSeen) > r.idleTTL {
			entry.limiter = r.newLimiter()
		}
		entry.lastSeen = now
		r.lru.MoveToFront(el)
		return entry.limiter
	}

	// Bound the map: evict the least-recently-seen entry before inserting.
	if r.maxEntries > 0 {
		for r.lru.Len() >= r.maxEntries {
			r.removeLocked(r.lru.Back())
		}
	}

	entry := &limiterEntry{ip: ip, limiter: r.newLimiter(), lastSeen: now}
	r.entries[ip] = r.lru.PushFront(entry)
	return entry.limiter
}

// removeLocked drops el from both the LRU and the map. r.mu must be held.
func (r *IPRateLimiter) removeLocked(el *list.Element) {
	if el == nil {
		return
	}
	entry := el.Value.(*limiterEntry)
	delete(r.entries, entry.ip)
	r.lru.Remove(el)
}

// sweep evicts every entry idle for longer than idleTTL. It walks the LRU
// from the least-recently-seen end and stops at the first fresh entry, so
// its cost is proportional to the number of evictions, not the map size.
// It returns the number of entries evicted.
func (r *IPRateLimiter) sweep() int {
	now := r.now()
	evicted := 0

	r.mu.Lock()
	for el := r.lru.Back(); el != nil; el = r.lru.Back() {
		if now.Sub(el.Value.(*limiterEntry).lastSeen) <= r.idleTTL {
			break
		}
		r.removeLocked(el)
		evicted++
	}
	r.mu.Unlock()

	r.sweeps.Add(1)
	return evicted
}

// Start launches the background sweeper. It runs until ctx is cancelled or
// Stop is called, whichever comes first, and is safe to call at most once
// (subsequent calls are no-ops). The server wires ctx to its background
// context so shutdown stops the goroutine. A nil ctx is treated as a
// context that is never cancelled (Stop remains the only exit); tests that
// build a bare Server without a background context rely on this.
func (r *IPRateLimiter) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.startOnce.Do(func() {
		go r.run(ctx)
	})
}

func (r *IPRateLimiter) run(ctx context.Context) {
	defer close(r.doneCh)
	ticker := time.NewTicker(r.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.sweep()
		}
	}
}

// Stop halts the background sweeper and waits for it to exit. It is
// idempotent and safe to call even if Start was never called.
func (r *IPRateLimiter) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.startOnce.Do(func() {
		// Start was never called: nothing to wait for. Consuming the once
		// here also guarantees a later Start cannot launch a goroutine
		// after Stop.
		close(r.doneCh)
	})
	<-r.doneCh
}

// SweepCount reports how many sweeps have completed. Exposed for tests and
// diagnostics.
func (r *IPRateLimiter) SweepCount() int64 {
	return r.sweeps.Load()
}

// Len reports the number of IPs currently tracked.
func (r *IPRateLimiter) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lru.Len()
}

// Middleware returns a Gin middleware that enforces the configured limit.
func (r *IPRateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if ip == "" {
			ip = "unknown"
		}
		if !r.limiterForIP(ip).Allow() {
			httputil.RespondWithError(c, http.StatusTooManyRequests, "rate limit exceeded", "TOO_MANY_REQUESTS")
			c.Abort()
			return
		}
		c.Next()
	}
}
