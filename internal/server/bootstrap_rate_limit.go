// file: internal/server/bootstrap_rate_limit.go
// version: 1.4.0
// guid: df3037e5-6d7d-452f-9474-a8d0b051a31b
// last-edited: 2026-10-20

package server

import (
	"container/list"
	"net"
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
//     because LAN devices share one ULA /64;
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
// (bootstrapDirectLANPeer says which do not). The owner runs bootstrap
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
// key as a tunnel visitor's. bootstrapDirectLANPeer already parsed the address,
// so an unparseable one cannot reach here; it falls back to the shared key.
func bootstrapDirectKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return untrustedLocalClientKey
	}
	return "direct:" + a.WithZone("").Unmap().String()
}

// bootstrapClientKey returns the bootstrap limiter key for the request, the
// address to log, and whether the request is exempt from the global ceiling
// (a direct LAN peer, bootstrapDirectLANPeer). Every other request is keyed
// through trustedClientIP: a visitor's own address when it can be believed,
// otherwise the shared untrustedLocalClientKey. A connector request with no
// usable CF-Connecting-IP is therefore untrusted-local and NOT exempt.
func bootstrapClientKey(c *gin.Context, connectors connectorPeers, isLocalPeer func(string) bool) (key, logIP string, exempt bool) {
	if bootstrapDirectLANPeer(c, connectors) {
		ip := strings.TrimSpace(c.ClientIP())
		return bootstrapDirectKey(ip), ip, true
	}
	ip, attributed := trustedClientIP(c, clientTrust{isLocalPeer: isLocalPeer, connectors: connectors})
	ip = strings.TrimSpace(ip)
	return bootstrapRateKey(ip, attributed), ip, false
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

// bootstrapDirectLANPeer reports whether the request is a direct caller on this
// network rather than tunnel or proxy traffic: the peer is a private
// (RFC 1918 / ULA) or loopback address, is not a configured tunnel connector,
// and the request carries none of the Cloudflare markers
// (hasCloudflareMarkers). Cloudflare's edge adds those to everything it
// proxies and the connector passes them through, so tunnel traffic is never
// direct even though the connectors reach this service from private LAN
// addresses. A direct caller keeps only its own per-address bucket and is
// exempt from the global ceiling. With TUNNEL_CONNECTOR_ADDRS unset, a
// connector request stripped of every Cloudflare header on the way would
// count as direct too (see trustedClientIP).
func bootstrapDirectLANPeer(c *gin.Context, connectors connectorPeers) bool {
	peer := c.Request.RemoteAddr
	if connectors.contains(peer) {
		return false
	}
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		host = peer
	}
	a, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return false
	}
	a = a.Unmap()
	if !a.IsLoopback() && !a.IsPrivate() {
		return false
	}
	return !hasCloudflareMarkers(c)
}

// allow records one attempt for key and reports whether it may proceed. A
// refused attempt is not recorded anywhere, so it spends neither the key's nor
// the global budget. exemptFromGlobal (a direct LAN peer) skips the global
// ceiling entirely: it is neither refused by it nor counted against it.
func (l *bootstrapLimiter) allow(key string, exemptFromGlobal bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)

	el, tracked := l.entries[key]
	if tracked && el.Value.(*bootstrapAttempt).count >= l.maxPerKey {
		return false
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
	if tracked {
		el.Value.(*bootstrapAttempt).count++
		return true
	}
	for l.maxKeys > 0 && l.order.Len() >= l.maxKeys {
		l.removeLocked(l.order.Front())
	}
	l.entries[key] = l.order.PushBack(&bootstrapAttempt{key: key, count: 1, firstAt: now})
	return true
}
