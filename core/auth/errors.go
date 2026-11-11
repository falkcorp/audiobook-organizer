// file: core/auth/errors.go
// version: 1.1.0
// guid: d92e3ae8-8bef-47eb-a6ac-a221aa99fd21
// last-edited: 2026-11-11

package auth

import "errors"

// The error block of 05-authn §3.3, plus the two store sentinels of §3.4.
// Every refusal auth returns wraps one of these, so a caller decides the
// response with errors.Is and never by matching text.
var (
	ErrNoCredential       = errors.New("auth: no credential presented")
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrExpired            = errors.New("auth: credential expired")
	ErrRevoked            = errors.New("auth: credential revoked")
	ErrInactiveUser       = errors.New("auth: user not active")
	ErrStaleEpoch         = errors.New("auth: credentials changed since issue")
	ErrThrottled          = errors.New("auth: throttled")
	ErrBootstrapInvalid   = errors.New("auth: bootstrap token invalid, expired or spent")
	ErrScopeEscalation    = errors.New("auth: requested scopes exceed caller's")
	ErrDominance          = errors.New("auth: actor does not dominate the target user") // wraps rbac.ErrDominance
	ErrLastAdmin          = errors.New("auth: change would leave no active core:admin holder")
	ErrEmailInUse         = errors.New("auth: email belongs to an account this provider may not link")
	ErrStoreUnavailable   = errors.New("auth: credential store unavailable")
	ErrMergeConflict      = errors.New("auth: an identity row changed since the merge preview")
	ErrConfined           = errors.New("auth: confined credential outside its scopes")
	ErrResetRequired      = errors.New("auth: password reset required before password sign-in")
	ErrMethodSpec         = errors.New("auth: compat method must be <moduleID>.<name>, non-interactive, without credential or owner rights")
)

// Errors coined by F4-02 (seams.d/F4-02.md): refusals the §3.2 methods need
// that the spec's block does not name.
var (
	// ErrDenied: the actor lacks the permission or the method the action
	// needs (core:users.manage, an interactive method, its own user).
	ErrDenied = errors.New("auth: actor may not perform this action")
	// ErrKeyRevoked: a status change out of revoked, or from
	// needs_confirmation to anything but active or revoked (409 key_revoked,
	// §4.4).
	ErrKeyRevoked = errors.New("auth: revoked key status is terminal")
	// ErrBadInput: a malformed request (an empty username, a short password,
	// an expiry above the maximum, an unknown status).
	ErrBadInput = errors.New("auth: invalid input")
	// ErrBusy: the password-hashing semaphore was not free within the wait
	// bound (503 busy, §6).
	ErrBusy = errors.New("auth: password hashing busy")
	// ErrUserExists: a username or e-mail another account holds.
	ErrUserExists = errors.New("auth: user exists")
	// ErrNotFound: the user, key, session or invite named does not exist.
	ErrNotFound = errors.New("auth: not found")
	// ErrSetupClosed: setup once a user exists, or without the bootstrap
	// token from a non-local caller (§4.6 items 3-4).
	ErrSetupClosed = errors.New("auth: setup is not available")
	// ErrInviteInvalid: an invite that is unknown, spent, expired, or whose
	// creator may no longer assign its role (SEC3-2).
	ErrInviteInvalid = errors.New("auth: invite invalid")
)

// ErrExtensionUnavailable is what an identity extension's Authenticate returns
// when it could not decide a request for a reason of its own, not because the
// credential is bad: its IdP's key set could not be fetched, its backend is
// down (05-authn §6, "Extension authenticator internal error"). Stage 12
// treats it as ErrNoCredential: it logs a warning, counts
// mediacore_auth_extension_errors_total{extension}, and the next
// authenticator runs, so an outage of an optional IdP cannot lock out
// password and key users. It opens only the extension: the extension granted
// nothing, and whatever the request carries is still decided by the methods
// after it. A presented assertion that verifies false (bad signature,
// audience, issuer or expiry) must NOT be reported with this error: that is a
// refusal and ends authentication with 401 (SEC-12). An extension that cannot
// tell the two apart must report the refusal.
var ErrExtensionUnavailable = errors.New("auth: identity extension unavailable")

// ErrUndecodableRecord: a stored identity record the store cannot decode. It
// is per record (§3.4): a scan yields it for that record and goes on, and
// auth treats the record as restricted (user inactive, key refused, §6).
var ErrUndecodableRecord = errors.New("auth: stored identity record cannot be decoded")

// ErrImportRejected: an Importer refused a record that breaks a Store
// invariant (§3.4).
var ErrImportRejected = errors.New("auth: imported record breaks an identity store invariant")
