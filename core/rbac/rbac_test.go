// file: core/rbac/rbac_test.go
// version: 1.0.0
// guid: 7075097a-01dc-44f0-8938-e0143d05800e
// last-edited: 2026-11-06

package rbac

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestParsePermission_Grammar: ns [a-z][a-z0-9]{1,15}, action and sub
// [a-z][a-z0-9_]{0,31}, one sub level, wildcards only through ParsePattern.
func TestParsePermission_Grammar(t *testing.T) {
	long := func(n int) string { return "a" + strings.Repeat("b", n-1) }

	valid := []struct{ in, ns, action string }{
		{"books:edit", "books", "edit"},
		{"books:edit.metadata", "books", "edit"},
		{"core:admin", "core", "admin"},
		{"ab:c", "ab", "c"},
		{"a1:b_2.c_3", "a1", "b_2"},
		{long(16) + ":" + long(32), long(16), long(32)},
		{"books:edit." + long(32), "books", "edit"},
	}
	for _, c := range valid {
		p, err := ParsePermission(c.in)
		if err != nil {
			t.Errorf("ParsePermission(%q): %v", c.in, err)
			continue
		}
		if string(p) != c.in || p.Namespace() != c.ns || p.Action() != c.action || p.IsWildcard() {
			t.Errorf("ParsePermission(%q) = %q ns=%q action=%q wildcard=%v", c.in, p, p.Namespace(), p.Action(), p.IsWildcard())
		}
		// The strict grammar is a subset of the pattern grammar.
		if q, err := ParsePattern(c.in); err != nil || q != p {
			t.Errorf("ParsePattern(%q) = %q, %v; want the same permission", c.in, q, err)
		}
	}

	invalid := []string{
		"",
		"books",                  // no colon
		":edit",                  // empty namespace
		"b:edit",                 // namespace too short
		long(17) + ":edit",       // namespace too long
		"Books:edit",             // upper case
		"1books:edit",            // starts with a digit
		"bo_oks:edit",            // underscore is not in the namespace class
		"books:",                 // empty action
		"books:Edit",             // upper case
		"books:1edit",            // starts with a digit
		"books:_edit",            // starts with an underscore
		"books:" + long(33),      // action too long
		"books:edit.",            // empty sub
		"books:edit.Meta",        // upper case sub
		"books:edit." + long(33), // sub too long
		"books:edit.a.b",         // two sub levels
		"books:edit:more",        // second colon
		"books:ed-it",            // hyphen
		" books:edit",            // whitespace
		"books:edit ",            // whitespace
		"books:edit\n",           // newline
		"bøoks:edit",             // non-ASCII
		"books:*",                // wildcard: pattern only
		"books:edit.*",           // wildcard: pattern only
		"*:*",                    // no namespace wildcard
		"books:*.x",              // wildcard is not an action
		"books:edit.*.x",         // wildcard is not a sub-action
		"books:**",               // not a wildcard
		"books:edit*",            // partial wildcard
	}
	for _, in := range invalid {
		if p, err := ParsePermission(in); err == nil {
			t.Errorf("ParsePermission(%q) = %q, want an error", in, p)
		} else if !errors.Is(err, ErrBadScope) {
			t.Errorf("ParsePermission(%q) error %v does not wrap ErrBadScope", in, err)
		}
	}

	// Wildcards: accepted by ParsePattern, and only in the two spec forms.
	for _, c := range []struct{ in, ns, action string }{
		{"books:*", "books", "*"},
		{"books:edit.*", "books", "edit"},
		{"core:*", "core", "*"},
	} {
		p, err := ParsePattern(c.in)
		if err != nil {
			t.Errorf("ParsePattern(%q): %v", c.in, err)
			continue
		}
		if !p.IsWildcard() || p.Namespace() != c.ns || p.Action() != c.action {
			t.Errorf("ParsePattern(%q) = %q ns=%q action=%q wildcard=%v", c.in, p, p.Namespace(), p.Action(), p.IsWildcard())
		}
	}
	for _, in := range []string{"*:*", "*", "books:*.x", "books:edit.*.x", "books:edit.**", "books:ed*", "B:*", "b:*"} {
		if p, err := ParsePattern(in); err == nil {
			t.Errorf("ParsePattern(%q) = %q, want an error", in, p)
		}
	}

	// CoreAdmin is a concrete scope under the strict grammar.
	if p, err := ParsePermission(string(CoreAdmin)); err != nil || p != CoreAdmin {
		t.Errorf("CoreAdmin does not round-trip ParsePermission: %q, %v", p, err)
	}

	// An unparsed Permission (a type conversion) never panics.
	if (Permission("nocolon")).Namespace() != "" || (Permission("nocolon")).Action() != "" {
		t.Error("a Permission without a colon must have no namespace and no action")
	}
}

// TestRequirement_ZeroIsInvalid: the zero value is invalid, and so is every
// constructor given nothing usable (so it fails closed, R6).
func TestRequirement_ZeroIsInvalid(t *testing.T) {
	var zero Requirement
	if !zero.IsZero() || zero.Kind() != KindInvalid || zero.Permissions() != nil || zero.String() != "invalid" {
		t.Fatalf("zero Requirement = zero:%v kind:%v perms:%v string:%q", zero.IsZero(), zero.Kind(), zero.Permissions(), zero.String())
	}

	for name, r := range map[string]Requirement{
		"AllOf()":            AllOf(),
		"AnyOf()":            AnyOf(),
		"Need(empty)":        Need(""),
		"Need(malformed)":    Need("books"),
		"Need(wildcard)":     Need("books:*"),
		"AllOf(ok,bad)":      AllOf("books:edit", "Books:view"),
		"AnyOf(ok,wildcard)": AnyOf("books:edit", "books:edit.*"),
		"LegacyAtom(empty)":  LegacyAtom(""),
		"LegacyCheck(empty)": LegacyCheck("", true),
	} {
		if !r.IsZero() {
			t.Errorf("%s = %s, want the zero Requirement", name, r)
		}
	}

	for name, r := range map[string]Requirement{
		"Public":           Public,
		"AnyAuthenticated": AnyAuthenticated,
		"Need":             Need("books:edit"),
		"AllOf":            AllOf("books:edit", "books:view"),
		"AnyOf":            AnyOf("books:edit", "books:view"),
		"Delegated":        Delegated("abs compat auth"),
		"LegacyAtom":       LegacyAtom("library.edit"),
		"LegacyCheck":      LegacyCheck("library.edit", false),
	} {
		if r.IsZero() || r.Kind() == KindInvalid {
			t.Errorf("%s is zero", name)
		}
	}
}

// TestRequirement_Kinds: each constructor reports the kind it was built with;
// Need reports AllOf with one scope.
func TestRequirement_Kinds(t *testing.T) {
	for _, c := range []struct {
		name string
		r    Requirement
		want RequirementKind
	}{
		{"Public", Public, KindPublic},
		{"AnyAuthenticated", AnyAuthenticated, KindAuthenticated},
		{"Need", Need("books:edit"), KindAllOf},
		{"AllOf", AllOf("books:edit", "books:view"), KindAllOf},
		{"AnyOf", AnyOf("books:edit", "books:view"), KindAnyOf},
		{"Delegated", Delegated("why"), KindDelegated},
		{"LegacyAtom", LegacyAtom("a"), KindLegacyAtom},
		{"LegacyCheck", LegacyCheck("a", true), KindLegacyCheck},
	} {
		if got := c.r.Kind(); got != c.want {
			t.Errorf("%s kind = %d, want %d", c.name, got, c.want)
		}
	}
	if !reflect.DeepEqual(Need("books:edit"), AllOf("books:edit")) {
		t.Error("Need(s) is not AllOf(s)")
	}

	// The spec's six kinds keep their values; the legacy two come after.
	if KindInvalid != 0 || KindPublic != 1 || KindAuthenticated != 2 || KindAllOf != 3 || KindAnyOf != 4 || KindDelegated != 5 {
		t.Error("the spec's RequirementKind values moved")
	}
}

// TestRequirement_String: the forms 05-rbac §3.1 gives, and the two scope
// lists in order.
func TestRequirement_String(t *testing.T) {
	for _, c := range []struct {
		r    Requirement
		want string
	}{
		{Need("books:edit"), "books:edit"},
		{Need("books:edit.metadata"), "books:edit.metadata"},
		{Public, "public"},
		{AnyAuthenticated, "authenticated"},
		{AllOf("books:edit", "books:view"), "all(books:edit,books:view)"},
		{AllOf("books:edit"), "books:edit"},
		{AnyOf("books:edit", "books:view"), "any(books:edit,books:view)"},
		{AnyOf("books:edit"), "any(books:edit)"},
		{Delegated("abs compat auth"), "delegated(abs compat auth)"},
		{LegacyAtom("library.edit"), "legacy(library.edit)"},
		{LegacyCheck("library.edit", false), "legacy_check(library.edit)"},
		{LegacyCheck("library.view", true), "legacy_check(library.view,read)"},
		{Requirement{}, "invalid"},
	} {
		if got := c.r.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}

// TestRequirement_PermissionsAreCopiedAndDeduplicated: a caller can neither
// reach into a Requirement through its input slice nor through its output.
func TestRequirement_PermissionsAreCopiedAndDeduplicated(t *testing.T) {
	in := []Permission{"books:edit", "books:view", "books:edit"}
	r := AllOf(in...)
	in[0] = "evil:admin"
	if got := r.Permissions(); !reflect.DeepEqual(got, []Permission{"books:edit", "books:view"}) {
		t.Fatalf("Permissions() = %v, want the deduplicated copy of the input", got)
	}

	out := r.Permissions()
	out[0] = "evil:admin"
	if got := r.Permissions(); got[0] != "books:edit" {
		t.Fatalf("mutating Permissions()'s result changed the Requirement: %v", got)
	}

	for name, r := range map[string]Requirement{"Public": Public, "AnyAuthenticated": AnyAuthenticated, "Delegated": Delegated("x"), "LegacyAtom": LegacyAtom("a")} {
		if r.Permissions() != nil {
			t.Errorf("%s.Permissions() = %v, want nil", name, r.Permissions())
		}
	}
}

// TestLegacyAtom_CarriesAtomOnly: the values report the atom they carry and
// are not zero; rbac gives them no evaluation API (their evaluation is F4's).
func TestLegacyAtom_CarriesAtomOnly(t *testing.T) {
	a := LegacyAtom("library.edit")
	if a.IsZero() || a.Atom() != "library.edit" || a.ReadSide() || a.Permissions() != nil {
		t.Errorf("LegacyAtom = zero:%v atom:%q read:%v perms:%v", a.IsZero(), a.Atom(), a.ReadSide(), a.Permissions())
	}
	c := LegacyCheck("library.view", true)
	if c.IsZero() || c.Atom() != "library.view" || !c.ReadSide() || c.Permissions() != nil {
		t.Errorf("LegacyCheck = zero:%v atom:%q read:%v perms:%v", c.IsZero(), c.Atom(), c.ReadSide(), c.Permissions())
	}
	if w := LegacyCheck("library.edit", false); w.ReadSide() || w.Atom() != "library.edit" {
		t.Errorf("LegacyCheck write-side = atom:%q read:%v", w.Atom(), w.ReadSide())
	}
	// An atom is not a scope: nothing parses it, so no Checker can match it by Has.
	if a.Kind() == c.Kind() {
		t.Error("LegacyAtom and LegacyCheck share a kind; they differ for the auth-disabled principal")
	}
	for name, r := range map[string]Requirement{"Public": Public, "Need": Need("books:edit"), "Delegated": Delegated("x")} {
		if r.Atom() != "" || r.ReadSide() {
			t.Errorf("%s reports an atom %q / read-side %v", name, r.Atom(), r.ReadSide())
		}
	}

	// "no evaluation API exists": the leaf declares no method that takes a
	// Checker or context, and Requirement has no Satisfied/Allows/Evaluate.
	rt := reflect.TypeFor[Requirement]()
	for method := range rt.Methods() {
		switch name := method.Name; name {
		case "IsZero", "Kind", "Permissions", "String", "Atom", "ReadSide":
		default:
			t.Errorf("Requirement has an unexpected exported method %s; legacy values carry the atom only", name)
		}
	}
}

// TestRoleNames: the three seed roles keep ABO's IDs (05-rbac §3.3).
func TestRoleNames(t *testing.T) {
	if RoleAdmin != "admin" || RoleEditor != "editor" || RoleViewer != "viewer" {
		t.Fatalf("seed roles = %q %q %q", RoleAdmin, RoleEditor, RoleViewer)
	}
}

// fakeChecker pins the Checker interface to the spec's four methods: a
// compile error here means the interface drifted.
type fakeChecker struct{}

func (fakeChecker) Has(Permission) bool        { return false }
func (fakeChecker) Satisfies(Requirement) bool { return false }
func (fakeChecker) Confined() bool             { return false }
func (fakeChecker) Concrete() []Permission     { return nil }

var _ Checker = fakeChecker{}

func TestChecker_FourMethods(t *testing.T) {
	if n := reflect.TypeFor[Checker]().NumMethod(); n != 4 {
		t.Fatalf("Checker has %d methods, want Has, Satisfies, Confined, Concrete", n)
	}
}
