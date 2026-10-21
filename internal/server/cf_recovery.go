// file: internal/server/cf_recovery.go
// version: 1.0.0
// guid: 7a4c2e91-5b3d-4f08-a6e2-1d9c0b8f3e57
// last-edited: 2026-10-20

package server

import (
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// cfRecoveryExcludedPrefixes are path prefixes the web-UI service-token recovery
// must NEVER act on, on top of everything isNonSPAPath already keeps off the SPA
// shell (/api, /auth/, /socket.io/). Two groups:
//
//   - the Audiobookshelf-compatible surface and its machine clients (/ping,
//     /status, /login, /logout, /public/, /abs/, /audiobookshelf/): AudioBooth,
//     Absorb and ShelfPlayer carry service tokens there on purpose, and
//     /auth/openid and /auth/openid/callback are covered by /auth/;
//   - operational endpoints and Cloudflare's own paths (/health, /metrics,
//     /cdn-cgi/).
//
// /auth/openid/browser (SSO hop 2) is in /auth/ and has its own recovery in
// handlers/abs/openid.go.
var cfRecoveryExcludedPrefixes = []string{
	"/ping", "/status", "/login", "/logout", "/public/", "/abs/", "/audiobookshelf/",
	"/health", "/metrics", "/cdn-cgi/",
}

// isCFRecoverySPAPath reports whether p is an SPA PAGE route: unmatched by any
// registered handler (this runs from the NoRoute fallback), not a server-side
// path, not on the ABS surface, and not a static asset.
func isCFRecoverySPAPath(p string) bool {
	if p == "" || isNonSPAPath(p) {
		return false
	}
	for _, prefix := range cfRecoveryExcludedPrefixes {
		if strings.HasPrefix(p, prefix) || p == strings.TrimSuffix(prefix, "/") {
			return false
		}
	}
	// Static assets: anything under /assets/, or whose last segment has a file
	// extension (favicon.ico, manifest.webmanifest, sw.js, ...). SPA routes in
	// this app have none.
	if strings.HasPrefix(p, "/assets/") || path.Ext(p) != "" {
		return false
	}
	return true
}

// newCFRecoveryMiddleware is the web-UI service-token browser recovery on v, the
// verifier CloudflareAccessAuth uses. See middleware/cfbounce.go for the failure
// it recovers and the rules for what it may touch.
func newCFRecoveryMiddleware(v servermiddleware.CFAssertionVerifier) gin.HandlerFunc {
	return servermiddleware.CFServiceTokenRecovery(servermiddleware.NewCFServiceTokenBouncer(v), isCFRecoverySPAPath)
}
