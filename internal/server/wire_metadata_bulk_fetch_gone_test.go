// file: internal/server/wire_metadata_bulk_fetch_gone_test.go
// version: 1.0.1
// guid: e3d3179f-4937-488d-9b70-a58146a3b10f
// last-edited: 2026-10-31

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const bulkFetchPath = "/api/v1/metadata/bulk-fetch"

// POST /metadata/bulk-fetch applied each book's top search candidate with no
// applygate, score floor, sequence guard or identity check (TODO
// BULK-FETCH-NO-GATE). It is retired, not gated: nothing in the web client
// called it, and the gated path is the library.bulk-metadata-fetch operation
// (which caches candidates) followed by metadata.batch-apply-cached (which
// runs every cached candidate through applygate).

// TestBulkFetchRoute_Answers410AndWritesNothing drives the retired route as an
// admin with a book that the old handler would have updated: 410 with the
// shared gone() shape, a message naming both replacement ops, and the book
// untouched.
func TestBulkFetchRoute_Answers410AndWritesNothing(t *testing.T) {
	f := setupCredGuardServer(t)
	book, err := f.store.CreateBook(&database.Book{Title: "Year One", Format: "m4b", FilePath: t.TempDir() + "/year-one.m4b"})
	require.NoError(t, err)

	w := f.do(t, f.sessionToken, http.MethodPost, bulkFetchPath,
		map[string]any{"book_ids": []string{book.ID}, "only_missing": false})
	require.Equal(t, http.StatusGone, w.Code, w.Body.String())

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	assert.Equal(t, "gone", body.Error)
	for _, want := range []string{"library.bulk-metadata-fetch", "metadata.batch-apply-cached"} {
		assert.True(t, strings.Contains(body.Message, want), "message %q must name %s", body.Message, want)
	}

	got, err := f.store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Year One", got.Title)
	assert.Nil(t, got.Publisher, "the retired route must not write a candidate")
}

// TestBulkFetchRoute_AuthRunsFirst: the stub is on the protected group, so a
// caller with no credentials gets 401 and never learns the route is gone.
func TestBulkFetchRoute_AuthRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	req := httptest.NewRequest(http.MethodPost, bulkFetchPath, nil)
	w := httptest.NewRecorder()
	f.srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}

// TestBulkFetchRoute_PermissionGuardRunsFirst: the stub keeps the old
// library.edit_metadata guard, so a read-only viewer gets 403.
func TestBulkFetchRoute_PermissionGuardRunsFirst(t *testing.T) {
	f := setupCredGuardServer(t)
	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	sess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword, 0)
	require.NoError(t, err)

	w := f.do(t, sess.ID, http.MethodPost, bulkFetchPath, map[string]any{"book_ids": []string{"b1"}})
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"error":"gone"`)
}
