// file: core/integrations/providerhttp/egress_guard_test.go
// version: 1.0.0
// guid: 8815c998-81fb-4bb2-89f4-58fb0a66ea1f
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// dialRecorder is a Resolver and a DialAddr over one real loopback server: it
// answers names with the IPs a test scripts and connects every checked address
// to the real server, recording what the guard handed to the dial.
type dialRecorder struct {
	mu      sync.Mutex
	answers func(call int, host string) []net.IP
	lookups []string
	dialed  []string
	real    string
}

func (d *dialRecorder) resolve(_ context.Context, host string) ([]net.IP, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lookups = append(d.lookups, host)
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	return d.answers(len(d.lookups), host), nil
}

func (d *dialRecorder) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, addr)
	d.mu.Unlock()
	if strings.HasPrefix(addr, "192.0.2.") { // the fake public address stands for the real server
		addr = d.real
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func (d *dialRecorder) dials() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dialed...)
}

func (d *dialRecorder) options(o *providerhttp.Options) {
	o.Resolver, o.DialAddr = d.resolve, d.dial
	o.Base = &http.Transport{DisableKeepAlives: true} // every request dials, so every dial is checked
}

func ips(s ...string) []net.IP {
	var out []net.IP
	for _, x := range s {
		out = append(out, net.ParseIP(x))
	}
	return out
}

// fake public name for the loopback server: 192.0.2.0/24 is the documentation
// range the guard leaves public.
const publicName = "alfred.example.test"

func nameURL(srv *httptest.Server, path string) string {
	_, port, _ := net.SplitHostPort(hostOf(srv))
	return "http://" + publicName + ":" + port + path
}

func blockedBy(err error) bool {
	return errors.Is(err, providerhttp.ErrBlockedAddress) && refusedFor(err, providerhttp.ReasonEgress)
}

// TestEgressPublicOnlyRefusesPrivate: data-derived URLs never reach a private
// address, whatever trust the instance holds.
func TestEgressPublicOnlyRefusesPrivate(t *testing.T) {
	r := newRegistry(t, newClock())
	srv, hits := server(t, ok)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Egress: integrations.EgressPublicOnly, Private: anyPrivate})
	if _, err := get(t, c, srv.URL); !blockedBy(err) {
		t.Fatalf("err = %v, want an egress refusal", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the private server was reached")
	}
}

// TestEgress_PrivateRefusedUnlessLocal: a private address is refused unless the
// instance is trusted for that endpoint.
func TestEgress_PrivateRefusedUnlessLocal(t *testing.T) {
	r := newRegistry(t, newClock())
	srv, hits := server(t, ok)
	for name, p := range map[string]func(string) bool{"no trust": nil, "untrusted": func(string) bool { return false }} {
		c := r.Client(ref(strings.ReplaceAll(name, " ", "")), providerhttp.Policy{Limits: limits(100, 100), Private: p})
		if _, err := get(t, c, srv.URL); !blockedBy(err) {
			t.Errorf("%s: err = %v, want refused", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("the private server was reached")
	}
	var asked []string
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Private: func(hp string) bool { asked = append(asked, hp); return hp == hostOf(srv) }})
	if _, err := get(t, c, srv.URL); err != nil {
		t.Fatalf("trusted endpoint: %v", err)
	}
	if len(asked) != 1 || asked[0] != hostOf(srv) {
		t.Fatalf("Private was asked %v, want the lower-case host:port once", asked)
	}
	if hits.Load() != 1 {
		t.Fatal("the trusted request was not sent")
	}
}

// TestEgress_LANAllowListAdmits: the operator's lan_allow admits an instance
// with no Local: a host, a host:port and a literal-IP CIDR; nothing else.
func TestEgress_LANAllowListAdmits(t *testing.T) {
	srv, hits := server(t, ok)
	allow := []string{}
	r := newRegistry(t, newClock(), func(o *providerhttp.Options) { o.LANAllow = func() []string { return allow } })
	spec := integrations.OutboundSpec{Ref: batcave, Egress: integrations.EgressConfigured, Limits: limits(100, 100)}
	h, _ := r.Outbound(spec) // no Trust at all
	do := func() error {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := h.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := do(); !blockedBy(err) {
		t.Fatalf("empty allow-list: %v", err)
	}
	host, port, _ := net.SplitHostPort(hostOf(srv))
	for _, entry := range []string{"127.0.0.1:" + port, "127.0.0.1", "127.0.0.0/8"} {
		allow = []string{"192.0.2.9", entry}
		if err := do(); err != nil {
			t.Errorf("allow %q: %v", entry, err)
		}
	}
	for _, entry := range []string{"127.0.0.1:1", "127.0.0.2", "10.0.0.0/8", "localhost", "127.0.0.1/"} {
		allow = []string{entry}
		if err := do(); !blockedBy(err) {
			t.Errorf("allow %q wrongly admits %s:%s: %v", entry, host, port, err)
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3", hits.Load())
	}
}

// TestEgress_LinkLocalAlwaysRefused: link-local addresses, the cloud metadata
// endpoint among them, are refused even for a trusted instance, by literal, by
// name and in IPv4-mapped form, and nothing is dialled.
func TestEgress_LinkLocalAlwaysRefused(t *testing.T) {
	srv, _ := server(t, ok)
	_, port, _ := net.SplitHostPort(hostOf(srv))
	rec := &dialRecorder{real: hostOf(srv), answers: func(int, string) []net.IP { return ips("169.254.169.254") }}
	r := newRegistry(t, newClock(), rec.options)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Private: anyPrivate})
	for _, u := range []string{
		"http://169.254.169.254:" + port + "/latest/meta-data/",
		"http://[fe80::1]:" + port + "/",
		"http://[::ffff:169.254.169.254]:" + port + "/",
		"http://" + publicName + ":" + port + "/", // a name that resolves to metadata
	} {
		if _, err := get(t, c, u); !blockedBy(err) {
			t.Errorf("%s: err = %v, want refused", u, err)
		}
	}
	if d := rec.dials(); len(d) != 0 {
		t.Fatalf("dialled %v: a link-local address was connected to", d)
	}
}

// TestEgress_RebindingCheckedAtDial: the RESOLVED address is checked at every
// dial and the dial goes to the checked literal, never the name; a name that
// answers public first and loopback later is refused the second time, and one
// answer holding a blocked address rejects the whole dial.
func TestEgress_RebindingCheckedAtDial(t *testing.T) {
	srv, hits := server(t, ok)
	rec := &dialRecorder{real: hostOf(srv), answers: func(call int, _ string) []net.IP {
		switch call {
		case 1:
			return ips("192.0.2.10")
		case 2:
			return ips("127.0.0.1")
		}
		return ips("192.0.2.10", "10.0.0.5") // one blocked answer poisons the set
	}}
	r := newRegistry(t, newClock(), rec.options)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Private: func(hp string) bool { return false }})
	u := nameURL(srv, "/")
	if _, err := get(t, c, u); err != nil {
		t.Fatalf("public answer: %v", err)
	}
	if d := rec.dials(); len(d) != 1 || !strings.HasPrefix(d[0], "192.0.2.10:") {
		t.Fatalf("dialled %v, want the checked literal 192.0.2.10", d)
	}
	if _, err := get(t, c, u); !blockedBy(err) {
		t.Fatalf("rebound answer: %v, want refused", err)
	}
	if _, err := get(t, c, u); !blockedBy(err) {
		t.Fatalf("mixed answer: %v, want the whole dial refused", err)
	}
	if len(rec.dials()) != 1 || hits.Load() != 1 {
		t.Fatalf("dials %v hits %d: a refused name was connected to", rec.dials(), hits.Load())
	}
}

// TestEgress_HostnameResolvingPrivateRefused: a name is judged by what it
// resolves to; trust is for the instance's bound endpoints only.
func TestEgress_HostnameResolvingPrivateRefused(t *testing.T) {
	srv, hits := server(t, ok)
	rec := &dialRecorder{real: hostOf(srv), answers: func(int, string) []net.IP { return ips("127.0.0.1") }}
	r := newRegistry(t, newClock(), rec.options)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100)})
	if _, err := get(t, c, nameURL(srv, "/")); !blockedBy(err) {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 0 || len(rec.dials()) != 0 {
		t.Fatal("connected to a name that resolves to loopback")
	}
}

// TestEgressSameHostOrPublic: the configured endpoint is admitted under the
// instance's own trust, any other private endpoint is not, and a public host
// is.
func TestEgressSameHostOrPublic(t *testing.T) {
	a, _ := server(t, ok)
	b, bHits := server(t, ok)
	r := newRegistry(t, newClock())
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Egress: integrations.EgressSameHostOrPublic,
		Private: func(hp string) bool { return hp == hostOf(a) }})
	if _, err := get(t, c, a.URL); err != nil {
		t.Fatalf("the configured host: %v", err)
	}
	if _, err := get(t, c, b.URL); !blockedBy(err) {
		t.Fatalf("another private host: %v", err)
	}
	if bHits.Load() != 0 {
		t.Fatal("reached the other private host")
	}
}

// TestLocalInstance_OtherPrivateHostRefused (SEC4-15): Local is bound to the
// instance's configured origins, not to the private range.
func TestLocalInstance_OtherPrivateHostRefused(t *testing.T) {
	a, _ := server(t, ok)
	b, bHits := server(t, ok)
	r := newRegistry(t, newClock())
	h, _ := r.Outbound(integrations.OutboundSpec{Ref: batcave, Egress: integrations.EgressConfigured, Limits: limits(100, 100),
		Trust: integrations.Trust{PrivateOrigins: []string{"http://" + hostOf(a)}}})
	for url, want := range map[string]bool{a.URL: true, b.URL: false} {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := h.Do(req)
		if want && err != nil || !want && !blockedBy(err) {
			t.Errorf("%s: err = %v, want admitted=%v", url, err, want)
		}
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	if bHits.Load() != 0 {
		t.Fatal("a Local instance reached a private host that is not its own")
	}
	// A different port on the bound host is another endpoint.
	_, port, _ := net.SplitHostPort(hostOf(a))
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+"1/", nil)
	if _, err := h.Do(req); !blockedBy(err) {
		t.Fatalf("another port of the bound host: %v", err)
	}
}

func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) }
}

func follow(*http.Request, []*http.Request) error { return nil }

// TestLocalInstance_RedirectToOtherPrivateHostRefused (SEC4-15): a redirect
// must not carry private trust to another host, for a client that follows
// redirects with its own CheckRedirect.
func TestLocalInstance_RedirectToOtherPrivateHostRefused(t *testing.T) {
	b, bHits := server(t, ok)
	a, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/landed", http.StatusFound)
		case "/landed":
			ok(w, r)
		default:
			http.Redirect(w, r, b.URL, http.StatusFound)
		}
	})
	r := newRegistry(t, newClock())
	h, _ := r.Outbound(integrations.OutboundSpec{Ref: batcave, Egress: integrations.EgressConfigured, Limits: limits(100, 100),
		Trust: integrations.Trust{PrivateOrigins: []string{"http://" + hostOf(a)}}})
	cl := *h.Client()
	cl.CheckRedirect = follow // as an SDK that sets its own policy
	resp, err := cl.Get(a.URL + "/same")
	if err != nil {
		t.Fatalf("a same-host redirect must be followed: %v", err)
	}
	_ = resp.Body.Close()
	if _, err := cl.Get(a.URL + "/away"); !blockedBy(err) {
		t.Fatalf("a redirect to another private host: %v", err)
	}
	if bHits.Load() != 0 {
		t.Fatal("the redirect target was reached")
	}
	// The stock client, with its redirect cap, refuses it too.
	if _, err := h.Client().Get(a.URL + "/away"); !blockedBy(err) {
		t.Fatalf("stock client: %v", err)
	}
}

// TestRedirectFromPublicToPrivateRefused: a public (or allow-listed) host that
// redirects to loopback is refused although the instance holds private trust
// for other endpoints.
func TestRedirectFromPublicToPrivateRefused(t *testing.T) {
	inner, innerHits := server(t, ok)
	pub, _ := server(t, redirectTo(inner.URL))
	rec := &dialRecorder{real: hostOf(pub), answers: func(int, string) []net.IP { return ips("192.0.2.10") }}
	r := newRegistry(t, newClock(), rec.options)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Private: anyPrivate})
	cl := *c.Client()
	cl.CheckRedirect = follow
	if _, err := cl.Get(nameURL(pub, "/")); !blockedBy(err) {
		t.Fatalf("err = %v, want the private hop refused", err)
	}
	if innerHits.Load() != 0 {
		t.Fatal("the private redirect target was reached")
	}
}

// TestCopiedClient_KeepsEveryControl is the F7-04 seam: the Deluge adapter
// calls Client(), copies it and sets its own CheckRedirect, and never calls Do.
// Every control must still hold in that copy.
func TestCopiedClient_KeepsEveryControl(t *testing.T) {
	trusted, _ := server(t, ok)
	other, otherHits := server(t, ok)
	redir, _ := server(t, redirectTo(other.URL))
	clk := newClock()
	rec := &dialRecorder{real: hostOf(trusted), answers: func(int, string) []net.IP { return ips("127.0.0.1") }}
	r := newRegistry(t, clk, rec.options, func(o *providerhttp.Options) { o.Base = &http.Transport{DisableKeepAlives: true} })
	lim := limits(1, 1)
	lim.Daily = daily(2, 2)
	h, g := r.Outbound(integrations.OutboundSpec{Ref: batcave, Egress: integrations.EgressConfigured, Limits: lim,
		Trust: integrations.Trust{PrivateOrigins: []string{"http://" + hostOf(trusted), "http://" + hostOf(redir)}}})
	c1, c2 := h.Client(), h.Client()
	if c1 == nil || c1 == c2 {
		t.Fatal("Client must return a fresh, non-nil *http.Client on every call")
	}
	if h.Gate() == nil || g == nil {
		t.Fatal("no gate")
	}
	deluge := func() *http.Client { // what Deluge's client does
		c := *h.Client()
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return &c
	}
	follows := func() *http.Client {
		c := *h.Client()
		c.CheckRedirect = follow
		return &c
	}

	// 1. a private origin the Trust does not allow is refused
	if _, err := deluge().Get(other.URL); !blockedBy(err) {
		t.Fatalf("untrusted private origin: %v", err)
	}
	// 2. a hostname that resolves to a private address is refused
	if _, err := deluge().Get(nameURL(trusted, "/")); !blockedBy(err) {
		t.Fatalf("hostname resolving private: %v", err)
	}
	// 3. a redirect into a private host is refused when followed
	if _, err := follows().Get(redir.URL); !blockedBy(err) {
		t.Fatalf("redirect into a private host: %v", err)
	}
	// ... and not followed when the copy says so: the 302 is returned as is
	resp, err := deluge().Get(redir.URL)
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("an unfollowed redirect = %v, %v", resp, err)
	}
	_ = resp.Body.Close()
	if otherHits.Load() != 0 {
		t.Fatal("an untrusted private host was reached")
	}

	// 4. quota and throttle still apply to the copy. Two units of the daily
	// quota went to the redirect probes above only if they were SENT; count what
	// was actually sent and spend the rest, then be refused.
	sent := h.Gate().State().QuotaUsed["requests"]
	for ; sent < 2; sent++ {
		resp, err := deluge().Get(trusted.URL)
		if err != nil {
			t.Fatalf("trusted request: %v", err)
		}
		_ = resp.Body.Close()
	}
	if _, err := deluge().Get(trusted.URL); !refusedFor(err, providerhttp.ReasonQuota) {
		t.Fatalf("past the daily quota: %v, want a quota refusal", err)
	}
	if len(clk.Sleeps()) == 0 {
		t.Fatal("the shared token bucket never paced the copy")
	}
}

// TestSchemeAllowlistOnEveryRequest: a copied client with no redirect policy
// still cannot reach another scheme.
func TestSchemeAllowlistOnEveryRequest(t *testing.T) {
	r := newRegistry(t, newClock())
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(100, 100), Private: anyPrivate})
	cl := *c.Client()
	cl.CheckRedirect = nil
	for _, u := range []string{"file:///etc/passwd", "gopher://localhost:70/", "ftp://localhost/x"} {
		resp, err := cl.Get(u)
		if err == nil {
			_ = resp.Body.Close()
			t.Errorf("%s was sent", u)
			continue
		}
		// Refused by the gate itself (an egress refusal that counts for
		// nothing), not by whatever the transport underneath would say.
		if !refusedFor(err, providerhttp.ReasonEgress) {
			t.Errorf("%s: err = %v, want an egress refusal", u, err)
		}
	}
}

// TestRedirectChainIsCapped: a client that follows redirects stops after five
// requests (the literal, so a changed constant fails here).
func TestRedirectChainIsCapped(t *testing.T) {
	r := newRegistry(t, newClock())
	var srv *httptest.Server
	var hits atomic.Int64
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := hits.Add(1)
		http.Redirect(w, req, fmt.Sprintf("%s/hop/%d", srv.URL, n), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := r.Client(batcave, providerhttp.Policy{Limits: limits(1000, 1000), Private: anyPrivate})
	resp, err := c.Client().Get(srv.URL + "/start")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("an endless redirect chain was followed to its end")
	}
	if got := hits.Load(); got != 5 {
		t.Errorf("the server saw %d requests, want a chain of 5 (the original safehttp cap)", got)
	}
}

// TestDialerAddressTable pins the dial guard's table through Dialer: which
// ranges a private-trusted dial may reach, and which are refused whatever the
// trust.
func TestDialerAddressTable(t *testing.T) {
	cases := []struct {
		ip      string
		trusted bool // admitted by a private-trusted dial
		public  bool // admitted by an untrusted dial
	}{
		{"192.0.2.10", true, true},
		{"203.0.113.9", true, true},
		{"2001:4860:4860::8888", true, true},
		{"127.0.0.1", true, false},
		{"::1", true, false},
		{"::ffff:127.0.0.1", true, false}, // IPv4-mapped loopback
		{"10.1.2.3", true, false},
		{"172.31.255.254", true, false},
		{"192.168.1.1", true, false},
		{"fd00::1", true, false}, // RFC 4193 ULA
		{"100.64.0.1", true, false},
		{"0.0.0.0", true, false},
		{"::", true, false},
		{"169.254.169.254", false, false}, // cloud metadata
		{"169.254.0.1", false, false},
		{"fe80::1", false, false},
		{"::ffff:169.254.169.254", false, false}, // IPv4-mapped metadata
		{"224.0.0.1", false, false},
		{"ff02::1", false, false},
		{"0.1.2.3", false, false},
		{"240.0.0.1", false, false},
		{"198.18.0.1", false, false},
		{"64:ff9b::7f00:1", false, false}, // NAT64 embedding loopback
		{"2002:7f00:1::1", false, false},  // 6to4 embedding loopback
		{"2001:db8::1", false, false},
	}
	for _, tc := range cases {
		for _, trusted := range []bool{true, false} {
			var dialed string
			d := &providerhttp.Dialer{AllowPrivate: trusted,
				LookupIP: func(context.Context, string) ([]net.IP, error) { return ips(tc.ip), nil },
				DialAddr: func(_ context.Context, _, addr string) (net.Conn, error) {
					dialed = addr
					return nil, errors.New("test: not connecting")
				}}
			_, err := d.DialContext(context.Background(), "tcp", "host.example.test:80")
			admitted := dialed != ""
			want := tc.public
			if trusted {
				want = tc.trusted
			}
			if admitted != want {
				t.Errorf("%s trusted=%v: admitted=%v want %v (err %v)", tc.ip, trusted, admitted, want, err)
			}
			if !admitted && !errors.Is(err, providerhttp.ErrBlockedAddress) {
				t.Errorf("%s trusted=%v: refusal %v is not ErrBlockedAddress", tc.ip, trusted, err)
			}
		}
	}
	// No answer at all fails closed.
	d := &providerhttp.Dialer{LookupIP: func(context.Context, string) ([]net.IP, error) { return nil, nil }}
	if _, err := d.DialContext(context.Background(), "tcp", "host.example.test:80"); !errors.Is(err, providerhttp.ErrBlockedAddress) {
		t.Fatalf("no answer = %v", err)
	}
}
