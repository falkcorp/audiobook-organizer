// file: internal/server/diagnostics_export_access_test.go
// version: 1.3.0
// guid: 6a2f8c15-9d43-4e70-b1a8-3c5d7e0f92b4
// last-edited: 2026-10-26

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// seedCompletedExport inserts a finished op of def whose result is a real file,
// queued by actor (nil: no recorded actor), with the scope the export was built
// for recorded as narrowed (nil: the result carries no record).
func seedCompletedExport(t *testing.T, store database.Store, id, def string, actor *string, narrowed *[]string) {
	t.Helper()
	zip := filepath.Join(t.TempDir(), id+".zip")
	require.NoError(t, os.WriteFile(zip, []byte("PK-invented-zip-body-for-"+id), 0o600))
	res, err := json.Marshal(diagnostics.ExportResult{ZipPath: zip, NarrowedTo: narrowed})
	require.NoError(t, err)
	r := string(res)
	now := time.Now().UTC()
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: id, DefID: def, Plugin: "diagnostics", Status: "completed",
		Params: `{"category":"general"}`, ResultData: &r, ActorUserID: actor,
		QueuedAt: now, CompletedAt: &now,
	}))
}

// narrowedFor is the scope an export built for the account id would record now.
func narrowedFor(srv *Server, id string) *[]string {
	held := srv.opActorPermissions(&database.OperationV2Row{ActorUserID: &id})
	out := srv.exportNarrowedTo(func(p auth.Permission) bool { return held[p] })
	return &out
}

// TestDiagnosticsDownload_ServedOnlyToACallerHoldingTheRecordedScope: the zip is
// narrowed, when built, to what the account it was built for may read, and the
// build records that scope on the export's result. The download route
// (settings.manage) serves it only to a caller whose own request holds all of
// it, so the narrowing cannot be undone from the run list's id: a custom role
// with settings.manage but not users.manage cannot read an admin's export, and
// neither can a narrowly scoped API key of the admin's own account. The gate
// reads the record, not the account's roles now, and fails closed.
// Not parallel: it swaps the process-wide config.
func TestDiagnosticsDownload_ServedOnlyToACallerHoldingTheRecordedScope(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	mgrToken := opsManagerToken(t, f)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	mgr, err := store.GetUserByUsername("opsmgr")
	require.NoError(t, err)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	require.NotNil(t, mgr)
	require.NotNil(t, admin)

	adminScope, mgrScope := narrowedFor(f.srv, admin.ID), narrowedFor(f.srv, mgr.ID)
	require.Contains(t, *adminScope, string(auth.PermUsersManage))
	require.NotContains(t, *mgrScope, string(auth.PermUsersManage))
	gone := "01JNOSUCHACCOUNT00000000A"
	empty := []string{}

	seedCompletedExport(t, store, "exp-by-admin", "diagnostics.export", &admin.ID, adminScope)
	seedCompletedExport(t, store, "exp-by-manager", "diagnostics.export", &mgr.ID, mgrScope)
	seedCompletedExport(t, store, "exp-no-actor", "diagnostics.export", nil, &empty)
	seedCompletedExport(t, store, "exp-no-record", "diagnostics.export", &admin.ID, nil)
	seedCompletedExport(t, store, "exp-actor-gone", "diagnostics.export", &gone, mgrScope)
	seedCompletedExport(t, store, "op-not-an-export", "repairs.plan", &admin.ID, adminScope)

	dl := func(token, id string) int {
		return getAs(f.srv, token, "/api/v1/diagnostics/export/"+id+"/download").Code
	}
	assert.Equal(t, http.StatusForbidden, dl(mgrToken, "exp-by-admin"),
		"a role without users.manage does not read an admin's export")
	assert.Equal(t, http.StatusOK, dl(f.admin, "exp-by-admin"), "the account it was built for")
	assert.Equal(t, http.StatusOK, dl(mgrToken, "exp-by-manager"), "the account it was built for")
	assert.Equal(t, http.StatusOK, dl(f.admin, "exp-by-manager"), "an account that holds more than the export was narrowed to")
	assert.Equal(t, http.StatusOK, dl(mgrToken, "exp-no-actor"), "built holding nothing: nothing to undo")
	assert.Equal(t, http.StatusNotFound, dl(mgrToken, "exp-does-not-exist"), "a missing export is still the handler's 404")
	assert.Equal(t, http.StatusForbidden, dl(f.editor, "exp-by-admin"), "the route's own gate is unchanged")

	// Fails closed.
	assert.Equal(t, http.StatusForbidden, dl(f.admin, "exp-no-record"),
		"an export whose result records no scope is served to nobody, its own account included")
	assert.Equal(t, http.StatusForbidden, dl(f.admin, "exp-actor-gone"),
		"an export whose account cannot be read back is served to nobody")
	assert.Equal(t, http.StatusNotFound, dl(f.admin, "op-not-an-export"),
		"another op's result is not an export and its path is not served")

	// An API key of the admin's own account, scoped to the route's permission
	// only, is not the account: it holds less than the export was narrowed to.
	raw, hash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	exp := time.Now().Add(time.Hour)
	_, err = store.CreateAPIKey(&database.APIKey{
		UserID: admin.ID, Name: "synthetic narrow key", TokenHash: hash,
		Scopes: []string{string(auth.PermSettingsManage)}, Status: "active", CreatedAt: time.Now(), ExpiresAt: &exp,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, dl(raw, "exp-by-admin"),
		"a narrowly scoped key of the account that built the export does not read it")
	assert.Equal(t, http.StatusForbidden, dl(raw, "exp-by-manager"),
		"nor one built for a manager, whose scope the key does not cover")
	assert.Equal(t, http.StatusOK, dl(raw, "exp-no-actor"),
		"it may read an export that was narrowed to nothing")

	// The record is compared as stored: raising the account's roles after the
	// build does not change what the export requires of a caller. A key of the
	// manager's account scoped to what it held at the build holds the recorded
	// scope, though the account now holds users.manage as well.
	base := []auth.Permission{auth.PermLibraryView, auth.PermScanTrigger, auth.PermLibraryEditMetadata, auth.PermSettingsManage}
	setOpsManagerPermissions(t, f, append(base, auth.PermUsersManage)...)
	mgrRaw, mgrHash, err := database.GenerateAPIKeyToken()
	require.NoError(t, err)
	_, err = store.CreateAPIKey(&database.APIKey{
		UserID: mgr.ID, Name: "synthetic manager key", TokenHash: mgrHash,
		Scopes: []string{string(base[0]), string(base[1]), string(base[2]), string(base[3])},
		Status: "active", CreatedAt: time.Now(), ExpiresAt: &exp,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, dl(mgrRaw, "exp-by-manager"),
		"the gate reads the recorded scope, not the account's roles as they are now")

	// A disabled account's export is served to nobody.
	_, err = store.SetUserStatus(mgr.ID, "disabled")
	require.NoError(t, err)
	w := getAs(f.srv, f.admin, "/api/v1/diagnostics/export/exp-by-manager/download")
	assert.Equal(t, http.StatusForbidden, w.Code, "the account it was built for is no longer active")
	assert.Contains(t, w.Body.String(), "not active")
}

// failingUserStore is a store whose user count or account lookup fails, for the
// download check that reads them when the request runs.
type failingUserStore struct {
	database.Store
	countErr, userErr error
}

func (s failingUserStore) CountUsers() (int, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.Store.CountUsers()
}

func (s failingUserStore) GetUserByID(id string) (*database.User, error) {
	if s.userErr != nil {
		return nil, s.userErr
	}
	return s.Store.GetUserByID(id)
}

// TestDiagnosticsDownload_ACheckThatCannotDecideDoesNotPass: the scope check
// reads the user count and the account the export was built for when the
// request runs; a store failure on either is a 500 and the archive is not
// served, where the old middleware let the request through on any error.
// Not parallel: it swaps the process-wide config.
func TestDiagnosticsDownload_ACheckThatCannotDecideDoesNotPass(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	real, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	admin, err := real.GetUserByUsername("admin")
	require.NoError(t, err)
	require.NotNil(t, admin)
	seedCompletedExport(t, real, "exp-court-of-owls", "diagnostics.export", &admin.ID, narrowedFor(f.srv, admin.ID))
	path := "/api/v1/diagnostics/export/exp-court-of-owls/download"
	require.Equal(t, http.StatusOK, getAs(f.srv, f.admin, path).Code, "served while the store answers")

	orig := f.srv.store
	t.Cleanup(func() { f.srv.store = orig })

	f.srv.store = failingUserStore{Store: real, countErr: errors.New("pebble: closed")}
	w := getAs(f.srv, f.admin, path)
	assert.Equal(t, http.StatusInternalServerError, w.Code, "the user count could not be read")
	assert.NotContains(t, w.Body.String(), "PK-invented-zip-body", "and nothing was served")

	f.srv.store = failingUserStore{Store: real, userErr: errors.New("pebble: corrupt user")}
	w = getAs(f.srv, f.admin, path)
	assert.Equal(t, http.StatusInternalServerError, w.Code, "the account the export was built for could not be read")
	assert.NotContains(t, w.Body.String(), "PK-invented-zip-body")
}

// TestRepairsList_LastRunErrorIsReadThroughTheTextRule pins the Repairs
// handler's WithOpAccess wiring: GET /repairs serves a fixer's last run with its
// error text, which a Repairs run can fill with a user's id. An admin
// (users.manage) reads it, and a caller who may use the fixer but not read users'
// data gets the withheld notice. Dropping the wiring would withhold it from the
// admin too, which this test also catches.
// Not parallel: it swaps the process-wide config.
func TestRepairsList_LastRunErrorIsReadThroughTheTextRule(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	now := time.Now().UTC()
	msg := "merge failed: user_id=" + secondaryUserID + " could not be moved"
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-last-apply-hush", DefID: repairs.ApplyOpID, Plugin: "repairs", Status: "failed",
		Params: `{"fixer_id":"` + f.defaultFixerID + `"}`, ErrorMessage: &msg, QueuedAt: now, CompletedAt: &now,
	}))
	// The key the lane records a fixer's newest apply under (lastApplyKey).
	require.NoError(t, store.SetSetting("repairs_last_apply_op:"+f.defaultFixerID, "op-last-apply-hush", "string", false))

	lastApplyError := func(token string) string {
		var page struct {
			Fixers []struct {
				ID        string `json:"id"`
				Permitted bool   `json:"permitted"`
				LastApply *struct {
					Error string `json:"error"`
				} `json:"last_apply"`
			} `json:"fixers"`
		}
		w := getAs(f.srv, token, "/api/v1/repairs")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
		raw := env.Data
		if len(raw) == 0 {
			raw = w.Body.Bytes()
		}
		require.NoError(t, json.Unmarshal(raw, &page), w.Body.String())
		for _, fx := range page.Fixers {
			if fx.ID == f.defaultFixerID {
				require.True(t, fx.Permitted, "the caller may use the fixer")
				require.NotNil(t, fx.LastApply, "its last apply is listed")
				return fx.LastApply.Error
			}
		}
		t.Fatalf("fixer %s not listed: %s", f.defaultFixerID, w.Body.String())
		return ""
	}

	assert.Contains(t, lastApplyError(f.admin), secondaryUserID, "users.manage reads the error whole")
	got := lastApplyError(f.editor)
	assert.NotContains(t, got, secondaryUserID, "an editor may use the fixer, not read users' data")
	assert.Contains(t, got, activity.WithheldUserText)
}

// TestGenericStartRoutes_StampTheCallerAsActor: POST /operations/v2 and a retry
// record who started the op, like POST /diagnostics/export and v3 do, so an
// export started or retried by an admin through them is not treated as having
// no requester (which narrows it to nothing). A retry stamps the caller who
// retried, not the original actor. Not parallel: it swaps the process-wide config.
func TestGenericStartRoutes_StampTheCallerAsActor(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store := f.srv.storeForWiring()
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	require.NotNil(t, admin)

	actorOf := func(id string) *database.OperationV2Row {
		t.Helper()
		row, err := store.GetOperationV2(id)
		require.NoError(t, err)
		require.NotNil(t, row, id)
		return row
	}
	opID := func(w *httptest.ResponseRecorder, keys ...string) string {
		t.Helper()
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		var m map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
			if d, ok := m["data"].(map[string]any); ok {
				if v, ok := d[k].(string); ok && v != "" {
					return v
				}
			}
		}
		t.Fatalf("no op id in %s", w.Body.String())
		return ""
	}

	// Trigger.
	w := postJSONAs(f.srv, f.admin, "/api/v1/operations/v2", `{"def_id":"diagnostics.export","params":{"category":"general"}}`)
	trig := actorOf(opID(w, "op_id"))
	require.NotNil(t, trig.ActorUserID, "a start through POST /operations/v2 records its caller")
	assert.Equal(t, admin.ID, *trig.ActorUserID)
	assert.Contains(t, trig.ActorPermissions, string(auth.PermSettingsManage), "and the permission set the request held")
	assert.False(t, trig.OwnerApply, "but never marks it as an owner apply: only the Repairs owner-apply handler does")

	// An export it queued carries what the admin may read.
	opsJSON, _ := exportFiles(t, f, trig.ID)
	assert.Contains(t, opsJSON, opDefaultApply, "an admin's export started here is not narrowed to nothing")

	// Retry, by a caller other than the original actor. The export def has a
	// concurrency key, so a second enqueue while the first is live answers with
	// the first's id; finish the first so the retry makes its own run.
	done := time.Now().UTC()
	require.NoError(t, store.UpdateOperationV2Status(trig.ID, "completed", &done, &done, nil))
	other := "user-someone-else"
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: "op-export-failed", DefID: "diagnostics.export", Plugin: "diagnostics", Status: "failed",
		Params: `{"category":"general"}`, ActorUserID: &other, QueuedAt: time.Now().UTC(),
	}))
	w = postJSONAs(f.srv, f.admin, "/api/v1/operations/v2/op-export-failed/retry", `{}`)
	retried := actorOf(opID(w, "id"))
	require.NotEqual(t, trig.ID, retried.ID, "the retry is its own run")
	require.NotNil(t, retried.ActorUserID, "a retry records its caller")
	assert.Equal(t, admin.ID, *retried.ActorUserID, "the caller who retried, not the original actor")
	assert.False(t, retried.OwnerApply, "a retry is never an owner apply either")
}

// TestOpActorPermissions_CapAtTheRequestsScopeAndRefuseDisabledAccounts: an
// export runs after its request is gone, so what it may read is the account's
// current roles capped at the permission set the request held (an API key's
// scopes), and nothing once the account is disabled.
func TestOpActorPermissions_CapAtTheRequestsScopeAndRefuseDisabledAccounts(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	require.NotNil(t, admin)
	id := admin.ID

	rowOf := func(perms []string) *database.OperationV2Row {
		return &database.OperationV2Row{ID: "x", ActorUserID: &id, ActorPermissions: perms}
	}

	full := f.srv.opActorPermissions(rowOf(nil))
	assert.True(t, full[auth.PermUsersManage], "no stamped set: the account's roles")

	// A recorded empty set is a request that held nothing: the job holds
	// nothing, not the account's roles that a nil (never recorded) set falls to.
	assert.Empty(t, f.srv.opActorPermissions(rowOf([]string{})), "a recorded empty set caps the job at nothing")

	scoped := f.srv.opActorPermissions(rowOf([]string{string(auth.PermSettingsManage)}))
	assert.True(t, scoped[auth.PermSettingsManage])
	assert.False(t, scoped[auth.PermUsersManage], "a key scoped to settings.manage does not reach users.manage through its account")

	// The same reach decides what the export carries.
	opsJSON, _ := exportFiles(t, f, seedExportOpWithPerms(t, store, "op-export-scoped-key", &id, []string{string(auth.PermSettingsManage)}))
	assert.NotContains(t, opsJSON, opDefaultApply, "a Repairs run of a fixer the key's scope does not cover is left out")
	assert.Contains(t, opsJSON, opScopedPlan, "one whose fixer needs only settings.manage stays")

	_, err = store.SetUserStatus(admin.ID, "disabled")
	require.NoError(t, err)
	assert.Empty(t, f.srv.opActorPermissions(rowOf(nil)), "a disabled account holds nothing")
}

// TestOperationRow_KeepsAnEmptyActorPermissionSetApartFromNone: the stored op
// row tells a recorded empty set (a request that held nothing) from a row that
// recorded none (written before 2026-10-26), through the store round trip.
func TestOperationRow_KeepsAnEmptyActorPermissionSetApartFromNone(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store := f.srv.storeForWiring()
	actor := "01JBRUCEWAYNE00000000000A"

	emptyID := seedExportOpWithPerms(t, store, "op-export-empty-set", &actor, []string{})
	row, err := store.GetOperationV2(emptyID)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.ActorPermissions, "a recorded empty set reads back as an empty set, not as none")
	assert.Empty(t, row.ActorPermissions)

	noneID := seedExportOpWithPerms(t, store, "op-export-no-set", &actor, nil)
	row, err = store.GetOperationV2(noneID)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Nil(t, row.ActorPermissions, "a row that recorded no set reads back as none")
}

func seedExportOpWithPerms(t *testing.T, store database.Store, id string, actor *string, perms []string) string {
	t.Helper()
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: id, DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
		Params: `{"category":"general"}`, ActorUserID: actor, ActorPermissions: perms, QueuedAt: time.Now().UTC(),
	}))
	return id
}

// TestDiagnosticsExportOp_RecordsTheScopeItWasNarrowedTo: the export op writes,
// beside the zip path, the permissions the account it was built for held when
// the build began; the download check compares callers against that record.
// Not parallel: it swaps the process-wide config.
func TestDiagnosticsExportOp_RecordsTheScopeItWasNarrowedTo(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	mgrToken := opsManagerToken(t, f)
	store := f.srv.storeForWiring()
	require.NotNil(t, f.srv.opRegistry, "the fixture builds the operations registry")
	f.srv.opRegistry.Start(context.Background())
	t.Cleanup(func() { _ = f.srv.opRegistry.Shutdown(context.Background()) })

	for name, token := range map[string]string{"admin": f.admin, "opsmgr": mgrToken} {
		w := postJSONAs(f.srv, token, "/api/v1/diagnostics/export", `{"category":"general"}`)
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		var m struct {
			Data struct {
				OperationID string `json:"operation_id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), w.Body.String())
		require.NotEmpty(t, m.Data.OperationID, w.Body.String())

		var row *database.OperationV2Row
		require.Eventually(t, func() bool {
			var err error
			row, err = store.GetOperationV2(m.Data.OperationID)
			return err == nil && row != nil && row.Status == "completed"
		}, 30*time.Second, 50*time.Millisecond, "%s's export finishes", name)
		require.NotNil(t, row.ResultData)
		res, err := diagnostics.ParseExportResult(*row.ResultData)
		require.NoError(t, err)
		require.NotNil(t, res.NarrowedTo, "the result records the scope")
		assert.NotEmpty(t, res.ZipPath)
		if name == "admin" {
			assert.Contains(t, *res.NarrowedTo, string(auth.PermUsersManage))
		} else {
			assert.NotContains(t, *res.NarrowedTo, string(auth.PermUsersManage),
				"a manager's export records that it was built without users.manage")
		}
		// Each export's own gate verdict follows the record.
		assert.Equal(t, http.StatusOK, getAs(f.srv, token, "/api/v1/diagnostics/export/"+m.Data.OperationID+"/download").Code)
		if name == "admin" {
			assert.Equal(t, http.StatusForbidden, getAs(f.srv, mgrToken, "/api/v1/diagnostics/export/"+m.Data.OperationID+"/download").Code)
		}
	}
}
