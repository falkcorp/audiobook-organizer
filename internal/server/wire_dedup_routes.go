// file: internal/server/wire_dedup_routes.go
// version: 1.13.0
// guid: b8c9d0e1-f2a3-4567-bcde-890123456789
// last-edited: 2026-10-18

package server

import (
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	deduphandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/dedup"
	duplicates "github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	"github.com/gin-gonic/gin"
)

// combineUndoGone is the replacement named by the retired POST
// /merge/undo/:journal_id stub. It reads inside gone()'s "use ... instead".
const combineUndoGone = "POST /api/v1/operations/:id/revert (for a combine a Repairs fixer made; a manual combine has no HTTP undo now)"

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
	// naming-audit classes "merge vs link verbs" and "dismiss vs reject vs
	// undo" (docs/audits/2026-09-25-interface-naming-consistency.md classes
	// 2/3): these endpoints link books into a version group or reject a
	// candidate, never move a file — named to say so. The old merge/dismiss
	// verb aliases were removed (01-P72).
	protected.POST("/dedup/candidates/:id/link", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCandidate)
	protected.POST("/dedup/candidates/:id/reject", s.perm(auth.PermLibraryEditMetadata), dedupH.RejectDedupCandidate)
	protected.POST("/dedup/candidates/bulk-link", s.perm(auth.PermLibraryEditMetadata), dedupH.BulkLinkDedupCandidates)
	// Filter-scoped reject for the review UI's cross-page selection; same filter body as bulk-link.
	protected.POST("/dedup/candidates/bulk-reject", s.perm(auth.PermLibraryEditMetadata), dedupH.BulkRejectDedupCandidates)
	protected.POST("/dedup/candidates/bulk-reject/revert", s.perm(auth.PermLibraryEditMetadata), dedupH.RevertBulkRejectDedupCandidates)
	// The count a cross-page confirmation shows and sends back as expected_total (writes nothing).
	protected.POST("/dedup/candidates/bulk-count", s.perm(auth.PermLibraryView), dedupH.BulkCountDedupCandidates)
	// The Clusters view's per-cluster "pairs on other pages" counts, one scan for the whole page (writes nothing).
	protected.POST("/dedup/candidates/book-pair-counts", s.perm(auth.PermLibraryView), dedupH.BookPairCounts)
	protected.POST("/dedup/candidates/link-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCluster)
	protected.POST("/dedup/candidates/reject-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.RejectDedupCluster)
	protected.POST("/dedup/candidates/remove-from-cluster", s.perm(auth.PermLibraryEditMetadata), dedupH.RemoveFromDedupCluster)
	protected.GET("/dedup/candidates/series-summary", s.perm(auth.PermLibraryView), dedupH.ListDedupCandidateSeries)
	// C6 — gold-dataset review (the dedup feedback-loop labels).
	protected.GET("/dedup/labels", s.perm(auth.PermLibraryView), dedupH.ListDedupLabels)
	protected.GET("/dedup/labels/stats", s.perm(auth.PermLibraryView), dedupH.GetDedupLabelStats)
	protected.GET("/dedup/labels/export", s.perm(auth.PermLibraryView), dedupH.ExportLabeledExamples)         // C7 — JSONL export of the labeled dataset.
	protected.GET("/dedup/labels/suspicious", s.perm(auth.PermLibraryView), dedupH.ListSuspiciousDedupLabels) // INIT-1 T4 — read-only suspicious-label review queue.
	protected.POST("/dedup/labels/:id/override", s.perm(auth.PermLibraryEditMetadata), dedupH.OverrideDedupLabel)
	protected.POST("/dedup/candidates/link-series", s.perm(auth.PermLibraryEditMetadata), dedupH.LinkDedupCandidateSeries)
	protected.POST("/dedup/scan", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupScan)
	protected.POST("/dedup/scan-llm", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupLLM)
	protected.POST("/dedup/scan-acoustid", s.perm(auth.PermScanTrigger), dedupH.TriggerDedupAcoustID)
	protected.POST("/audiobooks/:id/compare-acoustid", s.perm(auth.PermLibraryView), dedupH.HandleCompareAcoustID)
	protected.POST("/dedup/purge-stale", s.perm(auth.PermScanTrigger), dedupH.PurgeStaleCandidates)
	protected.POST("/dedup/reset-acoustid", s.perm(auth.PermScanTrigger), dedupH.ResetAcoustIDFingerprints)
	protected.POST("/dedup/embed", s.perm(auth.PermScanTrigger), dedupH.TriggerEmbedScan)

	// Duplicates domain (SQL-backed dup detection, series prune/normalize,
	// dedup-entry validation; migrated from server_lifecycle.go). Paths + permission
	// guards copied verbatim. The /authors/duplicates(/refresh), /series/duplicates(/refresh)
	// sibling routes were intentionally left here by the entities phase and are now
	// owned by this handler; /dedup/validate is the dedup-entry validator (distinct
	// from the embedding-based /dedup/* routes above and the split-book /dedup/* routes).
	//
	// The five /audiobooks/duplicates routes and their two DEPRECATED aliases
	// (/merge for /link, /dismiss for /reject) served only the retired Version
	// Groups and Duplicate Scan tabs of the old /dedup page. They answer 410
	// Gone for one release (owner decision D9, 03-PR12) and are then removed.
	// Each keeps its old permission guard so a caller who could not use the
	// route still gets the same 401/403 and only an allowed caller learns it is
	// gone. The op dedup.book-scan behind POST /scan stays registered (04 owns
	// its fate); only its HTTP entry point is retired here.
	duplicatesGone := gone("GET /api/v1/dedup/candidates (the Review page, Duplicates lane)")
	protected.GET("/audiobooks/duplicates", s.perm(auth.PermLibraryView), duplicatesGone)
	protected.GET("/audiobooks/duplicates/scan-results", s.perm(auth.PermLibraryView), duplicatesGone)
	protected.POST("/audiobooks/duplicates/scan", s.perm(auth.PermLibraryEditMetadata), duplicatesGone)
	protected.POST("/audiobooks/duplicates/link", s.perm(auth.PermLibraryEditMetadata), duplicatesGone)
	protected.POST("/audiobooks/duplicates/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesGone)
	protected.POST("/audiobooks/duplicates/reject", s.perm(auth.PermLibraryEditMetadata), duplicatesGone)
	protected.POST("/audiobooks/duplicates/dismiss", s.perm(auth.PermLibraryEditMetadata), duplicatesGone)
	protected.GET("/authors/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListDuplicateAuthors)
	protected.POST("/authors/duplicates/refresh", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RefreshDuplicateAuthors)
	protected.POST("/audiobooks/link", s.perm(auth.PermLibraryEditMetadata), duplicatesH.LinkBooks)
	protected.POST("/audiobooks/combine", s.perm(auth.PermLibraryEditMetadata), duplicatesH.CombineBooks)
	// Merge journals. Every combine (manual or review-queue combine/duplicate-of)
	// and every merge that carries a loser's version siblings into its group is
	// journaled. The two GETs list those journals; they are read-only
	// diagnostics the owner keeps (D73) and stay live.
	protected.GET("/merge/combine-journal", s.perm(auth.PermLibraryView), duplicatesH.ListCombineJournals)
	// POST /merge/undo/:journal_id and POST /merge/sibling-undo/:journal_id had
	// no caller in the repo (01-P81h). They answer 410 Gone for one release
	// (owner decision D9) and are then removed. The stubs stay on the protected
	// group behind their old library.edit_metadata guard, so an unauthenticated
	// caller still gets 401, a caller without the permission 403, and only an
	// allowed caller learns they are gone. The combine undo's live path is the
	// operation revert (merge.Service.UndoCombine through revertMergeCombineJournal);
	// the sibling-move undo has none (todo.d fragment 01-P81h).
	protected.POST("/merge/undo/:journal_id", s.perm(auth.PermLibraryEditMetadata), gone(combineUndoGone))
	protected.GET("/merge/sibling-journal", s.perm(auth.PermLibraryView), duplicatesH.ListSiblingMoveJournals)
	protected.POST("/merge/sibling-undo/:journal_id", s.perm(auth.PermLibraryEditMetadata), gone(""))
	protected.GET("/series/duplicates", s.perm(auth.PermLibraryView), duplicatesH.ListSeriesDuplicates)
	protected.POST("/series/duplicates/refresh", s.perm(auth.PermLibraryEditMetadata), duplicatesH.RefreshSeriesDuplicates)
	protected.POST("/series/deduplicate", s.perm(auth.PermLibraryEditMetadata), duplicatesH.DeduplicateSeriesHandler)
	protected.POST("/series/merge", s.perm(auth.PermLibraryEditMetadata), duplicatesH.MergeSeriesGroup)
	protected.GET("/series/prune/preview", s.perm(auth.PermLibraryView), duplicatesH.SeriesPrunePreview)
	protected.POST("/series/prune", s.perm(auth.PermLibraryEditMetadata), duplicatesH.SeriesPrune)
	protected.POST("/dedup/validate", s.perm(auth.PermLibraryEditMetadata), duplicatesH.ValidateDedupEntry)
}
