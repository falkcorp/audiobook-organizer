// file: internal/server/redirect_hardening_test.go
// version: 1.0.0
// guid: 7b0e3c52-91d4-4f68-a5c7-2e8d41f60b13
// last-edited: 2026-10-21

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// probePrefix is the caller-chosen X-Forwarded-Prefix the tests send. It is a
// path no route or redirect in the service could produce on its own.
const probePrefix = "/zzprobe"

func probeRequest(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Forwarded-Prefix", probePrefix)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func requireNoPrefixReflected(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	require.NotEqual(t, http.StatusMovedPermanently, w.Code, "a slash/case miss must not redirect")
	require.NotEqual(t, http.StatusTemporaryRedirect, w.Code, "a slash/case miss must not redirect")
	require.NotContains(t, w.Header().Get("Location"), probePrefix, "Location reflects X-Forwarded-Prefix")
	for k, vs := range w.Header() {
		for _, v := range vs {
			require.NotContains(t, v, probePrefix, "response header %s reflects X-Forwarded-Prefix", k)
		}
	}
}

// The control: gin's own defaults echo the header into Location. If this ever
// stops holding the assertions below no longer prove anything, so it fails
// loudly instead.
func TestRedirectHardening_GinDefaultReflectsForwardedPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := probeRequest(t, r, http.MethodGet, "/probe/")
	require.Equal(t, http.StatusMovedPermanently, w.Code)
	require.Contains(t, w.Header().Get("Location"), probePrefix, "control: gin's default redirect should reflect the header")
}

func TestRedirectHardening_BaseRouterNeverRedirects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newBaseRouter()
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/slashed/", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/probe/"},  // missing-slash route asked with a slash
		{http.MethodGet, "/slashed"}, // slash route asked without
		{http.MethodGet, "/PROBE"},   // fixed-path (case) variant
		{http.MethodGet, "/./probe"}, // fixed-path (clean) variant
		{http.MethodPost, "/probe/"}, // non-GET would be a 307
		{http.MethodPut, "/slashed"}, // ditto
	} {
		w := probeRequest(t, r, tc.method, tc.path)
		requireNoPrefixReflected(t, w)
		require.Equal(t, http.StatusNotFound, w.Code, "%s %s", tc.method, tc.path)
	}

	// The exact paths still resolve.
	require.Equal(t, http.StatusOK, probeRequest(t, r, http.MethodGet, "/probe").Code)
	require.Equal(t, http.StatusOK, probeRequest(t, r, http.MethodGet, "/slashed/").Code)
}

// The real router, not a lookalike: every registered GET route (parameterless
// ones, which can be requested as written) asked with the other slash form
// must not redirect or reflect the header. The probe set is built from
// router.Routes(), so a route this test never named is covered too.
func TestRedirectHardening_FullRouterNeverRedirects(t *testing.T) {
	f := setupCredGuardServer(t)
	routes := f.srv.router.Routes()
	require.Greater(t, len(routes), 300, "the fixture router is missing most routes")

	probed := 0
	for _, rt := range routes {
		if rt.Method != http.MethodGet || strings.ContainsAny(rt.Path, ":*") || rt.Path == "/" {
			continue
		}
		other := rt.Path + "/"
		if strings.HasSuffix(rt.Path, "/") {
			other = strings.TrimSuffix(rt.Path, "/")
		}
		w := probeRequest(t, f.srv.router, http.MethodGet, other)
		requireNoPrefixReflected(t, w)
		probed++
	}
	require.Greater(t, probed, 100, "too few routes probed for the test to mean anything")
}
