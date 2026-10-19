// file: internal/server/handlers/abs_session_revoke_test.go
// version: 1.1.0
// guid: f5a37469-0393-44b9-9335-7ab7e914098f
// last-edited: 2026-10-19

package handlers_test

import (
	"errors"
	"fmt"
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

// The password change and the CredentialEpoch bump land in one UpdateUser write;
// that bump is what ends every ABS session (including one minted with the old
// password while the change was in flight). The revoke-all runs after it as
// cleanup.
func TestChangePassword_BumpsEpochInTheSameWriteThenRevokes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		admin bool
	}{{name: "self"}, {name: "admin reset", admin: true}} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			mockStore := handlersmocks.NewMockAuthStore(t)
			rec := &absRevokeRecorder{events: &events}
			target := passwordUser(t, "u-target", "old-password")
			target.CredentialEpoch = 4
			oldHash := target.PasswordHash
			mockStore.EXPECT().GetUserByID("u-target").Return(target, nil)
			mockStore.EXPECT().UpdateUser(mock.Anything).RunAndReturn(func(u *database.User) error {
				require.NotEqual(t, oldHash, u.PasswordHash, "the write carries the new password")
				require.Equal(t, int64(5), u.CredentialEpoch, "and, in the same write, the next credential epoch")
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
			require.Equal(t, []string{"u-target"}, rec.calls, "every ABS session of the TARGET user is cleaned up")
			require.Equal(t, []string{"update", "revoke"}, events, "the cleanup revoke runs after the epoch write")
		})
	}
}

// The cleanup revoke cannot un-change a password that already landed with its
// epoch bump, and the sessions are already refused, so its failure is audited
// and the change is reported as done.
func TestChangePassword_RevokeFailureStillReportsTheChange(t *testing.T) {
	mockStore := handlersmocks.NewMockAuthStore(t)
	rec := &absRevokeRecorder{err: errors.New("pebble: partial revoke")}
	target := passwordUser(t, "u1", "old-password")
	mockStore.EXPECT().GetUserByID("u1").Return(target, nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).Return(nil)
	h := handlers.NewAuthHandler(authStoreWithABS{mockStore, rec}, true)

	c, w := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password",
		map[string]any{"current_password": "old-password", "new_password": "new-password-123"})
	setAuthUser(c, target)
	h.ChangePassword(c)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Equal(t, []string{"u1"}, rec.calls)
}

// A credential change that lost a race with another one is refused as a
// conflict, and nothing is revoked on its behalf.
func TestChangePassword_StaleUserIsConflict(t *testing.T) {
	mockStore := handlersmocks.NewMockAuthStore(t)
	rec := &absRevokeRecorder{}
	target := passwordUser(t, "u1", "old-password")
	mockStore.EXPECT().GetUserByID("u1").Return(target, nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).Return(fmt.Errorf("wrapped: %w", database.ErrUserStale))
	h := handlers.NewAuthHandler(authStoreWithABS{mockStore, rec}, true)

	c, w := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password",
		map[string]any{"current_password": "old-password", "new_password": "new-password-123"})
	setAuthUser(c, target)
	h.ChangePassword(c)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Empty(t, rec.calls)
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

func TestDeactivateUser_LocksAndBumpsEpochInOneWriteThenRevokes(t *testing.T) {
	var events []string
	mockStore := handlersmocks.NewMockUserStore(t)
	rec := &absRevokeRecorder{events: &events}
	user := sampleUser()
	user.CredentialEpoch = 2
	mockStore.EXPECT().GetUserByID("u-123").Return(user, nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).RunAndReturn(func(u *database.User) error {
		require.Equal(t, "locked", u.Status)
		require.Equal(t, int64(3), u.CredentialEpoch, "the lock and the epoch bump are one write")
		events = append(events, "update")
		return nil
	})
	h := handlers.NewUserHandler(userStoreWithABS{mockStore, rec})
	c, w := newAuthCtx(http.MethodPost, "/api/v1/users/u-123/deactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.DeactivateUser(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{"u-123"}, rec.calls)
	require.Equal(t, []string{"update", "revoke"}, events)
}

func TestDeactivateUser_RevokeFailureStillDeactivates(t *testing.T) {
	mockStore := handlersmocks.NewMockUserStore(t)
	rec := &absRevokeRecorder{err: errors.New("pebble: partial revoke")}
	mockStore.EXPECT().GetUserByID("u-123").Return(sampleUser(), nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).Return(nil)
	h := handlers.NewUserHandler(userStoreWithABS{mockStore, rec})
	c, w := newAuthCtx(http.MethodPost, "/api/v1/users/u-123/deactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.DeactivateUser(c)
	require.Equal(t, http.StatusOK, w.Code, "the account is locked and its sessions refused; the revoke is cleanup")
}

// Reactivation unlocks without moving the epoch back: the sessions the
// deactivation killed stay dead.
func TestReactivateUser_KeepsTheEpoch(t *testing.T) {
	mockStore := handlersmocks.NewMockUserStore(t)
	rec := &absRevokeRecorder{}
	locked := sampleUser()
	locked.Status = "locked"
	locked.CredentialEpoch = 3
	mockStore.EXPECT().GetUserByID("u-123").Return(locked, nil)
	mockStore.EXPECT().UpdateUser(mock.Anything).RunAndReturn(func(u *database.User) error {
		require.Equal(t, "active", u.Status)
		require.Equal(t, int64(3), u.CredentialEpoch)
		return nil
	})
	h := handlers.NewUserHandler(userStoreWithABS{mockStore, rec})
	c, w := newAuthCtx(http.MethodPost, "/api/v1/users/u-123/reactivate", nil)
	c.Params = gin.Params{{Key: "id", Value: "u-123"}}
	h.ReactivateUser(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{"u-123"}, rec.calls)
}
