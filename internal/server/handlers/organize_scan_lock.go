// file: internal/server/handlers/organize_scan_lock.go
// version: 1.2.0
// guid: 2b7e4c91-5d08-4f3a-b6e2-9a14c7d3f058
// last-edited: 2026-09-30

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
	"github.com/gin-gonic/gin"
)

// Single-book organize and a running library scan coordinate per BOOK
// (internal/scanlock), exactly like the metadata applies: the organize takes
// the book's scan lock, waiting at most organizeBookLockWait for the scanner
// to finish reading that book; any other book never waits. If the scanner
// holds the book past the bound, the organize is handed to the durable
// library.organize-when-scanned op (kind "organize") and the request answers
// 202 -- never 409, never a scan warning.

var organizeLockLog = logger.New("organize.scanlock")

// organizeBookLockWait bounds a request's wait for a book the scanner holds.
// A var so tests can shorten it.
var organizeBookLockWait = 60 * time.Second

// queuedOrganizeBeat is how often a queued organize waiting for its book
// reports progress, well inside the op's progress watchdog.
var queuedOrganizeBeat = 30 * time.Second

// organizeQueuedMessage is what the UI shows for a 202: information.
const organizeQueuedMessage = "The library scan is reading this book right now; the organize is queued and will run as soon as it moves on."

// OrganizeQueuer hands a single-book organize to the queued op.
type OrganizeQueuer interface {
	EnqueueOrganizeWhenScanned(ctx context.Context, bookID string) (opID string, err error)
}

// SetOrganizeQueuer wires the handoff. Nil makes a request that cannot get
// its book in time keep waiting on the request's own context instead.
func (h *OrganizeHandler) SetOrganizeQueuer(q OrganizeQueuer) { h.queuer = q }

// organizeLockTries bounds re-resolution when the library copy the organize
// acts on changes while it waits for the lock.
const organizeLockTries = 3

// organizeLockSet is the rows an organize of id must hold: id, and the library
// copy it resolves to (organizeBookCore acts on that copy's files), sorted. A
// scanner reaching the copy normally locks {copy, id} through their version
// group, but not when the group is over the scanner's cap -- so the organize
// holds both itself. subject is the row the organize acts on (the copy, or
// the book itself). A book that cannot be read is just {id} with a nil
// subject; the core reports the read error.
func (h *OrganizeHandler) organizeLockSet(id string) (ids []string, subject *database.Book) {
	ids = []string{id}
	if h.store == nil || h.resolveLibraryCopy == nil {
		return ids, nil
	}
	requested, err := h.store.GetBookByID(id)
	if err != nil || requested == nil {
		return ids, nil
	}
	subject = organizer.ResolveOrganizeSubject(h.resolveLibraryCopy, requested)
	if subject != nil && subject.ID != id {
		ids = append(ids, subject.ID)
	}
	slices.Sort(ids)
	return ids, subject
}

// lockOrganizeSet takes organizeLockSet(id) with lock (a bounded or unbounded
// LockSet), re-resolving under the lock: when the copy changed while it waited
// (created, repointed, removed) it releases and retries, at most
// organizeLockTries times, then keeps the last set it holds -- a copy that
// keeps changing is left to organizeBookCore, which resolves it again.
//
// subject is the row resolved UNDER the lock, which organizeBookCore acts on
// without resolving a third time, so the organize writes exactly the copy it
// holds. It is nil when the book could not be read or the set never settled;
// the core then resolves (and reports read errors) itself.
func (h *OrganizeHandler) lockOrganizeSet(id string, lock func([]string) (*scanlock.Hold, error)) (*scanlock.Hold, *database.Book, error) {
	for try := 0; ; try++ {
		want, _ := h.organizeLockSet(id)
		hold, err := lock(want)
		if err != nil {
			return nil, nil, err
		}
		again, subject := h.organizeLockSet(id)
		if slices.Equal(again, want) {
			return hold, subject, nil
		}
		if try+1 >= organizeLockTries {
			return hold, nil, nil
		}
		hold.Release()
	}
}

// lockBookForOrganize takes book id's scan lock for a request, waiting at
// most organizeBookLockWait. Past the bound it queues the organize and writes
// 202 (ok=false), or, with no queuer wired, keeps waiting on the request's
// context. ok=false with nothing written means the client went away.
//
// subject is lockOrganizeSet's: the row to organize, resolved under the lock.
func (h *OrganizeHandler) lockBookForOrganize(c *gin.Context, id string) (_ *scanlock.Hold, subject *database.Book, ok bool) {
	reqCtx := c.Request.Context()
	wctx, cancel := context.WithTimeout(reqCtx, organizeBookLockWait)
	hold, subject, err := h.lockOrganizeSet(id, func(ids []string) (*scanlock.Hold, error) { return scanlock.Books.LockSet(wctx, ids) })
	cancel()
	if err == nil {
		return hold, subject, true
	}
	if reqCtx.Err() != nil {
		return nil, nil, false
	}
	if h.queuer == nil {
		hold, subject, err = h.lockOrganizeSet(id, func(ids []string) (*scanlock.Hold, error) { return scanlock.Books.LockSet(reqCtx, ids) })
		return hold, subject, err == nil
	}
	opID, qerr := h.queuer.EnqueueOrganizeWhenScanned(reqCtx, id)
	if qerr != nil {
		httputil.InternalError(c, "queue the organize until the scan moves on", qerr)
		return nil, nil, false
	}
	organizeLockLog.Info("organize of book %s queued behind the library scan as operation %s",
		logger.SanitizeLogValue(id), opID)
	httputil.RespondWithSuccess(c, http.StatusAccepted, gin.H{
		"queued": true, "operation_id": opID, "message": organizeQueuedMessage, "book_id": id,
	})
	return nil, nil, false
}

// RunQueuedOrganize is the queued organize: wait for the book as long as the
// op lives, beating so the progress watchdog sees a live wait, then organize
// exactly as the request would have. A refusal or failure the request would
// have answered with a 4xx/5xx fails the op with the same message.
func (h *OrganizeHandler) RunQueuedOrganize(ctx context.Context, id string, beat func(msg string)) error {
	var hold *scanlock.Hold
	var subject *database.Book
	for {
		wctx, cancel := context.WithTimeout(ctx, queuedOrganizeBeat)
		var err error
		hold, subject, err = h.lockOrganizeSet(id, func(ids []string) (*scanlock.Hold, error) { return scanlock.Books.LockSet(wctx, ids) })
		cancel()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if beat != nil {
			beat("waiting for the library scan to finish reading book " + id)
		}
	}
	defer hold.Release()
	r := &recordingOrganizeResponder{}
	h.organizeBookCore(ctx, r, id, subject)
	return r.failure()
}

// organizeResponder receives organizeBookCore's outcome.
type organizeResponder interface {
	ok(body any)
	json(status int, body any)
	notFound(kind, id string)
	internal(msg string, err error)
}

// ginOrganizeResponder writes the outcome as the HTTP response.
type ginOrganizeResponder struct{ c *gin.Context }

func (g ginOrganizeResponder) ok(body any)               { httputil.RespondWithOK(g.c, body) }
func (g ginOrganizeResponder) json(status int, body any) { g.c.JSON(status, body) }
func (g ginOrganizeResponder) notFound(kind, id string)  { httputil.RespondWithNotFound(g.c, kind, id) }
func (g ginOrganizeResponder) internal(msg string, err error) {
	httputil.InternalError(g.c, msg, err)
}

// recordingOrganizeResponder keeps the outcome for the queued op.
type recordingOrganizeResponder struct {
	mu     sync.Mutex
	status int
	msg    string
}

func (r *recordingOrganizeResponder) set(status int, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status, r.msg = status, msg
	}
}
func (r *recordingOrganizeResponder) ok(any) { r.set(http.StatusOK, "") }
func (r *recordingOrganizeResponder) json(status int, body any) {
	msg := fmt.Sprint(body)
	if h, isH := body.(gin.H); isH {
		if e, has := h["error"]; has {
			msg = fmt.Sprint(e)
		}
	}
	r.set(status, msg)
}
func (r *recordingOrganizeResponder) notFound(kind, id string) {
	r.set(http.StatusNotFound, kind+" "+id+" not found")
}
func (r *recordingOrganizeResponder) internal(msg string, err error) {
	r.set(http.StatusInternalServerError, fmt.Sprintf("%s: %v", msg, err))
}

func (r *recordingOrganizeResponder) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status >= 400 {
		return fmt.Errorf("organize failed (%d): %s", r.status, r.msg)
	}
	return nil
}
