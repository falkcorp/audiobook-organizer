// file: internal/server/handlers/metadata/book_scan_lock.go
// version: 1.0.0
// guid: 070620af-532e-4357-a2a3-3f746b5e9e30
// last-edited: 2026-09-30

package metadatahandler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	c.JSON(http.StatusAccepted, resp)
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
func (h *Handler) applyCandidateCore(ctx context.Context, id string, cand metafetch.MetadataCandidate, fields []string, writeBack *bool) (*metafetch.FetchMetadataResponse, error) {
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
	resp, err := h.metadataFetchService.ApplyMetadataCandidate(id, cand, fields)
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
		pool.Submit(id, func() {
			// Cover download FIRST, then the file I/O, then the tags exactly
			// once (FinishApplyFileWork). It takes metafetch's own book and
			// path locks; this job holds none of that table. The response is
			// already written, so a failure can only be logged.
			if err := mfs.FinishApplyFileWork(id, pendingCover, true, shouldWriteBack, nil); err != nil {
				bookLockLog.Warn("background apply file work failed for book %s: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
			}
		})
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

// RunQueuedApply is the body of the metadata.apply-when-scanned op: wait for
// the book as long as the op lives (ctx), then run the same core the request
// would have run. The wait reports through beat so the op's progress watchdog
// sees it is alive.
func (h *Handler) RunQueuedApply(ctx context.Context, q QueuedApply, beat func(msg string)) error {
	var hold *scanlock.Hold
	for {
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var err error
		hold, err = scanlock.Books.LockSet(wctx, []string{q.BookID})
		cancel()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if beat != nil {
			beat("waiting for the library scan to finish reading book " + q.BookID)
		}
	}
	defer hold.Release()
	switch q.Kind {
	case QueuedApplyCandidate:
		if q.Candidate == nil {
			return fmt.Errorf("queued apply for %s has no candidate", q.BookID)
		}
		_, err := h.applyCandidateCore(ctx, q.BookID, *q.Candidate, q.Fields, q.WriteBack)
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
