// file: core/internal/taskhttp/start.go
// version: 1.0.0
// guid: 2490f91d-eebe-4721-a6ad-7bece467ef47
// last-edited: 2026-11-11

package taskhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/tasks"
)

// validIdempotencyKey is ^[A-Za-z0-9_-]{8,128}$ (05-http-api §4.5), written
// out because core declares no package-level regexp (03 R5).
func validIdempotencyKey(k string) bool {
	if len(k) < 8 || len(k) > 128 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// startBody is the start envelope (5T.3.7 rule 1): the mode is a member of
// the envelope, never of params. There is no plan_ref (a plan's apply run
// starts only through approve), no parent, no actor and no trigger.
type startBody struct {
	Mode     *string         `json:"mode"`
	Params   json.RawMessage `json:"params"`
	Priority *string         `json:"priority"`
}

// The spellings 5T.3.7 rule 1 refuses (D10: no alias).
const (
	modeFieldSnake = "dry_run"
	modeFieldCamel = "dryRun"
)

// hasModeField reports a dry_run/dryRun member of obj, a JSON object; a
// value that is not an object has none.
func hasModeField(obj json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(obj, &m) != nil {
		return false
	}
	_, snake := m[modeFieldSnake]
	_, camel := m[modeFieldCamel]
	return snake || camel
}

// parsePriority reads the priority names of the start envelope.
func parsePriority(s string) (tasks.Priority, bool) {
	switch strings.ToLower(s) {
	case "low":
		return tasks.PriorityLow, true
	case "normal":
		return tasks.PriorityNormal, true
	case "high":
		return tasks.PriorityHigh, true
	case "system":
		return tasks.PrioritySystem, true
	}
	return 0, false
}

// startRun: POST /definitions/{kind}/runs. The route floor is
// AnyAuthenticated; the definition's Perms.Start is checked here and again by
// the executor, which stamps the actor from the same principal.
func (s *Server) startRun(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	kind, ok := kindParam(r)
	if !ok {
		s.write(w, r, notFound("tasks.unknown_kind", "no task definition has this kind"))
		return
	}
	req, bad := s.startRequest(w, r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	// A kind with no definition here (unknown, or held) is left to the
	// executor, which answers 404 tasks.unknown_kind or 409 tasks.held.
	if def := s.defs[kind]; def != nil {
		if need := tasks.CommonOf(def).Perms.Start; !p.Perms.Satisfies(need) {
			s.write(w, r, forbidden(need))
			return
		}
	}
	ref, err := s.d.Client.Start(r.Context(), kind, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The run exists once Start returned. A caller may start a kind it may not
	// view, and a read can fail; the answer then names the run without the
	// view rather than failing a start that happened.
	var body any = map[string]string{"id": string(ref.ID), "kind": string(kind)}
	switch v, err := s.d.Client.Get(r.Context(), ref.ID); {
	case err == nil:
		body = runView(p, v)
	case !errors.Is(err, tasks.ErrRunNotFound):
		s.log.Warn("taskhttp: read of a run just started", "run", ref.ID, "err", err)
	}
	if ref.Existing {
		// A repeated Idempotency-Key within core.tasks.idempotency_ttl
		// replays the first run; a start that folded into a queued run of
		// the kind (dedupe) is the same answer without the replay header.
		if req.IdempotencyKey != "" {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		s.writeJSON(w, http.StatusOK, body)
		return
	}
	w.Header().Set("Location", BasePath+"/runs/"+string(ref.ID))
	s.writeJSON(w, http.StatusAccepted, body)
}

// startRequest builds the tasks.StartRequest from the body and headers. The
// actor is left zero (the executor stamps it from the principal) and the
// trigger is always manual.
func (s *Server) startRequest(w http.ResponseWriter, r *http.Request) (tasks.StartRequest, *problem) {
	req := tasks.StartRequest{Trigger: tasks.TriggerManual}
	vals := r.Header.Values("Idempotency-Key")
	switch {
	case len(vals) > 1:
		return req, &problem{Status: http.StatusBadRequest, Code: "tasks.invalid_idempotency_key", Detail: "send one Idempotency-Key header"}
	case len(vals) == 1:
		if !validIdempotencyKey(vals[0]) {
			return req, &problem{Status: http.StatusBadRequest, Code: "tasks.invalid_idempotency_key",
				Detail: "the Idempotency-Key must be 8 to 128 letters, digits, '_' or '-'"}
		}
		req.IdempotencyKey = vals[0]
	}
	raw, bad := readBody(w, r)
	if bad != nil {
		return req, bad
	}
	if len(raw) == 0 {
		return req, nil
	}
	// Rule 1 (5T.3.7): there is no dry_run/dryRun spelling, at the top level
	// or inside params. This runs before the strict decode, so the answer is
	// the specific refusal, not "unknown field".
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) == nil {
		for _, k := range []string{modeFieldSnake, modeFieldCamel} {
			if _, has := top[k]; has {
				return req, modeFieldProblem(k)
			}
		}
		if params, has := top["params"]; has && hasModeField(params) {
			return req, modeFieldProblem("params." + modeFieldSnake + "/" + modeFieldCamel)
		}
	}
	var b startBody
	if bad := decodeStrict(raw, &b); bad != nil {
		return req, bad
	}
	if b.Mode != nil {
		switch strings.ToLower(*b.Mode) {
		case "preview":
			req.Mode = tasks.ModeRequestPreview
		case "live":
			req.Mode = tasks.ModeRequestLive
		default:
			p := invalid(fieldError{Field: "/body/mode", Code: "invalid_enum", Message: `must be "preview" or "live"`})
			return req, &p
		}
	}
	if b.Priority != nil {
		pr, ok := parsePriority(*b.Priority)
		if !ok {
			p := invalid(fieldError{Field: "/body/priority", Code: "invalid_enum", Message: `must be "low", "normal", "high" or "system"`})
			return req, &p
		}
		req.Priority = pr
	}
	req.Params = b.Params
	return req, nil
}

func modeFieldProblem(field string) *problem {
	return &problem{Status: http.StatusBadRequest, Code: "tasks.mode_field_unsupported",
		Detail: field + " is not a start field: state the mode as \"mode\": \"preview\" or \"live\" in the request body"}
}
