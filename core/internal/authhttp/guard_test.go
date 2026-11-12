// file: core/internal/authhttp/guard_test.go
// version: 1.0.1
// guid: 0d6a4b92-8e15-4c37-9fa2-c1b7e30d5a68
// last-edited: 2026-11-11

package authhttp

import (
	"context"
	"iter"
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// call runs fn behind s's guard as stage 13 will, for p.
func call[Req, Resp any](s spec, fn func(context.Context, *auth.Principal, *httpapi.In[Req]) (Resp, error), p *auth.Principal, req Req) (Resp, error) {
	return guard(s, fn)(auth.WithPrincipal(context.Background(), p), in(p, req))
}

// tripwire is a Self, Accounts and Invites that fail the test when called: a
// route that refuses a caller must refuse it before any Service method runs.
type tripwire struct{ t testing.TB }

func (w tripwire) hit(name string) {
	w.t.Helper()
	w.t.Fatalf("Service method %s ran for a refused caller", name)
}

func (w tripwire) ChangeOwnPassword(context.Context, *auth.Principal, string, string) error {
	w.hit("ChangeOwnPassword")
	return nil
}
func (w tripwire) Sessions(context.Context, auth.UserID) iter.Seq2[auth.SessionView, error] {
	w.hit("Sessions")
	return nil
}
func (w tripwire) RevokeSession(context.Context, *auth.Principal, string) error {
	w.hit("RevokeSession")
	return nil
}
func (w tripwire) APIKeys(context.Context, auth.UserID) iter.Seq2[*auth.APIKey, error] {
	w.hit("APIKeys")
	return nil
}
func (w tripwire) CreateAPIKey(context.Context, *auth.Principal, auth.KeySpec) (string, *auth.APIKey, error) {
	w.hit("CreateAPIKey")
	return "", nil, nil
}
func (w tripwire) SetAPIKeyStatus(context.Context, *auth.Principal, string, auth.KeyStatus) error {
	w.hit("SetAPIKeyStatus")
	return nil
}
func (w tripwire) RevokeAPIKey(context.Context, *auth.Principal, string) error {
	w.hit("RevokeAPIKey")
	return nil
}
func (w tripwire) Users(context.Context, auth.UserFilter) iter.Seq2[*auth.User, error] {
	w.hit("Users")
	return nil
}
func (w tripwire) SetStatus(context.Context, *auth.Principal, auth.UserID, auth.UserStatus) error {
	w.hit("SetStatus")
	return nil
}
func (w tripwire) DeleteUser(context.Context, *auth.Principal, auth.UserID) error {
	w.hit("DeleteUser")
	return nil
}
func (w tripwire) SetRoles(context.Context, *auth.Principal, auth.UserID, []rbac.RoleName) error {
	w.hit("SetRoles")
	return nil
}
func (w tripwire) MintResetLink(context.Context, *auth.Principal, auth.UserID) (string, error) {
	w.hit("MintResetLink")
	return "", nil
}
func (w tripwire) MintTempLogin(context.Context, *auth.Principal, auth.UserID) (string, error) {
	w.hit("MintTempLogin")
	return "", nil
}
func (w tripwire) CreateInvite(context.Context, *auth.Principal, auth.InviteSpec) (string, auth.InviteView, error) {
	w.hit("CreateInvite")
	return "", auth.InviteView{}, nil
}
func (w tripwire) DeleteInvite(context.Context, *auth.Principal, string) error {
	w.hit("DeleteInvite")
	return nil
}
func (w tripwire) Invites(context.Context) iter.Seq2[auth.InviteView, error] {
	w.hit("Invites")
	return nil
}
func (w tripwire) User(context.Context, auth.UserID) (*auth.User, error) {
	w.hit("User")
	return nil, nil
}

// authOff is the principal of auth-off mode: no user, an admin's reach.
func (e *env) authOff() *auth.Principal {
	return &auth.Principal{Method: auth.MethodAuthDisabled, Perms: e.rb.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}})}
}

// guarded is every Interactive or user-required route's handler behind its
// guard, as a call with a zero request, for the guard tests.
func (a *api) guarded() map[string]func(p *auth.Principal) error {
	run := func(s spec, f func(*auth.Principal) error) func(*auth.Principal) error {
		return func(p *auth.Principal) error { return f(p) }
	}
	m := map[string]func(*auth.Principal) error{}
	add := func(s spec, f func(*auth.Principal) error) { m[s.method+" "+s.path] = run(s, f) }
	add(specMe(), func(p *auth.Principal) error { _, err := call(specMe(), a.me, p, noBody{}); return err })
	add(specMeEd(), func(p *auth.Principal) error {
		x := "Robin"
		_, err := call(specMeEd(), a.updateMe, p, updateMeReq{Body: ProfilePatch{DisplayName: &x}})
		return err
	})
	add(specMePassword(), func(p *auth.Principal) error {
		_, err := call(specMePassword(), a.changePassword, p, passwordReq{Body: PasswordChange{Current: "a", New: "b"}})
		return err
	})
	add(specMeSessions(), func(p *auth.Principal) error {
		_, err := call(specMeSessions(), a.mySessions, p, listReq{})
		return err
	})
	add(specMeSessionRevoke(), func(p *auth.Principal) error {
		_, err := call(specMeSessionRevoke(), a.revokeMySession, p, sessionReq{PublicID: "x"})
		return err
	})
	add(specMeKeys(), func(p *auth.Principal) error { _, err := call(specMeKeys(), a.myKeys, p, listReq{}); return err })
	add(specKeysAll(), func(p *auth.Principal) error { _, err := call(specKeysAll(), a.allKeys, p, listReq{}); return err })
	add(specKeyGet(), func(p *auth.Principal) error { _, err := call(specKeyGet(), a.getKey, p, keyReq{ID: "k"}); return err })
	add(specUserKeys(), func(p *auth.Principal) error {
		_, err := call(specUserKeys(), a.userKeys, p, userKeysReq{ID: "u-dick"})
		return err
	})
	add(specUserKeyNew(), func(p *auth.Principal) error {
		_, err := call(specUserKeyNew(), a.createUserKey, p, userKeyCreateReq{ID: "u-dick", Body: KeyCreate{Name: "n", Scopes: []string{"core:self"}}})
		return err
	})
	add(specInvites(), func(p *auth.Principal) error { _, err := call(specInvites(), a.invites, p, listReq{}); return err })
	add(specInviteNew(), func(p *auth.Principal) error {
		_, err := call(specInviteNew(), a.createInvite, p, inviteCreateReq{Body: InviteCreate{Username: "x", Role: "viewer"}})
		return err
	})
	add(specInviteDelete(), func(p *auth.Principal) error {
		_, err := call(specInviteDelete(), a.deleteInvite, p, userReq{ID: "i"})
		return err
	})
	add(specDisable(), func(p *auth.Principal) error {
		_, err := call(specDisable(), a.setStatus(specDisable(), auth.StatusDisabled), p, userReq{ID: "u-dick"})
		return err
	})
	add(specEnable(), func(p *auth.Principal) error {
		_, err := call(specEnable(), a.setStatus(specEnable(), auth.StatusActive), p, userReq{ID: "u-dick"})
		return err
	})
	add(specUserDelete(), func(p *auth.Principal) error {
		_, err := call(specUserDelete(), a.deleteUser, p, userReq{ID: "u-dick"})
		return err
	})
	add(specReset(), func(p *auth.Principal) error {
		_, err := call(specReset(), a.link(specReset(), a.Accounts.MintResetLink), p, userReq{ID: "u-dick"})
		return err
	})
	add(specRoles(), func(p *auth.Principal) error {
		_, err := call(specRoles(), a.setRoles, p, rolesReq{ID: "u-dick", Body: RolesReplace{Roles: []string{"viewer"}}})
		return err
	})
	add(specTemp(), func(p *auth.Principal) error {
		_, err := call(specTemp(), a.link(specTemp(), a.Accounts.MintTempLogin), p, userReq{ID: "u-dick"})
		return err
	})
	add(specUserSessions(), func(p *auth.Principal) error {
		_, err := call(specUserSessions(), a.userSessions, p, userSessionsReq{ID: "u-dick"})
		return err
	})
	add(specUserSessionDelete(), func(p *auth.Principal) error {
		_, err := call(specUserSessionDelete(), a.revokeUserSession, p, userSessionReq{ID: "u-dick", PublicID: "s"})
		return err
	})
	return m
}

// tripped is an api whose Service-side dependencies fail the test when
// called.
func (e *env) tripped() *api {
	return e.api(func(d *Deps) {
		w := tripwire{e.t}
		d.Self, d.Accounts, d.Invites, d.Users = w, w, w, w
	})
}

// Every Interactive route refuses an API key (403 interactive_required) and
// the auth-disabled principal (401 authentication_required) before any Service
// method runs: the guard is the route's own, ahead of the call, not a
// property of what the Service later decides.
func TestInteractiveRoutes_RefuseKeysAndAuthOffBeforeTheService(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	a := e.tripped()
	key := e.principal(bruce, auth.MethodAPIKey, "core:self", "core:users.manage", "core:users.view")
	for route, run := range a.guarded() {
		info := a.specOf(t, route)
		if !info.guards.Interactive {
			continue
		}
		wantErr(t, run(key), http.StatusForbidden, "interactive_required")
		wantErr(t, run(e.authOff()), http.StatusUnauthorized, "authentication_required")
		wantErr(t, run(nil), http.StatusUnauthorized, "authentication_required")
	}
}

func (a *api) specOf(t testing.TB, route string) spec {
	t.Helper()
	for _, rt := range a.routes() {
		if rt.s.method+" "+rt.s.path == route {
			return rt.s
		}
	}
	t.Fatalf("no route %s", route)
	return spec{}
}

// The auth-disabled principal has no user: every route that lists or reads
// credential metadata answers 401 authentication_required, before any Service
// call, though its checker holds every scope (05-authn §4.11, SEC-3).
func TestAuthOff_CredentialMetadataNeedsAUser(t *testing.T) {
	e := newEnv(t)
	e.user("bruce", "brucebruce", rbac.RoleAdmin)
	a := e.tripped()
	off := e.authOff()
	var seen int
	for route, run := range a.guarded() {
		s := a.specOf(t, route)
		if !s.needUser {
			continue
		}
		seen++
		wantErr(t, run(off), http.StatusUnauthorized, "authentication_required")
	}
	if seen < 19 {
		t.Fatalf("only %d user-required routes exercised", seen)
	}
	// The user list is not credential metadata: auth-off may read it.
	for _, s := range []spec{specUsers(), specUser()} {
		if s.needUser {
			t.Errorf("%s %s requires a user; the table lists no such rule for it", s.method, s.path)
		}
	}
}

// A caller without the permission is refused 403 on every route, with the
// permission named, before the Service runs.
func TestRoutes_RefuseACallerWithoutThePermission(t *testing.T) {
	e := newEnv(t)
	selina := e.user("selina", "selinaselina", rbac.RoleViewer)
	a := e.tripped()
	viewer := e.session(selina)
	for route, run := range a.guarded() {
		s := a.specOf(t, route)
		if s.perm.String() == needSelf().String() {
			continue // a viewer holds core:self
		}
		err := run(viewer)
		wantErr(t, err, http.StatusForbidden, "permission_denied")
	}
}

func TestAdmit_OwnerGuard(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	s := spec{perm: needSelf(), guards: httpapi.Guards{Owner: true}, needUser: true}
	p := e.session(bruce)
	wantErr(t, s.admit(p), http.StatusForbidden, "owner_required")
	p.Assurance = auth.AssuranceOwner
	if err := s.admit(p); err != nil {
		t.Fatalf("an owner-verified principal: %v", err)
	}
}

// Delegated sessions (an ABS-style session token) may change credentials, as
// MayChangeCredentials defines; API keys and auth-off may not.
func TestAdmit_InteractiveFollowsMayChangeCredentials(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "brucebruce", rbac.RoleAdmin)
	s := spec{perm: needSelf(), guards: httpapi.Guards{Interactive: true}, needUser: true}
	for _, m := range []auth.Method{auth.MethodSession, auth.MethodSessionDelegated} {
		if err := s.admit(e.principal(bruce, m)); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
	wantErr(t, s.admit(e.principal(bruce, auth.MethodAPIKey, "core:self")), http.StatusForbidden, "interactive_required")
}

// A principal with no checker holds nothing, AnyAuthenticated included: it is
// "core:self or a non-Confining scope" (05-rbac 4.10), so a nil principal, a
// principal with no checker and a Confined key (a fingerprint-worker or
// arr-facade key) all fail it, and a signed-in user and an ordinary key pass.
func TestHolds_NoCheckerHoldsNothing(t *testing.T) {
	if holds(&auth.Principal{}, needSelf()) {
		t.Fatal("a principal with no checker satisfied core:self")
	}
	e := newEnv(t)
	bruce := e.user("bruce", "", rbac.RoleViewer, roleWorker)
	cases := map[string]struct {
		p    *auth.Principal
		want bool
	}{
		"nil principal":        {nil, false},
		"no checker":           {&auth.Principal{}, false},
		"session":              {e.session(bruce), true},
		"key naming core:self": {e.principal(bruce, auth.MethodAPIKey, "core:self"), true},
		"confined worker key":  {e.principal(bruce, auth.MethodAPIKey, "gotham:arr.read"), false},
	}
	for name, c := range cases {
		if got := holds(c.p, rbac.AnyAuthenticated); got != c.want {
			t.Errorf("%s: holds(AnyAuthenticated) = %v, want %v", name, got, c.want)
		}
	}
	if !holds(nil, rbac.Public) {
		t.Error("Public is satisfied by anyone")
	}
}

// TestAdmit_AnyAuthenticatedRefusesAConfinedKey: the same rule through admit,
// the guard every route runs: a confined key on the logout route is 403, not
// served.
func TestAdmit_AnyAuthenticatedRefusesAConfinedKey(t *testing.T) {
	e := newEnv(t)
	bruce := e.user("bruce", "", rbac.RoleViewer, roleWorker)
	s := specLogout()
	if err := s.admit(e.principal(bruce, auth.MethodSession)); err != nil {
		t.Fatalf("a session was refused: %v", err)
	}
	wantErr(t, s.admit(e.principal(bruce, auth.MethodAPIKey, "gotham:arr.read")), http.StatusForbidden, "permission_denied")
}
