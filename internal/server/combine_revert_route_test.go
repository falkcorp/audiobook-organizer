// file: internal/server/combine_revert_route_test.go
// version: 1.0.0
// guid: 3c8a5f12-7e46-4d91-b0a3-9f2e6d17c548
// last-edited: 2026-10-18

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A manual combine (POST /api/v1/audiobooks/combine) runs in no operation, so
// it journals its undo pointer under a correlation id of its own and answers
// it as operation_id. POST /api/v1/operations/:id/revert with that id -- the
// live undo path once POST /merge/undo/:journal_id is retired -- puts every
// book, file row and external id back. Both requests go through the server's
// router.
func TestCombineRoute_UndoneByOperationsRevert(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	store := srv.store

	keep, a, b := ulid.Make().String(), ulid.Make().String(), ulid.Make().String()
	for id, title := range map[string]string{keep: "Keep", a: "Part A", b: "Part B"} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: title, Format: "mp3", FilePath: "/tmp/combine-route/" + id})
		require.NoError(t, err)
	}
	fileOwner := map[string]string{}
	for i, owner := range []string{keep, a, a, b} {
		id := ulid.Make().String()
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: id, BookID: owner, FilePath: fmt.Sprintf("/tmp/combine-route/%s/%02d.mp3", owner, i), Format: "mp3", TrackNumber: i + 1}))
		fileOwner[id] = owner
	}
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "PID-ROUTE", BookID: a}))

	owners := func() map[string]string {
		out := map[string]string{}
		for _, id := range []string{keep, a, b} {
			files, err := store.GetBookFiles(id)
			require.NoError(t, err)
			for _, f := range files {
				out[f.ID] = f.BookID
			}
		}
		return out
	}
	softDeleted := func(id string) bool {
		bk, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, bk)
		return bk.IsSoftDeleted()
	}
	post := func(path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			require.NoError(t, json.NewEncoder(&buf).Encode(body))
		}
		req := httptest.NewRequest(http.MethodPost, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		return w
	}

	w := post("/api/v1/audiobooks/combine", map[string]any{"keep_id": keep, "merge_ids": []string{a, b}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var combined struct {
		Data struct {
			OperationID string `json:"operation_id"`
			JournalID   string `json:"journal_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &combined))
	require.NotEmpty(t, combined.Data.OperationID, w.Body.String())
	require.NotEmpty(t, combined.Data.JournalID)
	require.True(t, softDeleted(a))
	require.True(t, softDeleted(b))
	for id, owner := range owners() {
		require.Equal(t, keep, owner, "file %s on keep after the combine", id)
	}

	w = post("/api/v1/operations/"+combined.Data.OperationID+"/revert", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.False(t, softDeleted(a), "a restored")
	require.False(t, softDeleted(b), "b restored")
	require.Equal(t, fileOwner, owners(), "every file row back on its book")
	pidOwner, err := store.GetBookByExternalID("itunes", "PID-ROUTE")
	require.NoError(t, err)
	require.Equal(t, a, pidOwner)
}
