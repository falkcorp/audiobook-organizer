// file: internal/server/bootstrap_gate.go
// version: 2.0.0
// guid: df3037e5-6d7d-452f-9474-a8d0b051a31b
// last-edited: 2026-11-10

package server

import (
	"net/netip"
	"strings"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/gin-gonic/gin"
)

// Bootstrap attempt keying: which windows of core's attempt limiter
// (coreauth.BootstrapLimiter, 05-authn §4.6 item 2, moved from this file at
// F4-02) a bootstrap request counts against. The limiter (the per-key window,
// the global ceiling across all keys, the sweep and the key cap) lives in
// core; what stays here is how ABO's perimeter turns a gin request into keys,
// and that goes with the legacy routes at F4-08.
//
//   - the key is an IPv4 address or an IPv6 /64 (bootstrapRateKey), so an
//     attacker who owns a /64 cannot mint 2^64 keys, and an unparseable or
//     unattributed client uses one fixed key rather than attacker-chosen text;
//     a direct LAN peer is keyed on its full address (bootstrapDirectKey),
//     because LAN devices share one ULA /64, and every address of a direct
//     LAN IPv6 /64 also shares one aggregate window of
//     coreauth.BootstrapLANPrefixFactor times the per-key cap, so one LAN host
//     cannot rotate through its /64 for fresh windows (tunnel traffic never
//     touches that window), and all direct LAN traffic except loopback shares
//     one direct total (bootstrapDirectLANTotalKey, half the global ceiling),
//     so one LAN host cannot rotate IPv4 aliases or /64s for fresh windows
//     either; a LAN host sending Cloudflare markers but not a listed
//     connector ("unlisted", classifyLANPeer) gets, on top of the global
//     ceiling, its own /64 (IPv6) or /24 (IPv4) aggregate ("unlisted-lan:")
//     and the unlisted LAN total (bootstrapUnlistedLANTotalKey, a quarter of
//     the global ceiling), so one LAN host rotating through many addresses,
//     /64s or /24s can never spend the global budget for everyone else.
//
// The global ceiling applies to EVERY request, a direct LAN peer included. Until
// 2026-10-21 a direct LAN peer (directLANPeer) was exempt from it, so that
// internet traffic spending the global budget could not lock the owner out of
// recovery. That exemption was a hole rather than a convenience: "direct LAN
// peer" is only a statement about the TCP peer's address and the absence of
// Cloudflare headers, and anything that can reach the port without going
// through the tunnel qualifies (any LAN host, any container bridge, as long
// as the host firewall lets it), so an off-tunnel caller got an attempt budget
// the tunnel's visitors could not touch, bounded only by its own aggregates.
// A direct peer still has its own per-address bucket and its aggregates below;
// they are now additional to the ceiling, not instead of it. It takes
// BootstrapGlobalMaxAttempts/BootstrapMaxAttempts distinct clients (20) to
// spend the global budget. What the owner loses is the guarantee that the
// recovery path survives that: the limiter is in memory, so restarting the
// service (the first step of the server-bootstrap flow) clears it. The tunnel
// connectors are themselves LAN hosts with private addresses, so "private
// peer" alone does not mean direct: a configured connector, or any request
// with Cloudflare markers, is tunnel traffic.

// bootstrapLimits is the limiter handleBootstrap uses.
var bootstrapLimits = coreauth.NewBootstrapLimiter(coreauth.BootstrapLimits{}, nil)

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
// shares (coreauth.BootstrapUnlistedLANTotalFactor).
const bootstrapUnlistedLANTotalKey = "unlisted-lan:all"

// bootstrapDirectLANTotalKey is the window every direct LAN request except
// loopback shares (coreauth.BootstrapDirectLANTotalFactor).
const bootstrapDirectLANTotalKey = "direct-lan:all"

// bootstrapClientKey returns the bootstrap limiter key for the request, the
// aggregate windows it also counts against, and the address to log. Every
// request also counts against the global ceiling (BootstrapLimiter.Allow),
// a direct LAN peer (directLANPeer) included. The aggregates are, for a direct LAN peer, its IPv6 /64's
// window (bootstrapDirectLANKey) and, unless it is loopback, the direct total
// (bootstrapDirectLANTotalKey); for an unlisted LAN host (Cloudflare markers, keyed on its own
// address, classifyLANPeer) its "unlisted-lan:" /64 or /24 window and the
// unlisted LAN total; none for anything else. Every request that is not
// direct is keyed through trustedClientIP: a visitor's own address when it
// can be believed, otherwise the shared untrustedLocalClientKey. A connector
// request with no usable CF-Connecting-IP is therefore untrusted-local.
func bootstrapClientKey(c *gin.Context, connectors connectorPeers, isLocalPeer func(string) bool) (key string, aggs []coreauth.LimiterAggregate, logIP string) {
	if directLANPeer(c, connectors) {
		a, _ := peerKeyAddr(c) // directLANPeer already parsed it
		ip := a.String()
		if k := bootstrapDirectLANKey(ip); k != "" {
			aggs = append(aggs, coreauth.LimiterAggregate{Key: k, Factor: coreauth.BootstrapLANPrefixFactor})
		}
		if !a.IsLoopback() {
			aggs = append(aggs, coreauth.LimiterAggregate{Key: bootstrapDirectLANTotalKey, Factor: coreauth.BootstrapDirectLANTotalFactor})
		}
		return bootstrapDirectKey(ip), aggs, ip
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
				aggs = append(aggs, coreauth.LimiterAggregate{Key: "unlisted-lan:" + lp.prefix, Factor: coreauth.BootstrapLANPrefixFactor})
			}
			aggs = append(aggs, coreauth.LimiterAggregate{Key: bootstrapUnlistedLANTotalKey, Factor: coreauth.BootstrapUnlistedLANTotalFactor})
		}
	}
	return key, aggs, ip
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
