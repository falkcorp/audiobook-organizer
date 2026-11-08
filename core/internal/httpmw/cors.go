// file: core/internal/httpmw/cors.go
// version: 1.0.0
// guid: 344601db-fffb-4a53-bb14-6fdf0fa65114
// last-edited: 2026-11-08

package httpmw

import (
	"net/http"
	"strings"
)

// The CORS answer headers, byte for byte HEAD's (server_middleware.go
// setCORSHeaders).
const (
	corsAllowHeaders = "Content-Type, Content-Length, Accept-Encoding, Authorization, Cache-Control, X-Requested-With"
	corsAllowMethods = "POST, OPTIONS, GET, PUT, DELETE, PATCH"
)

// cors is stage 6 (05-http-api §4.2): an allowed Origin (same-origin, or on
// core.http.cors.allowed_origins) gets HEAD's CORS headers on every
// response; every OPTIONS is answered here, before routing and before the
// Basic gate, as HEAD's gin CORS middleware did: 204, or 403 with an empty
// body for an Origin that is not allowed.
func (c *Chain) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		allowed := origin != "" && c.originAllowed(origin, r)
		if allowed {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			h.Set("Access-Control-Allow-Methods", corsAllowMethods)
		}
		if r.Method == http.MethodOptions {
			if origin != "" && !allowed {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed reports origin as the request's own (scheme-agnostic, by
// Host, as HEAD compared it) or on the allow-list.
func (c *Chain) originAllowed(origin string, r *http.Request) bool {
	if host := strings.TrimSpace(r.Host); host != "" && (origin == "http://"+host || origin == "https://"+host) {
		return true
	}
	return c.current().origins[strings.TrimRight(origin, "/")]
}
