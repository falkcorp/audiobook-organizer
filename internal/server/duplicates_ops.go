// file: internal/server/duplicates_ops.go
// version: 2.14.0
// guid: 8b3e1f92-d4c7-4a6e-b5f0-2a7c9d1e3f45
// last-edited: 2026-08-24

// duplicates_ops registers v2 OperationDefs for the 8 async dedup operations
// that previously used s.queue.Enqueue.  HTTP handlers in duplicates_handlers.go
// create v1 op records for backward compatibility and then enqueue these defs.
//
// Param structs have been moved to internal/dedup/op_params.go (exported names).
// Execution logic for book-scan, book-merge, series-scan, series-dedup, and
// series-merge has been extracted to internal/dedup/book_dedup.go and
// internal/dedup/series_dedup.go.  The Run bodies here are now thin wrappers
// that unmarshal params, call the domain function, and write results into
// server-owned state (dedupCache, etc.).
//
// Three ops are left as-is because they depend on server-owned services:
//   - dedup.author-scan: already calls dedup.FindDuplicateAuthors; the only
//     server-side step (filterReviewedAuthorGroups) cannot be extracted without
//     pulling the entire server into the dedup package.
//   - dedup.series-prune: a one-liner to s.executeSeriesPrune.
//   - dedup.series-normalize: uses s.organizeService and s.runBulkWriteBack.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/gin-gonic/gin"
)

// ── OperationDef registrations ────────────────────────────────────────────────

// RegisterBookDedupScanOp registers the "dedup.book-scan" v2 OperationDef.
func (s *Server) RegisterBookDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.book-scan",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Book Duplicate Scan",
		Description:     "Scan all audiobooks for duplicates using hash, folder, and metadata-based matching.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.book-scan",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.BookDedupScanParams
			// Empty params are legitimate (four of these ops take none at
			// all); only a NON-empty blob has to parse. This is the guard the
			// rest of the v2 ops use, and both directions are asserted by the
			// params-decode contract test in op_params_decode_test.go.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.book-scan: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.book-scan: database not initialized")
			}

			// Create operation context for structured logging
			// opID is the v2 run's own id. It keys structured logging, the
			// activity log, and the OperationChange ledger rows this op writes.
			// No v1 row is involved: the client polls this id against
			// GET /operations/v2/:id.
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{
				ID:     opID,
				Type:   "dedup.book-scan",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}
			dismissed := loadDismissedDedupGroups(store)

			logging.Info(ctx, "book duplicate scan starting")

			result, err := dedup.ScanBookDuplicates(ctx, store, dismissed, progress)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "book duplicate scan failed", "err", err)
				return err
			}

			cacheVal := gin.H{
				"groups":          result.Groups,
				"group_count":     len(result.Groups),
				"duplicate_count": result.TotalDuplicates,
			}
			s.dedupCache.SetWithTTL("book-dedup-scan", cacheVal, 30*time.Minute)

			op.SetStatus("success")
			logging.Info(ctx, "book duplicate scan complete", "groups", len(result.Groups), "duplicates", result.TotalDuplicates)

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "dedup.book-scan", "dedup",
					fmt.Sprintf("Book duplicate scan found %d groups (%d duplicates)", len(result.Groups), result.TotalDuplicates),
					activity.AlwaysShow)
			}
			return nil
		},
	})
}

// RegisterBookMergeOp registers the "dedup.book-merge" v2 OperationDef.
func (s *Server) RegisterBookMergeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.book-merge",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Book Merge",
		Description:     "Merge a set of duplicate audiobooks, keeping one and deleting the others.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.book-merge",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.BookMergeParams
			// Empty params are legitimate (four of these ops take none at
			// all); only a NON-empty blob has to parse. This is the guard the
			// rest of the v2 ops use, and both directions are asserted by the
			// params-decode contract test in op_params_decode_test.go.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.book-merge: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.book-merge: database not initialized")
			}

			// Create operation context for structured logging
			// opID is the v2 run's own id. It keys structured logging, the
			// activity log, and the OperationChange ledger rows this op writes.
			// No v1 row is involved: the client polls this id against
			// GET /operations/v2/:id.
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{
				ID:     opID,
				Type:   "dedup.book-merge",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			op.AddEntity("books", p.KeepID)
			op.AddEntity("books", p.MergeIDs...)
			logging.Info(ctx, "book merge starting", "keep_id", p.KeepID, "merge_count", len(p.MergeIDs))

			// F6 REROUTE: this op previously called dedup.MergeBooks, which
			// hard-deleted losers via store.DeleteBook. DeleteBook does NOT
			// tombstone external-ID (ext_id:*) mappings and does NOT enqueue
			// iTunes ITL removals, so every legacy merge orphaned the losers'
			// PID/ASIN lookups (leaving them resolving to a hard-deleted book)
			// and stranded their iTunes tracks in the library forever. Route
			// through merge.Service.MergeBooks instead (via applyBookMergeReroute),
			// which reassigns external IDs to the winner, enqueues ITL removals,
			// and soft-deletes losers (recoverable) — one merge path shared with
			// the UI/version-group merge.
			ms := s.mergeService
			if ms == nil {
				// Fallback matches wire_handlers.go's getMergeService nil-path.
				// Note: a fresh Service has no write-back batcher, so ITL removals
				// are skipped in that mode (tests / iTunes write-back disabled).
				ms = merge.NewService(store)
			}
			if err := applyBookMergeReroute(ctx, store, ms, p.KeepID, p.MergeIDs); err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "book merge failed", "err", err)
				return err
			}
			// Service takes no ProgressReporter; emit a final 100% so the op UI
			// completes cleanly.
			_ = progress.UpdateProgress(len(p.MergeIDs), len(p.MergeIDs), "Book merge complete")

			s.dedupCache.InvalidateAll()
			if s.dedupEngine != nil {
				s.dedupEngine.CleanupCandidatesAfterMerge(p.MergeIDs)
			}
			op.SetStatus("success")
			logging.Info(ctx, "book merge complete", "kept_id", p.KeepID, "merged_count", len(p.MergeIDs))

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "dedup.book-merge", "dedup",
					fmt.Sprintf("Book merge completed: merged %d books into %s", len(p.MergeIDs), p.KeepID),
					activity.AlwaysShow)
			}
			return nil
		},
	})
}

// applyBookMergeReroute performs the F6-rerouted book merge for the
// dedup.book-merge op. It (1) copies the losers' iTunes stats onto the keep book
// first-win (merge.Service.MergeBooks reassigns external-ID mappings but does
// NOT carry these Book-level iTunes fields, and after soft-delete they would
// otherwise survive only on the loser row and vanish on a later purge/archive
// sweep), persisting the copy BEFORE the merge — Service re-reads books fresh
// and writes the full keep object back, so the copy survives its own
// version-group UpdateBook — then (2) merges via merge.Service.MergeBooks, which
// reassigns external IDs to the winner, enqueues ITL removals, and soft-deletes
// losers. Extracted from the op Run body so the reroute (soft-delete +
// external-ID reassignment, NOT hard delete) is unit-testable on a real store.
func applyBookMergeReroute(ctx context.Context, store bookRerouteStore, ms *merge.Service, keepID string, mergeIDs []string) error {
	// Build the loser set once, excluding the keep book and de-duping. Callers
	// (the handler binds keep_id/merge_ids without validation) may include the
	// keep book in mergeIDs; legacy dedup.MergeBooks guarded this with a
	// `mergeID == keepID { continue }` skip. Without it, passing keepID into
	// merge.Service.MergeBooks makes the version-group loop write the keep book
	// twice and demote it to non-primary — leaving the group with NO primary and
	// the keep book neither primary nor soft-deleted. Excluding it here (for BOTH
	// the transfer and the Service call) preserves that integrity guarantee.
	losers := make([]string, 0, len(mergeIDs))
	seen := map[string]bool{keepID: true}
	for _, mid := range mergeIDs {
		if seen[mid] {
			continue
		}
		seen[mid] = true
		losers = append(losers, mid)
	}
	if len(losers) == 0 {
		return nil // nothing to merge (every id was the keep book / duplicate)
	}

	if keepBook, err := store.GetBookByID(keepID); err == nil && keepBook != nil {
		changed := false
		for _, mid := range losers {
			if mb, mErr := store.GetBookByID(mid); mErr == nil && mb != nil {
				dedup.TransferITunesMetadataFirstWin(keepBook, mb)
				changed = true
			}
		}
		if changed {
			if _, uErr := store.UpdateBook(keepBook.ID, keepBook); uErr != nil {
				logging.Warn(ctx, "book merge: iTunes-field transfer write failed", "err", uErr)
			}
		}
	}
	// Service takes ALL ids (losers + winner) and the winner id.
	_, err := ms.MergeBooks(append(append([]string{}, losers...), keepID), keepID)
	return err
}

// RegisterAuthorDedupScanOp registers the "dedup.author-scan" v2 OperationDef.
// NOTE: The author-scan logic is not extracted because the only server-side
// step beyond calling dedup.FindDuplicateAuthors is s.filterReviewedAuthorGroups,
// which depends on server-owned state that cannot be cleanly injected.
func (s *Server) RegisterAuthorDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.author-scan",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Author Duplicate Scan",
		Description:     "Scan all authors for duplicates using fuzzy name matching.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.author-scan",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.AuthorDedupScanParams
			// Empty params are legitimate (four of these ops take none at
			// all); only a NON-empty blob has to parse. This is the guard the
			// rest of the v2 ops use, and both directions are asserted by the
			// params-decode contract test in op_params_decode_test.go.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.author-scan: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.author-scan: database not initialized")
			}

			// Create operation context for structured logging
			// opID is the v2 run's own id. It keys structured logging, the
			// activity log, and the OperationChange ledger rows this op writes.
			// No v1 row is involved: the client polls this id against
			// GET /operations/v2/:id.
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{
				ID:     opID,
				Type:   "dedup.author-scan",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			logging.Info(ctx, "author duplicate scan starting")
			// We don't know the final count until authors load; start with N=0
			// and reset to a real N once we know it.
			sp := sdk.NewProgress(reporter, 0)
			sp.Start("Fetching authors...")
			_ = progress.Log("info", "Fetching authors...", nil)

			authors, err := store.GetAllAuthors()
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "author scan failed to fetch authors", "err", err)
				return err
			}
			logging.Info(ctx, "authors loaded", "count", len(authors))
			// Re-create the helper now that we know N = len(authors).
			sp = sdk.NewProgress(reporter, len(authors))
			sp.Start(fmt.Sprintf("Loaded %d authors, fetching book counts...", len(authors)))

			bookCounts, err := store.GetAllAuthorBookCounts()
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "author scan failed to fetch book counts", "err", err)
				return err
			}
			bookCountFn := func(authorID int) int { return bookCounts[authorID] }
			sp.StepN(0, "Finding duplicate authors...")

			progressFn := func(current, total int, message string) {
				// Forward the real (current, total) from the dedup callback
				// verbatim — never re-scale into 0..100.
				sp.StepN(current, message)
			}

			groups := dedup.FindDuplicateAuthors(authors, 0.9, bookCountFn, progressFn)

			// Filter out groups already reviewed by AI scans (server-owned state).
			groups = s.filterReviewedAuthorGroups(groups)

			for _, g := range groups {
				op.AddEntity("authors", strconv.Itoa(g.Canonical.ID))
				for _, v := range g.Variants {
					op.AddEntity("authors", strconv.Itoa(v.ID))
				}
			}

			result := gin.H{"groups": groups, "count": len(groups)}
			s.dedupCache.SetWithTTL("author-duplicates", result, 30*time.Minute)

			op.SetStatus("success")
			sp.Done(fmt.Sprintf("Found %d duplicate groups (after filtering reviewed)", len(groups)))
			logging.Info(ctx, "author duplicate scan complete", "groups", len(groups))

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "dedup.author-scan", "dedup",
					fmt.Sprintf("Author duplicate scan found %d groups", len(groups)),
					activity.AlwaysShow)
			}
			return nil
		},
	})
}

// RegisterSeriesDedupScanOp registers the "dedup.series-scan" v2 OperationDef.
func (s *Server) RegisterSeriesDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.series-scan",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Series Duplicate Scan",
		Description:     "Scan all series for duplicates using exact and sub-series matching.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.series-scan",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.SeriesDedupScanParams
			// Empty params are legitimate (four of these ops take none at
			// all); only a NON-empty blob has to parse. This is the guard the
			// rest of the v2 ops use, and both directions are asserted by the
			// params-decode contract test in op_params_decode_test.go.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.series-scan: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.series-scan: database not initialized")
			}

			// Create operation context for structured logging
			// opID is the v2 run's own id. It keys structured logging, the
			// activity log, and the OperationChange ledger rows this op writes.
			// No v1 row is involved: the client polls this id against
			// GET /operations/v2/:id.
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{
				ID:     opID,
				Type:   "dedup.series-scan",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			logging.Info(ctx, "series duplicate scan starting")

			result, err := dedup.ScanSeriesDuplicates(ctx, store, progress)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series duplicate scan failed", "err", err)
				return err
			}

			for _, g := range result.Groups {
				for _, sw := range g.Series {
					op.AddEntity("series", strconv.Itoa(sw.ID))
				}
			}

			resp := gin.H{
				"groups":       result.Groups,
				"count":        len(result.Groups),
				"total_series": result.TotalSeries,
			}
			s.dedupCache.Set("series-duplicates", resp)

			op.SetStatus("success")
			logging.Info(ctx, "series duplicate scan complete", "groups", len(result.Groups), "total_series", result.TotalSeries)

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "dedup.series-scan", "dedup",
					fmt.Sprintf("Series duplicate scan found %d groups (of %d total series)", len(result.Groups), result.TotalSeries),
					activity.AlwaysShow)
			}
			return nil
		},
	})
}

// RegisterSeriesDedupOp registers the "dedup.series-dedup" v2 OperationDef.
func (s *Server) RegisterSeriesDedupOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:          "dedup.series-dedup",
		Liveness:    opsregistry.LivenessManual,
		Plugin:      "dedup",
		DisplayName: "Series Deduplication",
		Description: "Merge all series with identical normalized names, reassigning their books. " +
			"Defaults to dry_run=true, which reports what WOULD merge without writing; " +
			"pass dry_run=false to apply.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.series-dedup",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.SeriesDedupParams
			// Empty params are legitimate (four of these ops take none at
			// all); only a NON-empty blob has to parse. This is the guard the
			// rest of the v2 ops use, and both directions are asserted by the
			// params-decode contract test in op_params_decode_test.go.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.series-dedup: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.series-dedup: database not initialized")
			}

			// Create operation context for structured logging
			// opID is the v2 run's own id. It keys structured logging, the
			// activity log, and the OperationChange ledger rows this op writes.
			// No v1 row is involved: the client polls this id against
			// GET /operations/v2/:id.
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{
				ID:     opID,
				Type:   "dedup.series-dedup",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			// Absent dry_run means TRUE. This op deletes series rows and had
			// no preview at all before TODO.md L3966, so the default has to be
			// the safe one; a caller that wants the writes says so explicitly.
			// Same nil-means-true read as maintenance.author-conjunction-repair.
			dryRun := p.DryRun == nil || *p.DryRun

			logging.Info(ctx, "series deduplication starting", "dry_run", dryRun)

			result, err := dedup.DedupSeries(ctx, store, progress, dryRun)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series deduplication failed", "dry_run", dryRun, "err", err)
				return err
			}

			// A dry run wrote nothing, so there is nothing to invalidate.
			if !dryRun {
				s.dedupCache.InvalidateAll()
			}
			op.SetStatus("success")
			logging.Info(ctx, "series deduplication complete",
				"dry_run", dryRun, "total_merged", result.TotalMerged,
				"books_reassigned", result.TotalBooksReassigned,
				"errors", len(result.Errors))

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				// The activity row must say which mode ran. An operator who
				// cannot tell a preview from an apply in the activity log is
				// the exact trap the dry run exists to prevent.
				msg := fmt.Sprintf(
					"Series deduplication completed: merged %d series, reassigned %d books",
					result.TotalMerged, result.TotalBooksReassigned)
				if dryRun {
					msg = fmt.Sprintf(
						"Series deduplication PREVIEW (dry_run=true, nothing written): "+
							"would merge %d series and reassign %d books",
						result.TotalMerged, result.TotalBooksReassigned)
				}
				activity.EmitInfo(s.activityWriter, opID, "dedup.series-dedup", "dedup",
					msg, activity.AlwaysShow)
			}
			return nil
		},
	})
}

// RegisterSeriesPruneOp registers the "dedup.series-prune" v2 OperationDef.
// NOTE: series-prune logic is not extracted because it is entirely implemented
// by s.executeSeriesPrune in duplicates_handlers.go.
func (s *Server) RegisterSeriesPruneOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.series-prune",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Series Prune",
		Description:     "Merge duplicate series and delete orphan series with no books.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.series-prune",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.SeriesPruneParams
			// Empty params are legitimate (four of these ops take none at
			// all); only a NON-empty blob has to parse. This is the guard the
			// rest of the v2 ops use, and both directions are asserted by the
			// params-decode contract test in op_params_decode_test.go.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.series-prune: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.series-prune: database not initialized")
			}

			// Create operation context for structured logging
			// opID is the v2 run's own id. It keys structured logging, the
			// activity log, and the OperationChange ledger rows this op writes.
			// No v1 row is involved: the client polls this id against
			// GET /operations/v2/:id.
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{
				ID:     opID,
				Type:   "dedup.series-prune",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			logging.Info(ctx, "series prune starting")
			runErr := s.executeSeriesPrune(ctx, store, progress, opID)

			if runErr != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series prune failed", "err", runErr)
			} else {
				op.SetStatus("success")
				logging.Info(ctx, "series prune complete")
			}

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				summary := "Series prune completed"
				if runErr != nil {
					summary = fmt.Sprintf("Series prune failed: %v", runErr)
				}
				activity.EmitInfo(s.activityWriter, opID, "dedup.series-prune", "dedup", summary, activity.AlwaysShow)
			}
			return runErr
		},
	})
}

// RegisterSeriesMergeOp registers the "dedup.series-merge" v2 OperationDef.
func (s *Server) RegisterSeriesMergeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.series-merge",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Series Merge",
		Description:     "Merge multiple series into one, reassigning all books and optionally renaming.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.series-merge",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.SeriesMergeParams
			// Empty params are legitimate (four of these ops take none at
			// all); only a NON-empty blob has to parse. This is the guard the
			// rest of the v2 ops use, and both directions are asserted by the
			// params-decode contract test in op_params_decode_test.go.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.series-merge: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.series-merge: database not initialized")
			}

			// Create operation context for structured logging
			// opID is the v2 run's own id. It keys structured logging, the
			// activity log, and the OperationChange ledger rows this op writes.
			// No v1 row is involved: the client polls this id against
			// GET /operations/v2/:id.
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{
				ID:     opID,
				Type:   "dedup.series-merge",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			op.AddEntity("series", strconv.Itoa(p.KeepID))
			for _, mid := range p.MergeIDs {
				op.AddEntity("series", strconv.Itoa(mid))
			}
			logging.Info(ctx, "series merge starting", "keep_id", p.KeepID, "merge_count", len(p.MergeIDs))

			_, err := dedup.MergeSeries(ctx, store, opID, p.KeepID, p.MergeIDs, p.CustomName, progress)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series merge failed", "err", err)
				return err
			}

			s.dedupCache.InvalidateAll()
			op.SetStatus("success")
			logging.Info(ctx, "series merge complete", "kept_id", p.KeepID, "merged_count", len(p.MergeIDs))

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "dedup.series-merge", "dedup",
					fmt.Sprintf("Series merge completed: merged %d series into series %d", len(p.MergeIDs), p.KeepID),
					activity.AlwaysShow)
			}
			return nil
		},
	})
}

// RegisterSeriesNormalizeOp registers the "dedup.series-normalize" v2 OperationDef.
// NOTE: series-normalize is not extracted because it depends on server-owned
// services: s.organizeService.ReOrganizeInPlace and s.runBulkWriteBack.
func (s *Server) RegisterSeriesNormalizeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.series-normalize",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Series Name Normalization",
		Description:     "Strip contamination from series names, merge sub-series, and re-organize affected books.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.series-normalize",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.SeriesNormalizeParams
			// Empty params are legitimate (four of these ops take none at
			// all); only a NON-empty blob has to parse. This is the guard the
			// rest of the v2 ops use, and both directions are asserted by the
			// params-decode contract test in op_params_decode_test.go.
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.series-normalize: decode params: %w", err)
				}
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.series-normalize: database not initialized")
			}

			// Create operation context for structured logging
			// opID is the v2 run's own id. It keys structured logging, the
			// activity log, and the OperationChange ledger rows this op writes.
			// No v1 row is involved: the client polls this id against
			// GET /operations/v2/:id.
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{
				ID:     opID,
				Type:   "dedup.series-normalize",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			logging.Info(ctx, "series normalization starting")
			_ = progress.Log("info", "Starting series name normalization...", nil)

			enqueueWB := func(bookID string) {
				if s.writeBackBatcher != nil {
					s.writeBackBatcher.Enqueue(bookID)
				}
			}

			// A partial failure must NOT discard the work that succeeded.
			//
			// This used to return immediately, skipping organize and write-back for
			// EVERY book in the run. The renames and merges have already committed
			// by the time an error surfaces here, so a re-run finds no contaminated
			// names, computes no actions, and never organizes those files -- the
			// failure is permanent rather than retryable.
			//
			// Organizing what was collected and THEN reporting the failure leaves
			// the files consistent with the series rows that did change. The op
			// still ends "failed", so the error is not swallowed.
			affectedBookIDs, opErr := executeSeriesNormalizeCore(ctx, store, enqueueWB)
			if opErr != nil {
				logging.Error(ctx, "series normalization reported errors; organizing the books it did collect before failing the operation",
					"err", opErr, "affected_books", len(affectedBookIDs))
				_ = progress.Log("warn", fmt.Sprintf(
					"Series normalization hit errors (%v); still organizing the %d books it collected so their files match the series rows that changed",
					opErr, len(affectedBookIDs)), nil)
			}

			for _, bookID := range affectedBookIDs {
				op.AddEntity("books", bookID)
			}

			logging.Info(ctx, "series normalization normalize complete, now organizing", "affected_books", len(affectedBookIDs))
			_ = progress.Log("info", fmt.Sprintf("Renamed/merged series; organizing %d affected books...", len(affectedBookIDs)), nil)

			log2 := logger.NewWithActivityLog("series-normalize", store)
			organizeFailed := 0
			organizeRefused := 0
			for _, bookID := range affectedBookIDs {
				if ctx.Err() != nil {
					op.SetStatus("failed")
					logging.Error(ctx, "series normalization cancelled", "err", ctx.Err())
					// errors.Join, not a bare ctx.Err(): if the normalize pass already
					// returned a real failure (a list of series whose rename failed),
					// returning only "context canceled" discards the only durable record
					// of which rows still need attention. Both are non-nil here, so the
					// join keeps the actionable one.
					return errors.Join(opErr, ctx.Err())
				}
				book, bErr := store.GetBookByID(bookID)
				if bErr != nil {
					organizeFailed++
					_ = progress.Log("warn", fmt.Sprintf(
						"book %s could not be loaded and was NOT organized; its series row changed but its files did not move: %v",
						bookID, bErr), nil)
					continue
				}
				// book == nil is "could not resolve", NOT "absent", and it was silently
				// skipped before. The Pebble store returns (nil, nil) on ErrNotFound,
				// and affectedBookIDs came from the memdb-backed membership getter,
				// which can list a row a later point-get cannot hydrate. Dropping it
				// here meant the book was counted in "organizing the N books it
				// collected" and never organized, with no line naming it anywhere.
				//
				// Sixty lines away in duplicates_helpers.go the identical divergence is
				// counted as repointFailed and blocks a delete. Here it counted as
				// nothing.
				if book == nil {
					organizeFailed++
					// Deliberately states BOTH causes, because this code cannot tell
					// them apart and claiming either one would be a guess presented as
					// a fact. GetBookByID returns (nil, nil) on ErrNotFound, so a book
					// hard-deleted between collection and here looks identical to one
					// whose on-disk row survives while the in-memory index has lost it.
					// The first is harmless; the second is a live book left behind.
					logging.Error(ctx, "book listed as affected does not resolve",
						"book_id", bookID, "op", "series-normalize")
					_ = progress.Log("warn", fmt.Sprintf(
						"book %s does not resolve: it was either deleted after this run collected it "+
							"(harmless), or its row exists but the in-memory index has lost it, in which "+
							"case its series changed and its files were NOT moved", bookID), nil)
					continue
				}
				if _, oErr := s.organizeService.ReOrganizeInPlace(book, log2); oErr != nil {
					// A refused organize is NOT a failed organize.
					//
					// ErrAuthorUnresolved means the organizer deliberately declined:
					// the book has no resolved author, so renaming it would file it
					// under a placeholder. That is a routine library state, not a
					// fault, and counting it would flip an otherwise-clean run red on
					// every unresolved-author book in the affected set. Reporting that
					// is worse than useless -- an operation that is red every time
					// teaches its operator to ignore it, which is how the silent
					// failures this PR exists to fix went unnoticed in the first place.
					if errors.Is(oErr, organizer.ErrAuthorUnresolved) {
						organizeRefused++
						_ = progress.Log("info", fmt.Sprintf(
							"book %s not organized: its author is unresolved, so organize declined to "+
								"rename it. Resolve the author via metadata fetch and organize will "+
								"pick it up.", bookID), nil)
						continue
					}
					organizeFailed++
					_ = progress.Log("warn", fmt.Sprintf("organize failed for book %s: %v", bookID, oErr), nil)
				}
			}
			if organizeRefused > 0 {
				logging.Info(ctx, "series normalization declined to organize some books by policy",
					"refused", organizeRefused, "total", len(affectedBookIDs))
			}
			// Folded into the operation's outcome rather than left in the log. A
			// re-run cannot recover these: normalize is idempotent on the series NAME,
			// so once the rename has committed a second run computes no actions and
			// never revisits the book. Silence here is permanent.
			//
			// The message points at the per-book warnings instead of naming one cause,
			// because the causes differ (a store failure, a vanished row, a missing
			// source file) and a summary that asserts one of them would be false for
			// the others. A PR about honest reporting does not get to round its own
			// summary off.
			if organizeFailed > 0 {
				organizeErr := fmt.Errorf("%d of %d affected books were not organized; their series rows changed, "+
					"so their files may no longer match their series path, and re-running this operation will not "+
					"retry them because the series names are already normalized. See the per-book warnings above "+
					"for each cause",
					organizeFailed, len(affectedBookIDs))
				logging.Error(ctx, "series normalization could not organize every affected book",
					"failed", organizeFailed, "refused", organizeRefused, "total", len(affectedBookIDs))
				_ = progress.Log("warn", organizeErr.Error(), nil)
				opErr = errors.Join(opErr, organizeErr)
			}

			if len(affectedBookIDs) > 0 {
				logging.Info(ctx, "writing tags for affected books", "count", len(affectedBookIDs))
				_ = progress.Log("info", fmt.Sprintf("Writing tags for %d affected books...", len(affectedBookIDs)), nil)
				if wbErr := s.runBulkWriteBack(ctx, opID, affectedBookIDs, false, 0, progress); wbErr != nil {
					logging.Warn(ctx, "tag write-back incomplete", "err", wbErr)
					_ = progress.Log("warn", fmt.Sprintf("tag write-back incomplete: %v", wbErr), nil)
				}
			}

			// Report the failure now that the recoverable work is done. The status
			// is still "failed" -- deferring it bought file consistency, not
			// silence.
			if opErr != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series normalization failed", "err", opErr, "affected_books", len(affectedBookIDs))
				if s.activityWriter != nil && opID != "" {
					activity.FlushOperation(s.activityWriter, opID)
				}
				return opErr
			}

			op.SetStatus("success")
			logging.Info(ctx, "series normalization complete", "affected_books", len(affectedBookIDs))
			_ = progress.Log("info", "Series normalization complete.", nil)

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "dedup.series-normalize", "dedup",
					fmt.Sprintf("Series normalization completed for %d affected books", len(affectedBookIDs)),
					activity.AlwaysShow)
			}
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBookDedupScanOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBookMergeOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAuthorDedupScanOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesDedupScanOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesDedupOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesPruneOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesMergeOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesNormalizeOp(reg) })
}

// ── kept for reference: unused import guard ───────────────────────────────────

var _ = strings.Join // strings is used by series-normalize progress messages
