// file: internal/server/bootstrap_rate_limit.go
// version: 1.9.0
// guid: df3037e5-6d7d-452f-9474-a8d0b051a31b
// last-edited: 2026-10-21

package server

import (
	"container/list"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Bootstrap attempt limiter: a fixed window per client key plus a global
// ceiling across all keys.
//
// The endpoint exchanges a one-time token for a full-privilege API key, so it
// is limited harder than the invite routes, and the limiter itself must not be
// a way to exhaust memory or to dodge the per-key cap:
//
//   - the key is an IPv4 address or an IPv6 /64 (bootstrapRateKey), so an
//     attacker who owns a /64 cannot mint 2^64 keys, and an unparseable or
//     unattributed client uses one fixed key rather than attacker-chosen text;
//     a direct LAN peer is keyed on its full address (bootstrapDirectKey),
//     because LAN devices share one ULA /64, and every address of a direct
//     LAN IPv6 /64 also shares one aggregate window of
//     bootstrapLANPrefixFactor times the per-key cap, so one LAN host cannot
//     rotate through its /64 for fresh windows (tunnel traffic never touches
//     that window); a LAN host sending Cloudflare markers but not a listed
//     connector ("unlisted", classifyLANPeer) gets, on top of the global
//     ceiling, its own /64 (IPv6) or /24 (IPv4) aggregate ("unlisted-lan:")
//     and the unlisted LAN total (bootstrapUnlistedLANTotalKey, a quarter of
//     the global ceiling), so one LAN host rotating through many addresses,
//     /64s or /24s can never spend the global budget for everyone else;
//   - entries are kept in creation order, and every call first drops the ones
//     whose window has ended, so the map holds only live windows;
//   - the map is hard-capped at bootstrapMaxTrackedKeys, evicting the oldest;
//   - bootstrapGlobalMaxAttempts bounds admitted attempts across ALL keys, so
//     however many keys an attacker can invent the total is bounded. Only
//     admitted attempts create entries, and the global ceiling is far below
//     the key cap, so the cap is a backstop that normal operation never hits.
//
// The global ceiling is also a way for an attacker to lock someone out, so it
// applies only to requests that came through the tunnel or a proxy
// (directLANPeer says which do not). The owner runs bootstrap
// directly from the LAN, so unauthenticated internet traffic can spend the
// global budget but cannot touch the owner's path, which keeps only its own
// per-address bucket. It takes bootstrapGlobalMaxAttempts/bootstrapMaxAttempts
// distinct tunnel clients (20) to spend the global budget. The tunnel
// connectors are themselves LAN hosts with private addresses, so "private
// peer" alone does not mean direct: a configured connector, or any request
// with Cloudflare markers, is tunnel traffic.
const (
	bootstrapMaxAttempts    = 5
	bootstrapWindowDuration = time.Hour

	// bootstrapLANPrefixFactor sizes the aggregate window every address of a
	// direct LAN IPv6 /64 shares (bootstrapDirectLANKey): a direct LAN peer
	// is keyed on its full address, so without it one LAN host could rotate
	// through its /64 for fresh per-address windows, and direct peers are
	// exempt from the global ceiling, so this aggregate is the only cap on
	// that rotation. The limiter counts every admitted attempt, not only
	// failures (it runs before the token is checked), so the window is four
	// times the per-key cap: room for several real devices on the LAN.
	bootstrapLANPrefixFactor = 4

	// bootstrapUnlistedLANTotalFactor sizes the one window all unlisted LAN
	// traffic shares (bootstrapUnlistedLANTotalKey): 25 attempts an hour, a
	// quarter of the global ceiling.
	bootstrapUnlistedLANTotalFactor = 5

	bootstrapGlobalMaxAttempts = 100
	bootstrapMaxTrackedKeys    = 10_000
)

// bootstrapAttempt is one window: count attempts admitted since firstAt.
type bootstrapAttempt struct {
	key     string
	count   int
	firstAt time.Time
}

// bootstrapLimiter is safe for concurrent use.
type bootstrapLimiter struct {
	mu      sync.Mutex
	entries map[string]*list.Element // key -> element whose Value is *bootstrapAttempt
	order   *list.List               // front = oldest window start
	global  bootstrapAttempt

	maxPerKey int
	globalMax int
	maxKeys   int
	window    time.Duration
	now       func() time.Time // injectable for tests
}

func newBootstrapLimiter() *bootstrapLimiter {
	return &bootstrapLimiter{
		entries:   map[string]*list.Element{},
		order:     list.New(),
		maxPerKey: bootstrapMaxAttempts,
		globalMax: bootstrapGlobalMaxAttempts,
		maxKeys:   bootstrapMaxTrackedKeys,
		window:    bootstrapWindowDuration,
		now:       time.Now,
	}
}

// bootstrapLimits is the limiter handleBootstrap uses.
var bootstrapLimits = newBootstrapLimiter()

// bootstrapRateKey is the limiter key for a client address: clientPrefixKey
// (the address for IPv4 and LAN IPv6, the /64 for public IPv6), or the one shared
// untrustedLocalClientKey when the client is not attributed or ip does not
// parse.
func bootstrapRateKey(ip string, attributed bool) string {
	if !attributed {
		return untrustedLocalClientKey
	}
	key, ok := clientPrefixKey(ip)
	if !ok {
		return untrustedLocalClientKey
	}
	return key
}

// bootstrapDirectKey is the bucket of a direct LAN peer: its FULL address,
// with a prefix of its own. Not the /64: on an IPv6 ULA network every machine
// on the LAN shares one /64, so a /64 key would let one LAN device spend the
// owner's attempts. The prefix keeps a direct bucket from ever being the same
// key as a tunnel visitor's. directLANPeer already parsed the address,
// so an unparseable one cannot reach here; it falls back to the shared key.
func bootstrapDirectKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return untrustedLocalClientKey
	}
	return "direct:" + a.WithZone("").Unmap().String()
}

// bootstrapUnlistedLANTotalKey is the window every unlisted LAN request
// shares (bootstrapUnlistedLANTotalFactor).
const bootstrapUnlistedLANTotalKey = "unlisted-lan:all"

// bootstrapAgg is an aggregate window a request also counts against, with
// room for factor times the per-key cap.
type bootstrapAgg struct {
	key    string
	factor int
}

// bootstrapClientKey returns the bootstrap limiter key for the request, the
// aggregate windows it also counts against, the address to log, and whether
// the request is exempt from the global ceiling (a direct LAN peer,
// directLANPeer). The aggregates are bootstrapDirectLANKey for a direct LAN
// IPv6 peer; for an unlisted LAN host (Cloudflare markers, keyed on its own
// address, classifyLANPeer) its "unlisted-lan:" /64 or /24 window and the
// unlisted LAN total; none for anything else. Every request that is not
// direct is keyed through trustedClientIP: a visitor's own address when it
// can be believed, otherwise the shared untrustedLocalClientKey. A connector
// request with no usable CF-Connecting-IP is therefore untrusted-local and
// NOT exempt.
func bootstrapClientKey(c *gin.Context, connectors connectorPeers, isLocalPeer func(string) bool) (key string, aggs []bootstrapAgg, logIP string, exempt bool) {
	if directLANPeer(c, connectors) {
		a, _ := peerAddr(c) // directLANPeer already parsed it
		ip := a.String()
		if k := bootstrapDirectLANKey(ip); k != "" {
			aggs = []bootstrapAgg{{key: k, factor: bootstrapLANPrefixFactor}}
		}
		return bootstrapDirectKey(ip), aggs, ip, true
	}
	ip, attributed := trustedClientIP(c, clientTrust{isLocalPeer: isLocalPeer, connectors: connectors})
	ip = strings.TrimSpace(ip)
	key = bootstrapRateKey(ip, attributed)
	if attributed {
		// A LAN host sending Cloudflare markers (not direct) and keyed on
		// its own address: its prefix shares a window of its own, never the
		// direct one, and all such traffic shares the unlisted total.
		if lp := classifyLANPeer(c, ip, connectors); lp.unlisted {
			if lp.prefix != "" {
				aggs = append(aggs, bootstrapAgg{key: "unlisted-lan:" + lp.prefix, factor: bootstrapLANPrefixFactor})
			}
			aggs = append(aggs, bootstrapAgg{key: bootstrapUnlistedLANTotalKey, factor: bootstrapUnlistedLANTotalFactor})
		}
	}
	return key, aggs, ip, false
}

// bootstrapDirectLANKey is the aggregate window key of a DIRECT LAN IPv6
// peer's /64, or "" for any other address. It is given only to direct peers
// (directLANPeer), under a prefix of its own: tunnel traffic, including an
// unlisted connector on a LAN address in the same /64, never touches it, so
// it cannot spend the window the owner's direct recovery depends on. Tunnel
// traffic has the global ceiling instead, and a LAN host sending Cloudflare
// markers its own "unlisted-lan:" window and the unlisted total.
func bootstrapDirectLANKey(ip string) string {
	if prefix, ok := lanIPv6Prefix(ip); ok {
		return "direct-lan64:" + prefix
	}
	return ""
}

// sweepLocked drops every window that has ended. Entries are in creation
// order, so it stops at the first live one: cost is O(dropped).
func (l *bootstrapLimiter) sweepLocked(now time.Time) {
	for el := l.order.Front(); el != nil; el = l.order.Front() {
		if now.Sub(el.Value.(*bootstrapAttempt).firstAt) <= l.window {
			return
		}
		l.removeLocked(el)
	}
}

func (l *bootstrapLimiter) removeLocked(el *list.Element) {
	delete(l.entries, el.Value.(*bootstrapAttempt).key)
	l.order.Remove(el)
}

// allow records one attempt for key and for each aggregate window in aggs,
// and reports whether it may proceed. Every window must admit the attempt,
// and the checks and the increments happen under one lock, all checks first,
// so they are atomic: a refused attempt is not recorded anywhere, so it
// spends no window and no global budget. exemptFromGlobal (a direct LAN peer)
// skips the global ceiling entirely: it is neither refused by it nor counted
// against it; its aggregates still apply to it.
func (l *bootstrapLimiter) allow(key string, exemptFromGlobal bool, aggs ...bootstrapAgg) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)

	if l.countLocked(key) >= l.maxPerKey {
		return false
	}
	for _, a := range aggs {
		if l.countLocked(a.key) >= a.factor*l.maxPerKey {
			return false
		}
	}
	if !exemptFromGlobal {
		if l.global.count == 0 || now.Sub(l.global.firstAt) > l.window {
			l.global = bootstrapAttempt{firstAt: now}
		}
		if l.global.count >= l.globalMax {
			return false
		}
		l.global.count++
	}
	l.incrementLocked(key, now)
	for _, a := range aggs {
		l.incrementLocked(a.key, now)
	}
	return true
}

// countLocked is key's admitted attempts in its live window (0 for none).
func (l *bootstrapLimiter) countLocked(key string) int {
	if el, ok := l.entries[key]; ok {
		return el.Value.(*bootstrapAttempt).count
	}
	return 0
}

// incrementLocked records one admitted attempt for key, opening its window
// (and evicting the oldest window at the cap) when it has none.
func (l *bootstrapLimiter) incrementLocked(key string, now time.Time) {
	if el, ok := l.entries[key]; ok {
		el.Value.(*bootstrapAttempt).count++
		return
	}
	for l.maxKeys > 0 && l.order.Len() >= l.maxKeys {
		l.removeLocked(l.order.Front())
	}
	l.entries[key] = l.order.PushBack(&bootstrapAttempt{key: key, count: 1, firstAt: now})
}
