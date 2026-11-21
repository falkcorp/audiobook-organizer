// file: internal/server/legacyauth/adopt_test.go
// version: 0.1.0
// guid: 3d8f1b52-6a49-4e07-9c2d-1f5b7a0e4c86
// last-edited: 2026-11-21

package legacyauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The identity value binds into the request and IdentityFrom finds it there
// and nowhere else.
func TestIdentityBind(t *testing.T) {
	b := newBridge(t, nil)
	id := &Identity{Bridge: b}
	if _, ok := IdentityFrom(context.Background()); ok {
		t.Fatal("a bare context reports core identity")
	}
	if _, ok := BridgeFrom(as(coreauth.MethodSession, laxChecker{})); ok {
		t.Fatal("a core principal alone reports core identity: the mode must come from the wiring")
	}
	var got *Bridge
	r := gin.New()
	r.Use(id.Bind())
	r.GET("/gotham", func(c *gin.Context) {
		got, _ = BridgeFrom(c.Request.Context())
		c.Status(http.StatusNoContent)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/gotham", nil))
	if got != b {
		t.Fatal("Bind did not put the bridge on the request context")
	}
}

func TestAuthOff(t *testing.T) {
	b := newBridge(t, nil)
	for name, tc := range map[string]struct {
		ctx  context.Context
		want bool
	}{
		"no principal":                {context.Background(), false},
		"a session":                   {as(coreauth.MethodSession, laxChecker{}), false},
		"the auth-disabled principal": {as(coreauth.MethodAuthDisabled, laxChecker{}), true},
	} {
		if got := b.AuthOff(tc.ctx); got != tc.want {
			t.Errorf("%s: AuthOff = %v, want %v", name, got, tc.want)
		}
	}
}

// AdminHeld is core:admin through the bound checker: key scoping applies (a
// key whose scopes leave core:admin out is refused) and the auth-disabled
// principal never holds it.
func TestAdminHeld(t *testing.T) {
	b := newBridge(t, nil)
	for name, tc := range map[string]struct {
		ctx  context.Context
		want bool
	}{
		"no principal":                {bound(nil, laxChecker{}), false},
		"no checker":                  {bound(principal(coreauth.MethodSession, laxChecker{}), nil), false},
		"a role holding core:admin":   {as(coreauth.MethodSession, setChecker{rbac.CoreAdmin}), true},
		"every atom but core:admin":   {as(coreauth.MethodSession, setChecker{"books:view", "books:edit", "core:users.manage"}), false},
		"a key scoped without admin":  {as(coreauth.MethodAPIKey, setChecker{"core:users.manage"}), false},
		"a key scoped with admin":     {as(coreauth.MethodAPIKey, setChecker{rbac.CoreAdmin}), true},
		"the auth-disabled principal": {as(coreauth.MethodAuthDisabled, laxChecker{}), false},
	} {
		if got := b.AdminHeld(tc.ctx); got != tc.want {
			t.Errorf("%s: AdminHeld = %v, want %v", name, got, tc.want)
		}
	}
}

// TestAuthenticate (05-http-api §8.3 RequireAuth row): the principal's user
// and permissions become HEAD's context values and the gin user.
func TestAuthenticate(t *testing.T) {
	b := newBridge(t, nil)
	var gotUser *database.User
	set := func(_ *gin.Context, u *database.User) { gotUser = u }
	run := func(ctx context.Context) (*httptest.ResponseRecorder, context.Context) {
		var seen context.Context
		r := gin.New()
		r.GET("/gotham", b.Authenticate(set), func(c *gin.Context) {
			seen = c.Request.Context()
			c.String(http.StatusOK, "reached")
		})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/gotham", nil).WithContext(ctx))
		return rec, seen
	}

	gotUser = nil
	rec, seen := run(as(coreauth.MethodSession, setChecker{"books:view", "core:tasks.view"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("a session: status %d (%s)", rec.Code, rec.Body.String())
	}
	if gotUser == nil || gotUser.ID != bruceID {
		t.Fatalf("the gin user = %+v, want bruce", gotUser)
	}
	if !auth.Can(seen, "library.view") || auth.Can(seen, "library.edit_metadata") {
		t.Errorf("HEAD's permissions are not the principal's: library.view=%v library.edit_metadata=%v",
			auth.Can(seen, "library.view"), auth.Can(seen, "library.edit_metadata"))
	}
	if auth.MethodFromContext(seen) != auth.MethodSession {
		t.Errorf("method = %q, want session", auth.MethodFromContext(seen))
	}

	for name, ctx := range map[string]context.Context{
		"no principal":        bound(nil, laxChecker{}),
		"no checker":          bound(principal(coreauth.MethodSession, laxChecker{}), nil),
		"an unmapped method":  as("ext:cfaccess", laxChecker{}),
		"a user core lacks":   bound(&coreauth.Principal{Method: coreauth.MethodSession, Perms: laxChecker{}, User: &coreauth.User{ID: "u-nobody", Status: coreauth.StatusActive}}, laxChecker{}),
		"a principal no user": bound(&coreauth.Principal{Method: coreauth.MethodSession, Perms: laxChecker{}}, laxChecker{}),
	} {
		gotUser = nil
		rec, _ := run(ctx)
		if rec.Code != http.StatusUnauthorized || rec.Body.String() == "reached" || gotUser != nil {
			t.Errorf("%s: status %d body %q user %v, want 401 and no user", name, rec.Code, rec.Body.String(), gotUser)
		}
	}

	gotUser = nil
	rec, _ = run(as(coreauth.MethodAuthDisabled, laxChecker{}))
	if rec.Code != http.StatusOK || gotUser != nil {
		t.Errorf("auth-disabled: status %d user %v, want a pass with no user bound", rec.Code, gotUser)
	}
}

// Held, HeldOrAuthOff and HeldLive answer only on a request that runs under
// core identity; elsewhere they report bridged=false and the caller's HEAD
// code decides.
func TestHeld(t *testing.T) {
	b := newBridge(t, nil)
	refresh := func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil }
	id := &Identity{Bridge: b, Refresh: refresh}
	lib := setChecker{"books:view", "core:tasks.view"}
	for name, tc := range map[string]struct {
		ctx             context.Context
		held, authOffOK bool
		bridged         bool
		atom            string
	}{
		"not under core identity":         {ctx: as(coreauth.MethodSession, lib), bridged: false, atom: "library.view"},
		"a principal holding the atom":    {ctx: WithIdentity(as(coreauth.MethodSession, lib), id), held: true, authOffOK: true, bridged: true, atom: "library.view"},
		"a principal lacking the atom":    {ctx: WithIdentity(as(coreauth.MethodSession, setChecker{"books:view"}), id), bridged: true, atom: "library.view"},
		"an atom the table does not know": {ctx: WithIdentity(as(coreauth.MethodSession, laxChecker{}), id), bridged: true, atom: "batcave.flood"},
		"no principal":                    {ctx: WithIdentity(context.Background(), id), bridged: true, atom: "library.view"},
		"the auth-disabled principal":     {ctx: WithIdentity(as(coreauth.MethodAuthDisabled, laxChecker{}), id), authOffOK: true, bridged: true, atom: "library.view"},
	} {
		held, bridged := Held(tc.ctx, tc.atom)
		if bridged != tc.bridged || (bridged && held != tc.held) {
			t.Errorf("%s: Held = (%v, %v), want (%v, %v)", name, held, bridged, tc.held, tc.bridged)
		}
		held, bridged = HeldOrAuthOff(tc.ctx, tc.atom)
		if bridged != tc.bridged || (bridged && held != (tc.held || tc.authOffOK)) {
			t.Errorf("%s: HeldOrAuthOff = (%v, %v), want (%v, %v)", name, held, bridged, tc.held || tc.authOffOK, tc.bridged)
		}
	}
}

func TestHeldLive(t *testing.T) {
	b := newBridge(t, nil)
	lib := setChecker{"books:view", "core:tasks.view"}
	same := func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil }
	run := func(refresh func(context.Context, *coreauth.Principal) (*coreauth.Principal, error), ctx context.Context) (bool, bool) {
		return HeldLive(WithIdentity(ctx, &Identity{Bridge: b, Refresh: refresh}), "library.view")
	}
	if _, bridged := HeldLive(as(coreauth.MethodSession, lib), "library.view"); bridged {
		t.Error("HeldLive reports core identity on a request without one")
	}
	if held, bridged := run(same, as(coreauth.MethodSession, lib)); !held || !bridged {
		t.Errorf("a standing principal: (%v, %v), want (true, true)", held, bridged)
	}
	narrowed := func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) {
		np := *p
		np.Perms = setChecker{"books:view"}
		return &np, nil
	}
	if held, _ := run(narrowed, as(coreauth.MethodSession, lib)); held {
		t.Error("a refreshed principal that lost a scope still holds the atom")
	}
	// The refresh decides, not the checker the request opened with: a fresh
	// checker that holds the atom is held even when the bound one no longer does.
	widened := func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) {
		np := *p
		np.Perms = lib
		return &np, nil
	}
	if held, _ := run(widened, as(coreauth.MethodSession, setChecker{})); !held {
		t.Error("the refreshed principal's checker is what is asked")
	}
	for name, refresh := range map[string]func(context.Context, *coreauth.Principal) (*coreauth.Principal, error){
		"a refusal": func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) {
			return nil, coreauth.ErrInactiveUser
		},
		"a nil principal": func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) { return nil, nil },
		"no checker": func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) {
			return &coreauth.Principal{Method: coreauth.MethodSession}, nil
		},
		"no refresh handle": nil,
	} {
		if held, bridged := run(refresh, as(coreauth.MethodSession, lib)); held || !bridged {
			t.Errorf("%s: (%v, %v), want (false, true)", name, held, bridged)
		}
	}
	if held, bridged := run(same, bound(nil, lib)); held || !bridged {
		t.Errorf("no principal: (%v, %v), want (false, true)", held, bridged)
	}
	off := as(coreauth.MethodAuthDisabled, laxChecker{})
	if held, _ := run(same, off); !held {
		t.Error("auth off passes while core still answers it")
	}
	if held, _ := run(func(context.Context, *coreauth.Principal) (*coreauth.Principal, error) {
		return nil, coreauth.ErrInactiveUser
	}, off); held {
		t.Error("auth off stops once core refuses the principal")
	}
}
