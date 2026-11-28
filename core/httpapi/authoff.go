// file: core/httpapi/authoff.go
// version: 0.1.0
// guid: 5e7d1c2a-93b4-4f60-8a1e-b2c6d0f4a739
// last-edited: 2026-11-22

package httpapi

import (
	"net/http"

	"github.com/falkcorp/mediacore/auth"
)

// The auth-disabled principal's refusals (F4-08 ruling G5(a), 2026-11-22).
//
// With core.auth.mode disabled the SPA shell treats a 401 whose problem code
// ends the session (authentication_required, expired, revoked, inactive_user,
// invalid_credential: core/web/packages/api-client/src/client.ts
// SESSION_ENDING_CODES) as "signed out" and drops a no-auth install to the
// sign-in screen, which has nothing to sign in to. So a refusal of the
// auth-disabled principal is never a 401: it is a 403 interactive_required, the
// code SEC5-8's Guards.Interactive tightening already uses, which a client
// reads as "turn authentication on to do this" and which does not end the
// session. The action is still refused; only the status and code change. A
// request with no principal at all (authentication on, no credential) keeps its
// 401 authentication_required.

// authOffDetail is the detail of AuthOffRefusal.
const authOffDetail = "turn authentication on to use this route"

// AuthOffRefusal is the refusal of the auth-disabled principal wherever a route
// needs something it cannot have: a user to name as the actor, credential
// metadata, a scope that is AuthRequired. It is 403 interactive_required, never
// the session-ending 401.
func AuthOffRefusal() *Error {
	return &Error{Status: http.StatusForbidden, Code: "interactive_required", Detail: authOffDetail}
}

// UserRequired is the refusal of a route that needs a user the caller does not
// have. It never returns nil, so a caller cannot turn it into an admit.
//
//   - the auth-disabled principal: AuthOffRefusal (403 interactive_required);
//   - no principal, or a principal that is neither signed in nor auth-disabled:
//     401 authentication_required, as before.
func UserRequired(p *auth.Principal) *Error {
	if p != nil && p.Method == auth.MethodAuthDisabled {
		return AuthOffRefusal()
	}
	return errAuthRequired()
}
