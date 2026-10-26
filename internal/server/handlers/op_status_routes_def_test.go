// file: internal/server/handlers/op_status_routes_def_test.go
// version: 1.0.0
// guid: 6a3e9d17-c258-4b04-8d71-f2b9a0c5e38d
// last-edited: 2026-10-26

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

const otherOpMessage = "moving state for user 01INVENTEDOTHEROP00000A"

// Each of these routes takes an operation id from the URL. Each answers only for
// a run of its own def: another operation's id (a Repairs run the caller may not
// read, whose progress message can name a user) is a 404 and nothing of its row
// reaches the body.
func TestAIResults_AnotherOperationsIdIsNotFound(t *testing.T) {
	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("op-other").Return(&database.OperationV2Row{
		ID: "op-other", DefID: "repairs.apply", Status: "running", ProgressMessage: otherOpMessage,
	}, nil)
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/op-other", "",
		gin.Params{{Key: "operationId", Value: "op-other"}})
	h.GetAIResults(c)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.NotContains(t, w.Body.String(), "01INVENTEDOTHEROP00000A")
}

func TestITunesImportStatus_AnotherOperationsIdIsNotFound(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().GetOperationV2("op-other").Return(&database.OperationV2Row{
		ID: "op-other", DefID: "repairs.apply", Status: "running", ProgressMessage: otherOpMessage,
	}, nil)
	imp := handlersmocks.NewMockITunesImporter(t)
	h := handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	c, w := newITunesCtx(http.MethodGet, "/itunes/import-status/op-other", "", gin.Params{{Key: "id", Value: "op-other"}})
	h.ImportStatus(c)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.NotContains(t, w.Body.String(), "01INVENTEDOTHEROP00000A")
}

func TestITunesImportStatusBulk_AnotherOperationsIdIsLeftOut(t *testing.T) {
	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().GetOperationV2("op-other").Return(&database.OperationV2Row{
		ID: "op-other", DefID: "repairs.apply", Status: "running", ProgressMessage: otherOpMessage,
	}, nil)
	imp := handlersmocks.NewMockITunesImporter(t)
	imp.EXPECT().GetStatusBulk([]string{"op-other"}).Return(nil)
	h := handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/import-status/bulk", `{"ids":["op-other"]}`, nil)
	h.ImportStatusBulk(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "op-other")
	require.NotContains(t, w.Body.String(), "01INVENTEDOTHEROP00000A")
}
