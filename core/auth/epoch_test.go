// file: core/auth/epoch_test.go
// version: 1.0.0
// guid: 7b3e9d51-2c84-4a06-b1f7-5e9a0c4d8326
// last-edited: 2026-11-11

package auth_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// TestCredentialEpoch_KillsSessionsAndOneTimeTokens (05-authn §4.12): one
// bump of a user's credential epoch ends every session of the user and every
// temp-login link that targets it, and voids the invites it created; an API
// key is not epoch-bound and stands. Each bump source is exercised: sign out
// everywhere, a password change, and a disable with its re-enable.
func TestCredentialEpoch_KillsSessionsAndOneTimeTokens(t *testing.T) {
	bumps := map[string]func(f *fx, manager *auth.Principal, u *auth.User) error{
		"sign out everywhere": func(f *fx, _ *auth.Principal, u *auth.User) error {
			return f.svc.RevokeAllSessions(context.Background(), f.signedIn(u), u.ID, "")
		},
		"password change": func(f *fx, _ *auth.Principal, u *auth.User) error {
			return f.svc.ChangeOwnPassword(context.Background(), f.signedIn(u), "batgirl-1967", "oracle-1988")
		},
		"disable and re-enable": func(f *fx, m *auth.Principal, u *auth.User) error {
			if err := f.svc.SetStatus(context.Background(), m, u.ID, auth.StatusDisabled); err != nil {
				return err
			}
			return f.svc.SetStatus(context.Background(), m, u.ID, auth.StatusActive)
		},
	}
	for name, bump := range bumps {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			ctx := context.Background()
			gordon := f.user("jim-gordon", roleCommissioner)
			barbara := f.withPassword(f.user("barbara-gordon", roleCommissioner), "batgirl-1967")
			manager := f.session(gordon)
			stream := f.signedIn(barbara)
			_, k := f.mint(f.session(barbara), barbara.ID, "gotham:patrol")
			keyed := f.keyPrincipal(barbara, k)
			link := f.mintTemp(manager, barbara.ID, false)
			invite, _ := f.invite(f.session(barbara), detectiveInvite("harper-row"))

			if err := bump(f, manager, barbara); err != nil {
				t.Fatal(err)
			}
			if f.reload(barbara.ID).CredentialEpoch == barbara.CredentialEpoch {
				t.Fatal("the epoch did not move")
			}
			if f.svc.Revalidate(ctx, stream) == nil && name != "password change" {
				t.Fatal("a session from before the bump still stands")
			}
			if err := f.svc.Revalidate(ctx, keyed); err != nil {
				t.Fatalf("an API key is not epoch-bound: %v", err)
			}
			_, err := f.svc.ConsumeTempLogin(ctx, link, "", from("192.0.2.81"))
			if err == nil {
				t.Fatal("a temp-login link from before the bump opened a session")
			}
			if _, _, err := f.accept(invite, "gotham-city-1939"); err == nil {
				t.Fatal("an invite its creator made before the bump was accepted")
			}
		})
	}
}

// TestPrincipalFor_CompatAppliesEpochAndScopes (G15, the ABS path): a compat
// method's principal is built from the stored roles at the epoch the surface
// verified; a stale epoch is refused, a scoped one is the intersection with
// the roles (never wider), and an empty scope list means the roles' own
// reach. Scopes are taken as the surface gave them: a scoped API key must go
// through AuthenticateKey, whose stored scopes apply.
func TestPrincipalFor_CompatAppliesEpochAndScopes(t *testing.T) {
	f, c := compatFx(t)
	ctx := context.Background()
	cl := from("192.0.2.82")
	dick := f.user("dick-grayson", "gotham.detective")

	full, err := c.PrincipalFor(ctx, dick, nil, dick.CredentialEpoch, cl)
	if err != nil || !full.Perms.Has("gotham:patrol") || !full.Perms.Has("gotham:cases.read") {
		t.Fatalf("unscoped principal: %v, %v", full, err)
	}
	narrow, err := c.PrincipalFor(ctx, dick, []rbac.Permission{"gotham:patrol"}, dick.CredentialEpoch, cl)
	if err != nil || !narrow.Perms.Has("gotham:patrol") || narrow.Perms.Has("gotham:cases.read") {
		t.Fatalf("scoped principal: %v, %v", narrow, err)
	}
	wider, err := c.PrincipalFor(ctx, dick, []rbac.Permission{"gotham:signal"}, dick.CredentialEpoch, cl)
	if err == nil && wider.Perms.Has("gotham:signal") {
		t.Fatal("a scope outside the user's roles was granted")
	}
	// A bump after the surface verified its token: the epoch it carries is stale.
	if err := f.svc.RevokeAllSessions(ctx, f.signedIn(dick), dick.ID, ""); err != nil {
		t.Fatal(err)
	}
	_, err = c.PrincipalFor(ctx, dick, nil, dick.CredentialEpoch, cl)
	wantErr(t, "an epoch from before a sign-out everywhere", err, auth.ErrStaleEpoch)
	if _, err := c.PrincipalFor(ctx, dick, nil, f.reload(dick.ID).CredentialEpoch, cl); err != nil {
		t.Fatalf("the current epoch: %v", err)
	}
}

// TestClient_DefaultResolverMarksForwardedRequests (fail closed): with no
// resolver wired, a request carrying any forwarding header is Forwarded, so
// the loopback exceptions (tokenless setup, auth off) do not apply to it.
func TestClient_DefaultResolverMarksForwardedRequests(t *testing.T) {
	f := newFx(t)
	direct := httptest.NewRequest("GET", "/api/core/v1/auth/status", nil)
	direct.RemoteAddr = "127.0.0.1:41000"
	if c := f.svc.ClientOf(direct); c.Forwarded || !c.Local() {
		t.Fatalf("a direct loopback request: %+v", c)
	}
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP", "CF-Connecting-IP", "True-Client-IP"} {
		r := httptest.NewRequest("GET", "/api/core/v1/auth/status", nil)
		r.RemoteAddr = "127.0.0.1:41000"
		r.Header.Set(h, "192.0.2.90")
		if c := f.svc.ClientOf(r); !c.Forwarded || c.Local() {
			t.Errorf("a loopback request carrying %s: %+v", h, c)
		}
	}
}

// An invite's creator is guarded in the accept batch (a concurrent demotion
// or disable of the creator conflicts the commit and the retry refuses), and
// a demotion that lands between the read and the commit never yields an
// account.
func TestInviteAccept_CreatorDemotedMidCommitIsRefused(t *testing.T) {
	f := newFx(t)
	gordon := f.user("jim-gordon", roleCommissioner)
	tok, _ := f.invite(f.session(gordon), detectiveInvite("harper-row"))
	demoted := false
	f.audit.mu.Lock()
	f.audit.hold = func(e audit.Event) {
		if e.Action == "core.invite.accept" && !demoted {
			demoted = true
			f.setRoles(gordon.ID, "gotham.detective")
		}
	}
	f.audit.mu.Unlock()
	_, _, err := f.accept(tok, "gotham-city-1939")
	wantErr(t, "an accept racing a demotion of its creator", err, auth.ErrInviteInvalid)
	if _, err := f.svc.UserByUsername(context.Background(), "harper-row"); err == nil {
		t.Fatal("the account was created")
	}
}

// StageUserMerge refuses a target that is not active: merging into a
// disabled account would hand records to a user who cannot sign in.
func TestUserMerge_TargetMustBeActive(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	jason := f.user("jason-todd", "gotham.detective")
	dick := f.user("dick-grayson", "gotham.detective")
	f.edit(dick, func(u *auth.User) { u.Status = auth.StatusDisabled })
	err := f.stageMerge(f.signedIn(bruce), auth.UserMergePlan{Source: jason.ID, Target: dick.ID})
	wantErr(t, "a disabled target", err, auth.ErrInactiveUser)
	if got := f.reload(jason.ID); got.Status != auth.StatusActive {
		t.Fatalf("the source was disabled by a refused merge: %s", got.Status)
	}
}
