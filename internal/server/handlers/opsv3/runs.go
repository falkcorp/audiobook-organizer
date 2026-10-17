// file: internal/server/handlers/opsv3/runs.go
// version: 1.0.0
// guid: 736a68db-9f7b-4593-a1b0-155e22d7ca66
// last-edited: 2026-10-17

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
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
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
	page, err := rd.ListRunsV3(database.RunQuery{
		DefID: c.Query("def_id"), State: st, Cursor: c.Query("cursor"), Limit: limit,
	})
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
// mode is "preview" or "live". Left out, the params decide, and a params body
// that says nothing is a preview (opmode.ResolveDryRun). A mode that
// contradicts a dry_run in params is a 400.
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
	params, merr := applyMode(defID, params, body.Mode)
	if merr != "" {
		httputil.RespondWithValidationError(c, "mode", merr)
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

// applyMode writes the request's mode into params as dry_run. With no mode the
// params are untouched (they either carry dry_run or mean preview). It checks
// the result with opmode.ParseDryRun so a params body whose dry_run and dryRun
// disagree, or disagree with mode, is refused here instead of picking a side.
func applyMode(defID string, params json.RawMessage, mode string) (json.RawMessage, string) {
	switch mode {
	case "":
		if _, err := opmode.ParseDryRun(defID, params); err != nil {
			return nil, err.Error()
		}
		return params, ""
	case "preview", "live":
	default:
		return nil, `must be "preview" or "live"`
	}
	want := mode == "preview"
	var m map[string]json.RawMessage
	if err := json.Unmarshal(params, &m); err != nil {
		return nil, "params must be a JSON object"
	}
	for _, key := range []string{"dry_run", "dryRun"} {
		raw, ok := m[key]
		if !ok {
			continue
		}
		var got bool
		if err := json.Unmarshal(raw, &got); err != nil {
			return nil, key + " must be true or false"
		}
		if got != want {
			return nil, "mode " + mode + " contradicts " + key + " in params"
		}
	}
	b, _ := json.Marshal(want)
	m["dry_run"] = b
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err.Error()
	}
	if _, err := opmode.ParseDryRun(defID, out); err != nil {
		return nil, err.Error()
	}
	return out, ""
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

// CancelRun implements POST /api/v3/ops/runs/:id/cancel. The caller must hold
// the definition's permission or be the user who started the run.
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
	if !h.mayCancel(c, rec) {
		httputil.RespondWithForbidden(c, "cancelling this run needs the definition's permission or being the user who started it")
		return
	}
	if p := state.ClassifyV3(rec.State); p.Terminal || p.Settled {
		httputil.RespondWithConflict(c, "run is already "+string(rec.State))
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

// mayCancel: the definition's permissions, or the run's starter.
func (h *Handler) mayCancel(c *gin.Context, rec *database.RunRecord) bool {
	var def DefInfo
	known := false
	if h.d.Defs != nil {
		def, known = h.d.Defs.GetDef(rec.DefID)
	}
	if h.holdsAll(c, requiredPerms(def, known)) {
		return true
	}
	u, ok := auth.UserFromContext(c.Request.Context())
	return ok && rec.Actor.UserID != "" && u.ID == rec.Actor.UserID
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
		httputil.RespondWithForbidden(c, "approving needs a signed-in session or an API token; a forwarded header is not an identity")
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
func verifiedForApproval(m auth.Method) bool {
	switch m {
	case auth.MethodSession, auth.MethodAPIKey, auth.MethodCFAccess:
		return true
	}
	return false
}
