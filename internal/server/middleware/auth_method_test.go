// file: internal/server/middleware/auth_method_test.go
// version: 1.0.0
// guid: 5e2b7c19-0d4a-4f86-b3e1-9a6c8d2f7041
// last-edited: 2026-10-06

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func authMethodStore() *database.MockStore {
	return &database.MockStore{
		CountUsersFunc: func() (int, error) { return 1, nil },
		GetSessionFunc: func(id string) (*database.Session, error) {
			return &database.Session{ID: id, UserID: "owner", ExpiresAt: time.Now().Add(time.Hour)}, nil
		},
		GetUserByIDFunc: func(id string) (*database.User, error) {
			return &database.User{ID: id, Username: id, Status: "active", Roles: []string{"admin"}}, nil
		},
		GetAPIKeyByHashFunc: func(string) (*database.APIKey, error) {
			return &database.APIKey{ID: "k1", UserID: "owner", Status: "active"}, nil
		},
		TouchAPIKeyLastUsedFunc: func(string, time.Time, string) error { return nil },
	}
}

// TestRequireAuth_RecordsAuthMethod: the method is the verifier that bound
// the user, not the transport. An API key sent in the session cookie is still
// an API key, and the owner's own user authenticated by API key is not an
// interactive login.
func TestRequireAuth_RecordsAuthMethod(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		set         func(*http.Request)
		want        auth.Method
		interactive bool
	}{
		{"session cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess-1"}) }, auth.MethodSession, true},
		{"session bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer sess-1") }, auth.MethodSession, true},
		{"api key bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer abk_secret") }, auth.MethodAPIKey, false},
		{"api key in cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "abk_secret"}) }, auth.MethodAPIKey, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			tc.set(req)
			resp, hit, c := executeAuthMiddleware(t, RequireAuth(authMethodStore()), req)
			require.True(t, hit, "status %d", resp.Code)
			got := CurrentAuthMethod(c)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.interactive, got.Interactive())
		})
	}
}

func TestAuthMethod_Interactive(t *testing.T) {
	t.Parallel()
	assert.True(t, auth.MethodSession.Interactive())
	assert.True(t, auth.MethodCFAccess.Interactive())
	assert.False(t, auth.MethodAPIKey.Interactive())
	assert.False(t, auth.MethodABS.Interactive())
	assert.False(t, auth.MethodNone.Interactive())
	assert.False(t, auth.Method("forged").Interactive())
}
