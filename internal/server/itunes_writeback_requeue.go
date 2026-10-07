// file: internal/server/itunes_writeback_requeue.go
// version: 1.1.0
// guid: 1a7f4c2e-8d36-4b9a-b5e0-3c9d2f6a8e14
// last-edited: 2026-10-07
//
// Requeue endpoints for the iTunes write-back batcher. Before #3821/#3822 the
// batcher dropped 4,293 book updates and one remove, and the drop log had no
// ids. These endpoints put them back through the fixed batcher, so they take
// its normal path, including its dry-run mode. See
// docs/plans/2026-10-07-itunes-writeback-requeue.md.
//
// Both default to dry_run=true. They queue only when the body sends
// "dry_run": false. Neither one ever queues an add, and /requeue never
// queues a remove.

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
	"github.com/gin-gonic/gin"
)

// requeueParseITL parses the write target. A var so tests can hand the
// handlers a library built in Go instead of a binary fixture.
var requeueParseITL = itunes.ParseITL

// maxRequeueBodyBytes caps the request body (a full book_ids list for a few
// thousand books fits comfortably).
const maxRequeueBodyBytes = 4 << 20

// requeueKindsNote is returned with every requeue response.
const requeueKindsNote = "kinds selects books only: once a book is queued, the flush writes every metadata and location difference it finds for it"

// writebackRequeueRequest is the body of POST /itunes/writeback/requeue.
type writebackRequeueRequest struct {
	// DryRun defaults to true: only an explicit false queues anything.
	DryRun  *bool    `json:"dry_run"`
	BookIDs []string `json:"book_ids"`
	// Kinds defaults to both "metadata" and "location".
	Kinds []string `json:"kinds"`
	// Limit, when > 0, queues at most this many of the selected books (the
	// first by book id), so a large delta can be fed to the batcher in
	// chunks. Run again after a chunk is written to queue the next.
	Limit int `json:"limit"`
}

// writebackRequeueRemoveRequest is the body of POST
// /itunes/writeback/requeue-remove.
type writebackRequeueRemoveRequest struct {
	DryRun  *bool    `json:"dry_run"`
	BookIDs []string `json:"book_ids"`
}

// writebackTargetState is the batcher configuration a requeue response reports,
// so the caller can see whether queued items will be written.
type writebackTargetState struct {
	LibraryWritePath    string `json:"library_write_path"`
	AutoWriteBack       bool   `json:"auto_write_back"`
	ITLWriteBackEnabled bool   `json:"itl_write_back_enabled"`
	WriteBackDryRun     bool   `json:"write_back_dry_run"`
}

// decodeRequeueBody decodes an optional JSON body into dst. An empty body
// leaves dst at its zero value (all defaults). Unknown fields are rejected, so
// a misspelled "dry_run" is an error rather than a silent default.
func decodeRequeueBody(c *gin.Context, dst any) error {
	if c.Request.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequeueBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(raw) > maxRequeueBodyBytes {
		return errors.New("request body too large")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// requeueLibrary resolves and parses the batcher's write target. It writes the
// error response itself and returns ok=false on failure.
func (s *Server) requeueLibrary(c *gin.Context) (*itunes.ITLLibrary, writebackTargetState, bool) {
	var st writebackTargetState
	if s.writeBackBatcher == nil {
		httputil.RespondWithServiceUnavailable(c, "iTunes write-back batcher not configured")
		return nil, st, false
	}
	path, itlEnabled, auto, dry := s.writeBackBatcher.WriteTarget()
	st = writebackTargetState{LibraryWritePath: path, AutoWriteBack: auto, ITLWriteBackEnabled: itlEnabled, WriteBackDryRun: dry}
	if path == "" {
		httputil.RespondWithBadRequest(c, "iTunes LibraryWritePath not configured")
		return nil, st, false
	}
	clean, err := pathvalidation.CleanAbsolutePath(path)
	if err != nil {
		httputil.RespondWithInternalError(c, "invalid iTunes LibraryWritePath in config")
		return nil, st, false
	}
	lib, err := requeueParseITL(clean)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("parse iTunes library: %v", err))
		return nil, st, false
	}
	return lib, st, true
}

// respondEnqueueError maps a checked-enqueue error to a response.
func respondEnqueueError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, itunesservice.ErrAutoWriteBackDisabled):
		httputil.RespondWithConflict(c, "iTunes auto write-back is disabled; nothing was queued")
	case errors.Is(err, itunesservice.ErrBatcherStopped):
		httputil.RespondWithServiceUnavailable(c, "iTunes write-back batcher is stopped; nothing was queued")
	default:
		httputil.RespondWithInternalError(c, fmt.Sprintf("enqueue failed: %v", err))
	}
}

// itunesWritebackRequeueHandler handles POST /api/v1/itunes/writeback/requeue.
//
// It plans every book (or the book_ids subset) with the flush's own planner
// and selects the books where at least one track in the library differs.
// dry_run (the default) returns counts and a sample of up to 50 books.
// dry_run:false also puts the selected book ids (at most limit of them, when
// limit > 0) on the queue. Library tracks
// that no book claims ("removes") and DB PIDs missing from the library
// ("adds") are only counted.
func (s *Server) itunesWritebackRequeueHandler(c *gin.Context) {
	var req writebackRequeueRequest
	if err := decodeRequeueBody(c, &req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	dryRun := req.DryRun == nil || *req.DryRun

	opts := itunesservice.RequeueOptions{BookIDs: req.BookIDs}
	kinds := req.Kinds
	if len(kinds) == 0 {
		kinds = []string{itunesservice.RequeueKindMetadata, itunesservice.RequeueKindLocation}
	}
	for _, k := range kinds {
		switch k {
		case itunesservice.RequeueKindMetadata:
			opts.Metadata = true
		case itunesservice.RequeueKindLocation:
			opts.Location = true
		default:
			httputil.RespondWithBadRequest(c, fmt.Sprintf("unknown kind %q (want %q or %q)", k, itunesservice.RequeueKindMetadata, itunesservice.RequeueKindLocation))
			return
		}
	}

	if req.Limit < 0 {
		httputil.RespondWithBadRequest(c, "limit must be >= 0")
		return
	}

	lib, target, ok := s.requeueLibrary(c)
	if !ok {
		return
	}
	plan, err := itunesservice.PlanRequeue(c.Request.Context(), s.storeForWiring(), lib, opts)
	if err != nil {
		httputil.RespondWithInternalError(c, fmt.Sprintf("requeue plan failed: %v", err))
		return
	}

	toQueue := plan.SelectedBookIDs
	if req.Limit > 0 && len(toQueue) > req.Limit {
		toQueue = toQueue[:req.Limit]
	}
	resp := gin.H{
		"dry_run":           dryRun,
		"kinds":             kinds,
		"kinds_note":        requeueKindsNote,
		"target":            target,
		"plan":              plan,
		"limit":             req.Limit,
		"selected_not_sent": len(plan.SelectedBookIDs) - len(toQueue),
	}
	if dryRun {
		resp["would_enqueue"] = len(toQueue)
		httputil.RespondWithOK(c, resp)
		return
	}

	queued, err := s.writeBackBatcher.EnqueueBooks(toQueue)
	if err != nil {
		respondEnqueueError(c, err)
		return
	}
	resp["enqueued"] = queued
	resp["already_pending"] = len(toQueue) - queued
	resp["status"] = s.writeBackBatcher.Status()
	httputil.RespondWithOK(c, resp)
}

// itunesWritebackRequeueRemoveHandler handles
// POST /api/v1/itunes/writeback/requeue-remove.
//
// It takes at most itunesservice.MaxRemoveRequeueBooks explicit book_ids of
// merged-away (or deleted) loser books and re-queues the iTunes remove of any
// PID the pre-v6 batcher tombstoned but never removed. See
// WriteBackBatcher.PlanRemoveRequeue for the eligibility rules. dry_run (the
// default) reports eligibility. dry_run:false queues the eligible PIDs.
func (s *Server) itunesWritebackRequeueRemoveHandler(c *gin.Context) {
	var req writebackRequeueRemoveRequest
	if err := decodeRequeueBody(c, &req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	dryRun := req.DryRun == nil || *req.DryRun

	lib, target, ok := s.requeueLibrary(c)
	if !ok {
		return
	}
	plan, err := s.writeBackBatcher.PlanRemoveRequeue(s.storeForWiring(), lib, req.BookIDs)
	if err != nil {
		if errors.Is(err, itunesservice.ErrRemoveRequeueRequest) {
			httputil.RespondWithBadRequest(c, err.Error())
			return
		}
		httputil.RespondWithInternalError(c, fmt.Sprintf("remove requeue plan failed: %v", err))
		return
	}

	resp := gin.H{"dry_run": dryRun, "target": target, "plan": plan}
	if dryRun {
		resp["would_enqueue"] = len(plan.EligiblePIDs)
		httputil.RespondWithOK(c, resp)
		return
	}

	type failure struct {
		PID   string `json:"pid"`
		Error string `json:"error"`
	}
	queued := []string{}
	failed := []failure{}
	for _, pid := range plan.EligiblePIDs {
		if err := s.writeBackBatcher.EnqueueRemoveChecked(pid); err != nil {
			if errors.Is(err, itunesservice.ErrAutoWriteBackDisabled) || errors.Is(err, itunesservice.ErrBatcherStopped) {
				if len(queued) == 0 {
					respondEnqueueError(c, err)
					return
				}
			}
			failed = append(failed, failure{PID: pid, Error: err.Error()})
			continue
		}
		queued = append(queued, pid)
	}
	resp["enqueued"] = queued
	resp["failed"] = failed
	resp["status"] = s.writeBackBatcher.Status()
	status := http.StatusOK
	if len(failed) > 0 {
		status = http.StatusMultiStatus
	}
	httputil.RespondWithSuccess(c, status, resp)
}
