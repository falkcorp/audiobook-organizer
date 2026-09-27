// file: internal/server/handlers/repairs/handler.go
// version: 1.0.0
// guid: 1d8e4c73-5a26-4b9f-8e03-7c2b9f6a1d58
// last-edited: 2026-09-27

// Package repairs serves the Repairs lane of /review (/api/v1/repairs/*):
// list the fixers, start a plan, page a stored plan's rows, and start an
// apply of chosen rows. Plans and applies run as operations (repairs.plan /
// repairs.apply); these handlers only enqueue them and read their stored
// results. Everything that makes an apply safe (guards, fingerprint check,
// history, scan stand-down) lives in internal/repairs and the ops, not here.
//
// Status codes: 202 with the operation id for plan/apply; 404 for an unknown
// fixer or plan op; 409 for a plan op that has not completed; 400 for a plan
// op that is not a plan of this fixer, a bad page request or an empty row
// selection; 503 without an operations registry.
package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Page size bounds of the rows endpoint.
const (
	DefaultLimit = 100
	MaxLimit     = 500
	// recentWindow bounds the op history GET /repairs reads for each fixer's
	// last plan and apply.
	recentWindow = 30 * 24 * time.Hour
	recentLimit  = 5000
)

// Enqueuer starts an operation.
type Enqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// OpStore reads stored operation rows.
type OpStore interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
	ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error)
}

// Handler serves the repairs routes.
type Handler struct {
	fixers   *repairs.Registry
	enqueuer Enqueuer // nil when the registry is not initialised
	ops      OpStore
	now      func() time.Time
}

// New builds the handler. enqueuer may be nil: plan/apply then answer 503.
func New(fixers *repairs.Registry, enqueuer Enqueuer, ops OpStore) *Handler {
	return &Handler{fixers: fixers, enqueuer: enqueuer, ops: ops, now: time.Now}
}

// OpRef is a pointer to a plan or apply run.
type OpRef struct {
	OperationID string     `json:"operation_id"`
	Status      string     `json:"status"`
	QueuedAt    time.Time  `json:"queued_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// Plan summary (completed plans only).
	Total         *int           `json:"total,omitempty"`
	Applicable    *int           `json:"applicable,omitempty"`
	SkippedByKind map[string]int `json:"skipped_by_kind,omitempty"`
	// Apply summary (completed or failed applies with a stored result).
	DryRun    *bool          `json:"dry_run,omitempty"`
	ByOutcome map[string]int `json:"by_outcome,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// FixerInfo is one entry of GET /repairs.
type FixerInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	LastPlan    *OpRef `json:"last_plan,omitempty"`
	LastApply   *OpRef `json:"last_apply,omitempty"`
}

// StartedResponse answers plan/apply.
type StartedResponse struct {
	OperationID string `json:"operation_id"`
	DefID       string `json:"def_id"`
	FixerID     string `json:"fixer_id"`
	Status      string `json:"status"`
}

// ApplyRequest is the body of POST /repairs/:fixer/apply.
type ApplyRequest struct {
	PlanOpID string   `json:"plan_op_id"`
	RowIDs   []string `json:"row_ids"`
	// DryRun: only an explicit false writes (owner rule 2026-09-25: an
	// omitted mode is a preview). The lane's Apply button sends false; true
	// or omitted re-checks the rows and reports would_apply per row.
	DryRun *bool `json:"dry_run,omitempty"`
}

// ListFixers implements GET /repairs.
func (h *Handler) ListFixers(c *gin.Context) {
	fixers := h.fixers.List()
	lastPlan, lastApply := h.recentRuns()
	out := make([]FixerInfo, 0, len(fixers))
	for _, f := range fixers {
		out = append(out, FixerInfo{ID: f.ID(), Title: f.Title(), Description: f.Description(),
			LastPlan: lastPlan[f.ID()], LastApply: lastApply[f.ID()]})
	}
	httputil.RespondWithOK(c, gin.H{"fixers": out})
}

// recentRuns finds each fixer's newest plan and apply in the recent op
// history. Best effort: a read failure leaves them unset.
func (h *Handler) recentRuns() (plan, apply map[string]*OpRef) {
	plan, apply = map[string]*OpRef{}, map[string]*OpRef{}
	if h.ops == nil {
		return plan, apply
	}
	rows, err := h.ops.ListOperationsV2Since(h.now().Add(-recentWindow), recentLimit)
	if err != nil {
		return plan, apply
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].QueuedAt.After(rows[j].QueuedAt) })
	for i := range rows {
		r := &rows[i]
		if r.DefID != repairs.PlanOpID && r.DefID != repairs.ApplyOpID {
			continue
		}
		var p struct {
			FixerID string `json:"fixer_id"`
		}
		if json.Unmarshal([]byte(r.Params), &p) != nil || p.FixerID == "" {
			continue
		}
		target := plan
		if r.DefID == repairs.ApplyOpID {
			target = apply
		}
		if _, seen := target[p.FixerID]; seen {
			continue
		}
		target[p.FixerID] = opRef(r)
	}
	return plan, apply
}

func opRef(r *database.OperationV2Row) *OpRef {
	ref := &OpRef{OperationID: r.ID, Status: r.Status, QueuedAt: r.QueuedAt, CompletedAt: r.CompletedAt}
	if r.ErrorMessage != nil {
		ref.Error = *r.ErrorMessage
	}
	if r.ResultData == nil {
		return ref
	}
	// Decode the summary fields only; rows are skipped by the decoder.
	var sum struct {
		Total         *int           `json:"total"`
		Applicable    *int           `json:"applicable"`
		SkippedByKind map[string]int `json:"skipped_by_kind"`
		DryRun        *bool          `json:"dry_run"`
		ByOutcome     map[string]int `json:"by_outcome"`
	}
	if json.Unmarshal([]byte(*r.ResultData), &sum) == nil {
		if r.DefID == repairs.PlanOpID {
			ref.Total, ref.Applicable, ref.SkippedByKind = sum.Total, sum.Applicable, sum.SkippedByKind
		} else {
			ref.DryRun, ref.ByOutcome = sum.DryRun, sum.ByOutcome
		}
	}
	return ref
}

func (h *Handler) fixer(c *gin.Context) (repairs.Fixer, bool) {
	id := c.Param("fixer")
	f, ok := h.fixers.Get(id)
	if !ok {
		httputil.RespondWithNotFound(c, "repair fixer", id)
		return nil, false
	}
	return f, true
}

// StartPlan implements POST /repairs/:fixer/plan. The body, if any, is the
// fixer's own params (a JSON object) and is passed through unchanged.
func (h *Handler) StartPlan(c *gin.Context) {
	f, ok := h.fixer(c)
	if !ok {
		return
	}
	if h.enqueuer == nil {
		httputil.RespondWithServiceUnavailable(c, "operations registry not initialized")
		return
	}
	var params json.RawMessage
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := json.NewDecoder(c.Request.Body).Decode(&params); err != nil {
			httputil.RespondWithBadRequest(c, "body must be a JSON object of fixer params: "+err.Error())
			return
		}
	}
	if len(params) > 0 && string(params) != "null" && params[0] != '{' {
		httputil.RespondWithBadRequest(c, "body must be a JSON object of fixer params")
		return
	}
	h.enqueue(c, repairs.PlanOpID, f.ID(), repairs.PlanParams{FixerID: f.ID(), Params: params})
}

// ListPlanRows implements GET /repairs/:fixer/plan/:op_id/rows
// ?offset=&limit=&filter=applicable|skipped.
func (h *Handler) ListPlanRows(c *gin.Context) {
	f, ok := h.fixer(c)
	if !ok {
		return
	}
	offset, err := intQuery(c, "offset", 0)
	if err != nil || offset < 0 {
		httputil.RespondWithValidationError(c, "offset", "must be a non-negative integer")
		return
	}
	limit, err := intQuery(c, "limit", DefaultLimit)
	if err != nil || limit < 1 {
		httputil.RespondWithValidationError(c, "limit", "must be a positive integer")
		return
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	opID := c.Param("op_id")
	plan, ok := h.loadPlan(c, opID, f.ID())
	if !ok {
		return
	}
	page, err := plan.Page(opID, c.Query("filter"), offset, limit)
	if err != nil {
		httputil.RespondWithValidationError(c, "filter", err.Error())
		return
	}
	httputil.RespondWithOK(c, page)
}

// StartApply implements POST /repairs/:fixer/apply {plan_op_id, row_ids}.
func (h *Handler) StartApply(c *gin.Context) {
	f, ok := h.fixer(c)
	if !ok {
		return
	}
	if h.enqueuer == nil {
		httputil.RespondWithServiceUnavailable(c, "operations registry not initialized")
		return
	}
	var req ApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid body: "+err.Error())
		return
	}
	if req.PlanOpID == "" {
		httputil.RespondWithValidationError(c, "plan_op_id", "required")
		return
	}
	if len(req.RowIDs) == 0 {
		httputil.RespondWithValidationError(c, "row_ids", "pick at least one row from the plan")
		return
	}
	// Check the plan now so a wrong id fails the request, not the op later.
	if _, ok := h.loadPlan(c, req.PlanOpID, f.ID()); !ok {
		return
	}
	dry := true
	if req.DryRun != nil {
		dry = *req.DryRun
	}
	h.enqueue(c, repairs.ApplyOpID, f.ID(), repairs.ApplyParams{
		FixerID: f.ID(), PlanOpID: req.PlanOpID, RowIDs: req.RowIDs, DryRun: &dry,
	})
}

func (h *Handler) loadPlan(c *gin.Context, opID, fixerID string) (*repairs.PlanResult, bool) {
	if h.ops == nil {
		httputil.RespondWithServiceUnavailable(c, "operations store not initialized")
		return nil, false
	}
	plan, err := repairs.LoadPlan(h.ops, opID, fixerID)
	switch {
	case err == nil:
		return plan, true
	case errors.Is(err, repairs.ErrPlanNotFound):
		httputil.RespondWithNotFound(c, "repair plan", opID)
	case errors.Is(err, repairs.ErrPlanNotComplete):
		httputil.RespondWithConflict(c, err.Error())
	case errors.Is(err, repairs.ErrNotAPlan), errors.Is(err, repairs.ErrPlanOtherFixer):
		httputil.RespondWithBadRequest(c, err.Error())
	default:
		httputil.InternalError(c, "failed to read repair plan", err)
	}
	return nil, false
}

func (h *Handler) enqueue(c *gin.Context, defID, fixerID string, params any) {
	opID, err := h.enqueuer.EnqueueOp(c.Request.Context(), defID, params)
	if err != nil {
		httputil.InternalError(c, "enqueue "+defID+" failed", err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"data": StartedResponse{
		OperationID: opID, DefID: defID, FixerID: fixerID, Status: "queued",
	}})
}

func intQuery(c *gin.Context, key string, def int) (int, error) {
	v := c.Query(key)
	if v == "" {
		return def, nil
	}
	return strconv.Atoi(v)
}
