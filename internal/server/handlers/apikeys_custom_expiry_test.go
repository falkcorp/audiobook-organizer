// file: internal/server/handlers/apikeys_custom_expiry_test.go
// version: 0.1.0
// guid: f9315ce2-3390-4ca5-99c8-194c5e37221e
// last-edited: 2026-11-20

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

// A custom API key expiry may be at most three years (1096 days, so the
// anniversary date is always reachable across a leap day) out, counted from
// the request (owner decisions, 2026-11-20). The clock is pinned
// so each boundary is tested to the second.

var keyClock = time.Date(2026, time.November, 20, 15, 4, 5, 0, time.UTC)

const threeYears = 1096 * 24 * time.Hour

// createKeyAt runs Create with the handler's clock at keyClock, as a
// signed-in user unless as is given.
func createKeyAt(t *testing.T, store *handlersmocks.MockAPIKeyHandlerStore, body map[string]any, as func(c *gin.Context)) (int, string) {
	t.Helper()
	c, w := newAuthCtx("POST", "/auth/api-keys", body)
	if as == nil {
		asCaller(c, &database.User{ID: "user-1"}, auth.MethodSession, nil)
	} else {
		as(c)
	}
	h := handlers.NewAPIKeyHandler(store)
	handlers.SetAPIKeyClockForTest(h, func() time.Time { return keyClock })
	h.Create(c)
	return w.Code, w.Body.String()
}

func TestAPIKeyCreate_ThreeYearCap_Days(t *testing.T) {
	t.Run("1096 days is accepted and lands exactly three years out", func(t *testing.T) {
		store := handlersmocks.NewMockAPIKeyHandlerStore(t)
		var created *database.APIKey
		echoCreatedKey(store, &created)
		code, body := createKeyAt(t, store, map[string]any{"name": "max", "expires_in_days": 1096}, nil)
		require.Equal(t, http.StatusCreated, code, body)
		require.NotNil(t, created.ExpiresAt)
		assert.True(t, created.ExpiresAt.Equal(keyClock.Add(threeYears)), "expiry %v, want %v", created.ExpiresAt, keyClock.Add(threeYears))
	})
	t.Run("1097 days is refused", func(t *testing.T) {
		store := handlersmocks.NewMockAPIKeyHandlerStore(t) // no CreateAPIKey expected
		code, body := createKeyAt(t, store, map[string]any{"name": "too long", "expires_in_days": 1097}, nil)
		assert.Equal(t, http.StatusBadRequest, code, body)
		assert.Contains(t, body, "between 1 and 1096 (3 years)")
	})
}

// expires_in_days 0, sent alone, is still the 30-day default.
func TestAPIKeyCreate_ExplicitZeroDaysIsTheDefault(t *testing.T) {
	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	var created *database.APIKey
	echoCreatedKey(store, &created)
	code, body := createKeyAt(t, store, map[string]any{"name": "zero", "expires_in_days": 0}, nil)
	require.Equal(t, http.StatusCreated, code, body)
	require.NotNil(t, created.ExpiresAt)
	assert.True(t, created.ExpiresAt.Equal(keyClock.Add(handlers.DefaultAPIKeyTTL)), "expiry %v", created.ExpiresAt)
}

func TestAPIKeyCreate_ThreeYearCap_ExpiresAt(t *testing.T) {
	accepted := map[string]time.Time{
		"exactly three years":       keyClock.Add(threeYears),
		"one second after the call": keyClock.Add(time.Second),
		"a date in another offset":  keyClock.Add(400 * 24 * time.Hour).In(time.FixedZone("UTC-7", -7*3600)),
	}
	for name, at := range accepted {
		t.Run("accepted/"+name, func(t *testing.T) {
			store := handlersmocks.NewMockAPIKeyHandlerStore(t)
			var created *database.APIKey
			echoCreatedKey(store, &created)
			code, body := createKeyAt(t, store, map[string]any{"name": "custom", "expires_at": at.Format(time.RFC3339)}, nil)
			require.Equal(t, http.StatusCreated, code, body)
			require.NotNil(t, created.ExpiresAt)
			assert.True(t, created.ExpiresAt.Equal(at), "expiry %v, want %v", created.ExpiresAt, at)
			assert.Equal(t, time.UTC, created.ExpiresAt.Location(), "the stored expiry is UTC")
		})
	}

	refused := map[string]struct {
		body map[string]any
		msg  string
	}{
		"three years and one day": {
			map[string]any{"name": "x", "expires_at": keyClock.Add(threeYears + 24*time.Hour).Format(time.RFC3339)},
			"no more than 1096 days (3 years) from now",
		},
		"three years and one second": {
			map[string]any{"name": "x", "expires_at": keyClock.Add(threeYears + time.Second).Format(time.RFC3339)},
			"no more than 1096 days (3 years) from now",
		},
		"a past date": {
			map[string]any{"name": "x", "expires_at": keyClock.Add(-24 * time.Hour).Format(time.RFC3339)},
			"must be in the future",
		},
		"the moment of the request": {
			map[string]any{"name": "x", "expires_at": keyClock.Format(time.RFC3339)},
			"must be in the future",
		},
		"a date with no time": {
			map[string]any{"name": "x", "expires_at": "2027-01-31"},
			"RFC 3339",
		},
		"both expiry fields, expires_in_days 0": {
			map[string]any{"name": "x", "expires_in_days": 0, "expires_at": keyClock.Add(48 * time.Hour).Format(time.RFC3339)},
			"not both",
		},
		"both expiry fields": {
			map[string]any{"name": "x", "expires_in_days": 30, "expires_at": keyClock.Add(48 * time.Hour).Format(time.RFC3339)},
			"not both",
		},
	}
	for name, tc := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			store := handlersmocks.NewMockAPIKeyHandlerStore(t) // no CreateAPIKey expected
			code, body := createKeyAt(t, store, tc.body, nil)
			assert.Equal(t, http.StatusBadRequest, code, body)
			assert.Contains(t, body, tc.msg)
		})
	}
}

// A key may not mint a key that outlives it, whatever form the expiry takes:
// a three-year expires_at from an 8h key is shortened to the 8h key's expiry.
func TestAPIKeyCreate_ExpiresAtByAPIKeyClampedToCallingKey(t *testing.T) {
	store := handlersmocks.NewMockAPIKeyHandlerStore(t)
	var created *database.APIKey
	echoCreatedKey(store, &created)

	callerExp := keyClock.Add(8 * time.Hour)
	callingKey := &database.APIKey{ID: "boot", UserID: "admin-1", ExpiresAt: &callerExp}
	code, body := createKeyAt(t, store,
		map[string]any{"name": "child", "expires_at": keyClock.Add(threeYears).Format(time.RFC3339), "scopes": []string{string(auth.PermLibraryView)}},
		func(c *gin.Context) {
			asCaller(c, &database.User{ID: "admin-1"}, auth.MethodAPIKey, callingKey, auth.All()...)
		})

	require.Equal(t, http.StatusCreated, code, body)
	require.NotNil(t, created.ExpiresAt)
	assert.True(t, created.ExpiresAt.Equal(callerExp), "child key %v, want the calling key's %v", created.ExpiresAt, callerExp)
	var resp struct {
		Data handlers.CreateAPIKeyResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	assert.Contains(t, resp.Data.Note, "cannot mint a key that outlives it")
}

// The cap is checked before the clamp: a key asking for more than three years
// is refused, not quietly clamped to its own expiry.
func TestAPIKeyCreate_OverCapByAPIKeyIsRefused(t *testing.T) {
	store := handlersmocks.NewMockAPIKeyHandlerStore(t) // no CreateAPIKey expected
	callerExp := keyClock.Add(8 * time.Hour)
	callingKey := &database.APIKey{ID: "boot", UserID: "admin-1", ExpiresAt: &callerExp}
	code, body := createKeyAt(t, store,
		map[string]any{"name": "child", "expires_in_days": 1097, "scopes": []string{string(auth.PermLibraryView)}},
		func(c *gin.Context) {
			asCaller(c, &database.User{ID: "admin-1"}, auth.MethodAPIKey, callingKey, auth.All()...)
		})
	assert.Equal(t, http.StatusBadRequest, code, body)
}
