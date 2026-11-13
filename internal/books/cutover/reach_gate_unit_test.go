// file: internal/books/cutover/reach_gate_unit_test.go
// version: 1.0.0
// guid: 5a8c2e71-0d36-4f94-b7a3-c1e9d6f20b58
// last-edited: 2026-11-13

package cutover

import (
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The classifier that holds the oracle to its expected rows, and the two stage
// 13 rules this file's oracle adds (the owner guard, the task start).

func TestReachClassify_ARowAssertsBothLetters(t *testing.T) {
	rows := []reachExpect{{
		name: "row", route: routeIs("GET /a"), who: whoIs("p1"), r0: 'A', r2: 'D',
	}}
	for _, tc := range []struct {
		name           string
		cell           reachCell
		wantUnexpected bool
	}{
		{"as asserted", reachCell{"GET /a", "p1", 'A', 'D'}, false},
		{"moved the other way", reachCell{"GET /a", "p1", 'D', 'A'}, true},
		{"another route", reachCell{"GET /b", "p1", 'A', 'D'}, true},
		{"another principal", reachCell{"GET /a", "p2", 'A', 'D'}, true},
	} {
		got := reachClassify([]reachCell{tc.cell}, rows)
		if (len(got.unexpected) == 1) != tc.wantUnexpected {
			t.Errorf("%s: unexpected %v, want unexpected=%v", tc.name, got.unexpected, tc.wantUnexpected)
		}
	}
}

func TestReachClassify_ARowThatMatchesNothingIsStale(t *testing.T) {
	rows := []reachExpect{{name: "stale row", route: routeIs("GET /gone"), who: anyWho, r0: 'A', r2: 'D'}}
	res := reachDiffResult{cells: []reachCell{{"GET /a", "p1", 'A', 'D'}}}
	problems := reachGateProblems(res, rows)
	if len(problems) != 2 {
		t.Fatalf("want an unexpected-cell problem and a stale-row problem, got %q", problems)
	}
	if !strings.Contains(problems[1], "stale row") {
		t.Errorf("the stale row is not named: %q", problems[1])
	}
}

func TestReachClassify_AnUnclaimedCellIsReportedAsStop2(t *testing.T) {
	msg := reportUnexpected([]reachCell{{"GET /a", "p1", 'A', 'D'}, {"GET /a", "p2", 'D', 'A'}, {"GET /b", "p1", 'A', 'D'}})
	for _, want := range []string{"3 cell(s) on 2 route(s)", "STOP 2", "GET /a", "p1 A>D, p2 D>A"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report lacks %q:\n%s", want, msg)
		}
	}
	if got := reachGateProblems(reachDiffResult{}, nil); len(got) != 0 {
		t.Errorf("a clean diff has problems: %q", got)
	}
}

// Guards.Owner with no OwnerVerifier falls back to Interactive plus core:admin
// (05-authn 4.8): a session of an admin passes; a role holding every atom does
// not, nor does any key, nor the auth-disabled principal.
func TestReachGate_OwnerGuardFallsBackToInteractiveAdmin(t *testing.T) {
	cat := reachCatalog(t)
	custom := reachCompile(cat, []rbac.Permission{"books:view", "books:edit", "core:users.manage"}, nil, true)
	whos := []reachWho{
		{name: "session:admin", chk: reachSession(t, cat, "admin")},
		{name: "session:custom", chk: custom},
		{name: "key:admin:every", chk: reachKey(t, cat, "admin", "core:admin")},
		{name: "authoff", chk: reachAuthDisabled(cat)},
	}
	routes := []httpapi.LegacyRoute{
		{Method: "POST", GinPath: "/merge", Perm: rbac.Need("core:users.manage"), Guards: httpapi.Guards{Owner: true}},
		{Method: "POST", GinPath: "/plain", Perm: rbac.Need("core:users.manage")},
	}
	got, err := reachEvaluate(idAtoms(), routes, whos)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ADDD"; got.verdict["POST /merge"] != want {
		t.Errorf("owner-guarded route: %q, want %q", got.verdict["POST /merge"], want)
	}
	if want := "AAAA"; got.verdict["POST /plain"] != want {
		t.Errorf("the same route without the guard: %q, want %q (without the guard the custom role, the key and auth-off all pass)", got.verdict["POST /plain"], want)
	}
}

// A task start is the scan.trigger floor on the start route and then the
// definition's own permissions; an approval is the route, the approver
// permission and a verified method.
func TestReachTaskVerdicts_FloorThenDefinitionThenApprover(t *testing.T) {
	cat := reachCatalog(t)
	whos := []reachWho{
		{name: "session:editor", chk: reachSession(t, cat, "editor")},
		{name: "session:viewer", chk: reachSession(t, cat, "viewer")},
		{name: "session:admin", chk: reachSession(t, cat, "admin")},
		{name: "authoff", chk: reachAuthDisabled(cat)},
	}
	atoms := idAtoms()
	routes := []httpapi.LegacyRoute{
		{Method: "POST", GinPath: "/api/v3/ops/defs/:id/runs", Perm: rbac.LegacyAtom("scan.trigger")},
		{Method: "POST", GinPath: "/api/v3/ops/runs/:id/approve", Perm: rbac.LegacyAtom("library.view")},
	}
	vd := &reachR0Doc{
		Tasks: []reachR0Task{
			{ID: "edit.def", Permissions: []string{"library.edit_metadata"}, Approver: "settings.manage", Verdict: "AAAA"},
			{ID: "view.def", Permissions: []string{"library.view"}, Approver: "settings.manage", Verdict: "AAAA"},
			{ID: "settings.def", Permissions: []string{"settings.manage"}, Approver: "settings.manage", Verdict: "AAAA"},
		},
		Approvals: []reachR0Approval{{RunID: "r1", Run: map[string]any{"def_id": "settings.def"}, Verdict: "AAAA"}},
	}
	got, err := reachTaskVerdicts(atoms, routes, vd, whos)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"TASK-START edit.def":       "ADAA", // the editor holds books:scan and books:edit, the viewer neither; auth-off holds every scope that is not AuthRequired
		"TASK-START view.def":       "ADAA", // the viewer holds books:view and not books:scan: only the floor refuses it
		"TASK-START settings.def":   "DDAA", // the editor lacks settings.manage's scopes; auth-off drops the AuthRequired ones and holds the rest
		"TASK-APPROVE settings.def": "DDAD", // approver settings.manage; the auth-disabled principal has no user to record
	} {
		if got[k] != want {
			t.Errorf("%s: %q, want %q", k, got[k], want)
		}
	}
}

// The three routes whose handler checks core:admin (reachHandlerAdmin) refuse a
// role that holds every atom without it, a key that does not name it and the
// auth-disabled principal; an admin session passes. The same route without the
// pin would admit them all (its route-level gate is users.manage).
func TestReachGate_HandlerAdminPinRefusesWhoLacksCoreAdmin(t *testing.T) {
	cat := reachCatalog(t)
	custom := reachCompile(cat, []rbac.Permission{"core:users.manage", "core:roles.view", "books:admin.view"}, nil, true)
	whos := []reachWho{
		{name: "session:admin", chk: reachSession(t, cat, "admin")},
		{name: "session:custom-all", chk: custom},
		{name: "key:admin:users", chk: reachKey(t, cat, "admin", "core:users.manage")},
		{name: "authoff", chk: reachAuthDisabled(cat)},
	}
	pinned := httpapi.LegacyRoute{Method: "GET", GinPath: "/api/v1/users/merges", Perm: rbac.Need("core:users.manage")}
	unpinned := httpapi.LegacyRoute{Method: "GET", GinPath: "/api/v1/users/other", Perm: rbac.Need("core:users.manage")}
	got, err := reachEvaluate(idAtoms(), []httpapi.LegacyRoute{pinned, unpinned}, whos)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ADDD"; got.verdict["GET /api/v1/users/merges"] != want {
		t.Errorf("pinned route: %q, want %q", got.verdict["GET /api/v1/users/merges"], want)
	}
	if want := "AAAA"; got.verdict["GET /api/v1/users/other"] != want {
		t.Errorf("the same requirement unpinned: %q, want %q", got.verdict["GET /api/v1/users/other"], want)
	}
	for _, k := range []string{"GET /api/v1/users/:id/footprint", "GET /api/v1/users/merges", "PUT /api/v1/users/:id/email"} {
		if reachHandlerAdmin[k] == "" {
			t.Errorf("%s is no longer pinned as a handler-level core:admin check", k)
		}
	}
	if len(reachHandlerAdmin) != 3 {
		t.Errorf("reachHandlerAdmin pins %d routes, want the three the spec names", len(reachHandlerAdmin))
	}
}
