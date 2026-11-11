// file: core/auth/method.go
// version: 1.0.2
// guid: 52a74e31-1d25-42c9-950d-3275d209be9e
// last-edited: 2026-11-11

package auth

import (
	"context"
	"net/netip"
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

// Method says how the request was authenticated. Core methods are constants;
// identity extensions use "ext:<name>", app compatibility surfaces "<moduleID>.<name>".
type Method string

const (
	MethodNone             Method = ""
	MethodSession          Method = "session"           // the user's own interactive sign-in (password or OIDC)
	MethodSessionDelegated Method = "session_delegated" // temp-login, invite, or session of unknown origin
	MethodAPIKey           Method = "api_key"
	MethodAuthDisabled     Method = "auth_disabled" // synthetic principal in auth-off mode (§4.11)
)

// Interactive and MayChangeCredentials keep today's allowlist semantics
// (internal/auth/context.go): among core methods only MethodSession is
// interactive. MethodSession and MethodSessionDelegated may change
// credentials: the reset-password flow is an admin minting a temp-login link
// and the user's delegated session then setting the password, and every route
// that mints a delegated session is itself guarded by this check, so a key
// cannot reach one (owner decision 2026-11-06, amending 05-authn §3.1's "only
// MethodSession"; seams.d/R1-12.md). MethodAuthDisabled is explicitly
// non-interactive and MayChangeCredentials=false (fail closed). This is a
// deliberate tightening: HEAD's credential guard is a pass-through with auth
// off (credential_routes.go:79-82), stated in R2's release note (§4.11, SEC5-8).
// Unknown methods are non-interactive and may not change credentials.
// Extensions declare their own classification at registration (MethodSpec).
func (m Method) Interactive() bool { return m == MethodSession }

// MayChangeCredentials: see Interactive. An allowlist, so a method nobody
// classified fails closed.
func (m Method) MayChangeCredentials() bool {
	return m == MethodSession || m == MethodSessionDelegated
}

type Assurance uint8

const (
	AssuranceNone  Assurance = 0
	AssuranceOwner Assurance = 1 << iota // an OwnerVerifier proved this principal is the deployment owner
	AssuranceMFA                         // reserved: a second factor was presented (WebAuthn, later)
)

// Principal is the authenticated caller bound into the request context.
type Principal struct {
	User         *User // nil only for MethodAuthDisabled
	Method       Method
	CredentialID string       // session public id or API key id; never a secret
	Perms        rbac.Checker // always built by core: rbac.Service.Compile(roles, key scopes, interactive) (05-rbac §4.4); never supplied by an authenticator (§3.3)
	Assurance    Assurance
	AuthTime     time.Time // when the credential was issued / sign-in happened
	ClientAddr   netip.Addr
	// keyID is the API key a compat surface's principal was built from
	// (Compat.AuthenticateKey): its Method is the surface's, not
	// MethodAPIKey, and CheckCredentialFor still has to re-read the key.
	keyID string
}

// principalKey is the context key of WithPrincipal; unexported so only this
// package can bind or read a principal.
type principalKey struct{}

// WithPrincipal returns a context carrying p. A nil p binds no principal: the
// result reports none from PrincipalFrom, even over a context that had one.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal bound into ctx by WithPrincipal, and
// false when there is none.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p, p != nil
}
