// file: core/kernel/integrations_build_test.go
// version: 0.1.0
// guid: d02de0fd-acb0-4b6c-b011-f6f2c6cc8dbc
// last-edited: 2026-11-22

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// parts is the sequence of "part=<name>" values of the log lines that contain
// marker, in order.
func (s *syncBuf) parts(marker string) []string {
	var out []string
	for line := range strings.SplitSeq(s.String(), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		for f := range strings.FieldsSeq(line) {
			if v, ok := strings.CutPrefix(f, "part="); ok {
				out = append(out, v)
			}
		}
	}
	return out
}

// healthEvents collects the integration health events the bus delivers.
type healthEvents struct {
	mu  sync.Mutex
	got []events.Envelope[integrationHealthChanged]
}

func (h *healthEvents) add(e events.Envelope[integrationHealthChanged]) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, e)
}

func (h *healthEvents) all() []events.Envelope[integrationHealthChanged] {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.got)
}

// The adapters of the rig: a core notify adapter with a required credential,
// and a module AI adapter (registered with a module origin).
const (
	integSignalAdapter integrations.AdapterID = "core.batsignal"
	integJokerAdapter  integrations.AdapterID = "gotham.joker_ai"
)

type integSignalSettings struct {
	Password secrets.Ref `json:"password"`
	Channel  string      `json:"channel,omitempty"`
}

type integJokerSettings struct{ ai.EndpointSettings }

var (
	integSignalRef = integrations.Ref{Kind: integrations.KindNotify, Instance: "gcpd-roof"}
	integJokerRef  = integrations.Ref{Kind: integrations.KindAI, Instance: "arkham"}
)

// integJoker is a text AI backend that serves joker-7b.
type integJoker struct{}

func (integJoker) Health(context.Context) integrations.Health {
	return integrations.Health{Status: integrations.HealthOK}
}
func (integJoker) Close() error { return nil }
func (integJoker) Caps(context.Context) (ai.BackendCaps, error) {
	return ai.BackendCaps{Models: []string{"joker-7b"}}, nil
}

// integRig is one booted store with every owner the integrations flip names,
// the real secret and audit stores, a frozen bus with the flip's topics, a
// real outbox and a fresh metrics provider.
type integRig struct {
	t      *testing.T
	db     *storage.DB
	prov   *metrics.Provider
	bus    *events.Bus
	logs   *syncBuf
	signal *integrationstest.Fake
	health *healthEvents

	fences   int
	mu       sync.Mutex
	integ    integrationsSettings
	library  librarySettings
	signalHC func(context.Context, *integrationstest.FakeInstance) integrations.Health
}

func integAdmin() context.Context {
	return integrationstest.As(context.Background(), auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
}

func (r *integRig) setIntegrations(f func(*integrationsSettings)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.integ)
}

func (r *integRig) setLibrary(f func(*librarySettings)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(&r.library)
}

// effectiveLibrary is the core.library section as the config store holds it
// when no operator has set a key: the declared defaults. It is what the flip's
// Library reader returns, so the rig does not hand library a zero struct (which
// marshals to explicit zeros and turns off its free-space floor).
func effectiveLibrary(t testing.TB) librarySettings {
	t.Helper()
	st, err := config.NewStore(config.Options{AppID: "gotham", Environ: []string{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	handles, err := config.RegisterSections(st.Registry(), coreSections()...)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, h := range handles {
		if lh, ok := h.(*config.Handle[librarySettings]); ok {
			return *lh.Get()
		}
	}
	t.Fatal("coreSections() has no core.library section")
	return librarySettings{}
}

// newIntegRig boots the rig. edit adjusts the inputs before they are returned.
func newIntegRig(t *testing.T, edit func(*integrationsInputs)) (*integRig, integrationsInputs) {
	t.Helper()
	ctx := context.Background()
	owners := append(integrationsOwners(), secretsOwnerSpec(), auditOwnerSpec(), outboxOwnerSpec())
	db := bootAuditDB(t, owners...)
	prov, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prov.Shutdown(ctx) })

	r := &integRig{t: t, db: db, prov: prov, logs: &syncBuf{}, signal: &integrationstest.Fake{}, health: &healthEvents{},
		library: effectiveLibrary(t)}

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", integrationsTopicSpecs()...); err != nil {
		t.Fatal(err)
	}
	if err := bus.Subscribe("core", events.On(integrationHealthTopic(), "core.test-integration-health",
		func(_ context.Context, e events.Envelope[integrationHealthChanged]) { r.health.add(e) })); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close(ctx) })
	r.bus = bus

	hr := health.NewRegistry()
	rep, err := hr.Register(outboxHealthCheck())
	if err != nil {
		t.Fatal(err)
	}
	ko, err := newKernelOutbox(db, prov, bus.Catalog(), nil, rep, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatal(err)
	}

	sec := openTestSecrets(t, db, t.TempDir(), nil)
	au, err := buildAudit(ctx, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	debug := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	in := integrationsInputs{
		AppID:    "gotham",
		Keyspace: db.Keyspace,
		Secrets:  sec.store,
		Integrations: func() integrationsSettings {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.integ
		},
		Library: func() librarySettings {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.library
		},
		Metrics:   prov,
		Catalog:   bus.Catalog,
		Publisher: func() events.Publisher { return bus },
		Outbox:    ko.ob,
		Audit:     au,
		Adapters: []integrations.Descriptor{r.signal.Descriptor(integrations.Meta{
			ID: integSignalAdapter, Kind: integrations.KindNotify, DisplayName: "Bat-Signal", Settings: integSchema[integSignalSettings](t),
		})},
		InstallID: "wayne-manor",
		Log:       func(component string) *slog.Logger { return debug.With("component", component) },
	}
	r.signal.HealthFn = func(ctx context.Context, i *integrationstest.FakeInstance) integrations.Health {
		r.mu.Lock()
		f := r.signalHC
		r.mu.Unlock()
		if f != nil {
			return f(ctx, i)
		}
		return integrations.Health{Status: integrations.HealthOK}
	}
	if edit != nil {
		edit(&in)
	}
	return r, in
}

func integSchema[T any](t testing.TB) config.Schema {
	t.Helper()
	s, err := config.SchemaFor[T]()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// build builds the services from in and closes them at cleanup.
func (r *integRig) build(in integrationsInputs) *integrationsServices {
	r.t.Helper()
	s, err := buildIntegrations(context.Background(), in)
	if err != nil {
		r.t.Fatalf("buildIntegrations: %v", err)
	}
	r.t.Cleanup(func() { _ = s.close(context.Background()) })
	return s
}

// buildLoaded builds, registers the module AI adapter and loads.
func (r *integRig) buildLoaded(in integrationsInputs) *integrationsServices {
	r.t.Helper()
	s := r.build(in)
	if err := s.registerAdapters("gotham", ai.Adapter(integrations.Meta{
		ID: integJokerAdapter, DisplayName: "Bat-AI", Version: "1.0.0", Settings: integSchema[integJokerSettings](r.t),
	}, func(context.Context, integrations.Env, integrations.InstanceConfig) (ai.Backend, error) {
		return integJoker{}, nil
	})); err != nil {
		r.t.Fatalf("registerAdapters: %v", err)
	}
	if err := s.load(context.Background(), nil); err != nil {
		r.t.Fatalf("load: %v", err)
	}
	return s
}

func integSignalConfig(version uint64) integrations.InstanceConfig {
	return integrations.InstanceConfig{
		Ref: integSignalRef, Adapter: integSignalAdapter, Name: "GCPD roof", Enabled: true,
		Settings: map[string]any{"channel": "gcpd"}, Version: version,
	}
}

func integPassword(v string) map[string]secrets.Value {
	return map[string]secrets.Value{"password": secrets.NewValue(v)}
}

// TestBuildIntegrations_OrderAndClose: the parts are built in the fixed order
// (providerhttp, Registry, ai, library, downloads, notify) and stopped in the
// reverse of it, and a second close stops nothing.
func TestBuildIntegrations_OrderAndClose(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.build(in)
	// All six steps run in the fixed order; the Pool and notify have nothing
	// to stop, so only four parts are closed.
	wantSteps := []string{"providerhttp", "integrations", "ai", "library", "downloads", "notify"}
	if got := r.logs.parts("step done"); !slices.Equal(got, wantSteps) {
		t.Fatalf("steps %v, want %v", got, wantSteps)
	}
	if got, want := r.logs.parts("part built"), []string{"providerhttp", "integrations", "ai", "library"}; !slices.Equal(got, want) {
		t.Fatalf("built %v, want %v", got, want)
	}
	if s.providerhttp == nil || s.registry == nil || s.ai == nil || s.library == nil || s.downloads == nil || s.notify == nil {
		t.Fatalf("a part was not built: %+v", s)
	}
	if err := s.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, want := r.logs.parts("part closed"), []string{"library", "ai", "integrations", "providerhttp"}; !slices.Equal(got, want) {
		t.Fatalf("closed %v, want the reverse %v", got, want)
	}
	if err := s.close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if n := len(r.logs.parts("part closed")); n != 4 {
		t.Fatalf("a second close stopped %d more part(s)", n-4)
	}
	// The effects: the Registry refuses leases once closed.
	if _, err := s.registry.Health(context.Background(), integSignalRef); !errors.Is(err, integrations.ErrClosed) {
		t.Fatalf("Health after close = %v, want ErrClosed", err)
	}
}

// TestBuildIntegrations_PartialFailureClosesWhatWasBuilt: notify is built last
// and fails without an outbox, so the four parts before it are closed, in
// reverse, and no services come back.
func TestBuildIntegrations_PartialFailureClosesWhatWasBuilt(t *testing.T) {
	r, in := newIntegRig(t, func(in *integrationsInputs) { in.Outbox = nil })
	s, err := buildIntegrations(context.Background(), in)
	if err == nil || s != nil {
		t.Fatalf("buildIntegrations = %v, %v; want a failure and no services", s, err)
	}
	if !strings.Contains(err.Error(), "notify") {
		t.Fatalf("the failure does not name the part: %v", err)
	}
	if got, want := r.logs.parts("part closed"), []string{"library", "ai", "integrations", "providerhttp"}; !slices.Equal(got, want) {
		t.Fatalf("closed %v, want %v", got, want)
	}
}

// TestBuildIntegrations_MissingLibraryOwnerNamed: library's owner left out of
// storageOwners fails the build at library, naming the owner.
func TestBuildIntegrations_MissingLibraryOwnerNamed(t *testing.T) {
	r, in := newIntegRig(t, nil)
	real := in.Keyspace
	in.Keyspace = func(o storage.Owner) (storage.Keyspace, bool) {
		if o == library.Owner {
			return storage.Keyspace{}, false
		}
		return real(o)
	}
	_, err := buildIntegrations(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), string(library.Owner)) || !strings.Contains(err.Error(), "storageOwners") {
		t.Fatalf("buildIntegrations = %v, want a failure naming %s and storageOwners", err, library.Owner)
	}
	if got, want := r.logs.parts("part closed"), []string{"ai", "integrations", "providerhttp"}; !slices.Equal(got, want) {
		t.Fatalf("closed %v, want %v", got, want)
	}
}

// TestBuildIntegrations_ScopesAreTakenOncePerConstructor: each constructor
// creates its own core scope from the provider. A second build over the same
// provider is refused at the first scope; a fresh provider builds.
func TestBuildIntegrations_ScopesAreTakenOncePerConstructor(t *testing.T) {
	r, in := newIntegRig(t, nil)
	r.build(in)
	if _, err := buildIntegrations(context.Background(), in); !errors.Is(err, metrics.ErrScopeTaken) {
		t.Fatalf("second build over one provider = %v, want ErrScopeTaken", err)
	}
	for _, sub := range []string{"providerhttp", "integrations", "ai", "library", "download", "notify"} {
		if _, err := in.Metrics.Core(sub); !errors.Is(err, metrics.ErrScopeTaken) {
			t.Errorf("scope %q is free after a build (%v); a constructor did not take it", sub, err)
		}
	}
}

// TestBuildIntegrations_TypedNilsAreNormalised: an interface holding a nil
// *audit.Store or nil *secrets.Store would pass the constructors' nil checks
// and fail on first use, so the build refuses them up front as the missing
// inputs they are.
func TestBuildIntegrations_TypedNilsAreNormalised(t *testing.T) {
	t.Run("audit", func(t *testing.T) {
		_, in := newIntegRig(t, func(in *integrationsInputs) { in.Audit = (*audit.Store)(nil) })
		if s, err := buildIntegrations(context.Background(), in); err == nil {
			_ = s.close(context.Background())
			t.Fatal("a typed-nil audit logger built")
		}
	})
	t.Run("secrets", func(t *testing.T) {
		_, in := newIntegRig(t, func(in *integrationsInputs) { in.Secrets = (*secrets.Store)(nil) })
		if s, err := buildIntegrations(context.Background(), in); err == nil {
			_ = s.close(context.Background())
			t.Fatal("a typed-nil secret store built")
		}
	})
}

// TestBuildIntegrations_SecretPutReleasesAuthHold: a Put that writes a new
// credential releases the ClassAuth hold the old one earned (ReleaseAuthHold is
// providerhttp's); a Put that writes none leaves the hold. The Put is made by
// an interactive admin: a secret write needs core:admin (W-2).
func TestBuildIntegrations_SecretPutReleasesAuthHold(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.buildLoaded(in)
	ctx := integAdmin()
	if _, err := s.registry.Put(ctx, integSignalConfig(0), integPassword("riddle-me-this")); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	hold := func() {
		g := s.providerhttp.GateFor(integSignalRef, providerhttp.Policy{})
		tk, err := g.Acquire(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		tk.Done(&integrations.Error{Class: integrations.ClassAuth, Status: 401})
		if _, held := s.providerhttp.HoldFor(integSignalRef); !held {
			t.Fatal("a 401 left no auth hold")
		}
	}
	hold()
	cur, _ := s.registry.Config(integSignalRef)
	if _, err := s.registry.Put(ctx, integSignalConfig(cur.Version), nil); err != nil {
		t.Fatalf("Put without a secret: %v", err)
	}
	if _, held := s.providerhttp.HoldFor(integSignalRef); !held {
		t.Fatal("a Put that wrote no credential released the hold")
	}
	cur, _ = s.registry.Config(integSignalRef)
	if _, err := s.registry.Put(ctx, integSignalConfig(cur.Version), integPassword("the-bat-is-back")); err != nil {
		t.Fatalf("Put with a secret: %v", err)
	}
	if h, held := s.providerhttp.HoldFor(integSignalRef); held {
		t.Fatalf("the auth hold survived a Put that wrote new credentials: %+v", h)
	}
}

// TestBuildIntegrations_LANAllowAndDisabled: core.integrations.egress.lan_allow
// reaches providerhttp only, and core.integrations.disabled reaches all three
// sinks (providerhttp, the Registry, the ai Router), each read per call.
func TestBuildIntegrations_LANAllowAndDisabled(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.buildLoaded(in)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }))
	t.Cleanup(srv.Close)
	client := s.providerhttp.Client(integrations.Ref{Kind: integrations.KindNotify, Instance: "wayne-perch"}, providerhttp.Policy{})
	get := func() error {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	t.Run("lan_allow", func(t *testing.T) {
		if err := get(); !errors.Is(err, providerhttp.ErrBlockedAddress) {
			t.Fatalf("a loopback host with no lan_allow = %v, want ErrBlockedAddress", err)
		}
		r.setIntegrations(func(v *integrationsSettings) { v.Egress.LANAllow = []string{"127.0.0.1"} })
		if err := get(); err != nil {
			t.Fatalf("a loopback host on lan_allow: %v", err)
		}
	})

	// The ai fixture: a text endpoint that serves gotham.riddle.
	ctx := integAdmin()
	c, err := s.ai.RegisterCapability("gotham", ai.CapabilitySpec{
		ID: "gotham.riddle", Kind: ai.KindText, Description: "Names the villain a riddle points at.",
		DataSent: "The riddle text, nothing else.", TypicalRequest: "A few lines of riddle text.",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ai.EndpointSettings{Concurrency: 1, Capabilities: []string{"gotham.riddle"}, TextModel: "joker-7b"})
	var settings map[string]any
	_ = json.Unmarshal(raw, &settings)
	if _, err := s.registry.Put(ctx, integrations.InstanceConfig{
		Ref: integJokerRef, Adapter: integJokerAdapter, Name: "Arkham", Enabled: true, Priority: 1, Settings: settings,
	}, nil); err != nil {
		t.Fatalf("Put ai endpoint: %v", err)
	}
	eps, _, err := s.ai.Candidates(c)
	if err != nil || len(eps) != 1 {
		t.Fatalf("Candidates before the switch = %v, %v; want the endpoint (the fixture is not valid)", eps, err)
	}

	r.setIntegrations(func(v *integrationsSettings) { v.Disabled = true })

	t.Run("providerhttp", func(t *testing.T) {
		if err := get(); !errors.Is(err, integrations.ErrDisabled) {
			t.Fatalf("a call while disabled = %v, want ErrDisabled", err)
		}
	})
	t.Run("registry", func(t *testing.T) {
		if _, err := s.registry.Test(ctx, integSignalConfig(0), integPassword("riddle-me-this")); !errors.Is(err, integrations.ErrDisabled) {
			t.Fatalf("Test while disabled = %v, want ErrDisabled", err)
		}
	})
	t.Run("ai", func(t *testing.T) {
		eps, refusals, err := s.ai.Candidates(c)
		if err != nil {
			t.Fatal(err)
		}
		if len(eps) != 0 || len(refusals) != 1 || !strings.Contains(refusals[0].Reason, "integrations disabled") {
			t.Fatalf("Candidates while disabled = %v, %v; want one disabled refusal", eps, refusals)
		}
	})
	r.setIntegrations(func(v *integrationsSettings) { v.Disabled = false })
	if eps, _, _ := s.ai.Candidates(c); len(eps) != 1 {
		t.Fatalf("the switch is not read per call: %d candidates after it was cleared", len(eps))
	}
}

// healthFence waits until the bus has delivered every health event published
// before the call, by publishing a marker on the same topic: a subscriber's
// queue is FIFO, so the marker arrives after everything ahead of it. It returns
// the events of the real instances, in delivery order, without the markers.
func (r *integRig) healthFence() []events.Envelope[integrationHealthChanged] {
	r.t.Helper()
	r.fences++
	marker := "fence-" + strconv.Itoa(r.fences)
	events.Publish(r.bus, context.Background(), integrationHealthTopic(), marker, integrationHealthChanged{Instance: marker})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		all := r.health.all()
		for _, e := range all {
			if e.Key == marker {
				var out []events.Envelope[integrationHealthChanged]
				for _, e := range all {
					if !strings.HasPrefix(e.Key, "fence-") {
						out = append(out, e)
					}
				}
				return out
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.t.Fatalf("the health fence %s never arrived", marker)
	return nil
}

func (r *integRig) setSignalHealth(st integrations.HealthStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signalHC = func(context.Context, *integrationstest.FakeInstance) integrations.Health {
		return integrations.Health{Status: st, Message: "http://192.0.2.9/leak?token=joker"}
	}
}

func healthStatuses(es []events.Envelope[integrationHealthChanged]) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Payload.Status)
	}
	return out
}

// TestIntegrationsHealth_PublishesOnMoveOnly: the health topic carries one
// event for each status move and none for a probe that leaves the status where
// it was, with fixed tokens only. The first observation of an instance that is
// ok is not a move worth a warning-severity event (TestIntegrationsHealth_
// FirstObservation).
func TestIntegrationsHealth_PublishesOnMoveOnly(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.buildLoaded(in)
	ctx := context.Background()
	if _, err := s.registry.Put(integAdmin(), integSignalConfig(0), integPassword("riddle-me-this")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if err := s.probeAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.healthFence(); len(got) != 0 {
		t.Fatalf("%d health events after four probes of one ok status, want none: %v", len(got), healthStatuses(got))
	}

	r.setSignalHealth(integrations.HealthDown)
	for range 3 {
		if err := s.probeAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got := r.healthFence()
	if len(got) != 1 {
		t.Fatalf("%d health events after one move probed three times, want 1: %v", len(got), healthStatuses(got))
	}
	last := got[0]
	if last.Payload != (integrationHealthChanged{Kind: "notify", Instance: "gcpd-roof", Status: "down"}) {
		t.Fatalf("payload = %+v", last.Payload)
	}
	if last.Key != integSignalRef.String() {
		t.Fatalf("key = %q, want %q", last.Key, integSignalRef.String())
	}
	raw, _ := json.Marshal(last.Payload)
	if strings.Contains(string(raw), "192.0.2.9") || strings.Contains(string(raw), "joker") {
		t.Fatalf("the payload carries the probe's message: %s", raw)
	}
}

// TestIntegrationsHealth_FirstObservation: every restart observes every
// instance once, so an instance whose first status is ok is not published (it
// would notify the operator of every instance on every boot), one whose first
// status is not ok is (booting into a broken state is news), and a later move
// to ok is a recovery and is published. A ref that is deleted and created again
// is observed first again.
func TestIntegrationsHealth_FirstObservation(t *testing.T) {
	t.Run("ok first is not published, a recovery is", func(t *testing.T) {
		r, in := newIntegRig(t, nil)
		s := r.buildLoaded(in)
		if _, err := s.registry.Put(integAdmin(), integSignalConfig(0), integPassword("riddle-me-this")); err != nil {
			t.Fatal(err)
		}
		if err := s.probeAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := r.healthFence(); len(got) != 0 {
			t.Fatalf("a first observation of ok was published: %v", healthStatuses(got))
		}
		r.setSignalHealth(integrations.HealthDown)
		_ = s.probeAll(context.Background())
		r.setSignalHealth(integrations.HealthOK)
		_ = s.probeAll(context.Background())
		if got := healthStatuses(r.healthFence()); !slices.Equal(got, []string{"down", "ok"}) {
			t.Fatalf("published %v, want the move to down and the recovery to ok", got)
		}
	})
	t.Run("not ok first is published", func(t *testing.T) {
		r, in := newIntegRig(t, nil)
		s := r.buildLoaded(in)
		r.setSignalHealth(integrations.HealthDown)
		if _, err := s.registry.Put(integAdmin(), integSignalConfig(0), integPassword("riddle-me-this")); err != nil {
			t.Fatal(err)
		}
		if err := s.probeAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := healthStatuses(r.healthFence()); !slices.Equal(got, []string{"down"}) {
			t.Fatalf("published %v, want the first observation, down", got)
		}
	})
	t.Run("a recreated ref is observed first again", func(t *testing.T) {
		r, in := newIntegRig(t, nil)
		s := r.buildLoaded(in)
		r.setSignalHealth(integrations.HealthDown)
		put := func() {
			t.Helper()
			if _, err := s.registry.Put(integAdmin(), integSignalConfig(0), integPassword("riddle-me-this")); err != nil {
				t.Fatal(err)
			}
			if err := s.probeAll(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		put()
		if err := s.registry.Delete(integAdmin(), integSignalRef); err != nil {
			t.Fatal(err)
		}
		put()
		if got := healthStatuses(r.healthFence()); !slices.Equal(got, []string{"down", "down"}) {
			t.Fatalf("published %v, want one first observation per incarnation", got)
		}
	})
}

// TestIntegrationsHealth_PublishesInRecordedOrder: the Registry calls the hook
// with its health lock held, so two probes of one ref cannot report their
// statuses in the other order from the one they were recorded in. The first
// publish is parked inside the hook (the Publisher getter is called there); a
// second probe with a different status must not reach the hook until the first
// is released, and the events then arrive in the order recorded.
func TestIntegrationsHealth_PublishesInRecordedOrder(t *testing.T) {
	r, in := newIntegRig(t, nil)
	var (
		calls   atomic.Int32
		entered = make(chan struct{})
		release = make(chan struct{})
		once    sync.Once
	)
	unpark := func() { once.Do(func() { close(release) }) }
	in.Publisher = func() events.Publisher {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return r.bus
	}
	s := r.buildLoaded(in)
	t.Cleanup(unpark) // before the build's own close: a failing run must not leave a probe parked under the health lock
	if _, err := s.registry.Put(integAdmin(), integSignalConfig(0), integPassword("riddle-me-this")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	probe := func() chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = s.registry.Health(ctx, integSignalRef)
		}()
		return done
	}

	r.setSignalHealth(integrations.HealthDown) // the first observation: published, parked in the hook
	first := probe()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first probe never reached the hook")
	}
	r.setSignalHealth(integrations.HealthDegraded) // a move: must wait behind the parked hook
	second := probe()
	time.Sleep(200 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d hook calls while the first was parked, want 1: a second probe reported past the first", n)
	}
	unpark()
	for _, d := range []chan struct{}{first, second} {
		select {
		case <-d:
		case <-time.After(5 * time.Second):
			t.Fatal("a probe never finished")
		}
	}
	if got := healthStatuses(r.healthFence()); !slices.Equal(got, []string{"down", "degraded"}) {
		t.Fatalf("published %v, want the recorded order [down degraded]", got)
	}
}

// TestIntegrationsHealth_ConcurrentProbesNeverRepeatAStatus: many concurrent
// probes of one flapping instance. Each published event is a move from the
// one before it, because the moves are published in the order the Registry
// recorded them; a reordered pair would leave two adjacent events with the same
// status. Run under -race.
func TestIntegrationsHealth_ConcurrentProbesNeverRepeatAStatus(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.buildLoaded(in)
	if _, err := s.registry.Put(integAdmin(), integSignalConfig(0), integPassword("riddle-me-this")); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	r.mu.Lock()
	r.signalHC = func(context.Context, *integrationstest.FakeInstance) integrations.Health {
		if n.Add(1)%2 == 0 {
			return integrations.Health{Status: integrations.HealthOK}
		}
		return integrations.Health{Status: integrations.HealthDown}
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 40 {
				_, _ = s.registry.Health(context.Background(), integSignalRef)
			}
		})
	}
	wg.Wait()
	got := healthStatuses(r.healthFence())
	if len(got) < 2 {
		t.Fatalf("only %d health events from 320 flapping probes: %v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Fatalf("events %d and %d both report %q: published out of the recorded order (%v)", i-1, i, got[i], got)
		}
	}
}

// TestDepsFor: a module's Deps take every integrations service, a library
// Scope with the module as its owner, and a notify publisher bound to the
// module (it refuses another module's topic). An empty module ID has no Scope
// and no publishable topic, and says so in the log instead of failing.
func TestDepsFor(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.build(in)

	d := s.depsFor("gotham")
	if d.HTTP != s.providerhttp || d.Integrations != s.registry || d.Downloads != s.downloads || d.AI != s.ai {
		t.Fatalf("depsFor did not hand out the built services: %+v", d)
	}
	if d.Library == nil || d.Library.Owner() != "gotham" {
		t.Fatalf("library scope = %v, want one owned by gotham", d.Library)
	}
	err := d.Notify.Publish(context.Background(), nil, notify.Notification{Topic: "arkham.inmate.escaped.v1"})
	if !errors.Is(err, notify.ErrForeignTopic) {
		t.Fatalf("a gotham publisher published an arkham topic: %v", err)
	}
	other := s.depsFor("arkham")
	if o := other.Library.Owner(); o != "arkham" {
		t.Fatalf("second module's scope owner = %q", o)
	}
	err = other.Notify.Publish(context.Background(), nil, notify.Notification{Topic: "gotham.signal.raised.v1"})
	if !errors.Is(err, notify.ErrForeignTopic) {
		t.Fatalf("an arkham publisher published a gotham topic: %v", err)
	}

	empty := s.depsFor("")
	if empty.Library != nil {
		t.Fatalf("an empty module ID got a library scope: %v", empty.Library)
	}
	if empty.HTTP == nil || empty.Notify == nil {
		t.Fatalf("an empty module ID lost its other services: %+v", empty)
	}
	if !strings.Contains(r.logs.String(), "no library scope for module") {
		t.Fatal("the missing scope was not logged")
	}
}

// TestFreezeAI: the AI catalog takes capabilities until freezeAI and none
// after it.
func TestFreezeAI(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.build(in)
	spec := func(id string) ai.CapabilitySpec {
		return ai.CapabilitySpec{
			ID: id, Kind: ai.KindText, Description: "Names the villain a riddle points at.",
			DataSent: "The riddle text, nothing else.", TypicalRequest: "A few lines of riddle text.",
		}
	}
	if _, err := s.ai.RegisterCapability("gotham", spec("gotham.riddle")); err != nil {
		t.Fatalf("before the freeze: %v", err)
	}
	s.freezeAI()
	if _, err := s.ai.RegisterCapability("gotham", spec("gotham.clue")); !errors.Is(err, ai.ErrCatalogFrozen) {
		t.Fatalf("after the freeze = %v, want ErrCatalogFrozen", err)
	}
	s.freezeAI() // idempotent
}
