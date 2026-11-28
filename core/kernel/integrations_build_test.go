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
	"strings"
	"sync"
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

	r := &integRig{t: t, db: db, prov: prov, logs: &syncBuf{}, signal: &integrationstest.Fake{}, health: &healthEvents{}}

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
	// Pool and notify have nothing to stop, so only four parts are closed.
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

// TestIntegrationsHealth_PublishesOnMoveOnly: the health topic carries one
// event for the first observation of an instance and one for each status move,
// not one per probe, with fixed tokens only.
func TestIntegrationsHealth_PublishesOnMoveOnly(t *testing.T) {
	r, in := newIntegRig(t, nil)
	s := r.buildLoaded(in)
	ctx := context.Background()
	if _, err := s.registry.Put(integAdmin(), integSignalConfig(0), integPassword("riddle-me-this")); err != nil {
		t.Fatal(err)
	}
	wait := func(n int) []events.Envelope[integrationHealthChanged] {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if got := r.health.all(); len(got) >= n {
				return got
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("%d health event(s), want %d", len(r.health.all()), n)
		return nil
	}
	settle := func() { time.Sleep(100 * time.Millisecond) }

	base := len(r.health.all())
	if err := s.probeAll(ctx); err != nil {
		t.Fatal(err)
	}
	wait(base + 1)
	for range 3 {
		if err := s.probeAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	settle()
	if n := len(r.health.all()); n != base+1 {
		t.Fatalf("%d health events after four probes of one status, want %d (first observation only)", n, base+1)
	}

	r.mu.Lock()
	r.signalHC = func(context.Context, *integrationstest.FakeInstance) integrations.Health {
		return integrations.Health{Status: integrations.HealthDown, Message: "http://192.0.2.9/leak?token=joker"}
	}
	r.mu.Unlock()
	if err := s.probeAll(ctx); err != nil {
		t.Fatal(err)
	}
	got := wait(base + 2)
	settle()
	if n := len(r.health.all()); n != base+2 {
		t.Fatalf("%d health events after one move, want %d", n, base+2)
	}
	last := got[len(got)-1]
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
