// file: internal/server/wire_dedup_routes.go
// version: 1.0.0
// guid: b8c9d0e1-f2a3-4567-bcde-890123456789
// last-edited: 2026-06-23

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
	duplicates "github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	"github.com/gin-gonic/gin"
)

// wireDedupRoutes registers the embedding-based dedup domain routes and the
// SQL-backed duplicates domain routes on the protected group.
// Handler instantiation stays in wireHandlers.
func (s *Server) wireDedupRoutes(
	protected *gin.RouterGroup,
	dedupH *deduphandler.Handler,
	duplicatesH *duplicates.Handler,
) {
	// Embedding-based dedup domain routes (migrated from server_lifecycle.go).
	// The split-book /dedup/* routes (registered in wireLibraryRoutes) and the
	// /dedup/fingerprint-rescan + /dedup/validate survivors stay where they are.
	protected.GET("/dedup/candidates", s.perm(auth.PermLibraryView), dedupH.ListDedupCandidates)
	protected.GET("/dedup/candidates/export", s.perm(auth.PermLibraryView), dedupH.ExportDedupCandidates)
	protected.GET("/dedup/stats", s.perm(auth.PermLibraryView), dedupH.GetDedupStats)
	// T016: breakdown and rescore endpoints (frozen API contract for T017).
	protected.GET("/dedup/candidates/:id/breakdown", s.perm(auth.PermLibraryView), dedupH.GetDedupCandidateBreakdown)
	protected.POST("/dedup/rescore", s.perm(auth.PermScanTrigger), dedupH.RescoreDedupCandidates)
	protected.POST("/dedup/candidates/:id/merge", s.perm(auth.PermLibraryEditMetadata), dedupH.MergeDedupCandidate)
	protected.POST("/dedup/candidates/:id/dismiss", s.perm(auth.PermLibraryEditMetadata), dedupH.DismissDedupCandidate)
	protected.POST("/dedup/candidates/bulk-merge", s.perm(auth.PermLibraryEditMetadata), dedupH.BulkMergeDedupCandidates)
	protected.POST("/dedup/candidates/merge-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.MergeDedupCluster)
	protected.POST("/dedup/candidates/dismiss-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.DismissDedupCluster)
	protected.POST("/dedup/candidates/remove-from-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.RemoveFromDedupCluster)
	protected.GET("/dedup/candidates/series-summary", s.perm(auth.PermLibraryView), dedupH.ListDedupCandidateSeries)
	// C6 — gold-dataset review (the dedup feedback-loop labels).
	protected.GET("/dedup/labels", s.perm(auth.PermLibraryView), dedupH.ListDedupLabels)
	protected.GET("/dedup/labels/stats", s.perm(auth.PermLibraryView), dedupH.GetDedupLabelStats)
	protected.POST("/dedup/labels/:id/override", s.perm(auth.PermLibraryEditMetadata), dedupH.OverrideDedupLabel)
	protected.POST("/dedup/candidates/merge-series", s.perm(auth.PermLibraryEditMetadata), dedupH.MergeDedupCandidateSeries)
	protected.POST("/dedup/scan", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupScan)
	protected.POST("/dedup/scan-llm", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupLLM)
	protected.POST("/dedup/scan-acoustid", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupAcoustID)
	protected.POST("/audiobooks/:id/compare-acoustid", s.perm(auth.PermLibraryView), dedupH.HandleCompareAcoustID)
	protected.POST("/dedup/scan-book-signature", s.perm(auth.PermScanTrigger), dedupH.TriggerBookSignatureScan)
	protected.POST("/dedup/refresh", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupRefresh)
	protected.POST("/dedup/purge-stale", s.perm(auth.PermScanTrigger), dedupH.PurgeStaleCandidates)
	protected.POST("/dedup/purge-legacy-fp", s.perm(auth.PermScanTrigger), dedupH.PurgeLegacyFPCandidates)
	protected.POST("/dedup/reset-acoustid", s.perm(auth.PermScanTrigger), dedupH.ResetAcoustIDFingerprints)
	protected.POST("/dedup/embed", s.perm(auth.PermScanTrigger), dedupH.TriggerEmbedScan)
	protected.POST("/dedup/embed-async", s.perm(auth.PermScanTrigger), dedupH.TriggerEmbedAsync)
	protected.POST("/dedup/lsh-index", s.perm(auth.PermScanTrigger), dedupH.TriggerLSHIndexBuild)
	protected.POST("/dedup/emb-reencode", s.perm(auth.PermScanTrigger), dedupH.EmbReeencode) // T021: float16+zstd re-encode op

	// Duplicates domain (SQL-backed dup detection, series prune/normalize,
	// dedup-entry validation; migrated from server_lifecycle.go). Paths + permission
	// guards copied verbatim. The /authors/duplicates(/refresh), /series/duplicates(/refresh)
	// sibling routes were intentionally left here by the entities phase and are now
	// owned by this handler; /dedup/validate is the dedup-entry validator (distinct
	// from the embedding-based /dedup/* routes above and the split-book /dedup/* routes).
	protected.GET("/audiobooks/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListDuplicateAudiobooks)
	protected.GET("/audiobooks/duplicates/scan-results", s.perm(auth.PermLibraryView), duplicatesH.ListBookDuplicateScanResults)
	protected.POST("/audiobooks/duplicates/scan", s.perm(auth.PermLibraryEditMetadata), duplicatesH.ScanBookDuplicates)
	protected.POST("/audiobooks/duplicates/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.MergeBookDuplicatesAsVersions)
	protected.POST("/audiobooks/duplicates/dismiss", s.perm(auth.PermLibraryEditMetadata), duplicatesH.DismissBookDuplicateGroup)
	protected.GET("/authors/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListDuplicateAuthors)
	protected.POST("/authors/duplicates/refresh", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RefreshDuplicateAuthors)
	protected.POST("/audiobooks/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.MergeBooks)
	protected.POST("/audiobooks/combine", s.perm(auth.PermLibraryEditMetadata), duplicatesH.CombineBooks)
	protected.GET("/series/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListSeriesDuplicates)
	protected.POST("/series/duplicates/refresh", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RefreshSeriesDuplicates)
	protected.POST("/series/deduplicate", s.perm(auth.PermLibraryEditMetadata), duplicatesH.DeduplicateSeriesHandler)
	protected.POST("/series/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.MergeSeriesGroup)
	protected.GET("/series/prune/preview", s.perm(auth.PermLibraryView), duplicatesH.SeriesPrunePreview)
	protected.POST("/series/prune", s.perm(auth.PermLibraryEditMetadata), duplicatesH.SeriesPrune)
	protected.GET("/series/normalize/preview", s.perm(auth.PermLibraryView), duplicatesH.SeriesNormalizePreview)
	protected.POST("/series/normalize", s.perm(auth.PermLibraryEditMetadata), duplicatesH.SeriesNormalize)
	protected.POST("/dedup/validate", s.perm(auth.PermLibraryEditMetadata), duplicatesH.ValidateDedupEntry)
}
