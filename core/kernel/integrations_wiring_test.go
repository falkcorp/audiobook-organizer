// file: core/kernel/integrations_wiring_test.go
// version: 0.1.0
// guid: 243d14e4-e2b2-4c49-ac0e-90c33c79c95e
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/library"
)

// These tests pin the wires buildIntegrations lays between the parts: the
// effect each one has on a real instance, not the log line that says it was
// laid. Each is killed by dropping that wire.

// The download adapter of the rig: a client that records the Env it was built
// with, so a test can use the outbound layer the Registry gave the instance.
const integAlfredAdapter integrations.AdapterID = "gotham.alfred_dl"

var integAlfredRef = integrations.Ref{Kind: integrations.KindDownload, Instance: "alfred"}

type integAlfred struct {
	mu    sync.Mutex
	envs  []integrations.Env
	paths []string // the RemotePath of each item the client reports
}

// env is the Env of the most recent build.
func (a *integAlfred) env(t *testing.T) integrations.Env {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.envs) == 0 {
		t.Fatal("the alfred adapter was never built")
	}
	return a.envs[len(a.envs)-1]
}

func (a *integAlfred) setPaths(p ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.paths = p
}

type integAlfredClient struct{ a *integAlfred }

func (integAlfredClient) Health(context.Context) integrations.Health {
	return integrations.Health{Status: integrations.HealthOK}
}
func (integAlfredClient) Close() error { return nil }
func (integAlfredClient) Caps(context.Context) (download.Caps, error) {
	return download.Caps{Protocol: download.Torrent}, nil
}
func (integAlfredClient) Status(context.Context) (download.ClientStatus, error) {
	return download.ClientStatus{FreeSpace: -1}, nil
}
func (c integAlfredClient) Items(context.Context, download.ItemFilter) ([]download.Item, error) {
	c.a.mu.Lock()
	defer c.a.mu.Unlock()
	var out []download.Item
	for i, p := range c.a.paths {
		out = append(out, download.Item{Ref: download.ItemRef{ID: download.ItemID("bat-" + string(rune('a'+i)))}, Name: p, RemotePath: p})
	}
	return out, nil
}

// buildWithAlfred builds the services, registers the alfred download adapter
// beside the rig's own, loads, and stores the alfred instance.
func (r *integRig) buildWithAlfred(in integrationsInputs) (*integrationsServices, *integAlfred) {
	r.t.Helper()
	s := r.build(in)
	a := &integAlfred{}
	if err := s.registerAdapters("gotham", download.Adapter(integrations.Meta{
		ID: integAlfredAdapter, DisplayName: "Alfred", Version: "1.0.0", Settings: integSchema[struct{}](r.t),
	}, func(_ context.Context, env integrations.Env, _ integrations.InstanceConfig) (download.Client, error) {
		a.mu.Lock()
		a.envs = append(a.envs, env)
		a.mu.Unlock()
		return integAlfredClient{a: a}, nil
	})); err != nil {
		r.t.Fatalf("registerAdapters: %v", err)
	}
	if err := s.load(context.Background(), nil); err != nil {
		r.t.Fatalf("load: %v", err)
	}
	r.putAlfred(s)
	return s, a
}

// putAlfred stores the alfred instance, and builds it with a health probe.
func (r *integRig) putAlfred(s *integrationsServices) {
	r.t.Helper()
	if _, err := s.registry.Put(integAdmin(), integrations.InstanceConfig{
		Ref: integAlfredRef, Adapter: integAlfredAdapter, Name: "Alfred", Enabled: true, Settings: map[string]any{},
	}, nil); err != nil {
		r.t.Fatalf("Put alfred: %v", err)
	}
	if _, err := s.registry.Health(context.Background(), integAlfredRef); err != nil {
		r.t.Fatalf("Health alfred: %v", err)
	}
}

// TestBuildIntegrations_PoolFollowsTheReloadedMapper: the download Pool maps
// paths with the library's mapper as it is after a Reload, not the one that was
// current at build. Library.Reload replaces the mapper, so a Pool that held the
// build-time one would keep mapping with a stale table (a download mapped to
// where it no longer belongs).
func TestBuildIntegrations_PoolFollowsTheReloadedMapper(t *testing.T) {
	mapping := func(remote, rel string) json.RawMessage {
		return json.RawMessage(`{"source":"download:alfred","remote":"` + remote + `","local":{"root":"batcave","rel":"` + rel + `"}}`)
	}
	root, err := json.Marshal(map[string]any{"id": "batcave", "path": t.TempDir(), "role": "library", "owner": "core", "sentinel": "required"})
	if err != nil {
		t.Fatal(err)
	}
	r, in := newIntegRig(t, nil)
	r.setLibrary(func(v *librarySettings) {
		v.Roots = []json.RawMessage{root}
		v.Mappings = []json.RawMessage{mapping("/dl/complete", "books")}
	})
	s, a := r.buildWithAlfred(in)
	a.setPaths("/dl/complete/Year One", "/dl/done/The Long Halloween")
	ctx := context.Background()
	localOf := func() map[string]library.Path {
		t.Helper()
		items, err := s.downloads.Items(ctx, integAlfredRef, download.ItemFilter{})
		if err != nil {
			t.Fatalf("Items: %v", err)
		}
		out := map[string]library.Path{}
		for _, it := range items {
			out[it.RemotePath] = it.LocalPath
		}
		return out
	}

	got := localOf()
	if got["/dl/complete/Year One"] == (library.Path{}) {
		t.Fatalf("the Pool did not map through the build-time mapping: %v", got)
	}
	if got["/dl/done/The Long Halloween"] != (library.Path{}) {
		t.Fatalf("a path no mapping covers was mapped: %v", got)
	}

	r.setLibrary(func(v *librarySettings) { v.Mappings = []json.RawMessage{mapping("/dl/done", "shelf")} })
	if err := s.library.(library.Reloader).Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	got = localOf()
	if got["/dl/done/The Long Halloween"] == (library.Path{}) {
		t.Fatalf("the Pool did not follow the Reload: the new mapping is unused (%v)", got)
	}
	if got["/dl/complete/Year One"] != (library.Path{}) {
		t.Fatalf("the Pool still maps through the replaced mapping (%v)", got)
	}
}

// TestBuildIntegrations_InstanceOutboundIsProviderHTTP: the Env the Registry
// builds an instance with is providerhttp's, keyed by the instance: its HTTP
// client refuses a private address until core.integrations.egress.lan_allow
// names it (read per call), and a credential failure through its Gate is a
// hold on providerhttp's table under the instance's ref. A Registry built with
// no Outbound gives its instances a client that refuses everything and a gate
// of its own, so neither would hold.
func TestBuildIntegrations_InstanceOutboundIsProviderHTTP(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s, a := r.buildWithAlfred(in)
	env := a.env(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }))
	t.Cleanup(srv.Close)
	get := func() error {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := env.HTTP.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := get(); !errors.Is(err, providerhttp.ErrBlockedAddress) {
		t.Fatalf("a loopback host through the instance's client = %v, want providerhttp's ErrBlockedAddress", err)
	}
	r.setIntegrations(func(v *integrationsSettings) { v.Egress.LANAllow = []string{"127.0.0.1"} })
	if err := get(); err != nil {
		t.Fatalf("a loopback host on lan_allow through the instance's client: %v", err)
	}

	if _, held := s.providerhttp.HoldFor(integAlfredRef); held {
		t.Fatal("the instance holds before any failure")
	}
	tk, err := env.Gate.Acquire(context.Background(), "")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	tk.Done(&integrations.Error{Class: integrations.ClassAuth, Status: 401})
	if _, held := s.providerhttp.HoldFor(integAlfredRef); !held {
		t.Fatal("a 401 through the instance's gate left no hold on providerhttp's table: the gate is not providerhttp's")
	}
}

// TestBuildIntegrations_RecreatedInstanceStartsClean: Delete tells providerhttp
// to forget the instance, so an instance created again under the same ref does
// not inherit the hold, the daily quota count or the open breaker its
// predecessor earned. The hold and the quota are checked after the Delete and
// again after the re-create; the breaker lives in the scope the new instance's
// gate joins, so it is read there.
func TestBuildIntegrations_RecreatedInstanceStartsClean(t *testing.T) {
	const quotaDay = "2039-04-01"
	ctx := context.Background()
	type probe struct {
		earn func(t *testing.T, s *integrationsServices, env integrations.Env, gs providerhttp.GateStore)
		// clean checks the state; env is the instance's newest Env, and
		// recreated says the instance has been built again since the Delete.
		clean func(t *testing.T, s *integrationsServices, env integrations.Env, gs providerhttp.GateStore, recreated bool)
	}
	fail := func(t *testing.T, env integrations.Env, class integrations.Class) {
		t.Helper()
		tk, err := env.Gate.Acquire(ctx, "")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		tk.Done(&integrations.Error{Class: class, Status: 500})
	}
	scope := integAlfredRef.String() // the default scope of an instance
	probes := map[string]probe{
		"hold": {
			earn: func(t *testing.T, s *integrationsServices, env integrations.Env, _ providerhttp.GateStore) {
				fail(t, env, integrations.ClassAuth)
				if _, held := s.providerhttp.HoldFor(integAlfredRef); !held {
					t.Fatal("the instance earned no hold")
				}
			},
			clean: func(t *testing.T, s *integrationsServices, _ integrations.Env, _ providerhttp.GateStore, _ bool) {
				if h, held := s.providerhttp.HoldFor(integAlfredRef); held {
					t.Fatalf("the hold survived: %+v", h)
				}
			},
		},
		"quota": {
			earn: func(t *testing.T, _ *integrationsServices, _ integrations.Env, gs providerhttp.GateStore) {
				if err := gs.PutQuota(ctx, scope, "query", quotaDay, 4); err != nil {
					t.Fatal(err)
				}
			},
			clean: func(t *testing.T, _ *integrationsServices, _ integrations.Env, gs providerhttp.GateStore, _ bool) {
				if _, used, err := gs.Quota(ctx, scope, "query"); err != nil || used != 0 {
					t.Fatalf("the quota count is %d (%v), want 0", used, err)
				}
			},
		},
		"breaker": {
			earn: func(t *testing.T, _ *integrationsServices, env integrations.Env, _ providerhttp.GateStore) {
				// The default threshold is five failures. A failure also sets a
				// hold, which refuses the next Acquire, so all five tickets are
				// taken before the first one is settled.
				var tks []integrations.Ticket
				for range 5 {
					tk, err := env.Gate.Acquire(ctx, "")
					if err != nil {
						t.Fatalf("Acquire: %v", err)
					}
					tks = append(tks, tk)
				}
				for _, tk := range tks {
					tk.Done(&integrations.Error{Class: integrations.ClassTransient, Status: 500})
				}
				if st := env.Gate.State().Breaker; st != integrations.BreakerOpen {
					t.Fatalf("five failures left the breaker %v, want open", st)
				}
			},
			clean: func(t *testing.T, _ *integrationsServices, env integrations.Env, _ providerhttp.GateStore, recreated bool) {
				if !recreated {
					return // the old Env still holds the old scope; only the new one shows the breaker
				}
				if st := env.Gate.State().Breaker; st != integrations.BreakerClosed {
					t.Fatalf("the re-created instance's breaker is %v, want closed", st)
				}
			},
		},
	}
	for name, p := range probes {
		t.Run(name, func(t *testing.T) {
			r, in := newIntegRig(t, nil)
			s, a := r.buildWithAlfred(in)
			ks, ok := in.Keyspace(providerhttp.Owner)
			if !ok {
				t.Fatal("no providerhttp keyspace")
			}
			gs := providerhttp.NativeGateStore(ks)
			p.earn(t, s, a.env(t), gs)
			if err := s.registry.Delete(integAdmin(), integAlfredRef); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			p.clean(t, s, a.env(t), gs, false)
			r.putAlfred(s)
			p.clean(t, s, a.env(t), gs, true)
		})
	}
}
