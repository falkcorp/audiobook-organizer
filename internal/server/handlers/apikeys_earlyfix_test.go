// file: internal/server/handlers/apikeys_earlyfix_test.go
// version: 1.0.0
// guid: 36700d48-c983-4568-a301-abd5f8a675a7
// last-edited: 2026-11-05

package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// earlyFixStore is a real in-memory store holding one active user, the owner
// of every key the early-fix tests create.
func earlyFixStore(t *testing.T) (*database.PebbleStore, *database.User) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(t.TempDir() + "/earlyfix.pebble")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	owner, err := store.CreateUser("bruce-wayne", "", "bcrypt", "x", []string{auth.SeedRoleAdmin}, "active")
	require.NoError(t, err)
	return store, owner
}

// storedKey writes key for owner under a fresh token and returns the raw token.
func storedKey(t *testing.T, store *database.PebbleStore, owner *database.User, key *database.APIKey) (string, *database.APIKey) {
	t.Helper()
	raw, hash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	exp := time.Now().Add(time.Hour)
	key.UserID = owner.ID
	key.TokenHash = hash
	key.ExpiresAt = &exp
	created, err := store.CreateAPIKey(key)
	require.NoError(t, err)
	return raw, created
}

// rowJSON is the stored row as bytes, so "unchanged" compares every field.
func rowJSON(t *testing.T, store *database.PebbleStore, id string) []byte {
	t.Helper()
	k, err := store.GetAPIKey(id)
	require.NoError(t, err)
	require.NotNil(t, k)
	b, err := json.Marshal(k)
	require.NoError(t, err)
	return b
}

// TestLegacyAPIKey_RevokedNotReactivated is 05-authn §11.1 item 3 on every
// path: a key whose status is "revoked", or whose revoked_at is set whatever
// its status, is never made usable again — not by PATCH, not by the store,
// and neither key-auth path accepts it.
func TestLegacyAPIKey_RevokedNotReactivated(t *testing.T) {
	store, owner := earlyFixStore(t)

	_, revoked := storedKey(t, store, owner, &database.APIKey{Name: "Hush", Status: "active"})
	require.NoError(t, store.RevokeAPIKey(revoked.ID))
	at := time.Now().Add(-time.Minute)
	rawRevokedAt, revokedAt := storedKey(t, store, owner, &database.APIKey{Name: "The Killing Joke", Status: "active", RevokedAt: &at})

	rows := map[string]string{
		"status revoked":                revoked.ID,
		"status active, revoked_at set": revokedAt.ID,
	}

	t.Run("handler PATCH answers 409", func(t *testing.T) {
		h := handlers.NewAPIKeyHandler(store)
		for name, id := range rows {
			for _, status := range []string{"active", "inactive"} {
				t.Run(name+"/"+status, func(t *testing.T) {
					before := rowJSON(t, store, id)
					c, w := newAuthCtx("PATCH", "/auth/api-keys/"+id, map[string]any{"status": status})
					c.Params = gin.Params{{Key: "id", Value: id}}
					setAuthUser(c, owner)
					h.UpdateStatus(c)
					assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
					assert.JSONEq(t, string(before), string(rowJSON(t, store, id)), "a refused PATCH must leave the row unchanged")
				})
			}
		}
	})

	t.Run("store refuses a transition to active", func(t *testing.T) {
		for name, id := range rows {
			t.Run(name, func(t *testing.T) {
				before := rowJSON(t, store, id)
				err := store.SetAPIKeyStatus(id, "active", time.Now())
				assert.True(t, errors.Is(err, database.ErrAPIKeyRevoked), "got %v", err)
				assert.JSONEq(t, string(before), string(rowJSON(t, store, id)), "a refused transition must write nothing")
			})
		}
	})

	t.Run("web key path answers 401 and writes nothing", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(servermiddleware.RequireAuth(store))
		r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
		before, err := store.GetAPIKey(revokedAt.ID)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer "+rawRevokedAt)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		after, err := store.GetAPIKey(revokedAt.ID)
		require.NoError(t, err)
		assert.Equal(t, before.UseCount, after.UseCount, "a refused key's request must not touch the row")
	})

	t.Run("ABS key path answers apikey-revoked", func(t *testing.T) {
		cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: strings.Repeat("batman", 6), AuthModes: "jwt"})
		require.NoError(t, err)
		resolver := servermiddleware.NewABSIdentityResolver(cfg, nil, nil, store)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/me", nil)
		c.Request.Header.Set("Authorization", "Bearer "+rawRevokedAt)
		id, e := resolver.ResolveAPIKey(c)
		assert.Nil(t, id)
		require.NotNil(t, e)
		assert.Equal(t, "apikey-revoked", e.Reason)
	})
}

// TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint is 05-authn §11.1 item 2:
// a key caller must name non-empty scopes, every one in its effective set,
// and a nil caller set is the empty set for every caller.
func TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint(t *testing.T) {
	callerExp := time.Now().Add(time.Hour)
	callingKey := &database.APIKey{ID: "calling-key", ExpiresAt: &callerExp}
	view := string(auth.PermLibraryView)

	cases := []struct {
		name   string
		as     func(c *gin.Context, owner *database.User)
		scopes []string
		want   int
	}{
		{"key, empty effective set, empty scopes", func(c *gin.Context, u *database.User) {
			asCaller(c, u, auth.MethodAPIKey, callingKey)
		}, []string{}, http.StatusForbidden},
		{"key, empty effective set, library.view", func(c *gin.Context, u *database.User) {
			asCaller(c, u, auth.MethodAPIKey, callingKey)
		}, []string{view}, http.StatusForbidden},
		{"key holding library.view, library.view", func(c *gin.Context, u *database.User) {
			asCaller(c, u, auth.MethodAPIKey, callingKey, auth.PermLibraryView)
		}, []string{view}, http.StatusCreated},
		{"key holding library.view, empty scopes", func(c *gin.Context, u *database.User) {
			asCaller(c, u, auth.MethodAPIKey, callingKey, auth.PermLibraryView)
		}, []string{}, http.StatusForbidden},
		{"session, empty scopes (HEAD behaviour kept)", func(c *gin.Context, u *database.User) {
			setAuthUser(c, u)
		}, []string{}, http.StatusCreated},
		{"session, nil set, library.view (nil no longer skips the subset check)", func(c *gin.Context, u *database.User) {
			setAuthUser(c, u)
		}, []string{view}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, owner := earlyFixStore(t)
			c, w := newAuthCtx("POST", "/auth/api-keys", map[string]any{"name": "Arkham Asylum", "scopes": tc.scopes})
			tc.as(c, owner)
			handlers.NewAPIKeyHandler(store).Create(c)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			keys, err := store.ListAPIKeysForUser(owner.ID)
			require.NoError(t, err)
			if tc.want == http.StatusCreated {
				assert.Len(t, keys, 1)
			} else {
				assert.Empty(t, keys, "a refused create must mint no key")
			}
		})
	}
}
