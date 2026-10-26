// file: internal/server/purge_preview_cancel_test.go
// version: 1.0.0
// guid: b815dc28-323d-4d1c-9334-dc7047bce5da
// last-edited: 2026-10-26

package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// libraryDeleteSession is exactPermGoneSession for library.delete that also
// returns the user's id (the actor a preview op is stored under).
func (f *credGuardFixture) libraryDeleteSession(t *testing.T, name string) (userID, token string) {
	t.Helper()
	role, err := f.store.CreateRole(&database.Role{
		ID: "synthetic-" + name, Name: "synthetic-" + name, Permissions: []string{auth.PermLibraryDelete},
	})
	require.NoError(t, err)
	u, err := f.store.CreateUser("synthetic-"+name, name+"@example.test", "bcrypt", "x", []string{role.ID}, "active")
	require.NoError(t, err)
	return u.ID, f.sessionFor(t, u.ID)
}

// queuedPurgeRow stores a queued operation row the way the enqueue does.
func (f *credGuardFixture) queuedPurgeRow(t *testing.T, id, defID, params, actor string) {
	t.Helper()
	row := database.OperationV2Row{
		ID: id, DefID: defID, Plugin: "library", Status: "queued", Params: params,
		QueuedAt: time.Now().UTC(), TraceID: id, SpanID: id,
	}
	if actor != "" {
		row.ActorUserID = &actor
	}
	require.NoError(t, database.GetOpsV2(f.store).InsertOperationV2(row))
}

func (f *credGuardFixture) opStatus(t *testing.T, id string) string {
	t.Helper()
	row, err := database.GetOpsV2(f.store).GetOperationV2(id)
	require.NoError(t, err)
	require.NotNil(t, row)
	return row.Status
}

// N-3. DELETE /audiobooks/purge-soft-deleted/preview/:id lets the user who
// started a preview cancel it with library.delete alone (the generic cancel
// needs settings.manage). It refuses another user's preview, a real purge and
// any other operation, and leaves their rows as they were.
func TestCancelPurgePreviewRoute(t *testing.T) {
	f := setupCredGuardServer(t)
	ownerID, ownerTok := f.libraryDeleteSession(t, "preview-owner")
	_, otherTok := f.libraryDeleteSession(t, "preview-other")
	onlyOrganize := f.exactPermGoneSession(t, "preview-organize", auth.PermLibraryOrganize)
	const base = "/api/v1/audiobooks/purge-soft-deleted/preview/"

	f.queuedPurgeRow(t, "prev-own", purgeSoftDeletedOpID, `{"dry_run":true,"delete_files":true}`, ownerID)
	f.queuedPurgeRow(t, "prev-default", purgeSoftDeletedOpID, `{"delete_files":true}`, ownerID)
	f.queuedPurgeRow(t, "real-purge", purgeSoftDeletedOpID, `{"dry_run":false,"trashed_before":"2026-10-26T00:00:00Z"}`, ownerID)
	f.queuedPurgeRow(t, "other-op", "maintenance.purge-deleted", `{"dry_run":true}`, ownerID)
	f.queuedPurgeRow(t, "no-actor", purgeSoftDeletedOpID, `{"dry_run":true}`, "")

	// The generic cancel is refused for this user: that is why the route exists.
	w := f.do(t, ownerTok, http.MethodDelete, "/api/v1/operations/v2/prev-own", nil)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// Permission: library.delete is required.
	w = f.do(t, onlyOrganize, http.MethodDelete, base+"prev-own", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// Someone else's preview: 403, and it stays queued.
	w = f.do(t, otherTok, http.MethodDelete, base+"prev-own", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Equal(t, "queued", f.opStatus(t, "prev-own"))

	// A real purge, another operation and an unknown id are not previews: 404,
	// even for the user who started the purge.
	for _, id := range []string{"real-purge", "other-op", "no-such-op"} {
		w = f.do(t, ownerTok, http.MethodDelete, base+id, nil)
		assert.Equal(t, http.StatusNotFound, w.Code, "%s: %s", id, w.Body.String())
	}
	assert.Equal(t, "queued", f.opStatus(t, "real-purge"), "a real purge is never cancelled here")
	assert.Equal(t, "queued", f.opStatus(t, "other-op"))

	// A row with no recorded actor is not provably this user's.
	w = f.do(t, ownerTok, http.MethodDelete, base+"no-actor", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// The requester cancels their own preview, one that says dry_run and one
	// that leaves it to the op's preview default.
	for _, id := range []string{"prev-own", "prev-default"} {
		w = f.do(t, ownerTok, http.MethodDelete, base+id, nil)
		assert.Equal(t, http.StatusNoContent, w.Code, "%s: %s", id, w.Body.String())
		assert.Equal(t, "canceled", f.opStatus(t, id))
	}
	// Cancelling again (the preview already ended) still answers 204: the goal,
	// no preview left running, is met.
	w = f.do(t, ownerTok, http.MethodDelete, base+"prev-own", nil)
	assert.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
}

// A preview enqueued through the real route carries the requester as its actor,
// which is what makes the cancel above work for the user who started it.
func TestPreviewRouteRecordsTheRequesterOnTheOperation(t *testing.T) {
	f := setupCredGuardServer(t)
	userID, tok := f.libraryDeleteSession(t, "preview-recorder")
	w := f.do(t, tok, http.MethodPost, "/api/v1/audiobooks/purge-soft-deleted/preview", nil)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var body struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	row, err := database.GetOpsV2(f.store).GetOperationV2(body.Data.ID)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.ActorUserID, "the preview op has no actor, so its requester cannot cancel it")
	assert.Equal(t, userID, *row.ActorUserID)
}
