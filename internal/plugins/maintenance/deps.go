// file: internal/plugins/maintenance/deps.go
// version: 1.17.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567891
// last-edited: 2026-09-02

// Package maintenance is the UOS plugin for all maintenance/janitor operations.
// It holds 26 OperationDefs migrated from the legacy scheduler_tasks.go.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ----- store access -----
//
// These interfaces replaced a single `Store() database.Store` accessor on
// 2026-08-19. That one method handed all 398 methods to 111 call sites in this
// package, of which the ops actually need 53. Measured with go/packages at
// full type resolution: the union required to keep a single accessor was 85,
// and 46 of those 85 existed solely to serve TWO of the call sites, both of
// which forward the store into another package. Segregating those two keeps
// the common path at 53.
//
// The sub-interfaces are grouped by the entity each one touches, and each is
// independently under the interfacebloat limit of 8 declared entries, so the
// width is gone rather than pushed one level down.

// opsBookReader reads books.
type opsBookReader interface {
	CountAllBooks() (int, error)
	CountPrimaryBooks() (int, error)
	GetAllBooksCore(limit int, offset int) ([]database.BookCore, error)
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookSnapshots(id string, limit int) ([]database.BookSnapshot, error)
	ListBookIDs() ([]string, error)
}

// opsBookWriter creates, mutates and retires books.
type opsBookWriter interface {
	CreateBook(book *database.Book) (*database.Book, error)
	DeleteBook(id string) error
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	ListSoftDeletedBooks(limit int, offset int, olderThan *time.Time) ([]database.Book, error)
	RecomputeBookAggregates(bookID string) error
	ResolveTombstoneChains() (int, error)
	UpdateBook(id string, book *database.Book) (*database.Book, error)
}

// opsFileAndPathReader reads book files and the configured import paths.
type opsFileAndPathReader interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllImportPaths() ([]database.ImportPath, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// opsBookFileMutator creates and updates book_file rows.
type opsBookFileMutator interface {
	BatchUpsertBookFiles(files []*database.BookFile) error
	CreateBookFile(file *database.BookFile) error
	BatchCreateBookFiles(files []*database.BookFile) error
	SetBookFileHash(id string, hash string) error
	UpdateBookFile(id string, file *database.BookFile) error
}

// opsBookFileDeleter removes book_file rows.
type opsBookFileDeleter interface {
	DeleteBookFile(id string) error
	DeleteBookFilesByIDs(ids []string) error
}

// opsBookFileMover reassigns book_file rows between books.
//
// This is row reassignment, NOT an on-disk move: no file is relocated, only the
// book a row belongs to changes. Both forms recompute every affected book's
// aggregates; prefer the bulk one from any loop, because the singular form
// recomputes both of its books on every call.
type opsBookFileMover interface {
	MoveBookFilesToBook(fileIDs []string, sourceBookID string, targetBookID string) error
	MoveBookFilesToBookBulk(moves []database.BookFileMove, targetBookID string) error
}

// opsBookFileWriter creates, moves and mutates book files.
//
// Split into the three interfaces above on 2026-08-24, when adding
// MoveBookFilesToBookBulk took it to 9 declared entries and over the
// interfacebloat limit of 8. The name is retained as their composition so the
// method set stays byte-identical and no consumer moves — the type checker
// verifies that, because every implementation fails to compile if a method were
// dropped or re-signatured in the regrouping. Same shape as the
// BookFileReader/Writer/Deleter split in internal/database.
type opsBookFileWriter interface {
	opsBookFileMutator
	opsBookFileDeleter
	opsBookFileMover
}

// opsAuthorStore reads and mutates authors.
type opsAuthorStore interface {
	CreateAuthor(name string) (*database.Author, error)
	DeleteAuthor(id int) error
	GetAllAuthorBookCounts() (map[int]int, error)
	GetAllAuthorFileCounts() (map[int]int, error)
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	UpdateAuthorName(id int, name string) error
}

// opsSeriesStore reads and mutates series.
type opsSeriesStore interface {
	CreateSeries(name string, authorID *int) (*database.Series, error)
	DeleteSeries(id int) error
	GetAllSeries() ([]database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
	// Display may filter; anything that WRITES must not. A repoint-then-delete
	// loop that reads the Core listing getter cannot see non-primary versions,
	// so it leaves them pointing at a series it just deleted.
	GetBooksBySeriesIDAllVersions(seriesID int) ([]database.BookCore, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
}

// opsLinkStore reads and writes the joins hanging off a book: its authors and
// its external-ID mappings. Grouped together because both live on the join row
// rather than on either entity, so a regroup has to rewrite them as a pair.
type opsLinkStore interface {
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	ReassignExternalID(source string, externalID string, newBookID string) error
	ReassignExternalIDs(oldBookID string, newBookID string) error
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

// opsHousekeeping is the remainder that belongs to no single entity: the review
// queue, metadata field state, operation records, playlist listing and the
// compaction hook.
type opsHousekeeping interface {
	CreateOperationChange(change *database.OperationChange) error
	DeleteReviewItem(id string) error
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	ListReviewItems(filter database.ReviewFilter) ([]database.ReviewItem, int, error)
	ListUserPlaylists(playlistType string, limit int, offset int) ([]database.UserPlaylist, int, error)
	Optimize() error
	UpdateOperationResultData(id string, resultData string) error
	UpsertReviewItem(item database.ReviewItem) (database.ReviewItem, error)
}

// OpsStore is the 53 methods the maintenance ops need -- what they call directly
// plus what the package's own helpers require of a store handed to them. Exported
// so *server.Server can name it as a return type.
type OpsStore interface {
	opsBookReader
	opsBookWriter
	opsFileAndPathReader
	opsBookFileWriter
	opsAuthorStore
	opsSeriesStore
	opsLinkStore
	opsHousekeeping
}

// ReconcileStore is what reconcile.RunITunesHeal requires. It is spelled out
// here rather than imported because reconcile's own requirement is unexported;
// Go assigns interface to interface on method-set superset, so naming it is
// unnecessary. dedup.Store is embedded BY NAME so this re-narrows on its own
// when dedup narrows further.
type ReconcileStore interface {
	dedup.Store

	GetBookByID(id string) (*database.Book, error)
	GetBookFileByPID(itunesPID string) (*database.BookFile, error)
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// StoreProvider exposes the database handles the ops need. Store() used to hand
// out all 398 methods; the two forwarding ops now take exactly what they pass on.
type StoreProvider interface {
	// OpsStore is the common path: 53 methods, used by 39 of the 41 sites.
	OpsStore() OpsStore
	// ReconcileStore serves runITunesHeal, which forwards into internal/reconcile.
	ReconcileStore() ReconcileStore
	// PlaylistStore serves runITunesPlaylistImport, which forwards into
	// internal/itunes/service.
	PlaylistStore() database.UserPlaylistStore
	// MetadataCacheStore serves runMetadataCacheReap. The metadata-candidate
	// cache is a separate keyspace ("metadata_cache:<book_id>") with its own
	// four-method interface, and OpsStore names none of them -- so the reaper
	// gets its own accessor rather than widening the 53-method common path for
	// one caller.
	MetadataCacheStore() database.MetadataCacheStore
	// FileProvenanceStore serves runFileProvenanceCapture. The provenance
	// chain is its own keyspace ("file_prov:*") deliberately kept out of the
	// wide Store interface, so like the metadata cache it gets its own
	// accessor rather than widening the common path for one caller. Returns
	// nil when no layer of the store implements it, and the op treats that as
	// "not initialized" rather than panicking.
	//
	// Implementors MUST resolve the capability with database.AsCapability, not
	// a bare type assertion: in production the server's store is wrapped in a
	// decorator that hides every capability from a bare assertion, and the
	// accessor then returns nil on exactly the deployment that matters.
	FileProvenanceStore() database.FileProvenanceStore
	// ReviewStatusIndexStore serves runReviewStatusIndexRepair. Rebuilding the
	// review_item:status:* index is a store-internal repair that nothing else
	// should reach for, so like the provenance ledger it is kept out of Store
	// and resolved as a capability (same rule as above: database.AsCapability,
	// never a bare assertion). Returns nil when no layer of the store
	// implements it; the op reports that as "not supported" rather than
	// panicking.
	ReviewStatusIndexStore() database.ReviewStatusIndexRepairer
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
//
// Neither method takes a store. They used to thread a database.Store -- 398
// methods -- from the caller purely so the implementation could reach a store it
// already had: the implementor is *Server, which holds one, and the caller
// obtained the very same value from this same interface's StoreProvider.Store().
// Removing the parameter deletes the coupling rather than shrinking it, and the
// nil-store guard moved to the implementation with it.
type SeriesRunners interface {
	// ExecuteSeriesPrune delegates to server.executeSeriesPrune.
	ExecuteSeriesPrune(ctx context.Context, progress operations.ProgressReporter, opID string) error
	// ExecuteSeriesNormalizeCore delegates to server.executeSeriesNormalizeCore.
	// Returns slice of affected series IDs and any error.
	ExecuteSeriesNormalizeCore(ctx context.Context, enqueueWB func(string)) ([]string, error)
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
	// CompactActivityLog runs the activity log compact+summarize+prune cycle
	// and then repairs orphaned activity secondary index entries.
	// indexOrphansRemoved is reported separately because it counts index keys,
	// not activity rows: folding it into pruned would overstate how much
	// history the run discarded.
	CompactActivityLog(ctx context.Context,
		compactionDays, changeDays, debugDays int,
	) (compacted int, summarized int, pruned int, indexOrphansRemoved int64, err error)
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
