// file: internal/server/peer_allowlist.go
// version: 1.1.1
// guid: 3f1c1ef0-2117-4b31-88a6-7c7a04767f3b
// last-edited: 2026-10-21

package server

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

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
// Loopback trust: anything on this host reaches the service as loopback and is
// allowed, so a reverse proxy, port forwarder (socat, ssh -L, a container with
// host networking) or any local process that relays other clients re-exposes
// the origin to those clients, and this gate cannot tell them apart. Run no
// such relay on the host, or give it its own authentication.
//
// The peer is the socket's remote address (Request.RemoteAddr), never a
// header: X-Forwarded-For and CF-Connecting-IP are the caller's own claims.
// This host's own LAN address is not admitted implicitly: a local process that
// connects to it, or a container on a bridge, arrives from that address or the
// bridge subnet and must be covered by a CIDR like any other peer.
//
// HTTP/3 0-RTT is off: early data is accepted before the peer's address is
// validated, which would let a refused peer present a spoofed source.
//
// The gate wraps the handler of every listener (HTTPS/HTTP2, HTTP/3, plain
// HTTP and the port-80 redirect), so it is outermost rather than a gin
// middleware: gin never sees a refused request. For HTTP/3 the QUIC handshake
// completes before the 403; the gate refuses requests, it does not hide the
// UDP port.

// peerAllowList is the parsed ALLOWED_PEER_CIDRS: CIDR prefixes (a bare address
// being a full-length one) and zoned IPv6 link-local addresses, which are only
// unique per interface and so are matched exactly, zone included, like
// TUNNEL_CONNECTOR_ADDRS entries.
type peerAllowList struct {
	prefixes []netip.Prefix
	zoned    []netip.Addr
}

// parsePeerAllowList parses a comma-separated list of CIDR networks and bare
// addresses. Entries that cannot be used are returned in invalid with the
// reason, never trusted. IPv4-mapped forms are unmapped (::ffff:192.0.2.0/120
// is 192.0.2.0/24). A CIDR carries no zone, so fe80::/10 admits link-local
// peers on any interface; a bare IPv6 link-local address must carry its zone
// (fe80::5%eth0) that names an interface of this host.
func parsePeerAllowList(raw string) (list peerAllowList, invalid []string) {
	bad := func(e, why string) { invalid = append(invalid, e+" ("+why+")") }
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			p, err := netip.ParsePrefix(e)
			if err != nil {
				bad(e, "not a CIDR network")
				continue
			}
			a, bits := p.Addr().WithZone(""), p.Bits()
			if a.Is4In6() {
				if bits < 96 {
					// A prefix shorter than the mapped block cannot be written
					// as an IPv4 prefix; refuse rather than guess.
					bad(e, "IPv4-mapped prefix shorter than /96")
					continue
				}
				a, bits = a.Unmap(), bits-96
			}
			list.prefixes = append(list.prefixes, netip.PrefixFrom(a, bits).Masked())
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			bad(e, "not an IP address")
			continue
		}
		a = a.Unmap()
		if needsZone(a) {
			if a.Zone() == "" {
				bad(e, "IPv6 link-local address without a zone; write it as fe80::5%eth0")
				continue
			}
			name, m := resolveZone(a.Zone())
			if m != zoneFound {
				bad(e, "zone is not an interface of this host, or is ambiguous")
				continue
			}
			list.zoned = append(list.zoned, a.WithZone(name))
			continue
		}
		a = a.WithZone("")
		list.prefixes = append(list.prefixes, netip.PrefixFrom(a, a.BitLen()))
	}
	return list, invalid
}

// contains reports whether a (in trust form, parseTrustAddr) is allowed.
func (l peerAllowList) contains(a netip.Addr) bool {
	for _, z := range l.zoned {
		if z == a {
			return true
		}
	}
	a = a.WithZone("").Unmap()
	for _, p := range l.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// openPrefixes returns the entries that match every address (0.0.0.0/0, ::/0).
func (l peerAllowList) openPrefixes() []string {
	var out []string
	for _, p := range l.prefixes {
		if p.Bits() == 0 {
			out = append(out, p.String())
		}
	}
	return out
}

// String is the effective list, for the startup log.
func (l peerAllowList) String() string {
	parts := make([]string, 0, len(l.prefixes)+len(l.zoned))
	for _, p := range l.prefixes {
		parts = append(parts, p.String())
	}
	for _, z := range l.zoned {
		parts = append(parts, z.String())
	}
	return strings.Join(parts, ", ")
}

// peerAllowParsed is one parse of one ALLOWED_PEER_CIDRS value.
type peerAllowParsed struct {
	raw  string
	list peerAllowList
}

// peerAllowCache holds the parse of the current value: parsed once per distinct
// value, read lock-free on every request. The value is environment-only and
// fixed for the life of the process, so a second distinct value only happens
// in tests.
type peerAllowCache struct {
	cur atomic.Pointer[peerAllowParsed]
}

var peerAllowed peerAllowCache

func (pc *peerAllowCache) get(raw string) peerAllowList {
	if p := pc.cur.Load(); p != nil && p.raw == raw {
		return p.list
	}
	list, invalid := parsePeerAllowList(raw)
	next := &peerAllowParsed{raw: raw, list: list}
	// Only the goroutine whose store wins reports, so a burst of first
	// requests logs once.
	if old := pc.cur.Load(); pc.cur.CompareAndSwap(old, next) {
		for _, e := range invalid {
			clientTrustLog.Warn("ALLOWED_PEER_CIDRS entry %s; ignored", e)
		}
	}
	return list
}

// validatePeerAllowList is the startup check: any entry that cannot be used is
// an error, so a typo stops the service instead of silently narrowing (or, with
// the empty fallback, widening) who may connect. An empty value is valid (allow
// all, with a warning).
func validatePeerAllowList(raw string) error {
	_, invalid := parsePeerAllowList(raw)
	if len(invalid) > 0 {
		return fmt.Errorf("ALLOWED_PEER_CIDRS has %d unusable entr(ies): %s", len(invalid), strings.Join(invalid, "; "))
	}
	return nil
}

// peerAllowedNow reports whether the connection peer remoteAddr may talk to this
// service. enforced is false when ALLOWED_PEER_CIDRS is empty (allow all). An
// unparseable peer address is refused when enforcing.
//
// Allowed: loopback, a TUNNEL_CONNECTOR_ADDRS entry, or an ALLOWED_PEER_CIDRS
// entry, and nothing else. This host's own interface addresses are deliberately
// NOT admitted on their own: a process here that connects to the host's LAN
// address arrives from that address, so the operator's list has to cover the
// LAN subnet (it does for any list that lets the LAN in at all), and the gate
// never widens the list implicitly.
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
		// Defence in depth for HTTP/3 0-RTT: early data arrives before the
		// handshake finishes and, in quic-go, before the peer's address is
		// validated, so RemoteAddr could be spoofed. The HTTP/3 server turns
		// 0-RTT off (http3ServerQUICConfig); this refuses any request that
		// still arrives early, whatever its peer address says.
		if r.TLS != nil && !r.TLS.HandshakeComplete {
			http.Error(w, "too early", http.StatusTooEarly)
			return
		}
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

// startupPeerAllowList is the startup half: it refuses to start on an unusable
// entry and logs the effective list. An empty value is valid and warns that
// every peer is accepted; a match-everything entry (0.0.0.0/0, ::/0) warns that
// the list restricts nothing.
func startupPeerAllowList() error {
	raw := config.AllowedPeerCIDRs()
	if raw == "" {
		lifecycleLog.Warn("ALLOWED_PEER_CIDRS is not set: this origin accepts a connection from any peer. Set it to the networks that should reach it (TUNNEL_CONNECTOR_ADDRS and loopback are always allowed)")
		return nil
	}
	if err := validatePeerAllowList(raw); err != nil {
		return err
	}
	list := peerAllowed.get(raw)
	lifecycleLog.Info("ALLOWED_PEER_CIDRS: only loopback, TUNNEL_CONNECTOR_ADDRS and [%s] may connect", list)
	if open := list.openPrefixes(); len(open) > 0 {
		lifecycleLog.Warn("ALLOWED_PEER_CIDRS contains %s, which matches every peer: the allow-list restricts nothing", strings.Join(open, ", "))
	}
	return nil
}
