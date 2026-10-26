// file: internal/server/diagnostics_export_isolation_test.go
// version: 1.0.0
// guid: 5d2c8e41-7a93-4f06-b1d8-0e6a9c3f7b52
// last-edited: 2026-10-26

// What one account's diagnostics export may share with another's: the run
// itself (it is built for, and narrowed to, the account that started it) and the
// credential scope recorded on the operations it lists.

package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Not parallel: it swaps the process-wide config.
//
// Two accounts starting an export with the same params while one is active each
// get their own run, which is built for and narrowed to its own requester; a
// repeat start by the same account joins its own run. Before the export's
// definition set DedupeSameActorOnly, the second caller was handed the first's
// run: an admin got a manager's narrowed archive, a manager a 403 on an admin's.
func TestDiagnosticsExportStart_EachAccountGetsItsOwnRun(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	mgrToken := opsManagerToken(t, f)
	store := f.srv.storeForWiring()
	require.NotNil(t, f.srv.opRegistry, "the fixture builds the operations registry")
	// The registry is not started: nothing is dispatched, so every run stays
	// queued and is still the active one the next start would be folded into.

	start := func(token string) string {
		t.Helper()
		w := postJSONAs(f.srv, token, "/api/v1/diagnostics/export", `{"category":"general"}`)
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		var m struct {
			Data struct {
				OperationID string `json:"operation_id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), w.Body.String())
		require.NotEmpty(t, m.Data.OperationID, w.Body.String())
		return m.Data.OperationID
	}
	actorOf := func(id string) string {
		t.Helper()
		row, err := store.GetOperationV2(id)
		require.NoError(t, err)
		require.NotNil(t, row)
		require.NotNil(t, row.ActorUserID)
		return *row.ActorUserID
	}
	mgr, err := store.GetUserByUsername("opsmgr")
	require.NoError(t, err)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)

	narrow := start(mgrToken)
	wide := start(f.admin)
	assert.NotEqual(t, narrow, wide, "the admin's export is not the manager's run")
	assert.Equal(t, mgr.ID, actorOf(narrow))
	assert.Equal(t, admin.ID, actorOf(wide), "the admin's run is built for the admin")

	assert.Equal(t, wide, start(f.admin), "the same account's repeat start joins its own run")
	assert.Equal(t, narrow, start(mgrToken), "and so does the manager's")
}

// Not parallel: it swaps the process-wide config.
//
// The export's operations.json lists each visible operation whole, and each
// carries ActorPermissions: the permission set the account that started it held.
// That is a credential scope, so a reader without users.manage gets it blanked;
// users.manage reads it.
func TestDiagnosticsExport_OperationsJSONBlanksActorPermissionsWithoutUsersManage(t *testing.T) {
	const (
		scanOp = "op-batcave-scan-actor-scope"
		scope  = "batcave.cowl.unlock"
	)
	f := newRepairsOpAccessFixture(t)
	_ = opsManagerToken(t, f)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	mgr, err := store.GetUserByUsername("opsmgr")
	require.NoError(t, err)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: scanOp, DefID: "library.scan", Plugin: "library", Status: "completed",
		Params: `{}`, ActorUserID: &admin.ID, ActorPermissions: []string{scope, "library.view"},
		QueuedAt: now, CompletedAt: &now,
	}))
	for id, actor := range map[string]string{"op-export-scope-mgr": mgr.ID, "op-export-scope-admin": admin.ID} {
		actor := actor
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
			Params: `{"category":"general"}`, ActorUserID: &actor, QueuedAt: now,
		}))
	}

	opsJSON, _ := exportFiles(t, f, "op-export-scope-mgr")
	require.Contains(t, opsJSON, scanOp, "the operation is in the manager's export")
	require.NotContains(t, opsJSON, scope, "but not the permission set of the account that started it")

	opsJSON, _ = exportFiles(t, f, "op-export-scope-admin")
	require.Contains(t, opsJSON, scope, "users.manage reads it")
}
