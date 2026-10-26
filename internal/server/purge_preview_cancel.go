// file: internal/server/purge_preview_cancel.go
// version: 1.0.0
// guid: ad206e0f-d077-4aec-b3da-08af104ee0c5
// last-edited: 2026-10-26

package server

import (
	"encoding/json"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// cancelPurgePreview handles DELETE /audiobooks/purge-soft-deleted/preview/:id:
// the dialog's cancel of a preview its user closed or replaced. A preview with
// delete_files re-checks the library on disk, so an abandoned one is wasted
// work worth stopping.
//
// The generic DELETE /operations/v2/:id needs settings.manage, which a user
// holding library.delete (the permission to preview) may not have: for them every
// cancel was a 403 the dialog swallowed and the abandoned preview ran to the end.
// This route takes the preview's own permission and narrows what it may cancel
// instead: only an operation of the manual purge, only a preview (dry_run), and
// only one the signed-in user enqueued (the row's actor). A real purge is never
// cancelled here, and neither is another user's preview.
//
// 204 cancelled (or already finished), 403 someone else's, 404 unknown or not a
// purge preview.
func (s *Server) cancelPurgePreview(c *gin.Context) {
	id := c.Param("id")
	ops := database.GetOpsV2(s.Ops())
	if ops == nil || s.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}
	row, err := ops.GetOperationV2(id)
	if err != nil {
		httputil.InternalError(c, "failed to read the operation", err)
		return
	}
	if row == nil || row.DefID != purgeSoftDeletedOpID || !isPurgePreviewParams(row.Params) {
		// Not found rather than forbidden: this route says nothing about
		// operations that are not purge previews.
		httputil.RespondWithNotFound(c, "purge preview", id)
		return
	}
	requester := ""
	if u, ok := servermiddleware.CurrentUser(c); ok && u != nil {
		requester = u.ID
	}
	owner := ""
	if row.ActorUserID != nil {
		owner = *row.ActorUserID
	}
	// With authentication off nobody is signed in and no row carries an actor;
	// with it on, a row without an actor (queued before the actor was recorded)
	// is not provably the requester's, so it is left to settings.manage.
	if requester != owner || (requester == "" && config.AppConfig.EnableAuth) {
		httputil.RespondWithForbidden(c, "only the user who started this preview can cancel it here")
		return
	}
	if err := s.opRegistry.Cancel(id); err != nil && !errors.Is(err, opsregistry.ErrOpNotFound) {
		httputil.InternalError(c, "cancel failed", err)
		return
	}
	// ErrOpNotFound here is a row that already finished: nothing to cancel, and
	// the dialog's goal (no preview left running) is met.
	httputil.RespondWithNoContent(c)
}

// isPurgePreviewParams reports whether the stored params of a purge operation
// are a preview. An unreadable body is not one: nothing is cancelled on a guess.
func isPurgePreviewParams(raw string) bool {
	var p PurgeSoftDeletedOpParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return false
	}
	// The op's own default: dry_run omitted means preview.
	dry := true
	if p.DryRun != nil {
		dry = *p.DryRun
	} else if p.DryRunCamel != nil {
		dry = *p.DryRunCamel
	}
	return dry
}
