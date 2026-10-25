// file: internal/server/batch_hard_delete_guard_test.go
// version: 1.0.0
// guid: fddff3e6-0729-46d7-8cdc-1d8219e7ce01
// last-edited: 2026-10-25

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const batchOpsPath = "/api/v1/audiobooks/batch-operations"

// TestBatchOperations_HardDeleteNeedsLibraryDelete: a role holding
// library.edit_metadata but not library.delete (synthetic "metadata-only")
// may update and soft-delete through the batch route, and gets 403 naming
// library.delete the moment any operation is a hard delete, wherever it sits
// in the list. A role holding both (editor) passes the guard: its request
// reaches the handler. Not parallel: it swaps the process-wide config.
func TestBatchOperations_HardDeleteNeedsLibraryDelete(t *testing.T) {
	srv, _, adminToken := repairsPermServer(t, true, true)
	store := srv.storeForWiring()
	ps, ok := store.(*database.PebbleStore)
	require.True(t, ok, "test store is a PebbleStore")

	_, err := ps.CreateRole(&database.Role{
		ID: "metadata-only", Name: "metadata-only",
		Permissions: []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata},
	})
	require.NoError(t, err)
	u, err := ps.CreateUser("meta", "meta@example.test", "bcrypt", "hash", []string{"metadata-only"}, "active")
	require.NoError(t, err)
	sess, err := ps.CreateSession(u.ID, "127.0.0.1", "test", time.Hour)
	require.NoError(t, err)
	meta := sess.ID

	editorUser, err := ps.CreateUser("ed2", "ed2@example.test", "bcrypt", "hash", []string{auth.SeedRoleEditor}, "active")
	require.NoError(t, err)
	es, err := ps.CreateSession(editorUser.ID, "127.0.0.1", "test", time.Hour)
	require.NoError(t, err)
	editor := es.ID

	hard := `{"operations":[{"id":"no-such-book","action":"update","updates":{"title":"t"}},{"id":"no-such-book","action":"delete","hard_delete":true}]}`
	soft := `{"operations":[{"id":"no-such-book","action":"delete"},{"id":"no-such-book","action":"delete","hard_delete":false}]}`
	update := `{"operations":[{"id":"no-such-book","action":"update","updates":{"title":"t"}}]}`

	t.Run("metadata-only role: hard delete refused", func(t *testing.T) {
		w := postRepairs(srv, meta, batchOpsPath, hard)
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "permission denied: library.delete")
	})
	t.Run("metadata-only role: soft delete and update reach the handler", func(t *testing.T) {
		for _, body := range []string{soft, update} {
			w := postRepairs(srv, meta, batchOpsPath, body)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		}
	})
	t.Run("editor and admin: hard delete reaches the handler with its body intact", func(t *testing.T) {
		for _, tok := range []string{editor, adminToken} {
			w := postRepairs(srv, tok, batchOpsPath, hard)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "no-such-book", "the handler read the restored body")
		}
	})
	t.Run("unparseable body is the handler's 400, not a delete", func(t *testing.T) {
		w := postRepairs(srv, meta, batchOpsPath, "{bad")
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	})
}
