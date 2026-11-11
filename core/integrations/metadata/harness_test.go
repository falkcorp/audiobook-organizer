// file: core/integrations/metadata/harness_test.go
// version: 1.0.0
// guid: b82824a7-c138-4bbc-b839-04e251f5b6c5
// last-edited: 2026-11-11

package metadata_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/integrations/metadata"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The harness: a real integrations.Registry over a booted in-memory store (the
// shape of download's harness, which an external package cannot import) with
// two metadata adapters of the app's own query and result types: hello.fake,
// whose instances are the coretest.FakeProviders a test hands out by instance
// ID, and hello.http, whose instances ask the coretest.ProviderHTTP upstream
// through the instance's own gate. It cannot use coretest.NewStore: that is
// for tests whose packages the kernel graph does not reach.

const (
	secretsOwner storage.Owner = "core/secrets"
	auditOwner   storage.Owner = "core/audit"
	cacheOwner   storage.Owner = "core/hello"
)

const (
	fakeAdapter integrations.AdapterID = "hello.fake"
	httpAdapter integrations.AdapterID = "hello.http"
	tvAdapter   integrations.AdapterID = "hello.tv" // a metadata adapter of ANOTHER app type
)

// query and book are the app's types: a Batman-comic lookup and its answer.
type query struct {
	Title string `json:"title"`
}

type book struct {
	Title  string `json:"title"`
	Source string `json:"source"`
}

func ref(name string) integrations.Ref {
	return integrations.Ref{Kind: integrations.KindMetadata, Instance: integrations.InstanceID(name)}
}

// tvQuery is another app's query type: its providers are not a books chain's.
type tvQuery struct{ Series string }

type fake = coretest.FakeProvider[query, book]
type tvFake = coretest.FakeProvider[tvQuery, string]

type harness struct {
	t   testing.TB
	reg *integrations.Registry
	clk *coretest.ProviderClock

	mu    sync.Mutex
	fakes map[integrations.InstanceID]*fake
	bases map[integrations.InstanceID]string // the upstream of each hello.http instance
	tvs   map[integrations.InstanceID]*tvFake
	// builds counts the builds of each instance.
	builds map[integrations.InstanceID]int
	ks     storage.Keyspace
}

// newHarness boots the store and a Registry. outbound is the Registry's
// Options.Outbound (nil: integrationstest's fake); clk drives it.
func newHarness(t testing.TB, clk *coretest.ProviderClock, outbound func(integrations.OutboundSpec) (integrations.HTTP, integrations.Gate), lim integrations.Limits) *harness {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: instancestore.Owner, Spec: instancestore.StorageSpec(), Enabled: true},
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
			{Owner: cacheOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build:           "coretest+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	h := &harness{t: t, clk: clk, fakes: map[integrations.InstanceID]*fake{}, bases: map[integrations.InstanceID]string{}, tvs: map[integrations.InstanceID]*tvFake{}, builds: map[integrations.InstanceID]int{}, ks: keyspaceOf(t, db, cacheOwner)}
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: root, Keyspace: keyspaceOf(t, db, secretsOwner), AppID: "gotham", Now: clk.Now})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	aks := keyspaceOf(t, db, auditOwner)
	au, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Now: clk.Now})
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	iks := keyspaceOf(t, db, instancestore.Owner)
	if outbound == nil {
		outbound = (&integrationstest.Outbound{}).Func()
	}
	reg, err := integrations.New(integrations.Options{
		Store: integrations.NativeInstanceStore(iks), NewBatch: iks.NewBatch,
		Secrets: sec, Audit: au, Outbound: outbound, Now: clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	schema, err := config.SchemaFor[struct{}]()
	if err != nil {
		t.Fatal(err)
	}
	fakeBuild := func(_ context.Context, _ integrations.Env, cfg integrations.InstanceConfig) (metadata.Provider[query, book], error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.builds[cfg.Ref.Instance]++
		f, ok := h.fakes[cfg.Ref.Instance]
		if !ok {
			return nil, fmt.Errorf("the harness has no fake for %s", cfg.Ref)
		}
		return f, nil
	}
	httpBuild := func(_ context.Context, env integrations.Env, cfg integrations.InstanceConfig) (metadata.Provider[query, book], error) {
		h.mu.Lock()
		h.builds[cfg.Ref.Instance]++
		h.mu.Unlock()
		h.mu.Lock()
		base := h.bases[cfg.Ref.Instance]
		h.mu.Unlock()
		return &httpProvider{ref: cfg.Ref, http: env.HTTP, base: base}, nil
	}
	tvBuild := func(_ context.Context, _ integrations.Env, cfg integrations.InstanceConfig) (metadata.Provider[tvQuery, string], error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		f, ok := h.tvs[cfg.Ref.Instance]
		if !ok {
			return nil, fmt.Errorf("the harness has no tv fake for %s", cfg.Ref)
		}
		return f, nil
	}
	if err := reg.Register("hello",
		metadata.Adapter(integrations.Meta{ID: tvAdapter, DisplayName: "Bat-TV", Settings: schema}, tvBuild),
		metadata.Adapter(integrations.Meta{ID: fakeAdapter, DisplayName: "Bat-Fake", Settings: schema}, fakeBuild),
		metadata.Adapter(integrations.Meta{ID: httpAdapter, DisplayName: "Bat-HTTP", Settings: schema, Limits: lim}, httpBuild),
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Load(ctx, nil); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := reg.Close(cctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	h.reg = reg
	return h
}

func keyspaceOf(t testing.TB, db *storage.DB, o storage.Owner) storage.Keyspace {
	t.Helper()
	ks, ok := db.Keyspace(o)
	if !ok {
		t.Fatalf("owner %s has no keyspace", o)
	}
	return ks
}

func admin() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
}

// addFake stores an enabled hello.fake instance named name and hands the
// harness f for it.
func (h *harness) addFake(name string, priority int, f *fake) *fake {
	h.t.Helper()
	if f == nil {
		f = coretest.NewFakeProvider[query, book](ref(name))
	}
	h.mu.Lock()
	h.fakes[integrations.InstanceID(name)] = f
	h.mu.Unlock()
	h.put(ref(name), fakeAdapter, priority, true, nil)
	return f
}

// addTV stores an enabled hello.tv instance (another query type).
func (h *harness) addTV(name string, priority int, f *tvFake) {
	h.t.Helper()
	h.mu.Lock()
	h.tvs[integrations.InstanceID(name)] = f
	h.mu.Unlock()
	h.put(ref(name), tvAdapter, priority, true, nil)
}

// addHTTP stores an enabled hello.http instance that asks base.
func (h *harness) addHTTP(name string, priority int, base string) {
	h.t.Helper()
	h.mu.Lock()
	h.bases[integrations.InstanceID(name)] = base
	h.mu.Unlock()
	h.put(ref(name), httpAdapter, priority, true, nil)
}

func (h *harness) put(r integrations.Ref, adapter integrations.AdapterID, priority int, enabled bool, settings map[string]any) integrations.InstanceConfig {
	h.t.Helper()
	if settings == nil {
		settings = map[string]any{}
	}
	var version uint64
	if cur, ok := h.reg.Config(r); ok {
		version = cur.Version
	}
	got, err := h.reg.Put(admin(), integrations.InstanceConfig{
		Ref: r, Adapter: adapter, Name: string(r.Instance), Enabled: enabled, Priority: priority, Settings: settings, Version: version,
	}, nil)
	if err != nil {
		h.t.Fatalf("Put %s: %v", r, err)
	}
	return got
}

// edit re-puts r with a new priority and enabled flag, keeping its settings
// (an edit that swaps the built instance).
func (h *harness) edit(r integrations.Ref, priority int, enabled bool) {
	h.t.Helper()
	cur, ok := h.reg.Config(r)
	if !ok {
		h.t.Fatalf("no instance %s", r)
	}
	h.put(r, cur.Adapter, priority, enabled, cur.Settings)
}

func (h *harness) buildCount(r integrations.Ref) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.builds[r.Instance]
}

// httpProvider is a hello.http instance: it asks base?title=<q.Title> through
// its gate (env.HTTP, the only way an adapter reaches the network) and reads a
// JSON list of titles, classifying a failed answer the way an adapter does.
type httpProvider struct {
	ref  integrations.Ref
	http integrations.HTTP
	base string
}

func (p *httpProvider) Ref() integrations.Ref { return p.ref }

func (p *httpProvider) Health(context.Context) integrations.Health {
	return integrations.Health{Status: integrations.HealthOK}
}

func (p *httpProvider) Close() error { return nil }

func (p *httpProvider) Search(ctx context.Context, q query) ([]book, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"?title="+url.QueryEscape(q.Title), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &integrations.Error{Ref: p.ref, Op: "search", Class: providerhttp.Classify(ctx, resp, body, nil), Status: resp.StatusCode}
	}
	var titles []string
	if err := json.Unmarshal(body, &titles); err != nil {
		return nil, &integrations.Error{Ref: p.ref, Op: "search", Class: integrations.ClassBadOutput, Status: resp.StatusCode}
	}
	out := make([]book, len(titles))
	for i, t := range titles {
		out[i] = book{Title: t, Source: string(p.ref.Instance)}
	}
	return out, nil
}

func newChain(t testing.TB, h *harness, o metadata.ChainOptions[query]) *metadata.Chain[query, book] {
	t.Helper()
	c, err := metadata.NewChain[query, book](h.reg, o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
