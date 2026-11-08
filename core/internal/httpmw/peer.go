// file: core/internal/httpmw/peer.go
// version: 1.0.0
// guid: b037bc9b-668f-488a-ac4e-4f8b7f86ae48
// last-edited: 2026-11-08

package httpmw

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// zoneRetryAfter is how long settings whose zoned entry named no interface
// are reused before the zones are resolved again (client_trust.go's
// connectorRetryAfter): the interface may come up after start.
const zoneRetryAfter = 30 * time.Second

// deniedPeerLogCap bounds how many distinct refused peers stage 0 logs
// (peer_allowlist.go's deniedPeerLogCap).
const deniedPeerLogCap = 1024

// compiled is Settings in the form the stages read: immutable once stored.
type compiled struct {
	s       Settings
	at      time.Time
	pending bool    // a zoned entry named no interface yet: re-resolve later
	allow   peerSet // stage 0's allow-list; enforced only when allowOn
	allowOn bool
	trusted peerSet
	header  string // canonical client-IP header, "" = none
	origins map[string]bool
	cop     *http.CrossOriginProtection
	rate    rateSpec
	jsonMax int64
	upMax   int64
}

// peerSet is a set of prefixes (zone-free, unmapped) and zoned addresses
// (IPv6 link-local with the interface's name as zone).
type peerSet struct {
	prefixes []netip.Prefix
	zoned    []netip.Addr
}

// contains reports a, in trust form (trustAddr), in the set: a zoned entry
// matches only the same zone, since fe80::5 on en0 and on en1 are
// different hosts (client_trust.go:133-201).
func (p peerSet) contains(a netip.Addr) bool {
	for _, z := range p.zoned {
		if z == a {
			return true
		}
	}
	bare := a.WithZone("").Unmap()
	for _, pre := range p.prefixes {
		if pre.Contains(bare) {
			return true
		}
	}
	return false
}

// compile turns s into its compiled form. Unusable entries are returned as
// errors and left out.
func compile(s Settings, now time.Time, ifs func() ([]net.Interface, error)) (*compiled, []error) {
	cp := &compiled{s: s, at: now}
	var errs []error
	var pend bool
	cp.allow, pend, errs = addPeers(ifs, cp.allow, s.PeerAllowlist, "peer allow-list", errs)
	cp.pending = cp.pending || pend
	cp.allowOn = len(s.PeerAllowlist) > 0
	cp.trusted, pend, errs = addPeers(ifs, cp.trusted, s.TrustedProxies, "trusted proxies", errs)
	cp.pending = cp.pending || pend
	if s.ClientIPHeader != "" {
		cp.header = http.CanonicalHeaderKey(strings.TrimSpace(s.ClientIPHeader))
	}
	cp.origins = map[string]bool{}
	cp.cop = http.NewCrossOriginProtection()
	for _, o := range s.AllowedOrigins {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" {
			continue
		}
		if err := cp.cop.AddTrustedOrigin(o); err != nil {
			errs = append(errs, fmt.Errorf("allowed origin %q: %w", o, err))
			continue
		}
		cp.origins[o] = true
	}
	cp.rate = newRateSpec(s)
	cp.jsonMax, cp.upMax = s.JSONMaxBytes, s.UploadMaxBytes
	if cp.jsonMax <= 0 {
		cp.jsonMax = DefaultJSONMax
	}
	if cp.upMax <= 0 {
		cp.upMax = DefaultUploadMax
	}
	return cp, errs
}

// addPeers adds entries to set. A link-local IPv6 address needs a zone
// naming an interface of this host, resolved to the interface's name
// (resolveZone); one whose zone names no interface yet is left out and
// reported as pending, so current re-resolves it later.
func addPeers(ifs func() ([]net.Interface, error), set peerSet, entries []Peer, what string, errs []error) (peerSet, bool, []error) {
	pending := false
	for _, e := range entries {
		if e.Addr.IsValid() {
			a := trustAddr(e.Addr)
			if needsZone(a) {
				if a.Zone() == "" {
					errs = append(errs, fmt.Errorf("%s entry %s: an IPv6 link-local address needs a zone (fe80::5%%eth0)", what, a))
					continue
				}
				name, m := resolveZone(ifs, a.Zone())
				switch m {
				case zoneUnknown:
					pending = true
					errs = append(errs, fmt.Errorf("%s entry %s: zone names no interface of this host (checked again every %s)", what, a, zoneRetryAfter))
					continue
				case zoneAmbiguous:
					errs = append(errs, fmt.Errorf("%s entry %s: zone matches more than one interface ignoring case and none exactly", what, a))
					continue
				}
				set.zoned = append(set.zoned, a.WithZone(name))
				continue
			}
			set.prefixes = append(set.prefixes, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		if !e.Prefix.IsValid() {
			errs = append(errs, fmt.Errorf("%s entry: neither a prefix nor an address", what))
			continue
		}
		a, bits := e.Prefix.Addr().WithZone(""), e.Prefix.Bits()
		if a.Is4In6() {
			if bits < 96 {
				errs = append(errs, fmt.Errorf("%s entry %s: an IPv4-mapped prefix shorter than /96", what, e.Prefix))
				continue
			}
			a, bits = a.Unmap(), bits-96
		}
		set.prefixes = append(set.prefixes, netip.PrefixFrom(a, bits).Masked())
	}
	return set, pending, errs
}

// needsZone reports an IPv6 link-local address, unique only with its zone.
func needsZone(a netip.Addr) bool { return a.Is6() && a.IsLinkLocalUnicast() }

// trustAddr is a in the form trust decisions compare: unmapped, the zone
// kept only on an IPv6 link-local address.
func trustAddr(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if !needsZone(a) {
		a = a.WithZone("")
	}
	return a
}

// parseTrustAddr parses "host:port", "[v6%zone]:port" or a bare address
// into trust form.
func parseTrustAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return trustAddr(ap.Addr()), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return trustAddr(a), true
	}
	return netip.Addr{}, false
}

type zoneMatch int

const (
	zoneFound zoneMatch = iota
	zoneUnknown
	zoneAmbiguous
)

// resolveZone resolves a configured zone to the interface's name: the exact
// name, then a numeric zone as an index, then a unique case-insensitive
// match (client_trust.go matchZone with foldCase). A peer's zone is always
// the kernel's exact name, so only configuration goes through here.
func resolveZone(interfaces func() ([]net.Interface, error), zone string) (string, zoneMatch) {
	ifs, err := interfaces()
	if err != nil {
		return "", zoneUnknown
	}
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

// peer is stage 0 (05-http-api §4.2): the peer allow-list (loopback and the
// trusted proxies always admitted) and the client address, resolved once
// and published in the State (ClientAddr). The client-IP header is believed
// only from a trusted proxy.
func (c *Chain) peer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && !r.TLS.HandshakeComplete {
			// A request inside TLS early data could be replayed (HEAD's
			// newPeerAllowListHandler).
			http.Error(w, "too early", http.StatusTooEarly)
			return
		}
		cp := c.current()
		a, ok := parseTrustAddr(r.RemoteAddr)
		trusted := ok && cp.trusted.contains(a)
		if cp.allowOn && !(ok && (a.IsLoopback() || trusted || cp.allow.contains(a))) {
			c.refusePeer(w, r, a, ok)
			return
		}
		st := From(r.Context())
		if st != nil {
			if ok {
				st.client = a.WithZone("")
			}
			if trusted && cp.header != "" {
				if h, hok := headerAddr(r.Header.Get(cp.header)); hok {
					st.client, st.attributed = h, true
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// headerAddr parses a client-IP header value: one address, or for a list
// (X-Forwarded-For) the last entry, the one the trusted proxy appended. A
// zoned value is refused: a zone means nothing beyond the proxy's host.
func headerAddr(v string) (netip.Addr, bool) {
	if i := strings.LastIndexByte(v, ','); i >= 0 {
		v = v[i+1:]
	}
	a, err := netip.ParseAddr(strings.TrimSpace(v))
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// refusePeer answers a peer outside the allow-list: 403 problem
// peer_refused, logged once per peer (an IPv6 peer once per /64).
func (c *Chain) refusePeer(w http.ResponseWriter, r *http.Request, a netip.Addr, ok bool) {
	key := r.RemoteAddr
	if ok {
		bare := a.WithZone("")
		key = bare.String()
		if bare.Is6() {
			key = netip.PrefixFrom(bare, 64).Masked().String()
		}
	}
	switch c.refused.add(key) {
	case onceAdded:
		c.deps.Log.Warn("httpapi: refused a connection from a peer outside core.http.peer_allowlist (logged once per peer)", "peer", key)
	case onceFull:
		c.deps.Log.Warn("httpapi: more than the logged maximum of distinct peers refused; no longer logging new ones", "max", deniedPeerLogCap)
	}
	c.inst.peerRefused(r.Context())
	c.deps.Problem(w, r, http.StatusForbidden, "peer_refused", nil)
}

// onceSet remembers up to max keys, for log-once decisions.
type onceSet struct {
	mu   sync.Mutex
	max  int
	seen map[string]bool
	full bool
}

type onceResult int

const (
	onceSeen onceResult = iota
	onceAdded
	onceFull
)

func newOnceSet(max int) *onceSet { return &onceSet{max: max, seen: map[string]bool{}} }

func (o *onceSet) add(k string) onceResult {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen[k] {
		return onceSeen
	}
	if len(o.seen) >= o.max {
		if o.full {
			return onceSeen
		}
		o.full = true
		return onceFull
	}
	o.seen[k] = true
	return onceAdded
}
