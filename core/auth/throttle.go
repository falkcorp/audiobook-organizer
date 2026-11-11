// file: core/auth/throttle.go
// version: 3.1.0
// guid: 3f9a1c86-2d57-4e0b-b843-9c60e5a7f218
// last-edited: 2026-11-10

package auth

import (
	"container/list"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The throttle's defaults: HEAD's absauth/throttle.go limits (05-authn §4.9,
// §8 core.auth.login.*).
const (
	DefaultMaxFailuresPerSource = 15
	DefaultThrottleWindow       = 15 * time.Minute
	DefaultSoftThreshold        = 5
	DefaultSoftStep             = 200 * time.Millisecond
	DefaultSoftMax              = 2 * time.Second
	// DefaultThrottleKeys bounds each table. At the bound a new key evicts the
	// least recently written entry; a counter that reached its refusal
	// threshold is pinned and evicted only when every counter is.
	DefaultThrottleKeys = 10000
	// maxAccountsPerSource bounds how many accounts one source's failures are
	// remembered for (what a success may take back).
	maxAccountsPerSource = 16
	// maxAccountKeyLen bounds a submitted username used as a table key.
	maxAccountKeyLen = 128
)

// ThrottleLimits are the throttle's policy (core.auth.login.*). A zero field
// takes its default.
type ThrottleLimits struct {
	MaxFailuresPerSource int
	Window               time.Duration
	SoftThreshold        int
	SoftStep             time.Duration
	SoftMax              time.Duration
	MaxKeys              int
}

func (l ThrottleLimits) withDefaults() ThrottleLimits {
	if l.MaxFailuresPerSource <= 0 {
		l.MaxFailuresPerSource = DefaultMaxFailuresPerSource
	}
	if l.Window <= 0 {
		l.Window = DefaultThrottleWindow
	}
	if l.SoftThreshold <= 0 {
		l.SoftThreshold = DefaultSoftThreshold
	}
	if l.SoftStep <= 0 {
		l.SoftStep = DefaultSoftStep
	}
	if l.SoftMax <= 0 {
		l.SoftMax = DefaultSoftMax
	}
	if l.MaxKeys <= 0 {
		l.MaxKeys = DefaultThrottleKeys
	}
	return l
}

// AccountKey is the per-account throttle key for a submitted username:
// trimmed, lower-cased, at most 128 bytes cut on a rune boundary. The key is
// the submitted name, not the user ID, so a name that does not exist is
// counted, delayed and refused exactly like one that does.
func AccountKey(username string) string {
	k := strings.ToLower(strings.TrimSpace(username))
	if len(k) > maxAccountKeyLen {
		k = k[:maxAccountKeyLen]
		for len(k) > 0 {
			if r, size := utf8.DecodeLastRuneInString(k); r == utf8.RuneError && size <= 1 {
				k = k[:len(k)-1] // a rune cut in half by the limit
				continue
			}
			break
		}
	}
	return k
}

// RefreshKey is the source key under which a compat surface's token-refresh
// failures are counted: a budget of their own, so a refresh storm cannot lock
// the visitor out of password sign-in.
func RefreshKey(sourceKey string) string {
	if sourceKey == "" {
		return ""
	}
	return "refresh:" + sourceKey
}

type failureCounter struct {
	count    int
	firstAt  time.Time
	lastSeen time.Time
	// byAcct is, for a source counter, how many of count were failures
	// against each account key in this window: what a success on that
	// account may take back. Nil on an account counter.
	byAcct map[string]int
}

// Throttle tracks failed authentication attempts (05-authn §4.9; moved from
// HEAD's absauth.Throttle). It is instance state: one per Service, never a
// package variable (R5). No hard account lockout: the hard limit is keyed on
// the source; the per-account counter drives only a soft, capped delay; a
// success clears the account and takes back from its source only what that
// source failed against that account (at most SoftThreshold). Safe for
// concurrent use.
type Throttle struct {
	mu     sync.Mutex
	lim    ThrottleLimits
	bySrc  *lru[*failureCounter]
	byAcct *lru[*failureCounter]
	now    func() time.Time
	sleep  func(time.Duration)
}

// NewThrottle returns an empty throttle. now and sleep may be nil (the real
// clock and time.Sleep).
func NewThrottle(l ThrottleLimits, now func() time.Time, sleep func(time.Duration)) *Throttle {
	l = l.withDefaults()
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	t := &Throttle{lim: l, now: now, sleep: sleep}
	// The pin rules read the limits in force (SetLimits); the tables only
	// call them under t.mu.
	t.bySrc = newLRU[*failureCounter](l.MaxKeys)
	t.bySrc.pinned = func(c *failureCounter) bool { return c.count >= t.lim.MaxFailuresPerSource }
	t.byAcct = newLRU[*failureCounter](l.MaxKeys)
	t.byAcct.pinned = func(c *failureCounter) bool { return c.count >= t.lim.SoftThreshold }
	return t
}

// Limits returns the policy in force.
func (t *Throttle) Limits() ThrottleLimits {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lim
}

// SetLimits applies a new policy: core.auth.login.* are reloadable (05-authn
// §8), so the service hands the throttle the settings in force on every
// sign-in. A zero field takes its default, and MaxKeys keeps the value the
// throttle was built with (its tables are sized once). Counters already
// held are judged by the new limits from the next call on.
func (t *Throttle) SetLimits(l ThrottleLimits) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l.MaxKeys = t.lim.MaxKeys
	t.lim = l.withDefaults()
}

// SetSleep replaces the delay function (HEAD's seam: a test injects a no-op
// into a throttle another component built). A nil fn is ignored.
func (t *Throttle) SetSleep(fn func(time.Duration)) {
	if fn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sleep = fn
}

// SetClock replaces the clock (HEAD's seam: a test rolls the window
// forward). A nil fn is ignored.
func (t *Throttle) SetClock(fn func() time.Time) {
	if fn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = fn
}

// SourceBlocked reports whether source has exhausted its failure budget.
// Callers check it before any credential work, so a spent source cannot keep
// probing.
func (t *Throttle) SourceBlocked(source string) bool {
	if source == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.bySrc.get(source)
	if !ok {
		return false
	}
	if t.now().Sub(c.firstAt) > t.lim.Window {
		t.bySrc.delete(source)
		return false
	}
	return c.count >= t.lim.MaxFailuresPerSource
}

// AccountFailures is the failures counted against accountKey in the current
// window (0 for an unknown or expired key).
func (t *Throttle) AccountFailures(accountKey string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.accountFailuresLocked(accountKey)
}

func (t *Throttle) accountFailuresLocked(accountKey string) int {
	if accountKey == "" {
		return 0
	}
	c, ok := t.byAcct.get(accountKey)
	if !ok || t.now().Sub(c.firstAt) > t.lim.Window {
		return 0
	}
	return c.count
}

// SaturationPolicy is what a saturated login (LoginAdmission.Saturated) on
// accountKey gets before any password work (moved from ABO's
// absauth/saturation.go): a delay of SaturationDelayBase plus
// SaturationDelayStep per failure already counted on the account, capped at
// SaturationDelayMax, or a refusal once the account has failed SoftThreshold
// times. An unknown username has no failures of its own, so the answer is
// decided by the SUBMITTED name and does not reveal whether it exists. The
// failures and the threshold are read under one lock.
func (t *Throttle) SaturationPolicy(accountKey string) (delay time.Duration, refuse bool) {
	t.mu.Lock()
	failures, threshold := t.accountFailuresLocked(accountKey), t.lim.SoftThreshold
	t.mu.Unlock()
	if failures >= threshold {
		return 0, true
	}
	return min(SaturationDelayBase+time.Duration(failures)*SaturationDelayStep, SaturationDelayMax), false
}

// RecordFailure counts one failed attempt against the account (soft) and the
// source (hard), and returns the soft delay the caller should apply. An empty
// account key charges only the source.
func (t *Throttle) RecordFailure(accountKey, source string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.sweepLocked(now)
	if source != "" {
		c := t.bump(t.bySrc, source, now)
		if accountKey != "" {
			if c.byAcct == nil {
				c.byAcct = make(map[string]int, 1)
			}
			if _, ok := c.byAcct[accountKey]; ok || len(c.byAcct) < maxAccountsPerSource {
				c.byAcct[accountKey]++
			}
		}
	}
	if accountKey == "" {
		return 0
	}
	n := t.bump(t.byAcct, accountKey, now).count
	if n <= t.lim.SoftThreshold {
		return 0
	}
	return min(time.Duration(n-t.lim.SoftThreshold)*t.lim.SoftStep, t.lim.SoftMax)
}

// Delay sleeps d through the injected sleep function, outside the lock.
func (t *Throttle) Delay(d time.Duration) {
	if d <= 0 {
		return
	}
	t.mu.Lock()
	fn := t.sleep
	t.mu.Unlock()
	fn(d)
}

// Clear is called after a successful authentication as accountKey from
// source: it resets the account's counter and takes back from the source the
// failures it made against that account in its window, at most
// SoftThreshold, so a success on one account buys no extra guesses at others.
func (t *Throttle) Clear(accountKey, source string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if accountKey != "" {
		t.byAcct.delete(accountKey)
	}
	if source == "" || accountKey == "" {
		return
	}
	c, ok := t.bySrc.get(source)
	if !ok {
		return
	}
	now := t.now()
	if now.Sub(c.firstAt) > t.lim.Window {
		t.bySrc.delete(source)
		return
	}
	n := min(c.byAcct[accountKey], t.lim.SoftThreshold)
	delete(c.byAcct, accountKey)
	c.count -= n
	if c.count <= 0 {
		t.bySrc.delete(source)
		return
	}
	c.lastSeen = now
	t.bySrc.touch(source)
}

// Len reports how many source and account keys the throttle holds.
func (t *Throttle) Len() (sources, accounts int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bySrc.len(), t.byAcct.len()
}

func (t *Throttle) bump(m *lru[*failureCounter], key string, now time.Time) *failureCounter {
	c, ok := m.get(key)
	if !ok || now.Sub(c.firstAt) > t.lim.Window {
		c = &failureCounter{count: 1, firstAt: now, lastSeen: now}
		m.put(key, c)
		return c
	}
	c.count++
	c.lastSeen = now
	m.touch(key)
	return c
}

// sweepLocked drops counters not written for a whole window from the cold
// end of both tables, at a cost proportional to what it removes.
func (t *Throttle) sweepLocked(now time.Time) {
	expired := func(c *failureCounter) bool { return now.Sub(c.lastSeen) > t.lim.Window }
	t.bySrc.sweep(expired)
	t.byAcct.sweep(expired)
}

// lru is a bounded map in write order with pinning (HEAD's absauth.LRU): a
// full table evicts the least recently written unpinned entry first, and a
// pinned one only when every entry is pinned.
type lru[V any] struct {
	max    int
	pinned func(V) bool
	cold   *list.List // unpinned; front = most recently written
	hot    *list.List // pinned; front = most recently written
	m      map[string]*list.Element
}

type lruEntry[V any] struct {
	key string
	val V
	hot bool
}

func newLRU[V any](size int) *lru[V] {
	return &lru[V]{max: max(size, 1), cold: list.New(), hot: list.New(), m: make(map[string]*list.Element)}
}

func (t *lru[V]) len() int { return len(t.m) }

func (t *lru[V]) get(key string) (V, bool) {
	if e, ok := t.m[key]; ok {
		return e.Value.(*lruEntry[V]).val, true
	}
	var zero V
	return zero, false
}

func (t *lru[V]) put(key string, val V) {
	if e, ok := t.m[key]; ok {
		e.Value.(*lruEntry[V]).val = val
		t.refile(e)
		return
	}
	if len(t.m) >= t.max {
		victim := t.cold.Back()
		if victim == nil {
			victim = t.hot.Back()
		}
		t.remove(victim)
	}
	ent := &lruEntry[V]{key: key, val: val, hot: t.isPinned(val)}
	t.m[key] = t.listFor(ent.hot).PushFront(ent)
}

func (t *lru[V]) touch(key string) {
	if e, ok := t.m[key]; ok {
		t.refile(e)
	}
}

func (t *lru[V]) delete(key string) {
	if e, ok := t.m[key]; ok {
		t.remove(e)
	}
}

func (t *lru[V]) sweep(expired func(V) bool) {
	for _, l := range []*list.List{t.cold, t.hot} {
		for e := l.Back(); e != nil; e = l.Back() {
			if !expired(e.Value.(*lruEntry[V]).val) {
				break
			}
			t.remove(e)
		}
	}
}

func (t *lru[V]) isPinned(v V) bool { return t.pinned != nil && t.pinned(v) }

func (t *lru[V]) listFor(hot bool) *list.List {
	if hot {
		return t.hot
	}
	return t.cold
}

func (t *lru[V]) refile(e *list.Element) {
	ent := e.Value.(*lruEntry[V])
	hot := t.isPinned(ent.val)
	if hot == ent.hot {
		t.listFor(hot).MoveToFront(e)
		return
	}
	t.listFor(ent.hot).Remove(e)
	ent.hot = hot
	t.m[ent.key] = t.listFor(hot).PushFront(ent)
}

func (t *lru[V]) remove(e *list.Element) {
	if e == nil {
		return
	}
	ent := e.Value.(*lruEntry[V])
	t.listFor(ent.hot).Remove(e)
	delete(t.m, ent.key)
}
