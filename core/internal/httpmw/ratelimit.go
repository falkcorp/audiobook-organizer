// file: core/internal/httpmw/ratelimit.go
// version: 1.0.0
// guid: ea223112-e08c-4eb4-9223-9beba44d045a
// last-edited: 2026-11-08

package httpmw

import (
	"container/list"
	"hash/maphash"
	"math"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// limiterIdleTTL is how long an untouched bucket keeps its state; an idle
// bucket is full again, as HEAD's IPRateLimiter (ratelimit.go defaultIdleTTL).
const limiterIdleTTL = 15 * time.Minute

// rateSpec is the compiled rate limit: off, or a token bucket of perMinute
// tokens a minute holding at most burst.
type rateSpec struct {
	on        bool
	perMinute int
	burst     int
}

func newRateSpec(s Settings) rateSpec {
	pm := s.PerMinute
	if pm <= 0 {
		pm = DefaultPerMinute
	}
	b := s.Burst
	if b <= 0 {
		b = max(pm/5, 10)
	}
	return rateSpec{on: s.RateLimit, perMinute: pm, burst: b}
}

// rateLimit is stage 10 (05-http-api §4.2): a token bucket per client
// address from stage 0 (ClientAddr, so a tunnel's visitors are not one
// bucket) and rate class. A refusal is 429 rate_limited with Retry-After,
// RateLimit-Policy and RateLimit (draft-ietf-httpapi-ratelimit-headers).
// RateExempt routes (infra probes, the SPA) run no limit.
func (c *Chain) rateLimit(spec RouteSpec, next http.Handler) http.Handler {
	if spec.Class == "" || spec.Class == "exempt" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs := c.current().rate
		if !rs.on {
			next.ServeHTTP(w, r)
			return
		}
		key := spec.Class + "|" + clientKey(r)
		ok, wait := c.limiter.allow(key, rs)
		if ok {
			next.ServeHTTP(w, r)
			return
		}
		secs := int(math.Ceil(wait.Seconds()))
		if secs < 1 {
			secs = 1
		}
		h := http.Header{}
		h.Set("Retry-After", strconv.Itoa(secs))
		h.Set("RateLimit-Policy", `"`+spec.Class+`";q=`+strconv.Itoa(rs.perMinute)+`;w=60`)
		h.Set("RateLimit", `"`+spec.Class+`";r=0;t=`+strconv.Itoa(secs))
		c.inst.rateLimited(r.Context(), spec.Class)
		c.deps.Problem(w, r, http.StatusTooManyRequests, "rate_limited", h)
	})
}

// clientKey is the bucket key of a request's client: the address stage 0
// resolved, a public IPv6 address by its /64 (one caller owns the whole
// /64), "unknown" when stage 0 resolved none.
func clientKey(r *http.Request) string {
	a, _ := ClientAddr(r.Context())
	if !a.IsValid() {
		return "unknown"
	}
	a = a.WithZone("").Unmap()
	if a.Is6() && !a.IsPrivate() && !a.IsLinkLocalUnicast() && !a.IsLoopback() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}

// limiter is the buckets: GOMAXPROCS shards, each an LRU map capped at its
// share of max clients (05-http-api §9). An idle bucket is reset when next
// touched instead of by a sweeper goroutine; the LRU cap bounds memory.
type limiter struct {
	seed   maphash.Seed
	shards []*limiterShard
	now    func() time.Time
}

type limiterShard struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	max     int
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
}

func newLimiter(maxClients int, now func() time.Time) *limiter {
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		n = 1
	}
	per := max(maxClients/n, 1)
	l := &limiter{seed: maphash.MakeSeed(), now: now}
	for range n {
		l.shards = append(l.shards, &limiterShard{entries: map[string]*list.Element{}, lru: list.New(), max: per})
	}
	return l
}

// allow takes one token from key's bucket, or reports how long until one is
// available.
func (l *limiter) allow(key string, rs rateSpec) (bool, time.Duration) {
	sh := l.shards[maphash.String(l.seed, key)%uint64(len(l.shards))]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := l.now()
	perSec := float64(rs.perMinute) / 60
	var b *bucket
	if el, ok := sh.entries[key]; ok {
		b = el.Value.(*bucket)
		sh.lru.MoveToFront(el)
		if now.Sub(b.last) > limiterIdleTTL {
			b.tokens, b.last = float64(rs.burst), now
		}
	} else {
		for sh.lru.Len() >= sh.max {
			old := sh.lru.Back()
			delete(sh.entries, old.Value.(*bucket).key)
			sh.lru.Remove(old)
		}
		b = &bucket{key: key, tokens: float64(rs.burst), last: now}
		sh.entries[key] = sh.lru.PushFront(b)
	}
	if el := now.Sub(b.last); el > 0 {
		b.tokens = math.Min(float64(rs.burst), b.tokens+el.Seconds()*perSec)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / perSec * float64(time.Second))
}
