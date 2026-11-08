// file: internal/server/build_id_test.go
// version: 1.0.0
// guid: 345f4eaa-2bfb-48ec-bb06-efb4828b7c96
// last-edited: 2026-11-08

package server

import (
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// TestBuildID_HeaderEqualsEmbeddedMeta (06 §6.10, 05-http-api §12): a server
// that embeds a bundle answers X-App-Build with the content of the bundle's
// <meta name="mediacore-build">, on every kind of answer the legacy mount's
// perimeter writes (the SPA page, a routed API call, an unrouted path), and
// --version-json's reader (BuildIDFromFS) returns the same value from the
// same filesystem. A bundle without the tag, or no bundle, is "dev". The PR
// image job asserts the same on the real image (scripts/ci/image_probe.sh,
// PROBE_BUILD_ID=1).
//
// The embedded filesystem is a fixture: the tag's value is whatever the
// Vite build wrote, and the server reads it back; nothing here compares it
// with a generated value.
func TestBuildID_HeaderEqualsEmbeddedMeta(t *testing.T) {
	restoreCSP(t)
	prev := spaBuildID.Load()
	t.Cleanup(func() { spaBuildID.Store(prev) })

	const buildID = "robin-1940.06.01"
	bundle := fstest.MapFS{
		"web/dist/index.html": {Data: []byte(
			"<!doctype html><html><head><meta charset=\"UTF-8\" />" +
				"<meta name=\"mediacore-build\" content=\"" + buildID + "\" />" +
				"</head><body><div id=\"root\"></div></body></html>")},
	}
	require.Equal(t, buildID, BuildIDFromFS(bundle), "the --version-json reader")
	require.Empty(t, BuildIDFromFS(fstest.MapFS{}), "no bundle")
	require.Empty(t, BuildIDFromFS(fstest.MapFS{"web/dist/index.html": {Data: []byte("<html></html>")}}), "no tag")

	f := legacyEngine(t, false, false)
	// What the embedded build's static setup does with the page it serves
	// (setupStaticFiles), before books asks for LegacyPerimeter.
	index, err := bundle.ReadFile("web/dist/index.html")
	require.NoError(t, err)
	setSPAInlineScripts(index)
	_, _, r1 := perimeterStacks(t, f, "")

	for _, c := range []perimeterCase{
		{name: "the SPA page", method: http.MethodGet, path: "/"},
		{name: "a routed API call", method: http.MethodGet, path: "/api/v1/health"},
		{name: "an unrouted path", method: http.MethodGet, path: "/api/v1/riddler-unrouted"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := serveCase(r1, c)
			require.Equal(t, buildID, w.Header().Get("X-App-Build"), "status %d", w.Code)
		})
	}

	// Without a bundle the header is "dev" (stage 5's unknown).
	setSPAInlineScripts([]byte("<html></html>"))
	_, _, noBundle := perimeterStacks(t, f, "")
	require.Equal(t, "dev", serveCase(noBundle, perimeterCase{method: http.MethodGet, path: "/api/v1/health"}).Header().Get("X-App-Build"))
}
