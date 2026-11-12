// file: core/internal/rbachttp/routes_test.go
// version: 1.0.1
// guid: 695e783a-feeb-4b32-97d7-a73f62a146d2
// last-edited: 2026-11-11

package rbachttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// ---- helpers --------------------------------------------------------------

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return string(b)
}

// wantStatus fails t unless err is an *httpapi.Error with status and code.
func wantStatus(t testing.TB, err error, status int, code string) {
	t.Helper()
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok {
		t.Errorf("err = %v (%T), want an *httpapi.Error %d %s", err, err, status, code)
		return
	}
	if he.Status != status || he.Code != code {
		t.Errorf("err = %d %s (%v), want %d %s", he.Status, he.Code, err, status, code)
	}
}

// errBlob is everything an answer built from err can show a client: the
// Detail and the Fields httpapi serializes. Err, the cause, is never
// serialized (Error()'s text is for logs), and a plain error is a 500 whose
// text is withheld, so it shows a client nothing.
func errBlob(t testing.TB, err error) string {
	t.Helper()
	he, ok := errors.AsType[*httpapi.Error](err)
	if !ok {
		return ""
	}
	return he.Code + " " + he.Detail + " " + mustJSON(t, he.Fields)
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeAPI is the handlers over a fake service, with an Emit recorder.
func fakeAPI(f *fakeRoles) (*api, *emitted) {
	em := &emitted{}
	return &api{roles: f, audit: em, log: discard(), base: "/api/core/v1"}, em
}

// ---- the route table ------------------------------------------------------

var wantRoutes = []struct {
	method, path, name, perm string
	interactive              bool
}{
	{http.MethodGet, "/api/core/v1/roles", "listRoles", "core:roles.view", false},
	{http.MethodPost, "/api/core/v1/roles", "createRole", "core:roles.manage", true},
	{http.MethodGet, "/api/core/v1/roles/{name}", "getRole", "core:roles.view", false},
	{http.MethodPatch, "/api/core/v1/roles/{name}", "updateRole", "core:roles.manage", true},
	{http.MethodDelete, "/api/core/v1/roles/{name}", "deleteRole", "core:roles.manage", true},
	{http.MethodPost, "/api/core/v1/roles/{name}/accept-seed", "acceptRoleSeed", "core:roles.manage", true},
}

func registered(t testing.TB) *httpapi.Router {
	t.Helper()
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: discard()})
	Register(r.CoreGroup(), Deps{})
	return r
}

// TestRegister_RouteTable pins 05-rbac §4.7's table: each route's method,
// path, name, permission, and Guards.Interactive on every write (and on no
// read), on the core group with the default rate class and no owner guard. A
// dropped guard fails here.
func TestRegister_RouteTable(t *testing.T) {
	var got []httpapi.RouteInfo
	for ri := range registered(t).Routes() {
		got = append(got, ri)
	}
	slices.SortFunc(got, func(a, b httpapi.RouteInfo) int { return strings.Compare(a.Method+a.Pattern, b.Method+b.Pattern) })
	if len(got) != len(wantRoutes) {
		t.Fatalf("%d routes registered, want %d: %+v", len(got), len(wantRoutes), got)
	}
	names := map[string]bool{}
	for _, w := range wantRoutes {
		i := slices.IndexFunc(got, func(ri httpapi.RouteInfo) bool { return ri.Method == w.method && ri.Pattern == w.path })
		if i < 0 {
			t.Errorf("no route %s %s", w.method, w.path)
			continue
		}
		ri := got[i]
		if ri.Owner != "core" || ri.Kind != httpapi.RouteKindNative || ri.Name != w.name {
			t.Errorf("%s %s = owner %s kind %d name %s, want core native %s", w.method, w.path, ri.Owner, ri.Kind, ri.Name, w.name)
		}
		if ri.Perm.IsZero() || ri.Perm.String() != w.perm {
			t.Errorf("%s requires %s, want %s", w.name, ri.Perm, w.perm)
		}
		if want := (httpapi.Guards{Interactive: w.interactive}); ri.Guards != want {
			t.Errorf("%s guards = %+v, want %+v", w.name, ri.Guards, want)
		}
		if ri.RateClass != httpapi.RateDefault {
			t.Errorf("%s rate class = %d, want the default", w.name, ri.RateClass)
		}
		if names[ri.Name] {
			t.Errorf("route name %s is used twice", ri.Name)
		}
		names[ri.Name] = true
	}
	// The brief's rule, independent of the table above: no write is without
	// the interactive guard.
	for _, ri := range got {
		if ri.Method != http.MethodGet && !ri.Guards.Interactive {
			t.Errorf("%s %s is a write without Guards.Interactive", ri.Method, ri.Pattern)
		}
	}
}

// TestRegister_BuildFailsOnlyForWantOfAuth: until F4-08 a router refuses every
// non-Public native route with ErrNoAuthService. Everything else Build checks
// (a malformed binding, a body httpapi cannot describe, a non-snake_case JSON
// name, a duplicate name, a pattern conflict) would be a further error here.
func TestRegister_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	_, err := registered(t).Build()
	if err == nil {
		t.Fatal("Build succeeded without an auth service")
	}
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Build error %T does not join its problems", err)
	}
	problems := joined.Unwrap()
	if len(problems) != len(wantRoutes) {
		t.Errorf("Build reported %d problems, want %d (one ErrNoAuthService per route): %v", len(problems), len(wantRoutes), err)
	}
	for _, p := range problems {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build reported %v, want only ErrNoAuthService", p)
		}
	}
}

// ---- the real service: 05-rbac §4.7, §4.8 ---------------------------------

// TestSeedRole_EditRefused is 05-rbac §8's test over a real service: editing or
// deleting a seed role is 409 seed_role_locked whatever the body says,
// accept-seed is the one write a seed role allows, and a custom role can be
// created, patched and deleted through the same handlers.
func TestSeedRole_EditRefused(t *testing.T) {
	ctx := context.Background()
	a, svc := realAPI(t)
	p := svc.admin()

	editor, err := a.get(ctx, in(p, roleReq{Name: "editor"}))
	if err != nil {
		t.Fatal(err)
	}
	if editor.Origin != "seed" || editor.Locked {
		t.Fatalf("editor = %+v", editor)
	}

	title := "Shadow Editor"
	_, err = a.update(ctx, in(p, patchReq{Name: "editor", IfMatch: fmt.Sprint(editor.Version), Body: RbacRolePatch{Title: &title}}))
	wantStatus(t, err, http.StatusConflict, "seed_role_locked")
	// A body naming a scope no module declares must not turn the refusal into
	// a 422: the 409 is what a seed role answers.
	ghost := []string{"gotham:no.such.scope"}
	_, err = a.update(ctx, in(p, patchReq{Name: "editor", IfMatch: fmt.Sprint(editor.Version), Body: RbacRolePatch{Permissions: &ghost}}))
	wantStatus(t, err, http.StatusConflict, "seed_role_locked")
	_, err = a.remove(ctx, in(p, versionReq{Name: "editor", IfMatch: fmt.Sprint(editor.Version)}))
	wantStatus(t, err, http.StatusConflict, "seed_role_locked")

	// admin is locked, with its own code.
	adm, err := a.get(ctx, in(p, roleReq{Name: "admin"}))
	if err != nil || !adm.Locked {
		t.Fatalf("admin = %+v, %v", adm, err)
	}
	_, err = a.update(ctx, in(p, patchReq{Name: "admin", IfMatch: fmt.Sprint(adm.Version), Body: RbacRolePatch{Title: &title}}))
	wantStatus(t, err, http.StatusConflict, "role_locked")
	_, err = a.remove(ctx, in(p, versionReq{Name: "admin", IfMatch: fmt.Sprint(adm.Version)}))
	wantStatus(t, err, http.StatusConflict, "role_locked")

	// accept-seed: gotham.detective holds a release's additions, pending.
	det, err := a.get(ctx, in(p, roleReq{Name: "gotham.detective"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gotham:cave.manage", "gotham:settings.manage"}; !slices.Equal(det.PendingSeed, want) {
		t.Fatalf("pending_seed = %v, want %v", det.PendingSeed, want)
	}
	if slices.Contains(det.Permissions, "gotham:cave.manage") {
		t.Error("a pending permission is effective")
	}
	acc, err := a.acceptSeed(ctx, in(p, versionReq{Name: "gotham.detective", IfMatch: fmt.Sprintf("%q", fmt.Sprint(det.Version))}))
	if err != nil {
		t.Fatalf("accept-seed: %v", err)
	}
	if len(acc.PendingSeed) != 0 || !slices.Contains(acc.Permissions, "gotham:cave.manage") || acc.Version != det.Version+1 {
		t.Errorf("accepted = %+v", acc)
	}
	_, err = a.acceptSeed(ctx, in(p, versionReq{Name: "gotham.detective", IfMatch: fmt.Sprint(acc.Version)}))
	wantStatus(t, err, http.StatusConflict, "no_pending_seed")

	// A custom role: create, patch (merge), stale write, delete.
	created, err := a.create(ctx, in(p, createReq{Body: RbacRoleCreate{
		Name: "custom.oracle", Title: "Oracle", Description: "Watches the wires", Permissions: []string{"gotham:patrol"},
	}}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Location != "/api/core/v1/roles/custom.oracle" || created.Body.Origin != "custom" || created.Body.Version != 1 {
		t.Errorf("created = %+v", created)
	}
	_, err = a.create(ctx, in(p, createReq{Body: RbacRoleCreate{Name: "custom.oracle", Title: "Again"}}))
	wantStatus(t, err, http.StatusConflict, "role_exists")

	newTitle := "Oracle of Gotham"
	upd, err := a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: "1", Body: RbacRolePatch{Title: &newTitle}}))
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if upd.Title != newTitle || upd.Description != "Watches the wires" || !slices.Equal(upd.Permissions, []string{"gotham:patrol"}) || upd.Version != 2 {
		t.Errorf("patched = %+v: a field the body omits must keep its value", upd)
	}
	_, err = a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: "1", Body: RbacRolePatch{Title: &newTitle}}))
	wantStatus(t, err, http.StatusPreconditionFailed, "precondition_failed")
	_, err = a.remove(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: "1"}))
	wantStatus(t, err, http.StatusPreconditionFailed, "precondition_failed")

	more := []string{"gotham:patrol", "gotham:signal"}
	upd, err = a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: `"2"`, Body: RbacRolePatch{Permissions: &more}}))
	if err != nil || !slices.Equal(upd.Permissions, more) || upd.Title != newTitle {
		t.Fatalf("patch permissions = %+v, %v", upd, err)
	}
	bad := []string{"gotham:no.such.scope"}
	_, err = a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: "3", Body: RbacRolePatch{Permissions: &bad}}))
	wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")

	if _, err := a.remove(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: "3"})); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = a.get(ctx, in(p, roleReq{Name: "custom.oracle"}))
	wantStatus(t, err, http.StatusNotFound, "not_found")
	_, err = a.remove(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: "3"}))
	wantStatus(t, err, http.StatusNotFound, "not_found")
	_, err = a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: "3", Body: RbacRolePatch{Title: &newTitle}}))
	wantStatus(t, err, http.StatusNotFound, "not_found")
}

// TestCreateRole_BadNameAndScopeAre422: the service's validation answers as
// field problems that quote nothing the caller sent.
func TestCreateRole_BadNameAndScopeAre422(t *testing.T) {
	ctx := context.Background()
	a, svc := realAPI(t)
	p := svc.admin()

	_, err := a.create(ctx, in(p, createReq{Body: RbacRoleCreate{Name: "oracle", Title: "Oracle"}}))
	wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
	if he, _ := errors.AsType[*httpapi.Error](err); he == nil || len(he.Fields) != 1 || he.Fields[0].Field != "/body/name" {
		t.Errorf("bad name fields = %+v", err)
	}

	secret := "gotham:vault-of-the-penguin"
	_, err = a.create(ctx, in(p, createReq{Body: RbacRoleCreate{Name: "custom.oracle", Title: "Oracle", Permissions: []string{secret}}}))
	wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
	if he, _ := errors.AsType[*httpapi.Error](err); he == nil || len(he.Fields) == 0 || he.Fields[0].Field != "/body/permissions" {
		t.Errorf("bad scope fields = %+v", err)
	}
	if strings.Contains(errBlob(t, err), "penguin") {
		t.Errorf("the 422 quotes the permission: %s", errBlob(t, err))
	}
}

// TestCreateRole_ScopeEscalation: an administrator of roles who lacks a scope
// may not hand it out. The service audits that refusal itself, so exactly one
// core.permission.deny exists even though the route's Emitter is the same
// logger: the route does not record it a second time.
func TestCreateRole_ScopeEscalation(t *testing.T) {
	ctx := context.Background()
	a, svc := realAPI(t)
	if _, err := a.create(ctx, in(svc.admin(), createReq{Body: RbacRoleCreate{
		Name: "custom.lucius", Title: "Lucius", Permissions: []string{"core:roles.view", "core:roles.manage"},
	}})); err != nil {
		t.Fatal(err)
	}
	lucius := svc.principal(auth.MethodSession, "custom.lucius")

	// He can still list and read roles.
	if _, err := a.list(ctx, in(lucius, listReq{Limit: 50})); err != nil {
		t.Fatalf("list as lucius: %v", err)
	}
	_, err := a.create(ctx, in(lucius, createReq{Body: RbacRoleCreate{Name: "custom.joker", Title: "Joker", Permissions: []string{"gotham:signal"}}}))
	wantStatus(t, err, http.StatusForbidden, "scope_escalation")

	denials := svc.audit.denials()
	if len(denials) != 1 || denials[0].Action != "core.permission.deny" || denials[0].Reason != "scope_escalation" {
		t.Errorf("denials = %+v, want exactly one scope_escalation", denials)
	}
	if _, err := a.get(ctx, in(svc.admin(), roleReq{Name: "custom.joker"})); err == nil {
		t.Error("the refused role exists")
	}
}

// TestListRoles_RealService: the list holds the seed and custom roles with
// their pending additions, and the wire shape carries nothing of the service's
// seed bookkeeping.
func TestListRoles_RealService(t *testing.T) {
	ctx := context.Background()
	a, svc := realAPI(t)
	page, err := a.list(ctx, in(svc.admin(), listReq{Limit: 50}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range page.Items {
		names = append(names, r.Name)
	}
	slices.Sort(names)
	if want := []string{"admin", "editor", "gotham.detective", "viewer"}; !slices.Equal(names, want) || page.Total != 4 {
		t.Errorf("roles = %v (total %d), want %v", names, page.Total, want)
	}
	var m map[string]any
	for _, r := range page.Items {
		if r.Name != "gotham.detective" {
			continue
		}
		if err := json.Unmarshal([]byte(mustJSON(t, r)), &m); err != nil {
			t.Fatal(err)
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"created_at", "description", "locked", "name", "origin", "pending_seed", "permissions", "title", "updated_at", "version"}
	if !slices.Equal(keys, want) {
		t.Errorf("role JSON keys = %v, want %v", keys, want)
	}
}

// ---- the guards -----------------------------------------------------------

// everyHandler calls every handler with p and returns what each answers.
func everyHandler(t testing.TB, a *api, p *auth.Principal) map[string]error {
	t.Helper()
	ctx := context.Background()
	out := map[string]error{}
	_, out["list"] = a.list(ctx, in(p, listReq{}))
	_, out["get"] = a.get(ctx, in(p, roleReq{Name: "custom.oracle"}))
	_, out["create"] = a.create(ctx, in(p, createReq{Body: RbacRoleCreate{Name: "custom.joker", Title: "Joker"}}))
	title := "x"
	_, out["update"] = a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: "1", Body: RbacRolePatch{Title: &title}}))
	_, out["remove"] = a.remove(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: "1"}))
	_, out["accept"] = a.acceptSeed(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: "1"}))
	return out
}

// TestHandlers_NoPrincipalIs401: a handler reached without a principal fails
// closed with 05-http-api's authentication_required, and touches no service.
func TestHandlers_NoPrincipalIs401(t *testing.T) {
	f := &fakeRoles{}
	a, _ := fakeAPI(f)
	for _, err := range everyHandler(t, a, nil) {
		wantStatus(t, err, http.StatusUnauthorized, "authentication_required")
	}
	if len(f.calls) != 0 {
		t.Errorf("service called without a principal: %v", f.calls)
	}
}

// TestHandlers_NoServiceIs503: a package mounted without its service answers
// 503, not a nil dereference.
func TestHandlers_NoServiceIs503(t *testing.T) {
	_, svc := realAPI(t)
	a := &api{log: discard(), base: "/api/core/v1"}
	for _, err := range everyHandler(t, a, svc.admin()) {
		wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	}
}

// TestWrites_NeedAnInteractiveSession: every write refuses a principal that
// may not change credentials, whatever it holds, with interactive_required
// (the refusal Guards.Interactive makes at stage 13), and calls no service.
func TestWrites_NeedAnInteractiveSession(t *testing.T) {
	_, svc := realAPI(t)
	for _, m := range []auth.Method{auth.MethodAPIKey, auth.MethodAuthDisabled, auth.MethodNone} {
		f := &fakeRoles{}
		a, _ := fakeAPI(f)
		p := svc.principal(m, rbac.RoleAdmin)
		got := everyHandler(t, a, p)
		for _, name := range []string{"create", "update", "remove", "accept"} {
			wantStatus(t, got[name], http.StatusForbidden, "interactive_required")
		}
		if slices.ContainsFunc(f.calls, func(c string) bool { return c != "Roles" && c != "Role" }) {
			t.Errorf("method %q: a write reached the service: %v", m, f.calls)
		}
	}
}

// TestHandlers_NeedTheirPermission: an interactive principal without
// roles.view reads nothing and one without roles.manage writes nothing (403
// permission_denied), whatever else it holds.
func TestHandlers_NeedTheirPermission(t *testing.T) {
	_, svc := realAPI(t)
	viewer := svc.principal(auth.MethodSession, rbac.RoleViewer)
	f := &fakeRoles{}
	a, _ := fakeAPI(f)
	for _, err := range everyHandler(t, a, viewer) {
		wantStatus(t, err, http.StatusForbidden, "permission_denied")
	}
	if len(f.calls) != 0 {
		t.Errorf("service called for a principal without the permission: %v", f.calls)
	}
	// A principal with no checker at all holds nothing.
	for _, err := range everyHandler(t, a, &auth.Principal{User: &auth.User{ID: "u-alfred"}, Method: auth.MethodSession}) {
		wantStatus(t, err, http.StatusForbidden, "permission_denied")
	}
}

// ---- If-Match -------------------------------------------------------------

// TestIfMatch: a role write names the role's version in If-Match, quoted or
// bare. Absent is 428; a value that is not a version, including the wildcard
// and a weak tag, is 422 on the header; both are refused before any lookup.
func TestIfMatch(t *testing.T) {
	ctx := context.Background()
	_, svc := realAPI(t)
	p := svc.admin()
	f := &fakeRoles{roles: []*rbac.Role{{Name: "custom.oracle", Title: "Oracle", Origin: rbac.OriginCustom, Version: 7}}}
	a, _ := fakeAPI(f)
	title := "x"

	for _, c := range []struct {
		header string
		want   int64
	}{{`7`, 7}, {`"7"`, 7}, {` "12" `, 12}, {`0`, 0}} {
		f.version = -1
		if _, err := a.remove(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: c.header})); err != nil || f.version != c.want {
			t.Errorf("If-Match %q: version %d, err %v, want %d", c.header, f.version, err, c.want)
		}
	}
	f.calls = nil
	for _, h := range []string{"", "   "} {
		_, err := a.remove(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: h}))
		wantStatus(t, err, http.StatusPreconditionRequired, "precondition_required")
		_, err = a.acceptSeed(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: h}))
		wantStatus(t, err, http.StatusPreconditionRequired, "precondition_required")
		_, err = a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: h, Body: RbacRolePatch{Title: &title}}))
		wantStatus(t, err, http.StatusPreconditionRequired, "precondition_required")
	}
	for _, h := range []string{"*", `W/"7"`, `"7`, `7"`, "-1", "7.5", "abc", `""`, "99999999999999999999", "7, 8"} {
		for name, err := range map[string]error{
			"remove": func() error {
				_, err := a.remove(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: h}))
				return err
			}(),
			"accept": func() error {
				_, err := a.acceptSeed(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: h}))
				return err
			}(),
			"update": func() error {
				_, err := a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: h, Body: RbacRolePatch{Title: &title}}))
				return err
			}(),
		} {
			wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
			if he, _ := errors.AsType[*httpapi.Error](err); he == nil || len(he.Fields) != 1 || he.Fields[0].Field != "/header/If-Match" {
				t.Errorf("%s with If-Match %q: fields = %+v", name, h, err)
			}
		}
	}
	if slices.Contains(f.calls, "DeleteRole") || slices.Contains(f.calls, "AcceptSeed") || slices.Contains(f.calls, "UpdateRole") {
		t.Errorf("a refused If-Match reached the service: %v", f.calls)
	}
}

// ---- error mapping --------------------------------------------------------

// TestMapError pins every status and code, that the cause is kept on Err and
// never in what a client sees, and that a wrapped sentinel maps like the bare
// one.
func TestMapError(t *testing.T) {
	const secret = "custom.the-riddler-vault-9f3a"
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{rbac.ErrEscalation, 403, "scope_escalation"},
		{rbac.ErrDominance, 403, "dominance_required"},
		{rbac.ErrDenied, 403, "permission_denied"},
		{rbac.ErrUnknownRole, 404, "not_found"},
		{rbac.ErrLockedRole, 409, "role_locked"},
		{rbac.ErrSeedRoleLocked, 409, "seed_role_locked"},
		{rbac.ErrRoleInUse, 409, "role_in_use"},
		{rbac.ErrLastAdmin, 409, "last_admin"},
		{rbac.ErrRoleExists, 409, "role_exists"},
		{rbac.ErrNoPendingSeed, 409, "no_pending_seed"},
		{rbac.ErrVersionMismatch, 412, "precondition_failed"},
		{rbac.ErrNotFrozen, 503, "unavailable"},
		{rbac.ErrRoleName, 422, "validation_failed"},
		{rbac.ErrBadScope, 422, "validation_failed"},
		{rbac.ErrUnknownScope, 422, "validation_failed"},
	} {
		t.Run(c.code+"/"+c.err.Error(), func(t *testing.T) {
			wrapped := fmt.Errorf("role %s at /var/lib/gotham/roles.db: %w", secret, c.err)
			got := mapError(wrapped, "/body/permissions")
			wantStatus(t, got, c.status, c.code)
			if blob := errBlob(t, got); strings.Contains(blob, secret) || strings.Contains(blob, "/var/lib") {
				t.Errorf("the answer carries the service's message: %s", blob)
			}
			if he, _ := errors.AsType[*httpapi.Error](got); he != nil && !errors.Is(he.Err, c.err) && c.status != 422 {
				t.Errorf("the cause %v is not kept on Err for the log", c.err)
			}
			wantStatus(t, mapError(c.err, ""), c.status, c.code)
		})
	}
	if mapError(nil, "") != nil {
		t.Error("mapError(nil) != nil")
	}
	// The same service error may wrap both sentinels; the escalation wins and
	// an unknown role named by CanAssign stays a refusal, not a 404.
	wantStatus(t, mapError(fmt.Errorf("%w: %w", rbac.ErrEscalation, rbac.ErrUnknownRole), ""), 403, "scope_escalation")
	wantStatus(t, mapError(fmt.Errorf("%w: %w", rbac.ErrDominance, rbac.ErrUnknownRole), ""), 403, "dominance_required")
	// Both scope causes are reported, each once, naming no permission.
	both := mapError(fmt.Errorf("%w; %w", rbac.ErrBadScope, rbac.ErrUnknownScope), "")
	if he, _ := errors.AsType[*httpapi.Error](both); he == nil || len(he.Fields) != 2 || he.Fields[0].Field != "/body" {
		t.Errorf("both causes = %+v", both)
	}
	// The boot-time refusals and anything unknown pass through unchanged, so
	// httpapi answers a 500 and withholds the text.
	for _, e := range []error{rbac.ErrFrozen, rbac.ErrConfiningSeed, rbac.ErrUndecodableRole, errors.New("pebble: disk on fire")} {
		got := mapError(e, "")
		if _, ok := errors.AsType[*httpapi.Error](got); ok || !errors.Is(got, e) {
			t.Errorf("mapError(%v) = %v, want it unchanged", e, got)
		}
	}
}

// TestHandlers_ServiceRefusalsMap: each write maps what its service call
// refuses, passes the principal's checker as the actor, and sends the version
// the client named.
func TestHandlers_ServiceRefusalsMap(t *testing.T) {
	ctx := context.Background()
	_, svc := realAPI(t)
	p := svc.admin()
	title := "x"
	for _, c := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"in use", rbac.ErrRoleInUse, 409, "role_in_use"},
		{"last admin", rbac.ErrLastAdmin, 409, "last_admin"},
		{"escalation", rbac.ErrEscalation, 403, "scope_escalation"},
		{"stale", rbac.ErrVersionMismatch, 412, "precondition_failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRoles{err: c.err, roles: []*rbac.Role{{Name: "custom.oracle", Origin: rbac.OriginCustom, Version: 4}}}
			a, _ := fakeAPI(f)
			_, err := a.create(ctx, in(p, createReq{Body: RbacRoleCreate{Name: "custom.oracle", Title: "Oracle"}}))
			wantStatus(t, err, c.status, c.code)
			_, err = a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: "4", Body: RbacRolePatch{Title: &title}}))
			wantStatus(t, err, c.status, c.code)
			if f.version != 4 || f.actor != p.Perms {
				t.Errorf("update sent version %d and actor %v, want 4 and the principal's checker", f.version, f.actor)
			}
			f.actor = nil
			err = func() error {
				_, err := a.remove(ctx, in(p, versionReq{Name: "custom.oracle", IfMatch: "4"}))
				return err
			}()
			wantStatus(t, err, c.status, c.code)
			if f.actor != p.Perms {
				t.Error("remove did not pass the principal's checker")
			}
			_, err = a.acceptSeed(ctx, in(p, versionReq{Name: "gotham.detective", IfMatch: "4"}))
			wantStatus(t, err, c.status, c.code)
		})
	}
}

// TestDominanceIsAuditedWhereWritten: a dominance refusal is a 403
// dominance_required and the route records the denial (05-rbac §8,
// seams.d/F4-02.md), with the actor the middleware bound to the context or,
// absent one, built from the principal. An escalation refusal is the
// service's to audit, so the route records nothing for it.
func TestDominanceIsAuditedWhereWritten(t *testing.T) {
	_, svc := realAPI(t)
	p := svc.admin()
	p.ClientAddr = mustAddr(t, "192.0.2.7")
	p.CredentialID = "sess-batcave"

	f := &fakeRoles{err: fmt.Errorf("target holds more: %w", rbac.ErrDominance), roles: []*rbac.Role{{Name: "custom.oracle", Origin: rbac.OriginCustom, Version: 1}}}
	a, em := fakeAPI(f)
	_, err := a.remove(context.Background(), in(p, versionReq{Name: "custom.oracle", IfMatch: "1"}))
	wantStatus(t, err, http.StatusForbidden, "dominance_required")
	evs := em.all()
	if len(evs) != 1 {
		t.Fatalf("emitted %d events, want 1: %+v", len(evs), evs)
	}
	e := evs[0]
	if e.Action != "core.permission.deny" || e.Outcome != audit.Denied || e.Reason != "dominance_required" ||
		e.Permission != "core:roles.manage" || e.Route != "DELETE /api/core/v1/roles/{name}" ||
		e.Target.Kind != "role" || e.Target.ID != "custom.oracle" || e.Target.Module != "core" {
		t.Errorf("event = %+v", e)
	}
	if e.Actor.UserID != "u-bruce" || e.Actor.Username != "bruce" || e.Actor.Method != "session" || e.Actor.ClientAddr != "192.0.2.7" || e.Actor.CredentialID != "sess-batcave" {
		t.Errorf("actor from the principal = %+v", e.Actor)
	}

	// An actor already on the context is the one recorded.
	bound := audit.Actor{UserID: "u-dick", Username: "dick", Method: "session"}
	ctx := audit.WithActor(context.Background(), bound)
	if _, err := a.acceptSeed(ctx, in(p, versionReq{Name: "gotham.detective", IfMatch: "1"})); err == nil {
		t.Fatal("accept-seed succeeded")
	}
	if evs = em.all(); len(evs) != 2 || evs[1].Actor != bound || evs[1].Route != "POST /api/core/v1/roles/{name}/accept-seed" {
		t.Errorf("second event = %+v", evs)
	}

	// Escalation, alone or beside a dominance cause, is not recorded here.
	for _, e := range []error{rbac.ErrEscalation, fmt.Errorf("%w: %w", rbac.ErrDominance, rbac.ErrEscalation)} {
		f.err = e
		a, em = fakeAPI(f)
		if _, err := a.create(context.Background(), in(p, createReq{Body: RbacRoleCreate{Name: "custom.joker", Title: "Joker"}})); err == nil {
			t.Fatal("create succeeded")
		}
		if got := em.all(); len(got) != 0 {
			t.Errorf("route recorded an escalation: %+v", got)
		}
	}
	// No Emitter: the refusal still maps.
	f.err = rbac.ErrDominance
	a = &api{roles: f, log: discard(), base: "/api/core/v1"}
	_, err = a.create(context.Background(), in(p, createReq{Body: RbacRoleCreate{Name: "custom.joker", Title: "Joker"}}))
	wantStatus(t, err, http.StatusForbidden, "dominance_required")
}

// ---- PATCH merge, create, get, list over a fake ---------------------------

func TestUpdate_MergesOverTheStoredRole(t *testing.T) {
	ctx := context.Background()
	_, svc := realAPI(t)
	p := svc.admin()
	f := &fakeRoles{roles: []*rbac.Role{{
		Name: "custom.oracle", Title: "Oracle", Description: "Watches the wires", Origin: rbac.OriginCustom,
		Added: []rbac.Permission{"gotham:patrol", "gotham:signal"}, Version: 3,
	}}}
	a, _ := fakeAPI(f)

	desc := "Now with a cowl"
	if _, err := a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: "3", Body: RbacRolePatch{Description: &desc}})); err != nil {
		t.Fatal(err)
	}
	want := rbac.RoleInput{Name: "custom.oracle", Title: "Oracle", Description: desc, Permissions: []rbac.Permission{"gotham:patrol", "gotham:signal"}}
	if f.updated.Name != want.Name || f.updated.Title != want.Title || f.updated.Description != want.Description || !slices.Equal(f.updated.Permissions, want.Permissions) {
		t.Errorf("sent %+v, want %+v", f.updated, want)
	}

	empty, blank := []string{}, ""
	if _, err := a.update(ctx, in(p, patchReq{Name: "custom.oracle", IfMatch: "3", Body: RbacRolePatch{Permissions: &empty, Description: &blank}})); err != nil {
		t.Fatal(err)
	}
	if len(f.updated.Permissions) != 0 || f.updated.Description != "" || f.updated.Title != "Oracle" {
		t.Errorf("an empty permissions list and a blank description are values to store: %+v", f.updated)
	}
	if f.version != 3 {
		t.Errorf("version %d sent, want the client's 3", f.version)
	}

	// A name past any role's length is a 404 that does not echo it.
	long := strings.Repeat("x", maxNameLen+1)
	_, err := a.get(ctx, in(p, roleReq{Name: long}))
	wantStatus(t, err, http.StatusNotFound, "not_found")
	if strings.Contains(errBlob(t, err), "xxxx") {
		t.Errorf("404 echoes the name: %s", errBlob(t, err))
	}
	_, err = a.update(ctx, in(p, patchReq{Name: long, IfMatch: "1", Body: RbacRolePatch{Description: &desc}}))
	wantStatus(t, err, http.StatusNotFound, "not_found")
	_, err = a.remove(ctx, in(p, versionReq{Name: long, IfMatch: "1"}))
	wantStatus(t, err, http.StatusNotFound, "not_found")
	_, err = a.acceptSeed(ctx, in(p, versionReq{Name: long, IfMatch: "1"}))
	wantStatus(t, err, http.StatusNotFound, "not_found")
}

func TestCreate_LocationAndBody(t *testing.T) {
	_, svc := realAPI(t)
	f := &fakeRoles{}
	a, _ := fakeAPI(f)
	got, err := a.create(context.Background(), in(svc.admin(), createReq{Body: RbacRoleCreate{
		Name: "custom.a b", Title: "Gordon", Description: "GCPD", Permissions: []string{"gotham:patrol"},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Location != "/api/core/v1/roles/custom.a%20b" {
		t.Errorf("Location = %q", got.Location)
	}
	if f.created.Name != "custom.a b" || f.created.Title != "Gordon" || f.created.Description != "GCPD" || !slices.Equal(f.created.Permissions, []rbac.Permission{"gotham:patrol"}) {
		t.Errorf("service got %+v", f.created)
	}
	if got.Body.Permissions == nil || got.Body.PendingSeed == nil {
		t.Errorf("empty lists must be [] on the wire: %s", mustJSON(t, got.Body))
	}
}

func TestList_PagesAndSkipsUndecodable(t *testing.T) {
	ctx := context.Background()
	_, svc := realAPI(t)
	p := svc.admin()
	var roles []*rbac.Role
	for _, n := range []string{"custom.a", "custom.b", "custom.c", "custom.d", "custom.e"} {
		roles = append(roles, &rbac.Role{Name: rbac.RoleName(n), Origin: rbac.OriginCustom, Version: 1})
	}
	var logs bytes.Buffer
	f := &fakeRoles{rows: func(yield func(*rbac.Role, error) bool) {
		for i, r := range roles {
			if i == 2 && !yield(nil, fmt.Errorf("%w: key gotham/roles/secret-row-7", rbac.ErrUndecodableRole)) {
				return
			}
			if !yield(r, nil) {
				return
			}
		}
	}}
	a := &api{roles: f, log: slog.New(slog.NewTextHandler(&logs, nil)), base: "/api/core/v1"}

	got, err := a.list(ctx, in(p, listReq{Limit: 2, Offset: 3}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 5 || got.Count != 2 || got.Limit != 2 || got.Offset != 3 || got.Items[0].Name != "custom.d" || got.Items[1].Name != "custom.e" {
		t.Errorf("page = %+v", got)
	}
	if !strings.Contains(logs.String(), "cannot be decoded") {
		t.Errorf("the skipped row was not logged: %q", logs.String())
	}
	// Past the end: an empty page, items [] not null.
	got, err = a.list(ctx, in(p, listReq{Limit: 10, Offset: 99}))
	if err != nil || got.Items == nil || len(got.Items) != 0 || got.Total != 5 {
		t.Errorf("past the end = %+v, %v", got, err)
	}
	// A negative offset or an oversize limit cannot panic a slice.
	if _, err := a.list(ctx, in(p, listReq{Limit: 1 << 30, Offset: -4})); err != nil {
		t.Errorf("unnormalised params: %v", err)
	}

	// Any other iteration failure is a 503 that carries nothing of the cause.
	logs.Reset()
	f.rows = func(yield func(*rbac.Role, error) bool) {
		yield(nil, errors.New("pebble: corrupt block at /var/lib/gotham"))
	}
	_, err = a.list(ctx, in(p, listReq{Limit: 50}))
	wantStatus(t, err, http.StatusServiceUnavailable, "unavailable")
	if strings.Contains(errBlob(t, err), "pebble") || strings.Contains(errBlob(t, err), "/var/lib") {
		t.Errorf("503 carries the cause: %s", errBlob(t, err))
	}
	if !strings.Contains(logs.String(), "corrupt block") {
		t.Errorf("the cause was not logged: %q", logs.String())
	}
}

func TestView_UnknownOriginHoldsNothing(t *testing.T) {
	got := view(&rbac.Role{Name: "custom.ghost", Origin: rbac.Origin(99), Added: []rbac.Permission{"gotham:patrol"}, SeedDigest: "d1g3st", Version: 2})
	if got.Origin != "unknown" || len(got.Permissions) != 0 || got.PendingSeed == nil || got.Permissions == nil {
		t.Errorf("view = %+v", got)
	}
	if strings.Contains(mustJSON(t, got), "d1g3st") {
		t.Error("seed bookkeeping reached the wire")
	}
}

// ---- request validation ---------------------------------------------------

func TestCreateReq_Validate(t *testing.T) {
	long := strings.Repeat("a", maxNameLen+1)
	for _, c := range []struct {
		name string
		body RbacRoleCreate
		want []string // JSON pointers, in order
	}{
		{"ok", RbacRoleCreate{Name: "custom.a", Title: "A", Permissions: []string{"gotham:patrol"}}, nil},
		{"nothing", RbacRoleCreate{}, []string{"/body/name", "/body/title"}},
		{"blank", RbacRoleCreate{Name: "  ", Title: "\t"}, []string{"/body/name", "/body/title"}},
		{"long name", RbacRoleCreate{Name: long, Title: "A"}, []string{"/body/name"}},
		{"long title", RbacRoleCreate{Name: "custom.a", Title: strings.Repeat("é", maxTitleRunes+1)}, []string{"/body/title"}},
		{"title at the bound in runes", RbacRoleCreate{Name: "custom.a", Title: strings.Repeat("é", maxTitleRunes)}, nil},
		{"long description", RbacRoleCreate{Name: "custom.a", Title: "A", Description: strings.Repeat("d", maxDescriptionRunes+1)}, []string{"/body/description"}},
		{"too many", RbacRoleCreate{Name: "custom.a", Title: "A", Permissions: make([]string, maxPermissions+1)}, []string{"/body/permissions"}},
		{"empty entry", RbacRoleCreate{Name: "custom.a", Title: "A", Permissions: []string{"gotham:patrol", ""}}, []string{"/body/permissions/1"}},
		{"long entry", RbacRoleCreate{Name: "custom.a", Title: "A", Permissions: []string{strings.Repeat("p", maxPermissionLen+1)}}, []string{"/body/permissions/0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := createReq{Body: c.body}
			checkPointers(t, r.Validate(), c.want)
		})
	}
}

func TestPatchReq_Validate(t *testing.T) {
	blank, ok := " ", "Oracle"
	long := strings.Repeat("d", maxDescriptionRunes+1)
	perms := []string{"gotham:patrol"}
	badPerms := []string{""}
	none := []string{}
	for _, c := range []struct {
		name string
		body RbacRolePatch
		want []string
	}{
		{"title", RbacRolePatch{Title: &ok}, nil},
		{"permissions", RbacRolePatch{Permissions: &perms}, nil},
		{"empty list is a value", RbacRolePatch{Permissions: &none}, nil},
		{"nothing", RbacRolePatch{}, []string{"/body"}},
		{"blank title", RbacRolePatch{Title: &blank}, []string{"/body/title"}},
		{"long description", RbacRolePatch{Description: &long}, []string{"/body/description"}},
		{"bad entry", RbacRolePatch{Permissions: &badPerms}, []string{"/body/permissions/0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := patchReq{Body: c.body}
			checkPointers(t, r.Validate(), c.want)
		})
	}
}

// checkPointers fails unless err is a 422 whose field pointers are want (nil:
// err must be nil).
func checkPointers(t testing.TB, err error, want []string) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Errorf("Validate = %v, want nil", err)
		}
		return
	}
	wantStatus(t, err, http.StatusUnprocessableEntity, "validation_failed")
	he, _ := errors.AsType[*httpapi.Error](err)
	if he == nil {
		return
	}
	var got []string
	for _, f := range he.Fields {
		got = append(got, f.Field)
	}
	if !slices.Equal(got, want) {
		t.Errorf("field pointers = %v, want %v", got, want)
	}
}
