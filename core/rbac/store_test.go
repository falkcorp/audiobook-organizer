// file: core/rbac/store_test.go
// version: 1.1.0
// guid: 2a7d4e19-6f3b-4c80-b5e2-8d1c9f4a7b63
// last-edited: 2026-11-10

package rbac_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/rbac/rbactest"
	"github.com/falkcorp/mediacore/storage"
)

// nativeHarness runs rbactest's cases over core/internal/rbacstore.
func nativeHarness() rbactest.Harness {
	return rbactest.Harness{New: func(t *testing.T, cat *rbac.Catalog) rbactest.Fixture {
		e := newEnv(t)
		st := e.store(t, cat)
		return rbactest.Fixture{
			Store: st, Importer: st, NewBatch: st.NewBatch,
			Corrupt: func(t *testing.T, name rbac.RoleName) { rawPut(t, e, name, []byte("not a role")) },
		}
	}}
}

// TestNativeStore_RoundTrip (R4-1): the native store passes the conformance
// suite, the importer's cases included.
func TestNativeStore_RoundTrip(t *testing.T) {
	rbactest.StoreSuite(t, nativeHarness())
}

// TestImporter_RejectsBrokenRole (§6.3): the cutover's importer refuses a
// seed name, a non-custom origin, seed bookkeeping, an unknown scope, any
// pattern that covers core:admin (core:admin, core:*, core:admin.*) and an
// empty set, staging nothing; an accepted import is a custom role the next
// Freeze loads.
func TestImporter_RejectsBrokenRole(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	f := boot(t, e, setup{modules: []module{gotham()}})
	imp := e.store(t, f.cat())
	for name, r := range map[string]*rbac.Role{
		"core seed name":      {Name: rbac.RoleEditor, Origin: rbac.OriginCustom, Added: perms("gotham:patrol")},
		"admin name":          {Name: rbac.RoleAdmin, Origin: rbac.OriginCustom, Added: perms("core:admin")},
		"module seed name":    {Name: "gotham.detective", Origin: rbac.OriginCustom, Added: perms("gotham:patrol")},
		"imported seed name":  {Name: "relay-worker", Origin: rbac.OriginCustom, Added: perms("gotham:relay.worker")},
		"seed origin":         {Name: "custom.alfred", Origin: rbac.OriginSeed, SeedPermissions: perms("gotham:patrol")},
		"seed bookkeeping":    {Name: "custom.alfred", Origin: rbac.OriginCustom, Added: perms("gotham:patrol"), PendingSeed: perms("gotham:signal")},
		"locked":              {Name: "custom.alfred", Origin: rbac.OriginCustom, Added: perms("gotham:patrol"), Locked: true},
		"unknown scope":       {Name: "custom.alfred", Origin: rbac.OriginCustom, Added: perms("gotham:patrol", "gotham:ghost")},
		"malformed scope":     {Name: "custom.alfred", Origin: rbac.OriginCustom, Added: perms("gotham:Patrol")},
		"core admin":          {Name: "custom.alfred", Origin: rbac.OriginCustom, Added: perms("core:admin")},
		"core wildcard":       {Name: "custom.alfred", Origin: rbac.OriginCustom, Added: perms("core:*")},
		"core admin wildcard": {Name: "custom.alfred", Origin: rbac.OriginCustom, Added: perms("gotham:patrol", "core:admin.*")},
		"empty set":           {Name: "custom.alfred", Origin: rbac.OriginCustom},
		"no origin":           {Name: "custom.alfred", Added: perms("gotham:patrol")},
		"bad slug":            {Name: "custom.Alfred", Origin: rbac.OriginCustom, Added: perms("gotham:patrol")},
		"pattern to nothing":  {Name: "custom.alfred", Origin: rbac.OriginCustom, Added: perms("arkham:*")},
		"already stored role": {Name: "custom.oracle", Origin: rbac.OriginCustom, Added: perms("gotham:patrol")},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "already stored role" {
				if _, err := f.store.GetRole(ctx, "custom.oracle"); errors.Is(err, rbac.ErrUnknownRole) {
					f.mustCreate("custom.oracle", "gotham:signal")
				}
			}
			b := imp.NewBatch()
			err := imp.ImportRole(b, r)
			errIs(t, "ImportRole", err, rbac.ErrImportRejected)
			if n := b.Count(); n != 0 {
				t.Errorf("a rejected import staged %d writes", n)
			}
			if err := b.Close(); err != nil {
				t.Error(err)
			}
		})
	}

	b := imp.NewBatch()
	if err := imp.ImportRole(b, &rbac.Role{Name: "custom.alfred", Title: "Butler", Origin: rbac.OriginCustom, Added: perms("gotham:signal", "gotham:patrol.*")}); err != nil {
		t.Fatalf("ImportRole: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	g := boot(t, e, setup{modules: []module{gotham()}})
	g.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{"custom.alfred"}})
	wantPerms(t, "imported role's patterns after the next Freeze", g.comp.last(t).RolePatterns, "gotham:patrol.*", "gotham:signal")
	r := g.role("custom.alfred")
	if r.Origin != rbac.OriginCustom || r.Version != 1 || r.CreatedAt.IsZero() {
		t.Errorf("imported role = %+v, want a custom role at version 1 with timestamps", r)
	}
}

// TestCovers_AgreesWithExpand (§4.2, review MAJOR-A): Covers is the one
// matching rule. Over a catalog with scopes that share prefixes, Expand of
// every pattern is exactly the declared scopes the pattern Covers, and
// GrantsAdmin is "some pattern Covers core:admin".
func TestCovers_AgreesWithExpand(t *testing.T) {
	cat, err := rbac.NewCatalog(append(rbac.CoreScopes(),
		rbac.PermissionSpec{Name: "gotham:patrol"},
		rbac.PermissionSpec{Name: "gotham:patrol.night"},
		rbac.PermissionSpec{Name: "gotham:patrols"},
		rbac.PermissionSpec{Name: "gotham:patrols.day"},
		rbac.PermissionSpec{Name: "gothamx:patrol"},
		rbac.PermissionSpec{Name: "arkham:cell.block"},
	)...)
	if err != nil {
		t.Fatal(err)
	}
	var scopes []rbac.Permission
	for spec := range cat.All() {
		scopes = append(scopes, spec.Name)
	}
	patterns := perms("gotham:*", "gothamx:*", "gotham:patrol.*", "gotham:patrols.*", "arkham:cell.*", "arkham:*",
		"core:*", "core:admin.*", "core:admin", "core:adm.*", "gotham:patrol", "gotham:patrol.night", "gotham:ghost",
		"Gotham:*", "gotham:patrol.night.*", "gotham", "", "gotham:*.*")
	for _, s := range scopes {
		patterns = append(patterns, s, rbac.Permission(s.Namespace()+":*"), rbac.Permission(s.Namespace()+":"+s.Action()+".*"))
	}
	for _, p := range patterns {
		var want []rbac.Permission
		for _, s := range scopes {
			if rbac.Covers(p, s) {
				want = append(want, s)
			}
		}
		if got := cat.Expand(p); !slices.Equal(got, want) {
			t.Errorf("Expand(%q) = %v, Covers selects %v", p, got, want)
		}
		if got, want := rbac.GrantsAdmin([]rbac.Permission{p}), rbac.Covers(p, rbac.CoreAdmin); got != want {
			t.Errorf("GrantsAdmin([%q]) = %v, Covers(%q, core:admin) = %v", p, got, p, want)
		}
	}
	for _, c := range []struct {
		pattern, scope rbac.Permission
		want           bool
	}{
		{"core:admin.*", "core:admin", true},
		{"core:admin.*", "core:administer", false},
		{"core:*", "core:admin", true},
		{"core:*", "corex:admin", false},
		{"gotham:patrol.*", "gotham:patrol", true},
		{"gotham:patrol.*", "gotham:patrol.night", true},
		{"gotham:patrol.*", "gotham:patrols.day", false},
		{"gotham:patrol", "gotham:patrol.night", false},
		{"gotham:patrol", "gotham:patrol", true},
		{"gotham:*", "gotham:*", false},
		{"gotham:patrol.*", "gotham:patrol.*", false},
		{"gotham:Patrol", "gotham:Patrol", false},
		{"", "", false},
	} {
		if got := rbac.Covers(c.pattern, c.scope); got != c.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", c.pattern, c.scope, got, c.want)
		}
	}
	for _, ps := range [][]rbac.Permission{perms("core:admin"), perms("core:*"), perms("core:admin.*"), perms("gotham:patrol", "core:admin.*")} {
		if !rbac.GrantsAdmin(ps) {
			t.Errorf("GrantsAdmin(%v) = false", ps)
		}
	}
	for _, ps := range [][]rbac.Permission{nil, perms("core:self"), perms("core:adm.*"), perms("gotham:*"), perms("core:admin.view")} {
		if rbac.GrantsAdmin(ps) {
			t.Errorf("GrantsAdmin(%v) = true", ps)
		}
	}
}

// TestFreeze_SkipsUndecodableRole (§6.3): a stored role that does not
// decode is skipped at Freeze and grants nothing; the boot goes on.
func TestFreeze_SkipsUndecodableRole(t *testing.T) {
	e := newEnv(t)
	f := boot(t, e, setup{modules: []module{gotham()}})
	f.mustCreate("custom.oracle", "gotham:signal")
	rawPut(t, e, "custom.oracle", []byte("not a role"))
	g := boot(t, e, setup{modules: []module{gotham()}})
	g.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{"custom.oracle"}})
	wantPerms(t, "patterns of an undecodable role", g.comp.last(t).RolePatterns)
	if err := g.svc.CanAssign(g.as("gotham:patrol"), []rbac.RoleName{"custom.oracle"}); !errors.Is(err, rbac.ErrUnknownRole) {
		t.Errorf("CanAssign of an undecodable role = %v, want it treated as unknown", err)
	}
}

// TestNoGlobals (03 R5): rbac and the native store hold no package-level
// mutable state and no init function.
func TestNoGlobals(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if root, err = archtest.ModuleRoot(root); err != nil {
		t.Fatal(err)
	}
	archtest.NoInit(t, root, "./rbac/...")
	archtest.NoInit(t, root, "./internal/rbacstore/...")
}
