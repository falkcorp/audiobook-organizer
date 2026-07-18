// file: internal/plugins/maintenance/deps.go
// version: 1.5.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567891
// last-edited: 2026-07-18

// Package maintenance is the UOS plugin for all maintenance/janitor operations.
// It holds 26 OperationDefs migrated from the legacy scheduler_tasks.go.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ServerDeps is the narrow interface that *server.Server satisfies implicitly.
// All operations are expressed as methods so there is no import cycle.
type ServerDeps interface {
	Store() database.Store

	// ----- delegated run helpers -----

	// RunIsbnEnrichment delegates to server.runIsbnEnrichment (idempotent).
	RunIsbnEnrichment(ctx context.Context, progress operations.ProgressReporter, opID string) error
	// RunMetadataRefreshScan delegates to server.runMetadataRefreshScan (read-only).
	RunMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error
	// RunBulkWriteBack delegates to server.runBulkWriteBack (resumable via startIdx).
	RunBulkWriteBack(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter) error
	// RunAutoPurgeSoftDeleted delegates to server.runAutoPurgeSoftDeleted.
	RunAutoPurgeSoftDeleted(opID string)
	// ExecuteSeriesPrune delegates to server.executeSeriesPrune.
	ExecuteSeriesPrune(ctx context.Context, store database.Store, progress operations.ProgressReporter, opID string) error
	// ExecuteSeriesNormalizeCore delegates to server.executeSeriesNormalizeCore.
	// Returns slice of affected series IDs and any error.
	ExecuteSeriesNormalizeCore(ctx context.Context, store database.Store, enqueueWB func(string)) ([]string, error)

	// ----- one-shot startup ops -----

	// BackfillExternalIDs, RemuxMalformedM4BFiles, and TranscodeMalformedM4BFiles
	// take a progress callback (processed, total int, msg string) — total may
	// be 0 when unknown ahead of a paginated pass — and return the impl's
	// error so a fatal setup failure or persistence error can fail the op
	// instead of being silently swallowed (C2/H7). progress may be nil.
	BackfillExternalIDs(progress func(processed, total int, msg string)) error
	// StripMovementAtoms, RemuxMalformedM4BFiles, and TranscodeMalformedM4BFiles
	// take ctx so their per-file library walks stop early on cancellation
	// (SYS-1); pass the op's run context.
	StripMovementAtoms(ctx context.Context)
	RemuxMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string)) error
	TranscodeMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string)) error

	// ----- store helpers called by ops -----

	CleanupOrphanedTempFiles(rootDir string, opID string) int
	CleanupTrashedVersions() int
	SweepArchivedBooks() int

	// ----- accessors for optional components -----

	// ActivityFlushOp flushes the activity log for the given operation.
	ActivityFlushOp(opID string)
	// EnqueueWriteBack enqueues a book for write-back via the batcher (no-op if nil).
	EnqueueWriteBack(bookID string)
	// PollBatch polls OpenAI for completed batch jobs; returns processed count.
	PollBatch(ctx context.Context) (int, error)
	// DedupLLMReview runs the LLM review of ambiguous dedup candidates.
	DedupLLMReview(ctx context.Context) error
	// InvalidateDedupCache invalidates the author-duplicates dedup cache.
	InvalidateDedupCache()
	// MetadataUpgradeRun runs the metadata upgrade scan up to limit books.
	MetadataUpgradeRun(ctx context.Context, limit int) (checked, upgraded, skipped, errs int, err error)
	// SearchTranscriptionCandidate finds the top-scoring metadata candidate for
	// bookID using transTitle as the query. The returned score may exceed 1.0
	// (uncapped scale with transcription boosts applied). Returns found=false
	// when no candidates exist or the service is unavailable. The caller is
	// responsible for applying score/title/author gates on the returned values.
	SearchTranscriptionCandidate(
		ctx context.Context,
		bookID string,
		transTitle string,
		transAuthor string,
	) (title string, author string, score float64, found bool, err error)
	// ApplyTranscriptionCandidate applies the top metadata candidate whose
	// title matches candTitle to the given book, re-fetching via the cached
	// search path. Relies on TASK-02 audio-confirm logic to set
	// MetadataReviewStatus="audio_confirmed" when the candidate title matches
	// the book's transcribed title.
	ApplyTranscriptionCandidate(
		ctx context.Context,
		bookID string,
		candTitle string,
		candAuthor string,
	) error
	// OptimizeAIScanStore optimizes the AI scan store (no-op if nil).
	OptimizeAIScanStore() error
	// OptimizeOLStore optimizes the OpenLibrary cache store (no-op if nil).
	OptimizeOLStore() error
	// PruneOldLogs prunes operation logs older than retentionDays.
	PruneOldLogs(retentionDays int) error
	// CompactActivityLog runs the activity log compact+summarize+prune cycle.
	CompactActivityLog(ctx context.Context,
		compactionDays, changeDays, debugDays int,
	) (compacted int, summarized int, pruned int, err error)

	// DedupTriageExactPending scans all pending book dedup candidates,
	// classifies each into one of five populations (genuine / stub / fragment /
	// title_leak / unknown), and returns a TriageReport. When apply is false
	// (dry-run, the default), no candidates are modified. When apply is true,
	// every candidate whose class is IsPurgeable (stub, title_leak) is
	// dismissed via UpdateCandidateStatus(id, "dismissed") — genuine, fragment,
	// and unknown candidates are never touched. Returns an error if the
	// embedding store is not initialised.
	DedupTriageExactPending(ctx context.Context, apply bool) (*TriageReport, error)

	// ----- feature flags -----

	HasDedupEngine() bool
	HasMetadataFetchService() bool
	HasISBNEnrichment() bool
	HasAIParsing() bool
	HasBatchPoller() bool
	RootDir() string
	LogRetentionDays() int
	PurgeSoftDeletedAfterDays() int
	ActivityLogCompactionDays() int
	ActivityLogRetentionChangeDays() int
	ActivityLogRetentionDebugDays() int
	BackupRetentionDays() int

	// ----- operation orchestration (used by library.optimize) -----

	// EnqueueOp enqueues a child operation by defID with optional params.
	// Returns the operation ID of the newly enqueued (or deduped existing) run.
	EnqueueOp(ctx context.Context, defID string, params any) (string, error)

	// WaitForOp blocks until the operation with the given ID reaches a terminal
	// state (completed, failed, canceled, dropped) or ctx is done.
	// Returns nil on success, non-nil on failure or context cancellation.
	WaitForOp(ctx context.Context, opID string) error
}

// ----- reporter adapter -----

// sdkToOpsAdapter wraps sdk.Reporter so that existing server helpers that
// accept operations.ProgressReporter can be called from UOS Run functions.
//
// Key difference:
//   - v1 ProgressReporter: Log(level string, message string, details *string) error
//   - v2 sdk.Reporter:     Log(level slog.Level, message string, attrs ...slog.Attr) error
type sdkToOpsAdapter struct {
	r sdk.Reporter
}

// newOpsAdapter creates a v1 ProgressReporter backed by a v2 sdk.Reporter.
func newOpsAdapter(r sdk.Reporter) operations.ProgressReporter {
	return &sdkToOpsAdapter{r: r}
}

func (a *sdkToOpsAdapter) UpdateProgress(current, total int, message string) error {
	return a.r.UpdateProgress(current, total, message)
}

func (a *sdkToOpsAdapter) Log(level, message string, details *string) error {
	var slogLevel slog.Level
	switch level {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	var attrs []slog.Attr
	if details != nil {
		attrs = append(attrs, slog.String("details", *details))
	}
	return a.r.Log(slogLevel, message, attrs...)
}

func (a *sdkToOpsAdapter) IsCanceled() bool {
	return a.r.IsCanceled()
}

// _ ensures the time import is used (used for Timeout fields in defs).
var _ = time.Duration(0)
