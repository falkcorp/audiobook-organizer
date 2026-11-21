// file: core/auth/refresh_test.go
// version: 0.1.0
// guid: 2a6f0c84-d1e3-4b97-8c5a-e94b17f3d260
// last-edited: 2026-11-20

package auth_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// refreshed calls Refresh on svc and checks the answer is for p's credential:
// the same Method, CredentialID, AuthTime, Assurance and ClientAddr, over the
// user as stored now.
func refreshed(t *testing.T, svc *auth.Service, p *auth.Principal) *auth.Principal {
	t.Helper()
	np, err := svc.Refresh(context.Background(), p)
	if err != nil {
		t.Fatalf("Refresh %s %q: %v", p.Method, p.CredentialID, err)
	}
	if np == p {
		t.Fatal("Refresh answered the request principal itself, not a rebuild")
	}
	if np.Method != p.Method || np.CredentialID != p.CredentialID || !np.AuthTime.Equal(p.AuthTime) ||
		np.Assurance != p.Assurance || np.ClientAddr != p.ClientAddr {
		t.Fatalf("Refresh answered for another credential: %+v, want %s %q", np, p.Method, p.CredentialID)
	}
	return np
}

// TestRefresh_SessionFollowsTheStoredRoles: a session principal is rebuilt
// over the user's roles as stored now, so a role change (which moves neither
// the epoch nor the generation) reaches it; a self-service password change
// keeps it, as it keeps the session; a deactivated user or a revoked session
// is a definite refusal.
func TestRefresh_SessionFollowsTheStoredRoles(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	barbara := f.withPassword(f.user("barbara-gordon", rbac.RoleViewer), "batgirl-1967")
	p := f.signedIn(barbara)
	if np := refreshed(t, f.svc, p); !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:signal") {
		t.Fatalf("a viewer's session rebuilt as %v", np.Perms.Concrete())
	}
	f.setRoles(barbara.ID, rbac.RoleEditor)
	if np := refreshed(t, f.svc, p); !np.Perms.Has("gotham:signal") || len(np.User.Roles) != 1 || np.User.Roles[0] != rbac.RoleEditor {
		t.Fatalf("a promotion did not reach the rebuilt session: %v", np.Perms.Concrete())
	}
	if err := f.svc.ChangeOwnPassword(ctx, p, "batgirl-1967", "oracle-1988"); err != nil {
		t.Fatal(err)
	}
	refreshed(t, f.svc, p) // the session was re-stamped at the new epoch

	f.edit(barbara, func(u *auth.User) { u.Status = auth.StatusDisabled })
	_, err := f.svc.Refresh(ctx, p)
	wantErr(t, "a deactivated user's session", err, auth.ErrInactiveUser)

	dick := f.user("dick-grayson", rbac.RoleViewer)
	gone := f.signedIn(dick)
	if err := f.svc.RevokeSession(ctx, gone, gone.CredentialID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Refresh(ctx, gone)
	wantErr(t, "a revoked session", err, auth.ErrRevoked)
}

// TestRefresh_CoreKeyFollowsTheKeysScopes: a core key principal is rebuilt
// over the stored key's scopes, never its owner's roles nor the scopes the
// principal carries; a revoked key, an expired key, another user's key and a
// deactivated owner are definite refusals.
func TestRefresh_CoreKeyFollowsTheKeysScopes(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	gordon := f.user("jim-gordon", roleCommissioner)
	barbara := f.user("barbara-gordon", "gotham.detective") // patrol and cases.read
	_, k := f.mint(f.session(barbara), barbara.ID, "gotham:patrol")
	p := f.keyPrincipal(barbara, k)
	np := refreshed(t, f.svc, p)
	if !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a patrol key rebuilt as %v: it widened to the owner's roles", np.Perms.Concrete())
	}
	// The answer is the scopes the principal was built with that the stored
	// key still holds: never wider than the key (a principal carrying others
	// is one Refresh must not trust) ...
	wide, err := f.svc.PrincipalFor(ctx, barbara, auth.MethodAPIKey, []rbac.Permission{"gotham:patrol", "gotham:cases.read"}, barbara.CredentialEpoch)
	if err != nil {
		t.Fatal(err)
	}
	wide.CredentialID = k.ID
	if np := refreshed(t, f.svc, wide); !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:cases.read") {
		t.Fatalf("rebuilt over %v, want the stored key's scope (patrol) only", np.Perms.Concrete())
	}
	// ... and never wider than the principal: one built narrower than its key
	// stays that narrow, and one sharing no scope with the key is refused.
	_, bk := f.mint(f.session(barbara), barbara.ID, "gotham:patrol", "gotham:cases.read")
	narrow, err := f.svc.PrincipalFor(ctx, barbara, auth.MethodAPIKey, []rbac.Permission{"gotham:cases.read"}, barbara.CredentialEpoch)
	if err != nil {
		t.Fatal(err)
	}
	narrow.CredentialID = bk.ID
	if np := refreshed(t, f.svc, narrow); !np.Perms.Has("gotham:cases.read") || np.Perms.Has("gotham:patrol") {
		t.Fatalf("a principal narrower than its key rebuilt as %v: it widened to the key", np.Perms.Concrete())
	}
	disjoint, err := f.svc.PrincipalFor(ctx, barbara, auth.MethodAPIKey, []rbac.Permission{"gotham:cases.read"}, barbara.CredentialEpoch)
	if err != nil {
		t.Fatal(err)
	}
	disjoint.CredentialID = k.ID // patrol only
	_, err = f.svc.Refresh(ctx, disjoint)
	wantErr(t, "a key principal sharing no scope with its key", err, auth.ErrRevoked)

	_, gk := f.mint(f.session(gordon), gordon.ID, "gotham:patrol")
	borrowed := f.keyPrincipal(barbara, gk)
	_, err = f.svc.Refresh(ctx, borrowed)
	wantErr(t, "another user's key", err, auth.ErrRevoked)

	_, rk := f.mint(f.session(barbara), barbara.ID, "gotham:patrol")
	revoked := f.keyPrincipal(barbara, rk)
	if err := f.svc.RevokeAPIKey(ctx, f.session(barbara), rk.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Refresh(ctx, revoked)
	wantErr(t, "a revoked key", err, auth.ErrRevoked)

	_, ek := f.mint(f.session(barbara), barbara.ID, "gotham:patrol")
	expiring := f.keyPrincipal(barbara, ek)
	refreshed(t, f.svc, expiring)
	f.advance(auth.DefaultKeyTTL + time.Minute)
	_, err = f.svc.Refresh(ctx, expiring)
	wantErr(t, "an expired key", err, auth.ErrExpired)

	_, dk := f.mint(f.session(gordon), gordon.ID, "gotham:patrol")
	disabledOwner := f.keyPrincipal(gordon, dk)
	f.edit(gordon, func(u *auth.User) { u.Status = auth.StatusDisabled })
	_, err = f.svc.Refresh(ctx, disabledOwner)
	wantErr(t, "a deactivated owner's key", err, auth.ErrInactiveUser)
}

// TestRefresh_CompatPrincipals: a compat key principal is rebuilt over its
// key's scopes and keeps its key reference (so Revalidate of the answer still
// follows the key); a compat surface's own principal keeps the scopes it was
// built with: nil stays the role permissions, a narrowing stays narrowed.
func TestRefresh_CompatPrincipals(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	cl := from("192.0.2.81")
	dick := f.user("dick-grayson", "gotham.detective") // patrol and cases.read
	sess := f.session(dick)

	raw, k := f.mint(sess, dick.ID, "gotham:patrol")
	kp, err := c.AuthenticateKey(ctx, raw, cl)
	if err != nil {
		t.Fatal(err)
	}
	np := refreshed(t, f.svc, kp)
	if np.Method != absMethod || !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a compat patrol key rebuilt as %s %v", np.Method, np.Perms.Concrete())
	}
	if err := f.svc.RevokeAPIKey(ctx, sess, k.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "Revalidate of the rebuilt compat key principal after a revoke", f.svc.Revalidate(ctx, np), auth.ErrRevoked)
	_, err = f.svc.Refresh(ctx, kp)
	wantErr(t, "a revoked compat key", err, auth.ErrRevoked)

	whole, err := c.PrincipalFor(ctx, dick, nil, dick.CredentialEpoch, cl)
	if err != nil {
		t.Fatal(err)
	}
	if np := refreshed(t, f.svc, whole); !np.Perms.Has("gotham:patrol") || !np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a compat session rebuilt as %v, want the role permissions", np.Perms.Concrete())
	}
	narrow, err := c.PrincipalFor(ctx, dick, []rbac.Permission{"gotham:patrol"}, dick.CredentialEpoch, cl)
	if err != nil {
		t.Fatal(err)
	}
	if np := refreshed(t, f.svc, narrow); !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a narrowed compat principal rebuilt as %v: it widened", np.Perms.Concrete())
	}

	// A required password reset ends the surface's own principal, as
	// Compat.PrincipalFor refuses to build one; the key principal follows the
	// key rules (no reset check), as Compat.AuthenticateKey does.
	raw2, _ := f.mint(sess, dick.ID, "gotham:patrol")
	kp2, err := c.AuthenticateKey(ctx, raw2, cl)
	if err != nil {
		t.Fatal(err)
	}
	f.edit(dick, func(u *auth.User) { u.PasswordResetRequired = true })
	_, err = f.svc.Refresh(ctx, whole)
	wantErr(t, "a compat surface principal whose user must reset", err, auth.ErrResetRequired)
	_, err = c.PrincipalFor(ctx, dick, nil, dick.CredentialEpoch, cl)
	wantErr(t, "Compat.PrincipalFor for a user who must reset", err, auth.ErrResetRequired)
	refreshed(t, f.svc, kp2)
}

// oracleExt is an identity extension asserting a fixed result.
type oracleExt struct{ a *auth.Assertion }

func (oracleExt) Name() string                                          { return "oracle" }
func (e oracleExt) Authenticate(*http.Request) (*auth.Assertion, error) { return e.a, nil }

// extPrincipal is the principal stage 12 binds for an ext:oracle extension
// asserting a, over a service that registers it.
func extPrincipal(t *testing.T, f *fx, a *auth.Assertion, owner bool) (*auth.Service, *auth.Principal) {
	t.Helper()
	e := auth.Extension{Authenticator: oracleExt{a: a}, Order: 150,
		Spec: auth.MethodSpec{Method: "ext:oracle", Interactive: true, OwnerVerifier: owner}}
	if owner {
		e.OwnerSet = func() bool { return true }
	}
	svc, err := f.withExtensions(t, e)
	if err != nil {
		t.Fatal(err)
	}
	_, got := serve(t, svc, request("", ""))
	if got.p == nil || got.p.Method != "ext:oracle" {
		t.Fatalf("stage 12 bound %+v, want an ext:oracle principal", got.p)
	}
	return svc, got.p
}

// TestRefresh_ExtensionPrincipals: an identity extension's principal (the
// production shape of ext:cfaccess, which stage 12 runs before the session)
// is refreshed, not refused: with nil scopes over the roles as stored now,
// with asserted scopes still narrowed to them; its owner assurance, credential
// and sign-in time are kept; and it ends when the user's epoch moves, as
// Revalidate decides for a method with no credential record of its own.
func TestRefresh_ExtensionPrincipals(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	signIn := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

	barbara := f.user("barbara-gordon", rbac.RoleViewer)
	svc, p := extPrincipal(t, f, &auth.Assertion{User: barbara.ID, CredentialID: "cf-belfry", AuthTime: signIn, Owner: true}, true)
	if p.Assurance&auth.AssuranceOwner == 0 {
		t.Fatal("the fixture's owner assertion was dropped")
	}
	if np := refreshed(t, svc, p); !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:signal") {
		t.Fatalf("a viewer's extension principal rebuilt as %v", np.Perms.Concrete())
	}
	f.setRoles(barbara.ID, rbac.RoleEditor)
	if np := refreshed(t, svc, p); !np.Perms.Has("gotham:signal") {
		t.Fatalf("a promotion did not reach the rebuilt extension principal: %v", np.Perms.Concrete())
	}

	dick := f.user("dick-grayson", "gotham.detective") // patrol and cases.read
	nsvc, narrow := extPrincipal(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-perch", Scopes: []rbac.Permission{"gotham:patrol"}}, false)
	if np := refreshed(t, nsvc, narrow); !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a narrowed extension principal rebuilt as %v: it widened to the roles", np.Perms.Concrete())
	}
	f.setRoles(dick.ID, rbac.RoleEditor, "gotham.detective")
	if np := refreshed(t, nsvc, narrow); np.Perms.Has("gotham:signal") || np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a role grant widened a narrowed extension principal: %v", np.Perms.Concrete())
	}
	f.setRoles(dick.ID, "gotham.detective")
	f.setRoles(dick.ID, rbac.RoleViewer) // patrol only: the narrowing still holds
	if np := refreshed(t, nsvc, narrow); !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a narrowed principal over a viewer rebuilt as %v", np.Perms.Concrete())
	}

	f.edit(barbara, func(u *auth.User) { u.CredentialEpoch++ })
	_, err := svc.Refresh(ctx, p)
	wantErr(t, "an extension principal after its user's epoch moved", err, auth.ErrStaleEpoch)
}

// TestRefresh_AuthOffAndRefusals: the auth-off binding is answered afresh
// while the mode is on and refused by a required-mode service; no principal,
// no user, and a principal auth did not build (which carries no record of its
// narrowing) are refused rather than rebuilt over the user's roles.
func TestRefresh_AuthOffAndRefusals(t *testing.T) {
	f, off := offFx(t)
	ctx := context.Background()
	_, got := serve(t, off, request("", ""))
	if got.p == nil || got.p.Method != auth.MethodAuthDisabled {
		t.Fatalf("auth-off bound %+v", got.p)
	}
	np, err := off.Refresh(ctx, got.p)
	if err != nil || np.Method != auth.MethodAuthDisabled || np.User != nil || !np.Perms.Has("gotham:patrol") || np.Perms.Has(rbac.CoreAdmin) {
		t.Fatalf("auth-off Refresh = %+v, %v", np, err)
	}
	if np.ClientAddr != got.p.ClientAddr {
		t.Errorf("auth-off Refresh dropped the client address %v", got.p.ClientAddr)
	}
	_, err = f.svc.Refresh(ctx, got.p)
	wantErr(t, "a required-mode service given the auth-off binding", err, auth.ErrInvalidCredentials)

	_, err = f.svc.Refresh(ctx, nil)
	wantErr(t, "no principal", err, auth.ErrInvalidCredentials)
	_, err = f.svc.Refresh(ctx, &auth.Principal{Method: auth.MethodSession})
	wantErr(t, "no user", err, auth.ErrInvalidCredentials)
	selina := f.user("selina-kyle", rbac.RoleEditor)
	handmade := &auth.Principal{User: selina, Method: auth.MethodSession, CredentialID: f.signedIn(selina).CredentialID}
	_, err = f.svc.Refresh(ctx, handmade)
	wantErr(t, "a principal auth did not build", err, auth.ErrInvalidCredentials)
}

// TestRefresh_Concurrent: Refresh holds no state, so a hub may call it from
// every stream's goroutine at once (run with -race).
func TestRefresh_Concurrent(t *testing.T) {
	f := newFx(t)
	alfred := f.user("alfred-pennyworth", "gotham.detective")
	sess := f.signedIn(alfred)
	_, k := f.mint(f.session(alfred), alfred.ID, "gotham:patrol")
	key := f.keyPrincipal(alfred, k)
	var wg sync.WaitGroup
	for i := range 64 {
		p := sess
		if i%2 == 1 {
			p = key
		}
		wg.Go(func() {
			if np, err := f.svc.Refresh(context.Background(), p); err != nil || np.CredentialID != p.CredentialID {
				t.Errorf("Refresh %q: %v, %v", p.CredentialID, np, err)
			}
		})
	}
	wg.Wait()
}

// TestRefresh_MiddlewareBoundPrincipals: the session and core key principals
// stage 12 binds (not ones a test builds through PrincipalFor) are refreshed,
// so no production authenticator assembles a principal Refresh refuses.
func TestRefresh_MiddlewareBoundPrincipals(t *testing.T) {
	f := newFx(t)
	tim := f.user("tim-drake", "gotham.detective") // patrol and cases.read
	is := f.issue(t, tim)
	_, got := serve(t, f.svc, request("", is.Token))
	if got.p == nil || got.p.Method != auth.MethodSession {
		t.Fatalf("the session cookie bound %+v", got.p)
	}
	if np := refreshed(t, f.svc, got.p); !np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a bound session rebuilt as %v", np.Perms.Concrete())
	}
	raw, _ := f.mint(f.session(tim), tim.ID, "gotham:patrol")
	_, got = serve(t, f.svc, request(raw, ""))
	if got.p == nil || got.p.Method != auth.MethodAPIKey {
		t.Fatalf("the key bound %+v", got.p)
	}
	if np := refreshed(t, f.svc, got.p); !np.Perms.Has("gotham:patrol") || np.Perms.Has("gotham:cases.read") {
		t.Fatalf("a bound patrol key rebuilt as %v", np.Perms.Concrete())
	}
}

// failingReads fails the user or key read Refresh makes.
type failingReads struct {
	auth.Store
	user, key error
}

func (s failingReads) GetUser(ctx context.Context, id auth.UserID) (*auth.User, error) {
	if s.user != nil {
		return nil, s.user
	}
	return s.Store.GetUser(ctx, id)
}

func (s failingReads) APIKeyByID(ctx context.Context, id string) (*auth.APIKey, error) {
	if s.key != nil {
		return nil, s.key
	}
	return s.Store.APIKeyByID(ctx, id)
}

// TestRefresh_StoreFailureIsTransient: a failed user or key read is
// ErrStoreUnavailable (the hub retries it), never a definite refusal; a
// record that is gone is ErrRevoked.
func TestRefresh_StoreFailureIsTransient(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	alfred := f.user("alfred-pennyworth", "gotham.detective")
	sess := f.signedIn(alfred)
	_, k := f.mint(f.session(alfred), alfred.ID, "gotham:patrol")
	key := f.keyPrincipal(alfred, k)
	disk := errors.New("the Batcomputer is down")
	over := func(st auth.Store) *auth.Service {
		t.Helper()
		svc, err := auth.New(ctx, auth.Options{}, auth.Deps{
			Store: st, NewBatch: f.st.NewBatch, RBAC: f.rb, Audit: f.audit, Revocations: f.jrnl, Clock: f.clock,
		})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	for _, tc := range []struct {
		name string
		st   failingReads
		p    *auth.Principal
		want error
	}{
		{"user read fails", failingReads{Store: f.st, user: disk}, sess, auth.ErrStoreUnavailable},
		{"key read fails", failingReads{Store: f.st, key: disk}, key, auth.ErrStoreUnavailable},
		{"user gone", failingReads{Store: f.st, user: auth.ErrNotFound}, sess, auth.ErrRevoked},
		{"key gone", failingReads{Store: f.st, key: auth.ErrNotFound}, key, auth.ErrRevoked},
	} {
		_, err := over(tc.st).Refresh(ctx, tc.p)
		wantErr(t, tc.name, err, tc.want)
		if tc.want == auth.ErrRevoked && errors.Is(err, auth.ErrStoreUnavailable) {
			t.Errorf("%s: a definite refusal reads as transient: %v", tc.name, err)
		}
	}
}

// TestRefresh_ExtensionPrincipalIsNotACompatSurface: the password-reset rule
// is a compat surface's own (compatSurfaceStanding). An identity extension's
// principal is not one, and stage 12 binds it for a user who must reset (only
// SignInRestricted refuses), so Refresh follows stage 12 and answers it; a
// ext: method that read as a compat one would end its stream at the next beat.
func TestRefresh_ExtensionPrincipalIsNotACompatSurface(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	dick := f.user("dick-grayson", rbac.RoleViewer)
	svc, p := extPrincipal(t, f, &auth.Assertion{User: dick.ID, CredentialID: "cf-perch"}, false)

	f.edit(dick, func(u *auth.User) { u.PasswordResetRequired = true })
	_, got := serve(t, svc, request("", ""))
	if got.p == nil || got.p.Method != "ext:oracle" {
		t.Fatalf("stage 12 for a user who must reset bound %+v (refusal %+v), want an ext:oracle principal", got.p, got.fail)
	}
	refreshed(t, svc, p)
	if err := svc.Revalidate(ctx, p); err != nil {
		t.Fatalf("Revalidate of an extension principal whose user must reset: %v", err)
	}
}
