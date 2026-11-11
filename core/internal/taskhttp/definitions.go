// file: core/internal/taskhttp/definitions.go
// version: 1.0.0
// guid: ea2690a4-bdfc-4cf8-9e01-2a186d69487f
// last-edited: 2026-11-11

package taskhttp

import (
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// viewRequirement is a definition's View rule (omitted: core:tasks.view).
func viewRequirement(c tasks.Common) rbac.Requirement {
	if c.Perms.View.IsZero() {
		return rbac.Need(scopeView)
	}
	return c.Perms.View
}

// mayView applies a definition's View rule to the caller.
func mayView(p *auth.Principal, def tasks.Definition) bool {
	return p.Perms.Satisfies(viewRequirement(tasks.CommonOf(def)))
}

// definitionDTO is one definition as the API shows it. The scopes it needs
// are not listed (they are in the catalog); the flags say what this caller
// may do.
type definitionDTO struct {
	Kind        string       `json:"kind"`
	Module      string       `json:"module"`
	Title       string       `json:"title"`
	Help        string       `json:"help,omitempty"`
	Shape       string       `json:"shape"`
	ReadOnly    bool         `json:"read_only"`
	ScheduleKey string       `json:"schedule_key,omitempty"`
	Approval    *approvalDTO `json:"approval,omitempty"`
	CanStart    bool         `json:"can_start"`
	CanApprove  bool         `json:"can_approve"`
}

type approvalDTO struct {
	PlanRequired   bool  `json:"plan_required"`
	MaxPlanAgeSecs int64 `json:"max_plan_age_seconds"`
}

func definitionView(p *auth.Principal, def tasks.Definition) definitionDTO {
	c := tasks.CommonOf(def)
	d := definitionDTO{
		Kind: string(def.Kind()), Module: def.Kind().Module(), Title: c.Title, Help: c.Help, Shape: def.Shape().String(),
		ReadOnly: c.Effects.IsReadOnly(), CanStart: p.Perms.Satisfies(c.Perms.Start),
		CanApprove: !c.Perms.Approve.IsZero() && p.Perms.Satisfies(c.Perms.Approve),
	}
	if c.Schedule != nil {
		d.ScheduleKey = c.Schedule.ConfigKey()
	}
	if c.Approval != nil {
		age := c.Approval.MaxPlanAge
		if age == 0 {
			age = tasks.DefaultMaxPlanAge
		}
		d.Approval = &approvalDTO{PlanRequired: c.Approval.PlanRequired, MaxPlanAgeSecs: int64(age / time.Second)}
	}
	return d
}

// listDefinitions: GET /definitions (AnyAuthenticated + per-def View).
func (s *Server) listDefinitions(w http.ResponseWriter, _ *http.Request, p *auth.Principal) {
	out := make([]definitionDTO, 0, len(s.order))
	for _, k := range s.order {
		if def := s.defs[k]; mayView(p, def) {
			out = append(out, definitionView(p, def))
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"definitions": out})
}

// getDefinition: GET /definitions/{kind}. A definition the caller may not
// view is not found, so its existence is not disclosed.
func (s *Server) getDefinition(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	k, ok := kindParam(r)
	def := s.defs[k]
	if !ok || def == nil || !mayView(p, def) {
		s.write(w, r, notFound("tasks.unknown_kind", "no task definition has this kind"))
		return
	}
	s.writeJSON(w, http.StatusOK, definitionView(p, def))
}
