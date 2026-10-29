// file: internal/server/handlers/operations/revert_actor_test.go
// version: 1.0.0
// guid: 1b6c8a4e-fe5c-436d-a9cc-dde22d9be34f
// last-edited: 2026-10-29

package operations_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations"
	operationsmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/operations/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// postRevertAs posts a revert of op-1 through a handler whose revert func
// records the actor it is handed. user is the signed-in user the auth
// middleware would have attached (nil: none).
func postRevertAs(t *testing.T, user *database.User) (audiobooks.RevertActor, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := operationsmocks.NewMockOperationsStore(t)
	allowRevertReads(store)
	var got audiobooks.RevertActor
	called := false
	h := operations.New(store, nil, nil, nil, nil, nil, nil,
		func(changes []*database.OperationChange) (*undo.UndoConflictReport, error) { return nil, nil },
		func(id string, actor audiobooks.RevertActor) (*audiobooks.RevertResult, error) {
			got, called = actor, true
			return &audiobooks.RevertResult{OperationID: id, Total: 1, Restored: 1}, nil
		},
	)
	w := run(http.MethodPost, "/operations/:id/revert", "/operations/op-1/revert", nil, func(r *gin.Engine) {
		r.Use(func(c *gin.Context) {
			if user != nil {
				c.Request = c.Request.WithContext(auth.WithUser(c.Request.Context(), user))
			}
			c.Next()
		})
		r.POST("/operations/:id/revert", h.RevertOperation)
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return got, called
}

// The revert is run for the user whose request asked for it, so the decision
// records it writes name them.
func TestRevertOperation_HandsTheSignedInUserToTheRevert(t *testing.T) {
	got, called := postRevertAs(t, &database.User{ID: "user-bruce", Username: "bruce.wayne"})
	require.True(t, called)
	assert.Equal(t, audiobooks.RevertActor{UserID: "user-bruce", Name: "bruce.wayne"}, got)
}

// A request with no user (auth off) is the zero actor, whose records read as
// automatic.
func TestRevertOperation_NoUserIsTheZeroActor(t *testing.T) {
	got, called := postRevertAs(t, nil)
	require.True(t, called)
	assert.Equal(t, audiobooks.RevertActor{}, got)
}
