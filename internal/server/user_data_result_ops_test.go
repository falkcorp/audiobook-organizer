// file: internal/server/user_data_result_ops_test.go
// version: 1.0.0
// guid: 9e2b6c41-7a05-4d38-b1f6-4c8d0a35e927
// last-edited: 2026-10-26

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// TestOperationResult_UserDataResultsNeedUsersManage: the result of an op that
// names users (opaccess.ResultUserDataDefs) is served by GET
// /operations/:id/result to a caller who holds users.manage only. The viewer
// and the editor are refused 403 and nothing of the result leaks into the
// refusal; the admin reads it; an ordinary op's result is still a library.view
// read. Before this the merged-user-state repair's items[] (per user: the books
// they have listening state on, positions, bookmarks) went to every viewer.
func TestOperationResult_UserDataResultsNeedUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	const (
		listener = "01INVENTEDLISTENER000000A"
		repairID = "op-invented-repair-merged-user-state"
		mergeID  = "op-invented-user-merge"
		scanID   = "op-invented-scan-result"
	)
	insert := func(id, def, result string) {
		t.Helper()
		res := result
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: def, Plugin: "test", Status: "completed", Params: `{}`,
			ResultData: &res, QueuedAt: now, CompletedAt: &now,
		}))
	}
	insert(repairID, merge.UserStateRepairOpID,
		`{"apply":false,"items":[{"kind":"state","user_id":"`+listener+`","from_book_id":"book-invented-year-one","to_book_id":"book-invented-hush","positions":3,"bookmarks":2}]}`)
	insert(mergeID, usermerge.OpDefID,
		`{"survivor_id":"`+listener+`","absorbed_id":"01INVENTEDABSORBED00000A","counts":{"user_book_state":{"absorbed":7}}}`)
	insert(scanID, "library.scan", `{"scanned":3}`)

	for _, tc := range []struct {
		name, id, secret string
	}{
		{"repair-merged-user-state", repairID, listener},
		{"user-merge", mergeID, "01INVENTEDABSORBED00000A"},
	} {
		path := "/api/v1/operations/" + tc.id + "/result"
		for _, who := range []struct{ name, token string }{{"viewer", f.viewer}, {"editor", f.editor}} {
			w := getAs(f.srv, who.token, path)
			require.Equal(t, 403, w.Code, "%s %s: %s", who.name, tc.name, w.Body.String())
			require.NotContains(t, w.Body.String(), tc.secret, "%s %s: nothing of the result leaks", who.name, tc.name)
		}
		w := getAs(f.srv, f.admin, path)
		require.Equal(t, 200, w.Code, "admin %s: %s", tc.name, w.Body.String())
		require.Contains(t, w.Body.String(), tc.secret, "admin %s reads the result", tc.name)
	}
	w := getAs(f.srv, f.viewer, "/api/v1/operations/"+scanID+"/result")
	require.Equal(t, 200, w.Code, "an ordinary op's result stays a library.view read: %s", w.Body.String())
	require.Contains(t, w.Body.String(), `"scanned":3`)

	// The op row itself (state, progress, timing) is not user data.
	w = getAs(f.srv, f.viewer, "/api/v1/operations/v2/"+repairID)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), listener)
}

// TestResultUserDataDefs_AreRegisteredDefs pins that every definition id the
// result scope lists is one the server really registers, so a renamed op cannot
// silently fall out of the gate.
func TestResultUserDataDefs_AreRegisteredDefs(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	require.NotNil(t, f.srv.opRegistry)
	require.NotEmpty(t, opaccess.ResultUserDataDefs)
	for id := range opaccess.ResultUserDataDefs {
		_, ok := f.srv.opRegistry.Def(id)
		require.True(t, ok, "%s is listed in opaccess.ResultUserDataDefs but is not a registered op definition", id)
	}
}
