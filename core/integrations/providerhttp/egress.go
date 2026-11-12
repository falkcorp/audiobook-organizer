// file: core/integrations/providerhttp/egress.go
// version: 1.2.0
// guid: 13d22c91-8f27-4c5b-a373-a9c87d5a44f7
// last-edited: 2026-11-11

// The egress dial guard (05-metadata M2.4), moved from internal/security/safehttp
// (04 §3.4) with its behaviour unchanged. It builds HTTP clients that are safe
// to point at a URL supplied by a third party (a metadata provider's cover URL,
// a proxied cover request).
//
// It is the single implementation of the server-side request forgery (SSRF)
// guard for this repository. There were two cover-fetch paths before
// 2026-09-10: internal/metadata/cover.go had a scheme allowlist plus an
// IP-blocking DialContext, and internal/covers/covers.go had a hostname-prefix
// allowlist feeding a bare http.Get. The second one followed a redirect from an
// allowlisted host to any address at all, including loopback and 169.254.169.254.
// Both now go through this package, and it lives under internal/security/
// alongside pathvalidation and safepath — which both packages already import —
// rather than in internal/httputil, which is coupled to gin.
//
// Three controls, all of which have to be present for the guard to hold:
//
//  1. A POSITIVE scheme allowlist (http/https). A blocklist would miss file://,
//     gopher:// and dict://.
//  2. An address check inside DialContext, on the RESOLVED IP. A check on the
//     URL string cannot see what the hostname resolves to, and a check that
//     resolves and then dials the hostname again leaves a DNS-rebinding window
//     between the two lookups. Dialer.DialContext dials the literal address it
//     just checked, so there is no second resolution.
//  3. A CheckRedirect that re-validates every hop and caps the chain. The
//     address check re-applies on redirects because the same Transport (and so
//     the same DialContext) handles every hop; CheckRedirect adds the scheme
//     check, which otherwise only ran on the first URL.

package providerhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

var (
	// ErrBlockedAddress reports that a host resolved to an address the guard
	// refuses to connect to (private, loopback, link-local, reserved).
	ErrBlockedAddress = errors.New("address is blocked (private/reserved range)")

	// ErrBlockedScheme reports a URL scheme outside the http/https allowlist.
	ErrBlockedScheme = errors.New("URL scheme not allowed (only http and https)")

	// ErrInvalidURL reports a URL that cannot be used for a fetch at all.
	ErrInvalidURL = errors.New("invalid URL")

	// ErrInsecureRedirect reports a redirect from https to http that the
	// instance's trust does not cover: the credentials of a request that left
	// over TLS must not follow it onto plain text.
	ErrInsecureRedirect = errors.New("redirect from https to http is not allowed")

	// ErrTooManyRedirects reports a redirect chain longer than MaxRedirects.
	ErrTooManyRedirects = errors.New("too many redirects")
)

// MaxRedirects caps a redirect chain. net/http's default is 10; a cover fetch
// has no legitimate reason to need more than a couple of hops, and every extra
// hop is another chance for an attacker-controlled Location header.
const MaxRedirects = 5

// DefaultDialTimeout bounds a single TCP connect attempt.
const DefaultDialTimeout = 10 * time.Second

// blockedPrefixes lists ranges that the net.IP predicates below do not
// already cover. A function, not a package variable: core holds no
// package-level state (R5). The Registry parses it ONCE per guard (each
// Dialer keeps its copy); IsBlockedIP, the exported check for callers with no
// Dialer, parses per call.
//
// Deliberately ABSENT: the documentation ranges 192.0.2.0/24 (TEST-NET-1),
// 198.51.100.0/24 (TEST-NET-2) and 203.0.113.0/24 (TEST-NET-3). They are
// unroutable, so blocking them buys no security — and this repository's tests
// and docs are required to use those ranges as stand-ins for public addresses,
// so blocking them would make it impossible to write a test that proves a
// public address is still allowed.
func blockedPrefixes() []netip.Prefix {
	blocks := []string{
		"0.0.0.0/8",        // "this network"; 0.0.0.0 reaches localhost on Linux
		"100.64.0.0/10",    // CGNAT / carrier internal (also Alibaba's 100.100.100.200 metadata)
		"168.63.129.16/32", // Azure's wire server: a public address that is a metadata endpoint
		"192.0.0.0/24",     // IETF protocol assignments (incl. 192.0.0.170 NAT64)
		"198.18.0.0/15",    // benchmarking
		"240.0.0.0/4",      // reserved, incl. 255.255.255.255 broadcast
		"::/96",            // IPv6 unspecified and the deprecated IPv4-compatible form (::127.0.0.1)
		"::ffff:0:0:0/96",  // SIIT (IPv4-translated): not the IPv4-mapped ::ffff:0:0/96, which is unmapped first
		"64:ff9b::/96",     // NAT64 — embeds an arbitrary IPv4 address
		"64:ff9b:1::/48",   // local-use NAT64 (RFC 8215)
		"100::/64",         // discard-only
		"2001::/32",        // Teredo — embeds an arbitrary IPv4 address
		"2001:db8::/32",    // IPv6 documentation
		"2002::/16",        // 6to4 — embeds an arbitrary IPv4 address
		"fc00::/7",         // IPv6 unique-local (belt and braces with IsPrivate)
		"fe80::/10",        // IPv6 link-local (belt and braces with IsLinkLocal*)
		"fec0::/10",        // deprecated site-local
	}
	out := make([]netip.Prefix, 0, len(blocks))
	for _, b := range blocks {
		if p, err := netip.ParsePrefix(b); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// IsBlockedIP reports whether ip must not be connected to.
//
// It fails closed: a nil or unparseable address is blocked, because "we could
// not tell what this is" is not a reason to dial it.
//
// IPv4-mapped IPv6 (::ffff:127.0.0.1) is unmapped first, so the v4 ranges
// catch the mapped forms.
func IsBlockedIP(ip net.IP) bool { return isBlocked(ip, blockedPrefixes()) }

func isBlocked(ip net.IP, nets []netip.Prefix) bool {
	if ip == nil {
		return true
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	for _, n := range nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// ValidateURL applies the scheme allowlist to a raw URL string.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	return ValidateParsedURL(u)
}

// ValidateParsedURL is ValidateURL for an already-parsed URL, so the redirect
// hook does not have to re-parse what net/http already parsed.
func ValidateParsedURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: no URL", ErrInvalidURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: got %q", ErrBlockedScheme, u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w: URL has no host", ErrInvalidURL)
	}
	return nil
}

// Dialer resolves a hostname, refuses the connection if any resolved address is
// blocked, and then dials one of the addresses it just checked.
//
// The zero value is usable and uses the process resolver and a net.Dialer.
type Dialer struct {
	// Timeout bounds one connect attempt. Zero means DefaultDialTimeout.
	Timeout time.Duration

	// AllowPrivate admits loopback, RFC 1918, RFC 4193 (ULA), CGNAT and the
	// exact unspecified addresses, the ranges an instance trusted for private
	// networks may reach (05-metadata M2.4). It never admits link-local
	// (169.254.0.0/16, which holds the cloud metadata endpoint, and fe80::/10),
	// multicast, 0.0.0.0/8 beyond 0.0.0.0, the reserved, benchmarking, NAT64,
	// 6to4 and documentation ranges: those are refused whatever the trust.
	// The zero value refuses every private address, as the covers guard does.
	AllowPrivate bool

	// LookupIP resolves a host. Tests override it to avoid real DNS; nil means
	// net.DefaultResolver.
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)

	// DialAddr dials a literal "ip:port". Tests override it; nil means a
	// net.Dialer. Implementations may assume the address has already been
	// checked by IsBlockedIP.
	DialAddr func(ctx context.Context, network, addr string) (net.Conn, error)

	// Log receives one Debug line per refused dial naming the host and the
	// address it resolved to: the detail an error must not carry (the refusal
	// text names neither, so a response to a caller never maps the private
	// network). nil discards.
	Log *slog.Logger

	nets []netip.Prefix // blockedPrefixes, parsed once by newGuard
}

// newGuard is the Dialer of a Registry's transports: the blocked ranges are
// parsed once, here.
func newGuard(allowPrivate bool, lookup func(context.Context, string) ([]net.IP, error),
	dial func(context.Context, string, string) (net.Conn, error), log *slog.Logger) *Dialer {
	return &Dialer{AllowPrivate: allowPrivate, LookupIP: lookup, DialAddr: dial, Log: log, nets: blockedPrefixes()}
}

// blocked reports whether the dial must refuse ip under d's trust.
func (d *Dialer) blocked(ip net.IP) bool {
	nets := d.nets
	if nets == nil {
		nets = blockedPrefixes()
	}
	if !isBlocked(ip, nets) {
		return false
	}
	return !(d.AllowPrivate && privateBypassable(ip))
}

// privateBypassable reports whether ip is in a range a private-trusted dial
// may reach: loopback, RFC 1918 and RFC 4193 private space, CGNAT, and the
// exact unspecified addresses (the spec's EgressPolicy text lists unspecified
// among them). IPv4-mapped IPv6 is normalised by To4 first, so
// ::ffff:127.0.0.1 is loopback and ::ffff:169.254.169.254 is link-local.
// Link-local, multicast and everything else stay refused.
func privateBypassable(ip net.IP) bool {
	if isMetadataAddr(ip) {
		return false
	}
	if ip == nil || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return false
	}
	if ip.IsUnspecified() {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 100 && v4[1]&0xC0 == 64 // 100.64.0.0/10
	}
	return false
}

// isMetadataAddr reports the cloud metadata endpoints that sit inside a range a
// private-trusted dial may otherwise reach: AWS's IPv6 endpoint fd00:ec2::254
// (unique-local) and Alibaba's 100.100.100.200 (CGNAT). A LAN service is never
// at either, and a credential store is at both.
func isMetadataAddr(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	switch a.Unmap() {
	case netip.AddrFrom4([4]byte{100, 100, 100, 200}),
		netip.AddrFrom16([16]byte{0xfd, 0x00, 0x0e, 0xc2, 15: 0x54, 14: 0x02}):
		return true
	}
	return false
}

func (d *Dialer) lookup(ctx context.Context, host string) ([]net.IP, error) {
	if d.LookupIP != nil {
		return d.LookupIP(ctx, host)
	}
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func (d *Dialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.DialAddr != nil {
		return d.DialAddr(ctx, network, addr)
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, addr)
}

// DialContext is the http.Transport hook. It is the only place the guard can be
// enforced correctly, because it is the only place that sees the address the
// connection will actually go to.
//
// Dialing the checked literal rather than the hostname is what closes the
// DNS-rebinding window: a resolve-then-dial-by-name implementation performs two
// lookups, and an attacker who controls the zone can answer the first with a
// public address and the second with 127.0.0.1.
//
// This does not affect TLS. http.Transport takes the SNI ServerName and the
// certificate hostname from the request URL, not from the address handed to
// DialContext, so a literal IP here still validates against the real hostname.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := d.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		d.debug("egress dial refused: the host resolved to no addresses", host, "")
		return nil, fmt.Errorf("%w: the host resolved to no addresses", ErrBlockedAddress)
	}
	// Any blocked address rejects the WHOLE dial. Skipping the blocked entries
	// and dialing the rest would let an attacker who controls the zone answer
	// with [public, 127.0.0.1] and still be refused — but it would also make
	// the guard depend on resolver ordering, and a split-horizon answer where
	// the public record is dead would fall through to the internal one.
	for _, ip := range ips {
		if d.blocked(ip) {
			d.debug("egress dial refused: the host resolved to a blocked address", host, ip.String())
			return nil, fmt.Errorf("%w: the destination is not allowed", ErrBlockedAddress)
		}
	}
	// Try each checked address in turn: a legitimate CDN with several A records
	// must not become unreachable because the first one is down.
	var lastErr error
	for _, ip := range ips {
		conn, err := d.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (d *Dialer) debug(msg, host, addr string) {
	if d.Log == nil {
		return
	}
	d.Log.Debug("providerhttp: "+msg, "host", host, "resolved", addr)
}

// CheckRedirect is the http.Client hook. It re-applies the scheme allowlist to
// every hop and caps the chain; the address check re-applies on its own because
// every hop goes back through the same Transport.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("%w: stopped after %d", ErrTooManyRedirects, MaxRedirects)
	}
	return ValidateParsedURL(req.URL)
}

// NewTransport returns a Transport whose every connection goes through the
// address guard.
//
// Proxy is left nil on purpose. http.ProxyFromEnvironment would send the
// request to the proxy by hostname, so the proxy — not this process — would do
// the resolution and the connect, and the guard would never see the target
// address at all.
func NewTransport() *http.Transport {
	d := newGuard(false, nil, nil, nil)
	d.Timeout = DefaultDialTimeout
	return &http.Transport{
		DialContext:           d.DialContext,
		Proxy:                 nil,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// NewClient returns a client with the transport guard and the redirect guard
// both wired in. timeout <= 0 means no overall deadline, so callers should pass
// one.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     NewTransport(),
		CheckRedirect: CheckRedirect,
	}
}
