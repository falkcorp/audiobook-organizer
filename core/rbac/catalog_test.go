// file: core/rbac/catalog_test.go
// version: 1.0.0
// guid: fc5a6c12-85ef-4e30-9f86-f40703839634
// last-edited: 2026-11-10

package rbac

import (
	"errors"
	"slices"
	"testing"
)

// wantCore is 05-rbac §6.1 "New permissions with no atom", the core scopes of
// the atom table and the editor/viewer seeds of §4.8, with each scope's class
// and flags. It is written out here, apart from CoreScopes, so a change to the
// catalog has to be made twice.
var wantCore = []struct {
	name         Permission
	class        PermissionClass
	sensitive    bool
	authRequired bool
	implies      []Permission
	grants       []RoleName
}{
	{name: "core:admin", authRequired: true},
	{name: "core:self", grants: []RoleName{RoleEditor, RoleViewer}},
	{name: "core:roles.view", class: ClassView},
	{name: "core:roles.manage", sensitive: true},
	{name: "core:users.view", class: ClassView},
	{name: "core:users.manage", implies: []Permission{"core:users.view"}},
	{name: "core:audit.view", class: ClassView, authRequired: true},
	{name: "core:system.status", class: ClassView, grants: []RoleName{RoleEditor, RoleViewer}},
	{name: "core:system.view", class: ClassView},
	{name: "core:system.manage", authRequired: true},
	{name: "core:backup.restore", sensitive: true, authRequired: true},
	{name: "core:integrations.read", class: ClassView},
	{name: "core:integrations.manage", implies: []Permission{"core:integrations.read"}},
	{name: "core:tasks.view", class: ClassView, grants: []RoleName{RoleEditor, RoleViewer}},
	{name: "core:tasks.manage"},
	{name: "core:metrics.read", class: ClassView, grants: []RoleName{RoleEditor, RoleViewer}},
	{name: "core:library.manage"},
	{name: "core:library.browse"},
	{name: "core:settings.manage"},
}

// atomTableCore is every core scope 05-rbac §6.1's atom table writes, plus the
// editor/viewer seeds of §4.8. All of them must be in the catalog.
var atomTableCore = []Permission{
	"core:tasks.view", "core:integrations.manage", "core:integrations.read",
	"core:users.manage", "core:users.view", "core:roles.view",
	"core:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse",
	"core:admin",
	// the four core seeds of §4.8's editor/viewer row
	"core:self", "core:tasks.view", "core:system.status", "core:metrics.read",
}

// TestCoreCatalog_FixedFromF2 is 05-rbac §12's gate (IMPL-4): the core scope
// list is fixed from F2, with the class and flags §6.1 states, whichever core
// subsystems are wired.
func TestCoreCatalog_FixedFromF2(t *testing.T) {
	specs := CoreScopes()
	if got, want := len(specs), len(wantCore); got != want {
		t.Fatalf("CoreScopes has %d specs, want %d", got, want)
	}
	byName := map[Permission]PermissionSpec{}
	for _, s := range specs {
		if _, dup := byName[s.Name]; dup {
			t.Fatalf("CoreScopes lists %s twice", s.Name)
		}
		byName[s.Name] = s
	}

	for _, w := range wantCore {
		s, ok := byName[w.name]
		if !ok {
			t.Errorf("CoreScopes lacks %s", w.name)
			continue
		}
		if s.Class != w.class {
			t.Errorf("%s: Class = %d, want %d", w.name, s.Class, w.class)
		}
		if s.Sensitive != w.sensitive || s.AuthRequired != w.authRequired {
			t.Errorf("%s: Sensitive/AuthRequired = %t/%t, want %t/%t",
				w.name, s.Sensitive, s.AuthRequired, w.sensitive, w.authRequired)
		}
		if s.Confining || s.Warn || s.Deprecated != "" {
			t.Errorf("%s: Confining/Warn/Deprecated = %t/%t/%q, want none", w.name, s.Confining, s.Warn, s.Deprecated)
		}
		if !slices.Equal(s.Implies, w.implies) {
			t.Errorf("%s: Implies = %v, want %v", w.name, s.Implies, w.implies)
		}
		if !slices.Equal(s.DefaultGrants, w.grants) {
			t.Errorf("%s: DefaultGrants = %v, want %v", w.name, s.DefaultGrants, w.grants)
		}
		if s.Title == "" || s.Description == "" {
			t.Errorf("%s: Title and Description must be set", w.name)
		}
		delete(byName, w.name)
	}
	for name := range byName {
		t.Errorf("CoreScopes holds %s, which the fixed list does not", name)
	}

	for _, p := range atomTableCore {
		if !slices.ContainsFunc(specs, func(s PermissionSpec) bool { return s.Name == p }) {
			t.Errorf("atom-table / seed scope %s is not in CoreScopes", p)
		}
	}

	// Dropped by 05-rbac §6.1's notes: never declared.
	for _, dropped := range []Permission{"core:events.subscribe", "core:tasks.run", "core:library"} {
		if slices.ContainsFunc(specs, func(s PermissionSpec) bool { return s.Name == dropped }) {
			t.Errorf("CoreScopes declares %s, which 05-rbac drops", dropped)
		}
	}

	// Only the four seeds of §4.8 carry DefaultGrants.
	var granted []Permission
	for _, s := range specs {
		if len(s.DefaultGrants) > 0 {
			granted = append(granted, s.Name)
		}
	}
	slices.Sort(granted)
	wantGranted := []Permission{"core:metrics.read", "core:self", "core:system.status", "core:tasks.view"}
	if !slices.Equal(granted, wantGranted) {
		t.Errorf("scopes with DefaultGrants = %v, want %v", granted, wantGranted)
	}

	c, err := NewCatalog(specs...)
	if err != nil {
		t.Fatalf("NewCatalog(CoreScopes()...): %v", err)
	}
	if n := len(slices.Collect(c.Namespace("core"))); n != len(wantCore) {
		t.Errorf("catalog core namespace holds %d scopes, want %d", n, len(wantCore))
	}
}

// TestCoreScopes_FreshSlice: no package-level mutable state (R5); mutating a
// result changes neither the next call nor a catalog built earlier.
func TestCoreScopes_FreshSlice(t *testing.T) {
	c, err := NewCatalog(CoreScopes()...)
	if err != nil {
		t.Fatal(err)
	}
	a := CoreScopes()
	for i := range a {
		a[i].Title = "mutated"
		a[i].DefaultGrants = append(a[i].DefaultGrants, "admin")
		if len(a[i].Implies) > 0 {
			a[i].Implies[0] = "core:admin"
		}
	}
	for _, s := range CoreScopes() {
		if s.Title == "mutated" || slices.Contains(s.DefaultGrants, "admin") || slices.Contains(s.Implies, "core:admin") {
			t.Fatalf("CoreScopes shares state between calls: %s", s.Name)
		}
	}
	got, ok := c.Lookup("core:users.manage")
	if !ok || got.Title == "mutated" || !slices.Equal(got.Implies, []Permission{"core:users.view"}) {
		t.Fatalf("a built catalog shares state with its input: %+v", got)
	}
}

func batSpec(name Permission, implies ...Permission) PermissionSpec {
	return PermissionSpec{Name: name, Title: string(name), Implies: implies}
}

func TestCatalog_RejectsDuplicate(t *testing.T) {
	_, err := NewCatalog(batSpec("batcave:view"), batSpec("batcave:edit"), batSpec("batcave:view"))
	if !errors.Is(err, ErrDuplicatePermission) {
		t.Fatalf("err = %v, want ErrDuplicatePermission", err)
	}
}

func TestCatalog_RejectsBadName(t *testing.T) {
	for _, name := range []Permission{"", "batcave", "batcave:*", "batcave:edit.*", "Batcave:view", "batcave:edit.a.b"} {
		if _, err := NewCatalog(batSpec(name)); !errors.Is(err, ErrBadScope) {
			t.Errorf("NewCatalog(%q) err = %v, want ErrBadScope", name, err)
		}
	}
}

func TestCatalog_RejectsForeignImplies(t *testing.T) {
	_, err := NewCatalog(batSpec("batcave:edit", "gotham:view"), batSpec("gotham:view"))
	if !errors.Is(err, ErrForeignNamespace) {
		t.Fatalf("err = %v, want ErrForeignNamespace", err)
	}
	// core:admin's implication of everything is a rule (§4.3), not a list entry.
	_, err = NewCatalog(batSpec("batcave:edit", "core:admin"), batSpec("core:admin"))
	if !errors.Is(err, ErrForeignNamespace) {
		t.Fatalf("implying core:admin: err = %v, want ErrForeignNamespace", err)
	}
}

func TestCatalog_RejectsUnknownImplies(t *testing.T) {
	_, err := NewCatalog(batSpec("batcave:edit", "batcave:view"))
	if !errors.Is(err, ErrUnknownScope) {
		t.Fatalf("err = %v, want ErrUnknownScope", err)
	}
	_, err = NewCatalog(batSpec("batcave:edit", "batcave:*"))
	if !errors.Is(err, ErrBadScope) {
		t.Fatalf("wildcard Implies: err = %v, want ErrBadScope", err)
	}
}

func TestCatalog_RejectsImpliesCycle(t *testing.T) {
	cases := map[string][]PermissionSpec{
		"self":  {batSpec("batcave:edit", "batcave:edit")},
		"pair":  {batSpec("batcave:a", "batcave:b"), batSpec("batcave:b", "batcave:a")},
		"three": {batSpec("batcave:a", "batcave:b"), batSpec("batcave:b", "batcave:c"), batSpec("batcave:c", "batcave:a")},
	}
	for name, specs := range cases {
		if _, err := NewCatalog(specs...); !errors.Is(err, ErrImpliesCycle) {
			t.Errorf("%s: err = %v, want ErrImpliesCycle", name, err)
		}
	}
	// A diamond is not a cycle.
	_, err := NewCatalog(
		batSpec("batcave:a", "batcave:b", "batcave:c"),
		batSpec("batcave:b", "batcave:d"),
		batSpec("batcave:c", "batcave:d"),
		batSpec("batcave:d"),
	)
	if err != nil {
		t.Fatalf("diamond rejected: %v", err)
	}
}

func TestCatalog_ConfiningImpliesNothing(t *testing.T) {
	confining := PermissionSpec{Name: "batcave:signal", Confining: true}
	// implies something
	withImplies := confining
	withImplies.Implies = []Permission{"batcave:view"}
	if _, err := NewCatalog(withImplies, batSpec("batcave:view")); !errors.Is(err, ErrConfiningImplication) {
		t.Errorf("confining scope with Implies: err = %v, want ErrConfiningImplication", err)
	}
	// is implied
	if _, err := NewCatalog(confining, batSpec("batcave:edit", "batcave:signal")); !errors.Is(err, ErrConfiningImplication) {
		t.Errorf("implied confining scope: err = %v, want ErrConfiningImplication", err)
	}
	if _, err := NewCatalog(confining, batSpec("batcave:view")); err != nil {
		t.Errorf("plain confining scope rejected: %v", err)
	}
}

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := NewCatalog(
		batSpec("core:users.view"),
		batSpec("core:users.manage", "core:users.view"),
		batSpec("core:users"),
		batSpec("core:self"),
		batSpec("batcave:view"),
		batSpec("batcave:edit", "batcave:view"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCatalog_ExpandPattern(t *testing.T) {
	c := testCatalog(t)
	tests := []struct {
		pattern Permission
		want    []Permission
	}{
		// the bare action is part of "<action>.*" (05-rbac §4.2)
		{"core:users.*", []Permission{"core:users", "core:users.manage", "core:users.view"}},
		{"core:*", []Permission{"core:self", "core:users", "core:users.manage", "core:users.view"}},
		{"batcave:*", []Permission{"batcave:edit", "batcave:view"}},
		{"core:self", []Permission{"core:self"}},
		{"core:nothing", nil},
		{"core:nothing.*", nil},
		{"tv:*", nil},
		{"not a scope", nil},
		{"*:*", nil},
	}
	for _, tc := range tests {
		if got := c.Expand(tc.pattern); !slices.Equal(got, tc.want) {
			t.Errorf("Expand(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

func TestCatalog_LookupAndNamespace(t *testing.T) {
	c := testCatalog(t)

	got, ok := c.Lookup("core:users.manage")
	if !ok || got.Name != "core:users.manage" || !slices.Equal(got.Implies, []Permission{"core:users.view"}) {
		t.Fatalf("Lookup(core:users.manage) = %+v, %t", got, ok)
	}
	if _, ok := c.Lookup("core:missing"); ok {
		t.Error("Lookup of an undeclared scope succeeded")
	}
	if _, ok := c.Lookup("core:users.*"); ok {
		t.Error("Lookup of a wildcard succeeded; only Expand takes patterns")
	}

	// Lookup hands out a copy.
	got.Implies[0] = "core:self"
	again, _ := c.Lookup("core:users.manage")
	if !slices.Equal(again.Implies, []Permission{"core:users.view"}) {
		t.Errorf("Lookup result aliases catalog state: %v", again.Implies)
	}

	names := func(seq func(func(PermissionSpec) bool)) []Permission {
		var out []Permission
		for s := range seq {
			out = append(out, s.Name)
		}
		return out
	}
	if got, want := names(c.Namespace("batcave")), []Permission{"batcave:edit", "batcave:view"}; !slices.Equal(got, want) {
		t.Errorf("Namespace(batcave) = %v, want %v", got, want)
	}
	if got := names(c.Namespace("tv")); len(got) != 0 {
		t.Errorf("Namespace(tv) = %v, want none", got)
	}
	if got := len(names(c.All())); got != 6 {
		t.Errorf("All yields %d specs, want 6", got)
	}
	// All is name-ordered, so catalog listings are deterministic.
	if all := names(c.All()); !slices.IsSorted(all) {
		t.Errorf("All is not name-ordered: %v", all)
	}
	// Breaking out of an iteration must not panic or hang.
	for range c.All() {
		break
	}
}
