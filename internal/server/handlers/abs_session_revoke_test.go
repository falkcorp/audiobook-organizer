// file: internal/server/handlers/abs_session_revoke_test.go
// version: 1.4.0
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

// credsFake is the database.UserCredentialWriter capability.
type credsFake struct {
	setPassword func(userID, expectedHash, algo, hash string) (*database.User, error)
	setStatus   func(userID, status string) (*database.User, error)
}

func (f *credsFake) SetUserPassword(userID, expectedHash, algo, hash string) (*database.User, error) {
	return f.setPassword(userID, expectedHash, algo, hash)
}

func (f *credsFake) SetUserStatus(userID, status string) (*database.User, error) {
	return f.setStatus(userID, status)
}

func (f *credsFake) BumpUserCredentialEpoch(string) (*database.User, error) {
	panic("not used by these handlers")
}

type authStoreWithABS struct {
	*handlersmocks.MockAuthStore
	*absRevokeRecorder
	*credsFake
}

type userStoreWithABS struct {
	*handlersmocks.MockUserStore
	*absRevokeRecorder
	*credsFake
}

// passwordSetter records SetUserPassword calls and answers with ret, err.
func passwordSetter(t *testing.T, wantID, wantExpected string, events *[]string, ret *database.User, err error) *credsFake {
	return &credsFake{setPassword: func(userID, expectedHash, algo, hash string) (*database.User, error) {
		require.Equal(t, wantID, userID)
		require.Equal(t, wantExpected, expectedHash, "self-service passes the verified hash, admin reset passes \"\"")
		require.Equal(t, "bcrypt", algo)
		require.NotEmpty(t, hash)
		if events != nil {
			*events = append(*events, "set-password")
		}
		return ret, err
	}}
}

func passwordUser(t *testing.T, id, password string) *database.User {
	t.Helper()
	return &database.User{ID: id, Username: id, Status: "active", PasswordHash: testBcryptHash(t, password), PasswordHashAlgo: "bcrypt"}
}

// The password change goes through SetUserPassword, which writes the hash and
// bumps CredentialEpoch on the fresh row; that bump is what ends every ABS
// session (including one minted with the old password while the change was in
// flight). A self-service change passes the hash it verified (a compare-and-
// swap); an admin reset passes "". The revoke-all runs after it as cleanup.
func TestChangePassword_GoesThroughSetUserPasswordThenRevokes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		admin bool
	}{{name: "self"}, {name: "admin reset", admin: true}} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			mockStore := handlersmocks.NewMockAuthStore(t)
			rec := &absRevokeRecorder{events: &events}
			target := passwordUser(t, "u-target", "old-password")
			oldHash := target.PasswordHash
			mockStore.EXPECT().GetUserByID("u-target").Return(target, nil)
			wantExpected := oldHash
			if tc.admin {
				wantExpected = ""
			}
			creds := passwordSetter(t, "u-target", wantExpected, &events, target, nil)
			h := handlers.NewAuthHandler(authStoreWithABS{mockStore, rec, creds}, true)

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
			require.Equal(t, []string{"set-password", "revoke"}, events, "the cleanup revoke runs after the epoch write")
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
	h := handlers.NewAuthHandler(authStoreWithABS{mockStore, rec, passwordSetter(t, "u1", target.PasswordHash, nil, target, nil)}, true)

	c, w := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password",
		map[string]any{"current_password": "old-password", "new_password": "new-password-123"})
	setAuthUser(c, target)
	h.ChangePassword(c)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Equal(t, []string{"u1"}, rec.calls)
}

// A self-service change whose verified password was replaced in the meantime is
// refused as a conflict, and nothing is revoked on its behalf.
func TestChangePassword_StalePasswordIsConflict(t *testing.T) {
	mockStore := handlersmocks.NewMockAuthStore(t)
	rec := &absRevokeRecorder{}
	target := passwordUser(t, "u1", "old-password")
	mockStore.EXPECT().GetUserByID("u1").Return(target, nil)
	h := handlers.NewAuthHandler(authStoreWithABS{mockStore, rec,
		passwordSetter(t, "u1", target.PasswordHash, nil, nil, fmt.Errorf("wrapped: %w", database.ErrUserStale))}, true)

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
	h := handlers.NewAuthHandler(struct {
		*handlersmocks.MockAuthStore
		*credsFake
	}{mockStore, passwordSetter(t, "u1", target.PasswordHash, nil, target, nil)}, true)

	c, w := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password",
		map[string]any{"current_password": "old-password", "new_password": "new-password-123"})
	setAuthUser(c, target)
	h.ChangePassword(c)
	require.Equal(t, http.StatusNoContent, w.Code)
}

// Without the credential capability there is no way to bump the epoch, so the
// change is refused rather than written through a whole-row UpdateUser.
func TestChangePassword_NoCredentialCapabilityRefuses(t *testing.T) {
	mockStore := handlersmocks.NewMockAuthStore(t)
	target := passwordUser(t, "u1", "old-password")
	mockStore.EXPECT().GetUserByID("u1").Return(target, nil)
	h := handlers.NewAuthHandler(mockStore, true)

	c, w := newAuthCtx(http.MethodPut, "/api/v1/auth/me/password",
		map[string]any{"current_password": "old-password", "new_password": "new-password-123"})
	setAuthUser(c, target)
	h.ChangePassword(c)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

// Deactivation and reactivation both go through SetUserStatus (status and epoch
// bump on the fresh row, one write), then revoke as cleanup; a revoke failure
// does not fail either.
func TestUserStatusChanges_GoThroughSetUserStatusThenRevoke(t *testing.T) {
	for _, tc := range []struct {
		action, status string
		revokeErr      error
	}{
		{"deactivate", "locked", nil},
		{"deactivate", "locked", errors.New("pebble: partial revoke")},
		{"reactivate", "active", nil},
		{"reactivate", "active", errors.New("pebble: partial revoke")},
	} {
		var events []string
		mockStore := handlersmocks.NewMockUserStore(t)
		rec := &absRevokeRecorder{events: &events, err: tc.revokeErr}
		mockStore.EXPECT().GetUserByID("u-123").Return(sampleUser(), nil)
		creds := &credsFake{setStatus: func(id, status string) (*database.User, error) {
			require.Equal(t, "u-123", id)
			require.Equal(t, tc.status, status)
			events = append(events, "set-status")
			u := sampleUser()
			u.Status = status
			return u, nil
		}}
		h := handlers.NewUserHandler(userStoreWithABS{mockStore, rec, creds})
		c, w := newAuthCtx(http.MethodPost, "/api/v1/users/u-123/"+tc.action, nil)
		c.Params = gin.Params{{Key: "id", Value: "u-123"}}
		if tc.action == "deactivate" {
			h.DeactivateUser(c)
		} else {
			h.ReactivateUser(c)
		}
		require.Equal(t, http.StatusOK, w.Code, "%s (revoke err %v): %s", tc.action, tc.revokeErr, w.Body.String())
		require.Equal(t, []string{"set-status", "revoke"}, events, tc.action)
	}
}

// A user deleted between the handler's read and the status write is a 404, not
// a 500.
func TestUserStatusChanges_DeletedUserIs404(t *testing.T) {
	for _, action := range []string{"deactivate", "reactivate"} {
		mockStore := handlersmocks.NewMockUserStore(t)
		mockStore.EXPECT().GetUserByID("u-123").Return(sampleUser(), nil)
		creds := &credsFake{setStatus: func(id, _ string) (*database.User, error) {
			return nil, fmt.Errorf("wrapped: %w", database.ErrUserNotFound)
		}}
		h := handlers.NewUserHandler(userStoreWithABS{mockStore, &absRevokeRecorder{}, creds})
		c, w := newAuthCtx(http.MethodPost, "/api/v1/users/u-123/"+action, nil)
		c.Params = gin.Params{{Key: "id", Value: "u-123"}}
		if action == "deactivate" {
			h.DeactivateUser(c)
		} else {
			h.ReactivateUser(c)
		}
		require.Equal(t, http.StatusNotFound, w.Code, "%s: %s", action, w.Body.String())
	}
}
