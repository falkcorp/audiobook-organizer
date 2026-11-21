// file: internal/server/legacyauth/abs.go
// version: 0.1.0
// guid: 4d9b7e2c-1a68-4c35-8f0b-6e3a2d5c9f17
// last-edited: 2026-11-20

package legacyauth

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

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
)

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
// or HTML body): 401 for no or a refused credential, 403 for a confined one,
// 429 when throttled, 503 when the store is unavailable, and 500 when the
// adapter was built without a handle or resolver.
func ABSRequireAuth(compat coreauth.Compat, resolve ABSResolver, perm rbac.Requirement) gin.HandlerFunc {
	return func(c *gin.Context) {
		if compat == nil || resolve == nil {
			absAbort(c, http.StatusInternalServerError, msgABSUnconfigured)
			return
		}
		cred, err := resolve(c)
		if err != nil || (cred.Key == "") == (cred.User == nil) {
			absAbort(c, http.StatusUnauthorized, msgAuthRequired)
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
			absAbort(c, status, msg)
			return
		}
		ctx = coreauth.WithPrincipal(ctx, p)
		ctx = rbac.WithChecker(ctx, p.Perms)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// absRefusal maps a compat refusal to its status and message. No message
// quotes the error, which can name a user or a key ID.
func absRefusal(err error) (int, string) {
	switch {
	case errors.Is(err, coreauth.ErrConfined):
		return http.StatusForbidden, msgABSConfined
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
