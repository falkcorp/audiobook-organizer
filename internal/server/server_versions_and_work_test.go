// file: internal/server/server_versions_and_work_test.go
// version: 1.1.1
// guid: 3a4b5c6d-7e8f-9012-a345-678901234567
// last-edited: 2026-04-30

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestVersionEndpoints_HappyPaths(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	book1, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "One", FilePath: "/tmp/one.m4b", Format: "m4b"})
	require.NoError(t, err)
	book2, err := database.GetGlobalStore().CreateBook(&database.Book{Title: "Two", FilePath: "/tmp/two.m4b", Format: "m4b"})
	require.NoError(t, err)

	// list versions when no group
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/"+book1.ID+"/versions", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// link the two books (creates group)
	body := bytes.NewBufferString(`{"other_id":"` + book2.ID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book1.ID+"/versions", body)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var linkResp struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &linkResp))
	groupID := linkResp.Data["version_group_id"]
	require.NotEmpty(t, groupID)

	// getVersionGroup
	req = httptest.NewRequest(http.MethodGet, "/api/v1/version-groups/"+groupID, nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// set primary (book1)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/audiobooks/"+book1.ID+"/set-primary", nil)
	w = httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	updated1, err := database.GetGlobalStore().GetBookByID(book1.ID)
	require.NoError(t, err)
	updated2, err := database.GetGlobalStore().GetBookByID(book2.ID)
	require.NoError(t, err)
	require.NotNil(t, updated1.VersionGroupID)
	require.NotNil(t, updated2.VersionGroupID)
	require.NotNil(t, updated1.IsPrimaryVersion)
	require.NotNil(t, updated2.IsPrimaryVersion)
	assert.True(t, *updated1.IsPrimaryVersion)
	assert.False(t, *updated2.IsPrimaryVersion)
}

func TestWorkEndpoints_WithMockStore(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	store.EXPECT().SetRootDir(mock.Anything).Return()
	author1 := 1
	author2 := 2
	works := []database.Work{{ID: "w1", Title: "Work One", AuthorID: &author1}, {ID: "w2", Title: "Work Two", AuthorID: &author2}}
	store.EXPECT().GetAllWorks().Return(works, nil)
	store.EXPECT().GetAllWorkBookCounts().Return(map[string]int{"w1": 2, "w2": 1}, nil)
	store.EXPECT().GetBooksByWorkID("w1").Return([]database.Book{{ID: "b1"}, {ID: "b2"}}, nil)
	store.EXPECT().GetBooksByWorkID("w2").Return(nil, assert.AnError)

	server, cleanup := setupTestServerWithStore(t, store)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/work", nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	store2 := dbmocks.NewMockStore(t)
	store2.EXPECT().SetRootDir(mock.Anything).Return()
	store2.EXPECT().GetAllWorks().Return(works, nil)
	store2.EXPECT().GetAllWorkBookCounts().Return(map[string]int{"w1": 2}, nil)
	server2, cleanup2 := setupTestServerWithStore(t, store2)
	defer cleanup2()

	req = httptest.NewRequest(http.MethodGet, "/api/v1/work/stats", nil)
	w = httptest.NewRecorder()
	server2.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var statsWrapper struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &statsWrapper))
	statsResp := statsWrapper.Data
	assert.Equal(t, float64(2), statsResp["total_books"])
	assert.Equal(t, float64(2), statsResp["total_works"])
	assert.Equal(t, float64(1), statsResp["works_with_multiple_editions"])
}
