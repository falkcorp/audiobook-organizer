// file: internal/server/wire_metadata_routes.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0123-defa-456789012345
// last-edited: 2026-06-23

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	"github.com/gin-gonic/gin"
)

// wireMetadataRoutes registers the metadata domain routes on the protected group.
// Handler instantiation stays in wireHandlers.
func (s *Server) wireMetadataRoutes(
	protected *gin.RouterGroup,
	metadataH *metadatahandler.Handler,
) {
	// Metadata domain (handlers/metadata) — 19 routes relocated from
	// server_lifecycle.go. EXACT paths + perm guards preserved.
	protected.POST("/metadata/batch-update", s.perm(auth.PermLibraryEditMetadata), metadataH.BatchUpdateMetadata)
	protected.POST("/metadata/validate", s.perm(auth.PermLibraryEditMetadata), metadataH.ValidateMetadata)
	protected.GET("/metadata/export", s.perm(auth.PermLibraryView), metadataH.ExportMetadata)
	protected.POST("/metadata/import", s.perm(auth.PermLibraryEditMetadata), metadataH.ImportMetadata)
	protected.GET("/metadata/search", s.perm(auth.PermLibraryView), metadataH.SearchMetadata)
	protected.GET("/metadata/fields", s.perm(auth.PermLibraryView), metadataH.GetMetadataFields)
	protected.POST("/metadata/bulk-fetch", s.perm(auth.PermLibraryEditMetadata), metadataH.BulkFetchMetadata)
	protected.POST("/audiobooks/:id/fetch-metadata", s.perm(auth.PermLibraryEditMetadata), metadataH.FetchAudiobookMetadata)
	protected.POST("/audiobooks/:id/search-metadata", s.perm(auth.PermLibraryEditMetadata), metadataH.SearchAudiobookMetadata)
	protected.POST("/audiobooks/:id/apply-metadata", s.perm(auth.PermLibraryEditMetadata), metadataH.ApplyAudiobookMetadata)
	protected.POST("/audiobooks/:id/mark-no-match", s.perm(auth.PermLibraryEditMetadata), metadataH.MarkAudiobookNoMatch)
	protected.POST("/audiobooks/:id/revert-metadata", s.perm(auth.PermLibraryEditMetadata), metadataH.RevertAudiobookMetadata)
	protected.GET("/audiobooks/:id/metadata-rejections", s.perm(auth.PermLibraryView), metadataH.HandleGetMetadataRejections)
	protected.GET("/audiobooks/:id/cow-versions", s.perm(auth.PermLibraryView), metadataH.ListBookCOWVersions)
	protected.POST("/audiobooks/:id/cow-versions/prune", s.perm(auth.PermLibraryEditMetadata), metadataH.PruneBookCOWVersions)
	protected.POST("/audiobooks/:id/write-back", s.perm(auth.PermLibraryEditMetadata), metadataH.WriteBackAudiobookMetadata)
	protected.PATCH("/audiobooks/:id/rating", s.perm(auth.PermLibraryEditMetadata), metadataH.HandleUpdateBookRating)
	protected.POST("/audiobooks/batch-write-back", s.perm(auth.PermLibraryEditMetadata), metadataH.BatchWriteBackAudiobooks)
	protected.POST("/audiobooks/bulk-write-back", s.perm(auth.PermLibraryEditMetadata), metadataH.HandleBulkWriteBack)
}
