// file: internal/server/handlers/repairs/handler.go
// version: 1.14.0
// guid: 1d8e4c73-5a26-4b9f-8e03-7c2b9f6a1d58
// last-edited: 2026-10-26

// Package repairs serves the Repairs lane of /review (/api/v1/repairs/*):
// list the fixers, start a plan, page a stored plan's rows, and start an
// apply of chosen rows. Plans and applies run as operations (repairs.plan /
// repairs.apply); these handlers only enqueue them and read their stored
// results. Everything that makes an apply safe (guards, fingerprint check,
// history, scan stand-down) lives in internal/repairs and the ops, not here.
//
// Status codes: 202 with the operation id for plan/apply; 404 for an unknown
// fixer or plan op; 409 for a plan op that has not completed or an apply of a
// fixer that only previews (repairs.PlanOnly); 400 for a plan
// op that is not a plan of this fixer, a bad page request or an empty row
// selection; 503 without an operations registry.
//
// POST /repairs/:fixer/owner-apply is the owner's own apply of one row the
// plan lists for him alone (Row.OwnerApplicable; repairs/owner.go). It is
// honoured only for a verified Cloudflare Access sign-in as owner_email
// (auth.OwnerProofWhyNot, owner decision 2026-10-07; never a password, OAuth,
// temp-login or invite session, an API key or an ABS token) holding the
// admin role, and
// only for a same-origin request carrying the X-Repairs-Owner-Apply header
// (Sec-Fetch-Site same-origin, or without it an Origin naming this host; a
// request showing neither is refused) (CSRF: a custom header cannot be sent cross-site without a CORS preflight
// the server never grants, and the session cookie is SameSite=Strict). It
// mints a one-shot grant and enqueues repairs.apply naming it; 403 for any
// other caller, nothing enqueued. GET /repairs/owner-status says whether the
// caller passes the owner proof, and why not, so the page can say it first.
//
// Per-fixer permission (owner decision 2026-10-14): the routes are gated by
// library.edit_metadata; starting a fixer's plan, apply or owner-apply also
// requires every permission repairs.RequiredPermissions(fixer) names (a
// repairs.PermissionScoped fixer can ask for more, e.g. settings.manage).
// 401/403 as the route middleware answers, nothing enqueued.
//
// Reading follows the same per-fixer permission (security fix 2026-10-24):
// plan rows carry other users' data (the master-list import's target and
// uploader ids, the Audible read-status fixer's reading state), so GET
// /repairs/:fixer/plan/:op_id/rows needs every permission
// repairs.RequiredPermissions(fixer) names, not just the route's
// library.view, and answers 401/403 before it reads anything. GET /repairs
// lists every fixer but leaves the last plan/apply pointers (op ids and the
// counts in their summaries) off an entry the caller may not use, and marks it
// "permitted": false so the lane can say so. The route-level library.view
// still lets a caller see that a fixer exists.
package repairs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// Page size bounds of the rows endpoint.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// Enqueuer starts an operation.
type Enqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// OpStore reads stored operation rows and the lane's last-run pointers.
type OpStore interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// Handler serves the repairs routes.
type Handler struct {
	fixers   *repairs.Registry
	enqueuer Enqueuer // nil when the registry is not initialised
	ops      OpStore
	// grants is where owner grants are minted; nil is the process's
	// repairs.DefaultOwnerGrants (the store repairs.apply takes from).
	grants *repairs.OwnerGrants
	// ownerEmail returns the configured owner_email, read per request so a
	// change takes effect without a restart.
	ownerEmail func() string
	// permCheck reports whether the caller holds a permission (PermissionCheck).
	permCheck PermissionCheck
	// access decides whether the caller may read other users' text; nil holds
	// nothing, so an op's error text that names a user is withheld (opRef).
	access *opaccess.Repairs
}

// PermissionCheck reports whether the request's caller holds p. It writes
// nothing: the handler answers a refusal itself (fixerPermitted), and GET
// /repairs asks it without refusing anything.
type PermissionCheck func(c *gin.Context, p auth.Permission) bool

// New builds the handler. enqueuer may be nil: plan/apply then answer 503.
// The permission check defaults to the strict one (StrictPermissionCheck);
// the server installs its own with WithPermissionCheck.
func New(fixers *repairs.Registry, enqueuer Enqueuer, ops OpStore) *Handler {
	return &Handler{fixers: fixers, enqueuer: enqueuer, ops: ops,
		ownerEmail: func() string { return config.Snapshot().OwnerEmail },
		permCheck:  StrictPermissionCheck}
}

// WithPermissionCheck sets how the per-fixer permissions are checked: the
// server passes the same semantics as its route middleware (auth disabled,
// first-run bypass). nil keeps the strict check.
func (h *Handler) WithPermissionCheck(check PermissionCheck) *Handler {
	if check != nil {
		h.permCheck = check
	}
	return h
}

// WithOpAccess sets the check that withholds an op's error text when it names
// a user, for a caller without users.manage (opaccess.Reader.UserText). nil
// keeps the fail-closed default: such text is always withheld.
func (h *Handler) WithOpAccess(a *opaccess.Repairs) *Handler {
	h.access = a
	return h
}

// StrictPermissionCheck is servermiddleware.HoldsPermission with no
// first-run bypass: the caller must hold p. It fails closed for a handler the
// server did not wire.
func StrictPermissionCheck(c *gin.Context, p auth.Permission) bool {
	return servermiddleware.HoldsPermission(nil, c, p)
}

// fixerHeld reports whether the caller holds every permission f requires
// (repairs.RequiredPermissions) and, when not, the first one it lacks. It
// writes nothing.
func (h *Handler) fixerHeld(c *gin.Context, f repairs.Fixer) (missing auth.Permission, ok bool) {
	check := h.permCheck
	if check == nil {
		check = StrictPermissionCheck
	}
	for _, p := range repairs.RequiredPermissions(f) {
		if !check(c, p) {
			return p, false
		}
	}
	return "", true
}

// fixerPermitted checks every permission f requires
// (repairs.RequiredPermissions); false means the 401/403 is written.
func (h *Handler) fixerPermitted(c *gin.Context, f repairs.Fixer) bool {
	missing, ok := h.fixerHeld(c, f)
	if !ok {
		servermiddleware.RespondPermissionDenied(c, missing)
	}
	return ok
}

// ownerWhyNot is why the request is not the owner ("" when it is): a
// verified Cloudflare Access sign-in as owner_email (auth.OwnerProofWhyNot).
func (h *Handler) ownerWhyNot(c *gin.Context) string {
	email := ""
	if h.ownerEmail != nil {
		email = h.ownerEmail()
	}
	return auth.OwnerProofWhyNot(c.Request.Context(), email, servermiddleware.OwnerPublicHost(c.Request))
}

// OwnerStatus is GET /repairs/owner-status: whether this request may apply
// owner rows, and if not, why, so the Repairs page can say so before a click.
type OwnerStatus struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// GetOwnerStatus implements GET /repairs/owner-status.
func (h *Handler) GetOwnerStatus(c *gin.Context) {
	why := h.ownerWhyNot(c)
	httputil.RespondWithOK(c, OwnerStatus{Allowed: why == "", Reason: why})
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
	// PlanOnly: the fixer only previews (repairs.PlanOnly). Its apply routes
	// answer 409 and the lane shows no Apply control for it.
	PlanOnly bool `json:"plan_only,omitempty"`
	// Permitted: the caller holds every permission the fixer requires
	// (repairs.RequiredPermissions), so may read its plan rows and start its
	// plan or apply. When false, LastPlan and LastApply are left out: they carry
	// operation ids that open the fixer's plan rows and counts that describe them.
	Permitted bool `json:"permitted"`
}

// StartedResponse answers plan/apply.
type StartedResponse struct {
	OperationID string `json:"operation_id"`
	DefID       string `json:"def_id"`
	FixerID     string `json:"fixer_id"`
	Status      string `json:"status"`
	// Deduped: an identical request (same fixer, params and rows) was
	// already queued or running, and OperationID is that run.
	Deduped bool `json:"deduped,omitempty"`
}

// ApplyRequest is the body of POST /repairs/:fixer/apply.
type ApplyRequest struct {
	PlanOpID string   `json:"plan_op_id"`
	RowIDs   []string `json:"row_ids"`
	// DryRun: only an explicit false writes (owner rule 2026-09-25: an
	// omitted mode is a preview). The lane's Apply button sends false; true
	// or omitted re-checks the rows and reports would_apply per row.
	DryRun *bool `json:"dry_run,omitempty"`
	// OwnerApplyRowIDs is refused here: owner rows are applied one at a
	// time through POST /repairs/:fixer/owner-apply (OwnerApply).
	OwnerApplyRowIDs []string `json:"owner_apply_row_ids,omitempty"`
}

// OwnerApplyRequest is the body of POST /repairs/:fixer/owner-apply.
type OwnerApplyRequest struct {
	PlanOpID string `json:"plan_op_id"`
	RowID    string `json:"row_id"`
}

// OwnerApplyHeader must be sent (value "1") with an owner apply: a custom
// header no cross-site form or simple request can carry.
const OwnerApplyHeader = "X-Repairs-Owner-Apply"

// ListFixers implements GET /repairs.
func (h *Handler) ListFixers(c *gin.Context) {
	fixers := h.fixers.List()
	out := make([]FixerInfo, 0, len(fixers))
	for _, f := range fixers {
		_, held := h.fixerHeld(c, f)
		info := FixerInfo{ID: f.ID(), Title: f.Title(), Description: f.Description(),
			PlanOnly: repairs.IsPlanOnly(f), Permitted: held}
		// Only for a caller who may read the fixer's rows: the pointers name the
		// plan op whose rows ListPlanRows serves, and their summaries count them.
		if info.Permitted {
			info.LastPlan = h.lastRun(c, lastPlanKey(f.ID()))
			info.LastApply = h.lastRun(c, lastApplyKey(f.ID()))
		}
		out = append(out, info)
	}
	httputil.RespondWithOK(c, gin.H{"fixers": out})
}

// Each fixer's newest plan and apply started from this lane are recorded
// under a settings key when they are enqueued, so GET /repairs is two point
// reads per fixer. (Scanning the op history instead would decode every op
// row and its result blob on every tab open.) A run started some other way,
// e.g. POST /operations/v2, is not recorded here.
func lastPlanKey(fixerID string) string  { return "repairs_last_plan_op:" + fixerID }
func lastApplyKey(fixerID string) string { return "repairs_last_apply_op:" + fixerID }

// lastRun resolves a recorded op id. Best effort: a missing setting or op
// row leaves it unset.
func (h *Handler) lastRun(c *gin.Context, key string) *OpRef {
	if h.ops == nil {
		return nil
	}
	st, err := h.ops.GetSetting(key)
	if err != nil || st == nil || st.Value == "" {
		return nil
	}
	row, err := h.ops.GetOperationV2(st.Value)
	if err != nil || row == nil {
		return nil
	}
	return opRef(row, h.access.For(c).UserText)
}

// opRef summarises a stored op. Its error text goes through userText, which
// withholds it when it names another user (opaccess.Reader.UserText): an op's
// ErrorMessage is free text and a Repairs op can put an account id in it.
func opRef(r *database.OperationV2Row, userText func(string) string) *OpRef {
	ref := &OpRef{OperationID: r.ID, Status: r.Status, QueuedAt: r.QueuedAt, CompletedAt: r.CompletedAt}
	if r.ErrorMessage != nil {
		ref.Error = userText(*r.ErrorMessage)
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
	if !h.fixerPermitted(c, f) {
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
	h.enqueue(c, repairs.PlanOpID, f.ID(), lastPlanKey(f.ID()), repairs.PlanParams{FixerID: f.ID(), Params: params})
}

// ListPlanRows implements GET /repairs/:fixer/plan/:op_id/rows
// ?offset=&limit=&filter=applicable|skipped|skipped:<kind>&class=<row class>.
// The caller must hold every permission the fixer requires (fixerPermitted,
// as the plan and apply routes do), checked before anything is read; the plan
// must be a completed plan of this fixer (loadPlan), so another fixer's op id
// cannot be read under a fixer the caller may use.
func (h *Handler) ListPlanRows(c *gin.Context) {
	f, ok := h.fixer(c)
	if !ok {
		return
	}
	if !h.fixerPermitted(c, f) {
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
	page, err := plan.Page(opID, c.Query("filter"), c.Query("class"), offset, limit)
	switch {
	case err == nil:
	case errors.Is(err, repairs.ErrUnknownFilter):
		httputil.RespondWithValidationError(c, "filter", err.Error())
		return
	case repairs.PlanNeedsRerun(err):
		respondPlanNeedsRerun(c, err)
		return
	default:
		httputil.InternalError(c, "failed to read repair plan rows", err)
		return
	}
	httputil.RespondWithOK(c, page)
}

// PlanNeedsRerunCode is the error code of the 409 answered for a stored plan
// that cannot be read any more (legacy format, rows that do not match its
// summary, or a row that does not decode): the only remedy is to run the plan
// again. The Repairs lane reads it.
const PlanNeedsRerunCode = "REPAIR_PLAN_NEEDS_RERUN"

// respondPlanNeedsRerun answers err (repairs.PlanNeedsRerun(err) is true) with
// a 409 carrying PlanNeedsRerunCode. It is a conflict with the state of that
// plan, not a server fault; the code lets the lane offer "Run plan again"
// instead of a Retry that re-reads the same plan.
func respondPlanNeedsRerun(c *gin.Context, err error) {
	httputil.RespondWithError(c, http.StatusConflict, err.Error(), PlanNeedsRerunCode)
}

// planOnlyMessage is the 409 body of an apply of a fixer that only previews.
func planOnlyMessage(f repairs.Fixer) string {
	return "repair fixer " + f.ID() + " only previews; " + repairs.ErrPlanOnly.Error()
}

// StartApply implements POST /repairs/:fixer/apply {plan_op_id, row_ids}.
func (h *Handler) StartApply(c *gin.Context) {
	f, ok := h.fixer(c)
	if !ok {
		return
	}
	if !h.fixerPermitted(c, f) {
		return
	}
	if repairs.IsPlanOnly(f) {
		httputil.RespondWithConflict(c, planOnlyMessage(f))
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
	if len(req.OwnerApplyRowIDs) > 0 {
		// Never honoured on the bulk endpoint, whoever asks: an API key is
		// refused outright, a person is pointed at the per-row endpoint.
		if why := h.ownerWhyNot(c); why != "" {
			httputil.RespondWithForbidden(c, why)
			return
		}
		httputil.RespondWithBadRequest(c, "owner rows are applied one at a time: POST /repairs/:fixer/owner-apply {plan_op_id, row_id}")
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
	h.enqueue(c, repairs.ApplyOpID, f.ID(), lastApplyKey(f.ID()), repairs.ApplyParams{
		FixerID: f.ID(), PlanOpID: req.PlanOpID, RowIDs: req.RowIDs, DryRun: &dry,
	})
}

// OwnerApply implements POST /repairs/:fixer/owner-apply {plan_op_id,
// row_id}: the owner's own apply of one owner-applicable row (see the
// package comment for who may call it). The write is implied: the confirm
// dialog in Repairs is the preview.
func (h *Handler) OwnerApply(c *gin.Context) {
	f, ok := h.fixer(c)
	if !ok {
		return
	}
	if !h.fixerPermitted(c, f) {
		return
	}
	if repairs.IsPlanOnly(f) {
		httputil.RespondWithConflict(c, planOnlyMessage(f))
		return
	}
	ctx := c.Request.Context()
	method := auth.MethodFromContext(ctx)
	user, hasUser := auth.UserFromContext(ctx)
	// The owner proof: a verified Cloudflare Access JWT for owner_email. It
	// is checked before anything else, and the grant minted below records
	// the Access email; repairs.OwnerGrants.Issue refuses any other kind of
	// grant, so the exceptions an owner grant unlocks (the owner row and its
	// OwnerITunesDatabaseOnly books) are reachable only from here.
	switch why := h.ownerWhyNot(c); {
	case why != "":
		httputil.RespondWithForbidden(c, why)
		return
	case !hasUser:
		httputil.RespondWithForbidden(c, "owner apply needs a signed-in user")
		return
	case !slices.Contains(user.Roles, auth.SeedRoleAdmin):
		httputil.RespondWithForbidden(c, "owner apply needs the admin role")
		return
	}
	if why := sameOriginWhyNot(c.Request); why != "" {
		httputil.RespondWithForbidden(c, why)
		return
	}
	if h.enqueuer == nil {
		httputil.RespondWithServiceUnavailable(c, "operations registry not initialized")
		return
	}
	var req OwnerApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, "invalid body: "+err.Error())
		return
	}
	if req.PlanOpID == "" {
		httputil.RespondWithValidationError(c, "plan_op_id", "required")
		return
	}
	if req.RowID == "" {
		httputil.RespondWithValidationError(c, "row_id", "required")
		return
	}
	plan, ok := h.loadPlan(c, req.PlanOpID, f.ID())
	if !ok {
		return
	}
	chosen, err := plan.Select([]string{req.RowID})
	if repairs.PlanNeedsRerun(err) {
		respondPlanNeedsRerun(c, err)
		return
	}
	if err != nil {
		httputil.InternalError(c, "failed to read repair plan row", err)
		return
	}
	if len(chosen.Rows) == 0 {
		httputil.RespondWithNotFound(c, "repair plan row", req.RowID)
		return
	}
	if row := chosen.Rows[0]; row.Applicable() || !row.OwnerApplicable {
		httputil.RespondWithBadRequest(c, "the plan does not list row "+req.RowID+" as one the owner may apply")
		return
	}
	grants := h.grants
	if grants == nil {
		grants = repairs.DefaultOwnerGrants
	}
	tok, err := grants.Issue(repairs.OwnerGrant{UserID: user.ID, AuthMethod: string(method),
		AccessEmail: auth.AccessEmailFromContext(ctx),
		FixerID:     f.ID(), PlanOpID: req.PlanOpID, RowIDs: []string{req.RowID}})
	if err != nil {
		httputil.InternalError(c, "owner grant", err)
		return
	}
	no := false
	h.enqueue(c, repairs.ApplyOpID, f.ID(), lastApplyKey(f.ID()), repairs.ApplyParams{
		FixerID: f.ID(), PlanOpID: req.PlanOpID, DryRun: &no,
		OwnerApplyRowIDs: []string{req.RowID}, OwnerGrant: tok,
	}, opsregistry.WithActor(user.ID), opsregistry.WithActorAuthMethod(string(method)),
		opsregistry.WithOwnerApply())
}

// sameOriginWhyNot is why r is not a same-origin request carrying the owner
// apply header ("" when it is). It fails closed: a request must positively
// show it came from this server's own page.
//
// Sec-Fetch-Site, when sent, decides alone, and only "same-origin" passes:
// the browser computes it from the page and request URLs, and no proxy
// rewrites it, whereas r.Host can be an internal name behind cloudflared or
// the Vite dev proxy while Origin is the public one. "same-site" (a sibling
// subdomain), "cross-site" and "none" (a user-typed navigation, never a
// fetch carrying a custom header) are refused. A client that sends no
// Sec-Fetch-Site (an older browser, or a non-browser client) must send an
// Origin naming r.Host; with neither header nothing shows where the request
// came from, and it is refused.
func sameOriginWhyNot(r *http.Request) string {
	if r.Header.Get(OwnerApplyHeader) != "1" {
		return "owner apply needs the " + OwnerApplyHeader + " header (sent by the Repairs page)"
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		if site != "same-origin" {
			return "owner apply refused: not a same-origin request (Sec-Fetch-Site " + site + ")"
		}
		return ""
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return "owner apply refused: the request shows neither Sec-Fetch-Site nor Origin, so it cannot be told to come from the Repairs page"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return "owner apply refused: origin " + origin + " is not this server"
	}
	return ""
}

func (h *Handler) loadPlan(c *gin.Context, opID, fixerID string) (*repairs.PlanResult, bool) {
	if h.ops == nil {
		httputil.RespondWithServiceUnavailable(c, "operations store not initialized")
		return nil, false
	}
	rows, err := repairs.PlanRowStoreOf(h.ops)
	if err != nil {
		httputil.RespondWithServiceUnavailable(c, err.Error())
		return nil, false
	}
	plan, err := repairs.LoadPlan(h.ops, rows, opID, fixerID)
	switch {
	case err == nil:
		return plan, true
	case errors.Is(err, repairs.ErrPlanNotFound):
		httputil.RespondWithNotFound(c, "repair plan", opID)
	case repairs.PlanNeedsRerun(err):
		// LoadPlan reads only the operation record and its summary, so what it
		// can report here is the old format, a summary that does not decode, or
		// a stored operation record that does not decode (a torn value, a time
		// that does not parse); the row-level failures surface from Page and
		// Select, which map them the same way.
		respondPlanNeedsRerun(c, err)
	case errors.Is(err, repairs.ErrPlanNotComplete):
		httputil.RespondWithConflict(c, err.Error())
	case errors.Is(err, repairs.ErrNotAPlan), errors.Is(err, repairs.ErrPlanOtherFixer):
		httputil.RespondWithBadRequest(c, err.Error())
	default:
		httputil.InternalError(c, "failed to read repair plan", err)
	}
	return nil, false
}

// enqueue starts defID. The registry returns the id of an already queued or
// running run when the request is byte-identical to it (same fixer, params
// and rows); that is reported as deduped with the run's status rather than
// as a new queued run. Otherwise the new id is recorded as the fixer's last
// plan/apply.
func (h *Handler) enqueue(c *gin.Context, defID, fixerID, lastKey string, params any, opts ...opsregistry.EnqueueOption) {
	before := h.activeOps(defID)
	opID, err := h.enqueuer.EnqueueOp(c.Request.Context(), defID, params, opts...)
	if err != nil {
		httputil.InternalError(c, "enqueue "+defID+" failed", err)
		return
	}
	resp := StartedResponse{OperationID: opID, DefID: defID, FixerID: fixerID, Status: "queued"}
	if status, seen := before[opID]; seen {
		resp.Status, resp.Deduped = status, true
	}
	if h.ops != nil {
		if err := h.ops.SetSetting(lastKey, opID, "string", false); err != nil {
			logger.New("repairs").Warn("repairs: last-run pointer not recorded: key=%s op_id=%s err=%s",
				logger.SanitizeLogValue(lastKey), logger.SanitizeLogValue(opID), logger.SanitizeLogValue(err.Error()))
		}
	}
	c.JSON(http.StatusAccepted, gin.H{"data": resp})
}

// activeOps returns id -> status of every queued/running run of defID.
func (h *Handler) activeOps(defID string) map[string]string {
	out := map[string]string{}
	if h.ops == nil {
		return out
	}
	rows, err := h.ops.ListActiveOperationsV2()
	if err != nil {
		return out
	}
	for _, r := range rows {
		if r.DefID == defID {
			out[r.ID] = r.Status
		}
	}
	return out
}

func intQuery(c *gin.Context, key string, def int) (int, error) {
	v := c.Query(key)
	if v == "" {
		return def, nil
	}
	return strconv.Atoi(v)
}
