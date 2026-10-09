// file: internal/server/handlers/operations/pause_test.go
// version: 1.0.0
// guid: 94ae9a81-a763-45fb-b24a-f9ede23037aa
// last-edited: 2026-10-09

package operations_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type pauseStateBody struct {
	Data struct {
		RunningPausable    []string `json:"running_pausable"`
		RunningNotPausable []string `json:"running_not_pausable"`
		Note               string   `json:"note"`
	} `json:"data"`
}

// GET /operations/pause reads running ops from the v2 table. Until 2026-10-09
// it read the v1 keyspace, which has had no writer since 2026-08-23, so both
// lists were always empty. The terminal rows are the over-suppression check:
// a filter that let everything through would list them too.
func TestGetPauseState_ListsRunningV2Ops(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	rows := []database.OperationV2Row{
		{ID: "op-1", DefID: "dedup.full-scan", Status: "running"},
		{ID: "op-2", DefID: "library.scan", Status: "running"},
		{ID: "op-3", DefID: "acoustid.backfill", Status: "completed"},
		{ID: "op-4", DefID: "metadata.batch-apply-cached", Status: "interrupted_dropped"},
		{ID: "op-5", DefID: "example.not-pausable", Status: "failed"},
	}
	// ListRecentOperationsV2 may call this more than once (window doubling).
	store.EXPECT().ListOperationsV2Since(mock.Anything, mock.Anything).Return(rows, nil).Maybe()

	w := run(http.MethodGet, "/operations/pause", "/operations/pause", nil, func(r *gin.Engine) {
		r.GET("/operations/pause", h.GetPauseState)
	})
	require.Equal(t, http.StatusOK, w.Code)

	var body pauseStateBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, []string{"dedup.full-scan"}, body.Data.RunningPausable)
	assert.Equal(t, []string{"library.scan"}, body.Data.RunningNotPausable)
	assert.NotEmpty(t, body.Data.Note, "a running op that cannot park must be explained")
}

func TestGetPauseState_NoRunningOps(t *testing.T) {
	h, store, _, _, _, _ := newTestHandler(t)
	store.EXPECT().ListOperationsV2Since(mock.Anything, mock.Anything).Return([]database.OperationV2Row{
		{ID: "op-1", DefID: "dedup.full-scan", Status: "cancelled"},
	}, nil).Maybe()

	w := run(http.MethodGet, "/operations/pause", "/operations/pause", nil, func(r *gin.Engine) {
		r.GET("/operations/pause", h.GetPauseState)
	})
	require.Equal(t, http.StatusOK, w.Code)

	var body pauseStateBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Empty(t, body.Data.RunningPausable)
	assert.Empty(t, body.Data.RunningNotPausable)
	assert.Empty(t, body.Data.Note)
}
