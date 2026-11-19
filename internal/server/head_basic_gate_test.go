// file: internal/server/head_basic_gate_test.go
// version: 1.0.0
// guid: 6e0b2c94-1d7a-4f35-9a68-b3c5e7d10f42
// last-edited: 2026-11-19

package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// HEAD's Basic gate, frozen. Until F3-07 e4 the gate was a gin middleware,
// internal/server/middleware/basicauth.go; core's stage 8 replaced it and the
// file was deleted. This is its decision logic as it stood at c3474de03, kept
// verbatim as the reference the parity tests compare stage 8 against, as
// perimeter_stages_test.go keeps HEAD's security headers and CORS: a request
// of the HEAD reference stack carries headGateKey (withHeadBasicGate) and
// runs the frozen gate in the engine through basicGateHookForTest; a request
// of the R1 stack does not, and meets only core's stage 8.

type headGateKey struct{}

func init() { basicGateHookForTest = headBasicGateHook }

// withHeadBasicGate marks every request of the HEAD reference stack.
func withHeadBasicGate(engine http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engine.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), headGateKey{}, true)))
	})
}

// headBasicGateHook is HEAD's BasicAuth() gin middleware, for the requests
// that carry headGateKey.
func headBasicGateHook(c *gin.Context) {
	if c.Request.Context().Value(headGateKey{}) == nil {
		c.Next()
		return
	}
	if headBasicGateRefuses(c.Request, func() bool { return headRouteEnforcesABSAuth(c) }) {
		c.Header("WWW-Authenticate", headBasicChallenge)
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Next()
}

const headBasicChallenge = `Basic realm="Audiobook Organizer"`

// headBasicGatePassword is HEAD's basicGatePassword.
func headBasicGatePassword(stored string) string {
	if stored != "" {
		return stored
	}
	return strings.TrimSpace(os.Getenv("BASIC_AUTH_PASSWORD"))
}

// headBasicGateRefuses is HEAD's basicGateRefuses, with the env-warning log
// dropped (stage 8's binding logs it; the log is checked on its own).
func headBasicGateRefuses(r *http.Request, routeExempt func() bool) bool {
	cfg := config.Snapshot()
	if !cfg.BasicAuthEnabled {
		return false
	}
	path := r.URL.Path
	if path == "/api/health" || path == "/api/v1/health" {
		return false
	}
	if strings.HasPrefix(path, "/assets/") ||
		path == "/favicon.ico" ||
		strings.HasSuffix(path, ".js") ||
		strings.HasSuffix(path, ".css") ||
		strings.HasSuffix(path, ".png") ||
		strings.HasSuffix(path, ".svg") ||
		strings.HasSuffix(path, ".woff2") {
		return false
	}
	if routeExempt() {
		return false
	}
	password := headBasicGatePassword(cfg.BasicAuthPassword)
	if password == "" {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		return true
	}
	userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(cfg.BasicAuthUsername)) == 1
	passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1
	return !userMatch || !passMatch
}

var headAbsRequireAuthHandlerName = headHandlerFuncName(servermiddleware.ABSRequireAuth(nil))

func headHandlerFuncName(h gin.HandlerFunc) string {
	return runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
}

// headRouteEnforcesABSAuth is HEAD's routeEnforcesABSAuth.
func headRouteEnforcesABSAuth(c *gin.Context) bool {
	if c.FullPath() == "" {
		return false
	}
	return slices.Contains(c.HandlerNames(), headAbsRequireAuthHandlerName)
}
