// file: internal/server/diagnostics_export_redaction_test.go
// version: 1.1.0
// guid: 2d6a9f13-8b47-4c20-a5e1-7f3b0c92d684
// last-edited: 2026-10-26

package server

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// exportFiles runs the diagnostics export as operation opID (whose actor decides
// what it may carry) and returns operations.json and logs.json.
func exportFiles(t *testing.T, f *repairsOpAccessFixture, opID string) (opsJSON, logsJSON string) {
	t.Helper()
	svc := diagnostics.NewService(f.srv.storeForWiring(), nil, "")
	zipPath, err := svc.GenerateExport(context.Background(), "general", "test", f.srv.diagnosticsExportOptions(opID), nil)
	require.NoError(t, err)
	defer os.Remove(zipPath)
	r, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer r.Close()
	read := func(name string) string {
		for _, zf := range r.File {
			if zf.Name != name {
				continue
			}
			rc, err := zf.Open()
			require.NoError(t, err)
			defer rc.Close()
			b, err := io.ReadAll(rc)
			require.NoError(t, err)
			return string(b)
		}
		t.Fatalf("%s is not in the export", name)
		return ""
	}
	return read("operations.json"), read("logs.json")
}

// TestDiagnosticsExport_OperationsAndLogsFollowTheRequestersAccess: the export
// dumps the 100 newest operation rows whole (params, result, error) and the last
// day of server log lines, to any settings.manage holder. A custom role with
// settings.manage but not users.manage must not get another user's listening
// facts that way: it gets no Repairs run of a fixer it may not use, no params or
// result of an operation whose result names users, and no log line naming a
// user; the admin gets everything. The requester is the actor stamped on the
// export's op row (not its params), and an op with no stamped actor is treated as
// holding nothing. Before this the export carried all of it to the custom role.
// Not parallel: it swaps the process-wide config.
func TestDiagnosticsExport_OperationsAndLogsFollowTheRequestersAccess(t *testing.T) {
	const (
		listener = "01INVENTEDLISTENER000000A"
		arsOp    = "op-export-audible-read-status"
		repairOp = "op-export-repair-merged-user-state"
		scanOp   = "op-export-plain-scan"
	)
	f := newRepairsOpAccessFixture(t)
	_ = opsManagerToken(t, f)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()

	insert := func(id, def, params, result string) {
		t.Helper()
		res := result
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: def, Plugin: "test", Status: "completed", Params: params,
			ResultData: &res, QueuedAt: now.Add(time.Second), CompletedAt: &now,
		}))
	}
	insert(arsOp, "repairs.apply", `{"fixer_id":"maintenance.audible-read-status","target_user_id":"`+listener+`"}`,
		`{"fixer_id":"maintenance.audible-read-status","results":[{"owner_user_id":"`+listener+`"}]}`)
	insert(repairOp, merge.UserStateRepairOpID, `{"apply":false}`, `{"items":[{"user_id":"`+listener+`","positions":3}]}`)
	insert(scanOp, "library.scan", `{}`, `{"scanned":3}`)
	require.NoError(t, store.AddSystemActivityLog("abs", "info", "progress patch: user="+listener+" currentTime=12.5"))
	// Lines of a user-data subsystem that carry no user text at all: the source
	// (or the "<subsystem>: " prefix, when slog's text handler made it "server")
	// is what marks them, the same predicate the activity reader applies.
	require.NoError(t, store.AddSystemActivityLog("abs", "info", "seek to Year One chapter 3 position 4021"))
	require.NoError(t, store.AddSystemActivityLog("server", "info", "merge: moved 3 rows for Knightfall"))
	require.NoError(t, store.AddSystemActivityLog("scanner", "info", "scan complete count=12"))

	exportOp := func(id string, actor *string) {
		t.Helper()
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
			Params: `{"category":"general"}`, ActorUserID: actor, QueuedAt: now,
		}))
	}
	mgr, err := store.GetUserByUsername("opsmgr")
	require.NoError(t, err)
	require.NotNil(t, mgr)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	require.NotNil(t, admin)
	exportOp("op-export-by-manager", &mgr.ID)
	exportOp("op-export-by-admin", &admin.ID)
	exportOp("op-export-no-actor", nil)

	for _, id := range []string{"op-export-by-manager", "op-export-no-actor"} {
		opsJSON, logsJSON := exportFiles(t, f, id)
		require.NotContains(t, opsJSON, arsOp, "%s: a Repairs run of a fixer the requester may not use", id)
		require.NotContains(t, opsJSON, listener, "%s: no user id in an operation row", id)
		require.NotContains(t, logsJSON, listener, "%s: no log line or column naming a user", id)
		require.NotContains(t, opsJSON, "audible-read-status", id)
		require.Contains(t, opsJSON, scanOp, "%s: an ordinary operation stays", id)
		require.Contains(t, opsJSON, `scanned`, "%s: and so does its result", id)
		require.Contains(t, opsJSON, repairOp, "%s: the row of a user-data operation stays, without its result", id)
		require.NotContains(t, logsJSON, "Year One", "%s: a line of the abs subsystem names no user and is still withheld", id)
		require.NotContains(t, logsJSON, "Knightfall", "%s: a line prefixed by the merge subsystem is withheld", id)
		require.Contains(t, logsJSON, "scan complete", "%s: an ordinary log line stays", id)
	}

	opsJSON, logsJSON := exportFiles(t, f, "op-export-by-admin")
	for _, want := range []string{arsOp, repairOp, scanOp, listener} {
		require.Contains(t, opsJSON, want, "the admin's export carries %s", want)
	}
	require.Contains(t, logsJSON, "progress patch", "the admin's export carries the log line naming a user")
	require.Contains(t, logsJSON, "Year One", "the admin's export carries the user-data subsystem lines")
	require.Contains(t, logsJSON, "Knightfall")

	// The handler stamps the caller as the export's actor, which is what the
	// check above reads.
	w := postJSONAs(f.srv, f.admin, "/api/v1/diagnostics/export", `{"category":"general"}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.True(t, strings.Contains(w.Body.String(), "operation_id"), w.Body.String())
	rows, err := store.ListOperationsV2Since(time.Time{}, 500)
	require.NoError(t, err)
	stamped := false
	for _, r := range rows {
		if r.DefID == "diagnostics.export" && r.ActorUserID != nil && *r.ActorUserID == admin.ID && r.ID != "op-export-by-admin" {
			stamped = true
		}
	}
	require.True(t, stamped, "POST /diagnostics/export stamps the signed-in caller as the op's actor")
}
