// file: internal/books/cutover/reach_eval_test.go
// version: 1.0.0
// guid: 4b9e1c75-8d23-4f60-a7c2-5e0d3b8f1a94
// last-edited: 2026-11-13

package cutover

import (
	"slices"

	"github.com/falkcorp/mediacore/rbac"
)

// The reach oracle's R2 evaluator (F4-07): the checker of one principal and
// the requirement test of stage 13, as 05-rbac §4 specifies them.
//
// It is a test-local copy of core/internal/authz's Compile, AuthDisabled, Has
// and Satisfies. Core exports no Compile to another module: authz is
// importable only by auth, core/internal/executor and kernel (SEC-26,
// archtest.NoSystemPrincipalOutsideKernel), and nothing outside core's own
// tests builds an rbac.Service until F4-08 wires one. F4-08 replaces this file
// with the registered path (the deviation is in seams.d/F4-07.md). A fault in
// this copy shows up in the oracle as a cell that differs from R0 and is on no
// expected-difference row, so it cannot hide; what it cannot show is a fault
// that makes this copy and core's agree on the same wrong verdict.

// reachChecker is one principal's checker.
type reachChecker struct {
	cat         *rbac.Catalog
	set         map[rbac.Permission]struct{}
	admin       bool
	interactive bool
	confined    bool
	authOff     bool
}

// reachCompile is authz.Compile: patterns are the union of the principal's
// roles' permissions, scopes the key's native scopes (nil for a session).
func reachCompile(cat *rbac.Catalog, patterns, scopes []rbac.Permission, interactive bool) *reachChecker {
	c := &reachChecker{cat: cat, set: map[rbac.Permission]struct{}{}, interactive: interactive}
	roleAdmin := rbac.GrantsAdmin(patterns)
	roles := map[rbac.Permission]struct{}{}
	for _, p := range patterns {
		pat, err := rbac.ParsePattern(string(p))
		if err != nil {
			continue
		}
		for _, s := range cat.Expand(pat) {
			reachClosure(cat, roles, s)
		}
	}
	if scopes == nil {
		c.set = roles
		c.admin = roleAdmin
	} else {
		key := map[rbac.Permission]struct{}{}
		keyNamesAdmin := false
		for _, s := range scopes {
			p, err := rbac.ParsePermission(string(s))
			if err != nil {
				continue
			}
			if p == rbac.CoreAdmin {
				keyNamesAdmin = true
			}
			if _, ok := cat.Lookup(p); ok {
				reachClosure(cat, key, p)
			}
		}
		for s := range key {
			if _, ok := roles[s]; ok || roleAdmin {
				c.set[s] = struct{}{}
			}
		}
		c.admin = roleAdmin && keyNamesAdmin
	}
	if c.admin {
		if _, ok := cat.Lookup(rbac.CoreAdmin); ok {
			c.set[rbac.CoreAdmin] = struct{}{}
		}
	} else {
		delete(c.set, rbac.CoreAdmin)
	}
	c.confined = !c.admin && reachConfined(cat, c.set)
	return c
}

// reachAuthDisabled is authz.AuthDisabled: every concrete scope that is neither
// AuthRequired nor Sensitive, no admin flag, non-interactive, never Confined.
func reachAuthDisabled(cat *rbac.Catalog) *reachChecker {
	c := &reachChecker{cat: cat, set: map[rbac.Permission]struct{}{}, authOff: true}
	for spec := range cat.All() {
		if spec.AuthRequired || spec.Sensitive || spec.Name == rbac.CoreAdmin {
			continue
		}
		c.set[spec.Name] = struct{}{}
	}
	return c
}

func reachClosure(cat *rbac.Catalog, set map[rbac.Permission]struct{}, s rbac.Permission) {
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

func reachConfined(cat *rbac.Catalog, set map[rbac.Permission]struct{}) bool {
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

// has is authz's Has: a Sensitive scope is refused to a non-interactive
// principal even with admin, admin holds every declared scope, and the set
// decides otherwise. No oracle route names a wildcard, so none is held here
// (authz holds one only as a literal role pattern).
func (c *reachChecker) has(s rbac.Permission) bool {
	p, err := rbac.ParsePattern(string(s))
	if err != nil {
		return false
	}
	if p.IsWildcard() {
		return false // no oracle route asks for one
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

func (c *reachChecker) holds(p rbac.Permission) bool {
	if c.confined {
		if _, ok := c.set[p]; !ok {
			return false
		}
	}
	return c.has(p)
}

func (c *reachChecker) authenticated() bool {
	if c.confined {
		return false
	}
	if c.has("core:self") {
		return true
	}
	for s := range c.set {
		if spec, ok := c.cat.Lookup(s); ok && !spec.Confining && c.has(s) {
			return true
		}
	}
	return false
}

// satisfies is authz's Satisfies for the kinds a route's requirement takes.
func (c *reachChecker) satisfies(r rbac.Requirement) bool {
	switch r.Kind() {
	case rbac.KindPublic:
		return true
	case rbac.KindAuthenticated:
		return c.authenticated()
	case rbac.KindAllOf:
		perms := r.Permissions()
		if c.authOff {
			kept := perms[:0:0]
			for _, p := range perms {
				if spec, ok := c.cat.Lookup(p); ok && spec.AuthRequired {
					continue
				}
				kept = append(kept, p)
			}
			perms = kept
		}
		if len(perms) == 0 {
			return false
		}
		return !slices.ContainsFunc(perms, func(p rbac.Permission) bool { return !c.holds(p) })
	case rbac.KindAnyOf:
		return slices.ContainsFunc(r.Permissions(), c.holds)
	default:
		return false
	}
}
