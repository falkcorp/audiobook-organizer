// file: core/httpapi/stage13.go
// version: 0.1.0
// guid: 12d9b9cf-31b9-4a72-b686-1b854c0bd6fc
// last-edited: 2026-11-20

package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// Stage 13, authorize (05-http-api §4.2 row 13). Authorize is the decision;
// Stage13 is the per-route middleware that applies it. Neither is wired into
// the chain yet: F4-08's flip (B4) adds Stage13 to every route that runs
// stage 12, and until then checkMount refuses a LegacyAuthCore mount and
// Build refuses a non-Public native route (ErrNoAuthService).

// AtomResolver maps a legacy atom to its native requirement: books' atom
// table (05-http-api §8.3, 05-rbac §6.1), injected because core cannot import
// an app. readSide asks for the atom's read-side entry (a LegacyCheck marked
// read-side). ok is false for an atom the table does not know or for which it
// has no entry of the asked side; Authorize refuses such a requirement.
type AtomResolver func(atom string, readSide bool) (req rbac.Requirement, ok bool)

// AuthzOptions are the inputs of Authorize that are not the request's.
type AuthzOptions struct {
	// OwnerRequired is core.auth.owner.required (05-authn §4.8). When false,
	// Guards.Owner falls back to Guards.Interactive plus core:admin; it never
	// means "anyone".
	OwnerRequired bool
	// ResolveAtom resolves rbac.LegacyAtom and rbac.LegacyCheck requirements
	// before Satisfies (COR6-1): no checker evaluates a legacy kind. nil
	// refuses every legacy requirement.
	ResolveAtom AtomResolver
	// Catalog answers whether a missing scope is Sensitive, so a
	// non-interactive principal refused one is told interactive_required
	// rather than permission_denied (05-rbac §8). nil keeps every Perm
	// refusal permission_denied. It never admits anything.
	Catalog *rbac.Catalog
}

// AuthzRequest is what stage 13 judges for one request.
type AuthzRequest struct {
	// Perm and Guards are the route's (Route.Perm/Guards, LegacyRoute.Perm/Guards).
	Perm   rbac.Requirement
	Guards Guards
	// Failure is what stage 12 recorded (auth.FailureFrom), nil when none.
	Failure *auth.Failure
	// Principal is what stage 12 bound (auth.PrincipalFrom), nil when none.
	Principal *auth.Principal
}

// AuthzDecision is stage 13's answer. Refusal nil admits.
type AuthzDecision struct {
	// Refusal is the problem to answer instead of running the handler: 401
	// with the stage-12 failure code or authentication_required, 403
	// interactive_required, owner_required or permission_denied (with
	// required_permission when a scope is named).
	Refusal *Error
	// ClearCookie asks the caller to clear the session cookie: the refused
	// credential was the session cookie (05-authn §4.3 cookie rule). It is set
	// on a Public route's anonymous admit and on a non-Public route's 401.
	ClearCookie bool
}

// Admitted reports whether the request may reach the handler.
func (d AuthzDecision) Admitted() bool { return d.Refusal == nil }

// Authorize is stage 13's decision for one request (05-http-api §4.2 row 13),
// in the row's order:
//
//  1. A failure stage 12 recorded: a session-cookie failure on a Public route
//     proceeds anonymous and clears the cookie; any other failure is 401
//     with the failure code (cookie cleared when it was the cookie). An
//     invalid Bearer or API key is 401 on Public routes too (05-authn §4.3).
//  2. Public (with no Guards): admitted.
//  3. No principal: 401 authentication_required.
//  4. Guards: Interactive needs Method.MayChangeCredentials (403
//     interactive_required). Session needs Method.Interactive, the user's own
//     sign-in and not a delegated session (403 interactive_required). Owner
//     needs AssuranceOwner (403 owner_required) while OwnerRequired, else
//     Interactive plus core:admin (05-authn §4.8).
//  5. Perm through the principal's checker (confinement and Sensitive are the
//     checker's, 05-rbac §4.4, §4.10), a legacy kind resolved through
//     opts.ResolveAtom first: 403 permission_denied naming the missing scope,
//     403 interactive_required when that scope is Sensitive and the method is
//     not interactive (05-rbac §8). The auth-disabled principal is answered
//     401 authentication_required instead (05-rbac §4.11, 05-authn §4.11),
//     and is refused every LegacyCheck requirement (HEAD's in-handler
//     auth.Can denies it, 05-rbac §4.11).
//
// Every input it cannot prove admits is refused: a nil checker, a zero,
// Delegated or unresolvable requirement, an unknown atom, a resolver that
// answers with something other than AllOf or AnyOf. A Public requirement that
// carries Guards is judged as a non-Public one (the guards need a principal).
// Authorize audits nothing and writes nothing.
func Authorize(req AuthzRequest, opts AuthzOptions) AuthzDecision {
	public := req.Perm.Kind() == rbac.KindPublic && req.Guards == (Guards{})
	if f := req.Failure; f != nil {
		if public && f.Cookie {
			return AuthzDecision{ClearCookie: true}
		}
		return AuthzDecision{Refusal: errFailure(f.Code), ClearCookie: f.Cookie}
	}
	if public {
		return AuthzDecision{}
	}
	p := req.Principal
	if p == nil {
		return refuse(errAuthRequired())
	}
	if req.Guards.Interactive && !p.Method.MayChangeCredentials() {
		return refuse(errInteractiveRequired())
	}
	if req.Guards.Session && !p.Method.Interactive() {
		return refuse(errSessionRequired())
	}
	if req.Guards.Owner {
		if e := ownerRefusal(p, opts.OwnerRequired); e != nil {
			return refuse(e)
		}
	}
	return refuse(permRefusal(p, req.Perm, opts))
}

func refuse(e *Error) AuthzDecision { return AuthzDecision{Refusal: e} }

// ownerRefusal is Guards.Owner (05-authn §4.8): the owner assurance while
// core.auth.owner.required, else an interactive method and core:admin.
func ownerRefusal(p *auth.Principal, required bool) *Error {
	if required {
		if p.Assurance&auth.AssuranceOwner == 0 {
			return errOwnerRequired()
		}
		return nil
	}
	if !p.Method.MayChangeCredentials() {
		return errInteractiveRequired()
	}
	if p.Perms == nil || !p.Perms.Has(rbac.CoreAdmin) || !p.Perms.Satisfies(rbac.Need(rbac.CoreAdmin)) {
		return Forbidden(rbac.CoreAdmin)
	}
	return nil
}

// permRefusal is the Perm step: nil when p's checker satisfies the route's
// requirement, resolved from a legacy kind first.
func permRefusal(p *auth.Principal, perm rbac.Requirement, opts AuthzOptions) *Error {
	authOff := p.Method == auth.MethodAuthDisabled
	need, ok := resolve(perm, opts.ResolveAtom)
	if ok && authOff && perm.Kind() == rbac.KindLegacyCheck {
		ok = false
	}
	if ok && p.Perms != nil && p.Perms.Satisfies(need) {
		return nil
	}
	if authOff {
		return errAuthRequired()
	}
	if !ok {
		return &Error{Status: http.StatusForbidden, Code: "permission_denied", Detail: "the route's permission cannot be resolved"}
	}
	missing, named := missingScope(p.Perms, need)
	if !named {
		return &Error{Status: http.StatusForbidden, Code: "permission_denied"}
	}
	if opts.Catalog != nil && !p.Method.Interactive() {
		if spec, found := opts.Catalog.Lookup(missing); found && spec.Sensitive {
			return errInteractiveRequired()
		}
	}
	return Forbidden(missing)
}

// resolve returns the native requirement Satisfies judges: perm itself for a
// native kind, the resolver's answer for a legacy one. false refuses: a nil
// resolver, an unknown atom, or an answer that is not AllOf or AnyOf (Public,
// AnyAuthenticated, Delegated, another legacy kind or the zero value would
// each turn an atom route into something it is not).
//
// A Delegated or zero requirement is refused here as well, not left to the
// checker: an OwnAuth route's permission is its own handler's to enforce, and
// stage 13 never runs on it.
func resolve(perm rbac.Requirement, r AtomResolver) (rbac.Requirement, bool) {
	switch perm.Kind() {
	case rbac.KindPublic, rbac.KindAuthenticated, rbac.KindAllOf, rbac.KindAnyOf:
		return perm, true
	case rbac.KindLegacyAtom, rbac.KindLegacyCheck:
	default:
		return rbac.Requirement{}, false
	}
	if r == nil {
		return rbac.Requirement{}, false
	}
	need, ok := r(perm.Atom(), perm.ReadSide())
	if !ok {
		return rbac.Requirement{}, false
	}
	switch need.Kind() {
	case rbac.KindAllOf, rbac.KindAnyOf:
		return need, true
	}
	return rbac.Requirement{}, false
}

// missingScope names the scope a 403 reports as required_permission: for
// AllOf the first member the checker does not satisfy on its own, for AnyOf
// the first member. false for a requirement that names no scope.
func missingScope(c rbac.Checker, need rbac.Requirement) (rbac.Permission, bool) {
	ps := need.Permissions()
	if len(ps) == 0 {
		return "", false
	}
	if need.Kind() == rbac.KindAllOf && c != nil {
		for _, s := range ps {
			if !c.Satisfies(rbac.Need(s)) {
				return s, true
			}
		}
	}
	return ps[0], true
}

// errFailure is the 401 for a recorded failure. Its codes are the ones
// 05-http-api §4.3 (FE-7) and 05-authn §6 list, a sign-in-restricted user's
// password_reset_required included. Any other code, the empty one included,
// is answered invalid_credential, so a 401 never carries an unlisted code.
func errFailure(code string) *Error {
	var detail string
	switch code {
	case auth.CodeExpired:
		detail = "the credential presented has expired"
	case auth.CodeRevoked:
		detail = "the credential presented was revoked"
	case auth.CodeInactiveUser:
		detail = "this account is not active"
	case auth.CodeResetRequired:
		detail = "this account must reset its password with a reset link"
	default:
		code, detail = auth.CodeInvalidCredential, "the credential presented is not valid"
	}
	return &Error{Status: http.StatusUnauthorized, Code: code, Detail: detail}
}

func errAuthRequired() *Error {
	return &Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "sign in to use this route"}
}

func errInteractiveRequired() *Error {
	return &Error{Status: http.StatusForbidden, Code: "interactive_required", Detail: "this route needs an interactive sign-in, not an API key"}
}

// errSessionRequired is Guards.Session's refusal: the interactive_required
// code a client already handles, with a detail that names the delegated
// session too.
func errSessionRequired() *Error {
	return &Error{Status: http.StatusForbidden, Code: "interactive_required", Detail: "this route needs your own interactive sign-in, not a delegated session or an API key"}
}

func errOwnerRequired() *Error {
	return &Error{Status: http.StatusForbidden, Code: "owner_required", Detail: "this route needs an owner-verified sign-in"}
}

// Stage13Options configure the Stage13 middleware.
type Stage13Options struct {
	// OwnerRequired reports core.auth.owner.required per request: the value
	// is derived from settings and the owner verifier's OwnerSet, which can
	// change while the process runs (auth's ownerRequired). nil means
	// required, the refusing side.
	OwnerRequired func() bool
	// ResolveAtom and Catalog are AuthzOptions'.
	ResolveAtom AtomResolver
	Catalog     *rbac.Catalog
	// ClearCookie returns the cookie that clears the session cookie for r:
	// auth.Service.SessionCookie(client(r), "", time.Time{}), which needs the
	// ClientInfo httpapi's resolver builds. Required.
	ClearCookie func(r *http.Request) *http.Cookie
	// Log receives a refusal's write failure; nil is allowed.
	Log *slog.Logger
}

// ErrStage13Options is Stage13's refusal of options it cannot run with.
var ErrStage13Options = errors.New("httpapi: stage 13 options incomplete")

// Stage13 returns the stage-13 middleware of one route: it reads the
// principal or failure stage 12 left in the request context, applies
// Authorize with the route's perm and guards, clears the session cookie when
// the decision asks, and answers a refusal as a problem without calling next.
//
// Not wired yet. B4 (F4-08's flip) must: add it after stage 12 on every route
// stage 12 runs on (native, Infra routes whose perm is not Public, legacy
// manifest routes of a LegacyAuthCore mount) and never on an OwnAuth legacy
// route, whose Delegated requirement Authorize refuses; pass books' atom table
// as ResolveAtom, the catalog, auth's owner-required derivation and a
// ClearCookie over auth.Service.SessionCookie; audit each refusal
// (core.permission.deny, coalesced, 05-rbac §7.1) where it is written; and
// relax checkMount/ErrNoAuthService only when auth exists.
func Stage13(perm rbac.Requirement, guards Guards, o Stage13Options) (func(http.Handler) http.Handler, error) {
	return stage13(perm, guards, o, stage12Outcome)
}

// stage12Outcome is what stage 12 left in r's context: the failure it
// recorded and the principal it bound, each nil when absent.
func stage12Outcome(r *http.Request) (*auth.Failure, *auth.Principal) {
	var fail *auth.Failure
	if f, ok := auth.FailureFrom(r.Context()); ok {
		fail = &f
	}
	p, _ := auth.PrincipalFrom(r.Context())
	return fail, p
}

// stage13 is Stage13 with stage 12's outcome read by outcome, so a test can
// hand it a failure (auth records one only from its own middleware).
func stage13(perm rbac.Requirement, guards Guards, o Stage13Options, outcome func(*http.Request) (*auth.Failure, *auth.Principal)) (func(http.Handler) http.Handler, error) {
	if o.ClearCookie == nil {
		return nil, ErrStage13Options
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req := AuthzRequest{Perm: perm, Guards: guards}
			req.Failure, req.Principal = outcome(r)
			ownerRequired := o.OwnerRequired == nil || o.OwnerRequired()
			d := Authorize(req, AuthzOptions{OwnerRequired: ownerRequired, ResolveAtom: o.ResolveAtom, Catalog: o.Catalog})
			if d.ClearCookie {
				if ck := o.ClearCookie(r); ck != nil {
					http.SetCookie(w, ck)
				}
			}
			if d.Refusal != nil {
				WriteError(w, r, d.Refusal, o.Log)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}
