// file: core/httpapi/client_info_test.go
// version: 0.1.0
// guid: c2e5a7f1-8d03-4b96-a1f4-5e7b90d3c862
// last-edited: 2026-11-21

package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// ciRouter is a router whose one Public route reports the ClientInfo
// ClientInfoFrom resolved, behind a real stage 0 configured with trusted
// proxies and the client-IP header.
func ciRouter(t *testing.T, trusted []string, header string) (http.Handler, *auth.ClientInfo) {
	t.Helper()
	var peers []TrustedPeer
	for _, s := range trusted {
		peers = append(peers, TrustedPeer{Prefix: netip.MustParsePrefix(s)})
	}
	r := NewRouter(Options{TrustedProxies: peers, ClientIPHeader: header}, Deps{})
	var got auth.ClientInfo
	Handle(r.CoreGroup(), Route{Method: http.MethodGet, Path: "/who", Name: "getWho", Perm: rbac.Public},
		func(ctx context.Context, in *In[struct{}]) (raOut, error) {
			got = ClientInfoFrom(in.Request)
			return raOut{OK: true}, nil
		})
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return h, &got
}

func ciServe(h http.Handler, remote string, hdr map[string]string) {
	r := httptest.NewRequest(http.MethodGet, "/api/core/v1/who", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
}

// TestClientInfoFrom_NeverMoreLocalThanAuthsFallback holds the resolver to
// auth's own no-resolver fallback over the cross product of peers, forwarding
// headers and trusted-proxy sets: wherever mine reports Local, so does the
// fallback. It also pins the fields Local reads: Peer is the unmapped TCP
// peer, Forwarded is true whenever the fallback's is, and TrustedProxy is true
// for a trusted peer.
func TestClientInfoFrom_NeverMoreLocalThanAuthsFallback(t *testing.T) {
	remotes := []string{
		"127.0.0.1:4000", "[::1]:4000", "[::ffff:127.0.0.1]:4000", "192.0.2.7:4000", "198.51.100.9:4000",
		"[fe80::1%en0]:4000", "127.0.0.1", "::1", "not-an-address", "",
	}
	headers := []map[string]string{
		nil,
		{"Forwarded": "for=192.0.2.50;proto=https"},
		{"X-Forwarded-For": "192.0.2.50"},
		{"X-Forwarded-Host": "gotham.example.test"},
		{"X-Forwarded-Proto": "https"},
		{"X-Real-IP": "192.0.2.50"},
		{"CF-Connecting-IP": "192.0.2.50"},
		{"True-Client-IP": "192.0.2.50"},
		{"X-Tunnel-IP": "192.0.2.50"},
		{"X-Forwarded-For": "192.0.2.50", "X-Forwarded-Proto": "https"},
		{"X-Forwarded-For": "not-an-address"},
	}
	trustedSets := [][]string{nil, {"127.0.0.1/32"}, {"::1/128"}, {"192.0.2.7/32"}, {"127.0.0.0/8", "::1/128", "192.0.2.0/24"}}
	for _, tr := range trustedSets {
		for _, hdrName := range []string{"", "X-Forwarded-For", "X-Tunnel-IP", "CF-Connecting-IP"} {
			h, got := ciRouter(t, tr, hdrName)
			for _, remote := range remotes {
				for _, hdr := range headers {
					*got = auth.ClientInfo{}
					ciServe(h, remote, hdr)
					req := httptest.NewRequest(http.MethodGet, "/", nil)
					req.RemoteAddr = remote
					for k, v := range hdr {
						req.Header.Set(k, v)
					}
					fb := auth.DirectClient(req)
					if got.Local() && !fb.Local() {
						t.Errorf("trusted %v header %q peer %q headers %v: the resolver admits loopback (%+v) where auth's fallback does not (%+v)",
							tr, hdrName, remote, hdr, *got, fb)
					}
					if got.Peer != fb.Peer {
						t.Errorf("trusted %v peer %q: Peer = %v, want the fallback's unmapped TCP peer %v", tr, remote, got.Peer, fb.Peer)
					}
					if fb.Forwarded && !got.Forwarded {
						t.Errorf("trusted %v peer %q headers %v: Forwarded dropped", tr, remote, hdr)
					}
					if got.HTTPS && !fb.HTTPS && !got.TrustedProxy {
						t.Errorf("trusted %v peer %q headers %v: HTTPS believed from an untrusted peer", tr, remote, hdr)
					}
				}
			}
		}
	}
}

// TestClientInfoFrom_Fields pins what the resolver adds to the fallback.
func TestClientInfoFrom_Fields(t *testing.T) {
	t.Run("trusted proxy names the client and reports https", func(t *testing.T) {
		h, got := ciRouter(t, []string{"192.0.2.7/32"}, "X-Forwarded-For")
		ciServe(h, "192.0.2.7:4000", map[string]string{"X-Forwarded-For": "198.51.100.20", "X-Forwarded-Proto": "https"})
		want := auth.ClientInfo{Addr: netip.MustParseAddr("198.51.100.20"), Peer: netip.MustParseAddr("192.0.2.7"),
			Forwarded: true, TrustedProxy: true, HTTPS: true}
		if got.Addr != want.Addr || got.Peer != want.Peer || got.Forwarded != want.Forwarded || got.TrustedProxy != want.TrustedProxy || got.HTTPS != want.HTTPS {
			t.Fatalf("got %+v, want %+v", *got, want)
		}
		if got.Local() {
			t.Fatal("a request through a trusted proxy is Local")
		}
	})
	t.Run("trusted proxy with the Forwarded header", func(t *testing.T) {
		h, got := ciRouter(t, []string{"192.0.2.7/32"}, "")
		ciServe(h, "192.0.2.7:4000", map[string]string{"Forwarded": "for=198.51.100.20;proto=https"})
		if !got.HTTPS || !got.TrustedProxy {
			t.Fatalf("got %+v, want HTTPS and TrustedProxy", *got)
		}
		ciServe(h, "192.0.2.7:4000", map[string]string{"Forwarded": "for=198.51.100.20;proto=http"})
		if got.HTTPS {
			t.Fatalf("a proto=http Forwarded reported HTTPS: %+v", *got)
		}
	})
	t.Run("untrusted peer is not believed", func(t *testing.T) {
		h, got := ciRouter(t, []string{"192.0.2.7/32"}, "X-Forwarded-For")
		ciServe(h, "198.51.100.9:4000", map[string]string{"X-Forwarded-For": "203.0.113.5", "X-Forwarded-Proto": "https"})
		if got.HTTPS || got.TrustedProxy || got.Addr != netip.MustParseAddr("198.51.100.9") || !got.Forwarded {
			t.Fatalf("got %+v: an untrusted peer's header was believed, or Forwarded was dropped", *got)
		}
	})
	t.Run("a loopback peer with no header and no proxy is local", func(t *testing.T) {
		h, got := ciRouter(t, nil, "")
		ciServe(h, "127.0.0.1:4000", nil)
		if !got.Local() {
			t.Fatalf("a direct loopback request is not Local: %+v", *got)
		}
	})
	t.Run("a loopback trusted proxy is not local even with no header", func(t *testing.T) {
		h, got := ciRouter(t, []string{"127.0.0.1/32"}, "")
		ciServe(h, "127.0.0.1:4000", nil)
		if got.Local() || !got.TrustedProxy {
			t.Fatalf("got %+v: behind a local reverse proxy the peer alone proves nothing", *got)
		}
	})
	t.Run("an IPv4-mapped loopback peer is unmapped", func(t *testing.T) {
		h, got := ciRouter(t, nil, "")
		ciServe(h, "[::ffff:127.0.0.1]:4000", nil)
		if got.Peer != netip.MustParseAddr("127.0.0.1") || got.Peer.Is4In6() {
			t.Fatalf("Peer = %v, want the unmapped 127.0.0.1", got.Peer)
		}
	})
}

func TestOnLink(t *testing.T) {
	lan := func() ([]net.Addr, error) {
		_, n, _ := net.ParseCIDR("2001:db8:1::5/64")
		_, v4, _ := net.ParseCIDR("192.0.2.0/24")
		return []net.Addr{n, v4, &net.IPAddr{}}, nil
	}
	for _, c := range []struct {
		addr string
		want bool
	}{
		{"2001:db8:1::99", true},
		{"2001:db8:2::1", false},
		{"fd00::1", false},
		{"fe80::1", false},
		{"::1", false},
		{"192.0.2.9", false},
		{"", false},
	} {
		var a netip.Addr
		if c.addr != "" {
			a = netip.MustParseAddr(c.addr)
		}
		if got := onLink(a, lan); got != c.want {
			t.Errorf("onLink(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
	if onLink(netip.MustParseAddr("2001:db8:1::99"), func() ([]net.Addr, error) { return nil, net.ErrClosed }) {
		t.Error("onLink with an interface error reported true")
	}
}
