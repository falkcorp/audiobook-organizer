// file: internal/server/handlers/metadata/book_scan_lock.go
// version: 1.5.0
// guid: 070620af-532e-4357-a2a3-3f746b5e9e30
// last-edited: 2026-09-30

package metadatahandler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/plugin"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
	"github.com/gin-gonic/gin"
)

// Metadata applies and a running library scan coordinate PER BOOK
// (internal/scanlock), not library-wide. Until 2026-09-30 these handlers
// refused with 409 SCAN_RUNNING whenever any scan was running. Now:
//
//   - an apply takes the scan lock of the ONE book it writes, waiting at most
//     requestBookLockWait for the scanner to finish reading that book;
//   - an apply of any other book never waits;
//   - it submits its file work (tag write, rename) while still holding the
//     lock, and the file-I/O pool marks the book pending until that work has
//     run, so the scanner cannot read the old tags in between;
//   - if the scanner holds the book past the bound, the apply is handed to the
//     durable metadata.apply-when-scanned op and the request answers 202.
//
// A handler that only writes the database and schedules no file work does not
// need the lock: the scanner's merge keeps every field another writer changed
// after the scanner read the book (scanner.mergeScannedKeepingForeignEdits).

var bookLockLog = logger.New("metadata.scanlock")

// requestBookLockWait bounds a request's wait for a book the scanner holds. A
// var so tests can shorten it.
var requestBookLockWait = 60 * time.Second

// Kinds of queued apply.
const (
	QueuedApplyCandidate = "apply-candidate"
	QueuedWriteBack      = "write-back"
	QueuedFetch          = "fetch"
	// QueuedOpResultCandidate is one book of POST
	// /metadata/batch-apply-candidates: the candidate stored for BookID in
	// candidate-fetch operation OperationID, applied fill-only through the
	// certainty gate. Run by package server, which owns that path.
	QueuedOpResultCandidate = "op-result-candidate"
	// QueuedOrganize is POST /audiobooks/:id/organize for BookID. Run by
	// package server through the organize handler.
	QueuedOrganize = "organize"
)

// QueuedApply is one apply handed to the metadata.apply-when-scanned op: the
// book the scanner was holding and everything needed to run it again later.
type QueuedApply struct {
	Kind       string                       `json:"kind"`
	BookID     string                       `json:"book_id"`
	Candidate  *metafetch.MetadataCandidate `json:"candidate,omitempty"`
	Fields     []string                     `json:"fields,omitempty"`
	WriteBack  *bool                        `json:"write_back,omitempty"`
	SegmentIDs []string                     `json:"segment_ids,omitempty"`
	Rename     *bool                        `json:"rename,omitempty"`
	// OperationID is the candidate-fetch operation (QueuedOpResultCandidate).
	OperationID string `json:"operation_id,omitempty"`
	// EditMark and ApplyBatchID (QueuedApplyCandidate) are fixed when the
	// apply is queued. EditMark is the book's newest field-edit history row
	// then (metafetch.ApplyEditMark); ApplyBatchID is the batch id the apply's
	// own history rows will carry. See checkQueuedCandidate.
	EditMark     int64  `json:"edit_mark,omitempty"`
	ApplyBatchID string `json:"apply_batch_id,omitempty"`
}

// ErrQueuedAlreadyApplied is returned by RunQueuedApply when the queued
// candidate apply's own history rows are already there: the op is re-running
// after a restart that interrupted it after the apply landed. Nothing is
// written again (no second history row); the op completes as "already
// applied".
var ErrQueuedAlreadyApplied = errors.New("already applied: this queued change landed before the server restarted")

// ErrQueuedApplyStale is returned by RunQueuedApply when someone edited the
// book (another apply, a manual edit, an undo, a bulk update, a repair) after
// this apply was queued. Nothing is written; the op fails with the edits named
// so the user can re-apply against the book as it is now. The library scan's
// own merge is not an edit here: it records no change history.
var ErrQueuedApplyStale = errors.New("this book was edited after the change was queued; nothing was applied")

// newQueuedApplyBatchID is the history batch id a queued apply's rows carry.
func newQueuedApplyBatchID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("apply-queued-%d", time.Now().UnixNano())
	}
	return "apply-queued-" + hex.EncodeToString(b[:])
}

// stampQueuedCandidate fixes the edit mark and the batch id when a candidate
// apply is queued. A history that cannot be read falls back to "now": any
// edit from here on still counts, only one racing the enqueue itself could be
// missed, and that is logged.
func (h *Handler) stampQueuedCandidate(q *QueuedApply) {
	q.ApplyBatchID = newQueuedApplyBatchID()
	mark, err := h.metadataFetchService.ApplyEditMark(q.BookID)
	if err != nil {
		mark = time.Now().UnixNano()
		bookLockLog.Warn("queued apply for book %s: change history unreadable (%v); treating edits from now on as later edits",
			logger.SanitizeLogValue(q.BookID), err)
	}
	q.EditMark = mark
}

// checkQueuedCandidate decides, under the book's scan lock, whether the
// queued candidate apply may run. It reads the book's change history newer
// than EditMark:
//   - a row with the apply's own batch id: it already landed
//     (ErrQueuedAlreadyApplied);
//   - any other field-edit row: someone edited the book after the apply was
//     queued, and the apply would overwrite them (ErrQueuedApplyStale);
//   - none: run.
//
// The library scan's merge -- which the op was queued behind, and which
// almost always rewrites the row from the file's tags -- writes no history,
// so it never refuses the apply. An unreadable history refuses (fail closed):
// the op fails and says so rather than risk overwriting an edit.
func (h *Handler) checkQueuedCandidate(q QueuedApply) error {
	if q.ApplyBatchID == "" {
		// Queued without a stamp (no fetch service at enqueue): nothing to
		// compare against; run as the request would have.
		return nil
	}
	edits, err := h.metadataFetchService.ApplyEditsSince(q.BookID, q.EditMark, q.ApplyBatchID)
	if err != nil {
		return fmt.Errorf("%w: could not read the book's change history to check for later edits: %v", ErrQueuedApplyStale, err)
	}
	if edits.OwnApplied {
		return ErrQueuedAlreadyApplied
	}
	if len(edits.Others) > 0 {
		return fmt.Errorf("%w (edited since: %s)", ErrQueuedApplyStale, strings.Join(edits.Others, "; "))
	}
	return nil
}

// QueuedApplyEnqueuer enqueues a QueuedApply as a durable operation.
type QueuedApplyEnqueuer interface {
	EnqueueApplyWhenScanned(ctx context.Context, q QueuedApply) (opID string, err error)
}

// SetQueuedApplyEnqueuer wires the handoff. Nil makes a request that cannot
// get its book in time keep waiting on the request's own context instead.
func (h *Handler) SetQueuedApplyEnqueuer(q QueuedApplyEnqueuer) { h.queuedApply = q }

// queuedMessage is what the UI shows for a 202: information, not a warning.
const queuedMessage = "The library scan is reading this book right now; your change is queued and will be applied as soon as it moves on."

// lockBookForRequest takes book id's scan lock for a request, waiting at most
// requestBookLockWait. When the bound passes it hands q to the queued op and
// writes 202 (ok=false), or, with no enqueuer wired, keeps waiting on the
// request's context. ok=false with nothing written means the client went away.
func (h *Handler) lockBookForRequest(c *gin.Context, q QueuedApply) (*scanlock.Hold, bool) {
	reqCtx := c.Request.Context()
	wctx, cancel := context.WithTimeout(reqCtx, requestBookLockWait)
	hold, err := scanlock.Books.LockSet(wctx, []string{q.BookID})
	cancel()
	if err == nil {
		return hold, true
	}
	if reqCtx.Err() != nil {
		return nil, false
	}
	if h.queuedApply == nil {
		hold, err = scanlock.Books.LockSet(reqCtx, []string{q.BookID})
		return hold, err == nil
	}
	if q.Kind == QueuedApplyCandidate && h.metadataFetchService != nil {
		h.stampQueuedCandidate(&q)
	}
	opID, qerr := h.queuedApply.EnqueueApplyWhenScanned(reqCtx, q)
	if qerr != nil {
		httputil.InternalError(c, "queue the change until the scan moves on", qerr)
		return nil, false
	}
	bookLockLog.Info("metadata %s for book %s queued behind the library scan as operation %s",
		q.Kind, logger.SanitizeLogValue(q.BookID), opID)
	resp := gin.H{"queued": true, "operation_id": opID, "message": queuedMessage}
	if h.store != nil {
		if b, gerr := h.store.GetBookByID(q.BookID); gerr == nil && b != nil && h.enrichBook != nil {
			resp["book"] = h.enrichBook(b)
		}
	}
	// Enveloped like every success, so the client reads it from body.data.
	httputil.RespondWithSuccess(c, http.StatusAccepted, resp)
	return nil, false
}

// errRenameWouldFail wraps a preflight refusal so the handler can answer 409
// with the reason the UI already understands.
type errRenameWouldFail struct{ err error }

func (e *errRenameWouldFail) Error() string { return e.err.Error() }
func (e *errRenameWouldFail) Unwrap() error { return e.err }

// applyCandidateCore is the single-book candidate apply, shared by the handler
// and the queued op. The caller holds the book's scan lock; the file work is
// submitted before it returns, so the pool's pending mark is in place before
// the lock is released.
//
// batchID fixes the apply's history batch id (the queued apply's
// ApplyBatchID); "" is the request path, which draws a fresh one.
func (h *Handler) applyCandidateCore(ctx context.Context, id string, cand metafetch.MetadataCandidate, fields []string, writeBack *bool, batchID string) (*metafetch.FetchMetadataResponse, error) {
	// The apply writes the database first; the rename runs afterwards in the
	// background file-IO job. When that rename is known to fail, refuse here,
	// before any write, so the book cannot end up with new metadata and its
	// old file names (three books did on 2026-09-13). Gated on the pool: the
	// job always runs the file I/O and write_back only decides the tag write.
	if h.fileIOPool != nil {
		if perr := h.metadataFetchService.RenamePreflight(id, cand, fields); perr != nil {
			return nil, &errRenameWouldFail{perr}
		}
	}
	var resp *metafetch.FetchMetadataResponse
	var err error
	if batchID == "" {
		resp, err = h.metadataFetchService.ApplyMetadataCandidate(id, cand, fields)
	} else {
		resp, err = h.metadataFetchService.ApplyMetadataCandidateWithOptions(id, cand, fields, metafetch.ApplyOptions{BatchID: batchID})
	}
	if err != nil {
		return nil, err
	}
	// Applying invalidates the candidate cache (METADATA-CACHED-MATCHER).
	_ = h.metadataFetchService.InvalidateCachedCandidates(id)

	shouldWriteBack := writeBack == nil || *writeBack
	// Enqueue before the pool submission so the iTunes batcher picks up the
	// change even if the file job panics on a malformed file.
	if wb := h.resolveWriteBack(); shouldWriteBack && wb != nil {
		wb.Enqueue(id)
	}
	if pool := h.fileIOPool; pool != nil {
		mfs := h.metadataFetchService
		pendingCover := resp.PendingCoverURL
		accepted := pool.Submit(id, func() {
			// Cover download FIRST, then the file I/O, then the tags exactly
			// once (FinishApplyFileWork). It takes metafetch's own book and
			// path locks; this job holds none of that table. The response is
			// already written, so a failure can only be logged.
			if err := mfs.FinishApplyFileWork(id, pendingCover, true, shouldWriteBack, nil); err != nil {
				bookLockLog.Warn("background apply file work failed for book %s: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
			}
		})
		if !accepted {
			// The pool is stopping (shutdown): the job was dropped without
			// running. The pool clears its own pending mark on a drop and this
			// path retains no scan hold, so nothing is left held -- but the
			// database now has the new metadata and the files do not. Say so.
			bookLockLog.Error("apply file work for book %s was dropped: the file I/O pool is stopped; the metadata is saved but the tags, cover and rename did not run (write back or re-apply to finish)",
				logger.SanitizeLogValue(id))
		}
	}
	if h.publishEvent != nil {
		h.publishEvent(ctx, plugin.NewEvent(plugin.EventMetadataApplied, id, map[string]any{
			"source":  resp.Source,
			"message": resp.Message,
		}))
	}
	return resp, nil
}

// writeBackResult is writeBackCore's outcome.
type writeBackResult struct {
	written int
	renamed bool
}

// errWriteBackConflict carries an HTTP status and code for a refusal.
type errWriteBackConflict struct {
	status int
	code   string
	msg    string
}

func (e *errWriteBackConflict) Error() string { return e.msg }

// writeBackCore writes the database's metadata into the book's files and,
// when asked or configured, renames them. Shared by the handler and the
// queued op; the caller holds the book's scan lock for the whole call.
func (h *Handler) writeBackCore(ctx context.Context, id string, segmentIDs []string, doRename bool) (writeBackResult, error) {
	var res writeBackResult
	book, err := h.store.GetBookByID(id)
	if err != nil || book == nil {
		return res, &errWriteBackConflict{status: http.StatusNotFound, code: "NOT_FOUND", msg: "audiobook not found"}
	}
	if doRename && len(segmentIDs) == 0 {
		// Refuse before anything moves when the rename is known to fail.
		if err := h.metadataFetchService.RenameOnlyPreflight(id); err != nil {
			if errors.Is(err, metafetch.ErrApplyFileWorkWouldFail) {
				return res, &errWriteBackConflict{status: http.StatusConflict, code: metafetch.ApplyRefusedReasonFileWorkWouldFail, msg: err.Error()}
			}
			return res, fmt.Errorf("rename preflight failed: %w", err)
		}
		if err := h.metadataFetchService.RunApplyPipelineRenameOnly(ctx, id, book); err != nil {
			// Some files may have moved; writing tags now would stamp a
			// half-moved book.
			status := http.StatusInternalServerError
			if errors.Is(err, organizer.ErrDuplicateRenameTarget) || errors.Is(err, metafetch.ErrApplyFileWorkWouldFail) {
				status = http.StatusConflict
			}
			return res, &errWriteBackConflict{status: status, code: "rename_failed", msg: "rename failed; no tags were written: " + err.Error()}
		}
		res.renamed = true
	}
	if len(segmentIDs) > 0 {
		res.written, err = h.metadataFetchService.WriteBackMetadataForBook(id, segmentIDs)
	} else {
		res.written, err = h.metadataFetchService.WriteBackMetadataForBook(id)
	}
	if err != nil {
		return res, fmt.Errorf("failed to write back metadata: %w", err)
	}
	return res, nil
}

// fetchCore fetches and applies provider metadata for one book. Its file work
// goes through the pool (metafetch's file-work scheduler), which marks the
// book pending while the caller still holds the lock.
func (h *Handler) fetchCore(ctx context.Context, id string) (*metafetch.FetchMetadataResponse, error) {
	resp, err := h.metadataFetchService.FetchMetadataForBook(ctx, id)
	if err != nil {
		return nil, err
	}
	_ = h.metadataFetchService.InvalidateCachedCandidates(id)
	if wb := h.resolveWriteBack(); wb != nil {
		wb.Enqueue(id)
	}
	return resp, nil
}

// queuedWaitBeat is how often a queued apply waiting for its book reports
// progress, well inside the op's progress watchdog.
var queuedWaitBeat = 30 * time.Second

// WaitForBook takes book id's scan lock for a queued apply, waiting as long as
// ctx lives and calling beat each queuedWaitBeat so the op's progress watchdog
// sees a live wait rather than a wedged op.
func WaitForBook(ctx context.Context, id string, beat func(msg string)) (*scanlock.Hold, error) {
	for {
		wctx, cancel := context.WithTimeout(ctx, queuedWaitBeat)
		hold, err := scanlock.Books.LockSet(wctx, []string{id})
		cancel()
		if err == nil {
			return hold, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if beat != nil {
			beat("waiting for the library scan to finish reading book " + id)
		}
	}
}

// RunQueuedApply is the body of the metadata.apply-when-scanned op: wait for
// the book as long as the op lives (ctx), then run the same core the request
// would have run. The wait reports through beat so the op's progress watchdog
// sees it is alive.
func (h *Handler) RunQueuedApply(ctx context.Context, q QueuedApply, beat func(msg string)) error {
	hold, err := WaitForBook(ctx, q.BookID, beat)
	if err != nil {
		return err
	}
	defer hold.Release()
	switch q.Kind {
	case QueuedApplyCandidate:
		if q.Candidate == nil {
			return fmt.Errorf("queued apply for %s has no candidate", q.BookID)
		}
		if err := h.checkQueuedCandidate(q); err != nil {
			bookLockLog.Info("queued apply for book %s not run: %s", logger.SanitizeLogValue(q.BookID), logger.SanitizeLogValue(err.Error()))
			return err
		}
		_, err := h.applyCandidateCore(ctx, q.BookID, *q.Candidate, q.Fields, q.WriteBack, q.ApplyBatchID)
		return err
	case QueuedWriteBack:
		_, err := h.writeBackCore(ctx, q.BookID, q.SegmentIDs, q.Rename != nil && *q.Rename)
		return err
	case QueuedFetch:
		_, err := h.fetchCore(ctx, q.BookID)
		return err
	default:
		return fmt.Errorf("unknown queued apply kind %q", q.Kind)
	}
}
