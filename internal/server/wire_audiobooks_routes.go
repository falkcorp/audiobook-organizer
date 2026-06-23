// file: internal/server/wire_audiobooks_routes.go
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9012-cdef-345678901234
// last-edited: 2026-06-23

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	audiobookshandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/audiobooks"
	"github.com/gin-gonic/gin"
)

// wireAudiobooksRoutes registers the audiobooks CRUD + tags routes on the
// protected group. Handler instantiation stays in wireHandlers.
func (s *Server) wireAudiobooksRoutes(
	protected *gin.RouterGroup,
	audiobooksH *audiobookshandler.Handler,
) {
	// Audiobooks domain (main library list / CRUD; migrated from
	// server_lifecycle.go). Paths + permission guards copied verbatim. Sibling
	// /audiobooks/:id/* routes owned by OTHER domains (quarantine, rating,
	// sample, organize/rename, versions, metadata, itunes, parse-with-ai, the
	// batch-write-back/bulk-write-back endpoints) stay in server_lifecycle.go.
	protected.GET("/audiobooks", s.perm(auth.PermLibraryView), audiobooksH.ListAudiobooks)
	protected.GET("/audiobooks/count", s.perm(auth.PermLibraryView), audiobooksH.CountAudiobooks)
	protected.GET("/audiobooks/facets", s.perm(auth.PermLibraryView), audiobooksH.AudiobookFacets)
	protected.GET("/audiobooks/soft-deleted", s.perm(auth.PermLibraryView), audiobooksH.ListSoftDeletedAudiobooks)
	protected.DELETE("/audiobooks/purge-soft-deleted", s.perm(auth.PermLibraryDelete), audiobooksH.PurgeSoftDeletedAudiobooks)
	protected.POST("/audiobooks/:id/restore", s.perm(auth.PermLibraryOrganize), audiobooksH.RestoreAudiobook)
	protected.POST("/audiobooks/:id/rescan", s.perm(auth.PermLibraryEditMetadata), audiobooksH.RescanAudiobook)
	protected.GET("/audiobooks/:id", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobook)
	protected.GET("/audiobooks/:id/tags", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookTags)
	protected.PUT("/audiobooks/:id", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UpdateAudiobook)
	protected.DELETE("/audiobooks/:id", s.perm(auth.PermLibraryDelete), audiobooksH.DeleteAudiobook)
	protected.GET("/audiobooks/:id/cover", s.perm(auth.PermLibraryView), audiobooksH.ServeAudiobookCover)
	protected.GET("/audiobooks/:id/segments", s.perm(auth.PermLibraryView), audiobooksH.ListAudiobookSegments)
	protected.GET("/audiobooks/:id/segments/:segmentId/tags", s.perm(auth.PermLibraryView), audiobooksH.GetSegmentTags)
	protected.GET("/audiobooks/:id/files", s.perm(auth.PermLibraryView), audiobooksH.ListBookFiles)
	protected.PATCH("/audiobooks/:id/files/:file_id", s.perm(auth.PermLibraryEditMetadata), audiobooksH.PatchBookFile)
	protected.GET("/audiobooks/:id/changelog", s.perm(auth.PermLibraryView), audiobooksH.GetBookChangelog)
	protected.GET("/audiobooks/:id/path-history", s.perm(auth.PermLibraryView), audiobooksH.GetBookPathHistory)
	protected.GET("/audiobooks/:id/external-ids", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookExternalIDs)
	protected.POST("/audiobooks/:id/extract-track-info", s.perm(auth.PermLibraryEditMetadata), audiobooksH.ExtractTrackInfo)
	protected.POST("/audiobooks/:id/relocate", s.perm(auth.PermLibraryOrganize), audiobooksH.RelocateBookFiles)
	protected.POST("/audiobooks/batch", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchUpdateAudiobooks)
	protected.POST("/audiobooks/batch-operations", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchOperations)
	protected.GET("/tags", s.perm(auth.PermLibraryView), audiobooksH.ListAllUserTags)
	protected.GET("/audiobooks/:id/user-tags", s.perm(auth.PermLibraryView), audiobooksH.GetBookUserTags)
	protected.GET("/audiobooks/:id/tags-detailed", s.perm(auth.PermLibraryView), audiobooksH.GetBookTagsDetailed)
	protected.POST("/audiobooks/batch-tags", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchUpdateTags)
	protected.GET("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryView), audiobooksH.GetBookAlternativeTitles)
	protected.POST("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryEditMetadata), audiobooksH.AddBookAlternativeTitle)
	protected.DELETE("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryDelete), audiobooksH.RemoveBookAlternativeTitle)
	protected.GET("/audiobooks/:id/metadata-history", s.perm(auth.PermLibraryView), audiobooksH.GetBookMetadataHistory)
	protected.GET("/audiobooks/:id/metadata-history/:field", s.perm(auth.PermLibraryView), audiobooksH.GetFieldMetadataHistory)
	protected.POST("/audiobooks/:id/metadata-history/:field/undo", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UndoMetadataChange)
	protected.POST("/audiobooks/:id/undo-last-apply", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UndoLastApply)
	protected.GET("/audiobooks/:id/field-states", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookFieldStates)
	protected.GET("/audiobooks/:id/changes", s.perm(auth.PermLibraryView), audiobooksH.GetBookChanges)
}
