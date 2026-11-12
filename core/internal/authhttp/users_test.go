// file: core/internal/authhttp/users_test.go
// version: 1.0.0
// guid: 48b2d7f1-6e03-4c59-a1d4-9c5e30b7f862
// last-edited: 2026-11-10

package authhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// A commissioner (core:users.manage, below admin) may not mint a temp-login or
// a reset link for an administrator: that would be a way to become one. The
// refusal is 403 dominance_required and is audited where it is written.
func TestUsersManage_CannotTempLoginAdminOrResetADominatingUser(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	gordon := e.session(f.gordon)
	temp := a.link(specTemp(), a.Accounts.MintTempLogin)
	reset := a.link(specReset(), a.Accounts.MintResetLink)
	_, err := call(specTemp(), temp, gordon, userReq{ID: string(f.bruce.ID)})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	_, err = call(specReset(), reset, gordon, userReq{ID: string(f.bruce.ID)})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	var denials []audit.Event
	for _, ev := range e.audit.emitted {
		if ev.Action == "core.permission.deny" {
			denials = append(denials, ev)
		}
	}
	if len(denials) != 2 || denials[0].Reason != "dominance_required" || denials[0].Outcome != audit.Denied ||
		denials[0].Actor.UserID != string(f.gordon.ID) || denials[0].Target.ID != string(f.bruce.ID) ||
		denials[0].Route != "POST /api/core/v1/users/{id}/temp-login" || denials[0].Permission != "core:users.manage" ||
		denials[1].Route != "POST /api/core/v1/users/{id}/reset-password" {
		t.Fatalf("denials %+v", denials)
	}
	// A user gordon dominates is another matter.
	l, err := call(specTemp(), temp, gordon, userReq{ID: string(f.dick.ID)})
	if err != nil || !strings.HasPrefix(l.Link, "/login/temp#token=") {
		t.Fatalf("%+v %v", l, err)
	}
	if l, err = call(specReset(), reset, gordon, userReq{ID: string(f.dick.ID)}); err != nil || !strings.HasPrefix(l.Link, "/login/temp#token=") {
		t.Fatalf("%+v %v", l, err)
	}
	// A user that is not there.
	_, err = call(specTemp(), temp, gordon, userReq{ID: "u-nobody"})
	wantErr(t, err, http.StatusNotFound, "not_found")
}

// The audit event of a refusal names the route and the user, never the link.
func TestUsersManage_RefusalAuditCarriesNoLinkOrToken(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	_, _ = call(specTemp(), a.link(specTemp(), a.Accounts.MintTempLogin), e.session(f.gordon), userReq{ID: string(f.bruce.ID)})
	b, _ := json.Marshal(e.audit.emitted)
	if strings.Contains(string(b), "token=") || strings.Contains(string(b), "mct_") {
		t.Fatalf("%s", b)
	}
}

func TestUsers_DisableEnableDeleteAndSetRoles(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	bruce := e.session(f.bruce)
	dis := a.setStatus(specDisable(), auth.StatusDisabled)
	ena := a.setStatus(specEnable(), auth.StatusActive)
	got, err := call(specDisable(), dis, bruce, userReq{ID: string(f.dick.ID)})
	if err != nil || got.Status != "disabled" {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err = call(specEnable(), ena, bruce, userReq{ID: string(f.dick.ID)}); err != nil || got.Status != "active" {
		t.Fatalf("%+v %v", got, err)
	}
	// The only active administrator cannot be disabled or deleted.
	_, err = call(specDisable(), dis, bruce, userReq{ID: string(f.bruce.ID)})
	wantErr(t, err, http.StatusConflict, "last_admin")
	_, err = call(specUserDelete(), a.deleteUser, bruce, userReq{ID: string(f.bruce.ID)})
	wantErr(t, err, http.StatusConflict, "last_admin")
	// A commissioner cannot disable an admin, or hand out a role it cannot assign.
	gordon := e.session(f.gordon)
	_, err = call(specDisable(), dis, gordon, userReq{ID: string(f.bruce.ID)})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	_, err = call(specRoles(), a.setRoles, gordon, rolesReq{ID: string(f.dick.ID), Body: RolesReplace{Roles: []string{string(rbac.RoleAdmin)}}})
	wantErr(t, err, http.StatusForbidden, "scope_escalation")
	if r := e.reload(f.dick.ID).Roles; !slices.Equal(r, []rbac.RoleName{rbac.RoleViewer}) {
		t.Fatalf("dick's roles are %v", r)
	}
	got, err = call(specRoles(), a.setRoles, bruce, rolesReq{ID: string(f.dick.ID), Body: RolesReplace{Roles: []string{string(rbac.RoleEditor)}}})
	if err != nil || !slices.Equal(got.Roles, []string{"editor"}) {
		t.Fatalf("%+v %v", got, err)
	}
	if (&rolesReq{}).Validate() == nil {
		t.Fatal("an empty role list validates")
	}
	if _, err := call(specUserDelete(), a.deleteUser, bruce, userReq{ID: string(f.dick.ID)}); err != nil {
		t.Fatal(err)
	}
	_, err = call(specUser(), a.user, bruce, userReq{ID: string(f.dick.ID)})
	wantErr(t, err, http.StatusNotFound, "not_found")
	// A user that is not there: the same answer on every write.
	_, err = call(specDisable(), dis, bruce, userReq{ID: "u-nobody"})
	wantErr(t, err, http.StatusNotFound, "not_found")
}

func TestUsers_ListAndReadCarryNoCredentialState(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	selina := e.session(f.selina) // a viewer: users.view is not core:self
	_, err := call(specUsers(), a.users, selina, usersReq{})
	wantErr(t, err, http.StatusForbidden, "permission_denied")
	gordon := e.session(f.gordon)
	page, err := call(specUsers(), a.users, gordon, usersReq{PageParams: httpapi.PageParams{Limit: 2}})
	if err != nil || page.Total != 4 || page.Count != 2 {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = call(specUsers(), a.users, gordon, usersReq{Role: string(rbac.RoleAdmin), PageParams: httpapi.PageParams{Limit: 50}})
	if err != nil || page.Total != 1 || page.Items[0].Username != "bruce" {
		t.Fatalf("role filter: %+v %v", page, err)
	}
	page, err = call(specUsers(), a.users, gordon, usersReq{Status: "disabled", PageParams: httpapi.PageParams{Limit: 50}})
	if err != nil || page.Total != 0 || page.Items == nil {
		t.Fatalf("status filter: %+v %v", page, err)
	}
	hash, _ := e.st.PasswordHash(context.Background(), f.bruce.ID)
	u, err := call(specUser(), a.user, gordon, userReq{ID: string(f.bruce.ID)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(u)
	if hash == "" || strings.Contains(string(b), hash) || strings.Contains(strings.ToLower(string(b)), "hash") || strings.Contains(strings.ToLower(string(b)), "epoch") {
		t.Fatalf("%s", b)
	}
}

func TestInvites_CreateListDeleteAndCanAssign(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	gordon := e.session(f.gordon)
	created, err := call(specInviteNew(), a.createInvite, gordon, inviteCreateReq{Body: InviteCreate{Username: "barbara", Role: string(rbac.RoleViewer), TTL: "48h"}})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(created.Body.Link, "/invite#token=")
	if token == created.Body.Link || token == "" || !strings.HasSuffix(created.Location, created.Body.Invite.ID) {
		t.Fatalf("%+v", created)
	}
	page, err := call(specInvites(), a.invites, gordon, listReq{PageParams: httpapi.PageParams{Limit: 50}})
	if err != nil || page.Total != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	if b, _ := json.Marshal(page); strings.Contains(string(b), token) || strings.Contains(string(b), "link") {
		t.Fatalf("the list carries the invite's token or link: %s", b)
	}
	// A role gordon cannot assign.
	_, err = call(specInviteNew(), a.createInvite, gordon, inviteCreateReq{Body: InviteCreate{Username: "oswald", Role: string(rbac.RoleAdmin)}})
	wantErr(t, err, http.StatusForbidden, "scope_escalation")
	// A name already taken, an e-mail that is not an address.
	_, err = call(specInviteNew(), a.createInvite, gordon, inviteCreateReq{Body: InviteCreate{Username: "dick", Role: string(rbac.RoleViewer)}})
	wantErr(t, err, http.StatusConflict, "user_exists")
	_, err = call(specInviteNew(), a.createInvite, gordon, inviteCreateReq{Body: InviteCreate{Username: "oswald", Email: "not an address", Role: string(rbac.RoleViewer)}})
	wantErr(t, err, http.StatusBadRequest, "bad_request")
	for _, r := range []InviteCreate{{Role: "viewer"}, {Username: "x"}, {Username: "x", Role: "viewer", TTL: "yesterday"}, {Username: "x", Role: "viewer", TTL: "-1h"}} {
		if (&inviteCreateReq{Body: r}).Validate() == nil {
			t.Errorf("%+v validates", r)
		}
	}
	if _, err := call(specInviteDelete(), a.deleteInvite, gordon, userReq{ID: created.Body.Invite.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = call(specInviteDelete(), a.deleteInvite, gordon, userReq{ID: created.Body.Invite.ID})
	wantErr(t, err, http.StatusNotFound, "not_found")
}

func TestUserSessions_ListAndRevokeNeedDominanceAndMembership(t *testing.T) {
	f := newKeysFx(t)
	a, e := f.a, f.e
	_, dicks := e.withSession(f.dick)
	_, brucesS := e.withSession(f.bruce)
	gordon := e.session(f.gordon)
	page, err := call(specUserSessions(), a.userSessions, gordon, userSessionsReq{ID: string(f.dick.ID), PageParams: httpapi.PageParams{Limit: 50}})
	if err != nil || page.Total != 1 || page.Items[0].PublicID != dicks.PublicID {
		t.Fatalf("%+v %v", page, err)
	}
	if b, _ := json.Marshal(page); strings.Contains(string(b), dicks.Token) {
		t.Fatalf("%s", b)
	}
	_, err = call(specUserSessions(), a.userSessions, gordon, userSessionsReq{ID: string(f.bruce.ID)})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	_, err = call(specUserSessionDelete(), a.revokeUserSession, gordon, userSessionReq{ID: string(f.bruce.ID), PublicID: brucesS.PublicID})
	wantErr(t, err, http.StatusForbidden, "dominance_required")
	// The URL names dick and a session of bruce's: that is no session of dick's.
	_, err = call(specUserSessionDelete(), a.revokeUserSession, gordon, userSessionReq{ID: string(f.dick.ID), PublicID: brucesS.PublicID})
	wantErr(t, err, http.StatusNotFound, "not_found")
	if !slices.Contains(e.sessionIDs(f.bruce.ID), brucesS.PublicID) {
		t.Fatal("the admin's session was revoked through dick's URL")
	}
	if _, err := call(specUserSessionDelete(), a.revokeUserSession, gordon, userSessionReq{ID: string(f.dick.ID), PublicID: dicks.PublicID}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(e.sessionIDs(f.dick.ID), dicks.PublicID) {
		t.Fatal("dick's session survives")
	}
	_, err = call(specUserSessions(), a.userSessions, gordon, userSessionsReq{ID: "u-nobody"})
	wantErr(t, err, http.StatusNotFound, "not_found")
}
