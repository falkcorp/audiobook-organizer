// file: core/internal/taskhttp/runs.go
// version: 1.0.0
// guid: 93050ea4-1336-4705-9ea2-58295da8fafc
// last-edited: 2026-11-11

package taskhttp

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/tasks"
)

// planRowPrefix prefixes the stored ID of a Fixer plan row (the executor
// stores Row.ID as "plan:"+ID, since ReplaceRows replaces by prefix). The API
// names a row by the definition's Row.ID, which is what approve takes.
const planRowPrefix = "plan:"

// runID is the {id} path value.
func runID(r *http.Request) tasks.RunID { return tasks.RunID(strings.TrimSpace(r.PathValue("id"))) }

// viewable reads run id as the caller: ErrRunNotFound for a run it may not
// view, so a View-gated run is indistinguishable from a missing one.
func (s *Server) viewable(r *http.Request) (tasks.RunView, error) {
	return s.d.Client.Get(r.Context(), runID(r))
}

// listRuns: GET /runs?kind=&state=&parent=&since=&after=&limit=. next_after,
// when present, is the cursor for the next page.
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	kinds, bad := kindParams(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	states, bad := stateParams(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	since, bad := timeParam(r, "since")
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	limit, bad := limitParam(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	q := tasks.RunQuery{Kinds: kinds,
		States: states, Parent: tasks.RunID(r.URL.Query().Get("parent")), Since: since,
		After: tasks.RunID(r.URL.Query().Get("after")), Limit: limit}
	out := make([]runDTO, 0, limit)
	for v, err := range s.d.Client.Runs(r.Context(), q) {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out = append(out, runView(p, v))
		if len(out) == limit {
			break
		}
	}
	body := map[string]any{"runs": out}
	if len(out) == limit {
		body["next_after"] = out[len(out)-1].ID
	}
	s.writeJSON(w, http.StatusOK, body)
}

// getRun: GET /runs/{id}.
func (s *Server) getRun(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	v, err := s.viewable(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, runView(p, v))
}

// rowDTO is one Fixer plan row. Inputs and State are the definition's own
// bookkeeping and are not served.
type rowDTO struct {
	ID        string            `json:"id"`
	Subjects  []subjectDTO      `json:"subjects,omitempty"`
	Title     string            `json:"title,omitempty"`
	Current   map[string]string `json:"current,omitempty"`
	Proposed  map[string]string `json:"proposed,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Risk      string            `json:"risk,omitempty"`
	Class     string            `json:"class,omitempty"`
	Evidence  []string          `json:"evidence,omitempty"`
	Hold      string            `json:"hold,omitempty"`
	Retry     bool              `json:"retry,omitempty"`
	OwnerOnly bool              `json:"owner_only,omitempty"`
}

type subjectDTO struct {
	Module string `json:"module"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}

func riskName(r tasks.Risk) string {
	switch r {
	case tasks.RiskLow:
		return "low"
	case tasks.RiskMedium:
		return "medium"
	case tasks.RiskHigh:
		return "high"
	}
	return ""
}

// listRows: GET /runs/{id}/rows?after=&limit=: the plan rows of a Fixer plan
// run, in row-ID order, named by Row.ID. after is the last ID read.
func (s *Server) listRows(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	limit, bad := limitParam(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	v, err := s.viewable(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	after := r.URL.Query().Get("after")
	rows := make([]rowDTO, 0, min(limit, 64))
	more := false
	for row, err := range s.d.Runs.Rows(r.Context(), v.ID) {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		id, ok := strings.CutPrefix(row.ID, planRowPrefix)
		if !ok || (after != "" && id <= after) {
			continue
		}
		if len(rows) == limit {
			more = true
			break
		}
		d := rowDTO{
			ID: id, Title: row.Title, Current: row.Current, Proposed: row.Proposed, Reason: row.Reason, Risk: riskName(row.Risk),
			Class: row.Class, Evidence: row.Evidence, Hold: row.Hold, Retry: row.Retry, OwnerOnly: row.OwnerOnly,
		}
		for _, sub := range row.Subjects {
			d.Subjects = append(d.Subjects, subjectDTO{Module: sub.Module, Kind: sub.Kind, ID: sub.ID})
		}
		rows = append(rows, d)
	}
	body := map[string]any{"rows": rows}
	if more {
		body["next_after"] = rows[len(rows)-1].ID
	}
	s.writeJSON(w, http.StatusOK, body)
}

// itemDTO is one failed or skipped item.
type itemDTO struct {
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
	Chunk   int    `json:"chunk,omitempty"`
}

// listItems: GET /runs/{id}/items?outcome=&after=&limit=. outcome is a
// prefix ("skipped" matches every skipped:<reason>); after is the last key.
func (s *Server) listItems(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	limit, bad := limitParam(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	v, err := s.viewable(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := tasks.PageQuery{Page: tasks.PageItems, After: r.URL.Query().Get("after"), Limit: limit + 1, Outcome: r.URL.Query().Get("outcome")}
	items := make([]itemDTO, 0, min(limit, 64))
	var last string
	more := false
	for e, err := range s.d.Runs.Pages(r.Context(), v.ID, q) {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if len(items) == limit {
			more = true
			break
		}
		items = append(items, itemDTO{Key: e.Item.Key, Outcome: e.Item.Outcome, Error: e.Item.Error, Chunk: e.Item.Chunk})
		last = e.Key
	}
	body := map[string]any{"items": items}
	if more {
		body["next_after"] = last
	}
	s.writeJSON(w, http.StatusOK, body)
}

// logLineDTO is one line of a run's log.
type logLineDTO struct {
	At      time.Time         `json:"at"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// getLog: GET /runs/{id}/log?limit=. Client.Log applies the definition's
// View rule. The log has no cursor yet (a LogLine carries no key), so the
// page is the first limit lines.
func (s *Server) getLog(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	limit, bad := limitParam(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	lines := make([]logLineDTO, 0, min(limit, 64))
	for l, err := range s.d.Client.Log(r.Context(), runID(r), tasks.PageQuery{Page: tasks.PageLog, Limit: limit}) {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		lines = append(lines, logLineDTO{At: l.At, Level: l.Level, Message: l.Message, Fields: l.Fields})
		if len(lines) == limit {
			break
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// getCounts: GET /counts (core:tasks.view): runs by state.
func (s *Server) getCounts(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	c, err := s.d.Runs.Counts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	counts := make(map[string]int64, len(c))
	var total int64
	for st, n := range c {
		counts[string(st)] = n
		total += n
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "total": total})
}

// getTimeline: GET /timeline?kind=&state=&from=&to=&limit= (core:tasks.view;
// Client.Timeline filters by each definition's View).
func (s *Server) getTimeline(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	kinds, bad := kindParams(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	states, bad := stateParams(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	from, bad := timeParam(r, "from")
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	to, bad := timeParam(r, "to")
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	limit, bad := limitParam(r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	vs, err := s.d.Client.Timeline(r.Context(), tasks.TimelineQuery{Kinds: kinds, States: states, From: from, To: to, Limit: limit})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"runs": runViews(p, slices.Clone(vs))})
}

// pauseDTO is the operator pause marker.
type pauseDTO struct {
	Paused bool      `json:"paused"`
	Reason string    `json:"reason,omitempty"`
	By     *actorDTO `json:"by,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

func (s *Server) pauseState(w http.ResponseWriter, r *http.Request, status int) {
	pa, paused, err := s.d.Client.PauseState(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := pauseDTO{Paused: paused}
	if paused {
		by := actorView(pa.By)
		d.Reason, d.By, d.At = pa.Reason, &by, pa.At
	}
	s.writeJSON(w, status, d)
}

// getPause: GET /pause (core:tasks.view).
func (s *Server) getPause(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	s.pauseState(w, r, http.StatusOK)
}

// maxPauseReason bounds the free text of a pause.
const maxPauseReason = 512

// pause: POST /pause {"reason": "..."} (core:tasks.manage).
func (s *Server) pause(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	raw, bad := readBody(w, r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	var b struct {
		Reason string `json:"reason"`
	}
	if bad := decodeStrict(raw, &b); bad != nil {
		s.write(w, r, *bad)
		return
	}
	if len(b.Reason) > maxPauseReason {
		s.write(w, r, invalid(fieldError{Field: "/body/reason", Code: "too_long", Message: "must be at most 512 bytes"}))
		return
	}
	if err := s.d.Client.Pause(r.Context(), b.Reason); err != nil {
		s.fail(w, r, err)
		return
	}
	s.pauseState(w, r, http.StatusOK)
}

// unpause: POST /unpause (core:tasks.manage).
func (s *Server) unpause(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
	if err := s.d.Client.Unpause(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.pauseState(w, r, http.StatusOK)
}
