// file: internal/server/handlers/review/handler.go
// version: 1.3.0
// guid: 2b6f9c14-8e37-4a5d-91c6-0f4a7d2e8b53
// last-edited: 2026-08-06

// Package reviewhandler hosts the universal review-queue HTTP handlers (PR-A1).
//
// The review queue is a generic, producer-agnostic home for everything the
// system has flagged for a human decision. A1 is pure infrastructure: the store
// (database.ReviewStore) plus this HTTP surface plus an apply-handler registry
// that future producers register into. v1's producer is the regroup op (Track
// B, not built yet), so at A1 the queue starts empty and no apply handlers are
// registered.
//
// Apply-handler registry + global switch: approving an item dispatches on the
// CHOSEN ACTION to a registered ApplyFunc, BUT only when the global apply "big
// switch" (applyEnabled) is on. When an action has a handler AND the switch is on,
// approve runs it and sets the item to "applied". Otherwise — no handler, OR the
// switch is off (review-only mode, the default) — approve just sets "approved" and
// returns OK with a note, never executing anything. This makes "see everything in
// the review pane, nothing auto-merges" the default until the switch is deliberately
// flipped. Track B's B2 registers the regroup apply handlers;
// config.ReviewApplyEnabled is the switch.
//
// 🔴 DISPATCH IS ON THE ACTION, NOT THE KIND (owner item 2, 2026-08-06).
//
// Until this change the registry was keyed by ReviewItem.Kind, which conflated two
// different questions: a Kind says what SHAPE the classifier saw ("these files live
// in Disc N folders"), an action says what a human should DO about it ("these are
// six different novels — leave them apart"). Prod proved the two disagree: 3 of the
// 130 `regroup.multidisc` holds measured on 2026-08-06 hold members that are each
// book-length, because the disc and chapter/edition branches of classifyGroup never
// evaluate the series guard. Kind-keyed dispatch would merge those distinct novels
// through an apply path that hard-deletes the absorbed Book rows.
//
// So approve now resolves an ACTION — the request body's explicit choice when the
// human made one, else the hold's own `recommendedAction` — validates it against the
// closed vocabulary in internal/itunes/service, and dispatches on that. A typo'd
// action is a 400, never a silent fall back to the recommendation: a human who meant
// "separate" must not be given "combine" because they mistyped.
//
// 🔴 AND THE CHOICE IS PERSISTED. ReviewItem.ChosenAction records what the human
// picked, written atomically with the status by SetReviewItemDecision. That matters
// because with review_apply_enabled OFF — production's setting — approving executes
// nothing, and ReplayApprovedItems does the work later. Replay reads ChosenAction
// first (effectiveActionFor) and only falls back to the payload's recommendation for
// rows written before the field existed. Without that, an override would evaporate at
// the moment it was recorded and the replay would merge books a human said to keep
// apart. See effectiveActionFor and database.ReviewItem.ChosenAction.
package reviewhandler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/gin-gonic/gin"
)

// approvableActions is the CLOSED approve vocabulary: action → may a human CHOOSE
// it. It mirrors the classifier's Action* constants
// (internal/itunes/service/fs_regroup_shape.go) by IMPORTING them rather than
// re-declaring the strings, so the producer and the dispatcher can never drift.
//
// ActionInsufficientEvidence is in the vocabulary but is NOT approvable — it is a
// statement BY the machine ("I cannot tell"), not a decision a human can make.
// Approving one would record a decision nobody took, on exactly the holds where the
// evidence is weakest. ActionDuplicateOf is approvable in principle but has no apply
// path yet; approveOne rejects it explicitly rather than no-op'ing (see there).
var approvableActions = map[string]bool{
	itunesservice.ActionCombine:              true,
	itunesservice.ActionSeparate:             true,
	itunesservice.ActionVersionGroup:         true,
	itunesservice.ActionDuplicateOf:          true,
	itunesservice.ActionInsufficientEvidence: false,
}

// ApplyFunc executes the real-world action an approved review item represents
// (e.g. the regroup op collapsing shattered books). Registered per ACTION — see
// RegisterApplyHandler and the package doc; it was per Kind before 2026-08-06.
type ApplyFunc func(ctx context.Context, item database.ReviewItem) error

// Handler hosts the review-queue HTTP endpoints.
type Handler struct {
	// store is the wire-time snapshot of the review-queue store.
	store database.ReviewStore

	// applyEnabled is the GLOBAL "big switch" gate, read at approve time. When it
	// returns false (the default), approving a hold records the decision but NEVER
	// executes its apply handler — every hold stays visible/reviewable. A nil gate is
	// treated as disabled (fail-safe: never auto-apply unless explicitly turned on).
	applyEnabled func() bool

	// applyHandlers maps a CHOSEN ACTION (an itunesservice.Action* string) to the
	// code that carries it out. Empty in A1; populated by producers (e.g. B2's
	// regroup) via RegisterApplyHandler. Guarded by mu because registration may
	// happen during wiring while requests could already be arriving.
	//
	// Keyed by action rather than by Kind since 2026-08-06 — see the package doc.
	// Actions with no entry (separate, insufficient-evidence, duplicate-of) are
	// handled by approveOne directly and never reach this map.
	mu            sync.RWMutex
	applyHandlers map[string]ApplyFunc
}

// New constructs a review Handler. applyEnabled is the global apply gate (see the
// field doc); pass nil to keep the queue review-only (apply handlers registered but
// never executed).
func New(store database.ReviewStore, applyEnabled func() bool) *Handler {
	return &Handler{
		store:         store,
		applyEnabled:  applyEnabled,
		applyHandlers: make(map[string]ApplyFunc),
	}
}

// applyGloballyEnabled reports whether the apply "big switch" is on. A nil gate is
// fail-safe OFF.
func (h *Handler) applyGloballyEnabled() bool {
	return h.applyEnabled != nil && h.applyEnabled()
}

// RegisterApplyHandler registers the code that carries out one ACTION (an
// itunesservice.Action* string, NOT a review Kind). Producers call this at wire
// time (B2 registers the regroup apply paths).
func (h *Handler) RegisterApplyHandler(action string, fn ApplyFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.applyHandlers[action] = fn
}

func (h *Handler) applyHandlerFor(action string) (ApplyFunc, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fn, ok := h.applyHandlers[action]
	return fn, ok
}

// recommendedActionFor reads the classifier's recommendation out of a hold's
// payload, defaulting to ActionInsufficientEvidence.
//
// 🔴 EVERY FAILURE MODE HERE FAILS CLOSED TO insufficient-evidence, which has no
// apply handler and is not approvable. A hold written before the recommendation
// existed (all ~356 currently in prod's queue), a hold whose payload is not JSON,
// and a hold carrying an empty action all land in the same place: approve refuses
// until a human names an action explicitly. The alternative — guessing a default —
// would guess `combine` on precisely the holds with the least evidence, and combine
// routes to an apply path that hard-deletes absorbed Book rows.
//
// A malformed payload is logged rather than swallowed: it means a producer wrote
// something this handler cannot read, which is a bug worth seeing even though the
// safe behaviour is already guaranteed.
func recommendedActionFor(item database.ReviewItem) string {
	if item.Payload == "" {
		return itunesservice.ActionInsufficientEvidence
	}
	var p struct {
		RecommendedAction string `json:"recommendedAction"`
	}
	if err := json.Unmarshal([]byte(item.Payload), &p); err != nil {
		slog.Warn("review approve: hold payload is not decodable JSON — treating as insufficient-evidence",
			"item", item.ID, "kind", item.Kind, "err", err)
		return itunesservice.ActionInsufficientEvidence
	}
	if p.RecommendedAction == "" {
		return itunesservice.ActionInsufficientEvidence
	}
	return p.RecommendedAction
}

// effectiveActionFor answers the ONE question both approve and replay must answer
// identically: what does this hold currently mean?
//
// 🔴 THE PERSISTED HUMAN CHOICE WINS OVER THE MACHINE'S SUGGESTION. ReviewItem.ChosenAction
// is written when a reviewer approves (SetReviewItemDecision); the payload's
// `recommendedAction` is what the classifier proposed. When they disagree, the human
// is right by definition — that disagreement IS owner item 2. A hold recommending
// `combine` that a human approved as `separate` must never be replayed as a merge,
// because the combine apply path hard-deletes the absorbed Book rows.
//
// The fallback covers rows written before ChosenAction existed: those resolve through
// the payload, and for a pre-recommendation hold that lands on insufficient-evidence,
// which has no apply handler and is not approvable. Old rows therefore keep working
// and keep failing closed.
//
// Both approveOne and ReplayApprovedItems route through here so the two can never
// carry out different decisions for the same row.
func effectiveActionFor(item database.ReviewItem) string {
	if a := strings.TrimSpace(item.ChosenAction); a != "" {
		return a
	}
	return recommendedActionFor(item)
}

// actionRejection is a per-item REFUSAL to approve — a caller error (bad action,
// unapprovable action, unimplemented action), never a server fault.
//
// It exists as a distinct type because approve is now reachable two ways with two
// different correct responses: a single approve turns it into a 4xx/501, while a
// BULK approve must SKIP that item and keep going. Before the action dispatch every
// approveOne error was a genuine fault and bulk was right to abort on it; conflating
// the two would let one unapprovable hold abort a 121-hold batch.
type actionRejection struct {
	status int    // HTTP status a single approve should return
	code   string // stable machine-readable error code
	msg    string // human-readable reason, safe to show a reviewer
}

func (e *actionRejection) Error() string { return e.msg }

func rejectf(status int, code, msg string) *actionRejection {
	return &actionRejection{status: status, code: code, msg: msg}
}

// GetReviewCount handles GET /api/v1/review/count.
//
// Response: { "count": N, "byKind": { "<kind>": n, ... } } where count and the
// byKind breakdown both cover PENDING items only (decision #1: the badge counts
// intentional holds awaiting a decision, never decided items).
func (h *Handler) GetReviewCount(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	count, err := h.store.CountReviewItems(database.ReviewStatusPending)
	if err != nil {
		httputil.InternalError(c, "failed to count review items", err)
		return
	}
	stats, err := h.store.ReviewStatsByKind()
	if err != nil {
		httputil.InternalError(c, "failed to compute review stats", err)
		return
	}
	byKind := gin.H{}
	for _, s := range stats {
		if s.Status == database.ReviewStatusPending {
			byKind[s.Kind] = s.Count
		}
	}
	httputil.RespondWithOK(c, gin.H{"count": count, "byKind": byKind})
}

// ListReviewItems handles GET /api/v1/review/items.
//
// Query params: status (default "pending"), kind, limit (default 50), offset.
func (h *Handler) ListReviewItems(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	status := c.DefaultQuery("status", database.ReviewStatusPending)
	// "all" is an explicit escape hatch for "every status".
	if status == "all" {
		status = ""
	}
	filter := database.ReviewFilter{
		Status: status,
		Kind:   c.Query("kind"),
		Limit:  atoiDefault(c.Query("limit"), 50),
		Offset: atoiDefault(c.Query("offset"), 0),
	}
	items, total, err := h.store.ListReviewItems(filter)
	if err != nil {
		httputil.InternalError(c, "failed to list review items", err)
		return
	}
	if items == nil {
		items = []database.ReviewItem{}
	}
	httputil.RespondWithList(c, items, total, filter.Limit, filter.Offset)
}

// approveRequest is the OPTIONAL POST /api/v1/review/items/:id/approve body.
//
// Absent or empty Action means "do what the hold recommends". A present Action
// OVERRIDES the recommendation — that is the entire point of owner item 2: the
// machine proposes, the human disposes, and the human's choice is what runs.
type approveRequest struct {
	Action string `json:"action,omitempty"`
}

// ApproveReviewItem handles POST /api/v1/review/items/:id/approve.
//
// The chosen action is the body's `action` when supplied, else the hold's
// `recommendedAction`. If that action has a registered apply handler AND the global
// switch is on, it runs and the item is set "applied"; otherwise the item is set
// "approved" with an explanatory note. Actions a human may not choose
// (insufficient-evidence), actions outside the vocabulary, and actions with no
// implementation (duplicate-of) are REFUSED, never quietly downgraded.
func (h *Handler) ApproveReviewItem(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	// The body is optional (the pre-override frontend sends none), so an empty body
	// is not an error — but a MALFORMED body is. Binding errors are surfaced rather
	// than ignored: silently discarding an unparseable `{"action": ...}` would run
	// the recommendation while the caller believes it overrode it.
	var req approveRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	id := c.Param("id")
	updated, chosen, note, err := h.approveOne(c.Request.Context(), id, strings.TrimSpace(req.Action))
	var rej *actionRejection
	if errors.As(err, &rej) {
		httputil.RespondWithError(c, rej.status, rej.msg, rej.code)
		return
	}
	if err != nil {
		httputil.InternalError(c, "failed to approve review item", err)
		return
	}
	if updated == nil {
		httputil.RespondWithNotFound(c, "review item", id)
		return
	}
	// chosenAction is echoed so the caller can see WHICH action ran — with an empty
	// body that is the recommendation, which the caller may not have read.
	resp := gin.H{"item": updated, "chosenAction": chosen}
	if note != "" {
		resp["note"] = note
	}
	httputil.RespondWithOK(c, resp)
}

// approveOne resolves, validates and dispatches the action for a single item, then
// transitions it. Returns (nil, "", "", nil) when the item does not exist.
//
// requested is the caller's explicit override ("" = use the hold's recommendation).
// The returned action is the one actually dispatched. A returned *actionRejection
// means the caller asked for something that must not happen; any other error is a
// genuine fault.
func (h *Handler) approveOne(ctx context.Context, id, requested string) (*database.ReviewItem, string, string, error) {
	item, err := h.store.GetReviewItem(id)
	if err != nil {
		return nil, "", "", err
	}
	if item == nil {
		return nil, "", "", nil
	}

	// The default is the hold's EFFECTIVE action, not the raw payload recommendation:
	// re-approving an already-decided hold with an empty body must not silently revert
	// a human's recorded override back to the machine's suggestion. Re-approve is a
	// supported flow — it is the recovery path replay's skip reason advertises.
	// Pending holds carry no ChosenAction, so for them this is the recommendation.
	chosen := requested
	if chosen == "" {
		chosen = effectiveActionFor(*item)
	}

	// (1) Closed vocabulary. An unrecognised action is a 400, NEVER a fall back to
	// the recommendation: a reviewer who typed "seperate" and meant "leave these six
	// novels apart" must not be handed "combine" because of a typo. Silent fallback
	// is how a merge happens that nobody chose.
	approvable, known := approvableActions[chosen]
	if !known {
		return nil, chosen, "", rejectf(http.StatusBadRequest, "REVIEW_UNKNOWN_ACTION",
			"unknown review action "+strconv.Quote(chosen)+"; expected one of combine, separate, version-group, duplicate-of")
	}

	// (2) insufficient-evidence is emitted BY the classifier, never chosen by a human.
	// Reaching here by default (an old hold, or one whose members have no runtimes)
	// means the machine has nothing to say, so the human must say it explicitly.
	if !approvable {
		return nil, chosen, "", rejectf(http.StatusBadRequest, "REVIEW_ACTION_NOT_DECIDABLE",
			"this hold recommends 'insufficient-evidence' — the classifier cannot tell what to do, "+
				"so approving requires an explicit {\"action\": \"...\"} choice (combine, separate or version-group)")
	}

	// (3) duplicate-of has no apply path yet (deciding a folder duplicates an existing
	// book needs cross-book identity evidence the regroup classifier never gathers).
	// Refuse loudly: accepting it would mark the hold decided while doing nothing,
	// and "decided" is sticky — UpsertReviewItem never re-offers a non-pending hold.
	if chosen == itunesservice.ActionDuplicateOf {
		return nil, chosen, "", rejectf(http.StatusNotImplemented, "REVIEW_ACTION_NOT_IMPLEMENTED",
			"action 'duplicate-of' is not implemented: the duplicate-detection track owns it. "+
				"Reject the hold instead, or choose combine/separate/version-group")
	}

	// (4) 🔴 EVERY TRANSITION BELOW WRITES THE CHOSEN ACTION ALONGSIDE THE STATUS.
	//
	// This is what makes an override durable, and it is the reason the 409
	// REVIEW_OVERRIDE_NOT_PERSISTABLE refusal that used to sit here is gone. Before
	// SetReviewItemDecision existed the chosen action was stored NOWHERE, so with
	// review_apply_enabled off — production's setting — a reviewer could not record a
	// disagreement at all: approving a `combine` hold as `separate` wrote plain
	// "approved", and ReplayApprovedItems re-resolved the action from the payload and
	// would merge the books the human said to keep apart. Refusing the override was the
	// only safe answer then, but it also meant owner item 2 did not actually ship.
	//
	// Now the choice is persisted atomically with the status and replay prefers it
	// (effectiveActionFor), so the override survives the switch being flipped later.

	// (5) separate needs NO apply handler and never will. Every member is already its
	// own book, so "separate into N" is a statement that the current on-disk state is
	// correct — there is nothing to execute, only a decision to record. The dedup-key
	// idempotency in UpsertReviewItem keeps it decided across re-scans, which is the
	// whole mechanism: a re-scan skips a non-pending hold. Recording the action still
	// matters: it is what stops a replay from falling back to a `combine` payload.
	if chosen == itunesservice.ActionSeparate {
		updated, serr := h.store.SetReviewItemDecision(id, database.ReviewStatusApproved, chosen)
		if serr != nil {
			return nil, "", "", serr
		}
		return updated, chosen, "action 'separate' needs no apply step — every member is already its own book; " +
			"recorded as approved so re-scans leave it alone", nil
	}

	fn, hasHandler := h.applyHandlerFor(chosen)
	if hasHandler && h.applyGloballyEnabled() {
		if err := fn(ctx, *item); err != nil {
			return nil, "", "", err
		}
		updated, err := h.store.SetReviewItemDecision(id, database.ReviewStatusApplied, chosen)
		return updated, chosen, "", err
	}
	// Either no apply handler for this action, OR the global apply switch is OFF
	// (review-only mode). Record the decision as "approved" but do NOT execute — the
	// item stays visible/reviewable and nothing is merged. The action is recorded even
	// though nothing ran, which is precisely the case replay later reads back.
	updated, err := h.store.SetReviewItemDecision(id, database.ReviewStatusApproved, chosen)
	if err != nil {
		return nil, "", "", err
	}
	note := "no apply handler registered for action " + chosen + "; marked approved"
	if hasHandler {
		note = "apply is disabled (review-only mode): item approved but NOT applied — " +
			"enable review_apply_enabled to perform the merge"
	}
	return updated, chosen, note, nil
}

// RejectReviewItem handles POST /api/v1/review/items/:id/reject.
func (h *Handler) RejectReviewItem(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	id := c.Param("id")
	updated, err := h.store.SetReviewItemStatus(id, database.ReviewStatusRejected)
	if err != nil {
		httputil.InternalError(c, "failed to reject review item", err)
		return
	}
	if updated == nil {
		httputil.RespondWithNotFound(c, "review item", id)
		return
	}
	httputil.RespondWithOK(c, gin.H{"item": updated})
}

// bulkRequest is the POST /api/v1/review/bulk body (decision #4: grouped bulk
// actions). One of Kind or IDs must be set — an unscoped bulk over the whole
// queue is rejected to avoid an accidental approve/reject-all.
//
// 🔴 Action here is the BULK VERB ("approve" | "reject"), and there is deliberately
// NO batch-wide review action. A single review action applied to a heterogeneous
// batch is the exact footgun this whole change exists to remove: a batch "combine"
// over `regroup.multidisc` would merge the 3 series near-misses that motivated it.
// Bulk approve instead uses EACH item's own recommendation, so it processes the 121
// all-fragment holds and skips those 3 — the payoff, not a limitation.
type bulkRequest struct {
	Action string   `json:"action"` // "approve" | "reject"
	Kind   string   `json:"kind,omitempty"`
	IDs    []string `json:"ids,omitempty"`
}

// bulkSkip is one item a bulk approve refused to act on, with the reason a reviewer
// needs in order to go handle it individually.
type bulkSkip struct {
	ID     string `json:"id"`
	Action string `json:"action"` // the action that was refused (usually insufficient-evidence)
	Reason string `json:"reason"`
}

// bulkResult reports the outcome of a bulk action.
type bulkResult struct {
	Action    string     `json:"action"`
	Approved  []string   `json:"approved,omitempty"`
	Applied   []string   `json:"applied,omitempty"`
	Rejected  []string   `json:"rejected,omitempty"`
	NotFound  []string   `json:"not_found,omitempty"`
	Skipped   []bulkSkip `json:"skipped,omitempty"`
	Processed int        `json:"processed"`
}

// BulkReviewAction handles POST /api/v1/review/bulk.
//
// Targets are the explicit IDs when provided, otherwise every PENDING item of
// the given Kind. Volume is bounded (v1 producer holds only), so this runs
// sequentially; a worker pool would be premature until real apply handlers land.
func (h *Handler) BulkReviewAction(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	var req bulkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}
	if req.Action != "approve" && req.Action != "reject" {
		httputil.RespondWithBadRequest(c, "action must be 'approve' or 'reject'")
		return
	}
	if req.Kind == "" && len(req.IDs) == 0 {
		httputil.RespondWithBadRequest(c, "bulk action requires 'kind' or 'ids' — refusing to act on the entire queue")
		return
	}

	ids := req.IDs
	if len(ids) == 0 {
		// Kind-scoped: act on every pending item of that Kind.
		items, _, err := h.store.ListReviewItems(database.ReviewFilter{
			Status: database.ReviewStatusPending,
			Kind:   req.Kind,
			Limit:  bulkScanLimit,
		})
		if err != nil {
			httputil.InternalError(c, "failed to list review items for bulk action", err)
			return
		}
		ids = make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ID)
		}
	}

	result := bulkResult{Action: req.Action}
	for _, id := range ids {
		switch req.Action {
		case "approve":
			// "" = use this item's OWN recommendation. See bulkRequest's comment for
			// why there is no batch-wide action.
			updated, chosen, _, err := h.approveOne(c.Request.Context(), id, "")
			// A per-item refusal (no decidable action, unimplemented action) SKIPS that
			// item and the batch continues. Aborting the whole batch on one
			// insufficient-evidence hold would make bulk approve useless on a queue
			// where 70 of 356 holds are exactly that.
			var rej *actionRejection
			if errors.As(err, &rej) {
				result.Skipped = append(result.Skipped, bulkSkip{ID: id, Action: chosen, Reason: rej.msg})
				continue
			}
			if err != nil {
				httputil.InternalError(c, "failed to approve review item "+id, err)
				return
			}
			if updated == nil {
				result.NotFound = append(result.NotFound, id)
				continue
			}
			if updated.Status == database.ReviewStatusApplied {
				result.Applied = append(result.Applied, id)
			} else {
				result.Approved = append(result.Approved, id)
			}
			result.Processed++
		case "reject":
			updated, err := h.store.SetReviewItemStatus(id, database.ReviewStatusRejected)
			if err != nil {
				httputil.InternalError(c, "failed to reject review item "+id, err)
				return
			}
			if updated == nil {
				result.NotFound = append(result.NotFound, id)
				continue
			}
			result.Rejected = append(result.Rejected, id)
			result.Processed++
		}
	}
	httputil.RespondWithOK(c, result)
}

// bulkScanLimit caps the kind-scoped pending fetch. Comfortably exceeds any
// realistic v1 hold population (intentional holds only, never raw backlogs).
const bulkScanLimit = 100_000

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
