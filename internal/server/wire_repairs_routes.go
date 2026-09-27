// file: internal/server/wire_repairs_routes.go
// version: 1.0.0
// guid: 4a9c1e62-3f75-4d08-b8a6-5e2d7c0b9f13
// last-edited: 2026-09-27

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	repairshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/repairs"
)

// wireRepairsRoutes registers the Repairs lane routes on the protected group.
// Reads need library.view; starting a plan or an apply changes (or leads to
// changing) the library, so both POSTs need library.edit_metadata, matching
// the review-queue mutations.
func (s *Server) wireRepairsRoutes(protected *gin.RouterGroup) {
	var enq repairshandler.Enqueuer
	if s.opRegistry != nil {
		enq = s.opRegistry
	}
	h := repairshandler.New(s.repairFixers, enq, s.storeForWiring())
	protected.GET("/repairs", s.perm(auth.PermLibraryView), h.ListFixers)
	protected.POST("/repairs/:fixer/plan", s.perm(auth.PermLibraryEditMetadata), h.StartPlan)
	protected.GET("/repairs/:fixer/plan/:op_id/rows", s.perm(auth.PermLibraryView), h.ListPlanRows)
	protected.POST("/repairs/:fixer/apply", s.perm(auth.PermLibraryEditMetadata), h.StartApply)
}
