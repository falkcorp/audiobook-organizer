// file: internal/server/login_ceiling.go
// version: 2.0.0
// guid: bbbd655c-e45d-48ca-badb-d7b48a4d69e0
// last-edited: 2026-10-21

package server

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Aggregate ceilings for password login (the web POST /api/v1/auth/login and
// the Audiobookshelf-compatible POST /login).
//
// The per-visitor failure throttle (handlers.AuthHandler, absauth.Throttle) is
// keyed on the visitor address (a public IPv6 visitor per /64), which is what
// stops one caller locking out everyone behind a tunnel connector. It does
// nothing against a caller with many addresses (a botnet): each address brings a
// fresh budget of password guesses. These ceilings cap the total, whatever the
// addresses:
//
//   - per connector: failures through one tunnel connector, 10x the visitor
//     budget, and
//   - global: failures through the whole service.
//
// What a spent ceiling does is the part that matters, because a ceiling that
// refused every login would hand an anonymous flood the power to lock out every
// user, the owner included. So:
//
//   - Only FAILED logins (401) count. A slot is taken on entry, so parallel
//     guesses cannot all pass a check made before any has failed, and is handed
//     back for anything but a 401. A successful login (2xx) also refunds the
//     failures that visitor key contributed this window.
//   - A request carrying a VERIFIED Cloudflare Access identity (a JWT verified
//     against the team's JWKS and AUD, for an allow-listed verified email, the
//     same checks CloudflareAccessAuth applies) is exempt: Cloudflare has
//     authenticated that person, so an anonymous flood cannot starve them. It is
//     still subject to the per-visitor and per-account limits in the handlers,
//     and its failures do not count here.
//   - Once a ceiling is spent, any other login is not refused here. It is
//     admitted marked saturated (absauth.MarkLoginSaturated), and the handlers
//     delay it before password work, progressively with the failures already on
//     its account, up to a few seconds (the delay holds no lock). Only an account
//     that has already failed absauth.SoftThreshold times, or a visitor past its
//     own budget, is refused with 429. At most maxSaturatedInFlight such requests
//     are in the handlers at once, so the delays cannot pile up unbounded; the
//     rest get 429.
//
// The cost that remains: while a ceiling is spent, a stranger's guesses at one
// account can push that account over its soft threshold, which then refuses even
// its owner's password login until the 15-minute window ends (the Cloudflare
// Access path is exempt). Sessions already issued are unaffected, and the state
// is in memory, so a restart clears it. POST /auth/refresh is deliberately
// outside the ceilings: it does no password work, and a server restart that
// invalidates every refresh token makes every client fail once, which must not
// read as an attack.
const (
	loginConnectorCeiling = 10 * absauth.MaxFailuresPerIP
	loginGlobalCeiling    = 500
	loginCeilingWindow    = absauth.Window
	// maxSaturatedInFlight bounds requests held in the delay path at once.
	maxSaturatedInFlight = 256
	// maxLoginVisitors bounds the per-visitor contribution table.
	maxLoginVisitors = 10000
)

type loginCeilingBucket struct {
	count  int
	start  time.Time
	warned bool
}

// loginCeilingTicket is what an admitted request holds so it can hand its slot
// back: the buckets it counted in, and the window each was in (a slot is not
// handed back into a window that has since rolled over). A zero ticket holds
// nothing (saturated or exempt requests).
type loginCeilingTicket struct {
	held                   bool
	connKey                string
	connStart, globalStart time.Time
}

// loginVisitor is what one visitor key has contributed to the buckets in their
// current windows, so a success can take it back.
type loginVisitor struct {
	n           int
	first       time.Time
	connKey     string
	connStart   time.Time
	globalStart time.Time
}

type loginCeiling struct {
	mu       sync.Mutex
	now      func() time.Time
	global   loginCeilingBucket
	conns    map[string]*loginCeilingBucket
	visitors map[string]*loginVisitor
	// exempt reports whether a request carries a verified, allow-listed Cloudflare
	// Access identity. nil: nothing is exempt.
	exempt    func(*gin.Context) bool
	saturated atomic.Int32 // saturated requests currently in the handlers
}

// loginCeiling returns the one ceiling both password logins share, so the global
// bucket counts web and ABS failures together.
func (s *Server) loginCeiling() *loginCeiling {
	s.loginCeilingOnce.Do(func() {
		s.loginCeilingState = newLoginCeiling()
		s.loginCeilingState.exempt = s.loginCeilingExempt
	})
	return s.loginCeilingState
}

func newLoginCeiling() *loginCeiling {
	return &loginCeiling{now: time.Now, conns: map[string]*loginCeilingBucket{}, visitors: map[string]*loginVisitor{}}
}

// reserve takes a slot in the global bucket and, when connKey is not empty, in
// that connector's bucket. When either is spent it takes nothing and reports
// the scope to log (once per bucket window; "" after that).
func (l *loginCeiling) reserve(connKey string) (t loginCeilingTicket, ok bool, warnScope string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	roll := func(b *loginCeilingBucket) {
		if now.Sub(b.start) > loginCeilingWindow {
			*b = loginCeilingBucket{start: now}
		}
	}
	roll(&l.global)
	var cb *loginCeilingBucket
	if connKey != "" {
		cb = l.conns[connKey]
		if cb == nil {
			cb = &loginCeilingBucket{start: now}
			l.conns[connKey] = cb
		}
		roll(cb)
		if cb.count >= loginConnectorCeiling {
			return loginCeilingTicket{}, false, warnOnce(cb, "connector "+connKey)
		}
	}
	if l.global.count >= loginGlobalCeiling {
		return loginCeilingTicket{}, false, warnOnce(&l.global, "global")
	}
	l.global.count++
	t = loginCeilingTicket{held: true, globalStart: l.global.start}
	if cb != nil {
		cb.count++
		t.connKey, t.connStart = connKey, cb.start
	}
	return t, true, ""
}

func warnOnce(b *loginCeilingBucket, scope string) string {
	if b.warned {
		return ""
	}
	b.warned = true
	return scope
}

// releaseLocked hands back the slots t holds. Caller holds l.mu.
func (l *loginCeiling) releaseLocked(t loginCeilingTicket, n int) {
	if !t.held {
		return
	}
	if l.global.start.Equal(t.globalStart) {
		l.global.count = max(0, l.global.count-n)
	}
	if t.connKey != "" {
		if cb := l.conns[t.connKey]; cb != nil && cb.start.Equal(t.connStart) {
			cb.count = max(0, cb.count-n)
		}
	}
}

// release hands back the slots of a request that was not a failed login.
func (l *loginCeiling) release(t loginCeilingTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(t, 1)
}

// failed records that the request holding t was a failed login by visitor: its
// slot stays spent and is remembered against the visitor.
func (l *loginCeiling) failed(visitor string, t loginCeilingTicket) {
	if visitor == "" || !t.held {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.visitors[visitor]
	if v == nil || v.globalStart != t.globalStart || v.connStart != t.connStart {
		// A visitor whose earlier contribution is from a window that has rolled
		// over starts again.
		if v == nil && len(l.visitors) >= maxLoginVisitors {
			l.evictOldestVisitorLocked()
		}
		v = &loginVisitor{first: l.now(), connKey: t.connKey, connStart: t.connStart, globalStart: t.globalStart}
		l.visitors[visitor] = v
	}
	v.n++
}

func (l *loginCeiling) evictOldestVisitorLocked() {
	var oldestKey string
	var oldest time.Time
	for k, v := range l.visitors {
		if oldestKey == "" || v.first.Before(oldest) {
			oldestKey, oldest = k, v.first
		}
	}
	delete(l.visitors, oldestKey)
}

// succeeded hands back the success's own slot and the failures its visitor key
// contributed in the windows still open: a person who mistyped and then got in
// should not leave their misses counting against everyone else.
func (l *loginCeiling) succeeded(visitor string, t loginCeilingTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(t, 1)
	if v := l.visitors[visitor]; v != nil {
		l.releaseLocked(loginCeilingTicket{held: true, connKey: v.connKey, connStart: v.connStart, globalStart: v.globalStart}, v.n)
		delete(l.visitors, visitor)
	}
}

// loginCeilingConnectorKey is the bucket key for a request that arrived through
// a configured tunnel connector, "" for any other peer (direct callers are
// covered by the per-visitor throttle and the global ceiling).
func loginCeilingConnectorKey(c *gin.Context) string {
	if !tunnelConnectorPeers().contains(c.Request.RemoteAddr) {
		return ""
	}
	if a, ok := peerKeyAddr(c); ok {
		return a.String()
	}
	return ""
}

// middleware applies the ceilings to the requests match accepts (all of them
// when match is nil).
func (l *loginCeiling) middleware(match func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if match != nil && !match(c) {
			c.Next()
			return
		}
		if l.exempt != nil && l.exempt(c) {
			c.Next()
			return
		}
		ticket, ok, warn := l.reserve(loginCeilingConnectorKey(c))
		if !ok {
			if warn != "" {
				clientTrustLog.Warn("login failure ceiling reached (%s): further password logins are delayed, and refused for an account past %d failures of its own, until its window ends", warn, absauth.SoftThreshold)
			}
			if l.saturated.Add(1) > maxSaturatedInFlight {
				l.saturated.Add(-1)
				c.Header("Retry-After", "60")
				httputil.RespondWithError(c, http.StatusTooManyRequests, "too many failed login attempts — try again later", "TOO_MANY_REQUESTS")
				c.Abort()
				return
			}
			defer l.saturated.Add(-1)
			absauth.MarkLoginSaturated(c)
			c.Next()
			return
		}
		visitor := loginThrottleKey(c)
		c.Next()
		switch code := c.Writer.Status(); {
		case code == http.StatusUnauthorized:
			l.failed(visitor, ticket)
		case code >= 200 && code < 300:
			l.succeeded(visitor, ticket)
		default:
			l.release(ticket)
		}
	}
}

// newAccessExemption is the ceilings' exemption test: the request carries a
// Cloudflare Access assertion (header, or the CF_Authorization cookie browsers
// deliver) that verifier accepts (signature, issuer, expiry, AUD) for a verified
// email cfg allows. A service-token assertion names no person and is not exempt.
// The answer is kept on the request.
func newAccessExemption(verifier servermiddleware.CFAssertionVerifier, cfg *oauth.Config) func(*gin.Context) bool {
	const ctxKey = "login_ceiling_access_exempt"
	if verifier == nil || cfg == nil {
		return nil
	}
	return func(c *gin.Context) bool {
		if v, ok := c.Get(ctxKey); ok {
			b, _ := v.(bool)
			return b
		}
		raw := c.GetHeader(oauth.CFAccessHeader)
		if raw == "" {
			if ck, err := c.Cookie("CF_Authorization"); err == nil {
				raw = ck
			}
		}
		exempt := false
		if raw != "" {
			if claims, err := verifier.Verify(c.Request.Context(), raw); err == nil && claims != nil {
				exempt = claims.EmailVerified && cfg.IsEmailAllowed(claims.Email)
			}
		}
		c.Set(ctxKey, exempt)
		return exempt
	}
}
