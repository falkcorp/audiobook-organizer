// file: core/coretest/identity_test.go
// version: 0.1.0
// guid: 0b7e4a92-6d15-4f38-8c21-e4a95b3d7f60
// last-edited: 2026-11-21

package coretest

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Batman names only. The fixture below has hello's shape (06 §6.12): the
// module ID "hello", a greeting route that needs hello:read, the permission
// declared with the viewer's default grant. It is a fixture, not the example
// module: the example's greeting route is rbac.Public until hello declares the
// identity boot itself, which needs its own OpenAPI document regenerated.

// greeter is the hello-shaped fixture module.
type greeter struct{}

const (
	permRead  rbac.Permission = "hello:read"
	permAdmin rbac.Permission = "hello:admin"
)

type greeterOut struct {
	Message string `json:"message"`
}

func (greeter) Manifest() module.Manifest {
	return module.Manifest{
		ID: "hello", Version: "1.0.0", DisplayName: "Hello", MinCoreVersion: "0.1.0", DefaultEnabled: true,
		Permissions: []rbac.PermissionSpec{
			{Name: permRead, Title: "Read greetings", Class: rbac.ClassView, DefaultGrants: []rbac.RoleName{rbac.RoleViewer}},
			{Name: permAdmin, Title: "Run hello maintenance"},
		},
		PublicRoutes: []string{"getOpen", "getBroken"},
		// A keyspace, so the App has the store the identity boot needs.
		Storage: storage.Spec{Keyspace: true},
	}
}

func (greeter) Register(r *module.Registrar) error {
	ok := func(msg string) func(context.Context, *httpapi.In[struct{}]) (greeterOut, error) {
		return func(context.Context, *httpapi.In[struct{}]) (greeterOut, error) {
			return greeterOut{Message: msg}, nil
		}
	}
	g := r.HTTP()
	httpapi.Handle(g, httpapi.Route{Method: http.MethodGet, Path: "/greeting", Name: "getGreeting",
		Perm: rbac.Need(permRead), Summary: "Greet Bruce"}, ok("Hello, Bruce"))
	httpapi.Handle(g, httpapi.Route{Method: http.MethodGet, Path: "/maintenance", Name: "getMaintenance",
		Perm: rbac.Need(permAdmin), Summary: "Hello maintenance"}, ok("tidy"))
	// Session refuses every credential but a person's own interactive sign-in:
	// a delegated session (temp login, invite) is refused as an API key is.
	httpapi.Handle(g, httpapi.Route{Method: http.MethodPost, Path: "/lock", Name: "lockGreeting",
		Perm: rbac.Need(permRead), Guards: httpapi.Guards{Session: true}, Summary: "Lock the greeting"}, ok("locked"))
	httpapi.Handle(g, httpapi.Route{Method: http.MethodGet, Path: "/open", Name: "getOpen",
		Perm: rbac.Public, Summary: "Open to everyone"}, ok("open"))
	// Public, and answers 401 to everyone: what a route answers when no
	// credential authenticates, whatever the caller presents.
	httpapi.Handle(g, httpapi.Route{Method: http.MethodGet, Path: "/broken", Name: "getBroken",
		Perm: rbac.Public, Summary: "Always unauthenticated"},
		func(context.Context, *httpapi.In[struct{}]) (greeterOut, error) {
			return greeterOut{}, &httpapi.Error{Status: http.StatusUnauthorized, Code: "unauthenticated"}
		})
	return r.Err()
}

func get(t testing.TB, c *Client, path string) int {
	t.Helper()
	return c.GetJSON(t, path, nil)
}

func post(t testing.TB, c *Client, path string) int {
	t.Helper()
	req := mustRequest(t, http.MethodPost, path)
	resp := c.Do(req)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestAssertPermission_Hello is 06 §6.4.8's R6 per-route check over a
// hello-shaped module, with a real stage 12 and stage 13: a route with a real
// permission answers 403 to a signed-in user lacking it, and neither 401 nor
// 403 to a user holding exactly it. Called on its own, with no WithUser, since
// AssertPermission seeds its users itself.
func TestAssertPermission_Hello(t *testing.T) {
	h := New(t, WithModules(greeter{}))
	AssertPermission(t, h, http.MethodGet, "/api/hello/v1/greeting", permRead)
	AssertPermission(t, h, http.MethodGet, "/api/hello/v1/maintenance", permAdmin)
	AssertPermission(t, h, http.MethodPost, "/api/hello/v1/lock", permRead)
}

// TestAssertPermission_RefusesTheWrongAnswers: AssertPermission fails, with a
// message naming what it saw, when the route is open to a user lacking the
// permission, when it needs a different permission than the one named (the
// holding user is refused), and when the lacking user is answered 401, which
// is the credential not authenticating and not the permission refusal the
// check exists to see.
func TestAssertPermission_RefusesTheWrongAnswers(t *testing.T) {
	h := New(t, WithModules(greeter{}))
	cases := []struct {
		name, path string
		perm       rbac.Permission
		want       string
	}{
		{"open to a user lacking it", "/api/hello/v1/open", permRead, "answered 200 to a user lacking hello:read, want 403"},
		{"needs another permission", "/api/hello/v1/maintenance", permRead, "answered 403 to a user holding exactly hello:read, want neither 401 nor 403"},
		{"401 for a signed-in user", "/api/hello/v1/broken", permRead, "answered 401 to a signed-in user lacking hello:read, want 403"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := runRecording(t, func(tb testing.TB) { AssertPermission(tb, h, http.MethodGet, c.path, c.perm) })
			if !rec.failed || !rec.contains(c.want) {
				t.Fatalf("AssertPermission(%s) did not fail with %q:\n%s", c.path, c.want, rec.text())
			}
		})
	}
}

// TestAs_ReachesStage13AsTheSeededPrincipal: As returns a Client that stage 13
// judges as the user WithUser seeded, Anonymous carries nothing, and a name no
// WithUser seeded fails the test instead of falling back to either.
func TestAs_ReachesStage13AsTheSeededPrincipal(t *testing.T) {
	h := New(t, WithModules(greeter{}),
		WithUser("alfred", permRead),
		WithUser("selina"))
	if st := get(t, h.As("alfred"), "/api/hello/v1/greeting"); st != http.StatusOK {
		t.Errorf("alfred (hello:read) GET greeting = %d, want 200", st)
	}
	if st := get(t, h.As("selina"), "/api/hello/v1/greeting"); st != http.StatusForbidden {
		t.Errorf("selina (no permission) GET greeting = %d, want 403: signed in, and refused", st)
	}
	if st := get(t, h.Anonymous(), "/api/hello/v1/greeting"); st != http.StatusUnauthorized {
		t.Errorf("Anonymous GET greeting = %d, want 401", st)
	}
	if st := get(t, h.Anonymous(), "/api/hello/v1/open"); st != http.StatusOK {
		t.Errorf("Anonymous GET open = %d, want 200", st)
	}
	// As reports through the TB the harness booted with, so the unseeded name
	// is asked of a harness booted on the recording one.
	rec := runRecording(t, func(tb testing.TB) {
		New(tb, WithModules(greeter{}), WithUser("alfred", permRead)).As("harvey")
		tb.Error("As of an unseeded user returned")
	})
	if !rec.failed || !rec.contains(`As("harvey"): no WithUser seeded that user`) {
		t.Errorf("As of an unseeded user did not fail the test:\n%s", rec.text())
	}
}

// TestWithUser_HoldsExactlyItsPermissions: the stored user's one role holds the
// seeded set and nothing more, and a route for a permission outside it is
// refused: no extra permission, no core:admin, nothing implied.
func TestWithUser_HoldsExactlyItsPermissions(t *testing.T) {
	h := New(t, WithModules(greeter{}), WithUser("alfred", permRead, permRead))
	u, err := h.id.auth.UserByUsername(t.Context(), "alfred")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Roles) != 1 {
		t.Fatalf("alfred holds roles %v, want exactly one", u.Roles)
	}
	role, err := h.id.rbac.Role(t.Context(), u.Roles[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := role.Permissions(); !slices.Equal(got, []rbac.Permission{permRead}) {
		t.Fatalf("alfred's role holds %v, want exactly [hello:read]", got)
	}
	if st := get(t, h.As("alfred"), "/api/hello/v1/maintenance"); st != http.StatusForbidden {
		t.Errorf("alfred GET maintenance (hello:admin) = %d, want 403", st)
	}
}

// TestAs_IsAnInteractiveSession: the credential As carries is the user's own
// interactive sign-in, so a route whose guard admits only that
// (Guards.Session) answers the seeded user on its permission. A delegated
// session, which a temp login or an invite issues, would be refused by the
// guard whatever the user holds.
func TestAs_IsAnInteractiveSession(t *testing.T) {
	h := New(t, WithModules(greeter{}), WithUser("alfred", permRead), WithUser("selina"))
	if st := post(t, h.As("alfred"), "/api/hello/v1/lock"); st != http.StatusOK {
		t.Errorf("alfred POST lock = %d, want 200", st)
	}
	if st := post(t, h.As("selina"), "/api/hello/v1/lock"); st != http.StatusForbidden {
		t.Errorf("selina POST lock = %d, want 403 on the permission", st)
	}
}

// TestBoot_DeclaresTheIdentityBoot: a module's route that needs a permission
// boots, which only an App with stages 12 and 13 allows, and the harness's
// own actor is a real administrator no test client reaches.
func TestBoot_DeclaresTheIdentityBoot(t *testing.T) {
	h := New(t, WithModules(greeter{}))
	if _, _, ok := h.k.IdentityServices(); !ok {
		t.Fatal("the harness booted without identity services")
	}
	for ri := range h.App().Routes() {
		if ri.Name == "getGreeting" && ri.Perm.Kind() == rbac.KindPublic {
			t.Fatalf("route %s is public", ri.Name)
		}
	}
}

// TestHarness_RebootKeepsItsUsers: a Reboot over the same store re-seeds the
// same WithUser principal without a name or role conflict, and the new
// harness's As works.
func TestHarness_RebootKeepsItsUsers(t *testing.T) {
	h := New(t, WithModules(greeter{}), WithUser("alfred", permRead))
	AssertPermission(t, h, http.MethodGet, "/api/hello/v1/greeting", permRead)
	h2 := h.Reboot(t, WithModules(greeter{}), WithUser("alfred", permRead))
	if st := get(t, h2.As("alfred"), "/api/hello/v1/greeting"); st != http.StatusOK {
		t.Errorf("after a reboot alfred GET greeting = %d, want 200", st)
	}
	AssertPermission(t, h2, http.MethodGet, "/api/hello/v1/greeting", permRead)
}

// TestIdentity_ZeroModuleHarnessHasIdentity: the identity boot is always
// declared (F4-08 B5b), so an App of no module at all has the store it needs and
// working WithUser, As and AssertPermission over a core route: core's user list
// needs core:users.view.
func TestIdentity_ZeroModuleHarnessHasIdentity(t *testing.T) {
	const usersPath = "/api/core/v1/users"
	const usersView rbac.Permission = "core:users.view"
	h := New(t, WithUser("alfred", usersView), WithUser("lucius"))
	if h.id == nil {
		t.Fatal("a zero-module harness declared no identity boot")
	}
	if _, _, ok := h.k.IdentityServices(); !ok {
		t.Fatal("a zero-module harness built no identity services")
	}
	if st := get(t, h.As("alfred"), usersPath); st != http.StatusOK {
		t.Errorf("alfred (core:users.view) GET %s = %d, want 200", usersPath, st)
	}
	if st := get(t, h.As("lucius"), usersPath); st != http.StatusForbidden {
		t.Errorf("lucius (no permission) GET %s = %d, want 403", usersPath, st)
	}
	if st := get(t, h.Anonymous(), usersPath); st != http.StatusUnauthorized {
		t.Errorf("anonymous GET %s = %d, want 401", usersPath, st)
	}
	AssertPermission(t, h, http.MethodGet, usersPath, usersView)
	// A storage-less module is no different: the store is the app's, not the module's.
	h2 := New(t, WithModules(plainMod{}), WithUser("alfred", usersView))
	if st := get(t, h2.As("alfred"), usersPath); st != http.StatusOK {
		t.Errorf("storage-less app: alfred GET %s = %d, want 200", usersPath, st)
	}
}

// plainMod is a module that declares no storage and no route.
type plainMod struct{}

func (plainMod) Manifest() module.Manifest {
	return module.Manifest{ID: "lucius", Version: "1.0.0", DisplayName: "Lucius", MinCoreVersion: "0.1.0", DefaultEnabled: true}
}
func (plainMod) Register(*module.Registrar) error { return nil }
