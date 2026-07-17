// file: internal/server/server_maintenance_deps.go
// version: 1.7.0
// guid: b4c5d6e7-f8a9-0123-7890-345678901234
// last-edited: 2026-07-17

// This file implements the maintenance.ServerDeps interface on *Server, giving
// the maintenance plugin access to server internals without creating an import
// cycle (internal/plugins/maintenance must NOT import internal/server).

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/sweep"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Verify *Server implements maintenance.ServerDeps at compile time.
var _ maintenanceplugin.ServerDeps = (*Server)(nil)

// ---- delegated run helpers ----

func (s *Server) RunIsbnEnrichment(ctx context.Context, progress operations.ProgressReporter, opID string) error {
	return s.runIsbnEnrichment(ctx, progress, opID)
}

func (s *Server) RunMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error {
	return s.runMetadataRefreshScan(ctx, progress)
}

func (s *Server) RunBulkWriteBack(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter) error {
	return s.runBulkWriteBack(ctx, opID, bookIDs, doRename, startIdx, progress)
}

func (s *Server) RunAutoPurgeSoftDeleted(opID string) {
	s.runAutoPurgeSoftDeleted(opID)
}

func (s *Server) ExecuteSeriesPrune(ctx context.Context, store database.Store, progress operations.ProgressReporter, opID string) error {
	return s.executeSeriesPrune(ctx, store, progress, opID)
}

func (s *Server) ExecuteSeriesNormalizeCore(ctx context.Context, store database.Store, enqueueWB func(string)) ([]string, error) {
	return executeSeriesNormalizeCore(ctx, store, enqueueWB)
}

// ---- one-shot startup ops ----

func (s *Server) BackfillExternalIDs() {
	s.backfillExternalIDs()
}

func (s *Server) StripMovementAtoms(ctx context.Context) {
	s.stripMovementAtoms(ctx)
}

func (s *Server) RemuxMalformedM4BFiles(ctx context.Context) {
	s.remuxMalformedM4BFiles(ctx)
}

func (s *Server) TranscodeMalformedM4BFiles(ctx context.Context) {
	s.transcodeMalformedM4BFiles(ctx)
}

// ---- store helpers ----

func (s *Server) CleanupOrphanedTempFiles(rootDir string, opID string) int {
	return sweep.CleanupOrphanedTempFiles(rootDir, s.activityWriter, opID)
}

func (s *Server) CleanupTrashedVersions() int {
	return CleanupTrashedVersions(s.Store())
}

func (s *Server) SweepArchivedBooks() int {
	return sweep.SweepArchivedBooks(s.Store())
}

// ---- optional component accessors ----

func (s *Server) ActivityFlushOp(opID string) {
	activity.FlushOperation(s.activityWriter, opID)
}

func (s *Server) EnqueueWriteBack(bookID string) {
	if s.writeBackBatcher != nil {
		s.writeBackBatcher.Enqueue(bookID)
	}
}

func (s *Server) PollBatch(ctx context.Context) (int, error) {
	if s.batchPoller == nil {
		return 0, nil
	}
	return s.batchPoller.Poll(ctx)
}

func (s *Server) DedupLLMReview(ctx context.Context) error {
	if s.dedupEngine == nil {
		return fmt.Errorf("dedup engine not initialized")
	}
	return s.dedupEngine.RunLLMReview(ctx)
}

func (s *Server) InvalidateDedupCache() {
	if s.dedupCache != nil {
		s.dedupCache.Invalidate("author-duplicates")
	}
}

func (s *Server) MetadataUpgradeRun(ctx context.Context, limit int) (checked, upgraded, skipped, errs int, err error) {
	if s.metadataFetchService == nil {
		return 0, 0, 0, 0, fmt.Errorf("metadata fetch service not initialized")
	}
	svc := NewMetadataUpgradeService(s.Store(), s.metadataFetchService)
	result, err := svc.RunUpgrade(ctx, limit)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return result.Checked, result.Upgraded, result.Skipped, result.Errors, nil
}

func (s *Server) OptimizeAIScanStore() error {
	if s.aiScanStore == nil {
		return nil
	}
	return s.aiScanStore.Optimize()
}

func (s *Server) OptimizeOLStore() error {
	if s.olService == nil || s.olService.Store() == nil {
		return nil
	}
	return s.olService.Store().Optimize()
}

func (s *Server) PruneOldLogs(retentionDays int) error {
	retLog := logger.New("purge_old_logs")
	_, err := logger.PruneOldLogs(s.Store(), retentionDays, retLog)
	return err
}

func (s *Server) CompactActivityLog(ctx context.Context, compactionDays, changeDays, debugDays int) (compacted int, summarized int, pruned int, err error) {
	if s.activityService == nil {
		return 0, 0, 0, nil
	}

	if compactionDays <= 0 {
		compactionDays = 14
	}
	compactionCutoff := time.Now().AddDate(0, 0, -compactionDays)
	compactResult, err := s.activityService.CompactByDay(ctx, compactionCutoff)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("compact activity: %w", err)
	}

	if changeDays <= 0 {
		changeDays = 90
	}
	changeCutoff := time.Now().AddDate(0, 0, -changeDays)
	sumCount, err := s.activityService.Summarize(ctx, changeCutoff, "change")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("summarize activity: %w", err)
	}

	if debugDays <= 0 {
		debugDays = 30
	}
	debugCutoff := time.Now().AddDate(0, 0, -debugDays)
	pruneCount, err := s.activityService.Prune(debugCutoff, "debug")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("prune activity: %w", err)
	}

	return compactResult.DaysCompacted, sumCount, pruneCount, nil
}

// ---- feature flags ----

func (s *Server) HasDedupEngine() bool {
	return s.dedupEngine != nil
}

func (s *Server) HasMetadataFetchService() bool {
	return s.metadataFetchService != nil
}

func (s *Server) HasISBNEnrichment() bool {
	return s.metadataFetchService != nil && s.metadataFetchService.ISBNEnrichment() != nil
}

func (s *Server) HasAIParsing() bool {
	return config.AppConfig.EnableAIParsing && config.AppConfig.OpenAIAPIKey != ""
}

func (s *Server) HasBatchPoller() bool {
	return s.batchPoller != nil
}

func (s *Server) RootDir() string {
	return config.AppConfig.RootDir
}

func (s *Server) LogRetentionDays() int {
	return config.AppConfig.LogRetentionDays
}

func (s *Server) PurgeSoftDeletedAfterDays() int {
	return config.AppConfig.PurgeSoftDeletedAfterDays
}

func (s *Server) ActivityLogCompactionDays() int {
	return config.AppConfig.ActivityLogCompactionDays
}

func (s *Server) ActivityLogRetentionChangeDays() int {
	return config.AppConfig.ActivityLogRetentionChangeDays
}

func (s *Server) ActivityLogRetentionDebugDays() int {
	return config.AppConfig.ActivityLogRetentionDebugDays
}

func (s *Server) BackupRetentionDays() int {
	days := config.AppConfig.PurgeSoftDeletedAfterDays
	if days <= 0 {
		days = 30
	}
	return days
}

// ---- operation orchestration (library.optimize) ----

// EnqueueOp implements maintenance.ServerDeps. It delegates to the UOS registry.
// Returns an error if the registry is not initialized or the operation enqueue fails.
func (s *Server) EnqueueOp(ctx context.Context, defID string, params any) (string, error) {
	if s.opRegistry == nil {
		return "", fmt.Errorf("operations registry not initialized")
	}
	return s.opRegistry.EnqueueOp(ctx, defID, params)
}

// DedupTriageExactPending implements maintenance.ServerDeps. It pages all
// pending book dedup candidates, classifies each via ClassifyCandidate, and
// returns a TriageReport with per-population counts and up to 5 examples each.
// No candidates are deleted regardless of their class.
func (s *Server) DedupTriageExactPending(ctx context.Context) (*maintenanceplugin.TriageReport, error) {
	if s.embeddingStore == nil {
		return nil, fmt.Errorf("embedding store not initialized")
	}

	candidates, _, err := s.embeddingStore.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Limit:      1_000_000,
	})
	if err != nil {
		return nil, fmt.Errorf("list candidates: %w", err)
	}
	slog.Info("dedup triage: starting", "candidates", len(candidates))

	// Memoize book lookups — a book may appear in many candidate pairs.
	// Failed lookups are counted (not swallowed) — a nil book skews the
	// classification toward stub/unknown, so the report must disclose them.
	var lookupErrs int
	var firstLookupErr error
	bookCache := make(map[string]*database.Book, len(candidates))
	getBook := func(id string) *database.Book {
		if b, ok := bookCache[id]; ok {
			return b
		}
		b, gerr := s.Store().GetBookByID(id)
		if gerr != nil {
			lookupErrs++
			if firstLookupErr == nil {
				firstLookupErr = fmt.Errorf("get book %s: %w", id, gerr)
			}
		}
		bookCache[id] = b
		return b
	}

	type popState struct {
		count    int
		examples []maintenanceplugin.TriageExample
	}
	pops := map[maintenanceplugin.TriageClass]*popState{
		maintenanceplugin.TriageClassGenuine:   {},
		maintenanceplugin.TriageClassStub:      {},
		maintenanceplugin.TriageClassFragment:  {},
		maintenanceplugin.TriageClassTitleLeak: {},
		maintenanceplugin.TriageClassUnknown:   {},
	}

	for i := range candidates {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if i > 0 && i%5000 == 0 {
			slog.Info("dedup triage: progress",
				"processed", i, "total", len(candidates), "lookup_errors", lookupErrs)
		}
		c := candidates[i]
		a := getBook(c.EntityAID)
		b := getBook(c.EntityBID)

		cls, reason := maintenanceplugin.ClassifyCandidate(c, a, b)
		ps := pops[cls]
		ps.count++
		if len(ps.examples) < 5 {
			titleA, titleB := "", ""
			if a != nil {
				titleA = a.Title
			}
			if b != nil {
				titleB = b.Title
			}
			ps.examples = append(ps.examples, maintenanceplugin.TriageExample{
				CandidateID: c.ID,
				BookAID:     c.EntityAID,
				BookBID:     c.EntityBID,
				BookATitle:  titleA,
				BookBTitle:  titleB,
				Layer:       c.Layer,
				Reason:      reason,
			})
		}
	}

	report := &maintenanceplugin.TriageReport{
		ScannedAt:        time.Now(),
		TotalScanned:     len(candidates),
		Populations:      make(map[maintenanceplugin.TriageClass]maintenanceplugin.TriagePopulation, len(pops)),
		BookLookupErrors: lookupErrs,
	}
	for cls, ps := range pops {
		report.Populations[cls] = maintenanceplugin.TriagePopulation{
			Class:    cls,
			Count:    ps.count,
			Examples: ps.examples,
		}
		switch {
		case maintenanceplugin.IsPurgeable(cls):
			report.PurgeableCount += ps.count
		case cls == maintenanceplugin.TriageClassGenuine:
			report.KeepCount += ps.count
		default:
			report.ReviewCount += ps.count
		}
	}
	if lookupErrs > 0 {
		slog.Warn("dedup triage: book lookups failed — population counts may be skewed",
			"lookup_errors", lookupErrs, "first_error", firstLookupErr)
	}
	slog.Info("dedup triage: complete",
		"scanned", report.TotalScanned,
		"purgeable", report.PurgeableCount,
		"keep", report.KeepCount,
		"review", report.ReviewCount,
		"lookup_errors", lookupErrs)
	return report, nil
}

// SearchTranscriptionCandidate implements maintenance.ServerDeps.
// Uses the local metadata cache — no external API calls. Returns (title,
// author, score, true, nil) when a cached candidate exists; (‟", "", 0,
// false, nil) on cache-miss or unavailable service.
func (s *Server) SearchTranscriptionCandidate(_ context.Context, bookID, _, _ string) (string, string, float64, bool, error) {
	if s.metadataFetchService == nil {
		return "", "", 0, false, nil
	}
	entry, _, err := s.metadataFetchService.GetCachedCandidates(bookID)
	if err != nil {
		return "", "", 0, false, err
	}
	if entry == nil || len(entry.Candidates) == 0 {
		return "", "", 0, false, nil
	}
	// Cache is stored score-descending; first entry is the best.
	var best metafetch.MetadataCandidate
	if err := json.Unmarshal(entry.Candidates[0], &best); err != nil {
		return "", "", 0, false, nil
	}
	return best.Title, best.Author, best.Score, true, nil
}

// ApplyTranscriptionCandidate implements maintenance.ServerDeps.
// Uses the local metadata cache to avoid a redundant external search, then
// applies the top cached candidate. TASK-02 audio-confirm logic sets
// MetadataReviewStatus="audio_confirmed" when the candidate title matches the
// book's transcribed title.
//
// TOCTOU guard (MATCH-6/BUG-3/QUAL-3): the caller (runAutoMatchTranscribed)
// gates on the candTitle/candAuthor returned by an earlier
// SearchTranscriptionCandidate read and passes that exact identity here as
// gatedTitle/gatedAuthor. Because the metadata cache is shared and keyed only
// by book ID, it can be refreshed between the gate and this call (a
// concurrent metadata search, another maintenance op, a UI-driven re-fetch).
// Re-reading Candidates[0] without re-checking identity would risk applying
// an ungated candidate while logging the stale, gated title as "applied". To
// close that window, verify the re-read cache slot 0 still matches the gated
// identity before applying, and error out on mismatch — the caller already
// treats a non-nil error as "skip + log".
//
// SourceHash layer (INIT-3-T5): the slot-0 identity re-check catches a cache
// row whose top candidate was swapped, but NOT a row whose search INPUTS
// drifted (the book's title/author/narrator/series was edited) while the top
// candidate happened to stay put. ValidateCachedIdentity recomputes the stored
// SourceHash over the book's CURRENT fields and refuses on mismatch (fail
// closed); legacy rows with an empty hash fail open with a warning. This runs
// BEFORE the slot-0 check as a first, independent layer — both guards are kept.
func (s *Server) ApplyTranscriptionCandidate(_ context.Context, bookID, gatedTitle, gatedAuthor string) error {
	if s.metadataFetchService == nil {
		return fmt.Errorf("metadata fetch service not initialized")
	}
	entry, _, err := s.metadataFetchService.GetCachedCandidates(bookID)
	if err != nil {
		return fmt.Errorf("get cached candidates for book %s: %w", bookID, err)
	}
	if entry == nil || len(entry.Candidates) == 0 {
		return fmt.Errorf("no cached candidates for book %s", bookID)
	}

	// SourceHash guard: recompute the cache row's search-input hash over the
	// book's CURRENT fields and refuse if it drifted since the write. A missing
	// store or book is anomalous at apply time — fail closed (skip + log).
	store := s.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	book, err := store.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("get book %s for cache-identity check: %w", bookID, err)
	}
	if book == nil {
		return fmt.Errorf("book %s not found for cache-identity check", bookID)
	}
	curAuthor := ""
	if book.Author != nil && book.Author.Name != "" {
		curAuthor = book.Author.Name
	}
	curNarrator := ""
	if book.Narrator != nil {
		curNarrator = *book.Narrator
	}
	curSeries := ""
	if book.Series != nil && book.Series.Name != "" {
		curSeries = book.Series.Name
	}
	if verr := s.metadataFetchService.ValidateCachedIdentity(entry, bookID, book.Title, curAuthor, curNarrator, curSeries); verr != nil {
		slog.Warn("apply-transcription-candidate: cache source-hash drift since write",
			"book_id", bookID, "error", verr)
		return verr
	}

	var cand metafetch.MetadataCandidate
	if err := json.Unmarshal(entry.Candidates[0], &cand); err != nil {
		return fmt.Errorf("decode cached candidate for book %s: %w", bookID, err)
	}

	titleMismatch := util.NormalizeTitle(cand.Title) != util.NormalizeTitle(gatedTitle)
	authorMismatch := false
	if len(gatedAuthor) > 3 {
		gl := strings.ToLower(gatedAuthor)
		cl := strings.ToLower(cand.Author)
		authorMismatch = !strings.Contains(cl, gl) && !strings.Contains(gl, cl)
	}
	if titleMismatch || authorMismatch {
		slog.Warn("apply-transcription-candidate: cache changed since gating",
			"book_id", bookID, "gated_title", gatedTitle, "cache_title", cand.Title,
			"gated_author", gatedAuthor, "cache_author", cand.Author)
		return fmt.Errorf("cached candidate for book %s changed since gating (want %q/%q, got %q/%q)",
			bookID, gatedTitle, gatedAuthor, cand.Title, cand.Author)
	}

	_, err = s.metadataFetchService.ApplyMetadataCandidate(bookID, cand, nil)
	return err
}

// WaitForOp implements maintenance.ServerDeps. It polls the database at 5-second
// intervals until the operation reaches a terminal state or ctx is canceled.
// Terminal states: completed, failed, canceled, interrupted_dropped, interrupted_quiesced.
func (s *Server) WaitForOp(ctx context.Context, opID string) error {
	store := s.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			row, err := store.GetOperationV2(opID)
			if err != nil {
				// DB error — keep polling; the op may still be in-flight.
				continue
			}
			if row == nil {
				// Not found yet — op may not be visible yet; keep polling.
				continue
			}
			switch row.Status {
			case "completed":
				return nil
			case "failed":
				return fmt.Errorf("child operation %s failed", opID)
			case "canceled":
				return fmt.Errorf("child operation %s was canceled", opID)
			case "interrupted_dropped", "interrupted_quiesced":
				return fmt.Errorf("child operation %s was interrupted (%s)", opID, row.Status)
			}
			// queued or running — continue polling.
		}
	}
}
