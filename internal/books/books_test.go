// file: internal/books/books_test.go
// version: 1.0.2
// guid: 7d3e9b14-2c6a-4f80-b5d1-8e0a4c97f2b3
// last-edited: 2026-11-08

package books

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/version"
)

// The tests in this package install process globals (config.AppConfig,
// slog's default, the OTel globals), so none of them calls t.Parallel.

// syncBuffer is a bytes.Buffer safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// withConfig replaces config.AppConfig for the test and restores it after.
func withConfig(t *testing.T, c config.Config) {
	t.Helper()
	orig := config.AppConfig
	config.AppConfig = c
	t.Cleanup(func() { config.AppConfig = orig })
}

// semver parses MAJOR.MINOR.PATCH.
func semver(t *testing.T, s string) [3]int {
	t.Helper()
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		t.Fatalf("%q is not MAJOR.MINOR.PATCH", s)
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			t.Fatalf("%q is not MAJOR.MINOR.PATCH", s)
		}
		v[i] = n
	}
	return v
}

// TestBooksMinCoreVersion (09 §9.4, R1 classification): books' MinCoreVersion
// is no newer than the core it is built with, so the kernel's P1 check
// admits it.
func TestBooksMinCoreVersion(t *testing.T) {
	man := Module(nil).Manifest()
	min, core := semver(t, man.MinCoreVersion), semver(t, version.Version)
	if slices.Compare(min[:], core[:]) > 0 {
		t.Fatalf("books' MinCoreVersion %s is newer than core %s", man.MinCoreVersion, version.Version)
	}
	semver(t, man.Version)
}

// authRequiredScopes are the AuthRequired scopes 05-rbac §4.11 names. The
// catalog that carries the flag lands at F2 (F2-08); until then the list is
// the spec's.
func authRequiredScopes() []rbac.Permission {
	return []rbac.Permission{"core:admin", "core:audit.view", "core:system.manage", "core:backup.restore", "books:admin.debug"}
}

// expectedCatalog is every scope the catalog will hold for core and books
// (05-rbac §6.1's "New permissions with no atom" and the atom rows; F2-08
// builds the catalog).
func expectedCatalog() []rbac.Permission {
	return []rbac.Permission{
		"core:admin", "core:self", "core:roles.view", "core:roles.manage", "core:users.view", "core:users.manage",
		"core:audit.view", "core:system.status", "core:system.view", "core:system.manage", "core:backup.restore",
		"core:integrations.read", "core:integrations.manage", "core:tasks.view", "core:tasks.manage",
		"core:metrics.read", "core:library.manage", "core:library.browse", "core:settings.manage",
		"books:view", "books:edit", "books:delete", "books:organize", "books:scan", "books:integrations.manage",
		"books:settings.manage", "books:playlists.create", "books:collections.manage", "books:request",
		"books:request.approve", "books:fingerprint.worker", "books:reading", "books:admin.view", "books:admin.debug",
	}
}

// everyAtomScopes is the every-atom row of 05-rbac §6.1: core:admin plus
// every native scope of the 13 rows, for HEAD's admin seed and for a key
// whose owner holds HEAD's admin role. Any other holder of every atom gets
// the union of the rows (unionScopes), never core:admin (05-rbac §4.6,
// SEC-12). It and unionScopes live here until R2's identity cutover, their
// first production caller, moves them into atoms.go: the deadcode ratchet
// refuses production code no main reaches (seams.d/R1-15.md).
func everyAtomScopes() []rbac.Permission {
	var names []string
	for _, a := range AtomTable() {
		names = append(names, a.Name)
	}
	return append([]rbac.Permission{rbac.CoreAdmin}, unionScopes(names...)...)
}

// unionScopes is the native scopes of a set of atoms: the union of their
// rows, deduplicated, in table order. An unknown atom contributes nothing.
func unionScopes(atoms ...string) []rbac.Permission {
	var out []rbac.Permission
	for _, a := range AtomTable() {
		if !slices.Contains(atoms, a.Name) {
			continue
		}
		for _, s := range a.Scopes {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// TestAtomTable_Golden (05-rbac §12) pins the 13 rows of §6.1 and the
// every-atom row: every route requirement is AllOf all of its row's native
// scopes, no single-scope requirement is AuthRequired, playlists.create has
// none, users.manage alone has a read-side entry, and every scope the table
// writes is one the catalog will hold.
func TestAtomTable_Golden(t *testing.T) {
	want := map[string][]rbac.Permission{
		"library.view":          {"books:view", "core:tasks.view"},
		"library.edit_metadata": {"books:edit"},
		"library.delete":        {"books:delete"},
		"library.organize":      {"books:organize"},
		"scan.trigger":          {"books:scan"},
		"integrations.manage":   {"core:integrations.manage", "books:integrations.manage"},
		"users.manage":          {"core:users.manage", "core:roles.view"},
		"settings.manage": {"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
			"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"},
		"playlists.create":   {"books:playlists.create"},
		"collections.manage": {"books:collections.manage"},
		"requests.create":    {"books:request"},
		"requests.approve":   {"books:request.approve"},
		"fingerprint.worker": {"books:fingerprint.worker"},
	}
	order := []string{"library.view", "library.edit_metadata", "library.delete", "library.organize", "scan.trigger",
		"integrations.manage", "users.manage", "settings.manage", "playlists.create", "collections.manage",
		"requests.create", "requests.approve", "fingerprint.worker"}
	table := AtomTable()
	if len(table) != 13 {
		t.Fatalf("AtomTable has %d rows, want 13", len(table))
	}
	catalog := expectedCatalog()
	for i, a := range table {
		if a.Name != order[i] {
			t.Errorf("row %d is %s, want %s (05-rbac §6.1 order)", i, a.Name, order[i])
		}
		if !slices.Equal(a.Scopes, want[a.Name]) {
			t.Errorf("%s scopes = %v, want %v", a.Name, a.Scopes, want[a.Name])
		}
		for _, s := range a.Scopes {
			if !slices.Contains(catalog, s) {
				t.Errorf("%s writes %s, which the catalog will not hold", a.Name, s)
			}
		}
		if a.Name == "playlists.create" {
			if !a.Route.IsZero() {
				t.Errorf("playlists.create has route requirement %s, want none (no route enforces it)", a.Route)
			}
			continue
		}
		if a.Route.Kind() != rbac.KindAllOf || !slices.Equal(a.Route.Permissions(), a.Scopes) {
			t.Errorf("%s route requirement = %s, want AllOf%v", a.Name, a.Route, a.Scopes)
		}
		if len(a.Scopes) == 1 && slices.Contains(authRequiredScopes(), a.Scopes[0]) {
			t.Errorf("%s's single-scope requirement %s is AuthRequired", a.Name, a.Scopes[0])
		}
		if a.Name == "users.manage" {
			if a.ReadSide.Kind() != rbac.KindAllOf || !slices.Equal(a.ReadSide.Permissions(), []rbac.Permission{"core:users.view"}) {
				t.Errorf("users.manage read-side entry = %s, want core:users.view", a.ReadSide)
			}
		} else if !a.ReadSide.IsZero() {
			t.Errorf("%s has a read-side entry %s; only users.manage has one", a.Name, a.ReadSide)
		}
	}

	every := everyAtomScopes()
	if every[0] != rbac.CoreAdmin {
		t.Errorf("the every-atom row starts with %s, want core:admin", every[0])
	}
	var union []rbac.Permission
	for _, name := range order {
		for _, s := range want[name] {
			if !slices.Contains(union, s) {
				union = append(union, s)
			}
		}
	}
	if !slices.Equal(every[1:], union) {
		t.Errorf("the every-atom row = %v, want core:admin plus %v", every, union)
	}
	if got := unionScopes("library.view", "library.edit_metadata", "Riddler's atom"); !slices.Equal(got,
		[]rbac.Permission{"books:view", "core:tasks.view", "books:edit"}) {
		t.Errorf("unionScopes(library.view, library.edit_metadata, unknown) = %v", got)
	}
	if slices.Contains(unionScopes(order...), rbac.CoreAdmin) {
		t.Error("the union of every row holds core:admin; only the every-atom row may")
	}
}

// masterListRoutes are the four routes HEAD gated by adminRole and
// s.perm(users.manage): the manifest records them as 05-rbac §6.2's
// AllOf(core:users.manage, books:admin.view), not as the atom (see
// seams.d/R1-15.md for the §6.1/§6.2 difference).
func masterListRoutes() []string {
	return []string{
		"GET /api/v1/master-lists", "POST /api/v1/master-lists",
		"DELETE /api/v1/master-lists/:id", "GET /api/v1/master-lists/elsewhere",
	}
}

// TestManifest_SPermRoutesCarryAtomRequirement (05-http-api §12): every
// manifest entry recorded from an s.perm(atom) route has Perm equal to its
// atom's atom-table route requirement, built with no catalog, and every other
// entry is unchanged. ABS on and off. It checks both the manifest
// resolveRoutes gives Register and the routes the composed App mounted. The
// manifest's own test (internal/server) proves which chains run s.perm(atom).
func TestManifest_SPermRoutesCarryAtomRequirement(t *testing.T) {
	routes := map[string]rbac.Requirement{}
	for _, a := range AtomTable() {
		routes[a.Name] = a.Route
	}
	for _, abs := range []bool{false, true} {
		t.Run("abs="+strconv.FormatBool(abs), func(t *testing.T) {
			recorded := server.LegacyRoutes(abs)
			resolved, err := resolveRoutes(recorded)
			if err != nil {
				t.Fatalf("resolveRoutes: %v", err)
			}
			if len(resolved) != len(recorded) {
				t.Fatalf("resolveRoutes returned %d routes for %d", len(resolved), len(recorded))
			}
			// want is each route's expected mounted requirement, by method
			// and gin path.
			want := map[string]rbac.Requirement{}
			atoms, masterList := 0, []string{}
			for i, lr := range recorded {
				key := lr.Method + " " + lr.GinPath
				got := resolved[i]
				if got.Method != lr.Method || got.GinPath != lr.GinPath {
					t.Fatalf("resolveRoutes reordered: %s at %d is %s %s", key, i, got.Method, got.GinPath)
				}
				rest, gotRest := lr, got
				rest.Perm, gotRest.Perm = rbac.Requirement{}, rbac.Requirement{}
				if !reflect.DeepEqual(rest, gotRest) {
					t.Errorf("%s: resolveRoutes changed a field other than Perm", key)
				}
				switch lr.Perm.Kind() {
				case rbac.KindLegacyAtom:
					atoms++
					w, ok := routes[lr.Perm.Atom()]
					if !ok {
						t.Errorf("%s names atom %q, which the atom table lacks", key, lr.Perm.Atom())
						continue
					}
					want[key] = w
					if got.Perm.Kind() != rbac.KindAllOf || !slices.Equal(got.Perm.Permissions(), w.Permissions()) {
						t.Errorf("%s carries %s, want the atom table's %s", key, got.Perm, w)
					}
				case rbac.KindAllOf:
					want[key] = lr.Perm
					if slices.Contains(lr.Perm.Permissions(), "core:users.manage") {
						masterList = append(masterList, key)
					}
					if got.Perm.String() != lr.Perm.String() {
						t.Errorf("%s: %s became %s", key, lr.Perm, got.Perm)
					}
				default:
					want[key] = lr.Perm
					if got.Perm.String() != lr.Perm.String() {
						t.Errorf("%s: %s became %s", key, lr.Perm, got.Perm)
					}
				}
			}
			if atoms < 400 {
				t.Errorf("only %d s.perm(atom) rows; the manifest walk is broken", atoms)
			}
			slices.Sort(masterList)
			wantMaster := masterListRoutes()
			slices.Sort(wantMaster)
			if !slices.Equal(masterList, wantMaster) {
				t.Errorf("routes recorded with core:users.manage but not as the atom = %v, want exactly %v", masterList, wantMaster)
			}

			// What the composed App mounted: no legacy route still carries
			// an atom, and each carries the requirement above.
			c := bootComposed(t, abs, nil, nil)
			mounted := 0
			for ri := range c.app.Routes() {
				if ri.Kind != httpapi.RouteKindLegacy || ri.Owner != string(ID) {
					continue
				}
				mounted++
				key := ri.Method + " " + ri.Name
				w, ok := want[key]
				if !ok {
					t.Errorf("the App mounted %s, which the manifest lacks", key)
					continue
				}
				if ri.Perm.Kind() == rbac.KindLegacyAtom {
					t.Errorf("%s mounted with %s; Register did not resolve it", key, ri.Perm)
				}
				if ri.Perm.String() != w.String() {
					t.Errorf("%s mounted with %s, want %s", key, ri.Perm, w)
				}
			}
			if mounted != len(recorded) {
				t.Errorf("the App mounted %d legacy routes, want the manifest's %d", mounted, len(recorded))
			}
			// 03 R6 over the whole booted router, the resolved requirements
			// included.
			archtest.RoutesHavePermissions(t, c.app.Routes())
		})
	}

	t.Run("refusals", func(t *testing.T) {
		for _, atom := range []string{"playlists.create", "Penguin's atom"} {
			_, err := resolveRoutes([]httpapi.LegacyRoute{
				{Method: http.MethodGet, GinPath: "/api/v1/health", Perm: rbac.AnyAuthenticated},
				{Method: http.MethodPost, GinPath: "/api/v1/batcave", Perm: rbac.LegacyAtom(atom)},
			})
			if err == nil || !strings.Contains(err.Error(), "POST /api/v1/batcave") || !strings.Contains(err.Error(), atom) {
				t.Errorf("atom %q: resolveRoutes error = %v, want one naming the route and the atom", atom, err)
			}
		}
		if got, ok := routeRequirement(AtomTable(), rbac.AnyAuthenticated); !ok || got.Kind() != rbac.AnyAuthenticated.Kind() {
			t.Errorf("a non-atom requirement came back as %s, %v; want it unchanged", got, ok)
		}
	})
}

// TestOnlyCompositionRootImportsModule (06 §6.10): in the real import graph,
// no package other than the main package, cmd and internal/books imports
// mediacore/module or mediacore/app in non-test code. internal/arch's
// TestLayering enforces the same rule through its Restricted map (R1-01's
// guard); this is the row's named check, and it asserts the graph is not
// vacuous: books imports module and cmd imports app.
func TestOnlyCompositionRootImportsModule(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	pkgs, err := archtest.Load(archtest.LoadOptions{Dir: root, TagSets: archtest.DefaultTagSet()})
	if err != nil {
		t.Fatalf("the loader is broken, not the code: %v", err)
	}
	const modulePath = "github.com/falkcorp/audiobook-organizer"
	g := archtest.BuildGraph(modulePath, "github.com/falkcorp/mediacore/", pkgs)
	allowed := []string{".", "cmd", "internal/books"}
	seen := map[string][]string{}
	for pkg, imps := range g.Foreign {
		for _, imp := range imps {
			if imp != "github.com/falkcorp/mediacore/module" && imp != "github.com/falkcorp/mediacore/app" {
				continue
			}
			seen[imp] = append(seen[imp], pkg)
			if !slices.Contains(allowed, pkg) {
				t.Errorf("%s imports %s; only %v may", pkg, imp, allowed)
			}
		}
	}
	if !slices.Contains(seen["github.com/falkcorp/mediacore/module"], "internal/books") ||
		!slices.Contains(seen["github.com/falkcorp/mediacore/app"], "cmd") {
		t.Fatalf("the graph is vacuous: importers = %v", seen)
	}
}

// TestBooksDisabledInert (06 §6.10, gate G1 for the real module): booted
// through app.New with books disabled, books' Register (and so its hooks and
// its server) never runs, and nothing answers under its prefixes but 404.
// ABS on and off, since the prefixes differ.
func TestBooksDisabledInert(t *testing.T) {
	for _, abs := range []bool{false, true} {
		t.Run("abs="+strconv.FormatBool(abs), func(t *testing.T) {
			withConfig(t, config.Config{ABSAPIEnabled: abs})
			mod := Module(nil)
			h := coretest.New(t, coretest.WithModules(mod), coretest.WithDisabled(ID))
			coretest.AssertModuleInert(t, h, ID)
			m := mod.(*booksModule)
			if n := m.registered.Load(); n != 0 {
				t.Errorf("books' Register ran %d time(s) while disabled", n)
			}
			if m.srv.Load() != nil {
				t.Error("a disabled books built its server")
			}
			select {
			case <-Stopped(mod):
			default:
				t.Error("Stopped is open for a books that never built a server")
			}
		})
	}
}

// TestR1Listen_NoServerUsesCoreListener: with no server built (books
// disabled), books' R1Listen opens core's own listener, so the App still
// serves what it has.
func TestR1Listen_NoServerUsesCoreListener(t *testing.T) {
	m := Module(nil).(*booksModule)
	ln, err := m.r1Listen(httpapi.ListenOptions{Addr: "127.0.0.1:0"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "Alfred")
	}), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ln.(*httpapi.Listener); !ok {
		t.Fatalf("R1Listen without a server returned %T, want core's *httpapi.Listener", ln)
	}
	served := make(chan error, 1)
	go func() { served <- ln.Serve() }()
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "Alfred" {
		t.Errorf("served %q, want the App's handler", body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ln.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Errorf("Serve after Shutdown: %v", err)
	}
}

// TestR1Telemetry_TracerFromEnv: with OTEL_EXPORTER_OTLP_ENDPOINT set, the
// config serve snapshots yields R1Telemetry tracing, and after
// app.InstallProcessDefaults the global tracer provider is the App's SDK
// provider, not the no-op one, so production traces survive the move to the
// App's Provider.
func TestR1Telemetry_TracerFromEnv(t *testing.T) {
	withConfig(t, config.AppConfig)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://192.0.2.1:4317")
	config.InitConfig()
	tel := r1Telemetry(config.Snapshot())
	if tel.Tracing == nil || tel.Tracing.Endpoint != "http://192.0.2.1:4317" {
		t.Fatalf("R1Telemetry.Tracing = %+v, want the env endpoint", tel.Tracing)
	}
	mod := Module(nil)
	opts, err := Options(mod, ServeInputs{Server: server.GetDefaultServerConfig(), Telemetry: config.Snapshot()})
	if err != nil {
		t.Fatal(err)
	}
	opts.LogOutput = &syncBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// No books: the tracer is the App's, whatever modules it runs.
	opts.LegacyAuthGinUntil = ""
	a, err := app.New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := app.InstallProcessDefaults(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		restore()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = a.Metrics().Shutdown(sctx)
	})
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("the global tracer provider is %T, want the App's *sdktrace.TracerProvider", otel.GetTracerProvider())
	}
}

// tempDir is a directory for one test's database files.
func tempDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "batcave")
}
