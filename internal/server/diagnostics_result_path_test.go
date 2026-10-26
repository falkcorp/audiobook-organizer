// file: internal/server/diagnostics_result_path_test.go
// version: 1.0.0
// guid: d27a5e90-1b64-4c83-9e0f-6a3b8d5c4e21
// last-edited: 2026-10-26

package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// GET /operations/:id/result needs only library.view, and a diagnostics export's
// result holds the archive's path on the server's disk (zip_path). That path is
// left out for a caller who cannot download the archive (no settings.manage);
// the recorded scope (narrowed_to) stays, and a caller with settings.manage or
// the admin reads the record whole. Another operation's result is untouched.
// Not parallel: it swaps the process-wide config.
func TestOperationResult_ExportPathNeedsSettingsManage(t *testing.T) {
	const (
		exportOp = "op-export-result-path"
		scanOp   = "op-scan-result-path"
		zipPath  = "/lib/Gotham/exports/hush-export.zip"
	)
	f := newRepairsOpAccessFixture(t)
	mgr := opsManagerToken(t, f)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	insert := func(id, def, result string) {
		t.Helper()
		res := result
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: def, Plugin: "test", Status: "completed", Params: `{}`,
			ResultData: &res, QueuedAt: now, CompletedAt: &now,
		}))
	}
	insert(exportOp, "diagnostics.export", `{"zip_path":"`+zipPath+`","narrowed_to":["library.view"]}`)
	insert(scanOp, "library.scan", `{"zip_path":"kept-by-another-op","scanned":3}`)

	get := func(token, id string) string {
		t.Helper()
		w := getAs(f.srv, token, "/api/v1/operations/"+id+"/result")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Body.String()
	}

	setOpsManagerPermissions(t, f, auth.PermLibraryView)
	body := get(mgr, exportOp)
	require.NotContains(t, body, zipPath, "a library.view caller does not learn where the archive is")
	require.NotContains(t, body, "zip_path")
	require.Contains(t, body, "narrowed_to", "the recorded scope stays")
	require.Contains(t, get(mgr, scanOp), "kept-by-another-op", "another operation's result is not touched")

	setOpsManagerPermissions(t, f, auth.PermLibraryView, auth.PermSettingsManage)
	require.Contains(t, get(mgr, exportOp), zipPath, "settings.manage may download it, so it reads the path")
	require.Contains(t, get(f.admin, exportOp), zipPath)
}
