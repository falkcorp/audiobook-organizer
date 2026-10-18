// file: internal/server/collections_membership_route_authz_test.go
// version: 1.0.0
// guid: c4e7a912-3d58-4b0f-a6c1-95e2f08b7d13
// last-edited: 2026-10-18

// The add-books and remove-book routes on /api/v1/collections are registered
// behind collections.manage, like the other collection writes: a viewer session
// gets 403 and leaves the collection untouched, an admin session reaches the
// handler and the locked store operation. Driven through the real router with
// auth enabled, since a route registered without s.perm(...) is exactly what a
// handler-level test cannot see.

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionMembershipRoutes_Permissions(t *testing.T) {
	srv, adminToken, viewerToken, bookID, cleanup := setupUserTagsAuthzTestServer(t)
	defer cleanup()

	send := func(token, method, path string, body any) *httptest.ResponseRecorder {
		var buf []byte
		if body != nil {
			buf, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(buf))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) (id string, books []string) {
		var env struct {
			Data struct {
				ID      string   `json:"id"`
				BookIDs []string `json:"book_ids"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
		return env.Data.ID, env.Data.BookIDs
	}

	w := send(adminToken, http.MethodPost, "/api/v1/collections",
		map[string]any{"name": "Authz Shelf", "type": "static"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	colID, _ := decode(w)
	require.NotEmpty(t, colID)
	booksPath := "/api/v1/collections/" + colID + "/books"

	// A viewer is refused on both routes, and the collection is not touched.
	w = send(viewerToken, http.MethodPost, booksPath, map[string]any{"book_ids": []string{bookID}})
	require.Equal(t, http.StatusForbidden, w.Code, "viewer must be forbidden; body=%s", w.Body.String())
	w = send(adminToken, http.MethodGet, "/api/v1/collections/"+colID, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	_, books := decode(w)
	require.Empty(t, books, "a refused add reached the store")

	// The admin adds the real book through the production route; a repeat is
	// idempotent.
	for range 2 {
		w = send(adminToken, http.MethodPost, booksPath, map[string]any{"book_ids": []string{bookID}})
		require.Equal(t, http.StatusOK, w.Code, "admin must reach the handler; body=%s", w.Body.String())
		_, books = decode(w)
		assert.Equal(t, []string{bookID}, books)
	}

	// An unknown book is a 400 from the handler, which also proves the route is
	// registered (an unregistered one would be a bare 404).
	w = send(adminToken, http.MethodPost, booksPath, map[string]any{"book_ids": []string{"no-such-book"}})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// Remove: viewer refused, collection still holds the book; admin removes it.
	removePath := booksPath + "/" + bookID
	w = send(viewerToken, http.MethodDelete, removePath, nil)
	require.Equal(t, http.StatusForbidden, w.Code, "viewer must be forbidden; body=%s", w.Body.String())
	w = send(adminToken, http.MethodGet, "/api/v1/collections/"+colID, nil)
	_, books = decode(w)
	require.Equal(t, []string{bookID}, books, "a refused remove reached the store")

	w = send(adminToken, http.MethodDelete, removePath, nil)
	require.Equal(t, http.StatusOK, w.Code, "admin must reach the handler; body=%s", w.Body.String())
	_, books = decode(w)
	assert.Empty(t, books)

	// Removing a non-member is the handler's own 404, naming the member.
	w = send(adminToken, http.MethodDelete, removePath, nil)
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "collection member")
}
