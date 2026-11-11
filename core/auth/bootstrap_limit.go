// file: core/auth/bootstrap_limit.go
// version: 1.0.0
// guid: 9a3c550c-d848-4f99-b68b-ba0675acdf12
// last-edited: 2026-11-10

package auth

import (
	"container/list"
	"sync"
	"time"
)

// Bootstrap attempt limiter (05-authn §4.6 item 2, §4.9; moved from ABO's
// internal/server/bootstrap_rate_limit.go, whose request keying stays in ABO
// as bootstrap_gate.go): a fixed window per client key plus a global ceiling
// across all keys.
//
// The bootstrap redeem exchanges a one-time token for a full-privilege API
// key, so it is limited harder than the invite routes, and the limiter itself
// must not be a way to exhaust memory or to dodge the per-key cap:
//
//   - the caller keys a client by an IPv4 address or an IPv6 /64, so an
//     attacker who owns a /64 cannot mint 2^64 keys, and an unattributed
//     client uses one fixed key; a direct LAN peer is keyed on its full
//     address with aggregate windows (LimiterAggregate) on top, so one LAN
//     host cannot rotate addresses for fresh windows;
//   - entries are kept in creation order, and every call first drops the ones
//     whose window has ended, so the map holds only live windows;
//   - the map is hard-capped at BootstrapLimits.MaxKeys, evicting the oldest;
//   - BootstrapLimits.GlobalMax bounds admitted attempts across ALL keys, so
//     however many keys an attacker can invent the total is bounded. Only
//     admitted attempts create entries, and the global ceiling is far below
//     the key cap, so the cap is a backstop that normal operation never hits.
//
// The global ceiling applies to EVERY request, a direct LAN peer included: a
// "direct LAN peer" is only a statement about the TCP peer's address and the
// absence of proxy headers, which anything reaching the port off-tunnel can
// satisfy, so an exemption would be an attempt budget for any such caller.
// The limiter is in memory, so restarting the service (the first step of the
// bootstrap recovery flow) clears it.
const (
	// BootstrapMaxAttempts is the attempts one client key may make per
	// window.
	BootstrapMaxAttempts = 5
	// BootstrapWindow is the window.
	BootstrapWindow = time.Hour
	// BootstrapLANPrefixFactor sizes the aggregate window every address of a
	// direct LAN IPv6 /64 shares (four times the per-key cap: the limiter
	// counts every admitted attempt, not only failures, so this leaves room
	// for several real devices on the LAN).
	BootstrapLANPrefixFactor = 4
	// BootstrapDirectLANTotalFactor sizes the one window all direct LAN
	// traffic except loopback shares: 50 attempts an hour, half the global
	// ceiling, so one LAN host rotating IPv4 aliases or /64s leaves the other
	// half to everyone else. Loopback is left out, so the owner's recovery
	// from the server itself cannot be locked out from the LAN.
	BootstrapDirectLANTotalFactor = 10
	// BootstrapUnlistedLANTotalFactor sizes the one window all unlisted LAN
	// traffic (a LAN host sending proxy markers that is not a listed
	// connector) shares: 25 attempts an hour, a quarter of the global
	// ceiling.
	BootstrapUnlistedLANTotalFactor = 5
	// BootstrapGlobalMaxAttempts bounds admitted attempts across all keys per
	// window.
	BootstrapGlobalMaxAttempts = 100
	// BootstrapMaxTrackedKeys hard-caps the tracked windows.
	BootstrapMaxTrackedKeys = 10_000
)

// BootstrapLimits are a BootstrapLimiter's policy. A zero field takes its
// Bootstrap* default.
type BootstrapLimits struct {
	PerKey    int
	GlobalMax int
	MaxKeys   int
	Window    time.Duration
}

func (l BootstrapLimits) withDefaults() BootstrapLimits {
	if l.PerKey <= 0 {
		l.PerKey = BootstrapMaxAttempts
	}
	if l.GlobalMax <= 0 {
		l.GlobalMax = BootstrapGlobalMaxAttempts
	}
	if l.MaxKeys <= 0 {
		l.MaxKeys = BootstrapMaxTrackedKeys
	}
	if l.Window <= 0 {
		l.Window = BootstrapWindow
	}
	return l
}

// LimiterAggregate is an aggregate window a request also counts against,
// with room for Factor times the per-key cap.
type LimiterAggregate struct {
	Key    string
	Factor int
}

// bootstrapAttempt is one window: count attempts admitted since firstAt.
type bootstrapAttempt struct {
	key     string
	count   int
	firstAt time.Time
}

// BootstrapLimiter is the bootstrap attempt limiter. It is safe for
// concurrent use and holds no package state.
type BootstrapLimiter struct {
	mu      sync.Mutex
	entries map[string]*list.Element // key -> element whose Value is *bootstrapAttempt
	order   *list.List               // front = oldest window start
	global  bootstrapAttempt
	lim     BootstrapLimits
	now     func() time.Time
}

// NewBootstrapLimiter returns an empty limiter. now may be nil (the real
// clock).
func NewBootstrapLimiter(l BootstrapLimits, now func() time.Time) *BootstrapLimiter {
	if now == nil {
		now = time.Now
	}
	return &BootstrapLimiter{entries: map[string]*list.Element{}, order: list.New(), lim: l.withDefaults(), now: now}
}

// Limits returns the policy in force.
func (l *BootstrapLimiter) Limits() BootstrapLimits { return l.lim }

// Len is how many windows the limiter tracks.
func (l *BootstrapLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Count is key's admitted attempts in its live window (0 for none, or for a
// window that has ended).
func (l *BootstrapLimiter) Count(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok && l.now().Sub(el.Value.(*bootstrapAttempt).firstAt) <= l.lim.Window {
		return el.Value.(*bootstrapAttempt).count
	}
	return 0
}

// GlobalCount is the attempts admitted across all keys in the global
// ceiling's live window.
func (l *BootstrapLimiter) GlobalCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.global.count == 0 || l.now().Sub(l.global.firstAt) > l.lim.Window {
		return 0
	}
	return l.global.count
}

// Allow records one attempt for key and for each aggregate window in aggs,
// and reports whether it may proceed. Every window must admit the attempt,
// and the checks and the increments happen under one lock, all checks first,
// so they are atomic: a refused attempt is not recorded anywhere, so it
// spends no window and no global budget. The global ceiling applies to every
// key.
func (l *BootstrapLimiter) Allow(key string, aggs ...LimiterAggregate) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)

	if l.countLocked(key) >= l.lim.PerKey {
		return false
	}
	for _, a := range aggs {
		if l.countLocked(a.Key) >= a.Factor*l.lim.PerKey {
			return false
		}
	}
	if l.global.count == 0 || now.Sub(l.global.firstAt) > l.lim.Window {
		l.global = bootstrapAttempt{firstAt: now}
	}
	if l.global.count >= l.lim.GlobalMax {
		return false
	}
	l.global.count++
	l.incrementLocked(key, now)
	for _, a := range aggs {
		l.incrementLocked(a.Key, now)
	}
	return true
}

// sweepLocked drops every window that has ended. Entries are in creation
// order, so it stops at the first live one: cost is O(dropped).
func (l *BootstrapLimiter) sweepLocked(now time.Time) {
	for el := l.order.Front(); el != nil; el = l.order.Front() {
		if now.Sub(el.Value.(*bootstrapAttempt).firstAt) <= l.lim.Window {
			return
		}
		l.removeLocked(el)
	}
}

func (l *BootstrapLimiter) removeLocked(el *list.Element) {
	delete(l.entries, el.Value.(*bootstrapAttempt).key)
	l.order.Remove(el)
}

// countLocked is key's admitted attempts in its live window (0 for none).
func (l *BootstrapLimiter) countLocked(key string) int {
	if el, ok := l.entries[key]; ok {
		return el.Value.(*bootstrapAttempt).count
	}
	return 0
}

// incrementLocked records one admitted attempt for key, opening its window
// (and evicting the oldest window at the cap) when it has none.
func (l *BootstrapLimiter) incrementLocked(key string, now time.Time) {
	if el, ok := l.entries[key]; ok {
		el.Value.(*bootstrapAttempt).count++
		return
	}
	for l.order.Len() >= l.lim.MaxKeys {
		l.removeLocked(l.order.Front())
	}
	l.entries[key] = l.order.PushBack(&bootstrapAttempt{key: key, count: 1, firstAt: now})
}
