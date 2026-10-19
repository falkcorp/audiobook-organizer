// file: internal/server/handlers/abs_session_revoke_test.go
// version: 1.0.0
// guid: f5a37469-0393-44b9-9335-7ab7e914098f
// last-edited: 2026-10-19

package handlers_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// absRevokeRecorder is the ABS session capability, recording each revoke and
// the order it happened in relative to UpdateUser.
type absRevokeRecorder struct {
	mu     sync.Mutex
	calls  []string
	events *[]string
	err    error
}

func (r *absRevokeRecorder) RevokeAllABSSessionsForUser(userID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, userID)
	if r.events != nil {
		*r.events = append(*r.events, "revoke")
	}
	return 2, r.err
}

type authStoreWithABS struct {
	*handlersmocks.MockAuthStore
	*absRevokeRecorder
}

type userStoreWithABS struct {
	*handlersmocks.MockUserStore
	*absRevokeRecorder
}

func passwordUser(t *testing.T, id, password string) *database.User {
	t.Helper()
	return &database.User{ID: id, Username: id, Status: "active", PasswordHash: testBcryptHash(t, password), PasswordHashAlgo: "bcrypt"}
}

func TestChangePassword_RevokesEveryABSSessionFirst(t *testing.T) {
	for _, tc := range []struct {
		name  string
		admin bool
	}{{name: "self"}, {name: "admin reset", admin: true}} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			mockStore := handlersmocks.NewMockAuthStore(t)
			rec := &absRevokeRecorder{events: &events}
			target := passwordUser(t, "u-target", "old-password")
			mockStore.EXPECT().GetUserByID("u-target").Return(target, nil)
			mockStore.EXPECT().UpdateUser(mock.Anything).RunAndReturn(func(*database.User) error {
				events = append(events, "update")
				return nil
			})
			h := handlers.NewAuthHandler(authStoreWithABS{mockStore, rec}, true)

			body := map[string]any{"current_password": "old-password", "new_password": "new-password-123"}
			caller := target
			if tc.admin {
				body = map[string]any{"user_id": "u-target", "new_password": "new-password-123"}
				caller = &database.User{ID: "u-admin", Status: "active"}
			}
			c, w := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password", body)
			asCaller(c, caller, auth.MethodSession, nil, auth.PermUsersManage)
			h.ChangePassword(c)

			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
			require.Equal(t, []string{"u-target"}, rec.calls, "every ABS session of the TARGET user must be revoked")
			require.Equal(t, []string{"revoke", "update"}, events, "revoke must happen before the password changes")
		})
	}
}

// A revoke failure must not leave a changed password with live sessions behind:
// the change is refused and the password is untouched.
func TestChangePassword_RevokeFailureLeavesPasswordUnchanged(t *testing.T) {
	mockStore := handlersmocks.NewMockAuthStore(t)
	rec := &absRevokeRecorder{err: errors.New("pebble: partial revoke")}
	target := passwordUser(t, "u1", "old-password")
	mockStore.EXPECT().GetUserByID("u1").Return(target, nil)
	// No UpdateUser expectation: the mock fails the test if it is called.
	h := handlers.NewAuthHandler(authStoreWithABS{mockStore, rec}, true)

	c, w := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password",
		map[string]any{"current_password": "old-password", "new_password": "new-password-123"})
	setAuthUser(c, target)
	h.ChangePassword(c)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "password was not changed")
}

// Without the ABS keyspace there are no ABS sessions; the password still changes.
func TestChangePassword_NoABSKeyspaceStillChangesPassword(t *testing.T) {
	mockStore := handlersmocks.NewMockAuthStore(t)
	target := passwordUser(t, "u1", "old-password")
	mockStore.EXPECT().GetUserByID("u1").Return(target, nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).Return(nil)
	h := handlers.NewAuthHandler(mockStore, true)

	c, w := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password",
		map[string]any{"current_password": "old-password", "new_password": "new-password-123"})
	setAuthUser(c, target)
	h.ChangePassword(c)
	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestDeactivateUser_LocksThenRevokesABSSessions(t *testing.T) {
	var events []string
	mockStore := handlersmocks.NewMockUserStore(t)
	rec := &absRevokeRecorder{events: &events}
	user := sampleUser()
	mockStore.EXPECT().GetUserByID("u-123").Return(user, nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).RunAndReturn(func(u *database.User) error {
		require.Equal(t, "locked", u.Status)
		events = append(events, "update")
		return nil
	})
	h := handlers.NewUserHandler(userStoreWithABS{mockStore, rec})
	c, w := newAuthCtx(http.MethodPost, "/api/v1/users/u-123/deactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.DeactivateUser(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{"u-123"}, rec.calls)
	require.Equal(t, []string{"update", "revoke"}, events, "lock first so the account is shut even if the revoke fails")
}

func TestDeactivateUser_RevokeFailureIsReported(t *testing.T) {
	mockStore := handlersmocks.NewMockUserStore(t)
	rec := &absRevokeRecorder{err: errors.New("pebble: partial revoke")}
	mockStore.EXPECT().GetUserByID("u-123").Return(sampleUser(), nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).Return(nil)
	h := handlers.NewUserHandler(userStoreWithABS{mockStore, rec})
	c, w := newAuthCtx(http.MethodPost, "/api/v1/users/u-123/deactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.DeactivateUser(c)
	require.Equal(t, http.StatusInternalServerError, w.Code, "a partial revoke must not report success")
}

// Reactivation revokes BEFORE unlocking and stays locked if that fails, so a
// session a failed deactivation-revoke left on disk cannot come back.
func TestReactivateUser_RevokesBeforeUnlocking(t *testing.T) {
	var events []string
	mockStore := handlersmocks.NewMockUserStore(t)
	rec := &absRevokeRecorder{events: &events}
	locked := sampleUser()
	locked.Status = "locked"
	mockStore.EXPECT().GetUserByID("u-123").Return(locked, nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).RunAndReturn(func(*database.User) error {
		events = append(events, "update")
		return nil
	})
	h := handlers.NewUserHandler(userStoreWithABS{mockStore, rec})
	c, w := newAuthCtx(http.MethodPost, "/api/v1/users/u-123/reactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.ReactivateUser(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{"revoke", "update"}, events)

	failing := handlersmocks.NewMockUserStore(t)
	locked2 := sampleUser()
	locked2.Status = "locked"
	failing.EXPECT().GetUserByID("u-123").Return(locked2, nil)
	h = handlers.NewUserHandler(userStoreWithABS{failing, &absRevokeRecorder{err: errors.New("boom")}})
	c, w = newAuthCtx(http.MethodPost, "/api/v1/users/u-123/reactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.ReactivateUser(c)
	require.Equal(t, http.StatusInternalServerError, w.Code, "a failed revoke must leave the user locked (no UpdateUser)")
}
