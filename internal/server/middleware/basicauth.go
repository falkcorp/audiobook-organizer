// file: internal/server/middleware/basicauth.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-09-12

package middleware

import (
	"crypto/subtle"
	"net/http"
	"reflect"
	"runtime"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
)

// BasicAuth returns a Gin middleware that enforces HTTP Basic Authentication
// when config.AppConfig.BasicAuthEnabled is true. Health endpoints, static
// assets, and Audiobookshelf-compatible routes that enforce ABS token auth in
// their own handler chain are exempt.
func BasicAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.AppConfig.BasicAuthEnabled {
			c.Next()
			return
		}

		path := c.Request.URL.Path

		// Exempt health endpoints
		if path == "/api/health" || path == "/api/v1/health" {
			c.Next()
			return
		}

		// Exempt static assets (Vite-built frontend files)
		if strings.HasPrefix(path, "/assets/") ||
			path == "/favicon.ico" ||
			strings.HasSuffix(path, ".js") ||
			strings.HasSuffix(path, ".css") ||
			strings.HasSuffix(path, ".png") ||
			strings.HasSuffix(path, ".svg") ||
			strings.HasSuffix(path, ".woff2") {
			c.Next()
			return
		}

		// Exempt ABS routes that run ABSRequireAuth, by design rather than by
		// oversight: Basic Auth and ABS bearer auth both live in the single
		// Authorization header, so an ABS client cannot present both, and with
		// Basic Auth on, every ABS route would otherwise be unreachable.
		//
		// The test is "does the route gin matched run ABS token auth later in its
		// own chain", not a path prefix, so the exempt set is a subset of the
		// token-enforced set by construction:
		//   - /api/items/:id is exempt; /api/items/:id/cover (no ABS auth) is
		//     not. A path prefix cannot express that split.
		//   - /ping, /status, /login, /auth/refresh, /auth/openid*, the cover
		//     routes and /public/session/:id/track/:index carry no ABSRequireAuth,
		//     so Basic Auth remains their only gate and they stay challenged.
		//   - No request path is compared, so ".." segments, %2e%2e or any
		//     other spelling cannot widen the exemption: the decision follows
		//     the handler chain gin will actually run.
		//   - A new ABS route added with ABSRequireAuth is exempted
		//     automatically; one added without it stays behind Basic Auth.
		if routeEnforcesABSAuth(c) {
			c.Next()
			return
		}

		user, pass, ok := c.Request.BasicAuth()
		if !ok {
			c.Header("WWW-Authenticate", `Basic realm="Audiobook Organizer"`)
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		expectedUser := config.AppConfig.BasicAuthUsername
		expectedPass := config.AppConfig.BasicAuthPassword

		userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(expectedUser)) == 1
		passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(expectedPass)) == 1

		if !userMatch || !passMatch {
			c.Header("WWW-Authenticate", `Basic realm="Audiobook Organizer"`)
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		c.Next()
	}
}

// absRequireAuthHandlerName is the name gin reports in c.HandlerNames() for the
// handler ABSRequireAuth returns. It is derived from a real ABSRequireAuth value
// with the same expression gin's unexported nameOfFunction uses, so a rename or
// move of the closure changes both sides together. Every ABSRequireAuth call
// returns the same function literal, so one name covers every resolver. The nil
// resolver is never dereferenced: the closure is only named here, not invoked.
var absRequireAuthHandlerName = handlerFuncName(ABSRequireAuth(nil))

func handlerFuncName(h gin.HandlerFunc) string {
	return runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
}

// routeEnforcesABSAuth reports whether the route gin matched for this request
// runs ABSRequireAuth later in its own handler chain. Unmatched requests
// (NoRoute / NoMethod, where FullPath is empty) have no route chain and are
// never exempt. If the name lookup ever stops matching, this returns false and
// the request falls through to Basic Auth, so a mismatch fails closed.
func routeEnforcesABSAuth(c *gin.Context) bool {
	if c.FullPath() == "" {
		return false
	}
	for _, name := range c.HandlerNames() {
		if name == absRequireAuthHandlerName {
			return true
		}
	}
	return false
}
