// file: internal/server/wire_apikeys_gone_test.go
// version: 1.1.0
// guid: 2e7b9d04-5c18-4a63-9f2a-b8d1c6e40a57
// last-edited: 2026-10-18

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

const apiKeyRotateGoneBody = `{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/auth/api-keys (create a new key), then DELETE /api/v1/auth/api-keys/:id (revoke the old one) instead"}`

// ownKeyID returns the id of the all-scope API key the fixture's admin
// authenticates with (the one f.apiKey is the token of).
func (f *credGuardFixture) ownKeyID(t *testing.T) string {
	t.Helper()
	keys, err := f.store.ListAPIKeysForUser(f.adminID)
	require.NoError(t, err)
	for _, k := range keys {
		if k.Name == "synthetic admin key" {
			return k.ID
		}
	}
	require.FailNow(t, "the fixture's admin key was not found")
	return ""
}

// TestRetiredAPIKeyRotate_Answer410 drives POST /auth/api-keys/:id/rotate
// through the real router: a signed-in session gets the shared gone() body for
// its own key and for another user's key, and the stub touches nothing — no key
// is minted and the old key keeps its status and expiry (the retired handler
// gave it a one-hour grace expiry).
func TestRetiredAPIKeyRotate_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)

	before, err := f.store.ListAllAPIKeys()
	require.NoError(t, err)
	byID := map[string]database.APIKey{}
	for _, k := range before {
		byID[k.ID] = k
	}

	for name, id := range map[string]string{"own key": f.ownKeyID(t), "another user's key": f.targetKeyID} {
		t.Run(name, func(t *testing.T) {
			w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/auth/api-keys/"+id+"/rotate", nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, apiKeyRotateGoneBody, w.Body.String())
		})
	}

	after, err := f.store.ListAllAPIKeys()
	require.NoError(t, err)
	require.Len(t, after, len(before), "the stub must not mint a key")
	for _, k := range after {
		old := byID[k.ID]
		assert.Equal(t, old.Status, k.Status, "key %s status", k.ID)
		require.NotNil(t, k.ExpiresAt)
		assert.True(t, old.ExpiresAt.Equal(*k.ExpiresAt), "key %s expiry must not get a grace window", k.ID)
	}
}

// TestRetiredAPIKeyRotate_AuthRunsFirst: no credentials, no 410.
func TestRetiredAPIKeyRotate_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/api-keys/"+f.targetKeyID+"/rotate", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}

// TestRetiredAPIKeyRotate_CredentialGuardRunsBeforeGone: the stub keeps the
// credential guard it had as a live route. An API key asking to rotate another
// user's key is refused with the credential-change message and never learns the
// route is gone; the same key asking about its own user's key reaches the 410.
func TestRetiredAPIKeyRotate_CredentialGuardRunsBeforeGone(t *testing.T) {
	f := setupCredGuardServer(t)

	w := f.do(t, f.apiKey, http.MethodPost, "/api/v1/auth/api-keys/"+f.targetKeyID+"/rotate", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), servermiddleware.CredentialChangeRefusedMessage)
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)

	w = f.do(t, f.apiKey, http.MethodPost, "/api/v1/auth/api-keys/"+f.ownKeyID(t)+"/rotate", nil)
	assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, apiKeyRotateGoneBody, w.Body.String())
}

// TestRetiredAPIKeyRotate_KeyManagementStillWorks proves the replacement the
// 410 names is a working substitute and that retiring rotate did not break the
// rest of key management: a signed-in owner creates a key, lists and reads it,
// disables and re-enables it, and revokes it; and a key can still mint (and the
// owner revoke) a clamped replacement for its own user. Creating and revoking
// the replacement is the supported way to replace a key.
func TestRetiredAPIKeyRotate_KeyManagementStillWorks(t *testing.T) {
	f := setupCredGuardServer(t)

	// Create as the signed-in owner: a 30-day key by default.
	w := f.do(t, f.sessionToken, http.MethodPost, "/api/v1/auth/api-keys", map[string]any{"name": "replacement"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Data struct {
			ID        string     `json:"id"`
			Token     string     `json:"token"`
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.NotEmpty(t, created.Data.ID)
	require.NotEmpty(t, created.Data.Token)
	require.NotNil(t, created.Data.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(30*24*time.Hour), *created.Data.ExpiresAt, time.Minute)
	keyPath := "/api/v1/auth/api-keys/" + created.Data.ID

	// The new token authenticates.
	w = f.do(t, created.Data.Token, http.MethodGet, "/api/v1/auth/me", nil)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// List and get.
	w = f.do(t, f.sessionToken, http.MethodGet, "/api/v1/auth/api-keys", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), created.Data.ID)
	w = f.do(t, f.sessionToken, http.MethodGet, keyPath, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// Disable and re-enable (session only: PATCH is credential-guarded).
	for _, status := range []string{"inactive", "active"} {
		w = f.do(t, f.sessionToken, http.MethodPatch, keyPath, map[string]any{"status": status})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		got, err := f.store.GetAPIKey(created.Data.ID)
		require.NoError(t, err)
		assert.Equal(t, status, got.Status)
	}

	// A key still mints a replacement for its own user, clamped to its own
	// expiry (the fixture key lives one hour; the request asks for 365 days).
	w = f.do(t, f.apiKey, http.MethodPost, "/api/v1/auth/api-keys", map[string]any{"name": "minted by a key", "expires_in_days": 365})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var minted struct {
		Data struct {
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &minted))
	require.NotNil(t, minted.Data.ExpiresAt)
	ownKey, err := f.store.GetAPIKey(f.ownKeyID(t))
	require.NoError(t, err)
	require.NotNil(t, ownKey.ExpiresAt)
	assert.False(t, minted.Data.ExpiresAt.After(*ownKey.ExpiresAt), "a key must not mint a key that outlives it")

	// Revoke the replaced key: it stops authenticating.
	w = f.do(t, f.sessionToken, http.MethodDelete, keyPath, nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	got, err := f.store.GetAPIKey(created.Data.ID)
	require.NoError(t, err)
	assert.Equal(t, "revoked", got.Status)
	w = f.do(t, created.Data.Token, http.MethodGet, "/api/v1/auth/me", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}
