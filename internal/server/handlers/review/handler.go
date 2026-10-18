// file: internal/server/handlers/review/handler.go
// version: 1.12.0
// guid: 2b6f9c14-8e37-4a5d-91c6-0f4a7d2e8b53
// last-edited: 2026-10-18

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
// picked, written atomically with the status (database.ReviewItemApplyClaimer: DecideReviewItem
// for a decision, FinishReviewItemApply for an apply). That matters
// because with review_apply_enabled OFF — production's setting — approving executes
// nothing, and approving the same item again once the switch is on does the work
// later (POST /review/replay-approved did that in one call until 01-P81j retired it).
// The second approve reads ChosenAction first (effectiveActionFor) and only falls back
// to the payload's recommendation for rows written before the field existed. Without
// that, an override would evaporate at the moment it was recorded and the second
// approve would merge books a human said to keep apart. See effectiveActionFor and database.ReviewItem.ChosenAction.
package reviewhandler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applycap"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
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
// evidence is weakest. ActionDuplicateOf is approvable AND now has an apply path
// (maintenance.ApplyDuplicateOf, wired 2026-08-19); it merges the folder's debris
// into the canonical book the dedup track names, and errors rather than guessing
// when that track names zero or several.
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

	// claimer owns every review write that can race an apply — the claim, its finish
	// and release, and every decision (database.ReviewItemApplyClaimer, resolved from
	// store with database.AsCapability). Nil when the store lacks the capability; then
	// approve, reject and bulk refuse with 500 REVIEW_CLAIM_UNAVAILABLE rather than
	// write unguarded, because an unguarded decision can land over a running apply.
	claimer database.ReviewItemApplyClaimer

	// unrecorded maps an item id to the claim token of an apply that SUCCEEDED in this
	// process but whose "applied" write failed (appliedUnrecordedError). The item stays
	// applying under that live claim, so nothing else can claim or decide it; the next
	// approve of the item finds the token here and retries the finish WITHOUT running
	// the apply again. Lost on restart, which is correct: the claim is then dead and an
	// approve takes it over and re-runs the apply (see appliedUnrecordedError).
	unrecordedMu sync.Mutex
	unrecorded   map[string]string

	// applyEnabled is the GLOBAL "big switch" gate, read at approve time. When it
	// returns false (the default), approving a hold records the decision but NEVER
	// executes its apply handler — every hold stays visible/reviewable. A nil gate is
	// treated as disabled (fail-safe: never auto-apply unless explicitly turned on).
	applyEnabled func() bool

	// applyCap returns the configured bulk apply ceiling (config
	// bulk_apply_max_items) for the fail-safe in internal/applycap. Injected like
	// applyEnabled so this package stays decoupled from the global config. A nil
	// func or a value ≤0 yields applycap.Default — never unlimited.
	applyCap func() int

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
// never executed). applyCap supplies the bulk apply ceiling; nil means
// applycap.Default.
func New(store database.ReviewStore, applyEnabled func() bool, applyCap func() int) *Handler {
	claimer, ok := database.AsCapability[database.ReviewItemApplyClaimer](store)
	if !ok && store != nil {
		reviewLog.Error("review store %T does not implement database.ReviewItemApplyClaimer: "+
			"approve, reject and bulk review actions will refuse with REVIEW_CLAIM_UNAVAILABLE", store)
	}
	return &Handler{
		store:         store,
		claimer:       claimer,
		unrecorded:    make(map[string]string),
		applyEnabled:  applyEnabled,
		applyCap:      applyCap,
		applyHandlers: make(map[string]ApplyFunc),
	}
}

var reviewLog = logger.New("review")

// refuseWithoutClaimer answers 500 REVIEW_CLAIM_UNAVAILABLE and returns true when the
// store has no apply-claim capability. Every review write goes through the claimer, so
// without one there is no write this handler can make safely.
func (h *Handler) refuseWithoutClaimer(c *gin.Context) bool {
	if h.claimer != nil {
		return false
	}
	httputil.RespondWithError(c, http.StatusInternalServerError,
		"the review store cannot claim items for apply, so review decisions are disabled", "REVIEW_CLAIM_UNAVAILABLE")
	return true
}

func (h *Handler) unrecordedToken(id string) (string, bool) {
	h.unrecordedMu.Lock()
	defer h.unrecordedMu.Unlock()
	tok, ok := h.unrecorded[id]
	return tok, ok
}

func (h *Handler) setUnrecorded(id, token string) {
	h.unrecordedMu.Lock()
	defer h.unrecordedMu.Unlock()
	if token == "" {
		delete(h.unrecorded, id)
		return
	}
	h.unrecorded[id] = token
}

// configuredApplyCap returns the raw configured ceiling (0 when no gate was
// injected); applycap.Effective turns that into the enforced value.
func (h *Handler) configuredApplyCap() int {
	if h.applyCap == nil {
		return 0
	}
	return h.applyCap()
}

// refuseIfOverCap answers with 422 BULK_APPLY_CAP_EXCEEDED and returns true when
// a bulk action targeting n items exceeds the fail-safe cap. Nothing has been
// written when it fires — callers check BEFORE the first store mutation.
func (h *Handler) refuseIfOverCap(c *gin.Context, op string, n int) bool {
	ex := applycap.Refuse(op, n, h.configuredApplyCap())
	if ex == nil {
		return false
	}
	httputil.RespondWithApplyCapExceeded(c, ex)
	return true
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

// effectiveActionFor answers the ONE question every approve of a hold must answer
// the same way, first time or re-approve: what does this hold currently mean?
//
// 🔴 THE PERSISTED HUMAN CHOICE WINS OVER THE MACHINE'S SUGGESTION. ReviewItem.ChosenAction
// is written when a reviewer approves (DecideReviewItem, or FinishReviewItemApply when
// the approve applied); the payload's
// `recommendedAction` is what the classifier proposed. When they disagree, the human
// is right by definition — that disagreement IS owner item 2. A hold recommending
// `combine` that a human approved as `separate` must never be re-approved as a merge,
// because the combine apply path hard-deletes the absorbed Book rows.
//
// The fallback covers rows written before ChosenAction existed: those resolve through
// the payload, and for a pre-recommendation hold that lands on insufficient-evidence,
// which has no apply handler and is not approvable. Old rows therefore keep working
// and keep failing closed.
//
// approveOne is the only caller, so a first approve and a later re-approve of the same
// row can never carry out different decisions.
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

// appliedUnrecordedError means the apply handler SUCCEEDED but the "applied" status
// could not be written, even after retries. The work is done. The item stays
// "applying" under this process's live claim, so no other approve, reject or bulk
// action can claim or decide it, and the claim's token is kept in Handler.unrecorded.
//
// What happens next, exactly:
//   - Approving the item again while this server keeps running retries ONLY the
//     status write (FinishReviewItemApply with the kept token). The apply does not
//     run again.
//   - After a restart the token is gone and the claim is dead. Approving the item
//     again (as the same action) takes the claim over and RE-RUNS the apply. The
//     registered handlers tolerate that: ApplyMultidisc and ApplyDuplicateOf skip
//     members that are already merged away (presentMembers) and no-op when too few
//     remain, and ApplyVersionGroup reuses the group the members already share. The
//     one thing a re-run does not redo is multidisc disc/track numbering that was
//     left incomplete (see ApplyMultidisc).
//   - Reject is refused (REVIEW_ALREADY_APPLIED): the work happened.
type appliedUnrecordedError struct{ err error }

func (e *appliedUnrecordedError) Error() string {
	return "applied, but the result could not be recorded (" + e.err.Error() + "); approve it again to record it — " +
		"the apply is not repeated while this server keeps running"
}
func (e *appliedUnrecordedError) Unwrap() error { return e.err }

// GetReviewCount handles GET /api/v1/review/count.
//
// Response: { "count": N, "by_kind": { "<kind>": n, ... }, "applying": M }. count and
// the by_kind breakdown cover PENDING items only (decision #1: the badge counts
// intentional holds awaiting a decision, never decided items). applying counts items
// held by an apply claim: running now, applied but not yet recorded, or interrupted
// by a restart. It is separate so the badge keeps its meaning, and it exists so an
// item stuck mid-apply is visible somewhere — it is in no pending list. List them
// with GET /review/items?status=applying.
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
	applying, err := h.store.CountReviewItems(database.ReviewStatusApplying)
	if err != nil {
		httputil.InternalError(c, "failed to count applying review items", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"count": count, "by_kind": byKind, "applying": applying})
}

// ListReviewItems handles GET /api/v1/review/items.
//
// Query params: status (default "pending"), kind, q, limit (default 50), offset.
//
// `q` is a case-insensitive substring narrowing, applied by the store BEFORE the
// total is taken. That is the whole point of it: the regroup lane caps its fetch
// at 500 rows and used to search only what it had loaded, so on the production
// queue measured 2026-09-01 -- 714 pending regroup.ambiguous holds -- 214 of them
// could not be found by typing, with or without the kind filter set. An absent or
// blank `q` is exactly the previous behaviour.
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
	limit, ok := parseReviewListLimit(c)
	if !ok {
		return
	}
	offset, ok := parseReviewListOffset(c)
	if !ok {
		return
	}
	filter := database.ReviewFilter{
		Status: status,
		Kind:   c.Query("kind"),
		Search: c.Query("q"),
		Limit:  limit,
		Offset: offset,
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
	if h.refuseWithoutClaimer(c) {
		return
	}
	var req approveRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}

	id := c.Param("id")
	updated, chosen, note, err := h.approveOne(c.Request.Context(), id, strings.TrimSpace(req.Action), h.applyGloballyEnabled())
	if rej, ok := errors.AsType[*actionRejection](err); ok {
		httputil.RespondWithError(c, rej.status, rej.msg, rej.code)
		return
	}
	if unrec, ok := errors.AsType[*appliedUnrecordedError](err); ok {
		httputil.RespondWithError(c, http.StatusInternalServerError, unrec.Error(), "REVIEW_APPLIED_UNRECORDED")
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
	// chosen_action is echoed so the caller can see WHICH action ran — with an empty
	// body that is the recommendation, which the caller may not have read.
	resp := gin.H{"item": updated, "chosen_action": chosen}
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
// genuine fault. applyOn is the apply switch as read ONCE by the caller, so a bulk
// batch and its per-item results can never disagree about it.
//
// Every write goes through h.claimer, and the STORE decides what an item's state
// allows (database.ReviewItemApplyClaimer): an applied item, or one under an apply
// claim held by this process, is refused with 409; a claim a previous process left is
// taken over by an approve of its own action and the apply re-runs. This function
// does not pre-judge the status it read, because that read is already stale by the
// time a write lands — pre-refusing "applying" here is what once made a crashed
// claim impossible to recover.
func (h *Handler) approveOne(ctx context.Context, id, requested string, applyOn bool) (*database.ReviewItem, string, string, error) {
	if h.claimer == nil {
		return nil, "", "", errNoClaimer
	}
	item, err := h.store.GetReviewItem(id)
	if err != nil {
		return nil, "", "", err
	}
	if item == nil {
		return nil, "", "", nil
	}
	// An apply that ran in THIS process but could not be recorded: record it now and
	// never run it again. The work happened as the claim's action, whatever this
	// request asks for.
	if item.Status == database.ReviewStatusApplying {
		if token, ok := h.unrecordedToken(id); ok {
			return h.finishUnrecorded(id, token, item.ApplyClaimAction)
		}
	}

	// The default is the hold's EFFECTIVE action, not the raw payload recommendation:
	// re-approving an already-decided hold with an empty body must not silently revert
	// a human's recorded override back to the machine's suggestion. Re-approve is a
	// supported flow — it is how a hold approved while apply was off gets executed now
	// that POST /review/replay-approved is retired (01-P81j).
	// Pending holds carry no ChosenAction, so for them this is the recommendation.
	//
	// An item under an apply claim means the claim's action: re-approving an item whose
	// apply was interrupted by a restart must re-run what was running, not whatever
	// the payload recommends.
	chosen := requested
	if chosen == "" {
		chosen = currentActionFor(*item)
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

	// (3) duplicate-of DOES have an apply path (maintenance.ApplyDuplicateOf). It was
	// refused with a 501 until 2026-08-19 on the grounds that deciding "this folder
	// duplicates an existing book" needs cross-book identity evidence the regroup
	// classifier never gathers. That was true about the CLASSIFIER and wrong about the
	// APPLY: the dedup track already records exactly that relationship, so the apply
	// reads the survivor from its candidate rows and merges through CombineBooks —
	// the same call combine and the duplicates UI use. It refuses (returning an error,
	// so the hold lands in "failed" with a reason) when the dedup track names zero or
	// more than one candidate survivor, rather than guessing one.
	//
	// The generic no-handler path below still covers the case where the handler could
	// not be registered — e.g. a non-Pebble store with no embedding store.

	// (4) 🔴 EVERY TRANSITION BELOW WRITES THE CHOSEN ACTION ALONGSIDE THE STATUS.
	//
	// This is what makes an override durable, and it is the reason the 409
	// REVIEW_OVERRIDE_NOT_PERSISTABLE refusal that used to sit here is gone. Before
	// SetReviewItemDecision existed the chosen action was stored NOWHERE, so with
	// review_apply_enabled off — production's setting — a reviewer could not record a
	// disagreement at all: approving a `combine` hold as `separate` wrote plain
	// "approved", and the later apply re-resolved the action from the payload and
	// would merge the books the human said to keep apart. Refusing the override was the
	// only safe answer then, but it also meant owner item 2 did not actually ship.
	//
	// Now the choice is persisted atomically with the status and the later re-approve
	// prefers it (effectiveActionFor), so the override survives the switch being
	// flipped later.

	// (5) separate needs NO apply handler and never will. Every member is already its
	// own book, so "separate into N" is a statement that the current on-disk state is
	// correct — there is nothing to execute, only a decision to record. The dedup-key
	// idempotency in UpsertReviewItem keeps it decided across re-scans, which is the
	// whole mechanism: a re-scan skips a non-pending hold. Recording the action still
	// matters: it is what stops a later re-approve from falling back to a `combine` payload.
	if chosen == itunesservice.ActionSeparate {
		updated, serr := h.claimer.DecideReviewItem(id, database.ReviewStatusApproved, chosen)
		if conflict, ok := errors.AsType[*database.ReviewClaimConflictError](serr); ok {
			return nil, chosen, "", conflictRejection(conflict)
		}
		if serr != nil {
			return nil, "", "", serr
		}
		return updated, chosen, "action 'separate' needs no apply step — every member is already its own book; " +
			"recorded as approved so re-scans leave it alone", nil
	}

	fn, hasHandler := h.applyHandlerFor(chosen)
	if hasHandler && applyOn {
		return h.applyUnderClaim(ctx, item, chosen, fn)
	}
	// Either no apply handler for this action, OR the global apply switch is OFF
	// (review-only mode). Record the decision as "approved" but do NOT execute — the
	// item stays visible/reviewable and nothing is merged. The action is recorded even
	// though nothing ran, which is precisely the case a later re-approve reads back.
	updated, err := h.claimer.DecideReviewItem(id, database.ReviewStatusApproved, chosen)
	if conflict, ok := errors.AsType[*database.ReviewClaimConflictError](err); ok {
		return nil, chosen, "", conflictRejection(conflict)
	}
	if err != nil {
		return nil, "", "", err
	}
	if updated == nil {
		return nil, "", "", nil
	}
	note := "no apply handler registered for action " + chosen + "; marked approved"
	if hasHandler {
		note = "apply is disabled (review-only mode): item approved but NOT applied — " +
			"enable review_apply_enabled and approve it again to perform the merge"
	}
	return updated, chosen, note, nil
}

// errNoClaimer is approveOne's and rejectOne's answer when the store has no
// apply-claim capability. The HTTP handlers refuse with REVIEW_CLAIM_UNAVAILABLE
// before calling either, so this is a backstop.
var errNoClaimer = errors.New("review store has no apply-claim capability; refusing to write")

// finishAttempts is how many times a successful apply's "applied" write is tried
// before the item is reported applied-but-unrecorded, and finishBackoff the pause
// before each retry.
const finishAttempts = 3

var finishBackoff = []time.Duration{0, 50 * time.Millisecond, 250 * time.Millisecond}

// applyUnderClaim claims item for chosen, runs fn on the claimed record, and records
// the outcome. The claim is a compare-and-set on the record approveOne read
// (item.UpdatedAt), so the action resolved from that read is still what the item
// means when fn runs: a re-scan refreshing the payload, or a decision landing in
// between, refuses the claim with 409 REVIEW_ITEM_CHANGED instead of applying a stale
// reading.
//
// Every exit leaves the claim finished or released, except appliedUnrecordedError,
// which keeps it on purpose. A panicking fn releases it too: a claim from this process
// is never taken over, so a leaked one would hold the item until a restart.
func (h *Handler) applyUnderClaim(ctx context.Context, item *database.ReviewItem, chosen string, fn ApplyFunc) (
	*database.ReviewItem, string, string, error) {
	id := item.ID
	claim, err := h.claimer.ClaimReviewItemForApply(id, chosen, item.UpdatedAt)
	if conflict, ok := errors.AsType[*database.ReviewClaimConflictError](err); ok {
		return nil, chosen, "", conflictRejection(conflict)
	}
	if err != nil {
		return nil, "", "", err
	}
	if claim == nil {
		return nil, "", "", nil
	}

	settled := false
	defer func() {
		if settled {
			return
		}
		r := recover()
		if relErr := h.releaseClaim(id, claim.Token); relErr != nil {
			reviewLog.Error("review apply of %s panicked and its claim could not be released: %v", id, relErr)
		}
		if r != nil {
			panic(r)
		}
	}()
	applyErr := fn(ctx, *claim.Item)
	if applyErr != nil {
		settled = true
		reason := applyErr
		// Nothing was applied: hand the claim back so the item is retryable.
		if relErr := h.releaseClaim(id, claim.Token); relErr != nil {
			reason = fmt.Errorf("%w; and its apply claim could not be released, so the item stays "+
				"\"applying\" until the server restarts: %v", applyErr, relErr)
		}
		// A refusal because a member has a file under the active iTunes library is
		// 409, not a 500: the item keeps its status and bulk approve skips it
		// instead of aborting.
		if errors.Is(applyErr, merge.ErrITunesProtected) {
			return nil, chosen, "", rejectf(http.StatusConflict, "REVIEW_ITUNES_PROTECTED", reason.Error())
		}
		return nil, "", "", reason
	}
	settled = true

	// The handler ran. Recording that is retried; if it still fails the claim stays
	// (blocking a second apply) and its token is kept so a re-approve can finish it.
	var updated *database.ReviewItem
	for attempt := range finishAttempts {
		if d := finishBackoff[attempt]; d > 0 {
			time.Sleep(d)
		}
		updated, err = h.claimer.FinishReviewItemApply(id, claim.Token)
		if err == nil {
			break
		}
		if _, ok := errors.AsType[*database.ReviewClaimConflictError](err); ok {
			break // the claim is not ours any more; retrying cannot help
		}
	}
	if err != nil || updated == nil {
		if err == nil {
			err = errors.New("the item disappeared before its result was recorded")
		}
		h.setUnrecorded(id, claim.Token)
		reviewLog.Error("review apply of %s (%s) succeeded but could not be recorded: %v", id, chosen, err)
		return nil, chosen, "", &appliedUnrecordedError{err: err}
	}
	note := ""
	if claim.TookOver {
		note = "this finished an apply that a server restart had interrupted; the apply was run again " +
			"and skipped any work already done"
	}
	return updated, chosen, note, nil
}

// releaseClaim hands a claim back after its apply failed, logging a failure. A false
// release (the token no longer holds the claim) is logged too: nothing else should be
// able to take a live claim, so it means something is wrong.
func (h *Handler) releaseClaim(id, token string) error {
	released, err := h.claimer.ReleaseReviewItemClaim(id, token)
	if err != nil {
		reviewLog.Error("review item %s: release apply claim: %v", id, err)
		return err
	}
	if !released {
		reviewLog.Warn("review item %s: release apply claim: the claim was no longer held by this apply", id)
	}
	return nil
}

// finishUnrecorded records an apply that already succeeded in this process but whose
// "applied" write failed earlier (see appliedUnrecordedError). It never runs the apply.
func (h *Handler) finishUnrecorded(id, token, action string) (*database.ReviewItem, string, string, error) {
	updated, err := h.claimer.FinishReviewItemApply(id, token)
	if err != nil || updated == nil {
		if err == nil {
			err = errors.New("the item disappeared before its result was recorded")
		}
		if _, ok := errors.AsType[*database.ReviewClaimConflictError](err); ok {
			h.setUnrecorded(id, "") // the claim is gone; the token can never finish it
		}
		return nil, action, "", &appliedUnrecordedError{err: err}
	}
	h.setUnrecorded(id, "")
	return updated, action, "the apply had already run; its result is recorded now (it was not run again)", nil
}

// currentActionFor is the action an approve with no explicit choice resolves to: the
// claim's action for an item under an apply claim (only that action may finish an
// interrupted apply), else the hold's effective action.
func currentActionFor(item database.ReviewItem) string {
	if item.Status == database.ReviewStatusApplying && item.ApplyClaimAction != "" {
		return item.ApplyClaimAction
	}
	return effectiveActionFor(item)
}

// conflictRejection turns a store refusal into the 409 a caller acts on.
func conflictRejection(c *database.ReviewClaimConflictError) *actionRejection {
	switch c.Reason {
	case database.ReviewClaimApplied:
		return rejectf(http.StatusConflict, "REVIEW_ALREADY_APPLIED", "already applied")
	case database.ReviewClaimInterrupted:
		return rejectf(http.StatusConflict, "REVIEW_APPLY_INTERRUPTED",
			"an apply of "+strconv.Quote(c.ClaimAction)+" was interrupted by a server restart; approve it again as "+
				strconv.Quote(c.ClaimAction)+" to finish it (the apply runs again and skips work already done). "+
				"No other decision can be recorded until then, because the interrupted apply may already have changed the library")
	case database.ReviewClaimChanged:
		return rejectf(http.StatusConflict, "REVIEW_ITEM_CHANGED",
			"the item changed while it was being approved; reload it and approve again")
	}
	return rejectf(http.StatusConflict, "REVIEW_APPLY_IN_PROGRESS", "an apply is in progress for this item")
}

// rejectOne rejects one item through the store's claim-aware decision write, which
// refuses an applied item or one under an apply claim. Returns (nil, nil) when the
// item does not exist.
func (h *Handler) rejectOne(id string) (*database.ReviewItem, error) {
	if h.claimer == nil {
		return nil, errNoClaimer
	}
	if _, ok := h.unrecordedToken(id); ok {
		return nil, rejectf(http.StatusConflict, "REVIEW_ALREADY_APPLIED",
			"already applied (its result is not recorded yet; approve it again to record it)")
	}
	updated, err := h.claimer.DecideReviewItem(id, database.ReviewStatusRejected, "")
	if conflict, ok := errors.AsType[*database.ReviewClaimConflictError](err); ok {
		return nil, conflictRejection(conflict)
	}
	return updated, err
}

// RejectReviewItem handles POST /api/v1/review/items/:id/reject.
func (h *Handler) RejectReviewItem(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	if h.refuseWithoutClaimer(c) {
		return
	}
	id := c.Param("id")
	updated, err := h.rejectOne(id)
	if rej, ok := errors.AsType[*actionRejection](err); ok {
		httputil.RespondWithError(c, rej.status, rej.msg, rej.code)
		return
	}
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
	// Code is the refusal's stable code, the one a single approve or reject would
	// return (REVIEW_ACTION_NOT_DECIDABLE, REVIEW_ALREADY_APPLIED,
	// REVIEW_APPLY_IN_PROGRESS, REVIEW_APPLY_INTERRUPTED, ...). The UI groups skips by
	// it: a hold skipped for having no decidable action is still pending, one skipped
	// because it is applied or mid-apply is not.
	Code string `json:"code,omitempty"`
}

// bulkResult reports the outcome of a bulk action.
type bulkResult struct {
	Action   string     `json:"action"`
	Approved []string   `json:"approved,omitempty"`
	Applied  []string   `json:"applied,omitempty"`
	Rejected []string   `json:"rejected,omitempty"`
	NotFound []string   `json:"not_found,omitempty"`
	Skipped  []bulkSkip `json:"skipped,omitempty"`
	// Failed lists items whose apply (or status write) errored BEFORE the apply ran
	// to completion. The item keeps the status it had, so an approved item stays
	// approved and a retry is another bulk approve naming its id.
	Failed []bulkFail `json:"failed,omitempty"`
	// AppliedUnrecorded lists items whose apply SUCCEEDED but whose "applied" status
	// write failed. The work is done and the item is held "applying" under this
	// server's claim. Approving it again records the result without re-running the
	// apply while this server keeps running; after a restart, approving it again
	// re-runs the apply, which the apply handlers tolerate (appliedUnrecordedError).
	AppliedUnrecorded []bulkFail `json:"applied_unrecorded,omitempty"`
	// ApplyDisabled is true on an approve while the global apply switch is off:
	// approvals were recorded but nothing was applied, so an empty `applied` is not
	// a result. Enable review_apply_enabled and approve the ids again.
	ApplyDisabled bool `json:"apply_disabled,omitempty"`
	Processed     int  `json:"processed"`
}

// bulkFail is one item a bulk action could not complete, with the cause a
// reviewer needs (the same text the retired replay-approved route reported).
type bulkFail struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// BulkReviewAction handles POST /api/v1/review/bulk.
//
// Targets are the explicit IDs when provided, otherwise every PENDING item of
// the given Kind. Volume is bounded (v1 producer holds only), so this runs
// sequentially; a worker pool would be premature until real apply handlers land.
//
// Approving an id that is already approved executes it (once the apply switch is
// on) with the action a human recorded; this is how holds approved while apply was
// off get carried out now that POST /review/replay-approved is retired.
func (h *Handler) BulkReviewAction(c *gin.Context) {
	if h.store == nil {
		httputil.RespondWithServiceUnavailable(c, "review store not available")
		return
	}
	if h.refuseWithoutClaimer(c) {
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

	// Fail-safe cap (internal/applycap) on the MATERIALIZED target list — a
	// kind-scoped request resolves to "every pending hold of that kind", which
	// is exactly the "whole list" shape the cap exists to refuse. Both actions
	// are capped: an accidental bulk reject of a whole queue is as much a
	// cleanup as an accidental bulk approve.
	if h.refuseIfOverCap(c, "review/bulk "+req.Action, len(ids)) {
		return
	}

	// Per-item faults (an apply handler error, a status write error) go in
	// result.Failed and the batch continues: stopping at the first one left earlier
	// items applied but unreported and later ones untouched. A 500 is kept only for
	// request-level failures (the store listing above).
	result := bulkResult{Action: req.Action}
	applyOn := h.applyGloballyEnabled() // read once: flag and per-item results agree
	result.ApplyDisabled = req.Action == "approve" && !applyOn
	for _, id := range ids {
		switch req.Action {
		case "approve":
			// "" = use this item's OWN recommendation. See bulkRequest's comment for
			// why there is no batch-wide action.
			updated, chosen, _, err := h.approveOne(c.Request.Context(), id, "", applyOn)
			// A per-item refusal (no decidable action, unimplemented action) SKIPS that
			// item and the batch continues. Aborting the whole batch on one
			// insufficient-evidence hold would make bulk approve useless on a queue
			// where 70 of 356 holds are exactly that.
			if rej, ok := errors.AsType[*actionRejection](err); ok {
				result.Skipped = append(result.Skipped, bulkSkip{ID: id, Action: chosen, Reason: rej.msg, Code: rej.code})
				continue
			}
			if unrec, ok := errors.AsType[*appliedUnrecordedError](err); ok {
				result.AppliedUnrecorded = append(result.AppliedUnrecorded, bulkFail{ID: id, Reason: unrec.Error()})
				continue
			}
			if err != nil {
				result.Failed = append(result.Failed, bulkFail{ID: id, Reason: err.Error()})
				continue
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
			updated, err := h.rejectOne(id)
			if rej, ok := errors.AsType[*actionRejection](err); ok {
				result.Skipped = append(result.Skipped, bulkSkip{ID: id, Reason: rej.msg, Code: rej.code})
				continue
			}
			if err != nil {
				result.Failed = append(result.Failed, bulkFail{ID: id, Reason: err.Error()})
				continue
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

// Bounds on GET /review/items?limit=. The default is what the handler always
// used; the ceiling is twice what the largest UI caller (the regroup lane, 500)
// asks for, and enough for an operator's curl. Anything outside is refused with
// a 400 and a stable code rather than silently rewritten.
const (
	defaultReviewListLimit = 50
	maxReviewListLimit     = 1000
	reviewListLimitCode    = "REVIEW_LIST_LIMIT_INVALID"
	reviewListOffsetCode   = "REVIEW_LIST_OFFSET_INVALID"
)

// parseReviewListLimit reads ?limit= and refuses anything the store would have
// silently reinterpreted. It returns ok=false after writing the 400.
//
// 🔴 WHY REFUSE INSTEAD OF CLAMP. The store treats Limit<=0 as "default page of
// 50". A client that sends limit=0 meaning "everything" therefore gets 50 rows,
// a total that says otherwise, and no error — the replay-approved handler that
// lived in this package until 01-P81j made exactly that mistake and reported a
// 300-hold queue as 50. A non-numeric limit used to fall to the default the
// same silent way. Refusing at the edge turns the misunderstanding into a message the caller sees on the
// first request instead of a page that looks complete and is not.
func parseReviewListLimit(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return defaultReviewListLimit, true
	}
	n, err := strconv.Atoi(raw)
	switch {
	case err != nil:
		httputil.RespondWithError(c, http.StatusBadRequest,
			fmt.Sprintf("limit %q is not an integer", raw), reviewListLimitCode)
		return 0, false
	case n <= 0:
		httputil.RespondWithError(c, http.StatusBadRequest,
			fmt.Sprintf("limit must be between 1 and %d; %d is not a request for every item — "+
				"page with offset instead", maxReviewListLimit, n),
			reviewListLimitCode)
		return 0, false
	case n > maxReviewListLimit:
		httputil.RespondWithError(c, http.StatusBadRequest,
			fmt.Sprintf("limit must be between 1 and %d, got %d — page with offset instead", maxReviewListLimit, n),
			reviewListLimitCode)
		return 0, false
	}
	return n, true
}

// parseReviewListOffset reads ?offset= under the same rule as the limit: an
// absent offset is page one, anything else must be a non-negative integer, and
// a value the store would have silently reinterpreted is refused with a 400
// instead. The store clamps a negative offset to 0, so offset=-50 used to
// return the first page with a body that echoed "offset": -50 — a paging client
// that computed the next offset from that echo walked the same page forever.
// A non-numeric offset used to fall to 0 the same silent way.
func parseReviewListOffset(c *gin.Context) (int, bool) {
	raw := c.Query("offset")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	switch {
	case err != nil:
		httputil.RespondWithError(c, http.StatusBadRequest,
			fmt.Sprintf("offset %q is not an integer", raw), reviewListOffsetCode)
		return 0, false
	case n < 0:
		httputil.RespondWithError(c, http.StatusBadRequest,
			fmt.Sprintf("offset must be 0 or greater, got %d", n), reviewListOffsetCode)
		return 0, false
	}
	return n, true
}
