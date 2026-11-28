// file: internal/cfaccess/verify_only_test.go
// version: 1.0.0
// guid: 5c248abd-9ec8-48a8-8b13-a1231ba45a7f
// last-edited: 2026-11-28

package cfaccess

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// CloudflareAccessVerifyOnly is the gin chain's Access stage under core
// identity: it marks the request exactly when HEAD's binder did and does
// nothing else (no user, no context value, no refusal).
func TestCloudflareAccessVerifyOnly_MarksAsTheBinderDidAndBindsNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name   string
		header string
		cookie string
		key    bool
		marked bool
		calls  int
	}{
		{name: "a person's header assertion", header: goodJWT, marked: true, calls: 1},
		{name: "a person's cookie assertion", cookie: goodJWT, marked: true, calls: 1},
		{name: "a service token", header: serviceJWT, marked: true, calls: 1},
		{name: "an assertion that does not verify", header: "jwt-forged-by-the-riddler", calls: 1},
		{name: "a key outage", header: outageJWT, calls: 1},
		{name: "no assertion"},
		{name: "an API key with a valid assertion riding along", header: goodJWT, key: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &tokenVerifier{}
			mw := CloudflareAccessVerifyOnly(v, func(*http.Request) bool { return tc.key })
			var marked, ran, bound bool
			r := gin.New()
			r.GET("/x", mw, func(c *gin.Context) {
				ran = true
				marked = CFAccessVerified(c)
				_, bound = auth.UserFromContext(c.Request.Context())
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tc.header != "" {
				req.Header.Set(CFAccessHeader, tc.header)
			}
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: CookieName, Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.True(t, ran, "the middleware never refuses a request")
			require.Equal(t, http.StatusNoContent, w.Code)
			require.Equal(t, tc.marked, marked, "verified marker")
			require.False(t, bound, "no user is bound")
			require.Equal(t, tc.calls, v.calls, "verifier calls")
		})
	}
}

// A nil verifier or key test is a pass-through, never a panic.
func TestCloudflareAccessVerifyOnly_IncompleteIsAPassThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mw := range []gin.HandlerFunc{
		CloudflareAccessVerifyOnly(nil, func(*http.Request) bool { return false }),
		CloudflareAccessVerifyOnly(&tokenVerifier{}, nil),
	} {
		var marked bool
		r := gin.New()
		r.GET("/x", mw, func(c *gin.Context) { marked = CFAccessVerified(c); c.Status(http.StatusNoContent) })
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set(CFAccessHeader, goodJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusNoContent, w.Code)
		require.False(t, marked)
	}
}
