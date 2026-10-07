// file: internal/server/middleware/credential_guard.go
// version: 1.0.0
// guid: 562599c5-5135-460b-8cd9-1ea9f91081f6
// last-edited: 2026-10-07

package middleware

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
)

// CredentialChangeRefusedMessage is the 403 message for a credential or
// identity change made with a method that may not make one (an API key, an
// ABS token, or no recorded method). One phrase everywhere, so logs and tests
// can tell this refusal from a missing permission.
const CredentialChangeRefusedMessage = "API keys cannot change passwords, users, roles, invites, sessions or sign-in settings; sign in to do this"

// CredentialChangeAllowed reports whether the request in c may change
// credentials or identity (auth.Method.MayChangeCredentials on the method
// the verifying stage recorded). Unknown or missing methods are refused.
func CredentialChangeAllowed(c *gin.Context) bool {
	return CurrentAuthMethod(c).MayChangeCredentials()
}

// RefuseCredentialChange writes the 403 for a refused credential change and
// logs it. Handlers whose guard is conditional (a key for ANOTHER user) call
// it directly; whole routes use RequireCredentialChangeMethod.
func RefuseCredentialChange(c *gin.Context) {
	LogCredentialChangeRefusal(c)
	httputil.RespondWithForbidden(c, CredentialChangeRefusedMessage)
	c.Abort()
}

// LogCredentialChangeRefusal logs a refused credential change: the method,
// the user and the API key that tried it. For handlers that write their own
// 403 body.
func LogCredentialChangeRefusal(c *gin.Context) {
	method := CurrentAuthMethod(c)
	userID := ""
	if u, ok := CurrentUser(c); ok && u != nil {
		userID = u.ID
	}
	keyID := ""
	if k, ok := CurrentAPIKey(c); ok && k != nil {
		keyID = k.ID
	}
	slog.Warn("credential change refused for non-interactive auth method",
		"method", string(method), "user", userID, "api_key", keyID,
		"route", c.FullPath(), "http_method", c.Request.Method)
}

// RequireCredentialChangeMethod refuses the route to any request whose auth
// method may not change credentials or identity. It must run after the auth
// middleware, which records the method. The caller decides whether to attach
// it at all (the server skips it when auth is disabled, like s.perm).
func RequireCredentialChangeMethod() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !CredentialChangeAllowed(c) {
			RefuseCredentialChange(c)
			return
		}
		c.Next()
	}
}
