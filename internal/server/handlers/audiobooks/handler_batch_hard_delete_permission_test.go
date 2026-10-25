// file: internal/server/handlers/audiobooks/handler_batch_hard_delete_permission_test.go
// version: 1.0.0
// guid: e8602b33-8ac7-4a49-ab67-bc5184d0e660
// last-edited: 2026-10-25

package audiobookshandler_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
)

// A batch hard delete needs library.delete, checked by the handler on the
// request it bound. An unwired handler uses the strict check, so with no user
// on the request it answers 401 and the batch service is never called
// (mockery fails the test on an unexpected call); a handler wired with a check
// that grants it reaches the service; a list with no hard delete never asks.
func TestBatchOperations_HardDeleteAsksForLibraryDelete(t *testing.T) {
	hard := map[string]any{"operations": []map[string]any{{"id": "b1", "action": "delete", "hard_delete": true}}}
	soft := map[string]any{"operations": []map[string]any{{"id": "b1", "action": "delete"}}}

	t.Run("unwired handler fails closed", func(t *testing.T) {
		h, _ := newHandler(t)
		c, w := newCtx("POST", "/audiobooks/batch-operations", hard, nil)
		h.BatchOperations(c)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("a check that refuses stops it, naming the permission it was asked for", func(t *testing.T) {
		h, _ := newHandler(t)
		var asked auth.Permission
		h.SetPermissionCheck(func(c *gin.Context, p auth.Permission) bool {
			asked = p
			c.AbortWithStatus(http.StatusForbidden)
			return false
		})
		c, w := newCtx("POST", "/audiobooks/batch-operations", hard, nil)
		h.BatchOperations(c)
		if w.Code != http.StatusForbidden || asked != auth.PermLibraryDelete {
			t.Fatalf("want 403 asking library.delete, got %d asking %q", w.Code, asked)
		}
	})
	t.Run("a check that grants it reaches the service", func(t *testing.T) {
		h, d := newHandler(t)
		h.SetPermissionCheck(func(*gin.Context, auth.Permission) bool { return true })
		d.batchSvc.EXPECT().ExecuteOperations(mock.Anything).Return(&batch.BatchResponse{}).Once()
		c, w := newCtx("POST", "/audiobooks/batch-operations", hard, nil)
		h.BatchOperations(c)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("no hard delete never asks", func(t *testing.T) {
		h, d := newHandler(t)
		h.SetPermissionCheck(func(*gin.Context, auth.Permission) bool {
			t.Error("the permission check must not run without a hard delete")
			return false
		})
		d.batchSvc.EXPECT().ExecuteOperations(mock.Anything).Return(&batch.BatchResponse{}).Once()
		c, w := newCtx("POST", "/audiobooks/batch-operations", soft, nil)
		h.BatchOperations(c)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
	})
}
