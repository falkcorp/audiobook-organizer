// file: internal/server/spa_fallback.go
// version: 1.2.0
// guid: 1e6d90b4-27a5-4c31-8f9e-a3b715c8d604
// last-edited: 2026-10-21

package server

import (
	"net/http"
	"path"
	"strings"
)

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
// The same failure recurred for socket.io, which is why it is listed below.
// Absorb (the other target client) opens a real-time connection with an
// Engine.IO polling handshake:
//
//	GET /socket.io/?EIO=4&transport=polling
//
// We do not implement socket.io. The honest answer is 404, which lets the client
// give up on real-time and degrade to polling. Instead the catch-all returned
// `200 text/html` (embedded build) or `302 → /` (non-embedded) — so the client's
// 200-guard passed and it then tried to parse the React index page as an
// Engine.IO frame. A 200 with an HTML body is worse than an error here: it
// converts "feature absent" into "feature broken."
//
// Keep this list to prefixes that are unambiguously server-side. Anything a
// human might type or bookmark belongs in the SPA.
var nonSPAPrefixes = []string{
	"/api", // the JSON API, including the ABS-compatible surface
}

// nonSPASegments are server-side top-level path segments matched as a whole
// segment (so "/auth" and "/auth/x" are server-side but "/authors" is not).
// Every one is a path the router registers (or a protocol probe it answers 404
// to), so a request for it in a form that misses the registered one -- a
// trailing slash, a doubled slash, other letter case -- must not be answered
// with the SPA shell: with redirects off (newBaseRouter) such a request lands in
// NoRoute, and a 200 text/html there tells an ABS client that "/ping/" or
// "/Status" is a working server answering in HTML.
//
// "/login" is deliberately NOT here: GET /login is the SPA's login page (the
// ABS surface registers only POST /login), so it is covered by the method rule in
// spaWillNotServe instead -- POST /login/ is a 404, GET /login/ is the page.
var nonSPASegments = map[string]bool{
	"auth":      true, // login/OAuth/OIDC machinery; registered routes match before NoRoute
	"socket.io": true, // Engine.IO transport probe; unimplemented, must not look implemented
	"ping":      true, // ABS liveness probe
	"status":    true, // ABS server status
	"logout":    true, // ABS logout
	"health":    true,
	"metrics":   true,
	"public":    true, // ABS public share/cover surface
}

// normalizeNonSPAPath folds the variations a request can use to miss a
// registered route without changing which route it names: a query string,
// repeated slashes, "." and ".." segments, a trailing slash, letter case. It is
// used ONLY to decide a 404. The normalised path is never dispatched into the
// router, so it cannot be used to reach a route the original did not match.
func normalizeNonSPAPath(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return path.Clean("/" + strings.ToLower(p))
}

// isNonSPAPath reports whether an unmatched path must 404 rather than render the
// SPA shell.
//
// Only UNMATCHED paths reach here -- this runs from NoRoute, so real routes such
// as /auth/temp-login are dispatched by gin long before it, and adding a prefix
// here cannot shadow a registered handler. Both the embedded and non-embedded
// static handlers consult this before they diverge, so one entry fixes both.
func isNonSPAPath(p string) bool {
	p = normalizeNonSPAPath(p)
	for _, prefix := range nonSPAPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	return nonSPASegments[first]
}

// spaWillNotServe reports whether the NoRoute fallback must answer 404 instead
// of the SPA shell: a server-side path (isNonSPAPath), or any method other than
// GET/HEAD -- the shell is a document to navigate to, and a POST or DELETE that
// matched no route has nothing to do with it.
func spaWillNotServe(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	return isNonSPAPath(r.URL.Path)
}
