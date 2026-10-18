// file: internal/server/entity_tag_handlers.go
// version: 3.1.0
// guid: 7e5f6a4b-8c9d-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-18
//
// Retired author and series tag endpoints (01-P81e). The handlers were
// deleted; the routes stay as 410 Gone stubs for one release.

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/gin-gonic/gin"
)

// registerEntityTagRoutes registers the retired author/series tag endpoints as
// 410 Gone stubs. Nothing in the repo called them. The protected group's auth
// middleware answers 401 to an unauthenticated caller, and s.perm answers 403
// to an authenticated caller who lacks the permission, both before the stub
// runs, so only an allowed caller learns the route is gone.
func (s *Server) registerEntityTagRoutes(protected *gin.RouterGroup) {
	protected.GET("/authors/:id/tags", s.perm(auth.PermLibraryView), gone(""))
	protected.POST("/authors/:id/tags", s.perm(auth.PermLibraryEditMetadata), gone(""))
	protected.GET("/series/:id/tags", s.perm(auth.PermLibraryView), gone(""))
	protected.POST("/series/:id/tags", s.perm(auth.PermLibraryEditMetadata), gone(""))
}
