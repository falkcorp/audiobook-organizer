// file: internal/server/duplicates_ops.go
// version: 2.4.0
// guid: 8b3e1f92-d4c7-4a6e-b5f0-2a7c9d1e3f45
// last-edited: 2026-07-01

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
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/gin-gonic/gin"
)

// ── OperationDef registrations ────────────────────────────────────────────────

// RegisterBookDedupScanOp registers the "dedup.book-scan" v2 OperationDef.
func (s *Server) RegisterBookDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.book-scan",
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
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("dedup.book-scan: decode params: %w", err)
			}
			store := s.Store()
			if store == nil {
				return fmt.Errorf("dedup.book-scan: database not initialized")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     p.LegacyOpID,
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

			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "dedup.book-scan", "dedup",
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
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("dedup.book-merge: decode params: %w", err)
			}
			store := s.Store()
			if store == nil {
				return fmt.Errorf("dedup.book-merge: database not initialized")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     p.LegacyOpID,
				Type:   "dedup.book-merge",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			op.AddEntity("books", p.KeepID)
			op.AddEntity("books", p.MergeIDs...)
			logging.Info(ctx, "book merge starting", "keep_id", p.KeepID, "merge_count", len(p.MergeIDs))

			_, err := dedup.MergeBooks(ctx, store, p.LegacyOpID, p.KeepID, p.MergeIDs, progress)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "book merge failed", "err", err)
				return err
			}

			s.dedupCache.InvalidateAll()
			if s.dedupEngine != nil {
				s.dedupEngine.CleanupCandidatesAfterMerge(p.MergeIDs)
			}
			op.SetStatus("success")
			logging.Info(ctx, "book merge complete", "kept_id", p.KeepID, "merged_count", len(p.MergeIDs))

			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "dedup.book-merge", "dedup",
					fmt.Sprintf("Book merge completed: merged %d books into %s", len(p.MergeIDs), p.KeepID),
					activity.AlwaysShow)
			}
			return nil
		},
	})
}

// RegisterAuthorDedupScanOp registers the "dedup.author-scan" v2 OperationDef.
// NOTE: The author-scan logic is not extracted because the only server-side
// step beyond calling dedup.FindDuplicateAuthors is s.filterReviewedAuthorGroups,
// which depends on server-owned state that cannot be cleanly injected.
func (s *Server) RegisterAuthorDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.author-scan",
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
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("dedup.author-scan: decode params: %w", err)
			}
			store := s.Store()
			if store == nil {
				return fmt.Errorf("dedup.author-scan: database not initialized")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     p.LegacyOpID,
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

			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "dedup.author-scan", "dedup",
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
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("dedup.series-scan: decode params: %w", err)
			}
			store := s.Store()
			if store == nil {
				return fmt.Errorf("dedup.series-scan: database not initialized")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     p.LegacyOpID,
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

			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "dedup.series-scan", "dedup",
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
		ID:              "dedup.series-dedup",
		Plugin:          "dedup",
		DisplayName:     "Series Deduplication",
		Description:     "Merge all series with identical normalized names, reassigning their books.",
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
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("dedup.series-dedup: decode params: %w", err)
			}
			store := s.Store()
			if store == nil {
				return fmt.Errorf("dedup.series-dedup: database not initialized")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     p.LegacyOpID,
				Type:   "dedup.series-dedup",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			logging.Info(ctx, "series deduplication starting")

			_, err := dedup.DedupSeries(ctx, store, progress)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series deduplication failed", "err", err)
				return err
			}

			s.dedupCache.InvalidateAll()
			op.SetStatus("success")
			logging.Info(ctx, "series deduplication complete")

			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "dedup.series-dedup", "dedup",
					"Series deduplication completed", activity.AlwaysShow)
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
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("dedup.series-prune: decode params: %w", err)
			}
			store := s.Store()
			if store == nil {
				return fmt.Errorf("dedup.series-prune: database not initialized")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     p.LegacyOpID,
				Type:   "dedup.series-prune",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}

			logging.Info(ctx, "series prune starting")
			runErr := s.executeSeriesPrune(ctx, store, progress, p.LegacyOpID)

			if runErr != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series prune failed", "err", runErr)
			} else {
				op.SetStatus("success")
				logging.Info(ctx, "series prune complete")
			}

			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				summary := "Series prune completed"
				if runErr != nil {
					summary = fmt.Sprintf("Series prune failed: %v", runErr)
				}
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "dedup.series-prune", "dedup", summary, activity.AlwaysShow)
			}
			return runErr
		},
	})
}

// RegisterSeriesMergeOp registers the "dedup.series-merge" v2 OperationDef.
func (s *Server) RegisterSeriesMergeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.series-merge",
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
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("dedup.series-merge: decode params: %w", err)
			}
			store := s.Store()
			if store == nil {
				return fmt.Errorf("dedup.series-merge: database not initialized")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     p.LegacyOpID,
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

			_, err := dedup.MergeSeries(ctx, store, p.LegacyOpID, p.KeepID, p.MergeIDs, p.CustomName, progress)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series merge failed", "err", err)
				return err
			}

			s.dedupCache.InvalidateAll()
			op.SetStatus("success")
			logging.Info(ctx, "series merge complete", "kept_id", p.KeepID, "merged_count", len(p.MergeIDs))

			if s.activityWriter != nil && p.LegacyOpID != "" {
				activity.FlushOperation(s.activityWriter, p.LegacyOpID)
				activity.EmitInfo(s.activityWriter, p.LegacyOpID, "dedup.series-merge", "dedup",
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
			if err := json.Unmarshal(rawParams, &p); err != nil {
				return fmt.Errorf("dedup.series-normalize: decode params: %w", err)
			}
			store := s.Store()
			if store == nil {
				return fmt.Errorf("dedup.series-normalize: database not initialized")
			}

			// Create operation context for structured logging
			op := &logging.OpContext{
				ID:     p.LegacyOpID,
				Type:   "dedup.series-normalize",
				Status: "pending",
			}
			ctx = logging.WithOp(ctx, op)

			progress := registryProgressAdapter{r: reporter}
			opID := p.LegacyOpID

			logging.Info(ctx, "series normalization starting")
			_ = progress.Log("info", "Starting series name normalization...", nil)

			enqueueWB := func(bookID string) {
				if s.writeBackBatcher != nil {
					s.writeBackBatcher.Enqueue(bookID)
				}
			}

			affectedBookIDs, opErr := executeSeriesNormalizeCore(ctx, store, enqueueWB)
			if opErr != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series normalization failed", "err", opErr)
				return opErr
			}

			for _, bookID := range affectedBookIDs {
				op.AddEntity("books", bookID)
			}

			logging.Info(ctx, "series normalization normalize complete, now organizing", "affected_books", len(affectedBookIDs))
			_ = progress.Log("info", fmt.Sprintf("Renamed/merged series; organizing %d affected books...", len(affectedBookIDs)), nil)

			log2 := logger.NewWithActivityLog("series-normalize", store)
			for _, bookID := range affectedBookIDs {
				if ctx.Err() != nil {
					op.SetStatus("failed")
					logging.Error(ctx, "series normalization cancelled", "err", ctx.Err())
					return ctx.Err()
				}
				book, bErr := store.GetBookByID(bookID)
				if bErr != nil || book == nil {
					continue
				}
				if _, oErr := s.organizeService.ReOrganizeInPlace(book, log2); oErr != nil {
					_ = progress.Log("warn", fmt.Sprintf("organize failed for book %s: %v", bookID, oErr), nil)
				}
			}

			if len(affectedBookIDs) > 0 {
				logging.Info(ctx, "writing tags for affected books", "count", len(affectedBookIDs))
				_ = progress.Log("info", fmt.Sprintf("Writing tags for %d affected books...", len(affectedBookIDs)), nil)
				if wbErr := s.runBulkWriteBack(ctx, opID, affectedBookIDs, false, 0, progress); wbErr != nil {
					logging.Warn(ctx, "tag write-back incomplete", "err", wbErr)
					_ = progress.Log("warn", fmt.Sprintf("tag write-back incomplete: %v", wbErr), nil)
				}
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

// ── local type aliases for backward compatibility ─────────────────────────────
// duplicates_handlers.go and scheduler_tasks.go reference the old unexported
// param struct names.  These aliases keep those files compiling without
// modification while the canonical definitions live in internal/dedup/op_params.go.

type bookDedupScanOpParams = dedup.BookDedupScanParams
type bookMergeOpParams = dedup.BookMergeParams
type authorDedupScanOpParams = dedup.AuthorDedupScanParams
type seriesDedupScanOpParams = dedup.SeriesDedupScanParams
type seriesDedupOpParams = dedup.SeriesDedupParams
type seriesPruneOpParams = dedup.SeriesPruneParams
type seriesMergeOpParams = dedup.SeriesMergeParams
type seriesNormalizeOpParams = dedup.SeriesNormalizeParams

// ── kept for reference: unused import guard ───────────────────────────────────

var _ = strings.Join // strings is used by series-normalize progress messages
