// file: core/integrations/notify/mail/email_egress_test.go
// version: 1.1.0
// guid: 2f6a9b13-d570-4c8e-8a41-b93e0c7d5a26
// last-edited: 2026-11-19

package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/internal/statefs"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// These tests drive the real Descriptor (or its test variant) through a real
// integrations.Registry over the real outbound layer (providerhttp), so the
// whole chain F7-09 wires is under test: the Registry's gate wrapper, the
// gate's guarded dial and the egress guard. The network is replaced at the two
// seams providerhttp offers for it: the resolver (a script) and the dial of the
// checked literal address (recorded, and connected to the fake server).

const (
	egressSecretsOwner storage.Owner = "core/secrets"
	egressAuditOwner   storage.Owner = "core/audit"
)

var egressNoon = time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)

// netScript is the resolver and the dial seam of one test.
type netScript struct {
	mu      sync.Mutex
	answers func(call int, host string) ([]net.IP, error)
	lookups []string
	dialed  []string
	real    string // the fake server's address; every checked dial connects to it
}

func (n *netScript) resolve(_ context.Context, host string) ([]net.IP, error) {
	n.mu.Lock()
	n.lookups = append(n.lookups, host)
	call := len(n.lookups)
	n.mu.Unlock()
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if n.answers == nil {
		return nil, errors.New("no answer scripted")
	}
	return n.answers(call, host)
}

func (n *netScript) dial(ctx context.Context, network, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, n.real)
}

// dialRecording wraps dial so the checked literal is recorded.
func (n *netScript) dialRecording(ctx context.Context, network, addr string) (net.Conn, error) {
	n.mu.Lock()
	n.dialed = append(n.dialed, addr)
	n.mu.Unlock()
	return n.dial(ctx, network, addr)
}

func (n *netScript) dials() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.dialed)
}

func (n *netScript) lookupCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.lookups)
}

func answering(ips ...string) func(int, string) ([]net.IP, error) {
	return func(int, string) ([]net.IP, error) {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

type egressRig struct {
	t   *testing.T
	reg *integrations.Registry
	net *netScript
	f   *fakeSMTP
}

type egressOpts struct {
	answers    func(int, string) ([]net.IP, error)
	lanAllow   []string
	noOutbound bool
	// trustFake makes the sender verify the fake server's certificate (the
	// hosts these tests name are not loopback, so TLS is required). The sender
	// names ServerName "localhost" unless noServerName is set, which leaves it
	// to default to the instance's host.
	trustFake    bool
	noServerName bool
	// seed rows are imported (as a cutover does, with the trust fields they
	// are given) before the Registry loads.
	seed []integrations.StoredInstance
}

func newEgressRig(t *testing.T, o egressOpts) *egressRig {
	t.Helper()
	ctx := context.Background()
	f := &fakeSMTP{offerTLS: true}
	port, pool := startFake(t, f)
	script := &netScript{answers: o.answers, real: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}

	st, err := storage.Open(ctx, storage.OpenOptions{
		Path: "/batcave/mail.pebble",
		Owners: []storage.OwnerSpec{
			{Owner: instancestore.Owner, Spec: instancestore.StorageSpec(), Enabled: true},
			{Owner: egressSecretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: egressAuditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := st.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = st.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks := func(o storage.Owner) storage.Keyspace {
		k, ok := db.Keyspace(o)
		if !ok {
			t.Fatalf("no keyspace for %s", o)
		}
		return k
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := func() time.Time { return egressNoon }
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: statefs.FromRoot(root), Keyspace: ks(egressSecretsOwner), AppID: "gotham", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	aks := ks(egressAuditOwner)
	au, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	ph, err := providerhttp.New(providerhttp.Options{
		Now: now, Resolver: script.resolve, DialAddr: script.dialRecording,
		LANAllow: func() []string { return o.lanAllow },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ph.Close)
	iks := ks(instancestore.Owner)
	opts := integrations.Options{
		Store: integrations.NativeInstanceStore(iks), NewBatch: iks.NewBatch, Secrets: sec, Audit: au,
		Outbound: ph.Outbound, Now: now,
	}
	if o.noOutbound {
		opts.Outbound = nil
	}
	reg, err := integrations.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.seed) > 0 {
		im := integrations.NativeImporter(iks, integrations.ImportAudit(au))
		b := iks.NewBatch()
		for _, row := range o.seed {
			if err := im.ImportInstance(ctx, b, row); err != nil {
				t.Fatalf("import: %v", err)
			}
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	if o.trustFake {
		// The same adapter, with the sender holding the fake's root pool and
		// naming the certificate's name. Host and Port stay the instance's:
		// they are what the guard judges. Dial is whatever the channel set.
		d = notify.Adapter(d.Meta(), buildWith(func(cfg SMTPConfig) (Sender, error) {
			cfg.TLSConfig = &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}
			if o.noServerName {
				cfg.TLSConfig.ServerName = ""
			}
			return NewSMTPSender(cfg)
		}))
	}
	if err := reg.Register("core", d); err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reg.Close(cctx)
	})
	return &egressRig{t: t, reg: reg, net: script, f: f}
}

func egressAdmin() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
}

// put stores an email instance for host (and port, when non-zero).
func (r *egressRig) put(local bool, host string, port int) {
	r.t.Helper()
	s := map[string]any{"host": host, "from": "Gotham Alerts <alerts@example.com>", "to": []any{"jim@example.com"}}
	if port != 0 {
		s["port"] = float64(port)
	}
	if _, err := r.reg.Put(egressAdmin(), integrations.InstanceConfig{
		Ref: batphone, Adapter: AdapterID, Name: "commissioner", Enabled: true, Local: local, Settings: s,
	}, nil); err != nil {
		r.t.Fatalf("Put: %v", err)
	}
}

func (r *egressRig) send() error {
	r.t.Helper()
	nr, release, err := integrations.Lease[notify.Notifier](context.Background(), r.reg, batphone)
	if err != nil {
		r.t.Fatalf("Lease: %v", err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return nr.Send(ctx, signalNote, notify.Target{Instance: batphone})
}

// refused asserts a send was refused by the egress guard before any
// connection, with a refusal that names neither the host nor an address.
func (r *egressRig) refused(err error, names ...string) {
	r.t.Helper()
	if !errors.Is(err, providerhttp.ErrBlockedAddress) {
		r.t.Fatalf("err = %v, want the egress guard's refusal", err)
	}
	if why, ok := providerhttp.ReasonOf(err); !ok || why != providerhttp.ReasonEgress {
		r.t.Fatalf("reason = %v, want egress", why)
	}
	if c := integrations.ClassOf(context.Background(), err); c != integrations.ClassRefusedLocal {
		r.t.Fatalf("class = %v, want refused_local", c)
	}
	if d := r.net.dials(); len(d) != 0 {
		r.t.Fatalf("a connection was attempted: %v", d)
	}
	if n := r.f.accepted.Load(); n != 0 {
		r.t.Fatalf("the server accepted %d connections", n)
	}
	for _, n := range names {
		if strings.Contains(err.Error(), n) {
			r.t.Errorf("the refusal names %q: %v", n, err)
		}
	}
}

func (r *egressRig) delivered(err error) {
	r.t.Helper()
	if err != nil {
		r.t.Fatalf("Send: %v", err)
	}
	r.f.wait(r.t)
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if !strings.Contains(r.f.rcptTo, "jim@example.com") {
		r.t.Fatalf("the server took RCPT %q, want the message to jim", r.f.rcptTo)
	}
}

// TestEmail_PrivateHostRefusedWithoutLocal is the F7-05 acceptance for the
// SMTP dial guard: an email instance whose host is a private address, without
// Local, is refused before any connection (TODO-F7-05-SMTP-DIAL-GUARD).
func TestEmail_PrivateHostRefusedWithoutLocal(t *testing.T) {
	r := newEgressRig(t, egressOpts{})
	r.put(false, "127.0.0.1", 2525)
	r.refused(r.send(), "127.0.0.1", "2525")
}

// With Local, the same instance reaches its own host (the loopback relay of
// an operator's own machine).
func TestEmail_LocalInstanceReachesItsOwnPrivateHost(t *testing.T) {
	r := newEgressRig(t, egressOpts{})
	r.put(true, "127.0.0.1", 2525)
	r.delivered(r.send())
	if d := r.net.dials(); !slices.Equal(d, []string{"127.0.0.1:2525"}) {
		t.Fatalf("dialled %v", d)
	}
}

// Every private class a hostname can resolve to is refused for an instance
// that is not Local, with the hostname in no error text.
func TestEmail_PrivateAnswersRefusedWithoutLocal(t *testing.T) {
	for name, ips := range map[string][]string{
		"loopback":                   {"127.0.0.1"},
		"loopback v6":                {"::1"},
		"RFC 1918 10/8":              {"10.1.2.3"},
		"RFC 1918 192.168/16":        {"192.168.1.1"},
		"RFC 1918 172.16/12 (top)":   {"172.31.255.254"},
		"unique local":               {"fc00::1"},
		"CGNAT":                      {"100.64.0.1"},
		"link-local (metadata)":      {"169.254.169.254"},
		"public first, then private": {"192.0.2.10", "10.1.2.3"},
		"no addresses":               {},
	} {
		t.Run(name, func(t *testing.T) {
			r := newEgressRig(t, egressOpts{answers: answering(ips...), trustFake: true})
			r.put(false, "mail.gotham.test", 587)
			r.refused(r.send(), "mail.gotham.test", "587")
		})
	}
}

// A lookup that fails is an error and a refusal to connect: nothing is dialled
// by name.
func TestEmail_ResolverErrorFailsClosed(t *testing.T) {
	boom := errors.New("lookup failed")
	r := newEgressRig(t, egressOpts{answers: func(int, string) ([]net.IP, error) { return nil, boom }, trustFake: true})
	r.put(false, "mail.gotham.test", 587)
	err := r.send()
	if err == nil {
		t.Fatal("sent although the host did not resolve")
	}
	if d := r.net.dials(); len(d) != 0 || r.f.accepted.Load() != 0 {
		t.Fatalf("connected after a failed lookup: %v", d)
	}
	if strings.Contains(err.Error(), "mail.gotham.test") {
		t.Errorf("the error names the host: %v", err)
	}
	if c := integrations.ClassOf(context.Background(), err); c == integrations.ClassRefusedLocal {
		t.Errorf("a failed lookup is a transient failure, not a verdict on the destination: class %v", c)
	}
}

// A public host is reached, and the connection goes to the address that was
// checked: one lookup, then a dial of the literal. A zone that answers a
// second lookup with loopback (DNS rebinding) is never asked.
func TestEmail_PublicHostDialsTheCheckedLiteralOnce(t *testing.T) {
	r := newEgressRig(t, egressOpts{trustFake: true, answers: func(call int, _ string) ([]net.IP, error) {
		if call == 1 {
			return []net.IP{net.ParseIP("192.0.2.10")}, nil
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}})
	r.put(false, "mail.gotham.test", 587)
	r.delivered(r.send())
	if d := r.net.dials(); !slices.Equal(d, []string{"192.0.2.10:587"}) {
		t.Fatalf("dialled %v, want the checked literal", d)
	}
	if n := r.net.lookupCount(); n != 1 {
		t.Fatalf("%d lookups, want 1", n)
	}
}

// An unset port is the submission port: an instance that is Local for its host
// with no port set is judged at host:587 and reaches it.
func TestEmail_LocalWithUnsetPortReachesSubmissionPort(t *testing.T) {
	r := newEgressRig(t, egressOpts{answers: answering("10.1.2.3"), trustFake: true})
	r.put(true, "mail.gotham.test", 0)
	r.delivered(r.send())
	if d := r.net.dials(); !slices.Equal(d, []string{"10.1.2.3:587"}) {
		t.Fatalf("dialled %v", d)
	}
}

// Local is bound to the instance's own host and port, and never reaches
// link-local space.
func TestEmail_LocalIsBoundToItsDestination(t *testing.T) {
	t.Run("link-local is refused for a Local instance", func(t *testing.T) {
		r := newEgressRig(t, egressOpts{answers: answering("169.254.169.254"), trustFake: true})
		r.put(true, "mail.gotham.test", 587)
		r.refused(r.send(), "mail.gotham.test")
	})
}

// Local is bound to the destination it was granted for. A stored row that is
// still Local but whose grant is for another host (the host moved afterwards)
// is refused at the new host; the same row granted for its own host is admitted.
func TestEmail_LocalDoesNotFollowAMovedHost(t *testing.T) {
	row := func(host, grantedFor string) integrations.StoredInstance {
		return integrations.StoredInstance{
			Config: integrations.InstanceConfig{
				Ref: batphone, Adapter: AdapterID, Name: "commissioner", Enabled: true, Local: true,
				Origin: integrations.OriginUI,
				Settings: map[string]any{
					"host": host, "port": float64(587),
					"from": "Gotham Alerts <alerts@example.com>", "to": []any{"jim@example.com"},
				},
			},
			LocalOrigin: "host=smtp://" + grantedFor + ":587",
		}
	}
	t.Run("granted for the old host: refused at the new one", func(t *testing.T) {
		r := newEgressRig(t, egressOpts{answers: answering("10.1.2.3"), trustFake: true,
			seed: []integrations.StoredInstance{row("relay.gotham.test", "mail.gotham.test")}})
		if cur, ok := r.reg.Config(batphone); !ok || !cur.Local {
			t.Fatalf("the seeded row is not Local: %+v (found %v)", cur, ok)
		}
		r.refused(r.send(), "relay.gotham.test")
	})
	t.Run("granted for this host: admitted", func(t *testing.T) {
		r := newEgressRig(t, egressOpts{answers: answering("10.1.2.3"), trustFake: true,
			seed: []integrations.StoredInstance{row("relay.gotham.test", "relay.gotham.test")}})
		r.delivered(r.send())
		if d := r.net.dials(); !slices.Equal(d, []string{"10.1.2.3:587"}) {
			t.Fatalf("dialled %v", d)
		}
	})
}

// The operator's lan_allow admits a private host for an instance that is not
// Local.
func TestEmail_LANAllowAdmitsAPrivateHost(t *testing.T) {
	r := newEgressRig(t, egressOpts{answers: answering("10.1.2.3"), lanAllow: []string{"mail.gotham.test"}, trustFake: true})
	r.put(false, "mail.gotham.test", 587)
	r.delivered(r.send())
}

// A Registry built without an outbound layer has no egress policy: the send is
// refused at Acquire (the gate refuses admission before the sender is built or
// the dial made), so nothing connects. The dial's own refusal in that case is
// pinned by TestGuardedDial_NoOutboundRefuses in the integrations package.
func TestEmail_NoOutboundLayerRefusesAtAcquire(t *testing.T) {
	r := newEgressRig(t, egressOpts{answers: answering("192.0.2.10"), noOutbound: true, trustFake: true})
	r.put(false, "mail.gotham.test", 587)
	err := r.send()
	if err == nil || integrations.ClassOf(context.Background(), err) != integrations.ClassRefusedLocal {
		t.Fatalf("err = %v, want a local refusal", err)
	}
	if r.f.accepted.Load() != 0 || len(r.net.dials()) != 0 {
		t.Fatal("connected without an outbound layer")
	}
}

// After a literal dial the certificate is still checked against the
// instance's host: the dialled address never becomes the TLS server name. The
// fake's certificate names mail.gotham.test (and localhost), not 192.0.2.10, so
// a server name taken from the dial would fail verification.
func TestEmail_TLSNameIsTheInstanceHostAfterALiteralDial(t *testing.T) {
	r := newEgressRig(t, egressOpts{answers: answering("192.0.2.10"), trustFake: true, noServerName: true})
	r.put(false, "mail.gotham.test", 587)
	r.delivered(r.send())
	if d := r.net.dials(); !slices.Equal(d, []string{"192.0.2.10:587"}) {
		t.Fatalf("dialled %v, want the checked literal", d)
	}
	r.f.mu.Lock()
	tlsUsed := r.f.startTLSUsed
	r.f.mu.Unlock()
	if !tlsUsed {
		t.Fatal("the message was not sent over STARTTLS, so no certificate was checked")
	}
}

// An IPv6 literal host is judged and dialled in its bracketed form, and the
// instance's Local binds to that same form: without Local it is refused, with
// Local the guard admits the dial of "[fd00::1]:587" (the send may fail later on
// the fake's certificate, which is not the guard's business).
func TestEmail_IPv6LiteralHost(t *testing.T) {
	t.Run("unique local without Local is refused", func(t *testing.T) {
		r := newEgressRig(t, egressOpts{trustFake: true})
		r.put(false, "fd00::1", 587)
		r.refused(r.send())
	})
	t.Run("Local binds to the bracketed literal", func(t *testing.T) {
		r := newEgressRig(t, egressOpts{trustFake: true})
		r.put(true, "fd00::1", 587)
		_ = r.send()
		if d := r.net.dials(); !slices.Equal(d, []string{"[fd00::1]:587"}) {
			t.Fatalf("dialled %v, want [[fd00::1]:587]", d)
		}
	})
}
