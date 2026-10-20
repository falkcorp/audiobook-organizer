// file: internal/server/middleware/ratelimit.go
// version: 1.5.0
// guid: 1331705a-85cb-4158-92f5-5ce203d8a0e7
// last-edited: 2026-10-20

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
	// doneCh is closed when the sweeper goroutine exits (the Start context was
	// cancelled). Tests wait on it to prove cancellation ends the goroutine.
	doneCh chan struct{}
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
	l, _ := r.limiterForKey(ip)
	return l
}

// limiterForKey is limiterForIP that also reports whether this call created
// the key's entry.
func (r *IPRateLimiter) limiterForKey(ip string) (*rate.Limiter, bool) {
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
		return entry.limiter, false
	}

	// Bound the map: evict the least-recently-seen entry before inserting.
	if r.maxEntries > 0 {
		for r.lru.Len() >= r.maxEntries {
			r.removeLocked(r.lru.Back())
		}
	}

	entry := &limiterEntry{ip: ip, limiter: r.newLimiter(), lastSeen: now}
	r.entries[ip] = r.lru.PushFront(entry)
	return entry.limiter, true
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

// Start launches the background sweeper. It runs until ctx is cancelled and
// is safe to call at most once (subsequent calls are no-ops). The server wires
// ctx to its background context so shutdown stops the goroutine. A nil ctx is
// treated as a context that is never cancelled, so the sweeper then runs for
// the life of the process; tests that build a bare Server without a background
// context rely on this.
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
		case <-ticker.C:
			r.sweep()
		}
	}
}

// AllowKey reports whether one more request may proceed for an arbitrary key, for
// callers whose meaningful client identity is not c.ClientIP() (behind the shared
// reverse proxies every request has the same RemoteAddr). Same buckets and same
// least-recently-seen bound as the per-IP path; keys share one namespace, so
// callers should prefix them. Idle keys are swept only if the limiter was
// Start()ed; otherwise they are reclaimed by that bound (defaultMaxEntries) or
// refreshed on their next lookup, exactly as for IPs.
func (r *IPRateLimiter) AllowKey(key string) bool {
	if key == "" {
		key = "unknown"
	}
	return r.limiterForIP(key).Allow()
}

// KeyReservation is one token ReserveKey took from a key's bucket. Cancel
// gives it back; a zero KeyReservation is a no-op.
type KeyReservation struct {
	lim     *IPRateLimiter
	key     string
	bucket  *rate.Limiter
	res     *rate.Reservation
	at      time.Time // when the token was reserved
	created bool      // ReserveKey created the key's entry
}

// ReserveKey takes one token from key's bucket if one is available right now,
// atomically under the bucket's own lock, so concurrent callers can never
// take more tokens than the bucket holds. ok is false, and nothing is held,
// when the bucket is empty. Callers that stack several limiters reserve from
// each in turn and Cancel the ones already taken when a later one refuses
// (ReserveKeys), so a refused request spends nothing.
func (r *IPRateLimiter) ReserveKey(key string) (KeyReservation, bool) {
	if key == "" {
		key = "unknown"
	}
	bucket, created := r.limiterForKey(key)
	now := time.Now()
	res := bucket.ReserveN(now, 1)
	if !res.OK() {
		r.dropIfUntouched(key, bucket, created)
		return KeyReservation{}, false
	}
	if res.DelayFrom(now) > 0 {
		res.CancelAt(now)
		r.dropIfUntouched(key, bucket, created)
		return KeyReservation{}, false
	}
	return KeyReservation{lim: r, key: key, bucket: bucket, res: res, at: now, created: created}, true
}

// Cancel returns the reserved token. When the reservation created the key's
// entry and nobody else has spent from it since, the entry is dropped again,
// so a refused request leaves no new key behind.
func (k KeyReservation) Cancel() {
	if k.res == nil {
		return
	}
	// CancelAt the reservation's own time: rate.Reservation.Cancel (now)
	// restores nothing for a token that was available at once, because its
	// time to act has already passed. At its own time the full token comes
	// back, less whatever later reservations on the bucket took meanwhile.
	k.res.CancelAt(k.at)
	k.lim.dropIfUntouched(k.key, k.bucket, k.created)
}

// dropIfUntouched removes key's entry when it was created for a reservation
// that did not go ahead and its bucket is still full (no other request spent
// from it in between), which is exactly the state an unseen key would get.
func (r *IPRateLimiter) dropIfUntouched(key string, bucket *rate.Limiter, created bool) {
	if !created {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	el, ok := r.entries[key]
	if !ok || el.Value.(*limiterEntry).limiter != bucket {
		return
	}
	if bucket.Tokens() >= float64(r.burst)-1e-9 {
		r.removeLocked(el)
	}
}

// KeyStep is one limiter and key for ReserveKeys.
type KeyStep struct {
	Limiter *IPRateLimiter
	Key     string
}

// ReserveKeys reserves one token from every step, in order. When a step's
// bucket is empty it cancels every reservation already taken and returns
// false: the request spends nothing anywhere. On success the tokens stay
// spent.
func ReserveKeys(steps ...KeyStep) bool {
	taken := make([]KeyReservation, 0, len(steps))
	for _, st := range steps {
		res, ok := st.Limiter.ReserveKey(st.Key)
		if !ok {
			CancelReservations(taken)
			return false
		}
		taken = append(taken, res)
	}
	return true
}

// CancelReservations cancels each reservation, last taken first.
func CancelReservations(rs []KeyReservation) {
	for i := len(rs) - 1; i >= 0; i-- {
		rs[i].Cancel()
	}
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
