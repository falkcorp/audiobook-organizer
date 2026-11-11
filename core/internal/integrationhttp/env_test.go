// file: core/internal/integrationhttp/env_test.go
// version: 1.3.0
// guid: 6d2e8b40-a7c3-4f19-95e1-0c3b7a48d256
// last-edited: 2026-11-11

package integrationhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The owners the kernel registers for the stores a Registry joins. The
// package's tests open their store with storage.Open over MemFS, as
// integrations' own tests do.
const (
	secretsOwner storage.Owner = "core/secrets"
	auditOwner   storage.Owner = "core/audit"
)

// The test adapters: hello-prefixed, Batman-named.
const (
	batsignal integrations.AdapterID = "hello.batsignal" // notify: one URL, a required credential
	oracle    integrations.AdapterID = "hello.oracle"    // metadata: host and port, an optional credential
)

// batsignalSettings is hello.batsignal's settings.
type batsignalSettings struct {
	URL      string      `json:"url,omitempty" schema:"format=uri"`
	Password secrets.Ref `json:"password"`
	Channel  string      `json:"channel,omitempty"`
	// Extra is an object-valued field, for a container an operator value locks.
	Extra map[string]string `json:"extra,omitempty"`
}

// oracleSettings is hello.oracle's settings.
type oracleSettings struct {
	Host     string      `json:"host,omitempty"`
	Port     int         `json:"port,omitempty"`
	Password secrets.Ref `json:"password,omitzero"`
}

func schemaOf[T any](t testing.TB) config.Schema {
	t.Helper()
	s, err := config.SchemaFor[T]()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// clock is a fixed time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// env is one booted store with the integrations, secrets and audit
// keyspaces, the real secrets and audit stores, the fakes, a Registry and the
// handlers over it.
type env struct {
	ks      storage.Keyspace
	secrets *secrets.Store
	sec     *flakySecrets
	audit   *audit.Store
	log     *recorder
	out     *integrationstest.Outbound
	fake    *integrationstest.Fake
	clock   *clock
	reg     *integrations.Registry
	holds   *fakeHolds
	api     *api
}

// flakySecrets is the real secrets store with a switch that fails Meta, so
// the secrets backend can be taken down (a Put reads Meta to find a stale
// record, and that failure is ErrUnavailable).
type flakySecrets struct {
	*secrets.Store
	mu   sync.Mutex
	down bool
}

func (f *flakySecrets) setDown(v bool) {
	f.mu.Lock()
	f.down = v
	f.mu.Unlock()
}

func (f *flakySecrets) Meta(ctx context.Context, n secrets.Name) (secrets.Meta, error) {
	f.mu.Lock()
	down := f.down
	f.mu.Unlock()
	if down {
		return secrets.Meta{}, errors.New("secrets backend down")
	}
	return f.Store.Meta(ctx, n)
}

// recorder is the route's audit.Logger over the real store, with a switch
// that fails Record and a count of the calls.
type recorder struct {
	inner *audit.Store
	mu    sync.Mutex
	fail  error
	calls int
}

func (r *recorder) Record(ctx context.Context, e audit.Event) error {
	r.mu.Lock()
	r.calls++
	fail := r.fail
	r.mu.Unlock()
	if fail != nil {
		return fail
	}
	return r.inner.Record(ctx, e)
}

func (r *recorder) RecordIn(ctx context.Context, b *storage.Batch, e audit.Event) error {
	return r.inner.RecordIn(ctx, b, e)
}

func (r *recorder) Emit(ctx context.Context, e audit.Event) { r.inner.Emit(ctx, e) }

func (r *recorder) failWith(err error) {
	r.mu.Lock()
	r.fail = err
	r.mu.Unlock()
}

func (r *recorder) recordCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// fakeHolds is a HoldReleaser over a table of holds by instance, with the
// semantics of providerhttp.Registry's pair: HoldFor reads one, ReleaseHoldIf
// releases it only while it is still the hold in force (nil when none is,
// ErrHoldChanged when a different one is). afterRead runs between HoldFor's
// answer and the caller's next call, to change the hold in that gap.
type fakeHolds struct {
	mu        sync.Mutex
	held      map[integrations.Ref]integrations.Hold
	asked     int
	expects   []integrations.Hold // every ReleaseHoldIf's argument
	released_ []integrations.Hold // the holds actually ended
	err       error
	afterRead func(f *fakeHolds)
}

// holdOn makes ref's active hold h (replacing any).
func (f *fakeHolds) holdOn(ref integrations.Ref, h integrations.Hold) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setLocked(ref, h)
}

func (f *fakeHolds) setLocked(ref integrations.Ref, h integrations.Hold) {
	if f.held == nil {
		f.held = map[integrations.Ref]integrations.Hold{}
	}
	f.held[ref] = h
}

func (f *fakeHolds) HoldFor(ref integrations.Ref) (integrations.Hold, bool) {
	f.mu.Lock()
	f.asked++
	h, ok := f.held[ref]
	hook := f.afterRead
	f.mu.Unlock()
	if hook != nil {
		hook(f)
	}
	return h, ok
}

func (f *fakeHolds) ReleaseHoldIf(_ context.Context, expect integrations.Hold) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expects = append(f.expects, expect)
	if f.err != nil {
		return f.err
	}
	for ref, cur := range f.held {
		if cur.Scope != expect.Scope {
			continue
		}
		if cur != expect {
			return fmt.Errorf("fake: %w", ErrHoldChanged)
		}
		delete(f.held, ref)
		f.released_ = append(f.released_, cur)
		return nil
	}
	return nil // no hold in force: the goal is met
}

// heldScopeCalls is how many times HoldFor was asked.
func (f *fakeHolds) heldScopeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked
}

// released is the scopes of the holds actually ended.
func (f *fakeHolds) released() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, h := range f.released_ {
		out = append(out, h.Scope)
	}
	return out
}

// releaseCalls is every ReleaseHoldIf argument.
func (f *fakeHolds) releaseCalls() []integrations.Hold {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.expects)
}

// hold is a hold on scope for the cases: quota class, an hour from now.
func (e *env) hold(scope string, extra time.Duration) integrations.Hold {
	return integrations.Hold{Scope: scope, Class: integrations.ClassQuota,
		Until: e.clock.now().Add(time.Hour + extra), SetAt: e.clock.now()}
}

func newEnv(t testing.TB) *env {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: instancestore.Owner, Spec: instancestore.StorageSpec(), Enabled: true},
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
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
	keyspace := func(owner storage.Owner) storage.Keyspace {
		ks, ok := db.Keyspace(owner)
		if !ok {
			t.Fatalf("owner %s has no keyspace", owner)
		}
		return ks
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	clk := &clock{t: time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)}
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: root, Keyspace: keyspace(secretsOwner), AppID: "gotham", Now: clk.now})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	aks := keyspace(auditOwner)
	au, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Now: clk.now})
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	ks := keyspace(instancestore.Owner)
	e := &env{
		ks: ks, secrets: sec, sec: &flakySecrets{Store: sec}, audit: au, log: &recorder{inner: au},
		out: &integrationstest.Outbound{}, fake: &integrationstest.Fake{}, clock: clk, holds: &fakeHolds{},
	}
	e.reg = e.registry(t)
	e.api = &api{reg: e.reg, holds: e.holds, audit: e.log, base: "/api/core/v1"}
	return e
}

// registry builds, registers and loads a Registry over e.
func (e *env) registry(t testing.TB) *integrations.Registry {
	t.Helper()
	r, err := integrations.New(integrations.Options{
		Store:    integrations.NativeInstanceStore(e.ks),
		NewBatch: e.ks.NewBatch,
		Secrets:  e.sec,
		Audit:    e.log,
		Outbound: e.out.Func(),
		Now:      e.clock.now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Register("hello",
		e.fake.Descriptor(integrations.Meta{ID: batsignal, Kind: integrations.KindNotify, DisplayName: "Bat-Signal", Version: "1.0",
			Settings: schemaOf[batsignalSettings](t), Limits: integrations.Limits{RPS: 2, Timeout: 5 * time.Second}}),
		e.fake.Descriptor(integrations.Meta{ID: oracle, Kind: integrations.KindMetadata, DisplayName: "Oracle", Settings: schemaOf[oracleSettings](t)}),
	); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Load(context.Background(), nil); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return r
}

// ---- principals and requests ---------------------------------------------

// The principals the cases act as, bound into a context the way httpapi's
// stage 12 does (auth.WithPrincipal).
func adminCtx() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
}

func managerCtx() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage)
}

func apiKeyCtx() context.Context {
	return integrationstest.As(context.Background(), auth.MethodAPIKey, integrationstest.Manage, integrationstest.Admin)
}

// in is the decoded request a handler receives: req with the principal ctx
// carries, the same source httpapi takes both from.
func in[R any](ctx context.Context, req R) *httpapi.In[R] {
	p, _ := auth.PrincipalFrom(ctx)
	return &httpapi.In[R]{Req: req, Principal: p}
}

// The instances the cases use.
const (
	kindNotify = "notify"
	roofID     = "gcpd-roof"
)

func truth() *bool { b := true; return &b }

// signalBody is a hello.batsignal body aimed at url with the credential
// password (empty: none).
func signalBody(id, url, password string) IntegrationInstanceBody {
	b := IntegrationInstanceBody{
		ID: id, Adapter: string(batsignal), Name: "GCPD roof", Enabled: truth(),
		Settings: map[string]any{"channel": "gcpd"},
	}
	if url != "" {
		b.Settings["url"] = url
	}
	if password != "" {
		b.Secrets = map[string]string{"password": password}
	}
	return b
}

// create posts body as kind through the handler, failing t on an error.
func (e *env) create(t testing.TB, ctx context.Context, kind string, body IntegrationInstanceBody) IntegrationInstance {
	t.Helper()
	req := createReq{Kind: kind, Body: body}
	if err := req.Validate(); err != nil {
		t.Fatalf("create %s: invalid: %v", body.ID, err)
	}
	got, err := e.api.createInstance(ctx, in(ctx, req))
	if err != nil {
		t.Fatalf("create %s: %v", body.ID, err)
	}
	return got.Body
}

// get reads an instance through the handler.
func (e *env) get(t testing.TB, ctx context.Context, kind, id string) IntegrationInstance {
	t.Helper()
	got, err := e.api.getInstance(ctx, in(ctx, instanceReq{Kind: kind, ID: id}))
	if err != nil {
		t.Fatalf("get %s/%s: %v", kind, id, err)
	}
	return got
}

// secretValue is the committed secret of ref's field ("" when unset).
func (e *env) secretValue(t testing.TB, kind, id, field string) string {
	t.Helper()
	n := secrets.Name("integrations." + kind + "." + id + "." + field)
	v, _, err := e.secrets.Get(context.Background(), n)
	if errors.Is(err, secrets.ErrNotSet) {
		return ""
	}
	if err != nil {
		t.Fatalf("read secret %s: %v", n, err)
	}
	return v.Reveal()
}

// actions lists the committed audit records as "action reason", oldest
// first.
func (e *env) actions(t testing.TB) []string {
	t.Helper()
	var out []string
	for rec, err := range e.audit.Query(context.Background(), audit.Filter{Limit: audit.MaxQueryLimit}) {
		if err != nil {
			t.Fatalf("query audit: %v", err)
		}
		a := rec.Action
		if rec.Reason != "" {
			a += " " + rec.Reason
		}
		out = append([]string{a}, out...)
	}
	return out
}

// credentialsSentTo lists the credentials sent to url, in order.
func (e *env) credentialsSentTo(url string) []string {
	var out []string
	for _, s := range e.out.Sent() {
		if s.URL == url {
			out = append(out, s.Credential)
		}
	}
	return out
}

// overlay is an operator overlay of path=value pairs (values are JSON
// strings, as env values arrive).
func overlay(kv ...string) iter.Seq2[config.Path, config.OperatorValue] {
	return func(yield func(config.Path, config.OperatorValue) bool) {
		for i := 0; i+1 < len(kv); i += 2 {
			raw, _ := json.Marshal(kv[i+1])
			if !yield(config.Path(kv[i]), config.OperatorValue{Value: raw, Src: config.SourceEnv, By: "env " + kv[i]}) {
				return
			}
		}
	}
}

// auditFilterTest selects the core.integration.test records.
func auditFilterTest() audit.Filter {
	return audit.Filter{ActionPrefix: "core.integration.test", Limit: audit.MaxQueryLimit}
}
