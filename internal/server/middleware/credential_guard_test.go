// file: internal/server/middleware/credential_guard_test.go
// version: 1.0.0
// guid: 8b6d2f40-3c19-4e7a-a5d8-0f4e7c1b9a62
// last-edited: 2026-10-07

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// TestRequireCredentialChangeMethod: a person's session (own sign-in,
// delegated, or Cloudflare Access SSO) passes; an API key, an ABS token, no
// method and an unclassified method are refused with the guard's message.
func TestRequireCredentialChangeMethod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		method auth.Method
		pass   bool
	}{
		{auth.MethodSession, true},
		{auth.MethodSessionDelegated, true},
		{auth.MethodCFAccess, true},
		{auth.MethodAPIKey, false},
		{auth.MethodABS, false},
		{auth.MethodNone, false},
		{auth.Method("unknown_new_method"), false},
	}
	for _, tc := range cases {
		t.Run(string(tc.method)+"_", func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(auth.WithMethod(c.Request.Context(), tc.method))
				c.Next()
			})
			hit := false
			r.PUT("/guarded", RequireCredentialChangeMethod(), func(c *gin.Context) {
				hit = true
				c.Status(http.StatusNoContent)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/guarded", nil))
			assert.Equal(t, tc.pass, hit)
			if tc.pass {
				assert.Equal(t, http.StatusNoContent, w.Code)
				return
			}
			assert.Equal(t, http.StatusForbidden, w.Code)
			assert.Contains(t, w.Body.String(), CredentialChangeRefusedMessage)
		})
	}
}
