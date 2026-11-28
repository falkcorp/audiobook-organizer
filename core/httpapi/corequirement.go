// file: core/httpapi/corequirement.go
// version: 1.0.0
// guid: f664663f-f1bd-43b1-ba0f-a41e2cc5715d
// last-edited: 2026-11-28

package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/rbac"
)

// App-declared route co-requirements (W6, 05-rbac §6.1).
//
// A core route requires scopes of core's own namespace only (R6, checkPerm),
// so core cannot say "and the app's books:integrations.manage too". The app
// says it: each Options.CoRequirements entry names a core route and a
// requirement the router ANDs onto the route's own, for the whole route or,
// when the entry names a path wildcard and a value of it, only for requests
// whose wildcard has that value (a route that serves one operation per kind,
// where the app gates the download kind and not the rest). The effect is one
// rule, narrowing only: a request is admitted when the route's own
// requirement admits it AND every co-requirement that applies to it does,
// each judged on its own by stage 13 (AuthzRequest.CoPerms after Perm).
//
// The two are judged SEPARATELY, never as one merged AllOf. The auth-disabled
// checker drops the AuthRequired members of an AllOf before judging it
// (05-rbac §4.11) and refuses one made only of them, so a merged AllOf of an
// AuthRequired-only own requirement and a co-scope that is not AuthRequired
// would admit the auth-disabled principal the own requirement refuses: the
// co-requirement would widen. Judged apart, it cannot.
//
// It is never an OR and never a replacement, so it can only refuse a request
// the route alone would have admitted. R6 is untouched: checkPerm still judges
// RouteInfo.Perm, the route's own, and the co-requirement lives on the
// routeEntry beside it, where only it may name the app's namespace. Build
// refuses the whole registration, so the process does not start, for every
// entry it cannot prove narrows: see applyCoRequirements.

// CoRequirement is one app-declared co-requirement of a core route (W6).
//
// Route is the Name of a core route. Need is the AllOf (or Need) of the app's
// scopes the router ANDs onto the route's own requirement; it can only
// narrow, never replace or widen.
//
// Param and Value, when both set, scope the entry to the requests whose path
// wildcard Param equals Value: Param must be a single-segment wildcard of the
// route's pattern ({kind}, not {rest...}) and Value a non-empty literal with
// no "/", "{" or "}". Both empty is a route-wide entry, which applies to every
// request. Setting only one is refused. A route may carry a route-wide entry
// and any number of per-value entries, and a request is admitted only when it
// passes the route's own requirement and every entry that applies to it. No
// two entries of one route may share (Param, Value).
//
// Value is matched case-insensitively against the decoded wildcard. A handler
// that folds case ("Download" and "download" name one kind) must not be a way
// round the entry, so the entry covers every spelling: a looser match can only
// refuse more, which is the safe direction. A handler must still treat a
// spelling the entry does not cover (a value it normalises further, say) as a
// different kind, or declare that value too; the router cannot see inside it.
type CoRequirement struct {
	Route string
	Param string
	Value string
	Need  rbac.Requirement
}

// coRule is a validated CoRequirement as the route entry keeps it.
type coRule struct {
	param, value string // both empty: route-wide
	need         rbac.Requirement
}

// applies reports whether the rule covers request r. The router's mux has
// already bound the wildcards, so PathValue is the decoded segment.
func (c coRule) applies(r *http.Request) bool {
	return c.param == "" || strings.EqualFold(r.PathValue(c.param), c.value)
}

// CoValuePerm is one per-value co-requirement of a route as RouteInfo reports
// it: the requirement and the wildcard value it applies to.
type CoValuePerm struct {
	Param, Value string
	Perm         rbac.Requirement
}

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
	// ErrCoRequirementRestricted is a co-scope the catalog flags Sensitive or
	// Confining. A Sensitive scope is held by no API key without an
	// interactive method (05-rbac 4.4), so a co-requirement on one would
	// refuse an administrator's key on a route its own requirement admits;
	// a Confining scope is held only by a principal that holds nothing else.
	// Refused to keep the feature narrow and predictable: a co-requirement
	// is an ordinary app scope. (AuthRequired is allowed: it only narrows the
	// auth-disabled principal.)
	ErrCoRequirementRestricted = errors.New("httpapi: route co-requirement names a Sensitive or Confining scope")
	// ErrCoRequirementParam is a Param/Value pair that cannot scope an entry:
	// only one of the two set, a Param that is not a single-segment wildcard
	// of the route's pattern, or a Value that is empty or has "/", "{" or "}".
	ErrCoRequirementParam = errors.New("httpapi: route co-requirement has a bad param or value")
	// ErrCoRequirementDuplicate is two entries of one route with the same
	// (Param, Value): which Need applies would be a guess.
	ErrCoRequirementDuplicate = errors.New("httpapi: route co-requirement declared twice")
	// ErrCoRequirementUnregistered is a scope the rbac catalog does not
	// declare, or a boot with no catalog to check against: a scope nobody can
	// hold would lock the route for everyone, and a typo would pass silently.
	ErrCoRequirementUnregistered = errors.New("httpapi: route co-requirement names a scope the catalog has not registered")
)

// perm is the requirement OpenAPI documents and Routes reports for e: its
// own, ANDed with the app's co-requirement when it has one. It is DISPLAY
// only. Stage 13 never judges it: it judges e.info.Perm and e.co one after
// the other (AuthzRequest.CoPerm), because one merged AllOf would widen
// access for the auth-disabled principal. checkPerm and every other reader of
// the route's own requirement keep e.info.Perm.
func (e *routeEntry) perm() rbac.Requirement {
	if e.co.IsZero() {
		return e.info.Perm
	}
	return combineRequirements(e.info.Perm, e.co)
}

// combineRequirements is AllOf(own ∪ co) for two AllOf requirements, for
// display only (perm). For any other input it returns the zero Requirement,
// which no reader judges: a pair this cannot AND displays as nothing.
func combineRequirements(own, co rbac.Requirement) rbac.Requirement {
	if own.Kind() != rbac.KindAllOf || co.Kind() != rbac.KindAllOf {
		return rbac.Requirement{}
	}
	return rbac.AllOf(append(own.Permissions(), co.Permissions()...)...)
}

// coRules is every co-requirement of e as stage 13 judges them: the
// route-wide one (when set) then each per-value one. r.mu is held.
func (e *routeEntry) coRules() []coRule {
	var out []coRule
	if !e.co.IsZero() {
		out = append(out, coRule{need: e.co})
	}
	return append(out, e.coBy...)
}

// isCoreRoute reports whether e is a core-owned native or raw route, the only
// routes a co-requirement can name.
func isCoreRoute(e *routeEntry) bool {
	return e.info.Owner == "core" && (e.info.Kind == RouteKindNative || e.info.Kind == RouteKindRaw)
}

// applyCoRequirements sets each route's co-requirements from
// Options.CoRequirements (cloned at NewRouter, so it cannot change between
// Builds and a second Build sets the same ones), returning one error per
// problem found; a bad entry is never attached. The catalog is the one
// SetAuth gave stage 13; r.mu is held. Build calls it once and it resets the
// entries it sets first, so a second Build cannot stack them.
//
// An entry attaches to EVERY route entry carrying the name, not the first one
// found: the router has no second mount of a route (a core route name is
// unique, Build refuses a repeat as ErrDuplicateRoute), but should a future
// alias or versioned mount ever carry the name, it gets the co-requirement too
// instead of becoming the way around it.
func (r *Router) applyCoRequirements() []error {
	for _, e := range r.routes {
		e.co, e.coBy = rbac.Requirement{}, nil
	}
	if len(r.opts.CoRequirements) == 0 {
		return nil
	}
	var cat *rbac.Catalog
	if r.auth != nil {
		cat = r.auth.opts.Catalog
	}
	var errs []error
	seen := map[string]bool{}
	for _, c := range r.opts.CoRequirements {
		label := coLabel(c)
		problems := checkCoRequirement(label, c.Need, cat)
		key := c.Route + "\x00" + c.Param + "\x00" + strings.ToLower(c.Value)
		if seen[key] {
			problems = append(problems, fmt.Errorf("%w: %s", ErrCoRequirementDuplicate, label))
		}
		seen[key] = true
		found := false
		for _, e := range r.routes {
			if !isCoreRoute(e) || e.info.Name != c.Route {
				continue
			}
			found = true
			own := e.info.Perm
			attach := len(problems) == 0
			if own.Kind() != rbac.KindAllOf {
				errs = append(errs, fmt.Errorf("%w: %s: the route's own requirement is %s, and a co-requirement can only be ANDed onto AllOf or Need",
					ErrCoRequirementShape, describe(e.info), own))
				attach = false
			}
			if err := checkCoParam(c, e.info.Pattern); err != nil {
				errs = append(errs, err)
				attach = false
			}
			if !attach {
				continue
			}
			if c.Param == "" {
				e.co = c.Need
			} else {
				e.coBy = append(e.coBy, coRule{param: c.Param, value: c.Value, need: c.Need})
			}
		}
		errs = append(errs, problems...)
		if !found {
			errs = append(errs, fmt.Errorf("%w: %q is not the name of a registered core route", ErrCoRequirementRoute, c.Route))
		}
	}
	return errs
}

// coLabel names an entry in an error: the route, and "[param=value]" for a
// per-value one.
func coLabel(c CoRequirement) string {
	if c.Param == "" && c.Value == "" {
		return c.Route
	}
	return fmt.Sprintf("%s[%s=%s]", c.Route, c.Param, c.Value)
}

// checkCoParam reports why c's Param and Value cannot scope an entry on a
// route whose pattern is pattern, or nil.
func checkCoParam(c CoRequirement, pattern string) error {
	label := coLabel(c)
	switch {
	case c.Param == "" && c.Value == "":
		return nil
	case c.Param == "" || c.Value == "":
		return fmt.Errorf("%w: %s sets only one of Param and Value", ErrCoRequirementParam, label)
	case strings.ContainsAny(c.Value, "/{}"):
		return fmt.Errorf("%w: %s: Value %q must be a literal with no \"/\", \"{\" or \"}\"", ErrCoRequirementParam, label, c.Value)
	}
	for _, w := range patternWildcards(pattern) {
		if w == c.Param {
			return nil
		}
	}
	return fmt.Errorf("%w: %s: %q is not a single-segment wildcard of %s", ErrCoRequirementParam, label, c.Param, pattern)
}

// patternWildcards is the single-segment wildcard names of a mux pattern
// ("/a/{id}/b/{rest...}" -> [id]; "{$}" and multi-segment "{rest...}" are not
// single-segment wildcards a value can name).
func patternWildcards(pattern string) []string {
	var out []string
	for _, seg := range strings.Split(pattern, "/") {
		if len(seg) < 3 || seg[0] != '{' || seg[len(seg)-1] != '}' {
			continue
		}
		name := seg[1 : len(seg)-1]
		if name == "$" || strings.HasSuffix(name, "...") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// checkCoRequirement reports why co cannot be the co-requirement named label (a
// route, or route[param=value]): it must be AllOf or Need over scopes that are the app's (not core's),
// that cat declares, and that cat does not flag Sensitive or Confining.
func checkCoRequirement(label string, co rbac.Requirement, cat *rbac.Catalog) []error {
	switch co.Kind() {
	case rbac.KindAllOf:
	case rbac.KindPublic, rbac.KindAuthenticated:
		return []error{fmt.Errorf("%w: %q requires %s, which is not a scope", ErrCoRequirementScope, label, co)}
	default:
		return []error{fmt.Errorf("%w: %q requires %s", ErrCoRequirementShape, label, co)}
	}
	var errs []error
	reserved := rbac.ReservedNamespaces()
	for _, p := range co.Permissions() {
		if p == rbac.CoreAdmin || slices.Contains(reserved, p.Namespace()) {
			errs = append(errs, fmt.Errorf("%w: %q requires %s", ErrCoRequirementScope, label, p))
			continue
		}
		if cat == nil {
			errs = append(errs, fmt.Errorf("%w: %w: %q requires %s and the router has no catalog to check it against",
				ErrCoRequirementUnregistered, ErrUnknownScope, label, p))
		} else if spec, ok := cat.Lookup(p); !ok {
			errs = append(errs, fmt.Errorf("%w: %w: %q requires %s", ErrCoRequirementUnregistered, ErrUnknownScope, label, p))
		} else if spec.Sensitive || spec.Confining {
			errs = append(errs, fmt.Errorf("%w: %q requires %s, which the catalog marks %s", ErrCoRequirementRestricted, label, p, restrictedFlags(spec)))
		}
	}
	return errs
}

// restrictedFlags names the refused flags of spec for the error.
func restrictedFlags(spec rbac.PermissionSpec) string {
	switch {
	case spec.Sensitive && spec.Confining:
		return "Sensitive and Confining"
	case spec.Sensitive:
		return "Sensitive"
	}
	return "Confining"
}
