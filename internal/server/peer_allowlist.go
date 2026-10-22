// file: internal/server/peer_allowlist.go
// version: 1.0.0
// guid: 3f1c1ef0-2117-4b31-88a6-7c7a04767f3b
// last-edited: 2026-10-21

package server

import (
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Peer allow-list (ALLOWED_PEER_CIDRS).
//
// Production reaches this service only through the Cloudflare tunnel
// connectors and the LAN, but the listener binds every interface. If a port
// were ever exposed (a router forward, a cloud security group, a VLAN that
// should not exist) the whole API would answer anyone on the internet.
// The allow-list is the origin's own statement of who may talk to it, checked
// on the connection's peer address before any routing, so no route, however
// it was registered, can answer a peer that is not listed.
//
// A peer is allowed when it is loopback, a TUNNEL_CONNECTOR_ADDRS entry, or
// inside an ALLOWED_PEER_CIDRS entry. Loopback is always allowed so the local
// health check, the server-bootstrap flow and an operator on the host keep
// working with a wrong list; the connectors are always allowed because the
// tunnel is the production path and listing them twice is a trap.
//
// The peer is the socket's remote address (Request.RemoteAddr), never a
// header: X-Forwarded-For and CF-Connecting-IP are the caller's own claims.
// That also means this host's OWN non-loopback addresses must be listed if
// anything on the host reaches the service through them.
//
// The gate wraps the handler of every listener (HTTPS/HTTP2, HTTP/3, plain
// HTTP and the port-80 redirect), so it is outermost rather than a gin
// middleware: gin never sees a refused request. For HTTP/3 the QUIC handshake
// completes before the 403; the gate refuses requests, it does not hide the
// UDP port.

// peerAllowList is the parsed ALLOWED_PEER_CIDRS: CIDR prefixes, a bare
// address being a full-length one.
type peerAllowList []netip.Prefix

// parsePeerAllowList parses a comma-separated list of CIDR networks and bare
// addresses. Entries that are neither are returned in invalid, never trusted.
// IPv4-mapped forms are unmapped (::ffff:192.0.2.0/120 is 192.0.2.0/24) and a
// zone is dropped, so fe80::/10 admits link-local peers on any interface.
func parsePeerAllowList(raw string) (list peerAllowList, invalid []string) {
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			p, err := netip.ParsePrefix(e)
			if err != nil {
				invalid = append(invalid, e)
				continue
			}
			a, bits := p.Addr().WithZone(""), p.Bits()
			if a.Is4In6() {
				if bits < 96 {
					// A prefix shorter than the mapped block cannot be written
					// as an IPv4 prefix; refuse rather than guess.
					invalid = append(invalid, e)
					continue
				}
				a, bits = a.Unmap(), bits-96
			}
			list = append(list, netip.PrefixFrom(a, bits).Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			invalid = append(invalid, e)
			continue
		}
		a = a.WithZone("").Unmap()
		list = append(list, netip.PrefixFrom(a, a.BitLen()))
	}
	return list, invalid
}

// contains reports whether a is inside any entry. An address with a zone is
// compared without it (netip never matches a zoned address).
func (l peerAllowList) contains(a netip.Addr) bool {
	a = a.WithZone("").Unmap()
	for _, p := range l {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// peerAllowCache is the parsed ALLOWED_PEER_CIDRS, parsed once per distinct
// value (the connectorCache pattern, minus the retry: nothing here depends on
// an interface appearing later).
type peerAllowCache struct {
	mu   sync.Mutex
	set  bool
	raw  string
	list peerAllowList
}

var peerAllowed peerAllowCache

func (pc *peerAllowCache) get(raw string) peerAllowList {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.set && raw == pc.raw {
		return pc.list
	}
	list, invalid := parsePeerAllowList(raw)
	pc.set, pc.raw, pc.list = true, raw, list
	for _, e := range invalid {
		clientTrustLog.Warn("ALLOWED_PEER_CIDRS entry %q is not a CIDR network or an IP address; ignored", e)
	}
	if raw != "" && len(list) == 0 {
		clientTrustLog.Warn("ALLOWED_PEER_CIDRS is set but has no valid entry: only loopback and TUNNEL_CONNECTOR_ADDRS peers are allowed")
	}
	return list
}

// peerAllowedNow reports whether the connection peer remoteAddr may talk to this
// service. enforced is false when ALLOWED_PEER_CIDRS is empty (allow all). An
// unparseable peer address is refused when enforcing.
func peerAllowedNow(remoteAddr string) (allowed, enforced bool) {
	raw := config.AllowedPeerCIDRs()
	if raw == "" {
		return true, false
	}
	a, ok := parseTrustAddr(remoteAddr)
	if !ok {
		return false, true
	}
	if a.IsLoopback() || tunnelConnectorPeers().contains(remoteAddr) {
		return true, true
	}
	return peerAllowed.get(raw).contains(a), true
}

// deniedPeerLogCap bounds the peers remembered for logging: once per peer, never
// per request, so a scan cannot fill the journal. An IPv6 peer is remembered
// by its /64.
const deniedPeerLogCap = 1024

// newPeerAllowListHandler wraps next so a request from a peer outside the
// allow-list is answered 403 with no body detail and next never runs. With
// ALLOWED_PEER_CIDRS empty it passes everything through.
func newPeerAllowListHandler(next http.Handler) http.Handler {
	logged := newOnceSet(deniedPeerLogCap)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, _ := peerAllowedNow(r.RemoteAddr)
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
		key := r.RemoteAddr
		if a, ok := parseTrustAddr(r.RemoteAddr); ok {
			key = a.WithZone("").String()
			if a.Is6() {
				key = netip.PrefixFrom(a.WithZone(""), 64).Masked().String()
			}
		}
		switch logged.add(key) {
		case onceSetAdded:
			// The peer address only: no path, query or header of a request
			// from a stranger goes into the journal.
			clientTrustLog.Warn("refused a connection from peer %s: not in ALLOWED_PEER_CIDRS (logged once per peer)", key)
		case onceSetFull:
			clientTrustLog.Warn("more than %d distinct peers refused; no longer logging new ones", deniedPeerLogCap)
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

// warnPeerAllowListOpen logs, once at startup, that every peer is accepted.
func warnPeerAllowListOpen() {
	if config.AllowedPeerCIDRs() == "" {
		lifecycleLog.Warn("ALLOWED_PEER_CIDRS is not set: this origin accepts a connection from any peer. Set it to the networks that should reach it (the tunnel connectors in TUNNEL_CONNECTOR_ADDRS and loopback are always allowed)")
	}
}
