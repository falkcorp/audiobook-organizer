// file: core/internal/modulehttp/modulehttp_test.go
// version: 1.0.0
// guid: 0b81ed3a-71eb-4116-9762-264f23af72d2
// last-edited: 2026-11-10

package modulehttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

func register(t testing.TB, d Deps) *httpapi.Router {
	t.Helper()
	r := newRouter()
	if err := Register(r.CoreGroup(), d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return r
}

func deps(view ModulesView) Deps {
	return Deps{Modules: view, Documents: DocumentMap{}}
}

func routes(r *httpapi.Router) []httpapi.RouteInfo {
	var out []httpapi.RouteInfo
	for ri := range r.Routes() {
		out = append(out, ri)
	}
	return out
}

// TestRegister_RouteTable pins 05-http-api §5 item 2 and §7 and 07 §7.5.1:
// each route's method, pattern, permission, kind, guards and rate class. None
// carries a guard (the manifest is a read, and the document route decides in
// its handler) and none uses RateExempt.
func TestRegister_RouteTable(t *testing.T) {
	want := []struct {
		method, pattern, name string
		kind                  httpapi.RouteKind
		perm                  rbac.RequirementKind
	}{
		{http.MethodGet, "/api/core/v1/modules", "listModules", httpapi.RouteKindNative, rbac.KindAuthenticated},
		{http.MethodGet, "/api/core/v1/openapi/{file}", "getOpenAPIDocument", httpapi.RouteKindRaw, rbac.KindPublic},
	}
	got := routes(register(t, deps(&fakeView{})))
	if len(got) != len(want) {
		t.Fatalf("%d routes registered, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		ri := got[i]
		if ri.Owner != "core" || ri.Method != w.method || ri.Pattern != w.pattern || ri.Name != w.name || ri.Kind != w.kind {
			t.Errorf("route %d = %s %s %s %s kind %d, want core %s %s %s kind %d", i, ri.Owner, ri.Method, ri.Pattern, ri.Name, ri.Kind, w.method, w.pattern, w.name, w.kind)
		}
		if ri.Perm.IsZero() || ri.Perm.Kind() != w.perm {
			t.Errorf("%s requires %s (kind %d), want kind %d", w.name, ri.Perm, ri.Perm.Kind(), w.perm)
		}
		if ri.Guards != (httpapi.Guards{}) {
			t.Errorf("%s has guards %+v, want none", w.name, ri.Guards)
		}
		if ri.RateClass != httpapi.RateDefault {
			t.Errorf("%s rate class = %d, want RateDefault", w.name, ri.RateClass)
		}
	}
}

// TestRegister_BuildFailsOnlyForWantOfAuth: until F4-08 a router refuses every
// non-Public native route with ErrNoAuthService, so Build cannot succeed for
// the manifest route. It checks everything else: a bad shape, a schema httpapi
// cannot describe, a non-snake_case name or a pattern conflict would each be a
// further error. The Public document route builds, so exactly one
// ErrNoAuthService is expected.
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	_, err := register(t, deps(&fakeView{})).Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		// A single problem is not a join.
		if !errors.Is(err, httpapi.ErrNoAuthService) {
			t.Fatalf("Build reported %v, want only ErrNoAuthService", err)
		}
		return
	}
	problems := joined.Unwrap()
	if len(problems) != 1 {
		t.Errorf("Build reported %d problems, want 1 ErrNoAuthService (the manifest route): %v", len(problems), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}

// TestModuleGroup_IsAuthenticated (05-http-api §12): every non-Public route this
// package registers requires at least AnyAuthenticated, and the Public one is
// the document route whose handler refuses an anonymous caller itself
// (TestOpenAPI_AnonymousIs401UnlessPublic). The manifest handler also refuses a
// request with no principal, because stage 13 is not built yet.
func TestModuleGroup_IsAuthenticated(t *testing.T) {
	var public []string
	for _, ri := range routes(register(t, deps(&fakeView{}))) {
		switch ri.Perm.Kind() {
		case rbac.KindPublic:
			public = append(public, ri.Name)
		case rbac.KindAuthenticated, rbac.KindAllOf, rbac.KindAnyOf:
		default:
			t.Errorf("%s requires %s, which is not an authenticated requirement", ri.Name, ri.Perm)
		}
	}
	if !reflect.DeepEqual(public, []string{"getOpenAPIDocument"}) {
		t.Errorf("Public routes = %v, want only getOpenAPIDocument", public)
	}

	view := &fakeView{snap: gotham()}
	a := &api{view: view, log: discard()}
	_, err := a.listModules(context.Background(), in[listModulesReq](nil, listModulesReq{}))
	wantStatus(t, err, http.StatusUnauthorized, "authentication_required")
	if view.calls != 0 {
		t.Errorf("an anonymous request read the catalog %d times", view.calls)
	}
}

// TestBuild_RejectsCamelCaseJSON (05-http-api §12): a native type with a
// createdAt tag is ErrJSONName at Build. It runs the real Router.Build over a
// twin of the manifest route whose response has a camelCase property, and
// requires that error to be reported; TestRegister_BuildFailsOnlyForWantOfAuth
// is the other half, that the real DTOs produce none.
func TestBuild_RejectsCamelCaseJSON(t *testing.T) {
	type camelResp struct {
		CreatedAt string `json:"createdAt"`
	}
	r := newRouter()
	httpapi.Handle(r.CoreGroup(), modulesRoute(), func(context.Context, *httpapi.In[listModulesReq]) (camelResp, error) {
		return camelResp{}, nil
	})
	_, err := r.Build()
	if !errors.Is(err, httpapi.ErrJSONName) {
		t.Fatalf("Build = %v, want ErrJSONName", err)
	}
}

func TestRegister_RefusesMissingDeps(t *testing.T) {
	for name, d := range map[string]Deps{
		"no view":      {Documents: DocumentMap{}},
		"no documents": {Modules: &fakeView{}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRouter()
			if err := Register(r.CoreGroup(), d); err == nil {
				t.Error("Register accepted a missing dependency")
			}
			if n := len(routes(r)); n != 0 {
				t.Errorf("a refused Register left %d routes registered", n)
			}
		})
	}
}

// jsonOf is v as the client receives it.
func jsonOf(t testing.TB, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestListModules_Manifest: the body is 07 §7.5.1's shape, with enabled modules
// sorted by ID and the disabled one absent from modules; every UI list is an
// array, a module with no UI declares empty ones.
func TestListModules_Manifest(t *testing.T) {
	a := &api{view: &fakeView{snap: gotham()}, log: discard()}
	got, err := a.listModules(context.Background(), in(user(), listModulesReq{}))
	if err != nil {
		t.Fatal(err)
	}
	want := ModulesResponse{
		Core:            CoreInfo{Version: "0.4.0"},
		App:             AppInfo{ID: "gcpd", DisplayName: "Gotham City Police", Version: "2.0.0", UIBuildID: "b3f1c2"},
		RestartRequired: true,
		Modules: []ModuleInfo{
			{ID: "batman", Version: "1.12.0", DisplayName: "Batman", UI: UIInfo{
				Pages: []string{"cave", "gadget"}, Nav: []string{}, SettingsPanels: []string{"cave"}, Widgets: []string{},
				Banners: []string{}, TopBarItems: []string{}, UserPage: []string{}, ActivityPanes: []string{}, SetupSteps: []string{"roof-signal"}}},
			{ID: "robin", Version: "1.0.0", DisplayName: "Robin", UI: UIInfo{
				Pages: []string{"patrol"}, Nav: []string{"patrol"}, SettingsPanels: []string{}, Widgets: []string{},
				Banners: []string{}, TopBarItems: []string{}, UserPage: []string{}, ActivityPanes: []string{}, SetupSteps: []string{}}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("manifest = %+v\nwant     %+v", got, want)
	}

	// The wire names, as snake_case as 05-http-api §3.4 requires, and no null list.
	m := jsonOf(t, got)
	for _, k := range []string{"core", "app", "restart_required", "modules"} {
		if _, ok := m[k]; !ok {
			t.Errorf("manifest has no %q member: %v", k, m)
		}
	}
	app := m["app"].(map[string]any)
	for _, k := range []string{"id", "display_name", "version", "ui_build_id"} {
		if _, ok := app[k]; !ok {
			t.Errorf("app has no %q member: %v", k, app)
		}
	}
	ui := m["modules"].([]any)[0].(map[string]any)["ui"].(map[string]any)
	for _, k := range []string{"pages", "nav", "settings_panels", "widgets", "banners", "top_bar_items", "user_page", "activity_panes", "setup_steps"} {
		if l, ok := ui[k].([]any); !ok || l == nil {
			t.Errorf("ui.%s = %v, want an array", k, ui[k])
		}
	}
}

// TestListModules_DisabledModulesOnlyForAdmin (07 §7.5.1): disabled_modules is
// absent for a caller without core:admin, present for one with it, and an
// admin with nothing disabled gets an empty array, not an absent member. A
// principal with no compiled permission set is not an admin.
func TestListModules_DisabledModulesOnlyForAdmin(t *testing.T) {
	a := &api{view: &fakeView{snap: gotham()}, log: discard()}
	ctx := context.Background()

	got, _ := a.listModules(ctx, in(user(), listModulesReq{}))
	if _, ok := jsonOf(t, got)["disabled_modules"]; ok {
		t.Error("a caller without core:admin sees disabled_modules")
	}
	for _, m := range got.Modules {
		if m.ID == "joker" {
			t.Error("a disabled module is listed in modules")
		}
	}

	got, _ = a.listModules(ctx, in(admin(), listModulesReq{}))
	if !reflect.DeepEqual(got.DisabledModules, []string{"joker"}) {
		t.Errorf("admin disabled_modules = %v, want [joker]", got.DisabledModules)
	}

	allOn := gotham()
	allOn.Modules[1].Enabled = true
	a.view = &fakeView{snap: allOn}
	got, _ = a.listModules(ctx, in(admin(), listModulesReq{}))
	if s, ok := jsonOf(t, got)["disabled_modules"].([]any); !ok || len(s) != 0 {
		t.Errorf("admin with nothing disabled: disabled_modules = %v, want []", jsonOf(t, got)["disabled_modules"])
	}

	bare := &auth.Principal{Method: auth.MethodSession}
	got, err := a.listModules(ctx, in(bare, listModulesReq{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.DisabledModules != nil {
		t.Error("a principal with no permission set was treated as an admin")
	}
}

// TestListModules_SnapshotErrorIs503WithoutCause: a view that fails is a 503, not
// an empty manifest (which would hide every page), and its text reaches neither
// the response nor, beyond the one log line, anything else.
func TestListModules_SnapshotErrorIs503WithoutCause(t *testing.T) {
	a := &api{view: &fakeView{err: errors.New("pebble: /var/lib/gotham/catalog: corrupt")}, log: discard()}
	_, err := a.listModules(context.Background(), in(user(), listModulesReq{}))
	wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	he, _ := errors.AsType[*httpapi.Error](err)
	if strings.Contains(he.Error(), "pebble") || strings.Contains(he.Detail, "gotham") {
		t.Errorf("the 503 carries the cause: %v", he)
	}
}
