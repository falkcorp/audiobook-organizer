// file: internal/server/middleware/auth_test.go
// version: 1.0.0
// guid: 7c6f88af-1ef3-4f1f-b702-13935ff2cf9f

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionTokenFromRequest(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	assert.Equal(t, "", SessionTokenFromRequest(nil))
	assert.Equal(t, "", SessionTokenFromRequest(req))

	req.Header.Set("Authorization", "Bearer test-token")
	assert.Equal(t, "test-token", SessionTokenFromRequest(req))

	req.Header.Set("Authorization", "bearer lower-token")
	assert.Equal(t, "lower-token", SessionTokenFromRequest(req))

	req.Header.Set("Authorization", "Bearer   ")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "cookie-token"})
	assert.Equal(t, "cookie-token", SessionTokenFromRequest(req))
}

func TestCurrentUserAndSession(t *testing.T) {
	t.Parallel()

	assert.Nil(t, func() *database.User {
		user, _ := CurrentUser(nil)
		return user
	}())

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	user := &database.User{ID: "user-1", Username: "admin"}
	session := &database.Session{ID: "sess-1", UserID: "user-1"}
	ctx.Set(contextUserKey, user)
	ctx.Set(contextSessionKey, session)

	gotUser, okUser := CurrentUser(ctx)
	require.True(t, okUser)
	assert.Equal(t, user, gotUser)

	gotSession, okSession := CurrentSession(ctx)
	require.True(t, okSession)
	assert.Equal(t, session, gotSession)
}

func TestRequireAuth_AllowsWhenStoreIsNil(t *testing.T) {
	t.Parallel()

	resp, hit, _ := executeAuthMiddleware(
		t,
		RequireAuth(nil),
		httptest.NewRequest(http.MethodGet, "/protected", nil),
	)

	assert.True(t, hit)
	assert.Equal(t, http.StatusOK, resp.Code)
}

func TestRequireAuth_BootstrapMode(t *testing.T) {
	t.Parallel()

	store := &database.MockStore{
		CountUsersFunc: func() (int, error) { return 0, nil },
	}
	resp, hit, _ := executeAuthMiddleware(
		t,
		RequireAuth(store),
		httptest.NewRequest(http.MethodGet, "/protected", nil),
	)

	assert.True(t, hit)
	assert.Equal(t, http.StatusOK, resp.Code)
}

func TestRequireAuth_StoreCountFailure(t *testing.T) {
	t.Parallel()

	store := &database.MockStore{
		CountUsersFunc: func() (int, error) { return 0, errors.New("boom") },
	}
	resp, hit, _ := executeAuthMiddleware(
		t,
		RequireAuth(store),
		httptest.NewRequest(http.MethodGet, "/protected", nil),
	)

	assert.False(t, hit)
	assert.Equal(t, http.StatusInternalServerError, resp.Code)
	assert.Contains(t, resp.Body.String(), "failed to check auth state")
}

func TestRequireAuth_MissingToken(t *testing.T) {
	t.Parallel()

	store := &database.MockStore{
		CountUsersFunc: func() (int, error) { return 1, nil },
	}
	resp, hit, _ := executeAuthMiddleware(
		t,
		RequireAuth(store),
		httptest.NewRequest(http.MethodGet, "/protected", nil),
	)

	assert.False(t, hit)
	assert.Equal(t, http.StatusUnauthorized, resp.Code)
	assert.Contains(t, resp.Body.String(), "authentication required")
}

func TestRequireAuth_InvalidAndExpiredSession(t *testing.T) {
	t.Parallel()

	t.Run("invalid session", func(t *testing.T) {
		t.Parallel()
		store := &database.MockStore{
			CountUsersFunc: func() (int, error) { return 1, nil },
			GetSessionFunc: func(id string) (*database.Session, error) { return nil, nil },
		}

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer invalid")

		resp, hit, _ := executeAuthMiddleware(t, RequireAuth(store), req)
		assert.False(t, hit)
		assert.Equal(t, http.StatusUnauthorized, resp.Code)
		assert.Contains(t, resp.Body.String(), "invalid session")
	})

	t.Run("expired session is revoked", func(t *testing.T) {
		t.Parallel()
		revoked := ""
		store := &database.MockStore{
			CountUsersFunc: func() (int, error) { return 1, nil },
			GetSessionFunc: func(id string) (*database.Session, error) {
				return &database.Session{
					ID:        id,
					UserID:    "user-1",
					ExpiresAt: time.Now().Add(-1 * time.Minute),
				}, nil
			},
			RevokeSessionFunc: func(id string) error {
				revoked = id
				return nil
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer expired-token")

		resp, hit, _ := executeAuthMiddleware(t, RequireAuth(store), req)
		assert.False(t, hit)
		assert.Equal(t, http.StatusUnauthorized, resp.Code)
		assert.Equal(t, "expired-token", revoked)
		assert.Contains(t, resp.Body.String(), "session expired")
	})
}

func TestRequireAuth_UserValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing user", func(t *testing.T) {
		t.Parallel()
		store := &database.MockStore{
			CountUsersFunc: func() (int, error) { return 1, nil },
			GetSessionFunc: func(id string) (*database.Session, error) {
				return &database.Session{
					ID:        id,
					UserID:    "user-1",
					ExpiresAt: time.Now().Add(15 * time.Minute),
				}, nil
			},
			GetUserByIDFunc: func(id string) (*database.User, error) {
				return nil, nil
			},
		}
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer missing-user")
		resp, hit, _ := executeAuthMiddleware(t, RequireAuth(store), req)
		assert.False(t, hit)
		assert.Equal(t, http.StatusUnauthorized, resp.Code)
		assert.Contains(t, resp.Body.String(), "invalid session user")
	})

	t.Run("inactive user", func(t *testing.T) {
		t.Parallel()
		store := &database.MockStore{
			CountUsersFunc: func() (int, error) { return 1, nil },
			GetSessionFunc: func(id string) (*database.Session, error) {
				return &database.Session{
					ID:        id,
					UserID:    "user-1",
					ExpiresAt: time.Now().Add(15 * time.Minute),
				}, nil
			},
			GetUserByIDFunc: func(id string) (*database.User, error) {
				return &database.User{ID: id, Status: "disabled"}, nil
			},
		}
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer inactive")
		resp, hit, _ := executeAuthMiddleware(t, RequireAuth(store), req)
		assert.False(t, hit)
		assert.Equal(t, http.StatusUnauthorized, resp.Code)
		assert.Contains(t, resp.Body.String(), "inactive user")
	})
}

func TestRequireAuth_ValidSessionSetsContext(t *testing.T) {
	t.Parallel()

	store := &database.MockStore{
		CountUsersFunc: func() (int, error) { return 1, nil },
		GetSessionFunc: func(id string) (*database.Session, error) {
			return &database.Session{
				ID:        id,
				UserID:    "user-1",
				ExpiresAt: time.Now().Add(15 * time.Minute),
			}, nil
		},
		GetUserByIDFunc: func(id string) (*database.User, error) {
			return &database.User{ID: id, Username: "admin", Status: "active"}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	resp, hit, ctx := executeAuthMiddleware(t, RequireAuth(store), req)

	require.True(t, hit)
	assert.Equal(t, http.StatusOK, resp.Code)

	user, okUser := CurrentUser(ctx)
	require.True(t, okUser)
	assert.Equal(t, "user-1", user.ID)

	session, okSession := CurrentSession(ctx)
	require.True(t, okSession)
	assert.Equal(t, "valid-token", session.ID)
}

func executeAuthMiddleware(
	t *testing.T,
	mw gin.HandlerFunc,
	req *http.Request,
) (*httptest.ResponseRecorder, bool, *gin.Context) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	called := false
	var captured *gin.Context

	router.Use(mw)
	router.GET("/protected", func(c *gin.Context) {
		captured = c.Copy()
		called = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp, called, captured
}
