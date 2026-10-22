// file: internal/server/purge_soft_deleted_api_helpers_test.go
// version: 1.0.0
// guid: 3b0f6d1e-8c52-4a47-9e0b-5d7a41c92f66
// last-edited: 2026-10-22

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// purgeViaAPI calls a purge-soft-deleted endpoint, requires the 202 + op id the
// endpoint now answers (it used to purge inside the request and answer 200),
// waits for the operation, and returns the result it stored. The endpoint is
// "DELETE .../purge-soft-deleted" for a real purge and
// "POST .../purge-soft-deleted/preview" for a dry run.
func purgeViaAPI(t *testing.T, server *Server, method, target string) audiobooks.PurgeResult {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.NotEmpty(t, env.Data.ID, "the purge endpoint must answer with the operation id")

	done := waitForOperationStatus(t, env.Data.ID, 30*time.Second)
	require.Equal(t, "completed", done.Status)

	row, err := database.GetGlobalStore().GetOperationV2(env.Data.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.ResultData, "the op must store its result for the UI")
	var res audiobooks.PurgeResult
	require.NoError(t, json.Unmarshal([]byte(*row.ResultData), &res))
	return res
}
