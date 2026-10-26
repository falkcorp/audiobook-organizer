// file: internal/server/handlers/operations_v2_logs_user_data_test.go
// version: 1.0.0
// guid: 8d3f6a21-47c9-4b05-9e18-c2a7f0d5b364
// last-edited: 2026-10-26

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// GetOperationLogs (the system.OperationLogsProvider) drops the lines that name
// a user for a caller without users.manage, for any operation, and keeps them
// for one who holds it.
func TestOperationsV2Handler_GetOperationLogs_UserLinesNeedUsersManage(t *testing.T) {
	rows := []database.OpLogV2Row{
		{OperationID: "op1", Level: "info", Message: "repairs.apply x: owner apply by user 01INVENTEDOWNER0000000000A (cf-access): 1 of 1 owner row(s) written"},
		{OperationID: "op1", Level: "info", Message: "scanned Year One"},
	}
	run := func(holdsUsersManage bool) string {
		store := databasemocks.NewMockOpsV2Store(t)
		store.EXPECT().GetOperationV2("op1").Return(nil, nil)
		store.EXPECT().GetOpLogsV2("op1", 50).Return(rows, nil)
		access := opaccess.NewRepairs(nil, nil, func(_ *gin.Context, p auth.Permission) bool {
			return p != auth.PermUsersManage || holdsUsersManage
		})
		h := handlers.NewOperationsV2Handler(store, nil, nil, false, handlers.WithRepairsOpAccess(access))
		c, w := newOpsV2Ctx(http.MethodGet, "/system/logs", "", gin.Params{{Key: "id", Value: "op1"}})
		h.GetOperationLogs(c)
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	without := run(false)
	require.Contains(t, without, "scanned Year One")
	require.NotContains(t, without, "01INVENTEDOWNER0000000000A")

	with := run(true)
	require.Contains(t, with, "scanned Year One")
	require.Contains(t, with, "01INVENTEDOWNER0000000000A")
}
