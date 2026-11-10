// file: core/rbac/service_test.go
// version: 1.1.0
// guid: 4b9e2d17-8c5a-4f63-a1d0-6e3f7b2c8d95
// last-edited: 2026-11-10

package rbac_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/rbac"
)

// TestNew_RequiresDeps: New refuses a service it could not run fail-closed.
func TestNew_RequiresDeps(t *testing.T) {
	e := newEnv(t)
	st := e.store(t, nil)
	full := rbac.Deps{
		Store: st, Compile: (&compiler{}).compile, Audit: &auditLog{},
		Revocations: &revJournal{}, NewBatch: st.NewBatch,
	}
	ctx := context.Background()
	if _, err := rbac.New(ctx, rbac.Options{}, full); err != nil {
		t.Fatalf("New with every required dep: %v", err)
	}
	for name, drop := range map[string]func(*rbac.Deps){
		"Store":       func(d *rbac.Deps) { d.Store = nil },
		"Compile":     func(d *rbac.Deps) { d.Compile = nil },
		"Audit":       func(d *rbac.Deps) { d.Audit = nil },
		"Revocations": func(d *rbac.Deps) { d.Revocations = nil },
		"NewBatch":    func(d *rbac.Deps) { d.NewBatch = nil },
	} {
		d := full
		drop(&d)
		if _, err := rbac.New(ctx, rbac.Options{}, d); err == nil {
			t.Errorf("New without Deps.%s succeeded", name)
		}
	}
	if _, err := rbac.New(ctx, rbac.Options{CacheSize: -1}, full); err == nil {
		t.Error("New with a negative cache size succeeded")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := rbac.New(canceled, rbac.Options{}, full); err == nil {
		t.Error("New with a canceled context succeeded")
	}
}

// TestDeclare_ModuleRules (§4.9): each declaration rule refuses with its
// error, on the call and again at Freeze.
func TestDeclare_ModuleRules(t *testing.T) {
	cases := []struct {
		name  string
		mod   module
		want  error
		roles bool // the refusal comes from Roles, not Permissions
	}{
		{"foreign namespace", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "arkham:cell"}}}, rbac.ErrForeignNamespace, false},
		{"core scope", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "core:audit.view"}}}, rbac.ErrCoreGrant, false},
		{"pattern as a name", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:*"}}}, rbac.ErrBadScope, false},
		{"reserved module id", module{id: "custom", specs: []rbac.PermissionSpec{{Name: "custom:x"}}}, rbac.ErrForeignNamespace, false},
		{"grant to admin", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:patrol", DefaultGrants: []rbac.RoleName{rbac.RoleAdmin}}}}, rbac.ErrForeignNamespace, false},
		{"grant to another module's role", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:patrol", DefaultGrants: []rbac.RoleName{"arkham.warden"}}}}, rbac.ErrForeignNamespace, false},
		{"grant to a custom role", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:patrol", DefaultGrants: []rbac.RoleName{"custom.oracle"}}}}, rbac.ErrForeignNamespace, false},
		{"sensitive default grant", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:cave.manage", Sensitive: true, DefaultGrants: []rbac.RoleName{rbac.RoleEditor}}}}, rbac.ErrRestrictedGrant, false},
		{"auth-required default grant", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:vault", AuthRequired: true, DefaultGrants: []rbac.RoleName{rbac.RoleViewer}}}}, rbac.ErrRestrictedGrant, false},
		{"confining default grant", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:relay.worker", Confining: true, DefaultGrants: []rbac.RoleName{rbac.RoleEditor}}}}, rbac.ErrRestrictedGrant, false},
		{"settings default grant", module{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:settings.manage", DefaultGrants: []rbac.RoleName{rbac.RoleEditor}}}}, rbac.ErrRestrictedGrant, false},
		{"integrations default grant to own role", module{id: "gotham", seeds: []rbac.RoleSeed{{Name: "gotham.oracle"}}, specs: []rbac.PermissionSpec{{Name: "gotham:integrations.manage", DefaultGrants: []rbac.RoleName{"gotham.oracle"}}}}, rbac.ErrRestrictedGrant, false},
		{"seed role outside namespace", module{id: "gotham", seeds: []rbac.RoleSeed{{Name: "arkham.warden"}}}, rbac.ErrForeignNamespace, true},
		{"seed role named admin", module{id: "gotham", imported: []rbac.RoleName{"admin"}, seeds: []rbac.RoleSeed{{Name: "admin"}}}, rbac.ErrForeignNamespace, true},
		{"un-namespaced seed not imported", module{id: "gotham", seeds: []rbac.RoleSeed{{Name: "relay-worker"}}}, rbac.ErrForeignNamespace, true},
		{"seed holding a core scope", module{id: "gotham", seeds: []rbac.RoleSeed{{Name: "gotham.oracle", Permissions: perms("core:admin")}}}, rbac.ErrCoreGrant, true},
		{"seed holding core wildcard", module{id: "gotham", seeds: []rbac.RoleSeed{{Name: "gotham.oracle", Permissions: perms("core:*")}}}, rbac.ErrCoreGrant, true},
		{"seed holding a foreign scope", module{id: "gotham", seeds: []rbac.RoleSeed{{Name: "gotham.oracle", Permissions: perms("arkham:cell")}}}, rbac.ErrForeignNamespace, true},
		{"seed holding a malformed scope", module{id: "gotham", seeds: []rbac.RoleSeed{{Name: "gotham.oracle", Permissions: perms("gotham:Patrol")}}}, rbac.ErrBadScope, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := build(t, newEnv(t), setup{})
			d := f.svc.Declarer(tc.mod.id, tc.mod.imported...)
			perr := d.Permissions(tc.mod.specs...)
			rerr := d.Roles(tc.mod.seeds...)
			got := perr
			if tc.roles {
				got = rerr
			}
			errIs(t, "declare", got, tc.want)
			errIs(t, "Freeze", f.svc.Freeze(context.Background()), tc.want)
			if f.svc.Catalog() != nil {
				t.Error("a failed Freeze left a catalog")
			}
		})
	}
}

// TestFreeze_CatalogRules (§4.9): the rules that need the whole catalog fail
// Freeze and leave the service unfrozen.
func TestFreeze_CatalogRules(t *testing.T) {
	cases := []struct {
		name string
		mods []module
		want error
	}{
		{"seed scope not declared", []module{{id: "gotham", seeds: []rbac.RoleSeed{{Name: "gotham.oracle", Permissions: perms("gotham:cowl")}}}}, rbac.ErrUnknownScope},
		{"seed pattern matching nothing", []module{{id: "gotham", seeds: []rbac.RoleSeed{{Name: "gotham.oracle", Permissions: perms("gotham:*")}}}}, rbac.ErrUnknownScope},
		{"default grant to an undeclared own role", []module{{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:patrol", DefaultGrants: []rbac.RoleName{"gotham.oracle"}}}}}, rbac.ErrUnknownRole},
		{"seed declared twice", []module{
			{id: "gotham", imported: []rbac.RoleName{"night-shift"}, specs: []rbac.PermissionSpec{{Name: "gotham:patrol"}}, seeds: []rbac.RoleSeed{{Name: "night-shift", Permissions: perms("gotham:patrol")}}},
			{id: "arkham", imported: []rbac.RoleName{"night-shift"}, specs: []rbac.PermissionSpec{{Name: "arkham:cell"}}, seeds: []rbac.RoleSeed{{Name: "night-shift", Permissions: perms("arkham:cell")}}},
		}, rbac.ErrRoleExists},
		{"duplicate permission", []module{{id: "gotham", specs: []rbac.PermissionSpec{{Name: "gotham:patrol"}, {Name: "gotham:patrol"}}}}, rbac.ErrDuplicatePermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := build(t, newEnv(t), setup{})
			for _, m := range tc.mods {
				d := f.svc.Declarer(m.id, m.imported...)
				if err := errors.Join(d.Permissions(m.specs...), d.Roles(m.seeds...)); err != nil {
					t.Fatalf("declare %s: %v", m.id, err)
				}
			}
			errIs(t, "Freeze", f.svc.Freeze(context.Background()), tc.want)
			if f.svc.Catalog() != nil {
				t.Error("a failed Freeze left a catalog")
			}
			errIs(t, "CommitSeeds", f.svc.CommitSeeds(context.Background()), rbac.ErrNotFrozen)
		})
	}
}

// TestImplies_ClosureAndCycle (§4.3): an implication chain in the declarer's
// namespace freezes and keeps its edges; a cycle, a foreign edge and an edge
// touching a Confining scope fail Freeze. Following the closure in a check is
// the compiled checker's (F4-09's TestChecker_MatchVectors).
func TestImplies_ClosureAndCycle(t *testing.T) {
	chain := module{id: "gotham", specs: []rbac.PermissionSpec{
		{Name: "gotham:case.close", Implies: perms("gotham:case.edit")},
		{Name: "gotham:case.edit", Implies: perms("gotham:case.view")},
		{Name: "gotham:case.view"},
	}}
	f := build(t, newEnv(t), setup{})
	f.declare(chain)
	if err := f.svc.Freeze(context.Background()); err != nil {
		t.Fatalf("Freeze of a chain: %v", err)
	}
	spec, ok := f.cat().Lookup("gotham:case.close")
	if !ok || !slices.Equal(spec.Implies, perms("gotham:case.edit")) {
		t.Errorf("gotham:case.close = %+v, want its edge kept", spec)
	}
	wantPerms(t, "Expand(gotham:case.*)", f.cat().Expand("gotham:case.*"), "gotham:case.close", "gotham:case.edit", "gotham:case.view")

	for name, tc := range map[string]struct {
		specs []rbac.PermissionSpec
		want  error
	}{
		"self loop": {[]rbac.PermissionSpec{{Name: "gotham:case.view", Implies: perms("gotham:case.view")}}, rbac.ErrImpliesCycle},
		"three cycle": {[]rbac.PermissionSpec{
			{Name: "gotham:a", Implies: perms("gotham:b")}, {Name: "gotham:b", Implies: perms("gotham:c")}, {Name: "gotham:c", Implies: perms("gotham:a")},
		}, rbac.ErrImpliesCycle},
		"foreign edge":    {[]rbac.PermissionSpec{{Name: "gotham:a", Implies: perms("arkham:cell")}}, rbac.ErrForeignNamespace},
		"core edge":       {[]rbac.PermissionSpec{{Name: "gotham:a", Implies: perms("core:self")}}, rbac.ErrForeignNamespace},
		"confining edge":  {[]rbac.PermissionSpec{{Name: "gotham:w", Confining: true, Implies: perms("gotham:a")}, {Name: "gotham:a"}}, rbac.ErrConfiningImplication},
		"unknown target":  {[]rbac.PermissionSpec{{Name: "gotham:a", Implies: perms("gotham:ghost")}}, rbac.ErrUnknownScope},
		"wildcard target": {[]rbac.PermissionSpec{{Name: "gotham:a", Implies: perms("gotham:*")}}, rbac.ErrBadScope},
	} {
		t.Run(name, func(t *testing.T) {
			f := build(t, newEnv(t), setup{})
			if err := f.svc.Declarer("gotham").Permissions(tc.specs...); err != nil {
				t.Fatalf("declare: %v", err)
			}
			errIs(t, "Freeze", f.svc.Freeze(context.Background()), tc.want)
		})
	}
}

// TestFreeze_OnceAndClosed: a second Freeze, a declaration after Freeze and a
// write before Freeze are refused.
func TestFreeze_OnceAndClosed(t *testing.T) {
	ctx := context.Background()
	f := build(t, newEnv(t), setup{})
	_, err := f.svc.CreateRole(ctx, grant(nil, true, rbac.CoreAdmin), rbac.RoleInput{Name: "custom.oracle"})
	errIs(t, "CreateRole before Freeze", err, rbac.ErrNotFrozen)
	errIs(t, "CanAssign before Freeze", f.svc.CanAssign(grant(nil, true, rbac.CoreAdmin), []rbac.RoleName{rbac.RoleViewer}), rbac.ErrEscalation)
	f.declare(gotham())
	if err := f.svc.Freeze(ctx); err != nil {
		t.Fatal(err)
	}
	errIs(t, "second Freeze", f.svc.Freeze(ctx), rbac.ErrFrozen)
	errIs(t, "late Permissions", f.svc.Declarer("arkham").Permissions(rbac.PermissionSpec{Name: "arkham:cell"}), rbac.ErrFrozen)
	errIs(t, "late Roles", f.svc.Declarer("gotham").Roles(rbac.RoleSeed{Name: "gotham.oracle"}), rbac.ErrFrozen)
}

// TestRoleWrite_EscalationRefused (§4.7 rule 1): a role write needs every
// permission it grants, wildcards checked against what they expand to and
// against the pattern itself; a refusal stages nothing and is audited as a
// scope_escalation denial.
func TestRoleWrite_EscalationRefused(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	cat := f.cat()
	allGotham := grant(cat, true, "gotham:patrol", "gotham:patrol.night", "gotham:signal", "gotham:cave.manage", "gotham:settings.manage", "gotham:relay.worker")
	cases := []struct {
		name  string
		actor rbac.Checker
		ps    []rbac.Permission
	}{
		{"no actor", nil, perms("gotham:patrol")},
		{"unheld scope", f.as("gotham:patrol"), perms("gotham:patrol", "gotham:signal")},
		{"wildcard without the pattern", allGotham, perms("gotham:*")},
		{"action wildcard without the pattern", f.as("gotham:patrol", "gotham:patrol.night"), perms("gotham:patrol.*")},
		{"sensitive scope, non-interactive admin", grant(cat, false, rbac.CoreAdmin), perms("gotham:cave.manage")},
		{"wildcard reaching a sensitive scope, non-interactive admin", grant(cat, false, rbac.CoreAdmin), perms("gotham:*")},
		{"core:admin without core:admin", allGotham, perms("core:admin")},
		{"implied scope only", f.as("gotham:patrol.night"), perms("gotham:signal")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.audit.denials())
			gen := f.svc.Generation()
			_, err := f.svc.CreateRole(ctx, tc.actor, rbac.RoleInput{Name: "custom.joker", Permissions: tc.ps})
			errIs(t, "CreateRole", err, rbac.ErrEscalation)
			if _, err := f.store.GetRole(ctx, "custom.joker"); !errors.Is(err, rbac.ErrUnknownRole) {
				t.Errorf("a refused create stored the role: %v", err)
			}
			if f.svc.Generation() != gen {
				t.Error("a refused create bumped the generation")
			}
			if tc.actor == nil {
				return
			}
			d := f.audit.denials()
			if len(d) != before+1 || d[len(d)-1].Reason != "scope_escalation" || d[len(d)-1].Action != "core.permission.deny" || d[len(d)-1].Outcome != audit.Denied {
				t.Errorf("denials = %+v, want one core.permission.deny scope_escalation", d[before:])
			}
		})
	}

	// What the actor holds, it may grant: a wildcard it holds literally, an
	// interactive admin anything.
	if _, err := f.svc.CreateRole(ctx, grant(cat, true, "gotham:*"), rbac.RoleInput{Name: "custom.robin", Permissions: perms("gotham:*")}); err != nil {
		t.Errorf("holder of gotham:* creating gotham:*: %v", err)
	}
	r, err := f.svc.CreateRole(ctx, f.admin(), rbac.RoleInput{Name: "custom.oracle", Permissions: perms("gotham:signal", "gotham:cave.manage", "gotham:signal")})
	if err != nil {
		t.Fatalf("admin create: %v", err)
	}
	wantPerms(t, "custom.oracle", r.Permissions(), "gotham:cave.manage", "gotham:signal")

	// UpdateRole: granting and removing both need the scope (rule 2's reading
	// for role edits: the edit acts on every holder).
	_, err = f.svc.UpdateRole(ctx, f.as("gotham:signal"), "custom.oracle", rbac.RoleInput{Permissions: perms("gotham:signal")}, r.Version)
	errIs(t, "removing an unheld scope", err, rbac.ErrEscalation)
	_, err = f.svc.UpdateRole(ctx, f.as("gotham:signal", "gotham:cave.manage"), "custom.oracle", rbac.RoleInput{Permissions: perms("gotham:signal", "gotham:cave.manage", "gotham:patrol")}, r.Version)
	errIs(t, "adding an unheld scope", err, rbac.ErrEscalation)
	f.unchanged("custom.oracle", r.Version)
	errIs(t, "deleting a role holding an unheld scope", f.svc.DeleteRole(ctx, f.as("gotham:signal"), "custom.oracle", r.Version), rbac.ErrEscalation)
	f.unchanged("custom.oracle", r.Version)
}

// TestRoleWrite_InputRules: a custom role is named custom.<slug>, holds only
// scopes the catalog has, keeps its name, and moves by version.
func TestRoleWrite_InputRules(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	for _, name := range []rbac.RoleName{"oracle", "custom.", "custom.Oracle", "custom.1oracle", "gotham.oracle", "editor", "custom.o/racle"} {
		_, err := f.svc.CreateRole(ctx, f.admin(), rbac.RoleInput{Name: name})
		errIs(t, "CreateRole("+string(name)+")", err, rbac.ErrRoleName)
	}
	for _, p := range []string{"gotham:ghost", "arkham:*", "gotham:Patrol", "gotham:", ":x"} {
		_, err := f.svc.CreateRole(ctx, f.admin(), rbac.RoleInput{Name: "custom.oracle", Permissions: perms(p)})
		if !errors.Is(err, rbac.ErrUnknownScope) && !errors.Is(err, rbac.ErrBadScope) {
			t.Errorf("CreateRole holding %q: err = %v, want ErrUnknownScope or ErrBadScope", p, err)
		}
	}
	r := f.mustCreate("custom.oracle", "gotham:signal")
	_, err := f.svc.CreateRole(ctx, f.admin(), rbac.RoleInput{Name: "custom.oracle"})
	errIs(t, "create twice", err, rbac.ErrRoleExists)
	_, err = f.svc.UpdateRole(ctx, f.admin(), "custom.oracle", rbac.RoleInput{Name: "custom.batgirl"}, r.Version)
	errIs(t, "rename", err, rbac.ErrRoleName)
	_, err = f.svc.UpdateRole(ctx, f.admin(), "custom.oracle", rbac.RoleInput{Permissions: perms("gotham:patrol")}, r.Version+1)
	errIs(t, "stale version", err, rbac.ErrVersionMismatch)
	_, err = f.svc.UpdateRole(ctx, f.admin(), "custom.ghost", rbac.RoleInput{}, 1)
	errIs(t, "update of an absent role", err, rbac.ErrUnknownRole)
	errIs(t, "delete stale", f.svc.DeleteRole(ctx, f.admin(), "custom.oracle", r.Version+1), rbac.ErrVersionMismatch)
	f.unchanged("custom.oracle", r.Version)
	u, err := f.svc.UpdateRole(ctx, f.admin(), "custom.oracle", rbac.RoleInput{Title: "Oracle", Permissions: perms("gotham:patrol.*")}, r.Version)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if u.Version != r.Version+1 || u.Title != "Oracle" {
		t.Errorf("updated = %+v, want version %d and the new title", u, r.Version+1)
	}
	wantPerms(t, "updated permissions", u.Permissions(), "gotham:patrol.*")
	if got := f.journal.kinds(); !slices.Equal(got, []string{"role.narrowed custom.oracle"}) {
		t.Errorf("journal = %v, want the narrowing of custom.oracle", got)
	}
	if f.journal.lines[0].Source != "gotham-test" || !slices.Equal(f.journal.lines[0].Detail, []string{"gotham:signal"}) {
		t.Errorf("narrowing line = %+v, want Source and the removed scope", f.journal.lines[0])
	}
}

// TestSeedRole_EditRefused (§4.8): seed roles are read-only except
// AcceptSeed, admin is locked on every path, and a refusal stages nothing.
func TestSeedRole_EditRefused(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	recorded := len(f.audit.actions())
	for _, name := range []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer, "gotham.detective", "relay-worker"} {
		v := f.role(name).Version
		_, err := f.svc.UpdateRole(ctx, f.admin(), name, rbac.RoleInput{Permissions: perms("gotham:patrol")}, v)
		errIs(t, "UpdateRole("+string(name)+")", err, rbac.ErrSeedRoleLocked)
		errIs(t, "DeleteRole("+string(name)+")", f.svc.DeleteRole(ctx, f.admin(), name, v), rbac.ErrSeedRoleLocked)
		f.unchanged(name, v)
	}
	v := f.role(rbac.RoleAdmin).Version
	_, err := f.svc.UpdateRole(ctx, f.admin(), rbac.RoleAdmin, rbac.RoleInput{Permissions: perms("core:admin", "gotham:patrol")}, v)
	errIs(t, "UpdateRole(admin)", err, rbac.ErrLockedRole)
	errIs(t, "DeleteRole(admin)", f.svc.DeleteRole(ctx, f.admin(), rbac.RoleAdmin, v), rbac.ErrLockedRole)
	_, err = f.svc.AcceptSeed(ctx, f.admin(), rbac.RoleAdmin, v)
	errIs(t, "AcceptSeed(admin)", err, rbac.ErrLockedRole)
	f.unchanged(rbac.RoleAdmin, v)
	if got := f.audit.actions(); len(got) != recorded {
		t.Errorf("refused edits audited %v", got[recorded:])
	}
	if len(f.journal.kinds()) != 0 {
		t.Errorf("refused edits journalled %v", f.journal.kinds())
	}
}

// TestCanAssign_CannotGrantUnheld (§4.7 rule 2): an actor assigns only roles
// whose every scope it holds; an unknown role only with core:admin.
func TestCanAssign_CannotGrantUnheld(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	f.mustCreate("custom.oracle", "gotham:signal", "gotham:cave.manage")
	f.mustCreate("custom.robin", "gotham:patrol.*")
	cat := f.cat()
	viewerScopes := f.role(rbac.RoleViewer).Permissions()
	cases := []struct {
		name  string
		actor rbac.Checker
		roles []rbac.RoleName
		ok    bool
	}{
		{"no actor", nil, []rbac.RoleName{rbac.RoleViewer}, false},
		{"holds the viewer seed", f.as(viewerScopes...), []rbac.RoleName{rbac.RoleViewer}, true},
		{"lacks one editor scope", f.as(viewerScopes...), []rbac.RoleName{rbac.RoleViewer, rbac.RoleEditor}, false},
		{"admin role needs core:admin", f.as(viewerScopes...), []rbac.RoleName{rbac.RoleAdmin}, false},
		{"interactive admin", f.admin(), []rbac.RoleName{rbac.RoleAdmin, rbac.RoleEditor, "custom.oracle"}, true},
		{"non-interactive admin, sensitive role", grant(cat, false, rbac.CoreAdmin), []rbac.RoleName{"custom.oracle"}, false},
		{"non-interactive admin, plain role", grant(cat, false, rbac.CoreAdmin), []rbac.RoleName{rbac.RoleEditor}, true},
		{"wildcard role needs the pattern", f.as("gotham:patrol", "gotham:patrol.night"), []rbac.RoleName{"custom.robin"}, false},
		{"wildcard role, pattern held", f.as("gotham:patrol.*"), []rbac.RoleName{"custom.robin"}, true},
		{"unknown role, no admin", f.as(viewerScopes...), []rbac.RoleName{"custom.ghost"}, false},
		{"unknown role, admin", f.admin(), []rbac.RoleName{"custom.ghost"}, true},
		{"no roles", f.as(), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := f.svc.CanAssign(tc.actor, tc.roles)
			if tc.ok && err != nil {
				t.Errorf("CanAssign: %v, want allowed", err)
			}
			if !tc.ok {
				errIs(t, "CanAssign", err, rbac.ErrEscalation)
			}
		})
	}
	errIs(t, "unknown role", f.svc.CanAssign(f.as(viewerScopes...), []rbac.RoleName{"custom.ghost"}), rbac.ErrUnknownRole)
}

// TestCanAssign_RemovalCheckedLikeAssignment (§4.7 rule 2): taking a role
// away is the same check as giving it (auth calls CanAssign for both), and a
// role edit that removes a scope needs that scope like one that adds it.
func TestCanAssign_RemovalCheckedLikeAssignment(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	r := f.mustCreate("custom.oracle", "gotham:signal", "gotham:patrol")
	weak := f.as("gotham:patrol")
	errIs(t, "removing custom.oracle from a user", f.svc.CanAssign(weak, []rbac.RoleName{"custom.oracle"}), rbac.ErrEscalation)
	_, err := f.svc.UpdateRole(ctx, weak, "custom.oracle", rbac.RoleInput{Permissions: perms("gotham:patrol")}, r.Version)
	errIs(t, "removing gotham:signal from the role", err, rbac.ErrEscalation)
	f.unchanged("custom.oracle", r.Version)
	strong := f.as("gotham:patrol", "gotham:signal")
	if err := f.svc.CanAssign(strong, []rbac.RoleName{"custom.oracle"}); err != nil {
		t.Errorf("holder of both scopes: %v", err)
	}
	if _, err := f.svc.UpdateRole(ctx, strong, "custom.oracle", rbac.RoleInput{Permissions: perms("gotham:patrol")}, r.Version); err != nil {
		t.Errorf("holder of both scopes narrowing the role: %v", err)
	}
}

// TestDominates (§4.7 rule 5): acting on a user needs every scope the user
// holds. A target role the service does not know is refused unless the actor
// holds core:admin (review MINOR-D: the actor cannot be shown to hold what
// the role will grant once someone creates it).
func TestDominates(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	editorScopes := f.role(rbac.RoleEditor).Permissions()
	viewerScopes := f.role(rbac.RoleViewer).Permissions()
	errIs(t, "viewer over editor", f.svc.Dominates(f.as(viewerScopes...), []rbac.RoleName{rbac.RoleEditor}), rbac.ErrDominance)
	errIs(t, "editor over admin", f.svc.Dominates(f.as(editorScopes...), []rbac.RoleName{rbac.RoleAdmin}), rbac.ErrDominance)
	errIs(t, "no actor", f.svc.Dominates(nil, nil), rbac.ErrDominance)
	err := f.svc.Dominates(f.as(editorScopes...), []rbac.RoleName{rbac.RoleViewer, "custom.ghost"})
	errIs(t, "editor over viewer and an unknown role", err, rbac.ErrDominance)
	errIs(t, "editor over viewer and an unknown role", err, rbac.ErrUnknownRole)
	if err := f.svc.Dominates(f.as(editorScopes...), []rbac.RoleName{rbac.RoleViewer}); err != nil {
		t.Errorf("editor over viewer: %v", err)
	}
	if err := f.svc.Dominates(f.admin(), []rbac.RoleName{rbac.RoleAdmin}); err != nil {
		t.Errorf("admin over admin: %v", err)
	}
	if err := f.svc.Dominates(f.admin(), []rbac.RoleName{rbac.RoleViewer, "custom.ghost"}); err != nil {
		t.Errorf("admin over viewer and an unknown role: %v", err)
	}
}

// TestDeleteRole: an unassigned custom role is deleted with its journal line
// and audit record; an assigned one, or any when no holder source is wired,
// answers ErrRoleInUse.
func TestDeleteRole(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	r := f.mustCreate("custom.oracle", "gotham:signal")
	f.users.put(t, "barbara", userRec{Roles: []rbac.RoleName{"custom.oracle"}, Active: false})
	errIs(t, "delete assigned (even to an inactive user)", f.svc.DeleteRole(ctx, f.admin(), "custom.oracle", r.Version), rbac.ErrRoleInUse)
	f.unchanged("custom.oracle", r.Version)
	f.users.put(t, "barbara", userRec{Roles: []rbac.RoleName{rbac.RoleViewer}, Active: true})
	gen := f.svc.Generation()
	if err := f.svc.DeleteRole(ctx, f.admin(), "custom.oracle", r.Version); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := f.store.GetRole(ctx, "custom.oracle"); !errors.Is(err, rbac.ErrUnknownRole) {
		t.Errorf("deleted role still stored: %v", err)
	}
	if !slices.Contains(f.journal.kinds(), "role.deleted custom.oracle") {
		t.Errorf("journal = %v, want role.deleted", f.journal.kinds())
	}
	if !slices.Contains(f.audit.actions(), "core.role.delete custom.oracle") {
		t.Errorf("audit = %v, want core.role.delete", f.audit.actions())
	}
	if f.svc.Generation() != gen+1 || (*f.gens)[len(*f.gens)-1] != gen+1 {
		t.Errorf("generation %d, OnChange %v; want %d reported", f.svc.Generation(), *f.gens, gen+1)
	}
	errIs(t, "delete absent", f.svc.DeleteRole(ctx, f.admin(), "custom.oracle", r.Version), rbac.ErrUnknownRole)

	g := boot(t, newEnv(t), setup{modules: []module{gotham()}, noHolders: true})
	r = g.mustCreate("custom.oracle", "gotham:signal")
	errIs(t, "delete without Holders", g.svc.DeleteRole(ctx, g.admin(), "custom.oracle", r.Version), rbac.ErrRoleInUse)
	g.unchanged("custom.oracle", r.Version)
}

// TestRoleWrite_FailsClosed: a failed audit record or holder read fails the
// write and commits nothing; a failed journal append still commits (a disk
// problem never blocks a revocation) and stages a gap, and a failed gap fails
// the write.
func TestRoleWrite_FailsClosed(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	r := f.mustCreate("custom.oracle", "gotham:signal", "gotham:patrol")
	gen := f.svc.Generation()

	f.audit.recordErr = errors.New("audit disk full")
	_, err := f.svc.UpdateRole(ctx, f.admin(), "custom.oracle", rbac.RoleInput{Permissions: perms("gotham:patrol")}, r.Version)
	if err == nil {
		t.Error("update with a failing audit record succeeded")
	}
	_, err = f.svc.CreateRole(ctx, f.admin(), rbac.RoleInput{Name: "custom.robin"})
	if err == nil {
		t.Error("create with a failing audit record succeeded")
	}
	f.unchanged("custom.oracle", r.Version)
	if _, err := f.store.GetRole(ctx, "custom.robin"); !errors.Is(err, rbac.ErrUnknownRole) {
		t.Errorf("custom.robin stored despite the audit failure: %v", err)
	}
	if f.svc.Generation() != gen {
		t.Error("a failed write bumped the generation")
	}
	f.audit.recordErr = nil

	f.journal.appendErr = errors.New("journal disk full")
	u, err := f.svc.UpdateRole(ctx, f.admin(), "custom.oracle", rbac.RoleInput{Permissions: perms("gotham:patrol")}, r.Version)
	if err != nil {
		t.Fatalf("a failed journal append blocked the narrowing: %v", err)
	}
	if f.journal.gaps != 1 {
		t.Errorf("gaps = %d, want 1", f.journal.gaps)
	}
	f.journal.gapErr = errors.New("gap disk full")
	_, err = f.svc.UpdateRole(ctx, f.admin(), "custom.oracle", rbac.RoleInput{}, u.Version)
	if err == nil {
		t.Error("a narrowing whose append and gap both failed committed")
	}
	f.unchanged("custom.oracle", u.Version)
	f.journal.appendErr, f.journal.gapErr = nil, nil

	f.users.mu.Lock()
	f.users.err = errors.New("user store down")
	f.users.mu.Unlock()
	if err := f.svc.DeleteRole(ctx, f.admin(), "custom.oracle", u.Version); err == nil {
		t.Error("delete with a failing holder read succeeded")
	}
	f.unchanged("custom.oracle", u.Version)
}

// TestLastAdmin_ConcurrentDemotions (§4.6, §9): two edits that each take
// core:admin from a different role race; exactly one wins and an active
// admin remains. A demotion committed in auth between the holder read and
// the commit fails the write's batch, and the retry refuses the change.
func TestLastAdmin_ConcurrentDemotions(t *testing.T) {
	ctx := context.Background()
	t.Run("two role edits", func(t *testing.T) {
		f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
		wayne := f.mustCreate("custom.wayne", "core:admin")
		fox := f.mustCreate("custom.fox", "core:admin")
		f.users.put(t, "bruce", userRec{Roles: []rbac.RoleName{"custom.wayne"}, Active: true})
		f.users.put(t, "lucius", userRec{Roles: []rbac.RoleName{"custom.fox"}, Active: true})
		f.users.put(t, "alfred", userRec{Roles: []rbac.RoleName{rbac.RoleAdmin}, Active: false})

		// The barrier sits inside HoldersOf, after each writer read the role
		// cache and before it reads the holders. The first writer to get
		// there waits for the second; the second releases it. If nothing
		// serialises the writers, both are inside at once and both decide on
		// the same holder snapshot, so both narrowings pass the rule and
		// every admin role is gone: the race is forced, not sampled. If the
		// service serialises them, the second writer cannot arrive while
		// the first waits, so the first gives up after the timeout, commits,
		// and the second then reads the committed state.
		var calls atomic.Int32
		met := make(chan struct{})
		f.users.mu.Lock()
		f.users.wait = func() {
			switch calls.Add(1) {
			case 1:
				select {
				case <-met:
				case <-time.After(300 * time.Millisecond):
				}
			case 2:
				close(met)
			}
		}
		f.users.mu.Unlock()

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, r := range []*rbac.Role{wayne, fox} {
			wg.Go(func() {
				_, errs[i] = f.svc.UpdateRole(ctx, f.admin(), r.Name, rbac.RoleInput{Permissions: perms("gotham:patrol")}, r.Version)
			})
		}
		wg.Wait()
		if n := calls.Load(); n < 2 {
			t.Fatalf("HoldersOf ran %d times, want both writers to read the holders", n)
		}
		won, lost := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, rbac.ErrLastAdmin):
				lost++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}
		if won != 1 || lost != 1 {
			t.Fatalf("won %d, lost %d (%v); want exactly one of each", won, lost, errs)
		}
		admins := 0
		for _, name := range []rbac.RoleName{"custom.wayne", "custom.fox"} {
			if rbac.GrantsAdmin(f.role(name).Permissions()) {
				admins++
			}
		}
		if admins != 1 {
			t.Errorf("%d roles still grant core:admin, want 1", admins)
		}
	})

	t.Run("demotion in auth before commit", func(t *testing.T) {
		f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
		wayne := f.mustCreate("custom.wayne", "core:admin")
		f.users.put(t, "bruce", userRec{Roles: []rbac.RoleName{"custom.wayne"}, Active: true})
		f.users.put(t, "lucius", userRec{Roles: []rbac.RoleName{rbac.RoleAdmin}, Active: true})
		attempts := 0
		rbac.SetBeforeCommit(f.svc, func() {
			attempts++
			if attempts == 1 {
				// auth demotes lucius after the holder read saw him.
				f.users.put(t, "lucius", userRec{Roles: []rbac.RoleName{rbac.RoleViewer}, Active: true})
			}
		})
		_, err := f.svc.UpdateRole(ctx, f.admin(), "custom.wayne", rbac.RoleInput{Permissions: perms("gotham:patrol")}, wayne.Version)
		errIs(t, "narrowing the last admin role", err, rbac.ErrLastAdmin)
		if attempts != 1 {
			t.Errorf("commit attempts = %d, want 1 (the retry decides before committing)", attempts)
		}
		f.unchanged("custom.wayne", wayne.Version)
	})

	t.Run("no holders wired", func(t *testing.T) {
		f := boot(t, newEnv(t), setup{modules: []module{gotham()}, noHolders: true})
		wayne := f.mustCreate("custom.wayne", "core:admin")
		_, err := f.svc.UpdateRole(ctx, f.admin(), "custom.wayne", rbac.RoleInput{Permissions: perms("gotham:patrol")}, wayne.Version)
		errIs(t, "narrowing an admin role without Holders", err, rbac.ErrLastAdmin)
		if _, err := f.svc.UpdateRole(ctx, f.admin(), "custom.wayne", rbac.RoleInput{Title: "Wayne", Permissions: perms("core:admin", "gotham:patrol")}, wayne.Version); err != nil {
			t.Errorf("an edit that keeps core:admin needs no holders: %v", err)
		}
	})

	// Review MAJOR-A: an action wildcard over core:admin grants it, so the
	// rule must see it (GrantsAdmin is Covers(p, core:admin)).
	t.Run("admin granted by core:admin.*", func(t *testing.T) {
		f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
		wayne := f.mustCreate("custom.wayne", "core:admin.*")
		f.users.put(t, "bruce", userRec{Roles: []rbac.RoleName{"custom.wayne"}, Active: true})
		f.users.put(t, "alfred", userRec{Roles: []rbac.RoleName{rbac.RoleAdmin}, Active: false})
		_, err := f.svc.UpdateRole(ctx, f.admin(), "custom.wayne", rbac.RoleInput{Permissions: perms("core:self")}, wayne.Version)
		errIs(t, "narrowing core:admin.* with only an inactive admin left", err, rbac.ErrLastAdmin)
		f.unchanged("custom.wayne", wayne.Version)
		f.users.put(t, "alfred", userRec{Roles: []rbac.RoleName{rbac.RoleAdmin}, Active: true})
		if _, err := f.svc.UpdateRole(ctx, f.admin(), "custom.wayne", rbac.RoleInput{Permissions: perms("core:self")}, wayne.Version); err != nil {
			t.Errorf("narrowing core:admin.* with an active admin left: %v", err)
		}
	})

	t.Run("inactive holders do not count", func(t *testing.T) {
		f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
		wayne := f.mustCreate("custom.wayne", "core:*")
		f.users.put(t, "bruce", userRec{Roles: []rbac.RoleName{"custom.wayne"}, Active: true})
		f.users.put(t, "alfred", userRec{Roles: []rbac.RoleName{rbac.RoleAdmin}, Active: false})
		_, err := f.svc.UpdateRole(ctx, f.admin(), "custom.wayne", rbac.RoleInput{Permissions: perms("core:self")}, wayne.Version)
		errIs(t, "narrowing core:* with only an inactive admin left", err, rbac.ErrLastAdmin)
	})
}

// TestRoles_ReadPaths: Roles and Role read the store.
func TestRoles_ReadPaths(t *testing.T) {
	ctx := context.Background()
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	f.mustCreate("custom.oracle", "gotham:signal")
	var names []rbac.RoleName
	for r, err := range f.svc.Roles(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, r.Name)
	}
	want := []rbac.RoleName{rbac.RoleAdmin, "custom.oracle", rbac.RoleEditor, "gotham.detective", "relay-worker", rbac.RoleViewer}
	if !slices.Equal(names, want) {
		t.Errorf("Roles = %v, want %v", names, want)
	}
	if _, err := f.svc.Role(ctx, "custom.ghost"); !errors.Is(err, rbac.ErrUnknownRole) {
		t.Errorf("Role(absent) = %v, want ErrUnknownRole", err)
	}
	r, err := f.svc.Role(ctx, "custom.oracle")
	if err != nil || r.Origin != rbac.OriginCustom {
		t.Errorf("Role(custom.oracle) = %+v, %v", r, err)
	}
}
