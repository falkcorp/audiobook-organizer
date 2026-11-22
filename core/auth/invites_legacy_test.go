// file: core/auth/invites_legacy_test.go
// version: 0.1.0
// guid: b0b01128-79bc-417b-858d-fafb35e12db0
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/rbac"
)

// The legacy surface's invite list and delete (F4-08 B10y, item 4 cause B and
// auth-off cause 2). HEAD's GET and DELETE /api/v1/users/invites were
// s.perm(users.manage) and nothing more, so InviteAdmin's AdminInvites and
// AdminDeleteInvite admit core:users.manage through any method, a key, a
// delegated session and the auth-disabled principal included, and refuse a
// principal without it. Service.DeleteInvite, core's native route's, keeps its
// rule: a user and an interactive method.

func TestInviteAdmin_LegacyListAndDeleteTakeUsersManageThroughAnyMethod(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	viewer := f.user("alfred-pennyworth", rbac.RoleViewer)
	admin := f.session(bruce)
	_, managerKey := f.mint(admin, bruce.ID, "core:users.manage")
	_, viewKey := f.mint(admin, bruce.ID, "core:users.view")
	delegated, err := f.svc.PrincipalFor(context.Background(), bruce, auth.MethodSessionDelegated, nil, bruce.CredentialEpoch)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := rbac.NewCatalog(rbac.CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	authOff := &auth.Principal{Method: auth.MethodAuthDisabled, Perms: authz.AuthDisabled(cat)}
	if !authOff.Perms.Has("core:users.manage") {
		t.Fatal("the auth-disabled checker does not hold core:users.manage; the auth-off cells would be vacuous")
	}

	admitted := map[string]*auth.Principal{
		"admin session":                 admin,
		"key holding core:users.manage": f.keyPrincipal(bruce, managerKey),
		"delegated session":             delegated,
		"auth-disabled principal":       authOff,
	}
	refused := map[string]*auth.Principal{
		"viewer session":                     f.session(viewer),
		"key without core:users.manage":      f.keyPrincipal(bruce, viewKey),
		"auth-disabled without users.manage": {Method: auth.MethodAuthDisabled, Perms: scopedChecker{"core:users.view": true}},
		"a session principal with no user":   {Method: auth.MethodSession, Perms: scopedChecker{"core:users.manage": true}},
	}

	for name, p := range admitted {
		t.Run("admit/"+name, func(t *testing.T) {
			_, v := f.invite(admin, auth.InviteSpec{Username: "irena-dubrovna", Role: "gotham.detective"})
			ctx := auth.WithPrincipal(context.Background(), p)
			var listed bool
			for iv, err := range f.svc.AdminInvites(ctx) {
				if err != nil {
					t.Fatalf("AdminInvites: %v", err)
				}
				listed = listed || iv.ID == v.ID
			}
			if !listed {
				t.Fatalf("AdminInvites did not list %s", v.ID)
			}
			if err := f.svc.AdminDeleteInvite(ctx, v.ID); err != nil {
				t.Fatalf("AdminDeleteInvite: %v", err)
			}
			if f.storedInvite(v.ID) != nil {
				t.Fatal("AdminDeleteInvite left the invite")
			}
			if err := f.svc.AdminDeleteInvite(ctx, v.ID); !errors.Is(err, auth.ErrNotFound) {
				t.Fatalf("a second AdminDeleteInvite = %v, want ErrNotFound", err)
			}
		})
	}
	for name, p := range refused {
		t.Run("refuse/"+name, func(t *testing.T) {
			_, v := f.invite(admin, auth.InviteSpec{Username: "harvey-bullock", Role: "gotham.detective"})
			defer func() { _ = f.svc.AdminDeleteInvite(f.as(admin), v.ID) }()
			ctx := auth.WithPrincipal(context.Background(), p)
			for _, err := range f.svc.AdminInvites(ctx) {
				if !errors.Is(err, auth.ErrDenied) {
					t.Fatalf("AdminInvites yielded %v, want ErrDenied", err)
				}
			}
			wantErr(t, "AdminDeleteInvite", f.svc.AdminDeleteInvite(ctx, v.ID), auth.ErrDenied)
			if f.storedInvite(v.ID) == nil {
				t.Fatal("a refused AdminDeleteInvite removed the invite")
			}
		})
	}
	t.Run("refuse/no principal", func(t *testing.T) {
		wantErr(t, "AdminDeleteInvite", f.svc.AdminDeleteInvite(context.Background(), "01NOSUCH"), auth.ErrDenied)
	})

	// Core's native delete keeps its rule: the key and the auth-disabled
	// principal that the legacy delete admits are refused by it.
	t.Run("native DeleteInvite unchanged", func(t *testing.T) {
		_, v := f.invite(admin, auth.InviteSpec{Username: "renee-montoya", Role: "gotham.detective"})
		key := f.keyPrincipal(bruce, managerKey)
		wantErr(t, "DeleteInvite as a key", f.svc.DeleteInvite(f.as(key), key, v.ID), auth.ErrDenied)
		wantErr(t, "DeleteInvite as the auth-disabled principal", f.svc.DeleteInvite(f.as(authOff), authOff, v.ID), auth.ErrDenied)
		wantErr(t, "DeleteInvite as a delegated session", f.svc.DeleteInvite(f.as(delegated), delegated, v.ID), auth.ErrDenied)
		if f.storedInvite(v.ID) == nil {
			t.Fatal("a refused native delete removed the invite")
		}
	})
}
