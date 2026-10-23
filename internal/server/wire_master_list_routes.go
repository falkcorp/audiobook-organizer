// file: internal/server/wire_master_list_routes.go
// version: 1.1.0
// guid: 0c5b9e17-6a42-4d83-b1f7-9e8d2a4c6b35
// last-edited: 2026-10-23

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// wireMasterListRoutes registers the master-list upload (design:
// .claude/notes/2026-10-23-master-list-import-design.md, section 3). Admin only:
// a list is the input of an import that will write a user's read status, and
// the same admin who starts the Repairs plan uploads it. RequireAdmin gates the
// signed-in role; s.perm(PermUsersManage) is the same permission the Repairs
// fixer demands, and is what narrows an API key to its scopes (RequireAdmin
// alone looks only at the role, so a key scoped to library.view would pass).
// Both stay. The upload is an exact entry of credential_routes_test's
// exemptRoutes: it writes only the list's own keyspace, names no path and
// changes no credential.
func (s *Server) wireMasterListRoutes(protected *gin.RouterGroup) {
	adminOnly := protected.Group("")
	adminOnly.Use(servermiddleware.RequireAdmin())
	adminOnly.POST("/master-lists", s.perm(auth.PermUsersManage), s.uploadMasterList)
	adminOnly.GET("/master-lists", s.perm(auth.PermUsersManage), s.listMasterLists)
}
