// file: internal/server/repairs_revert_gate_test.go
// version: 1.1.0
// guid: 5d1c8e2a-7b43-4f96-8a10-c3e9f2b6d704
// last-edited: 2026-10-26

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

const (
	opArsApply           = "op-ars-apply-revert"
	opArsApplyDiscarded  = "op-ars-apply-discarded-revert"
	opArsApplyEmpty      = "op-ars-apply-empty-revert"
	opDefaultLibraryOnly = "op-default-fixer-library-only-revert"
	opDefaultUserRow     = "op-default-fixer-user-row-revert"
	arsFixerID           = "maintenance.audible-read-status" // users.manage
	revertBookID         = "book-invented-revert"
)

func postAs(srv *Server, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w
}

// POST /operations/:id/revert is a library.organize route, which the editor
// role holds, and it rewrites what an op wrote and answers with a per-row
// report. Before the gate an editor could revert the Audible read-status apply
// (a users.manage fixer) and read its report. Pins: the editor is refused on
// the op, and on a discarded op whose row is gone (the journal's rows are the
// check there), with nothing changed; the admin reverts it; an ordinary op
// still reverts for the editor.
// Not parallel: it swaps the process-wide config.
func TestRevertOperation_RepairsFixerOpsNeedTheFixersPermissions(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	now := time.Now().UTC()

	// What the Audible read-status apply wrote: one reader's listening state on
	// one book, journaled with the state before (none) and after.
	reader, err := store.CreateUser("selina_kyle", "selina.kyle@example.invalid", "bcrypt", "hash", nil, "active")
	require.NoError(t, err)
	seed := func(opID, bookID, fixer string, keepRow bool) {
		if keepRow {
			require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
				ID: opID, DefID: repairs.ApplyOpID, Plugin: "test", Status: "completed",
				Params: `{"fixer_id":"` + fixer + `"}`, QueuedAt: now, CompletedAt: &now,
			}))
		}
		require.NoError(t, store.SetUserBookState(&database.UserBookState{
			UserID: reader.ID, BookID: bookID, Status: database.UserBookStatusFinished,
			LastActivityAt: now, UpdatedAt: now,
		}))
		after, err := undo.ReadUserStateSnapshot(store, reader.ID, bookID)
		require.NoError(t, err)
		newV, err := undo.EncodeUserStateSnapshot(after)
		require.NoError(t, err)
		oldV, err := undo.EncodeUserStateSnapshot(undo.UserStateSnapshot{})
		require.NoError(t, err)
		require.NoError(t, store.CreateOperationChange(&database.OperationChange{
			ID: "chg-" + opID, OperationID: opID, BookID: bookID,
			ChangeType: undo.ChangeTypeUserBookStateSet, FieldName: undo.UserStateField(reader.ID),
			OldValue: oldV, NewValue: newV, Source: fixer, CreatedAt: now,
		}))
	}
	seed(opArsApply, revertBookID+"-a", arsFixerID, true)
	seed(opArsApplyDiscarded, revertBookID+"-b", arsFixerID, false)

	// An apply whose journal is empty has no row to refuse on: the op row alone
	// must (a 403, not the 409 "nothing to revert" that confirms the op exists).
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: opArsApplyEmpty, DefID: repairs.ApplyOpID, Plugin: "test", Status: "completed",
		Params: `{"fixer_id":"` + arsFixerID + `"}`, QueuedAt: now, CompletedAt: &now,
	}))
	w0 := postAs(f.srv, f.editor, "/api/v1/operations/"+opArsApplyEmpty+"/revert")
	require.Equal(t, http.StatusForbidden, w0.Code, "editor reverting an op with no journal rows: %s", w0.Body.String())

	reverted := func(opID string) bool {
		rows, err := store.GetOperationChanges(opID)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0].RevertedAt != nil
	}

	for _, op := range []string{opArsApply, opArsApplyDiscarded} {
		for who, token := range map[string]string{"viewer": f.viewer, "editor": f.editor} {
			w := postAs(f.srv, token, "/api/v1/operations/"+op+"/revert")
			require.Equal(t, http.StatusForbidden, w.Code, "%s reverting %s: %s", who, op, w.Body.String())
			require.False(t, reverted(op), "%s must not have changed %s", who, op)
		}
	}

	for _, op := range []string{opArsApply, opArsApplyDiscarded} {
		w := postAs(f.srv, f.admin, "/api/v1/operations/"+op+"/revert")
		require.Equal(t, http.StatusOK, w.Code, "admin reverting %s: %s", op, w.Body.String())
		require.True(t, reverted(op), "the admin's revert of %s took effect", op)
	}

	// Not a blanket refusal: the editor reverts an op that is not a Repairs
	// op, and a default-permission fixer's op that holds no user data.
	w := postAs(f.srv, f.editor, "/api/v1/operations/"+f.plainScan+"/revert")
	require.NotEqual(t, http.StatusForbidden, w.Code, "a plain op is not a repairs op: %s", w.Body.String())
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: opDefaultLibraryOnly, DefID: repairs.ApplyOpID, Plugin: "test", Status: "completed",
		Params: `{"fixer_id":"` + f.defaultFixerID + `"}`, QueuedAt: now, CompletedAt: &now,
	}))
	require.NoError(t, store.CreateOperationChange(&database.OperationChange{
		ID: "chg-" + opDefaultLibraryOnly, OperationID: opDefaultLibraryOnly, BookID: revertBookID + "-c",
		ChangeType: undo.ChangeTypeBookFileMove, FieldName: "book_file:file-invented-2",
		OldValue: "/lib/Gotham/year-one.m4b", NewValue: "/lib/Gotham/hush.m4b", Source: f.defaultFixerID, CreatedAt: now,
	}))
	w = postAs(f.srv, f.editor, "/api/v1/operations/"+opDefaultLibraryOnly+"/revert")
	require.NotEqual(t, http.StatusForbidden, w.Code, "the editor holds the default fixer's permission and the op has no user row: %s", w.Body.String())
}

// Undoing a user-state row writes another user's listening state back, so the
// revert and the undo report both need users.manage on top of the fixer's
// permissions: an editor who may run the default fixer is refused an op whose
// journal holds one of its user_state_follow rows (the review's S4), with
// nothing reverted, and the admin reverts it. Before this MayUndoChange had no
// user-data leg and the editor could revert it.
// Not parallel: it swaps the process-wide config.
func TestRevertOperation_UserStateRowsNeedUsersManage(t *testing.T) {
	f := newRepairsOpAccessFixture(t)
	store, ok := f.srv.storeForWiring().(*database.PebbleStore)
	require.True(t, ok)
	now := time.Now().UTC()
	require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
		ID: opDefaultUserRow, DefID: repairs.ApplyOpID, Plugin: "test", Status: "completed",
		Params: `{"fixer_id":"` + f.defaultFixerID + `"}`, QueuedAt: now, CompletedAt: &now,
	}))
	follow := `{"sync_redirected":false,"progress":[{"user_id":"user-invented-selina","absorbed_state":{"user_id":"user-invented-selina","status":"finished"}}]}`
	for _, ch := range []*database.OperationChange{
		{ID: "chg-ur-move", OperationID: opDefaultUserRow, BookID: revertBookID + "-d", ChangeType: undo.ChangeTypeBookFileMove,
			FieldName: "book_file:file-invented-3", OldValue: "/lib/Gotham/a.m4b", NewValue: "/lib/Gotham/b.m4b", Source: f.defaultFixerID, CreatedAt: now},
		{ID: "chg-ur-follow", OperationID: opDefaultUserRow, BookID: revertBookID + "-d", ChangeType: undo.ChangeTypeUserStateFollow,
			FieldName: undo.SurvivorField("book-invented-survivor"), OldValue: "", NewValue: follow, Source: f.defaultFixerID, CreatedAt: now},
	} {
		require.NoError(t, store.CreateOperationChange(ch))
	}
	untouched := func() {
		rows, err := store.GetOperationChanges(opDefaultUserRow)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		for _, r := range rows {
			require.Nil(t, r.RevertedAt, "%s must not have been reverted", r.ID)
		}
	}

	w := postAs(f.srv, f.editor, "/api/v1/operations/"+opDefaultUserRow+"/revert")
	require.Equal(t, http.StatusForbidden, w.Code, "the editor reverting a user-state row: %s", w.Body.String())
	require.NotContains(t, w.Body.String(), "user-invented-selina")
	untouched()
	w = getAs(f.srv, f.editor, "/api/v1/operations/"+opDefaultUserRow+"/undo/preflight")
	require.Equal(t, http.StatusForbidden, w.Code, "the undo report offers nothing the revert refuses: %s", w.Body.String())
	untouched()

	w = getAs(f.srv, f.admin, "/api/v1/operations/"+opDefaultUserRow+"/undo/preflight")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
