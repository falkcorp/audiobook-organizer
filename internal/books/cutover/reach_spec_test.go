// file: internal/books/cutover/reach_spec_test.go
// version: 1.0.0
// guid: 5e2b8d94-3a17-4c60-b8f1-9d4a6c0e7b25
// last-edited: 2026-11-13

package cutover

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/rbac"
)

// What the reach oracle (F4-07) takes as the cutover's identity vocabulary:
// the native scopes of 05-rbac section 6.1 and the seed roles of section 4.8,
// written out from the spec rather than borrowed from F4-06's rig fixtures.
//
// Books production declares no books:* catalog and no DefaultGrants until
// F4-08, so the oracle states them. The rig's own idSeedRoles gives the editor
// books:collections.manage and no books:delete or books:request.approve, and
// idCatalog leaves books:admin.debug neither AuthRequired nor Sensitive and
// declares no implication; any of those differs from R0's reach for a reason
// that is the fixture's, not the atom table's. TestReachSpec_* pins this
// vocabulary to the spec's text so a drift here is a test failure of its own.

// reachSeedRoles are HEAD's four seed roles with the native sets 05-rbac
// 4.8's table gives them: core's seed plus books' DefaultGrants.
func reachSeedRoles() []m3.SeedRole {
	core := []rbac.Permission{"core:self", "core:tasks.view", "core:system.status", "core:metrics.read"}
	return []m3.SeedRole{
		{ID: "admin", Permissions: []rbac.Permission{rbac.CoreAdmin}},
		{ID: "editor", Permissions: append(slices.Clone(core),
			"books:view", "books:edit", "books:delete", "books:organize", "books:scan",
			"books:playlists.create", "books:request", "books:request.approve", "books:reading")},
		{ID: "viewer", Permissions: append(slices.Clone(core), "books:view", "books:request", "books:reading")},
		{ID: "fp-worker", Permissions: []rbac.Permission{"books:fingerprint.worker"}},
	}
}

// reachAmbient are the scopes a key that is not confined gains besides its
// effective set (05-rbac 6.3).
func reachAmbient() []rbac.Permission {
	return []rbac.Permission{"core:self", "core:metrics.read", "books:reading"}
}

// reachCatalog is core's scopes and books' as 05-rbac 6.1 declares them:
// the implications of its table, books:admin.debug AuthRequired and Sensitive,
// the read classes, and books:fingerprint.worker the one Confining scope.
func reachCatalog(t testing.TB) *rbac.Catalog {
	t.Helper()
	books := func(name string, mod func(*rbac.PermissionSpec)) rbac.PermissionSpec {
		s := rbac.PermissionSpec{Name: rbac.Permission("books:" + name), Title: name, Description: "books " + name}
		if mod != nil {
			mod(&s)
		}
		return s
	}
	view := func(s *rbac.PermissionSpec) { s.Class = rbac.ClassView }
	impliesView := func(s *rbac.PermissionSpec) { s.Implies = []rbac.Permission{"books:view"} }
	specs := append(rbac.CoreScopes(),
		books("view", view),
		books("edit", impliesView),
		books("delete", impliesView),
		books("organize", impliesView),
		books("scan", nil),
		books("integrations.manage", nil),
		books("settings.manage", nil),
		books("playlists.create", nil),
		books("collections.manage", nil),
		books("request", nil),
		books("request.approve", func(s *rbac.PermissionSpec) { s.Implies = []rbac.Permission{"books:request"} }),
		books("reading", nil),
		books("admin.view", view),
		books("admin.debug", func(s *rbac.PermissionSpec) { s.AuthRequired, s.Sensitive = true, true }),
		books("fingerprint.worker", func(s *rbac.PermissionSpec) { s.Confining = true }),
	)
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestReachSpec_SeedRolesAreTheSpecsTable(t *testing.T) {
	roles := map[string][]rbac.Permission{}
	for _, r := range reachSeedRoles() {
		roles[r.ID] = r.Permissions
	}
	// 05-rbac 4.8's table, row by row.
	want := map[string][]rbac.Permission{
		"admin": {"core:admin"},
		"editor": {"core:self", "core:tasks.view", "core:system.status", "core:metrics.read",
			"books:view", "books:edit", "books:delete", "books:organize", "books:scan",
			"books:playlists.create", "books:request", "books:request.approve", "books:reading"},
		"viewer": {"core:self", "core:tasks.view", "core:system.status", "core:metrics.read",
			"books:view", "books:request", "books:reading"},
		"fp-worker": {"books:fingerprint.worker"},
	}
	for id, w := range want {
		got := slices.Clone(roles[id])
		slices.Sort(got)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("seed role %s: got %v, want %v (05-rbac 4.8)", id, got, w)
		}
	}
	if len(roles) != len(want) {
		t.Errorf("seed roles: got %d, want %d", len(roles), len(want))
	}
}

func TestReachSpec_CatalogFlagsAreTheSpecs(t *testing.T) {
	cat := reachCatalog(t)
	spec := func(n rbac.Permission) rbac.PermissionSpec {
		s, ok := cat.Lookup(n)
		if !ok {
			t.Fatalf("%s is not in the catalog", n)
		}
		return s
	}
	if d := spec("books:admin.debug"); !d.AuthRequired || !d.Sensitive {
		t.Error("books:admin.debug must be AuthRequired and Sensitive (05-rbac 6.2: 401 with auth off)")
	}
	if !spec("books:fingerprint.worker").Confining {
		t.Error("books:fingerprint.worker is the one Confining scope")
	}
	for _, n := range []rbac.Permission{"books:edit", "books:delete", "books:organize"} {
		if !slices.Contains(spec(n).Implies, "books:view") {
			t.Errorf("%s must imply books:view (05-rbac 6.1)", n)
		}
	}
	if !slices.Contains(spec("books:request.approve").Implies, "books:request") {
		t.Error("books:request.approve must imply books:request (05-rbac 6.1)")
	}
}
