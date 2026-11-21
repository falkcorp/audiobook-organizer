// file: internal/cfaccess/clientaddr.go
// version: 0.1.0
// guid: 3e8c1b47-9a52-4d06-b7f1-05d2a6c9e8f3
// last-edited: 2026-11-21

package cfaccess

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
)

// ClientAddrTrust is the "local peer + verified Access assertion" rule of
// internal/server/client_trust.go's trustedClientIP, in net/http form for the
// identity flip (F4-08 A8; 05-http-api §8.4 SEC2-19: "at the flip its
// Access-assertion branch moves into ABO's internal/cfaccess and the rest is
// deleted"). Core's stage 0 already believes CF-Connecting-IP from a
// configured connector (core.http.trusted_proxies); this is the one case
// stage 0 does not cover: a request whose peer is this host (a cloudflared on
// the same machine) and that carries an Access assertion which verifies.
//
// Until the switch (F4-08 B10) the gin copy stays authoritative and nothing
// calls this: the gin binder has already verified the assertion
// (CFAccessVerified), and verifying it twice per request would buy nothing.
// After the switch the gin binder is gone, so the rule has to verify the
// assertion itself, which is why it holds a verifier.
type ClientAddrTrust struct {
	// Verifier checks the Access JWT (signature, issuer, expiry, AUD). A
	// service-token assertion (ErrNonIdentityAssertion) counts as verified, as
	// it does for the gin binder's marker: it proves the request passed Access
	// even though it names no person.
	Verifier CFAssertionVerifier
	// IsLocalPeer reports whether a raw RemoteAddr is this host (loopback or
	// one of its own interface addresses).
	IsLocalPeer func(remoteAddr string) bool
	// KeyPresented reports whether the request presents an API key. Such a
	// request is the key's, and the gin binder never marks it verified, so its
	// CF-Connecting-IP is not believed either. Nil means no request does.
	KeyPresented func(r *http.Request) bool
}

// AccessClientAddr returns the client address r's CF-Connecting-IP names and
// true when every condition holds:
//
//   - the rule is configured (a nil Verifier or IsLocalPeer believes nothing);
//   - r's peer is this host;
//   - r presents no API key;
//   - r carries an Access JWT (the Cf-Access-Jwt-Assertion header, else the
//     CF_Authorization cookie, as the gin binder reads them) that verifies, or
//     is a service token;
//   - CF-Connecting-IP is one bare IP address (no zone, no port, no list). An
//     IPv4-mapped IPv6 address is returned as IPv4, as net.IP's String gave it.
//
// Otherwise it returns the zero Addr and false, and the caller keys the request
// as trustedClientIP does without the branch. A key outage
// (ErrKeysUnavailable) believes nothing. Neither the token nor any header value
// is logged.
func (t ClientAddrTrust) AccessClientAddr(r *http.Request) (netip.Addr, bool) {
	if t.Verifier == nil || t.IsLocalPeer == nil || r == nil {
		return netip.Addr{}, false
	}
	if !t.IsLocalPeer(r.RemoteAddr) {
		return netip.Addr{}, false
	}
	if t.KeyPresented != nil && t.KeyPresented(r) {
		return netip.Addr{}, false
	}
	raw := r.Header.Get(CFAccessHeader)
	if raw == "" {
		if ck, err := r.Cookie("CF_Authorization"); err == nil {
			raw = ck.Value
		}
	}
	if raw == "" {
		return netip.Addr{}, false
	}
	if _, err := t.Verifier.Verify(r.Context(), raw); err != nil && !errors.Is(err, ErrNonIdentityAssertion) {
		return netip.Addr{}, false
	}
	return connectingIP(r.Header.Get("CF-Connecting-IP"))
}

// connectingIP parses a CF-Connecting-IP value the way trustedClientIP's
// net.ParseIP did: one address, surrounding space trimmed, no zone.
func connectingIP(v string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(v))
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}
