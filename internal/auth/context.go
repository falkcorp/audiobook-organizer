// file: internal/auth/context.go
// version: 1.3.0
// guid: 8c4a2f1d-9b3e-4f60-a8d5-2c7e0f1b9a47
// last-edited: 2026-10-07
//
// Request-scoped auth state plumbing (spec 3.7). Long-lived deps
// (database.Store, services) live on the Server struct; per-request
// state (who is calling, what they can do) flows through
// context.Context using typed helpers so handlers can't accidentally
// read them as strings.

package auth

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type ctxKey int

const (
	userKey ctxKey = iota
	permissionsKey
	methodKey
)

// Method is how a request was authenticated: which verifier bound its
// user. It is recorded by the stage that verified the credential, never
// inferred from the transport (an "abk_" API key sent in the session cookie
// is still an API key).
type Method string

// Auth methods.
const (
	// MethodNone: no verifier bound a user (unauthenticated, or the
	// first-run bootstrap with no users).
	MethodNone Method = ""
	// MethodSession: a login session token (cookie or Bearer) issued by the
	// user's own sign-in (password or OAuth; database.Session.InteractiveLogin).
	MethodSession Method = "session"
	// MethodSessionDelegated: a session NOT proven to come from the user's
	// own sign-in: one minted through a temp-login link (which an admin, or
	// an admin API key, can mint for any user), an invite, or issued before
	// sessions recorded their origin. Not interactive.
	MethodSessionDelegated Method = "session_delegated"
	// MethodAPIKey: an "abk_" API key. Automation and agents use these.
	MethodAPIKey Method = "api_key"
	// MethodCFAccess: a verified Cloudflare Access SSO assertion resolved to
	// an allowlisted user. A service-token assertion never resolves a user
	// (internal/oauth/cfaccess.go), so this is always a person's SSO login.
	MethodCFAccess Method = "cf_access"
	// MethodABS: an identity bound by the Audiobookshelf-compatible surface
	// (its own JWT sessions or API keys); the mode is recorded there.
	MethodABS Method = "abs"
)

// Interactive reports whether m is a person's own login (a session or a
// Cloudflare Access SSO identity) rather than a credential automation holds
// (an API key, an ABS client token). Actions the owner reserves for himself
// (Repairs owner apply) are honoured only for these.
func (m Method) Interactive() bool { return m == MethodSession || m == MethodCFAccess }

// CredentialChangeRefusedMessage is the one 403 message for a credential,
// identity, sign-in setting, executable or server-path change attempted by a
// method that may not make one (an API key, an ABS token, or no recorded
// method). The route guard and the config service both use it, so logs and
// tests can tell this refusal from a missing permission.
const CredentialChangeRefusedMessage = "API keys cannot change passwords, users, roles, invites, sessions, keys of other users, sign-in settings, executables, database locations or server paths; sign in to do this"

// MayChangeCredentials reports whether a request authenticated by m may
// change credentials or identity: passwords, users, invites, temp-login
// links, keys for another user, sign-in settings.
//
// An allowlist, so a method nobody classified fails closed. A person's
// session is allowed whether it came from their own sign-in or was delegated
// (temp-login, invite, pre-origin): the reset-password flow is an admin
// minting a temp-login link and the user's delegated session then setting
// the password. An API key, an ABS client token and the empty method are
// refused. Every route that mints a delegated session is itself guarded by
// this check, so a key cannot reach one.
func (m Method) MayChangeCredentials() bool {
	switch m {
	case MethodSession, MethodSessionDelegated, MethodCFAccess:
		return true
	default:
		return false
	}
}

// WithMethod records how the request in ctx was authenticated.
//
// Downgrade only: once a method is recorded, a later call may replace it
// only with a non-interactive one. No stage running after the verifier
// (a fallback, a second binder on the same request) can turn an API-key or
// delegated request into an interactive one, while a later stage that
// verified a weaker credential still marks the request with it.
func WithMethod(ctx context.Context, m Method) context.Context {
	if m == MethodNone {
		return ctx
	}
	if cur := MethodFromContext(ctx); cur != MethodNone && m.Interactive() {
		return ctx
	}
	return context.WithValue(ctx, methodKey, m)
}

// MethodFromContext returns the method WithMethod recorded, MethodNone when
// none was.
func MethodFromContext(ctx context.Context) Method {
	if ctx == nil {
		return MethodNone
	}
	m, _ := ctx.Value(methodKey).(Method)
	return m
}

// WithUser attaches the calling user to ctx. Typically set by the
// authenticate middleware after session/JWT verification.
func WithUser(ctx context.Context, u *database.User) context.Context {
	if u == nil {
		return ctx
	}
	return context.WithValue(ctx, userKey, u)
}

// UserFromContext returns the user attached to ctx by WithUser, or
// (nil, false) if no user is set (unauthenticated request or a
// handler that bypassed auth middleware).
func UserFromContext(ctx context.Context) (*database.User, bool) {
	if ctx == nil {
		return nil, false
	}
	u, ok := ctx.Value(userKey).(*database.User)
	return u, ok && u != nil
}

// WithPermissions attaches the calling user's flattened permission
// set to ctx. Computed at session creation by unioning the role
// permissions and cached on the session blob; middleware copies it
// into request context so Can() is a cheap map lookup.
func WithPermissions(ctx context.Context, perms []Permission) context.Context {
	if len(perms) == 0 {
		return ctx
	}
	set := make(map[Permission]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return context.WithValue(ctx, permissionsKey, set)
}

// PermissionsFromContext returns the permission set attached to ctx,
// or nil if no permissions have been loaded (which effectively means
// the caller has none).
func PermissionsFromContext(ctx context.Context) map[Permission]struct{} {
	if ctx == nil {
		return nil
	}
	set, _ := ctx.Value(permissionsKey).(map[Permission]struct{})
	return set
}

// Can reports whether the caller in ctx has permission p.
//
// Returns false for an unauthenticated caller (no permissions set)
// and for an authenticated caller whose roles do not include p.
// Never panics — unset context or nil permission set both yield
// false, which is the safe default.
func Can(ctx context.Context, p Permission) bool {
	set := PermissionsFromContext(ctx)
	if set == nil {
		return false
	}
	_, ok := set[p]
	return ok
}
