// file: internal/server/handlers/apikeys_test.go
// version: 1.1.0
// guid: e6f7a8b9-c0d1-2345-6789-0abcdef01234
// last-edited: 2026-07-03

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyHandler_List_OwnKeys(t *testing.T) {
	caller := &database.User{ID: "user-1", Username: "alice"}
	keys := []database.APIKey{
		{ID: "key-1", UserID: "user-1", Name: "My Key", TokenHash: "abcdefghij", Status: "active", CreatedAt: time.Now()},
	}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().ListAPIKeysForUser("user-1").Return(keys, nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("GET", "/auth/api-keys", nil)
	setAuthUser(c, caller)
	h.List(c)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, float64(1), data["count"])
}

func TestAPIKeyHandler_List_Unauthenticated(t *testing.T) {
	store := handlersmocks.NewMockAPIKeyHandlerStore(t)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("GET", "/auth/api-keys", nil)
	// no user in context
	h.List(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAPIKeyHandler_Get_OwnKey(t *testing.T) {
	caller := &database.User{ID: "user-1"}
	key := &database.APIKey{ID: "key-1", UserID: "user-1", TokenHash: "abcdefghij", Status: "active", CreatedAt: time.Now()}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("key-1").Return(key, nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("GET", "/auth/api-keys/key-1", nil)
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	setAuthUser(c, caller)
	h.Get(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyHandler_Get_NotOwner_Forbidden(t *testing.T) {
	caller := &database.User{ID: "user-1"}
	key := &database.APIKey{ID: "key-1", UserID: "user-2", TokenHash: "abcdefghij", Status: "active", CreatedAt: time.Now()}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("key-1").Return(key, nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("GET", "/auth/api-keys/key-1", nil)
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	setAuthUser(c, caller)
	// no admin permission in context → isAdminUser returns false
	h.Get(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAPIKeyHandler_Get_NotFound(t *testing.T) {
	caller := &database.User{ID: "user-1"}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("missing").Return(nil, nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("GET", "/auth/api-keys/missing", nil)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	setAuthUser(c, caller)
	h.Get(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPIKeyHandler_UpdateStatus_RevokedRejected(t *testing.T) {
	caller := &database.User{ID: "user-1"}
	key := &database.APIKey{ID: "key-1", UserID: "user-1", TokenHash: "abcdefghij", Status: "active", CreatedAt: time.Now()}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("key-1").Return(key, nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("PATCH", "/auth/api-keys/key-1", map[string]any{"status": "revoked"})
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	setAuthUser(c, caller)
	h.UpdateStatus(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAPIKeyHandler_UpdateStatus_Success(t *testing.T) {
	caller := &database.User{ID: "user-1"}
	key := &database.APIKey{ID: "key-1", UserID: "user-1", TokenHash: "abcdefghij", Status: "active", CreatedAt: time.Now()}
	updated := &database.APIKey{ID: "key-1", UserID: "user-1", TokenHash: "abcdefghij", Status: "inactive", CreatedAt: time.Now()}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("key-1").Return(key, nil).Once()
	store.EXPECT().SetAPIKeyStatus("key-1", "inactive", mock.Anything).Return(nil)
	store.EXPECT().GetAPIKey("key-1").Return(updated, nil).Once()

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("PATCH", "/auth/api-keys/key-1", map[string]any{"status": "inactive"})
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	setAuthUser(c, caller)
	h.UpdateStatus(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyHandler_Revoke_Success(t *testing.T) {
	caller := &database.User{ID: "user-1"}
	key := &database.APIKey{ID: "key-1", UserID: "user-1", Name: "My Key", TokenHash: "abcdefghij", Status: "active", CreatedAt: time.Now()}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("key-1").Return(key, nil)
	store.EXPECT().RevokeAPIKey("key-1").Return(nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("DELETE", "/auth/api-keys/key-1", nil)
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	setAuthUser(c, caller)
	h.Revoke(c)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestAPIKeyHandler_Revoke_NotOwner_Forbidden(t *testing.T) {
	caller := &database.User{ID: "user-1"}
	key := &database.APIKey{ID: "key-1", UserID: "user-2", Name: "Other Key", TokenHash: "abcdefghij", Status: "active", CreatedAt: time.Now()}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("key-1").Return(key, nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("DELETE", "/auth/api-keys/key-1", nil)
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	setAuthUser(c, caller)
	h.Revoke(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// Rotate (SEC-1/PROC-6): the old key must NOT be revoked outright — it gets
// a short grace-window ExpiresAt via SetAPIKeyExpiry instead, so in-flight
// callers keep working briefly. The new key inherits scopes and gets a
// fresh, far-future ExpiresAt.
func TestAPIKeyHandler_Rotate_Success(t *testing.T) {
	caller := &database.User{ID: "user-1"}
	oldKey := &database.APIKey{
		ID: "key-1", UserID: "user-1", Name: "My Key", Description: "desc",
		TokenHash: "abcdefghij", Scopes: []string{"library.view", "library.edit"},
		Status: "active", CreatedAt: time.Now(),
	}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("key-1").Return(oldKey, nil)
	store.EXPECT().CreateAPIKey(mock.MatchedBy(func(k *database.APIKey) bool {
		if k.UserID != "user-1" || k.Status != "active" {
			return false
		}
		if len(k.Scopes) != 2 || k.Scopes[0] != "library.view" || k.Scopes[1] != "library.edit" {
			return false
		}
		if k.ExpiresAt == nil || !k.ExpiresAt.After(time.Now().Add(29*24*time.Hour)) {
			return false // fresh expiry should be ~30d out, well past the 1h grace window
		}
		return true
	})).Return(&database.APIKey{
		ID: "key-2", UserID: "user-1", Name: "My Key", Scopes: oldKey.Scopes,
		Status: "active", CreatedAt: time.Now(),
		ExpiresAt: func() *time.Time { t := time.Now().Add(30 * 24 * time.Hour); return &t }(),
	}, nil)
	store.EXPECT().SetAPIKeyExpiry("key-1", mock.MatchedBy(func(at time.Time) bool {
		// Grace window is short (1h) — must be far short of the new key's ~30d expiry.
		return at.After(time.Now()) && at.Before(time.Now().Add(2*time.Hour))
	})).Return(nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("POST", "/auth/api-keys/key-1/rotate", nil)
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	setAuthUser(c, caller)
	h.Rotate(c)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp struct {
		Data struct {
			ID     string   `json:"id"`
			Token  string   `json:"token"`
			Scopes []string `json:"scopes"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "key-2", resp.Data.ID)
	assert.NotEmpty(t, resp.Data.Token)
}

func TestAPIKeyHandler_Rotate_NotOwner_Forbidden(t *testing.T) {
	caller := &database.User{ID: "user-1"}
	oldKey := &database.APIKey{ID: "key-1", UserID: "user-2", TokenHash: "abcdefghij", Status: "active", CreatedAt: time.Now()}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("key-1").Return(oldKey, nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("POST", "/auth/api-keys/key-1/rotate", nil)
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	setAuthUser(c, caller)
	h.Rotate(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAPIKeyHandler_Rotate_NotFound(t *testing.T) {
	caller := &database.User{ID: "user-1"}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("missing").Return(nil, nil)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("POST", "/auth/api-keys/missing/rotate", nil)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	setAuthUser(c, caller)
	h.Rotate(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAPIKeyHandler_Rotate_Unauthenticated(t *testing.T) {
	store := handlersmocks.NewMockAPIKeyHandlerStore(t)

	h := handlers.NewAPIKeyHandler(store)
	c, w := newAuthCtx("POST", "/auth/api-keys/key-1/rotate", nil)
	c.Params = gin.Params{{Key: "id", Value: "key-1"}}
	h.Rotate(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
