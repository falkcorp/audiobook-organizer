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

// F7-09 W5: browseLibrary asks AllOf(core:library.browse, core:settings.manage).
// HEAD's picker was behind the settings.manage atom, which maps to
// core:library.browse and core:settings.manage among eight scopes
// (internal/books/atoms.go), so core:library.browse alone was wider than HEAD.
// The cells run through the real compiled checker, as TestBrowse_RealCheckerDecides
// does; the registered-roots confinement is browse.go's and is unchanged.

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
	for _, c := range []struct {
		name   string
		scopes []rbac.Permission
		code   int
	}{
		{"library.browse alone", []rbac.Permission{scopeBrowse}, http.StatusForbidden},
		{"core:settings.manage alone", []rbac.Permission{scopeSettings}, http.StatusForbidden},
		{"integrations.manage atom only", []rbac.Permission{"core:integrations.manage", "books:integrations.manage"}, http.StatusForbidden},
		{"browse plus settings", []rbac.Permission{scopeBrowse, scopeSettings}, http.StatusOK},
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
			if pr.Code != "permission_denied" || pr.Required != "all(core:library.browse,core:settings.manage)" {
				t.Errorf("problem = %+v, want permission_denied naming all(core:library.browse,core:settings.manage)", pr)
			}
		})
	}
}

// TestBrowse_RouteRequirementIsBothScopes pins the declared requirement itself,
// so the cells above cannot pass against a route that asks something else.
func TestBrowse_RouteRequirementIsBothScopes(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	routes := r.srv.Routes()
	if len(routes) != 1 || routes[0].Name != "browseLibrary" {
		t.Fatalf("routes = %+v, want browseLibrary alone", routes)
	}
	got := routes[0].Perm
	if got.Kind() != rbac.KindAllOf || !slices.Equal(got.Permissions(), []rbac.Permission{"core:library.browse", "core:settings.manage"}) {
		t.Errorf("browseLibrary requires %s (kind %d), want all(core:library.browse,core:settings.manage)", got, got.Kind())
	}
}
