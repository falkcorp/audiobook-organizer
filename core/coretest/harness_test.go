// file: core/coretest/harness_test.go
// version: 1.0.1
// guid: 2d3cedf2-afcc-4dd3-818a-7d6ca97695ab
// last-edited: 2026-11-07

package coretest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/examples/hello"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// TestBootZeroModules is 06 §6.10's G7 row: core boots with no module. At R1
// no core route is mounted yet (/readyz and /api/core/v1/modules land with
// their rows), so "serves /api/core/v1/*" is checked as core's claim
// dispatching: a path under it gets core's problem 404, not the SPA or a
// legacy answer. The health-ready half waits for the row that mounts /readyz.
func TestBootZeroModules(t *testing.T) {
	h := New(t)
	if mods := h.App().Modules(); len(mods) != 0 {
		t.Fatalf("Modules() = %v, want none (G7)", mods)
	}
	var problem struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
	}
	status := h.Anonymous().GetJSON(t, "/api/core/v1/health", &problem)
	if status != http.StatusNotFound || problem.Status != http.StatusNotFound || problem.Code != "not_found" {
		t.Fatalf("GET /api/core/v1/health = %d %+v, want core's problem 404", status, problem)
	}
	resp := h.Anonymous().Do(mustRequest(t, http.MethodGet, "/api/core/v1/health"))
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	// Zero modules build zero routes: the gate must pass an empty route set,
	// not fail or panic on it.
	AssertRoutesHavePermissions(t, h)
}

// TestDisabledModuleInert is 06 §6.10's G1 row through AssertModuleInert on
// hello, over the cells that exist at R1 (F2-16 adds the held-runs cell).
func TestDisabledModuleInert(t *testing.T) {
	var n atomic.Int32
	h := New(t, WithModules(counting{hello.New(), &n}), WithDisabled(hello.ID))
	AssertModuleInert(t, h, hello.ID)
	// AssertModuleInert does not enumerate a module's native routes, so the
	// test names hello's: its greeting is a 404, not a 403 or a 422.
	resp := h.Anonymous().Do(mustRequest(t, http.MethodGet, "/api/hello/v1/greeting?name=Bruce"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/hello/v1/greeting on a disabled hello = %d, want 404", resp.StatusCode)
	}
	// The assertion itself never runs the module's code (06 §6.3 A5).
	if got := n.Load(); got != 0 {
		t.Fatalf("hello's Register ran %d time(s) during boot and AssertModuleInert, want 0", got)
	}
}

// counting counts its module's Register calls.
type counting struct {
	module.Module
	n *atomic.Int32
}

func (c counting) Register(r *module.Registrar) error { c.n.Add(1); return c.Module.Register(r) }

// joker has one native route with a real permission, the shape of every
// module once core authentication exists. Enabled it cannot boot at R1
// (ErrNoAuthService); disabled it must.
type joker struct{}

// Laugh is joker's response.
type Laugh struct {
	Ha string `json:"ha"`
}

func (joker) Manifest() module.Manifest {
	return module.Manifest{ID: "joker", Version: "1.0.0", MinCoreVersion: "0.1.0", DefaultEnabled: true}
}

func (joker) Register(r *module.Registrar) error {
	httpapi.Handle(r.HTTP(), httpapi.Route{Method: http.MethodGet, Path: "/laugh", Name: "getLaugh",
		Perm: rbac.Need("joker:laugh.read"), Summary: "Laugh"},
		func(context.Context, *httpapi.In[struct{}]) (Laugh, error) { return Laugh{Ha: "ha"}, nil })
	return r.Err()
}

// TestDisabledModuleInert_PermissionedRoute: a disabled module whose routes
// carry real permissions passes. AssertModuleInert used to describe the
// module through app.Describe, which runs Register and fails on such a route
// at R1.
func TestDisabledModuleInert_PermissionedRoute(t *testing.T) {
	h := New(t, WithModules(joker{}), WithDisabled("joker"))
	AssertModuleInert(t, h, "joker")
}

// TestAssertModuleInert_FailsOnEnabled proves AssertModuleInert is not blind:
// over an enabled hello it fails on the module state and the registered
// route, and it then sends no request and calls no module code.
func TestAssertModuleInert_FailsOnEnabled(t *testing.T) {
	var n atomic.Int32
	h := New(t, WithModules(counting{hello.New(), &n}))
	rec := runRecording(t, func(tb testing.TB) { AssertModuleInert(tb, h, hello.ID) })
	for _, want := range []string{"is enabled", "route GET /api/hello/v1/greeting", "no request was sent"} {
		if !rec.contains(want) {
			t.Errorf("AssertModuleInert over an enabled hello did not report %q; it reported:\n%s", want, rec.text())
		}
	}
	if strings.Contains(rec.text(), "answered") {
		t.Errorf("AssertModuleInert probed a live module:\n%s", rec.text())
	}
	if got := n.Load(); got != 1 {
		t.Errorf("hello's Register ran %d time(s), want 1 (boot only)", got)
	}
}

// TestClient_GetJSON reaches hello's greeting through App.Handler.
func TestClient_GetJSON(t *testing.T) {
	h := New(t, WithModules(hello.New()))
	var g hello.Greeting
	if status := h.Anonymous().GetJSON(t, "/api/hello/v1/greeting?name=Bruce", &g); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if g.Message != "Hello, Bruce" {
		t.Fatalf("message = %q, want %q", g.Message, "Hello, Bruce")
	}
}

// batcave is a fixture module that logs from Register, so a test can see
// which harness's log capture received the record.
type batcave struct{ signal string }

func (b batcave) Manifest() module.Manifest {
	return module.Manifest{ID: "batcave", Version: "1.0.0", MinCoreVersion: "0.1.0", DefaultEnabled: true}
}

func (b batcave) Register(r *module.Registrar) error {
	r.Deps().Log.Info("signal lit", "signal", b.signal)
	return r.Err()
}

// TestHarness_TwoInstancesIsolated is I2: two harnesses booted at once share
// no store, state directory, log capture or handler, and neither touches
// slog.Default.
func TestHarness_TwoInstancesIsolated(t *testing.T) {
	before := slog.Default()
	beforeHandler := before.Handler()

	signals := []string{"gordon", "robin"}
	hs := make([]*Harness, len(signals))
	errs := make([]error, len(signals))
	var wg sync.WaitGroup
	for i, s := range signals {
		wg.Go(func() { hs[i], errs[i] = Boot(t, WithModules(batcave{signal: s})) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	a, b := hs[0], hs[1]
	if a.stateDir == b.stateDir || a.stateDir == "" {
		t.Errorf("state directories %q and %q, want two distinct ones", a.stateDir, b.stateDir)
	}
	if a.logs == b.logs {
		t.Error("the two harnesses share one log capture")
	}
	if a.App() == b.App() {
		t.Error("the two harnesses share one App")
	}
	for i, h := range hs {
		got := signalsLogged(h.logs)
		if !slices.Equal(got, signals[i:i+1]) {
			t.Errorf("harness %d captured signals %v, want %v", i, got, signals[i:i+1])
		}
	}
	if slog.Default() != before || slog.Default().Handler() != beforeHandler {
		t.Error("booting a harness changed slog.Default (03 R5)")
	}
}

// signalsLogged returns the signal attribute of every "signal lit" record.
func signalsLogged(c *logcapture.Capture) []string {
	var out []string
	for _, r := range c.Records() {
		if r.Message != "signal lit" {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "signal" {
				out = append(out, a.Value.String())
			}
			return true
		})
	}
	return out
}

// TestRoutesHavePermissions_Hello: the gate passes over hello's built router,
// and the router it walks is not empty.
func TestRoutesHavePermissions_Hello(t *testing.T) {
	h := New(t, WithModules(hello.New()))
	found := false
	for ri := range h.App().Routes() {
		found = found || (ri.Owner == string(hello.ID) && ri.Pattern == "/api/hello/v1/greeting")
	}
	if !found {
		t.Fatal("hello's greeting route is not in the built router's Routes(); the gate would be vacuous")
	}
	AssertRoutesHavePermissions(t, h)
}

// TestRoutesHavePermissions_RejectsFixtures is I4. Router.Build already
// refuses a native route with no permission or with RateExempt, so the
// fixtures are RouteInfo values, the shape Routes() yields: each defect fails
// the gate, and the failure names the route.
func TestRoutesHavePermissions_RejectsFixtures(t *testing.T) {
	ok := []httpapi.RouteInfo{
		{Owner: "batcave", Method: http.MethodGet, Pattern: "/api/batcave/v1/signal", Name: "getSignal",
			Perm: rbac.Public, Kind: httpapi.RouteKindNative},
		{Owner: "batcave", Method: http.MethodPost, Pattern: "/api/batcave/v1/signal", Name: "lightSignal",
			Perm: rbac.Need("batcave:signal.light"), Kind: httpapi.RouteKindNative},
		{Owner: "core", Method: http.MethodGet, Pattern: "/livez", Perm: rbac.Public,
			RateClass: httpapi.RateExempt, Kind: httpapi.RouteKindInfra},
		{Owner: "core", Pattern: "/", Name: "spa", Perm: rbac.Public, RateClass: httpapi.RateExempt,
			Kind: httpapi.RouteKindSPA},
		{Owner: "batcave", Method: http.MethodGet, Pattern: "/api/v1/cases", Name: "/api/v1/cases",
			Perm: rbac.AnyAuthenticated, RateClass: httpapi.RateExempt, Kind: httpapi.RouteKindLegacy},
		{Owner: "batcave", Method: http.MethodGet, Pattern: "/api/items", Name: "/api/items",
			Perm: rbac.Delegated("batcave.wayne"), Kind: httpapi.RouteKindLegacy, OwnAuth: true,
			AuthMethod: "batcave.wayne"},
	}
	t.Run("accepts", func(t *testing.T) {
		archtest.RoutesHavePermissions(t, slices.Values(ok))
	})

	cases := []struct {
		name string
		ri   httpapi.RouteInfo
		want string
	}{
		{"native without permission", httpapi.RouteInfo{Owner: "batcave", Method: http.MethodGet,
			Pattern: "/api/batcave/v1/cowl", Name: "getCowl", Kind: httpapi.RouteKindNative},
			"GET /api/batcave/v1/cowl"},
		{"legacy missing from its manifest", httpapi.RouteInfo{Owner: "batcave", Method: http.MethodDelete,
			Pattern: "/api/v1/cases/{p4}", Name: "/api/v1/cases/:id", Kind: httpapi.RouteKindLegacy},
			"DELETE /api/v1/cases/{p4}"},
		{"RateExempt outside the infra set", httpapi.RouteInfo{Owner: "batcave", Method: http.MethodPut,
			Pattern: "/api/batcave/v1/grapple", Name: "putGrapple", Perm: rbac.Need("batcave:grapple.fire"),
			RateClass: httpapi.RateExempt, Kind: httpapi.RouteKindNative},
			"PUT /api/batcave/v1/grapple"},
		{"SPA owner not Public", httpapi.RouteInfo{Owner: "core", Pattern: "/", Name: "spa",
			Perm: rbac.AnyAuthenticated, RateClass: httpapi.RateExempt, Kind: httpapi.RouteKindSPA},
			"/ (core, spa)"},
		{"OwnAuth naming no auth method", httpapi.RouteInfo{Owner: "batcave", Method: http.MethodGet,
			Pattern: "/api/me", Name: "/api/me", Perm: rbac.Delegated("batcave.wayne"),
			Kind: httpapi.RouteKindLegacy, OwnAuth: true},
			"GET /api/me"},
		{"legacy requirement on a native route", httpapi.RouteInfo{Owner: "batcave", Method: http.MethodGet,
			Pattern: "/api/batcave/v1/files", Name: "getFiles", Perm: rbac.LegacyAtom("files.read"),
			Kind: httpapi.RouteKindNative},
			"GET /api/batcave/v1/files"},
		{"infra with a legacy atom", httpapi.RouteInfo{Owner: "core", Method: http.MethodGet, Pattern: "/metrics",
			Perm: rbac.LegacyAtom("admin"), RateClass: httpapi.RateExempt, Kind: httpapi.RouteKindInfra},
			"GET /metrics (core, ): requirement"},
		{"infra delegated", httpapi.RouteInfo{Owner: "core", Method: http.MethodGet, Pattern: "/readyz",
			Perm: rbac.Delegated("batcave.wayne"), RateClass: httpapi.RateExempt, Kind: httpapi.RouteKindInfra},
			"GET /readyz (core, ): requirement"},
		{"infra not RateExempt", httpapi.RouteInfo{Owner: "core", Method: http.MethodGet, Pattern: "/healthz",
			Perm: rbac.Public, RateClass: httpapi.RateDefault, Kind: httpapi.RouteKindInfra},
			"GET /healthz (core, ): an infra route is RateExempt, got RateDefault"},
		{"SPA owner rate-limited", httpapi.RouteInfo{Owner: "core", Pattern: "/", Name: "spa",
			Perm: rbac.Public, RateClass: httpapi.RateAuth, Kind: httpapi.RouteKindSPA},
			"got public and RateAuth"},
		{"legacy in-handler check", httpapi.RouteInfo{Owner: "batcave", Method: http.MethodGet,
			Pattern: "/api/v1/vault", Name: "/api/v1/vault", Perm: rbac.LegacyCheck("vault.read", true),
			Kind: httpapi.RouteKindLegacy},
			"GET /api/v1/vault (batcave, /api/v1/vault): requirement"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			routes := append(slices.Clone(ok), c.ri)
			rec := runRecording(t, func(tb testing.TB) { archtest.RoutesHavePermissions(tb, slices.Values(routes)) })
			if !rec.failed {
				t.Fatalf("RoutesHavePermissions passed a route set with %s", c.name)
			}
			if !rec.contains(c.want) {
				t.Fatalf("the failure does not name %q:\n%s", c.want, rec.text())
			}
			if n := strings.Count(rec.text(), "\n  "); n != 1 {
				t.Fatalf("%d offenders reported, want exactly the fixture:\n%s", n, rec.text())
			}
		})
	}
	t.Run("reports every offender", func(t *testing.T) {
		routes := slices.Clone(ok)
		for _, c := range cases {
			routes = append(routes, c.ri)
		}
		rec := runRecording(t, func(tb testing.TB) { archtest.RoutesHavePermissions(tb, slices.Values(routes)) })
		for _, c := range cases {
			if !rec.contains(c.want) {
				t.Errorf("the failure does not name %q", c.want)
			}
		}
	})
}

// TestAssertPermission_Hello: the "Authentication at R1" check found no
// principal seam the router authorises from (route.go only binds the
// principal into In; Build refuses every non-Public native route), so As and
// AssertPermission fail the test with the identity-flip message, and WithUser
// only records the principal.
func TestAssertPermission_Hello(t *testing.T) {
	const want = "needs core authentication, which lands at R2's identity flip"
	rec := runRecording(t, func(tb testing.TB) {
		h := New(tb, WithModules(hello.New()), WithUser("alfred", "hello:read"))
		if got := h.users["alfred"]; !slices.Equal(got, []rbac.Permission{"hello:read"}) {
			t.Errorf("WithUser recorded %v, want [hello:read]", got)
		}
		h.As("alfred")
		t.Error("As returned instead of failing the test")
	})
	if !rec.contains("coretest: As " + want) {
		t.Errorf("As did not fail with the identity-flip message:\n%s", rec.text())
	}

	h := New(t, WithModules(hello.New()))
	rec = runRecording(t, func(tb testing.TB) {
		AssertPermission(tb, h, http.MethodGet, "/api/hello/v1/greeting", "hello:read")
	})
	if !rec.contains("coretest: AssertPermission " + want) {
		t.Errorf("AssertPermission did not fail with the identity-flip message:\n%s", rec.text())
	}
}

// TestBoot_Refusals: Boot returns, never fails the test for, a boot error;
// the options it cannot honour at R1 are refused, not ignored.
func TestBoot_Refusals(t *testing.T) {
	t.Run("validation error", func(t *testing.T) {
		_, err := Boot(t, WithModules(batcave{}, batcave{}))
		if _, ok := errors.AsType[*module.ValidationError](err); !ok {
			t.Fatalf("Boot with a duplicate module = %v, want a *module.ValidationError", err)
		}
	})
	t.Run("WithDisabled names no module", func(t *testing.T) {
		_, err := Boot(t, WithModules(hello.New()), WithDisabled("arkham"))
		if err == nil || !strings.Contains(err.Error(), `"arkham"`) {
			t.Fatalf("err = %v, want one naming arkham", err)
		}
	})
	t.Run("WithConfig before config exists", func(t *testing.T) {
		_, err := Boot(t, WithConfig(map[string]any{"modules.hello.greeting": "Hi"}))
		if err == nil || !strings.Contains(err.Error(), "modules.hello.greeting") {
			t.Fatalf("err = %v, want a refusal naming the key", err)
		}
	})
	t.Run("nil module", func(t *testing.T) {
		_, err := Boot(t, WithModules(nil))
		if _, ok := errors.AsType[*module.ValidationError](err); !ok {
			t.Fatalf("Boot with a nil module = %v, want the kernel's *module.ValidationError", err)
		}
	})
	t.Run("New fails the test", func(t *testing.T) {
		rec := runRecording(t, func(tb testing.TB) { New(tb, WithModules(batcave{}, batcave{})) })
		if !rec.failed || !rec.contains("coretest: boot") {
			t.Fatalf("New did not fail the test on a boot error:\n%s", rec.text())
		}
	})
}

// TestWithDisabled_ModuleState: a disabled module is reported disabled and
// its Register never runs, while its sibling boots.
func TestWithDisabled_ModuleState(t *testing.T) {
	h := New(t, WithModules(hello.New(), batcave{signal: "gordon"}), WithDisabled(hello.ID))
	for _, s := range h.App().Modules() {
		if want := s.ID != hello.ID; s.Enabled != want {
			t.Errorf("%s enabled = %t, want %t", s.ID, s.Enabled, want)
		}
	}
	if h.watched[hello.ID].registered.Load() {
		t.Error("the disabled module's Register ran")
	}
	// Only a module that boots disabled is wrapped; the enabled one is the
	// value the test passed, and its Register ran.
	if h.watched["batcave"] != nil {
		t.Error("the enabled module was wrapped")
	}
	if got := signalsLogged(h.logs); !slices.Equal(got, []string{"gordon"}) {
		t.Errorf("the enabled module's Register logged %v, want [gordon]", got)
	}
}

func mustRequest(t testing.TB, method, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// recordingTB records failures instead of failing the real test. Fatal and
// FailNow end the calling goroutine, so runRecording runs fn on its own.
type recordingTB struct {
	testing.TB
	mu     sync.Mutex
	msgs   []string
	failed bool
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) { r.record(fmt.Sprintf(format, args...)) }
func (r *recordingTB) Error(args ...any)                 { r.record(fmt.Sprint(args...)) }
func (r *recordingTB) Fatalf(format string, args ...any) { r.Errorf(format, args...); runtime.Goexit() }
func (r *recordingTB) Fatal(args ...any)                 { r.Error(args...); runtime.Goexit() }
func (r *recordingTB) Fail()                             { r.record("") }
func (r *recordingTB) FailNow()                          { r.Fail(); runtime.Goexit() }

func (r *recordingTB) Failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed
}

func (r *recordingTB) record(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = true
	if msg != "" {
		r.msgs = append(r.msgs, msg)
	}
}

func (r *recordingTB) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.msgs, "\n")
}

func (r *recordingTB) contains(s string) bool { return strings.Contains(r.text(), s) }

// runRecording runs fn with a recordingTB over t on a goroutine of its own and
// waits for it, so a Fatal inside fn ends only that goroutine.
func runRecording(t *testing.T, fn func(testing.TB)) *recordingTB {
	t.Helper()
	rec := &recordingTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(rec)
	}()
	<-done
	return rec
}

// arkham is a fixture module with a LegacyAuthCore mount whose routes are all
// Public: the one shape of legacy mount that boots before core
// authentication (a non-Public LegacyAuthCore route is ErrNoAuthService).
type arkham struct{}

func (arkham) Manifest() module.Manifest {
	return module.Manifest{ID: "arkham", Version: "1.0.0", MinCoreVersion: "0.1.0", DefaultEnabled: true,
		LegacyPrefixes: []string{"/asylum/"}}
}

func (arkham) Register(r *module.Registrar) error {
	r.MountLegacy(httpapi.LegacyMount{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		Auth:    httpapi.LegacyAuthCore,
		Routes: slices.Values([]httpapi.LegacyRoute{
			{Method: http.MethodGet, GinPath: "/asylum/cells", Perm: rbac.Public, Public: true},
			{Method: http.MethodGet, GinPath: "/asylum/cells/:id", Perm: rbac.Public, Public: true,
				RateClass: httpapi.RateExempt},
		}),
		TopSegments: []string{"asylum"},
	})
	return r.Err()
}

// TestRoutesHavePermissions_LegacyMount runs the gate over a built router
// that holds legacy manifest routes, through the harness.
func TestRoutesHavePermissions_LegacyMount(t *testing.T) {
	h := New(t, WithModules(arkham{}))
	legacy := 0
	for ri := range h.App().Routes() {
		if ri.Kind == httpapi.RouteKindLegacy && ri.Owner == "arkham" {
			legacy++
		}
	}
	if legacy != 2 {
		t.Fatalf("%d legacy routes of arkham in Routes(), want 2", legacy)
	}
	resp := h.Anonymous().Do(mustRequest(t, http.MethodGet, "/asylum/cells/7"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("GET /asylum/cells/7 = %d, want the mount's 418", resp.StatusCode)
	}
	AssertRoutesHavePermissions(t, h)
}

// TestClient_FillsServerFields: Do serves a copy of the request with the
// fields httptest.NewRequest sets, and leaves the caller's request alone.
func TestClient_FillsServerFields(t *testing.T) {
	var got *http.Request
	c := &Client{handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r })}
	req := mustRequest(t, http.MethodGet, "/api/batcave/v1/signal?lit=1")
	resp := c.Do(req)
	defer resp.Body.Close()
	if got.RemoteAddr != "192.0.2.1:1234" || got.Host != "example.com" || got.RequestURI != "/api/batcave/v1/signal?lit=1" {
		t.Fatalf("served RemoteAddr %q Host %q RequestURI %q, want httptest.NewRequest's", got.RemoteAddr, got.Host, got.RequestURI)
	}
	if req.RemoteAddr != "" || req.Host != "" || req.RequestURI != "" {
		t.Fatal("Do modified the caller's request")
	}
	set := mustRequest(t, http.MethodGet, "http://gotham.example/x")
	set.RemoteAddr = "192.0.2.9:80"
	resp = c.Do(set)
	defer resp.Body.Close()
	if got.RemoteAddr != "192.0.2.9:80" || got.Host != "gotham.example" {
		t.Fatalf("Do replaced fields the caller set: RemoteAddr %q Host %q", got.RemoteAddr, got.Host)
	}
}

// TestHarness_InSynctestBubble is I3: a harness boots, serves and asserts
// inside a synctest bubble, which would deadlock on a real listener or hang
// on a timer nothing advances.
func TestHarness_InSynctestBubble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := New(t, WithModules(hello.New()), WithDisabled(hello.ID))
		AssertModuleInert(t, h, hello.ID)
		h2 := New(t, WithModules(hello.New()))
		var g hello.Greeting
		if status := h2.Anonymous().GetJSON(t, "/api/hello/v1/greeting?name=Selina", &g); status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		AssertRoutesHavePermissions(t, h2)
	})
}

// TestHarness_NoProcessGlobals is I2 over what a boot could leave behind in
// the process: Prometheus's default registry and goroutines. otel's global
// setters are not reachable from core at all: the otel root package is not
// on archtest/thirdparty_allow.txt, and app's TestNoGlobals bans the calls.
func TestHarness_NoProcessGlobals(t *testing.T) {
	families := func() int {
		mfs, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		return len(mfs)
	}
	before, g0 := families(), runtime.NumGoroutine()
	for range 20 {
		New(t, WithModules(hello.New()))
	}
	if after := families(); after != before {
		t.Errorf("Prometheus's default registry went from %d to %d families", before, after)
	}
	// Twenty boots leave no goroutine behind: nothing a harness boots runs on
	// its own. The slack absorbs the runtime's own background goroutines.
	if g1 := runtime.NumGoroutine(); g1 > g0+2 {
		t.Errorf("goroutines went from %d to %d over 20 boots", g0, g1)
	}
}

// TestHarness_ParallelBoots boots and asserts from parallel tests (run with
// -race): harnesses share no state.
func TestHarness_ParallelBoots(t *testing.T) {
	for i := range 4 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			for range 20 {
				h := New(t, WithModules(hello.New()), WithDisabled(hello.ID))
				AssertModuleInert(t, h, hello.ID)
				AssertRoutesHavePermissions(t, New(t, WithModules(hello.New())))
			}
		})
	}
}

// gcpd and gordon are modules whose values are not comparable (a slice
// field, a func field): Boot must tell modules apart without ==, which
// panics on such interface values.
type gcpd struct{ units []string }

func (gcpd) Manifest() module.Manifest {
	return module.Manifest{ID: "gcpd", Version: "1.0.0", MinCoreVersion: "0.1.0", DefaultEnabled: true}
}

func (gcpd) Register(r *module.Registrar) error { return r.Err() }

type gordon struct{ signal func() string }

func (gordon) Manifest() module.Manifest {
	return module.Manifest{ID: "gordon", Version: "1.0.0", MinCoreVersion: "0.1.0", DefaultEnabled: true}
}

func (gordon) Register(r *module.Registrar) error { return r.Err() }

// TestBoot_UncomparableModuleValues: modules passed by value with a slice or
// func field boot, enabled or disabled.
func TestBoot_UncomparableModuleValues(t *testing.T) {
	mods := func() []module.Module {
		return []module.Module{gcpd{units: []string{"bullock"}}, gordon{signal: func() string { return "bat" }}}
	}
	for _, c := range []struct {
		name string
		opts []Option
	}{
		{"enabled", nil},
		{"disabled", []Option{WithDisabled("gcpd", "gordon")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("Boot panicked: %v", p)
				}
			}()
			h := New(t, append([]Option{WithModules(mods()...)}, c.opts...)...)
			if c.opts != nil {
				AssertModuleInert(t, h, "gcpd")
				AssertModuleInert(t, h, "gordon")
			}
		})
	}
}
