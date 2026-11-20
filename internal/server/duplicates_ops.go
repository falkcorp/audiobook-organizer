// file: internal/server/duplicates_ops.go
// version: 2.33.2
// guid: 8b3e1f92-d4c7-4a6e-b5f0-2a7c9d1e3f45
// last-edited: 2026-11-19

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
// Every def has two shells over one core (F5-13): this file's v2 shell and the
// native books.<id> kind. The cores reach the store, the merge service and the
// server-owned engines (filterReviewedAuthorGroups, executeSeriesPrune,
// organizeService, runBulkWriteBack) only through their env's narrow store and
// writer interfaces, which HEAD's shells fill from the server.

package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	logging "github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/gin-gonic/gin"
)

// Op ids of the dedup definitions.
const (
	dedupBookScanOpID        = "dedup.book-scan"
	dedupBookMergeOpID       = "dedup.book-merge"
	dedupBookCombineOpID     = "dedup.book-combine"
	dedupAuthorScanOpID      = "dedup.author-scan"
	dedupSeriesScanOpID      = "dedup.series-scan"
	dedupSeriesDedupOpID     = "dedup.series-dedup"
	dedupSeriesPruneOpID     = "dedup.series-prune"
	dedupSeriesMergeOpID     = "dedup.series-merge"
	dedupSeriesNormalizeOpID = "dedup.series-normalize"
)

// dedupStoreReady is the check every HEAD shell makes before it runs a body: the
// library store must be initialized. A native shell does the same with t7NeedStore.
func (s *Server) dedupStoreReady(id string) error {
	if s.storeForWiring() == nil {
		return fmt.Errorf("%s: database not initialized", id)
	}
	return nil
}

// dedupAnnounce closes a run's activity batch and writes its one summary row. A
// nil a (a native env, or a HEAD server with no activity writer) and an empty
// opID write nothing, as HEAD's own guard did.
func dedupAnnounce(a t7ActivityWriter, opID, opType, summary string) {
	t7Announce(a, opID, opType, "dedup", summary)
}

// dedupCacheSink is the server's dedup list cache, which the HTTP handlers serve
// the last scan from. It is in-process state, not a store, so a scan fills it
// through the env rather than a writer.
type dedupCacheSink interface {
	SetWithTTL(key string, value gin.H, ttl time.Duration)
	Set(key string, value gin.H)
	InvalidateAll()
}

// dedupScanStore is the read side of the three scans: the library reads and the
// duplicate engines that run over them (dedup.ScanBookDuplicates,
// dedup.ScanSeriesDuplicates), and the reviewed-group filter, which depends on the
// server's AI-scan state.
type dedupScanStore interface {
	userPreferenceReader
	// ScanBookDuplicates runs the three-tier duplicate-book scan, dropping dismissed groups.
	ScanBookDuplicates(ctx context.Context, dismissed map[string]bool, progress dedup.ProgressReporter) (dedup.BookScanResult, error)
	// ScanSeriesDuplicates groups the series by normalized name and sub-series pattern.
	ScanSeriesDuplicates(ctx context.Context, progress dedup.ProgressReporter) (dedup.SeriesScanResult, error)
	GetAllAuthors() ([]database.Author, error)
	GetAllAuthorBookCounts() (map[int]int, error)
	// FilterReviewedAuthorGroups drops the groups an AI scan already reviewed.
	FilterReviewedAuthorGroups(groups []dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup
}

// dedupScanEnv is what the three scan cores take from their shell (rules R2,
// R10). Cache and Activity are HEAD's publications of a scan; both are nil in a
// native env, whose Task returns the result instead (like SetResult).
type dedupScanEnv struct {
	t7Env
	Store    dedupScanStore
	Cache    dedupCacheSink
	Activity t7ActivityWriter
}

// headDedupScanEnv is the scan env HEAD's shells hand a core.
func (s *Server) headDedupScanEnv(rep opsregistry.Reporter) dedupScanEnv {
	return dedupScanEnv{
		t7Env:    t7HeadEnv(rep, true),
		Store:    headDedupScan{s: s},
		Cache:    s.dedupCache,
		Activity: headT7Activity{w: s.activityWriter},
	}
}

// headDedupScan is HEAD's side of dedupScanStore: the server's store and
// engines, each called as HEAD's bodies called them.
type headDedupScan struct{ s *Server }

func (h headDedupScan) GetUserPreference(key string) (*database.UserPreference, error) {
	return h.s.storeForWiring().GetUserPreference(key)
}

func (h headDedupScan) ScanBookDuplicates(ctx context.Context, dismissed map[string]bool, progress dedup.ProgressReporter) (dedup.BookScanResult, error) {
	return dedup.ScanBookDuplicates(ctx, h.s.storeForWiring(), dismissed, progress)
}

func (h headDedupScan) ScanSeriesDuplicates(ctx context.Context, progress dedup.ProgressReporter) (dedup.SeriesScanResult, error) {
	return dedup.ScanSeriesDuplicates(ctx, h.s.storeForWiring(), progress)
}

func (h headDedupScan) GetAllAuthors() ([]database.Author, error) {
	return h.s.storeForWiring().GetAllAuthors()
}

func (h headDedupScan) GetAllAuthorBookCounts() (map[int]int, error) {
	return h.s.storeForWiring().GetAllAuthorBookCounts()
}

func (h headDedupScan) FilterReviewedAuthorGroups(groups []dedup.AuthorDedupGroup) []dedup.AuthorDedupGroup {
	return h.s.filterReviewedAuthorGroups(groups)
}

// headDedupOps is HEAD's side of the dedup writers (and of the series-dedup
// preview): the merge service, the dedup engine, the duplicate engines and the
// server's caches, each called as HEAD's bodies called them.
type headDedupOps struct {
	headT7Activity
	s *Server
}

func (s *Server) headDedupOps() headDedupOps {
	return headDedupOps{headT7Activity: headT7Activity{w: s.activityWriter}, s: s}
}

// mergeSvc is the server's merge service. Without one, a fresh Service has no
// write-back batcher, so ITL removals are skipped in that mode (tests / iTunes
// write-back disabled): wire_handlers.go's getMergeService nil-path.
func (h headDedupOps) mergeSvc() *merge.Service {
	if h.s.mergeService != nil {
		return h.s.mergeService
	}
	return merge.NewService(h.s.storeForWiring())
}

func (h headDedupOps) MergeBooks(opID, keepID string, mergeIDs []string) (*merge.Result, []SkippedMergeLoser, error) {
	return applyBookMergeReroute(h.mergeSvc(), opID, keepID, mergeIDs)
}

func (h headDedupOps) CombineBooks(ctx context.Context, opID string, ids []string, keepID string, override *merge.CombineOverride) (*merge.CombineResult, error) {
	return h.mergeSvc().CombineBooksWithOptions(ids, keepID, override, merge.CombineOptions{OperationID: opID, Context: ctx})
}

func (h headDedupOps) CleanupMergeCandidates(ids []string) {
	if h.s.dedupEngine != nil {
		h.s.dedupEngine.CleanupCandidatesAfterMerge(ids)
	}
}

func (h headDedupOps) InvalidateDedupCache() { h.s.dedupCache.InvalidateAll() }

func (h headDedupOps) InvalidateSeriesCache() { h.s.InvalidateSeriesCache() }

func (h headDedupOps) PreviewSeriesDedup(ctx context.Context, opID string, progress dedup.ProgressReporter) (dedup.SeriesDedupResult, error) {
	// The dry run acquires no stand-down and journals nothing: the controller
	// and the opID only matter to an apply.
	return dedup.DedupSeries(ctx, h.s.storeForWiring(), opID, h.s, progress, true)
}

func (h headDedupOps) ApplySeriesDedup(ctx context.Context, opID string, scan dedup.ScanStandDownController, progress dedup.ProgressReporter) (dedup.SeriesDedupResult, error) {
	return dedup.DedupSeries(ctx, h.s.storeForWiring(), opID, scan, progress, false)
}

func (h headDedupOps) PruneSeries(ctx context.Context, opID string, progress operations.ProgressReporter) error {
	return h.s.executeSeriesPrune(ctx, h.s.storeForWiring(), progress, opID)
}

func (h headDedupOps) MergeSeries(ctx context.Context, opID string, keepID int, mergeIDs []int, customName string, progress dedup.ProgressReporter, opts dedup.MergeSeriesOptions) (dedup.SeriesMergeResult, error) {
	return dedup.MergeSeries(ctx, h.s.storeForWiring(), opID, keepID, mergeIDs, customName, progress, opts)
}

func (h headDedupOps) NormalizeSeries(ctx context.Context, opID string) ([]string, error) {
	return executeSeriesNormalizeCore(ctx, h.s.storeForWiring(), opID)
}

func (h headDedupOps) GetBookByID(id string) (*database.Book, error) {
	return h.s.storeForWiring().GetBookByID(id)
}

func (h headDedupOps) WriteBackBooks(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter, onDone func(bookID string)) error {
	return h.s.runBulkWriteBack(ctx, opID, bookIDs, doRename, startIdx, progress, onDone)
}

// headSeriesNormalize is headDedupOps plus the activity-log logger the
// in-place organize writes through.
type headSeriesNormalize struct {
	headDedupOps
	log *logger.StandardLogger
}

func (s *Server) headSeriesNormalize() headSeriesNormalize {
	return headSeriesNormalize{
		headDedupOps: s.headDedupOps(),
		log:          logger.NewWithActivityLog("series-normalize", s.storeForWiring()),
	}
}

func (h headSeriesNormalize) OrganizeBook(book *database.Book, opID string) error {
	// Recorded under this operation (CommitLanding), so the move -- a
	// multi-file book's per-file moves included -- can be undone.
	// ReOrganizeInPlace wrote no undo rows at all.
	_, err := h.s.organizeService.ReOrganizeInPlaceRecorded(book, opID, h.log)
	return err
}

// ── OperationDef registrations ────────────────────────────────────────────────

// RegisterBookDedupScanOp registers the "dedup.book-scan" v2 OperationDef.
func (s *Server) RegisterBookDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.bookDedupScanDef())
}

// bookDedupScanDef is HEAD's definition of dedup.book-scan. Its Run is the v2 shell.
func (s *Server) bookDedupScanDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              dedupBookScanOpID,
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
			if err := s.dedupStoreReady(dedupBookScanOpID); err != nil {
				return err
			}
			_, err := bookDedupScanCore(ctx, s.headDedupScanEnv(reporter))
			return err
		},
	}
}

// bookDedupScanResult is what dedup.book-scan found: the groups and the counts
// HEAD caches under "book-dedup-scan".
type bookDedupScanResult struct {
	Groups         []dedup.BookDupGroup `json:"groups"`
	GroupCount     int                  `json:"group_count"`
	DuplicateCount int                  `json:"duplicate_count"`
}

// bookDedupScanCore is the body of dedup.book-scan, shared by HEAD's shell and
// the native kind. It reads the library and writes nothing: HEAD's shell also
// hands it the dedup list cache and the activity log (env.Cache, env.Activity),
// which the native env leaves nil; the native Task returns the result instead.
func bookDedupScanCore(ctx context.Context, env dedupScanEnv) (bookDedupScanResult, error) {
	op := &opContext{ID: env.OpID, Type: dedupBookScanOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))

	progress := env.reporter()
	dismissed := loadDismissedDedupGroups(env.Store)

	logger.InfoCtx(ctx, "book duplicate scan starting")

	result, err := env.Store.ScanBookDuplicates(ctx, dismissed, progress)
	if err != nil {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "book duplicate scan failed", "err", err)
		return bookDedupScanResult{}, err
	}

	out := bookDedupScanResult{Groups: result.Groups, GroupCount: len(result.Groups), DuplicateCount: result.TotalDuplicates}
	if env.Cache != nil {
		env.Cache.SetWithTTL("book-dedup-scan", gin.H{
			"groups":          out.Groups,
			"group_count":     out.GroupCount,
			"duplicate_count": out.DuplicateCount,
		}, 30*time.Minute)
	}

	op.SetStatus("success")
	logger.InfoCtx(ctx, "book duplicate scan complete", "groups", len(result.Groups), "duplicates", result.TotalDuplicates)

	dedupAnnounce(env.Activity, env.OpID, dedupBookScanOpID,
		fmt.Sprintf("Book duplicate scan found %d groups (%d duplicates)", len(result.Groups), result.TotalDuplicates))
	return out, nil
}

// bookDedupScanNative is the native books.dedup.book-scan kind. It reads the
// library and writes nothing (HEAD declares no Writes and only library.read), so
// it is ReadOnly. The dedup list cache and the activity row are HEAD shell
// concerns, so the Task returns the result.
func (s *Server) bookDedupScanNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupBookScanOpID)
	c := t7Common(s.bookDedupScanDef(), sched)
	c.Effects = tasks.ReadOnly()
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, bookDedupScanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (bookDedupScanResult, error) {
			if _, err := t7DecodeNative[dedup.BookDedupScanParams](kind, raw); err != nil {
				return bookDedupScanResult{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return bookDedupScanResult{}, err
			}
			return bookDedupScanCore(rc.Context(), dedupScanEnv{t7Env: t7NativeEnv(rc), Store: deps.Store})
		},
	})
}

// RegisterBookMergeOp registers the "dedup.book-merge" v2 OperationDef.
func (s *Server) RegisterBookMergeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.bookMergeDef())
}

// bookMergeDef is HEAD's definition of dedup.book-merge. Its Run is the v2 shell.
func (s *Server) bookMergeDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              dedupBookMergeOpID,
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
			if err := s.dedupStoreReady(dedupBookMergeOpID); err != nil {
				return err
			}
			_, err := bookMergeCore(ctx, p, bookMergeEnv{
				t7Env:  t7HeadEnv(reporter, true),
				Writer: s.headDedupOps(),
			})
			return err
		},
	}
}

// bookMergeWriter is the writes of dedup.book-merge: the merge itself (the
// merge service: external-ID reassignment, ITL removals, soft-deleted losers,
// the survivor's flag and carried iTunes fields, the undo journal), the clean-up
// of the dedup candidates it made stale, the dedup list cache, and the activity
// row the run closes with.
type bookMergeWriter interface {
	// MergeBooks folds mergeIDs into keepID (applyBookMergeReroute).
	MergeBooks(opID, keepID string, mergeIDs []string) (*merge.Result, []SkippedMergeLoser, error)
	// CleanupMergeCandidates drops the dedup candidate rows ids made stale.
	CleanupMergeCandidates(ids []string)
	// InvalidateDedupCache drops every cached duplicate list.
	InvalidateDedupCache()
	t7ActivityWriter
}

// bookMergeEnv is what the shared core takes from its shell (rules R2, R10).
type bookMergeEnv struct {
	t7Env
	Writer bookMergeWriter
}

// bookMergeCore is the body of dedup.book-merge, shared by HEAD's shell and the
// native kind. The result payload is stored through env.SetResult when the shell
// has one (HEAD), and returned for the native Task.
func bookMergeCore(ctx context.Context, p dedup.BookMergeParams, env bookMergeEnv) (BookMergeOpResult, error) {
	opID := env.OpID
	op := &opContext{ID: opID, Type: dedupBookMergeOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))

	progress := env.reporter()

	op.AddEntity("books", p.KeepID)
	op.AddEntity("books", p.MergeIDs...)
	logger.InfoCtx(ctx, "book merge starting", "keep_id", p.KeepID, "merge_count", len(p.MergeIDs))

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
	mergeRes, skipped, err := env.Writer.MergeBooks(opID, p.KeepID, p.MergeIDs)
	if err != nil {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "book merge failed", "err", err)
		// Store what the page needs to say about a failed merge: the
		// op result is read by the same client that reads a finished
		// one, and a bare "failed" cannot tell a merge that changed
		// nothing from one that moved siblings or soft-deleted some
		// losers before it stopped (a PartialMergeError).
		_, partial := errors.AsType[*merge.PartialMergeError](err)
		failed := BookMergeOpResult{KeepID: p.KeepID, Skipped: skipped, Failed: true, Partial: partial, Error: err.Error()}
		if env.SetResult != nil {
			if serr := env.SetResult(failed); serr != nil {
				logger.WarnCtx(ctx, "book merge: failure payload not stored", "err", serr)
			}
		}
		return failed, err
	}
	for _, sk := range skipped {
		op.AddEntity("skipped_books", sk.BookID)
		logger.WarnCtx(ctx, "book merge skipped a loser", "book_id", sk.BookID, "reason", sk.Reason)
	}
	merged := 0
	if mergeRes != nil {
		merged = mergeRes.SoftDeleted
	}
	payload := BookMergeOpResult{KeepID: p.KeepID, Merged: merged, Skipped: skipped, NoOp: mergeRes == nil}
	if env.SetResult != nil {
		if err := env.SetResult(payload); err != nil {
			// The merge is done; only its report is lost. Say so rather than
			// failing an op that changed the library.
			logger.WarnCtx(ctx, "book merge: result payload not stored", "err", err)
		}
	}
	// The async twin of the link endpoints' response: which book holds
	// the group's flag and the losers' user state, and whether
	// Audiobookshelf lists it. Logged on the op and in the activity
	// line, since this op has no response body.
	out := mergeRes.FlagOutcome()
	env.Log(slog.LevelInfo, "book merge outcome",
		slog.String("primary_id", out.PrimaryID),
		slog.String("group_primary_id", out.GroupPrimaryID),
		slog.String("hidden_from_abs", out.HiddenFromABS),
		slog.String("state_holder_id", out.StateHolderID),
		slog.Int("merged", merged),
		slog.Int("skipped", len(skipped)))
	// Service takes no ProgressReporter; emit a final 100% so the op UI
	// completes cleanly.
	_ = progress.UpdateProgress(len(p.MergeIDs), len(p.MergeIDs), "Book merge complete")

	env.Writer.InvalidateDedupCache()
	// Only the losers that were folded in: a skipped (trashed)
	// loser was not merged, so its candidate rows are not stale.
	env.Writer.CleanupMergeCandidates(mergedLoserIDs(p.KeepID, p.MergeIDs, skipped, mergeRes))
	op.SetStatus("success")
	logger.InfoCtx(ctx, "book merge complete", "kept_id", p.KeepID, "merged_count", merged, "skipped_count", len(skipped))

	dedupAnnounce(env.Writer, opID, dedupBookMergeOpID, bookMergeSummary(p.KeepID, payload, out))
	return payload, nil
}

// bookMergeNative is the native books.dedup.book-merge kind. HEAD runs it only
// live (no Writes, no Preview, library.write), so a Preview run is refused. Its
// write set is inferred from what the merge service writes: the book rows, their
// file rows, the reassigned external IDs and listening state, the undo journal and
// the dedup candidates it cleans up.
func (s *Server) bookMergeNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupBookMergeOpID)
	c := t7Common(s.bookMergeDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResBookFiles, ops.ResExternalIDs, ops.ResUserState, ops.ResOperations, ops.ResEmbeddings)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, BookMergeOpResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (BookMergeOpResult, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return BookMergeOpResult{}, err
			}
			p, err := t7DecodeNative[dedup.BookMergeParams](kind, raw)
			if err != nil {
				return BookMergeOpResult{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return BookMergeOpResult{}, err
			}
			return bookMergeCore(rc.Context(), p, bookMergeEnv{t7Env: t7NativeEnv(rc), Writer: w})
		},
	})
}

// RegisterBookCombineOp registers the "dedup.book-combine" v2 OperationDef:
// POST /audiobooks/combine ("Merge into one book") runs here instead of inside
// the HTTP request. A combine moves every selected book's files onto the
// survivor and journals each step; on a library of thousands of multi-file
// books that took long enough to hang the page, which is why the handler only
// checks the request (merge.PreflightCombine) and queues this op.
//
// ConcurrencyKey is dedup.book-merge's: a combine and a merge that name the
// same books queue behind each other instead of interleaving (the service's
// own merge lock serializes the writes either way; the key keeps the queued
// ops from racing on the pre-checks).
//
// The op's own id is the combine's undo operation: reverting it
// (POST /operations/:id/revert) undoes the combine, and the result payload
// carries that id.
//
// The run honours its context between the absorbed books (CombineOptions.Context):
// the 1h Timeout (or a server shutdown) stops the combine before the next book
// rather than leaving it running, with the merge lock held, after the operation
// was reported timed out. The stop is stored as a failed, incomplete result
// carrying the undo ids, which the page words as "stopped part-way, Undo puts
// back what it changed". The op stays Cancellable:false: a user cancel would be
// the same part-way stop, which is not something to offer on a button.
func (s *Server) RegisterBookCombineOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.bookCombineDef())
}

// bookCombineDef is HEAD's definition of dedup.book-combine. Its Run is the v2 shell.
func (s *Server) bookCombineDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              dedupBookCombineOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "dedup",
		DisplayName:     "Book Combine",
		Description:     "Combine several single-file books into one multi-file book on the kept book; the absorbed books go to the trash and the combine can be undone.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     false,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "dedup.book-merge",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p dedup.BookCombineParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("dedup.book-combine: decode params: %w", err)
				}
			}
			if err := bookCombineValidate(p); err != nil {
				return err
			}
			if err := s.dedupStoreReady(dedupBookCombineOpID); err != nil {
				return err
			}
			_, err := bookCombineCore(ctx, p, bookCombineEnv{
				t7Env:  t7HeadEnv(reporter, true),
				Writer: s.headDedupOps(),
			})
			return err
		},
	}
}

// bookCombineValidate refuses a request that names no survivor or nothing to
// absorb. Both shells call it before the store check, as HEAD's body did.
func bookCombineValidate(p dedup.BookCombineParams) error {
	if p.KeepID == "" || len(p.MergeIDs) == 0 {
		return fmt.Errorf("%s: keep_id and at least one merge_id are required", dedupBookCombineOpID)
	}
	return nil
}

// bookCombineWriter is the writes of dedup.book-combine: the combine itself (the
// merge service moves every absorbed book's files onto the survivor and journals
// each step), the clean-up of the dedup candidates it made stale, the dedup list
// cache, and the activity row the run closes with.
type bookCombineWriter interface {
	// CombineBooks combines ids (the absorbed books plus the survivor) onto keepID,
	// journaling the undo pointer under opID and stopping between books once ctx is done.
	CombineBooks(ctx context.Context, opID string, ids []string, keepID string, override *merge.CombineOverride) (*merge.CombineResult, error)
	// CleanupMergeCandidates drops the dedup candidate rows ids made stale.
	CleanupMergeCandidates(ids []string)
	// InvalidateDedupCache drops every cached duplicate list.
	InvalidateDedupCache()
	t7ActivityWriter
}

// bookCombineEnv is what the shared core takes from its shell (rules R2, R10).
type bookCombineEnv struct {
	t7Env
	Writer bookCombineWriter
}

// bookCombineCore is the body of dedup.book-combine, shared by HEAD's shell and
// the native kind. The result payload is stored through env.SetResult when the
// shell has one (HEAD), and returned for the native Task. p has passed
// bookCombineValidate.
func bookCombineCore(ctx context.Context, p dedup.BookCombineParams, env bookCombineEnv) (BookCombineOpResult, error) {
	opID := env.OpID
	op := &opContext{ID: opID, Type: dedupBookCombineOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))
	op.AddEntity("books", p.KeepID)
	op.AddEntity("books", p.MergeIDs...)
	logger.InfoCtx(ctx, "book combine starting", "keep_id", p.KeepID, "merge_count", len(p.MergeIDs))

	// ALL ids (the absorbed books plus the survivor) and the survivor id.
	// OperationID: the combine's undo pointer is journaled under this
	// op, so reverting the op undoes the combine.
	res, err := env.Writer.CombineBooks(ctx, opID, append(append([]string{}, p.MergeIDs...), p.KeepID), p.KeepID, p.Override)
	if err != nil && merge.IsRetryLater(err) {
		// Refused with no absorbed file moved, for a reason that passes by
		// itself: the library index (the complete list of the rows holding
		// a path) is still loading, or other writers kept changing the
		// files the combine moves (database.ErrBookFileConflict). The same
		// combine succeeds once the cause is gone: the op ends as "retry
		// later", not as a failure, and the result says so. The error
		// text, which names the cause, is kept in retry_reason.
		op.SetStatus("retry_later")
		logger.WarnCtx(ctx, "book combine not run: retry later; no absorbed file moved", "err", err)
		cause, retryMessage := combineRetryLaterCause(err)
		retry := BookCombineOpResult{PrimaryID: p.KeepID, RetryLater: true, RetryReason: err.Error(), RetryCause: cause, Message: retryMessage}
		if env.SetResult != nil {
			if serr := env.SetResult(retry); serr != nil {
				logger.WarnCtx(ctx, "book combine: retry-later payload not stored", "err", serr)
			}
		}
		dedupAnnounce(env.Writer, opID, dedupBookCombineOpID, retry.Message)
		return retry, nil
	}
	if err != nil {
		op.SetStatus("failed")
		failed := BookCombineOpResult{PrimaryID: p.KeepID, Failed: true, Error: err.Error()}
		if incomplete, ok := errors.AsType[*merge.CombineIncompleteError](err); ok {
			// It may have moved files or retired books before it stopped.
			// The result names the ids that undo it and says so; the
			// store error itself stays in the log.
			logger.ErrorCtx(ctx, "book combine stopped part-way", "journal_id", incomplete.JournalID, "err", incomplete.Err)
			failed.Incomplete = true
			failed.JournalID = incomplete.JournalID
			failed.OperationID = incomplete.OperationID
			failed.Error = fmt.Sprintf("The combine stopped part-way and may already have moved some files or put some books in the trash. Revert operation %s to undo what it did.", incomplete.OperationID)
		} else {
			logger.ErrorCtx(ctx, "book combine failed", "err", err)
		}
		if env.SetResult != nil {
			if serr := env.SetResult(failed); serr != nil {
				logger.WarnCtx(ctx, "book combine: failure payload not stored", "err", serr)
			}
		}
		return failed, err
	}

	payload := newBookCombineOpResult(res, opID)
	if env.SetResult != nil {
		if serr := env.SetResult(payload); serr != nil {
			// The combine is done; only its report is lost.
			logger.WarnCtx(ctx, "book combine: result payload not stored", "err", serr)
		}
	}
	env.Writer.InvalidateDedupCache()
	env.Writer.CleanupMergeCandidates(combinedAwayIDs(p.MergeIDs, res.PrimaryID))
	op.SetStatus("success")
	logger.InfoCtx(ctx, "book combine complete", "primary_id", res.PrimaryID, "files_moved", res.FilesMoved, "books_deleted", res.BooksDeleted)
	dedupAnnounce(env.Writer, opID, dedupBookCombineOpID, payload.Message)
	return payload, nil
}

// bookCombineNative is the native books.dedup.book-combine kind. HEAD runs it
// only live (no Writes, no Preview, library.write), so a Preview run is refused.
// Its write set is inferred from what the combine writes: the survivor and the
// absorbed book rows, their file rows, the reassigned external IDs and listening
// state, the undo journal and the dedup candidates it cleans up. It stays
// non-cancellable (HEAD's Cancellable false is not carried: the kernel's cancel
// is the Task's own).
func (s *Server) bookCombineNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupBookCombineOpID)
	c := t7Common(s.bookCombineDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResBookFiles, ops.ResExternalIDs, ops.ResUserState, ops.ResOperations, ops.ResEmbeddings)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, BookCombineOpResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (BookCombineOpResult, error) {
			if err := t7LiveOnly(rc, kind); err != nil {
				return BookCombineOpResult{}, err
			}
			p, err := t7DecodeNative[dedup.BookCombineParams](kind, raw)
			if err != nil {
				return BookCombineOpResult{}, err
			}
			if err := bookCombineValidate(p); err != nil {
				return BookCombineOpResult{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return BookCombineOpResult{}, err
			}
			return bookCombineCore(rc.Context(), p, bookCombineEnv{t7Env: t7NativeEnv(rc), Writer: w})
		},
	})
}

// BookCombineOpResult is the dedup.book-combine op's result payload
// (GET /operations/:id/result). On success it is what the synchronous
// POST /audiobooks/combine used to answer with. Failed is set on the payload
// stored when the combine returned an error; Incomplete is set with it when the
// combine had journaled its undo pointer first (merge.CombineIncompleteError):
// it may have changed the library, and OperationID / JournalID undo that.
//
// RetryLater is the third ending: the combine was refused, with no absorbed
// file moved, only because the library index is still loading or other writers
// kept changing the files it moves (merge.IsRetryLater). The op is not failed
// and there is nothing to undo beyond the survivor's own-file attach that a busy
// refusal may already have journaled (a retry finds it done); RetryReason is the
// refusal's text, RetryCause is its machine-readable cause (CombineRetryCauseBusy
// or CombineRetryCauseLoading, which the UI picks its own text by) and Message
// says to try again shortly. Failed and RetryLater are never both set.
type BookCombineOpResult struct {
	PrimaryID    string `json:"primary_id"`
	FilesMoved   int    `json:"files_moved"`
	BooksDeleted int    `json:"books_deleted"`
	// JournalID and OperationID: POST /operations/:id/revert with
	// OperationID undoes the combine.
	JournalID   string `json:"journal_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	// JournalPending is why the journal could not be marked applied; it
	// stays pending and the revert replays it.
	JournalPending string   `json:"journal_pending,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	Message        string   `json:"message,omitempty"`
	Failed         bool     `json:"failed,omitempty"`
	Incomplete     bool     `json:"incomplete,omitempty"`
	Error          string   `json:"error,omitempty"`
	RetryLater     bool     `json:"retry_later,omitempty"`
	RetryReason    string   `json:"retry_reason,omitempty"`
	RetryCause     string   `json:"retry_cause,omitempty"`
}

// The causes a retry-later combine reports in BookCombineOpResult.RetryCause.
const (
	// CombineRetryCauseBusy: other writers kept changing the files the combine
	// moves (database.ErrBookFileConflict).
	CombineRetryCauseBusy = "busy"
	// CombineRetryCauseLoading: the library index, the complete list of the rows
	// holding a path, is still loading (database.ErrOwnershipLookupIncomplete).
	CombineRetryCauseLoading = "loading"
)

// The result's message for each cause of a retry-later combine. Each names only
// its own cause: the user is told what is actually wrong, not both candidates.
const (
	combineRetryLaterBusyMessage    = "Other writers are changing the files the combine moves; try the combine again shortly. No files were moved from the other books."
	combineRetryLaterLoadingMessage = "The library is still loading; try the combine again shortly. No files were moved from the other books."
)

// combineRetryLaterCause names why a retry-later combine (merge.IsRetryLater)
// was refused and the message that says so. A busy refusal is read first: it is
// the only one that can have got past the journal, so it is never reported as the
// library still loading.
func combineRetryLaterCause(err error) (cause, message string) {
	if errors.Is(err, database.ErrBookFileConflict) {
		return CombineRetryCauseBusy, combineRetryLaterBusyMessage
	}
	return CombineRetryCauseLoading, combineRetryLaterLoadingMessage
}

// newBookCombineOpResult builds the success payload from the combine's result,
// with the message the synchronous endpoint used to return.
//
// OperationID is ALWAYS set on success: it is the id POST
// /operations/:id/revert takes, and the Undo action on the combine's outcome
// toast is built from it. The merge service journals under opID, so the two are
// the same id; opID backs it up when the service reports none.
func newBookCombineOpResult(res *merge.CombineResult, opID string) BookCombineOpResult {
	out := BookCombineOpResult{
		PrimaryID:      res.PrimaryID,
		FilesMoved:     res.FilesMoved,
		BooksDeleted:   res.BooksDeleted,
		JournalID:      res.JournalID,
		OperationID:    cmp.Or(res.OperationID, opID),
		JournalPending: res.JournalPending,
		Warnings:       res.Warnings,
	}
	done := fmt.Sprintf("Combined %d files onto book; moved %d absorbed entries to trash", res.FilesMoved, res.BooksDeleted)
	if res.JournalPending != "" {
		// The combine applied but its journal could not be marked applied.
		// It stays pending and still names every step that ran, so the
		// revert replays it (the split-book merge's rule).
		out.Message = fmt.Sprintf("%s; its undo journal could not be finalized and is still pending: reverting operation %s replays what it recorded", done, out.OperationID)
	} else {
		out.Message = fmt.Sprintf("%s; revert operation %s to undo it", done, out.OperationID)
	}
	return out
}

// combinedAwayIDs is ids without the survivor: the books a combine absorbed,
// for DedupEngine.CleanupCandidatesAfterMerge.
func combinedAwayIDs(ids []string, primaryID string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != primaryID {
			out = append(out, id)
		}
	}
	return out
}

// mergedLoserIDs is the ids a finished dedup.book-merge op actually folded into
// keepID: the request's ids without the keep book, duplicates, or the losers
// the op skipped. Nothing was merged (a nil Result) means none.
func mergedLoserIDs(keepID string, mergeIDs []string, skipped []SkippedMergeLoser, res *merge.Result) []string {
	if res == nil {
		return nil
	}
	skip := map[string]bool{keepID: true}
	for _, sk := range skipped {
		skip[sk.BookID] = true
	}
	out := make([]string, 0, len(mergeIDs))
	for _, id := range mergeIDs {
		if skip[id] {
			continue
		}
		skip[id] = true
		out = append(out, id)
	}
	return out
}

// bookMergeSummary is the activity line of a finished dedup.book-merge op.
func bookMergeSummary(keepID string, r BookMergeOpResult, out merge.FlagOutcome) string {
	var msg string
	if r.NoOp {
		msg = fmt.Sprintf("Book merge did nothing: no live book was left to merge into %s", keepID)
	} else {
		msg = fmt.Sprintf("Book merge completed: merged %d books into %s", r.Merged, keepID)
	}
	if len(r.Skipped) > 0 {
		ids := make([]string, 0, len(r.Skipped))
		for _, sk := range r.Skipped {
			ids = append(ids, sk.BookID)
		}
		msg += fmt.Sprintf("; skipped %d already in the trash (%s)", len(r.Skipped), strings.Join(ids, ", "))
	}
	if out.GroupPrimaryID != "" {
		msg += fmt.Sprintf("; %s holds the group's primary flag and the listening state", out.GroupPrimaryID)
	}
	if out.HiddenFromABS != "" {
		msg += fmt.Sprintf("; Audiobookshelf will not list it (%s)", out.HiddenFromABS)
	}
	return msg
}

// applyBookMergeReroute performs the F6-rerouted book merge for the
// dedup.book-merge op through merge.Service.MergeBooksWithOptions, which
// reassigns external IDs to the winner, enqueues ITL removals, soft-deletes
// losers, and (CarryITunesFields) copies the losers' Book-level iTunes stats
// onto the keep book first-win. ReassignExternalIDs does not carry those
// fields, so without the carry they would survive only on the loser row and
// vanish on a later purge/archive sweep.
//
// The carry used to happen HERE: a GetBookByID + full-row UpdateBook of the
// keep book before the merge, outside mergeSerializeMu, with a write failure
// only logged (A1#10). That (a) let the merge soft-delete the losers after the
// stats failed to land, (b) reverted any write to the keep book made between
// the read and the UpdateBook, and (c) copied stats from a loser another merge
// had already consumed onto a keeper whose own merge was then refused. Inside
// the Service all three go away: the carry is read and written under the lock,
// in the same ModifyBook that marks the survivor primary, and after the
// soft-deleted-input guard.
//
// Extracted from the op Run body so the reroute (soft-delete + external-ID
// reassignment, NOT hard delete) is unit-testable on a real store.
//
// opID is the dedup.book-merge operation: a sibling move's undo pointer is
// journaled under it, so reverting the op puts the siblings back.
//
// An already-soft-deleted LOSER is skipped, not fatal. A merge group is built
// from a view of the library that can be minutes old (a review candidate, the
// 30-minute dedup scan cache, a selection a user made before trashing one of
// the books), and on 2026-10-21 one trashed copy failed a five-book merge
// outright ("book ... is soft-deleted and cannot be merged as loser"). The skip
// is driven by the Service's own refusal rather than a pre-read of the rows, so
// the Service's rules stay the only ones (a soft-deleted loser that already
// belongs to the group this merge resolves to is a replayed merge and is still
// admitted, untouched) and a book trashed between a read here and the Service's
// lock is caught too. The Service refuses before it writes anything, so each
// retry starts from unchanged rows, and every retry drops a loser, so the loop
// ends after at most len(losers) of them. A soft-deleted SURVIVOR is still an
// error.
//
// When no live loser is left the merge is a no-op: a nil Result and a nil
// error, with every dropped id in the skipped list so the caller can say why.
//
// It returns the merge's Result (nil when there was nothing to merge) and the
// losers it skipped.
func applyBookMergeReroute(ms *merge.Service, opID, keepID string, mergeIDs []string) (*merge.Result, []SkippedMergeLoser, error) {
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

	var skipped []SkippedMergeLoser
	for len(losers) > 0 {
		// Service takes ALL ids (losers + winner) and the winner id.
		res, err := ms.MergeBooksWithOptions(append(append([]string{}, losers...), keepID), keepID,
			merge.MergeOptions{CarryITunesFields: true, OperationID: opID})
		if err == nil {
			return res, skipped, nil
		}
		sd, ok := errors.AsType[*merge.SoftDeletedInputError](err)
		if !ok || sd.AsPrimary || sd.BookID == keepID || !slices.Contains(losers, sd.BookID) {
			return nil, skipped, err
		}
		skipped = append(skipped, SkippedMergeLoser{BookID: sd.BookID, Reason: SkipReasonAlreadyTrashed})
		losers = slices.DeleteFunc(losers, func(id string) bool { return id == sd.BookID })
	}
	// Nothing to merge: every id was the keep book / a duplicate, or every
	// loser was already in the trash.
	return nil, skipped, nil
}

// SkipReasonAlreadyTrashed is why a loser was left out of a book merge.
const SkipReasonAlreadyTrashed = "already in the trash (soft-deleted), so it was not merged"

// SkippedMergeLoser is one loser a book merge left out, and why. It is part of
// the dedup.book-merge op's result payload.
type SkippedMergeLoser struct {
	BookID string `json:"book_id"`
	Reason string `json:"reason"`
}

// BookMergeOpResult is the dedup.book-merge op's result payload
// (GET /operations/:id/result). Merged is the number of losers actually folded
// into KeepID; it is smaller than the request when losers were skipped.
type BookMergeOpResult struct {
	KeepID  string              `json:"keep_id"`
	Merged  int                 `json:"merged"`
	Skipped []SkippedMergeLoser `json:"skipped,omitempty"`
	// NoOp is set when nothing was merged (every loser was skipped, or the
	// request named only the keep book).
	NoOp bool `json:"no_op"`
	// Failed is set on the payload stored when the merge returned an error
	// (the op then also ends failed). Merged is 0 in it: what was or was not
	// folded in is what Partial says.
	Failed bool `json:"failed,omitempty"`
	// Partial is set with Failed when the merge changed something before it
	// stopped (merge.PartialMergeError: a sibling moved, a loser was
	// soft-deleted, or a loser's clean-up failed and it stayed live).
	Partial bool `json:"partial,omitempty"`
	// Error is the failure's text, with Failed.
	Error string `json:"error,omitempty"`
}

// RegisterAuthorDedupScanOp registers the "dedup.author-scan" v2 OperationDef.
//
// maintenance.author-dedup-scan ran a second copy until 2026-10-13 (04-P14a); its
// FormerID alias was dropped 2026-10-31 after a soak in which nothing used it
// (P14A-SOAK). The dedup_refresh scheduler task enqueues this def. The retired
// copy computed the groups and threw them away (no cache, no
// reviewed-group filter), at threshold 0.85 against this one's 0.9, so nothing was
// ported. Its cron (0 1 * * *) was never driven by a task, so no schedule moves.
// NOTE: The author-scan logic is not extracted because the only server-side
// step beyond calling dedup.FindDuplicateAuthors is s.filterReviewedAuthorGroups,
// which depends on server-owned state that cannot be cleanly injected.
func (s *Server) RegisterAuthorDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.authorDedupScanDef())
}

// authorDedupScanDef is HEAD's definition of dedup.author-scan. Its Run is the v2 shell.
func (s *Server) authorDedupScanDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              dedupAuthorScanOpID,
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
			if err := s.dedupStoreReady(dedupAuthorScanOpID); err != nil {
				return err
			}
			_, err := authorDedupScanCore(ctx, s.headDedupScanEnv(reporter))
			return err
		},
	}
}

// authorDedupScanResult is what dedup.author-scan found: the groups still worth
// a reviewer's time and their count, HEAD's "author-duplicates" cache entry.
type authorDedupScanResult struct {
	Groups []dedup.AuthorDedupGroup `json:"groups"`
	Count  int                      `json:"count"`
}

// authorDedupScanCore is the body of dedup.author-scan, shared by HEAD's shell
// and the native kind. Like the other scans it writes nothing itself; see
// bookDedupScanCore for env.Cache and env.Activity.
func authorDedupScanCore(ctx context.Context, env dedupScanEnv) (authorDedupScanResult, error) {
	op := &opContext{ID: env.OpID, Type: dedupAuthorScanOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))

	progress := env.reporter()

	logger.InfoCtx(ctx, "author duplicate scan starting")
	// We don't know the final count until authors load; start with N=0
	// and reset to a real N once we know it.
	sp := sdk.NewProgress(progress, 0)
	sp.Start("Fetching authors...")
	_ = progress.Log("info", "Fetching authors...", nil)

	authors, err := env.Store.GetAllAuthors()
	if err != nil {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "author scan failed to fetch authors", "err", err)
		return authorDedupScanResult{}, err
	}
	logger.InfoCtx(ctx, "authors loaded", "count", len(authors))
	// Re-create the helper now that we know N = len(authors).
	sp = sdk.NewProgress(progress, len(authors))
	sp.Start(fmt.Sprintf("Loaded %d authors, fetching book counts...", len(authors)))

	bookCounts, err := env.Store.GetAllAuthorBookCounts()
	if err != nil {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "author scan failed to fetch book counts", "err", err)
		return authorDedupScanResult{}, err
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
	groups = env.Store.FilterReviewedAuthorGroups(groups)

	for _, g := range groups {
		op.AddEntity("authors", strconv.Itoa(g.Canonical.ID))
		for _, v := range g.Variants {
			op.AddEntity("authors", strconv.Itoa(v.ID))
		}
	}

	out := authorDedupScanResult{Groups: groups, Count: len(groups)}
	if env.Cache != nil {
		env.Cache.SetWithTTL("author-duplicates", gin.H{"groups": out.Groups, "count": out.Count}, 30*time.Minute)
	}

	op.SetStatus("success")
	sp.Done(fmt.Sprintf("Found %d duplicate groups (after filtering reviewed)", len(groups)))
	logger.InfoCtx(ctx, "author duplicate scan complete", "groups", len(groups))

	dedupAnnounce(env.Activity, env.OpID, dedupAuthorScanOpID,
		fmt.Sprintf("Author duplicate scan found %d groups", len(groups)))
	return out, nil
}

// authorDedupScanNative is the native books.dedup.author-scan kind: ReadOnly, as
// the other scans.
func (s *Server) authorDedupScanNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupAuthorScanOpID)
	c := t7Common(s.authorDedupScanDef(), sched)
	c.Effects = tasks.ReadOnly()
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, authorDedupScanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (authorDedupScanResult, error) {
			if _, err := t7DecodeNative[dedup.AuthorDedupScanParams](kind, raw); err != nil {
				return authorDedupScanResult{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return authorDedupScanResult{}, err
			}
			return authorDedupScanCore(rc.Context(), dedupScanEnv{t7Env: t7NativeEnv(rc), Store: deps.Store})
		},
	})
}

// RegisterSeriesDedupScanOp registers the "dedup.series-scan" v2 OperationDef.
func (s *Server) RegisterSeriesDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.seriesDedupScanDef())
}

// seriesDedupScanDef is HEAD's definition of dedup.series-scan. Its Run is the v2 shell.
func (s *Server) seriesDedupScanDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              dedupSeriesScanOpID,
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
			if err := s.dedupStoreReady(dedupSeriesScanOpID); err != nil {
				return err
			}
			_, err := seriesDedupScanCore(ctx, s.headDedupScanEnv(reporter))
			return err
		},
	}
}

// seriesDedupScanResult is what dedup.series-scan found: HEAD's
// "series-duplicates" cache entry.
type seriesDedupScanResult struct {
	Groups      []dedup.SeriesDupGroup `json:"groups"`
	Count       int                    `json:"count"`
	TotalSeries int                    `json:"total_series"`
}

// seriesDedupScanCore is the body of dedup.series-scan, shared by HEAD's shell
// and the native kind. See bookDedupScanCore for env.Cache and env.Activity.
func seriesDedupScanCore(ctx context.Context, env dedupScanEnv) (seriesDedupScanResult, error) {
	op := &opContext{ID: env.OpID, Type: dedupSeriesScanOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))

	progress := env.reporter()

	logger.InfoCtx(ctx, "series duplicate scan starting")

	result, err := env.Store.ScanSeriesDuplicates(ctx, progress)
	if err != nil {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "series duplicate scan failed", "err", err)
		return seriesDedupScanResult{}, err
	}

	for _, g := range result.Groups {
		for _, sw := range g.Series {
			op.AddEntity("series", strconv.Itoa(sw.ID))
		}
	}

	out := seriesDedupScanResult{Groups: result.Groups, Count: len(result.Groups), TotalSeries: result.TotalSeries}
	if env.Cache != nil {
		env.Cache.Set("series-duplicates", gin.H{
			"groups":       out.Groups,
			"count":        out.Count,
			"total_series": out.TotalSeries,
		})
	}

	op.SetStatus("success")
	logger.InfoCtx(ctx, "series duplicate scan complete", "groups", len(result.Groups), "total_series", result.TotalSeries)

	dedupAnnounce(env.Activity, env.OpID, dedupSeriesScanOpID,
		fmt.Sprintf("Series duplicate scan found %d groups (of %d total series)", len(result.Groups), result.TotalSeries))
	return out, nil
}

// seriesDedupScanNative is the native books.dedup.series-scan kind: ReadOnly, as
// the other scans.
func (s *Server) seriesDedupScanNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupSeriesScanOpID)
	c := t7Common(s.seriesDedupScanDef(), sched)
	c.Effects = tasks.ReadOnly()
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, seriesDedupScanResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (seriesDedupScanResult, error) {
			if _, err := t7DecodeNative[dedup.SeriesDedupScanParams](kind, raw); err != nil {
				return seriesDedupScanResult{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return seriesDedupScanResult{}, err
			}
			return seriesDedupScanCore(rc.Context(), dedupScanEnv{t7Env: t7NativeEnv(rc), Store: deps.Store})
		},
	})
}

// SeriesMergeFailedIDsPrefix opens the list of series ids a failed
// dedup.series-merge run did not merge: "<prefix>12 15]: merged 0 of 2 ...". The
// web lane (useAuthorsLane) parses it; change both together. The list may be
// empty ("[]"): every series merged and a later step (linking authors) failed.
const SeriesMergeFailedIDsPrefix = "series merge left series ids ["

// SeriesDedupFailedIDsPrefix is the same for dedup.series-dedup: the duplicate
// series an apply kept. Nothing in the web lane parses it (its Merge All merges
// the groups the reviewer confirmed, one by one), but the Dedup page and any
// other client that polls the op read the failure text.
const SeriesDedupFailedIDsPrefix = "series dedup left series ids ["

// RegisterSeriesDedupOp registers the "dedup.series-dedup" v2 OperationDef.
func (s *Server) RegisterSeriesDedupOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.seriesDedupDef())
}

// seriesDedupDef is HEAD's definition of dedup.series-dedup. Its Run is the v2 shell.
func (s *Server) seriesDedupDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:          dedupSeriesDedupOpID,
		Preview:     opsregistry.PreviewSpec{Params: dedup.SeriesDedupParams{}},
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
			// Absent dry_run means TRUE. This op deletes series rows and had
			// no preview at all before TODO.md L3966, so the default has to be
			// the safe one; a caller that wants the writes says so explicitly.
			// Both spellings go through opmode.ResolveDryRun, which refuses a
			// body that states them with different values, before the op
			// touches anything.
			dryRun, modeErr := opmode.ResolveDryRun(dedupSeriesDedupOpID, p.DryRun, p.DryRunCamel)
			if modeErr != nil {
				return modeErr
			}
			if err := s.dedupStoreReady(dedupSeriesDedupOpID); err != nil {
				return err
			}
			h := s.headDedupOps()
			// Scan: s is the ScanStandDownController — the *Server methods in
			// server_maintenance_deps.go satisfy it.
			_, err := seriesDedupCore(ctx, seriesDedupEnv{
				t7Env:  t7HeadEnv(reporter, !dryRun),
				Store:  h,
				Writer: h,
				Scan:   s,
			})
			return err
		},
	}
}

// seriesDedupNativeParams are the native dedup.series-dedup params: HEAD's
// SeriesDedupParams without the dry_run fields, which the run's mode replaces.
type seriesDedupNativeParams struct {
	Detail string `json:"detail"`
}

// seriesDedupStore is the read side of dedup.series-dedup: its preview, which
// runs the apply's loop with both mutating calls guarded (a dry run acquires no
// stand-down and journals nothing).
type seriesDedupStore interface {
	// PreviewSeriesDedup reports what an apply WOULD merge, writing nothing.
	PreviewSeriesDedup(ctx context.Context, opID string, progress dedup.ProgressReporter) (dedup.SeriesDedupResult, error)
}

// seriesDedupWriter is the write side of dedup.series-dedup: the apply, the dedup
// list cache and the activity row.
type seriesDedupWriter interface {
	// ApplySeriesDedup merges every duplicate series group, standing the scan down
	// through scan for the duration (dedup.DedupSeries with dryRun false).
	ApplySeriesDedup(ctx context.Context, opID string, scan dedup.ScanStandDownController, progress dedup.ProgressReporter) (dedup.SeriesDedupResult, error)
	// InvalidateDedupCache drops every cached duplicate list.
	InvalidateDedupCache()
	t7ActivityWriter
}

// seriesDedupEnv is what the shared core takes from its shell (rules R2, R10).
// Writer is nil in a native Preview run, which builds none; a HEAD dry run still
// gets one, for the activity row its preview writes.
type seriesDedupEnv struct {
	t7Env
	Store  seriesDedupStore
	Writer seriesDedupWriter
	// Scan stands the library scan down for an apply: HEAD passes the server's
	// controller, a native run an adapter over rc.StandDown.
	Scan dedup.ScanStandDownController
}

// seriesDedupCore is the body of dedup.series-dedup, shared by HEAD's shell and
// the native kind. env.Live is false for a dry run, which is HEAD's default.
func seriesDedupCore(ctx context.Context, env seriesDedupEnv) (dedup.SeriesDedupResult, error) {
	dryRun := !env.Live
	opID := env.OpID
	op := &opContext{ID: opID, Type: dedupSeriesDedupOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))

	progress := env.reporter()

	logger.InfoCtx(ctx, "series deduplication starting", "dry_run", dryRun)

	// opID is both the undo-ledger OperationChange.OperationID and the
	// scan stand-down holder key: one id ties the journal rows to the
	// run that made them and to the gate that kept the scanner off
	// those rows while it did.
	var result dedup.SeriesDedupResult
	var err error
	if dryRun {
		result, err = env.Store.PreviewSeriesDedup(ctx, opID, progress)
	} else {
		result, err = env.Writer.ApplySeriesDedup(ctx, opID, env.Scan, progress)
		// A dry run wrote nothing, so there is nothing to invalidate. An apply
		// invalidates on EVERY exit that can follow a write -- including a
		// cancel or a lost stand-down lease, which return an error after
		// merging some groups -- or the cached duplicate list keeps offering
		// series that are already gone.
		env.Writer.InvalidateDedupCache()
	}
	if err != nil {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "series deduplication failed", "dry_run", dryRun, "err", err)
		return result, err
	}
	// An apply that kept a duplicate series is not a finished dedup. It
	// used to return nil here -- the refusal sat in result.Errors, the op
	// logged "complete", wrote a "completed" activity row, and a client
	// taking `completed` at its word treated the library as deduplicated
	// while the series was still there. The merged ones stay merged (no
	// rollback) and the cache is already invalidated; the failure names
	// what was kept.
	//
	// The condition is ANY error on an apply, not only a kept series:
	// DedupSeries also records, as errors, a book reassigned whose
	// undo-ledger row could not be written and a series deleted whose
	// ledger row could not be written (an un-undoable delete). Neither
	// leaves a series behind, so the id list is empty -- the same
	// contract as series-merge -- but "completed" would hide that the
	// run cannot be undone. A preview deletes nothing and keeps
	// reporting success: its errors are predictions.
	kept := !dryRun && len(result.Errors) > 0
	if kept {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "series deduplication incomplete",
			"total_merged", result.TotalMerged, "kept", len(result.LeftIDs), "errors", len(result.Errors))
	} else {
		op.SetStatus("success")
		logger.InfoCtx(ctx, "series deduplication complete",
			"dry_run", dryRun, "total_merged", result.TotalMerged,
			"books_reassigned", result.TotalBooksReassigned,
			"errors", len(result.Errors))
	}

	dedupAnnounce(env.Writer, opID, dedupSeriesDedupOpID, seriesDedupActivityMessage(result, dryRun))
	if kept {
		detail := strings.Join(result.Errors[:min(len(result.Errors), 3)], "; ")
		return result, fmt.Errorf("%s%s]: merged %d series, kept %d, %d error(s): %s",
			SeriesDedupFailedIDsPrefix, joinInts(result.LeftIDs),
			result.TotalMerged, len(result.LeftIDs), len(result.Errors), detail)
	}
	return result, nil
}

// seriesDedupNative is the native books.dedup.series-dedup kind. HEAD's
// dry_run (default true) becomes the run's mode: Preview runs the dry-run loop
// through the store and builds no writer, Live applies. Its write set is
// inferred: the repointed books, the deleted series and the undo journal.
func (s *Server) seriesDedupNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupSeriesDedupOpID)
	c := t7Common(s.seriesDedupDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResSeries, ops.ResOperations)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, dedup.SeriesDedupResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (dedup.SeriesDedupResult, error) {
			if _, err := t7DecodeNative[seriesDedupNativeParams](kind, raw); err != nil {
				return dedup.SeriesDedupResult{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return dedup.SeriesDedupResult{}, err
			}
			env := seriesDedupEnv{t7Env: t7NativeEnv(rc), Store: deps.Store}
			if env.Live {
				w, err := t7NewWriter(kind, deps, rc)
				if err != nil {
					return dedup.SeriesDedupResult{}, err
				}
				env.Writer = w
				env.Scan = &t7ScanController{acquire: t7NativeStandDown(rc, deps.StandDownKey)}
			}
			return seriesDedupCore(rc.Context(), env)
		},
	})
}

// seriesDedupActivityMessage is the activity-log row for a dedup.series-dedup
// run. It must say which mode ran (an operator who cannot tell a preview from an
// apply in the activity log is the exact trap the dry run exists to prevent) and
// must not say "completed" for an apply that kept series: that row is what the
// operator reads after the failure.
func seriesDedupActivityMessage(result dedup.SeriesDedupResult, dryRun bool) string {
	switch {
	case dryRun:
		return fmt.Sprintf(
			"Series deduplication PREVIEW (dry_run=true, nothing written): "+
				"would merge %d series and reassign %d books",
			result.TotalMerged, result.TotalBooksReassigned)
	case len(result.Errors) > 0:
		return fmt.Sprintf(
			"Series deduplication INCOMPLETE: merged %d series, reassigned %d books, "+
				"kept %d series, %d error(s) -- see the operation's errors",
			result.TotalMerged, result.TotalBooksReassigned, len(result.LeftIDs), len(result.Errors))
	default:
		return fmt.Sprintf(
			"Series deduplication completed: merged %d series, reassigned %d books",
			result.TotalMerged, result.TotalBooksReassigned)
	}
}

// RegisterSeriesPruneOp registers the "dedup.series-prune" v2 OperationDef.
// NOTE: series-prune logic is not extracted because it is entirely implemented
// by s.executeSeriesPrune in duplicates_handlers.go.
func (s *Server) RegisterSeriesPruneOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.seriesPruneDef())
}

// seriesPruneDef is HEAD's definition of dedup.series-prune. Its Run is the v2 shell.
func (s *Server) seriesPruneDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              dedupSeriesPruneOpID,
		FormerIDs:       []string{"maintenance.series-prune"},
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
		// Writes: repoints books between series and deletes series rows, so
		// the write-set gate serializes it against the other series writers
		// (series-normalize, series-merge, entities.series-rename) and
		// repairs.apply, whose series prune fixer applies these same cores
		// one row at a time.
		Writes:       []opsregistry.Resource{opsregistry.ResSeries},
		Permissions:  []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
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
			if err := s.dedupStoreReady(dedupSeriesPruneOpID); err != nil {
				return err
			}
			return seriesPruneCore(ctx, seriesPruneEnv{
				t7Env:  t7HeadEnv(reporter, true),
				Writer: s.headDedupOps(),
			})
		},
	}
}

// seriesPruneWriter is the writes of dedup.series-prune: the whole prune (merge
// duplicate series, delete orphans; executeSeriesPrune) and the activity row.
type seriesPruneWriter interface {
	// PruneSeries merges duplicate series and deletes orphans, journaling under opID.
	PruneSeries(ctx context.Context, opID string, progress operations.ProgressReporter) error
	t7ActivityWriter
}

// seriesPruneEnv is what the shared core takes from its shell (rules R2, R10).
type seriesPruneEnv struct {
	t7Env
	Writer seriesPruneWriter
}

// seriesPruneCore is the body of dedup.series-prune, shared by HEAD's shell and
// the native kind.
func seriesPruneCore(ctx context.Context, env seriesPruneEnv) error {
	// opID is the run's own id. It keys structured logging, the
	// activity log, and the OperationChange ledger rows this op writes.
	opID := env.OpID
	op := &opContext{ID: opID, Type: dedupSeriesPruneOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))

	logger.InfoCtx(ctx, "series prune starting")
	runErr := env.Writer.PruneSeries(ctx, opID, env.reporter())

	if runErr != nil {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "series prune failed", "err", runErr)
	} else {
		op.SetStatus("success")
		logger.InfoCtx(ctx, "series prune complete")
	}

	summary := "Series prune completed"
	if runErr != nil {
		summary = fmt.Sprintf("Series prune failed: %v", runErr)
	}
	dedupAnnounce(env.Writer, opID, dedupSeriesPruneOpID, summary)
	return runErr
}

// seriesPruneNative is the native books.dedup.series-prune kind. HEAD declares
// Writes(series), carried as is.
func (s *Server) seriesPruneNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupSeriesPruneOpID)
	c := t7Common(s.seriesPruneDef(), sched)
	c.Effects = t7Writes(ops.ResSeries)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if _, err := t7DecodeNative[dedup.SeriesPruneParams](kind, raw); err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, seriesPruneCore(rc.Context(), seriesPruneEnv{t7Env: t7NativeEnv(rc), Writer: w})
		},
	})
}

// RegisterSeriesMergeOp registers the "dedup.series-merge" v2 OperationDef.
func (s *Server) RegisterSeriesMergeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.seriesMergeDef())
}

// seriesMergeDef is HEAD's definition of dedup.series-merge. Its Run is the v2 shell.
func (s *Server) seriesMergeDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              dedupSeriesMergeOpID,
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
		// Writes: renames the kept series, so the write-set gate serializes it
		// against entities.series-rename and series-normalize.
		Writes:       []opsregistry.Resource{opsregistry.ResSeries},
		Permissions:  []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
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
			if err := s.dedupStoreReady(dedupSeriesMergeOpID); err != nil {
				return err
			}
			// Scan: s is the ScanStandDownController.
			return seriesMergeCore(ctx, p, seriesMergeEnv{
				t7Env:  t7HeadEnv(reporter, true),
				Writer: s.headDedupOps(),
				Scan:   s,
			})
		},
	}
}

// seriesMergeWriter is the writes of dedup.series-merge: the merge (reassign
// every book, rename the kept series, relink authors; dedup.MergeSeries), the
// dedup list cache and the activity row.
type seriesMergeWriter interface {
	// MergeSeries merges mergeIDs into keepID under opts' protections.
	MergeSeries(ctx context.Context, opID string, keepID int, mergeIDs []int, customName string, progress dedup.ProgressReporter, opts dedup.MergeSeriesOptions) (dedup.SeriesMergeResult, error)
	// InvalidateDedupCache drops every cached duplicate list.
	InvalidateDedupCache()
	t7ActivityWriter
}

// seriesMergeEnv is what the shared core takes from its shell (rules R2, R10).
type seriesMergeEnv struct {
	t7Env
	Writer seriesMergeWriter
	// Scan stands the library scan down for the merge: HEAD passes the server's
	// controller, a native run an adapter over rc.StandDown.
	Scan dedup.ScanStandDownController
}

// seriesMergeCore is the body of dedup.series-merge, shared by HEAD's shell and
// the native kind.
func seriesMergeCore(ctx context.Context, p dedup.SeriesMergeParams, env seriesMergeEnv) error {
	// opID is the run's own id. It keys structured logging, the
	// activity log, and the OperationChange ledger rows this op writes.
	opID := env.OpID
	op := &opContext{ID: opID, Type: dedupSeriesMergeOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))

	progress := env.reporter()

	op.AddEntity("series", strconv.Itoa(p.KeepID))
	for _, mid := range p.MergeIDs {
		op.AddEntity("series", strconv.Itoa(mid))
	}
	logger.InfoCtx(ctx, "series merge starting", "keep_id", p.KeepID, "merge_count", len(p.MergeIDs))

	// Locks are honoured unless the caller says otherwise, and the scanner is
	// stood down for the merge. The Authors
	// & series lane sends neither field and runs many of these per click --
	// Merge All used to be one dedup.series-dedup op, which had both
	// protections -- so they cannot be opt-in.
	result, err := env.Writer.MergeSeries(ctx, opID, p.KeepID, p.MergeIDs, p.CustomName, progress,
		dedup.MergeSeriesOptions{HonorLocks: p.HonorLocks == nil || *p.HonorLocks, Scan: env.Scan})
	if err != nil {
		// A cancel or a lost lease returns here after deleting some of the
		// series; the cached list must not keep offering them.
		env.Writer.InvalidateDedupCache()
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "series merge failed", "err", err)
		return err
	}

	// The result is READ, not discarded, and every count below is the
	// one MergeSeries actually achieved.
	//
	// This used to throw the result away and report len(p.MergeIDs) as
	// the merged count on a hardcoded "success". MergeSeries returns
	// (result, nil) on its normal path, so a run in which every delete
	// was REFUSED -- the state SERIES-DELETE-UNGUARDED (#2908) added,
	// and the state the ~6,893 phantom-ID population will actually
	// produce -- rendered as "Series merge completed: merged 3 series",
	// status success, zero errors. That trades a data-loss bug for a
	// silent no-op, which is the same failure one layer up.
	//
	// A non-nil return is what puts the refusal in front of the
	// operator: the reporter's op_errors_v2 table only records
	// error-level entries, and MergeSeries's own digest goes out at
	// warn. executeSeriesPrune already reports its refusals this way.
	// The op is ResumeDrop with no retry policy, so failing here marks
	// the run and does not re-attempt the merge.
	env.Writer.InvalidateDedupCache()
	logger.InfoCtx(ctx, "series merge complete", "kept_id", p.KeepID,
		"merged_count", result.MergedCount,
		"requested_count", len(p.MergeIDs),
		"errors", len(result.Errors))

	msg := fmt.Sprintf("Series merge completed: merged %d of %d series into series %d",
		result.MergedCount, len(p.MergeIDs), p.KeepID)
	if len(result.Errors) > 0 {
		msg = fmt.Sprintf(
			"Series merge INCOMPLETE: merged %d of %d series into series %d, %d error(s) — "+
				"a merged-from series is kept whenever books that could not be reassigned "+
				"(trashed rows) still reference it",
			result.MergedCount, len(p.MergeIDs), p.KeepID, len(result.Errors))
	}
	dedupAnnounce(env.Writer, opID, dedupSeriesMergeOpID, msg)

	if len(result.Errors) > 0 {
		op.SetStatus("failed")
		detail := strings.Join(result.Errors[:min(len(result.Errors), 10)], "; ")
		if len(result.Errors) > 10 {
			detail += fmt.Sprintf(" (and %d more)", len(result.Errors)-10)
		}
		// The id list leads: it is the part a client parses (see
		// SeriesMergeFailedIDsPrefix). It is empty when every series merged
		// and a later step failed.
		return fmt.Errorf("%s%s]: series merge merged %d of %d series with %d error(s): %s",
			SeriesMergeFailedIDsPrefix, joinInts(result.LeftIDs),
			result.MergedCount, len(p.MergeIDs), len(result.Errors), detail)
	}

	op.SetStatus("success")
	return nil
}

// seriesMergeNative is the native books.dedup.series-merge kind. HEAD declares
// Writes(series), carried as is.
func (s *Server) seriesMergeNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupSeriesMergeOpID)
	c := t7Common(s.seriesMergeDef(), sched)
	c.Effects = t7Writes(ops.ResSeries)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			p, err := t7DecodeNative[dedup.SeriesMergeParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, seriesMergeCore(rc.Context(), p, seriesMergeEnv{
				t7Env:  t7NativeEnv(rc),
				Writer: w,
				Scan:   &t7ScanController{acquire: t7NativeStandDown(rc, deps.StandDownKey)},
			})
		},
	})
}

// RegisterSeriesNormalizeOp registers the "dedup.series-normalize" v2 OperationDef.
// NOTE: series-normalize is not extracted because it depends on server-owned
// services: s.organizeService.ReOrganizeInPlace and s.runBulkWriteBack.
func (s *Server) RegisterSeriesNormalizeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.seriesNormalizeDef())
}

// seriesNormalizeDef is HEAD's definition of dedup.series-normalize. Its Run is the v2 shell.
func (s *Server) seriesNormalizeDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              dedupSeriesNormalizeOpID,
		FormerIDs:       []string{"maintenance.series-normalize"},
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
		// Writes: renames series, so the write-set gate serializes it against
		// entities.series-rename and dedup.series-merge.
		Writes:       []opsregistry.Resource{opsregistry.ResSeries},
		Permissions:  []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
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
			if err := s.dedupStoreReady(dedupSeriesNormalizeOpID); err != nil {
				return err
			}
			h := s.headSeriesNormalize()
			return seriesNormalizeCore(ctx, seriesNormalizeEnv{
				t7Env:  t7HeadEnv(reporter, true),
				Store:  h,
				Writer: h,
			})
		},
	}
}

// seriesNormalizeStore is the read side of dedup.series-normalize: the row of
// each affected book, read before it is organized.
type seriesNormalizeStore interface {
	GetBookByID(id string) (*database.Book, error)
}

// seriesNormalizeWriter is the writes of dedup.series-normalize: the rename and
// merge pass over contaminated series names (executeSeriesNormalizeCore), the
// series list cache, the in-place organize of each affected book, the tag
// write-back of the set, and the activity rows.
type seriesNormalizeWriter interface {
	// NormalizeSeries renames and merges contaminated series, journaling the
	// renames under opID, and returns the ids of the books it changed.
	NormalizeSeries(ctx context.Context, opID string) ([]string, error)
	// InvalidateSeriesCache drops the cached series list.
	InvalidateSeriesCache()
	// OrganizeBook re-organizes book in place, recording the move under opID.
	OrganizeBook(book *database.Book, opID string) error
	// WriteBackBooks rewrites the tags of bookIDs, calling onDone after every
	// examined book (written, failed or skipped).
	WriteBackBooks(ctx context.Context, opID string, bookIDs []string, doRename bool, startIdx int, progress operations.ProgressReporter, onDone func(bookID string)) error
	t7ActivityWriter
}

// seriesNormalizeEnv is what the shared core takes from its shell (rules R2, R10).
type seriesNormalizeEnv struct {
	t7Env
	Store  seriesNormalizeStore
	Writer seriesNormalizeWriter
}

// seriesNormalizeCore is the body of dedup.series-normalize, shared by HEAD's
// shell and the native kind.
func seriesNormalizeCore(ctx context.Context, env seriesNormalizeEnv) error {
	// opID is the run's own id. It keys structured logging, the
	// activity log, and the OperationChange ledger rows this op writes.
	opID := env.OpID
	op := &opContext{ID: opID, Type: dedupSeriesNormalizeOpID, Status: "pending"}
	ctx = logging.WithAttrs(ctx, slog.Any("", op))

	progress := env.reporter()

	logger.InfoCtx(ctx, "series normalization starting")
	_ = progress.Log("info", "Starting series name normalization...", nil)

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
	affectedBookIDs, opErr := env.Writer.NormalizeSeries(ctx, opID)
	if opErr != nil {
		logger.ErrorCtx(ctx, "series normalization reported errors; organizing the books it did collect before failing the operation",
			"err", opErr, "affected_books", len(affectedBookIDs))
		_ = progress.Log("warn", fmt.Sprintf(
			"Series normalization hit errors (%v); still organizing the %d books it collected so their files match the series rows that changed",
			opErr, len(affectedBookIDs)), nil)
	}

	for _, bookID := range affectedBookIDs {
		op.AddEntity("books", bookID)
	}

	// A rename changes the cached series list, which carries a 24-hour
	// TTL; without this the rename lands in the store while
	// /api/v1/series keeps serving the old names. maintenance.series-normalize
	// (merged into this op, 04-P5) did this and this body did not. Only
	// invalidate when rows actually changed: dropping a warm cache for a
	// no-op run costs a full recount for nothing.
	if len(affectedBookIDs) > 0 {
		env.Writer.InvalidateSeriesCache()
	}

	logger.InfoCtx(ctx, "series normalization normalize complete, now organizing", "affected_books", len(affectedBookIDs))
	_ = progress.Log("info", fmt.Sprintf("Renamed/merged series; organizing %d affected books...", len(affectedBookIDs)), nil)

	organizeFailed := 0
	organizeRefused := 0
	for _, bookID := range affectedBookIDs {
		if ctx.Err() != nil {
			op.SetStatus("failed")
			logger.ErrorCtx(ctx, "series normalization cancelled", "err", ctx.Err())
			// errors.Join, not a bare ctx.Err(): if the normalize pass already
			// returned a real failure (a list of series whose rename failed),
			// returning only "context canceled" discards the only durable record
			// of which rows still need attention. Both are non-nil here, so the
			// join keeps the actionable one.
			return errors.Join(opErr, ctx.Err())
		}
		book, bErr := env.Store.GetBookByID(bookID)
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
			logger.ErrorCtx(ctx, "book listed as affected does not resolve",
				"book_id", bookID, "op", "series-normalize")
			_ = progress.Log("warn", fmt.Sprintf(
				"book %s does not resolve: it was either deleted after this run collected it "+
					"(harmless), or its row exists but the in-memory index has lost it, in which "+
					"case its series changed and its files were NOT moved", bookID), nil)
			continue
		}
		// Recorded under this operation (CommitLanding), so the move -- a
		// multi-file book's per-file moves included -- can be undone.
		// ReOrganizeInPlace wrote no undo rows at all.
		if oErr := env.Writer.OrganizeBook(book, opID); oErr != nil {
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
			// An occupied destination organize declined (same audio it could not
			// verify, fragments collapsing onto one path, a pair declined on an
			// earlier run) is a recorded refusal, not a failure: the files and
			// rows are unchanged and a durable skip names the pair.
			if errors.Is(oErr, organizer.ErrDestinationConflictUnresolved) {
				organizeRefused++
				_ = progress.Log("info", fmt.Sprintf("book %s not organized: %v", bookID, oErr), nil)
				continue
			}
			organizeFailed++
			_ = progress.Log("warn", fmt.Sprintf("organize failed for book %s: %v", bookID, oErr), nil)
		}
	}
	if organizeRefused > 0 {
		logger.InfoCtx(ctx, "series normalization declined to organize some books by policy",
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
	// the others. A PR about accurate reporting does not get to round its own
	// summary off.
	if organizeFailed > 0 {
		organizeErr := fmt.Errorf("%d of %d affected books were not organized; their series rows changed, "+
			"so their files may no longer match their series path, and re-running this operation will not "+
			"retry them because the series names are already normalized. See the per-book warnings above "+
			"for each cause",
			organizeFailed, len(affectedBookIDs))
		logger.ErrorCtx(ctx, "series normalization could not organize every affected book",
			"failed", organizeFailed, "refused", organizeRefused, "total", len(affectedBookIDs))
		_ = progress.Log("warn", organizeErr.Error(), nil)
		opErr = errors.Join(opErr, organizeErr)
	}

	if len(affectedBookIDs) > 0 {
		logger.InfoCtx(ctx, "writing tags for affected books", "count", len(affectedBookIDs))
		_ = progress.Log("info", fmt.Sprintf("Writing tags for %d affected books...", len(affectedBookIDs)), nil)
		if wbErr := env.Writer.WriteBackBooks(ctx, opID, affectedBookIDs, false, 0, progress, nil); wbErr != nil {
			logger.WarnCtx(ctx, "tag write-back incomplete", "err", wbErr)
			_ = progress.Log("warn", fmt.Sprintf("tag write-back incomplete: %v", wbErr), nil)
		}
	}

	// Report the failure now that the recoverable work is done. The status
	// is still "failed" -- deferring it bought file consistency, not
	// silence.
	if opErr != nil {
		op.SetStatus("failed")
		logger.ErrorCtx(ctx, "series normalization failed", "err", opErr, "affected_books", len(affectedBookIDs))
		if opID != "" {
			env.Writer.FlushActivityOp(opID)
		}
		return opErr
	}

	op.SetStatus("success")
	logger.InfoCtx(ctx, "series normalization complete", "affected_books", len(affectedBookIDs))
	_ = progress.Log("info", "Series normalization complete.", nil)

	dedupAnnounce(env.Writer, opID, dedupSeriesNormalizeOpID,
		fmt.Sprintf("Series normalization completed for %d affected books", len(affectedBookIDs)))
	return nil
}

// seriesNormalizeNative is the native books.dedup.series-normalize kind. HEAD
// declares Writes(series), carried as is; the organize and tag write-back also
// write book and file rows, which HEAD's declaration does not name (surfaced in
// the PR, not widened here).
func (s *Server) seriesNormalizeNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(dedupSeriesNormalizeOpID)
	c := t7Common(s.seriesNormalizeDef(), sched)
	c.Effects = t7Writes(ops.ResSeries)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			if _, err := t7DecodeNative[dedup.SeriesNormalizeParams](kind, raw); err != nil {
				return struct{}{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, seriesNormalizeCore(rc.Context(), seriesNormalizeEnv{t7Env: t7NativeEnv(rc), Store: deps.Store, Writer: w})
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBookDedupScanOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBookMergeOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterBookCombineOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAuthorDedupScanOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesDedupScanOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesDedupOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesPruneOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesMergeOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterSeriesNormalizeOp(reg) })
}

// ── kept for reference: unused import guard ───────────────────────────────────

var _ = strings.Join // strings is used by series-normalize progress messages
