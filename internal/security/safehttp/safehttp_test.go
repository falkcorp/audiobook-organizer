// file: internal/security/safehttp/safehttp_test.go
// version: 1.0.0
// guid: 23c60e98-8378-4efd-9fe1-69d3ae85b3e9
// last-edited: 2026-09-10

package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
		why     string
	}{
		{"127.0.0.1", true, "IPv4 loopback"},
		{"127.1.2.3", true, "rest of 127.0.0.0/8"},
		{"0.0.0.0", true, "unspecified — reaches localhost on Linux"},
		{"0.1.2.3", true, "rest of 0.0.0.0/8"},
		{"169.254.169.254", true, "cloud instance metadata"},
		{"10.1.2.3", true, "RFC1918"},
		// The 172.16/12 sample is taken from the top of the range rather than
		// the bottom: this repo is public and the pre-commit hook rejects any
		// literal 172.16.x.x, which is the prefix its internal network uses.
		{"172.31.255.254", true, "RFC1918"},
		{"192.168.1.1", true, "RFC1918"},
		{"100.64.0.1", true, "CGNAT"},
		{"192.0.0.170", true, "IETF protocol assignments"},
		{"198.18.0.1", true, "benchmarking"},
		{"224.0.0.1", true, "multicast"},
		{"240.0.0.1", true, "reserved"},
		{"255.255.255.255", true, "broadcast"},
		{"::1", true, "IPv6 loopback"},
		{"::", true, "IPv6 unspecified"},
		{"::ffff:127.0.0.1", true, "IPv4-mapped loopback"},
		{"::ffff:169.254.169.254", true, "IPv4-mapped metadata address"},
		{"fe80::1", true, "IPv6 link-local"},
		{"fc00::1", true, "IPv6 unique-local"},
		{"ff02::1", true, "IPv6 multicast"},
		{"2002:7f00:0001::", true, "6to4 wrapping 127.0.0.1"},
		{"64:ff9b::7f00:1", true, "NAT64 wrapping 127.0.0.1"},

		{"8.8.8.8", false, "public"},
		{"1.1.1.1", false, "public"},
		{"93.184.216.34", false, "public"},
		{"2606:4700:4700::1111", false, "public IPv6"},
		{"192.0.2.10", false, "TEST-NET-1 is deliberately allowed; tests use it as a public stand-in"},
		{"198.51.100.7", false, "TEST-NET-2 is deliberately allowed"},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("test bug: %q is not a parseable IP", c.ip)
		}
		if got := IsBlockedIP(ip); got != c.blocked {
			t.Errorf("IsBlockedIP(%s) = %v, want %v (%s)", c.ip, got, c.blocked, c.why)
		}
	}
	if !IsBlockedIP(nil) {
		t.Error("IsBlockedIP(nil) = false; an unparseable address must fail closed")
	}
}

func TestValidateURL(t *testing.T) {
	blocked := []string{
		"file:///etc/passwd",
		"file://localhost/etc/passwd",
		"gopher://192.0.2.10:70/_x",
		"dict://192.0.2.10:2628/",
		"ftp://192.0.2.10/cover.jpg",
		"data:image/jpeg;base64,AAAA",
		"//covers.example.test/cover.jpg", // scheme-relative: no scheme
		"https://",                        // no host
	}
	for _, raw := range blocked {
		if err := ValidateURL(raw); err == nil {
			t.Errorf("ValidateURL(%q) = nil; want rejection", raw)
		}
	}

	allowed := []string{
		"https://covers.openlibrary.org/b/id/1234-L.jpg",
		"http://covers.openlibrary.org/b/id/1234-L.jpg",
		"https://user:pass@covers.example.test/cover.jpg",
		"https://covers.example.test:8443/cover.jpg",
	}
	for _, raw := range allowed {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("ValidateURL(%q) = %v; want nil", raw, err)
		}
	}

	// A scheme rejection must be distinguishable from a malformed URL.
	if err := ValidateURL("file:///etc/passwd"); !errors.Is(err, ErrBlockedScheme) {
		t.Errorf("file:// rejection = %v; want ErrBlockedScheme", err)
	}
}

// stubDialer builds a Dialer whose DNS answers are fixed and whose dials are
// recorded, so the guard can be exercised without touching the network.
func stubDialer(t *testing.T, hosts map[string][]string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (*Dialer, *[]string) {
	t.Helper()
	var dialed []string
	d := &Dialer{
		LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
			if ip := net.ParseIP(host); ip != nil {
				return []net.IP{ip}, nil
			}
			raw, ok := hosts[host]
			if !ok {
				return nil, fmt.Errorf("no such host: %s", host)
			}
			ips := make([]net.IP, 0, len(raw))
			for _, r := range raw {
				ips = append(ips, net.ParseIP(r))
			}
			return ips, nil
		},
		DialAddr: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			if dial == nil {
				return nil, errors.New("stub: no dial configured")
			}
			return dial(ctx, network, addr)
		},
	}
	return d, &dialed
}

func TestDialer_RefusesHostResolvingToLoopback(t *testing.T) {
	d, dialed := stubDialer(t, map[string][]string{"rebind.example.test": {"127.0.0.1"}}, nil)

	_, err := d.DialContext(context.Background(), "tcp", "rebind.example.test:80")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("DialContext error = %v; want ErrBlockedAddress", err)
	}
	if len(*dialed) != 0 {
		t.Fatalf("dialed %v; a blocked host must not reach the dial at all", *dialed)
	}
}

func TestDialer_RefusesWhenAnyResolvedAddressIsBlocked(t *testing.T) {
	// The split-horizon answer: one good address, one internal one. Refusing
	// only the blocked entry and dialing the other would still be exploitable
	// whenever the public record is dead.
	d, dialed := stubDialer(t, map[string][]string{
		"mixed.example.test": {"198.51.100.7", "169.254.169.254"},
	}, nil)

	_, err := d.DialContext(context.Background(), "tcp", "mixed.example.test:80")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("DialContext error = %v; want ErrBlockedAddress", err)
	}
	if len(*dialed) != 0 {
		t.Fatalf("dialed %v; want no dial when any resolved address is blocked", *dialed)
	}
}

func TestDialer_DialsCheckedLiteralNotHostname(t *testing.T) {
	// This is the DNS-rebinding guarantee: the address handed to the underlying
	// dial must be the literal that IsBlockedIP just approved, never the
	// hostname (which would be resolved a second time).
	d, dialed := stubDialer(t, map[string][]string{"cdn.example.test": {"198.51.100.7"}},
		func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connect refused") })

	_, _ = d.DialContext(context.Background(), "tcp", "cdn.example.test:443")
	if len(*dialed) != 1 || (*dialed)[0] != "198.51.100.7:443" {
		t.Fatalf("dialed %v; want exactly [198.51.100.7:443]", *dialed)
	}
}

func TestDialer_FallsBackToNextCheckedAddress(t *testing.T) {
	d, dialed := stubDialer(t, map[string][]string{
		"cdn.example.test": {"198.51.100.7", "192.0.2.10"},
	}, func(_ context.Context, _, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "198.51.100.7") {
			return nil, errors.New("connect refused")
		}
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	})

	conn, err := d.DialContext(context.Background(), "tcp", "cdn.example.test:80")
	if err != nil {
		t.Fatalf("DialContext = %v; want the second A record to be tried", err)
	}
	_ = conn.Close()
	if len(*dialed) != 2 {
		t.Fatalf("dialed %v; want both addresses tried in order", *dialed)
	}
}

func TestCheckRedirect(t *testing.T) {
	hop := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("test bug: %v", err)
		}
		return &http.Request{URL: u}
	}

	if err := CheckRedirect(hop("file:///etc/passwd"), nil); !errors.Is(err, ErrBlockedScheme) {
		t.Errorf("redirect to file:// = %v; want ErrBlockedScheme", err)
	}
	if err := CheckRedirect(hop("https://covers.example.test/a.jpg"), nil); err != nil {
		t.Errorf("redirect to https = %v; want nil", err)
	}

	via := make([]*http.Request, MaxRedirects)
	if err := CheckRedirect(hop("https://covers.example.test/a.jpg"), via); !errors.Is(err, ErrTooManyRedirects) {
		t.Errorf("hop %d = %v; want ErrTooManyRedirects", MaxRedirects+1, err)
	}
}

// guardedClientTo builds a real http.Client with both guards wired in, whose
// DNS answers "cdn.example.test" with a public address and whose dials land on
// the given loopback listener. Everything the guard inspects is real; only the
// resolver and the socket destination are substituted, because a test cannot
// stand up a listener on a genuinely public address.
func guardedClientTo(listenerPort string) *http.Client {
	d := &Dialer{
		LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
			if host == "cdn.example.test" {
				return []net.IP{net.ParseIP("198.51.100.7")}, nil
			}
			if ip := net.ParseIP(host); ip != nil {
				return []net.IP{ip}, nil
			}
			return nil, fmt.Errorf("no such host: %s", host)
		},
		DialAddr: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "198.51.100.7:") {
				addr = "127.0.0.1:" + listenerPort
			}
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, addr)
		},
	}
	return &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{DialContext: d.DialContext},
		CheckRedirect: CheckRedirect,
	}
}

func portOf(t *testing.T, srvURL string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srvURL, "http://"))
	if err != nil {
		t.Fatalf("test bug: %v", err)
	}
	return port
}

func TestClient_AllowsPublicAddress(t *testing.T) {
	// Anti-over-suppression: a host that resolves to a non-blocked address must
	// still fetch successfully with the guard active.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("cover-bytes"))
	}))
	defer srv.Close()

	c := guardedClientTo(portOf(t, srv.URL))
	resp, err := c.Get("http://cdn.example.test/cover.jpg")
	if err != nil {
		t.Fatalf("fetch from an allowed address failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "cover-bytes" {
		t.Fatalf("body = %q, want the cover bytes", body)
	}
}

func TestClient_RefusesRedirectHopToLoopback(t *testing.T) {
	// An allowlisted host 302s to a loopback address. The hop must be refused
	// by the address guard, which re-applies because every hop goes back
	// through the same Transport.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("internal-service-response"))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/latest/meta-data/", http.StatusFound)
	}))
	defer origin.Close()

	c := guardedClientTo(portOf(t, origin.URL))
	resp, err := c.Get("http://cdn.example.test/cover.jpg")
	if err == nil {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("the redirect to %s was followed and returned %q; the hop must be refused", target.URL, body)
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("error = %v; want ErrBlockedAddress so the refusal is attributable to the guard, not a broken fixture", err)
	}
}
