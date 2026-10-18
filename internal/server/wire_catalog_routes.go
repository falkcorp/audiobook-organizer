// file: internal/server/wire_catalog_routes.go
// version: 2.0.0
// guid: 8f4a2d61-0c7b-4e39-a5d2-6e1b9c3f8a07
// last-edited: 2026-10-18

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// wireCatalogRoutes registers the two retired author-catalog read routes.
//
// GET /catalog/entries and GET /catalog/entries/:id listed and fetched what
// catalog.harvest-authors wrote. Nothing in the web app, scripts, cmd or the
// agent tooling called them (01-P81j, owner decision D9), so they answer 410
// Gone via gone() for one release and are then removed. Each stub keeps its
// original library.view guard: the protected group still authenticates and
// s.perm still runs first, so only a caller who was allowed to read the
// catalog learns the route is gone. The harvest and coverage ops are
// unaffected; they read the catalog store directly.
func (s *Server) wireCatalogRoutes(protected *gin.RouterGroup) {
	protected.GET("/catalog/entries", s.perm(auth.PermLibraryView), gone(""))
	protected.GET("/catalog/entries/:id", s.perm(auth.PermLibraryView), gone(""))
}
