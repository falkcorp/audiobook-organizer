// file: internal/server/duplicates_ops.go
// version: 2.31.2
// guid: 8b3e1f92-d4c7-4a6e-b5f0-2a7c9d1e3f45
// last-edited: 2026-10-27

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
			mergeRes, skipped, err := applyBookMergeReroute(ms, opID, p.KeepID, p.MergeIDs)
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "book merge failed", "err", err)
				// Store what the page needs to say about a failed merge: the
				// op result is read by the same client that reads a finished
				// one, and a bare "failed" cannot tell a merge that changed
				// nothing from one that moved siblings or soft-deleted some
				// losers before it stopped (a PartialMergeError).
				_, partial := errors.AsType[*merge.PartialMergeError](err)
				failed := BookMergeOpResult{KeepID: p.KeepID, Skipped: skipped, Failed: true, Partial: partial, Error: err.Error()}
				if serr := opsregistry.ReporterSetResult(reporter, failed); serr != nil {
					logging.Warn(ctx, "book merge: failure payload not stored", "err", serr)
				}
				return err
			}
			for _, sk := range skipped {
				op.AddEntity("skipped_books", sk.BookID)
				logging.Warn(ctx, "book merge skipped a loser", "book_id", sk.BookID, "reason", sk.Reason)
			}
			merged := 0
			if mergeRes != nil {
				merged = mergeRes.SoftDeleted
			}
			payload := BookMergeOpResult{KeepID: p.KeepID, Merged: merged, Skipped: skipped, NoOp: mergeRes == nil}
			if err := opsregistry.ReporterSetResult(reporter, payload); err != nil {
				// The merge is done; only its report is lost. Say so rather than
				// failing an op that changed the library.
				logging.Warn(ctx, "book merge: result payload not stored", "err", err)
			}
			// The async twin of the link endpoints' response: which book holds
			// the group's flag and the losers' user state, and whether
			// Audiobookshelf lists it. Logged on the op and in the activity
			// line, since this op has no response body.
			out := mergeRes.FlagOutcome()
			_ = reporter.Log(slog.LevelInfo, "book merge outcome",
				slog.String("primary_id", out.PrimaryID),
				slog.String("group_primary_id", out.GroupPrimaryID),
				slog.String("hidden_from_abs", out.HiddenFromABS),
				slog.String("state_holder_id", out.StateHolderID),
				slog.Int("merged", merged),
				slog.Int("skipped", len(skipped)))
			// Service takes no ProgressReporter; emit a final 100% so the op UI
			// completes cleanly.
			_ = progress.UpdateProgress(len(p.MergeIDs), len(p.MergeIDs), "Book merge complete")

			s.dedupCache.InvalidateAll()
			if s.dedupEngine != nil {
				// Only the losers that were folded in: a skipped (trashed)
				// loser was not merged, so its candidate rows are not stale.
				s.dedupEngine.CleanupCandidatesAfterMerge(mergedLoserIDs(p.KeepID, p.MergeIDs, skipped, mergeRes))
			}
			op.SetStatus("success")
			logging.Info(ctx, "book merge complete", "kept_id", p.KeepID, "merged_count", merged, "skipped_count", len(skipped))

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				msg := bookMergeSummary(p.KeepID, payload, out)
				activity.EmitInfo(s.activityWriter, opID, "dedup.book-merge", "dedup", msg,
					activity.AlwaysShow)
			}
			return nil
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
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.book-combine",
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
			if p.KeepID == "" || len(p.MergeIDs) == 0 {
				return fmt.Errorf("dedup.book-combine: keep_id and at least one merge_id are required")
			}
			store := s.storeForWiring()
			if store == nil {
				return fmt.Errorf("dedup.book-combine: database not initialized")
			}
			opID := opsregistry.ReporterOpID(reporter)
			op := &logging.OpContext{ID: opID, Type: "dedup.book-combine", Status: "pending"}
			ctx = logging.WithOp(ctx, op)
			op.AddEntity("books", p.KeepID)
			op.AddEntity("books", p.MergeIDs...)
			logging.Info(ctx, "book combine starting", "keep_id", p.KeepID, "merge_count", len(p.MergeIDs))

			ms := s.mergeService
			if ms == nil {
				ms = merge.NewService(store)
			}
			// ALL ids (the absorbed books plus the survivor) and the survivor id.
			// OperationID: the combine's undo pointer is journaled under this
			// op, so reverting the op undoes the combine.
			res, err := ms.CombineBooksWithOptions(append(append([]string{}, p.MergeIDs...), p.KeepID), p.KeepID, p.Override,
				merge.CombineOptions{OperationID: opID, Context: ctx})
			if err != nil && merge.IsRetryLater(err) {
				// Refused with no absorbed file moved, for a reason that passes by
				// itself: the library index (the complete list of the rows holding
				// a path) is still loading, or other writers kept changing the
				// files the combine moves (database.ErrBookFileConflict). The same
				// combine succeeds once the cause is gone: the op ends as "retry
				// later", not as a failure, and the result says so. The error
				// text, which names the cause, is kept in retry_reason.
				op.SetStatus("retry_later")
				logging.Warn(ctx, "book combine not run: retry later; no absorbed file moved", "err", err)
				retry := BookCombineOpResult{PrimaryID: p.KeepID, RetryLater: true, RetryReason: err.Error(), Message: combineRetryLaterMessage}
				if serr := opsregistry.ReporterSetResult(reporter, retry); serr != nil {
					logging.Warn(ctx, "book combine: retry-later payload not stored", "err", serr)
				}
				if s.activityWriter != nil && opID != "" {
					activity.FlushOperation(s.activityWriter, opID)
					activity.EmitInfo(s.activityWriter, opID, "dedup.book-combine", "dedup", retry.Message, activity.AlwaysShow)
				}
				return nil
			}
			if err != nil {
				op.SetStatus("failed")
				failed := BookCombineOpResult{PrimaryID: p.KeepID, Failed: true, Error: err.Error()}
				if incomplete, ok := errors.AsType[*merge.CombineIncompleteError](err); ok {
					// It may have moved files or retired books before it stopped.
					// The result names the ids that undo it and says so; the
					// store error itself stays in the log.
					logging.Error(ctx, "book combine stopped part-way", "journal_id", incomplete.JournalID, "err", incomplete.Err)
					failed.Incomplete = true
					failed.JournalID = incomplete.JournalID
					failed.OperationID = incomplete.OperationID
					failed.Error = fmt.Sprintf("The combine stopped part-way and may already have moved some files or put some books in the trash. Revert operation %s to undo what it did.", incomplete.OperationID)
				} else {
					logging.Error(ctx, "book combine failed", "err", err)
				}
				if serr := opsregistry.ReporterSetResult(reporter, failed); serr != nil {
					logging.Warn(ctx, "book combine: failure payload not stored", "err", serr)
				}
				return err
			}

			payload := newBookCombineOpResult(res, opID)
			if serr := opsregistry.ReporterSetResult(reporter, payload); serr != nil {
				// The combine is done; only its report is lost.
				logging.Warn(ctx, "book combine: result payload not stored", "err", serr)
			}
			s.dedupCache.InvalidateAll()
			if s.dedupEngine != nil {
				s.dedupEngine.CleanupCandidatesAfterMerge(combinedAwayIDs(p.MergeIDs, res.PrimaryID))
			}
			op.SetStatus("success")
			logging.Info(ctx, "book combine complete", "primary_id", res.PrimaryID, "files_moved", res.FilesMoved, "books_deleted", res.BooksDeleted)
			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "dedup.book-combine", "dedup", payload.Message, activity.AlwaysShow)
			}
			return nil
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
// refusal's text and Message says to try again shortly. Failed and RetryLater are never both set.
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
}

// combineRetryLaterMessage is the result's message when the combine was not run
// because the library index is still loading or other writers kept changing the
// files it moves.
const combineRetryLaterMessage = "The library is still loading, or other writers are changing the files the combine moves; try the combine again shortly. No files were moved from the other books."

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
// maintenance.author-dedup-scan ran a second copy until 2026-10-13 (04-P14a); it
// resolves here as a FormerID and the dedup_refresh scheduler task enqueues this
// def. The retired copy computed the groups and threw them away (no cache, no
// reviewed-group filter), at threshold 0.85 against this one's 0.9, so nothing was
// ported. Its cron (0 1 * * *) was never driven by a task, so no schedule moves.
// NOTE: The author-scan logic is not extracted because the only server-side
// step beyond calling dedup.FindDuplicateAuthors is s.filterReviewedAuthorGroups,
// which depends on server-owned state that cannot be cleanly injected.
func (s *Server) RegisterAuthorDedupScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.author-scan",
		FormerIDs:       []string{"maintenance.author-dedup-scan"},
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
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:          "dedup.series-dedup",
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

			// opID is both the undo-ledger OperationChange.OperationID and the
			// scan stand-down holder key: one id ties the journal rows to the
			// run that made them and to the gate that kept the scanner off
			// those rows while it did. s is the ScanStandDownController — the
			// *Server methods in server_maintenance_deps.go satisfy it.
			result, err := dedup.DedupSeries(ctx, store, opID, s, progress, dryRun)
			// A dry run wrote nothing, so there is nothing to invalidate. An apply
			// invalidates on EVERY exit that can follow a write -- including a
			// cancel or a lost stand-down lease, which return an error after
			// merging some groups -- or the cached duplicate list keeps offering
			// series that are already gone.
			if !dryRun {
				s.dedupCache.InvalidateAll()
			}
			if err != nil {
				op.SetStatus("failed")
				logging.Error(ctx, "series deduplication failed", "dry_run", dryRun, "err", err)
				return err
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
				logging.Error(ctx, "series deduplication incomplete",
					"total_merged", result.TotalMerged, "kept", len(result.LeftIDs), "errors", len(result.Errors))
			} else {
				op.SetStatus("success")
				logging.Info(ctx, "series deduplication complete",
					"dry_run", dryRun, "total_merged", result.TotalMerged,
					"books_reassigned", result.TotalBooksReassigned,
					"errors", len(result.Errors))
			}

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				activity.EmitInfo(s.activityWriter, opID, "dedup.series-dedup", "dedup",
					seriesDedupActivityMessage(result, dryRun), activity.AlwaysShow)
			}
			if kept {
				detail := strings.Join(result.Errors[:min(len(result.Errors), 3)], "; ")
				return fmt.Errorf("%s%s]: merged %d series, kept %d, %d error(s): %s",
					SeriesDedupFailedIDsPrefix, joinInts(result.LeftIDs),
					result.TotalMerged, len(result.LeftIDs), len(result.Errors), detail)
			}
			return nil
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
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.series-prune",
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

			// Locks are honoured unless the caller says otherwise, and the scanner is
			// stood down for the merge (s is the ScanStandDownController). The Authors
			// & series lane sends neither field and runs many of these per click --
			// Merge All used to be one dedup.series-dedup op, which had both
			// protections -- so they cannot be opt-in.
			result, err := dedup.MergeSeries(ctx, store, opID, p.KeepID, p.MergeIDs, p.CustomName, progress,
				dedup.MergeSeriesOptions{HonorLocks: p.HonorLocks == nil || *p.HonorLocks, Scan: s})
			if err != nil {
				// A cancel or a lost lease returns here after deleting some of the
				// series; the cached list must not keep offering them.
				s.dedupCache.InvalidateAll()
				op.SetStatus("failed")
				logging.Error(ctx, "series merge failed", "err", err)
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
			s.dedupCache.InvalidateAll()
			logging.Info(ctx, "series merge complete", "kept_id", p.KeepID,
				"merged_count", result.MergedCount,
				"requested_count", len(p.MergeIDs),
				"errors", len(result.Errors))

			if s.activityWriter != nil && opID != "" {
				activity.FlushOperation(s.activityWriter, opID)
				msg := fmt.Sprintf("Series merge completed: merged %d of %d series into series %d",
					result.MergedCount, len(p.MergeIDs), p.KeepID)
				if len(result.Errors) > 0 {
					msg = fmt.Sprintf(
						"Series merge INCOMPLETE: merged %d of %d series into series %d, %d error(s) — "+
							"a merged-from series is kept whenever books that could not be reassigned "+
							"(trashed rows) still reference it",
						result.MergedCount, len(p.MergeIDs), p.KeepID, len(result.Errors))
				}
				activity.EmitInfo(s.activityWriter, opID, "dedup.series-merge", "dedup",
					msg, activity.AlwaysShow)
			}

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
		},
	})
}

// RegisterSeriesNormalizeOp registers the "dedup.series-normalize" v2 OperationDef.
// NOTE: series-normalize is not extracted because it depends on server-owned
// services: s.organizeService.ReOrganizeInPlace and s.runBulkWriteBack.
func (s *Server) RegisterSeriesNormalizeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "dedup.series-normalize",
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
			affectedBookIDs, opErr := executeSeriesNormalizeCore(ctx, store, opID)
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

			// A rename changes the cached series list, which carries a 24-hour
			// TTL; without this the rename lands in the store while
			// /api/v1/series keeps serving the old names. maintenance.series-normalize
			// (merged into this op, 04-P5) did this and this body did not. Only
			// invalidate when rows actually changed: dropping a warm cache for a
			// no-op run costs a full recount for nothing.
			if len(affectedBookIDs) > 0 {
				s.InvalidateSeriesCache()
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
				// Recorded under this operation (CommitLanding), so the move -- a
				// multi-file book's per-file moves included -- can be undone.
				// ReOrganizeInPlace wrote no undo rows at all.
				if _, oErr := s.organizeService.ReOrganizeInPlaceRecorded(book, opID, log2); oErr != nil {
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
			// the others. A PR about accurate reporting does not get to round its own
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
				if wbErr := s.runBulkWriteBack(ctx, opID, affectedBookIDs, false, 0, progress, nil); wbErr != nil {
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
