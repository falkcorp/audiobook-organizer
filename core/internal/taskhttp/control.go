// file: core/internal/taskhttp/control.go
// version: 1.0.0
// guid: 13b7c60a-c524-4e3b-bb06-f9180a83fc7e
// last-edited: 2026-11-11

package taskhttp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// The audit actions (05-rbac 7.1; audit.CoreActions names them). Approve and
// decide record core.task.approve or core.task.reject, cancel by another
// actor records core.task.cancel. A schedule edit is a config write recorded
// as core.settings.update by config.Apply itself.
const (
	actionApprove = "core.task.approve"
	actionReject  = "core.task.reject"
	actionCancel  = "core.task.cancel"
)

// maxReason bounds the free text of an approval or rejection.
const maxReason = 1024

// target is the audit target of a run.
func target(id tasks.RunID) entity.Ref {
	return entity.Ref{Module: "core", Kind: "task_run", ID: string(id)}
}

// auditActor is the caller as the audit record names it.
func auditActor(p *auth.Principal, r *http.Request) audit.Actor {
	a := audit.Actor{Method: string(p.Method), CredentialID: p.CredentialID}
	if p.User != nil {
		a.UserID, a.Username = string(p.User.ID), p.User.Username
	}
	if p.ClientAddr.IsValid() {
		a.ClientAddr = p.ClientAddr.String()
	}
	if ua := r.UserAgent(); len(ua) > 256 {
		a.UserAgent = ua[:256]
	} else {
		a.UserAgent = ua
	}
	return a
}

// record writes a state change's audit record (synchronous, 05-rbac 7.1:
// the audit sink is the record of who decided). The action has already been
// taken when it runs, so a failure cannot undo it: the route answers 500
// tasks.audit_failed, which says so, and logs the failure.
func (s *Server) record(w http.ResponseWriter, r *http.Request, p *auth.Principal, action string, id tasks.RunID, details map[string]any) bool {
	e := audit.Event{
		Action: action, Outcome: audit.Success, Actor: auditActor(p, r), Target: target(id),
		Route: routePattern(r), Details: details,
	}
	if err := s.d.Audit.Record(r.Context(), e); err != nil {
		s.log.Error("taskhttp: audit record failed after the action was taken", "action", action, "run", id, "err", err)
		s.write(w, r, problem{Status: http.StatusInternalServerError, Code: "tasks.audit_failed",
			Detail: "the action was taken but its audit record could not be written"})
		return false
	}
	return true
}

// deny queues a Denied record (async; denials are high-volume) for a refusal
// this package decided.
func (s *Server) deny(r *http.Request, p *auth.Principal, action string, id tasks.RunID, need rbac.Requirement, reason string) {
	e := audit.Event{
		Action: action, Outcome: audit.Denied, Actor: auditActor(p, r), Target: target(id), Reason: reason,
		Route: routePattern(r),
	}
	if !need.IsZero() {
		e.Permission = need.String()
	}
	s.d.Audit.Emit(r.Context(), e)
}

// routePattern is the matched route's pattern ("POST /api/core/v1/tasks/runs/{id}/cancel"),
// never the raw path (05-rbac 7.1: a record names the pattern).
func routePattern(r *http.Request) string {
	p, _ := r.Context().Value(routeKey{}).(string)
	return p
}

// sameActor reports whether a run was started by the caller: the same
// signed-in user.
func sameActor(p *auth.Principal, v tasks.RunView) bool {
	return p.User != nil && v.Actor.UserID != "" && v.Actor.UserID == string(p.User.ID)
}

// cancelRun: POST /runs/{id}/cancel. The permission (the run's actor, the
// definition's Perms.Cancel or core:tasks.manage) is the executor's; this
// handler adds the audit record when the caller is not the run's actor.
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	id := runID(r)
	// The view may be refused (a run the caller may cancel but not view): the
	// cancel is still the executor's call, and the audit record then cannot
	// show the run's actor, so it records the cancel as another actor's.
	before, viewErr := s.viewable(r)
	if viewErr != nil && !errors.Is(viewErr, tasks.ErrRunNotFound) {
		s.fail(w, r, viewErr)
		return
	}
	if err := s.d.Client.Cancel(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	if viewErr != nil || !sameActor(p, before) {
		details := map[string]any{}
		if viewErr == nil {
			details["kind"] = string(before.Kind)
			details["run_actor"] = before.Actor.UserID
			if before.Actor.System != "" {
				details["run_actor"] = "system:" + before.Actor.System
			}
		}
		if !s.record(w, r, p, actionCancel, id, details) {
			return
		}
	}
	s.respondRun(w, r, p, id)
}

// respondRun answers 200 with the run as it reads now; when the re-read
// fails (the run was deleted meanwhile) the action stands and the answer is
// 204.
func (s *Server) respondRun(w http.ResponseWriter, r *http.Request, p *auth.Principal, id tasks.RunID) {
	v, err := s.d.Client.Get(r.Context(), id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writeJSON(w, http.StatusOK, runView(p, v))
}

// discardRun: DELETE /runs/{id} (Client.Discard: the cancel permission rule;
// 409 tasks.run_active while the run is live).
func (s *Server) discardRun(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	if err := s.d.Client.Discard(r.Context(), runID(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifiedGate applies approve's and decide's two rules in front of the
// executor: the caller satisfies the definition's requirement (need, when
// the definition is known) and the credential is a verified method (SEC-24).
// It answers false, having written the refusal and queued its denial record.
func (s *Server) verifiedGate(w http.ResponseWriter, r *http.Request, p *auth.Principal, action string, id tasks.RunID, need rbac.Requirement) bool {
	if !need.IsZero() && !p.Perms.Satisfies(need) {
		s.deny(r, p, action, id, need, "permission_denied")
		s.write(w, r, forbidden(need))
		return false
	}
	if !s.verify(p, need) {
		s.deny(r, p, action, id, need, "unverified_method")
		s.write(w, r, problem{Status: http.StatusForbidden, Code: "tasks.verified_method_required",
			Detail: "approving or deciding a run needs an interactive sign-in, or an API key that names the approval scope", Required: need.String()})
		return false
	}
	return true
}

// approveBody is POST /runs/{id}/approve.
type approveBody struct {
	Rows   []string `json:"rows"`
	Reject bool     `json:"reject"`
	Reason string   `json:"reason"`
}

// approveRun: POST /runs/{id}/approve {rows, reject, reason}. Needs the
// definition's Perms.Approve and a verified method.
func (s *Server) approveRun(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	raw, bad := readBody(w, r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	var b approveBody
	if bad := decodeStrict(raw, &b); bad != nil {
		s.write(w, r, *bad)
		return
	}
	switch {
	case len(b.Reason) > maxReason:
		s.write(w, r, invalid(fieldError{Field: "/body/reason", Code: "too_long", Message: "must be at most 1024 bytes"}))
		return
	case b.Reject && len(b.Rows) > 0:
		s.write(w, r, invalid(fieldError{Field: "/body/rows", Code: "conflicts_with_reject", Message: "a rejection names no rows"}))
		return
	}
	v, err := s.viewable(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	action := actionApprove
	if b.Reject {
		action = actionReject
	}
	// A run whose definition is not loaded (held, retired) is still behind
	// the verified-method rule; the executor then answers 409 held or 404.
	var need rbac.Requirement
	if def := s.defs[v.Kind]; def != nil {
		need = tasks.CommonOf(def).Perms.Approve
	}
	if !s.verifiedGate(w, r, p, action, v.ID, need) {
		return
	}
	req := tasks.ApproveRequest{Rows: b.Rows, Reject: b.Reject, Reason: b.Reason}
	if err := s.d.Approver.Approve(r.Context(), v.ID, req); err != nil {
		s.fail(w, r, err)
		return
	}
	details := map[string]any{"kind": string(v.Kind), "rows": len(b.Rows)}
	if b.Reason != "" {
		details["reason"] = b.Reason
	}
	if !s.record(w, r, p, action, v.ID, details) {
		return
	}
	s.respondRun(w, r, p, v.ID)
}

// decideBody is POST /runs/{id}/decide.
type decideBody struct {
	Decision string `json:"decision"`
}

// decideRun: POST /runs/{id}/decide {"decision": "resume"|"finish"|"drop"}
// for a run awaiting a decision. Needs the definition's Perms.Approve, else
// Perms.Start, and a verified method. resume and finish record
// core.task.approve, drop records core.task.reject.
func (s *Server) decideRun(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	raw, bad := readBody(w, r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	var b decideBody
	if bad := decodeStrict(raw, &b); bad != nil {
		s.write(w, r, *bad)
		return
	}
	var d tasks.Decision
	action := actionApprove
	switch strings.ToLower(b.Decision) {
	case "resume":
		d = tasks.DecideResume
	case "finish":
		d = tasks.DecideFinish
	case "drop":
		d, action = tasks.DecideDrop, actionReject
	default:
		s.write(w, r, invalid(fieldError{Field: "/body/decision", Code: "invalid_enum", Message: `must be "resume", "finish" or "drop"`}))
		return
	}
	v, err := s.viewable(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var need rbac.Requirement
	if def := s.defs[v.Kind]; def != nil {
		c := tasks.CommonOf(def)
		need = c.Perms.Approve
		if need.IsZero() {
			need = c.Perms.Start
		}
	}
	if !s.verifiedGate(w, r, p, action, v.ID, need) {
		return
	}
	if err := s.d.Client.Decide(r.Context(), v.ID, d); err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.record(w, r, p, action, v.ID, map[string]any{"kind": string(v.Kind), "decision": strings.ToLower(b.Decision)}) {
		return
	}
	s.respondRun(w, r, p, v.ID)
}
