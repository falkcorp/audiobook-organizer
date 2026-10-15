// file: internal/server/wire_repairs_routes.go
// version: 1.3.0
// guid: 4a9c1e62-3f75-4d08-b8a6-5e2d7c0b9f13
// last-edited: 2026-10-14

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	repairshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/repairs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// wireRepairsRoutes registers the Repairs lane routes on the protected group.
// Reads need library.view; starting a plan or an apply changes (or leads to
// changing) the library, so both POSTs need library.edit_metadata, matching
// the review-queue mutations. A fixer that needs more
// (repairs.PermissionScoped, e.g. the missing-files fixer's settings.manage)
// is checked by the handler once it knows the fixer, with the same semantics
// as s.perm.
func (s *Server) wireRepairsRoutes(protected *gin.RouterGroup) {
	var enq repairshandler.Enqueuer
	if s.opRegistry != nil {
		enq = s.opRegistry
	}
	h := repairshandler.New(s.repairFixers, enq, s.storeForWiring()).WithPermissionCheck(s.permCheck())
	protected.GET("/repairs", s.perm(auth.PermLibraryView), h.ListFixers)
	protected.GET("/repairs/owner-status", s.perm(auth.PermLibraryView), h.GetOwnerStatus)
	protected.POST("/repairs/:fixer/plan", s.perm(auth.PermLibraryEditMetadata), h.StartPlan)
	protected.GET("/repairs/:fixer/plan/:op_id/rows", s.perm(auth.PermLibraryView), h.ListPlanRows)
	protected.POST("/repairs/:fixer/apply", s.perm(auth.PermLibraryEditMetadata), h.StartApply)
	// The owner's own apply of one owner-only row: the handler also requires
	// a verified Cloudflare Access sign-in as owner_email, the admin role and
	// a same-origin request.
	protected.POST("/repairs/:fixer/owner-apply", s.perm(auth.PermLibraryEditMetadata), h.OwnerApply)
}

// permCheck is s.perm as a check a handler runs inline, for a permission it
// learns only from the request: every permission is held with auth disabled
// (read once, as s.perm does) and while no user exists (first run).
func (s *Server) permCheck() repairshandler.PermissionCheck {
	if !config.AppConfig.EnableAuth {
		return func(*gin.Context, auth.Permission) bool { return true }
	}
	store := s.storeForWiring()
	return func(c *gin.Context, p auth.Permission) bool {
		return servermiddleware.CheckPermission(store, c, p)
	}
}
