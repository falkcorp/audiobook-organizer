// file: core/internal/rbachttp/errors.go
// version: 1.0.0
// guid: 69b1d61d-585a-4f6f-b17d-d80d01b620cc
// last-edited: 2026-11-10

package rbachttp

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// mapError turns the service's sentinels into the statuses 05-rbac §4.7 and
// §8 fix, reading the sentinel and never the message. Every Detail is fixed
// text: the service's messages name roles, scopes and versions, which stay on
// Error.Err (httpapi never serializes it) so no answer carries a value the
// service formatted. permField is the JSON pointer a permission-list problem
// is reported at ("" when the request carried no list, so the stored set is
// what failed).
//
// Where 05-rbac names a status and code the row says so below; the rest are
// coined here and listed in the PR report:
//
//	ErrEscalation       403 scope_escalation   (§8; the service audits it)
//	ErrDominance        403 dominance_required (§4.7 rule 5, §8; audited by the route)
//	ErrDenied           403 permission_denied
//	ErrSeedRoleLocked   409 seed_role_locked   (§4.8, §8)
//	ErrRoleInUse        409 role_in_use        (§4.7 rule 4)
//	ErrLastAdmin        409 last_admin         (§4.6, §8)
//	ErrLockedRole       409 role_locked        (coined: admin; §4.7 rule 3 gives no code)
//	ErrRoleExists       409 role_exists        (coined)
//	ErrNoPendingSeed    409 no_pending_seed    (coined)
//	ErrVersionMismatch  412 precondition_failed (§8)
//	ErrUnknownRole      404 not_found
//	ErrRoleName, ErrBadScope, ErrUnknownScope 422 validation_failed
//	ErrNotFrozen        503 unavailable
//
// ErrFrozen and ErrConfiningSeed are boot-time refusals (a declaration after
// Freeze, a confining seed) that none of these methods returns; like any
// other error they are returned unchanged and httpapi answers 500 with the
// text withheld.
func mapError(err error, permField string) error {
	switch {
	case err == nil:
		return nil
	// A refusal that wraps ErrUnknownRole (CanAssign, Dominates) is still a
	// refusal, so these two come before the 404.
	case errors.Is(err, rbac.ErrEscalation):
		return &httpapi.Error{Status: http.StatusForbidden, Code: "scope_escalation",
			Detail: "a role may be written only by an actor who holds every permission the change touches", Err: err}
	case errors.Is(err, rbac.ErrDominance):
		return &httpapi.Error{Status: http.StatusForbidden, Code: "dominance_required",
			Detail: "the actor does not hold every permission of the target", Err: err}
	case errors.Is(err, rbac.ErrDenied):
		return &httpapi.Error{Status: http.StatusForbidden, Code: "permission_denied", Detail: "permission denied", Err: err}
	case errors.Is(err, rbac.ErrUnknownRole):
		return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such role", Err: err}
	case errors.Is(err, rbac.ErrLockedRole):
		return &httpapi.Error{Status: http.StatusConflict, Code: "role_locked", Detail: "this role is locked and cannot be changed", Err: err}
	case errors.Is(err, rbac.ErrSeedRoleLocked):
		return &httpapi.Error{Status: http.StatusConflict, Code: "seed_role_locked",
			Detail: "seed roles are read-only; create a custom role instead, or accept a release's pending permissions", Err: err}
	case errors.Is(err, rbac.ErrRoleInUse):
		return &httpapi.Error{Status: http.StatusConflict, Code: "role_in_use",
			Detail: "the role is assigned to users; take it from them first", Err: err}
	case errors.Is(err, rbac.ErrLastAdmin):
		return &httpapi.Error{Status: http.StatusConflict, Code: "last_admin",
			Detail: "the change would leave no active administrator", Err: err}
	case errors.Is(err, rbac.ErrRoleExists):
		return &httpapi.Error{Status: http.StatusConflict, Code: "role_exists", Detail: "a role with this name exists", Err: err}
	case errors.Is(err, rbac.ErrNoPendingSeed):
		return &httpapi.Error{Status: http.StatusConflict, Code: "no_pending_seed",
			Detail: "the role has no pending seed permissions to accept", Err: err}
	case errors.Is(err, rbac.ErrVersionMismatch):
		return &httpapi.Error{Status: http.StatusPreconditionFailed, Code: "precondition_failed",
			Detail: "the role changed since it was read; read it again and retry", Err: err}
	case errors.Is(err, rbac.ErrNotFrozen):
		return &httpapi.Error{Status: http.StatusServiceUnavailable, Code: "unavailable",
			Detail: "roles are not available yet", Err: err}
	case errors.Is(err, rbac.ErrRoleName):
		return httpapi.Invalid(httpapi.Field("/body/name", "invalid_name", "a custom role is named custom. followed by 1 to 32 of a-z, 0-9, _ and -, starting with a letter"))
	case errors.Is(err, rbac.ErrBadScope), errors.Is(err, rbac.ErrUnknownScope):
		return permissionProblem(err, permField)
	}
	return err
}

// permissionProblem is the 422 of a permission the catalog refuses. Each
// cause present is its own field; none quotes the permission.
func permissionProblem(err error, field string) *httpapi.Error {
	if field == "" {
		field = "/body"
	}
	var fs []httpapi.FieldError
	if errors.Is(err, rbac.ErrBadScope) {
		fs = append(fs, httpapi.Field(field, "invalid_scope", "a permission is not a valid scope or pattern"))
	}
	if errors.Is(err, rbac.ErrUnknownScope) {
		fs = append(fs, httpapi.Field(field, "unknown_scope", "a permission is not one this binary declares"))
	}
	return httpapi.Invalid(fs...)
}

// unauthenticated is the 401 a route answers when no principal reached it.
// Stage 13 never passes a nil principal to a non-Public route, so this is a
// backstop for a mounting mistake, not the check. The code is the one
// 05-http-api §4.3 gives a request with no credential.
func unauthenticated() error {
	return &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"}
}

// unavailable is the 503 of a route mounted without its service.
func unavailable() error {
	return httpapi.Unavailable("roles are not available", 5*time.Second)
}

// guard is every handler's first line. It is not the route's permission
// check: Perm and Guards are the router's to enforce (05-http-api §4.2 stage
// 13, which F4-08 wires), and the service re-checks escalation on every write.
// It makes a handler fail closed when it is reached without what it needs: no
// principal (401), no service (503), a write by a principal that may not
// change credentials (403 interactive_required, §8: the same refusal
// Guards.Interactive makes), and a principal whose checker lacks perm (403).
// The interactive check comes first so a non-interactive administrator, whose
// checker holds no Sensitive scope at all (§4.4), is told why.
func (a *api) guard(p *auth.Principal, perm rbac.Permission, write bool) error {
	switch {
	case p == nil:
		return unauthenticated()
	case a.roles == nil:
		return unavailable()
	case write && !p.Method.MayChangeCredentials():
		return &httpapi.Error{Status: http.StatusForbidden, Code: "interactive_required",
			Detail: "changing roles needs an interactive session"}
	case p.Perms == nil || !p.Perms.Has(perm) || !p.Perms.Satisfies(rbac.Need(perm)):
		return httpapi.Forbidden(perm)
	}
	return nil
}

// ifVersion parses an If-Match header into the version a role write names. The
// header must hold the role's version, as a quoted entity tag ("3") or bare
// (3). Absent is 428 precondition_required (05-rbac §4.7 makes If-Match part
// of every role write; 05-http-api names no code for its absence, and RFC
// 6585's is this one). A value that is not a version is a 422 on the header:
// the wildcard and weak tags are refused because either would defeat the
// optimistic lock. A version no role has is the service's 412.
func ifVersion(header string) (int64, error) {
	h := strings.TrimSpace(header)
	if h == "" {
		return 0, &httpapi.Error{Status: http.StatusPreconditionRequired, Code: "precondition_required",
			Detail: "send the role's version in If-Match"}
	}
	bad := func() error {
		return httpapi.Invalid(httpapi.Field("/header/If-Match", "invalid_version", `must be the role's version, as "3" or 3`))
	}
	if len(h) >= 2 && h[0] == '"' && h[len(h)-1] == '"' {
		h = h[1 : len(h)-1]
	}
	if h == "" || strings.TrimLeft(h, "0123456789") != "" {
		return 0, bad()
	}
	n, err := strconv.ParseInt(h, 10, 64)
	if err != nil {
		return 0, bad()
	}
	return n, nil
}
