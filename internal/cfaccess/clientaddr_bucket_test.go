// file: internal/cfaccess/clientaddr_bucket_test.go
// version: 0.1.0
// guid: 4b9e7d12-8a3c-4f50-b6e1-2c7a5d9f3e86
// last-edited: 2026-11-21

package cfaccess

import (
	"net/netip"
	"testing"
)

// TestClientBucket (F4-08 B10b1): the failure-throttle bucket of a request under
// core identity is the visitor's address as HEAD's loginThrottleKey keyed it.
func TestClientBucket(t *testing.T) {
	stage0 := netip.MustParseAddr("203.0.113.50")
	type tc struct {
		name       string
		c          addrCase
		stage0     netip.Addr
		attributed bool
		isLAN      func(netip.Addr) bool
		want       string
	}
	for _, c := range []tc{
		{name: "stage 0 attributed the visitor", c: addrCase{remote: "10.0.0.2:1"}, stage0: stage0, attributed: true, want: "203.0.113.50"},
		{name: "stage 0 attribution beats a believed Access address", c: addrCase{remote: "127.0.0.1:1", jwt: goodJWT, ip: "203.0.113.9"}, stage0: stage0, attributed: true, want: "203.0.113.50"},
		{name: "an unattributed stage 0 address is only the peer", c: addrCase{remote: "198.51.100.7:1"}, stage0: stage0, want: "198.51.100.7"},
		{name: "local peer behind a verified assertion keys on the visitor", c: addrCase{remote: "127.0.0.1:1", jwt: goodJWT, ip: "203.0.113.9"}, want: "203.0.113.9"},
		{name: "a forged assertion keys on the peer", c: addrCase{remote: "127.0.0.1:1", jwt: "jwt-forged-by-the-joker", ip: "203.0.113.9"}, want: "127.0.0.1"},
		{name: "a made-up CF-Connecting-IP from a public peer never picks the bucket", c: addrCase{remote: "198.51.100.7:1", jwt: goodJWT, ip: "203.0.113.9"}, want: "198.51.100.7"},
		{name: "an API key request keys on the peer, not the header", c: addrCase{remote: "127.0.0.1:1", jwt: goodJWT, ip: "203.0.113.9", key: true}, want: "127.0.0.1"},
		{name: "a public IPv6 visitor is one bucket per /64", c: addrCase{remote: "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:1"}, want: "2001:db8:1:2::/64"},
		{name: "another address in the same /64 shares it", c: addrCase{remote: "[2001:db8:1:2:1:2:3:4]:1"}, want: "2001:db8:1:2::/64"},
		{name: "a ULA IPv6 peer is its own bucket", c: addrCase{remote: "[fd00::5]:1"}, want: "fd00::5"},
		{name: "a link-local peer drops its zone", c: addrCase{remote: "[fe80::1%eth0]:1"}, want: "fe80::1"},
		{name: "an on-link global IPv6 peer is its own bucket when the host says LAN", c: addrCase{remote: "[2001:db8:1:2::9]:1"}, isLAN: func(netip.Addr) bool { return true }, want: "2001:db8:1:2::9"},
		{name: "an IPv4-mapped IPv6 peer is IPv4", c: addrCase{remote: "[::ffff:198.51.100.7]:1"}, want: "198.51.100.7"},
		{name: "a bare address peer", c: addrCase{remote: "198.51.100.8"}, want: "198.51.100.8"},
		{name: "an unparseable peer has no bucket", c: addrCase{remote: "gotham"}, want: ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			trust := ClientAddrTrust{Verifier: &tokenVerifier{}, IsLocalPeer: localPeer, KeyPresented: keyHeader, IsLANAddr: c.isLAN}
			if got := trust.ClientBucket(c.c.request(), c.stage0, c.attributed); got != c.want {
				t.Fatalf("ClientBucket = %q, want %q", got, c.want)
			}
		})
	}
	if got := (ClientAddrTrust{}).ClientBucket(nil, netip.Addr{}, false); got != "" {
		t.Fatalf("a nil request has bucket %q", got)
	}
}
