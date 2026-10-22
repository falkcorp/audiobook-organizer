// file: internal/server/peer_allowlist_test.go
// version: 1.0.0
// guid: 89583aea-53c7-4ab3-9f35-dea7d459f40f
// last-edited: 2026-10-21

package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// 192.0.2.0/24, 198.51.100.0/24 and 203.0.113.0/24 are the RFC 5737 TEST-NET
// blocks; 2001:db8::/32 is the RFC 3849 documentation block.

func setAllowedPeerCIDRs(t *testing.T, csv string) {
	t.Helper()
	prev := config.AllowedPeerCIDRs()
	config.Mutate(func(c *config.Config) { c.AllowedPeerCIDRs = csv })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.AllowedPeerCIDRs = prev }) })
}

// peerGateStatus sends one request from remote through the gate and reports
// the status and whether the inner handler ran.
func peerGateStatus(remote string) (status int, reached bool) {
	h := newPeerAllowListHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks?q=secret", nil)
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, reached
}

func TestPeerAllowList_Decisions(t *testing.T) {
	setTunnelConnectors(t, "198.51.100.20")
	setAllowedPeerCIDRs(t, " 192.0.2.0/24, 203.0.113.9 ,2001:db8:1::/48")
	tests := []struct {
		name   string
		remote string
		want   bool
	}{
		{"IPv4 loopback", "127.0.0.1:5000", true},
		{"IPv6 loopback", "[::1]:5000", true},
		{"a configured tunnel connector, not in the CIDR list", "198.51.100.20:5000", true},
		{"an address inside a listed network", "192.0.2.77:5000", true},
		{"the first address of a listed network", "192.0.2.0:5000", true},
		{"a listed bare address", "203.0.113.9:5000", true},
		{"an IPv4-mapped form of a listed address", "[::ffff:192.0.2.77]:5000", true},
		{"an IPv6 address in a listed network", "[2001:db8:1:5::9]:5000", true},
		{"an address just outside a listed network", "192.0.3.1:5000", false},
		{"a neighbour of a listed bare address", "203.0.113.10:5000", false},
		{"an unlisted public address", "8.8.8.8:5000", false},
		{"an unlisted address in a documentation block", "198.51.100.21:5000", false},
		{"an IPv6 address outside every listed network", "[2001:db8:2::1]:5000", false},
		{"a peer that is not an address", "not-an-address", false},
		{"an empty peer", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reached := peerGateStatus(tt.remote)
			if tt.want {
				require.Equal(t, http.StatusNoContent, status)
				require.True(t, reached)
				return
			}
			require.Equal(t, http.StatusForbidden, status)
			require.False(t, reached, "the inner handler must never run for a refused peer")
		})
	}
}

// Whatever the caller claims in headers has no say: the peer is the socket.
func TestPeerAllowList_HeadersCannotAdmitAPeer(t *testing.T) {
	setAllowedPeerCIDRs(t, "192.0.2.0/24")
	reached := false
	h := newPeerAllowListHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "8.8.8.8:5000"
	for k, v := range map[string]string{
		"X-Forwarded-For": "192.0.2.5", "X-Real-IP": "192.0.2.5", "CF-Connecting-IP": "192.0.2.5", "Forwarded": "for=192.0.2.5",
	} {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.False(t, reached)
}

func TestPeerAllowList_EmptyListAllowsEveryPeer(t *testing.T) {
	setAllowedPeerCIDRs(t, "")
	for _, remote := range []string{"8.8.8.8:5000", "192.0.2.1:1", "[2001:db8::1]:1", "127.0.0.1:1"} {
		status, reached := peerGateStatus(remote)
		require.Equal(t, http.StatusNoContent, status, remote)
		require.True(t, reached, remote)
	}
}

// A typo must not open the door: a list with no valid entry refuses everything
// except loopback and the connectors.
func TestPeerAllowList_ListWithNoValidEntryStillEnforces(t *testing.T) {
	setTunnelConnectors(t, "198.51.100.20")
	setAllowedPeerCIDRs(t, "192.0.2.0/33, nonsense, 10.0.0.0/8/8")
	status, reached := peerGateStatus("192.0.2.5:5000")
	require.Equal(t, http.StatusForbidden, status)
	require.False(t, reached)
	status, _ = peerGateStatus("127.0.0.1:5000")
	require.Equal(t, http.StatusNoContent, status, "loopback stays allowed")
	status, _ = peerGateStatus("198.51.100.20:5000")
	require.Equal(t, http.StatusNoContent, status, "a connector stays allowed")
}

// A bad entry is skipped; the good ones next to it still count.
func TestPeerAllowList_InvalidEntryDoesNotDropItsNeighbours(t *testing.T) {
	setAllowedPeerCIDRs(t, "nonsense,192.0.2.0/24")
	status, _ := peerGateStatus("192.0.2.5:5000")
	require.Equal(t, http.StatusNoContent, status)
}

func TestParsePeerAllowList(t *testing.T) {
	list, invalid := parsePeerAllowList("192.0.2.17/24, 203.0.113.9, ::ffff:198.51.100.0/120, ::ffff:0:0/80, fe80::1%eth0, 2001:db8::/32, , x")
	require.Equal(t, []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),    // host bits masked
		netip.MustParsePrefix("203.0.113.9/32"),  // bare address
		netip.MustParsePrefix("198.51.100.0/24"), // mapped prefix written as IPv4
		netip.MustParsePrefix("fe80::1/128"),     // zone dropped
		netip.MustParsePrefix("2001:db8::/32"),
	}, []netip.Prefix(list))
	require.Equal(t, []string{"::ffff:0:0/80", "x"}, invalid, "a prefix shorter than the mapped block is refused, not guessed")
	require.True(t, list.contains(netip.MustParseAddr("fe80::1%en7")), "a zoned peer matches its address")
}

// The gate replaces gin's handler on EVERY serving listener. Each is driven
// with a peer that is not allowed; a listener still serving s.router directly
// would answer 200.
func TestConfigureAndStartHTTP_EveryListenerIsGatedByThePeerAllowList(t *testing.T) {
	setAllowedPeerCIDRs(t, "192.0.2.0/24")
	certPath, keyPath, _ := writeSelfSignedCert(t)
	cfg := listenTestConfig("0")
	cfg.TLSCertFile, cfg.TLSKeyFile = certPath, keyPath
	cfg.HTTP3Port = "0"

	s := listenTestServer()
	require.NoError(t, s.configureAndStartHTTP(cfg))
	t.Cleanup(func() {
		_ = s.httpServer.Close()
		if s.http3Server != nil {
			_ = s.http3Server.Close()
		}
		if s.httpRedirectServer != nil {
			_ = s.httpRedirectServer.Close()
		}
	})

	probe := func(h http.Handler, remote string) int {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	handlers := map[string]http.Handler{"https": s.httpServer.Handler}
	require.NotNil(t, s.http3Server, "HTTP/3 server was not configured")
	handlers["http3"] = s.http3Server.Handler
	if s.httpRedirectServer != nil { // port 80 is not always bindable
		handlers["redirect"] = s.httpRedirectServer.Handler
	}
	for name, h := range handlers {
		require.Equal(t, http.StatusForbidden, probe(h, "8.8.8.8:5000"), name+": an unlisted peer must be refused")
	}
	require.Equal(t, http.StatusOK, probe(s.httpServer.Handler, "192.0.2.5:5000"), "an allowed peer reaches the router")
	require.Equal(t, http.StatusOK, probe(s.http3Server.Handler, "127.0.0.1:5000"))
}

func TestConfigureAndStartHTTP_PlainHTTPListenerIsGated(t *testing.T) {
	setAllowedPeerCIDRs(t, "192.0.2.0/24")
	s := listenTestServer()
	require.NoError(t, s.configureAndStartHTTP(listenTestConfig("0")))
	t.Cleanup(func() { _ = s.httpServer.Close() })
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = "8.8.8.8:5000"
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)

	// A real connection from loopback is always allowed, even with a list that
	// names no loopback network: a wrong list cannot lock the operator out of
	// the host.
	resp, err := http.Get("http://" + *s.httpBoundAddr.Load() + "/ping")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
