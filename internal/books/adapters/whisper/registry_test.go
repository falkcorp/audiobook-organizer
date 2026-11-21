// file: internal/books/adapters/whisper/registry_test.go
// version: 0.1.0
// guid: 5f1e2b6e-0c7e-4f6a-9a59-2f8a0a6f9d11
// last-edited: 2026-11-21

package whisper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

var regNoon = time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)

// registryRig is a real integrations.Registry (real storage over an in-memory
// FS, secrets, audit and the providerhttp gate) with both whisper adapters
// registered under the books origin, as F7-09 registers them.
type registryRig struct {
	t   testing.TB
	reg *integrations.Registry
	run *fakeRunner
}

func admin() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
}

func newRegistryRig(t testing.TB) *registryRig {
	t.Helper()
	ctx := context.Background()
	const (
		integOwner   storage.Owner = "core/integrations"
		secretsOwner storage.Owner = "core/secrets"
		auditOwner   storage.Owner = "core/audit"
	)
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: "/batcave/whisper.pebble",
		Owners: []storage.OwnerSpec{
			// core/integrations' own spec is not importable (core/internal/instancestore):
			// its Serialized range is the instance rows, c/integrations/instances/.
			{Owner: integOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{string(keys.Prefix(keys.Str("instances")))}}, Enabled: true},
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
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
	now := func() time.Time { return regNoon }
	sec, err := secrets.Open(ctx, secrets.Options{StateDir: secrets.RootStateFiles(root), Keyspace: ks(secretsOwner), AppID: "gotham", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	aks := ks(auditOwner)
	au, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	ph, err := providerhttp.New(providerhttp.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	iks := ks(integOwner)
	reg, err := integrations.New(integrations.Options{
		Store: integrations.NativeInstanceStore(iks), NewBatch: iks.NewBatch, Secrets: sec, Audit: au, Outbound: ph.Outbound, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &registryRig{t: t, reg: reg, run: &fakeRunner{model: "base.en", text: "Wayne Enterprises"}}
	sd, err := ServerDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	ld, err := LocalDescriptor(r.run)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []integrations.Descriptor{sd, ld} {
		if err := reg.Register("books", d); err != nil {
			t.Fatalf("Register %s: %v", d.Meta().ID, err)
		}
	}
	if err := reg.Load(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reg.Close(cctx)
	})
	return r
}

func (r *registryRig) put(ref integrations.Ref, adapter integrations.AdapterID, local bool, settings map[string]any) (integrations.InstanceConfig, error) {
	return r.reg.Put(admin(), integrations.InstanceConfig{
		Ref: ref, Adapter: adapter, Name: string(ref.Instance), Enabled: true, Local: local, Settings: settings,
	}, nil)
}

func (r *registryRig) backend(ref integrations.Ref) (ai.Backend, func()) {
	r.t.Helper()
	b, release, err := integrations.Lease[ai.Backend](context.Background(), r.reg, ref)
	if err != nil {
		r.t.Fatalf("Lease %s: %v", ref, err)
	}
	return b, release
}

// A whisper_server instance is stored by an admin, built by the Registry over
// the gate, and measures and transcribes a loopback worker end to end: the
// path the adapter takes in production, not the one a test constructs.
func TestRegistry_ServerInstanceEndToEnd(t *testing.T) {
	t.Parallel()
	var transcribes atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, metalWorker("/mnt/gotham/books"))
	})
	mux.HandleFunc("POST /transcribe-path-batch", func(w http.ResponseWriter, _ *http.Request) {
		transcribes.Add(1)
		writeJSON(t, w, 200, map[string]any{"results": map[string]any{"clip": map[string]any{"text": "Arkham Asylum", "error": nil}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	r := newRegistryRig(t)
	ref := integrations.Ref{Kind: integrations.KindAI, Instance: "batcave-mini"}
	if _, err := r.put(ref, ServerAdapterID, true, map[string]any{
		"url": srv.URL, "concurrency": 2, "root_paths": map[string]string{"books-library": "/mnt/gotham/books"},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b, release := r.backend(ref)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	caps, err := b.Caps(ctx)
	if err != nil {
		t.Fatalf("Caps: %v", err)
	}
	if !slices.Contains(caps.Features, ai.Feature("shared_fs:books-library")) || !slices.Contains(caps.Labels, "gpu") {
		t.Errorf("caps = %+v, want the measured shared_fs feature and gpu label", caps)
	}
	if h := b.Health(ctx); h.Status != integrations.HealthOK {
		t.Errorf("Health = %+v", h)
	}
	tr, ok := b.(ai.Transcriber)
	if !ok {
		t.Fatalf("%T is not a Transcriber", b)
	}
	got, err := tr.Transcribe(ctx, "m", ai.TranscribeRequest{Audio: ai.AudioSource{Path: library.Path{Root: "books-library", Rel: "Pennyworth/01.m4b"}}})
	if err != nil || got.Text != "Arkham Asylum" {
		t.Errorf("Transcribe = %+v, %v", got, err)
	}
	if transcribes.Load() != 1 {
		t.Errorf("the worker saw %d path call(s), want 1", transcribes.Load())
	}
}

// The egress guard still judges the worker's URL: a private destination is not
// reachable by an instance an admin did not mark Local, whatever the adapter
// says about it.
func TestRegistry_EgressGuardJudgesTheWorkerURL(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, metalWorker("/mnt/gotham"))
	}))
	t.Cleanup(srv.Close)
	r := newRegistryRig(t)
	ref := integrations.Ref{Kind: integrations.KindAI, Instance: "batcave-remote"}
	if _, err := r.put(ref, ServerAdapterID, false, map[string]any{"url": srv.URL, "concurrency": 1}); err != nil {
		t.Skipf("the registry refuses the instance at Put (%v): the guard is stricter than the adapter needs", err)
	}
	b, release := r.backend(ref)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := b.Caps(ctx); err == nil {
		t.Fatal("a non-local instance reached a loopback worker")
	}
}

// Settings the schema does not admit are refused at Put, so build never sees
// them: a url that is not a URL, a missing concurrency, and (for the local
// adapter) any url at all.
func TestRegistry_SettingsAreValidatedAtPut(t *testing.T) {
	t.Parallel()
	r := newRegistryRig(t)
	n := 0
	for name, c := range map[string]struct {
		adapter  integrations.AdapterID
		settings map[string]any
	}{
		"server: url is not a URL":      {ServerAdapterID, map[string]any{"url": "not a url", "concurrency": 1}},
		"server: no concurrency":        {ServerAdapterID, map[string]any{"url": "http://192.0.2.7:9000"}},
		"server: clip_seconds below 1":  {ServerAdapterID, map[string]any{"url": "http://192.0.2.7:9000", "concurrency": 1, "clip_seconds": -5}},
		"local: carries a url":          {LocalAdapterID, map[string]any{"url": "http://192.0.2.7:9000", "concurrency": 1}},
		"local: M6's local: pseudo-url": {LocalAdapterID, map[string]any{"url": "local:uv-openai-whisper", "concurrency": 1}},
	} {
		n++
		ref := integrations.Ref{Kind: integrations.KindAI, Instance: integrations.InstanceID("bad-" + strconv.Itoa(n))}
		if _, err := r.put(ref, c.adapter, true, c.settings); err == nil {
			t.Errorf("%s: Put accepted %v", name, c.settings)
		}
	}
}

// A whisper_local instance runs through the Registry over the supplied Runner.
func TestRegistry_LocalInstanceEndToEnd(t *testing.T) {
	t.Parallel()
	r := newRegistryRig(t)
	ref := integrations.Ref{Kind: integrations.KindAI, Instance: "batcave-local"}
	if _, err := r.put(ref, LocalAdapterID, true, map[string]any{"concurrency": 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b, release := r.backend(ref)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	caps, err := b.Caps(ctx)
	if err != nil || caps.Locality != ai.LocalityLocal || !slices.Equal(caps.Models, []string{"base.en"}) {
		t.Fatalf("Caps = %+v, %v", caps, err)
	}
	got, err := b.(ai.Transcriber).Transcribe(ctx, "base.en", ai.TranscribeRequest{Audio: ai.AudioSource{Data: wavBytes, MIME: "audio/wav"}})
	if err != nil || got.Text != "Wayne Enterprises" {
		t.Errorf("Transcribe = %+v, %v", got, err)
	}
}
