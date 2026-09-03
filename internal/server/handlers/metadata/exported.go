// file: internal/server/handlers/metadata/exported.go
// version: 1.0.0
// guid: 34fcf0d9-304d-4ce1-8020-bb03430c90a7
// last-edited: 2026-06-03

// Exported HTTP entry points for the metadata-domain Handler. Each delegates to
// the unexported *Impl method that holds the original handler body verbatim, so
// the bodies read identically to the server-package originals while the router
// in wire_handlers.go binds to stable exported names. One exported method per
// route (19 total), matching the 19 routes relocated out of server_lifecycle.go.

package metadatahandler

import "github.com/gin-gonic/gin"

// BatchUpdateMetadata handles POST /api/v1/metadata/batch-update.
func (h *Handler) BatchUpdateMetadata(c *gin.Context) { h.batchUpdateMetadataImpl(c) }

// ValidateMetadata handles POST /api/v1/metadata/validate.
func (h *Handler) ValidateMetadata(c *gin.Context) { h.validateMetadataImpl(c) }

// ExportMetadata handles GET /api/v1/metadata/export.
func (h *Handler) ExportMetadata(c *gin.Context) { h.exportMetadataImpl(c) }

// ImportMetadata handles POST /api/v1/metadata/import.
func (h *Handler) ImportMetadata(c *gin.Context) { h.importMetadataImpl(c) }

// SearchMetadata handles GET /api/v1/metadata/search.
func (h *Handler) SearchMetadata(c *gin.Context) { h.searchMetadataImpl(c) }

// FetchAudiobookMetadata handles POST /api/v1/audiobooks/:id/fetch-metadata.
func (h *Handler) FetchAudiobookMetadata(c *gin.Context) { h.fetchAudiobookMetadataImpl(c) }

// SearchAudiobookMetadata handles POST /api/v1/audiobooks/:id/search-metadata.
func (h *Handler) SearchAudiobookMetadata(c *gin.Context) { h.searchAudiobookMetadataImpl(c) }

// ApplyAudiobookMetadata handles POST /api/v1/audiobooks/:id/apply-metadata.
func (h *Handler) ApplyAudiobookMetadata(c *gin.Context) { h.applyAudiobookMetadataImpl(c) }

// MarkAudiobookNoMatch handles POST /api/v1/audiobooks/:id/mark-no-match.
func (h *Handler) MarkAudiobookNoMatch(c *gin.Context) { h.markAudiobookNoMatchImpl(c) }

// HandleGetMetadataRejections handles GET /api/v1/audiobooks/:id/metadata-rejections.
func (h *Handler) HandleGetMetadataRejections(c *gin.Context) { h.handleGetMetadataRejectionsImpl(c) }

// RevertAudiobookMetadata handles POST /api/v1/audiobooks/:id/revert-metadata.
func (h *Handler) RevertAudiobookMetadata(c *gin.Context) { h.revertAudiobookMetadataImpl(c) }

// ListBookCOWVersions handles GET /api/v1/audiobooks/:id/cow-versions.
func (h *Handler) ListBookCOWVersions(c *gin.Context) { h.listBookCOWVersionsImpl(c) }

// PruneBookCOWVersions handles POST /api/v1/audiobooks/:id/cow-versions/prune.
func (h *Handler) PruneBookCOWVersions(c *gin.Context) { h.pruneBookCOWVersionsImpl(c) }

// WriteBackAudiobookMetadata handles POST /api/v1/audiobooks/:id/write-back.
func (h *Handler) WriteBackAudiobookMetadata(c *gin.Context) { h.writeBackAudiobookMetadataImpl(c) }

// BulkFetchMetadata handles POST /api/v1/metadata/bulk-fetch.
func (h *Handler) BulkFetchMetadata(c *gin.Context) { h.bulkFetchMetadataImpl(c) }

// HandleBulkWriteBack handles POST /api/v1/audiobooks/bulk-write-back.
func (h *Handler) HandleBulkWriteBack(c *gin.Context) { h.handleBulkWriteBackImpl(c) }

// BatchWriteBackAudiobooks handles POST /api/v1/audiobooks/batch-write-back.
func (h *Handler) BatchWriteBackAudiobooks(c *gin.Context) { h.batchWriteBackAudiobooksImpl(c) }

// GetMetadataFields handles GET /api/v1/metadata/fields.
func (h *Handler) GetMetadataFields(c *gin.Context) { h.getMetadataFieldsImpl(c) }

// HandleUpdateBookRating handles PATCH /api/v1/audiobooks/:id/rating.
func (h *Handler) HandleUpdateBookRating(c *gin.Context) { h.handleUpdateBookRatingImpl(c) }

// ListProviderThrottles handles GET /api/v1/metadata/providers/throttles.
func (h *Handler) ListProviderThrottles(c *gin.Context) { h.listProviderThrottlesImpl(c) }

// ClearProviderThrottle handles DELETE /api/v1/metadata/providers/throttles/:id.
func (h *Handler) ClearProviderThrottle(c *gin.Context) { h.clearProviderThrottleImpl(c) }

// ClearAllProviderThrottles handles DELETE /api/v1/metadata/providers/throttles.
func (h *Handler) ClearAllProviderThrottles(c *gin.Context) { h.clearAllProviderThrottlesImpl(c) }
