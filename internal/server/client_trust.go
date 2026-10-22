// file: internal/server/client_trust.go
// version: 1.19.1
// guid: aaef6f46-d2c0-4e29-aaf4-2b93e15c6c37
// last-edited: 2026-10-22

package server

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

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

// connectorReject is a TUNNEL_CONNECTOR_ADDRS entry parseConnectorPeers
// refused, and why. A refused entry is never trusted.
type connectorReject struct {
	entry  string
	reason connectorRejectReason
}

type connectorRejectReason int

const (
	// rejectNotIP: the entry is not an IP address.
	rejectNotIP connectorRejectReason = iota
	// rejectNoZone: an IPv6 link-local entry without a zone.
	rejectNoZone
	// rejectUnknownZone: an IPv6 link-local entry whose zone names no
	// interface of this host (yet: tunnelConnectorPeers retries it).
	rejectUnknownZone
	// rejectAmbiguousZone: an IPv6 link-local entry whose zone matches no
	// interface exactly and more than one ignoring case.
	rejectAmbiguousZone
)

// parseConnectorPeers parses a comma-separated address list. A link-local
// address is only unique per interface, so an IPv6 link-local entry must be
// written with its zone (fe80::5%eth0): a zoneless one would trust that
// address arriving on every interface. The zone is normalised to the
// interface's NAME (resolveZone), because the peer address the server sees
// always carries the name: a numeric zone (fe80::5%2) or one in the wrong
// case (fe80::5%ETH0) would otherwise be accepted and never match. An entry
// that is not an address, a link-local one without a zone, or one whose zone
// names no interface or, ignoring case, more than one, is refused
// (rejected) and NOT trusted. Any other entry
// is kept without a zone, which it cannot meaningfully carry.
func parseConnectorPeers(csv string) (peers connectorPeers, rejected []connectorReject) {
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			rejected = append(rejected, connectorReject{part, rejectNotIP})
			continue
		}
		a = trustAddr(a)
		if needsZone(a) {
			if a.Zone() == "" {
				rejected = append(rejected, connectorReject{part, rejectNoZone})
				continue
			}
			name, m := resolveZone(a.Zone())
			switch m {
			case zoneUnknown:
				rejected = append(rejected, connectorReject{part, rejectUnknownZone})
				continue
			case zoneAmbiguous:
				rejected = append(rejected, connectorReject{part, rejectAmbiguousZone})
				continue
			}
			a = a.WithZone(name)
		}
		peers = append(peers, a)
	}
	return peers, rejected
}

// zoneMatch is how a zone matched this host's interfaces (matchZone).
type zoneMatch int

const (
	zoneFound zoneMatch = iota
	zoneUnknown
	zoneAmbiguous
)

// resolveZone resolves a zone written in TUNNEL_CONNECTOR_ADDRS to the
// interface's name (matchZone with the case-insensitive fallback). Config
// parsing only: a peer's zone is always the exact kernel name, so the trust
// path uses lookupZoneInterface, which never folds case. A variable so tests
// can describe a host.
var resolveZone = func(zone string) (string, zoneMatch) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", zoneUnknown
	}
	return matchZone(zone, ifs, true)
}

// matchZone finds the interface zone names among ifs: the exact name first,
// then a numeric zone as an index, then, only when foldCase is set (config
// parsing), a case-insensitive name match, which must be unique: two names
// that fold to the zone with neither exact (eth0 and Eth0 for ETH0) is
// ambiguous rather than a guess.
func matchZone(zone string, ifs []net.Interface, foldCase bool) (string, zoneMatch) {
	for _, ifi := range ifs {
		if ifi.Name == zone {
			return ifi.Name, zoneFound
		}
	}
	if n, err := strconv.Atoi(zone); err == nil {
		for _, ifi := range ifs {
			if ifi.Index == n {
				return ifi.Name, zoneFound
			}
		}
		return "", zoneUnknown
	}
	if !foldCase {
		return "", zoneUnknown
	}
	name, found := "", 0
	for _, ifi := range ifs {
		if strings.EqualFold(ifi.Name, zone) {
			name, found = ifi.Name, found+1
		}
	}
	switch found {
	case 0:
		return "", zoneUnknown
	case 1:
		return name, zoneFound
	default:
		return "", zoneAmbiguous
	}
}

// lookupZoneInterface finds the interface a PEER's zone names, for the trust
// path: the exact name first (net.InterfaceByName), then a numeric zone as an
// index. No case folding: Go fills a peer's zone from the kernel, exactly.
func lookupZoneInterface(zone string) (*net.Interface, error) {
	ifi, err := net.InterfaceByName(zone)
	if err == nil {
		return ifi, nil
	}
	if n, aerr := strconv.Atoi(zone); aerr == nil {
		return net.InterfaceByIndex(n)
	}
	return nil, err
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

// connectorRetryAfter is how long a parse of TUNNEL_CONNECTOR_ADDRS that
// refused an entry for an unknown zone is reused before it is parsed again:
// the interface may come up after the server starts (a VLAN or bridge
// created later), and the entry should then be trusted without a restart.
const connectorRetryAfter = 30 * time.Second

// connectorCache is the parsed TUNNEL_CONNECTOR_ADDRS: parsed once per
// distinct value, and again every connectorRetryAfter while an entry is
// refused for an unknown zone. Refusals are logged once per distinct set,
// not at every retry.
type connectorCache struct {
	mu       sync.Mutex
	set      bool
	raw      string
	at       time.Time
	peers    connectorPeers
	retry    bool   // the last parse refused an entry for an unknown zone
	reported string // the refusals last logged
}

var tunnelConnectors connectorCache

// tunnelConnectorPeers returns the configured connector addresses
// (connectorCache); an invalid entry is logged and ignored.
func tunnelConnectorPeers() connectorPeers {
	return tunnelConnectors.get(config.TunnelConnectorAddrs(), time.Now())
}

func (cc *connectorCache) get(raw string, now time.Time) connectorPeers {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.set && raw == cc.raw && !(cc.retry && now.Sub(cc.at) >= connectorRetryAfter) {
		return cc.peers
	}
	peers, rejected := parseConnectorPeers(raw)
	cc.set, cc.raw, cc.at, cc.peers, cc.retry = true, raw, now, peers, false
	for _, r := range rejected {
		if r.reason == rejectUnknownZone {
			cc.retry = true
		}
	}
	if key := fmt.Sprint(rejected); key != cc.reported {
		cc.reported = key
		reportConnectorRejects(rejected)
	}
	return peers
}

// reportConnectorRejects logs why each TUNNEL_CONNECTOR_ADDRS entry was
// refused.
func reportConnectorRejects(rejected []connectorReject) {
	notIP := 0
	for _, r := range rejected {
		switch r.reason {
		case rejectNotIP:
			notIP++
		case rejectNoZone:
			clientTrustLog.Warn("TUNNEL_CONNECTOR_ADDRS entry %q is an IPv6 link-local address without a zone; ignored, because a link-local address is only unique per interface. Write it with the interface it arrives on, e.g. %s%%eth0", r.entry, r.entry)
		case rejectUnknownZone:
			clientTrustLog.Warn("TUNNEL_CONNECTOR_ADDRS entry %q names an interface this host does not have; ignored until it appears (checked again every %s). Write the zone as the name of the interface the connector arrives on, e.g. fe80::5%%eth0", r.entry, connectorRetryAfter)
		case rejectAmbiguousZone:
			clientTrustLog.Warn("TUNNEL_CONNECTOR_ADDRS entry %q matches more than one interface name ignoring case and none exactly; ignored. Write the interface name exactly", r.entry)
		}
	}
	if notIP > 0 {
		clientTrustLog.Warn("TUNNEL_CONNECTOR_ADDRS has %d entries that are not IP addresses; ignored", notIP)
	}
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
// a caller who owns a /64 cannot mint 2^64 buckets. A LAN IPv6 address
// (isLANAddr: ULA fc00::/7, link-local, loopback, or a global address inside
// an on-link prefix of this host) is keyed on its FULL address instead: every
// machine on an IPv6 LAN shares one /64, so a /64 key would let one LAN device
// spend every other device's budget (the bootstrap limiter keys direct LAN
// peers the same way, bootstrapDirectKey). For a direct LAN peer callers add
// an aggregate bucket per /64 (lanIPv6Prefix) so rotating addresses cannot
// mint unlimited buckets. A ULA, link-local or loopback address can only be a
// direct peer (it is not routable from the internet, and Cloudflare's edge
// never sets CF-Connecting-IP to one); an on-link global address can also be
// a tunnel visitor's, but only a device on this network has one, so keying
// it on its full address gives nobody else anything. ok is false when ip does not parse;
// callers then treat the client as unattributed and never fall back to text
// taken from the request.
func clientPrefixKey(ip string) (key string, ok bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", false
	}
	a = a.WithZone("").Unmap()
	if a.Is6() && !isLANAddr(a) {
		return netip.PrefixFrom(a, 64).Masked().String(), true
	}
	return a.String(), true
}

// clientSaturatedKey is clientPrefixKey with a coarser IPv6 grain: a public
// IPv6 address is keyed on its /48, the allocation an ISP hands one customer
// site, instead of its /64. It is the key of the login ceilings' SATURATED
// admission (what a visitor may do once the aggregate ceilings are spent),
// where the per-visitor refusal is the only thing that keeps one caller from
// holding a seat per address: a caller who owns a /48 can mint 65,536 distinct
// /64 keys, so a /64 key would let it start 65,536 "first" attempts. IPv4 and
// LAN addresses are keyed exactly as clientPrefixKey keys them (a LAN device
// must not share a key with the rest of its network). ok is false when ip
// does not parse.
func clientSaturatedKey(ip string) (key string, ok bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", false
	}
	a = a.WithZone("").Unmap()
	if a.Is6() && !isLANAddr(a) {
		return netip.PrefixFrom(a, 48).Masked().String(), true
	}
	return a.String(), true
}

// lanIPv6Prefix returns the /64 of a LAN IPv6 address (isLANAddr: ULA
// fc00::/7, link-local, or a global address in an on-link prefix of this
// host), and false for anything else (IPv4, other public IPv6, loopback,
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
	if !a.Is6() || a.IsLoopback() || !isLANAddr(a) {
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
// interfaces. It runs for every unauthenticated request that is not from a
// configured connector, so it asks the kernel once, not once per interface:
//   - an IPv6 link-local peer counts only when the interface its zone names
//     owns the address (zoneInterfaceAddrs: that one interface's addresses),
//     since the same link-local address on another interface is another
//     host; a zoneless one never counts;
//   - any other address is looked up in one net.InterfaceAddrs() call
//     (hostInterfaceAddrs).
func remoteAddrIsLocal(addr string) bool {
	a, ok := parseTrustAddr(addr)
	if !ok {
		return false
	}
	if a.IsLoopback() {
		return true
	}
	var owned []netip.Addr
	var err error
	if needsZone(a) {
		if a.Zone() == "" {
			return false
		}
		owned, err = zoneInterfaceAddrs(a.Zone())
	} else {
		owned, err = hostInterfaceAddrs()
	}
	if err != nil {
		return false
	}
	bare := a.WithZone("")
	for _, own := range owned {
		if own == bare {
			return true
		}
	}
	return false
}

// zoneInterfaceAddrs returns the addresses (zone dropped, unmapped) of the
// interface zone names (lookupZoneInterface). A variable so tests can
// describe a host and count lookups.
var zoneInterfaceAddrs = func(zone string) ([]netip.Addr, error) {
	ifi, err := lookupZoneInterface(zone)
	if err != nil {
		return nil, err
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, err
	}
	return netAddrsToAddrs(addrs), nil
}

// hostInterfaceAddrs returns every address (zone dropped, unmapped) assigned
// to this host's interfaces, in one net.InterfaceAddrs() call. A variable so
// tests can describe a host and count lookups.
var hostInterfaceAddrs = func() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	return netAddrsToAddrs(addrs), nil
}

// netAddrsToAddrs converts interface addresses to netip form, zone dropped
// and unmapped, skipping anything that is not an *net.IPNet.
func netAddrsToAddrs(addrs []net.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(addrs))
	for _, ad := range addrs {
		if ipn, ok := ad.(*net.IPNet); ok {
			if a, ok := netip.AddrFromSlice(ipn.IP); ok {
				out = append(out, a.WithZone("").Unmap())
			}
		}
	}
	return out
}

// onLinkPrefixTTL is how long the on-link prefix list (onLinkIPv6Prefixes)
// is reused before it is read again: addresses change rarely (a new
// SLAAC prefix), and isLANAddr runs on every request.
const onLinkPrefixTTL = 30 * time.Second

// onLinkCache holds the last on-link prefix list read.
var onLinkCache struct {
	mu       sync.Mutex
	at       time.Time
	prefixes []netip.Prefix
}

// onLinkIface is one interface as onLinkPrefixesOf sees it.
type onLinkIface struct {
	flags net.Flags
	addrs []net.Addr
}

// hostOnLinkIfaces lists this host's interfaces with their flags and
// addresses. A variable so tests can describe a host.
var hostOnLinkIfaces = func() ([]onLinkIface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]onLinkIface, 0, len(ifs))
	for _, ifi := range ifs {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		out = append(out, onLinkIface{flags: ifi.Flags, addrs: addrs})
	}
	return out, nil
}

// onLinkMinPrefixBits is the widest on-link prefix counted as LAN: a /64,
// the size of one IPv6 link. A wider one (a /48 assigned to an interface)
// spans more than the segment the host is on, so being in it proves little.
const onLinkMinPrefixBits = 64

// onLinkPrefixesOf returns the on-link prefixes isLANAddr counts: for each
// interface that is up, broadcast-capable (a shared segment such as Ethernet
// or Wi-Fi) and not point-to-point (a tunnel, VPN or PPP link, whose prefix
// covers the far side), the prefix of each global-unicast, non-ULA IPv6
// address on it whose length is /64 or narrower and not /128.
func onLinkPrefixesOf(ifaces []onLinkIface) []netip.Prefix {
	var out []netip.Prefix
	for _, ifi := range ifaces {
		if ifi.flags&net.FlagUp == 0 || ifi.flags&net.FlagBroadcast == 0 || ifi.flags&net.FlagPointToPoint != 0 {
			continue
		}
		for _, ad := range ifi.addrs {
			ipn, ok := ad.(*net.IPNet)
			if !ok {
				continue
			}
			a, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			a = a.WithZone("").Unmap()
			ones, bits := ipn.Mask.Size()
			if !isOnLinkGlobalIPv6(a) || bits != 128 || ones < onLinkMinPrefixBits || ones == 128 {
				continue
			}
			out = append(out, netip.PrefixFrom(a, ones).Masked())
		}
	}
	return out
}

// onLinkIPv6Prefixes returns onLinkPrefixesOf(this host's interfaces), read
// at most every onLinkPrefixTTL; on a read error the previous list is kept.
func onLinkIPv6Prefixes() []netip.Prefix {
	onLinkCache.mu.Lock()
	defer onLinkCache.mu.Unlock()
	now := time.Now()
	if !onLinkCache.at.IsZero() && now.Sub(onLinkCache.at) < onLinkPrefixTTL {
		return onLinkCache.prefixes
	}
	onLinkCache.at = now
	ifaces, err := hostOnLinkIfaces()
	if err != nil {
		return onLinkCache.prefixes
	}
	onLinkCache.prefixes = onLinkPrefixesOf(ifaces)
	return onLinkCache.prefixes
}

// isOnLinkGlobalIPv6 reports whether a is the kind of address an on-link
// global prefix can contain: global-unicast IPv6 that is not ULA (the
// other LAN kinds isLANAddr covers on their own).
func isOnLinkGlobalIPv6(a netip.Addr) bool {
	return a.Is6() && a.IsGlobalUnicast() && !a.IsPrivate()
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

// loginThrottleKey is the bucket the password-login failure throttles (the web
// POST /api/v1/auth/login and the ABS POST /login and /auth/refresh) charge a
// request to. Behind the tunnel c.ClientIP() is the connector's address, so a
// throttle keyed on it is one bucket per connector shared by every visitor: a
// single caller failing 15 times locks everyone out through that connector.
// This keys on trustedClientIP instead, the visitor's CF-Connecting-IP
// when the peer is a configured tunnel connector (or this host behind a
// verified Access assertion), and otherwise the peer's own address, whatever
// headers the caller sends, so a header a caller made up never picks its
// bucket. Where the visitor cannot be established (a connector request with
// no usable CF-Connecting-IP) that is the connector's address, the old shared
// bucket. The address goes through clientPrefixKey, so a public IPv6 visitor
// is one bucket per /64, as the bootstrap and invite limiters key it: now that
// the visitor's own address is believed, an unkeyed one would let a caller who
// owns a /64 rotate for a fresh failure budget per attempt. "" only when no
// address parses, and the callers then fall back to c.ClientIP().
//
// The answer is kept on the request: one login asks several times, and the
// answer involves a kernel lookup.
func loginThrottleKey(c *gin.Context) string {
	if v, ok := c.Get(loginThrottleKeyCtx); ok {
		if k, ok := v.(string); ok {
			return k
		}
	}
	key := ""
	if ip := requestClientIP(c); ip != "" {
		key, _ = clientPrefixKey(ip)
	}
	c.Set(loginThrottleKeyCtx, key)
	return key
}

// loginThrottleKeyCtx is the gin context key loginThrottleKey keeps its answer under.
const loginThrottleKeyCtx = "login_throttle_key"

// loginSaturatedKey is the visitor key the login ceilings use for SATURATED
// admission, and the CSP report limiter for its per-visitor budget: the same address loginThrottleKey establishes, grouped by
// clientSaturatedKey (a public IPv6 visitor per /48, not /64). "" when no
// address parses, as loginThrottleKey.
func loginSaturatedKey(c *gin.Context) string {
	if v, ok := c.Get(loginSaturatedKeyCtx); ok {
		if k, ok := v.(string); ok {
			return k
		}
	}
	key := ""
	if ip := requestClientIP(c); ip != "" {
		key, _ = clientSaturatedKey(ip)
	}
	c.Set(loginSaturatedKeyCtx, key)
	return key
}

// loginSaturatedKeyCtx is the gin context key loginSaturatedKey keeps its answer under.
const loginSaturatedKeyCtx = "login_saturated_key"

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
// addresses. Both limiters give a direct caller LAN aggregates no other
// traffic can spend (classifyLANPeer); neither exempts it from the global
// ceiling (it was exempt until 2026-10-21; anything that can reach the port
// off-tunnel is "direct", so the exemption was budget for any such caller).
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

// isLANAddr reports whether a is a LAN address: private (RFC 1918 / ULA),
// link-local or loopback, or a global-unicast IPv6 address inside one of
// onLinkIPv6Prefixes, i.e. the /64 (or narrower) prefix of a global address
// assigned to an up, broadcast-capable, non-point-to-point interface of this
// host (home IPv6 LANs mostly use the ISP's global /64, not ULA). A peer
// cannot complete a TCP handshake from such an address without being on that
// segment, since the replies are delivered on-link. Global IPv4 prefixes are
// never counted: a public IPv4 /24 on a hosting provider's segment is shared
// with other customers.
func isLANAddr(a netip.Addr) bool {
	if a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLoopback() {
		return true
	}
	if !isOnLinkGlobalIPv6(a) {
		return false
	}
	bare := a.WithZone("")
	for _, p := range onLinkIPv6Prefixes() {
		if p.Contains(bare) {
			return true
		}
	}
	return false
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
	// isLocalPeer asks the kernel; a connector's answer would change nothing
	// (both branches below treat a connector the same whether or not it is
	// local), so it is asked only for other peers.
	local := !isConnector && t.isLocalPeer(c.Request.RemoteAddr)
	if isConnector || (servermiddleware.CFAccessVerified(c) && local) {
		if parsed := net.ParseIP(strings.TrimSpace(c.GetHeader("CF-Connecting-IP"))); parsed != nil {
			return parsed.String(), true
		}
	}
	ip = peer
	if !isConnector && !local && hasCloudflareMarkers(c) {
		warnUnlistedConnector(c.Request.RemoteAddr)
	}
	return ip, !isConnector && !local
}

// unlistedConnectorWarnCap bounds how many distinct addresses (IPv6: /64s)
// warnUnlistedConnector remembers (and so warns about). Real deployments have
// a handful of connectors; past the cap it stops warning rather than grow.
const unlistedConnectorWarnCap = 64

// unlistedConnectors remembers the private peers already warned about.
var unlistedConnectors = newOnceSet(unlistedConnectorWarnCap)

// warnUnlistedConnector logs, once per address (once per /64 for IPv6, and
// for link-local once per /64 per interface), that a LAN peer not in
// TUNNEL_CONNECTOR_ADDRS sent Cloudflare markers: it is most likely a
// cloudflared connector nobody listed, so every visitor it relays shares one
// per-client bucket. remoteAddr is the raw peer address, so a link-local
// peer keeps its zone: fe80::/64 on en0 and on en1 are different links, and
// each gets its own warning. Public peers are skipped: a connector is on the
// LAN, and anyone on the internet can send the headers, so warning about
// them would let a caller fill the log (and the remembered set).
func warnUnlistedConnector(remoteAddr string) {
	a, ok := parseTrustAddr(remoteAddr)
	if !ok || a.IsLoopback() || !isLANAddr(a) {
		return
	}
	// An IPv6 host is remembered by its /64, so one host rotating through
	// its addresses cannot fill the set and silence the warning about a real
	// connector.
	key := a.WithZone("").String()
	scope, byPrefix := lanIPv6Prefix(key)
	if byPrefix {
		key = scope
		if z := a.Zone(); z != "" {
			key += "%" + z
			scope += " on interface " + z
		}
	}
	switch unlistedConnectors.add(key) {
	case onceSetAdded:
		clientTrustLog.Warn("%s", unlistedConnectorMessage(a, scope))
	case onceSetFull:
		clientTrustLog.Warn("more than %d unlisted private peers sent Cloudflare headers; no longer warning about new ones. Check TUNNEL_CONNECTOR_ADDRS", unlistedConnectorWarnCap)
	}
}

// unlistedConnectorMessage is warnUnlistedConnector's warning for peer a.
// scope is what the warning covers when it is keyed by a /64 (IPv6), e.g.
// "fd12::/64" or "fe80::/64 on interface en0", and "" otherwise: the message
// then says it covers every address there, since later peers in it are not
// warned about separately.
func unlistedConnectorMessage(a netip.Addr, scope string) string {
	const tail = " sent Cloudflare headers but is not listed in TUNNEL_CONNECTOR_ADDRS; it is probably a cloudflared connector, and every visitor it relays shares one rate-limit bucket. Add its address to that setting"
	if scope != "" {
		return fmt.Sprintf("peer %s%s (warning once for all addresses in %s)", a, tail, scope)
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
