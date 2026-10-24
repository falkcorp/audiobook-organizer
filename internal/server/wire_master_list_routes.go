// file: internal/server/wire_master_list_routes.go
// version: 1.3.0
// guid: 0c5b9e17-6a42-4d83-b1f7-9e8d2a4c6b35
// last-edited: 2026-10-24

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// wireMasterListRoutes registers the master-list routes (design:
// docs/design/2026-10-23-master-list-import-design.md, section 3). Admin only:
// a list is the input of an import that will write a user's read status, and
// the same admin who starts the Repairs plan uploads it. adminRole gates the
// signed-in role; s.perm(PermUsersManage) is the same permission the Repairs
// fixer demands for planning and applying, and is what narrows an API key to its
// scopes (the role check alone looks only at the role, so a key scoped to
// library.view would pass). Both stay. The upload and the delete are exact
// entries of credential_routes_test's exemptRoutes: they write only the list's
// own keyspace, name no path and change no credential.
//
// Who may delete: any caller that passes both gates, an admin holding
// users.manage, may delete ANY stored list, not only their own. Lists are shared
// admin input, not private: the Repairs plan takes any list_id from such a
// caller whoever uploaded it, so an uploader-only delete would protect nothing
// the plan does not already expose. (Retention is per uploader and prunes only
// the uploader's own lists.)
//
// What these gates do NOT cover: the rows of a plan made from a list. They are
// served by the Repairs lane (GET /repairs and GET /repairs/:fixer/plan/:op/rows),
// which needs only library.view and does not yet apply the fixer's
// RequiredPermissions to reads, so until falkcorp/audiobook-organizer#4148 merges
// any library.view holder can read a master-list plan's rows (the list rows, the
// target user and that user's current state on each matched book). That is the
// Repairs framework's gap, fixed in #4148, not something this route group can
// close.
func (s *Server) wireMasterListRoutes(protected *gin.RouterGroup) {
	adminOnly := protected.Group("")
	adminOnly.Use(s.adminRole())
	adminOnly.POST("/master-lists", s.perm(auth.PermUsersManage), s.uploadMasterList)
	adminOnly.GET("/master-lists", s.perm(auth.PermUsersManage), s.listMasterLists)
	// The records are listed under the same prefix as the lists; gin keeps the
	// DELETE /:id and GET /elsewhere trees apart by method.
	adminOnly.GET("/master-lists/elsewhere", s.perm(auth.PermUsersManage), s.listMasterListElsewhere)
	adminOnly.DELETE("/master-lists/:id", s.perm(auth.PermUsersManage), s.deleteMasterList)
}

// adminRole is servermiddleware.RequireAdmin as wired for routes that must also
// work with authentication disabled. With EnableAuth=false no request carries a
// user, so RequireAdmin answers every one with 401; s.perm already passes
// everything in that mode, and adminRole does the same (read once, at wiring,
// like s.perm). With authentication on it IS RequireAdmin.
//
// It is deliberately not a change to RequireAdmin itself. RequireAdmin also
// guards the retired library wipe stub, the cache introspection route, the
// admin debug API and the destructive store-reset routes under /system (which
// chose the admin role over a permission precisely so a custom role cannot reach
// them); with auth disabled those answer 401 today, and making them reachable
// without a login is a separate decision for their owner, not a side effect of
// this PR.
func (s *Server) adminRole() gin.HandlerFunc {
	if !config.AppConfig.EnableAuth {
		return func(c *gin.Context) { c.Next() }
	}
	return servermiddleware.RequireAdmin()
}
