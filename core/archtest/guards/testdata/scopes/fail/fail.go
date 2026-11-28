// file: core/archtest/guards/testdata/scopes/fail/fail.go
// version: 1.0.0
// guid: 4b7e1a93-6c20-4d58-9e3f-a1d05b8c7e24
// last-edited: 2026-11-28

// Package fail names a scope outside the core namespace in every form
// CoreScopesOnly reports. Batman names only.
package fail

import (
	"github.com/falkcorp/mediacore/httpapi"
	rb "github.com/falkcorp/mediacore/rbac"
)

const untyped = "gotham:patrol"
const ns = "gotham"
const typed rb.Permission = "gotham:signal" // want: gotham:signal

// 1. an untyped const passed to rbac.Need (the use is flagged, the string const is not a Permission).
var viaUntyped = rb.Need(untyped) // want: gotham:patrol

// 2. "gotham"+":cave", a constant concatenation.
var viaConcat = rb.Need("gotham" + ":cave") // want: gotham:cave

// 3. rbac.Permission(ns+":belfry") over a constant ns.
var viaConversion = rb.Need(rb.Permission(ns + ":belfry")) // want: gotham:belfry

// 4. Checker.Has with a literal.
func has(c rb.Checker) bool { return c.Has("gotham:batmobile") } // want: gotham:batmobile

// 5. httpapi.Forbidden with a literal.
var forbidden = httpapi.Forbidden("gotham:utility") // want: gotham:utility

// 6. a Permission-typed struct field set from a literal.
type gate struct{ perm rb.Permission }

var field = gate{perm: "gotham:grapple"} // want: gotham:grapple

// 7. a map[rbac.Permission] key.
var key = map[rb.Permission]bool{"gotham:cowl": true} // want: gotham:cowl

// 8. an import alias is every use in this file (rb).

// 9. return from a func returning Permission.
func ret() rb.Permission { return "gotham:cape" } // want: gotham:cape

// 10. an assignment.
func assign() rb.Permission {
	var p rb.Permission
	p = "gotham:belt" // want: gotham:belt
	return p
}

// Also: the string-taking parsers, a slice element, a spec's Name and Implies,
// an AllOf member, and a use of a typed const (reported at its declaration
// above, not here).
var _, _ = rb.ParsePermission("gotham:radio")                  // want: gotham:radio
var _, _ = rb.ParsePattern("gotham:*")                         // want: gotham:*
var elems = []rb.Permission{"core:users.view", "gotham:alarm"} // want: gotham:alarm
var spec = rb.PermissionSpec{
	Name:    "gotham:vault", // want: gotham:vault
	Title:   "gotham:title",
	Implies: []rb.Permission{"gotham:ledger"}, // want: gotham:ledger
}
var allOf = rb.AllOf("core:users.view", "gotham:cells") // want: gotham:cells
var usesTyped = rb.Need(typed)
