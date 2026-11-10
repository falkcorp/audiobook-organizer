// file: core/rbac/compile_test.go
// version: 1.1.0
// guid: 8d3f1a64-2e7b-4c95-b6a0-9f4e2c7d1b58
// last-edited: 2026-11-10

package rbac_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// books is the module of the worker-key gate test: the Confining worker
// scope, two scopes granted to both core roles and the imported fp-worker
// role (05-rbac §4.8, §6.2).
func books(extra ...rbac.PermissionSpec) module {
	return module{
		id:       "books",
		imported: []rbac.RoleName{"fp-worker"},
		specs: append([]rbac.PermissionSpec{
			{Name: "books:fingerprint.worker", Title: "Fingerprint worker", Confining: true},
			{Name: "books:view", Title: "View", Class: rbac.ClassView, DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}},
			{Name: "books:reading", Title: "Reading state", DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}},
		}, extra...),
		seeds: []rbac.RoleSeed{{Name: "fp-worker", Title: "Fingerprint worker", Permissions: perms("books:fingerprint.worker")}},
	}
}

// TestWorkerKey_Confined (gate test, rbac's cells; F4-09 adds the compiled
// checker's, F4-08 the routes'): the fp-worker seed holds only Confining
// scopes after every seed merge, the ambient rule leaves it alone, and an
// admin-owned key scoped to the worker scope, an fp-worker user's key and an
// fp-worker session each reach Deps.Compile with the key's scopes intact.
func TestWorkerKey_Confined(t *testing.T) {
	e := newEnv(t)
	releases := [][]rbac.PermissionSpec{
		nil,
		{{Name: "books:edit", Title: "Edit", DefaultGrants: []rbac.RoleName{rbac.RoleEditor}}},
		{{Name: "books:edit", Title: "Edit", DefaultGrants: []rbac.RoleName{rbac.RoleEditor}}, {Name: "books:arr.request", Title: "Request", Confining: true}},
		nil,
	}
	var f *fixture
	for i, extra := range releases {
		f = boot(t, e, setup{modules: []module{books(extra...)}})
		w := f.role("fp-worker")
		wantPerms(t, "fp-worker after release "+strconv.Itoa(i), w.Permissions(), "books:fingerprint.worker")
		if len(w.PendingSeed) != 0 || !rbac.Confined(f.cat(), w.Permissions()) {
			t.Errorf("release %d: fp-worker = %+v, want confined with nothing pending", i, w)
		}
	}
	cat := f.cat()
	ambient := perms("core:self", "core:metrics.read", "books:reading")
	wantPerms(t, "WithAmbient(fp-worker)", rbac.WithAmbient(cat, f.role("fp-worker").Permissions(), ambient...), "books:fingerprint.worker")
	wantPerms(t, "WithAmbient(worker key)", rbac.WithAmbient(cat, perms("books:fingerprint.worker"), ambient...), "books:fingerprint.worker")
	wantPerms(t, "WithAmbient(plain role)", rbac.WithAmbient(cat, perms("books:view"), ambient...), "books:reading", "books:view", "core:metrics.read", "core:self")
	wantPerms(t, "WithAmbient(admin)", rbac.WithAmbient(cat, perms("core:admin"), ambient...), "books:reading", "core:admin", "core:metrics.read", "core:self")

	cells := []struct {
		name string
		in   rbac.CheckerInput
		want rbac.CompileInput
	}{
		{"admin-owned worker key",
			rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Scopes: perms("books:fingerprint.worker")},
			rbac.CompileInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Scopes: perms("books:fingerprint.worker"), RolePatterns: perms("core:admin")}},
		{"fp-worker user's key",
			rbac.CheckerInput{Roles: []rbac.RoleName{"fp-worker"}, Scopes: perms("books:fingerprint.worker")},
			rbac.CompileInput{Roles: []rbac.RoleName{"fp-worker"}, Scopes: perms("books:fingerprint.worker"), RolePatterns: perms("books:fingerprint.worker")}},
		{"fp-worker session",
			rbac.CheckerInput{Roles: []rbac.RoleName{"fp-worker"}, Interactive: true},
			rbac.CompileInput{Roles: []rbac.RoleName{"fp-worker"}, Interactive: true, RolePatterns: perms("books:fingerprint.worker")}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			before := f.comp.calls()
			if f.svc.Compile(c.in) == nil {
				t.Fatal("Compile returned nil")
			}
			if f.comp.calls() != before+1 {
				t.Fatalf("Deps.Compile calls = %d, want %d", f.comp.calls(), before+1)
			}
			got := f.comp.last(t)
			if !slices.Equal(got.Roles, c.want.Roles) || !slices.Equal(got.Scopes, c.want.Scopes) ||
				(got.Scopes == nil) != (c.want.Scopes == nil) || got.Interactive != c.want.Interactive ||
				!slices.Equal(got.RolePatterns, c.want.RolePatterns) {
				t.Errorf("CompileInput = %+v, want %+v", got, c.want)
			}
		})
	}

	// A release can neither grant the worker scope by default nor give the
	// worker role a non-Confining scope.
	g := build(t, newEnv(t), setup{})
	err := g.svc.Declarer("books", "fp-worker").Permissions(rbac.PermissionSpec{Name: "books:fingerprint.worker", Confining: true, DefaultGrants: []rbac.RoleName{rbac.RoleEditor}})
	errIs(t, "default grant of the worker scope", err, rbac.ErrRestrictedGrant)
}

// TestSeed_NoNonConfiningScopeOnConfinedRole (§4.8): a confined seed role
// gains no non-Confining scope, neither at Freeze (from its own seed or a
// DefaultGrant) nor in the merge, when a release swaps its seed for an
// unconfined one.
func TestSeed_NoNonConfiningScopeOnConfinedRole(t *testing.T) {
	for name, mod := range map[string]module{
		"default grant to the worker role": {
			id: "books", imported: []rbac.RoleName{"fp-worker"},
			specs: []rbac.PermissionSpec{
				{Name: "books:fingerprint.worker", Confining: true},
				{Name: "books:view", DefaultGrants: []rbac.RoleName{"fp-worker"}},
			},
			seeds: []rbac.RoleSeed{{Name: "fp-worker", Permissions: perms("books:fingerprint.worker")}},
		},
		"mixed worker seed": {
			id: "books", imported: []rbac.RoleName{"fp-worker"},
			specs: []rbac.PermissionSpec{{Name: "books:fingerprint.worker", Confining: true}, {Name: "books:view"}},
			seeds: []rbac.RoleSeed{{Name: "fp-worker", Permissions: perms("books:fingerprint.worker", "books:view")}},
		},
		"worker seed with a wildcard": {
			id: "books", imported: []rbac.RoleName{"fp-worker"},
			specs: []rbac.PermissionSpec{{Name: "books:fingerprint.worker", Confining: true}, {Name: "books:arr.request", Confining: true}},
			seeds: []rbac.RoleSeed{{Name: "fp-worker", Permissions: perms("books:fingerprint.worker", "books:arr.*")}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := build(t, newEnv(t), setup{})
			f.declare(mod)
			errIs(t, "Freeze", f.svc.Freeze(context.Background()), rbac.ErrConfiningSeed)
		})
	}

	t.Run("release swaps the worker seed", func(t *testing.T) {
		e := newEnv(t)
		boot(t, e, setup{modules: []module{books()}})
		swapped := books()
		swapped.seeds = []rbac.RoleSeed{{Name: "fp-worker", Title: "Fingerprint worker", Permissions: perms("books:view")}}
		for boots := range 3 {
			f := boot(t, e, setup{modules: []module{swapped}})
			w := f.role("fp-worker")
			wantPerms(t, "effective", w.Permissions())
			wantPerms(t, "pending", w.PendingSeed, "books:view")
			if boots > 0 && w.Version != 2 {
				t.Errorf("boot %d rewrote fp-worker (version %d): the merge is not idempotent", boots, w.Version)
			}
		}
	})
}

// TestCompileCache_GenerationInvalidates (§9): Compile calls Deps.Compile
// once per (roles, scopes, interactive, generation); a committed role change
// bumps the generation and the next Compile rebuilds; unknown role IDs are
// skipped and counted.
func TestCompileCache_GenerationInvalidates(t *testing.T) {
	ctx := context.Background()
	p, sc := rbacScope(t)
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}, metrics: sc})
	r := f.mustCreate("custom.oracle", "gotham:signal")
	in := rbac.CheckerInput{Roles: []rbac.RoleName{"custom.oracle", rbac.RoleViewer}}
	base := f.comp.calls()
	c1 := f.svc.Compile(in)
	c2 := f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleViewer, "custom.oracle", rbac.RoleViewer}})
	if f.comp.calls() != base+1 || c1 != c2 {
		t.Errorf("calls = %d, want one (role order and duplicates share an entry)", f.comp.calls()-base)
	}
	for _, other := range []rbac.CheckerInput{
		{Roles: in.Roles, Interactive: true},
		{Roles: in.Roles, Scopes: perms("gotham:signal")},
		{Roles: in.Roles, Scopes: perms("gotham:signal", "gotham:patrol")},
		{Roles: in.Roles[:1]},
	} {
		f.svc.Compile(other)
	}
	if got := f.comp.calls() - base; got != 5 {
		t.Errorf("calls = %d, want 5 (interactive, key scopes and roles each part of the key)", got)
	}
	f.svc.Compile(rbac.CheckerInput{Roles: in.Roles, Scopes: perms("gotham:patrol", "gotham:signal", "gotham:patrol")})
	if got := f.comp.calls() - base; got != 5 {
		t.Errorf("calls = %d, want 5 (scope order and duplicates share an entry)", got)
	}

	gen := f.svc.Generation()
	if _, err := f.svc.UpdateRole(ctx, f.admin(), "custom.oracle", rbac.RoleInput{Permissions: perms("gotham:signal", "gotham:patrol")}, r.Version); err != nil {
		t.Fatal(err)
	}
	if f.svc.Generation() != gen+1 {
		t.Fatalf("generation = %d, want %d", f.svc.Generation(), gen+1)
	}
	f.svc.Compile(in)
	if got := f.comp.calls() - base; got != 6 {
		t.Errorf("calls = %d, want 6 (the generation bump invalidates)", got)
	}
	wantPerms(t, "patterns after the edit", f.comp.last(t).RolePatterns, "core:metrics.read", "core:self", "core:system.status", "core:tasks.view", "gotham:patrol", "gotham:signal")

	f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{"custom.ghost", "custom.phantom"}})
	if v := gauge(t, p, "mediacore_rbac_unknown_roles_total", nil); v != 2 {
		t.Errorf("unknown_roles_total = %v, want 2", v)
	}
	if v := gauge(t, p, "mediacore_rbac_checker_cache_total", map[string]string{"result": "miss"}); v != 7 {
		t.Errorf("cache misses = %v, want 7", v)
	}
	if v := gauge(t, p, "mediacore_rbac_checker_cache_total", map[string]string{"result": "hit"}); v != 2 {
		t.Errorf("cache hits = %v, want 2", v)
	}

	// A bounded cache evicts and rebuilds.
	small := boot(t, newEnv(t), setup{modules: []module{gotham()}, cacheSize: 1})
	small.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleViewer}})
	for _, role := range []rbac.RoleName{rbac.RoleEditor, rbac.RoleAdmin, "gotham.detective", "relay-worker", "custom.a", "custom.b", "custom.c", "custom.d", "custom.e", "custom.f", "custom.g", "custom.h", "custom.i", "custom.j", "custom.k", "custom.l", "custom.m", "custom.n", "custom.o", "custom.p", "custom.q"} {
		small.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{role}})
	}
	n := small.comp.calls()
	small.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleViewer}})
	small.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleEditor}})
	if small.comp.calls() == n {
		t.Error("a cache of one entry per shard kept every checker")
	}
}

// TestCompile_ResolvesRolePatterns (05-rbac §3.4 CompileInput): Service.Compile
// hands Deps.Compile the union of the known roles' patterns, deduplicated
// and sorted, the caller's input unchanged, and skips unknown roles.
func TestCompile_ResolvesRolePatterns(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	f.mustCreate("custom.oracle", "gotham:signal", "gotham:patrol.*", "core:self")
	in := rbac.CheckerInput{Roles: []rbac.RoleName{"custom.oracle", "custom.ghost", rbac.RoleViewer}, Scopes: perms("gotham:signal"), Interactive: true}
	f.svc.Compile(in)
	got := f.comp.last(t)
	wantPerms(t, "RolePatterns", got.RolePatterns, "core:metrics.read", "core:self", "core:system.status", "core:tasks.view", "gotham:patrol", "gotham:patrol.*", "gotham:signal")
	if !slices.Equal(got.Roles, in.Roles) || !slices.Equal(got.Scopes, in.Scopes) || !got.Interactive {
		t.Errorf("CheckerInput = %+v, want %+v unchanged", got.CheckerInput, in)
	}
	in.Roles[0], in.Scopes[0] = "custom.mutated", "gotham:mutated"
	if got.Roles[0] != "custom.oracle" || got.Scopes[0] != "gotham:signal" {
		t.Error("Deps.Compile shares the caller's slices")
	}
	f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}})
	wantPerms(t, "admin patterns", f.comp.last(t).RolePatterns, "core:admin")
	f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{"custom.ghost"}})
	wantPerms(t, "unknown role patterns", f.comp.last(t).RolePatterns)
}

// TestCompile_FailsClosed: before Freeze, for key scopes §4.5 forbids, and
// for a nil checker from Deps.Compile, Compile returns a checker holding
// nothing, and caches none of them.
func TestCompile_FailsClosed(t *testing.T) {
	f := build(t, newEnv(t), setup{})
	c := f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}})
	if c == nil || c.Has(rbac.CoreAdmin) || c.Satisfies(rbac.Public) || !c.Confined() || len(c.Concrete()) != 0 {
		t.Error("Compile before Freeze grants something")
	}
	if f.comp.calls() != 0 {
		t.Error("Compile before Freeze called Deps.Compile")
	}
	f.declare(gotham())
	if err := f.svc.Freeze(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CommitSeeds(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, scopes := range map[string][]rbac.Permission{
		"empty key":     {},
		"wildcard key":  perms("gotham:*"),
		"action key":    perms("gotham:patrol.*"),
		"malformed key": perms("gotham:Patrol"),
		"mixed key":     perms("gotham:signal", "core:*"),
	} {
		c := f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Scopes: scopes})
		if c == nil || c.Has(rbac.CoreAdmin) || c.Has("gotham:signal") {
			t.Errorf("%s: Compile granted something", name)
		}
	}
	if f.comp.calls() != 0 {
		t.Errorf("Deps.Compile called %d times for forbidden keys", f.comp.calls())
	}
	f.comp.nilOut = true
	for range 2 {
		c := f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}})
		if c == nil || c.Has(rbac.CoreAdmin) {
			t.Error("a nil checker from Deps.Compile became a grant")
		}
	}
	if f.comp.calls() != 2 {
		t.Errorf("Deps.Compile calls = %d, want 2 (a nil checker is not cached)", f.comp.calls())
	}
}

// TestWildcard_RolesAllowedKeysRejected (§4.2, §4.5): a role may hold a
// wildcard; an API key may not, nor an empty list, nor an unknown scope.
func TestWildcard_RolesAllowedKeysRejected(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	r := f.mustCreate("custom.robin", "gotham:*", "gotham:patrol.*")
	wantPerms(t, "role", r.Permissions(), "gotham:*", "gotham:patrol.*")
	cat := f.cat()
	for _, tc := range []struct {
		scopes []rbac.Permission
		want   error
	}{
		{perms("gotham:*"), rbac.ErrWildcardInKey},
		{perms("gotham:patrol.*"), rbac.ErrWildcardInKey},
		{perms("gotham:signal", "core:*"), rbac.ErrWildcardInKey},
		{nil, rbac.ErrBadScope},
		{perms(), rbac.ErrBadScope},
		{perms("gotham:ghost"), rbac.ErrUnknownScope},
		{perms("gotham:Patrol"), rbac.ErrBadScope},
	} {
		errIs(t, "CheckKeyScopes("+joinPerms(tc.scopes)+")", rbac.CheckKeyScopes(cat, tc.scopes), tc.want)
	}
	if err := rbac.CheckKeyScopes(cat, perms("gotham:signal", "core:self", "gotham:relay.worker")); err != nil {
		t.Errorf("concrete key scopes: %v", err)
	}
	errIs(t, "CheckKeyScopes without a catalog", rbac.CheckKeyScopes(nil, perms("core:self")), rbac.ErrUnknownScope)
}

// TestScopeGrammar_Table (§4.1, §4.2): what parses as a scope, as a pattern,
// as a role permission and as a key scope.
func TestScopeGrammar_Table(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	cat := f.cat()
	cases := []struct {
		in             string
		scope, pattern bool
		role, key      bool // accepted by CheckRolePermissions / CheckKeyScopes
	}{
		{"gotham:signal", true, true, true, true},
		{"gotham:patrol.night", true, true, true, true},
		{"core:admin", true, true, true, true},
		{"gotham:*", false, true, true, false},
		{"gotham:patrol.*", false, true, true, false},
		{"core:*", false, true, true, false},
		{"gotham:ghost", true, true, false, false},
		{"arkham:*", false, true, false, false},
		{"gotham:signal.*", false, true, true, false}, // the bare action matches (§4.2)
		{"gotham:ghost.*", false, true, false, false},
		{"*:signal", false, false, false, false},
		{"*", false, false, false, false},
		{"gotham", false, false, false, false},
		{"gotham:", false, false, false, false},
		{":signal", false, false, false, false},
		{"Gotham:signal", false, false, false, false},
		{"gotham:Signal", false, false, false, false},
		{"gotham:signal.", false, false, false, false},
		{"gotham:a.b.c", false, false, false, false},
		{"gotham:*.night", false, false, false, false},
		{"gotham:patrol.**", false, false, false, false},
		{"gotham :signal", false, false, false, false},
		{"", false, false, false, false},
	}
	for _, tc := range cases {
		_, serr := rbac.ParsePermission(tc.in)
		_, perr := rbac.ParsePattern(tc.in)
		rerr := rbac.CheckRolePermissions(cat, perms(tc.in))
		kerr := rbac.CheckKeyScopes(cat, perms(tc.in))
		if (serr == nil) != tc.scope || (perr == nil) != tc.pattern || (rerr == nil) != tc.role || (kerr == nil) != tc.key {
			t.Errorf("%q: scope %v pattern %v role %v key %v; want %v %v %v %v",
				tc.in, serr == nil, perr == nil, rerr == nil, kerr == nil, tc.scope, tc.pattern, tc.role, tc.key)
		}
	}
}

// FuzzParsePermission: the grammar's invariants hold for any input.
func FuzzParsePermission(f *testing.F) {
	for _, s := range []string{"core:admin", "gotham:patrol.night", "gotham:*", "gotham:patrol.*", "*", ":", "a:b.c.d", "gotham:Signal", "x:y", "core:*"} {
		f.Add(s)
	}
	specs := append(rbac.CoreScopes(), rbac.PermissionSpec{Name: "gotham:patrol"}, rbac.PermissionSpec{Name: "gotham:patrol.night"})
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, serr := rbac.ParsePermission(s)
		pat, perr := rbac.ParsePattern(s)
		if serr == nil {
			if perr != nil || string(p) != s || p.IsWildcard() || p.Namespace() == "" || p.Action() == "" {
				t.Fatalf("%q: scope %q, pattern err %v", s, p, perr)
			}
		}
		if perr == nil && pat.IsWildcard() && serr == nil {
			t.Fatalf("%q parses as both a wildcard and a scope", s)
		}
		if serr != nil && !errors.Is(serr, rbac.ErrBadScope) {
			t.Fatalf("%q: error %v does not wrap ErrBadScope", s, serr)
		}
		if rbac.CheckKeyScopes(cat, perms(s)) == nil {
			if serr != nil {
				t.Fatalf("%q: a key scope that is no scope", s)
			}
			if _, ok := cat.Lookup(p); !ok {
				t.Fatalf("%q: a key scope the catalog lacks", s)
			}
		}
		if exp := cat.Expand(rbac.Permission(s)); len(exp) > 0 && perr != nil {
			t.Fatalf("%q expands to %v but is no pattern", s, exp)
		}
	})
}

// TestRequire_DeniesByDefault (§3.4, §4.10): no checker, a malformed or
// wildcard scope, an empty list, and a checker lax on only one of Has and
// Satisfies all deny; a bound service records each denial.
func TestRequire_DeniesByDefault(t *testing.T) {
	p, sc := rbacScope(t)
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}, metrics: sc})
	cat := f.cat()
	bare := context.Background()
	if rbac.Can(bare, "gotham:signal") {
		t.Error("Can without a checker")
	}
	errIs(t, "Require without a checker", rbac.Require(bare, "gotham:signal"), rbac.ErrDenied)
	if _, ok := rbac.CheckerFrom(rbac.WithChecker(bare, nil)); ok {
		t.Error("a nil checker is bound")
	}

	ctx := f.svc.Bind(bare, grant(cat, true, "gotham:signal", "gotham:patrol"))
	if !rbac.Can(ctx, "gotham:signal") || rbac.Require(ctx, "gotham:signal") != nil || rbac.RequireAll(ctx, "gotham:signal", "gotham:patrol") != nil {
		t.Error("a held scope is denied")
	}
	for _, s := range []rbac.Permission{"gotham:cave.manage", "gotham:*", "gotham:Signal", ""} {
		if rbac.Can(ctx, s) {
			t.Errorf("Can(%q) holds", s)
		}
		errIs(t, "Require("+string(s)+")", rbac.Require(ctx, s), rbac.ErrDenied)
	}
	errIs(t, "RequireAll()", rbac.RequireAll(ctx), rbac.ErrDenied)
	errIs(t, "RequireAll(held, unheld)", rbac.RequireAll(ctx, "gotham:signal", "gotham:cave.manage"), rbac.ErrDenied)
	errIs(t, "RequireAll(held, malformed)", rbac.RequireAll(ctx, "gotham:signal", "gotham:Signal"), rbac.ErrDenied)

	for name, c := range map[string]rbac.Checker{
		"Has only":       halfChecker{has: true},
		"Satisfies only": halfChecker{satisfies: true},
	} {
		if rbac.Can(rbac.WithChecker(bare, c), "gotham:signal") {
			t.Errorf("%s checker passes Can", name)
		}
	}

	d := f.audit.denials()
	if len(d) == 0 || d[0].Action != "core.permission.deny" || d[0].Permission != "gotham:cave.manage" || d[0].Outcome != "denied" {
		t.Errorf("denials = %+v, want core.permission.deny of gotham:cave.manage first", d)
	}
	if v := gauge(t, p, "mediacore_rbac_denials_total", map[string]string{"owner": "gotham", "scope": "gotham:cave.manage"}); v != 2 {
		t.Errorf("denials{gotham,cave.manage} = %v, want 2", v)
	}
	if v := gauge(t, p, "mediacore_rbac_denials_total", map[string]string{"owner": "unknown", "scope": "unknown"}); v < 3 {
		t.Errorf("denials{unknown} = %v, want the malformed and wildcard scopes folded there", v)
	}
	unbound := len(f.audit.denials())
	errIs(t, "Require on an unbound context", rbac.Require(rbac.WithChecker(bare, grant(cat, true)), "gotham:signal"), rbac.ErrDenied)
	if len(f.audit.denials()) != unbound {
		t.Error("an unbound context recorded a denial")
	}
}

// TestConfined_AdminNeverConfined (§4.4): a set granting core:admin is never
// confined, whatever catalog it is judged against, and an empty set is not
// confined either.
func TestConfined_AdminNeverConfined(t *testing.T) {
	bare, err := rbac.NewCatalog(rbac.PermissionSpec{Name: "books:fingerprint.worker", Confining: true}, rbac.PermissionSpec{Name: "books:view"})
	if err != nil {
		t.Fatal(err)
	}
	full, err := rbac.NewCatalog(append(rbac.CoreScopes(), rbac.PermissionSpec{Name: "books:fingerprint.worker", Confining: true})...)
	if err != nil {
		t.Fatal(err)
	}
	for _, cat := range []*rbac.Catalog{bare, full} {
		for _, ps := range [][]rbac.Permission{
			perms("core:admin", "books:fingerprint.worker"),
			perms("core:*", "books:fingerprint.worker"),
			perms("core:admin.*", "books:fingerprint.worker"),
			nil,
		} {
			if rbac.Confined(cat, ps) {
				t.Errorf("Confined(%v) holds", ps)
			}
		}
		if !rbac.Confined(cat, perms("books:fingerprint.worker")) {
			t.Error("the worker scope alone is not confined")
		}
	}
	if rbac.Confined(nil, perms("books:fingerprint.worker")) {
		t.Error("Confined without a catalog holds")
	}
	if rbac.Confined(bare, perms("books:fingerprint.worker", "books:view")) {
		t.Error("a mixed set is confined")
	}
}

// halfChecker answers Has and Satisfies as configured.
type halfChecker struct{ has, satisfies bool }

func (h halfChecker) Has(rbac.Permission) bool        { return h.has }
func (h halfChecker) Satisfies(rbac.Requirement) bool { return h.satisfies }
func (h halfChecker) Confined() bool                  { return false }
func (h halfChecker) Concrete() []rbac.Permission     { return nil }

// TestRBACSeries (§11): the rbac scope serves its six families with the
// catalog-bounded labels.
func TestRBACSeries(t *testing.T) {
	p, sc := rbacScope(t)
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}, metrics: sc})
	f.mustCreate("custom.oracle", "gotham:signal")
	f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{"custom.ghost"}})
	errIs(t, "Require", rbac.Require(f.svc.Bind(context.Background(), grant(f.cat(), true)), "gotham:signal"), rbac.ErrDenied)
	for _, c := range []struct {
		family string
		labels map[string]string
		want   float64
	}{
		{"mediacore_rbac_denials_total", map[string]string{"owner": "gotham", "scope": "gotham:signal"}, 1},
		{"mediacore_rbac_checker_cache_total", map[string]string{"result": "miss"}, 1},
		{"mediacore_rbac_checker_cache_total", map[string]string{"result": "hit"}, 0},
		{"mediacore_rbac_unknown_roles_total", nil, 1},
		{"mediacore_rbac_generation", nil, float64(f.svc.Generation())},
		{"mediacore_rbac_roles", map[string]string{"origin": "seed"}, 5},
		{"mediacore_rbac_roles", map[string]string{"origin": "custom"}, 1},
		{"mediacore_rbac_pending_seed_roles", nil, 0},
	} {
		if v := gauge(t, p, c.family, c.labels); v != c.want {
			t.Errorf("%s%v = %v, want %v", c.family, c.labels, v, c.want)
		}
	}
}

// rbacScope builds a Provider and the core "rbac" scope with the labels the
// service uses.
func rbacScope(t *testing.T) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	sc, err := p.Core("rbac", "owner", "scope", "result", "origin")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// gauge is the value of family's series whose labels include want; fatal
// when there is none.
func gauge(t *testing.T, p *metrics.Provider, family string, want map[string]string) float64 {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if labels[k] != v {
					continue next
				}
			}
			return sample(m)
		}
	}
	t.Fatalf("no %s series with labels %v", family, want)
	return 0
}

func sample(m *dto.Metric) float64 {
	switch {
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	}
	return m.GetUntyped().GetValue()
}

// joinPerms renders a permission list for a message.
func joinPerms(ps []rbac.Permission) string {
	out := ""
	for i, p := range ps {
		if i > 0 {
			out += ","
		}
		out += string(p)
	}
	return "[" + out + "]"
}
