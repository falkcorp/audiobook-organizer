// file: core/auth/method.go
// version: 1.0.3
// guid: 52a74e31-1d25-42c9-950d-3275d209be9e
// last-edited: 2026-11-11

package auth

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
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

// isExtension reports whether m is an identity extension's method ("ext:<name>",
// see extensionPrefix), the one class of method whose principal carries a
// notAfter bound.
func (m Method) isExtension() bool { return strings.HasPrefix(string(m), extensionPrefix) }

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
//
// These two are core's classification of core's own methods ONLY. An identity
// extension declares its own at registration (MethodSpec), which a Method
// alone cannot see: ext:cfaccess answers false here whatever its spec says.
// Every authorization decision therefore asks the principal
// (Principal.Interactive, Principal.MayChangeCredentials), which carries the
// registered classification; asking the Method is the F4-08 B10y bug that
// locked an Access-only owner out of the owner routes.
func (m Method) Interactive() bool { return m == MethodSession }

// MayChangeCredentials: see Interactive. An allowlist, so a method nobody
// classified fails closed. Core methods only; authorization asks
// Principal.MayChangeCredentials.
func (m Method) MayChangeCredentials() bool {
	return m == MethodSession || m == MethodSessionDelegated
}

// methodClass is the classification of one method as the service's registry
// answered it when PrincipalFor built the principal: core's own
// (Method.Interactive, Method.MayChangeCredentials) or what the method's
// MethodSpec declared at registration. method names the method it was read
// for, so a holder that edits Principal.Method does not carry another
// method's classification over to it.
type methodClass struct {
	method               Method
	interactive          bool
	mayChangeCredentials bool
}

// Interactive reports whether p's credential is a person's own interactive
// sign-in: core's MethodSession, or an identity extension registered with
// MethodSpec.Interactive (Cloudflare Access). It is what Guards.Session, the
// Sensitive rule's refusal code and the owner-fallback questions ask. A nil
// principal, an unknown method, and a principal core did not build for a
// method outside core's own list are not interactive (fail closed).
func (p *Principal) Interactive() bool {
	if p == nil {
		return false
	}
	if p.Method.Interactive() {
		return true
	}
	return p.class.method != MethodNone && p.class.method == p.Method && p.class.interactive
}

// MayChangeCredentials reports whether p's credential may change credentials
// or identity (HEAD's credGuard, Guards.Interactive, config.Actor.Interactive):
// core's MethodSession and MethodSessionDelegated, or an identity extension
// registered with MethodSpec.MayChangeCredentials. It is a property of its own,
// not Interactive: a delegated session may change credentials (the
// reset-password flow) and is not interactive. Fails closed as Interactive
// does.
func (p *Principal) MayChangeCredentials() bool {
	if p == nil {
		return false
	}
	if p.Method.MayChangeCredentials() {
		return true
	}
	return p.class.method != MethodNone && p.class.method == p.Method && p.class.mayChangeCredentials
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
	// scopes are the scopes PrincipalFor built this principal over: nil is
	// the method's own rule (the user's role permissions), a non-empty list a
	// narrowing (an identity extension's Assertion.Scopes, a compat
	// surface's own, a key's). Refresh rebuilds over them, so a narrowed
	// credential stays narrowed; a key principal is rebuilt over the key's
	// current scopes instead. seal marks a principal core built and records
	// who it was built for (see principalSeal): one assembled elsewhere
	// carries no record of its narrowing, so Refresh refuses it rather than
	// guess, and so does one whose exported fields no longer say what the
	// seal does.
	scopes []rbac.Permission
	seal   principalSeal
	// class is the registry's classification of Method, set only by
	// PrincipalFor (see methodClass, Principal.Interactive).
	class methodClass
	// notAfter is when an identity extension's principal stops standing
	// (Assertion.ExpiresAt, or core.auth.extension_max_age after AuthTime);
	// zero for every other method, whose credential record carries its own
	// expiry. stands refuses at or after it, and refuses an extension
	// principal that has none (one PrincipalFor built, which only stage 12
	// bounds); Refresh copies it, never extends it.
	notAfter time.Time
}

// principalSeal is core's own record of the identity of a principal it built:
// the values of every exported field Refresh trusts to decide whom to rebuild
// and what to carry over, taken as copies when core finished the principal
// (never the *User, whose contents a holder can edit through the shared
// pointer). A holder of a principal can write its exported fields, so Refresh
// compares them with this before it believes any: a principal whose User.ID,
// Method, CredentialID, Assurance, AuthTime or ClientAddr was edited after core
// finished it is refused, and one core did not build has no seal and is
// refused. It is unexported, so nothing outside this package can set it.
type principalSeal struct {
	set          bool
	user         UserID
	method       Method
	credentialID string
	assurance    Assurance
	authTime     time.Time
	clientAddr   netip.Addr
}

// sealIdentity records p's exported identity as it stands now. Core calls it as
// the last step of finishing a principal: PrincipalFor, and again after each
// in-package step that sets a field afterwards (the client address, the
// credential and its time), so a legitimate edit is always sealed and only a
// holder's edit disagrees. It must never run on a principal a module holds.
func (p *Principal) sealIdentity() {
	s := principalSeal{set: true, method: p.Method, credentialID: p.CredentialID,
		assurance: p.Assurance, authTime: p.AuthTime, clientAddr: p.ClientAddr}
	if p.User != nil {
		s.user = p.User.ID // a copy of the string, not the pointer
	}
	p.seal = s
}

// checkSeal is nil when p is a principal core built whose exported identity
// still matches its seal, and otherwise names the first field that disagrees
// (ErrInvalidCredentials). It reads p once and mutates nothing.
func (p *Principal) checkSeal() error {
	bad := func(what string) error {
		return fmt.Errorf("%w: the principal's %s is not what auth built", ErrInvalidCredentials, what)
	}
	if p == nil || !p.seal.set {
		return fmt.Errorf("%w: a principal auth did not build cannot be refreshed", ErrInvalidCredentials)
	}
	switch s := p.seal; {
	case p.User == nil || p.User.ID != s.user:
		return bad("user")
	case p.Method != s.method:
		return bad("method")
	case p.CredentialID != s.credentialID:
		return bad("credential")
	case p.Assurance != s.assurance:
		return bad("assurance")
	case !p.AuthTime.Equal(s.authTime):
		return bad("auth time")
	case p.ClientAddr != s.clientAddr:
		return bad("client address")
	}
	return nil
}

// keyRef is the API key p was authenticated by, and false when p was not
// built from a key. It covers both shapes of key principal: core's own
// (Method MethodAPIKey, CredentialID the key) and a compat surface's (its own
// Method, keyID the key). Every gate that depends on the key (Revalidate,
// the effective set, key minting and management, CheckCredentialFor) asks
// this and never compares Method to MethodAPIKey, so a compat key principal
// cannot slip past a key rule by carrying another method name.
func (p *Principal) keyRef() (id string, ok bool) {
	switch {
	case p == nil:
		return "", false
	case p.keyID != "":
		return p.keyID, true
	case p.Method == MethodAPIKey:
		// An empty CredentialID is still a key principal: the read of key ""
		// finds nothing and every gate refuses, rather than skipping.
		return p.CredentialID, true
	}
	return "", false
}

// compatKey reports whether p is a compat surface's key principal: a key
// principal whose Method is not core's.
func (p *Principal) compatKey() bool { return p != nil && p.keyID != "" && p.Method != MethodAPIKey }

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
