// file: internal/server/handlers/op_status_routes_def_test.go
// version: 1.1.0
// guid: 6a3e9d17-c258-4b04-8d71-f2b9a0c5e38d
// last-edited: 2026-10-26

package handlers_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
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

const ownRunMessage = "importing for user_id=01INVENTEDOWNRUN000000A"

// opAccessHolding is an op-text reader whose caller holds exactly perms.
func opAccessHolding(perms ...auth.Permission) *opaccess.Repairs {
	return opaccess.NewRepairs(nil, nil, func(_ *gin.Context, p auth.Permission) bool {
		for _, q := range perms {
			if p == q {
				return true
			}
		}
		return false
	})
}

// A run of its own def answers with its progress message read through the text
// rule: withheld when it names a user and the caller may not read users' data
// (and when no reader is wired), shown whole to users.manage.
func TestAIResults_ProgressMessageGoesThroughTheTextRule(t *testing.T) {
	newHandler := func(t *testing.T) *handlers.DiagnosticsHandler {
		store := databasemocks.NewMockStore(t)
		store.EXPECT().GetOperationV2("op-ai").Return(&database.OperationV2Row{
			ID: "op-ai", DefID: handlers.DiagnosticsAIDefID, Status: "running", ProgressMessage: ownRunMessage,
		}, nil)
		return handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	}
	read := func(h *handlers.DiagnosticsHandler) string {
		c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/op-ai", "",
			gin.Params{{Key: "operationId", Value: "op-ai"}})
		h.GetAIResults(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Body.String()
	}
	require.NotContains(t, read(newHandler(t)), "01INVENTEDOWNRUN000000A", "unwired: withheld")
	require.NotContains(t, read(newHandler(t).WithOpAccess(opAccessHolding(auth.PermSettingsManage))), "01INVENTEDOWNRUN000000A")
	require.Contains(t, read(newHandler(t).WithOpAccess(opAccessHolding(auth.PermUsersManage))), "01INVENTEDOWNRUN000000A", "users.manage reads it whole")
}

func TestITunesImportStatus_ProgressMessageGoesThroughTheTextRule(t *testing.T) {
	row := &database.OperationV2Row{ID: "op-it", DefID: handlers.ITunesImportDefID, Status: "running", ProgressMessage: ownRunMessage}
	newHandler := func(t *testing.T) *handlers.ITunesHandler {
		store := handlersmocks.NewMockITunesStore(t)
		store.EXPECT().GetOperationV2("op-it").Return(row, nil)
		imp := handlersmocks.NewMockITunesImporter(t)
		imp.EXPECT().GetStatus("op-it").Return(&itunesservice.ImportStatusSnapshot{}).Maybe()
		imp.EXPECT().GetStatusBulk([]string{"op-it"}).Return(nil).Maybe()
		return handlers.NewITunesHandler(enabledSvc(t), imp, nil, store)
	}
	single := func(h *handlers.ITunesHandler) string {
		c, w := newITunesCtx(http.MethodGet, "/itunes/import-status/op-it", "", gin.Params{{Key: "id", Value: "op-it"}})
		h.ImportStatus(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Body.String()
	}
	bulk := func(h *handlers.ITunesHandler) string {
		c, w := newITunesCtx(http.MethodPost, "/itunes/import-status/bulk", `{"ids":["op-it"]}`, nil)
		h.ImportStatusBulk(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Body.String()
	}
	for name, read := range map[string]func(*handlers.ITunesHandler) string{"single": single, "bulk": bulk} {
		require.NotContains(t, read(newHandler(t)), "01INVENTEDOWNRUN000000A", name+": unwired: withheld")
		require.NotContains(t, read(newHandler(t).WithOpAccess(opAccessHolding(auth.PermSettingsManage))), "01INVENTEDOWNRUN000000A", name)
		require.Contains(t, read(newHandler(t).WithOpAccess(opAccessHolding(auth.PermUsersManage))), "01INVENTEDOWNRUN000000A", name+": users.manage reads it whole")
	}
}

// A store failure while the operation is read is a 500, not "no such analysis":
// the caller must not be told their run never existed, and the failure is logged.
// A missing row (or another def's) stays a 404.
func TestAIRoutes_AStoreFailureIsA500AndAMissingRowA404(t *testing.T) {
	boom := errors.New("pebble: batcave read failed")
	run := func(t *testing.T, row *database.OperationV2Row, err error, call func(*handlers.DiagnosticsHandler) int) int {
		store := databasemocks.NewMockStore(t)
		store.EXPECT().GetOperationV2("op-ai").Return(row, err)
		return call(handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil))
	}
	results := func(h *handlers.DiagnosticsHandler) int {
		c, w := newDiagCtx(http.MethodGet, "/diagnostics/ai-results/op-ai", "", gin.Params{{Key: "operationId", Value: "op-ai"}})
		h.GetAIResults(c)
		return w.Code
	}
	apply := func(h *handlers.DiagnosticsHandler) int {
		c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
			`{"operation_id":"op-ai","approved_suggestion_ids":["s1"]}`, nil)
		h.ApplySuggestions(c)
		return w.Code
	}
	for name, call := range map[string]func(*handlers.DiagnosticsHandler) int{"ai-results": results, "apply-suggestions": apply} {
		require.Equal(t, http.StatusInternalServerError, run(t, nil, boom, call), name+": store error")
		require.Equal(t, http.StatusNotFound, run(t, nil, nil, call), name+": missing row")
	}
}
