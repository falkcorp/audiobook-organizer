// file: core/kernel/kernel_test.go
// version: 1.0.2
// guid: c5a656df-cf96-4fcc-b1af-7a5f1be4cf7b
// last-edited: 2026-11-07

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// testModule is a module built from a manifest and a Register func.
type testModule struct {
	man      module.Manifest
	register func(r *module.Registrar) error
}

func (m testModule) Manifest() module.Manifest { return m.man }

func (m testModule) Register(r *module.Registrar) error {
	if m.register == nil {
		return nil
	}
	return m.register(r)
}

// manor is a named response type: OpenAPI needs a component name.
type manor struct {
	Owner string `json:"owner"`
}

// getManor is a route handler for the tests' routes.
func getManor(context.Context, *httpapi.In[struct{}]) (manor, error) {
	return manor{Owner: "Bruce Wayne"}, nil
}

// mod is an enabled module with ID id.
func mod(id string, register func(r *module.Registrar) error) testModule {
	return testModule{man: module.Manifest{ID: module.ID(id), Version: "1.0.0", DefaultEnabled: true}, register: register}
}

// with returns m with its manifest changed by fn.
func (m testModule) with(fn func(*module.Manifest)) testModule {
	fn(&m.man)
	return m
}

// testConfig is a Config for mods whose log goes to a capture.
func testConfig(t *testing.T, mods ...module.Module) Config {
	t.Helper()
	return Config{AppID: "gotham", Logger: coretest.NewLogCapture(t).Logger(), Modules: mods}
}

// mustNew boots c or fails the test.
func mustNew(t *testing.T, c Config) *Kernel {
	t.Helper()
	k, err := New(t.Context(), c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := k.closeCore(context.Background(), time.Now().Add(defaultShutdownBudget)); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return k
}

// problems returns the problems of err's *module.ValidationError.
func problems(t *testing.T, err error) []module.Problem {
	t.Helper()
	var ve *module.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error %v is not a *module.ValidationError", err)
	}
	return ve.Problems
}

// hasKind reports a problem of kind in err.
func hasKind(err error, kind module.ProblemKind) bool {
	var ve *module.ValidationError
	return errors.As(err, &ve) && slices.ContainsFunc(ve.Problems, func(p module.Problem) bool { return p.Kind == kind })
}

func TestBootZeroModules(t *testing.T) {
	k := mustNew(t, testConfig(t))
	if got := k.Modules(); len(got) != 0 {
		t.Fatalf("Modules() = %v, want none", got)
	}
	rec := httptest.NewRecorder()
	k.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/batcave/v1/gadgets", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unmatched API path = %d %q, want 404 problem+json", rec.Code, rec.Header().Get("Content-Type"))
	}
	// The phases that exist all ran and were recorded.
	if len(k.timings) != 0 {
		t.Fatalf("phase timings left unflushed: %v", k.timings)
	}
}

func TestCatalog_EnablementAndDisabledClaims(t *testing.T) {
	registered := false
	wayne := mod("wayne", func(*module.Registrar) error { registered = true; return nil }).with(func(m *module.Manifest) {
		m.DefaultEnabled = false
		m.LegacyPrefixes = []string{"/manor/"}
	})
	k := mustNew(t, testConfig(t, wayne, mod("alfred", nil)))
	if registered {
		t.Fatal("Register ran for a disabled module")
	}
	want := []ModuleState{{ID: "alfred", Version: "1.0.0", Enabled: true}, {ID: "wayne", Version: "1.0.0", Disabled: disabledByDefault}}
	if got := k.Modules(); !slices.Equal(got, want) {
		t.Fatalf("Modules() = %+v, want %+v", got, want)
	}
	// A disabled module's claims stay reserved (06 §6.5.1 P1).
	thief := mod("selina", nil).with(func(m *module.Manifest) { m.LegacyPrefixes = []string{"/manor/vault"} })
	_, err := New(t.Context(), testConfig(t, wayne, thief))
	if !hasKind(err, module.ProblemLegacyPrefixCollision) || !errors.Is(err, httpapi.ErrPrefixCollision) {
		t.Fatalf("claim inside a disabled module's prefix = %v, want ProblemLegacyPrefixCollision", err)
	}
}

// TestModuleRootsClaimed is the review's B1: every catalog entry's
// /api/<id>/ is claimed, enabled or not, and so is core's /api/core/, so a
// legacy prefix over any of them fails boot now, not on the day the module is
// enabled or the first core route lands (06 §6.5.1 P1; 05-http-api §4.9).
func TestModuleRootsClaimed(t *testing.T) {
	bane := mod("bane", nil).with(func(m *module.Manifest) { m.DefaultEnabled = false })
	joker := mod("joker", nil).with(func(m *module.Manifest) {
		m.LegacyPrefixes = []string{"/api/bane/", "/api/core/", "/api/alfred/venom"}
	})
	_, err := New(t.Context(), testConfig(t, bane, joker, mod("alfred", nil)))
	if !errors.Is(err, httpapi.ErrPrefixCollision) {
		t.Fatalf("New = %v, want ErrPrefixCollision", err)
	}
	var got []string
	for _, p := range problems(t, err) {
		if p.Module != "joker" || p.Kind != module.ProblemLegacyPrefixCollision {
			t.Fatalf("problem %v, want only joker's ProblemLegacyPrefixCollision", p)
		}
		got = append(got, p.Detail)
	}
	if len(got) != 3 {
		t.Fatalf("problems = %q, want one per prefix over a claimed root (disabled bane, core, enabled alfred)", got)
	}
}

func TestValidation_ProblemKinds(t *testing.T) {
	handler := getManor
	stand := http.NotFoundHandler()
	cases := []struct {
		kind  module.ProblemKind
		cfg   func(*Config)
		mods  []module.Module
		cause error // the httpapi error a P7/P8 problem keeps, when there is one
	}{
		{module.ProblemInvalidID, nil, []module.Module{mod("Batman", nil)}, nil},
		{module.ProblemInvalidID, func(c *Config) { c.AppID = "G" }, nil, nil},
		{module.ProblemReservedID, nil, []module.Module{mod("api", nil)}, nil},
		{module.ProblemReservedID, nil, []module.Module{mod("v2", nil)}, nil},
		{module.ProblemDuplicateID, nil, []module.Module{mod("robin", nil), mod("robin", nil)}, nil},
		{module.ProblemCoreTooOld, nil, []module.Module{mod("joker", nil).with(func(m *module.Manifest) { m.MinCoreVersion = "99.0.0" })}, nil},
		{module.ProblemCoreTooOld, nil, []module.Module{mod("joker", nil).with(func(m *module.Manifest) { m.MinCoreVersion = "v1" })}, nil},
		{module.ProblemRouteWithoutPermission, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			httpapi.Handle(r.HTTP(), httpapi.Route{Method: http.MethodGet, Path: "/gadgets", Name: "listGadgets"}, handler)
			return nil
		})}, httpapi.ErrNoPermission},
		{module.ProblemPermissionNamespace, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			httpapi.Handle(r.HTTP(), httpapi.Route{Method: http.MethodGet, Path: "/gadgets", Name: "listGadgets", Perm: rbac.Need("joker:read")}, handler)
			return nil
		})}, httpapi.ErrForeignScope},
		{module.ProblemInvalidRegistration, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			httpapi.Handle(r.HTTP(), httpapi.Route{Method: http.MethodGet, Path: "/gadgets", Name: "listGadgets", Perm: rbac.Public}, handler)
			return nil // rbac.Public outside Manifest.PublicRoutes
		})}, httpapi.ErrPublicNotAllowed},
		{module.ProblemInvalidRegistration, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			r.Hook(module.Hook{Name: "arkham.lockdown"})
			return nil
		})}, nil},
		{module.ProblemRouteOutsidePrefix, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			r.MountLegacy(httpapi.LegacyMount{Handler: stand, Auth: httpapi.LegacyAuthCore, TopSegments: []string{"arkham"}})
			return nil
		})}, nil},
		{module.ProblemLegacyPrefixCollision, nil, []module.Module{
			mod("batcave", nil).with(func(m *module.Manifest) { m.LegacyPrefixes = []string{"/api/v1/"} }),
			mod("arkham", nil).with(func(m *module.Manifest) { m.LegacyPrefixes = []string{"/api/v1/inmates"} }),
		}, httpapi.ErrPrefixCollision},
		{module.ProblemSPAOwner, nil, []module.Module{
			mod("batcave", func(r *module.Registrar) error {
				r.MountLegacy(httpapi.LegacyMount{Handler: stand, Auth: httpapi.LegacyAuthCore, ServesSPA: true})
				return nil
			}),
			mod("arkham", func(r *module.Registrar) error {
				r.MountLegacy(httpapi.LegacyMount{Handler: stand, Auth: httpapi.LegacyAuthCore, ServesSPA: true})
				return nil
			}),
		}, nil},
		{module.ProblemWorkerPolicy, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			r.Worker(module.Worker{Name: "patrol", Run: func(context.Context) error { return nil }})
			return nil
		})}, nil},
		{module.ProblemAuthMethod, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			r.AuthMethod(auth.MethodSpec{Method: "batcave.cowl", Interactive: true})
			return nil
		})}, nil},
		{module.ProblemLegacyAuth, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			r.MountLegacy(httpapi.LegacyMount{Handler: stand, Auth: httpapi.LegacyAuthGin})
			return nil
		})}, nil},
		{module.ProblemSPASegmentCollision, nil, []module.Module{mod("batcave", func(r *module.Registrar) error {
			r.MountLegacy(httpapi.LegacyMount{Handler: stand, Auth: httpapi.LegacyAuthCore, TopSegments: []string{"settings"}})
			return nil
		}).with(func(m *module.Manifest) { m.LegacyPrefixes = []string{"/settings/"} })}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			c := testConfig(t, tc.mods...)
			if tc.cfg != nil {
				tc.cfg(&c)
			}
			k, err := New(t.Context(), c)
			if k != nil || !hasKind(err, tc.kind) {
				t.Fatalf("New = %v, %v; want nil and a %s problem", k, err, tc.kind)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("errors.Is(err, %v) = false: %v", tc.cause, err)
			}
		})
	}
}

func TestValidation_AllProblemsJoined(t *testing.T) {
	// P1: three manifests, three problems, one error.
	_, err := New(t.Context(), testConfig(t,
		mod("Batman", nil),
		mod("api", nil),
		mod("joker", nil).with(func(m *module.Manifest) { m.MinCoreVersion = "9.0.0" }),
	))
	if got := problems(t, err); len(got) != 3 {
		t.Fatalf("P1 problems = %v, want 3", got)
	}
	for _, s := range []error{module.ErrInvalidID, module.ErrReservedID, module.ErrCoreTooOld} {
		if !errors.Is(err, s) {
			t.Errorf("errors.Is(%v) = false", s)
		}
	}
	// P6: every module registers; their problems and Register's errors join.
	escaped := errors.New("Joker escaped")
	_, err = New(t.Context(), testConfig(t,
		mod("alfred", func(r *module.Registrar) error { r.Hook(module.Hook{Name: "tea"}); return nil }),
		mod("bane", func(r *module.Registrar) error { r.Worker(module.Worker{Name: "venom"}); return nil }),
		mod("joker", func(*module.Registrar) error { return escaped }),
		// "return r.Err()" hands back the registrar's own problems: counted once.
		mod("penguin", func(r *module.Registrar) error { r.Hook(module.Hook{Name: "umbrella"}); return r.Err() }),
		mod("riddler", func(*module.Registrar) error { panic("riddle me this") }),
	))
	if got := problems(t, err); len(got) != 3 {
		t.Fatalf("P6 problems = %v, want 3", got)
	}
	if n := strings.Count(err.Error(), "umbrella"); n != 1 {
		t.Fatalf("penguin's problem appears %d times, want once: %v", n, err)
	}
	if !errors.Is(err, escaped) || !strings.Contains(err.Error(), "riddle me this") {
		t.Fatalf("P6 error = %v, want Register's error and the recovered panic joined", err)
	}
}

func TestModuleAuthMethod_CannotBeInteractive(t *testing.T) {
	for name, spec := range map[string]auth.MethodSpec{
		"interactive":            {Method: "batcave.cowl", Interactive: true},
		"may change credentials": {Method: "batcave.cowl", MayChangeCredentials: true},
		"owner verifier":         {Method: "batcave.cowl", OwnerVerifier: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(t.Context(), testConfig(t, mod("batcave", func(r *module.Registrar) error { r.AuthMethod(spec); return nil })))
			if !errors.Is(err, module.ErrAuthMethod) {
				t.Fatalf("New = %v, want ErrAuthMethod", err)
			}
		})
	}
	// A non-interactive method boots; app.Options.Authenticators, which may
	// carry interactive ones, lands with auth.
	mustNew(t, testConfig(t, mod("batcave", func(r *module.Registrar) error {
		r.AuthMethod(auth.MethodSpec{Method: "batcave.signal"})
		return nil
	})))
}

func TestLegacyAuthGinOnlyNamedModule(t *testing.T) {
	mount := func(r *module.Registrar) error {
		r.MountLegacy(httpapi.LegacyMount{Handler: http.NotFoundHandler(), Auth: httpapi.LegacyAuthGin})
		return nil
	}
	c := testConfig(t, mod("batcave", mount), mod("arkham", mount))
	c.LegacyAuthGinUntil = "batcave"
	_, err := New(t.Context(), c)
	got := problems(t, err)
	if len(got) != 1 || got[0].Module != "arkham" || got[0].Kind != module.ProblemLegacyAuth {
		t.Fatalf("problems = %v, want one ProblemLegacyAuth for arkham", got)
	}
	// The OwnAuth half needs auth (R2's identity flip).
	c = testConfig(t, mod("batcave", mount))
	c.LegacyAuthGinUntil = "batcave"
	mustNew(t, c)
}

func TestReservedIDsIncludeLegacyTopSegments(t *testing.T) {
	batcave := mod("batcave", nil).with(func(m *module.Manifest) { m.LegacyPrefixes = []string{"/robin/", "/batcave/"} })
	// Another manifest's LegacyPrefixes first segment is reserved; a module's
	// own is not.
	_, err := New(t.Context(), testConfig(t, batcave, mod("robin", nil)))
	got := problems(t, err)
	if len(got) != 1 || got[0].Module != "robin" || got[0].Kind != module.ProblemReservedID {
		t.Fatalf("problems = %v, want ProblemReservedID for robin only", got)
	}
	c := testConfig(t, mod("joker", nil))
	c.ReservedAPINamespaces = []string{"joker"}
	if _, err := New(t.Context(), c); !hasKind(err, module.ProblemReservedID) {
		t.Fatalf("ID in ReservedAPINamespaces = %v, want ProblemReservedID", err)
	}
	// P7 re-checks IDs against every mount's TopSegments and SPAShared.
	_, err = New(t.Context(), testConfig(t, mod("robin", nil), mod("arkham", func(r *module.Registrar) error {
		r.MountLegacy(httpapi.LegacyMount{Handler: http.NotFoundHandler(), Auth: httpapi.LegacyAuthCore, SPAShared: []string{"robin"}})
		return nil
	})))
	if !hasKind(err, module.ProblemReservedID) || !hasKind(err, module.ProblemRouteOutsidePrefix) {
		t.Fatalf("mount segment equal to a module ID = %v, want ProblemReservedID and ProblemRouteOutsidePrefix", err)
	}
}

func TestReservedSegmentsDisjointFromNonSPA(t *testing.T) {
	// A SPAShared shell segment boots: GET /login is the shell page, POST
	// /login the mount's. The request half needs R1-14's manifest routes.
	mustNew(t, testConfig(t, mod("batcave", func(r *module.Registrar) error {
		r.MountLegacy(httpapi.LegacyMount{Handler: http.NotFoundHandler(), Auth: httpapi.LegacyAuthCore, TopSegments: []string{"login"}, SPAShared: []string{"login"}})
		return nil
	}).with(func(m *module.Manifest) { m.LegacyPrefixes = []string{"/login"} })))
	// The failing half is the table row ProblemSPASegmentCollision above.
}

func TestSPAOwner(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Owner", "batcave") })
	k := mustNew(t, testConfig(t, mod("batcave", func(r *module.Registrar) error {
		r.MountLegacy(httpapi.LegacyMount{Handler: spa, Auth: httpapi.LegacyAuthCore, ServesSPA: true})
		return nil
	})))
	rec := httptest.NewRecorder()
	k.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library", nil))
	if rec.Header().Get("X-Owner") != "batcave" {
		t.Fatalf("GET /library did not reach the ServesSPA mount: %d %v", rec.Code, rec.Header())
	}
}

func TestDepsScoping(t *testing.T) {
	capture := coretest.NewLogCapture(t)
	var got []module.Deps
	reg := func(r *module.Registrar) error {
		d := r.Deps()
		got = append(got, d)
		d.Log.Info("on patrol")
		d.ExtendStart("patrol")
		return nil
	}
	c := Config{AppID: "gotham", MetricNamespace: "gotham", Logger: capture.Logger(), Modules: []module.Module{mod("alfred", reg), mod("bane", reg)}}
	mustNew(t, c)
	if len(got) != 2 {
		t.Fatalf("Register calls = %d, want 2", len(got))
	}
	for _, d := range got {
		if d.Log == nil || d.Metrics == nil || d.ExtendStart == nil {
			t.Fatalf("Deps of %s has a nil handle: %+v", d.Module, d)
		}
		// Two modules compiled in: module series are <namespace>_<ID>_*.
		if want := "gotham." + string(d.Module); d.Metrics.Prefix() != want {
			t.Errorf("Deps.Metrics prefix of %s = %q, want %q", d.Module, d.Metrics.Prefix(), want)
		}
	}
	var mods []string
	for _, r := range capture.Records() {
		if r.Message != "on patrol" {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == log.KeyModule {
				mods = append(mods, a.Value.String())
			}
			return true
		})
	}
	if !slices.Equal(mods, []string{"alfred", "bane"}) {
		t.Fatalf("Deps.Log module attrs = %v, want alfred, bane", mods)
	}
	// One module compiled in: its series are <namespace>_*.
	got = nil
	mustNew(t, Config{AppID: "gotham", MetricNamespace: "gotham", Logger: capture.Logger(), Modules: []module.Module{mod("alfred", reg)}})
	if got[0].Metrics.Prefix() != "gotham" {
		t.Fatalf("single-module prefix = %q, want gotham", got[0].Metrics.Prefix())
	}
}

func TestRegistrarClosedAfterRegister(t *testing.T) {
	var kept *module.Registrar
	capture := coretest.NewLogCapture(t)
	c := testConfig(t, mod("batcave", func(r *module.Registrar) error { kept = r; return nil }))
	c.Logger = capture.Logger()
	k := mustNew(t, c)
	kept.Hook(module.Hook{Name: "batcave.late", Start: func(context.Context) error { t.Error("a late hook ran"); return nil }})
	if !errors.Is(kept.Err(), module.ErrRegistrarClosed) {
		t.Fatalf("late Hook: Err() = %v, want ErrRegistrarClosed", kept.Err())
	}
	if len(k.hooks) != 0 {
		t.Fatalf("the kernel holds %d hooks, want none", len(k.hooks))
	}
	// The review's m4: the kernel read Err once, so a late call is logged,
	// not lost.
	if !slices.ContainsFunc(capture.Records(), func(r slog.Record) bool {
		return r.Level == slog.LevelError && strings.Contains(r.Message, "registrar call after Register returned")
	}) {
		t.Fatalf("no ERROR for the late registrar call: %v", capture.Messages())
	}
}

func TestRegistrarContextCancelled(t *testing.T) {
	var regCtx context.Context
	mustNew(t, testConfig(t, mod("batcave", func(r *module.Registrar) error {
		regCtx = r.Context()
		if err := regCtx.Err(); err != nil {
			t.Errorf("Context() during Register: %v", err)
		}
		return nil
	})))
	if regCtx.Err() == nil {
		t.Fatal("Registrar.Context() still live after New returned")
	}
	// Cancelling New's ctx cancels it during Register.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := testConfig(t, mod("batcave", func(r *module.Registrar) error {
		cancel()
		<-r.Context().Done()
		return r.Context().Err()
	}))
	if _, err := New(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("New with its ctx cancelled during Register = %v, want context.Canceled", err)
	}
}

func TestDescribe_EveryModuleEnabled(t *testing.T) {
	handler := getManor
	quiet := mod("wayne", func(r *module.Registrar) error {
		httpapi.Handle(r.HTTP(), httpapi.Route{Method: http.MethodGet, Path: "/manor", Name: "getManor", Perm: rbac.Public}, handler)
		return nil
	}).with(func(m *module.Manifest) {
		m.DefaultEnabled = false
		m.Version = "2.3.4"
		m.PublicRoutes = []string{"getManor"}
	})
	d, err := Describe(t.Context(), Config{AppID: "gotham", Modules: []module.Module{quiet}})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Info  struct{ Version string } `json:"info"`
		Paths map[string]any           `json:"paths"`
	}
	if err := json.Unmarshal(d.OpenAPI["wayne"], &doc); err != nil {
		t.Fatalf("wayne document: %v", err)
	}
	if doc.Info.Version != "2.3.4" || doc.Paths["/api/wayne/v1/manor"] == nil {
		t.Fatalf("wayne document = %+v, want version 2.3.4 and /api/wayne/v1/manor", doc)
	}
	if err := json.Unmarshal(d.Core, &doc); err != nil || doc.Info.Version == "" {
		t.Fatalf("core document version = %q, %v", doc.Info.Version, err)
	}
	// Describe returns the documents a live boot builds (06 §6.10).
	live := mustNew(t, testConfig(t, quiet.with(func(m *module.Manifest) { m.DefaultEnabled = true })))
	ld, err := live.describe()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ld.Core, d.Core) || !bytes.Equal(ld.OpenAPI["wayne"], d.OpenAPI["wayne"]) {
		t.Fatal("Describe's documents differ from a live boot's")
	}
}

func TestKernelFamiliesGathered(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", Logger: coretest.NewLogCapture(t).Logger()})
	srv := httptest.NewServer(k.provider.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	families, err := scrapeFamilies(resp, metricstest.FamilyPrefix(k.scope))
	if err != nil {
		t.Fatal(err)
	}
	wantNames, err := readFamilies("testdata/kernel_families.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(families, wantNames) {
		t.Fatalf("kernel families served = %v, want %v (testdata/kernel_families.golden)", families, wantNames)
	}
	metricstest.AssertSeries(t, k.provider, k.scope, "testdata/series.golden")
	phases, err := bootPhases(k)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(phases, []string{"P1", "P5", "P6", "P7", "P8"}) {
		t.Fatalf("boot phases recorded = %v, want P1 P5 P6 P7 P8", phases)
	}
}
