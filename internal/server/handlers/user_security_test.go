// file: internal/server/handlers/user_security_test.go
// version: 1.0.1
// guid: 7f1c2a93-4d8e-4b6a-9c10-2e5b7a0d1f44
// last-edited: 2026-06-09

package handlers_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// assertNoPasswordHashLeak fails if the JSON response contains any password-hash
// field anywhere in its body. Pen-test finding CRIT-2: handlers returned the raw
// *database.User, which serialized password_hash/password_hash_algo.
func assertNoPasswordHashLeak(t *testing.T, body []byte) {
	t.Helper()
	s := string(body)
	for _, banned := range []string{"password_hash", "password_hash_algo", "PasswordHash"} {
		assert.NotContains(t, s, banned, "response leaked a password hash field (CRIT-2)")
	}
}

func sampleUser() *database.User {
	return &database.User{
		ID:               "u-123",
		Username:         "editoruser",
		Email:            "editor@local",
		Roles:            []string{"editor"},
		Status:           "active",
		PasswordHash:     "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV",
		PasswordHashAlgo: "bcrypt",
		CreatedAt:        time.Now(),
	}
}

func TestAcceptInvite_DoesNotLeakPasswordHash(t *testing.T) {
	store := handlersmocks.NewMockUserStore(t)
	user := sampleUser()
	store.EXPECT().ConsumeInvite("tok-1", "bcrypt", mock.Anything).Return(user, nil)
	store.EXPECT().CreateSession(user.ID, mock.Anything, mock.Anything, mock.Anything).
		Return(&database.Session{ID: "sess-1", ExpiresAt: time.Now().Add(time.Hour)}, nil)

	h := handlers.NewUserHandler(store)
	c, w := newAuthCtx("POST", "/api/v1/auth/accept-invite", map[string]any{
		"token":    "tok-1",
		"password": "supersecret",
	})
	h.AcceptInvite(c)

	require.Equal(t, http.StatusCreated, w.Code)
	assertNoPasswordHashLeak(t, w.Body.Bytes())

	// The safe user shape must still carry the non-sensitive identity fields.
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	gotUser := data["user"].(map[string]any)
	assert.Equal(t, "editoruser", gotUser["username"])
}

func TestDeactivateUser_DoesNotLeakPasswordHash(t *testing.T) {
	store := handlersmocks.NewMockUserStore(t)
	user := sampleUser()
	store.EXPECT().GetUserByID("u-123").Return(user, nil)
	store.EXPECT().UpdateUser(mock.Anything).Return(nil)

	h := handlers.NewUserHandler(store)
	c, w := newAuthCtx("POST", "/api/v1/users/u-123/deactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.DeactivateUser(c)

	require.Equal(t, http.StatusOK, w.Code)
	assertNoPasswordHashLeak(t, w.Body.Bytes())
}

func TestReactivateUser_DoesNotLeakPasswordHash(t *testing.T) {
	store := handlersmocks.NewMockUserStore(t)
	user := sampleUser()
	store.EXPECT().GetUserByID("u-123").Return(user, nil)
	store.EXPECT().UpdateUser(mock.Anything).Return(nil)

	h := handlers.NewUserHandler(store)
	c, w := newAuthCtx("POST", "/api/v1/users/u-123/reactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.ReactivateUser(c)

	require.Equal(t, http.StatusOK, w.Code)
	assertNoPasswordHashLeak(t, w.Body.Bytes())
}

// guard against accidental field-name drift in the banned-substring list.
func TestSampleUserActuallyHasHash(t *testing.T) {
	require.True(t, strings.HasPrefix(sampleUser().PasswordHash, "$2a$"))
}
