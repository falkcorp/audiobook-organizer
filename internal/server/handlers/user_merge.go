// file: internal/server/handlers/user_merge.go
// version: 1.0.0
// guid: 5e1c7a93-2b48-4d06-8f3a-c91d07e4b256
// last-edited: 2026-10-23

package handlers

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
	"github.com/gin-gonic/gin"
)

// UserMergeOpID is the background operation the apply, resume and undo routes
// enqueue (internal/plugins/maintenance.UserMergeOpID; a plugin cannot be
// imported from here).
const UserMergeOpID = usermerge.OpDefID

// UserMergeService is what the merge routes need of usermerge.Service.
type UserMergeService interface {
	Footprint(userID string) (*usermerge.Footprint, error)
	BuildPlan(ctx context.Context, req usermerge.PlanRequest) (*usermerge.Plan, error)
	LoadJournal(id string) (*usermerge.Journal, error)
	ListJournals() ([]usermerge.JournalSummary, error)
}

// UserMergeEnqueuer starts the merge operation.
type UserMergeEnqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...opsregistry.EnqueueOption) (string, error)
}

// SetUserMerge wires the merge routes (wire time). Either argument nil leaves
// the routes answering 503.
func (h *UserHandler) SetUserMerge(svc UserMergeService, enq UserMergeEnqueuer, grants *usermerge.Grants) {
	h.merge, h.mergeEnqueuer, h.mergeGrants = svc, enq, grants
}

// mergeParams is the maintenance.user-merge parameter shape (the JSON names of
// the plugin's userMergeParams; user_merge_test.go pins them against a real
// operation definition). dry_run is always sent so a live run never depends on
// the omitted-means-preview default.
type mergeParams struct {
	DryRun                   bool   `json:"dry_run"`
	Action                   string `json:"action"`
	SurvivorID               string `json:"survivor_id,omitempty"`
	AbsorbedID               string `json:"absorbed_id,omitempty"`
	Digest                   string `json:"digest,omitempty"`
	JournalID                string `json:"journal_id,omitempty"`
	AcknowledgeOwnerIdentity bool   `json:"acknowledge_owner_identity,omitempty"`
	Grant                    string `json:"grant"`
}

// mergeActor is the signed-in active admin behind a merge route. users.manage
// (the route guard) is not enough: a merge moves another account's sign-ins, so
// the actor must hold the admin role itself, like SetUserEmail.
func (h *UserHandler) mergeActor(c *gin.Context) (id string, ok bool) {
	actor, has := servermiddleware.CurrentUser(c)
	if !has || actor == nil || actor.Status != "active" || !slices.Contains(actor.Roles, adminRole) {
		httputil.RespondWithForbidden(c, "merging accounts needs the admin role")
		return "", false
	}
	if h.merge == nil {
		httputil.RespondWithServiceUnavailable(c, "user merge is not available on this server")
		return "", false
	}
	return actor.ID, true
}

// UserFootprint handles GET /api/v1/users/:id/footprint: the account's sign-ins
// (masked) and how much data hangs off it. Read-only.
func (h *UserHandler) UserFootprint(c *gin.Context) {
	if _, ok := h.mergeActor(c); !ok {
		return
	}
	id := c.Param("id")
	fp, err := h.merge.Footprint(id)
	if err != nil {
		h.mergeError(c, "user footprint", err)
		return
	}
	httputil.RespondWithOK(c, fp)
}

// PreviewUserMerge handles POST /api/v1/users/:id/merge-into/:target/preview:
// what merging account :id (absorbed) into :target (the survivor) would do. It
// writes nothing. A refused merge is still a 200 whose plan lists the refusals.
func (h *UserHandler) PreviewUserMerge(c *gin.Context) {
	actorID, ok := h.mergeActor(c)
	if !ok {
		return
	}
	plan, err := h.merge.BuildPlan(c.Request.Context(), usermerge.PlanRequest{
		ActorID: actorID, SurvivorID: c.Param("target"), AbsorbedID: c.Param("id")})
	if err != nil {
		h.mergeError(c, "preview user merge", err)
		return
	}
	httputil.RespondWithOK(c, plan)
}

// ApplyUserMerge handles POST /api/v1/users/:id/merge-into/:target/apply
// {"digest": "...", "acknowledge_owner_identity": bool}. The digest is the one
// the preview returned: the plan is rebuilt here and a changed digest (or a
// refusal) answers 409 with the fresh plan so the page shows what changed. The
// merge itself runs as a background operation; the answer is 202 with its id.
func (h *UserHandler) ApplyUserMerge(c *gin.Context) {
	actorID, ok := h.mergeActor(c)
	if !ok {
		return
	}
	var req struct {
		Digest                   string `json:"digest"`
		AcknowledgeOwnerIdentity bool   `json:"acknowledge_owner_identity"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Digest == "" {
		httputil.RespondWithBadRequest(c, `body must be {"digest": "<digest from the preview>"}`)
		return
	}
	survivorID, absorbedID := c.Param("target"), c.Param("id")
	plan, err := h.merge.BuildPlan(c.Request.Context(), usermerge.PlanRequest{
		ActorID: actorID, SurvivorID: survivorID, AbsorbedID: absorbedID})
	if err != nil {
		h.mergeError(c, "apply user merge", err)
		return
	}
	switch {
	case !plan.Applicable():
		respondMergeConflict(c, "the merge is refused; see the plan", plan)
		return
	case plan.Digest != req.Digest:
		respondMergeConflict(c, "the accounts changed since the preview; review the new preview", plan)
		return
	case plan.OwnerIdentityOnAbsorbed && !req.AcknowledgeOwnerIdentity:
		respondMergeConflict(c, "the account being merged away holds the owner's sign-in identity; acknowledge it to continue", plan)
		return
	}
	h.enqueueMerge(c, actorID, usermerge.Grant{Action: usermerge.ActionApply, SurvivorID: survivorID,
		AbsorbedID: absorbedID, Digest: req.Digest, AcknowledgeOwnerIdentity: req.AcknowledgeOwnerIdentity})
}

// ListUserMerges handles GET /api/v1/users/merges: every merge journal, newest
// first, without its ops.
func (h *UserHandler) ListUserMerges(c *gin.Context) {
	if _, ok := h.mergeActor(c); !ok {
		return
	}
	list, err := h.merge.ListJournals()
	if err != nil {
		h.mergeError(c, "list user merges", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"merges": list})
}

// ResumeUserMerge handles POST /api/v1/users/merges/:jid/resume: finishes a
// merge that stopped part way (a crash, a restart, a cancelled run).
func (h *UserHandler) ResumeUserMerge(c *gin.Context) {
	h.journalAction(c, usermerge.ActionResume, func(status string) bool { return status == usermerge.StatusApplying })
}

// UndoUserMerge handles POST /api/v1/users/merges/:jid/undo: puts back what the
// merge changed, except anything written since (reported, never overwritten).
func (h *UserHandler) UndoUserMerge(c *gin.Context) {
	h.journalAction(c, usermerge.ActionUndo, func(status string) bool {
		return status == usermerge.StatusApplying || status == usermerge.StatusApplied || status == usermerge.StatusUndoing
	})
}

func (h *UserHandler) journalAction(c *gin.Context, action usermerge.Action, allowed func(status string) bool) {
	actorID, ok := h.mergeActor(c)
	if !ok {
		return
	}
	jid := c.Param("jid")
	j, err := h.merge.LoadJournal(jid)
	if err != nil {
		h.mergeError(c, "load merge journal", err)
		return
	}
	if !allowed(j.Status) {
		httputil.RespondWithConflict(c, "the merge is "+j.Status+"; it cannot be "+string(action)+"d")
		return
	}
	if j.AbsorbedID == actorID {
		httputil.RespondWithConflict(c, "an account cannot run a merge it was merged away by")
		return
	}
	h.enqueueMerge(c, actorID, usermerge.Grant{Action: action, JournalID: jid})
}

// enqueueMerge mints the single-use grant for g (bound to the actor) and
// enqueues the operation that redeems it. The generic operations route cannot
// start a live merge: without a grant from here the operation refuses.
func (h *UserHandler) enqueueMerge(c *gin.Context, actorID string, g usermerge.Grant) {
	if h.mergeEnqueuer == nil || h.mergeGrants == nil {
		httputil.RespondWithServiceUnavailable(c, "operations registry not initialized")
		return
	}
	ctx := c.Request.Context()
	g.ActorID = actorID
	tok, err := h.mergeGrants.Issue(g)
	if err != nil {
		httputil.InternalError(c, "merge grant", err)
		return
	}
	// Dry-run is sent explicitly: omitted means preview, which would silently
	// do nothing.
	params := mergeParams{DryRun: false, Action: string(g.Action), SurvivorID: g.SurvivorID, AbsorbedID: g.AbsorbedID,
		Digest: g.Digest, JournalID: g.JournalID, AcknowledgeOwnerIdentity: g.AcknowledgeOwnerIdentity, Grant: tok}
	opID, err := h.mergeEnqueuer.EnqueueOp(ctx, UserMergeOpID, params,
		opsregistry.WithActor(actorID), opsregistry.WithActorAuthMethod(string(auth.MethodFromContext(ctx))))
	if err != nil {
		// The grant is single-use and expires; a failed enqueue just leaves it unused.
		httputil.InternalError(c, "enqueue "+UserMergeOpID, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"data": gin.H{"operation_id": opID, "action": g.Action}})
}

func respondMergeConflict(c *gin.Context, msg string, plan *usermerge.Plan) {
	httputil.RespondWithErrorFields(c, http.StatusConflict, msg, "conflict", map[string]any{"plan": plan})
}

func (h *UserHandler) mergeError(c *gin.Context, what string, err error) {
	switch {
	case errors.Is(err, usermerge.ErrJournalNotFound):
		httputil.RespondWithNotFound(c, "merge journal", c.Param("jid"))
	case errors.Is(err, usermerge.ErrUnsupported):
		httputil.RespondWithServiceUnavailable(c, "this store cannot merge users")
	case errors.Is(err, usermerge.ErrJournalState):
		httputil.RespondWithConflict(c, err.Error())
	default:
		logger.New("user-merge").Warn("%s: %s", what, logger.SanitizeLogValue(err.Error()))
		httputil.InternalError(c, what, err)
	}
}
