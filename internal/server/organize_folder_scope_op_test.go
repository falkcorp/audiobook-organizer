// file: internal/server/organize_folder_scope_op_test.go
// version: 1.0.0
// guid: 68a4ccb3-bf07-4543-afe9-e41ff5d49960
// last-edited: 2026-10-10

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/require"
)

// S2 (PR #3937 review round 1): an explicit empty book_ids means "these zero
// books" and organizes nothing, but `omitempty` dropped an empty slice when a
// typed Go caller marshalled these params for EnqueueOp. It arrived as an
// absent key, decoded to nil, and nil means the whole library. `omitzero`
// keeps [] and still omits nil, so the bytes today's callers produce (which
// the ConcurrencyKey dedupe byte-compares) do not change.
func TestLibraryOrganizeParams_EmptyBookIDsSurviveTheRoundTrip(t *testing.T) {
	raw, err := json.Marshal(libraryOrganizeParams{BookIDs: []string{}})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"book_ids":[]`)

	var back libraryOrganizeParams
	require.NoError(t, json.Unmarshal(raw, &back))
	require.NotNil(t, back.BookIDs, "an explicit empty scope must not decode as 'no scope'")
	require.Empty(t, back.BookIDs)

	raw, err = json.Marshal(libraryOrganizeParams{})
	require.NoError(t, err)
	require.Equal(t, `{"fetch_metadata_first":false}`, string(raw),
		"a nil book_ids must stay omitted: these bytes are what the dedupe compares")
	back = libraryOrganizeParams{}
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Nil(t, back.BookIDs)
}

// N4 (PR #3937 review round 1): a folder_path the organizer cannot honor
// must fail the operation, recorded as failed, before any book is touched.
func TestLibraryOrganizeOp_UnusableFolderPathRecordsAFailedRun(t *testing.T) {
	env, cleanup := testutil.SetupIntegration(t)
	defer cleanup()

	srcPath := env.CopyFixture("test_sample.m4b", env.ImportDir, "Book.m4b")
	author, err := env.Store.CreateAuthor("Test Author")
	require.NoError(t, err)
	created, err := env.Store.CreateBook(&database.Book{
		Title: "Test Book", FilePath: srcPath, Format: "m4b", AuthorID: &author.ID,
	})
	require.NoError(t, err)
	require.NoError(t, env.Store.CreateBookFile(&database.BookFile{
		ID: "bf-scope-1", BookID: created.ID, FilePath: srcPath, Format: "m4b",
	}))

	server := newTestServer(t, env.Store)
	require.NotNil(t, server.opRegistry)
	server.opRegistry.Start(context.Background())
	// Registered after defer cleanup() so it runs first (LIFO): no pebble: closed panics.
	defer func() { _ = server.opRegistry.Shutdown(context.Background()) }()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/operations/v2",
		strings.NewReader(`{"def_id":"library.organize","params":{"folder_path":"relative/x"}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	opID, ok := resp["id"].(string)
	if !ok {
		opID, ok = resp["op_id"].(string)
	}
	require.True(t, ok, "response should contain id or op_id, got: %v", resp)
	testutil.WaitForOp(t, env.Store, opID, 15*time.Second)

	row, err := env.Store.GetOperationV2(opID)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "failed", row.Status, "a refused scope must be recorded as a failed run, not a success")
	require.NotNil(t, row.ErrorMessage)
	require.Contains(t, *row.ErrorMessage, "invalid folder_path scope")

	books, err := env.Store.GetAllBooksCore(100, 0)
	require.NoError(t, err)
	require.Len(t, books, 1, "nothing may be organized when the scope is refused")
	require.Equal(t, srcPath, books[0].FilePath)
}
