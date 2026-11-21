// file: core/kernel/identity_refresh_test.go
// version: 0.1.0
// guid: 5c0d7a39-e214-4f68-b3a1-9d86f2e50c47
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Batman names only.

// refreshHandle boots an identity-switch-on kernel and returns the AuthRefresh
// the probe module (which declares Manifest.AuthRefresh) was handed in
// Register, with the fixture that stores users.
func refreshHandle(t *testing.T) (module.RefreshFunc, *mountFx) {
	t.Helper()
	h, c, p := probeHostDeclaring(t, true, true)
	k := h.mustBoot(c)
	t.Cleanup(func() { h.close(k) })
	if p.deps.AuthRefresh == nil {
		t.Fatal("an identity boot handed a module no AuthRefresh")
	}
	return p.deps.AuthRefresh, &mountFx{k: k}
}

// boundPrincipal is the principal auth's own middleware (stage 12's session
// path) binds for cookie's session, so it carries a stored session as its
// credential: the kind of principal a request-bound module holds.
func boundPrincipal(t *testing.T, fx *mountFx, cookie *http.Cookie) *auth.Principal {
	t.Helper()
	var got *auth.Principal
	h := fx.k.ident.svc.Auth.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = fx.k.ident.svc.Auth.PrincipalFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:4000"
	req.AddCookie(cookie)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatal("the middleware bound no principal for the session cookie")
	}
	return got
}

// putUser stores u past auth.Service, with its version bumped, as an admin's
// edit would.
func putUser(t *testing.T, fx *mountFx, u *auth.User) {
	t.Helper()
	u.Version++
	b := fx.k.ident.users.NewBatch()
	defer func() { _ = b.Close() }()
	if err := fx.k.ident.users.PutUser(b, u); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatal(err)
	}
}

// TestIdentityP5_AuthRefreshHandle: Deps.AuthRefresh is auth's Refresh and
// keeps its refusals. A refreshed principal is the same user's, a demotion
// since the principal was built narrows the checker, a disabled or
// reset-required user and a principal auth never built are refused, and
// nothing a module passes widens it (the principal's own forgery matrix is
// auth's TestRefresh_RefusesAnEditedPrincipal).
func TestIdentityP5_AuthRefreshHandle(t *testing.T) {
	refresh, fx := refreshHandle(t)

	t.Run("same user, same method", func(t *testing.T) {
		u, cookie := fx.user(t, "u-lucius", "relay-worker")
		p := boundPrincipal(t, fx, cookie)
		np, err := refresh(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		if np.User.ID != u.ID || np.Method != p.Method {
			t.Errorf("refreshed %s via %s, want %s via %s", np.User.ID, np.Method, u.ID, p.Method)
		}
		if !np.Perms.Has("alfred:signal") {
			t.Error("the holder of relay-worker lost alfred:signal on refresh")
		}
	})
	t.Run("a demotion narrows", func(t *testing.T) {
		u, cookie := fx.user(t, "u-barbara", "relay-worker")
		p := boundPrincipal(t, fx, cookie)
		if !p.Perms.Has("alfred:signal") {
			t.Fatal("fixture: the principal does not hold alfred:signal to begin with")
		}
		u.Roles = []rbac.RoleName{}
		putUser(t, fx, u)
		np, err := refresh(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		if np.Perms.Has("alfred:signal") {
			t.Error("a refreshed principal kept a scope its user's role no longer grants")
		}
	})
	t.Run("a disabled user is refused", func(t *testing.T) {
		u, cookie := fx.user(t, "u-harvey", "relay-worker")
		p := boundPrincipal(t, fx, cookie)
		u.Status = auth.StatusDisabled
		putUser(t, fx, u)
		if _, err := refresh(t.Context(), p); !errors.Is(err, auth.ErrInactiveUser) {
			t.Errorf("refresh of a disabled user = %v, want ErrInactiveUser", err)
		}
	})
	t.Run("a reset-required user is refused", func(t *testing.T) {
		u, cookie := fx.user(t, "u-oswald", "relay-worker")
		p := boundPrincipal(t, fx, cookie)
		u.SignInRestricted = true
		putUser(t, fx, u)
		if _, err := refresh(t.Context(), p); !errors.Is(err, auth.ErrResetRequired) {
			t.Errorf("refresh of a sign-in-restricted user = %v, want ErrResetRequired", err)
		}
	})
	t.Run("a principal auth did not build is refused", func(t *testing.T) {
		u, _ := fx.user(t, "u-edward", "relay-worker")
		literal := &auth.Principal{User: u, Method: auth.MethodSession}
		if _, err := refresh(t.Context(), literal); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("refresh of a literal-built principal = %v, want ErrInvalidCredentials", err)
		}
	})
	t.Run("no principal is refused", func(t *testing.T) {
		if _, err := refresh(context.Background(), nil); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("refresh of nil = %v, want ErrInvalidCredentials", err)
		}
	})
}

// TestIdentityP5_AuthRefreshOnlyForADeclaringModule: Refresh re-resolves a
// principal into a fresh one, so it is a reviewed manifest line like AppConfig.
// In one boot the module that declares it holds the handle and a module that
// does not holds nil, and a declaring module in a switch-off boot holds nil.
func TestIdentityP5_AuthRefreshOnlyForADeclaringModule(t *testing.T) {
	h, c, p := probeHostDeclaring(t, true, true)
	k := h.mustBoot(c)
	defer h.close(k)
	if p.deps.AuthRefresh == nil {
		t.Error("a module that declares AuthRefresh was handed nil")
	}
	if p.sibling.AuthRefresh != nil {
		t.Error("a module that does not declare AuthRefresh was handed auth's Refresh")
	}
	if p.sibling.Auth == nil {
		t.Error("fixture: the sibling got no identity handles at all, so its nil AuthRefresh proves nothing")
	}

	// Declared, but the identity switch is off: there is no auth service.
	h2, c2, p2 := probeHostDeclaring(t, false, true)
	k2 := h2.mustBoot(c2)
	defer h2.close(k2)
	if p2.deps.AuthRefresh != nil {
		t.Error("a declaring module in a switch-off boot was handed an AuthRefresh")
	}
}

// TestIdentityP5_SwitchOffGivesNoAuthRefresh: a boot without IdentityBoot has no
// auth service, and the module's AuthRefresh stays nil like Auth and AuthAdmin.
func TestIdentityP5_SwitchOffGivesNoAuthRefresh(t *testing.T) {
	h, c, p := probeHost(t, false)
	k := h.mustBoot(c)
	defer h.close(k)
	if p.deps.AuthRefresh != nil {
		t.Error("a switch-off module was handed an AuthRefresh")
	}
}
