// file: core/kernel/realtime_test.go
// version: 0.1.0
// guid: ea705830-7202-4718-96f3-1fbf9e7891ba
// last-edited: 2026-11-20

package kernel

import (
	"bufio"
	"context"
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

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only: the Batcave streams sightings; Oracle holds keys.

var (
	rtSighting    = events.NewTopic[kernelSighting]("batcave.sighting.reported.v1")
	rtInvalidated = events.NewTopic[map[string]string]("core.principal.invalidated.v1")
	rtRBACChanged = events.NewTopic[map[string]uint64]("core.rbac.changed.v1")
)

// fakeAuth is realtimeAuth: PrincipalFor records what it was asked and
// answers with a principal whose checker grants exactly the roles' or the
// scopes' permissions; Revalidate accepts everyone.
type fakeAuth struct {
	mu    sync.Mutex
	calls []principalForCall
	err   error
}

type principalForCall struct {
	user   auth.UserID
	method auth.Method
	scopes []rbac.Permission
	epoch  int64
}

func (f *fakeAuth) Revalidate(context.Context, *auth.Principal) error { return nil }

func (f *fakeAuth) PrincipalFor(_ context.Context, u *auth.User, m auth.Method, scopes []rbac.Permission, epoch int64) (*auth.Principal, error) {
	f.mu.Lock()
	f.calls = append(f.calls, principalForCall{user: u.ID, method: m, scopes: slices.Clone(scopes), epoch: epoch})
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &auth.Principal{User: u, Method: m}, nil
}

func (f *fakeAuth) last(t *testing.T) principalForCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("PrincipalFor was not called")
	}
	return f.calls[len(f.calls)-1]
}

// fakeCreds is realtimeCredentials over two maps, with an injectable error.
type fakeCreds struct {
	mu      sync.Mutex
	users   map[auth.UserID]*auth.User
	keys    map[string]*auth.APIKey
	userErr error
	keyErr  error
}

func (f *fakeCreds) GetUser(_ context.Context, id auth.UserID) (*auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.userErr != nil {
		return nil, f.userErr
	}
	u, ok := f.users[id]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *fakeCreds) APIKeyByID(_ context.Context, id string) (*auth.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keyErr != nil {
		return nil, f.keyErr
	}
	k, ok := f.keys[id]
	if !ok {
		return nil, auth.ErrNotFound
	}
	cp := *k
	return &cp, nil
}

// gothamCreds stores Oracle (two roles, epoch 7) with one active key scoped
// to a single permission, one disabled key, and one key Robin owns.
func gothamCreds() *fakeCreds {
	return &fakeCreds{
		users: map[auth.UserID]*auth.User{
			"u-oracle": {ID: "u-oracle", Username: "oracle", Roles: []rbac.RoleName{"batcave.keeper", "arkham.warden"}, Status: auth.StatusActive, CredentialEpoch: 7},
			"u-robin":  {ID: "u-robin", Username: "robin", Status: auth.StatusActive, CredentialEpoch: 1},
		},
		keys: map[string]*auth.APIKey{
			"k-batcomputer": {ID: "k-batcomputer", Owner: "u-oracle", Scopes: []rbac.Permission{"batcave:view"}, Status: auth.KeyActive},
			"k-disabled":    {ID: "k-disabled", Owner: "u-oracle", Scopes: []rbac.Permission{"batcave:view"}, Status: auth.KeyDisabled},
			"k-robin":       {ID: "k-robin", Owner: "u-robin", Scopes: []rbac.Permission{"batcave:view"}, Status: auth.KeyActive},
			"k-empty":       {ID: "k-empty", Owner: "u-oracle", Status: auth.KeyActive},
		},
	}
}

// oracle is a request principal as auth's middleware would bind it: the
// user's record as read at sign-in (epoch 6: the store has moved since).
func oracle(m auth.Method, cred string) *auth.Principal {
	return &auth.Principal{User: &auth.User{ID: "u-oracle", Username: "oracle", CredentialEpoch: 6}, Method: m, CredentialID: cred}
}

// TestRefreshPrincipal_RebuildsFromTheStore: a session principal is rebuilt
// over the user as stored now (roles via nil scopes, the stored epoch), a key
// principal over the stored key's own scopes, and both keep their method and
// credential ID.
func TestRefreshPrincipal_RebuildsFromTheStore(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     auth.Method
		cred       string
		wantScopes []rbac.Permission
	}{
		{"session", auth.MethodSession, "s-cave", nil},
		{"delegated session", auth.MethodSessionDelegated, "s-temp", nil},
		{"api key", auth.MethodAPIKey, "k-batcomputer", []rbac.Permission{"batcave:view"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fa := &fakeAuth{}
			np, err := refreshPrincipal(fa, gothamCreds())(context.Background(), oracle(tc.method, tc.cred))
			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if np.Method != tc.method || np.CredentialID != tc.cred {
				t.Errorf("answer is %q/%q, want the request's %q/%q", np.Method, np.CredentialID, tc.method, tc.cred)
			}
			got := fa.last(t)
			if got.user != "u-oracle" || got.method != tc.method || got.epoch != 7 {
				t.Errorf("PrincipalFor(%s, %q, epoch %d), want (u-oracle, %q, the stored epoch 7)", got.user, got.method, got.epoch, tc.method)
			}
			if !slices.Equal(got.scopes, tc.wantScopes) || (tc.wantScopes == nil) != (got.scopes == nil) {
				t.Errorf("scopes = %#v, want %#v (a key never widens to its owner's roles)", got.scopes, tc.wantScopes)
			}
			if !slices.Equal(np.User.Roles, []rbac.RoleName{"batcave.keeper", "arkham.warden"}) {
				t.Errorf("rebuilt over roles %v, want the stored roles", np.User.Roles)
			}
		})
	}
}

// TestRefreshPrincipal_AuthDisabledPassesThrough: the auth-off principal has
// no user to read and is answered as is, with no store read.
func TestRefreshPrincipal_AuthDisabledPassesThrough(t *testing.T) {
	fa, fc := &fakeAuth{}, gothamCreds()
	fc.userErr, fc.keyErr = errors.New("must not be read"), errors.New("must not be read")
	p := &auth.Principal{Method: auth.MethodAuthDisabled}
	np, err := refreshPrincipal(fa, fc)(context.Background(), p)
	if err != nil || np != p {
		t.Fatalf("refresh = %p, %v; want the same principal %p and no error", np, err, p)
	}
	if len(fa.calls) != 0 {
		t.Errorf("PrincipalFor called %d times for the auth-off principal", len(fa.calls))
	}
}

// TestRefreshPrincipal_Refusals: every refusal the hub must treat as definite
// is not auth.ErrStoreUnavailable, and every store failure is, so the hub
// retries the second kind and ends the stream on the first.
func TestRefreshPrincipal_Refusals(t *testing.T) {
	boom := errors.New("pebble: disk on fire")
	for _, tc := range []struct {
		name      string
		p         *auth.Principal
		setup     func(*fakeAuth, *fakeCreds)
		want      error // errors.Is target
		transient bool
	}{
		{"nil principal", nil, nil, auth.ErrInvalidCredentials, false},
		{"session with no user", &auth.Principal{Method: auth.MethodSession, CredentialID: "s-cave"}, nil, auth.ErrInvalidCredentials, false},
		{"session with an empty user ID", &auth.Principal{User: &auth.User{}, Method: auth.MethodSession, CredentialID: "s-cave"}, nil, auth.ErrInvalidCredentials, false},
		{"auth-disabled with a user", oracle(auth.MethodAuthDisabled, ""), nil, errRefreshMethod, false},
		{"compat method", oracle("abs", "k-batcomputer"), nil, errRefreshMethod, false},
		{"no method", oracle(auth.MethodNone, ""), nil, errRefreshMethod, false},
		{"user gone", oracle(auth.MethodSession, "s-cave"), func(_ *fakeAuth, c *fakeCreds) { delete(c.users, "u-oracle") }, auth.ErrRevoked, false},
		{"user undecodable", oracle(auth.MethodSession, "s-cave"), func(_ *fakeAuth, c *fakeCreds) { c.userErr = auth.ErrUndecodableRecord }, auth.ErrUndecodableRecord, false},
		{"user read store down", oracle(auth.MethodSession, "s-cave"), func(_ *fakeAuth, c *fakeCreds) { c.userErr = auth.ErrStoreUnavailable }, auth.ErrStoreUnavailable, true},
		{"user read other failure", oracle(auth.MethodSession, "s-cave"), func(_ *fakeAuth, c *fakeCreds) { c.userErr = boom }, boom, true},
		{"key gone", oracle(auth.MethodAPIKey, "k-joker"), nil, auth.ErrRevoked, false},
		{"key read store down", oracle(auth.MethodAPIKey, "k-batcomputer"), func(_ *fakeAuth, c *fakeCreds) { c.keyErr = auth.ErrStoreUnavailable }, auth.ErrStoreUnavailable, true},
		{"key read other failure", oracle(auth.MethodAPIKey, "k-batcomputer"), func(_ *fakeAuth, c *fakeCreds) { c.keyErr = boom }, boom, true},
		{"key of another user", oracle(auth.MethodAPIKey, "k-robin"), nil, auth.ErrRevoked, false},
		{"key disabled", oracle(auth.MethodAPIKey, "k-disabled"), nil, auth.ErrRevoked, false},
		{"key with no scope", oracle(auth.MethodAPIKey, "k-empty"), nil, auth.ErrInvalidCredentials, false},
		{"principal refused", oracle(auth.MethodSession, "s-cave"), func(a *fakeAuth, _ *fakeCreds) { a.err = auth.ErrInactiveUser }, auth.ErrInactiveUser, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fa, fc := &fakeAuth{}, gothamCreds()
			if tc.setup != nil {
				tc.setup(fa, fc)
			}
			np, err := refreshPrincipal(fa, fc)(context.Background(), tc.p)
			if err == nil {
				t.Fatalf("refresh = %+v, nil; want an error", np)
			}
			if np != nil {
				t.Errorf("refresh answered a principal with its error")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want errors.Is %v", err, tc.want)
			}
			if got := errors.Is(err, auth.ErrStoreUnavailable); got != tc.transient {
				t.Errorf("errors.Is(err, ErrStoreUnavailable) = %v, want %v (err %v)", got, tc.transient, err)
			}
			if !tc.transient && errors.Is(tc.want, auth.ErrRevoked) && len(fa.calls) != 0 {
				t.Errorf("PrincipalFor was called after a definite refusal")
			}
		})
	}
}

// TestRefreshPrincipal_Concurrent: the closure holds no state, so the hub may
// call it from every stream's goroutine at once (run with -race).
func TestRefreshPrincipal_Concurrent(t *testing.T) {
	fa, fc := &fakeAuth{}, gothamCreds()
	refresh := refreshPrincipal(fa, fc)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			m, cred := auth.MethodSession, "s-cave"
			if i%2 == 1 {
				m, cred = auth.MethodAPIKey, "k-batcomputer"
			}
			np, err := refresh(context.Background(), oracle(m, cred))
			if err != nil || np.CredentialID != cred {
				t.Errorf("refresh %s: %v, %v", cred, np, err)
			}
		})
	}
	wg.Wait()
	if len(fa.calls) != 64 {
		t.Errorf("PrincipalFor called %d times, want 64", len(fa.calls))
	}
}

// TestRealtimeOptions maps every core.realtime key onto the hub's options and
// raises ring_size to the floor the live tail needs.
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
	for _, tc := range []struct{ in, want int }{
		{0, realtimeRingFloor},
		{1024, realtimeRingFloor},
		{realtimeRingFloor - 1, realtimeRingFloor},
		{realtimeRingFloor, realtimeRingFloor},
		{realtimeRingFloor + 1, realtimeRingFloor + 1},
		{-1, -1}, // the hub refuses it; the floor must not hide it
	} {
		if got := realtimeOptions(realtimeSettings{RingSize: tc.in}).RingSize; got != tc.want {
			t.Errorf("ring_size %d -> RingSize %d, want %d", tc.in, got, tc.want)
		}
	}
	if realtimeRingFloor < 4096 {
		t.Errorf("realtimeRingFloor = %d, want at least 4096 (F6-03 pre-scope Q8)", realtimeRingFloor)
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
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	kr, err := newKernelRealtime(ke.catalog(), s, &fakeAuth{}, gothamCreds(), p,
		slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return &rtFixture{ke: ke, kr: kr}
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

// TestNewKernelRealtime_Refusals: no auth service, or no store, builds no
// hub; nor does a setting the hub refuses.
func TestNewKernelRealtime_Refusals(t *testing.T) {
	ke := newTestKernelEvents(t, 0, 0)
	// One provider per build: a core scope registers once per provider.
	provider := func() *metrics.Provider {
		p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
		return p
	}
	if _, err := newKernelRealtime(ke.catalog(), realtimeSettings{}, nil, gothamCreds(), provider(), nil, nil); err == nil {
		t.Error("built a hub with no auth service")
	}
	if _, err := newKernelRealtime(ke.catalog(), realtimeSettings{}, &fakeAuth{}, nil, provider(), nil, nil); err == nil {
		t.Error("built a hub with no credential store")
	}
	if _, err := newKernelRealtime(ke.catalog(), realtimeSettings{RingSize: -1}, &fakeAuth{}, gothamCreds(), provider(), nil, nil); err == nil {
		t.Error("built a hub with ring_size -1")
	}
	p := provider()
	kr, err := newKernelRealtime(ke.catalog(), realtimeSettings{}, &fakeAuth{}, gothamCreds(), p, nil, nil)
	if err != nil || kr.log == nil {
		t.Fatalf("nil logger: %v, %v; want a hub with a discard logger", kr, err)
	}
	if _, err := newKernelRealtime(ke.catalog(), realtimeSettings{}, &fakeAuth{}, gothamCreds(), p, nil, nil); err == nil {
		t.Error("built a second hub over the same metrics scope")
	}
}

// TestKernelRealtime_ShutdownSendsClosedAndRunReturns: an open stream gets
// core.stream.closed.v1{shutdown} from the RegisterOnShutdown hook, a new
// stream is refused afterwards, and run returns once its context ends.
func TestKernelRealtime_ShutdownSendsClosedAndRunReturns(t *testing.T) {
	f := newRTFixture(t, realtimeSettings{}, false)
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

	bind := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := oracle(auth.MethodSession, "s-cave")
			p.Perms = allowAll{}
			h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
	srv := httptest.NewServer(bind(f.kr.hub.Handler()))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- f.kr.run(ctx) }()

	resp, err := http.Get(srv.URL + "/?topic=batcave.*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
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

	f.kr.closeOnShutdown(5 * time.Second)()
	waitLine(t, lines, `"reason":"shutdown"`)

	if resp2, err := http.Get(srv.URL + "/?topic=batcave.*"); err == nil {
		if resp2.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("a stream opened after shutdown answered %d, want 503", resp2.StatusCode)
		}
		_ = resp2.Body.Close()
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
