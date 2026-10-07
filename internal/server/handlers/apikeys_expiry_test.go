// file: internal/server/handlers/apikeys_expiry_test.go
// version: 1.0.0
// guid: 4c8e2a17-9b3d-4f61-8a05-d7e2c9b14f38
// last-edited: 2026-10-07

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// asCaller binds user, method and permissions the way the auth middleware
// does; key, when non-nil, is the API key that authenticated the request.
func asCaller(c *gin.Context, user *database.User, method auth.Method, key *database.APIKey, perms ...auth.Permission) {
	setAuthUser(c, user)
	if key != nil {
		c.Set("auth_api_key", key)
	}
	ctx := auth.WithUser(c.Request.Context(), user)
	ctx = auth.WithPermissions(ctx, perms)
	ctx = auth.WithMethod(ctx, method)
	c.Request = c.Request.WithContext(ctx)
}

func echoCreatedKey(store *handlersmocks.MockAPIKeyHandlerStore, captured **database.APIKey) {
	store.EXPECT().CreateAPIKey(mock.Anything).RunAndReturn(func(k *database.APIKey) (*database.APIKey, error) {
		k.ID = "new-key"
		k.CreatedAt = time.Now()
		*captured = k
		return k, nil
	})
}

func expiresIn(t *testing.T, got *time.Time, want time.Duration) {
	t.Helper()
	require.NotNil(t, got, "every API key must have an expiry")
	d := time.Until(*got)
	assert.InDelta(t, want.Seconds(), d.Seconds(), 120, "expiry in %v, want about %v", d, want)
}

func TestAPIKeyCreate_DefaultsTo30Days(t *testing.T) {
	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	var created *database.APIKey
	echoCreatedKey(store, &created)

	c, w := newAuthCtx("POST", "/auth/api-keys", map[string]any{"name": "no expiry asked"})
	asCaller(c, &database.User{ID: "user-1"}, auth.MethodSession, nil)
	handlers.NewAPIKeyHandler(store).Create(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	expiresIn(t, created.ExpiresAt, handlers.DefaultAPIKeyTTL)
}

func TestAPIKeyCreate_365Accepted(t *testing.T) {
	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	var created *database.APIKey
	echoCreatedKey(store, &created)

	c, w := newAuthCtx("POST", "/auth/api-keys", map[string]any{"name": "max", "expires_in_days": 365})
	asCaller(c, &database.User{ID: "user-1"}, auth.MethodSession, nil)
	handlers.NewAPIKeyHandler(store).Create(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	expiresIn(t, created.ExpiresAt, handlers.MaxAPIKeyTTL)
}

func TestAPIKeyCreate_OutOfRangeIs400(t *testing.T) {
	for _, days := range []int{366, 3650, -1} {
		store := handlersmocks.NewMockAPIKeyHandlerStore(t) // no CreateAPIKey expected
		c, w := newAuthCtx("POST", "/auth/api-keys", map[string]any{"name": "too long", "expires_in_days": days})
		asCaller(c, &database.User{ID: "user-1"}, auth.MethodSession, nil)
		handlers.NewAPIKeyHandler(store).Create(c)
		assert.Equal(t, http.StatusBadRequest, w.Code, "expires_in_days=%d: %s", days, w.Body.String())
	}
}

// An 8h bootstrap key must not mint itself a 365-day key.
func TestAPIKeyCreate_ByAPIKeyClampedToCallingKey(t *testing.T) {
	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	var created *database.APIKey
	echoCreatedKey(store, &created)

	callerExp := time.Now().Add(8 * time.Hour)
	callingKey := &database.APIKey{ID: "boot", UserID: "admin-1", ExpiresAt: &callerExp}
	c, w := newAuthCtx("POST", "/auth/api-keys", map[string]any{"name": "child", "expires_in_days": 365})
	asCaller(c, &database.User{ID: "admin-1"}, auth.MethodAPIKey, callingKey, auth.All()...)
	handlers.NewAPIKeyHandler(store).Create(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.NotNil(t, created.ExpiresAt)
	assert.False(t, created.ExpiresAt.After(callerExp), "child key %v outlives the calling key %v", created.ExpiresAt, callerExp)
	var resp struct {
		Data handlers.CreateAPIKeyResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp.Data.Note, "cannot mint a key that outlives it")
}

func TestAPIKeyCreate_ForAnotherUser(t *testing.T) {
	t.Run("an API key is refused", func(t *testing.T) {
		store := handlersmocks.NewMockAPIKeyHandlerStore(t) // no CreateAPIKey expected
		exp := time.Now().Add(time.Hour)
		c, w := newAuthCtx("POST", "/auth/api-keys", map[string]any{"name": "for bob", "user_id": "bob"})
		asCaller(c, &database.User{ID: "admin-1"}, auth.MethodAPIKey, &database.APIKey{ID: "k", ExpiresAt: &exp}, auth.All()...)
		handlers.NewAPIKeyHandler(store).Create(c)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), servermiddleware.CredentialChangeRefusedMessage)
	})
	t.Run("a signed-in admin is allowed", func(t *testing.T) {
		store := handlersmocks.NewMockAPIKeyHandlerStore(t)
		var created *database.APIKey
		echoCreatedKey(store, &created)
		c, w := newAuthCtx("POST", "/auth/api-keys", map[string]any{"name": "for bob", "user_id": "bob"})
		asCaller(c, &database.User{ID: "admin-1"}, auth.MethodSession, nil, auth.All()...)
		handlers.NewAPIKeyHandler(store).Create(c)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		assert.Equal(t, "bob", created.UserID)
	})
}

func rotateCtx(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, w := newAuthCtx("POST", "/auth/api-keys/old/rotate", nil)
	c.Params = gin.Params{{Key: "id", Value: "old"}}
	return c, w
}

// Rotation keeps the old key's lifetime: an 8h key rotates into an 8h key.
func TestAPIKeyRotate_KeepsLifetime(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	exp := created.Add(8 * time.Hour)
	old := &database.APIKey{ID: "old", UserID: "user-1", Status: "active", CreatedAt: created, ExpiresAt: &exp}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("old").Return(old, nil)
	var newKey *database.APIKey
	echoCreatedKey(store, &newKey)
	store.EXPECT().SetAPIKeyExpiry("old", mock.Anything).Return(nil)

	c, w := rotateCtx(t)
	asCaller(c, &database.User{ID: "user-1"}, auth.MethodSession, nil)
	handlers.NewAPIKeyHandler(store).Rotate(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	expiresIn(t, newKey.ExpiresAt, 8*time.Hour)
}

// An 8h bootstrap key must not rotate a year-long sibling key (same user)
// into a fresh year-long token.
func TestAPIKeyRotate_ByAPIKeyClampedToCallingKey(t *testing.T) {
	callerExp := time.Now().Add(8 * time.Hour)
	boot := &database.APIKey{ID: "boot", UserID: "admin-1", Status: "active", CreatedAt: time.Now(), ExpiresAt: &callerExp}
	yearExp := time.Now().Add(300 * 24 * time.Hour)
	sibling := &database.APIKey{ID: "old", UserID: "admin-1", Status: "active", CreatedAt: time.Now().Add(-65 * 24 * time.Hour), ExpiresAt: &yearExp}

	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	store.EXPECT().GetAPIKey("old").Return(sibling, nil)
	var newKey *database.APIKey
	echoCreatedKey(store, &newKey)
	store.EXPECT().SetAPIKeyExpiry("old", mock.Anything).Return(nil)

	c, w := rotateCtx(t)
	asCaller(c, &database.User{ID: "admin-1"}, auth.MethodAPIKey, boot, auth.All()...)
	handlers.NewAPIKeyHandler(store).Rotate(c)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.NotNil(t, newKey.ExpiresAt)
	assert.False(t, newKey.ExpiresAt.After(callerExp), "rotated key %v outlives the calling key %v", newKey.ExpiresAt, callerExp)
	assert.Contains(t, w.Body.String(), "cannot mint a key that outlives it")
}

func TestAPIKeyRotate_LifetimeCappedAndDefaulted(t *testing.T) {
	cases := []struct {
		name string
		old  func() *database.APIKey
		want time.Duration
	}{
		{"no expiry gets the default", func() *database.APIKey {
			return &database.APIKey{ID: "old", UserID: "user-1", CreatedAt: time.Now()}
		}, handlers.DefaultAPIKeyTTL},
		{"over the maximum is capped", func() *database.APIKey {
			exp := time.Now().Add(10 * 365 * 24 * time.Hour)
			return &database.APIKey{ID: "old", UserID: "user-1", CreatedAt: time.Now(), ExpiresAt: &exp}
		}, handlers.MaxAPIKeyTTL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := handlersmocks.NewMockAPIKeyHandlerStore(t)
			store.EXPECT().GetAPIKey("old").Return(tc.old(), nil)
			var newKey *database.APIKey
			echoCreatedKey(store, &newKey)
			store.EXPECT().SetAPIKeyExpiry("old", mock.Anything).Return(nil)

			c, w := rotateCtx(t)
			asCaller(c, &database.User{ID: "user-1"}, auth.MethodSession, nil)
			handlers.NewAPIKeyHandler(store).Rotate(c)

			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			expiresIn(t, newKey.ExpiresAt, tc.want)
		})
	}
}

// Rotating ANOTHER user's key hands back that user's new token.
func TestAPIKeyRotate_AnotherUsersKey(t *testing.T) {
	bobsKey := func() *database.APIKey {
		return &database.APIKey{ID: "old", UserID: "bob", Status: "active", CreatedAt: time.Now()}
	}
	t.Run("an API key is refused", func(t *testing.T) {
		store := handlersmocks.NewMockAPIKeyHandlerStore(t)
		store.EXPECT().GetAPIKey("old").Return(bobsKey(), nil)
		c, w := rotateCtx(t)
		exp := time.Now().Add(time.Hour)
		asCaller(c, &database.User{ID: "admin-1"}, auth.MethodAPIKey, &database.APIKey{ID: "k", ExpiresAt: &exp}, auth.All()...)
		handlers.NewAPIKeyHandler(store).Rotate(c)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), servermiddleware.CredentialChangeRefusedMessage)
	})
	t.Run("a signed-in admin is allowed", func(t *testing.T) {
		store := handlersmocks.NewMockAPIKeyHandlerStore(t)
		store.EXPECT().GetAPIKey("old").Return(bobsKey(), nil)
		var newKey *database.APIKey
		echoCreatedKey(store, &newKey)
		store.EXPECT().SetAPIKeyExpiry("old", mock.Anything).Return(nil)
		c, w := rotateCtx(t)
		asCaller(c, &database.User{ID: "admin-1"}, auth.MethodCFAccess, nil, auth.All()...)
		handlers.NewAPIKeyHandler(store).Rotate(c)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		assert.Equal(t, "bob", newKey.UserID)
	})
}
