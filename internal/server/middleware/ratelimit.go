// file: internal/server/middleware/ratelimit.go
// version: 1.1.0
// guid: 1331705a-85cb-4158-92f5-5ce203d8a0e7
// last-edited: 2026-05-01

package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiter is a lightweight per-IP token bucket limiter.
type IPRateLimiter struct {
	mu             sync.Mutex
	entries        map[string]*limiterEntry
	requestsPerMin int
	burst          int
	idleTTL        time.Duration
}

func NewIPRateLimiter(requestsPerMinute int, burst int) *IPRateLimiter {
	if requestsPerMinute < 1 {
		requestsPerMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &IPRateLimiter{
		entries:        make(map[string]*limiterEntry),
		requestsPerMin: requestsPerMinute,
		burst:          burst,
		idleTTL:        15 * time.Minute,
	}
}

func (r *IPRateLimiter) limiterForIP(ip string) *rate.Limiter {
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	for key, entry := range r.entries {
		if now.Sub(entry.lastSeen) > r.idleTTL {
			delete(r.entries, key)
		}
	}

	entry, ok := r.entries[ip]
	if !ok {
		perSecond := float64(r.requestsPerMin) / 60.0
		entry = &limiterEntry{
			limiter:  rate.NewLimiter(rate.Limit(perSecond), r.burst),
			lastSeen: now,
		}
		r.entries[ip] = entry
		return entry.limiter
	}

	entry.lastSeen = now
	return entry.limiter
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
