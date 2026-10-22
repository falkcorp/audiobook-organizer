// file: internal/server/absauth/throttle.go
// version: 2.2.0
// guid: 3f9a1c86-2d57-4e0b-b843-9c60e5a7f218
// last-edited: 2026-10-22

package absauth

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Throttle policy for the password logins: the Audiobookshelf-compatible POST /login
// and the web POST /api/v1/auth/login. ONE Throttle is shared by both (the server
// builds it once), so a guess at an account through either door counts toward the
// same per-account counter and the same per-source budget. It replaced a second,
// identical implementation inside handlers.AuthHandler (pen-test findings
// HIGH-2/HIGH-3).
//
//   - The HARD limit is keyed on the ATTACKER'S SOURCE, never on the target
//     account. A third party hammering a known username can therefore never deny the
//     real user access from their own address.
//   - The per-account counter drives only a SOFT, progressive, capped delay on failed
//     attempts. A correct password always succeeds immediately.
//   - The account key is the normalised SUBMITTED username (AccountKey), not the
//     user id, so a name that does not exist is counted, delayed and refused exactly
//     like one that does and the answers do not reveal which names are real.
//   - A successful login clears the account's own counter and takes back from its
//     source only what that source failed against THAT account in the window, at
//     most SoftThreshold (Clear). It does not reset the source: a caller holding
//     one valid account would otherwise get unlimited guesses at every other account
//     from one address by logging in between rounds.
//   - Refresh failures (a dead ABS refresh token) are a budget of their own under
//     RefreshKey(source), which only /auth/refresh checks and charges: a refresh
//     storm cannot lock the same visitor out of either password login, and
//     password typos cannot stop a token refresh. No success path hands refresh
//     failures back.
//
// §1.9.4 item 3 makes this non-optional: AudioBooth issue #326 records a post-403
// request storm of 959 requests in 44 s that got a real user CrowdSec-banned. Both
// target clients also poll hard by design (Absorb hits /ping every 20 s offline and
// session /sync every 15 s in the foreground), so a legitimate retry loop must not be
// able to escalate into a lockout or an edge WAF ban. Only genuine credential
// FAILURES are counted here — a successful refresh, however frequent, costs nothing.
//
// HTTP Basic auth on the API routes is a separate path and is NOT throttled here;
// it is only as strong as the passwords behind it.
const (
	// MaxFailuresPerIP is the hard per-source failure budget inside Window.
	MaxFailuresPerIP = 15
	// Window is the rolling window for both counters.
	Window = 15 * time.Minute
	// SoftThreshold is how many per-account failures pass before any delay applies.
	SoftThreshold = 5
	// SoftStep is added per failure past SoftThreshold.
	SoftStep = 200 * time.Millisecond
	// SoftMaxDelay caps the per-account slowdown so it can never become a denial.
	SoftMaxDelay = 2 * time.Second
	// MaxKeys bounds each table. At the bound a new key evicts the least recently
	// written entry (O(1)), so rotating source addresses or usernames cannot grow
	// the table without limit; the aggregate login ceilings cap what rotation buys.
	// A counter that has reached its refusal threshold (SoftThreshold for a
	// username, MaxFailuresPerIP for a source) is pinned: the bound evicts any
	// counter below its threshold first, and a refused one only when every counter
	// in the table is refused, so a stream of distinct names cannot wash a refused
	// account's counter out.
	MaxKeys = 10000
	// maxAccountsPerSource bounds how many accounts one source's failures are
	// remembered for (they are what a success may take back).
	maxAccountsPerSource = 16
	// maxAccountKeyLen bounds a submitted username used as a table key.
	maxAccountKeyLen = 128
)

// RefreshKey is the source key under which ABS refresh-token failures are
// counted. Refresh has its own budget, apart from the password logins': a client
// storming /auth/refresh with a dead token must not spend the visitor's password
// budget (and a mistyped password must not stop a token refresh). Only
// /auth/refresh reads or charges it; neither password login does.
func RefreshKey(sourceKey string) string {
	if sourceKey == "" {
		return ""
	}
	return "refresh:" + sourceKey
}

// AccountKey is the per-account throttle key for a submitted username: trimmed,
// lowercased, at most maxAccountKeyLen bytes (cut on a rune boundary).
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

type failureCounter struct {
	count   int
	firstAt time.Time
	// lastSeen is the last write; tables are in write order, so expiry is checked
	// against it.
	lastSeen time.Time
	// byAcct is, for a source counter, how many of count were failures against each
	// account key in this window (at most maxAccountsPerSource keys): what a success
	// on that account may take back. Nil on an account counter.
	byAcct map[string]int
}

// Throttle tracks failed authentication attempts. Safe for concurrent use.
type Throttle struct {
	mu     sync.Mutex
	byIP   *LRU[*failureCounter]
	byAcct *LRU[*failureCounter]
	now    func() time.Time
	sleep  func(time.Duration)
}

// NewThrottle returns an empty Throttle.
func NewThrottle() *Throttle {
	byIP := NewLRU[*failureCounter](MaxKeys)
	byIP.SetPinned(func(c *failureCounter) bool { return c.count >= MaxFailuresPerIP })
	byAcct := NewLRU[*failureCounter](MaxKeys)
	byAcct.SetPinned(func(c *failureCounter) bool { return c.count >= SoftThreshold })
	return &Throttle{byIP: byIP, byAcct: byAcct, now: time.Now, sleep: time.Sleep}
}

// SetSleep replaces the delay function. Tests inject a no-op to stay fast.
func (t *Throttle) SetSleep(fn func(time.Duration)) {
	if fn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sleep = fn
}

// SetClock replaces the clock. Tests use it to roll the window forward.
func (t *Throttle) SetClock(fn func() time.Time) {
	if fn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = fn
}

// IPBlocked reports whether this source has exhausted its failure budget. Callers
// must check it BEFORE doing any credential work, so a spent source cannot keep
// probing.
func (t *Throttle) IPBlocked(ip string) bool {
	if ip == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.byIP.Get(ip)
	if !ok {
		return false
	}
	if t.now().Sub(c.firstAt) > Window {
		t.byIP.Delete(ip)
		return false
	}
	return c.count >= MaxFailuresPerIP
}

// AccountFailures is the number of failures counted against account key
// accountKey in the current window (0 for an unknown or expired key). It only
// reads.
func (t *Throttle) AccountFailures(accountKey string) int {
	if accountKey == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.byAcct.Get(accountKey)
	if !ok || t.now().Sub(c.firstAt) > Window {
		return 0
	}
	return c.count
}

// RecordFailure counts one failed attempt against both the account key (soft) and
// the source (hard), and returns the soft delay the caller should apply. The
// account key is AccountKey(submitted username) whether or not the user exists;
// it may be empty for a request that named none, in which case only the source is
// charged, so guessing cannot dodge the hard limit.
func (t *Throttle) RecordFailure(accountKey, ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.sweepLocked(now)
	if ip != "" {
		c := bump(t.byIP, ip, now)
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
	n := bump(t.byAcct, accountKey, now).count
	if n <= SoftThreshold {
		return 0
	}
	d := min(time.Duration(n-SoftThreshold)*SoftStep, SoftMaxDelay)
	return d
}

// Delay applies a soft delay through the injected sleep function.
func (t *Throttle) Delay(d time.Duration) {
	if d <= 0 {
		return
	}
	t.mu.Lock()
	fn := t.sleep
	t.mu.Unlock()
	fn(d)
}

// Clear is called after a successful authentication as accountKey from source ip.
// It resets that account's own counter, and takes back from the source only the
// failures that source made against that same account in its current window, at
// most SoftThreshold of them. The source's failures against any other account
// stay counted, so a success on one account buys no extra guesses at others.
func (t *Throttle) Clear(accountKey, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if accountKey != "" {
		t.byAcct.Delete(accountKey)
	}
	if ip == "" || accountKey == "" {
		return
	}
	c, ok := t.byIP.Get(ip)
	if !ok {
		return
	}
	if t.now().Sub(c.firstAt) > Window {
		t.byIP.Delete(ip)
		return
	}
	n := min(c.byAcct[accountKey], SoftThreshold)
	delete(c.byAcct, accountKey)
	c.count -= n
	if c.count <= 0 {
		t.byIP.Delete(ip)
		return
	}
	// The count changed: file it under its new pin state, and as written now. The
	// table is in write order and expiry is read from lastSeen, so a counter moved
	// to the front keeps a lastSeen to match.
	c.lastSeen = t.now()
	t.byIP.Touch(ip)
}

// bump adds one failure to key's counter, starting a new window when there is none
// or the old one has elapsed, and returns the counter.
func bump(m *LRU[*failureCounter], key string, now time.Time) *failureCounter {
	c, ok := m.Get(key)
	if !ok || now.Sub(c.firstAt) > Window {
		c = &failureCounter{count: 1, firstAt: now, lastSeen: now}
		m.Put(key, c)
		return c
	}
	c.count++
	c.lastSeen = now
	m.Touch(key)
	return c
}

// sweepLocked drops counters not written for a whole Window (their window has
// elapsed, so the next failure would restart them anyway) from the cold end of
// both tables, at a cost proportional to what it removes. Called on every write.
func (t *Throttle) sweepLocked(now time.Time) {
	expired := func(c *failureCounter) bool { return now.Sub(c.lastSeen) > Window }
	t.byIP.Sweep(expired)
	t.byAcct.Sweep(expired)
}
