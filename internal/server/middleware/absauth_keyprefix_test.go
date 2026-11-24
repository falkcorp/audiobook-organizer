// file: internal/server/middleware/absauth_keyprefix_test.go
// version: 0.1.0
// guid: 9a3d5f21-6c84-4e7b-b0a2-1f7e8c4d35b9
// last-edited: 2026-11-22

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// TestABSKeyPrefix_ReadersSplitKeysFromAccessTokens pins the one rule the ABS
// readers share (IsABSAPIKey, F4-08 B10z): on a HEAD server only "abk_" is a
// key and an "mck_" string stays what it always was there, a bearer candidate;
// under core identity "mck_" is a key too, read from the header and from
// ?token= on a safe method, and never handed to the access-token parser.
func TestABSKeyPrefix_ReadersSplitKeysFromAccessTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const mck = "mck_" + "fixture-not-a-key"
	const abk = "abk_" + "fixture-not-a-key"
	const jwt = "eyJhbGciOiJIUzI1NiJ9.e30.signature"

	ctxFor := func(method, target, bearer string, identity bool) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		req := httptest.NewRequest(method, target, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if identity {
			req = req.WithContext(legacyauth.WithIdentity(req.Context(), &legacyauth.Identity{}))
		}
		c.Request = req
		return c
	}
	cases := []struct {
		name             string
		method, target   string
		bearer           string
		identity         bool
		wantKey, wantJWT string
	}{
		{"HEAD: abk_ header is a key", http.MethodGet, "/api/me", abk, false, abk, ""},
		{"HEAD: mck_ header is not a key (unchanged)", http.MethodGet, "/api/me", mck, false, "", mck},
		{"HEAD: jwt header is an access token", http.MethodGet, "/api/me", jwt, false, "", jwt},
		{"identity: mck_ header is a key, never an access token", http.MethodGet, "/api/me", mck, true, mck, ""},
		{"identity: abk_ header is a key", http.MethodGet, "/api/me", abk, true, abk, ""},
		{"identity: jwt header is an access token", http.MethodGet, "/api/me", jwt, true, "", jwt},
		{"identity: mck_ ?token= on GET is a key, never an access token", http.MethodGet, "/api/me?token=" + mck, "", true, mck, ""},
		{"identity: mck_ ?token= on POST is neither", http.MethodPost, "/api/me?token=" + mck, "", true, "", ""},
		{"HEAD: mck_ ?token= on GET is not a key (unchanged)", http.MethodGet, "/api/me?token=" + mck, "", false, "", mck},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ctxFor(tc.method, tc.target, tc.bearer, tc.identity)
			require.Equal(t, tc.wantKey, PresentedAPIKey(c), "the key reader")
			require.Equal(t, tc.wantJWT, absBearerFromRequest(c), "the access-token reader")
		})
	}
}
