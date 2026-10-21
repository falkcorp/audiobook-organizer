// file: internal/server/client_trust.go
// version: 1.14.0
// guid: aaef6f46-d2c0-4e29-aaf4-2b93e15c6c37
// last-edited: 2026-10-21

package server

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
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
// connect to this service from (config TUNNEL_CONNECTOR_ADDRS), in
// trust form (trustAddr): an IPv6 link-local entry carries its zone.
type connectorPeers []netip.Addr

// contains reports whether remoteAddr ("host:port" or a bare host) is one of
// the configured connector addresses, compared in trust form (parseTrustAddr):
// IPv4-mapped IPv6 forms match their IPv4 address, and an IPv6 link-local
// address matches only with the same zone, since fe80::5 on en0 and fe80::5
// on en1 are different hosts ("[fe80::5%en0]:4000" matches fe80::5%en0, not
// fe80::5%en1).
func (p connectorPeers) contains(remoteAddr string) bool {
	if len(p) == 0 {
		return false
	}
	a, ok := parseTrustAddr(remoteAddr)
	if !ok {
		return false
	}
	for _, c := range p {
		if c == a {
			return true
		}
	}
	return false
}

// parseConnectorPeers parses a comma-separated address list. Entries that are
// not IP addresses are returned in bad, and IPv6 link-local entries without a
// zone in noZone; neither is trusted. A link-local address is only unique per
// interface, so a zoneless one would trust that address arriving on every
// interface: it must be written with its zone (fe80::5%eth0). Any other
// entry is kept without a zone, which it cannot meaningfully carry.
func parseConnectorPeers(csv string) (peers connectorPeers, bad, noZone []string) {
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
		a = trustAddr(a)
		if needsZone(a) && a.Zone() == "" {
			noZone = append(noZone, part)
			continue
		}
		peers = append(peers, a)
	}
	return peers, bad, noZone
}

// needsZone reports whether a is an IPv6 link-local address, which is unique
// only together with its zone (the interface). IPv4 link-local addresses
// carry no zone in Go's address forms, so they are compared as they are.
func needsZone(a netip.Addr) bool { return a.Is6() && a.IsLinkLocalUnicast() }

// trustAddr is a in the form trust decisions compare: IPv4-mapped forms
// unmapped, and the zone kept only on an IPv6 link-local address (needsZone).
func trustAddr(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if !needsZone(a) {
		a = a.WithZone("")
	}
	return a
}

// parseTrustAddr parses "host:port", "[v6%zone]:port" or a bare address into
// trust form (trustAddr). Every trust decision (is this peer a configured
// connector, is it this host) goes through it, never through parseKeyAddr,
// which drops the zone for rate-limit bucketing.
func parseTrustAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	var a netip.Addr
	if ap, err := netip.ParseAddrPort(s); err == nil {
		a = ap.Addr()
	} else if parsed, err := netip.ParseAddr(s); err == nil {
		a = parsed
	} else {
		return netip.Addr{}, false
	}
	return trustAddr(a), true
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
		peers, bad, noZone := parseConnectorPeers(raw)
		if len(bad) > 0 {
			clientTrustLog.Warn("TUNNEL_CONNECTOR_ADDRS has %d entries that are not IP addresses; ignored", len(bad))
		}
		for _, e := range noZone {
			clientTrustLog.Warn("TUNNEL_CONNECTOR_ADDRS entry %q is an IPv6 link-local address without a zone; ignored, because a link-local address is only unique per interface. Write it with the interface it arrives on, e.g. %s%%eth0", e, e)
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
// runs on, and the connector does not strip them. directLANPeer uses
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
// itself for IPv4 (and IPv4-mapped IPv6), its /64 for a public IPv6 address, so
// a caller who owns a /64 cannot mint 2^64 buckets. A LAN IPv6 address (ULA
// fc00::/7, link-local, loopback) is keyed on its FULL address instead: every
// machine on an IPv6 LAN shares one /64, so a /64 key would let one LAN device
// spend every other device's budget (the bootstrap limiter keys direct LAN
// peers the same way, bootstrapDirectKey). For a direct LAN peer callers add
// an aggregate bucket per /64 (lanIPv6Prefix) so rotating addresses cannot
// mint unlimited buckets. Such an address can only be a
// direct peer: it is not routable from the internet, and Cloudflare's edge
// never sets CF-Connecting-IP to one. ok is false when ip does not parse;
// callers then treat the client as unattributed and never fall back to text
// taken from the request.
func clientPrefixKey(ip string) (key string, ok bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", false
	}
	a = a.WithZone("").Unmap()
	if a.Is6() && !a.IsPrivate() && !a.IsLinkLocalUnicast() && !a.IsLoopback() {
		return netip.PrefixFrom(a, 64).Masked().String(), true
	}
	return a.String(), true
}

// lanIPv6Prefix returns the /64 of a LAN IPv6 address (ULA fc00::/7 or
// link-local), and false for anything else (IPv4, public IPv6, loopback,
// unparseable). clientPrefixKey keys such an address on its full address so
// LAN devices sharing one /64 do not share a bucket; this /64 is then the
// aggregate bucket on top of that (classifyLANPeer, separate buckets for
// direct and unlisted peers), so one LAN host rotating through addresses in
// its /64 cannot mint unlimited per-address buckets.
func lanIPv6Prefix(ip string) (string, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", false
	}
	a = a.WithZone("").Unmap()
	if !a.Is6() || !(a.IsPrivate() || a.IsLinkLocalUnicast()) {
		return "", false
	}
	return netip.PrefixFrom(a, 64).Masked().String(), true
}

// peerKeyAddr is the request's TCP peer address in KEY form (parseKeyAddr):
// zone dropped, IPv4-mapped forms unmapped. It is what a peer is keyed,
// logged and classified (LAN, loopback) on, used instead of c.ClientIP():
// gin's ClientIP returns "" for a zoned link-local RemoteAddr
// ("[fe80::1%en0]:4000"), which made a link-local client unattributed. The
// router trusts no proxy headers, so for every other address this is what
// ClientIP returns. It must never decide trust: fe80::5%en0 and
// fe80::5%en1 have the same key form but are different hosts. Trust
// decisions pass c.Request.RemoteAddr to connectorPeers.contains and
// isLocalPeer, which compare in trust form (parseTrustAddr).
func peerKeyAddr(c *gin.Context) (netip.Addr, bool) {
	return parseKeyAddr(c.Request.RemoteAddr)
}

// parseKeyAddr is servermiddleware.ParseHostAddr: the address with its zone
// dropped and IPv4-mapped forms unmapped. It is for rate-limit keys and
// logs only, where two link-local hosts sharing a bucket costs nothing; trust
// decisions use parseTrustAddr.
func parseKeyAddr(s string) (netip.Addr, bool) { return servermiddleware.ParseHostAddr(s) }

// remoteAddrIsLocal reports whether addr ("host:port" or a bare address) is
// this host: a loopback address, or an address assigned to one of its
// interfaces (hostInterfaces, addrIsLocal). An IPv6 link-local peer counts
// only when the interface that owns the address is the one named by the
// peer's zone.
func remoteAddrIsLocal(addr string) bool {
	a, ok := parseTrustAddr(addr)
	if !ok {
		return false
	}
	if a.IsLoopback() {
		return true
	}
	ifaces, err := hostInterfaces()
	if err != nil {
		return false
	}
	return addrIsLocal(a, ifaces)
}

// localIface is one of this host's interfaces and its addresses (zone
// dropped, unmapped).
type localIface struct {
	name  string
	index int
	addrs []netip.Addr
}

// hostInterfaces lists this host's interfaces and their addresses. A
// variable so tests can describe a host.
var hostInterfaces = func() ([]localIface, error) {
	nifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]localIface, 0, len(nifs))
	for _, nif := range nifs {
		li := localIface{name: nif.Name, index: nif.Index}
		addrs, err := nif.Addrs()
		if err != nil {
			continue
		}
		for _, ad := range addrs {
			if ipn, ok := ad.(*net.IPNet); ok {
				if own, ok := netip.AddrFromSlice(ipn.IP); ok {
					li.addrs = append(li.addrs, own.WithZone("").Unmap())
				}
			}
		}
		out = append(out, li)
	}
	return out, nil
}

// addrIsLocal reports whether a (trust form, parseTrustAddr) is assigned to
// one of ifaces. An IPv6 link-local address is only unique per interface,
// so it matches only on the interface its zone names (by name, or by index
// for a numeric zone); a zoneless one never matches. Any other address
// matches on any interface.
func addrIsLocal(a netip.Addr, ifaces []localIface) bool {
	bare := a.WithZone("")
	for _, li := range ifaces {
		if needsZone(a) && !zoneNames(a.Zone(), li) {
			continue
		}
		for _, own := range li.addrs {
			if own == bare {
				return true
			}
		}
	}
	return false
}

// zoneNames reports whether zone names interface li: its name, or its index
// when the zone is numeric. An empty zone names nothing.
func zoneNames(zone string, li localIface) bool {
	if zone == "" {
		return false
	}
	if zone == li.name {
		return true
	}
	n, err := strconv.Atoi(zone)
	return err == nil && n == li.index
}

// requestClientIP is the client address to record or log for a request: the
// visitor's address when trustedClientIP can establish it (a tunnel visitor's
// CF-Connecting-IP from a configured connector), otherwise the peer's own
// address, zone dropped. Never c.ClientIP(), which is a connector's address
// for every tunnel request and "" for a zoned link-local peer.
func requestClientIP(c *gin.Context) string {
	ip, _ := trustedClientIP(c, clientTrust{isLocalPeer: remoteAddrIsLocal, connectors: tunnelConnectorPeers()})
	return ip
}

// lanPeer classifies a request keyed on ip whose ip is the request's own
// peer (not a visitor address taken from CF-Connecting-IP) and not a listed
// connector:
//   - direct: a direct LAN caller (directLANPeer: LAN address, no Cloudflare
//     markers);
//   - unlisted: a LAN host sending Cloudflare markers, most likely an
//     unlisted connector or a host pretending to be one;
//   - prefix: the aggregate bucket its address shares, "" for none: the /64
//     of a LAN IPv6 address (direct or unlisted), and the /24 of an unlisted
//     private IPv4 address, so one host cannot rotate addresses (IPv6 in its
//     /64, IPv4 aliases in its /24) for fresh per-client buckets. A direct
//     IPv4 caller has no aggregate: IPv4 aliases on a LAN host are few, and
//     a /24 bucket would be shared by every device on the home network.
//
// Direct and unlisted traffic use separate buckets, so tunnel-looking traffic
// can never spend the aggregate a direct LAN caller (the owner) depends on.
// Every field is zero for any other request.
type lanPeer struct {
	direct, unlisted bool
	prefix           string
}

func classifyLANPeer(c *gin.Context, ip string, connectors connectorPeers) lanPeer {
	peer, parsed := peerKeyAddr(c)
	if !parsed || peer.String() != ip || connectors.contains(c.Request.RemoteAddr) || !isLANAddr(peer) || peer.IsLoopback() {
		return lanPeer{}
	}
	lp := lanPeer{direct: directLANPeer(c, connectors)}
	lp.unlisted = !lp.direct
	if prefix, ok := lanIPv6Prefix(ip); ok {
		lp.prefix = prefix
	} else if lp.unlisted && peer.Is4() {
		lp.prefix = netip.PrefixFrom(peer, 24).Masked().String()
	}
	return lp
}

// directLANPeer reports whether the request is a direct caller on this
// network rather than tunnel or proxy traffic: the peer is a private
// (RFC 1918 / ULA), link-local or loopback address, is not a configured
// tunnel connector,
// and the request carries none of the Cloudflare markers
// (hasCloudflareMarkers). Cloudflare's edge adds those to everything it
// proxies and the connector passes them through, so tunnel traffic is never
// direct even though the connectors reach this service from private LAN
// addresses. Both limiters exempt a direct caller from their global ceiling
// and give it LAN aggregates no other traffic can spend (classifyLANPeer).
// With TUNNEL_CONNECTOR_ADDRS unset, a
// connector request stripped of every Cloudflare header on the way would
// count as direct too (see trustedClientIP).
func directLANPeer(c *gin.Context, connectors connectorPeers) bool {
	a, ok := peerKeyAddr(c)
	if !ok || connectors.contains(c.Request.RemoteAddr) || !isLANAddr(a) {
		return false
	}
	return !hasCloudflareMarkers(c)
}

// isLANAddr reports whether a is only reachable from this network: private
// (RFC 1918 / ULA), link-local or loopback.
func isLANAddr(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLoopback()
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
// Every other request is keyed on the peer's own address (peerKeyAddr; the
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
//     it gets one bucket for its own address, shared by everyone it relays,
//     and a warning is logged once per such address (warnUnlistedConnector).
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
	a, parsed := peerKeyAddr(c)
	if !parsed {
		return "", false
	}
	peer := a.String()
	// Trust is decided on the raw RemoteAddr, zone included (parseTrustAddr);
	// peer, zone dropped, is only the key.
	isConnector := t.connectors.contains(c.Request.RemoteAddr)
	local := t.isLocalPeer(c.Request.RemoteAddr)
	if isConnector || (servermiddleware.CFAccessVerified(c) && local) {
		if parsed := net.ParseIP(strings.TrimSpace(c.GetHeader("CF-Connecting-IP"))); parsed != nil {
			return parsed.String(), true
		}
	}
	ip = peer
	if !isConnector && !local && hasCloudflareMarkers(c) {
		warnUnlistedConnector(ip)
	}
	return ip, !isConnector && !local
}

// unlistedConnectorWarnCap bounds how many distinct addresses (IPv6: /64s)
// warnUnlistedConnector remembers (and so warns about). Real deployments have
// a handful of connectors; past the cap it stops warning rather than grow.
const unlistedConnectorWarnCap = 64

// unlistedConnectors remembers the private peers already warned about.
var unlistedConnectors = newOnceSet(unlistedConnectorWarnCap)

// warnUnlistedConnector logs, once per address (once per /64 for IPv6), that a private LAN peer not
// in TUNNEL_CONNECTOR_ADDRS sent Cloudflare markers: it is most likely a
// cloudflared connector nobody listed, so every visitor it relays shares one
// per-client bucket. Public peers are skipped: a connector is on the LAN, and
// anyone on the internet can send the headers, so warning about them would
// let a caller fill the log (and the remembered set).
func warnUnlistedConnector(ip string) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return
	}
	a = a.WithZone("").Unmap()
	if !a.IsPrivate() && !a.IsLinkLocalUnicast() {
		return
	}
	// An IPv6 host is remembered by its /64, so one host rotating through
	// its addresses cannot fill the set and silence the warning about a real
	// connector.
	key := a.String()
	prefix, byPrefix := lanIPv6Prefix(key)
	if byPrefix {
		key = prefix
	}
	switch unlistedConnectors.add(key) {
	case onceSetAdded:
		clientTrustLog.Warn("%s", unlistedConnectorMessage(a, prefix))
	case onceSetFull:
		clientTrustLog.Warn("more than %d unlisted private peers sent Cloudflare headers; no longer warning about new ones. Check TUNNEL_CONNECTOR_ADDRS", unlistedConnectorWarnCap)
	}
}

// unlistedConnectorMessage is warnUnlistedConnector's warning for peer a.
// prefix is a's /64 when the warning is keyed by it (IPv6), "" otherwise:
// the message then says it covers every address in that /64, since later
// peers there are not warned about separately.
func unlistedConnectorMessage(a netip.Addr, prefix string) string {
	const tail = " sent Cloudflare headers but is not listed in TUNNEL_CONNECTOR_ADDRS; it is probably a cloudflared connector, and every visitor it relays shares one rate-limit bucket. Add its address to that setting"
	if prefix != "" {
		return fmt.Sprintf("peer %s%s (warning once for all addresses in %s)", a, tail, prefix)
	}
	return fmt.Sprintf("peer %s%s", a, tail)
}

// onceSet is a bounded set of strings: add reports whether a value is new.
// Once it holds max values it accepts no more, and reports onceSetFull exactly
// once (on the first refused value) so the caller can say it stopped.
type onceSet struct {
	mu       sync.Mutex
	max      int
	seen     map[string]struct{}
	fullSaid bool
}

type onceSetResult int

const (
	onceSetSeen  onceSetResult = iota // already present, or full and already said so
	onceSetAdded                      // new, now remembered
	onceSetFull                       // new, but the set is full; reported once
)

func newOnceSet(max int) *onceSet {
	return &onceSet{max: max, seen: make(map[string]struct{})}
}

func (o *onceSet) add(v string) onceSetResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.seen[v]; ok {
		return onceSetSeen
	}
	if len(o.seen) >= o.max {
		if o.fullSaid {
			return onceSetSeen
		}
		o.fullSaid = true
		return onceSetFull
	}
	o.seen[v] = struct{}{}
	return onceSetAdded
}
