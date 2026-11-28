// file: core/httpapi/corequirement.go
// version: 1.0.0
// guid: f664663f-f1bd-43b1-ba0f-a41e2cc5715d
// last-edited: 2026-11-28

package httpapi

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/falkcorp/mediacore/rbac"
)

// App-declared route co-requirements (W6, 05-rbac §6.1).
//
// A core route requires scopes of core's own namespace only (R6, checkPerm),
// so core cannot say "and the app's books:integrations.manage too". The app
// says it: Options.CoRequirements maps a core route's Name to a requirement
// the router ANDs onto the route's own. The effect is one rule, narrowing
// only:
//
//	effective(route) = AllOf(route's own permissions ∪ the co-requirement's)
//
// It is never an OR and never a replacement, so it can only refuse a request
// the route alone would have admitted. R6 is untouched: checkPerm still judges
// RouteInfo.Perm, the route's own, and the co-requirement lives on the
// routeEntry beside it, where only it may name the app's namespace. Build
// refuses the whole registration, so the process does not start, for every
// entry it cannot prove narrows: see applyCoRequirements.

// Registration-time errors of Options.CoRequirements, joined into Build's.
var (
	// ErrCoRequirementRoute is an entry naming no core route: a typo, a route
	// that is not mounted in this build, or the name of a module's route.
	ErrCoRequirementRoute = errors.New("httpapi: route co-requirement names no core route")
	// ErrCoRequirementShape is a requirement that is not AllOf (or Need) over
	// at least one scope, or an entry whose target route's own requirement is
	// not AllOf: an AnyOf, Public or AnyAuthenticated route cannot be ANDed
	// into one Requirement without changing what it means.
	ErrCoRequirementShape = errors.New("httpapi: route co-requirement is not AllOf or Need")
	// ErrCoRequirementScope is a requirement that names Public,
	// AnyAuthenticated, core:admin or a scope of a namespace core owns. A
	// co-requirement is the app's to add; core's own scopes are the route's.
	ErrCoRequirementScope = errors.New("httpapi: route co-requirement names a scope core owns")
	// ErrCoRequirementUnregistered is a scope the rbac catalog does not
	// declare, or a boot with no catalog to check against: a scope nobody can
	// hold would lock the route for everyone, and a typo would pass silently.
	ErrCoRequirementUnregistered = errors.New("httpapi: route co-requirement names a scope the catalog has not registered")
)

// perm is the requirement stage 13 judges, OpenAPI documents and Routes
// reports for e: its own, ANDed with the app's co-requirement when it has
// one. checkPerm and every other reader of the route's own requirement keep
// e.info.Perm.
func (e *routeEntry) perm() rbac.Requirement {
	if e.co.IsZero() {
		return e.info.Perm
	}
	return combineRequirements(e.info.Perm, e.co)
}

// combineRequirements is AllOf(own ∪ co) for two AllOf requirements. For any
// other input it returns the zero Requirement, which stage 13 refuses
// (resolve): a pair this cannot AND fails closed, never open.
func combineRequirements(own, co rbac.Requirement) rbac.Requirement {
	if own.Kind() != rbac.KindAllOf || co.Kind() != rbac.KindAllOf {
		return rbac.Requirement{}
	}
	return rbac.AllOf(append(own.Permissions(), co.Permissions()...)...)
}

// isCoreRoute reports whether e is a core-owned native or raw route, the only
// routes a co-requirement can name.
func isCoreRoute(e *routeEntry) bool {
	return e.info.Owner == "core" && (e.info.Kind == RouteKindNative || e.info.Kind == RouteKindRaw)
}

// applyCoRequirements resets every route's co-requirement and sets it from
// Options.CoRequirements, returning one error per problem found (a Build that
// ran before leaves nothing behind). The catalog is the one SetAuth gave
// stage 13; r.mu is held.
//
// A co-requirement attaches to EVERY route entry carrying the name, not the
// first one found: the router has no second mount of a route (a core route
// name is unique, Build refuses a repeat as ErrDuplicateRoute), but should a
// future alias or versioned mount ever carry the name, it gets the
// co-requirement too instead of becoming the way around it.
func (r *Router) applyCoRequirements() []error {
	for _, e := range r.routes {
		e.co = rbac.Requirement{}
	}
	if len(r.opts.CoRequirements) == 0 {
		return nil
	}
	var cat *rbac.Catalog
	if r.auth != nil {
		cat = r.auth.opts.Catalog
	}
	names := make([]string, 0, len(r.opts.CoRequirements))
	for name := range r.opts.CoRequirements {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		co := r.opts.CoRequirements[name]
		problems := checkCoRequirement(name, co, cat)
		errs = append(errs, problems...)
		found := false
		for _, e := range r.routes {
			if !isCoreRoute(e) || e.info.Name != name {
				continue
			}
			found = true
			if own := e.info.Perm; own.Kind() != rbac.KindAllOf {
				errs = append(errs, fmt.Errorf("%w: %s: the route's own requirement is %s, and a co-requirement can only be ANDed onto AllOf or Need",
					ErrCoRequirementShape, describe(e.info), own))
				continue
			}
			if len(problems) == 0 {
				e.co = co
			}
		}
		if !found {
			errs = append(errs, fmt.Errorf("%w: %q is not the name of a registered core route", ErrCoRequirementRoute, name))
		}
	}
	return errs
}

// checkCoRequirement reports why co cannot be the co-requirement of the route
// name: it must be AllOf or Need over scopes that are the app's (not core's)
// and that cat declares.
func checkCoRequirement(name string, co rbac.Requirement, cat *rbac.Catalog) []error {
	switch co.Kind() {
	case rbac.KindAllOf:
	case rbac.KindPublic, rbac.KindAuthenticated:
		return []error{fmt.Errorf("%w: %q requires %s, which is not a scope", ErrCoRequirementScope, name, co)}
	default:
		return []error{fmt.Errorf("%w: %q requires %s", ErrCoRequirementShape, name, co)}
	}
	var errs []error
	reserved := rbac.ReservedNamespaces()
	for _, p := range co.Permissions() {
		if p == rbac.CoreAdmin || slices.Contains(reserved, p.Namespace()) {
			errs = append(errs, fmt.Errorf("%w: %q requires %s", ErrCoRequirementScope, name, p))
			continue
		}
		if cat == nil {
			errs = append(errs, fmt.Errorf("%w: %w: %q requires %s and the router has no catalog to check it against",
				ErrCoRequirementUnregistered, ErrUnknownScope, name, p))
		} else if _, ok := cat.Lookup(p); !ok {
			errs = append(errs, fmt.Errorf("%w: %w: %q requires %s", ErrCoRequirementUnregistered, ErrUnknownScope, name, p))
		}
	}
	return errs
}
