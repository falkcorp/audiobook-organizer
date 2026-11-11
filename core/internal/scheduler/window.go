// file: core/internal/scheduler/window.go
// version: 1.0.0
// guid: 595ea63d-e3fd-4e96-98e7-440412a07142
// last-edited: 2026-11-11

package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// Maintenance windows (05-tasks 5T.3.8, 5T.8.2 row maintenance.window). A
// window is core.tasks.windows.<name>: hours in core.tasks.timezone and an
// enable flag. Its kind, core.window.<name> (declared by core/tasks, the one
// owner), is fired by this scheduler once per opening, and its run starts the
// window's members, the kinds whose ScheduleConfig.Window names it, one after
// another in (Order, kind) order. A member's own clock is not polled.
//
// Once per opening, by date: the window's clock fires when the poll is inside
// the window and the local date of its LastDue is before the local date of
// the opening it is in. This is HEAD's rule (maintenance.go's hasRunToday
// against maintenance_window_last_run, a local YYYY-MM-DD), which the tasks
// cutover imports as LastDue = local midnight of that date (5T.8.3), so a
// window that ran on day D is not run again on D after the cutover. Measuring
// against the opening's date, not the poll's, keeps a window that wraps
// midnight (23-4) from firing again after midnight within one opening.

// windowOpen reports whether now is inside w, and the instant the opening it
// is in began. The hours wrap midnight when End <= Start (Start == End is the
// whole day), as HEAD's IsInMaintenanceWindowAt.
func windowOpen(now time.Time, w WindowConfig, loc *time.Location) (in bool, opened time.Time) {
	l := now.In(loc)
	h := l.Hour()
	y, mo, d := l.Date()
	if w.Start < w.End {
		if h < w.Start || h >= w.End {
			return false, time.Time{}
		}
		return true, wallInstant(y, mo, d, w.Start, 0, loc)
	}
	switch {
	case h >= w.Start:
		return true, wallInstant(y, mo, d, w.Start, 0, loc)
	case h < w.End:
		return true, wallInstant(y, mo, d-1, w.Start, 0, loc)
	}
	return false, time.Time{}
}

// localDate is t's calendar date in loc, comparable as an int.
func localDate(t time.Time, loc *time.Location) int {
	y, mo, d := t.In(loc).Date()
	return y*10000 + int(mo)*100 + d
}

// decideWindow is one due check of a window's clock.
func decideWindow(prev tasks.Clock, had bool, now time.Time, w WindowConfig, loc *time.Location) decision {
	hash := hashOf("window_hours", fmt.Sprint(w.Start, w.End), loc.String())
	if !had {
		// First sight seeds, inside the window or not: seeded outside, the
		// window runs at its next opening; seeded inside, not before the
		// next one (no boot stampede).
		return decision{write: true, next: tasks.Clock{LastDue: now, ScheduleHash: hash}}
	}
	in, opened := windowOpen(now, w, loc)
	if !w.Enabled || !in {
		return decision{}
	}
	if localDate(prev.LastDue, loc) >= localDate(opened, loc) {
		return decision{}
	}
	next := prev
	next.LastDue = now
	next.ScheduleHash = hash
	return decision{write: true, next: next, fire: true, dueAt: opened}
}

// validWindowHours reports hours inside 0-23.
func validWindowHours(w WindowConfig) bool {
	return w.Start >= 0 && w.Start <= 23 && w.End >= 0 && w.End <= 23
}

// pollWindow is one due check of window name.
func (s *Scheduler) pollWindow(ctx context.Context, name string, now time.Time) {
	kind := tasks.WindowKind(name)
	w, ok := s.opts.Config.Window(name)
	if !ok || !w.Enabled {
		return
	}
	if !validWindowHours(w) {
		s.scheduleError(kind, fmt.Errorf("%w: window %s hours %d-%d are outside 0-23", errConfig, name, w.Start, w.End))
		return
	}
	members := s.members(name)
	start := func(ctx context.Context, b *storage.Batch) (tasks.RunRef, bool, error) {
		ref, ok, err := s.activeRun(ctx, kind, tasks.ModeLive)
		if err != nil || ok {
			return ref, ok, err
		}
		ref, err = s.opts.Starter.StartIn(ctx, b, kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerWindow})
		return ref, false, err
	}
	var obs Observer
	if s.opts.Observer != nil {
		obs = &windowObserver{Observer: s.opts.Observer, name: name, members: members}
	}
	s.commitFire(ctx, kind, tasks.TriggerWindow, func(prev tasks.Clock, had bool) (decision, error) {
		return decideWindow(prev, had, now, w, s.opts.Location), nil
	}, start, obs)
}

// member is one window member, as the current config states it.
type member struct {
	e     entry
	order int
}

// members are the kinds whose current ScheduleConfig is enabled and joins
// window name, not held, in (Order, kind) order (HEAD's MaintenanceOrder).
func (s *Scheduler) members(name string) []member {
	var out []member
	for _, e := range s.entries {
		if s.held(e.kind) || e.sched.ConfigKey() == "" {
			continue
		}
		c, err := s.opts.Config.Schedule(e.sched.ConfigKey())
		if err != nil || !c.Enabled || c.Window != name {
			continue
		}
		if _, err := effectiveOf(c, s.opts.Poll, s.opts.Location); err != nil {
			continue
		}
		out = append(out, member{e: e, order: c.Order})
	}
	slices.SortStableFunc(out, func(a, b member) int {
		if a.order != b.order {
			return a.order - b.order
		}
		return cmpKind(a.e.kind, b.e.kind)
	})
	return out
}

// Members are the kinds window name's run would start now, in order: the
// window's reachability, which the schedule-parity harness compares with
// HEAD's maintenance order.
func (s *Scheduler) Members(name string) []tasks.Kind {
	ms := s.members(name)
	out := make([]tasks.Kind, len(ms))
	for i, m := range ms {
		out[i] = m.e.kind
	}
	return out
}

// windowHost is what the window body needs of its run; *tasks.RC provides it.
type windowHost interface {
	Context() context.Context
	RunID() tasks.RunID
	Kind() tasks.Kind
	Trigger() tasks.Trigger
	Await(ref tasks.RunRef) (tasks.Outcome, error)
	Log() *slog.Logger
}

// RunWindow is the body of a window kind's run (WindowDefinition): it starts
// the window's members one after another in order and waits for each, as
// HEAD's maintenance.window op did. A member is skipped when its When gate
// is closed or a run of it is already active (HEAD: "already running,
// skipping"); a scheduled run stops starting members once the window has
// closed (a manual start, HEAD's ignore_window, runs them all). Each member is
// started through the executor's StartIn, so it runs as its own module's
// system actor, with the window run as its parent and trigger window. It
// returns an error naming the members that failed.
func (s *Scheduler) RunWindow(rc *tasks.RC) error { return s.runWindow(rc) }

func (s *Scheduler) runWindow(h windowHost) error {
	ctx := h.Context()
	name, ok := tasks.WindowName(h.Kind())
	if !ok {
		return fmt.Errorf("scheduler: %s is not a window kind", h.Kind())
	}
	scheduled := h.Trigger() == tasks.TriggerWindow
	members := s.members(name)
	h.Log().Info("maintenance window starting", "window", name, "members", len(members))
	var failed []string
	for i, m := range members {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		if scheduled {
			w, ok := s.opts.Config.Window(name)
			if in, _ := windowOpen(s.opts.Now(), w, s.opts.Location); !ok || !w.Enabled || !in {
				rest := make([]string, 0, len(members)-i)
				for _, r := range members[i:] {
					rest = append(rest, string(r.e.kind))
				}
				h.Log().Warn("maintenance window closed; members not started", "window", name, "skipped", strings.Join(rest, ", "))
				break
			}
		}
		if !m.e.sched.Open() {
			h.Log().Info("window member gated off; skipped", "kind", string(m.e.kind))
			continue
		}
		start := s.startFor(m.e, tasks.TriggerWindow, h.RunID())
		var ref tasks.RunRef
		var folded bool
		err := s.opts.Starter.WriteRuns(ctx, s.open, func(ctx context.Context, b *storage.Batch) error {
			var err error
			ref, folded, err = start(ctx, b)
			return err
		})
		if o := s.opts.Observer; o != nil {
			o.Fired(Fire{Kind: m.e.kind, Trigger: tasks.TriggerWindow, Run: ref, Folded: folded, Err: err, Parent: h.RunID()})
		}
		if err != nil {
			h.Log().Warn("window member not started", "kind", string(m.e.kind), "err", err)
			failed = append(failed, string(m.e.kind))
			continue
		}
		if folded {
			h.Log().Info("window member already active; skipped", "kind", string(m.e.kind), "run", string(ref.ID))
			continue
		}
		out, err := h.Await(ref)
		if err != nil {
			return err
		}
		if out.State != tasks.StateSucceeded {
			failed = append(failed, string(m.e.kind))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("scheduler: window %s: %d of %d members did not succeed: %s",
			name, len(failed), len(members), strings.Join(failed, ", "))
	}
	return nil
}

// WindowDefinition is the definition of window name's kind,
// tasks.WindowKind(name) (core/tasks declares the kind; this package runs it).
// run is its body: the kernel passes a closure over the Scheduler's RunWindow,
// built after the executor that registers this definition (the scheduler's
// Starter is that executor). The run is core plumbing: PrioritySystem,
// read-only itself (each member declares its own effects), started by
// core:tasks.manage, dropped rather than resumed after a crash (HEAD's
// ResumeDrop; the next opening runs again), at most 12 hours (HEAD's
// timeout).
//
//tasklint:constructor the kernel passes the result to Registrar.Tasks for each core.tasks.windows entry (F2-16); this package registers nothing
func WindowDefinition(name string, run func(rc *tasks.RC) error) tasks.Definition {
	return tasks.Task(tasks.WindowKind(name), tasks.TaskSpec[struct{}, struct{}]{
		Title:    "Maintenance window " + name,
		Help:     "Runs the kinds scheduled into this window, in their order, once per opening.",
		Effects:  tasks.ReadOnly(),
		Perms:    tasks.Permissions{Start: rbac.Need("core:tasks.manage")},
		Resume:   tasks.ResumeDrop,
		Priority: tasks.PrioritySystem,
		Timeout:  12 * time.Hour,
		Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
			if run == nil {
				return struct{}{}, fmt.Errorf("scheduler: window %s has no body", name)
			}
			return struct{}{}, run(rc)
		},
	})
}

// windowObserver adds the WindowOpened call to a window's fire.
type windowObserver struct {
	Observer
	name    string
	members []member
}

func (w *windowObserver) Fired(f Fire) {
	if f.Err == nil && !f.Folded {
		ks := make([]tasks.Kind, len(w.members))
		for i, m := range w.members {
			ks[i] = m.e.kind
		}
		w.Observer.WindowOpened(w.name, ks)
	}
	w.Observer.Fired(f)
}
