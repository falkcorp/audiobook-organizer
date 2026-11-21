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
