// file: internal/server/wire_repairs_routes.go
// version: 1.4.0
// guid: 4a9c1e62-3f75-4d08-b8a6-5e2d7c0b9f13
// last-edited: 2026-10-24

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	repairshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/repairs"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// wireRepairsRoutes registers the Repairs lane routes on the protected group.
// The route gates are library.view for the two list reads and the rows read,
// and library.edit_metadata for starting a plan or an apply (which change, or
// lead to changing, the library), matching the review-queue mutations. They
// are only the floor: once the handler knows the fixer it checks every
// permission the fixer requires (repairs.RequiredPermissions: the lane's
// library.edit_metadata, or what a repairs.PermissionScoped fixer names, e.g.
// the missing-files fixer's settings.manage), with the same semantics as
// s.perm, on the plan, apply and owner-apply POSTs and also on the rows read,
// because plan rows carry other users' data and a guest holds library.view.
// GET /repairs lists every fixer to a library.view caller but leaves the
// last-run pointers off the ones the caller may not use.
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
		return servermiddleware.HoldsPermission(store, c, p)
	}
}
