// file: core/integrations/integrationstest/fakes.go
// version: 1.0.0
// guid: a8b1050c-6d27-4e3f-9c84-1f5a2e7b9d60
// last-edited: 2026-11-11

package integrationstest

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/rbac"
)

// ---- principals ----------------------------------------------------------

// Checker is an rbac.Checker holding exactly its permissions (no
// implication, no catalog): enough for a Registry, which asks Has only.
type Checker []rbac.Permission

// Has reports whether p is held.
func (c Checker) Has(p rbac.Permission) bool { return slices.Contains(c, p) }

// Satisfies evaluates the public, all-of and any-of requirements over Has.
func (c Checker) Satisfies(r rbac.Requirement) bool {
	switch r.Kind() {
	case rbac.KindPublic:
		return true
	case rbac.KindAllOf:
		ps := r.Permissions()
		for _, p := range ps {
			if !c.Has(p) {
				return false
			}
		}
		return len(ps) > 0
	case rbac.KindAnyOf:
		return slices.ContainsFunc(r.Permissions(), c.Has)
	}
	return false
}

// Confined is false: the checker holds no confining scope.
func (Checker) Confined() bool { return false }

// Concrete lists the held permissions.
func (c Checker) Concrete() []rbac.Permission { return slices.Clone(c) }

// The permissions a Registry checks.
const (
	Manage rbac.Permission = "core:integrations.manage"
	Admin  rbac.Permission = rbac.CoreAdmin
)

// As returns ctx carrying a principal authenticated by m and holding
// perms. Every method but MethodAuthDisabled gets a user, Bruce Wayne.
func As(ctx context.Context, m auth.Method, perms ...rbac.Permission) context.Context {
	p := &auth.Principal{Method: m, Perms: Checker(perms), CredentialID: "cred-gotham"}
	if m != auth.MethodAuthDisabled {
		p.User = &auth.User{ID: "01GOTHAMBRUCEWAYNE0000000", Username: "bruce.wayne"}
	}
	return auth.WithPrincipal(ctx, p)
}

// ---- a fake adapter -------------------------------------------------------

// Fake is a fake adapter: its descriptor builds FakeInstances and records
// every build. Its zero value builds instances whose Health is Call.
type Fake struct {
	// BuildErr fails every build.
	BuildErr error
	// HealthFn replaces each built instance's Health.
	HealthFn func(ctx context.Context, i *FakeInstance) integrations.Health
	// TestFn makes each built instance an integrations.Tester running it.
	TestFn func(ctx context.Context, i *FakeInstance) error

	mu        sync.Mutex
	instances []*FakeInstance
}

// Descriptor returns m's descriptor building f's instances.
func (f *Fake) Descriptor(m integrations.Meta) integrations.Descriptor {
	return integrations.NewDescriptor(m, func(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (integrations.Instance, error) {
		if f.BuildErr != nil {
			return nil, f.BuildErr
		}
		i := &FakeInstance{Env: env, Config: cfg, f: f}
		f.mu.Lock()
		f.instances = append(f.instances, i)
		f.mu.Unlock()
		if f.TestFn != nil {
			return &FakeTester{FakeInstance: i}, nil
		}
		return i, nil
	})
}

// Built lists every instance f built, in build order.
func (f *Fake) Built() []*FakeInstance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.instances)
}

// FakeInstance is one built instance of a Fake.
type FakeInstance struct {
	Env    integrations.Env
	Config integrations.InstanceConfig

	f      *Fake
	probes atomic.Int64
	closed atomic.Int64
}

// Health runs Fake.HealthFn, else Call.
func (i *FakeInstance) Health(ctx context.Context) integrations.Health {
	i.probes.Add(1)
	if i.f.HealthFn != nil {
		return i.f.HealthFn(ctx, i)
	}
	return i.Call(ctx)
}

// Close records the close.
func (i *FakeInstance) Close() error {
	i.closed.Add(1)
	return nil
}

// Closed is the number of times the instance was closed.
func (i *FakeInstance) Closed() int64 { return i.closed.Load() }

// Probes is the number of Health calls.
func (i *FakeInstance) Probes() int64 { return i.probes.Load() }

// CredentialField is the secret field Call sends, when the instance's
// settings name it.
const CredentialField = "password"

// CredentialHeader carries Call's secret.
const CredentialHeader = "X-Gotham-Credential"

// Call sends one GET through Env.HTTP to the instance's "url" setting (or
// its "host" and "port" as http://host:port/, or http://192.0.2.10/), with
// the secret field CredentialField in CredentialHeader when the settings
// name it. A refused secret is HealthAuthFailed; a failed request
// HealthDown.
func (i *FakeInstance) Call(ctx context.Context) integrations.Health {
	target := "http://192.0.2.10/"
	if u, ok := i.Config.Settings["url"].(string); ok && u != "" {
		target = u
	} else if h, ok := i.Config.Settings["host"].(string); ok && h != "" {
		target = "http://" + h + "/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return integrations.Health{Status: integrations.HealthMisconfigured}
	}
	if _, ok := i.Config.Settings[CredentialField]; ok {
		v, err := i.Env.Secrets.Secret(ctx, CredentialField)
		if err != nil {
			return integrations.Health{Status: integrations.HealthAuthFailed, Message: "credential refused"}
		}
		req.Header.Set(CredentialHeader, v.Reveal())
	}
	resp, err := i.Env.HTTP.Do(req)
	if err != nil {
		return integrations.Health{Status: integrations.HealthDown, Message: "unreachable"}
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return integrations.Health{Status: integrations.HealthDown, Message: "remote error"}
	}
	return integrations.Health{Status: integrations.HealthOK, RemoteVersion: "gotham-1"}
}

// FakeTester is a FakeInstance that is an integrations.Tester.
type FakeTester struct{ *FakeInstance }

// Test runs Fake.TestFn.
func (t *FakeTester) Test(ctx context.Context) error { return t.f.TestFn(ctx, t.FakeInstance) }

// ---- a fake outbound policy -----------------------------------------------

// Sent is one request that reached Outbound's transport.
type Sent struct {
	Ref        integrations.Ref
	URL        string
	Credential string // CredentialHeader's value
}

// Outbound is a fake Options.Outbound: it records every spec it is asked
// for and every request that reaches its transport, answering each with
// Status (200 when zero) and never touching a network.
type Outbound struct {
	Status int
	State  integrations.GateState

	mu    sync.Mutex
	specs []integrations.OutboundSpec
	sent  []Sent
}

// Func is the Options.Outbound func.
func (o *Outbound) Func() func(integrations.OutboundSpec) (integrations.HTTP, integrations.Gate) {
	return func(s integrations.OutboundSpec) (integrations.HTTP, integrations.Gate) {
		o.mu.Lock()
		o.specs = append(o.specs, s)
		o.mu.Unlock()
		g := fakeGate{o: o}
		return fakeHTTP{o: o, ref: s.Ref, gate: g}, g
	}
}

// Specs lists the specs asked for.
func (o *Outbound) Specs() []integrations.OutboundSpec {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.specs)
}

// Sent lists the requests that reached the transport.
func (o *Outbound) Sent() []Sent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.sent)
}

// roundTrip records req and answers it.
func (o *Outbound) roundTrip(ref integrations.Ref, req *http.Request) (*http.Response, error) {
	o.mu.Lock()
	o.sent = append(o.sent, Sent{Ref: ref, URL: req.URL.String(), Credential: req.Header.Get(CredentialHeader)})
	status := o.Status
	o.mu.Unlock()
	if req.Body != nil {
		_ = req.Body.Close()
	}
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Request: req, Header: http.Header{}}, nil
}

type fakeHTTP struct {
	o    *Outbound
	ref  integrations.Ref
	gate integrations.Gate
}

func (h fakeHTTP) Do(req *http.Request) (*http.Response, error) { return h.o.roundTrip(h.ref, req) }

func (h fakeHTTP) Client() *http.Client { return &http.Client{Transport: fakeTransport(h)} }

func (h fakeHTTP) Gate() integrations.Gate { return h.gate }

type fakeTransport fakeHTTP

func (t fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.o.roundTrip(t.ref, req)
}

type fakeGate struct{ o *Outbound }

func (fakeGate) Acquire(context.Context, string) (integrations.Ticket, error) { return noTicket{}, nil }

func (g fakeGate) State() integrations.GateState {
	g.o.mu.Lock()
	defer g.o.mu.Unlock()
	return g.o.State
}

type noTicket struct{}

func (noTicket) Done(error) {}
