// file: core/httpapi/ownerwhenset_test.go
// version: 0.1.0
// guid: a648b186-96b5-427b-8666-affcb3189c8f
// last-edited: 2026-11-22

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// LegacyRoute.OwnerWhenSet (F4-08 B10y): HEAD's ownerGateWhenOwnerSet on a
// legacy Guards.Owner row. While core.auth.owner.required holds, stage 13
// demands the owner as on every owner route; while it does not, the row has no
// owner leg and its Guards.Interactive and Perm decide (HEAD's gate passed
// through with owner_email unset). Core's own owner routes keep 05-authn
// §4.8's not-required branch, MayChangeCredentials and core:admin, and cannot
// opt out: the knob exists only on LegacyRoute, and a native route's authWrap
// passes false.

// owsAuthn is stage 12 for these tests: it binds the principal X-Test-As names.
type owsAuthn struct{ cat *rbac.Catalog }

func (f *owsAuthn) principal(name string) *auth.Principal {
	switch name {
	case "viewer": // an interactive session holding the route's perm, not core:admin
		return s13Session(f.cat, "core:users.view")
	case "delegated": // a temp-login or invite session holding the perm
		return s13Delegated(f.cat, "core:users.view")
	case "key": // a key holding the perm
		return s13Key(f.cat, []rbac.Permission{"core:users.view"}, "core:users.view")
	case "nobody":
		return s13Session(f.cat, "core:self")
	case "admin":
		return s13Session(f.cat, rbac.CoreAdmin)
	case "auth-off":
		return s13AuthOff(f.cat)
	case "owner": // an owner-assured session holding the perm
		p := s13Session(f.cat, "core:users.view")
		p.Assurance = auth.AssuranceOwner
		return p
	}
	return nil
}

func (f *owsAuthn) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p := f.principal(r.Header.Get("X-Test-As")); p != nil {
				r = r.WithContext(auth.WithPrincipal(r.Context(), p))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (f *owsAuthn) SessionCookie(auth.ClientInfo, string, time.Time) *http.Cookie {
	return &http.Cookie{Name: "hello_session", Path: "/", MaxAge: -1}
}

// owsRig is a router with a legacy mount holding two owner rows, one with the
// knob and one without, and a native owner route, all answered by owner.
type owsRig struct {
	h       http.Handler
	reached atomic.Int64
}

func owsBuild(t *testing.T, owner func() bool) *owsRig {
	t.Helper()
	cat := s13Catalog(t)
	rg := &owsRig{}
	r := NewRouter(Options{OpenAPI: OpenAPIOptions{Version: "1.0.0", CookieName: "hello_session"}}, Deps{})
	if err := r.SetAuth(AuthWiring{Service: &owsAuthn{cat: cat}, OwnerRequired: owner, ResolveAtom: s13Resolver, Catalog: cat}); err != nil {
		t.Fatal(err)
	}
	if err := r.Claim("gcpd", "/api/v1/"); err != nil {
		t.Fatal(err)
	}
	reach := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { rg.reached.Add(1); w.WriteHeader(http.StatusOK) })
	g := Guards{Interactive: true, Owner: true}
	if err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: reach, Auth: LegacyAuthCore, Routes: manifestOf(
		LegacyRoute{Method: http.MethodPost, GinPath: "/api/v1/evidence/restore", Perm: rbac.Need("core:users.view"), Guards: g, OwnerWhenSet: true},
		LegacyRoute{Method: http.MethodPost, GinPath: "/api/v1/evidence/seal", Perm: rbac.Need("core:users.view"), Guards: g},
	)}); err != nil {
		t.Fatal(err)
	}
	// The native owner route asks the same perm as the legacy rows, not
	// core:admin, so only §4.8's owner branch can demand core:admin of it: a
	// knob that leaked onto it would admit the viewer.
	Handle(r.CoreGroup(), Route{Method: http.MethodPost, Path: "/vault", Name: "openVault", Perm: rbac.Need("core:users.view"), Guards: g},
		func(context.Context, *In[struct{}]) (raOut, error) { rg.reached.Add(1); return raOut{OK: true}, nil })
	h, err := r.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rg.h = h
	return rg
}

type owsCell struct {
	name, path, as string
	status         int
	code           string
}

func (rg *owsRig) run(t *testing.T, cells []owsCell) {
	t.Helper()
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			before := rg.reached.Load()
			req := httptest.NewRequest(http.MethodPost, c.path, nil)
			req.RemoteAddr = "192.0.2.10:4000"
			req.Header.Set("X-Test-As", c.as)
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, req)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, c.status, rec.Body)
			}
			if c.code != "" {
				if got := s13Problem(t, rec).Code; got != c.code {
					t.Fatalf("code %q, want %q", got, c.code)
				}
			}
			if moved := rg.reached.Load() != before; moved != (c.status == http.StatusOK) {
				t.Fatalf("handler reached = %v with status %d", moved, c.status)
			}
		})
	}
}

const (
	owsKnob   = "/api/v1/evidence/restore"
	owsNoKnob = "/api/v1/evidence/seal"
	owsCore   = "/api/core/v1/vault"
)

// TestOwnerWhenSet_NotRequiredLeavesTheRowsOwnGuards: with no owner required,
// the knob row admits an interactive or delegated session holding its perm
// (HEAD's ordinary guards: credGuard = MayChangeCredentials, and the perm) and
// still refuses a key and the auth-disabled principal at the interactive
// guard, and a session without the perm at the perm.
func TestOwnerWhenSet_NotRequiredLeavesTheRowsOwnGuards(t *testing.T) {
	rg := owsBuild(t, func() bool { return false })
	rg.run(t, []owsCell{
		{"session holding the perm", owsKnob, "viewer", 200, ""},
		{"delegated session holding the perm", owsKnob, "delegated", 200, ""},
		{"key holding the perm", owsKnob, "key", 403, "interactive_required"},
		{"auth-disabled principal", owsKnob, "auth-off", 403, "interactive_required"},
		{"session without the perm", owsKnob, "nobody", 403, "permission_denied"},
		{"no principal", owsKnob, "", 401, "authentication_required"},
	})
}

// TestOwnerWhenSet_RequiredDemandsTheOwner: with the owner required the knob
// row is an owner route as before: everyone but the owner-assured principal is
// refused owner_required, core:admin included.
func TestOwnerWhenSet_RequiredDemandsTheOwner(t *testing.T) {
	rg := owsBuild(t, func() bool { return true })
	rg.run(t, []owsCell{
		{"session holding the perm", owsKnob, "viewer", 403, "owner_required"},
		{"delegated session holding the perm", owsKnob, "delegated", 403, "owner_required"},
		{"admin session", owsKnob, "admin", 403, "owner_required"},
		{"key holding the perm", owsKnob, "key", 403, "interactive_required"},
		{"owner", owsKnob, "owner", 200, ""},
	})
}

// TestOwnerWhenSet_OnlyTheRowThatSetsIt: a legacy owner row without the knob,
// and core's native owner route, keep 05-authn §4.8's not-required branch,
// MayChangeCredentials and core:admin: a session or a delegated session
// holding the route's perm but not core:admin is refused permission_denied,
// a key interactive_required, and only the admin passes. This is the pin that
// the knob reaches no core route.
func TestOwnerWhenSet_OnlyTheRowThatSetsIt(t *testing.T) {
	rg := owsBuild(t, func() bool { return false })
	for _, path := range []string{owsNoKnob, owsCore} {
		rg.run(t, []owsCell{
			{path + " session without core:admin", path, "viewer", 403, "permission_denied"},
			{path + " delegated session without core:admin", path, "delegated", 403, "permission_denied"},
			{path + " key", path, "key", 403, "interactive_required"},
			{path + " admin session", path, "admin", 200, ""},
		})
	}
}

// TestOwnerWhenSet_NeedsGuardsOwner: the knob on a row that is no owner route
// is a manifest error, so it can only ever narrow an owner row back to HEAD's
// gate, never mean anything on its own.
func TestOwnerWhenSet_NeedsGuardsOwner(t *testing.T) {
	r := NewRouter(Options{}, Deps{})
	if err := r.Claim("gcpd", "/api/v1/"); err != nil {
		t.Fatal(err)
	}
	err := r.MountLegacy(LegacyMount{Owner: "gcpd", Handler: http.NotFoundHandler(), Auth: LegacyAuthGin, Routes: manifestOf(
		LegacyRoute{Method: http.MethodPost, GinPath: "/api/v1/evidence/restore", Perm: rbac.Need("core:users.view"),
			Guards: Guards{Interactive: true}, OwnerWhenSet: true},
	)})
	if err == nil {
		_, err = r.Build()
	}
	if !errors.Is(err, ErrLegacyManifest) {
		t.Fatalf("OwnerWhenSet without Guards.Owner = %v, want ErrLegacyManifest", err)
	}
}
