// file: core/internal/libraryhttp/reach_f709_test.go
// version: 1.0.0
// guid: 4c7f510e-8a74-46aa-b2c5-cb848c37045b
// last-edited: 2026-11-28

package libraryhttp

import (
	"net/http"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/rbac"
)

// F7-09 W5 and round 2: browseLibrary asks rbac.OperatorRequirement(), the seven
// core scopes of HEAD's settings.manage atom. HEAD's picker (GET
// /api/v1/filesystem/browse, internal/server/legacy_manifest.go:681) is behind
// that atom and has the same verdict on every principal of the reach oracle as
// GET /config (internal/books/rbacreach/verdicts.json), so the exact HEAD reach
// is the atom's: core:library.browse alone, the round-1 pair (browse plus
// settings) and a native role of any other subset are wider than HEAD. The
// cells run through the real compiled checker, as TestBrowse_RealCheckerDecides
// does; the registered-roots confinement is browse.go's and is unchanged.
//
// Three 403 shapes come out of core's integration and library routes: the
// router's (required_permission), this package's (the body's "required" holds
// the requirement string, asserted below) and integrationhttp's backstop (Detail
// "this needs ..."); web/src/services/coreHttp/transport.ts:301 reads
// required_permission.

func TestBrowse_ReachIsHEADsSettingsManageAtom(t *testing.T) {
	cat, err := rbac.NewCatalog(append(rbac.CoreScopes(),
		rbac.PermissionSpec{Name: "books:integrations.manage", Title: "i", Description: "books integrations.manage"},
		rbac.PermissionSpec{Name: "books:settings.manage", Title: "s", Description: "books settings.manage"})...)
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	settingsAtom := []rbac.Permission{"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
		"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"}
	sixOfSeven := slices.DeleteFunc(operatorScopes(), func(s rbac.Permission) bool { return s == "core:tasks.manage" })
	for _, c := range []struct {
		name   string
		scopes []rbac.Permission
		code   int
	}{
		{"library.browse alone", []rbac.Permission{scopeBrowse}, http.StatusForbidden},
		{"core:settings.manage alone", []rbac.Permission{scopeSettings}, http.StatusForbidden},
		{"integrations.manage atom only", []rbac.Permission{"core:integrations.manage", "books:integrations.manage"}, http.StatusForbidden},
		{"browse plus settings (the round-1 pair)", []rbac.Permission{scopeBrowse, scopeSettings}, http.StatusForbidden},
		{"six of the seven scopes", sixOfSeven, http.StatusForbidden},
		{"the seven core scopes", operatorScopes(), http.StatusOK},
		{"settings.manage atom (all eight scopes)", settingsAtom, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &auth.Principal{User: &auth.User{ID: "lucius", Username: "lucius"}, Method: auth.MethodSession, CredentialID: "cred-lucius",
				Perms: authz.Compile(rbac.CompileInput{Interactive: true, RolePatterns: slices.Clone(c.scopes)}, cat)}
			for _, s := range c.scopes {
				if !p.Perms.Has(s) {
					t.Fatalf("the fixture does not hold %s: the catalog lacks it", s)
				}
			}
			res := r.get(p, nil)
			if res.code != c.code {
				t.Fatalf("= %d %s, want %d", res.code, res.body, c.code)
			}
			if c.code != http.StatusForbidden {
				return
			}
			pr := res.problem(t)
			if pr.Code != "permission_denied" || pr.Required != "all(core:settings.manage,core:tasks.manage,core:system.view,core:system.manage,core:library.manage,core:library.browse,core:integrations.manage)" {
				t.Errorf("problem = %+v, want permission_denied naming the operator requirement", pr)
			}
		})
	}
}

// TestBrowse_RouteRequirementIsTheSettingsAtom pins the declared requirement
// itself, so the cells above cannot pass against a route that asks something else.
func TestBrowse_RouteRequirementIsTheSettingsAtom(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	routes := r.srv.Routes()
	if len(routes) != 1 || routes[0].Name != "browseLibrary" {
		t.Fatalf("routes = %+v, want browseLibrary alone", routes)
	}
	got := routes[0].Perm
	if got.Kind() != rbac.KindAllOf || got.String() != rbac.OperatorRequirement().String() {
		t.Errorf("browseLibrary requires %s (kind %d), want rbac.OperatorRequirement %s", got, got.Kind(), rbac.OperatorRequirement())
	}
	if want := "all(core:settings.manage,core:tasks.manage,core:system.view,core:system.manage,core:library.manage,core:library.browse,core:integrations.manage)"; got.String() != want {
		t.Errorf("browseLibrary requires %s, want %s", got, want)
	}
}
