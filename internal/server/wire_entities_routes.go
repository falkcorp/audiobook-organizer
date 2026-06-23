// file: internal/server/wire_entities_routes.go
// version: 1.0.0
// guid: e5f6a7b8-c9d0-1234-efab-567890123456
// last-edited: 2026-06-23

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	entities "github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/gin-gonic/gin"
)

// wireEntitiesRoutes registers the entities domain routes (authors, narrators,
// series, works) on the protected group. Handler instantiation stays in wireHandlers.
func (s *Server) wireEntitiesRoutes(
	protected *gin.RouterGroup,
	entitiesH *entities.Handler,
) {
	// Entities domain (migrated from server_lifecycle.go): authors, narrators,
	// series, and works. Paths + permission guards copied verbatim. Sibling
	// /authors/duplicates*, /series/duplicates*, /authors/duplicates/ai-review*
	// (now aiH.*) and the entity-tag routes stay on *Server / their own handlers.
	protected.GET("/authors", s.perm(auth.PermLibraryView), entitiesH.ListAuthors)
	protected.GET("/authors/count", s.perm(auth.PermLibraryView), entitiesH.CountAuthors)
	protected.POST("/authors/merge", s.perm(auth.PermLibraryEditMetadata), entitiesH.MergeAuthors)
	protected.POST("/authors/:id/reclassify-as-narrator", s.perm(auth.PermLibraryEditMetadata), entitiesH.ReclassifyAuthorAsNarrator)
	protected.PUT("/authors/:id/name", s.perm(auth.PermLibraryEditMetadata), entitiesH.RenameAuthor)
	protected.POST("/authors/:id/split", s.perm(auth.PermLibraryEditMetadata), entitiesH.SplitCompositeAuthor)
	protected.POST("/authors/:id/resolve-production", s.perm(auth.PermLibraryEditMetadata), entitiesH.ResolveProductionAuthor)
	protected.GET("/authors/:id/aliases", s.perm(auth.PermLibraryView), entitiesH.GetAuthorAliases)
	protected.POST("/authors/:id/aliases", s.perm(auth.PermLibraryEditMetadata), entitiesH.CreateAuthorAlias)
	protected.DELETE("/authors/:id/aliases/:aliasId", s.perm(auth.PermLibraryDelete), entitiesH.DeleteAuthorAlias)
	protected.GET("/authors/:id/books", s.perm(auth.PermLibraryView), entitiesH.GetAuthorBooks)
	protected.DELETE("/authors/:id", s.perm(auth.PermLibraryDelete), entitiesH.DeleteAuthor)
	protected.POST("/authors/bulk-delete", s.perm(auth.PermLibraryDelete), entitiesH.BulkDeleteAuthors)

	protected.GET("/narrators", s.perm(auth.PermLibraryView), entitiesH.ListNarrators)
	protected.GET("/narrators/count", s.perm(auth.PermLibraryView), entitiesH.CountNarrators)
	protected.GET("/audiobooks/:id/narrators", s.perm(auth.PermLibraryView), entitiesH.ListAudiobookNarrators)
	protected.PUT("/audiobooks/:id/narrators", s.perm(auth.PermLibraryEditMetadata), entitiesH.SetAudiobookNarrators)

	protected.GET("/series", s.perm(auth.PermLibraryView), entitiesH.ListSeries)
	protected.GET("/series/count", s.perm(auth.PermLibraryView), entitiesH.CountSeries)
	protected.PATCH("/series/:id", s.perm(auth.PermLibraryEditMetadata), entitiesH.UpdateSeriesName)
	protected.GET("/series/:id/books", s.perm(auth.PermLibraryView), entitiesH.GetSeriesBooks)
	protected.PUT("/series/:id/name", s.perm(auth.PermLibraryEditMetadata), entitiesH.RenameSeries)
	protected.POST("/series/:id/split", s.perm(auth.PermLibraryEditMetadata), entitiesH.SplitSeries)
	protected.DELETE("/series/:id", s.perm(auth.PermLibraryDelete), entitiesH.DeleteEmptySeries)
	protected.POST("/series/bulk-delete", s.perm(auth.PermLibraryDelete), entitiesH.BulkDeleteSeries)

	protected.GET("/works", s.perm(auth.PermLibraryView), entitiesH.ListWorks)
	protected.POST("/works", s.perm(auth.PermLibraryEditMetadata), entitiesH.CreateWork)
	protected.GET("/works/:id", s.perm(auth.PermLibraryView), entitiesH.GetWork)
	protected.PUT("/works/:id", s.perm(auth.PermLibraryEditMetadata), entitiesH.UpdateWork)
	protected.DELETE("/works/:id", s.perm(auth.PermLibraryDelete), entitiesH.DeleteWork)
	protected.GET("/works/:id/books", s.perm(auth.PermLibraryView), entitiesH.ListWorkBooks)
	protected.GET("/work", s.perm(auth.PermLibraryView), entitiesH.ListWork)
	protected.GET("/work/stats", s.perm(auth.PermLibraryView), entitiesH.GetWorkStats)
}
