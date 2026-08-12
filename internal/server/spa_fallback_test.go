// file: internal/server/spa_fallback_test.go
// version: 1.1.0
// guid: 9a4c7e01-3f28-4b95-86d0-c1e57b2f0348
// last-edited: 2026-08-12

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// isNonSPAPath decides whether an unmatched path 404s or renders the SPA shell.
// Getting it wrong in either direction is bad: too narrow and we keep lying to
// protocol probes, too broad and we break deep links into the app.
func TestIsNonSPAPath(t *testing.T) {
	mustNotBeSPA := []string{
		// THE REGRESSION. AudioBooth probes this to discover OIDC support; a
		// 200 from the SPA shell told it we support single sign-on, and it then
		// launched a PKCE flow we can never complete.
		"/auth/openid",
		"/auth/openid?client_id=AudioBooth&response_type=code",
		"/auth/openid/callback",
		"/auth",
		"/auth/",
		"/api",
		"/api/v1/nope",
		"/api/me/sessions/xyz",
		// THE SECOND REGRESSION, same shape as the first. Absorb opens a
		// real-time connection with an Engine.IO polling handshake; the SPA
		// shell answered 200, so its 200-guard passed and it then tried to
		// parse index.html as an Engine.IO frame.
		"/socket.io/",
		"/socket.io/?EIO=4&transport=polling",
		"/socket.io/?EIO=4&transport=websocket&sid=abc",
		"/socket.io",
	}
	for _, p := range mustNotBeSPA {
		require.True(t, isNonSPAPath(p), "%q is server-side and must 404, not render the SPA", p)
	}

	mustBeSPA := []string{
		"/",
		"/library",
		"/library/01ABC",
		"/settings",
		// The prefix requires a trailing slash precisely so this is not
		// swallowed. /authors is a real page in the app.
		"/authors",
		"/authors/123",
		"/authentication-settings",
		"/index.html",
		"/assets/index-abc123.js",
		// Same slash discipline as /authors: "/socket.io/" must not swallow a
		// path that merely starts with those characters.
		"/socket.iodine",
	}
	for _, p := range mustBeSPA {
		require.False(t, isNonSPAPath(p), "%q is a client route and must still render the SPA", p)
	}
}

// The fix must not shadow a REGISTERED /auth route. NoRoute only fires for
// unmatched paths, so /auth/temp-login is dispatched by gin long beforehand —
// this pins that reasoning rather than trusting it.
func TestAuthTempLoginStillRoutes(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/temp-login?token=nope", nil))

	// The handler redirects to /login with an error for a bad token. What
	// matters is that it REACHED the handler: a 404 would mean the new prefix
	// swallowed a live route.
	require.NotEqual(t, http.StatusNotFound, w.Code,
		"/auth/temp-login is a registered route and must not be captured by the SPA fallback")
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Contains(t, w.Header().Get("Location"), "/login")
}

// End-to-end shape of the bug: an unimplemented auth endpoint must answer 404,
// not 200-with-HTML.
func TestUnimplementedAuthEndpointIs404(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	for _, path := range []string{
		"/auth/openid?client_id=AudioBooth&response_type=code&scope=openid&redirect_uri=audiobooth://oauth",
		"/auth/openid",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			require.Equal(t, http.StatusNotFound, w.Code,
				"%s %s must 404 so a client's capability probe fails honestly", method, path)
			require.NotContains(t, w.Body.String(), "<!doctype html",
				"%s %s must not return the SPA shell", method, path)
		}
	}
}

// A socket.io handshake must fail honestly. We do not implement socket.io, and
// "not implemented" has to be distinguishable from "implemented and broken".
//
// The two build variants fail differently without the fix — the embedded build
// answers 200 text/html, the non-embedded one 302 → /. Asserting 404 pins both,
// and asserting NOT-2xx-or-3xx states the property the client actually depends
// on rather than the specific status of whichever variant these tests run under.
func TestSocketIOHandshakeIs404(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	for _, path := range []string{
		"/socket.io/?EIO=4&transport=polling",
		"/socket.io/?EIO=4&transport=polling&t=OaBcDeF",
		"/socket.io/",
		"/socket.io",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			server.router.ServeHTTP(w, httptest.NewRequest(method, path, nil))

			require.Equal(t, http.StatusNotFound, w.Code,
				"%s %s must 404 so the client can give up on real-time and degrade", method, path)
			require.GreaterOrEqual(t, w.Code, 400,
				"%s %s must not answer 2xx/3xx: the client's success guard would pass and it "+
					"would parse the body as an Engine.IO frame", method, path)
			require.NotContains(t, w.Body.String(), "<!doctype html",
				"%s %s must not return the SPA shell", method, path)
		}
	}
}
