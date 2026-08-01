// file: internal/server/spa_fallback.go
// version: 1.0.0
// guid: 1e6d90b4-27a5-4c31-8f9e-a3b715c8d604
// last-edited: 2026-08-01

package server

import "strings"

// nonSPAPrefixes are URL prefixes that must NEVER fall through to the SPA
// shell. A request under one of these is asking the SERVER a question, and an
// unmatched one has exactly one honest answer: 404.
//
// # Why this matters more than it looks
//
// The SPA fallback answers `200 text/html` for any unmatched path so that deep
// links like /library/123 load the React app and let the client router take
// over. Applied to a machine-facing path that is a protocol probe, that same
// behaviour is an active lie.
//
// AudioBooth (an Audiobookshelf client) discovers whether a server supports
// OpenID Connect SSO by probing the endpoint directly:
//
//	HEAD /auth/openid?client_id=AudioBooth&response_type=code&scope=openid
//	     &redirect_uri=audiobooth://oauth&code_challenge=...
//
// It got 200 from the catch-all, concluded OIDC was supported, and launched a
// PKCE flow expecting a redirect back to audiobooth://oauth. We have no OIDC
// implementation, so the app received the React index page instead and the
// login collapsed — surfacing to the user as Cloudflare Access's "Invalid login
// session", because the edge could not reconcile a flow the origin never joined.
//
// Note the client had already been TOLD we do not support this: /status
// advertises authMethods: ["local"] precisely so clients avoid an OIDC flow
// they cannot complete against us (see handlers/abs/status.go). AudioBooth does
// not take that at face value — it probes. So the advertisement alone is not a
// sufficient defence; the endpoint itself has to answer honestly.
//
// Keep this list to prefixes that are unambiguously server-side. Anything a
// human might type or bookmark belongs in the SPA.
var nonSPAPrefixes = []string{
	"/api",   // the JSON API, including the ABS-compatible surface
	"/auth/", // login/OAuth/OIDC machinery; registered routes match before NoRoute
}

// isNonSPAPath reports whether an unmatched path must 404 rather than render the
// SPA shell.
//
// Only UNMATCHED paths reach here — this runs from NoRoute, so real routes such
// as /auth/temp-login are dispatched by gin long before it, and adding a prefix
// here cannot shadow a registered handler.
func isNonSPAPath(path string) bool {
	for _, prefix := range nonSPAPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	// Exact "/auth" with no trailing slash is still server-side; the prefix form
	// above deliberately requires the slash so a future SPA route like
	// /authors is not captured by accident.
	return path == "/auth"
}
