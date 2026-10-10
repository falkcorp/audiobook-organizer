// file: internal/server/apply_when_scanned_op.go
// version: 1.8.0
// guid: 4c1f7e2a-9b3d-4e85-a6f0-2d8c5b71e934
// last-edited: 2026-10-10

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/errhandling"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
	metadatahandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"golang.org/x/sync/errgroup"
)

// applyWhenScannedOpID is the durable op a metadata apply is handed to when
// the library scan holds its book longer than the request's wait bound.
//
// It MUST run while a scan is running, which is the whole point of it, so the
// def deliberately declares:
//
//   - no ConcurrencyKey: the key is static per def, so one would serialize
//     every queued apply of every book behind each other. Per-book
//     serialization comes from the scan lock itself (internal/scanlock).
//   - no Writes: Gate 3b would otherwise hold it back behind any running op
//     whose declared resources overlap (library.scan's).
//   - no scan stand-down: it is not a stand-down holder, so it never parks the
//     scan and Gate 3.5 (which only ever holds library.scan) is irrelevant.
//
// No plugin concurrency cap is configured for "metadata" (SetPluginMaxConcurrent
// has no caller), so Gate 2 does not hold it either.
const applyWhenScannedOpID = "metadata.apply-when-scanned"

// organizeWhenScannedOpID is the same op for a single-book organize, as its
// own def so it carries the organize route's permission. An op def's
// Permissions are static, and POST /operations/v2 lets any caller holding
// them enqueue the def with ANY params: were organize a kind of
// metadata.apply-when-scanned, a caller with library.edit_metadata but not
// library.organize could organize books through it. Each def refuses the
// other's kinds.
const organizeWhenScannedOpID = "library.organize-when-scanned"

// applyWhenScannedLog logs the op and the batch handler's per-book handoff.
var applyWhenScannedLog = logger.New("server.apply-when-scanned")

// batchBookLockWait bounds how long batch-apply-candidates waits, in total,
// for the books the scan held on its first pass. A var so tests can shorten it.
var batchBookLockWait = 60 * time.Second

// queuedApplyRunner runs the single-book kinds of a queued apply;
// *metadatahandler.Handler implements it (RunQueuedApply).
type queuedApplyRunner interface {
	RunQueuedApply(ctx context.Context, q metadatahandler.QueuedApply, beat func(msg string)) error
}

// applyWhenScannedParams is the op's params: the queued apply plus the
// preview switch every writing op carries (owner rule 2026-09-25: an omitted
// mode is a preview). EnqueueApplyWhenScanned always sends dry_run:false,
// because the user already asked for the apply itself; `{}` from anywhere
// else only reports what would run.
type applyWhenScannedParams struct {
	metadatahandler.QueuedApply
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
}

// queuedOrganizeRunner runs the "organize" kind;
// *handlers.OrganizeHandler implements it.
type queuedOrganizeRunner interface {
	RunQueuedOrganize(ctx context.Context, bookID string, beat func(msg string)) error
}

// EnqueueOrganizeWhenScanned implements handlers.OrganizeQueuer: the same
// durable op as a queued apply, kind "organize".
func (s *Server) EnqueueOrganizeWhenScanned(ctx context.Context, bookID string) (string, error) {
	if s.opRegistry == nil {
		return "", errors.New("no operations registry to queue the organize on")
	}
	live := false
	return s.opRegistry.EnqueueOp(ctx, organizeWhenScannedOpID, applyWhenScannedParams{
		QueuedApply: metadatahandler.QueuedApply{Kind: metadatahandler.QueuedOrganize, BookID: bookID}, DryRun: &live})
}

// EnqueueApplyWhenScanned implements metadatahandler.QueuedApplyEnqueuer.
func (s *Server) EnqueueApplyWhenScanned(ctx context.Context, q metadatahandler.QueuedApply) (string, error) {
	if s.opRegistry == nil {
		return "", errors.New("no operations registry to queue the change on")
	}
	live := false
	return s.opRegistry.EnqueueOp(ctx, applyWhenScannedOpID, applyWhenScannedParams{QueuedApply: q, DryRun: &live})
}

// RegisterApplyWhenScannedOp registers metadata.apply-when-scanned and
// library.organize-when-scanned.
func (s *Server) RegisterApplyWhenScannedOp(reg *opsregistry.Registry) error {
	if err := reg.RegisterOp(s.whenScannedDef(applyWhenScannedOpID, "metadata", "Apply Metadata After Scan",
		"Applies a metadata change to one book as soon as the running library scan has finished reading that book.",
		auth.PermLibraryEditMetadata, func(kind string) bool { return kind != metadatahandler.QueuedOrganize })); err != nil {
		return err
	}
	return reg.RegisterOp(s.whenScannedDef(organizeWhenScannedOpID, "library", "Organize After Scan",
		"Organizes one book as soon as the running library scan has finished reading that book.",
		auth.PermLibraryOrganize, func(kind string) bool { return kind == metadatahandler.QueuedOrganize }))
}

// whenScannedDef is one of the two queued-behind-the-scan defs. accepts says
// which kinds this def runs; any other kind fails before anything is read or
// written, so the def's permission always matches what it does.
func (s *Server) whenScannedDef(opID, plugin, display, desc string, perm auth.Permission, accepts func(kind string) bool) opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              opID,
		Plugin:          plugin,
		DisplayName:     display,
		Description:     desc,
		DefaultPriority: opsregistry.PriorityHigh,
		Cancellable:     true,
		// Survives a restart: the user was told the change is queued.
		ResumePolicy: opsregistry.ResumeRequeue,
		Liveness:     opsregistry.LivenessManual,
		// The wait beats every 30s; the apply itself is one uninterruptible
		// call (a multi-file tag write-back can take minutes), so the gap
		// allowed after the last beat is wider than the 5m default.
		ProgressTimeout: 20 * time.Minute,
		Timeout:         6 * time.Hour,
		Permissions:     []auth.Permission{perm},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter opsregistry.Reporter) error {
			var p applyWhenScannedParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return fmt.Errorf("%s: decode params: %w", opID, err)
			}
			q := p.QueuedApply
			if q.BookID == "" {
				return fmt.Errorf("%s: no book id", opID)
			}
			if !accepts(q.Kind) {
				return fmt.Errorf("%s does not run kind %q", opID, q.Kind)
			}
			dryRun, err := opmode.ResolveDryRun(opID, p.DryRun, p.DryRunCamel)
			if err != nil {
				return err
			}
			if dryRun {
				_ = reporter.UpdateProgress(1, 1, fmt.Sprintf("preview: would apply %s to book %s once the library scan has read it; nothing written", q.Kind, q.BookID))
				return nil
			}
			beat := func(msg string) { _ = reporter.UpdateProgress(0, 1, msg) }
			beat("waiting for the library scan to finish reading book " + q.BookID)
			switch q.Kind {
			case metadatahandler.QueuedOpResultCandidate:
				err = s.runQueuedOpResultCandidate(ctx, q, beat)
			case metadatahandler.QueuedOrganize:
				r := s.queuedOrganizeRunner
				if r == nil {
					return errors.New("apply-when-scanned: the organize handler is not wired")
				}
				err = r.RunQueuedOrganize(ctx, q.BookID, beat)
			default:
				h := s.applyWhenScannedHandler
				if h == nil {
					return errors.New("apply-when-scanned: the metadata handler is not wired")
				}
				err = h.RunQueuedApply(ctx, q, beat)
			}
			if errors.Is(err, metadatahandler.ErrQueuedAlreadyApplied) {
				// A re-run after the apply landed (ResumeRequeue after a
				// restart): complete, write nothing, no second history row.
				_ = reporter.UpdateProgress(1, 1, "book "+q.BookID+": "+err.Error()+"; nothing written")
				return nil
			}
			if err != nil {
				return err
			}
			_ = reporter.UpdateProgress(1, 1, "applied to book "+q.BookID)
			return nil
		},
	}
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterApplyWhenScannedOp(reg) })
}

// opResultApplyOutcome is one book's result in batch-apply-candidates.
type opResultApplyOutcome struct {
	applied bool
	skipped bool
	// blocked: refused before any write (certainty gate or rename
	// preflight); blockMsg says which.
	blocked  bool
	blockMsg string
	// queued: the scan held the book past the bound; queuedOpID runs it.
	queued     bool
	queuedOpID string
	errMsg     string
}

// loadOpResultApplyInputs loads a candidate-fetch operation's results by book
// and the sibling-part claim index over EVERY row of it (not only the books
// being applied), the universe the preview uses, so a subset request sees the
// same siblings. A book the index cannot read is recorded with what is still
// known; only a cancelled ctx fails.
func (s *Server) loadOpResultApplyInputs(ctx context.Context, opID string) (map[string]database.OperationResult, *applygate.ClaimIndex, error) {
	results, err := s.Ops().GetOperationResults(opID)
	if err != nil {
		return nil, nil, fmt.Errorf("load operation results: %w", err)
	}
	byBook := make(map[string]database.OperationResult, len(results))
	for _, r := range results {
		byBook[r.BookID] = r
	}
	claims, err := buildClaimIndex(ctx, keysOf(byBook), opResultClaimLoader(s.store, func(id string) (CandidateResult, bool, error) {
		r, ok := byBook[id]
		if !ok {
			return CandidateResult{}, false, nil
		}
		var cr CandidateResult
		if err := json.Unmarshal([]byte(r.ResultJSON), &cr); err != nil {
			return CandidateResult{}, false, fmt.Errorf("decode operation result: %w", err)
		}
		return cr, true, nil
	}))
	if err != nil {
		return nil, nil, fmt.Errorf("build the sibling-part index: %w", err)
	}
	return byBook, claims, nil
}

// applyOpResultBooks applies each book under its own scan lock, in two passes,
// and returns the outcomes in request order (each worker owns one slot, read
// only after Wait, so the slots need no lock).
//
// Pass 1 only TRIES each book's lock: a book the scan is reading is set aside
// rather than parking one of the batchApplyConcurrency workers, so every other
// book is applied at full speed. Pass 2 waits for the set-aside books, all
// sharing one batchBookLockWait bound, and hands any the scan still holds to
// metadata.apply-when-scanned. Nothing is refused because a scan is running.
func (s *Server) applyOpResultBooks(ctx context.Context, opID string, bookIDs []string, byBook map[string]database.OperationResult, claims *applygate.ClaimIndex) []opResultApplyOutcome {
	outcomes := make([]opResultApplyOutcome, len(bookIDs))
	busy := make([]bool, len(bookIDs))
	// One import-path read shared by every book of this call.
	books := withCachedImportPaths(s.store)

	pass := func(waitCtx context.Context, only []bool) {
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(batchApplyConcurrency)
		for i, bookID := range bookIDs {
			if only != nil && !only[i] {
				continue
			}
			g.Go(func() error {
				if gctx.Err() != nil {
					outcomes[i] = opResultApplyOutcome{errMsg: fmt.Sprintf("%s: canceled: %v", bookID, gctx.Err())}
					return nil
				}
				var hold *scanlock.Hold
				if waitCtx == nil {
					h, ok := scanlock.Books.TryLockSet([]string{bookID})
					if !ok {
						busy[i] = true
						return nil
					}
					hold = h
				} else {
					h, err := scanlock.Books.LockSet(waitCtx, []string{bookID})
					if err != nil {
						outcomes[i] = s.queueOpResultCandidate(ctx, opID, bookID)
						return nil
					}
					hold = h
				}
				defer hold.Release()
				outcomes[i] = s.applyOpResultCandidateLocked(ctx, books, opID, bookID, byBook, claims)
				return nil
			})
		}
		// Every worker returns nil (failures are in outcomes), so an error
		// here is a bug; log it rather than drop it.
		errhandling.MustLog(g.Wait(), "batch candidate apply: a worker returned an unexpected error")
	}

	pass(nil, nil)
	anyBusy := false
	for _, b := range busy {
		anyBusy = anyBusy || b
	}
	if !anyBusy {
		return outcomes
	}
	waitCtx, cancel := context.WithTimeout(ctx, batchBookLockWait)
	defer cancel()
	if s.opRegistry == nil {
		// Nothing to hand a book to: wait on the request itself.
		waitCtx = ctx
	}
	pass(waitCtx, busy)
	return outcomes
}

// queueOpResultCandidate hands one book the scan still holds to
// metadata.apply-when-scanned. A cancelled request is reported as such.
func (s *Server) queueOpResultCandidate(ctx context.Context, opID, bookID string) opResultApplyOutcome {
	if ctx.Err() != nil {
		return opResultApplyOutcome{errMsg: fmt.Sprintf("%s: canceled: %v", bookID, ctx.Err())}
	}
	qid, err := s.EnqueueApplyWhenScanned(ctx, metadatahandler.QueuedApply{
		Kind: metadatahandler.QueuedOpResultCandidate, BookID: bookID, OperationID: opID,
	})
	if err != nil {
		return opResultApplyOutcome{errMsg: fmt.Sprintf("%s: the library scan is reading it and the change could not be queued: %v", bookID, err)}
	}
	applyWhenScannedLog.Info("batch-apply-candidates: book %s queued behind the library scan as operation %s",
		logger.SanitizeLogValue(bookID), qid)
	return opResultApplyOutcome{queued: true, queuedOpID: qid}
}

// runQueuedOpResultCandidate is metadata.apply-when-scanned for one book of a
// batch-apply-candidates request: wait for the book, then apply exactly as the
// request would have. A block or error fails the op so the user sees why.
func (s *Server) runQueuedOpResultCandidate(ctx context.Context, q metadatahandler.QueuedApply, beat func(string)) error {
	hold, err := metadatahandler.WaitForBook(ctx, q.BookID, beat)
	if err != nil {
		return err
	}
	defer hold.Release()
	defer invalidateMetadataResultsCache()
	byBook, claims, err := s.loadOpResultApplyInputs(ctx, q.OperationID)
	if err != nil {
		return err
	}
	o := s.applyOpResultCandidateLocked(ctx, s.store, q.OperationID, q.BookID, byBook, claims)
	switch {
	case o.blocked:
		return fmt.Errorf("not applied: %s", o.blockMsg)
	case o.errMsg != "":
		return errors.New(o.errMsg)
	}
	return nil
}

// applyOpResultCandidateLocked applies one book's stored candidate. The caller
// holds the book's scan lock; the file work is submitted before this returns,
// so the pool's pending mark is in place before the lock is released and the
// scanner cannot read the old tags in between.
func (s *Server) applyOpResultCandidateLocked(ctx context.Context, books bookReader, opID, bookID string, byBook map[string]database.OperationResult, claims *applygate.ClaimIndex) opResultApplyOutcome {
	mfs := s.metadataFetchService
	opResult, ok := byBook[bookID]
	if !ok {
		return opResultApplyOutcome{skipped: true}
	}
	var cr CandidateResult
	if err := json.Unmarshal([]byte(opResult.ResultJSON), &cr); err != nil {
		return opResultApplyOutcome{errMsg: fmt.Sprintf("%s: failed to parse result", bookID)}
	}
	if cr.Candidate == nil || cr.Status != "matched" {
		return opResultApplyOutcome{skipped: true}
	}

	// Certainty gate: score floor, fetch-time identity, and the
	// sequence-number guard. The "matched" status above is only "the
	// top non-rejected candidate", with no floor behind it.
	plan := planOpResultApply(books, bookID, cr, claims)
	if plan.Reason == applySkipMarkedNoMatch {
		// Marked "no match" since the fetch: nothing to apply.
		return opResultApplyOutcome{skipped: true}
	}
	if plan.Reason == applySkipGateBlocked {
		return opResultApplyOutcome{blocked: true, blockMsg: fmt.Sprintf("%s: %v", bookID, plan.Err)}
	}
	if plan.Reason != "" {
		return opResultApplyOutcome{errMsg: fmt.Sprintf("%s: %s: %v", bookID, plan.Reason, plan.Err)}
	}
	candidate := *plan.Candidate

	// The apply below writes the database first; the rename runs afterwards
	// in the file-IO job queued further down. That is the order in which
	// three books got new metadata and old file names on 2026-09-13. When the
	// rename is known to fail, refuse here, before any write -- no apply, no
	// "applied" op-result row, no file job -- with the same read-only
	// preflight the cached batch apply (#3385) and the single-book apply
	// (#3389) run. Gated exactly as the file job is: a pool means a rename
	// follows. Reported as blocked, not as an error: nothing was written.
	if s.fileIOPool != nil {
		if perr := mfs.RenamePreflightWithOptions(bookID, candidate, nil, metafetch.ApplyOptions{FillOnly: true}); perr != nil {
			batchApplyCandidatesLog.Warn("batch-apply-candidates: refused %s before any write: %s",
				logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(perr.Error()))
			return opResultApplyOutcome{blocked: true, blockMsg: fmt.Sprintf("%s: %s: %v",
				bookID, metafetch.ApplyRefusedReasonFileWorkWouldFail, perr)}
		}
	}

	// Batch apply: fill-only (owner decision A3#3).
	resp, err := mfs.ApplyMetadataCandidateWithOptions(bookID, candidate, nil, metafetch.ApplyOptions{FillOnly: true})
	if err != nil && errors.Is(err, metafetch.ErrMarkedNoMatch) {
		// Marked "no match" between the plan and the apply.
		return opResultApplyOutcome{skipped: true}
	}
	if err != nil {
		return opResultApplyOutcome{errMsg: fmt.Sprintf("%s: apply failed: %v", bookID, err)}
	}
	pendingCover := ""
	if resp != nil {
		pendingCover = resp.PendingCoverURL
	}

	// Persist "applied" status so re-opens of the dialog don't show this
	// book as still needing review. Mirrors the reject handler.
	cr.Status = "applied"
	if updatedJSON, err := json.Marshal(cr); err == nil {
		errhandling.MustLog(s.Ops().CreateOperationResult(&database.OperationResult{
			OperationID: opID,
			BookID:      bookID,
			ResultJSON:  string(updatedJSON),
			Status:      "applied",
		}), "applied status not saved; the review dialog may list this book again", "op_id", opID, "book_id", bookID)
	}

	s.submitOpResultFileWork(ctx, bookID, pendingCover)
	return opResultApplyOutcome{applied: true}
}

// submitOpResultFileWork queues one batch-apply-candidates book's file work on
// the file-I/O pool (bounded concurrency); Submit marks the book pending for
// the scanner until the job has run. No pool, no file work.
//
// Every caller is a batch-apply-candidates request (applied now, or queued as
// metadata.apply-when-scanned behind a scan), so the job runs under
// tagger.WithoutBackup: no .bak-* sibling per file (owner decision D69).
func (s *Server) submitOpResultFileWork(ctx context.Context, bookID, pendingCover string) {
	pool := s.fileIOPool
	if pool == nil {
		return
	}
	mfs := s.metadataFetchService
	fileCtx := tagger.WithoutBackup(ctx)
	submitted := pool.Submit(bookID, func() {
		// Logged, not returned: this runs in the pool after the caller has
		// already answered. The shared sequel: cover download, file I/O, and
		// the tags exactly once.
		if err := finishApplyFileWork(mfs, fileCtx, bookID, pendingCover, true, true, nil); err != nil {
			batchApplyCandidatesLog.Warn("background apply file work failed for book %s: %s",
				logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(err.Error()))
		}
	})
	if !submitted {
		// The pool is stopping (shutdown): the database has the applied
		// metadata and the files do not. Say so; the next write-back or
		// apply of the book writes them.
		batchApplyCandidatesLog.Warn("file work for book %s dropped: the file-I/O pool is stopped; its tags and names were not updated",
			logger.SanitizeLogValue(bookID))
	}
}
