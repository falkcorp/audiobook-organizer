// file: internal/server/repairs_revert_gate_test.go
// version: 1.0.0
// guid: 5d1c8e2a-7b43-4f96-8a10-c3e9f2b6d704
// last-edited: 2026-10-25

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
	opArsApply          = "op-ars-apply-revert"
	opArsApplyDiscarded = "op-ars-apply-discarded-revert"
	opArsApplyEmpty     = "op-ars-apply-empty-revert"
	arsFixerID          = "maintenance.audible-read-status" // users.manage
	revertBookID        = "book-invented-revert"
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
	reader, err := store.CreateUser("reader", "reader@plimmet.example.invalid", "bcrypt", "hash", nil, "active")
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
	// op, and the default-permission fixer's op.
	w := postAs(f.srv, f.editor, "/api/v1/operations/"+f.defaultApply+"/revert")
	require.NotEqual(t, http.StatusForbidden, w.Code, "the editor holds the default fixer's permission: %s", w.Body.String())
	w = postAs(f.srv, f.editor, "/api/v1/operations/"+f.plainScan+"/revert")
	require.NotEqual(t, http.StatusForbidden, w.Code, "a plain op is not a repairs op: %s", w.Body.String())
}
