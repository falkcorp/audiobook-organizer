// file: internal/plugins/maintenance/deps.go
// version: 1.7.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567891
// last-edited: 2026-08-14

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

// StoreProvider exposes the database handle.
type StoreProvider interface {
	Store() database.Store
}

// MetadataRunners runs the metadata enrichment and write-back operations.
type MetadataRunners interface {
	// RunIsbnEnrichment delegates to server.runIsbnEnrichment (idempotent).
	RunIsbnEnrichment(ctx context.Context, progress operations.ProgressReporter, opID string) error
	// RunMetadataRefreshScan delegates to server.runMetadataRefreshScan (read-only).
	RunMetadataRefreshScan(ctx context.Context, progress operations.ProgressReporter) error
	// RunBulkWriteBack delegates to server.runBulkWriteBack (resumable via startIdx).
	RunBulkWriteBack(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter) error
	// BackfillExternalIDs, RemuxMalformedM4BFiles, and TranscodeMalformedM4BFiles
	// take a progress callback (processed, total int, msg string) — total may
	// be 0 when unknown ahead of a paginated pass — and return the impl's
	// error so a fatal setup failure or persistence error can fail the op
	// instead of being silently swallowed (C2/H7). progress may be nil.
	BackfillExternalIDs(progress func(processed, total int, msg string)) error
	// MetadataUpgradeRun runs the metadata upgrade scan up to limit books.
	// progress may be nil; when non-nil it is updated every 25 books checked
	// (M7, 2026-07 error-correction sweep — this is a 120-minute,
	// network-bound op that previously reported nothing between start and
	// result).
	MetadataUpgradeRun(ctx context.Context, limit int, progress operations.ProgressReporter) (checked, upgraded, skipped, errs int, err error)
}

// SeriesRunners runs the series maintenance operations.
type SeriesRunners interface {
	// ExecuteSeriesPrune delegates to server.executeSeriesPrune.
	ExecuteSeriesPrune(ctx context.Context, store database.Store, progress operations.ProgressReporter, opID string) error
	// ExecuteSeriesNormalizeCore delegates to server.executeSeriesNormalizeCore.
	// Returns slice of affected series IDs and any error.
	ExecuteSeriesNormalizeCore(ctx context.Context, store database.Store, enqueueWB func(string)) ([]string, error)
}

// MediaFileRunners runs the audio-container repair operations.
type MediaFileRunners interface {
	// StripMovementAtoms, RemuxMalformedM4BFiles, and TranscodeMalformedM4BFiles
	// take ctx so their per-file library walks stop early on cancellation
	// (SYS-1); pass the op's run context.
	StripMovementAtoms(ctx context.Context)
	RemuxMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string)) error
	TranscodeMalformedM4BFiles(ctx context.Context, progress func(processed, total int, msg string)) error
}

// CleanupRunners runs the retention and reclamation operations.
type CleanupRunners interface {
	// RunAutoPurgeSoftDeleted delegates to server.runAutoPurgeSoftDeleted.
	RunAutoPurgeSoftDeleted(opID string)
	CleanupOrphanedTempFiles(rootDir string, opID string) int
	CleanupTrashedVersions() int
	SweepArchivedBooks() int
	// PruneOldLogs prunes operation logs older than retentionDays.
	PruneOldLogs(retentionDays int) error
}

// ActivityLogOps covers the activity log.
type ActivityLogOps interface {
	// ActivityFlushOp flushes the activity log for the given operation.
	ActivityFlushOp(opID string)
	// CompactActivityLog runs the activity log compact+summarize+prune cycle.
	CompactActivityLog(ctx context.Context,
		compactionDays, changeDays, debugDays int,
	) (compacted int, summarized int, pruned int, err error)
}

// WriteBackOps covers the iTunes write-back queue.
type WriteBackOps interface {
	// EnqueueWriteBack enqueues a book for write-back via the batcher (no-op if nil).
	EnqueueWriteBack(bookID string)
	// PollBatch polls OpenAI for completed batch jobs; returns processed count.
	PollBatch(ctx context.Context) (int, error)
}

// DedupRunners runs the dedup review and triage operations.
type DedupRunners interface {
	// DedupLLMReview runs the LLM review of ambiguous dedup candidates.
	DedupLLMReview(ctx context.Context) error
	// DedupTriageExactPending scans all pending book dedup candidates,
	// classifies each into one of five populations (genuine / stub / fragment /
	// title_leak / unknown), and returns a TriageReport. When apply is false
	// (dry-run, the default), no candidates are modified. When apply is true,
	// every candidate whose class is IsPurgeable (stub, title_leak) is
	// dismissed via UpdateCandidateStatus(id, "dismissed") — genuine, fragment,
	// and unknown candidates are never touched. Returns an error if the
	// embedding store is not initialised.
	DedupTriageExactPending(ctx context.Context, apply bool) (*TriageReport, error)
}

// CacheInvalidator drops cached aggregates so they recompute.
type CacheInvalidator interface {
	// InvalidateDedupCache invalidates the author-duplicates dedup cache.
	InvalidateDedupCache()
	// InvalidateAuthorsCache invalidates the cached author list.
	//
	// Any op that renames or deletes an author MUST call this. The cache holds
	// a 24-hour TTL and only the entities API invalidated it, so a maintenance
	// op that mutated authors left the author list serving the pre-repair names
	// for up to a day -- the repair had landed in the store and in every book
	// record, and the one page a user would check to confirm it still showed
	// the old names. Measured 2026-08-14 after author-conjunction-repair.
	InvalidateAuthorsCache()
	// InvalidateSeriesCache invalidates the cached series list. Call it from any
	// op that created, renamed, merged or deleted a series: the cache carries a
	// 24-hour TTL and is warmed at startup, so without this the op's result is
	// invisible on /api/v1/series for up to a day and reads as a no-op.
	InvalidateSeriesCache()
}

// TranscriptionRunners covers transcription candidate search and apply.
type TranscriptionRunners interface {
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
}

// StoreOptimizer compacts the auxiliary stores.
type StoreOptimizer interface {
	// OptimizeAIScanStore optimizes the AI scan store (no-op if nil).
	OptimizeAIScanStore() error
	// OptimizeOLStore optimizes the OpenLibrary cache store (no-op if nil).
	OptimizeOLStore() error
}

// CapabilityProbes reports which optional subsystems are wired.
type CapabilityProbes interface {
	HasDedupEngine() bool
	HasMetadataFetchService() bool
	HasISBNEnrichment() bool
	HasAIParsing() bool
	HasBatchPoller() bool
}

// RuntimeConfig exposes the retention and path settings the jobs read.
type RuntimeConfig interface {
	RootDir() string
	LogRetentionDays() int
	PurgeSoftDeletedAfterDays() int
	ActivityLogCompactionDays() int
	ActivityLogRetentionChangeDays() int
	ActivityLogRetentionDebugDays() int
	BackupRetentionDays() int
}

// OpEnqueuer enqueues and awaits other operations.
type OpEnqueuer interface {
	// EnqueueOp enqueues a child operation by defID with optional params.
	// Returns the operation ID of the newly enqueued (or deduped existing) run.
	EnqueueOp(ctx context.Context, defID string, params any) (string, error)
	// WaitForOp blocks until the operation with the given ID reaches a terminal
	// state (completed, failed, canceled, dropped) or ctx is done.
	// Returns nil on success, non-nil on failure or context cancellation.
	WaitForOp(ctx context.Context, opID string) error
}

// ServerDeps is the narrow interface that *server.Server satisfies implicitly.
// All operations are expressed as methods so there is no import cycle.
//
// Split into the 14 interfaces above on 2026-08-18. At 43 methods this was the
// widest interface in the repository -- wider than anything in internal/database,
// which is where the debt was assumed to live.
//
// The name is retained as their composition so the method set is byte-identical:
// *server.Server still satisfies it implicitly, and the test fakes asserting
// `var _ ServerDeps = ...` (title_backfill_test.go, backfill_ops_test.go) compile
// unchanged. The type checker is what proves that.
//
// The payoff is in plugin_test.go, which skips three tests with "requires full
// ServerDeps stub": an op needing only CapabilityProbes and RuntimeConfig can now
// take those two rather than stubbing all 43.
type ServerDeps interface { //nolint:interfacebloat // transitional composition of the
	// 14 interfaces above, deleted once each op takes only the pieces it uses.
	StoreProvider
	MetadataRunners
	SeriesRunners
	MediaFileRunners
	CleanupRunners
	ActivityLogOps
	WriteBackOps
	DedupRunners
	CacheInvalidator
	TranscriptionRunners
	StoreOptimizer
	CapabilityProbes
	RuntimeConfig
	OpEnqueuer
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
