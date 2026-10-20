// file: internal/server/client_trust.go
// version: 1.0.0
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

// untrustedLocalClientKey is the ONE rate-limit identity of every request whose
// real client address cannot be established (see trustedClientIP). Callers
// derive their bucket key from it and never from the request's own headers.
const untrustedLocalClientKey = "untrusted-local"

// connectorPeers is the set of addresses the Cloudflare tunnel connector
// connects to this service from (config TUNNEL_CONNECTOR_ADDRS).
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

// trustedClientIP returns the address a per-client limiter should key the
// request on, and whether that address identifies the client.
//
// CF-Connecting-IP is set by Cloudflare's edge, which overwrites it on every
// request it proxies, but the service cannot see the edge: it sees only the
// peer that delivered the request. Any local process, or anything that can reach
// a loopback listener, can send the header with a value of its choosing, so
// "the peer is local" does not prove Cloudflare set it. The header is therefore
// believed only when one of these holds:
//
//   - the peer is an address the operator configured as the tunnel connector
//     (TUNNEL_CONNECTOR_ADDRS), the explicit statement that this peer is
//     cloudflared; or
//   - the request carries a Cf-Access-Jwt-Assertion that CloudflareAccessAuth
//     verified AND the peer is this host: the request provably passed Access
//     at the edge and arrived through the local connector.
//
// The header is parsed with net.ParseIP and re-rendered with String() so that
// equivalent spellings share one key; an unparseable value counts as absent.
//
// Every other request is keyed on c.ClientIP(), the peer's own address (the
// router trusts no proxy headers), so a spoofed header is ignored.
//
// ok is false when the only address available is shared: the peer is a
// configured connector or this host and no trusted header names the visitor.
// ip is still returned, for logging. Callers must then use
// untrustedLocalClientKey as the identity, never anything derived from the
// header.
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
