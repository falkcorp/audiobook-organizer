// file: internal/server/wire_review_routes.go
// version: 1.3.0
// guid: 5c8e1a37-9b24-4f60-83d1-7e2a9c4b6f18
// last-edited: 2026-10-18

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
	"github.com/gin-gonic/gin"
)

// wireReviewRoutes registers the universal review-queue domain routes (PR-A1) on
// the protected group. Handler instantiation stays in wireHandlers.
//
// Reads (count/list) require library.view. Mutations (approve/reject/bulk) apply
// real library changes via producer apply-handlers, so they require the stricter
// library.edit-metadata guard, matching dedup's merge/dismiss mutations.
func (s *Server) wireReviewRoutes(protected *gin.RouterGroup, reviewH *reviewhandler.Handler) {
	protected.GET("/review/count", s.perm(auth.PermLibraryView), reviewH.GetReviewCount)
	protected.GET("/review/items", s.perm(auth.PermLibraryView), reviewH.ListReviewItems)
	protected.POST("/review/items/:id/approve", s.perm(auth.PermLibraryEditMetadata), reviewH.ApproveReviewItem)
	protected.POST("/review/items/:id/reject", s.perm(auth.PermLibraryEditMetadata), reviewH.RejectReviewItem)
	// Moves an item held by an interrupted apply to needs_attention (01-P81j). Same
	// permission as approve: it is a review decision, and it changes nothing on disk.
	protected.POST("/review/items/:id/abandon-apply", s.perm(auth.PermLibraryEditMetadata), reviewH.AbandonApply)
	protected.POST("/review/bulk", s.perm(auth.PermLibraryEditMetadata), reviewH.BulkReviewAction)
	// Retired (01-P81j, owner decision D9): POST /review/replay-approved re-ran
	// apply for every item already marked approved. Nothing in the web app,
	// scripts, cmd or the agent tooling called it. It answers 410 Gone via gone()
	// for one release and is then removed; the library.edit_metadata guard stays
	// first, so auth and the permission check run before the stub and only an
	// allowed caller learns it is gone. The replacement is approving the items
	// again (their recorded action is honoured), one call per list of ids.
	protected.POST("/review/replay-approved", s.perm(auth.PermLibraryEditMetadata),
		gone("POST /api/v1/review/bulk with {\"action\":\"approve\",\"ids\":[...]} (list the approved items with GET /api/v1/review/items?status=approved; items run their recorded action only while review_apply_enabled is on, and the response lists failed ones)"))
}
