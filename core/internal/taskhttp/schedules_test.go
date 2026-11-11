// file: core/internal/taskhttp/schedules_test.go
// version: 1.0.0
// guid: ba797a4a-4479-4dd9-b7b2-93040fbdd6f4
// last-edited: 2026-11-11

package taskhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

func patchSchedule(kind, body string, as req) req {
	as.method, as.path, as.body = "PATCH", "/schedules/"+kind, body
	return as
}

func TestSchedules_List(t *testing.T) {
	g := newRig(t)
	g.cfg.keys[sweepKey] = config.KeyState{Path: sweepKey, Effective: json.RawMessage(`{"enabled":true,"every":"6h"}`), LockedBy: "env ABO_CORE_BATCAVE"}
	rec := g.do(req{method: "GET", path: "/schedules", as: session("alfred", scopeView)})
	var b struct{ Schedules []scheduleDTO }
	decode(t, rec, &b)
	if rec.Code != 200 || len(b.Schedules) != 1 {
		t.Fatalf("= %d %+v\n%s", rec.Code, b.Schedules, rec.Body.String())
	}
	s := b.Schedules[0]
	if s.Kind != "batcave.sweep" || s.ConfigKey != sweepKey || !s.Schedule.Enabled || s.Schedule.Every.Std().Hours() != 6 || s.LockedBy != "env ABO_CORE_BATCAVE" {
		t.Fatalf("schedule = %+v", s)
	}
	// A kind the caller may not view is omitted, not listed as forbidden.
	g = newRig(t, func(d *Deps) {
		d.Definitions = append(d.Definitions, tasks.Task(tasks.Kind("batcave.secret"), tasks.TaskSpec[struct{}, struct{}]{
			Title: "Sweep the cave's secrets", Effects: tasks.Writes(tasks.Res("batcave", "vault")),
			Perms:    tasks.Permissions{Start: rbacNeed(scopeRun), View: rbacNeed(scopeSee)},
			Resume:   tasks.ResumeContinue,
			Schedule: tasks.ScheduleFromConfig("core.batcave.schedules.secret").Live(), Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil }}))
	})
	g.cfg.keys["core.batcave.schedules.secret"] = config.KeyState{Path: "core.batcave.schedules.secret"}
	rec = g.do(req{method: "GET", path: "/schedules", as: session("alfred", scopeView)})
	decode(t, rec, &b)
	if len(b.Schedules) != 1 {
		t.Fatalf("a view-gated kind's schedule is listed: %+v", b.Schedules)
	}
	b.Schedules = nil
	rec = g.do(req{method: "GET", path: "/schedules", as: session("alfred", scopeView, scopeSee)})
	decode(t, rec, &b)
	if len(b.Schedules) != 2 {
		t.Fatalf("schedules = %+v, want both for a viewer of the gated kind", b.Schedules)
	}
}

func TestSchedules_Patch(t *testing.T) {
	manage := req{as: session("lucius", scopeManage)}
	t.Run("merge patch: sets, keeps and clears", func(t *testing.T) {
		g := newRig(t)
		rec := g.do(patchSchedule("batcave.sweep", `{"every": "12h"}`, manage))
		if rec.Code != 200 {
			t.Fatalf("= %d\n%s", rec.Code, rec.Body.String())
		}
		var got scheduleDTO
		decode(t, rec, &got)
		if !got.Schedule.Enabled || got.Schedule.Every.Std().Hours() != 12 {
			t.Fatalf("response = %+v, want enabled kept and every 12h", got.Schedule)
		}
		if len(g.cfg.applied) != 1 {
			t.Fatalf("Apply calls = %d", len(g.cfg.applied))
		}
		var written tasks.ScheduleConfig
		if err := json.Unmarshal(g.cfg.applied[0].Set[sweepKey], &written); err != nil || written.Every.Std().Hours() != 12 || !written.Enabled {
			t.Fatalf("written = %s (%v)", g.cfg.applied[0].Set[sweepKey], err)
		}
		if a := g.cfg.actors[0]; a.Subject != "lucius" || !a.Interactive || !a.Can("core:tasks.manage") || a.Can("core:other") {
			t.Fatalf("config actor = %+v", a)
		}

		// null clears: switch from every to daily_at.
		rec = g.do(patchSchedule("batcave.sweep", `{"every": null, "daily_at": "03:30"}`, manage))
		got = scheduleDTO{}
		decode(t, rec, &got)
		if rec.Code != 200 || got.Schedule.Every != 0 || got.Schedule.DailyAt != "03:30" {
			t.Fatalf("= %d %+v", rec.Code, got.Schedule)
		}
		// disable keeps the rest.
		rec = g.do(patchSchedule("batcave.sweep", `{"enabled": false}`, manage))
		got = scheduleDTO{}
		decode(t, rec, &got)
		if got.Schedule.Enabled || got.Schedule.DailyAt != "03:30" {
			t.Fatalf("= %+v", got.Schedule)
		}
	})
	t.Run("refusals that write nothing", func(t *testing.T) {
		for _, c := range []struct {
			name, kind, body string
			status           int
			code             string
		}{
			{"two timers", "batcave.sweep", `{"daily_at": "03:30"}`, 422, "validation_failed"}, // every is still set
			{"enabled with none", "batcave.sweep", `{"every": null}`, 422, "validation_failed"},
			{"bad every", "batcave.sweep", `{"every": "soon"}`, 422, "validation_failed"},
			{"bad cron", "batcave.sweep", `{"every": null, "cron": "* * *"}`, 422, "validation_failed"},
			{"unknown member", "batcave.sweep", `{"enabld": true}`, 400, "bad_request"},
			{"not an object", "batcave.sweep", `[1]`, 400, "bad_request"},
			{"empty body", "batcave.sweep", ``, 400, "bad_request"},
			{"unknown kind", "gotham.riddle", `{"enabled": false}`, 404, "tasks.unknown_kind"},
			{"kind with no schedule", "batcave.scan", `{"enabled": false}`, 404, "tasks.no_schedule"},
		} {
			t.Run(c.name, func(t *testing.T) {
				g := newRig(t)
				wantProblem(t, g.do(patchSchedule(c.kind, c.body, manage)), c.status, c.code)
				if len(g.cfg.applied) != 0 {
					t.Fatalf("a refused patch reached config: %+v", g.cfg.applied)
				}
			})
		}
	})
	t.Run("operator-locked key is 409", func(t *testing.T) {
		g := newRig(t)
		g.cfg.keys[sweepKey] = config.KeyState{Path: sweepKey, Effective: json.RawMessage(`{"enabled":true,"every":"6h"}`), LockedBy: "file batcave.toml"}
		wantProblem(t, g.do(patchSchedule("batcave.sweep", `{"enabled": false}`, manage)), 409, "tasks.schedule_locked")
		if len(g.cfg.applied) != 0 {
			t.Fatal("a locked key reached Apply")
		}
	})
	t.Run("config refusals map", func(t *testing.T) {
		for _, c := range []struct {
			err    error
			status int
			code   string
		}{
			{fmt.Errorf("apply: %w", config.ErrLocked), 409, "tasks.schedule_locked"},
			{fmt.Errorf("apply: %w", config.ErrInvalid), 422, "validation_failed"},
			{fmt.Errorf("apply: %w", config.ErrForbidden), 403, "permission_denied"},
			{errors.New("pebble: disk gone"), 500, "internal"},
		} {
			g := newRig(t)
			g.cfg.applyErr = c.err
			wantProblem(t, g.do(patchSchedule("batcave.sweep", `{"enabled": false}`, manage)), c.status, c.code)
		}
	})
	t.Run("undeclared key is an internal error", func(t *testing.T) {
		g := newRig(t)
		delete(g.cfg.keys, sweepKey)
		wantProblem(t, g.do(patchSchedule("batcave.sweep", `{"enabled": false}`, manage)), 500, "internal")
	})
}

func rbacNeed(p rbac.Permission) rbac.Requirement { return rbac.Need(p) }
