// file: core/auth/ceiling.go
// version: 1.1.0
// guid: a42f3d77-303a-4d7b-93d2-3877302aa52a
// last-edited: 2026-11-10

package auth

import (
	"math"
	"sync"
	"time"
)

// Aggregate ceilings for password login (05-authn §4.9; moved from ABO's
// internal/server/login_ceiling.go, whose gin middleware stays in ABO and
// drives this type through Admit).
//
// The per-visitor failure throttle (Throttle) is keyed on the visitor address
// (a public IPv6 visitor per /64), which is what stops one caller locking out
// everyone behind a tunnel connector. It does nothing against a caller with
// many addresses (a botnet): each address brings a fresh budget of password
// guesses. These ceilings cap the total, whatever the addresses:
//
//   - per connector: failures through one tunnel connector, 10x the visitor
//     budget, and
//   - global: failures through the whole service.
//
// Each visitor key may also have at most MaxLoginInFlightPerVisitor logins in
// flight at once.
//
// What a spent ceiling does is the part that matters, because a ceiling that
// refused every login would hand an anonymous flood the power to lock out
// every user, the owner included. So:
//
//   - Only FAILED logins count. A slot is taken on entry, so parallel guesses
//     cannot all pass a check made before any has failed, and is handed back
//     for anything but a failure. A successful login refunds only the failures
//     that SAME visitor made against the SAME account (the normalised
//     submitted username, AccountKey), from the last window, at most
//     DefaultSoftThreshold of them. Someone who mistyped and got in does not
//     leave their misses counting against everyone; a caller with one valid
//     account cannot log in to clear the count and keep guessing at other
//     accounts, whose failures stay counted at the visitor, connector and
//     global levels. Throttle's per-source budget follows the same rule.
//   - Once a ceiling is spent, any other login is not refused by the ceiling
//     itself. It is admitted saturated (LoginAdmission.Saturated), and the
//     caller delays it before password work by Throttle.SaturationPolicy,
//     progressively with the failures already on the submitted username, up
//     to a few seconds. Only a username that has already failed the
//     throttle's SoftThreshold times, or a visitor past its own budget, is refused, and the decision is
//     the same whether or not the username exists. Admission of saturated
//     requests is bounded in three ways: PER VISITOR (the saturated key, a
//     public IPv6 visitor per /48, ClientInfo.saturatedKey): a visitor holds at
//     most MaxSaturatedPerVisitor at once (one while the service holds
//     MaxSaturatedSoft saturated requests or more), and a visitor that has
//     already failed while saturated is refused outright (no delay) until the
//     window ends; SERVICE-WIDE RATE: a token bucket
//     (core.auth.login.saturated_rate / saturated_burst, 2 logins per second
//     with a burst of 8 by default) that every admitted saturated login spends
//     one token of, checked BEFORE any user lookup, so a name that exists and
//     one that does not get the same answer; and a hard count
//     (MaxSaturatedHard) that protects goroutines. The bucket is what bounds
//     the argon2id derivations a flood of fresh visitors can force (every
//     admitted login, even for an unknown name, costs one). A visitor refused
//     by its own limits spends no token. While saturated, a fresh visitor's
//     attempt is NOT always admitted, because the bucket can be empty, and
//     that visitor is told to retry shortly (the time the bucket needs to hold
//     one token).
//
// The cost that remains: while a ceiling is spent, a stranger's guesses at
// one account can push that account over its soft threshold, which then
// refuses even its owner's password login until the window ends. Sessions
// already issued are unaffected, and the state is in memory, so a restart
// clears it.
const (
	// LoginConnectorCeiling is the failures one tunnel connector may carry in
	// a window.
	LoginConnectorCeiling = 10 * DefaultMaxFailuresPerSource
	// LoginGlobalCeiling is the failures the whole service may carry in a
	// window.
	LoginGlobalCeiling = 500
	// LoginCeilingWindow is the ceilings' window.
	LoginCeilingWindow = DefaultThrottleWindow
	// MaxLoginInFlightPerVisitor is how many logins one visitor key may have
	// in flight at once, saturated or not.
	MaxLoginInFlightPerVisitor = 4
	// MaxSaturatedPerVisitor is how many saturated (delayed) requests one
	// visitor key may have in flight at once.
	MaxSaturatedPerVisitor = 2
	// MaxSaturatedSoft is the service-wide saturated count at or above which
	// a visitor is held to one saturated request at a time. A fresh visitor
	// (none in flight) is still admitted.
	MaxSaturatedSoft = 256
	// MaxSaturatedHard bounds the saturated requests held at once, for
	// goroutine safety only.
	MaxSaturatedHard = 4096
	// MaxLoginVisitors bounds the per-visitor table.
	MaxLoginVisitors = 10000
	// maxLoginVisitorAccounts bounds the accounts tracked per visitor.
	maxLoginVisitorAccounts = 16
	// ceilingBusyRetryAfter is the Retry-After (seconds) for a visitor whose
	// own requests are still in flight: they end within the saturation delay.
	ceilingBusyRetryAfter = 5
)

// The saturation policy (moved from ABO's absauth/saturation.go, whose gin
// request marks stay in ABO): what a saturated login waits before password
// work.
const (
	// SaturationDelayBase is the delay a saturated login with no failures of
	// its own on the account takes before password work.
	SaturationDelayBase = time.Second
	// SaturationDelayStep is added per failure already counted on the account.
	SaturationDelayStep = 500 * time.Millisecond
	// SaturationDelayMax caps the delay.
	SaturationDelayMax = 3 * time.Second
)

// The saturated-admission bucket's settings (core.auth.login.saturated_rate
// and saturated_burst, 05-authn §8; HEAD's LOGIN_SATURATED_RATE and
// LOGIN_SATURATED_BURST).
const (
	// DefaultSaturatedRate is the sustained rate, in logins per second.
	DefaultSaturatedRate = 2.0
	// DefaultSaturatedBurst is the bucket size.
	DefaultSaturatedBurst = 8
	// MaxSaturatedRate and MaxSaturatedBurst bound what an operator may set.
	// The argon2id derivation costs on the order of 70 ms of one core and
	// 64 MiB, so beyond these the setting no longer bounds anything.
	MaxSaturatedRate  = 50.0
	MaxSaturatedBurst = 200
	// MinSaturatedRate is the slowest sustained rate an operator may set.
	// Below it a refused caller would wait more than 100 seconds for one
	// token (the refusal's Retry-After is the time to the next token), which
	// is a lockout in all but name, not a bound on derivations.
	MinSaturatedRate = 0.01
)

// SaturatedLimitsValid reports whether rate (logins per second) and burst are
// usable saturated-bucket settings: a finite rate in [MinSaturatedRate,
// MaxSaturatedRate] and a burst in [1, MaxSaturatedBurst]. A value outside
// refuses boot (§8), so a typo neither removes nor tightens the bound
// unnoticed.
func SaturatedLimitsValid(rate float64, burst int) bool {
	return !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate >= MinSaturatedRate && rate <= MaxSaturatedRate &&
		burst >= 1 && burst <= MaxSaturatedBurst
}

// CeilingRefusal is why the ceiling refused a login before any password work.
type CeilingRefusal int

const (
	// CeilingAdmitted: the login may go on (saturated or not).
	CeilingAdmitted CeilingRefusal = iota
	// CeilingBusy: the visitor already has MaxLoginInFlightPerVisitor logins
	// in flight.
	CeilingBusy
	// CeilingSaturatedFailed: the ceilings are spent and the visitor already
	// failed while saturated in this window.
	CeilingSaturatedFailed
	// CeilingSaturatedBusy: the ceilings are spent and the visitor already has
	// its saturated requests in flight.
	CeilingSaturatedBusy
	// CeilingSaturatedCapacity: the ceilings are spent and the service-wide
	// bucket is empty, or the hard bound is reached.
	CeilingSaturatedCapacity
)

// LoginOutcome is how an admitted login ended, for LoginAdmission.Done.
type LoginOutcome int

const (
	// LoginOther is any answer but a failed credential or a success (a
	// throttle refusal, a bad request, a disabled account, a server error):
	// the slot is handed back.
	LoginOther LoginOutcome = iota
	// LoginFailed is a failed credential check (HTTP 401).
	LoginFailed
	// LoginSucceeded is a successful sign-in (HTTP 2xx).
	LoginSucceeded
	// LoginSaturationRefused is Throttle.SaturationPolicy's refusal of a saturated
	// login: a failure while saturated, like LoginFailed.
	LoginSaturationRefused
)

// CeilingDecision is Admit's answer beside the admission.
type CeilingDecision struct {
	Refusal CeilingRefusal
	// RetryAfter is, on a refusal, the seconds the caller should wait (at
	// least 1).
	RetryAfter int
	// Warn names a ceiling ("global", "connector <key>") reached for the
	// first time in its window, for the caller to log; "" otherwise.
	Warn string
}

// ceilingBucket is one ceiling's failure count in its current window.
type ceilingBucket struct {
	count  int
	start  time.Time
	warned bool
}

// ceilingTicket is what an admitted request holds so it can hand its slot
// back: the buckets it counted in, and the window each was in (a slot is not
// handed back into a window that has since rolled over). A zero ticket holds
// nothing (saturated requests).
type ceilingTicket struct {
	held                   bool
	connKey                string
	connStart, globalStart time.Time
}

// loginFailure is one failed login by a visitor against a known account:
// when, and the bucket windows its slot was spent in, so a refund goes back
// only into windows that are still open.
type loginFailure struct {
	at                     time.Time
	connKey                string
	connStart, globalStart time.Time
}

// loginVisitor is what the ceiling remembers about one visitor key.
type loginVisitor struct {
	// lastSeen is the last write; the table is in write order, so it decides
	// expiry.
	lastSeen time.Time
	// byAccount are the refundable failures per account key, newest last, at
	// most DefaultSoftThreshold each. A failure with no account key is not
	// recorded.
	byAccount map[string][]loginFailure
	// satFailedAt is when the visitor last failed a login while saturated.
	satFailedAt time.Time
	// inFlight is how many logins of the visitor are in flight; satInFlight
	// how many of those are saturated.
	inFlight, satInFlight int
}

// tokenBucket is the saturated-admission bound: rate tokens a second up to
// burst, full at the start (golang.org/x/time/rate's Limiter semantics for
// AllowN(now, 1) and TokensAt, kept stdlib-only so core takes no new
// dependency). An infinite rate (math.Inf(1), or math.MaxFloat64, which is
// rate.Inf) admits everything.
type tokenBucket struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate float64, burst int) tokenBucket {
	return tokenBucket{rate: rate, burst: float64(burst), tokens: float64(burst)}
}

func (b *tokenBucket) unbounded() bool { return math.IsInf(b.rate, 1) || b.rate == math.MaxFloat64 }

// tokensAt is the bucket's tokens at now, without spending any.
func (b *tokenBucket) tokensAt(now time.Time) float64 {
	if b.unbounded() {
		return b.burst
	}
	if b.last.IsZero() || !now.After(b.last) {
		return b.tokens
	}
	return min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
}

// allow spends one token at now when there is one.
func (b *tokenBucket) allow(now time.Time) bool {
	if b.unbounded() {
		return true
	}
	t := b.tokensAt(now)
	if now.After(b.last) {
		b.last = now
	}
	if t < 1 {
		b.tokens = t
		return false
	}
	b.tokens = t - 1
	return true
}

// LoginCeiling is the aggregate login ceilings and the saturated-admission
// bound (05-authn §4.9). One instance serves every password login of a
// process (core's Service holds one; ABO's legacy logins share another until
// the identity flip). It holds no package state.
type LoginCeiling struct {
	mu        sync.Mutex
	now       func() time.Time
	global    ceilingBucket
	conns     map[string]*ceilingBucket
	visitors  *lru[*loginVisitor]
	saturated int // saturated requests currently in flight; under mu
	// satBound is the service-wide token bucket on saturated admission.
	// Spent only by an admission (admitSaturated, under mu) so a refused
	// visitor never takes a token.
	satBound tokenBucket
}

// NewLoginCeiling returns an empty ceiling with the default saturated bucket.
// now may be nil (the real clock).
func NewLoginCeiling(now func() time.Time) *LoginCeiling {
	if now == nil {
		now = time.Now
	}
	l := &LoginCeiling{now: now, conns: map[string]*ceilingBucket{}}
	l.satBound = newTokenBucket(DefaultSaturatedRate, DefaultSaturatedBurst)
	visitors := newLRU[*loginVisitor](MaxLoginVisitors)
	// A visitor with a login in flight, or one refused for the rest of the
	// window, is state the bound must not evict in favour of a stream of new
	// addresses (evicting the first would let leave find no entry; evicting
	// the second would hand a refused visitor a fresh start), and an entry
	// that cannot be swept yet must not stand in front of the ones that can.
	// Such entries live on the LRU's pinned list; the bound evicts idle
	// entries first. The refusal pins only for its window: after it the
	// visitor is admitted again, so the entry is ordinary and evictable.
	visitors.pinned = func(v *loginVisitor) bool {
		return v.inFlight > 0 || v.satInFlight > 0 ||
			(!v.satFailedAt.IsZero() && l.now().Sub(v.satFailedAt) <= LoginCeilingWindow)
	}
	l.visitors = visitors
	return l
}

// SetSaturatedBound replaces the service-wide saturated-admission bucket
// (perSecond tokens a second, burst). Call before the ceiling serves
// requests. math.Inf(1) disables the bound.
func (l *LoginCeiling) SetSaturatedBound(perSecond float64, burst int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.satBound = newTokenBucket(perSecond, burst)
}

// SetClock replaces the clock (the Throttle's seam: a test rolls the window
// forward). A nil fn is ignored.
func (l *LoginCeiling) SetClock(fn func() time.Time) {
	if fn == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = fn
}

// Failures returns the failures counted in the current windows of the global
// ceiling and of connKey's connector ceiling (0 for "" or an unknown
// connector). A window that has run out reads as empty.
func (l *LoginCeiling) Failures(connKey string) (global, conn int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	open := func(b *ceilingBucket) int {
		if b == nil || now.Sub(b.start) > LoginCeilingWindow {
			return 0
		}
		return b.count
	}
	global = open(&l.global)
	if connKey != "" {
		conn = open(l.conns[connKey])
	}
	return global, conn
}

// InFlight returns how many logins of visitor (its throttle key, "" read as
// the shared unattributed key) are in flight, and how many saturated
// requests its saturated key holds.
func (l *LoginCeiling) InFlight(visitor string) (logins, saturated int) {
	if visitor == "" {
		visitor = "?"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if v, ok := l.visitors.get(visitor); ok {
		return v.inFlight, v.satInFlight
	}
	return 0, 0
}

// SaturatedBound returns the saturated-admission bucket's rate (tokens a
// second) and burst.
func (l *LoginCeiling) SaturatedBound() (perSecond float64, burst int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.satBound.rate, int(l.satBound.burst)
}

// LoginAdmission is one admitted login's hold on the ceiling. Done must be
// called exactly once, when the login ends.
type LoginAdmission struct {
	l         *LoginCeiling
	visitor   string
	satKey    string
	ticket    ceilingTicket
	saturated bool
	leave     func()
	satDone   func()
	done      bool
}

// Saturated reports whether the login was admitted while a ceiling is
// spent: the caller then applies Throttle.SaturationPolicy before password
// work.
func (a *LoginAdmission) Saturated() bool { return a.saturated }

// Admit admits one login from visitor (the throttle key,
// ClientInfo.sourceKey) arriving through connKey (the tunnel connector it
// came through, "" for any other peer): the visitor's in-flight cap, then a
// slot in the global and connector ceilings, and, when either is spent, the
// saturated admission keyed on satKey (ClientInfo.saturatedKey, asked only
// then). On a refusal the admission is nil. Decided before any user lookup,
// so every name gets the same answer.
func (l *LoginCeiling) Admit(visitor string, satKey func() string, connKey string) (*LoginAdmission, CeilingDecision) {
	leave, ok := l.enter(visitor)
	if !ok {
		return nil, CeilingDecision{Refusal: CeilingBusy, RetryAfter: ceilingBusyRetryAfter}
	}
	a := &LoginAdmission{l: l, visitor: visitor, leave: leave}
	ticket, ok, warn := l.reserve(connKey)
	if ok {
		a.ticket = ticket
		return a, CeilingDecision{}
	}
	key := ""
	if satKey != nil {
		key = satKey()
	}
	release, refusal, retryAfter := l.admitSaturated(key)
	if refusal != CeilingAdmitted {
		leave()
		return nil, CeilingDecision{Refusal: refusal, RetryAfter: retryAfter, Warn: warn}
	}
	a.saturated, a.satKey, a.satDone = true, key, release
	return a, CeilingDecision{Warn: warn}
}

// Done records how the login ended and releases its hold. account is the
// normalised submitted username (AccountKey), "" when the request named
// none. While saturated a failure or the saturation refusal bars the visitor
// from further saturated attempts this window; otherwise a failure keeps its
// slot spent (refundable by a later success of the same visitor on the same
// account), a success refunds, and anything else hands the slot back.
func (a *LoginAdmission) Done(outcome LoginOutcome, account string) {
	if a == nil || a.done {
		return
	}
	a.done = true
	defer a.leave()
	if a.saturated {
		if outcome == LoginFailed || outcome == LoginSaturationRefused {
			a.l.saturatedFailed(a.satKey)
		}
		a.satDone()
		return
	}
	switch outcome {
	case LoginFailed:
		a.l.failed(a.visitor, account, a.ticket)
	case LoginSucceeded:
		a.l.succeeded(a.visitor, account, a.ticket)
	default:
		a.l.release(a.ticket)
	}
}

// reserve takes a slot in the global bucket and, when connKey is not empty,
// in that connector's bucket. When either is spent it takes nothing and
// reports the scope to log (once per bucket window; "" after that).
func (l *LoginCeiling) reserve(connKey string) (t ceilingTicket, ok bool, warnScope string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	roll := func(b *ceilingBucket) {
		if now.Sub(b.start) > LoginCeilingWindow {
			*b = ceilingBucket{start: now}
		}
	}
	roll(&l.global)
	var cb *ceilingBucket
	if connKey != "" {
		cb = l.conns[connKey]
		if cb == nil {
			cb = &ceilingBucket{start: now}
			l.conns[connKey] = cb
		}
		roll(cb)
		if cb.count >= LoginConnectorCeiling {
			return ceilingTicket{}, false, warnOnce(cb, "connector "+connKey)
		}
	}
	if l.global.count >= LoginGlobalCeiling {
		return ceilingTicket{}, false, warnOnce(&l.global, "global")
	}
	l.global.count++
	t = ceilingTicket{held: true, globalStart: l.global.start}
	if cb != nil {
		cb.count++
		t.connKey, t.connStart = connKey, cb.start
	}
	return t, true, ""
}

func warnOnce(b *ceilingBucket, scope string) string {
	if b.warned {
		return ""
	}
	b.warned = true
	return scope
}

// releaseLocked hands back n slots of t. Caller holds l.mu.
func (l *LoginCeiling) releaseLocked(t ceilingTicket, n int) {
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

// release hands back the slot of a request that was not a failed login.
func (l *LoginCeiling) release(t ceilingTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(t, 1)
}

// visitorLocked returns the state of visitor, creating it, and marks it the
// most recently written entry. Entries nobody has written for a whole window
// (and with nothing in flight) are swept from the cold end of each of the
// table's lists first, so the table holds what is live rather than filling to
// the bound and evicting, and an entry with a login in flight (on the pinned
// list) never stops the sweep reaching the idle ones; at the bound the least
// recently written idle entry goes, in O(1). Every in-place change to an
// entry is followed by touchLocked, which re-files it. Caller holds l.mu.
func (l *LoginCeiling) visitorLocked(visitor string) *loginVisitor {
	now := l.now()
	l.visitors.sweep(func(v *loginVisitor) bool {
		return v.inFlight == 0 && v.satInFlight == 0 && now.Sub(v.lastSeen) > LoginCeilingWindow
	})
	v, ok := l.visitors.get(visitor)
	if !ok {
		v = &loginVisitor{byAccount: map[string][]loginFailure{}}
	}
	v.lastSeen = now
	l.visitors.put(visitor, v)
	return v
}

// touchLocked re-files visitor's entry after an in-place change to the
// fields the pin predicate reads (in flight, saturated in flight, refused),
// and marks it written. Caller holds l.mu.
func (l *LoginCeiling) touchLocked(visitor string, v *loginVisitor) {
	v.lastSeen = l.now()
	l.visitors.touch(visitor)
}

// failed records that the request holding t was a failed login by visitor
// against account: its slot stays spent and is remembered so a later success
// on that same account can take it back.
func (l *LoginCeiling) failed(visitor, account string, t ceilingTicket) {
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
	if len(fs) > DefaultSoftThreshold {
		fs = fs[len(fs)-DefaultSoftThreshold:]
	}
	v.byAccount[account] = fs
}

// succeeded hands back the success's own slot and, for the same visitor and
// the same account only, the failures from the last window (at most
// DefaultSoftThreshold, into windows still open). Failures against any other
// account stay counted.
func (l *LoginCeiling) succeeded(visitor, account string, t ceilingTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(t, 1)
	if account == "" {
		return
	}
	v, ok := l.visitors.get(visitor)
	if !ok {
		return
	}
	now := l.now()
	for _, f := range v.byAccount[account] {
		if now.Sub(f.at) <= LoginCeilingWindow {
			l.releaseLocked(ceilingTicket{held: true, connKey: f.connKey, connStart: f.connStart, globalStart: f.globalStart}, 1)
		}
	}
	delete(v.byAccount, account)
}

// enter admits one login from visitor, refusing the visitor's fifth
// concurrent one (MaxLoginInFlightPerVisitor). The returned func must be
// called when the request ends.
func (l *LoginCeiling) enter(visitor string) (leave func(), ok bool) {
	if visitor == "" {
		visitor = "?" // no parseable address: one shared key
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.visitorLocked(visitor)
	if v.inFlight >= MaxLoginInFlightPerVisitor {
		return nil, false
	}
	v.inFlight++
	l.touchLocked(visitor, v)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if cur, ok := l.visitors.get(visitor); ok && cur == v {
			v.inFlight--
			l.touchLocked(visitor, v)
		}
	}, true
}

// admitSaturated decides whether a saturated request from visitor (the
// saturated key; already counted by enter under its own key) may go on to be
// delayed. It refuses a visitor that already failed while saturated in this
// window (CeilingSaturatedFailed), one with MaxSaturatedPerVisitor already in
// flight (one when the service-wide count is at or above MaxSaturatedSoft;
// CeilingSaturatedBusy), anything past MaxSaturatedHard
// (CeilingSaturatedCapacity, goroutine safety) and, once the visitor passed
// its own limits, a request that finds the service-wide bucket empty
// (CeilingSaturatedCapacity). The token is taken last and only for an
// admission, so a refused visitor never spends one, and a caller flooding
// from many addresses is held to the bucket's rate however many fresh keys
// it has. On a refusal, retryAfter is the seconds the caller should wait (at
// least 1), computed under the lock from the state that refused it: the rest
// of the visitor's window, the time the bucket needs to hold one token, or
// the longest saturation delay. The returned func must be called when the
// request ends.
func (l *LoginCeiling) admitSaturated(visitor string) (release func(), refusal CeilingRefusal, retryAfter int) {
	if visitor == "" {
		visitor = "?"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.saturated >= MaxSaturatedHard {
		return nil, CeilingSaturatedCapacity, ceilSeconds(SaturationDelayMax)
	}
	v := l.visitorLocked(visitor)
	if !v.satFailedAt.IsZero() && now.Sub(v.satFailedAt) <= LoginCeilingWindow {
		return nil, CeilingSaturatedFailed, ceilSeconds(v.satFailedAt.Add(LoginCeilingWindow).Sub(now))
	}
	limit := MaxSaturatedPerVisitor
	if l.saturated >= MaxSaturatedSoft {
		limit = 1
	}
	if v.satInFlight >= limit {
		return nil, CeilingSaturatedBusy, ceilingBusyRetryAfter
	}
	if !l.satBound.allow(now) {
		return nil, CeilingSaturatedCapacity, l.bucketRetryAfterLocked(now)
	}
	v.satInFlight++
	l.saturated++
	l.touchLocked(visitor, v)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.saturated--
		if cur, ok := l.visitors.get(visitor); ok && cur == v {
			v.satInFlight--
			l.touchLocked(visitor, v)
		}
	}, CeilingAdmitted, 0
}

// bucketRetryAfterLocked is how many seconds until the saturated bucket holds
// one token again: max(1, ceil((1 - tokens) / rate)), never more than the
// window. The window cap is defensive: the configured rate is validated to be
// at least MinSaturatedRate (0.01 a second, so a wait of at most 100
// seconds), so no configuration reaches it. Caller holds l.mu.
func (l *LoginCeiling) bucketRetryAfterLocked(now time.Time) int {
	rps := l.satBound.rate
	if rps <= 0 {
		return int(LoginCeilingWindow / time.Second)
	}
	need := (1 - l.satBound.tokensAt(now)) / rps
	return min(max(1, int(math.Ceil(need))), int(LoginCeilingWindow/time.Second))
}

// ceilSeconds is d in whole seconds, rounded up, at least 1.
func ceilSeconds(d time.Duration) int {
	return max(1, int((d+time.Second-1)/time.Second))
}

// saturatedFailed records that visitor failed a login while saturated.
func (l *LoginCeiling) saturatedFailed(visitor string) {
	if visitor == "" {
		visitor = "?"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.visitorLocked(visitor)
	v.satFailedAt = l.now()
	l.touchLocked(visitor, v)
}
