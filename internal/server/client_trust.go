// file: internal/server/client_trust.go
// version: 1.2.0
// guid: aaef6f46-d2c0-4e29-aaf4-2b93e15c6c37
// last-edited: 2026-10-20

package server

import (
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// untrustedLocalClientKey is the ONE rate-limit identity the bootstrap limiter
// gives every request whose real client address cannot be established (see
// trustedClientIP): a configured connector or this host with no trusted
// header. It is never derived from the request's own headers. The invite limiter skips its
// per-client layer for such a request instead, because one shared bucket would
// let a single caller lock out every guest.
const untrustedLocalClientKey = "untrusted-local"

// connectorPeers is the set of addresses the Cloudflare tunnel connectors
// connect to this service from (config TUNNEL_CONNECTOR_ADDRS).
type connectorPeers []netip.Addr

// contains reports whether remoteAddr ("host:port" or a bare host) is one of
// the configured connector addresses. IPv4-mapped IPv6 forms match their IPv4
// address.
func (p connectorPeers) contains(remoteAddr string) bool {
	if len(p) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	a, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, c := range p {
		if c == a {
			return true
		}
	}
	return false
}

// parseConnectorPeers parses a comma-separated address list. Entries that are
// not IP addresses are returned in bad and are NOT trusted.
func parseConnectorPeers(csv string) (peers connectorPeers, bad []string) {
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			bad = append(bad, part)
			continue
		}
		peers = append(peers, a.Unmap())
	}
	return peers, bad
}

var clientTrustLog = logger.New("server.client-trust")

var (
	connectorPeersMu  sync.Mutex
	connectorPeersRaw string
	connectorPeersVal connectorPeers
	connectorPeersSet bool
)

// tunnelConnectorPeers returns the configured connector addresses, parsed once
// per distinct configured value; an invalid entry is logged once and ignored.
func tunnelConnectorPeers() connectorPeers {
	raw := config.TunnelConnectorAddrs()
	connectorPeersMu.Lock()
	defer connectorPeersMu.Unlock()
	if !connectorPeersSet || raw != connectorPeersRaw {
		peers, bad := parseConnectorPeers(raw)
		if len(bad) > 0 {
			clientTrustLog.Warn("TUNNEL_CONNECTOR_ADDRS has %d entries that are not IP addresses; ignored", len(bad))
		}
		connectorPeersRaw, connectorPeersVal, connectorPeersSet = raw, peers, true
	}
	return connectorPeersVal
}

// clientTrust is what trustedClientIP needs to decide whose word to take.
type clientTrust struct {
	// isLocalPeer reports whether an address is this host (loopback or one of its
	// own interface addresses). Injectable for tests.
	isLocalPeer func(addr string) bool
	// connectors are the configured tunnel connector addresses.
	connectors connectorPeers
}

// hasCloudflareMarkers reports whether the request carries anything
// Cloudflare's edge adds to the requests it proxies: any Cf-* header (which
// covers CF-Connecting-IP, Cf-Ray and Cf-Access-*) or the CF_Authorization
// cookie. Tunnel traffic always carries them, whichever host the connector
// runs on, and the connector does not strip them. bootstrapDirectLANPeer uses
// it to tell a direct LAN caller from tunnel traffic; it never decides whose
// address a request is keyed on, since anyone can send the headers.
func hasCloudflareMarkers(c *gin.Context) bool {
	for k := range c.Request.Header {
		if strings.HasPrefix(strings.ToLower(k), "cf-") {
			return true
		}
	}
	_, err := c.Request.Cookie("CF_Authorization")
	return err == nil
}

// clientPrefixKey is the rate-limit identity for a client address: the address
// itself for IPv4 (and IPv4-mapped IPv6), its /64 for IPv6, so a caller who
// owns a /64 cannot mint 2^64 buckets. ok is false when ip does not parse;
// callers then treat the client as unattributed and never fall back to text
// taken from the request.
func clientPrefixKey(ip string) (key string, ok bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", false
	}
	a = a.WithZone("").Unmap()
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String(), true
	}
	return a.String(), true
}

// trustedClientIP returns the address a per-client limiter should key the
// request on, and whether that address identifies the client.
//
// Production reaches the service through Cloudflare tunnel connectors
// (cloudflared) that run on other hosts on the LAN and connect from their own
// private addresses, so the peer the service sees is a connector, shared by
// every visitor. CF-Connecting-IP is set by Cloudflare's edge, which overwrites
// it on every request it proxies, but the service cannot see the edge: anyone
// who can reach the service port can send the header with a value of its
// choosing, so neither "the peer is local" nor "the peer is private" proves
// Cloudflare set it. The header is therefore believed only when one of these
// holds:
//
//   - the peer is an address the operator configured as a tunnel connector
//     (TUNNEL_CONNECTOR_ADDRS), the explicit statement that this peer is
//     cloudflared; or
//   - the request carries a Cf-Access-Jwt-Assertion that CloudflareAccessAuth
//     verified AND the peer is this host (a connector on the same host): the
//     request provably passed Access at the edge.
//
// The header is parsed with net.ParseIP and re-rendered with String() so that
// equivalent spellings share one key; an unparseable value counts as absent.
//
// Every other request is keyed on c.ClientIP(), the peer's own address (the
// router trusts no proxy headers), so a spoofed header is ignored.
//
// ok is false only when the peer is a configured connector or this host and
// no trusted header names the visitor: the peer's address is then shared by
// every visitor that came through it.
//
// Any other peer is keyed on its own address (callers group IPv6 by /64,
// clientPrefixKey), whatever headers it sends, so adding a Cf-* header never
// takes a caller out of per-client limiting:
//
//   - a public peer is never a LAN connector, so it is always limited per
//     address;
//   - a private peer that is not a listed connector but carries Cloudflare
//     markers (hasCloudflareMarkers) is most likely a connector nobody listed;
//     it gets one bucket for its own address, shared by everyone it relays.
//     TUNNEL_CONNECTOR_ADDRS lists every connector in production, so this
//     only happens when the setting is unset or incomplete, and then each
//     source address is still limited;
//   - a private peer with no markers is a direct caller, keyed on its own
//     address too. With TUNNEL_CONNECTOR_ADDRS unset, a connector request
//     stripped of every Cloudflare header on the way would look exactly like
//     one; the edge adds them and cloudflared passes them through, so that
//     needs something in between rewriting requests.
//
// ip is still returned when ok is false, for logging. Callers must never
// derive an identity from the header then: the invite limiter skips its
// per-client layer, and the bootstrap limiter uses the fixed
// untrustedLocalClientKey.
func trustedClientIP(c *gin.Context, t clientTrust) (ip string, ok bool) {
	peer := c.Request.RemoteAddr
	isConnector := t.connectors.contains(peer)
	if isConnector || (servermiddleware.CFAccessVerified(c) && t.isLocalPeer(peer)) {
		if parsed := net.ParseIP(strings.TrimSpace(c.GetHeader("CF-Connecting-IP"))); parsed != nil {
			return parsed.String(), true
		}
	}
	ip = c.ClientIP()
	return ip, !isConnector && !t.isLocalPeer(ip)
}
