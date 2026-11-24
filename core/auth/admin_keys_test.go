// file: core/auth/admin_keys_test.go
// version: 0.1.0
// guid: 6c4f2a83-9e17-4b5d-8f02-3a7d1e6b95c8
// last-edited: 2026-11-22

package auth_test

import (
	"context"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// The API-key half of the actor-checked admin handle (F4-08 B10z): every
// method acts as the principal bound on ctx, the reads apply core's key-route
// rule (own keys; another owner's only for core:users.manage and dominance),
// and KeyOwnerScopes is the owner's role-only set under mayManage. Batman
// names.

// listed is the IDs AdminAPIKeys yields to actor, failing on an error.
func (f *fx) listed(actor *auth.Principal, all bool) []string {
	f.t.Helper()
	var out []string
	for k, err := range f.svc.AdminAPIKeys(f.as(actor), actor, all) {
		if err != nil {
			f.t.Fatalf("AdminAPIKeys: %v", err)
		}
		out = append(out, k.ID)
	}
	slices.Sort(out)
	return out
}

func sorted(ids ...string) []string { slices.Sort(ids); return ids }

func TestKeyAdmin_ReadsFollowTheKeyRouteRule(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	gordon := f.user("jim-gordon", roleCommissioner)
	tim := f.user("tim-drake", "gotham.detective")
	admin, comm, det := f.session(bruce), f.session(gordon), f.session(tim)
	_, bk := f.mint(admin, bruce.ID, "gotham:patrol")
	_, gk := f.mint(comm, gordon.ID, "gotham:patrol")
	_, tk := f.mint(det, tim.ID, "gotham:patrol")

	// Own keys, with or without all, for a caller without users.manage.
	for _, all := range []bool{false, true} {
		if got := f.listed(det, all); !slices.Equal(got, []string{tk.ID}) {
			t.Fatalf("detective all=%v lists %v, want only its own %s", all, got, tk.ID)
		}
	}
	// A manager lists the owners whose roles it dominates, never an admin's.
	if got, want := f.listed(comm, true), sorted(gk.ID, tk.ID); !slices.Equal(got, want) {
		t.Fatalf("commissioner all=true lists %v, want %v (bruce's admin key left out)", got, want)
	}
	if got := f.listed(comm, false); !slices.Equal(got, []string{gk.ID}) {
		t.Fatalf("commissioner all=false lists %v, want its own", got)
	}
	if got, want := f.listed(admin, true), sorted(bk.ID, gk.ID, tk.ID); !slices.Equal(got, want) {
		t.Fatalf("admin all=true lists %v, want %v", got, want)
	}

	// One key: missing is ErrNotFound, existing but unreadable is ErrDenied.
	get := func(p *auth.Principal, id string) error {
		_, err := f.svc.AdminAPIKey(f.as(p), p, id)
		return err
	}
	wantErr(t, "a missing key", get(det, "01JBATCAVE0000000000000000"), auth.ErrNotFound)
	wantErr(t, "another user's key, no users.manage", get(det, bk.ID), auth.ErrDenied)
	wantErr(t, "an admin's key, a manager that does not dominate", get(comm, bk.ID), auth.ErrDenied)
	for _, c := range []struct {
		p  *auth.Principal
		id string
	}{{det, tk.ID}, {comm, tk.ID}, {admin, gk.ID}} {
		if err := get(c.p, c.id); err != nil {
			t.Fatalf("AdminAPIKey %s as %s: %v", c.id, c.p.User.ID, err)
		}
	}
}

func TestKeyAdmin_ActsOnlyAsTheBoundPrincipal(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	tim := f.user("tim-drake", "gotham.detective")
	admin, det := f.session(bruce), f.session(tim)
	_, tk := f.mint(det, tim.ID, "gotham:patrol")
	off := &auth.Principal{Method: auth.MethodAuthDisabled, Perms: scopedChecker{rbac.CoreAdmin: true, "core:users.manage": true}}

	for name, ctx := range map[string]context.Context{
		"no principal bound":                  context.Background(),
		"another principal bound":             f.as(det),
		"the auth-off principal is the actor": auth.WithPrincipal(context.Background(), off),
	} {
		actor := admin
		if name == "the auth-off principal is the actor" {
			actor = off
		}
		_, _, err := f.svc.AdminCreateAPIKey(ctx, actor, auth.KeySpec{Owner: tim.ID, Name: "batarang", Scopes: []rbac.Permission{"gotham:patrol"}})
		wantErr(t, name+": AdminCreateAPIKey", err, auth.ErrDenied)
		wantErr(t, name+": AdminSetAPIKeyStatus", f.svc.AdminSetAPIKeyStatus(ctx, actor, tk.ID, auth.KeyDisabled), auth.ErrDenied)
		wantErr(t, name+": AdminRevokeAPIKey", f.svc.AdminRevokeAPIKey(ctx, actor, tk.ID), auth.ErrDenied)
		_, err = f.svc.AdminAPIKey(ctx, actor, tk.ID)
		wantErr(t, name+": AdminAPIKey", err, auth.ErrDenied)
		_, err = f.svc.KeyOwnerScopes(ctx, actor, tim.ID)
		wantErr(t, name+": KeyOwnerScopes", err, auth.ErrDenied)
		n := 0
		for k, err := range f.svc.AdminAPIKeys(ctx, actor, true) {
			n++
			if k != nil {
				t.Fatalf("%s: AdminAPIKeys yielded key %s", name, k.ID)
			}
			wantErr(t, name+": AdminAPIKeys", err, auth.ErrDenied)
		}
		if n != 1 {
			t.Fatalf("%s: AdminAPIKeys yielded %d elements, want the one refusal", name, n)
		}
	}
	if k := f.key(tk.ID); k.Status != auth.KeyActive {
		t.Fatalf("a refused call changed the key: status %s", k.Status)
	}

	// Bound as itself, the same calls go through core's own rules.
	raw, k, err := f.svc.AdminCreateAPIKey(f.as(det), det, auth.KeySpec{Name: "grapnel", Scopes: []rbac.Permission{"gotham:patrol"}})
	if err != nil || raw == "" || k.Owner != tim.ID {
		t.Fatalf("AdminCreateAPIKey as itself: %v (owner %v)", err, k)
	}
	_, _, err = f.svc.AdminCreateAPIKey(f.as(det), det, auth.KeySpec{Name: "signal", Scopes: []rbac.Permission{"gotham:signal"}})
	wantErr(t, "a scope the detective lacks", err, auth.ErrScopeEscalation)
	if err := f.svc.AdminSetAPIKeyStatus(f.as(det), det, k.ID, auth.KeyDisabled); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AdminRevokeAPIKey(f.as(det), det, k.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "a revoked key back to active", f.svc.AdminSetAPIKeyStatus(f.as(det), det, k.ID, auth.KeyActive), auth.ErrKeyRevoked)
}

func TestKeyAdmin_KeyOwnerScopes(t *testing.T) {
	f := newFx(t)
	bruce := f.user("bruce", rbac.RoleAdmin)
	tim := f.user("tim-drake", "gotham.detective")
	admin, det := f.session(bruce), f.session(tim)

	got, err := f.svc.KeyOwnerScopes(f.as(det), det, tim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "gotham:patrol") || !slices.Contains(got, "gotham:cases.read") || slices.Contains(got, rbac.CoreAdmin) || slices.Contains(got, "gotham:signal") {
		t.Fatalf("the detective's own set = %v, want its role's scopes only", got)
	}
	_, err = f.svc.KeyOwnerScopes(f.as(det), det, bruce.ID)
	wantErr(t, "another user's set, no users.manage", err, auth.ErrDenied)
	if got, err = f.svc.KeyOwnerScopes(f.as(admin), admin, tim.ID); err != nil || slices.Contains(got, rbac.CoreAdmin) {
		t.Fatalf("admin reading the detective's set: %v %v", got, err)
	}
	if got, err = f.svc.KeyOwnerScopes(f.as(admin), admin, bruce.ID); err != nil || !slices.Contains(got, rbac.CoreAdmin) {
		t.Fatalf("an admin's own set carries no core:admin: %v %v", got, err)
	}
	// The set is the role's as stored now: a demotion shows at once.
	f.setRoles(tim.ID, rbac.RoleViewer)
	if got, err = f.svc.KeyOwnerScopes(f.as(admin), admin, tim.ID); err != nil || slices.Contains(got, "gotham:cases.read") {
		t.Fatalf("after the demotion the set is %v (%v), want the viewer's", got, err)
	}
}
