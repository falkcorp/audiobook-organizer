// file: internal/server/legacyauth/abs.go
// version: 0.1.0
// guid: 4d9b7e2c-1a68-4c35-8f0b-6e3a2d5c9f17
// last-edited: 2026-11-20

package legacyauth

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// AccessClaims is a verified Cloudflare Access person as the ABS surface hands
// it to core: the token's stable subject, its e-mail and whether the verifier
// vouched for that address. The provider is not here: it is the admission
// policy's (books binds "cfaccess"), so a caller cannot name another one.
type AccessClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
}

// AccessResolver admits a verified Cloudflare Access person under core's
// account resolver and books' Access admission policy (the one the Access
// extension holds), and returns core's user: found by the linked subject,
// linked by a verified address, or created. It reads and writes core's store
// only, never books'. Its refusals are the oidc and auth sentinels core's
// resolver returns (oidc.ErrEmailNotAllowed, auth.ErrEmailInUse,
// auth.ErrInactiveUser, ...); the ABS surface maps each to HEAD's answer.
type AccessResolver func(ctx context.Context, c AccessClaims) (*coreauth.User, error)

// BooksUserFromCore is the books-shaped copy of a core user that the ABS
// surface's session minting, authorization-code mint and response body read:
// ID, Username, Email, Status, CredentialEpoch and CreatedAt. Nothing else of
// books' User is read on those paths (no hash, no roles). It is the one
// mapping of a core user the ABS surface makes: the password login, the
// refresh, the A7 callback and a resolved Access person all use it. It is a
// deliberate subset for that surface (it carries the epoch and no roles), not
// an account-list mapping: the store's account list and the Users page use
// LegacyUserFromCore (user_record.go, which names the lists that do not).
func BooksUserFromCore(u *coreauth.User) *database.User {
	return &database.User{
		ID:              string(u.ID),
		Username:        u.Username,
		Email:           u.Email,
		Status:          string(u.Status),
		CredentialEpoch: u.CredentialEpoch,
		CreatedAt:       u.CreatedAt,
	}
}

// ABSCredential is what the ABS surface's own verifier read from a request.
// Exactly one of Key and User is set.
type ABSCredential struct {
	// Key is a raw API key the request presented (an abk_ key). It is
	// passed to Compat.AuthenticateKey and never logged.
	Key string
	// User is a user the surface resolved itself, from its own token (an ABS
	// JWT or refresh session) or a Cloudflare Access login, with the epoch
	// the token was issued at and the surface's own narrowing (nil: the
	// user's role permissions).
	User   *coreauth.User
	Epoch  int64
	Scopes []rbac.Permission
	// Client is the caller as the mount resolved it (address, user agent,
	// proxy trust); core's throttle keys on it.
	Client coreauth.ClientInfo
}

// ABSResolver reads the request's ABS credential. It verifies the surface's
// own tokens; it decides no permission and builds no principal.
type ABSResolver func(c *gin.Context) (ABSCredential, error)

// Messages of the ABS refusals.
const (
	msgABSUnconfigured = "authentication is not available"
	msgABSUnavailable  = "the credential store is unavailable"
	msgABSThrottled    = "too many attempts; try again later"
	msgABSConfined     = "this credential is limited to the fingerprint worker API"
	msgABSInactive     = "account is not active"
)

// ABSRefusal is a refusal the surface's own resolver (ABSResolver) decided,
// with the status and message HEAD answered for the same condition. Returning
// one lets the resolver keep HEAD's distinctions (403 for a person who is not
// allowed, 500 for a store that could not answer) that a bare error would
// flatten to 401. The message must name no credential, user or key.
type ABSRefusal struct {
	Status  int
	Message string
}

func (e *ABSRefusal) Error() string { return "legacyauth: abs refusal: " + e.Message }

// ABSAdmitted is called once the compat handle admitted the credential and the
// principal and its checker are bound into the request context, and before the
// chain continues. It binds whatever else the handlers behind the surface read
// (HEAD's user, permission set and method). Returning false means it wrote its
// own refusal (and aborted); the chain then stops. cred is the credential the
// resolver produced.
type ABSAdmitted func(c *gin.Context, p *coreauth.Principal, cred ABSCredential) bool

// ABSRefused is called for every refusal before its body is written, with the
// status it will answer and the error behind it (nil for a refusal that has no
// error to name), so the surface can keep its own audit trail. It must not
// write a response.
type ABSRefused func(c *gin.Context, status int, err error)

// ABSRequireAuth is the adapter of HEAD's ABSRequireAuth for an OwnAuth route
// (05-http-api §8.3 ABS row), which stage 13 skips. It resolves the request
// through books' method-bound compat handle, never *auth.Service (SEC-1): a
// key through Compat.AuthenticateKey (stage 12's status, expiry and purpose
// rules), a surface-verified user through Compat.PrincipalFor (standing,
// epoch, sign-in restriction, core's throttle). Then it calls
// Compat.CheckCredentialFor(p, perm), perm being the route's manifest Perm,
// which refuses a Confined principal (an fp-worker key) outside its scopes
// (SEC2-4). On success it binds the principal and its checker into the
// request context, so Check and Can work inside the handler.
//
// Every refusal answers HEAD's ABS JSON body {"error": "..."} (never an empty
// or HTML body): 401 for no or a refused credential, 403 for a confined one or
// an account that is not active, 429 when throttled, 503 when the store is unavailable, and 500 when the
// adapter was built without a handle or resolver.
func ABSRequireAuth(compat coreauth.Compat, resolve ABSResolver, perm rbac.Requirement) gin.HandlerFunc {
	return ABSRequireAuthThen(compat, resolve, perm, nil, nil)
}

// ABSRequireAuthThen is ABSRequireAuth with the two hooks of a surface that
// keeps more than the principal (F4-08 B10b1): admitted runs after the
// principal is admitted and bound, and refused sees every refusal. Either may
// be nil. A resolver error that is an *ABSRefusal answers its own status and
// message; any other resolver error is 401.
func ABSRequireAuthThen(compat coreauth.Compat, resolve ABSResolver, perm rbac.Requirement, admitted ABSAdmitted, refused ABSRefused) gin.HandlerFunc {
	refuse := func(c *gin.Context, status int, msg string, err error) {
		if refused != nil {
			refused(c, status, err)
		}
		absAbort(c, status, msg)
	}
	return func(c *gin.Context) {
		if compat == nil || resolve == nil {
			refuse(c, http.StatusInternalServerError, msgABSUnconfigured, nil)
			return
		}
		cred, err := resolve(c)
		if err != nil {
			if r, ok := errors.AsType[*ABSRefusal](err); ok {
				refuse(c, r.Status, r.Message, err)
				return
			}
			refuse(c, http.StatusUnauthorized, msgAuthRequired, err)
			return
		}
		if (cred.Key == "") == (cred.User == nil) {
			refuse(c, http.StatusUnauthorized, msgAuthRequired, nil)
			return
		}
		ctx := c.Request.Context()
		var p *coreauth.Principal
		if cred.Key != "" {
			p, err = compat.AuthenticateKey(ctx, cred.Key, cred.Client)
		} else {
			p, err = compat.PrincipalFor(ctx, cred.User, cred.Scopes, cred.Epoch, cred.Client)
		}
		if err == nil && p == nil {
			err = coreauth.ErrInvalidCredentials
		}
		if err == nil {
			err = compat.CheckCredentialFor(p, perm)
		}
		if err != nil {
			status, msg := absRefusal(err)
			refuse(c, status, msg, err)
			return
		}
		ctx = coreauth.WithPrincipal(ctx, p)
		ctx = rbac.WithChecker(ctx, p.Perms)
		c.Request = c.Request.WithContext(ctx)
		if admitted != nil && !admitted(c, p, cred) {
			return
		}
		c.Next()
	}
}

// absRefusal maps a compat refusal to its status and message. No message
// quotes the error, which can name a user or a key ID.
func absRefusal(err error) (int, string) {
	switch {
	case errors.Is(err, coreauth.ErrConfined):
		return http.StatusForbidden, msgABSConfined
	case errors.Is(err, coreauth.ErrInactiveUser), errors.Is(err, coreauth.ErrResetRequired):
		// A credential that is fine for an account that may not sign in is 403,
		// as HEAD answered it (absCheckUserActive): a 401 is a client's cue to
		// forget the credential.
		return http.StatusForbidden, msgABSInactive
	case errors.Is(err, coreauth.ErrThrottled):
		return http.StatusTooManyRequests, msgABSThrottled
	case errors.Is(err, coreauth.ErrStoreUnavailable):
		return http.StatusServiceUnavailable, msgABSUnavailable
	default:
		return http.StatusUnauthorized, msgAuthRequired
	}
}

// absAbort writes the ABS surface's JSON refusal and stops the chain.
func absAbort(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}
