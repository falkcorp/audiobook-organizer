// file: core/integrations/ai/harness_test.go
// version: 1.0.0
// guid: 3a6f9c18-d4e2-4b75-8e01-c7b25f6a9d34
// last-edited: 2026-11-12

package ai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// The harness: a real integrations.Registry over a booted in-memory store (the
// shape of integrations/metadata's harness), one ai adapter whose instances are
// the coretest.FakeAIs a test hands out by instance ID, a Router over it with a
// native job store and a fake stager, and a clock the test moves. Nothing here
// is shared between tests, so every test runs t.Parallel() (I1).

const (
	secretsOwner storage.Owner = "core/secrets"
	auditOwner   storage.Owner = "core/audit"
)

// fakeAdapter is the adapter ID of the harness's endpoints.
const fakeAdapter integrations.AdapterID = "gotham.fake_ai"

// fakeSettings is the harness adapter's settings: the endpoint routing fields
// and nothing else.
type fakeSettings struct {
	ai.EndpointSettings
}

// clock is the router's and the Registry's clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// started is one apply run a poll staged.
type started struct {
	Kind   tasks.Kind
	Params ai.BatchApplyParams
}

// fakeStager is the kernel's stage-only starter: WriteRuns runs the poll's
// closure in a fresh batch under RetryOnConflict and commits it, StartIn
// records the run the closure staged. A run counts as started only when its
// batch committed.
type fakeStager struct {
	mu       sync.Mutex
	started  []started
	staged   []started // the current attempt's, per goroutine-agnostic simplicity: guarded by mu
	startErr error
	commits  atomic.Int64
	// beforeCommit, when set, runs after the closure staged and before the
	// batch commits: the window a racing writer uses to commit first.
	beforeCommit func()
}

func (s *fakeStager) StartIn(_ context.Context, _ *storage.Batch, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startErr != nil {
		return tasks.RunRef{}, s.startErr
	}
	var p ai.BatchApplyParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return tasks.RunRef{}, err
	}
	s.staged = append(s.staged, started{Kind: kind, Params: p})
	return tasks.RunRef{ID: tasks.RunID(fmt.Sprintf("run-%d", len(s.started)+len(s.staged)))}, nil
}

func (s *fakeStager) WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error {
	return storage.RetryOnConflict(ctx, 5, func(ctx context.Context) error {
		s.mu.Lock()
		s.staged = nil
		s.mu.Unlock()
		b := open()
		defer func() { _ = b.Close() }()
		if err := stage(ctx, b); err != nil {
			return err
		}
		if s.beforeCommit != nil {
			s.beforeCommit()
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			return err
		}
		s.commits.Add(1)
		s.mu.Lock()
		s.started = append(s.started, s.staged...)
		s.staged = nil
		s.mu.Unlock()
		return nil
	})
}

// Started returns the committed apply runs.
func (s *fakeStager) Started() []started {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]started(nil), s.started...)
}

// hookStore wraps the native job store the router writes through. after runs
// once a Transition has read the job and staged its write and before the
// router commits, which is the window a racing writer needs: the hook commits
// a competing write there, so the router's commit must lose to it.
type hookStore struct {
	ai.JobStore
	mu    sync.Mutex
	after func(id ai.BatchJobID, from ai.JobState, next ai.StoredJob)
}

func (s *hookStore) Transition(ctx context.Context, b *storage.Batch, id ai.BatchJobID, from ai.JobState, next ai.StoredJob) error {
	if err := s.JobStore.Transition(ctx, b, id, from, next); err != nil {
		return err
	}
	s.mu.Lock()
	fn := s.after
	s.mu.Unlock()
	if fn != nil {
		fn(id, from, next)
	}
	return nil
}

// onTransition sets the hook (nil clears it).
func (s *hookStore) onTransition(fn func(id ai.BatchJobID, from ai.JobState, next ai.StoredJob)) {
	s.mu.Lock()
	s.after = fn
	s.mu.Unlock()
}

type harness struct {
	t      testing.TB
	reg    *integrations.Registry
	r      *ai.Router
	clk    *clock
	stager *fakeStager
	ks     storage.Keyspace
	jobs   ai.JobStore // the native store, for reads and competing writes
	hooks  *hookStore  // what the router writes through

	prov  *metrics.Provider // nil unless the harness was built withMetrics
	scope *metrics.Scope

	disabled atomic.Bool
	cfgMu    sync.Mutex
	cfg      ai.Config

	mu       sync.Mutex
	backends map[integrations.InstanceID]ai.Backend
}

type harnessOptions struct {
	metrics, noStager bool
	outbound          func(integrations.OutboundSpec) (integrations.HTTP, integrations.Gate)
	adapters          []adapterReg
}

// adapterReg is an adapter the harness registers besides its fake one.
type adapterReg struct {
	origin string
	d      integrations.Descriptor
}

// harnessOption configures newHarness.
type harnessOption func(*harnessOptions)

// withMetrics gives the router the core "ai" scope of a real Provider.
func withMetrics() harnessOption { return func(o *harnessOptions) { o.metrics = true } }

// withOutbound replaces the Registry's outbound builder (the default hands every
// instance a fake HTTP): a real providerhttp gate, for a test of the egress
// guard.
func withOutbound(f func(integrations.OutboundSpec) (integrations.HTTP, integrations.Gate)) harnessOption {
	return func(o *harnessOptions) { o.outbound = f }
}

// withAdapter registers a real adapter beside the harness's fake one.
func withAdapter(origin string, d integrations.Descriptor) harnessOption {
	return func(o *harnessOptions) { o.adapters = append(o.adapters, adapterReg{origin, d}) }
}

// withoutStager builds the router with no Stager, as a service that cannot
// start module runs would be.
func withoutStager() harnessOption { return func(o *harnessOptions) { o.noStager = true } }

// newHarness boots the store, the Registry and the Router.
func newHarness(t testing.TB, opts ...harnessOption) *harness {
	t.Helper()
	var ho harnessOptions
	for _, o := range opts {
		o(&ho)
	}
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			{Owner: instancestore.Owner, Spec: instancestore.StorageSpec(), Enabled: true},
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
			{Owner: ai.Owner, Spec: ai.StorageSpec(), Enabled: true},
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
	clk := newClock()
	h := &harness{t: t, clk: clk, stager: &fakeStager{}, backends: map[integrations.InstanceID]ai.Backend{}}
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
	outbound := (&integrationstest.Outbound{}).Func()
	if ho.outbound != nil {
		outbound = ho.outbound
	}
	reg, err := integrations.New(integrations.Options{
		Store: integrations.NativeInstanceStore(iks), NewBatch: iks.NewBatch,
		Secrets: sec, Audit: au, Outbound: outbound, Now: clk.Now,
	})
	if err != nil {
		t.Fatalf("New registry: %v", err)
	}
	schema, err := config.SchemaFor[fakeSettings]()
	if err != nil {
		t.Fatal(err)
	}
	build := func(_ context.Context, _ integrations.Env, cfg integrations.InstanceConfig) (ai.Backend, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		b, ok := h.backends[cfg.Ref.Instance]
		if !ok {
			return nil, fmt.Errorf("the harness has no backend for %s", cfg.Ref)
		}
		return b, nil
	}
	for _, a := range ho.adapters {
		if err := reg.Register(a.origin, a.d); err != nil {
			t.Fatalf("Register %s: %v", a.origin, err)
		}
	}
	if err := reg.Register("gotham", ai.Adapter(integrations.Meta{ID: fakeAdapter, DisplayName: "Bat-AI", Version: "1.0.0", Settings: schema}, build)); err != nil {
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
	h.ks = keyspaceOf(t, db, ai.Owner)
	h.jobs = ai.NativeJobStore(h.ks)
	h.hooks = &hookStore{JobStore: h.jobs}
	if ho.metrics {
		p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
		sc, err := p.Core("ai", ai.ScopeKeys()...)
		if err != nil {
			t.Fatal(err)
		}
		h.prov, h.scope = p, sc
	}
	var stager tasks.Stager = h.stager
	if ho.noStager {
		stager = nil
	}
	r, err := ai.NewRouter(ai.RouterOptions{
		Registry: reg, Disabled: h.disabled.Load, Jobs: h.hooks, NewBatch: h.ks.NewIndexedBatch,
		Stager: stager, Now: clk.Now, Metrics: h.scope,
		Config: func() ai.Config {
			h.cfgMu.Lock()
			defer h.cfgMu.Unlock()
			return h.cfg
		},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Router.Close: %v", err)
		}
	})
	h.r = r
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

// setConfig replaces the router's live core.ai section.
func (h *harness) setConfig(c ai.Config) {
	h.cfgMu.Lock()
	h.cfg = c
	h.cfgMu.Unlock()
}

// aiRef is the ref of the ai instance named name.
func aiRef(name string) integrations.Ref {
	return integrations.Ref{Kind: integrations.KindAI, Instance: integrations.InstanceID(name)}
}

// settingsMap is set as the Registry stores it: its JSON object.
func settingsMap(t testing.TB, set ai.EndpointSettings) map[string]any {
	t.Helper()
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// textEndpoint is the settings of an endpoint that ticks caps for text work
// with one model.
func textEndpoint(model string, caps ...string) ai.EndpointSettings {
	return ai.EndpointSettings{Concurrency: 1, Capabilities: caps, TextModel: model}
}

// addEndpoint stores an enabled ai instance named name with priority and
// settings, served by backend.
func (h *harness) addEndpoint(name string, priority int, set ai.EndpointSettings, backend ai.Backend) integrations.Ref {
	h.t.Helper()
	ref := aiRef(name)
	h.mu.Lock()
	h.backends[ref.Instance] = backend
	h.mu.Unlock()
	h.put(ref, priority, true, set)
	return ref
}

func (h *harness) put(ref integrations.Ref, priority int, enabled bool, set ai.EndpointSettings) {
	h.t.Helper()
	var version uint64
	if cur, ok := h.reg.Config(ref); ok {
		version = cur.Version
	}
	if _, err := h.reg.Put(admin(), integrations.InstanceConfig{
		Ref: ref, Adapter: fakeAdapter, Name: string(ref.Instance), Enabled: enabled, Priority: priority,
		Settings: settingsMap(h.t, set), Version: version,
	}, nil); err != nil {
		h.t.Fatalf("Put %s: %v", ref, err)
	}
}

// edit re-puts ref with a new settings value (the same backend).
func (h *harness) edit(ref integrations.Ref, set ai.EndpointSettings) {
	h.t.Helper()
	cur, ok := h.reg.Config(ref)
	if !ok {
		h.t.Fatalf("no instance %s", ref)
	}
	h.put(ref, cur.Priority, cur.Enabled, set)
}

// capability registers gotham.<name> with sensible texts and returns its handle.
func (h *harness) capability(name string, mut ...func(*ai.CapabilitySpec)) ai.Capability {
	h.t.Helper()
	spec := capSpec(name)
	for _, m := range mut {
		m(&spec)
	}
	c, err := h.r.RegisterCapability("gotham", spec)
	if err != nil {
		h.t.Fatalf("RegisterCapability %s: %v", spec.ID, err)
	}
	return c
}

// capSpec is a valid text capability gotham.<name>.
func capSpec(name string) ai.CapabilitySpec {
	return ai.CapabilitySpec{
		ID: "gotham." + name, Kind: ai.KindText,
		Description:    "Names the villain a riddle points at.",
		DataSent:       "The riddle text, nothing else.",
		TypicalRequest: "A few lines of riddle text.",
	}
}

// text is a one-turn user request.
func text(s string) ai.TextRequest {
	return ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: s}}}}}
}

// fake is a FakeAI with the models and features a test needs.
func fake() *coretest.FakeAI { return coretest.NewFakeAI() }

// newRC is the run context of a task body, as the executor would hand it.
func newRC(t testing.TB) *tasks.RC {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return taskstest.NewFakeRun(ctx, "core.ai.batch-poll").RC()
}

// newApplyRC is the run context of a module's apply run.
func newApplyRC(t testing.TB, kind tasks.Kind) *tasks.RC {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return taskstest.NewFakeRun(ctx, kind).RC()
}

// handler is a BatchHandler.
type handler = func(rc *tasks.RC, job ai.BatchJob, results iter.Seq2[ai.BatchResult, error]) (ai.BatchOutcome, error)

// noopHandler consumes the results and applies nothing.
func noopHandler(_ *tasks.RC, _ ai.BatchJob, results iter.Seq2[ai.BatchResult, error]) (ai.BatchOutcome, error) {
	var out ai.BatchOutcome
	for _, err := range results {
		if err != nil {
			return out, err
		}
		out.Applied++
	}
	return out, nil
}

// applyEffects is the effect set a Batman module declares for its apply task.
func applyEffects() tasks.Effects { return tasks.Writes(tasks.Res("gotham", "riddles")) }

// registerApply registers the apply task of c with h (which records the apply
// kind on the router, as the module's Register does) and returns the
// definition.
func registerApply(t testing.TB, c ai.Capability, h handler) tasks.Definition {
	t.Helper()
	def := ai.BatchApplyTask(c, h, applyEffects())
	if def == nil {
		t.Fatal("BatchApplyTask returned nil")
	}
	return def
}

// competing commits a write of its own to job id, in a separate batch over the
// native store: the racing writer of the one-winner tests. It fails the test
// when its own commit does not succeed, since it must always win its window.
func (h *harness) competing(id ai.BatchJobID, from ai.JobState, to ai.JobState) {
	h.t.Helper()
	ctx := context.Background()
	cur, err := h.jobs.Get(ctx, id)
	if err != nil {
		h.t.Fatalf("competing writer: read %s: %v", id, err)
	}
	if cur.State != from {
		h.t.Fatalf("competing writer: job %s is %s, want %s", id, cur.State, from)
	}
	next := cur
	next.State, next.Updated, next.Payload = to, h.clk.Now(), nil
	b := h.ks.NewIndexedBatch()
	defer func() { _ = b.Close() }()
	if err := h.jobs.Transition(ctx, b, id, from, next); err != nil {
		h.t.Fatalf("competing writer: stage %s -> %s: %v", from, to, err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		h.t.Fatalf("competing writer: commit %s -> %s: %v", from, to, err)
	}
}

// mustJob reads job id.
func (h *harness) mustJob(id ai.BatchJobID) ai.StoredJob {
	h.t.Helper()
	j, err := h.jobs.Get(context.Background(), id)
	if err != nil {
		h.t.Fatalf("job %s: %v", id, err)
	}
	return j
}
