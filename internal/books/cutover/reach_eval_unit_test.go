// file: internal/books/cutover/reach_eval_unit_test.go
// version: 1.1.0
// guid: 7e3a9c15-2b84-4d70-a6f1-9c0d5b8e2a47
// last-edited: 2026-11-13

package cutover

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The pieces of the reach oracle that need no store: the evaluator's rules
// (05-rbac section 4), the atom-table resolution of a route, and the diff.

func reachSeedPerms(t *testing.T, id string) []rbac.Permission {
	t.Helper()
	for _, s := range reachSeedRoles() {
		if s.ID == id {
			return s.Permissions
		}
	}
	t.Fatalf("no seed role %q", id)
	return nil
}

func reachSession(t *testing.T, cat *rbac.Catalog, role string) *reachChecker {
	t.Helper()
	return reachCompile(cat, reachSeedPerms(t, role), nil, true)
}

func reachKey(t *testing.T, cat *rbac.Catalog, role string, scopes ...rbac.Permission) *reachChecker {
	t.Helper()
	return reachCompile(cat, reachSeedPerms(t, role), scopes, false)
}

func TestReachEval_SeedSessions(t *testing.T) {
	cat := reachCatalog(t)
	editor, viewer, admin := reachSession(t, cat, "editor"), reachSession(t, cat, "viewer"), reachSession(t, cat, "admin")
	for _, tc := range []struct {
		who  string
		chk  *reachChecker
		req  rbac.Requirement
		want bool
	}{
		{"editor", editor, rbac.Need("books:edit"), true},
		{"editor", editor, rbac.Need("books:playlists.create"), true},
		{"editor", editor, rbac.Need("core:settings.manage"), false},
		{"editor", editor, rbac.Need("core:users.manage"), false},
		{"editor", editor, rbac.Need("core:system.status"), true},
		{"viewer", viewer, rbac.Need("books:view"), true},
		{"viewer", viewer, rbac.Need("books:edit"), false},
		{"viewer", viewer, rbac.Need("core:system.status"), true},
		{"admin", admin, rbac.Need("core:settings.manage"), true},
		{"admin", admin, rbac.Need("core:roles.manage"), true}, // Sensitive, and the session is interactive
		{"admin", admin, rbac.AllOf("core:users.manage", "core:roles.view"), true},
		{"editor", editor, rbac.AnyOf("core:settings.manage", "books:edit"), true},
		{"editor", editor, rbac.AnyOf("core:settings.manage", "core:users.manage"), false},
		{"editor", editor, rbac.AnyAuthenticated, true},
		{"editor", editor, rbac.Public, true},
	} {
		if got := tc.chk.satisfies(tc.req); got != tc.want {
			t.Errorf("%s session %s: got %v, want %v", tc.who, tc.req, got, tc.want)
		}
	}
	if editor.satisfies(rbac.Delegated("books.abs")) || editor.satisfies(rbac.Requirement{}) || editor.satisfies(rbac.LegacyAtom("library.view")) {
		t.Error("a delegated, zero or unresolved legacy requirement is never satisfied by the checker")
	}
}

// A key holds the intersection of its owner's roles and its scopes; an admin
// holds whatever a key names, and core:admin only when the key names it.
func TestReachEval_KeyIsTheIntersection(t *testing.T) {
	cat := reachCatalog(t)
	editorEdit := reachKey(t, cat, "editor", "books:edit", "core:settings.manage")
	if !editorEdit.satisfies(rbac.Need("books:edit")) {
		t.Error("an editor-owned key naming books:edit holds it")
	}
	if editorEdit.satisfies(rbac.Need("core:settings.manage")) {
		t.Error("an editor-owned key cannot hold a scope its owner's roles lack")
	}
	if editorEdit.satisfies(rbac.Need("books:organize")) {
		t.Error("a key holds only the scopes it names")
	}
	adminSettings := reachKey(t, cat, "admin", "core:settings.manage")
	if !adminSettings.satisfies(rbac.Need("core:settings.manage")) || adminSettings.satisfies(rbac.Need("core:users.manage")) {
		t.Error("an admin-owned key holds what it names and nothing else")
	}
	adminAll := reachKey(t, cat, "admin", rbac.CoreAdmin, "core:settings.manage")
	if !adminAll.satisfies(rbac.Need("core:users.manage")) {
		t.Error("an admin-owned key that names core:admin holds every scope")
	}
	if adminAll.satisfies(rbac.Need("core:roles.manage")) {
		t.Error("a Sensitive scope is refused to a non-interactive principal, core:admin included")
	}
}

func TestReachEval_ConfinedWorker(t *testing.T) {
	cat := reachCatalog(t)
	worker := reachSession(t, cat, "fp-worker")
	if !worker.confined {
		t.Fatal("fp-worker holds only Confining scopes")
	}
	if !worker.satisfies(rbac.Need("books:fingerprint.worker")) {
		t.Error("a confined principal satisfies a requirement naming its scope")
	}
	if worker.satisfies(rbac.AnyAuthenticated) {
		t.Error("a confined principal is never merely authenticated")
	}
	if worker.satisfies(rbac.Need("books:view")) || worker.satisfies(rbac.Need("core:self")) {
		t.Error("a confined principal satisfies nothing it does not name")
	}
	key := reachKey(t, cat, "fp-worker", "books:fingerprint.worker")
	if !key.confined || !key.satisfies(rbac.Need("books:fingerprint.worker")) {
		t.Error("a worker's key is confined and holds the worker scope")
	}
}

// §4.11: the auth-disabled principal holds every concrete scope that is neither
// AuthRequired nor Sensitive, never core:admin, and an AllOf drops its
// AuthRequired members before the test.
func TestReachEval_AuthDisabled(t *testing.T) {
	cat := reachCatalog(t)
	off := reachAuthDisabled(cat)
	for _, s := range []rbac.Permission{rbac.CoreAdmin, "core:audit.view", "core:system.manage", "core:backup.restore", "core:roles.manage"} {
		if off.has(s) {
			t.Errorf("the auth-disabled principal holds %s", s)
		}
	}
	for _, s := range []rbac.Permission{"core:settings.manage", "core:users.manage", "books:edit", "core:self"} {
		if !off.has(s) {
			t.Errorf("the auth-disabled principal lacks %s", s)
		}
	}
	if off.satisfies(rbac.Need("core:system.manage")) {
		t.Error("an AllOf made only of AuthRequired scopes is refused")
	}
	if !off.satisfies(rbac.AllOf("core:system.manage", "core:settings.manage")) {
		t.Error("an AllOf drops its AuthRequired members and tests the rest")
	}
	if off.satisfies(rbac.AnyOf("core:system.manage", "core:audit.view")) {
		t.Error("an AnyOf does not drop members: the auth-disabled principal holds neither")
	}
}

func TestReachResolve(t *testing.T) {
	atoms := idAtoms()
	route := func(atom string) httpapi.LegacyRoute {
		return httpapi.LegacyRoute{Method: "GET", GinPath: "/x", Perm: rbac.LegacyAtom(atom)}
	}
	g, err := reachResolve(atoms, route("settings.manage"))
	if err != nil {
		t.Fatal(err)
	}
	want := rbac.AllOf(atomScopes(t, atoms, "settings.manage")...)
	if !slices.Equal(g.req.Permissions(), want.Permissions()) || g.req.Kind() != rbac.KindAllOf {
		t.Errorf("settings.manage resolved to %s, want AllOf of its %d scopes", g.req, len(want.Permissions()))
	}
	if _, err := reachResolve(atoms, route("playlists.create")); err == nil {
		t.Error("a route naming playlists.create must be an error: no route enforces it")
	}
	if _, err := reachResolve(atoms, route("batcave.access")); err == nil {
		t.Error("an atom the table lacks must be an error")
	}
	if g, err = reachResolve(atoms, httpapi.LegacyRoute{Method: "GET", GinPath: "/p", Perm: rbac.Public}); err != nil || g.delegated {
		t.Errorf("a public route resolves as itself: %v %v", g, err)
	}
	if g, err = reachResolve(atoms, httpapi.LegacyRoute{Method: "GET", GinPath: "/a", Perm: rbac.Delegated("books.abs"), OwnAuth: true}); err != nil || !g.delegated {
		t.Errorf("an OwnAuth route is delegated: %v %v", g, err)
	}
	if _, err := reachResolve(atoms, httpapi.LegacyRoute{Method: "GET", GinPath: "/z"}); err == nil {
		t.Error("a route with no requirement must be an error")
	}
}

func atomScopes(t *testing.T, atoms []m3.AtomRow, name string) []rbac.Permission {
	t.Helper()
	for _, a := range atoms {
		if a.Name == name {
			return a.Scopes
		}
	}
	t.Fatalf("no atom %q", name)
	return nil
}

// reachWhosOf builds principals over the seed roles for the evaluator tests.
func reachWhosOf(t *testing.T, cat *rbac.Catalog) []reachWho {
	t.Helper()
	return []reachWho{
		{name: "session:editor", chk: reachSession(t, cat, "editor")},
		{name: "session:viewer", chk: reachSession(t, cat, "viewer")},
		{name: "key:viewer:view", chk: reachKey(t, cat, "viewer", "books:view")},
		{name: "anonymous"},
		{name: "authoff", chk: reachAuthDisabled(cat)},
	}
}

func TestReachEvaluate(t *testing.T) {
	cat := reachCatalog(t)
	whos := reachWhosOf(t, cat)
	routes := []httpapi.LegacyRoute{
		{Method: "PUT", GinPath: "/edit", Perm: rbac.LegacyAtom("library.edit_metadata")},
		{Method: "GET", GinPath: "/health", Perm: rbac.Public},
		{Method: "GET", GinPath: "/me", Perm: rbac.AnyAuthenticated},
		{Method: "POST", GinPath: "/creds", Perm: rbac.AnyAuthenticated, Guards: httpapi.Guards{Interactive: true}},
		{Method: "GET", GinPath: "/abs", Perm: rbac.Delegated("books.abs"), OwnAuth: true},
	}
	got, err := reachEvaluate(idAtoms(), routes, whos)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"PUT /edit":   "ADDDA", // auth-off holds every scope that is neither AuthRequired nor Sensitive
		"GET /health": "AAAAA",
		"GET /me":     "AAADA",
		"POST /creds": "AADDD", // a key and auth-off never pass the interactive guard
	} {
		if v := got.verdict[k]; v != want {
			t.Errorf("%s: %q, want %q", k, v, want)
		}
	}
	if _, ok := got.verdict["GET /abs"]; ok {
		t.Error("a delegated route has no verdict: the ABS handler enforces it")
	}
	if !slices.Equal(got.delegated, []string{"GET /abs"}) {
		t.Errorf("delegated: %v", got.delegated)
	}
}

// The atom table is a parameter: changing one row changes the verdicts of the
// routes that atom gates, and of no other.
func TestReachEvaluate_ChangedAtomRowChangesVerdicts(t *testing.T) {
	cat := reachCatalog(t)
	whos := reachWhosOf(t, cat)
	routes := []httpapi.LegacyRoute{
		{Method: "PUT", GinPath: "/edit", Perm: rbac.LegacyAtom("library.edit_metadata")},
		{Method: "GET", GinPath: "/view", Perm: rbac.LegacyAtom("library.view")},
	}
	base, err := reachEvaluate(idAtoms(), routes, whos)
	if err != nil {
		t.Fatal(err)
	}
	mutated := idAtoms()
	for i := range mutated {
		if mutated[i].Name == "library.edit_metadata" {
			mutated[i].Scopes = []rbac.Permission{"books:admin.view"}
		}
	}
	got, err := reachEvaluate(mutated, routes, whos)
	if err != nil {
		t.Fatal(err)
	}
	if got.verdict["GET /view"] != base.verdict["GET /view"] {
		t.Error("a change to one atom row moved another atom's route")
	}
	if got.verdict["PUT /edit"] == base.verdict["PUT /edit"] {
		t.Errorf("flipping library.edit_metadata to books:admin.view (an admin-only scope) left PUT /edit at %s", got.verdict["PUT /edit"])
	}
}

func TestReachDiff(t *testing.T) {
	r0 := &reachR0Doc{
		Principals: []string{"p1", "p2"},
		Routes: []reachR0Route{
			{Method: "GET", Path: "/same", Verdict: "AD"},
			{Method: "GET", Path: "/differs", Verdict: "AD"},
			{Method: "GET", Path: "/r0only", Verdict: "AA"},
			{Method: "GET", Path: "/abs", Verdict: "AA"},
		},
	}
	r2 := &reachR2{
		verdict:   map[string]string{"GET /same": "AD", "GET /differs": "DD", "GET /r2only": "AA"},
		delegated: []string{"GET /abs"},
	}
	d := reachDiff(r0, r2)
	if d.compared != 4 {
		t.Errorf("compared %d cells, want 4 (two routes, two principals)", d.compared)
	}
	if len(d.cells) != 1 || d.cells[0].String() != "GET /differs / p1: R0 A, R2 D" {
		t.Errorf("cells: %v", d.cells)
	}
	if !slices.Equal(d.r0Only, []string{"GET /r0only"}) || !slices.Equal(d.r2Only, []string{"GET /r2only"}) {
		t.Errorf("r0-only %v, r2-only %v", d.r0Only, d.r2Only)
	}
	if !slices.Equal(d.delegated, []string{"GET /abs"}) {
		t.Errorf("delegated: %v", d.delegated)
	}
}
