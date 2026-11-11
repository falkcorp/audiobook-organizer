// file: core/internal/taskhttp/schedules.go
// version: 1.0.0
// guid: 096960e8-d9c8-4d1a-ad90-4fe23647f17a
// last-edited: 2026-11-11

package taskhttp

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/tasks"
)

// scheduleDTO is one schedulable kind's timer, as its config key holds it.
type scheduleDTO struct {
	Kind      string               `json:"kind"`
	Title     string               `json:"title"`
	ConfigKey string               `json:"config_key"`
	Schedule  tasks.ScheduleConfig `json:"schedule"`
	// LockedBy names the operator layer that sets the key ("env ...",
	// "file ..."), which makes PATCH answer 409; empty when the key is free.
	LockedBy string `json:"locked_by,omitempty"`
}

// scheduleOf reads def's schedule key, or ok false when the kind has no
// configurable schedule (none, or the relay's fixed Every).
func (s *Server) scheduleOf(def tasks.Definition) (scheduleDTO, bool, error) {
	c := tasks.CommonOf(def)
	if c.Schedule == nil || c.Schedule.ConfigKey() == "" {
		return scheduleDTO{}, false, nil
	}
	key := c.Schedule.ConfigKey()
	st, found := s.d.Config.Lookup(config.Path(key))
	d := scheduleDTO{Kind: string(def.Kind()), Title: c.Title, ConfigKey: key, LockedBy: st.LockedBy}
	if !found {
		return scheduleDTO{}, false, errScheduleUndeclared(key)
	}
	if len(st.Effective) > 0 {
		if err := json.Unmarshal(st.Effective, &d.Schedule); err != nil {
			return scheduleDTO{}, false, err
		}
	}
	return d, true, nil
}

type errScheduleUndeclared string

func (e errScheduleUndeclared) Error() string {
	return "taskhttp: schedule key " + string(e) + " is not declared in config"
}

// listSchedules: GET /schedules (core:tasks.view; a definition the caller may
// not view is omitted).
func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	out := make([]scheduleDTO, 0)
	for _, k := range s.order {
		def := s.defs[k]
		if !mayView(p, def) {
			continue
		}
		d, ok, err := s.scheduleOf(def)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if ok {
			out = append(out, d)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

// patchSchedule: PATCH /schedules/{kind} (core:tasks.manage). The body is a
// JSON merge patch over the key's effective ScheduleConfig (RFC 7396: a
// member sets, null clears, an absent member stays); the result must pass
// ScheduleConfig.Validate and is written to the kind's config key through
// config.Apply, which refuses a key an operator layer sets (409) and records
// core.settings.update with the write.
func (s *Server) patchSchedule(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	k, ok := kindParam(r)
	def := s.defs[k]
	if !ok || def == nil {
		s.write(w, r, notFound("tasks.unknown_kind", "no task definition has this kind"))
		return
	}
	cur, has, err := s.scheduleOf(def)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !has {
		s.write(w, r, notFound("tasks.no_schedule", "this kind has no configurable schedule"))
		return
	}
	if cur.LockedBy != "" {
		s.fail(w, r, &config.LockedError{Path: config.Path(cur.ConfigKey), By: cur.LockedBy})
		return
	}
	raw, bad := readBody(w, r)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	next, bad := mergeSchedule(cur.Schedule, raw)
	if bad != nil {
		s.write(w, r, *bad)
		return
	}
	if err := next.Validate(); err != nil {
		s.write(w, r, invalid(fieldError{Field: "/body", Code: "invalid_schedule", Message: err.Error()}))
		return
	}
	value, err := json.Marshal(next)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, err = s.d.Config.Apply(r.Context(), config.Patch{Set: map[config.Path]json.RawMessage{config.Path(cur.ConfigKey): value}}, configActor(p))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cur.Schedule = next
	s.writeJSON(w, http.StatusOK, cur)
}

// scheduleMember reports a member of tasks.ScheduleConfig a patch may name.
func scheduleMember(name string) bool {
	switch name {
	case "enabled", "every", "daily_at", "cron", "window", "order":
		return true
	}
	return false
}

// mergeSchedule applies a merge patch to cur. A member that is not a
// ScheduleConfig member, or a body that is not an object, is a 400.
func mergeSchedule(cur tasks.ScheduleConfig, raw []byte) (tasks.ScheduleConfig, *problem) {
	if len(raw) == 0 {
		return cur, &problem{Status: http.StatusBadRequest, Code: "bad_request", Detail: "the request body must be a JSON object"}
	}
	var patch map[string]json.RawMessage
	if bad := decodeStrict(raw, &patch); bad != nil {
		return cur, bad
	}
	base, err := json.Marshal(cur)
	if err != nil {
		return cur, &problem{Status: http.StatusInternalServerError, Code: "internal"}
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return cur, &problem{Status: http.StatusInternalServerError, Code: "internal"}
	}
	for name, v := range patch {
		if !scheduleMember(name) {
			return cur, &problem{Status: http.StatusBadRequest, Code: "bad_request", Detail: "unknown schedule member " + name}
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			delete(merged, name)
			continue
		}
		merged[name] = v
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return cur, &problem{Status: http.StatusInternalServerError, Code: "internal"}
	}
	var next tasks.ScheduleConfig
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return cur, &problem{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Detail: "the schedule is not valid: " + err.Error()}
	}
	return next, nil
}

// configActor is the caller as config sees it (05-config: Subject, whether
// the credential may change protected keys, owner proof, a scope check).
func configActor(p *auth.Principal) config.Actor {
	a := config.Actor{
		Interactive: p.Method.MayChangeCredentials(),
		Owner:       p.Assurance&auth.AssuranceOwner != 0,
		Can:         func(perm string) bool { return p.Perms != nil && p.Perms.Has(permission(perm)) },
	}
	if p.User != nil {
		a.Subject = string(p.User.ID)
	}
	return a
}
