// file: internal/server/wire_review_routes.go
// version: 1.1.0
// guid: 5c8e1a37-9b24-4f60-83d1-7e2a9c4b6f18
// last-edited: 2026-07-13

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
	protected.POST("/review/bulk", s.perm(auth.PermLibraryEditMetadata), reviewH.BulkReviewAction)
	// Re-runs apply for items already marked approved. Exists because approving
	// while the global switch is off records the decision but never executes it, and
	// nothing else ever reads that state back.
	protected.POST("/review/replay-approved", s.perm(auth.PermLibraryEditMetadata), reviewH.ReplayApprovedItems)
}
