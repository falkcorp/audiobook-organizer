// file: internal/server/login_ceiling.go
// version: 4.2.0
// guid: bbbd655c-e45d-48ca-badb-d7b48a4d69e0
// last-edited: 2026-10-22

package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// Aggregate ceilings for password login (the web POST /api/v1/auth/login and
// the Audiobookshelf-compatible POST /login).
//
// The per-visitor failure throttle (absauth.Throttle, shared by both logins) is
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
// Before anything is counted the middleware reads the login body itself, under
// loginBodyLimit and a read deadline (loginBodyReadTimeout), and hands the
// handler a buffered copy: a caller that opens a login and then stalls its body
// holds a connection for a few seconds, not a ceiling slot, and never reaches the
// handler's JSON decoding. Each visitor key may also have at most
// maxLoginInFlightPerVisitor logins in the handlers at once.
//
// What a spent ceiling does is the part that matters, because a ceiling that
// refused every login would hand an anonymous flood the power to lock out every
// user, the owner included. So:
//
//   - Only FAILED logins (401) count. A slot is taken on entry, so parallel
//     guesses cannot all pass a check made before any has failed, and is handed
//     back for anything but a 401. A successful login (2xx) refunds only the
//     failures that SAME visitor made against the SAME account (the handlers
//     report the account with absauth.NoteLoginAccount: the normalised submitted
//     username), from the last 15 minutes, at most absauth.SoftThreshold of them.
//     Someone who mistyped and got in does not leave their misses counting
//     against everyone; a caller with one valid account cannot log in to clear
//     the count and keep guessing at other accounts, whose failures stay counted
//     at the visitor, connector and global levels. absauth.Throttle's per-source
//     budget follows the same rule.
//   - A request carrying a VERIFIED Cloudflare Access identity (a JWT verified
//     against the team's JWKS and AUD, for an allow-listed verified email, the
//     same checks CloudflareAccessAuth applies) is exempt: Cloudflare has
//     authenticated that person, so an anonymous flood cannot starve them. It is
//     still subject to the per-visitor and per-account limits in the handlers,
//     and its failures do not count here.
//   - Once a ceiling is spent, any other login is not refused here by the
//     ceiling itself. It is admitted marked saturated (absauth.MarkLoginSaturated),
//     and the handlers delay it before password work, progressively with the
//     failures already on the submitted username, up to a few seconds (the delay
//     holds no lock). Only a username that has already failed
//     absauth.SoftThreshold times, or a visitor past its own budget, is refused
//     with 429 by the handlers, and the decision is the same whether or not the
//     username exists. Admission of saturated requests is bounded in three ways:
//     PER VISITOR (keyed on the visitor's address, a public IPv6 visitor per /48,
//     see clientSaturatedKey): a visitor holds at most maxSaturatedPerVisitor at
//     once (one while the service holds maxSaturatedSoft saturated requests or
//     more), and a visitor that has already failed while saturated, by a 401 or
//     by the account refusal, is refused outright (429, no delay) until the
//     window ends; SERVICE-WIDE RATE: a token bucket (config.LoginSaturatedLimits,
//     2 logins per second with a burst of 8 unless LOGIN_SATURATED_RATE and
//     LOGIN_SATURATED_BURST say otherwise) that every admitted saturated login
//     spends one token of, checked here BEFORE any user lookup, so a name that
//     exists and one that does not get the same answer; and a hard count
//     (maxSaturatedHard) that protects goroutines. The bucket is what bounds the
//     argon2id derivations a flood of fresh visitors can force (every admitted
//     login, even for an unknown name, costs one): without it each new address
//     was a free derivation. A visitor refused by its own limits spends no token.
//     The trade-off is deliberate: while saturated, a fresh visitor's attempt is
//     NOT always admitted, because the bucket can be empty, and that visitor is
//     told to retry shortly (429, Retry-After). Requests with a verified
//     Cloudflare Access identity are exempt from all of it (they carry no
//     password work for the bucket to bound, and an Audiobookshelf login through
//     Cloudflare Access is unaffected).
//
// The cost that remains: while a ceiling is spent, a stranger's guesses at one
// account can push that account over its soft threshold, which then refuses even
// its owner's password login until the 15-minute window ends (the Cloudflare
// Access path is exempt). Sessions already issued are unaffected, and the state
// is in memory, so a restart clears it. The middleware's own refusals (413, 408,
// 400, 429) are written in the error shape of the door it guards (see
// loginResponder). POST /auth/refresh is deliberately
// outside the ceilings: it does no password work, and a server restart that
// invalidates every refresh token makes every client fail once, which must not
// read as an attack. HTTP Basic auth on the API is a separate path and is not
// throttled here.
const (
	loginConnectorCeiling = 10 * absauth.MaxFailuresPerIP
	loginGlobalCeiling    = 500
	loginCeilingWindow    = absauth.Window
	// loginBodyLimit caps a login body (the real one is a few hundred bytes).
	loginBodyLimit = 4 << 10
	// maxLoginInFlightPerVisitor is how many logins one visitor key may have in the
	// handlers at once, saturated or not.
	maxLoginInFlightPerVisitor = 4
	// maxSaturatedPerVisitor is how many saturated (delayed) requests one visitor
	// key may have in the handlers at once.
	maxSaturatedPerVisitor = 2
	// maxSaturatedSoft is the service-wide saturated count at or above which a
	// visitor is held to one saturated request at a time. A fresh visitor (none in
	// flight) is still admitted.
	maxSaturatedSoft = 256
	// maxSaturatedHard bounds the saturated requests held at once, for goroutine
	// safety only.
	maxSaturatedHard = 4096
	// maxLoginVisitors bounds the per-visitor table.
	maxLoginVisitors = 10000
	// maxLoginVisitorAccounts bounds the accounts tracked per visitor.
	maxLoginVisitorAccounts = 16
)

// loginBodyReadTimeout bounds how long the middleware waits for a login body. A
// variable so tests can shorten it.
var loginBodyReadTimeout = 5 * time.Second

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

// loginFailure is one failed login by a visitor against a known account: when,
// and the bucket windows its slot was spent in, so a refund goes back only into
// windows that are still open.
type loginFailure struct {
	at                     time.Time
	connKey                string
	connStart, globalStart time.Time
}

// loginVisitor is what the ceiling remembers about one visitor key.
type loginVisitor struct {
	// lastSeen is the last write; the table is in write order, so it decides expiry.
	lastSeen time.Time
	// byAccount are the refundable failures per account key, newest last, at most
	// absauth.SoftThreshold each. A failure with no account key is not recorded.
	byAccount map[string][]loginFailure
	// satFailedAt is when the visitor last failed a login while saturated.
	satFailedAt time.Time
	// inFlight is how many logins of the visitor are in the handlers;
	// satInFlight how many of those are saturated.
	inFlight, satInFlight int
}

type loginCeiling struct {
	mu       sync.Mutex
	now      func() time.Time
	global   loginCeilingBucket
	conns    map[string]*loginCeilingBucket
	visitors *absauth.LRU[*loginVisitor]
	// exempt reports whether a request carries a verified, allow-listed Cloudflare
	// Access identity. nil: nothing is exempt. Read on every request.
	exempt    func(*gin.Context) bool
	saturated int // saturated requests currently in the handlers; under mu
	// satBound is the service-wide token bucket on saturated admission. Spent
	// only by an admission (admitSaturated, under mu) so a refused visitor never
	// takes a token.
	satBound *rate.Limiter
}

// satVerdict is admitSaturated's answer.
type satVerdict int

const (
	satAdmitted        satVerdict = iota
	satRefusedVisitor             // the visitor's own limits: failed this window, or too many in flight
	satRefusedCapacity            // the service-wide bucket or the hard bound
)

// startupLoginSaturated is the startup half of the saturated-admission bucket:
// it refuses to start on an unusable LOGIN_SATURATED_RATE / LOGIN_SATURATED_BURST
// (a typo must neither remove nor tighten the bound unnoticed) and logs the
// effective limits.
func startupLoginSaturated() error {
	r, b, err := config.LoginSaturatedLimits()
	if err != nil {
		return fmt.Errorf("login saturation limits: %w", err)
	}
	lifecycleLog.Info("saturated password logins (while the login failure ceilings are spent) are admitted at %v per second, burst %d", r, b)
	return nil
}

// loginThrottle returns the one failed-login throttle both password logins share,
// so a guess at an account through either door counts toward the same
// per-account counter and the same per-source budget.
func (s *Server) loginThrottle() *absauth.Throttle {
	s.loginThrottleOnce.Do(func() { s.loginThrottleState = absauth.NewThrottle() })
	return s.loginThrottleState
}

// loginCeiling returns the one ceiling both password logins share, so the global
// bucket counts web and ABS failures together.
func (s *Server) loginCeiling() *loginCeiling {
	s.loginCeilingOnce.Do(func() {
		s.loginCeilingState = newLoginCeiling()
		// An unusable setting stops the service starting (startupLoginSaturated);
		// the defaults here only cover a ceiling built without that check.
		if r, b, err := config.LoginSaturatedLimits(); err == nil {
			s.loginCeilingState.setSaturatedBound(rate.Limit(r), b)
		}
		// Looked up per request, not copied here: buildOAuthWiring sets
		// loginCeilingExempt, and this may run first.
		s.loginCeilingState.exempt = func(c *gin.Context) bool {
			fn := s.loginCeilingExempt
			return fn != nil && fn(c)
		}
	})
	return s.loginCeilingState
}

func newLoginCeiling() *loginCeiling {
	l := &loginCeiling{now: time.Now, conns: map[string]*loginCeilingBucket{}}
	l.setSaturatedBound(rate.Limit(config.DefaultLoginSaturatedRate), config.DefaultLoginSaturatedBurst)
	visitors := absauth.NewLRU[*loginVisitor](maxLoginVisitors)
	// A visitor with a login in the handlers, or one refused for the rest of the
	// window, is state the bound must not evict in favour of a stream of new
	// addresses (evicting the first would let leave() find no entry; evicting the
	// second would hand a refused visitor a fresh start), and an entry that cannot
	// be swept yet must not stand in front of the ones that can. Such entries live
	// on the LRU's pinned list; the bound evicts idle entries first.
	// The refusal pins only for its window: after it the visitor is admitted
	// again, so the entry is ordinary and must be evictable.
	visitors.SetPinned(func(v *loginVisitor) bool {
		return v.inFlight > 0 || v.satInFlight > 0 ||
			(!v.satFailedAt.IsZero() && l.now().Sub(v.satFailedAt) <= loginCeilingWindow)
	})
	l.visitors = visitors
	return l
}

// setSaturatedBound replaces the service-wide saturated-admission bucket. Call
// before the ceiling serves requests. rate.Inf disables the bound.
func (l *loginCeiling) setSaturatedBound(limit rate.Limit, burst int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.satBound = rate.NewLimiter(limit, burst)
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

// visitorLocked returns the state of visitor, creating it, and marks it the most
// recently written entry. Entries nobody has written for a whole window (and
// with nothing in flight) are swept from the cold end of each of the table's
// lists first, so the table holds what is live rather than filling to the bound
// and evicting, and an entry with a login in flight (on the pinned list) never
// stops the sweep reaching the idle ones; at the bound the least recently
// written idle entry goes, in O(1). Every in-place change to an entry is
// followed by touchLocked, which re-files it. Caller holds l.mu.
func (l *loginCeiling) visitorLocked(visitor string) *loginVisitor {
	now := l.now()
	l.visitors.Sweep(func(v *loginVisitor) bool {
		return v.inFlight == 0 && v.satInFlight == 0 && now.Sub(v.lastSeen) > loginCeilingWindow
	})
	v, ok := l.visitors.Get(visitor)
	if !ok {
		v = &loginVisitor{byAccount: map[string][]loginFailure{}}
	}
	v.lastSeen = now
	l.visitors.Put(visitor, v)
	return v
}

// touchLocked re-files visitor's entry after an in-place change to the fields the
// pin predicate reads (in flight, saturated in flight, refused), and marks it
// written. Caller holds l.mu.
func (l *loginCeiling) touchLocked(visitor string, v *loginVisitor) {
	v.lastSeen = l.now()
	l.visitors.Touch(visitor)
}

// failed records that the request holding t was a failed login by visitor
// against account (the normalised submitted username): its slot stays spent and
// is remembered so a later success on that same account can take it back.
func (l *loginCeiling) failed(visitor, account string, t loginCeilingTicket) {
	if visitor == "" || account == "" || !t.held {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.visitorLocked(visitor)
	if _, ok := v.byAccount[account]; !ok && len(v.byAccount) >= maxLoginVisitorAccounts {
		return // not refundable; still counted
	}
	fs := append(v.byAccount[account], loginFailure{at: l.now(), connKey: t.connKey, connStart: t.connStart, globalStart: t.globalStart})
	if len(fs) > absauth.SoftThreshold {
		fs = fs[len(fs)-absauth.SoftThreshold:]
	}
	v.byAccount[account] = fs
}

// succeeded hands back the success's own slot and, for the same visitor and the
// same account only, the failures from the last window (at most
// absauth.SoftThreshold, into windows still open). Failures against any other
// account stay counted.
func (l *loginCeiling) succeeded(visitor, account string, t loginCeilingTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(t, 1)
	if account == "" {
		return
	}
	v, ok := l.visitors.Get(visitor)
	if !ok {
		return
	}
	now := l.now()
	for _, f := range v.byAccount[account] {
		if now.Sub(f.at) <= loginCeilingWindow {
			l.releaseLocked(loginCeilingTicket{held: true, connKey: f.connKey, connStart: f.connStart, globalStart: f.globalStart}, 1)
		}
	}
	delete(v.byAccount, account)
}

// enter admits one login from visitor into the handlers, refusing the visitor's
// fifth concurrent one (maxLoginInFlightPerVisitor). The returned func must be
// called when the request ends.
func (l *loginCeiling) enter(visitor string) (leave func(), ok bool) {
	if visitor == "" {
		visitor = "?" // no parseable address: one shared key
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.visitorLocked(visitor)
	if v.inFlight >= maxLoginInFlightPerVisitor {
		return nil, false
	}
	v.inFlight++
	l.touchLocked(visitor, v)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if cur, ok := l.visitors.Get(visitor); ok && cur == v {
			v.inFlight--
			l.touchLocked(visitor, v)
		}
	}, true
}

// admitSaturated decides whether a saturated request from visitor (the
// saturated key, loginSaturatedKey; already counted by enter under its own key)
// may go on to be delayed in the handlers. It refuses a visitor that already
// failed while saturated in this window (satRefusedVisitor), one with
// maxSaturatedPerVisitor already in flight (one when the service-wide count is
// at or above maxSaturatedSoft; satRefusedVisitor), anything past
// maxSaturatedHard (satRefusedCapacity, goroutine safety) and, once the visitor
// passed its own limits, a request that finds the service-wide bucket empty
// (satRefusedCapacity). The token is taken last and only for an admission, so a
// refused visitor never spends one, and a caller flooding from many addresses
// is held to the bucket's rate however many fresh keys it has. The returned func
// must be called when the request ends.
func (l *loginCeiling) admitSaturated(visitor string) (release func(), verdict satVerdict) {
	if visitor == "" {
		visitor = "?"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.saturated >= maxSaturatedHard {
		return nil, satRefusedCapacity
	}
	v := l.visitorLocked(visitor)
	if !v.satFailedAt.IsZero() && l.now().Sub(v.satFailedAt) <= loginCeilingWindow {
		return nil, satRefusedVisitor
	}
	limit := maxSaturatedPerVisitor
	if l.saturated >= maxSaturatedSoft {
		limit = 1
	}
	if v.satInFlight >= limit {
		return nil, satRefusedVisitor
	}
	if !l.satBound.AllowN(l.now(), 1) {
		return nil, satRefusedCapacity
	}
	v.satInFlight++
	l.saturated++
	l.touchLocked(visitor, v)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.saturated--
		if cur, ok := l.visitors.Get(visitor); ok && cur == v {
			v.satInFlight--
			l.touchLocked(visitor, v)
		}
	}, satAdmitted
}

// saturatedFailed records that visitor failed a login while saturated.
func (l *loginCeiling) saturatedFailed(visitor string) {
	if visitor == "" {
		visitor = "?"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.visitorLocked(visitor)
	v.satFailedAt = l.now()
	l.touchLocked(visitor, v)
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

// loginResponder writes the middleware's own refusals (413, 408, 400, 429) in
// the error shape of the door it guards: the web API's envelope, or the
// Audiobookshelf-compatible {"error": msg} that ABS clients parse. code is the
// web envelope's machine code and is ignored by shapes without one.
type loginResponder func(c *gin.Context, status int, msg, code string)

// respondWebLoginError is the web API's error envelope.
func respondWebLoginError(c *gin.Context, status int, msg, code string) {
	httputil.RespondWithError(c, status, msg, code)
}

// respondABSLoginError is the Audiobookshelf error shape, the one the ABS
// handlers themselves answer with.
func respondABSLoginError(c *gin.Context, status int, msg, _ string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// readLoginBody reads the request body under loginBodyLimit and
// loginBodyReadTimeout and puts a buffered copy back, so what the handler decodes
// is bytes already in memory. It answers (through respond) and aborts itself when
// the body is too large (413), not delivered in time (408) or unreadable (400),
// and reports whether the request may go on.
func readLoginBody(c *gin.Context, respond loginResponder) bool {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return true
	}
	fail := func(status int, msg, code string) bool {
		// The body did not reach EOF, so the read deadline stays in force (it also
		// bounds the drain net/http does before answering). HTTP/1 closes the
		// connection after the answer; HTTP/2 and HTTP/3 reset just the stream.
		if c.Request.ProtoMajor == 1 {
			c.Header("Connection", "close")
		}
		respond(c, status, msg, code)
		c.Abort()
		return false
	}
	if c.Request.ContentLength > loginBodyLimit {
		return fail(http.StatusRequestEntityTooLarge, "request body too large", "PAYLOAD_TOO_LARGE")
	}
	restore := setBodyReadDeadline(c, loginBodyReadTimeout)
	buf, err := io.ReadAll(io.LimitReader(c.Request.Body, loginBodyLimit+1))
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return fail(http.StatusRequestTimeout, "timed out reading the request body", "REQUEST_TIMEOUT")
	case err != nil:
		return fail(http.StatusBadRequest, "could not read the request body", "BAD_REQUEST")
	case len(buf) > loginBodyLimit:
		return fail(http.StatusRequestEntityTooLarge, "request body too large", "PAYLOAD_TOO_LARGE")
	}
	restore() // EOF reached: a keep-alive connection's next request is unaffected
	c.Request.Body = io.NopCloser(bytes.NewReader(buf))
	return true
}

// middleware applies the ceilings to the requests match accepts (all of them
// when match is nil). respond writes the middleware's own refusals; nil means the
// web API's envelope.
func (l *loginCeiling) middleware(match func(*gin.Context) bool, respond loginResponder) gin.HandlerFunc {
	if respond == nil {
		respond = respondWebLoginError
	}
	return func(c *gin.Context) {
		if match != nil && !match(c) {
			c.Next()
			return
		}
		// Body first, before anything is reserved or counted: a stalled or oversized
		// body must not hold a ceiling slot or reach the handler.
		if !readLoginBody(c, respond) {
			return
		}
		if l.exempt != nil && l.exempt(c) {
			c.Next()
			return
		}
		visitor := loginThrottleKey(c)
		leave, entered := l.enter(visitor)
		if !entered {
			c.Header("Retry-After", "5")
			respond(c, http.StatusTooManyRequests, "too many simultaneous login attempts — try again shortly", "TOO_MANY_REQUESTS")
			c.Abort()
			return
		}
		defer leave()
		ticket, ok, warn := l.reserve(loginCeilingConnectorKey(c))
		if !ok {
			if warn != "" {
				clientTrustLog.Warn("login failure ceiling reached (%s): further password logins are delayed, and refused for a username past %d failures of its own, until its window ends", warn, absauth.SoftThreshold)
			}
			// Saturated admission is keyed coarser than the per-visitor throttle
			// (a public IPv6 visitor per /48) and decided HERE, before any handler
			// looks up the submitted user, so every name gets the same answer.
			satKey := loginSaturatedKey(c)
			done, verdict := l.admitSaturated(satKey)
			switch verdict {
			case satRefusedVisitor:
				c.Header("Retry-After", absauth.RetryAfterSeconds)
				respond(c, http.StatusTooManyRequests, "too many failed login attempts — try again later", "TOO_MANY_REQUESTS")
				c.Abort()
				return
			case satRefusedCapacity:
				c.Header("Retry-After", "5")
				respond(c, http.StatusTooManyRequests, "too many login attempts right now — try again shortly", "TOO_MANY_REQUESTS")
				c.Abort()
				return
			}
			defer done()
			absauth.MarkLoginSaturated(c)
			c.Next()
			// A 401 and the handler's account refusal are both failures while
			// saturated: either way this visitor gets no further attempt this window.
			if c.Writer.Status() == http.StatusUnauthorized || absauth.LoginSaturationRefused(c) {
				l.saturatedFailed(satKey)
			}
			return
		}
		c.Next()
		account := absauth.LoginAccount(c)
		switch code := c.Writer.Status(); {
		case code == http.StatusUnauthorized:
			l.failed(visitor, account, ticket)
		case code >= 200 && code < 300:
			l.succeeded(visitor, account, ticket)
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
