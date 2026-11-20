// file: core/integrations/providerhttp/egress_dial_test.go
// version: 1.0.0
// guid: 4e8f1c27-6b93-4d05-a1e2-7c30d95b8a64
// last-edited: 2026-11-19

package providerhttp_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// Tests of the dial a non-HTTP adapter (core.email's SMTP sender) uses: the
// gate's integrations.GuardedDialer. It makes the same decision the HTTP
// transport's pick makes (the instance's Trust or lan_allow chooses the
// private-capable guard, anything else the public-only one), so these tests
// are about the SMTP-shaped inputs: an origin with an unset port, a dial with
// no URL, and what is dialled after the check.

// smtpRec is a Resolver and a DialAddr that touch no network: it answers a name
// with what a test scripts, records each lookup, and records the literal
// "ip:port" the guard handed to the dial, answering with one end of a pipe.
type smtpRec struct {
	mu      sync.Mutex
	answers func(call int, host string) ([]net.IP, error)
	lookups []string
	dialed  []string
}

func (s *smtpRec) resolve(_ context.Context, host string) ([]net.IP, error) {
	s.mu.Lock()
	s.lookups = append(s.lookups, host)
	n := len(s.lookups)
	s.mu.Unlock()
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if s.answers == nil {
		return nil, errors.New("no answer scripted")
	}
	return s.answers(n, host)
}

func (s *smtpRec) dial(_ context.Context, _, addr string) (net.Conn, error) {
	s.mu.Lock()
	s.dialed = append(s.dialed, addr)
	s.mu.Unlock()
	a, b := net.Pipe()
	go func() { _ = b.Close() }()
	return a, nil
}

func (s *smtpRec) dials() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dialed)
}

func (s *smtpRec) lookupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lookups)
}

func (s *smtpRec) options(o *providerhttp.Options) { o.Resolver, o.DialAddr = s.resolve, s.dial }

// answer scripts one fixed answer for every name.
func answer(ips ...string) func(int, string) ([]net.IP, error) {
	return func(int, string) ([]net.IP, error) { return parseIPs(ips), nil }
}

func parseIPs(ss []string) []net.IP {
	var out []net.IP
	for _, x := range ss {
		out = append(out, net.ParseIP(x))
	}
	return out
}

// smtpDialer is the dial of an instance whose Trust is trust, over a registry
// with no lan_allow unless the test adds one.
func smtpDialer(t *testing.T, rec *smtpRec, spec integrations.OutboundSpec, mut ...func(*providerhttp.Options)) integrations.GuardedDialer {
	t.Helper()
	r := newRegistry(t, newClock(), append([]func(*providerhttp.Options){noLAN, rec.options}, mut...)...)
	spec.Ref = batcave
	if spec.Limits.RPS == 0 {
		spec.Limits = limits(100, 100)
	}
	_, g := r.Outbound(spec)
	d, ok := g.(integrations.GuardedDialer)
	if !ok {
		t.Fatalf("the gate %T offers no guarded dial", g)
	}
	return d
}

func trustOf(origins ...string) integrations.OutboundSpec {
	return integrations.OutboundSpec{Egress: integrations.EgressConfigured, Trust: integrations.Trust{PrivateOrigins: origins}}
}

func refusedByGuard(err error) bool {
	return errors.Is(err, providerhttp.ErrBlockedAddress) && refusedFor(err, providerhttp.ReasonEgress)
}

// TestGuardedDial_RefusesEveryPrivateClassWithoutTrust: a host that resolves to
// any non-public address is refused before a connection, whatever the answer
// shape.
func TestGuardedDial_RefusesEveryPrivateClassWithoutTrust(t *testing.T) {
	for name, ips := range map[string][]string{
		"loopback":                   {"127.0.0.1"},
		"loopback v6":                {"::1"},
		"loopback mapped":            {"::ffff:127.0.0.1"},
		"RFC 1918 10/8":              {"10.1.2.3"},
		"RFC 1918 192.168/16":        {"192.168.1.1"},
		"RFC 1918 top of 172.16/12":  {"172.31.255.254"},
		"unique local":               {"fc00::1"},
		"CGNAT":                      {"100.64.0.1"},
		"unspecified":                {"0.0.0.0"},
		"link-local (metadata)":      {"169.254.169.254"},
		"link-local v6":              {"fe80::1"},
		"multicast":                  {"224.0.0.1"},
		"public first, private next": {"192.0.2.10", "10.1.2.3"},
		"private first, public next": {"10.1.2.3", "192.0.2.10"},
		"public then loopback":       {"192.0.2.10", "127.0.0.1"},
		"no addresses":               {},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &smtpRec{answers: answer(ips...)}
			d := smtpDialer(t, rec, trustOf())
			conn, err := d.DialContext(context.Background(), "tcp", "mail.gotham.test:587")
			if conn != nil {
				_ = conn.Close()
			}
			if !refusedByGuard(err) {
				t.Fatalf("err = %v, want an egress refusal", err)
			}
			if got := rec.dials(); len(got) != 0 {
				t.Fatalf("a connection was attempted: %v", got)
			}
			if strings.Contains(err.Error(), "mail.gotham.test") || strings.Contains(err.Error(), "10.1.2.3") {
				t.Errorf("the refusal names the host or the address: %v", err)
			}
		})
	}
}

// TestGuardedDial_ResolverErrorFailsClosed: a lookup that fails is an error,
// never a dial of the name and never an admit. It is not an egress refusal (the
// destination was never judged): a retry resolves again.
func TestGuardedDial_ResolverErrorFailsClosed(t *testing.T) {
	boom := errors.New("lookup mail.gotham.test: server misbehaving")
	rec := &smtpRec{answers: func(int, string) ([]net.IP, error) { return nil, boom }}
	d := smtpDialer(t, rec, trustOf("smtp://mail.gotham.test:587"))
	conn, err := d.DialContext(context.Background(), "tcp", "mail.gotham.test:587")
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the resolver's error", err)
	}
	if got := rec.dials(); len(got) != 0 {
		t.Fatalf("a connection was attempted after a failed lookup: %v", got)
	}
}

// TestGuardedDial_MalformedAddressRefused: an address the dial cannot split is
// an error before any lookup or connection.
func TestGuardedDial_MalformedAddressRefused(t *testing.T) {
	for _, addr := range []string{"mail.gotham.test", "", ":587", "mail.gotham.test:"} {
		rec := &smtpRec{answers: answer("192.0.2.10")}
		d := smtpDialer(t, rec, trustOf())
		conn, err := d.DialContext(context.Background(), "tcp", addr)
		if conn != nil {
			_ = conn.Close()
		}
		if err == nil {
			t.Errorf("%q: dialled", addr)
		}
		if rec.lookupCount() != 0 || len(rec.dials()) != 0 {
			t.Errorf("%q: looked up or dialled (%d lookups, %v)", addr, rec.lookupCount(), rec.dials())
		}
	}
}

// TestGuardedDial_PublicAddressDialsTheCheckedLiteral: a public answer is
// dialled, as the literal address that was checked, after exactly one lookup
// (the DNS-rebinding window of a resolve-then-dial-by-name dial stays shut).
func TestGuardedDial_PublicAddressDialsTheCheckedLiteral(t *testing.T) {
	rec := &smtpRec{answers: func(call int, _ string) ([]net.IP, error) {
		if call == 1 {
			return parseIPs([]string{"192.0.2.10"}), nil
		}
		return parseIPs([]string{"127.0.0.1"}), nil // the zone's second answer
	}}
	d := smtpDialer(t, rec, trustOf())
	conn, err := d.DialContext(context.Background(), "tcp", "mail.gotham.test:587")
	if err != nil {
		t.Fatalf("a public host was refused: %v", err)
	}
	_ = conn.Close()
	if got := rec.dials(); !slices.Equal(got, []string{"192.0.2.10:587"}) {
		t.Fatalf("dialled %v, want the checked literal 192.0.2.10:587", got)
	}
	if n := rec.lookupCount(); n != 1 {
		t.Fatalf("%d lookups, want 1: a second lookup is the rebinding window", n)
	}
}

// TestGuardedDial_LocalIsBoundToItsOwnHostAndPort: Trust admits the private
// address of the destination it names and nothing else. An origin with an unset
// port is the submission port the sender dials (587).
func TestGuardedDial_LocalIsBoundToItsOwnHostAndPort(t *testing.T) {
	for _, c := range []struct {
		name   string
		origin string
		addr   string
		ip     string
		want   bool
	}{
		{"its own host and port", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "10.1.2.3", true},
		{"unset port stands for 587", "smtp://mail.gotham.test:", "mail.gotham.test:587", "10.1.2.3", true},
		{"unset port, another port", "smtp://mail.gotham.test:", "mail.gotham.test:25", "10.1.2.3", false},
		{"origin in upper case", "smtp://MAIL.Gotham.Test:2525", "mail.gotham.test:2525", "10.1.2.3", true},
		{"address in upper case", "smtp://mail.gotham.test:2525", "MAIL.GOTHAM.TEST:2525", "10.1.2.3", true},
		{"another port", "smtp://mail.gotham.test:2525", "mail.gotham.test:2526", "10.1.2.3", false},
		{"another host", "smtp://mail.gotham.test:2525", "relay.gotham.test:2525", "10.1.2.3", false},
		{"a literal origin", "smtp://10.1.2.3:2525", "10.1.2.3:2525", "10.1.2.3", true},
		{"a literal, another port", "smtp://10.1.2.3:2525", "10.1.2.3:25", "10.1.2.3", false},
		{"loopback", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "127.0.0.1", true},
		{"unique local", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "fc00::1", true},
		{"CGNAT", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "100.64.0.1", true},
		// Whatever the trust: link-local and the metadata endpoints.
		{"link-local", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "169.254.169.254", false},
		{"link-local v6", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "fe80::1", false},
		{"Alibaba metadata in CGNAT", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "100.100.100.200", false},
		{"AWS v6 metadata in unique local", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "fd00:ec2::254", false},
		{"multicast", "smtp://mail.gotham.test:2525", "mail.gotham.test:2525", "224.0.0.1", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &smtpRec{answers: answer(c.ip)}
			d := smtpDialer(t, rec, trustOf(c.origin))
			conn, err := d.DialContext(context.Background(), "tcp", c.addr)
			if conn != nil {
				_ = conn.Close()
			}
			if c.want && err != nil {
				t.Fatalf("a trusted private destination was refused: %v", err)
			}
			if !c.want && !refusedByGuard(err) {
				t.Fatalf("err = %v, want an egress refusal", err)
			}
			if !c.want && len(rec.dials()) != 0 {
				t.Fatalf("a connection was attempted: %v", rec.dials())
			}
		})
	}
}

// TestGuardedDial_LANAllowAdmitsWithoutLocal: the operator's lan_allow admits
// an instance with no Trust: a host, a host:port, a literal-IP CIDR. A CIDR
// does not admit a name that resolves into it, and lan_allow never admits a
// link-local address.
func TestGuardedDial_LANAllowAdmitsWithoutLocal(t *testing.T) {
	allow := func(entries ...string) func(*providerhttp.Options) {
		return func(o *providerhttp.Options) { o.LANAllow = func() []string { return entries } }
	}
	for _, c := range []struct {
		name  string
		allow []string
		addr  string
		ip    string
		want  bool
	}{
		{"a host, any port", []string{"mail.gotham.test"}, "mail.gotham.test:587", "10.1.2.3", true},
		{"a host:port", []string{"mail.gotham.test:587"}, "mail.gotham.test:587", "10.1.2.3", true},
		{"a host:port, another port", []string{"mail.gotham.test:587"}, "mail.gotham.test:25", "10.1.2.3", false},
		{"a literal inside a CIDR", []string{"10.0.0.0/8"}, "10.1.2.3:587", "10.1.2.3", true},
		{"a name does not use a CIDR", []string{"10.0.0.0/8"}, "mail.gotham.test:587", "10.1.2.3", false},
		{"another host", []string{"relay.gotham.test"}, "mail.gotham.test:587", "10.1.2.3", false},
		{"link-local is never admitted", []string{"169.254.0.0/16", "mail.gotham.test"}, "mail.gotham.test:587", "169.254.169.254", false},
		{"empty list", nil, "mail.gotham.test:587", "10.1.2.3", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &smtpRec{answers: answer(c.ip)}
			d := smtpDialer(t, rec, trustOf(), allow(c.allow...))
			conn, err := d.DialContext(context.Background(), "tcp", c.addr)
			if conn != nil {
				_ = conn.Close()
			}
			if c.want && err != nil {
				t.Fatalf("lan_allow did not admit: %v", err)
			}
			if !c.want && !refusedByGuard(err) {
				t.Fatalf("err = %v, want an egress refusal", err)
			}
		})
	}
}

// TestGuardedDial_PublicOnlyIgnoresTrust: an adapter whose policy is
// EgressPublicOnly never reaches a private address, whatever Trust says.
func TestGuardedDial_PublicOnlyIgnoresTrust(t *testing.T) {
	rec := &smtpRec{answers: answer("10.1.2.3")}
	spec := trustOf("smtp://mail.gotham.test:587")
	spec.Egress = integrations.EgressPublicOnly
	d := smtpDialer(t, rec, spec, func(o *providerhttp.Options) { o.LANAllow = func() []string { return []string{"mail.gotham.test"} } })
	conn, err := d.DialContext(context.Background(), "tcp", "mail.gotham.test:587")
	if conn != nil {
		_ = conn.Close()
	}
	if !refusedByGuard(err) {
		t.Fatalf("err = %v, want an egress refusal", err)
	}
}

// TestGuardedDial_SameHostOrPublicNeedsAConfiguredOrigin: under
// EgressSameHostOrPublic lan_allow alone does not admit a host the instance
// is not configured for.
func TestGuardedDial_SameHostOrPublicNeedsAConfiguredOrigin(t *testing.T) {
	rec := &smtpRec{answers: answer("10.1.2.3")}
	spec := integrations.OutboundSpec{Egress: integrations.EgressSameHostOrPublic}
	d := smtpDialer(t, rec, spec, func(o *providerhttp.Options) { o.LANAllow = func() []string { return []string{"mail.gotham.test"} } })
	conn, err := d.DialContext(context.Background(), "tcp", "mail.gotham.test:587")
	if conn != nil {
		_ = conn.Close()
	}
	if !refusedByGuard(err) {
		t.Fatalf("err = %v, want an egress refusal", err)
	}
}

// TestGuardedDial_DisabledRefusesBeforeAnyLookup: the global switch stops the
// dial before a lookup, with the refusal the gate's Acquire gives.
func TestGuardedDial_DisabledRefusesBeforeAnyLookup(t *testing.T) {
	rec := &smtpRec{answers: answer("192.0.2.10")}
	d := smtpDialer(t, rec, trustOf(), func(o *providerhttp.Options) { o.Disabled = func() bool { return true } })
	conn, err := d.DialContext(context.Background(), "tcp", "mail.gotham.test:587")
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, integrations.ErrDisabled) || !refusedFor(err, providerhttp.ReasonDisabled) {
		t.Fatalf("err = %v, want a disabled refusal", err)
	}
	if rec.lookupCount() != 0 || len(rec.dials()) != 0 {
		t.Fatalf("looked up or dialled while disabled: %d lookups, %v", rec.lookupCount(), rec.dials())
	}
}

// TestGuardedDial_CanceledContext: a context that is already done dials
// nothing.
func TestGuardedDial_CanceledContext(t *testing.T) {
	rec := &smtpRec{answers: answer("192.0.2.10")}
	d := smtpDialer(t, rec, trustOf())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, err := d.DialContext(ctx, "tcp", "mail.gotham.test:587")
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(rec.dials()) != 0 {
		t.Fatalf("dialled with a canceled context: %v", rec.dials())
	}
}

// TestGuardedDial_RefusalDoesNotFeedTheBreakerOrAHold: a guard refusal is a
// local verdict on the destination, not evidence about the remote: a run of
// them opens no breaker and places no hold, so the instance recovers the
// moment its destination or its trust is fixed.
func TestGuardedDial_RefusalDoesNotFeedTheBreakerOrAHold(t *testing.T) {
	rec := &smtpRec{answers: answer("10.1.2.3")}
	lim := limits(1000, 1000)
	lim.Breaker = integrations.BreakerPolicy{Threshold: 1, Cooldown: 1 << 40}
	spec := trustOf()
	spec.Limits = lim
	r := newRegistry(t, newClock(), noLAN, rec.options)
	spec.Ref = batcave
	_, g := r.Outbound(spec)
	d := g.(integrations.GuardedDialer)
	for range 5 {
		tk, err := g.Acquire(context.Background(), "")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		conn, derr := d.DialContext(context.Background(), "tcp", "mail.gotham.test:587")
		if conn != nil {
			_ = conn.Close()
		}
		if !refusedByGuard(derr) {
			t.Fatalf("dial err = %v", derr)
		}
		tk.Done(derr)
	}
	if st := g.State(); st.Breaker != integrations.BreakerClosed || st.Hold != nil {
		t.Fatalf("the gate is %v with hold %v after five guard refusals", st.Breaker, st.Hold)
	}
}
