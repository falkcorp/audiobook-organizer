// file: core/internal/integrationhttp/corequirement_test.go
// version: 1.0.0
// guid: 1ce9787c-85d4-40dc-aa7e-c82b48b08c9b
// last-edited: 2026-11-28

package integrationhttp

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/rbac"
)

// The app's co-requirement on the integrations manage routes (W6, 05-rbac
// §6.1): ABO's integrations.manage atom is AllOf(core:integrations.manage,
// books:integrations.manage), and the core routes name only the first. These
// cells mount the real Register over a real Router with the co-requirement
// the app will declare, and serve requests through the built handler.

const appManage rbac.Permission = "books:integrations.manage"

// manageRouteNames are the six routes that declare integrations.ManagePerm.
func manageRouteNames() []string {
	return []string{"createIntegrationInstance", "updateIntegrationInstance", "deleteIntegrationInstance",
		"testIntegration", "testIntegrationInstance", "releaseIntegrationInstanceHold"}
}

// headerAuthn is stage 12: it binds the principal the X-Test-As header names.
type headerAuthn struct{ as map[string][]rbac.Permission }

func (a headerAuthn) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if perms, ok := a.as[r.Header.Get("X-Test-As")]; ok {
				if p, ok := auth.PrincipalFrom(integrationstest.As(r.Context(), auth.MethodSession, perms...)); ok {
					r = r.WithContext(auth.WithPrincipal(r.Context(), p))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (headerAuthn) SessionCookie(auth.ClientInfo, string, time.Time) *http.Cookie {
	return &http.Cookie{Name: "gotham_session", Path: "/", MaxAge: -1}
}

func appCatalog(t *testing.T) *rbac.Catalog {
	t.Helper()
	cat, err := rbac.NewCatalog(append(rbac.CoreScopes(),
		rbac.PermissionSpec{Name: appManage, Title: "Manage integrations", Description: "the app's manage scope"})...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// coRequiredRouter mounts Register with co as the router's co-requirements.
func coRequiredRouter(t *testing.T, co map[string]rbac.Requirement) (http.Handler, error) {
	t.Helper()
	e := newEnv(t)
	r := httpapi.NewRouter(httpapi.Options{CoRequirements: co}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	if err := r.SetAuth(httpapi.AuthWiring{
		Service: headerAuthn{as: map[string][]rbac.Permission{
			"core-only": {integrations.ManagePerm, permRead},
			"both":      {integrations.ManagePerm, permRead, appManage},
			"app-only":  {appManage, permRead},
		}},
		Catalog: appCatalog(t),
	}); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}
	Register(r.CoreGroup(), e.reg, e.holds, e.log)
	return r.Build()
}

// TestRegister_ManageRoutesTakeTheAppCoRequirement: with books:
// integrations.manage declared on each of the six manage routes, a native role
// holding only core:integrations.manage is refused 403 naming the app's scope
// on every one, a principal holding both reaches the handler (anything but
// 401 or 403: these ids name no instance, so the handler answers 400 or 404),
// and an app-scope-only principal is refused for the core scope. The read
// routes are not named and keep their own requirement.
func TestRegister_ManageRoutesTakeTheAppCoRequirement(t *testing.T) {
	co := map[string]rbac.Requirement{}
	for _, n := range manageRouteNames() {
		co[n] = rbac.Need(appManage)
	}
	h, err := coRequiredRouter(t, co)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	do := func(method, path, as string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/core/v1"+path, nil)
		req.RemoteAddr = "192.0.2.10:4000"
		req.Header.Set("X-Test-As", as)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	wantRefused := func(t *testing.T, rec *httptest.ResponseRecorder, missing rbac.Permission) {
		t.Helper()
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status %d, want 403 (body %s)", rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if want := `"required_permission":"` + string(missing) + `"`; !strings.Contains(body, want) {
			t.Fatalf("body %s does not name %s", body, missing)
		}
	}
	for _, c := range []struct{ name, method, path string }{
		{"create", http.MethodPost, "/integrations/notify/instances"},
		{"update", http.MethodPut, "/integrations/notify/instances/gcpd-roof"},
		{"delete", http.MethodDelete, "/integrations/notify/instances/gcpd-roof"},
		{"test", http.MethodPost, "/integrations/notify/test"},
		{"instanceTest", http.MethodPost, "/integrations/notify/instances/gcpd-roof/test"},
		{"hold", http.MethodDelete, "/integrations/notify/instances/gcpd-roof/hold"},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantRefused(t, do(c.method, c.path, "core-only"), appManage)
			wantRefused(t, do(c.method, c.path, "app-only"), integrations.ManagePerm)
			if rec := do(c.method, c.path, "both"); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Fatalf("both scopes: status %d, want the handler's answer (body %s)", rec.Code, rec.Body)
			}
			if rec := do(c.method, c.path, ""); rec.Code != http.StatusUnauthorized {
				t.Fatalf("no principal: status %d, want 401", rec.Code)
			}
		})
	}
	t.Run("read routes are untouched", func(t *testing.T) {
		for _, as := range []string{"core-only", "both", "app-only"} {
			if rec := do(http.MethodGet, "/integrations/adapters", as); rec.Code != http.StatusOK {
				t.Fatalf("GET adapters as %s: status %d, want 200 (body %s)", as, rec.Code, rec.Body)
			}
		}
	})
}

// TestRegister_NoCoRequirementIsToday: with no co-requirement the same
// principals are admitted to the manage routes by core:integrations.manage
// alone, exactly as before.
func TestRegister_NoCoRequirementIsToday(t *testing.T) {
	h, err := coRequiredRouter(t, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/core/v1/integrations/notify/instances/gcpd-roof", nil)
	req.RemoteAddr = "192.0.2.10:4000"
	req.Header.Set("X-Test-As", "core-only")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("core-only with no co-requirement: status %d, want the handler's answer (body %s)", rec.Code, rec.Body)
	}
}

// TestRegister_ManageRouteNamesMatchTheTable: the six names a co-requirement
// map names are exactly the routes that declare the manage permission, so a
// renamed route or a new manage route fails here and not by silently dropping
// out of the app's map (an unknown name refuses boot; a missing one would not).
func TestRegister_ManageRouteNamesMatchTheTable(t *testing.T) {
	var fromTable []string
	for _, w := range wantRoutes {
		if w.perm == string(integrations.ManagePerm) {
			fromTable = append(fromTable, w.name)
		}
	}
	got := manageRouteNames()
	slices.Sort(fromTable)
	slices.Sort(got)
	if !slices.Equal(fromTable, got) {
		t.Fatalf("the route table's manage routes are %v, this test names %v", fromTable, got)
	}
}
