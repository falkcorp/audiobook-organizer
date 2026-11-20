// file: core/internal/taskhttp/dto.go
// version: 1.0.0
// guid: d00aab43-8e26-4627-a86e-420e13e05e71
// last-edited: 2026-11-11

package taskhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/tasks"
)

// maxBody caps a request body this package reads (httpapi's perimeter caps
// it too; a bare mount has only this).
const maxBody = 1 << 20

// Page sizes: the default and the cap match tasks.RunFilter and PageQuery.
const (
	defaultLimit = 100
	maxLimit     = 1000
)

// actorDTO is who started or acted on a run: the wire form tasks owns, shared
// with the run-changed and finished topics.
type actorDTO = tasks.ActorPayload

func actorView(a tasks.Actor) actorDTO { return tasks.NewActorPayload(a) }

// runDTO is the JSON of a tasks.RunView (which has no tags of its own): the
// same type core.task.run-changed.v1 and core.task.finished.v1 carry, so a run
// reads the same over REST and over the stream.
type runDTO = tasks.RunPayload

// runView is the ONLY way a RunView leaves this package: it passes through
// tasks.RedactRunView with the caller's checker (COR2-2), so Current is blank
// unless the caller holds core:users.view.
func runView(p *auth.Principal, v tasks.RunView) runDTO {
	return tasks.NewRunPayload(tasks.RedactRunView(p.Perms, v))
}

func runViews(p *auth.Principal, vs []tasks.RunView) []runDTO {
	out := make([]runDTO, len(vs))
	for i, v := range vs {
		out[i] = runView(p, v)
	}
	return out
}

// writeJSON sends v with status.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("taskhttp: write response", "err", err)
	}
}

// readBody reads the request body, capped at maxBody. An empty body is
// allowed and returns nil.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, *problem) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, &problem{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Detail: "the request body is too large"}
		}
		return nil, &problem{Status: http.StatusBadRequest, Code: "bad_request", Detail: "the request body could not be read"}
	}
	return bytes.TrimSpace(b), nil
}

// decodeStrict decodes raw into v and refuses an unknown field or trailing
// data. An empty raw leaves v as it is.
func decodeStrict(raw []byte, v any) *problem {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &problem{Status: http.StatusBadRequest, Code: "bad_request", Detail: "the request body is not valid for this route: " + err.Error()}
	}
	if dec.More() {
		return &problem{Status: http.StatusBadRequest, Code: "bad_request", Detail: "the request body has data after its JSON value"}
	}
	return nil
}

// limitParam reads ?limit=, default defaultLimit, at most maxLimit.
func limitParam(r *http.Request) (int, *problem) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxLimit {
		p := invalid(fieldError{Field: "/query/limit", Code: "out_of_range", Message: "must be a whole number from 1 to " + strconv.Itoa(maxLimit)})
		return 0, &p
	}
	return n, nil
}

// timeParam reads an RFC 3339 query value; absent is the zero time.
func timeParam(r *http.Request, name string) (time.Time, *problem) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		p := invalid(fieldError{Field: "/query/" + name, Code: "invalid_time", Message: "must be an RFC 3339 time"})
		return time.Time{}, &p
	}
	return t, nil
}

// kindParams reads every ?kind= as a Kind.
func kindParams(r *http.Request) ([]tasks.Kind, *problem) {
	var out []tasks.Kind
	for _, raw := range r.URL.Query()["kind"] {
		k, err := tasks.ParseKind(raw)
		if err != nil {
			p := invalid(fieldError{Field: "/query/kind", Code: "invalid_kind", Message: err.Error()})
			return nil, &p
		}
		out = append(out, k)
	}
	return out, nil
}

// stateParams reads every ?state= as a RunState from the known set.
func stateParams(r *http.Request) ([]tasks.RunState, *problem) {
	var out []tasks.RunState
	for _, raw := range r.URL.Query()["state"] {
		st := tasks.RunState(raw)
		if !knownState(st) {
			p := invalid(fieldError{Field: "/query/state", Code: "invalid_enum", Message: "unknown run state " + strconv.Quote(raw)})
			return nil, &p
		}
		out = append(out, st)
	}
	return out, nil
}

func knownState(s tasks.RunState) bool {
	switch s {
	case tasks.StateQueued, tasks.StateWaitingDeps, tasks.StateRunning, tasks.StateStopping, tasks.StateInterrupted,
		tasks.StateInterruptedQuiesced, tasks.StateAwaitingDecision, tasks.StateSucceeded, tasks.StateFailed,
		tasks.StateTimedOut, tasks.StateCanceled, tasks.StateInterruptedDropped:
		return true
	}
	return false
}
