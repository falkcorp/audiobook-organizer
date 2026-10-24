// file: internal/server/wire_audiobooks_routes.go
// version: 1.10.2
// guid: c3d4e5f6-a7b8-9012-cdef-345678901234
// last-edited: 2026-10-28

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
	// Status of a list search that outlived its request (the 202 path).
	protected.GET("/search/:search_id", s.perm(auth.PermLibraryView), s.getSearchJob)
	protected.GET("/audiobooks/count", s.perm(auth.PermLibraryView), audiobooksH.CountAudiobooks)
	protected.GET("/audiobooks/facets", s.perm(auth.PermLibraryView), audiobooksH.AudiobookFacets)
	protected.GET("/audiobooks/soft-deleted", s.perm(auth.PermLibraryView), audiobooksH.ListSoftDeletedAudiobooks)
	protected.DELETE("/audiobooks/purge-soft-deleted", s.perm(auth.PermLibraryDelete), audiobooksH.PurgeSoftDeletedAudiobooks)
	protected.POST("/audiobooks/purge-soft-deleted/preview", s.perm(auth.PermLibraryDelete), audiobooksH.PreviewPurgeSoftDeleted)
	// Forget the journal records of file removals waiting for a folder that is
	// missing or empty. Records only, audited; same permission as the purge that
	// deletes files.
	// The list the dialog shows first, and the body the forget then sends back.
	protected.GET("/audiobooks/purge-soft-deleted/waiting-file-records", s.perm(auth.PermLibraryDelete), audiobooksH.ListWaitingPurgeFileRecords)
	protected.POST("/audiobooks/purge-soft-deleted/forget-waiting-file-records", s.perm(auth.PermLibraryDelete), audiobooksH.ForgetWaitingPurgeFileRecords)
	// Cancel a preview its requester abandoned. Same permission as the preview
	// (library.delete), not settings.manage like the generic operation cancel,
	// and only the requester's own preview op.
	protected.DELETE("/audiobooks/purge-soft-deleted/preview/:id", s.perm(auth.PermLibraryDelete), s.cancelPurgePreview)
	protected.POST("/audiobooks/:id/restore", s.perm(auth.PermLibraryOrganize), audiobooksH.RestoreAudiobook)
	// The owner's explicit "discard progress and purge" for one trashed book
	// the nightly purge keeps because a user has listening state on it.
	protected.POST("/audiobooks/:id/discard-progress-and-purge", s.perm(auth.PermLibraryDelete), audiobooksH.DiscardProgressAndPurge)
	// reconcile-files re-stats the book's files and corrects FileSize. It does
	// NOT re-read anything. The old /rescan alias for it is retired (01-P81d);
	// the stub below answers 410 and names reconcile-files as the replacement.
	protected.POST("/audiobooks/:id/reconcile-files", s.perm(auth.PermLibraryEditMetadata), audiobooksH.ReconcileAudiobookFiles)
	// Retired (01-P81d, owner decision D9): no caller in the repo. The stub is
	// removed after one release; auth and the permission guard run first.
	protected.POST("/audiobooks/:id/rescan", s.perm(auth.PermLibraryEditMetadata), gone("POST /api/v1/audiobooks/:id/reconcile-files"))
	// force-rescan is the real per-book forced re-read: it sets NeedsRescan and
	// the next scan re-reads exactly this book.
	protected.POST("/audiobooks/:id/force-rescan", s.perm(auth.PermLibraryEditMetadata), audiobooksH.ForceRescanAudiobook)
	protected.GET("/audiobooks/:id", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobook)
	protected.GET("/audiobooks/:id/tags", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookTags)
	protected.PUT("/audiobooks/:id", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UpdateAudiobook)
	protected.DELETE("/audiobooks/:id", s.perm(auth.PermLibraryDelete), audiobooksH.DeleteAudiobook)
	protected.GET("/audiobooks/:id/cover", s.perm(auth.PermLibraryView), audiobooksH.ServeAudiobookCover)
	protected.GET("/audiobooks/:id/cover-text", s.perm(auth.PermLibraryView), s.handleGetCoverText)
	protected.GET("/audiobooks/:id/segments", s.perm(auth.PermLibraryView), audiobooksH.ListAudiobookSegments)
	protected.GET("/audiobooks/:id/segments/:segmentId/tags", s.perm(auth.PermLibraryView), audiobooksH.GetSegmentTags)
	protected.GET("/audiobooks/:id/files", s.perm(auth.PermLibraryView), audiobooksH.ListBookFiles)
	protected.PATCH("/audiobooks/:id/files/:file_id", s.perm(auth.PermLibraryEditMetadata), audiobooksH.PatchBookFile)
	// Retired versions (PLAN.md section 3.2, PR 9): the versions of a shell book
	// that were moved onto this one, and the per-version restore, which answers
	// 501 until restores are journaled (handler_retired_versions.go).
	protected.GET("/audiobooks/:id/retired-versions", s.perm(auth.PermLibraryView), audiobooksH.ListRetiredVersions)
	protected.POST("/audiobooks/:id/retired-versions/:vid/restore", s.perm(auth.PermLibraryOrganize), audiobooksH.RestoreRetiredVersion)
	protected.GET("/audiobooks/:id/changelog", s.perm(auth.PermLibraryView), audiobooksH.GetBookChangelog)
	// Retired (01-P81d, owner decision D9): no caller in the repo. The stub is
	// removed after one release; auth and the permission guard run first.
	protected.GET("/audiobooks/:id/path-history", s.perm(auth.PermLibraryView), gone("GET /api/v1/audiobooks/:id/changelog"))
	protected.GET("/audiobooks/:id/external-ids", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookExternalIDs)
	protected.POST("/audiobooks/:id/extract-track-info", s.perm(auth.PermLibraryEditMetadata), audiobooksH.ExtractTrackInfo)
	protected.POST("/audiobooks/:id/relocate", s.perm(auth.PermLibraryOrganize), audiobooksH.RelocateBookFiles)
	// The batch routes stay on library.edit_metadata, which is right for a
	// metadata edit. What a request does to books' place in the library needs
	// what the single-book route for the same act needs, and the handler asks
	// for it once it has read the body (permittedForTrashEffects): hard delete
	// and soft delete (action "delete", marked_for_deletion=true, library_state
	// "deleted") need library.delete, restore (action "restore",
	// marked_for_deletion=false) needs library.organize.
	protected.POST("/audiobooks/batch", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchUpdateAudiobooks)
	protected.POST("/audiobooks/batch-operations", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchOperations)
	protected.GET("/tags", s.perm(auth.PermLibraryView), audiobooksH.ListAllUserTags)
	protected.GET("/audiobooks/:id/user-tags", s.perm(auth.PermLibraryView), audiobooksH.GetBookUserTags)
	protected.GET("/audiobooks/:id/tags-detailed", s.perm(auth.PermLibraryView), audiobooksH.GetBookTagsDetailed)
	protected.POST("/audiobooks/batch-tags", s.perm(auth.PermLibraryEditMetadata), audiobooksH.BatchUpdateTags)
	// Retired (01-P81d, owner decision D9): no caller in the repo. The stubs are
	// removed after one release; auth and each route's permission guard run first.
	protected.GET("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryView), gone(""))
	protected.POST("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryEditMetadata), gone(""))
	protected.DELETE("/audiobooks/:id/alternative-titles", s.perm(auth.PermLibraryDelete), gone(""))
	protected.GET("/audiobooks/:id/metadata-history", s.perm(auth.PermLibraryView), audiobooksH.GetBookMetadataHistory)
	protected.GET("/audiobooks/:id/metadata-history/:field", s.perm(auth.PermLibraryView), audiobooksH.GetFieldMetadataHistory)
	protected.POST("/audiobooks/:id/metadata-history/:field/undo", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UndoMetadataChange)
	protected.POST("/audiobooks/:id/undo-last-apply", s.perm(auth.PermLibraryEditMetadata), audiobooksH.UndoLastApply)
	protected.GET("/audiobooks/:id/field-states", s.perm(auth.PermLibraryView), audiobooksH.GetAudiobookFieldStates)
	protected.GET("/audiobooks/:id/changes", s.perm(auth.PermLibraryView), audiobooksH.GetBookChanges)
}
