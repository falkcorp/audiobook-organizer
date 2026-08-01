// file: internal/server/toplevel_auth_chain.go
// version: 2.0.0
// guid: 7a1c4f28-95d6-4b03-8e2f-c61d0a8b3947
// last-edited: 2026-08-01

package server

import "github.com/gin-gonic/gin"

// buildTopLevelAuthChain composes the gin handler chain for an authenticated
// route registered directly on the router rather than inside the /api/v1 group.
//
// Used by /api/events (which must sit ahead of the /api/* redirect middleware)
// and /metrics (which lives at the root by Prometheus convention). Both inherit
// none of the /api/v1 group's middleware and have to assemble an equivalent
// chain by hand. The ordering mirrors /api/v1 exactly:
//
//	cfMW (fail-open identity binding)  →  authGuard (RequireAuth)  →  handler
//
// cfMW is optional and is nil whenever Cloudflare Access is not configured
// (CF_ACCESS_TEAM_DOMAIN / CF_ACCESS_AUD unset), so it is appended only when
// present — a nil gin.HandlerFunc in the chain would panic on dispatch.
//
// Why cfMW has to be here at all: under Cloudflare Access SSO the browser holds
// no application session cookie. Identity arrives solely as a verified
// Cf-Access-Jwt-Assertion header, and cfMW is the only stage that reads it and
// binds the resolved user. Without cfMW the authGuard sees an anonymous request
// and 401s, so EventSource enters a permanent reconnect loop and the UI shows
// "Connection lost" even though every /api/v1 request succeeds.
func buildTopLevelAuthChain(cfMW, authGuard, handler gin.HandlerFunc) []gin.HandlerFunc {
	chain := make([]gin.HandlerFunc, 0, 3)
	if cfMW != nil {
		chain = append(chain, cfMW)
	}
	return append(chain, authGuard, handler)
}
