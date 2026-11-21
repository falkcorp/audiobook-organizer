// file: core/kernel/metrics_mount_test.go
// version: 0.1.0
// guid: e462e8ad-afe8-4e04-a995-38f7b108a63f
// last-edited: 2026-11-20

package kernel

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// mmChecker holds exactly its scopes (no implication, no admin rule).
type mmChecker []rbac.Permission

func (c mmChecker) Has(p rbac.Permission) bool { return slices.Contains(c, p) }
func (c mmChecker) Satisfies(r rbac.Requirement) bool {
	switch r.Kind() {
	case rbac.KindPublic, rbac.KindAuthenticated:
		return true
	case rbac.KindAllOf:
		for _, p := range r.Permissions() {
			if !c.Has(p) {
				return false
			}
		}
		return true
	}
	return false
}
func (mmChecker) Confined() bool                { return false }
func (c mmChecker) Concrete() []rbac.Permission { return slices.Clone(c) }

// mmHasNotSatisfies answers Has for core:metrics.read and refuses every
// requirement; mmSatisfiesNotHas admits every requirement and holds nothing.
// The gate repeats both of the checker's answers, so a gate that drops either
// one admits one of these.
type mmHasNotSatisfies struct{ mmChecker }

func (mmHasNotSatisfies) Satisfies(rbac.Requirement) bool { return false }

type mmSatisfiesNotHas struct{ mmChecker }

func (mmSatisfiesNotHas) Has(rbac.Permission) bool        { return false }
func (mmSatisfiesNotHas) Satisfies(rbac.Requirement) bool { return true }
func (mmSatisfiesNotHas) Concrete() []rbac.Permission     { return nil }

func mmWith(c rbac.Checker) *auth.Principal {
	return &auth.Principal{Method: auth.MethodAPIKey, Perms: c, CredentialID: "cred-oracle",
		User: &auth.User{ID: "user-oracle", Username: "oracle"}, ClientAddr: netip.MustParseAddr("192.0.2.9")}
}

func mmAs(m auth.Method, perms ...rbac.Permission) *auth.Principal {
	return &auth.Principal{Method: m, Perms: mmChecker(perms), CredentialID: "cred-oracle",
		User: &auth.User{ID: "user-oracle", Username: "oracle"}, ClientAddr: netip.MustParseAddr("192.0.2.9")}
}

// mmRecorder is an infraMounter that records what the mount registered, and
// forwards it to a real router so the router's own record is checked too.
type mmRecorder struct {
	router  *httpapi.Router
	pattern string
	perm    rbac.Requirement
	h       http.Handler
	calls   int
}

func (m *mmRecorder) Infra(pattern string, perm rbac.Requirement, h http.Handler) {
	m.calls++
	m.pattern, m.perm, m.h = pattern, perm, h
	m.router.Infra(pattern, perm, h)
}

func newMMRecorder() *mmRecorder {
	return &mmRecorder{router: httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})}
}

// metricsRoutes is every route of r whose pattern is path.
func metricsRoutes(r *httpapi.Router, path string) []httpapi.RouteInfo {
	var out []httpapi.RouteInfo
	for ri := range r.Routes() {
		if ri.Pattern == path {
			out = append(out, ri)
		}
	}
	return out
}

func gatheredNames(t *testing.T, g prometheus.Gatherer) []string {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, mf := range mfs {
		out = append(out, mf.GetName())
	}
	return out
}

func serve(t *testing.T, h http.Handler, p *auth.Principal) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

// TestMetricsMount_RequiresMetricsRead is the core half of 05-http-api §12's
// TestInfra_MetricsRequiresScope, on the handler the mount registered: no
// principal is 401; a principal without core:metrics.read is 403 whatever
// else it holds (core:system.manage, core:tasks.view) and whatever its method;
// a principal holding it gets the scrape. The route is recorded as a core
// Infra route on GET /metrics requiring exactly core:metrics.read, which is
// what the chain's stages 10-13 will enforce once auth exists (the chain half
// of the test lands with the F8-04 fold, because a router with no auth
// service cannot Build a non-Public route today).
func TestMetricsMount_RequiresMetricsRead(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", Logger: logcapture.New(t).Logger(), StoragePath: tempStoragePath(t), Version: "1.2.3"})
	rec := newMMRecorder()
	if err := mountMetrics(rec, k.provider, "", "1.2.3", nil); err != nil {
		t.Fatal(err)
	}
	if rec.calls != 1 || rec.pattern != "GET /metrics" || rec.h == nil {
		t.Fatalf("mount registered %d routes, last %q (handler %v)", rec.calls, rec.pattern, rec.h != nil)
	}
	if got := rec.perm.Permissions(); rec.perm.Kind() != rbac.KindAllOf || !slices.Equal(got, []rbac.Permission{"core:metrics.read"}) {
		t.Fatalf("route permission = %v (%v), want exactly core:metrics.read", got, rec.perm.Kind())
	}
	ris := metricsRoutes(rec.router, "/metrics")
	if len(ris) != 1 || ris[0].Kind != httpapi.RouteKindInfra || ris[0].Owner != "core" || ris[0].Method != http.MethodGet ||
		!slices.Equal(ris[0].Perm.Permissions(), []rbac.Permission{"core:metrics.read"}) {
		t.Fatalf("router records %+v, want one core Infra GET /metrics requiring core:metrics.read", ris)
	}

	cases := []struct {
		name string
		p    *auth.Principal
		want int
	}{
		{"no principal", nil, http.StatusUnauthorized},
		{"no checker", &auth.Principal{Method: auth.MethodAPIKey, User: &auth.User{ID: "user-oracle"}}, http.StatusForbidden},
		{"key without the scope", mmAs(auth.MethodAPIKey), http.StatusForbidden},
		{"system manager without the scope", mmAs(auth.MethodSession, "core:system.manage", "core:tasks.view", "core:self"), http.StatusForbidden},
		// Each half of the gate on its own: Has without Satisfies, and
		// Satisfies without Has, are both refused. mmChecker answers the two
		// alike, so it cannot tell a gate that lost one half.
		{"holds the scope but does not satisfy", mmWith(mmHasNotSatisfies{mmChecker{"core:metrics.read"}}), http.StatusForbidden},
		{"satisfies but does not hold", mmWith(mmSatisfiesNotHas{}), http.StatusForbidden},
		{"key with the scope", mmAs(auth.MethodAPIKey, "core:metrics.read"), http.StatusOK},
		{"session with the scope", mmAs(auth.MethodSession, "core:metrics.read"), http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := serve(t, rec.h, c.p)
			if status != c.want {
				t.Fatalf("status %d, want %d: %s", status, c.want, body)
			}
			if c.want != http.StatusOK && strings.Contains(body, "go_goroutines") {
				t.Fatalf("a refused scrape leaked the exposition: %s", body)
			}
			if c.want == http.StatusOK && !strings.Contains(body, "go_goroutines") {
				t.Fatalf("an admitted scrape has no exposition: %s", body)
			}
		})
	}
}

// TestMetricsMount_RealCheckers runs the gate over checkers compiled by the
// real authz.Compile against the real core catalog, not over the hand-written
// mmChecker: what the scope's holders and non-holders are is the catalog's and
// the compiler's answer, and the gate must agree with it for sessions, keys and
// a key narrowed by scopes.
func TestMetricsMount_RealCheckers(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", Logger: logcapture.New(t).Logger(), StoragePath: tempStoragePath(t)})
	rec := newMMRecorder()
	if err := mountMetrics(rec, k.provider, "", "1.2.3", nil); err != nil {
		t.Fatal(err)
	}
	comp := newDWCompiler(t)
	comp.roles["operator"] = []rbac.Permission{"core:system.manage", "core:tasks.view"}
	as := func(m auth.Method, in rbac.CheckerInput) *auth.Principal {
		c := comp.Compile(in)
		if c == nil {
			t.Fatalf("no checker for %+v", in)
		}
		return &auth.Principal{Method: m, Perms: c, CredentialID: "cred-oracle",
			User: &auth.User{ID: "user-oracle", Username: "oracle"}, ClientAddr: netip.MustParseAddr("192.0.2.9")}
	}
	cases := []struct {
		name string
		p    *auth.Principal
		want int
	}{
		{"session of a viewer", as(auth.MethodSession, rbac.CheckerInput{Roles: []rbac.RoleName{"viewer"}, Interactive: true}), http.StatusOK},
		{"key of a viewer", as(auth.MethodAPIKey, rbac.CheckerInput{Roles: []rbac.RoleName{"viewer"}}), http.StatusOK},
		{"key of a viewer narrowed to the scope", as(auth.MethodAPIKey, rbac.CheckerInput{Roles: []rbac.RoleName{"viewer"}, Scopes: []rbac.Permission{"core:metrics.read"}}), http.StatusOK},
		{"key of a viewer narrowed away from the scope", as(auth.MethodAPIKey, rbac.CheckerInput{Roles: []rbac.RoleName{"viewer"}, Scopes: []rbac.Permission{"core:self"}}), http.StatusForbidden},
		{"key of an admin narrowed away from the scope", as(auth.MethodAPIKey, rbac.CheckerInput{Roles: []rbac.RoleName{"admin"}, Scopes: []rbac.Permission{"core:system.manage"}}), http.StatusForbidden},
		{"session of an operator", as(auth.MethodSession, rbac.CheckerInput{Roles: []rbac.RoleName{"operator"}, Interactive: true}), http.StatusForbidden},
		{"session of nobody", as(auth.MethodSession, rbac.CheckerInput{Interactive: true}), http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if status, body := serve(t, rec.h, c.p); status != c.want {
				t.Fatalf("status %d, want %d: %s", status, c.want, body)
			}
			// The gate repeats what the route records: the real checker
			// answers the route's own requirement the same way.
			if got := c.p.Perms.Satisfies(rec.perm); got != (c.want == http.StatusOK) {
				t.Errorf("checker.Satisfies(route requirement) = %v, the handler answered %d", got, c.want)
			}
		})
	}
}

// TestMetricsMount_DarkUntilMounted is the dark proof. A booted kernel's
// Provider (which ABO's gin /metrics serves today) carries no build_info
// family and no /metrics route is mounted; the global Prometheus registry
// never carries one at all. Only mountMetrics adds the gauge, with the exact
// exposition <namespace>_build_info{version="…"} 1 under the app's namespace.
// kernel_families.golden is untouched by this row, and its test staying green
// is the rest of the proof.
func TestMetricsMount_DarkUntilMounted(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", Logger: logcapture.New(t).Logger(), StoragePath: tempStoragePath(t),
		MetricNamespace: "gotham_city", Version: "7.0.1"})
	hasBuildInfo := func(names []string) bool {
		return slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, "_build_info") })
	}
	if names := gatheredNames(t, k.provider.Gatherer()); hasBuildInfo(names) {
		t.Fatalf("a booted kernel's Provider already carries build_info: %v", names)
	}
	if names := gatheredNames(t, prometheus.DefaultGatherer); hasBuildInfo(names) {
		t.Fatalf("the default registry carries build_info before the mount: %v", names)
	}
	if ris := metricsRoutes(k.router, "/metrics"); len(ris) != 0 {
		t.Fatalf("a booted kernel already mounts /metrics: %+v", ris)
	}

	rec := newMMRecorder()
	if err := mountMetrics(rec, k.provider, "", "7.0.1", nil); err != nil {
		t.Fatal(err)
	}
	if names := gatheredNames(t, k.provider.Gatherer()); !slices.Contains(names, "gotham_city_build_info") {
		t.Fatalf("after the mount the Provider gathers %v, want gotham_city_build_info", names)
	}
	if names := gatheredNames(t, prometheus.DefaultGatherer); hasBuildInfo(names) {
		t.Fatalf("the mount registered build_info on the default registry: %v", names)
	}
	_, body := serve(t, rec.h, mmAs(auth.MethodAPIKey, "core:metrics.read"))
	var lines []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "gotham_city_build_info") {
			lines = append(lines, line)
		}
	}
	if want := []string{`gotham_city_build_info{version="7.0.1"} 1`}; !slices.Equal(lines, want) {
		t.Fatalf("build_info series = %q, want %q", lines, want)
	}

	// The scope is the Provider's once: a second mount fails instead of
	// registering a second family.
	if err := mountMetrics(newMMRecorder(), k.provider, "", "7.0.1", nil); err == nil {
		t.Fatal("a second mount on the same Provider succeeded")
	}
}

// A custom core.metrics.path is mounted as given; nil collaborators refuse.
func TestMetricsMount_PathAndNilDeps(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", Logger: logcapture.New(t).Logger(), StoragePath: tempStoragePath(t)})
	rec := newMMRecorder()
	if err := mountMetrics(rec, k.provider, "/internal/scrape", "", nil); err != nil {
		t.Fatal(err)
	}
	if rec.pattern != "GET /internal/scrape" {
		t.Fatalf("pattern = %q", rec.pattern)
	}
	if err := mountMetrics(nil, k.provider, "", "", nil); err == nil {
		t.Error("a nil router was accepted")
	}
	if err := mountMetrics(newMMRecorder(), nil, "", "", nil); err == nil {
		t.Error("a nil Provider was accepted")
	}
}

// An empty or blank Config.Version is labelled "dev", never version="".
func TestMetricsMount_UnsetVersionIsDev(t *testing.T) {
	for _, v := range []string{"", "  "} {
		k := mustNew(t, Config{AppID: "gotham", Logger: logcapture.New(t).Logger(), StoragePath: tempStoragePath(t), MetricNamespace: "gotham_city"})
		rec := newMMRecorder()
		if err := mountMetrics(rec, k.provider, "", v, nil); err != nil {
			t.Fatal(err)
		}
		_, body := serve(t, rec.h, mmAs(auth.MethodAPIKey, "core:metrics.read"))
		if want := `gotham_city_build_info{version="dev"} 1`; !strings.Contains(body, want) {
			t.Errorf("version %q: exposition lacks %s", v, want)
		}
		if strings.Contains(body, `build_info{version=""}`) {
			t.Errorf("version %q: an empty version label was exposed", v)
		}
	}
}

// The mount's own handler gzips for a client that accepts it (promhttp's
// default), which is what lets mountMetrics's comment say a non-default path
// is not left uncompressed and is not compressed twice: the chain meets a
// handler-set Content-Encoding: gzip and passes it through.
func TestMetricsMount_HandlerGzipsItself(t *testing.T) {
	k := mustNew(t, Config{AppID: "gotham", Logger: logcapture.New(t).Logger(), StoragePath: tempStoragePath(t)})
	rec := newMMRecorder()
	if err := mountMetrics(rec, k.provider, "/internal/scrape", "1.2.3", nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/internal/scrape", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req = req.WithContext(auth.WithPrincipal(req.Context(), mmAs(auth.MethodAPIKey, "core:metrics.read")))
	w := httptest.NewRecorder()
	rec.h.ServeHTTP(w, req)
	res := w.Result()
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d, Content-Encoding %q; want 200 and gzip", res.StatusCode, res.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "go_goroutines") {
		t.Fatalf("the gzip body is not the exposition: %.200s", b)
	}
}
