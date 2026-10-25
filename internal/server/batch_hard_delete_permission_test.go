// file: internal/server/batch_hard_delete_permission_test.go
// version: 1.0.0
// guid: e0a974ef-0780-48c3-921a-e32b2e124b6c
// last-edited: 2026-10-25

package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const batchOpsPath = "/api/v1/audiobooks/batch-operations"

// TestBatchOperations_HardDeleteNeedsLibraryDelete: POST
// /audiobooks/batch-operations is guarded by library.edit_metadata, and a hard
// delete in the list also needs library.delete, as DELETE /audiobooks/:id
// does. The check is the handler's, made on the request it just bound and is
// about to run, so a body the handler reads as a hard delete is refused
// however the bytes are shaped. A role holding edit_metadata but not delete
// (synthetic "metadata-only") gets 403 naming library.delete and the book
// survives for: a plain hard delete, a hard delete after other operations,
// trailing bytes after the JSON value (the handler's decoder reads one value
// and ignores the rest), and a duplicate hard_delete key (the last wins). The
// same role may still update and soft-delete. Editor and admin (both hold
// library.delete) really hard delete a file-less book through the router, which
// also proves the wired store resolves the owned-files check. Not parallel: it
// swaps the process-wide config.
func TestBatchOperations_HardDeleteNeedsLibraryDelete(t *testing.T) {
	srv, _, adminToken := repairsPermServer(t, true, true)
	ps, ok := srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok, "test store is a PebbleStore")

	_, err := ps.CreateRole(&database.Role{
		ID: "metadata-only", Name: "metadata-only",
		Permissions: []auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata},
	})
	require.NoError(t, err)
	session := func(name, role string) string {
		u, err := ps.CreateUser(name, name+"@example.test", "bcrypt", "hash", []string{role}, "active")
		require.NoError(t, err)
		s, err := ps.CreateSession(u.ID, "127.0.0.1", "test", time.Hour)
		require.NoError(t, err)
		return s.ID
	}
	meta := session("meta", "metadata-only")
	editor := session("ed2", auth.SeedRoleEditor)

	newBook := func(title string) string {
		b, err := ps.CreateBook(&database.Book{Title: title, FilePath: "/library/" + title + ".m4b"})
		require.NoError(t, err)
		return b.ID
	}
	exists := func(id string) bool {
		b, err := ps.GetBookByID(id)
		return err == nil && b != nil
	}

	// Each shape runs on its own file-less book so a mutation shows which
	// shapes slip through.
	for _, tc := range []struct{ name, body string }{
		{"plain", `{"operations":[{"id":"%s","action":"delete","hard_delete":true}]}`},
		{"after update", `{"operations":[{"id":"%s","action":"update","updates":{"title":"t"}},{"id":"%s","action":"delete","hard_delete":true}]}`},
		{"trailing bytes", `{"operations":[{"id":"%s","action":"delete","hard_delete":true}]}x`},
		{"trailing value", `{"operations":[{"id":"%s","action":"delete","hard_delete":true}]}{}`},
		{"duplicate key", `{"operations":[{"id":"%s","action":"delete","hard_delete":false,"hard_delete":true}]}`},
	} {
		t.Run("metadata-only role refused: "+tc.name, func(t *testing.T) {
			id := newBook("Synthetic Refused " + tc.name)
			body := strings.ReplaceAll(tc.body, "%s", id)
			w := postRepairs(srv, meta, batchOpsPath, body)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "permission denied: library.delete")
			require.True(t, exists(id), "the book must survive")
		})
	}
	t.Run("metadata-only role: update and soft delete still reach the handler", func(t *testing.T) {
		id := newBook("Synthetic Soft")
		for _, body := range []string{
			`{"operations":[{"id":"` + id + `","action":"update","updates":{"title":"t2"}}]}`,
			`{"operations":[{"id":"` + id + `","action":"delete"},{"id":"` + id + `","action":"delete","hard_delete":false}]}`,
		} {
			w := postRepairs(srv, meta, batchOpsPath, body)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		}
		require.True(t, exists(id), "a soft delete keeps the row")
	})
	t.Run("editor and admin: a file-less book is really hard deleted", func(t *testing.T) {
		for _, tok := range []string{editor, adminToken} {
			id := newBook("Synthetic Deleted " + tok[:4])
			w := postRepairs(srv, tok, batchOpsPath, `{"operations":[{"id":"`+id+`","action":"delete","hard_delete":true}]}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.False(t, exists(id), "the book is gone: %s", w.Body.String())
		}
	})
}
