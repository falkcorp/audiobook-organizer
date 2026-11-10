// file: core/internal/authz/authz.go
// version: 1.0.2
// guid: 9ab14091-8ae1-439c-ad61-28782f1e547d
// last-edited: 2026-11-10

// Package authz is the implementation of rbac's Checker (05-rbac §3, layer
// L4): the compiled checker the kernel injects as rbac.Deps.Compile, and the
// two privileged checkers that bypass role resolution, AuthDisabled (§4.11)
// and ForSystem (the _system principal of background tasks, §3.4).
//
// authz imports rbac; rbac imports nothing from authz (COR4-12). Go's
// internal/ rule keeps authz out of app code, and
// archtest.NoSystemPrincipalOutsideKernel lets only auth, the tasks executor
// and kernel import authz at all (SEC-26): Compile mints an interactive admin
// checker as readily as the privileged constructors bypass roles.
//
// Every checker here fails closed: a scope it cannot prove it holds is not
// held. A checker is immutable once built (05-rbac §9) and the package holds
// no state of its own (R5).
package authz

import (
	"maps"
	"slices"

	"github.com/falkcorp/mediacore/rbac"
)

// checker is the one implementation of rbac.Checker. Every field is set by a
// constructor and never written again, so a checker is safe for concurrent
// use without locks.
type checker struct {
	// cat is the catalog the checker was built against. It answers a
	// scope's flags (Sensitive, AuthRequired, Confining) on every check. nil
	// only for the refusal checker, which holds nothing.
	cat *rbac.Catalog
	// set is the effective concrete set: every concrete scope held, with
	// implication closures and wildcard expansions folded in (§4.4).
	set map[rbac.Permission]struct{}
	// nsWild and actWild are the role wildcards a session holds literally:
	// "<ns>" for "<ns>:*" and "<ns>:<action>" for "<ns>:<action>.*". A key
	// never holds one (§4.2), so a key's checker leaves both empty.
	nsWild  map[string]struct{}
	actWild map[string]struct{}
	// admin is the core:admin flag (§4.4): set iff the roles grant core:admin
	// and, for a key, the key names it.
	admin bool
	// interactive is auth.Method.Interactive() of the credential (§4.4 rule 1).
	interactive bool
	// confined is Confined(), decided once on the effective set (§4.4, §4.10).
	confined bool
	// authOff marks AuthDisabled's checker: its Satisfies alone applies the
	// auth-off evaluation rule for AllOf (§4.11).
	authOff bool
	// concrete is Concrete()'s value, sorted; handed out only as a copy.
	concrete []rbac.Permission
}

var _ rbac.Checker = (*checker)(nil)

// denied returns the checker of a refused construction: it holds nothing and
// is Confined(), so no rule that admits by default can admit it either (the
// rule rbac's own deny-all checker keeps). Each constructor returns it for a
// nil catalog, and ForSystem also for an entry it must never hold.
func denied() *checker {
	return &checker{confined: true}
}

// Compile builds a principal's compiled checker (05-rbac §4.4, §4.10). It is
// the value of rbac.Deps.Compile: rbac.Service.Compile resolves the named
// roles into in.RolePatterns before it calls it, so Compile reads no store
// and no role cache. A nil catalog yields a checker that holds nothing.
//
// The effective set is the roles' patterns expanded against cat with their
// implication closures folded in. core:admin, granted by any pattern that
// covers it (rbac.GrantsAdmin), sets the admin flag; its reach over every
// other scope is Has's rule 2, not members of the set, so Concrete() lists
// what the patterns expand to. With key scopes (in.Scopes != nil) the set is the intersection of the
// roles' set and the key's scopes after implication, and the admin flag
// needs the key to name core:admin as well (SEC-12). Malformed patterns and
// scopes the catalog does not declare grant nothing.
func Compile(in rbac.CompileInput, cat *rbac.Catalog) rbac.Checker {
	if cat == nil {
		return denied()
	}
	c := &checker{
		cat:         cat,
		set:         map[rbac.Permission]struct{}{},
		nsWild:      map[string]struct{}{},
		actWild:     map[string]struct{}{},
		interactive: in.Interactive,
	}
	roleAdmin := rbac.GrantsAdmin(in.RolePatterns)

	roles := map[rbac.Permission]struct{}{}
	for _, p := range in.RolePatterns {
		pat, err := rbac.ParsePattern(string(p))
		if err != nil {
			continue
		}
		if pat.IsWildcard() {
			if pat.Action() == "*" {
				c.nsWild[pat.Namespace()] = struct{}{}
			} else {
				c.actWild[pat.Namespace()+":"+pat.Action()] = struct{}{}
			}
		}
		for _, s := range cat.Expand(pat) {
			addClosure(cat, roles, s)
		}
	}

	if in.Scopes == nil {
		c.set = roles
		c.admin = roleAdmin
	} else {
		key := map[rbac.Permission]struct{}{}
		keyNamesAdmin := false
		for _, s := range in.Scopes {
			p, err := rbac.ParsePermission(string(s))
			if err != nil {
				continue
			}
			if p == rbac.CoreAdmin {
				keyNamesAdmin = true
			}
			if _, ok := cat.Lookup(p); ok {
				addClosure(cat, key, p)
			}
		}
		// core:admin implies every scope (§4.3), so an admin's roles hold
		// whatever the key names; anyone else's only what they expand to.
		for s := range key {
			if _, ok := roles[s]; ok || roleAdmin {
				c.set[s] = struct{}{}
			}
		}
		c.admin = roleAdmin && keyNamesAdmin
		// A key holds no wildcard (§4.2): the intersection is concrete.
		c.nsWild, c.actWild = map[string]struct{}{}, map[string]struct{}{}
	}
	// core:admin is in the set exactly when the flag is: a key that does not
	// name it narrows an admin to the key's scopes.
	if c.admin {
		if _, ok := cat.Lookup(rbac.CoreAdmin); ok {
			c.set[rbac.CoreAdmin] = struct{}{}
		}
	} else {
		delete(c.set, rbac.CoreAdmin)
	}
	c.confined = !c.admin && confinedSet(cat, c.set)
	c.concrete = sortedKeys(c.set)
	return c
}

// AuthDisabled is the checker of the auth-disabled principal (05-rbac §4.11,
// 05-authn §4.11, SEC-2): every concrete catalog scope that is neither
// AuthRequired nor Sensitive, no admin flag, no implication from core:admin,
// non-interactive and never Confined(). core:admin is left out by name as
// well as by its AuthRequired flag, so a catalog that forgot the flag still
// cannot hand the auth-off principal admin. Its Satisfies alone applies the
// auth-off evaluation rule (an AllOf drops its AuthRequired members). A nil
// catalog yields a checker that holds nothing.
//
// Only auth, the tasks executor and kernel may import authz
// (archtest.NoSystemPrincipalOutsideKernel).
func AuthDisabled(cat *rbac.Catalog) rbac.Checker {
	if cat == nil {
		return denied()
	}
	c := &checker{cat: cat, set: map[rbac.Permission]struct{}{}, authOff: true}
	for spec := range cat.All() {
		if spec.AuthRequired || spec.Sensitive || spec.Name == rbac.CoreAdmin {
			continue
		}
		c.set[spec.Name] = struct{}{}
	}
	c.concrete = sortedKeys(c.set)
	return c
}

// ForSystem is the checker of a module's system actor, the _system principal
// of background tasks (05-rbac §3.4, SEC2-15): it holds exactly the perms cat
// declares, is non-interactive and has no admin flag. The kernel passes the
// union of Perms.Start of the module's own kinds plus Manifest.TaskStarts,
// never core:admin or an AuthRequired scope (05-tasks §5T.3.10).
//
// Every check goes through Has against cat, so §4.4 rule 1 holds for the
// background actor as for any non-interactive principal: a Sensitive scope
// it was given, a module's as much as core's, is refused, and so is a scope
// cat does not declare (which the set leaves out). It holds exactly what it
// is given, with no implication and no wildcard.
//
// Confined() reports true. The system actor is not a request principal:
// AnyAuthenticated gates the HTTP surface a person or a key reaches (the
// event stream, /me), which a background run has no business reaching, and
// its set is the exact list the kernel computed, so judging requirements by
// literal naming refuses nothing it holds.
//
// A nil catalog yields a checker that holds nothing. So does an entry the
// kernel must never pass: a malformed scope or a wildcard, core:admin, or a
// scope cat marks AuthRequired. A kernel bug then fails loudly instead of
// granting part of a set.
//
// Only auth, the tasks executor and kernel may import authz
// (archtest.NoSystemPrincipalOutsideKernel).
func ForSystem(cat *rbac.Catalog, perms []rbac.Permission) rbac.Checker {
	if cat == nil {
		return denied()
	}
	set := map[rbac.Permission]struct{}{}
	for _, s := range perms {
		p, err := rbac.ParsePermission(string(s))
		if err != nil || p == rbac.CoreAdmin {
			return denied()
		}
		spec, ok := cat.Lookup(p)
		if !ok {
			continue // undeclared: held by no one
		}
		if spec.AuthRequired {
			return denied()
		}
		set[p] = struct{}{}
	}
	return &checker{cat: cat, set: set, confined: true, concrete: sortedKeys(set)}
}

// Has reports whether the principal holds s (05-rbac §4.4). For a concrete
// scope the order is: (1) a Sensitive scope for a non-interactive principal
// is false, even with admin; (2) admin is true; (3) the effective set decides.
// A scope the catalog does not declare is false, admin included, since its
// flags cannot be read. A wildcard is held only when the principal holds that
// pattern or a wider one literally (seams.d/F4-01.md), and never by a
// non-interactive principal when the pattern reaches a Sensitive scope.
// Malformed input is false.
func (c *checker) Has(s rbac.Permission) bool {
	p, err := rbac.ParsePattern(string(s))
	if err != nil {
		return false
	}
	if p.IsWildcard() {
		return c.holdsPattern(p)
	}
	if c.cat == nil {
		return false
	}
	spec, ok := c.cat.Lookup(p)
	if !ok {
		return false
	}
	if spec.Sensitive && !c.interactive {
		return false
	}
	if c.admin {
		return true
	}
	_, ok = c.set[p]
	return ok
}

// holdsPattern is Has of a wildcard: a literal hold of the pattern or a wider
// one, and for a non-interactive principal no Sensitive scope under it.
func (c *checker) holdsPattern(p rbac.Permission) bool {
	_, ns := c.nsWild[p.Namespace()]
	held := ns
	if !held && p.Action() != "*" {
		_, held = c.actWild[p.Namespace()+":"+p.Action()]
	}
	if !held {
		return false
	}
	if !c.interactive && c.cat != nil {
		for _, s := range c.cat.Expand(p) {
			if spec, _ := c.cat.Lookup(s); spec.Sensitive {
				return false
			}
		}
	}
	return true
}

// Satisfies reports whether the principal meets r (05-rbac §4.10):
//   - Public: true.
//   - AnyAuthenticated: the principal holds core:self or some non-Confining
//     scope, each through Has; never for a Confined() principal.
//   - AllOf (Need) and AnyOf: over Has. A Confined() principal is judged by
//     literal membership instead, so it satisfies only a requirement that
//     names its scopes, never one a scope merely implies.
//   - Delegated (enforced by the mounted handler, 05-http-api §8.2), the
//     legacy kinds (resolved through books' atom table before a check, not
//     here) and the zero Requirement: false.
//
// AuthDisabled's checker drops the AuthRequired members of an AllOf first
// and refuses one made only of them (§4.11).
func (c *checker) Satisfies(r rbac.Requirement) bool {
	switch r.Kind() {
	case rbac.KindPublic:
		return true
	case rbac.KindAuthenticated:
		return c.authenticated()
	case rbac.KindAllOf:
		perms := r.Permissions()
		if c.authOff {
			perms = c.dropAuthRequired(perms)
		}
		if len(perms) == 0 {
			return false
		}
		for _, p := range perms {
			if !c.holds(p) {
				return false
			}
		}
		return true
	case rbac.KindAnyOf:
		return slices.ContainsFunc(r.Permissions(), c.holds)
	default:
		return false
	}
}

// holds is the per-scope test of Satisfies: Has, for a Confined() principal
// too, so Has's rules (an undeclared scope, a Sensitive scope for a
// non-interactive principal) hold on the confined path.
//
// The literal-membership block below is defense in depth and can never
// change a result: a confined principal is never admin, so Has is true only
// for a member of the set, and a confined set (only Confining scopes)
// holds no implied scope, because NewCatalog refuses a Confining scope that
// implies or is implied (rbac.ErrConfiningImplication). So every member was
// named literally (§4.10) and no vector can kill the block. It stays so the
// literal rule does not rest on a catalog check in another package.
func (c *checker) holds(p rbac.Permission) bool {
	if c.confined {
		if _, ok := c.set[p]; !ok {
			return false
		}
	}
	return c.Has(p)
}

// authenticated is AnyAuthenticated (§4.10): core:self or any non-Confining
// scope, each through Has so a scope Has refuses (a Sensitive scope of a
// non-interactive key) does not count; never a Confined() principal.
func (c *checker) authenticated() bool {
	if c.confined || c.cat == nil {
		return false
	}
	if c.Has("core:self") {
		return true
	}
	for _, s := range c.concrete {
		if spec, ok := c.cat.Lookup(s); ok && !spec.Confining && c.Has(s) {
			return true
		}
	}
	return false
}

// dropAuthRequired is the auth-off evaluation rule (§4.11): the members of
// an AllOf the catalog marks AuthRequired are dropped. A member the catalog
// does not declare is kept, and Has then refuses it.
func (c *checker) dropAuthRequired(perms []rbac.Permission) []rbac.Permission {
	out := perms[:0:0]
	for _, p := range perms {
		if spec, ok := c.cat.Lookup(p); ok && spec.AuthRequired {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Confined reports whether every concrete scope held is Confining (§4.4,
// §4.10), decided once on the effective set; an admin is never confined.
func (c *checker) Confined() bool { return c.confined }

// Concrete returns the effective concrete set, sorted, as a fresh copy.
func (c *checker) Concrete() []rbac.Permission { return slices.Clone(c.concrete) }

// addClosure adds s and every scope it implies, transitively, to set.
func addClosure(cat *rbac.Catalog, set map[rbac.Permission]struct{}, s rbac.Permission) {
	stack := []rbac.Permission{s}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, seen := set[p]; seen {
			continue
		}
		set[p] = struct{}{}
		if spec, ok := cat.Lookup(p); ok {
			stack = append(stack, spec.Implies...)
		}
	}
}

// confinedSet is §4.4's confinement rule on an effective set: non-empty and
// every member Confining.
func confinedSet(cat *rbac.Catalog, set map[rbac.Permission]struct{}) bool {
	if len(set) == 0 {
		return false
	}
	for s := range set {
		if spec, ok := cat.Lookup(s); !ok || !spec.Confining {
			return false
		}
	}
	return true
}

func sortedKeys(set map[rbac.Permission]struct{}) []rbac.Permission {
	return slices.Sorted(maps.Keys(set))
}
