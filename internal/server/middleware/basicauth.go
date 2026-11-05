// file: internal/server/middleware/basicauth.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-11-05

package middleware

import (
	"crypto/subtle"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/gin-gonic/gin"
)

// BasicGateNoPasswordToken is the /health degraded token raised while the
// Basic gate is enabled with no effective password (05-health §5H.3.1).
const BasicGateNoPasswordToken = "basic_gate_no_password"

// BasicGateEnvPasswordWarning is the warn line logged while the gate takes its
// password from BASIC_AUTH_PASSWORD because the saved one is empty. It names
// the setting and the variable, never the value.
const BasicGateEnvPasswordWarning = "Basic authentication: the saved password (basic_auth_password) is empty; using BASIC_AUTH_PASSWORD in memory. Save a password in Settings."

// basicAuthPasswordEnv is the environment variable the gate falls back to when
// the saved password is empty (05-authn §11.1 item 5, Recovery).
const basicAuthPasswordEnv = "BASIC_AUTH_PASSWORD"

// basicGateLog carries this file's diagnostics through the log-injection barrier.
var basicGateLog = logger.New("http")

// basicGateEnvWarnOnce makes the gate log BasicGateEnvPasswordWarning once per
// process, on the first request that reaches the comparison.
var basicGateEnvWarnOnce sync.Once

// basicGatePassword returns the password the gate compares against: the stored
// basic_auth_password, or, when that is empty, BASIC_AUTH_PASSWORD from the
// environment, in memory only and never written (05-authn §11.1 item 5,
// Recovery). fromEnv reports the fallback; "" means no password.
//
// An env value that is empty or only blanks counts as unset (09 §9.1
// "Precedence"). The env value is never assigned into config.AppConfig or any
// store: persisting it would turn a recovery into a saved credential.
func basicGatePassword() (pass string, fromEnv bool) {
	if stored := config.AppConfig.BasicAuthPassword; stored != "" {
		return stored, false
	}
	if env := strings.TrimSpace(os.Getenv(basicAuthPasswordEnv)); env != "" {
		return env, true
	}
	return "", false
}

// BasicGateNoPassword reports whether the gate is enabled with no effective
// password: then every non-exempt route answers 401 and health shows
// BasicGateNoPasswordToken.
func BasicGateNoPassword() bool {
	if !config.AppConfig.BasicAuthEnabled {
		return false
	}
	pass, _ := basicGatePassword()
	return pass == ""
}

// BasicGateUsesEnvPassword reports whether the gate is enabled and takes its
// password from BASIC_AUTH_PASSWORD because the saved one is empty (the
// in-memory recovery). The startup log uses it to choose the warn line over
// the error line.
func BasicGateUsesEnvPassword() bool {
	if !config.AppConfig.BasicAuthEnabled {
		return false
	}
	_, fromEnv := basicGatePassword()
	return fromEnv
}

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
	return slices.Contains(c.HandlerNames(), absRequireAuthHandlerName)
}
