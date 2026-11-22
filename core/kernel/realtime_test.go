// file: core/kernel/realtime_test.go
// version: 0.1.0
// guid: ea705830-7202-4718-96f3-1fbf9e7891ba
// last-edited: 2026-11-20

package kernel

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// Batman names only: the Batcave streams sightings to Oracle.

var (
	rtSighting    = events.NewTopic[kernelSighting]("batcave.sighting.reported.v1")
	rtInvalidated = events.NewTopic[map[string]string]("core.principal.invalidated.v1")
	rtRBACChanged = events.NewTopic[map[string]uint64]("core.rbac.changed.v1")
)

// fakeAuth is realtimeAuth: Revalidate accepts everyone; Refresh counts its
// calls and answers the request principal, or err once one is set.
type fakeAuth struct {
	refreshed atomic.Int64
	err       atomic.Value // error
}

func (f *fakeAuth) Revalidate(context.Context, *auth.Principal) error { return nil }

func (f *fakeAuth) Refresh(_ context.Context, p *auth.Principal) (*auth.Principal, error) {
	f.refreshed.Add(1)
	if err, _ := f.err.Load().(error); err != nil {
		return nil, err
	}
	return p, nil
}

// oracle is a request principal as auth's middleware would bind it, with a
// checker that grants everything.
func oracle() *auth.Principal {
	return &auth.Principal{User: &auth.User{ID: "u-oracle", Username: "oracle", CredentialEpoch: 6},
		Method: auth.MethodSession, CredentialID: "s-cave", Perms: allowAll{}}
}

// fixed is a settings getter a test reloads.
type fixed struct {
	v atomic.Pointer[realtimeSettings]
}

func settingsOf(s realtimeSettings) *fixed {
	f := &fixed{}
	f.v.Store(&s)
	return f
}
func (f *fixed) set(s realtimeSettings) { f.v.Store(&s) }
func (f *fixed) get() realtimeSettings  { return *f.v.Load() }

// TestRealtimeLimits maps the four reloadable core.realtime keys onto the
// hub's Limits, and nothing else.
func TestRealtimeLimits(t *testing.T) {
	s := realtimeSettings{
		RingSize: 8192, ClientBuffer: 64, MaxClients: 32, MaxPerBucket: 4,
		Heartbeat: config.Duration(20 * time.Second), WriteTimeout: config.Duration(7 * time.Second),
		RetryAfter: config.Duration(3 * time.Second), MaxCoalesceKeys: 99,
	}
	want := realtime.Limits{MaxClients: 32, MaxPerBucket: 4, WriteTimeout: 7 * time.Second, RetryAfter: 3 * time.Second}
	if got := realtimeLimits(s); got != want {
		t.Errorf("realtimeLimits = %+v, want %+v", got, want)
	}
}

// TestNewKernelRealtime_LiveReadsSettingsOnEachCall: the hub reads the
// settings getter on every subscribe and refusal, so a reloaded
// max_per_bucket or retry_after reaches the running hub with no rebuild.
func TestNewKernelRealtime_LiveReadsSettingsOnEachCall(t *testing.T) {
	st := settingsOf(realtimeSettings{MaxPerBucket: 1, RetryAfter: config.Duration(7 * time.Second)})
	f := newRTFixtureWith(t, st.get, &fakeAuth{}, nil, false)
	srv := f.serve(t)
	f.stream(t, srv)
	if code, ra := f.refusal(t, srv); code != http.StatusTooManyRequests || ra != "7" {
		t.Fatalf("second stream at max_per_bucket 1: %d Retry-After %q, want 429 and 7", code, ra)
	}
	st.set(realtimeSettings{MaxPerBucket: 2, RetryAfter: config.Duration(2 * time.Second)})
	f.stream(t, srv)
	if code, ra := f.refusal(t, srv); code != http.StatusTooManyRequests || ra != "2" {
		t.Fatalf("third stream after reloading max_per_bucket 2: %d Retry-After %q, want 429 and the reloaded 2", code, ra)
	}
}

// TestNewKernelRealtime_RefreshIsAuths: the hub's Deps.Refresh is the auth
// service's Refresh: a heartbeat calls it, and its definite refusal ends the
// stream with closed{credential_revoked}.
func TestNewKernelRealtime_RefreshIsAuths(t *testing.T) {
	fa := &fakeAuth{}
	f := newRTFixtureWith(t, settingsOf(realtimeSettings{Heartbeat: config.Duration(20 * time.Millisecond)}).get, fa, nil, false)
	srv := f.serve(t)
	lines := f.stream(t, srv)
	deadline := time.Now().Add(10 * time.Second)
	for fa.refreshed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fa.refreshed.Load() == 0 {
		t.Fatal("no heartbeat called the auth service's Refresh")
	}
	fa.err.Store(auth.ErrRevoked)
	waitLine(t, lines, `"reason":"credential_revoked"`)
}

// syncBuf is a log sink safe for the hub's goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestRealtimeOptions maps every core.realtime key onto the hub's options; ring_size
// is passed through unchanged (the section default, 4096, is what sizes the ring for
// the live tail, TestCoreSections_Defaults).
func TestRealtimeOptions(t *testing.T) {
	s := realtimeSettings{
		RingSize: 8192, ClientBuffer: 64, MaxClients: 32, MaxPerBucket: 4,
		Heartbeat: config.Duration(20 * time.Second), WriteTimeout: config.Duration(7 * time.Second),
		RetryAfter: config.Duration(3 * time.Second), MaxCoalesceKeys: 99,
	}
	o := realtimeOptions(s)
	if o.RingSize != 8192 || o.ClientBuffer != 64 || o.MaxClients != 32 || o.MaxPerBucket != 4 ||
		o.Heartbeat != 20*time.Second || o.WriteTimeout != 7*time.Second || o.RetryAfter != 3*time.Second {
		t.Errorf("realtimeOptions(%+v) = %+v", s, o)
	}
	if o.AllowNoControlTopics || o.MaxPayload != 0 {
		t.Errorf("AllowNoControlTopics %v, MaxPayload %d: want false and the hub default", o.AllowNoControlTopics, o.MaxPayload)
	}
	for _, in := range []int{0, 1, 1024, 4096, 4097, -1} {
		if got := realtimeOptions(realtimeSettings{RingSize: in}).RingSize; got != in {
			t.Errorf("ring_size %d -> RingSize %d, want it passed through (the hub refuses a negative one)", in, got)
		}
	}
}

// rtFixture is a bus with the Batcave's realtime topic and, unless
// skipControl, the two control topics, plus a hub built by newKernelRealtime.
type rtFixture struct {
	ke *kernelEvents
	kr *kernelRealtime
}

func newRTFixture(t *testing.T, s realtimeSettings, skipControl bool) *rtFixture {
	t.Helper()
	return newRTFixtureWith(t, settingsOf(s).get, &fakeAuth{}, nil, skipControl)
}

func newRTFixtureWith(t *testing.T, settings func() realtimeSettings, a realtimeAuth, l *slog.Logger, skipControl bool) *rtFixture {
	t.Helper()
	ke := newTestKernelEvents(t, 0, 0)
	core := []events.TopicSpec{}
	if !skipControl {
		core = append(core,
			rtInvalidated.Spec(events.TopicSpec{Permission: rbac.AnyAuthenticated}),
			rtRBACChanged.Spec(events.TopicSpec{Permission: rbac.AnyAuthenticated}))
	}
	if len(core) > 0 {
		if probs := ke.declareCore(core); len(probs) != 0 {
			t.Fatalf("declareCore: %v", probs)
		}
	}
	if probs := ke.declare("batcave", []events.TopicSpec{rtSighting.Spec(events.TopicSpec{
		Permission: rbac.AnyAuthenticated, Realtime: &events.RealtimeMeta{},
	})}); len(probs) != 0 {
		t.Fatalf("declare: %v", probs)
	}
	if l == nil {
		l = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	kr, err := newKernelRealtime(ke.catalog(), settings, a, rtProvider(t), l, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return &rtFixture{ke: ke, kr: kr}
}

// rtProvider is a fresh metrics provider: a core scope registers once per
// provider, so every hub needs its own.
func rtProvider(t *testing.T) *metrics.Provider {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p
}

// TestKernelRealtime_ControlTopicsRequired: with the control topics declared
// the hub's subscriptions register on the bus as core's; without them
// subscriptions is a core problem naming the missing topic (the kernel never
// sets AllowNoControlTopics), and a second call is one too.
func TestKernelRealtime_ControlTopicsRequired(t *testing.T) {
	t.Run("declared", func(t *testing.T) {
		f := newRTFixture(t, realtimeSettings{}, false)
		subs, probs := f.kr.subscriptions()
		if len(probs) != 0 {
			t.Fatalf("subscriptions: %v", probs)
		}
		if len(subs) != 3 {
			t.Errorf("%d subscriptions, want 3 (one realtime topic, two control topics)", len(subs))
		}
		if probs := f.ke.subscribe("", subs); len(probs) != 0 {
			t.Fatalf("subscribe as core: %v", probs)
		}
		if _, probs := f.kr.subscriptions(); len(probs) != 1 || probs[0].Module != "" {
			t.Errorf("second subscriptions = %v, want one core problem", probs)
		}
	})
	t.Run("missing", func(t *testing.T) {
		f := newRTFixture(t, realtimeSettings{}, true)
		subs, probs := f.kr.subscriptions()
		if subs != nil || len(probs) != 1 {
			t.Fatalf("subscriptions = %d subs, %v; want none and one problem", len(subs), probs)
		}
		pr := probs[0]
		if pr.Module != "" || pr.Kind != module.ProblemInvalidRegistration || !strings.Contains(pr.Error(), "core.principal.invalidated.v1") {
			t.Errorf("problem = %+v (%v), want a core ProblemInvalidRegistration naming core.principal.invalidated.v1", pr, pr)
		}
	})
}

// TestNewKernelRealtime_Refusals: no auth service, or no settings getter,
// builds no hub; nor does a setting the hub refuses, or a second hub over the
// same metrics scope.
func TestNewKernelRealtime_Refusals(t *testing.T) {
	ke := newTestKernelEvents(t, 0, 0)
	none := settingsOf(realtimeSettings{}).get
	if _, err := newKernelRealtime(ke.catalog(), none, nil, rtProvider(t), nil, nil); err == nil {
		t.Error("built a hub with no auth service")
	}
	if _, err := newKernelRealtime(ke.catalog(), nil, &fakeAuth{}, rtProvider(t), nil, nil); err == nil {
		t.Error("built a hub with no settings getter")
	}
	if _, err := newKernelRealtime(ke.catalog(), settingsOf(realtimeSettings{RingSize: -1}).get, &fakeAuth{}, rtProvider(t), nil, nil); err == nil {
		t.Error("built a hub with ring_size -1")
	}
	p := rtProvider(t)
	kr, err := newKernelRealtime(ke.catalog(), none, &fakeAuth{}, p, nil, nil)
	if err != nil || kr.log == nil {
		t.Fatalf("nil logger: %v, %v; want a hub with a discard logger", kr, err)
	}
	if _, err := newKernelRealtime(ke.catalog(), none, &fakeAuth{}, p, nil, nil); err == nil {
		t.Error("built a second hub over the same metrics scope")
	}
}

// serve opens the bus with the hub's subscriptions and serves its handler
// with oracle() bound, as auth's middleware would.
func (f *rtFixture) serve(t *testing.T) *httptest.Server {
	t.Helper()
	subs, probs := f.kr.subscriptions()
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	if probs := f.ke.subscribe("", subs); len(probs) != 0 {
		t.Fatal(probs)
	}
	f.ke.freeze()
	if err := f.ke.open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.ke.close(context.Background()) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.kr.hub.Handler().ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), oracle())))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.kr.hub.Close(ctx)
	})
	return srv
}

// stream opens a stream, waits for its opened frame and returns its lines.
func (f *rtFixture) stream(t *testing.T, srv *httptest.Server) <-chan string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/?topic=batcave.*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitLine(t, lines, "core.stream.opened.v1")
	return lines
}

// refusal opens a stream expected to be refused and returns its status and
// Retry-After.
func (f *rtFixture) refusal(t *testing.T, srv *httptest.Server) (int, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/?topic=batcave.*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header.Get("Retry-After")
}

// TestKernelRealtime_ShutdownSendsClosedAndRunReturns: an open stream gets
// core.stream.closed.v1{shutdown} from the RegisterOnShutdown hook, a new
// stream is refused afterwards, and run returns once its context ends.
func TestKernelRealtime_ShutdownSendsClosedAndRunReturns(t *testing.T) {
	f := newRTFixture(t, realtimeSettings{}, false)
	srv := f.serve(t)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- f.kr.run(ctx) }()

	lines := f.stream(t, srv)
	f.kr.closeOnShutdown(5 * time.Second)()
	waitLine(t, lines, `"reason":"shutdown"`)

	if code, _ := f.refusal(t, srv); code != http.StatusServiceUnavailable {
		t.Errorf("a stream opened after shutdown answered %d, want 503", code)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("run = %v, want nil after an orderly close", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after its context ended")
	}
}

// waitLine reads lines until one contains want.
func waitLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before a line containing %q", want)
			}
			if strings.Contains(l, want) {
				return
			}
		case <-deadline:
			t.Fatalf("no line containing %q", want)
		}
	}
}

// allowAll is a checker that grants everything and is not Confined.
type allowAll struct{}

func (allowAll) Has(rbac.Permission) bool        { return true }
func (allowAll) Satisfies(rbac.Requirement) bool { return true }
func (allowAll) Confined() bool                  { return false }
func (allowAll) Concrete() []rbac.Permission     { return nil }
