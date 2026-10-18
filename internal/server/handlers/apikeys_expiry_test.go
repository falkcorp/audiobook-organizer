// file: internal/server/handlers/apikeys_expiry_test.go
// version: 1.2.0
// guid: 4c8e2a17-9b3d-4f61-8a05-d7e2c9b14f38
// last-edited: 2026-10-18

package handlers_test

import (
	"encoding/json"
	"net/http"
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
)

// asCaller binds user, method and permissions the way the auth middleware
// does; key, when non-nil, is the API key that authenticated the request.
func asCaller(c *gin.Context, user *database.User, method auth.Method, key *database.APIKey, perms ...auth.Permission) {
	c.Set("auth_user", user) // not setAuthUser: that records a session method
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
	// An API key asking for another user's key is refused at the route
	// (server.credRouteWhen); credential_routes_test.go covers it.
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

// A caller that is not a person and has no calling key with a real expiry
// cannot mint a key: the clamp has nothing to clamp to (the
// 2026-10-07 review's fail-open: a key with no expiry used to mint keys of
// any length).
func TestAPIKey_NoExpiryToClampTo_Refused(t *testing.T) {
	callers := map[string]func(c *gin.Context){
		"api key without expiry": func(c *gin.Context) {
			asCaller(c, &database.User{ID: "user-1"}, auth.MethodAPIKey, &database.APIKey{ID: "k"}, auth.All()...)
		},
		"api key with zero expiry": func(c *gin.Context) {
			var zero time.Time
			asCaller(c, &database.User{ID: "user-1"}, auth.MethodAPIKey, &database.APIKey{ID: "k", ExpiresAt: &zero}, auth.All()...)
		},
		"api-key method but no key on the request": func(c *gin.Context) {
			asCaller(c, &database.User{ID: "user-1"}, auth.MethodAPIKey, nil, auth.All()...)
		},
		"abs token": func(c *gin.Context) {
			asCaller(c, &database.User{ID: "user-1"}, auth.MethodABS, nil, auth.All()...)
		},
	}
	for name, as := range callers {
		t.Run("create/"+name, func(t *testing.T) {
			store := handlersmocks.NewMockAPIKeyHandlerStore(t) // no CreateAPIKey expected
			c, w := newAuthCtx("POST", "/auth/api-keys", map[string]any{"name": "child", "expires_in_days": 365})
			as(c)
			handlers.NewAPIKeyHandler(store).Create(c)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		})
	}
}
