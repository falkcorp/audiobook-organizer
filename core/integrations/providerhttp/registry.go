// file: core/integrations/providerhttp/registry.go
// version: 1.0.0
// guid: 76674dec-435f-4e2d-a9aa-515d33dd8a4c
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// Registry is per-App, never a package global (R5). It owns the gate state of
// every Scope, the hold table and the two guarded transports every request
// leaves through.
type Registry struct {
	o     Options
	log   *slog.Logger
	now   func() time.Time
	store GateStore
	met   *instruments

	// strict dials public addresses only; lan also dials loopback and private
	// ones. Which one a request uses is decided per request from the instance's
	// Policy (transport.go), so the two connection pools never mix: a
	// connection dialled under private trust is never reused by an untrusted
	// request.
	strict, lan *http.Transport

	mu       sync.Mutex
	scopes   map[string]*scopeState
	refScope map[integrations.Ref]string

	hmu    sync.Mutex
	holds  map[string]integrations.Hold
	warned atomic.Bool
}

// New builds a Registry, loading the persisted holds before any instance is
// built (M2.4). It does no network I/O.
func New(opts Options) (*Registry, error) {
	r := &Registry{o: opts, log: opts.Log, now: opts.Now, store: opts.Store,
		scopes: map[string]*scopeState{}, refScope: map[integrations.Ref]string{}, holds: map[string]integrations.Hold{}}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.store == nil {
		r.store = newMemStore()
	}
	var err error
	if r.met, err = instrument(opts.Metrics); err != nil {
		return nil, err
	}
	r.strict = r.newTransport(false)
	r.lan = r.newTransport(true)
	r.loadHolds()
	return r, nil
}

// newTransport clones the base transport and puts the dial guard in its dial.
func (r *Registry) newTransport(allowPrivate bool) *http.Transport {
	var t *http.Transport
	if r.o.Base != nil {
		t = r.o.Base.Clone()
	} else {
		t = http.DefaultTransport.(*http.Transport).Clone()
	}
	d := &Dialer{AllowPrivate: allowPrivate, LookupIP: r.o.Resolver, DialAddr: r.o.DialAddr}
	t.Proxy = nil // a proxy would resolve and connect on the guard's behalf
	t.DialContext = d.DialContext
	t.DialTLSContext = nil
	t.DialTLS = nil //nolint:staticcheck // cleared on purpose: a custom TLS dial would bypass the guard
	return t
}

// Close releases the idle connections of both transports.
func (r *Registry) Close() {
	r.strict.CloseIdleConnections()
	r.lan.CloseIdleConnections()
}

func (r *Registry) settings() Settings {
	if r.o.Settings == nil {
		return Settings{}.withDefaults()
	}
	return r.o.Settings().withDefaults()
}

func (r *Registry) isDisabled() bool { return r.o.Disabled != nil && r.o.Disabled() }

// sleep waits d or until ctx ends.
func (r *Registry) sleep(ctx context.Context, d time.Duration) error {
	if r.o.Sleep != nil {
		return r.o.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// jitter is the random addition to a back-off delay of d.
func (r *Registry) jitter(d time.Duration) time.Duration {
	if r.o.Jitter != nil {
		return r.o.Jitter(d)
	}
	return time.Duration(rand.Int64N(int64(d/2) + 1))
}

// gateFor returns ref's gate over the scope p names, creating the scope's
// state on first use and applying p.Limits to it in place afterwards: a
// rebuilt instance with new limits resizes the one bucket (HEAD defect 1).
func (r *Registry) gateFor(ref integrations.Ref, p Policy) *gate {
	name := p.Scope
	if name == "" {
		name = ref.String()
	}
	lim := normalizeLimits(p.Limits, r.settings())
	r.mu.Lock()
	sc := r.scopes[name]
	if sc == nil {
		sc = r.newScope(name, lim)
		r.scopes[name] = sc
	}
	r.refScope[ref] = name
	r.mu.Unlock()
	sc.setLimits(lim)
	return &gate{r: r, ref: ref, pol: p, sc: sc}
}

// GateFor returns the gate alone, for SMTP and subprocess adapters.
func (r *Registry) GateFor(ref integrations.Ref, p Policy) integrations.Gate {
	return r.gateFor(ref, p)
}

// Client returns the instance-scoped client. Every Client for the same Scope
// shares ONE bucket, ONE breaker, ONE hold and ONE quota counter set.
func (r *Registry) Client(ref integrations.Ref, p Policy) *Client {
	g := r.gateFor(ref, p)
	return &Client{gate: g, rt: &transport{g: g}}
}

// Outbound is integrations.Options.Outbound: it builds an instance's client
// and gate from the Registry's OutboundSpec (name coined by F7-02; F7-09 passes
// it to integrations.New). The limits are the adapter's with the instance's
// override on top; the Scope is the instance's; the instance may reach a
// private address only on the origins its Trust lists and on lan_allow.
func (r *Registry) Outbound(spec integrations.OutboundSpec) (integrations.HTTP, integrations.Gate) {
	c := r.Client(spec.Ref, Policy{
		Limits:  MergeLimits(spec.Limits, spec.Override),
		Egress:  spec.Egress,
		Private: r.privateFor(spec.Trust),
	})
	return c, c.Gate()
}

// privateFor is the Policy.Private of an instance whose Trust is t.
func (r *Registry) privateFor(t integrations.Trust) func(string) bool {
	bound := make(map[string]bool, len(t.PrivateOrigins))
	for _, o := range t.PrivateOrigins {
		if _, hp, ok := strings.Cut(o, "://"); ok && hp != "" {
			bound[strings.ToLower(hp)] = true
		}
	}
	return func(hostport string) bool {
		return bound[hostport] || r.lanAllowed(hostport)
	}
}

// lanAllowed reports whether hostport is on core.integrations.egress.lan_allow:
// an entry is a host (any port), a host:port, or a CIDR. A CIDR admits a
// literal-IP host only (a name that resolves into it is not admitted: the
// allow-list is read before the dial, so it cannot judge a resolved address).
func (r *Registry) lanAllowed(hostport string) bool {
	if r.o.LANAllow == nil {
		return false
	}
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	ip, ipErr := netip.ParseAddr(host)
	for _, e := range r.o.LANAllow() {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case e == hostport || e == host:
			return true
		case strings.Contains(e, "/"):
			if p, err := netip.ParsePrefix(e); err == nil && ipErr == nil && p.Contains(ip.Unmap()) {
				return true
			}
		}
	}
	return false
}
