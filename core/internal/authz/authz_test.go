// file: core/internal/authz/authz_test.go
// version: 1.0.0
// guid: 25ae54fc-a0cc-4987-aa9e-3573a03acd0a
// last-edited: 2026-11-10

package authz

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

// perms is shorthand for a scope list.
func perms(s ...string) []rbac.Permission {
	out := make([]rbac.Permission, len(s))
	for i, p := range s {
		out[i] = rbac.Permission(p)
	}
	return out
}

// booksScopes is a books module's slice of the catalog, enough for the
// checker cells: an implied pair, a sub-action, the Confining worker scope
// and an AuthRequired debug scope.
func booksScopes() []rbac.PermissionSpec {
	return []rbac.PermissionSpec{
		{Name: "books:view", Class: rbac.ClassView},
		{Name: "books:edit", Implies: perms("books:view")},
		{Name: "books:edit.metadata"},
		{Name: "books:scan"},
		{Name: "books:reading"},
		{Name: "books:organize"},
		{Name: "books:fingerprint.worker", Confining: true},
		{Name: "books:admin.debug", AuthRequired: true},
	}
}

// testCatalog is core's fixed scopes plus booksScopes.
func testCatalog(t testing.TB) *rbac.Catalog {
	t.Helper()
	cat, err := rbac.NewCatalog(append(rbac.CoreScopes(), booksScopes()...)...)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// session is a session's CompileInput (no key scopes).
func session(interactive bool, patterns ...string) rbac.CompileInput {
	return rbac.CompileInput{
		Roles: []rbac.RoleName{"gotham"}, Interactive: interactive,
		RolePatterns: perms(patterns...),
	}
}

// key is an API key's CompileInput: non-interactive, with key scopes.
func key(scopes []string, patterns ...string) rbac.CompileInput {
	return rbac.CompileInput{
		Roles: []rbac.RoleName{"gotham"}, Scopes: perms(scopes...),
		RolePatterns: perms(patterns...),
	}
}

// vector is one case of core/rbac/testdata/match_vectors.json.
type vector struct {
	Name    string `json:"name"`
	Catalog []struct {
		Name         string   `json:"name"`
		Sensitive    bool     `json:"sensitive"`
		AuthRequired bool     `json:"auth_required"`
		Confining    bool     `json:"confining"`
		Implies      []string `json:"implies"`
	} `json:"catalog"`
	Roles       []string `json:"roles"`
	KeyScopes   []string `json:"key_scopes"`
	Interactive bool     `json:"interactive"`
	Check       struct {
		Op          string `json:"op"`
		Scope       string `json:"scope"`
		Requirement struct {
			Kind   string   `json:"kind"`
			Scopes []string `json:"scopes"`
		} `json:"requirement"`
	} `json:"check"`
	Expected *bool `json:"expected"`
}

// requirement builds the vector's requirement from its kind name.
func (v vector) requirement(t *testing.T) rbac.Requirement {
	t.Helper()
	r := v.Check.Requirement
	switch r.Kind {
	case "public":
		return rbac.Public
	case "authenticated":
		return rbac.AnyAuthenticated
	case "all_of":
		return rbac.AllOf(perms(r.Scopes...)...)
	case "any_of":
		return rbac.AnyOf(perms(r.Scopes...)...)
	case "delegated":
		return rbac.Delegated("vector")
	case "invalid":
		return rbac.Requirement{}
	}
	t.Fatalf("unknown requirement kind %q", r.Kind)
	return rbac.Requirement{}
}

// TestChecker_MatchVectors runs every case of the shared matching vectors
// (05-rbac §4.10; also run by vitest in @mediacore/shell) through Compile.
func TestChecker_MatchVectors(t *testing.T) {
	// Relative to this package's directory, the test's working directory
	// (CI builds with -trimpath, so runtime.Caller paths are not usable).
	raw, err := os.ReadFile(filepath.Join("..", "..", "rbac", "testdata", "match_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 40 {
		t.Fatalf("%d vectors; the file was truncated", len(vectors))
	}
	ops := map[string]int{}
	names := map[string]bool{}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			if names[v.Name] {
				t.Fatalf("duplicate vector name %q", v.Name)
			}
			names[v.Name] = true
			if v.Expected == nil {
				t.Fatal("vector has no expected value")
			}
			var specs []rbac.PermissionSpec
			for _, s := range v.Catalog {
				specs = append(specs, rbac.PermissionSpec{
					Name: rbac.Permission(s.Name), Sensitive: s.Sensitive, AuthRequired: s.AuthRequired,
					Confining: s.Confining, Implies: perms(s.Implies...),
				})
			}
			cat, err := rbac.NewCatalog(specs...)
			if err != nil {
				t.Fatalf("vector catalog: %v", err)
			}
			in := rbac.CompileInput{
				Roles: []rbac.RoleName{"vector"}, Interactive: v.Interactive,
				RolePatterns: perms(v.Roles...),
			}
			if v.KeyScopes != nil {
				in.Scopes = perms(v.KeyScopes...)
			}
			c := Compile(in, cat)
			var got bool
			switch v.Check.Op {
			case "has":
				got = c.Has(rbac.Permission(v.Check.Scope))
			case "satisfies":
				got = c.Satisfies(v.requirement(t))
			case "confined":
				got = c.Confined()
			default:
				t.Fatalf("unknown op %q", v.Check.Op)
			}
			ops[v.Check.Op]++
			if got != *v.Expected {
				t.Errorf("got %v, want %v", got, *v.Expected)
			}
		})
	}
	for _, op := range []string{"has", "satisfies", "confined"} {
		if ops[op] == 0 {
			t.Errorf("no %q vector", op)
		}
	}
}

// TestCompile_KeyNarrowsRoles: a key's checker is role ∩ key after
// implication (05-rbac §4.4), a wildcard role included.
func TestCompile_KeyNarrowsRoles(t *testing.T) {
	cat := testCatalog(t)

	// A wildcard role and a concrete key: only the key's scope (and what it
	// implies) is held.
	c := Compile(key([]string{"books:edit"}, "books:*"), cat)
	for s, want := range map[string]bool{
		"books:edit": true, "books:view": true, // books:edit implies books:view
		"books:scan": false, "books:edit.metadata": false, "books:fingerprint.worker": false,
		"books:*": false, "books:edit.*": false, // a key holds no wildcard
	} {
		if got := c.Has(rbac.Permission(s)); got != want {
			t.Errorf("wildcard role ∩ [books:edit]: Has(%s) = %v, want %v", s, got, want)
		}
	}
	if got := c.Concrete(); !slices.Equal(got, perms("books:edit", "books:view")) {
		t.Errorf("Concrete() = %v, want [books:edit books:view]", got)
	}

	// The same role as a session holds the whole namespace and the pattern.
	s := Compile(session(true, "books:*"), cat)
	for _, p := range []string{"books:scan", "books:edit.metadata", "books:*", "books:edit.*"} {
		if !s.Has(rbac.Permission(p)) {
			t.Errorf("books:* session: Has(%s) = false", p)
		}
	}
	if s.Has("core:self") {
		t.Error("books:* session holds core:self")
	}

	// A key gains nothing its owner's roles lack, even through implication.
	n := Compile(key([]string{"books:edit", "books:scan"}, "books:view"), cat)
	if got := n.Concrete(); !slices.Equal(got, perms("books:view")) {
		t.Errorf("[books:view] role ∩ [books:edit books:scan] key: Concrete() = %v, want [books:view]", got)
	}

	// A key scope outside the catalog grants nothing.
	u := Compile(key([]string{"books:ghost"}, "books:*"), cat)
	if u.Has("books:ghost") || len(u.Concrete()) != 0 {
		t.Errorf("unknown key scope: Has = %v, Concrete = %v", u.Has("books:ghost"), u.Concrete())
	}

	// Malformed role patterns grant nothing.
	m := Compile(session(true, "Books:*", "books", "*:*"), cat)
	if len(m.Concrete()) != 0 || m.Has("books:view") {
		t.Errorf("malformed patterns granted %v", m.Concrete())
	}
}

// TestAdmin_IsScopeAndKeyScopingApplies is the checker cell of 05-rbac §12's
// regression for RequireAdmin: an admin user's books:view key does not
// satisfy Need(core:admin); the admin flag needs the key to name core:admin
// (SEC-12). The 403 route cell runs with the live mount (F4-08).
func TestAdmin_IsScopeAndKeyScopingApplies(t *testing.T) {
	cat := testCatalog(t)
	for _, admin := range []string{"core:admin", "core:*", "core:admin.*"} {
		scoped := Compile(key([]string{"books:view"}, admin), cat)
		if scoped.Satisfies(rbac.Need(rbac.CoreAdmin)) || scoped.Has(rbac.CoreAdmin) {
			t.Errorf("%s owner's books:view key satisfies core:admin", admin)
		}
		if !scoped.Has("books:view") || scoped.Has("books:edit") || scoped.Has("core:users.view") {
			t.Errorf("%s owner's books:view key: Concrete = %v, want [books:view]", admin, scoped.Concrete())
		}

		named := Compile(key([]string{"core:admin"}, admin), cat)
		if !named.Satisfies(rbac.Need(rbac.CoreAdmin)) || !named.Has("books:edit") || !named.Has("core:system.manage") {
			t.Errorf("%s owner's core:admin key is not admin", admin)
		}

		sess := Compile(session(true, admin), cat)
		if !sess.Satisfies(rbac.Need(rbac.CoreAdmin)) || !sess.Has("books:organize") {
			t.Errorf("%s session is not admin", admin)
		}
	}

	// A non-admin's key naming core:admin gets no admin and nothing from it.
	editor := Compile(key([]string{"core:admin", "books:view"}, "books:*", "core:self"), cat)
	if editor.Has(rbac.CoreAdmin) || editor.Satisfies(rbac.Need(rbac.CoreAdmin)) || editor.Has("books:edit") {
		t.Errorf("editor key naming core:admin: Concrete = %v", editor.Concrete())
	}
	if !editor.Has("books:view") {
		t.Error("editor key lost its books:view")
	}

	// Admin's reach stops at scopes the catalog does not declare.
	if Compile(session(true, "core:admin"), cat).Has("books:ghost") {
		t.Error("admin holds an undeclared scope")
	}
	// And Has of a pattern is literal: admin is not "holding" books:*.
	if Compile(session(true, "core:admin"), cat).Has("books:*") {
		t.Error("admin session holds the books:* pattern literally")
	}
}

// TestSensitive_EvaluatedBeforeAdmin: §4.4 rule 1 (SEC-23). A bootstrap-style
// ["core:admin"] key cannot write roles; an interactive admin session can.
func TestSensitive_EvaluatedBeforeAdmin(t *testing.T) {
	cat := testCatalog(t)
	bootstrap := Compile(key([]string{"core:admin"}, "core:admin"), cat)
	if bootstrap.Has("core:roles.manage") || bootstrap.Satisfies(rbac.Need("core:roles.manage")) {
		t.Error("bootstrap key holds core:roles.manage")
	}
	if bootstrap.Has("core:backup.restore") {
		t.Error("bootstrap key holds core:backup.restore")
	}
	if !bootstrap.Has(rbac.CoreAdmin) || !bootstrap.Has("core:users.manage") {
		t.Error("bootstrap key lost its non-Sensitive admin reach")
	}

	sess := Compile(session(true, "core:admin"), cat)
	if !sess.Has("core:roles.manage") || !sess.Satisfies(rbac.Need("core:roles.manage")) {
		t.Error("interactive admin session lacks core:roles.manage")
	}

	// The rule is not admin's alone: a non-interactive holder of the scope
	// itself fails it too, and through a wildcard pattern as well.
	if Compile(key([]string{"core:roles.manage"}, "core:roles.manage"), cat).Has("core:roles.manage") {
		t.Error("non-interactive key holds its Sensitive scope")
	}
	nonInteractive := Compile(session(false, "core:*"), cat)
	if nonInteractive.Has("core:*") || nonInteractive.Has("core:roles.manage") {
		t.Error("non-interactive core:* holder passes a Sensitive check")
	}
	if !Compile(session(true, "core:*"), cat).Has("core:*") {
		t.Error("interactive core:* session does not hold core:*")
	}
}

// TestAuthDisabled_CheckerHasNoAdmin: §4.11 (SEC-2, SEC-3).
func TestAuthDisabled_CheckerHasNoAdmin(t *testing.T) {
	cat := testCatalog(t)
	c := AuthDisabled(cat)
	for _, s := range []string{
		"core:admin", "core:system.manage", "core:audit.view", "core:backup.restore", // AuthRequired
		"core:roles.manage", "books:admin.debug", // Sensitive; a module AuthRequired scope
		"books:ghost", "books:*", "core:*", // undeclared, patterns
	} {
		if c.Has(rbac.Permission(s)) {
			t.Errorf("auth-disabled checker holds %s", s)
		}
	}
	if c.Satisfies(rbac.Need(rbac.CoreAdmin)) {
		t.Error("auth-disabled checker satisfies core:admin")
	}
	for _, s := range c.Concrete() {
		spec, ok := cat.Lookup(s)
		if !ok || spec.AuthRequired || spec.Sensitive || s == rbac.CoreAdmin {
			t.Errorf("auth-disabled Concrete() holds %s (declared %v, %+v)", s, ok, spec)
		}
	}
	want := 0
	for spec := range cat.All() {
		if !spec.AuthRequired && !spec.Sensitive {
			want++
			if !c.Has(spec.Name) {
				t.Errorf("auth-disabled checker lacks ordinary scope %s", spec.Name)
			}
		}
	}
	if got := len(c.Concrete()); got != want {
		t.Errorf("Concrete() holds %d scopes, want %d", got, want)
	}
	if c.Confined() {
		t.Error("auth-disabled checker is Confined()")
	}
	if !c.Satisfies(rbac.AnyAuthenticated) || !c.Satisfies(rbac.Need("books:fingerprint.worker")) {
		t.Error("auth-disabled checker lost AnyAuthenticated or a Confining scope")
	}
	ch := c.(*checker)
	if ch.admin || ch.interactive {
		t.Errorf("auth-disabled checker: admin=%v interactive=%v", ch.admin, ch.interactive)
	}

	// A catalog that forgot core:admin's AuthRequired flag still cannot hand
	// the auth-off principal admin; nor is it confined when every scope it
	// holds is Confining.
	odd, err := rbac.NewCatalog(
		rbac.PermissionSpec{Name: rbac.CoreAdmin},
		rbac.PermissionSpec{Name: "books:fingerprint.worker", Confining: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	o := AuthDisabled(odd)
	if o.Has(rbac.CoreAdmin) || o.Satisfies(rbac.Need(rbac.CoreAdmin)) || slices.Contains(o.Concrete(), rbac.CoreAdmin) {
		t.Error("auth-disabled checker holds an unflagged core:admin")
	}
	if o.Confined() {
		t.Error("auth-disabled checker over a Confining-only catalog is Confined()")
	}

	// No catalog: a refusal that holds nothing.
	n := AuthDisabled(nil)
	if n.Has("core:self") || n.Satisfies(rbac.AnyAuthenticated) || len(n.Concrete()) != 0 || !n.Confined() {
		t.Error("AuthDisabled(nil) is not the refusal checker")
	}
}

// TestAuthDisabled_AllOfDropsAuthRequired: the auth-off evaluation rule
// (§4.11). Only AuthDisabled applies it.
func TestAuthDisabled_AllOfDropsAuthRequired(t *testing.T) {
	cat := testCatalog(t)
	off := AuthDisabled(cat)
	seven := perms("core:settings.manage", "core:library.manage", "core:integrations.manage",
		"core:tasks.manage", "books:scan", "books:organize", "books:edit")
	settings := rbac.AllOf(append(slices.Clone(seven), "core:system.manage")...)
	if !off.Satisfies(settings) {
		t.Error("auth off: AllOf of seven ordinary scopes plus core:system.manage refused")
	}
	for _, r := range []rbac.Requirement{
		rbac.Need(rbac.CoreAdmin),
		rbac.Need("core:system.manage"),
		rbac.Need("books:admin.debug"),
		rbac.AllOf("core:system.manage", "core:audit.view", "core:backup.restore"),
		rbac.AnyOf("core:system.manage", "core:audit.view"),
	} {
		if off.Satisfies(r) {
			t.Errorf("auth off: %s (only AuthRequired scopes) satisfied", r)
		}
	}
	// The rule drops only AuthRequired members: an ordinary member it lacks
	// (a Sensitive one) still refuses, and so does an undeclared one.
	if off.Satisfies(rbac.AllOf("books:scan", "core:system.manage", "core:roles.manage")) {
		t.Error("auth off: AllOf with a Sensitive member satisfied")
	}
	if off.Satisfies(rbac.AllOf("books:scan", "core:system.manage", "books:ghost")) {
		t.Error("auth off: AllOf with an undeclared member satisfied")
	}

	// A Compile checker holding the seven does not get the rule.
	compiled := Compile(session(true, perms2s(seven)...), cat)
	if compiled.Satisfies(settings) {
		t.Error("a compiled checker holding seven of eight satisfies the AllOf")
	}
	if !compiled.Satisfies(rbac.AllOf(seven...)) {
		t.Error("a compiled checker holding the seven does not satisfy them")
	}
}

func perms2s(ps []rbac.Permission) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

// TestWorkerKey_Confined is this package's checker cells (05-rbac §12, R10-2,
// SEC10-1); F4-01's CompileInput cells live in rbac and F4-08 adds the route
// cells. Confinement is decided on the effective set, whoever owns the key.
func TestWorkerKey_Confined(t *testing.T) {
	cat := testCatalog(t)
	worker := rbac.Permission("books:fingerprint.worker")
	principals := map[string]rbac.CompileInput{
		"admin-owned worker key": key([]string{string(worker)}, "core:admin"),
		"fp-worker user's key":   key([]string{string(worker)}, string(worker)),
		"fp-worker session":      session(true, string(worker)),
		"wildcard worker role":   session(true, "books:fingerprint.*"),
	}
	for name, in := range principals {
		t.Run(name, func(t *testing.T) {
			c := Compile(in, cat)
			if !c.Confined() {
				t.Fatalf("Confined() = false; Concrete = %v", c.Concrete())
			}
			for _, r := range []rbac.Requirement{
				rbac.Need("core:self"), rbac.Need("core:metrics.read"), rbac.Need("books:reading"),
				rbac.AnyAuthenticated, rbac.AllOf(worker, "books:reading"),
			} {
				if c.Satisfies(r) {
					t.Errorf("confined principal satisfies %s", r)
				}
			}
			if !c.Satisfies(rbac.Need(worker)) || !c.Satisfies(rbac.AnyOf("books:reading", worker)) {
				t.Error("confined principal does not satisfy a requirement naming its scope")
			}
			if c.Has(rbac.CoreAdmin) {
				t.Error("confined principal holds core:admin")
			}
		})
	}

	// Not confined: an admin session, a worker beside a plain scope, an
	// empty effective set.
	for name, in := range map[string]rbac.CompileInput{
		"admin session":         session(true, "core:admin", string(worker)),
		"worker and reading":    session(true, string(worker), "books:reading"),
		"empty effective set":   key([]string{"books:scan"}, "books:view"),
		"admin key naming both": key([]string{"core:admin", string(worker)}, "core:admin"),
	} {
		if Compile(in, cat).Confined() {
			t.Errorf("%s is Confined()", name)
		}
	}
	if !Compile(session(true, string(worker), "books:reading"), cat).Satisfies(rbac.AnyAuthenticated) {
		t.Error("worker plus books:reading is not authenticated")
	}
	if Compile(key([]string{"books:scan"}, "books:view"), cat).Satisfies(rbac.AnyAuthenticated) {
		t.Error("an empty effective set is authenticated")
	}
}

// TestForSystem_HoldsExactlyGivenPerms: the system actor holds exactly its
// scopes (no implication, no wildcard), is non-interactive, has no admin
// flag, and holds nothing at all when handed a scope it must never hold.
func TestForSystem_HoldsExactlyGivenPerms(t *testing.T) {
	given := perms("books:scan", "books:edit", "core:tasks.view")
	c := ForSystem(given)
	for _, s := range given {
		if !c.Has(s) || !c.Satisfies(rbac.Need(s)) {
			t.Errorf("system actor lacks given scope %s", s)
		}
	}
	for _, s := range []string{"books:view", "books:edit.metadata", "books:*", "core:self", "core:admin", "Books:Scan"} {
		if c.Has(rbac.Permission(s)) {
			t.Errorf("system actor holds %s, which it was not given", s)
		}
	}
	if got := c.Concrete(); !slices.Equal(got, perms("books:edit", "books:scan", "core:tasks.view")) {
		t.Errorf("Concrete() = %v", got)
	}
	if !c.Satisfies(rbac.AllOf(given...)) || !c.Satisfies(rbac.AnyOf("books:view", "books:scan")) {
		t.Error("system actor fails a requirement over its scopes")
	}
	if c.Satisfies(rbac.AllOf("books:scan", "books:view")) {
		t.Error("system actor satisfies an AllOf naming a scope it lacks")
	}
	if c.Satisfies(rbac.AnyAuthenticated) || c.Satisfies(rbac.Delegated("gotham")) || c.Satisfies(rbac.Requirement{}) {
		t.Error("system actor satisfies AnyAuthenticated, Delegated or the zero requirement")
	}
	if !c.Satisfies(rbac.Public) || !c.Confined() {
		t.Error("system actor: Public refused or not Confined()")
	}
	ch := c.(*checker)
	if ch.admin || ch.interactive || ch.authOff {
		t.Errorf("system actor: admin=%v interactive=%v authOff=%v", ch.admin, ch.interactive, ch.authOff)
	}

	for name, bad := range map[string][]rbac.Permission{
		"core:admin":           perms("books:scan", "core:admin"),
		"AuthRequired core":    perms("books:scan", "core:system.manage"),
		"Sensitive core":       perms("books:scan", "core:roles.manage"),
		"undeclared core":      perms("books:scan", "core:batcave"),
		"wildcard":             perms("books:scan", "books:*"),
		"action wildcard":      perms("books:scan", "books:edit.*"),
		"malformed":            perms("books:scan", "Joker"),
		"core:* grants admin":  perms("core:*"),
		"empty string":         perms("books:scan", ""),
		"backup restore (A+S)": perms("core:backup.restore"),
	} {
		r := ForSystem(bad)
		if r.Has("books:scan") || len(r.Concrete()) != 0 || r.Satisfies(rbac.Need("books:scan")) || !r.Confined() {
			t.Errorf("%s: ForSystem(%v) holds %v, want nothing", name, bad, r.Concrete())
		}
	}
	if e := ForSystem(nil); len(e.Concrete()) != 0 || e.Satisfies(rbac.AnyAuthenticated) {
		t.Error("ForSystem(nil) holds something")
	}
}

// TestChecker_LegacyKindsRefused: no checker here evaluates a legacy atom
// requirement; books' atom table resolves it first (COR6-1).
func TestChecker_LegacyKindsRefused(t *testing.T) {
	cat := testCatalog(t)
	for name, c := range map[string]rbac.Checker{
		"admin session": Compile(session(true, "core:admin"), cat),
		"auth disabled": AuthDisabled(cat),
		"system actor":  ForSystem(perms("books:scan")),
	} {
		for _, r := range []rbac.Requirement{
			rbac.LegacyAtom("library.view"), rbac.LegacyCheck("library.view", true),
			rbac.LegacyCheck("library.view", false), rbac.Delegated("gotham"), {},
		} {
			if c.Satisfies(r) {
				t.Errorf("%s satisfies %s", name, r)
			}
		}
	}
}

// TestCompile_NilCatalogDeniesAll: Compile without a catalog holds nothing.
func TestCompile_NilCatalogDeniesAll(t *testing.T) {
	c := Compile(session(true, "core:admin"), nil)
	if c.Has(rbac.CoreAdmin) || c.Satisfies(rbac.Need(rbac.CoreAdmin)) || c.Satisfies(rbac.AnyAuthenticated) ||
		len(c.Concrete()) != 0 || !c.Confined() {
		t.Error("Compile(in, nil) is not the refusal checker")
	}
	if !c.Satisfies(rbac.Public) {
		t.Error("the refusal checker refuses Public")
	}
}

// TestChecker_Immutable: concurrent reads of one checker under -race, a
// Concrete() copy that does not alias, and inputs whose later mutation does
// not reach a built checker (05-rbac §9).
func TestChecker_Immutable(t *testing.T) {
	cat := testCatalog(t)
	in := session(false, "books:edit", "core:self", "books:fingerprint.*")
	sys := perms("books:scan")
	checkers := []rbac.Checker{Compile(in, cat), AuthDisabled(cat), ForSystem(sys)}
	before := make([][]rbac.Permission, len(checkers))
	for i, c := range checkers {
		before[i] = c.Concrete()
	}

	// Mutating the inputs after the build changes nothing.
	in.RolePatterns[0] = "core:admin"
	sys[0] = "core:admin"
	// Nor does mutating a returned Concrete().
	for _, c := range checkers {
		if got := c.Concrete(); len(got) > 0 {
			got[0] = "core:admin"
		}
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for _, c := range checkers {
				for range 50 {
					_ = c.Has("books:view")
					_ = c.Has("books:*")
					_ = c.Satisfies(rbac.AllOf("books:view", "core:system.manage"))
					_ = c.Satisfies(rbac.AnyAuthenticated)
					_ = c.Confined()
					_ = c.Concrete()
				}
			}
		})
	}
	wg.Wait()

	for i, c := range checkers {
		if got := c.Concrete(); !slices.Equal(got, before[i]) {
			t.Errorf("checker %d: Concrete() changed from %v to %v", i, before[i], got)
		}
		if c.Has(rbac.CoreAdmin) {
			t.Errorf("checker %d gained core:admin", i)
		}
	}
}
