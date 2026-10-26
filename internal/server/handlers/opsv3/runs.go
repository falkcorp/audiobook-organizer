// file: internal/server/handlers/opsv3/runs.go
// version: 1.8.0
// guid: 736a68db-9f7b-4593-a1b0-155e22d7ca66
// last-edited: 2026-10-25

package opsv3

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// ListRuns implements GET /api/v3/ops/runs?state=&def_id=&limit=&cursor=.
// total is the store's exact count for the filter (RunPage.Total), the same
// number /counts reports for it; it does not depend on the page.
func (h *Handler) ListRuns(c *gin.Context) {
	rd, ok := h.runs(c)
	if !ok {
		return
	}
	st, ok := parseState(c)
	if !ok {
		return
	}
	limit, ok := limitParam(c)
	if !ok {
		return
	}
	q := database.RunQuery{DefID: c.Query("def_id"), State: st, Cursor: c.Query("cursor"), Limit: limit}
	// A Repairs run the caller may not read is left out by the store, inside the
	// one pass that counts and pages: total is the number of runs the caller may
	// read for the filter (the number /counts reports for it), the page is full
	// while readable runs remain, and next_cursor never points past the last one.
	// A def filter that names no Repairs definition has nothing to hide, so it
	// keeps the store's key-only count; otherwise every record is judged once.
	if q.DefID == "" || opaccess.IsRepairsOp(q.DefID) {
		reader := h.d.RepairsAccess.For(c)
		// The runs the caller may not read are known from the def indexes, so the
		// store skips their keys before it reads a record: a corrupt record of a
		// hidden run cannot fail this list. Keep still judges every record it
		// reads (a run created since the census), and a visible record that cannot
		// be decoded is skipped and logged rather than failing the page.
		hidden, err := h.hiddenIDs(reader, rd, q.DefID)
		if err != nil {
			storeErr(c, "list runs", err)
			return
		}
		q.SkipID = func(id string) bool { _, ok := hidden[id]; return ok }
		q.Keep = reader.MayReadRun
		q.OnUndecodable = func(id string, err error) {
			accessLog.Warn("run %s cannot be read and is left out of this list: %v", logger.SanitizeLogValue(id), err)
		}
	}
	page, err := rd.ListRunsV3(q)
	if err != nil {
		storeErr(c, "list runs", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items": h.views(page.Runs), "next_cursor": page.Next, "total": page.Total,
	})
}

// GetRun implements GET /api/v3/ops/runs/:id.
func (h *Handler) GetRun(c *gin.Context) {
	rd, ok := h.runs(c)
	if !ok {
		return
	}
	rec, err := rd.GetRunV3(c.Param("id"))
	if err != nil {
		httputil.InternalError(c, "get run", err)
		return
	}
	if rec == nil {
		httputil.RespondWithNotFound(c, "run", c.Param("id"))
		return
	}
	if !h.mayReadRun(c, rec) {
		return
	}
	c.JSON(http.StatusOK, toView(*rec, h.zombieSet()))
}

// startBody is the body of POST /api/v3/ops/defs/:id/runs. Def is read only
// by the unnested POST /api/v3/ops/runs.
type startBody struct {
	Def    string          `json:"def"`
	Params json.RawMessage `json:"params"`
	Mode   string          `json:"mode"`
}

// StartRun implements POST /api/v3/ops/defs/:id/runs {params, mode} (and
// POST /api/v3/ops/runs {def, params, mode}).
//
// Order: the definition exists (404), the caller holds its permission (empty
// means settings.manage, D1), params are valid against its schema (400, with
// field paths, nothing queued), it is ready (409 with the reason), then the
// run is queued (202 {run_id}).
//
// mode is "preview" or "live", and what it means depends on the definition
// (DefInfo.Preview, the op's own declaration of how it previews):
//
//   - A writing definition with a preview: every mode key its params type
//     decodes (dry_run, dryRun, apply, live) is part of the mode. Preview
//     writes each at its non-writing value (dry_run=true, apply=false,
//     live=false), live each at its writing value. Left out, the mode keys
//     already in params decide, and params that state none are a preview.
//     Its LiveOneOf switches, if any, are written false for a preview, and a
//     live run must set exactly one of them true itself. Its StartCheck, if
//     any, refuses params that cannot do what the mode asks.
//   - A writing definition with no declared preview (it ignores every key,
//     or previews only somewhere else, such as an HTTP handler): only mode
//     "live" starts it, and params are passed through untouched. "preview"
//     or no mode is a 409, because answering 202 would queue a live run the
//     caller asked to be a preview.
//   - A read-only definition: params are passed through untouched, so a
//     strictly decoded op never sees a key it does not declare.
//
// For every definition, a 400 refuses: a mode key in params that contradicts
// mode, or another mode key in params; a params key that differs from a
// mode key only in case (encoding/json would decode {"APPLY":true} into the
// apply field); and, for a definition with a preview, a mode key its params
// type does not decode (the op would never see it).
//
// A batchable definition does not create a run on enqueue; it adds the
// request to a pending batch. That answers 202 {"batched": true} with no
// run_id.
func (h *Handler) StartRun(c *gin.Context) {
	if h.d.Defs == nil || h.d.Enqueuer == nil {
		httputil.RespondWithServiceUnavailable(c, "operations registry not initialized")
		return
	}
	var body startBody
	if err := c.ShouldBindJSON(&body); err != nil && !isEmptyBody(err) {
		httputil.RespondWithBadRequest(c, "invalid body: "+err.Error())
		return
	}
	defID := c.Param("id")
	if defID == "" {
		defID = body.Def
	}
	if defID == "" {
		httputil.RespondWithValidationError(c, "def", "required")
		return
	}
	def, known := h.d.Defs.GetDef(defID)
	if !known {
		httputil.RespondWithNotFound(c, "definition", defID)
		return
	}
	if !h.requireAll(c, requiredPerms(def, true)) {
		return
	}

	params, perr := normalizeParams(body.Params)
	if perr != "" {
		httputil.RespondWithValidationError(c, "params", perr)
		return
	}
	// A Repairs definition carries no permission of its own beyond the generic
	// one, so the fixer its params name decides who may start it, as on the
	// Repairs routes (users.manage for the Audible read-status fixer).
	if ref := h.d.RepairsAccess.For(c).CheckStart(defID, params); ref != nil {
		ref.Respond(c)
		return
	}
	if len(def.ParamsSchema) > 0 {
		violations, err := ValidateParams(def.ParamsSchema, params)
		if err != nil {
			httputil.InternalError(c, "definition "+logger.SanitizeLogValue(defID)+" has a params schema this server cannot check", err)
			return
		}
		if len(violations) > 0 {
			httputil.RespondWithErrorFields(c, http.StatusBadRequest,
				"params do not match the definition's schema", "VALIDATION_ERROR",
				map[string]any{"fields": violations})
			return
		}
	}
	params, merr := resolveMode(def, params, body.Mode)
	if merr.msg != "" {
		if merr.conflict {
			httputil.RespondWithConflict(c, merr.msg)
		} else {
			httputil.RespondWithValidationError(c, "mode", merr.msg)
		}
		return
	}
	if ok, reason := h.d.Defs.Ready(defID); !ok {
		httputil.RespondWithConflict(c, "definition is not ready: "+reason)
		return
	}

	opts := actorOptions(c)
	runID, err := h.d.Enqueuer.EnqueueOp(c.Request.Context(), defID, params, opts...)
	if err != nil {
		httputil.InternalError(c, "enqueue failed", err)
		return
	}
	if runID == "" {
		// EnqueueOp's contract for a batchable def: ("", nil) means the
		// request joined a pending batch and no run exists yet.
		accepted(c, gin.H{"batched": true})
		return
	}
	accepted(c, gin.H{"run_id": runID})
}

// isEmptyBody: a POST with no body at all (an empty params, a plain approve)
// is not a malformed one.
func isEmptyBody(err error) bool { return errors.Is(err, io.EOF) }

// normalizeParams makes params a JSON object: absent and null are {}; anything
// that is not an object is refused. The returned message is "" when fine.
func normalizeParams(raw json.RawMessage) (json.RawMessage, string) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return json.RawMessage("{}"), ""
	}
	if t[0] != '{' || !json.Valid(t) {
		return nil, "must be a JSON object"
	}
	return json.RawMessage(t), ""
}

// modeErr is why a mode was refused: a 400 (malformed or contradicting
// params), or a 409 when the definition cannot run in the asked-for mode.
type modeErr struct {
	msg      string
	conflict bool
}

// resolveMode applies StartRun's mode rules (see there) and returns the params
// to queue.
func resolveMode(def DefInfo, params json.RawMessage, mode string) (json.RawMessage, modeErr) {
	switch mode {
	case "", opsregistry.ModePreview, opsregistry.ModeLive:
	default:
		return nil, modeErr{msg: `must be "preview" or "live"`}
	}
	if err := opsregistry.CheckModeKeySpelling(params); err != nil {
		return nil, modeErr{msg: err.Error()}
	}
	if !def.Writer {
		// Nothing to write into: the run changes nothing either way, and
		// params stay as sent, so an op that decodes strictly is not handed
		// a key it does not declare. A mode key that contradicts the asked
		// mode is still refused: the caller believes the run will do
		// something it will not.
		if err := opsregistry.CheckStatedMode(params, mode); err != nil {
			return nil, modeErr{msg: err.Error()}
		}
		return params, modeErr{}
	}
	if !def.HasPreview() {
		return resolveNoPreview(def.ID, params, mode)
	}
	out, err := def.Preview.Resolve(params, mode)
	if err != nil {
		return nil, modeErr{msg: "definition " + def.ID + ": " + err.Error()}
	}
	return out, modeErr{}
}

// resolveNoPreview: a writer with no declared preview starts only in mode
// live, with params untouched. A mode key in them that asks for a preview
// (dry_run=true, apply=false, live=false) is one the op cannot honour.
func resolveNoPreview(defID string, params json.RawMessage, mode string) (json.RawMessage, modeErr) {
	if mode != opsregistry.ModeLive {
		return nil, modeErr{conflict: true, msg: "definition " + defID + " has no preview mode: it writes whatever params say. " +
			`Send mode "live" to run it for real`}
	}
	if err := opsregistry.CheckStatedMode(params, mode); err != nil {
		return nil, modeErr{msg: err.Error()}
	}
	return params, modeErr{}
}

// actorOptions records who started the run and how they were verified. The
// identity is the verified session or token only; a forwarded email header is
// never read here.
func actorOptions(c *gin.Context) []opsregistry.EnqueueOption {
	var opts []opsregistry.EnqueueOption
	if u, ok := auth.UserFromContext(c.Request.Context()); ok {
		opts = append(opts, opsregistry.WithActor(u.ID))
		if m := servermiddleware.CurrentAuthMethod(c); m != auth.MethodNone {
			opts = append(opts, opsregistry.WithActorAuthMethod(string(m)))
		}
	}
	return opts
}

// CancelRun implements POST /api/v3/ops/runs/:id/cancel. The caller must be
// the user who started the run, or hold scan.trigger (the start floor) and
// the definition's permission. v1's DELETE /operations/v2/:id is stricter
// (settings.manage, no starter exception); the brief's rule is kept here.
func (h *Handler) CancelRun(c *gin.Context) {
	rd, ok := h.runs(c)
	if !ok {
		return
	}
	if h.d.Enqueuer == nil {
		httputil.RespondWithServiceUnavailable(c, "operations registry not initialized")
		return
	}
	rec, err := rd.GetRunV3(c.Param("id"))
	if err != nil {
		httputil.InternalError(c, "get run", err)
		return
	}
	if rec == nil {
		httputil.RespondWithNotFound(c, "run", c.Param("id"))
		return
	}
	// A Repairs run the caller may not read is refused before anything of it (its
	// state in the 409s below) is said, except to the user who started it.
	if !h.startedBy(c, rec) && !h.mayReadRun(c, rec) {
		return
	}
	if !h.mayCancel(c, rec) {
		httputil.RespondWithForbidden(c, "cancelling this run needs being the user who started it, or scan.trigger and the definition's permission")
		return
	}
	// The spec's only stop transition is running -> stopping
	// (state-and-persistence.md section 1). A terminal run is done. An
	// interrupted or awaiting_decision run is not executing, so there is
	// nothing to stop: its way out is "decide drop" (discard), not cancel.
	if p := state.ClassifyV3(rec.State); p.Terminal {
		httputil.RespondWithConflict(c, "run is already "+string(rec.State))
		return
	} else if p.Settled {
		httputil.RespondWithConflict(c, "run is "+string(rec.State)+" and not executing, so there is nothing to cancel; discard it or decide on it instead")
		return
	}
	if err := h.d.Enqueuer.Cancel(rec.ID); err != nil {
		if errors.Is(err, opsregistry.ErrOpNotFound) {
			httputil.RespondWithNotFound(c, "run", rec.ID)
			return
		}
		httputil.InternalError(c, "cancel failed", err)
		return
	}
	accepted(c, gin.H{"run_id": rec.ID})
}

// startedBy reports whether the caller is the user who started rec.
func (h *Handler) startedBy(c *gin.Context, rec *database.RunRecord) bool {
	u, ok := auth.UserFromContext(c.Request.Context())
	return ok && rec.Actor.UserID != "" && u.ID == rec.Actor.UserID
}

// mayCancel: the run's starter, or scan.trigger plus the definition's
// permissions.
func (h *Handler) mayCancel(c *gin.Context, rec *database.RunRecord) bool {
	if h.startedBy(c, rec) {
		return true
	}
	var def DefInfo
	known := false
	if h.d.Defs != nil {
		def, known = h.d.Defs.GetDef(rec.DefID)
	}
	return h.holdsAll(c, append([]auth.Permission{auth.PermScanTrigger}, requiredPerms(def, known)...))
}

// approveBody is the body of POST /api/v3/ops/runs/:id/approve.
type approveBody struct {
	Decision string `json:"decision"` // approve (default) | reject
	Note     string `json:"note"`
}

// ApproveRun implements POST /api/v3/ops/runs/:id/approve. It needs a verified
// auth method (a session, an API token or a verified Cloudflare Access
// sign-in) and the definition's approver permission (settings.manage when it
// names none). A request that is authenticated only by a forwarded header has
// no verified method and is refused with 403; the run is not touched.
func (h *Handler) ApproveRun(c *gin.Context) {
	rd, ok := h.runs(c)
	if !ok {
		return
	}
	method := servermiddleware.CurrentAuthMethod(c)
	if !verifiedForApproval(method) {
		httputil.RespondWithForbidden(c, "approving needs a signed-in session, an API token or a verified Cloudflare Access sign-in; a forwarded header is not an identity")
		return
	}
	user, hasUser := auth.UserFromContext(c.Request.Context())
	if !hasUser {
		httputil.RespondWithForbidden(c, "approving needs a signed-in user")
		return
	}
	rec, err := rd.GetRunV3(c.Param("id"))
	if err != nil {
		httputil.InternalError(c, "get run", err)
		return
	}
	if rec == nil {
		httputil.RespondWithNotFound(c, "run", c.Param("id"))
		return
	}
	if !h.mayReadRun(c, rec) {
		return
	}
	approver := auth.Permission(auth.PermSettingsManage)
	if h.d.Defs != nil {
		if def, known := h.d.Defs.GetDef(rec.DefID); known && def.Approver != "" {
			approver = def.Approver
		}
	}
	if !h.d.Authz.Require(c, approver) {
		return
	}
	var body approveBody
	if err := c.ShouldBindJSON(&body); err != nil && !isEmptyBody(err) {
		httputil.RespondWithBadRequest(c, "invalid body: "+err.Error())
		return
	}
	switch body.Decision {
	case "", "approve":
		body.Decision = "approve"
	case "reject":
	default:
		httputil.RespondWithValidationError(c, "decision", `must be "approve" or "reject"`)
		return
	}
	if h.d.Approver == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "approval is not wired to a runner in this build", "code": "NOT_IMPLEMENTED", "status": http.StatusNotImplemented})
		return
	}
	err = h.d.Approver.Approve(c.Request.Context(), rec.ID, database.ApprovalRecord{
		ApprovedBy: database.Actor{UserID: user.ID, AuthMethod: string(method)},
		ApprovedAt: h.d.Now().UTC().Truncate(time.Millisecond), Decision: body.Decision, Note: body.Note,
	})
	switch {
	case err == nil:
		accepted(c, gin.H{"run_id": rec.ID, "decision": body.Decision})
	case errors.Is(err, ErrRunNotFound):
		httputil.RespondWithNotFound(c, "run", rec.ID)
	case errors.Is(err, ErrRunNotApprovable):
		httputil.RespondWithConflict(c, err.Error())
	default:
		httputil.InternalError(c, "approve failed", err)
	}
}

// verifiedForApproval: the methods that prove who the caller is. A delegated
// session (temp-login, invite), an ABS token and no method at all do not.
//
// MethodCFAccess is accepted: middleware/cfaccess.go sets it only after it has
// verified the Cf-Access-Jwt-Assertion signature against the team's keys and
// resolved the signed email to a user, the same proof the owner gate
// (auth/owner.go) relies on. The unsigned Cf-Access-Authenticated-User-Email
// header never sets a method, so a request carrying only that header has
// none and is refused (TestOpsV3Routes_ForwardedHeaderCannotApprove).
func verifiedForApproval(m auth.Method) bool {
	switch m {
	case auth.MethodSession, auth.MethodAPIKey, auth.MethodCFAccess:
		return true
	}
	return false
}
