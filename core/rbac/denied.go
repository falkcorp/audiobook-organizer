// file: core/rbac/denied.go
// version: 0.1.0
// guid: 0b6f2d8e-4c71-4a39-b5e2-9d3a7f1c6e58
// last-edited: 2026-11-20

package rbac

import "context"

// DeniedError is the refusal Require, RequireAll and RequireRequirement
// return (§3.4). errors.Is(err, ErrDenied) holds for it, and its text is the
// one those functions have always returned ("rbac: permission denied: <what>"),
// so a caller that matched ErrDenied or the text sees no change. httpapi maps
// it to 403 permission_denied with Permission as required_permission
// (05-http-api §3.3).
type DeniedError struct {
	// Permission is the scope the caller was refused: the one Require
	// named, the first RequireAll did not hold, or for RequireRequirement
	// the first scope of the requirement the bound checker does not Has (its
	// first scope when it Has them all, or when no checker is bound). It is
	// "" when no valid scope was named.
	Permission Permission

	what string // the text after "rbac: permission denied: "
}

// Error implements error.
func (e *DeniedError) Error() string { return ErrDenied.Error() + ": " + e.what }

// Unwrap returns ErrDenied, so errors.Is(err, ErrDenied) holds.
func (e *DeniedError) Unwrap() error { return ErrDenied }

// denial is the DeniedError of scope p, worded as Require has always worded it.
func denial(p Permission) *DeniedError { return &DeniedError{Permission: p, what: string(p)} }

// RequireRequirement is Require for a whole requirement: it holds when the
// checker bound into ctx Satisfies r. It is how an in-handler check judges a
// route-shaped requirement (05-http-api §8.3: the legacy mount's adapters
// require the atom table's entry, an AllOf). RequireAll cannot stand in for
// it: the auth-disabled checker drops the AuthRequired members of an AllOf
// and requires the rest (§4.11), which a scope-by-scope check does not do.
//
// Only AllOf, AnyOf and AnyAuthenticated are checked. Public names nothing to
// hold, Delegated is enforced by its own handler, and the legacy kinds are
// resolved through the app's atom table before any check, because no checker
// evaluates them (COR6-1); each of those, and the zero Requirement, is
// refused. No checker in ctx is refused. A refusal is recorded as Require's
// is (the service Bind bound audits core.permission.deny and counts it).
func RequireRequirement(ctx context.Context, r Requirement) error {
	switch r.Kind() {
	case KindAllOf, KindAnyOf, KindAuthenticated:
	default:
		denied(ctx, "")
		return &DeniedError{what: "requirement " + r.String() + " is not checked in a handler"}
	}
	c, ok := CheckerFrom(ctx)
	if ok && c.Satisfies(r) {
		return nil
	}
	p := firstUnheld(c, ok, r)
	denied(ctx, p)
	return &DeniedError{Permission: p, what: r.String()}
}

// firstUnheld is the scope a RequireRequirement refusal names: the first
// scope of r that c does not Has, else r's first scope, and "" for
// AnyAuthenticated, which names none.
func firstUnheld(c Checker, bound bool, r Requirement) Permission {
	ps := r.Permissions()
	if len(ps) == 0 {
		return ""
	}
	if bound {
		for _, p := range ps {
			if !c.Has(p) {
				return p
			}
		}
	}
	return ps[0]
}
