// file: core/internal/taskhttp/problem.go
// version: 1.0.0
// guid: ccb2b806-3a12-47fa-8b5a-a89f4b2a7884
// last-edited: 2026-11-11

package taskhttp

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// problemTypeBase is httpapi's problem type base (05-http-api §3.3); httpapi
// keeps it unexported and this package may not import httpapi (same layer).
const problemTypeBase = "https://mediacore.dev/problems/"

// problem is the native error body, RFC 9457 application/problem+json with
// httpapi's extension members (05-http-api §3.3). The task codes are the
// ones 05-tasks 5T.5 names ("tasks.unknown_kind").
type problem struct {
	Status   int          `json:"status"`
	Code     string       `json:"code"`
	Detail   string       `json:"detail,omitempty"`
	Errors   []fieldError `json:"errors,omitempty"`
	Required string       `json:"required_permission,omitempty"`
}

// fieldError is one validation detail.
type fieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// problemBody is problem as serialised.
type problemBody struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"`
	Code      string       `json:"code"`
	RequestID string       `json:"request_id,omitempty"`
	Errors    []fieldError `json:"errors,omitempty"`
	Required  string       `json:"required_permission,omitempty"`
}

// write sends p as application/problem+json.
func (s *Server) write(w http.ResponseWriter, r *http.Request, p problem) {
	b := problemBody{
		Type: problemTypeBase + p.Code, Title: http.StatusText(p.Status), Status: p.Status, Detail: p.Detail,
		Instance: r.URL.Path, Code: p.Code, RequestID: r.Header.Get("X-Request-Id"), Errors: p.Errors, Required: p.Required,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	if err := json.NewEncoder(w).Encode(b); err != nil {
		s.log.Warn("taskhttp: write problem", "err", err)
	}
}

// forbidden is a 403 naming the requirement the caller does not satisfy.
func forbidden(need rbac.Requirement) problem {
	p := problem{Status: http.StatusForbidden, Code: "permission_denied"}
	if !need.IsZero() {
		p.Required = need.String()
	}
	return p
}

func invalid(fields ...fieldError) problem {
	return problem{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Errors: fields}
}

func notFound(code, detail string) problem {
	return problem{Status: http.StatusNotFound, Code: code, Detail: detail}
}

// fail answers err: the sentinel mapping of 5T.5, or a 500 whose detail is
// withheld (the cause is logged).
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	p, ok := mapError(err)
	if !ok {
		s.log.Error("taskhttp: unmapped error", "method", r.Method, "path", r.URL.Path, "err", err)
		p = problem{Status: http.StatusInternalServerError, Code: "internal"}
	}
	s.write(w, r, p)
}

// mapError maps the executor's, tasks' and config's refusals to problems.
// It matches by errors.Is, so a wrapped sentinel maps as its root.
func mapError(err error) (problem, bool) {
	detail := err.Error()
	var (
		syntax *json.SyntaxError
		typed  *json.UnmarshalTypeError
	)
	switch {
	case errors.Is(err, executor.ErrUnknownKind):
		return problem{Status: 404, Code: "tasks.unknown_kind", Detail: detail}, true
	case errors.Is(err, tasks.ErrRunNotFound):
		return problem{Status: 404, Code: "tasks.run_not_found", Detail: detail}, true
	case errors.Is(err, executor.ErrHeld):
		return problem{Status: 409, Code: "tasks.held", Detail: detail}, true
	case errors.Is(err, executor.ErrNotPermitted):
		return problem{Status: 403, Code: "tasks.actor_not_permitted", Detail: detail}, true
	case errors.Is(err, tasks.ErrRunActive):
		return problem{Status: 409, Code: "tasks.run_active", Detail: detail}, true
	case errors.Is(err, tasks.ErrRunConflict):
		return problem{Status: 409, Code: "tasks.run_changed", Detail: detail}, true
	case errors.Is(err, executor.ErrInvalidPriority):
		return problem{Status: 422, Code: "tasks.invalid_priority", Detail: detail}, true
	case errors.Is(err, executor.ErrPlanRequired):
		return problem{Status: 422, Code: "tasks.plan_required", Detail: detail}, true
	case errors.Is(err, executor.ErrApprovalRequired):
		return problem{Status: 422, Code: "tasks.approval_required", Detail: detail}, true
	case errors.Is(err, executor.ErrPlanInvalid):
		return problem{Status: 422, Code: "tasks.plan_invalid", Detail: detail}, true
	case errors.Is(err, executor.ErrRowNotInPlan):
		return problem{Status: 422, Code: "tasks.row_not_in_plan", Detail: detail}, true
	case errors.Is(err, executor.ErrPlanTooOld):
		return problem{Status: 409, Code: "tasks.plan_too_old", Detail: detail}, true
	case errors.Is(err, executor.ErrPlanRejected):
		return problem{Status: 409, Code: "tasks.plan_rejected", Detail: detail}, true
	case errors.Is(err, executor.ErrPlanDecided):
		return problem{Status: 409, Code: "tasks.plan_decided", Detail: detail}, true
	case errors.Is(err, executor.ErrNoApproval):
		return problem{Status: 409, Code: "tasks.no_approval", Detail: detail}, true
	case errors.Is(err, executor.ErrNotAwaitingDecision):
		return problem{Status: 409, Code: "tasks.not_awaiting_decision", Detail: detail}, true
	case errors.Is(err, executor.ErrShuttingDown):
		return problem{Status: 503, Code: "unavailable", Detail: detail}, true
	case errors.Is(err, tasks.ErrModeUnset):
		return problem{Status: 400, Code: "tasks.mode_unset", Detail: detail}, true
	case errors.Is(err, config.ErrLocked):
		return problem{Status: 409, Code: "tasks.schedule_locked", Detail: detail}, true
	case errors.Is(err, config.ErrInvalid):
		return problem{Status: 422, Code: "validation_failed", Detail: detail}, true
	case errors.Is(err, config.ErrProtected), errors.Is(err, config.ErrForbidden):
		return problem{Status: 403, Code: "permission_denied", Detail: detail}, true
	case errors.As(err, &syntax), errors.As(err, &typed):
		// StartRequest.Params that do not decode into the definition's
		// params type: the executor returns the decoder's own error.
		return problem{Status: 422, Code: "tasks.invalid_params", Detail: detail}, true
	}
	return problem{}, false
}
