// file: core/internal/modulehttp/modulehttp.go
// version: 1.0.1
// guid: bff56a15-dc5f-476c-8aea-4d0f3836947f
// last-edited: 2026-11-11

// Package modulehttp is the HTTP surface of the module catalog and of the
// served OpenAPI documents (05-http-api §5 item 2 and §7; 07 §7.5.1; FE3-16):
//
//   - GET /api/core/v1/modules, the server manifest the shell intersects with
//     its compile-time registry (07 §7.5), over the kernel's read-only
//     ModulesView, with <ns>_ui_manifest_requests_total{outcome} (07 §7.16);
//   - GET /api/core/v1/openapi/{file}, one built OpenAPI document per owner,
//     over a read-only Documents view (see openapi.go).
//
// The package imports nothing above L6: ModulesView and Documents are
// declared here with plain-data types, and the kernel (L8) passes adapters
// over its catalog and its built documents, so no L6 to L8 import exists. It
// is layer L6 (03 R4, internal/<capability>http), imported only by kernel, and
// holds no package-level mutable state (R5). It is dark until F4-08 mounts it.
package modulehttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// ModulesView is the kernel's read-only view of the module catalog (06 §6.4.6).
// It is one method on purpose: restart_required and each module's enablement
// change together when a setting does, and one Snapshot cannot be torn between
// two reads. The kernel's adapter builds it from its catalog, version.Version
// and the served bundle's build ID. A returned error is a 503, not an empty
// manifest: a shell that read "no modules" would hide every page.
type ModulesView interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// Snapshot is the catalog at one moment.
type Snapshot struct {
	CoreVersion     string // version.Version
	App             App
	RestartRequired bool // stored enablement differs from the running state (06 §6.5.3)
	Modules         []Module
}

// App identifies the running app.
type App struct {
	ID          string
	DisplayName string
	Version     string
	// UIBuildID is the served shell bundle's build ID (ui.Bundle.BuildID()),
	// "" while a legacy mount owns / or no bundle is served (07 §7.5.1). It is
	// not the X-App-Build value.
	UIBuildID string
}

// Module is one catalog entry, enabled or not.
type Module struct {
	ID          string
	Version     string
	DisplayName string
	Enabled     bool
	UI          UI
}

// UI lists the descriptor IDs a module's client bundle may contribute
// (module.UIDescriptor, 07 §7.4.2), copied here because module is L7.
type UI struct {
	Pages, Nav, SettingsPanels, Widgets, Banners []string
	TopBarItems, UserPage, ActivityPanes         []string
	SetupSteps                                   []string
}

// Deps are what Register needs. Modules and Documents are required: Register
// refuses their absence rather than mount a route that fails at request time.
type Deps struct {
	Modules   ModulesView
	Documents Documents
	// OpenAPIPublic reports core.http.openapi.public, read at each request (the
	// key is hot-reloadable, 05-http-api §10). Nil means false.
	OpenAPIPublic func() bool
	// Scope is the core "ui" metrics scope. Nil records nothing.
	Scope *metrics.Scope
	// Log receives the one line a failed snapshot writes. Nil discards.
	Log *slog.Logger
}

// api is what the handlers share; its fields are fixed at Register.
type api struct {
	view    ModulesView
	docs    Documents
	public  func() bool
	metrics *uiMetrics
	log     *slog.Logger
}

// maxBody is the body limit of the routes: none of them reads a body, so the
// smallest the router accepts.
const maxBody = 1 << 10

// Register registers the module and OpenAPI routes on g, which must be the
// router's CoreGroup(). It returns an error, and registers nothing, for a nil
// view or document set or a metrics scope that refuses the counter.
func Register(g *httpapi.Group, d Deps) error {
	a, err := newAPI(d)
	if err != nil {
		return err
	}
	httpapi.Handle(g, modulesRoute(), a.listModules)
	registerOpenAPI(g, a)
	return nil
}

// newAPI checks d and fills the defaults: no flag means not public, no logger
// discards.
func newAPI(d Deps) (*api, error) {
	if d.Modules == nil {
		return nil, errors.New("modulehttp: Deps.Modules is required")
	}
	if d.Documents == nil {
		return nil, errors.New("modulehttp: Deps.Documents is required")
	}
	m, err := newMetrics(d.Scope)
	if err != nil {
		return nil, err
	}
	a := &api{view: d.Modules, docs: d.Documents, public: d.OpenAPIPublic, metrics: m, log: d.Log}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	if a.public == nil {
		a.public = func() bool { return false }
	}
	return a, nil
}

// modulesRoute is GET /modules: 07 §7.5.1 names the permission,
// rbac.AnyAuthenticated, and no guard or rate class.
func modulesRoute() httpapi.Route {
	return httpapi.Route{
		Method: http.MethodGet, Path: "/modules", Name: "listModules", Perm: rbac.AnyAuthenticated,
		Tags: []string{"modules"}, MaxBody: maxBody,
		Summary: "The server manifest: core and app identity, the enabled modules and what UI each may contribute",
		Doc:     "The shell intersects this list with its compile-time registry and drops what is not declared (07 §7.5). A disabled module is absent from modules; disabled_modules lists them for a caller holding core:admin and is absent for every other caller.",
	}
}

// ---- wire types (snake_case, 05-http-api §3.4) ------------------------------

// ModulesResponse is GET /modules (07 §7.5.1).
type ModulesResponse struct {
	Core            CoreInfo     `json:"core"`
	App             AppInfo      `json:"app"`
	RestartRequired bool         `json:"restart_required"`
	Modules         []ModuleInfo `json:"modules"`
	// DisabledModules is present, possibly empty, only for a caller holding
	// core:admin: a non-nil slice is emitted, nil is omitted.
	DisabledModules []string `json:"disabled_modules,omitzero"`
}

// CoreInfo is the core library's version (version.Version).
type CoreInfo struct {
	Version string `json:"version"`
}

// AppInfo is the running app. UIBuildID is "" when no shell bundle is served.
type AppInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
	UIBuildID   string `json:"ui_build_id"`
}

// ModuleInfo is one enabled module.
type ModuleInfo struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	DisplayName string `json:"display_name"`
	UI          UIInfo `json:"ui"`
}

// UIInfo is a module's declared descriptor IDs; every list is an array, never
// null, so the shell can iterate without a guard.
type UIInfo struct {
	Pages          []string `json:"pages"`
	Nav            []string `json:"nav"`
	SettingsPanels []string `json:"settings_panels"`
	Widgets        []string `json:"widgets"`
	Banners        []string `json:"banners"`
	TopBarItems    []string `json:"top_bar_items"`
	UserPage       []string `json:"user_page"`
	ActivityPanes  []string `json:"activity_panes"`
	SetupSteps     []string `json:"setup_steps"`
}

type listModulesReq struct{}

// errUnauthenticated is the 401 of a route that finds no principal. The router's
// stage 13 answers it first; the handler repeats it because stage 13 is not
// built yet (F4-08) and a route mounted bare must not serve anonymously.
func errUnauthenticated() error {
	return &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "sign in to use this route"}
}

// errNotSignedIn is the 403 of a principal that is not AnyAuthenticated.
func errNotSignedIn() error {
	return &httpapi.Error{Status: http.StatusForbidden, Code: "permission_denied", Detail: "this route needs a signed-in caller"}
}

// signedIn applies rbac.AnyAuthenticated to p as stage 13 will (F4-08): no
// principal is 401; a principal with no compiled permission set, or one that
// does not satisfy it (a Confined key never does, 05-rbac 4.10), is 403.
func signedIn(p *auth.Principal) error {
	switch {
	case p == nil:
		return errUnauthenticated()
	case p.Perms == nil || !p.Perms.Satisfies(rbac.AnyAuthenticated):
		return errNotSignedIn()
	}
	return nil
}

func (a *api) listModules(ctx context.Context, in *httpapi.In[listModulesReq]) (ModulesResponse, error) {
	p := in.Principal
	if err := signedIn(p); err != nil {
		a.metrics.record(ctx, outcomeUnauthenticated)
		return ModulesResponse{}, err
	}
	snap, err := a.view.Snapshot(ctx)
	if err != nil {
		a.metrics.record(ctx, outcomeError)
		a.log.Error("module manifest unavailable", "err", err)
		return ModulesResponse{}, httpapi.Unavailable("the module manifest is not available", 0)
	}
	admin := p.Perms != nil && p.Perms.Has(rbac.CoreAdmin)
	out := ModulesResponse{
		Core:            CoreInfo{Version: snap.CoreVersion},
		App:             AppInfo(snap.App),
		RestartRequired: snap.RestartRequired,
		Modules:         []ModuleInfo{},
	}
	mods := slices.SortedFunc(slices.Values(snap.Modules), func(x, y Module) int { return strings.Compare(x.ID, y.ID) })
	if admin {
		out.DisabledModules = []string{}
	}
	for _, m := range mods {
		if !m.Enabled {
			if admin {
				out.DisabledModules = append(out.DisabledModules, m.ID)
			}
			continue
		}
		out.Modules = append(out.Modules, ModuleInfo{ID: m.ID, Version: m.Version, DisplayName: m.DisplayName, UI: uiInfo(m.UI)})
	}
	if admin {
		a.metrics.record(ctx, outcomeAdmin)
	} else {
		a.metrics.record(ctx, outcomeOK)
	}
	return out, nil
}

// uiInfo copies u with every nil list turned into an empty one.
func uiInfo(u UI) UIInfo {
	l := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return slices.Clone(s)
	}
	return UIInfo{
		Pages: l(u.Pages), Nav: l(u.Nav), SettingsPanels: l(u.SettingsPanels), Widgets: l(u.Widgets),
		Banners: l(u.Banners), TopBarItems: l(u.TopBarItems), UserPage: l(u.UserPage),
		ActivityPanes: l(u.ActivityPanes), SetupSteps: l(u.SetupSteps),
	}
}
