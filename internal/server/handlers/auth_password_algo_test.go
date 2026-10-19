// file: internal/server/handlers/auth_password_algo_test.go
// version: 1.1.0
// guid: 4f0c7a52-9d3e-4b86-a1c7-6e25b8d0f913
// last-edited: 2026-10-19

package handlers_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The ABS sign-in rehashes a bcrypt user to argon2id and records
// PasswordHashAlgo=argon2id (internal/server/handlers/abs/login.go). The web
// sign-in and the self-service password change used to call bcrypt directly and
// ignore the algorithm column, so after one phone-app sign-in the user could do
// neither. These tests store hashes exactly as the ABS rehash does.

// absRehashedUser returns a user stored the way the ABS rehash leaves it: the
// hash comes from absauth.HashPassword, the same call rehashPassword makes.
func absRehashedUser(t *testing.T, password string) *database.User {
	t.Helper()
	algo, hash, err := absauth.HashPassword(password)
	require.NoError(t, err)
	require.Equal(t, absauth.AlgoArgon2id, algo)
	return &database.User{
		ID:               "user-1",
		Username:         "alice",
		PasswordHash:     hash,
		PasswordHashAlgo: algo,
		Roles:            []string{"user"},
		Status:           "active",
		CreatedAt:        time.Now(),
	}
}

func TestAuthHandler_Login_AcceptsArgon2idHash(t *testing.T) {
	user := absRehashedUser(t, "password123")
	session := &database.Session{ID: "sess-abc", UserID: user.ID, ExpiresAt: time.Now().Add(24 * time.Hour)}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("alice").Return(user, nil)
	store.EXPECT().GetRoleByID("user").Return(nil, nil)
	store.EXPECT().CreateSessionWithOrigin(user.ID, mock.Anything, mock.Anything, mock.Anything, database.SessionOriginPassword).Return(session, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "alice", "password": "password123"})
	h.Login(c)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestAuthHandler_Login_Argon2idWrongPasswordDenied(t *testing.T) {
	user := absRehashedUser(t, "password123")

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("alice").Return(user, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "alice", "password": "wrong-password"})
	h.Login(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// An explicit "bcrypt" algo (what every web-created user has) still verifies.
func TestAuthHandler_Login_AcceptsExplicitBcryptAlgo(t *testing.T) {
	user := &database.User{
		ID: "user-1", Username: "alice", PasswordHash: testBcryptHash(t, "password123"),
		PasswordHashAlgo: absauth.AlgoBcrypt, Roles: []string{"user"}, Status: "active", CreatedAt: time.Now(),
	}
	session := &database.Session{ID: "sess-abc", UserID: user.ID, ExpiresAt: time.Now().Add(24 * time.Hour)}

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByUsername("alice").Return(user, nil)
	store.EXPECT().GetRoleByID("user").Return(nil, nil)
	store.EXPECT().CreateSessionWithOrigin(user.ID, mock.Anything, mock.Anything, mock.Anything, database.SessionOriginPassword).Return(session, nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "alice", "password": "password123"})
	h.Login(c)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// An algorithm the verifier does not know fails closed, even when the stored
// hash happens to be a valid bcrypt hash of the supplied password.
func TestAuthHandler_Login_UnknownAlgoDenied(t *testing.T) {
	for _, algo := range []string{"scrypt", "md5", absauth.AlgoOAuth} {
		t.Run(algo, func(t *testing.T) {
			user := &database.User{
				ID: "user-1", Username: "alice", PasswordHash: testBcryptHash(t, "password123"),
				PasswordHashAlgo: algo, Roles: []string{"user"}, Status: "active", CreatedAt: time.Now(),
			}
			store := handlersmocks.NewMockAuthStore(t)
			store.EXPECT().GetUserByUsername("alice").Return(user, nil)

			h := handlers.NewAuthHandler(store, true)
			c, w := newAuthCtx("POST", "/auth/login", map[string]any{"username": "alice", "password": "password123"})
			h.Login(c)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	}
}

func TestAuthHandler_ChangePassword_AcceptsArgon2idCurrentPassword(t *testing.T) {
	user := absRehashedUser(t, "old-password-1")

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByID(user.ID).Return(user, nil)
	var saved *database.User
	store.EXPECT().UpdateUser(mock.Anything).RunAndReturn(func(u *database.User) error {
		saved = u
		return nil
	})

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("PUT", "/auth/me/password", map[string]any{
		"current_password": "old-password-1",
		"new_password":     "new-password-2",
	})
	setAuthUser(c, user)
	h.ChangePassword(c)

	require.Equal(t, http.StatusNoContent, c.Writer.Status(), w.Body.String())
	require.NotNil(t, saved)
	// Web password writes use the shared policy (argon2id), and the algo column
	// must say so, or the new hash would be read with the wrong algorithm.
	assert.Equal(t, absauth.AlgoArgon2id, saved.PasswordHashAlgo)
	ok, _ := absauth.VerifyPassword(saved.PasswordHashAlgo, saved.PasswordHash, "new-password-2")
	assert.True(t, ok)
	oldOK, _ := absauth.VerifyPassword(saved.PasswordHashAlgo, saved.PasswordHash, "old-password-1")
	assert.False(t, oldOK)
}

func TestAuthHandler_ChangePassword_Argon2idWrongCurrentPasswordDenied(t *testing.T) {
	user := absRehashedUser(t, "old-password-1")

	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByID(user.ID).Return(user, nil)

	h := handlers.NewAuthHandler(store, true)
	c, _ := newAuthCtx("PUT", "/auth/me/password", map[string]any{
		"current_password": "not-the-password",
		"new_password":     "new-password-2",
	})
	setAuthUser(c, user)
	h.ChangePassword(c)

	assert.Equal(t, http.StatusUnauthorized, c.Writer.Status())
}

func TestAuthHandler_ChangePassword_BcryptStillWorks(t *testing.T) {
	user := &database.User{
		ID: "user-1", Username: "alice", PasswordHash: testBcryptHash(t, "old-password-1"),
		PasswordHashAlgo: absauth.AlgoBcrypt, Roles: []string{"user"}, Status: "active", CreatedAt: time.Now(),
	}
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByID(user.ID).Return(user, nil)
	store.EXPECT().UpdateUser(mock.Anything).Return(nil)

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("PUT", "/auth/me/password", map[string]any{
		"current_password": "old-password-1",
		"new_password":     "new-password-2",
	})
	setAuthUser(c, user)
	h.ChangePassword(c)

	assert.Equal(t, http.StatusNoContent, c.Writer.Status(), w.Body.String())
}

// A row written before the algo column was populated has an empty algo and a bcrypt
// hash. The user can change their password, and the new hash is argon2id.
func TestAuthHandler_ChangePassword_LegacyEmptyAlgoBcryptRow(t *testing.T) {
	user := &database.User{
		ID: "user-1", Username: "alice", PasswordHash: testBcryptHash(t, "old-password-1"),
		PasswordHashAlgo: "", Roles: []string{"user"}, Status: "active", CreatedAt: time.Now(),
	}
	store := handlersmocks.NewMockAuthStore(t)
	store.EXPECT().GetUserByID(user.ID).Return(user, nil)
	var saved *database.User
	store.EXPECT().UpdateUser(mock.Anything).RunAndReturn(func(u *database.User) error {
		saved = u
		return nil
	})

	h := handlers.NewAuthHandler(store, true)
	c, w := newAuthCtx("PUT", "/auth/me/password", map[string]any{
		"current_password": "old-password-1",
		"new_password":     "new-password-2",
	})
	setAuthUser(c, user)
	h.ChangePassword(c)

	require.Equal(t, http.StatusNoContent, c.Writer.Status(), w.Body.String())
	require.NotNil(t, saved)
	assert.Equal(t, absauth.AlgoArgon2id, saved.PasswordHashAlgo)
	ok, _ := absauth.VerifyPassword(saved.PasswordHashAlgo, saved.PasswordHash, "new-password-2")
	assert.True(t, ok)
}

func TestAuthHandler_ChangePassword_UnknownAlgoDenied(t *testing.T) {
	for _, algo := range []string{"scrypt", absauth.AlgoOAuth} {
		t.Run(algo, func(t *testing.T) {
			user := &database.User{
				ID: "user-1", Username: "alice", PasswordHash: testBcryptHash(t, "old-password-1"),
				PasswordHashAlgo: algo, Roles: []string{"user"}, Status: "active", CreatedAt: time.Now(),
			}
			// No UpdateUser expectation: the strict mock fails the test if the
			// handler writes a new password after a denied verification.
			store := handlersmocks.NewMockAuthStore(t)
			store.EXPECT().GetUserByID(user.ID).Return(user, nil)

			h := handlers.NewAuthHandler(store, true)
			c, _ := newAuthCtx("PUT", "/auth/me/password", map[string]any{
				"current_password": "old-password-1",
				"new_password":     "new-password-2",
			})
			setAuthUser(c, user)
			h.ChangePassword(c)

			assert.Equal(t, http.StatusUnauthorized, c.Writer.Status())
		})
	}
}
