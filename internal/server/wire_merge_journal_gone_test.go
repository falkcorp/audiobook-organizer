// file: internal/server/wire_merge_journal_gone_test.go
// version: 1.0.0
// guid: f7287a40-9996-4784-8c46-4f09afcab92e
// last-edited: 2026-10-18

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

const (
	combineUndoGoneBody = `{"error":"gone","message":"this endpoint has been removed; use POST /api/v1/operations/:id/revert (for a combine a Repairs fixer made; a manual combine has no HTTP undo now) instead"}`
	siblingUndoGoneBody = `{"error":"gone","message":"this endpoint has been removed and has no replacement"}`
)

// retiredMergeUndoRoutes are the two merge-journal undo routes retired by
// 01-P81h with the exact gone() body each answers. Both sit behind
// library.edit_metadata, so a viewer must be refused (403) before the 410.
var retiredMergeUndoRoutes = []struct {
	method, pattern, path, body string
}{
	{http.MethodPost, "/api/v1/merge/undo/:journal_id", "/api/v1/merge/undo/synthetic-journal", combineUndoGoneBody},
	{http.MethodPost, "/api/v1/merge/sibling-undo/:journal_id", "/api/v1/merge/sibling-undo/synthetic-journal", siblingUndoGoneBody},
}

// TestRetiredMergeUndoRoutes_Answer410 drives each retired path through the
// real router as an admin: 410 with the shared gone() body.
func TestRetiredMergeUndoRoutes_Answer410(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, r := range retiredMergeUndoRoutes {
		t.Run(r.method+" "+r.pattern, func(t *testing.T) {
			w := f.do(t, f.sessionToken, r.method, r.path, nil)
			assert.Equal(t, http.StatusGone, w.Code, w.Body.String())
			assert.Equal(t, r.body, w.Body.String())
		})
	}
}

// TestRetiredMergeUndoRoutes_GuardsRunFirst: the stubs sit on the protected
// group behind the old permission guard. No credentials gets 401; a viewer
// (valid session, no library.edit_metadata) gets 403; neither learns the
// route is gone.
func TestRetiredMergeUndoRoutes_GuardsRunFirst(t *testing.T) {
	f := setupCredGuardServer(t)

	viewer, err := f.store.CreateUser("synthetic-viewer", "viewer@example.test", "bcrypt", "x", []string{auth.SeedRoleViewer}, "active")
	require.NoError(t, err)
	viewerSess, err := f.store.CreateSessionWithOrigin(viewer.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	for _, r := range retiredMergeUndoRoutes {
		t.Run("unauthenticated "+r.pattern, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, nil)
			w := httptest.NewRecorder()
			f.srv.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
		t.Run("viewer "+r.pattern, func(t *testing.T) {
			w := f.do(t, viewerSess.ID, r.method, r.path, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
	}
}

// TestRetiredMergeUndoRoutes_KeptJournalListsNotGone: the two owner-kept (D73)
// read-only journal lists beside the retired stubs still run their handlers.
// Each is seeded with one synthetic journal so the control proves the handler
// read it back, not just that the path is not 410. A fp-worker session (only
// PermFingerprintWorker) is refused 403 on both, so the view guard is real.
func TestRetiredMergeUndoRoutes_KeptJournalListsNotGone(t *testing.T) {
	f := setupCredGuardServer(t)

	combine, err := json.Marshal(merge.CombineJournal{ID: "01JSYNTHETICCOMBINE0000000", Status: merge.CombineJournalApplied})
	require.NoError(t, err)
	require.NoError(t, f.store.SetRaw("merge:combine-journal:01JSYNTHETICCOMBINE0000000", combine))
	sibling, err := json.Marshal(merge.SiblingMoveJournal{ID: "01JSYNTHETICSIBLING0000000", Status: merge.SiblingJournalApplied})
	require.NoError(t, err)
	require.NoError(t, f.store.SetRaw("merge:sibling-journal:01JSYNTHETICSIBLING0000000", sibling))

	registered := map[string]bool{}
	for _, r := range f.srv.router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}

	fpWorker, err := f.store.CreateUser("synthetic-fp-worker", "fpworker@example.test", "bcrypt", "x", []string{auth.SeedRoleFPWorker}, "active")
	require.NoError(t, err)
	fpSess, err := f.store.CreateSessionWithOrigin(fpWorker.ID, "127.0.0.1", "test", time.Hour, database.SessionOriginPassword)
	require.NoError(t, err)

	kept := []struct{ pattern, path, seeded string }{
		{"/api/v1/merge/combine-journal", "/api/v1/merge/combine-journal", "01JSYNTHETICCOMBINE0000000"},
		{"/api/v1/merge/sibling-journal", "/api/v1/merge/sibling-journal", "01JSYNTHETICSIBLING0000000"},
	}
	for _, k := range kept {
		t.Run("GET "+k.pattern, func(t *testing.T) {
			require.True(t, registered[http.MethodGet+" "+k.pattern], "route not registered")
			w := f.do(t, f.sessionToken, http.MethodGet, k.path, nil)
			assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), k.seeded)
			assert.NotContains(t, w.Body.String(), `"error":"gone"`)
		})
		t.Run("fp-worker GET "+k.pattern, func(t *testing.T) {
			w := f.do(t, fpSess.ID, http.MethodGet, k.path, nil)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		})
	}
}
