// file: internal/server/login_ceiling.go
// version: 1.0.0
// guid: bbbd655c-e45d-48ca-badb-d7b48a4d69e0
// last-edited: 2026-10-21

package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/gin-gonic/gin"
)

// Aggregate ceilings for password login (the web POST /api/v1/auth/login and
// the Audiobookshelf-compatible POST /login).
//
// The per-visitor failure throttle (handlers.AuthHandler, absauth.Throttle) is
// keyed on the visitor address, which is what stops one caller locking out
// everyone behind a tunnel connector. It does nothing against a caller that has
// many addresses (a botnet, or a /48 of IPv6 when only the /64 is bucketed): each
// address brings a fresh budget of password guesses. These ceilings cap the
// total, whatever the addresses:
//
//   - per connector: failures through one tunnel connector, 10x the visitor
//     budget, so a few abusive visitors cannot exhaust what the other
//     connectors carry, and
//   - global: failures through the whole service.
//
// Both are checked BEFORE any password work (like the bootstrap and invite
// ceilings), and count failures plus attempts still in flight: a slot is taken
// on entry and handed back when the answer is anything but a 401, so a flood of
// parallel guesses cannot all pass a check made before any of them failed, and
// successful logins cost nothing.
//
// Tradeoff, as with the bootstrap and invite ceilings: a flood that spends a
// ceiling also refuses the owner's password logins (429) until the window ends.
// Sessions already issued are unaffected. The state is in memory, so a restart
// clears it. POST /auth/refresh is deliberately outside the ceilings: it does no
// password work, and a server restart that invalidates every refresh token makes
// every client fail once, which must not read as an attack.
const (
	loginConnectorCeiling = 10 * absauth.MaxFailuresPerIP
	loginGlobalCeiling    = 500
	loginCeilingWindow    = absauth.Window
)

type loginCeilingBucket struct {
	count  int
	start  time.Time
	warned bool
}

// loginCeilingTicket is what an admitted request holds so it can hand its slot
// back: the buckets it counted in, and the window each was in (a slot is not
// handed back into a window that has since rolled over).
type loginCeilingTicket struct {
	connKey                string
	connStart, globalStart time.Time
}

type loginCeiling struct {
	mu     sync.Mutex
	now    func() time.Time
	global loginCeilingBucket
	conns  map[string]*loginCeilingBucket
}

// loginCeiling returns the one ceiling both password logins share, so the global
// bucket counts web and ABS failures together.
func (s *Server) loginCeiling() *loginCeiling {
	s.loginCeilingOnce.Do(func() { s.loginCeilingState = newLoginCeiling() })
	return s.loginCeilingState
}

func newLoginCeiling() *loginCeiling {
	return &loginCeiling{now: time.Now, conns: map[string]*loginCeilingBucket{}}
}

// reserve takes a slot in the global bucket and, when connKey is not empty, in
// that connector's bucket. A refusal takes nothing.
func (l *loginCeiling) reserve(connKey string) (loginCeilingTicket, bool, string) {
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
			return loginCeilingTicket{}, false, l.warnLocked(cb, "connector "+connKey)
		}
	}
	if l.global.count >= loginGlobalCeiling {
		return loginCeilingTicket{}, false, l.warnLocked(&l.global, "global")
	}
	if l.global.start.IsZero() {
		l.global.start = now
	}
	l.global.count++
	t := loginCeilingTicket{globalStart: l.global.start}
	if cb != nil {
		cb.count++
		t.connKey, t.connStart = connKey, cb.start
	}
	return t, true, ""
}

// warnLocked returns the scope to log, once per bucket window.
func (l *loginCeiling) warnLocked(b *loginCeilingBucket, scope string) string {
	if b.warned {
		return ""
	}
	b.warned = true
	return scope
}

// release hands back the slots of a request that was not a failed login.
func (l *loginCeiling) release(t loginCeilingTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.global.count > 0 && l.global.start.Equal(t.globalStart) {
		l.global.count--
	}
	if t.connKey != "" {
		if cb := l.conns[t.connKey]; cb != nil && cb.count > 0 && cb.start.Equal(t.connStart) {
			cb.count--
		}
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
// when match is nil). A request is a failure when it is answered 401.
func (l *loginCeiling) middleware(match func(*gin.Context) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if match != nil && !match(c) {
			c.Next()
			return
		}
		ticket, ok, warn := l.reserve(loginCeilingConnectorKey(c))
		if !ok {
			if warn != "" {
				clientTrustLog.Warn("login failure ceiling reached (%s): password logins are refused with 429 until its window ends", warn)
			}
			c.Header("Retry-After", "900")
			httputil.RespondWithError(c, http.StatusTooManyRequests, "too many failed login attempts — try again later", "TOO_MANY_REQUESTS")
			c.Abort()
			return
		}
		c.Next()
		if c.Writer.Status() != http.StatusUnauthorized {
			l.release(ticket)
		}
	}
}
