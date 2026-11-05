// file: internal/server/handlers/auth_sessions_view_test.go
// version: 1.0.0
// guid: ab3c08b3-cc69-42b1-a17f-75c65d2d9430
// last-edited: 2026-11-05

package handlers_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// handleOf restates the brief's handle rule independently of the handler:
// the first 16 hex characters of SHA-256(id).
func handleOf(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:16]
}

// walkStrings calls fn for every string value in a decoded JSON tree and fails
// the test on any object that has an "id" key.
func walkStrings(t *testing.T, v any, fn func(string)) {
	t.Helper()
	switch x := v.(type) {
	case string:
		fn(x)
	case []any:
		for _, e := range x {
			walkStrings(t, e, fn)
		}
	case map[string]any:
		_, hasID := x["id"]
		assert.False(t, hasID, "a sessions entry must not carry an id key")
		for _, e := range x {
			walkStrings(t, e, fn)
		}
	}
}

// TestLegacySessionsList_NoToken pins 05-authn section 11.1 item 1: the sessions
// list never shows a session ID (the bearer token), and a session is revoked
// by the derived handle the list shows, never by the token.
func TestLegacySessionsList_NoToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, _, err = auth.SeedRoles(store)
	require.NoError(t, err)

	hash, err := bcrypt.GenerateFromPassword([]byte("pw-pw-pw-pw"), bcrypt.MinCost)
	require.NoError(t, err)
	user, err := store.CreateUser("robin-user", "r@example.test", "bcrypt", string(hash), []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)

	var sess []*database.Session
	for i := 0; i < 3; i++ {
		s, err := store.CreateSession(user.ID, "127.0.0.1", "ua", time.Hour, 0)
		require.NoError(t, err)
		sess = append(sess, s)
	}

	h := handlers.NewAuthHandler(store, true)
	r := gin.New()
	r.GET("/auth/sessions", servermiddleware.RequireAuth(store), h.ListMySessions)
	r.DELETE("/auth/sessions/:id", servermiddleware.RequireAuth(store), h.RevokeMySession)

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+sess[0].ID)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. No live token and no id key anywhere in the body.
	w := do(http.MethodGet, "/auth/sessions")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	walkStrings(t, body, func(s string) {
		for _, x := range sess {
			assert.NotEqual(t, x.ID, s, "a session token leaked into the list")
		}
	})

	// 2. Every entry has a 16-character handle; exactly one is current.
	// The success envelope wraps the payload as {"data": {"sessions": [...]}}.
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, w.Body.String())
	entries, ok := data["sessions"].([]any)
	require.True(t, ok, w.Body.String())
	require.Len(t, entries, 3)
	current := 0
	handles := map[string]bool{}
	for _, e := range entries {
		m := e.(map[string]any)
		hd, _ := m["handle"].(string)
		assert.Len(t, hd, 16)
		handles[hd] = true
		if c, _ := m["current"].(bool); c {
			current++
		}
	}
	assert.Equal(t, 1, current)
	assert.Len(t, handles, 3)

	// 3. Revoke the second session by its derived handle.
	secondHandle := handleOf(sess[1].ID)
	require.True(t, handles[secondHandle], "list must show the derived handle")
	w = do(http.MethodDelete, "/auth/sessions/"+secondHandle)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	got, err := store.GetSession(sess[1].ID)
	require.NoError(t, err)
	assert.True(t, got == nil || got.Revoked, "second session must be revoked or gone")
	for _, i := range []int{0, 2} {
		s, err := store.GetSession(sess[i].ID)
		require.NoError(t, err)
		require.NotNil(t, s)
		assert.False(t, s.Revoked, "session %d must be untouched", i)
	}

	// 4. A raw token is not a handle; an unknown handle is 404.
	assert.Equal(t, http.StatusNotFound, do(http.MethodDelete, "/auth/sessions/"+sess[1].ID).Code)
	assert.Equal(t, http.StatusNotFound, do(http.MethodDelete, "/auth/sessions/0000000000000000").Code)
}
