// file: internal/server/repairs_apply_result_user_data_test.go
// version: 1.0.0
// guid: 3b7e1d95-8a42-4c60-9f18-e5d2a0c64b37
// last-edited: 2026-10-26

// A repairs.apply result records which user's grant an owner row ran under
// (owner_user_id, on the result and on each row) and the free text a fixer wrote
// when a row, its follow-up or the run failed. A caller who may read the run but
// not other users' data (no users.manage) is served the result without the owner
// and with that text withheld, by the result route and by the diagnostics export
// alike; users.manage reads it whole.

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

const (
	applyOwnerID  = "01INVENTEDAPPLYOWNER0000A"
	opApplyOwners = "op-apply-owner-rows"
)

func seedApplyWithOwnerRows(t *testing.T, f *repairsOpAccessFixture) {
	t.Helper()
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	result := `{"fixer_id":"` + f.defaultFixerID + `","plan_op_id":"01JPLANYEARONE0000000000A","requested":3,` +
		`"owner_rows":1,"owner_user_id":"` + applyOwnerID + `",` +
		`"follow_up_error":"could not queue for user_id=` + applyOwnerID + `",` +
		`"aborted":"stopped: scan of Year One failed",` +
		`"rows":[{"row_id":"b1","outcome":"applied","owner_user_id":"` + applyOwnerID + `"},` +
		`{"row_id":"b2","outcome":"failed","error":"merge failed: user_id=` + applyOwnerID + ` could not be moved"},` +
		`{"row_id":"b3","outcome":"failed","error":"disk full while writing Knightfall"}]}`
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: opApplyOwners, DefID: repairs.ApplyOpID, Plugin: "repairs", Status: "completed",
		Params: `{"fixer_id":"` + f.defaultFixerID + `"}`, ResultData: &result, QueuedAt: now, CompletedAt: &now,
	}))
}

// Not parallel: it swaps the process-wide config.
func TestRepairsApplyResult_ResultRouteWithholdsTheOwnerAndFixerText(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	seedApplyWithOwnerRows(t, f)
	path := "/api/v1/operations/" + opApplyOwners + "/result"

	w := getAs(f.srv, f.editor, path)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), applyOwnerID, "an editor reads the run, not the account its owner rows ran under")
	require.NotContains(t, w.Body.String(), "owner_user_id")
	require.Contains(t, w.Body.String(), activity.WithheldUserText)
	require.Contains(t, w.Body.String(), "disk full while writing Knightfall", "text that names no user stays")

	w = getAs(f.srv, f.admin, path)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), applyOwnerID, "users.manage reads the result whole")
	require.Contains(t, w.Body.String(), "owner_user_id")
}

// Not parallel: it swaps the process-wide config.
func TestRepairsApplyResult_DiagnosticsExportWithholdsTheOwnerAndFixerText(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	_ = opsManagerToken(t, f)
	seedApplyWithOwnerRows(t, f)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	mgr, err := store.GetUserByUsername("opsmgr")
	require.NoError(t, err)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	for id, actor := range map[string]string{"op-export-apply-mgr": mgr.ID, "op-export-apply-admin": admin.ID} {
		actor := actor
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
			Params: `{"category":"general"}`, ActorUserID: &actor, QueuedAt: now,
		}))
	}

	opsJSON, _ := exportFiles(t, f, "op-export-apply-mgr")
	require.Contains(t, opsJSON, opApplyOwners, "the run is in the manager's export: its fixer is one the manager may use")
	require.NotContains(t, opsJSON, applyOwnerID, "but not the account its owner rows ran under, nor any text naming it")
	require.Contains(t, opsJSON, activity.WithheldUserText)
	require.Contains(t, opsJSON, "disk full while writing Knightfall")

	opsJSON, _ = exportFiles(t, f, "op-export-apply-admin")
	require.Contains(t, opsJSON, applyOwnerID, "the admin's export carries the result whole")
}
