// file: internal/server/middleware/auth_revoked_test.go
// version: 1.0.0
// guid: a6a5afde-8201-4dbb-8e03-4ec5d3e9df1a
// last-edited: 2026-11-05

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestLegacyAPIKeyAuth_RevokedAtRefusedWhateverStatus is 05-authn §11.1 item
// 3 at the two key-auth paths: a row with revoked_at set is refused even when
// its status says "active", and the same row with revoked_at nil is accepted.
// Before early fix 3 both paths tested only the status string.
func TestLegacyAPIKeyAuth_RevokedAtRefusedWhateverStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	revokedAt := time.Now().Add(-time.Minute)
	cases := []struct {
		name      string
		revokedAt *time.Time
		refused   bool
	}{
		{"active with revoked_at set", &revokedAt, true},
		{"active with revoked_at nil", nil, false},
	}

	for _, tc := range cases {
		t.Run("web/"+tc.name, func(t *testing.T) {
			store := authMethodStore()
			touched := 0
			store.GetAPIKeyByHashFunc = func(string) (*database.APIKey, error) {
				exp := time.Now().Add(time.Hour)
				return &database.APIKey{ID: "k1", UserID: "owner", Status: "active", ExpiresAt: &exp, RevokedAt: tc.revokedAt}, nil
			}
			store.TouchAPIKeyLastUsedFunc = func(string, time.Time, string) error {
				touched++
				return nil
			}
			r := gin.New()
			r.Use(RequireAuth(store))
			r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Authorization", "Bearer abk_two_face")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if tc.refused {
				assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
				assert.Contains(t, w.Body.String(), "API key has been revoked")
				assert.Zero(t, touched, "a refused key's request must not touch the row")
			} else {
				assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
				assert.Equal(t, 1, touched)
			}
		})

		t.Run("abs/"+tc.name, func(t *testing.T) {
			h := newAPIKeyHarness(t)
			h.store.addUser(&database.User{ID: "u-harvey", Username: "harvey-dent", Status: "active"})
			h.store.addKey("abk_two_face", &database.APIKey{ID: "k1", UserID: "u-harvey", Status: "active", RevokedAt: tc.revokedAt})

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
			c.Request.Header.Set("Authorization", "Bearer abk_two_face")
			id, e := h.resolver.ResolveAPIKey(c)
			if tc.refused {
				assert.Nil(t, id)
				require.NotNil(t, e)
				assert.Equal(t, http.StatusUnauthorized, e.Status)
				assert.Equal(t, "apikey-revoked", e.Reason)
			} else {
				require.Nil(t, e)
				require.NotNil(t, id)
				assert.Equal(t, "u-harvey", id.User.ID)
			}
		})
	}
}
