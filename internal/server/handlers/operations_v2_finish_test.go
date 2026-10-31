// file: internal/server/handlers/operations_v2_finish_test.go
// version: 1.0.0
// guid: 0d8f3b46-2a79-4c15-b6e8-91f4c7a53e20
// last-edited: 2026-10-30

// Covers POST /operations/v2/:id/finish ("Finish without resuming", the third
// decision next to Retry and Discard) and the can_finish flag the Activity page
// shows its button from.

package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func finishParams(id string) gin.Params { return gin.Params{{Key: "id", Value: id}} }

func TestFinishOperationV2_NilRegistry(t *testing.T) {
	h := handlers.NewOperationsV2Handler(nil, nil, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2/op1/finish", "", finishParams("op1"))
	h.FinishOperationV2(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestFinishOperationV2_UnknownID_Returns404(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().GetOperationV2("bad-id").Return(nil, nil)
	// No FinishWithoutResuming expectation: mockery fails the test if the
	// handler asks the registry to finish a row it could not load.
	registry := handlersmocks.NewMockOperationsRegistry(t)

	h := handlers.NewOperationsV2Handler(store, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2/bad-id/finish", "", finishParams("bad-id"))
	h.FinishOperationV2(c)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Only a row waiting for a decision can take it. Every other status, the other
// interrupted ones included, is a 409 that never reaches the registry.
func TestFinishOperationV2_NotAwaitingDecision_Returns409(t *testing.T) {
	for _, status := range []string{"queued", "running", "waiting_deps", "completed", "failed", "canceled",
		"interrupted", "interrupted_quiesced", "interrupted_dropped", "interrupted_restart"} {
		t.Run(status, func(t *testing.T) {
			store := databasemocks.NewMockOpsV2Store(t)
			store.EXPECT().GetOperationV2("op-x").Return(&database.OperationV2Row{
				ID: "op-x", DefID: "dedup.full-scan", Status: status, Params: "{}",
			}, nil)
			registry := handlersmocks.NewMockOperationsRegistry(t)

			h := handlers.NewOperationsV2Handler(store, registry, nil, false)
			c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2/op-x/finish", "", finishParams("op-x"))
			h.FinishOperationV2(c)
			require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		})
	}
}

// The decision re-queues the SAME row: the response names the same id, status
// queued and mode finish_only, and the actor reaches the registry.
func TestFinishOperationV2_AwaitingDecision_QueuesInPlace(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().GetOperationV2("op-old").Return(&database.OperationV2Row{
		ID: "op-old", DefID: "dedup.full-scan", Status: "interrupted_ask", Params: "{}",
	}, nil)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().FinishWithoutResuming(mock.Anything, "op-old", mock.Anything).Return(nil).Once()

	h := handlers.NewOperationsV2Handler(store, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2/op-old/finish", "", finishParams("op-old"))
	h.FinishOperationV2(c)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var resp struct {
		Data struct {
			ID     string `json:"id"`
			DefID  string `json:"def_id"`
			Status string `json:"status"`
			Mode   string `json:"mode"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "op-old", resp.Data.ID, "the decision re-queues the run under its own id")
	assert.Equal(t, "dedup.full-scan", resp.Data.DefID)
	assert.Equal(t, "queued", resp.Data.Status)
	assert.Equal(t, "finish_only", resp.Data.Mode)
}

// The registry's refusals keep their status: 404 for a row that vanished, 409
// with the reason for a live handle or a def that cannot take the decision, 500
// for anything else.
func TestFinishOperationV2_RegistryErrorsMapToStatuses(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"not found":      {opsregistry.ErrOpNotFound, http.StatusNotFound},
		"active":         {fmt.Errorf("%w: op op-old still has a live run handle", opsregistry.ErrOpActive), http.StatusConflict},
		"not finishable": {fmt.Errorf("%w: op op-old is running, not awaiting a decision", opsregistry.ErrOpNotFinishable), http.StatusConflict},
		"other":          {assert.AnError, http.StatusInternalServerError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := databasemocks.NewMockOpsV2Store(t)
			store.EXPECT().GetOperationV2("op-old").Return(&database.OperationV2Row{
				ID: "op-old", DefID: "dedup.full-scan", Status: "interrupted_ask",
			}, nil)
			registry := handlersmocks.NewMockOperationsRegistry(t)
			registry.EXPECT().FinishWithoutResuming(mock.Anything, "op-old", mock.Anything).Return(tc.err).Once()

			h := handlers.NewOperationsV2Handler(store, registry, nil, false)
			c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2/op-old/finish", "", finishParams("op-old"))
			h.FinishOperationV2(c)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want == http.StatusConflict {
				assert.Contains(t, w.Body.String(), tc.err.Error())
			}
		})
	}
}

// Finish shares defPermissionsHeld with Retry: a def that declares nothing
// needs settings.manage, and a caller without it is refused before the registry
// is asked.
func TestFinishOperationV2_NoPermissionsDefRequiresSettingsManage(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	store.EXPECT().GetOperationV2("op-old").Return(&database.OperationV2Row{
		ID: "op-old", DefID: "dedup.full-scan", Status: "interrupted_ask", Params: "{}",
	}, nil)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	registry.EXPECT().Def("dedup.full-scan").Return(opsregistry.OperationDef{ID: "dedup.full-scan"}, true)
	// Deliberately NO FinishWithoutResuming expectation: reaching it is the failure.

	h := handlers.NewOperationsV2Handler(store, registry, nil, true)
	c, w := newOpsV2Ctx(http.MethodPost, "/operations/v2/op-old/finish", "", finishParams("op-old"))
	withCallerPerms(c, auth.PermScanTrigger)
	h.FinishOperationV2(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "permission denied: settings.manage")
	registry.AssertNotCalled(t, "FinishWithoutResuming", mock.Anything, mock.Anything, mock.Anything)
}

// can_finish is true only on an interrupted_ask row whose def can take the
// decision; the timeline never asks the registry about any other status.
func TestGetOperationTimeline_CanFinishOnlyOnAwaitingDecisionRowsOfFinishableDefs(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	store.EXPECT().ListOperationsV2Since(mock.Anything, 5000).Return([]database.OperationV2Row{
		{ID: "ask-native", DefID: "dedup.full-scan", Status: "interrupted_ask"},
		{ID: "ask-v2", DefID: "library.scan", Status: "interrupted_ask"},
		{ID: "quiesced-native", DefID: "dedup.full-scan", Status: "interrupted_quiesced"},
		{ID: "done-native", DefID: "dedup.full-scan", Status: "completed"},
	}, nil)
	expectRegisteredDefs(registry, []opsregistry.OperationDef{
		{ID: "dedup.full-scan", DisplayName: "Dedup full scan"},
		{ID: "library.scan", DisplayName: "Library Scan"},
	})
	registry.EXPECT().CanFinishWithoutResuming("dedup.full-scan").Return(true).Once()
	registry.EXPECT().CanFinishWithoutResuming("library.scan").Return(false).Once()

	h := handlers.NewOperationsV2Handler(store, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/timeline?since=15m", "", nil)
	h.GetOperationTimeline(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data struct {
			Operations []map[string]any `json:"operations"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	got := map[string]bool{}
	for _, op := range resp.Data.Operations {
		v, _ := op["can_finish"].(bool)
		got[op["id"].(string)] = v
	}
	assert.Equal(t, map[string]bool{
		"ask-native": true, "ask-v2": false, "quiesced-native": false, "done-native": false,
	}, got)
}

func TestGetOperationV2_CanFinishOnAwaitingDecisionRow(t *testing.T) {
	store := databasemocks.NewMockOpsV2Store(t)
	registry := handlersmocks.NewMockOperationsRegistry(t)
	store.EXPECT().GetOperationV2("op1").Return(&database.OperationV2Row{ID: "op1", DefID: "dedup.full-scan", Status: "interrupted_ask"}, nil)
	store.EXPECT().GetOpLogsV2("op1", 50).Return(nil, nil)
	expectRegisteredDefs(registry, []opsregistry.OperationDef{{ID: "dedup.full-scan", DisplayName: "Dedup full scan"}})
	registry.EXPECT().CanFinishWithoutResuming("dedup.full-scan").Return(true).Once()

	h := handlers.NewOperationsV2Handler(store, registry, nil, false)
	c, w := newOpsV2Ctx(http.MethodGet, "/operations/v2/op1", "", finishParams("op1"))
	h.GetOperationV2(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"can_finish":true`)
}
