// file: internal/server/handlers/audiobooks/handler_batch_delete_events_test.go
// version: 1.0.0
// guid: 35f9b812-6b97-4f1b-a43a-63f30d01e7df
// last-edited: 2026-10-29

package audiobookshandler_test

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/batch"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
)

// A hard delete the bulk endpoint carried out ends like the single-book one:
// book.deleted is published for it. Only for the hard deletes that succeeded:
// a refused one, a soft delete (the bulk path has never announced those) and
// an update announce nothing.
func TestBatchOperations_PublishesBookDeletedForEachSuccessfulHardDelete(t *testing.T) {
	h, d := newHandler(t)
	h.SetPermissionCheck(func(*gin.Context, auth.Permission) bool { return true })
	d.batchSvc.EXPECT().ExecuteOperations(mock.Anything).Return(&batch.BatchResponse{
		Results: []batch.BatchResult{
			{ID: "bane", Success: true},
			{ID: "ra-s-al-ghul", Error: "hard delete ra-s-al-ghul: book still owns book_file rows"},
			{ID: "two-face", Success: true},
			{ID: "scarecrow", Success: true},
			{ID: "bane", Success: true},
		},
		Total: 5,
	})
	c, w := newCtx("POST", "/audiobooks/batch-operations", opsBody(
		map[string]any{"id": "bane", "action": "delete", "hard_delete": true},
		map[string]any{"id": "ra-s-al-ghul", "action": "delete", "hard_delete": true},
		map[string]any{"id": "two-face", "action": "delete"},
		map[string]any{"id": "scarecrow", "action": "update", "updates": map[string]any{"title": "Fear"}},
		map[string]any{"id": "bane", "action": "delete", "hard_delete": true},
	), nil)

	h.BatchOperations(c)

	require.Equal(t, 200, w.Code, w.Body.String())
	var got []string
	for _, ev := range d.rec.publishedEvents {
		require.Equal(t, plugin.EventBookDeleted, ev.Type)
		got = append(got, ev.BookID)
		require.Equal(t, false, ev.Data["soft_delete"])
	}
	require.Equal(t, []string{"bane", "bane"}, got,
		"one event per successful hard delete, none for the refused one, the soft delete or the update")
}

// A response that does not line up with the request (it should never happen)
// announces nothing rather than guessing which result belongs to which row.
func TestBatchOperations_MisalignedResultsPublishNothing(t *testing.T) {
	h, d := newHandler(t)
	h.SetPermissionCheck(func(*gin.Context, auth.Permission) bool { return true })
	d.batchSvc.EXPECT().ExecuteOperations(mock.Anything).Return(&batch.BatchResponse{
		Results: []batch.BatchResult{{ID: "bane", Success: true}},
		Total:   2,
	})
	c, w := newCtx("POST", "/audiobooks/batch-operations", opsBody(
		map[string]any{"id": "bane", "action": "delete", "hard_delete": true},
		map[string]any{"id": "two-face", "action": "delete", "hard_delete": true},
	), nil)

	h.BatchOperations(c)

	require.Equal(t, 200, w.Code, w.Body.String())
	require.Empty(t, d.rec.publishedEvents)
}
