// file: internal/server/wire_dedup_routes.go
// version: 1.6.0
// guid: b8c9d0e1-f2a3-4567-bcde-890123456789
// last-edited: 2026-09-25

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
	// Hand-picked pairs into the review queue (layer/source "manual"); dry_run defaults true.
	protected.POST("/dedup/candidates", s.perm(auth.PermLibraryEditMetadata), dedupH.EnqueueManualDedupCandidates)
	protected.GET("/dedup/candidates/export", s.perm(auth.PermLibraryView), dedupH.ExportDedupCandidates)
	protected.GET("/dedup/stats", s.perm(auth.PermLibraryView), dedupH.GetDedupStats)
	// T016: breakdown and rescore endpoints (frozen API contract for T017).
	protected.GET("/dedup/candidates/:id/breakdown", s.perm(auth.PermLibraryView), dedupH.GetDedupCandidateBreakdown)
	protected.POST("/dedup/rescore", s.perm(auth.PermScanTrigger), dedupH.RescoreDedupCandidates)
	protected.POST("/dedup/purge-acoustid-conflicts", s.perm(auth.PermScanTrigger), dedupH.PurgeAcoustIDConflicts)
	// naming-audit classes "merge vs link verbs" and "dismiss vs reject vs
	// undo" (docs/audits/2026-09-25-interface-naming-consistency.md classes
	// 2/3): these "merge"/"dismiss" endpoints link books into a version group
	// or reject a candidate, never move a file — renamed to say so. The old
	// paths are kept registered as DEPRECATED aliases pointing at the same
	// handlers, matching the /rescan alias pattern in wire_audiobooks_routes.go.
	protected.POST("/dedup/candidates/:id/link", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCandidate)
	protected.POST("/dedup/candidates/:id/merge", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCandidate) // DEPRECATED alias for /link
	protected.POST("/dedup/candidates/:id/reject", s.perm(auth.PermLibraryEditMetadata), dedupH.RejectDedupCandidate)
	protected.POST("/dedup/candidates/:id/dismiss", s.perm(auth.PermLibraryEditMetadata), dedupH.RejectDedupCandidate) // DEPRECATED alias for /reject
	protected.POST("/dedup/candidates/bulk-link", s.perm(auth.PermLibraryEditMetadata), dedupH.BulkLinkDedupCandidates)
	protected.POST("/dedup/candidates/bulk-merge", s.perm(auth.PermLibraryEditMetadata), dedupH.BulkLinkDedupCandidates) // DEPRECATED alias for /bulk-link
	protected.POST("/dedup/candidates/link-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCluster)
	protected.POST("/dedup/candidates/merge-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCluster) // DEPRECATED alias for /link-cluster
	protected.POST("/dedup/candidates/reject-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.RejectDedupCluster)
	protected.POST("/dedup/candidates/dismiss-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.RejectDedupCluster) // DEPRECATED alias for /reject-cluster
	protected.POST("/dedup/candidates/remove-from-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.RemoveFromDedupCluster)
	protected.GET("/dedup/candidates/series-summary", s.perm(auth.PermLibraryView), dedupH.ListDedupCandidateSeries)
	// C6 — gold-dataset review (the dedup feedback-loop labels).
	protected.GET("/dedup/labels", s.perm(auth.PermLibraryView), dedupH.ListDedupLabels)
	protected.GET("/dedup/labels/stats", s.perm(auth.PermLibraryView), dedupH.GetDedupLabelStats)
	protected.GET("/dedup/labels/export", s.perm(auth.PermLibraryView), dedupH.ExportLabeledExamples)         // C7 — JSONL export of the labeled dataset.
	protected.GET("/dedup/labels/suspicious", s.perm(auth.PermLibraryView), dedupH.ListSuspiciousDedupLabels) // INIT-1 T4 — read-only suspicious-label review queue.
	protected.POST("/dedup/labels/:id/override", s.perm(auth.PermLibraryEditMetadata), dedupH.OverrideDedupLabel)
	protected.POST("/dedup/candidates/link-series", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCandidateSeries)
	protected.POST("/dedup/candidates/merge-series", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCandidateSeries) // DEPRECATED alias for /link-series
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
	// naming-audit classes "merge vs link verbs" and "dismiss vs reject vs
	// undo" (docs/audits/2026-09-25-interface-naming-consistency.md classes
	// 2/3): despite the old verb "merge", this links the duplicates as
	// versions — it never moves a file. The old paths are kept registered as
	// DEPRECATED aliases pointing at the same handlers.
	protected.POST("/audiobooks/duplicates/link", s.perm(auth.PermLibraryEditMetadata), duplicatesH.LinkBookDuplicatesAsVersions)
	protected.POST("/audiobooks/duplicates/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.LinkBookDuplicatesAsVersions) // DEPRECATED alias for /link
	protected.POST("/audiobooks/duplicates/reject", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RejectBookDuplicateGroup)
	protected.POST("/audiobooks/duplicates/dismiss", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RejectBookDuplicateGroup) // DEPRECATED alias for /reject
	protected.GET("/authors/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListDuplicateAuthors)
	protected.POST("/authors/duplicates/refresh", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RefreshDuplicateAuthors)
	protected.POST("/audiobooks/link", s.perm(auth.PermLibraryEditMetadata), duplicatesH.LinkBooks)
	protected.POST("/audiobooks/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.LinkBooks) // DEPRECATED alias for /link
	protected.POST("/audiobooks/combine", s.perm(auth.PermLibraryEditMetadata), duplicatesH.CombineBooks)
	// Combine undo: every combine (manual or review-queue combine/duplicate-of)
	// is journaled; these list the journals and reverse one.
	protected.GET("/merge/combine-journal", s.perm(auth.PermLibraryView), duplicatesH.ListCombineJournals)
	protected.POST("/merge/undo/:journal_id", s.perm(auth.PermLibraryEditMetadata), duplicatesH.UndoCombine)
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
