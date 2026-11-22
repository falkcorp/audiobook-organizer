// file: core/auth/system_user_test.go
// version: 0.1.0
// guid: 3d8e6b14-92a7-4c05-b1f3-6a40c7e9d258
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The system user ("_system", status system, SystemUserID) is an attribution
// identity: it owns rows HEAD's writers attributed to it and signs in nowhere.
// HEAD refuses it by status (internal/auth/user_active.go:23, UserIsActive:
// `return status == "" || status == "active"`, so "system" is not active), and
// core refuses it the same way: Status != StatusActive is ErrInactiveUser in
// Login (login.go), issueSession (sessions.go) and standing (sessions.go), which
// PrincipalFor and Revalidate share, so a key, a session, an extension
// assertion (Cloudflare Access) and a compat surface's credential (the ABS
// login) all stop there.
//
// The user here is given every means to sign in that a real account has (a
// password, a verified address, a role, a live key and a live session issued
// while it was active), so each refusal below is the status gate and not a
// missing credential.

const systemPassword = "wayne-manor-1939"

// systemUserFx is a compat fixture holding the system user with credentials
// issued before it took the system status, then the status set.
func systemUserFx(t *testing.T) (f *fx, c auth.Compat, u *auth.User, rawKey string, sess *auth.IssuedSession) {
	t.Helper()
	f, c = compatFx(t)
	f.commit(func(b *storage.Batch) error {
		return f.st.PutUser(b, &auth.User{
			ID: auth.SystemUserID, Username: string(auth.SystemUserID), Email: string(auth.SystemUserID) + "@local", EmailVerified: true,
			Roles: []rbac.RoleName{rbac.RoleViewer}, Status: auth.StatusActive, CreatedAt: f.clock(), UpdatedAt: f.clock(), Version: 1,
		})
	})
	u = f.withPassword(f.reload(auth.SystemUserID), systemPassword)
	rawKey, _ = f.mint(f.session(u), u.ID, "gotham:patrol")
	sess = f.issue(t, u)
	u = f.edit(u, func(u *auth.User) { u.Status = auth.StatusSystem })
	return f, c, u, rawKey, sess
}

func TestSystemUser_CannotAuthenticateByAnyPath(t *testing.T) {
	f, c, u, rawKey, sess := systemUserFx(t)
	ctx := context.Background()
	cl := from("192.0.2.91")

	refused := func(t *testing.T, got seen) {
		t.Helper()
		if got.p != nil || !got.failed || got.fail.Code != auth.CodeInactiveUser {
			t.Fatalf("bound %+v, refusal %+v (failed %v), want the inactive_user refusal", got.p, got.fail, got.failed)
		}
	}

	t.Run("password login", func(t *testing.T) {
		_, err := f.login("_system", systemPassword, cl)
		wantErr(t, "the system user's right password", err, auth.ErrInactiveUser)
	})
	t.Run("api key", func(t *testing.T) {
		_, got := serve(t, f.svc, request(rawKey, ""))
		refused(t, got)
	})
	t.Run("session cookie", func(t *testing.T) {
		_, got := serve(t, f.svc, request("", sess.Token))
		refused(t, got)
	})
	t.Run("session issue", func(t *testing.T) {
		_, err := f.svc.IssueSession(ctx, u, auth.OriginPassword, false, cl)
		wantErr(t, "a session for the system user", err, auth.ErrInactiveUser)
	})
	t.Run("revalidate", func(t *testing.T) {
		// A principal bound before the status changed stops standing.
		active := f.edit(u, func(x *auth.User) { x.Status = auth.StatusActive })
		p := f.session(active)
		f.edit(active, func(x *auth.User) { x.Status = auth.StatusSystem })
		wantErr(t, "a principal bound before the status", f.svc.Revalidate(ctx, p), auth.ErrInactiveUser)
	})
	t.Run("cloudflare access", func(t *testing.T) {
		// The Access extension resolves the verified address (_system@local) to a
		// user and asserts it; stage 12 binds it through PrincipalFor.
		_, got := bindExt(t, f, &auth.Assertion{User: u.ID, CredentialID: "cf-system", ExpiresAt: f.clock().Add(30 * time.Minute)})
		refused(t, got)
	})
	t.Run("abs compat login", func(t *testing.T) {
		// Every ABS surface (login, bearer, OpenID hand-off, identity) crosses
		// Compat.PrincipalFor or AuthenticateKey with the user it resolved.
		_, err := c.PrincipalFor(ctx, u, nil, u.CredentialEpoch, cl)
		wantErr(t, "the system user at the compat handle", err, auth.ErrInactiveUser)
		_, err = c.AuthenticateKey(ctx, rawKey, cl)
		wantErr(t, "its key at the compat handle", err, auth.ErrInactiveUser)
	})
}
