// file: internal/server/absauth/throttle.go
// version: 1.0.0
// guid: 3f9a1c86-2d57-4e0b-b843-9c60e5a7f218
// last-edited: 2026-07-30

package absauth

import (
	"sync"
	"time"
)

// Throttle policy for POST /login and POST /auth/refresh.
//
// This mirrors, deliberately and to the same numbers, the lockout machinery already
// in internal/server/handlers/auth.go (pen-test findings HIGH-2/HIGH-3). It is a
// separate type rather than a shared one only because the existing implementation is
// unexported state on AuthHandler and this task must not rewire the existing auth
// path. The policy is the important part and it is identical:
//
//   - The HARD limit is keyed on the ATTACKER'S SOURCE IP, never on the target
//     account. A third party hammering a known username can therefore never deny the
//     real user access from their own address.
//   - The per-account counter drives only a SOFT, progressive, capped delay on failed
//     attempts. A correct password always succeeds immediately.
//
// §1.9.4 item 3 makes this non-optional: AudioBooth issue #326 records a post-403
// request storm of 959 requests in 44 s that got a real user CrowdSec-banned. Both
// target clients also poll hard by design (Absorb hits /ping every 20 s offline and
// session /sync every 15 s in the foreground), so a legitimate retry loop must not be
// able to escalate into a lockout or an edge WAF ban. Only genuine credential
// FAILURES are counted here — a successful refresh, however frequent, costs nothing.
const (
	// MaxFailuresPerIP is the hard per-source-IP failure budget inside Window.
	MaxFailuresPerIP = 15
	// Window is the rolling window for both counters.
	Window = 15 * time.Minute
	// SoftThreshold is how many per-account failures pass before any delay applies.
	SoftThreshold = 5
	// SoftStep is added per failure past SoftThreshold.
	SoftStep = 200 * time.Millisecond
	// SoftMaxDelay caps the per-account slowdown so it can never become a denial.
	SoftMaxDelay = 2 * time.Second
	// idleTTL bounds the maps: entries untouched for this long are dropped.
	idleTTL = 30 * time.Minute
)

type failureCounter struct {
	count    int
	firstAt  time.Time
	lastSeen time.Time
}

// Throttle tracks failed authentication attempts. Safe for concurrent use.
type Throttle struct {
	mu     sync.Mutex
	byIP   map[string]*failureCounter
	byAcct map[string]*failureCounter
	now    func() time.Time
	sleep  func(time.Duration)
	lastGC time.Time
}

// NewThrottle returns an empty Throttle.
func NewThrottle() *Throttle {
	return &Throttle{
		byIP:   make(map[string]*failureCounter),
		byAcct: make(map[string]*failureCounter),
		now:    time.Now,
		sleep:  time.Sleep,
	}
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

// IPBlocked reports whether this source IP has exhausted its failure budget. Callers
// must check it BEFORE doing any credential work, so a spent source cannot keep
// probing.
func (t *Throttle) IPBlocked(ip string) bool {
	if ip == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.byIP[ip]
	if !ok {
		return false
	}
	if t.now().Sub(c.firstAt) > Window {
		delete(t.byIP, ip)
		return false
	}
	return c.count >= MaxFailuresPerIP
}

// RecordFailure counts one failed attempt against both the account (soft) and the
// source IP (hard), and returns the soft delay the caller should apply. accountID may
// be empty for an unknown username — the IP is still charged so username guessing
// cannot dodge the hard limit.
func (t *Throttle) RecordFailure(accountID, ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.gcLocked(now)
	if ip != "" {
		bump(t.byIP, ip, now)
	}
	if accountID == "" {
		return 0
	}
	n := bump(t.byAcct, accountID, now)
	if n <= SoftThreshold {
		return 0
	}
	d := time.Duration(n-SoftThreshold) * SoftStep
	if d > SoftMaxDelay {
		d = SoftMaxDelay
	}
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

// Clear resets both counters after a successful authentication.
func (t *Throttle) Clear(accountID, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if accountID != "" {
		delete(t.byAcct, accountID)
	}
	if ip != "" {
		delete(t.byIP, ip)
	}
}

func bump(m map[string]*failureCounter, key string, now time.Time) int {
	c, ok := m[key]
	if !ok || now.Sub(c.firstAt) > Window {
		m[key] = &failureCounter{count: 1, firstAt: now, lastSeen: now}
		return 1
	}
	c.count++
	c.lastSeen = now
	return c.count
}

// gcGlocked drops entries nobody has touched for idleTTL so the maps cannot grow
// without bound under a distributed probe. Runs at most once a minute.
func (t *Throttle) gcLocked(now time.Time) {
	if now.Sub(t.lastGC) < time.Minute {
		return
	}
	t.lastGC = now
	for _, m := range []map[string]*failureCounter{t.byIP, t.byAcct} {
		for k, c := range m {
			if now.Sub(c.lastSeen) > idleTTL {
				delete(m, k)
			}
		}
	}
}
