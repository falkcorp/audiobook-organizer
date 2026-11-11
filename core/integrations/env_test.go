// file: core/integrations/env_test.go
// version: 1.0.0
// guid: 08e48a33-e0c4-4f6d-8167-db3c1f261851
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The owners the kernel registers for the stores a Registry joins.
const (
	secretsOwner storage.Owner = "core/secrets"
	auditOwner   storage.Owner = "core/audit"
)

// The test adapters: hello-prefixed, Batman-named.
const (
	batsignal integrations.AdapterID = "hello.batsignal" // notify, one URL, a required credential
	oracle    integrations.AdapterID = "hello.oracle"    // metadata, host and port, an optional credential
)

// batsignalSettings is hello.batsignal's settings: one URL-typed field
// (unset = the adapter's default), a required secret and a plain field.
type batsignalSettings struct {
	URL      string      `json:"url,omitempty" schema:"format=uri"`
	Password secrets.Ref `json:"password"`
	Channel  string      `json:"channel,omitempty"`
}

// oracleSettings is hello.oracle's settings: a host-and-port destination.
type oracleSettings struct {
	Host     string      `json:"host,omitempty"`
	Port     int         `json:"port,omitempty"`
	Password secrets.Ref `json:"password,omitzero"`
}

// schema reflects T or fails t.
func schema[T any](t testing.TB) config.Schema {
	t.Helper()
	s, err := config.SchemaFor[T]()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// env is one booted store with the integrations, secrets and audit
// keyspaces, the real secrets and audit stores, and the fakes.
type env struct {
	db      *storage.DB
	ks      storage.Keyspace
	secrets *secrets.Store
	audit   *audit.Store
	rec     *auditRecorder
	out     *integrationstest.Outbound
	fake    *integrationstest.Fake
	clock   *clock
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
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
	ks := keyspace(t, db, instancestore.Owner)
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	clk := &clock{t: time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)}
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: root, Keyspace: keyspace(t, db, secretsOwner), AppID: "gotham", Now: clk.now})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}
	aks := keyspace(t, db, auditOwner)
	au, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Now: clk.now})
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	return &env{
		db: db, ks: ks, secrets: sec, audit: au, rec: &auditRecorder{inner: au},
		out: &integrationstest.Outbound{}, fake: &integrationstest.Fake{}, clock: clk,
	}
}

func keyspace(t testing.TB, db *storage.DB, o storage.Owner) storage.Keyspace {
	t.Helper()
	ks, ok := db.Keyspace(o)
	if !ok {
		t.Fatalf("owner %s has no keyspace", o)
	}
	return ks
}

// auditRecorder is the real audit store with a switch that fails RecordIn,
// for the atomicity cases.
type auditRecorder struct {
	inner *audit.Store
	mu    sync.Mutex
	fail  error
}

func (a *auditRecorder) RecordIn(ctx context.Context, b *storage.Batch, e audit.Event) error {
	a.mu.Lock()
	fail := a.fail
	a.mu.Unlock()
	if fail != nil {
		return fail
	}
	return a.inner.RecordIn(ctx, b, e)
}

func (a *auditRecorder) failWith(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail = err
}

// options is the Registry's Options over e.
func (e *env) options() integrations.Options {
	return integrations.Options{
		Store:    integrations.NativeInstanceStore(e.ks),
		NewBatch: e.ks.NewBatch,
		Secrets:  e.secrets,
		Audit:    e.rec,
		Outbound: e.out.Func(),
		Now:      e.clock.now,
	}
}

// descriptors are the test adapters, built by e.fake.
func (e *env) descriptors(t testing.TB) []integrations.Descriptor {
	t.Helper()
	return []integrations.Descriptor{
		e.fake.Descriptor(integrations.Meta{ID: batsignal, Kind: integrations.KindNotify, DisplayName: "Bat-Signal", Settings: schema[batsignalSettings](t)}),
		e.fake.Descriptor(integrations.Meta{ID: oracle, Kind: integrations.KindMetadata, DisplayName: "Oracle", Settings: schema[oracleSettings](t)}),
	}
}

// registry builds, registers and loads a Registry over e. edit adjusts the
// options; overlay is the operator overlay (nil for none).
func (e *env) registry(t testing.TB, overlay iter.Seq2[config.Path, config.OperatorValue], edit func(*integrations.Options)) *integrations.Registry {
	t.Helper()
	o := e.options()
	if edit != nil {
		edit(&o)
	}
	r, err := integrations.New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Register("hello", e.descriptors(t)...); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Load(context.Background(), overlay); err != nil {
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

// The principals the cases write as.
func admin() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
}

func manager() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage)
}

func apiKey() context.Context {
	return integrationstest.As(context.Background(), auth.MethodAPIKey, integrationstest.Manage, integrationstest.Admin)
}

func authOff() context.Context {
	return integrationstest.As(context.Background(), auth.MethodAuthDisabled, integrationstest.Manage, integrationstest.Admin)
}

// The instances the cases use.
var (
	signalRef = integrations.Ref{Kind: integrations.KindNotify, Instance: "gcpd-roof"}
	oracleRef = integrations.Ref{Kind: integrations.KindMetadata, Instance: "clocktower"}
)

// signal is a hello.batsignal instance aimed at url.
func signal(url string) integrations.InstanceConfig {
	s := map[string]any{"channel": "gcpd"}
	if url != "" {
		s["url"] = url
	}
	return integrations.InstanceConfig{Ref: signalRef, Adapter: batsignal, Name: "GCPD roof", Enabled: true, Settings: s}
}

// cred is a secretsIn map holding password.
func cred(password string) map[string]secrets.Value {
	return map[string]secrets.Value{"password": secrets.NewValue(password)}
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

// mustPut is r.Put, failing t on an error.
func mustPut(t testing.TB, ctx context.Context, r *integrations.Registry, inst integrations.InstanceConfig, in map[string]secrets.Value) integrations.InstanceConfig {
	t.Helper()
	got, err := r.Put(ctx, inst, in)
	if err != nil {
		t.Fatalf("Put %s: %v", inst.Ref, err)
	}
	return got
}

// stored is ref's committed row, read straight from the native store.
func (e *env) stored(t testing.TB, ref integrations.Ref) (integrations.StoredInstance, bool) {
	t.Helper()
	rows, err := integrations.NativeInstanceStore(e.ks).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Config.Ref == ref {
			return row, true
		}
	}
	return integrations.StoredInstance{}, false
}

// secret is the committed secret of ref's field ("" when unset).
func (e *env) secret(t testing.TB, ref integrations.Ref, field string) string {
	t.Helper()
	n := secrets.Name("integrations." + string(ref.Kind) + "." + string(ref.Instance) + "." + field)
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

// lease leases ref as a FakeInstance.
func lease(t testing.TB, r *integrations.Registry, ref integrations.Ref) (*integrationstest.FakeInstance, func()) {
	t.Helper()
	inst, release, err := integrations.Lease[integrations.Instance](context.Background(), r, ref)
	if err != nil {
		t.Fatalf("Lease %s: %v", ref, err)
	}
	return fakeOf(t, inst), release
}

// fakeOf is the FakeInstance behind inst.
func fakeOf(t testing.TB, inst integrations.Instance) *integrationstest.FakeInstance {
	t.Helper()
	switch v := inst.(type) {
	case *integrationstest.FakeInstance:
		return v
	case *integrationstest.FakeTester:
		return v.FakeInstance
	}
	t.Fatalf("instance %T is not a fake", inst)
	return nil
}
