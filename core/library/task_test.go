// file: core/library/task_test.go
// version: 1.0.0
// guid: 6b3f2f0e-5a0a-4a1f-9c53-0f4b2f5e8d71
// last-edited: 2026-11-11

package library

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/falkcorp/mediacore/tasks/taskstest/schedule"
)

// erasedTask is the executor's view of a definition (tasks.ExecutorRunners).
type erasedTask interface {
	ExecutorRunners() (decode func(json.RawMessage) (any, error),
		task *tasks.TaskSpec[any, json.RawMessage], batch *tasks.BatchSpec[any, any, json.RawMessage], fixer *tasks.FixerSpec[any, any])
}

// runRecoverTask runs def's task body the way the executor does, over a fake
// run, and returns the stored result.
func runRecoverTask(t *testing.T, def tasks.Definition, rc *tasks.RC) (TransferRecoverResult, error) {
	t.Helper()
	e, ok := def.(erasedTask)
	if !ok {
		t.Fatalf("%T does not expose its runners", def)
	}
	decode, task, _, _ := e.ExecutorRunners()
	if task == nil || task.Run == nil {
		t.Fatalf("%s has no task body", def.Kind())
	}
	p, err := decode(nil)
	if err != nil {
		t.Fatalf("decode params: %v", err)
	}
	raw, err := task.Run(rc, p)
	var res TransferRecoverResult
	if raw != nil {
		if jerr := json.Unmarshal(raw, &res); jerr != nil {
			t.Fatalf("result %s: %v", raw, jerr)
		}
	}
	return res, err
}

// The kind passes the shared conformance suite and declares what 05-library
// 5L.3.3 says: a write on core's library resource, started by
// core:library.manage, resumed from zero, scheduled from the config key and
// run once at start, in the live mode.
func TestTransferRecoverTask_Conformance(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	def := TransferRecoverTask(r.m)
	taskstest.Conformance(t, def)

	if def.Shape() != tasks.ShapeTask {
		t.Errorf("shape = %v, want ShapeTask", def.Shape())
	}
	c := tasks.CommonOf(def)
	if c.Title == "" || c.Help == "" {
		t.Errorf("Title %q and Help %q must both be set", c.Title, c.Help)
	}
	if !c.Effects.Declared() || c.Effects.IsReadOnly() ||
		!slices.Equal(c.Effects.WriteSet(), []entity.Ref{tasks.Res("core", "library")}) ||
		len(c.Effects.ReadSet()) != 0 || len(c.Effects.DeleteSet()) != 0 {
		t.Errorf("effects = writes %v reads %v deletes %v, want one write on core's library",
			c.Effects.WriteSet(), c.Effects.ReadSet(), c.Effects.DeleteSet())
	}
	if c.Perms.Start.Kind() != rbac.KindAllOf || c.Perms.Start.String() != "core:library.manage" {
		t.Errorf("Perms.Start = %v, want Need(core:library.manage)", c.Perms.Start)
	}
	if c.Resume != tasks.ResumeFromZero {
		t.Errorf("Resume = %v, want ResumeFromZero (a pass is idempotent)", c.Resume)
	}
	if c.Timeout != TransferRecoverTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, TransferRecoverTimeout)
	}
	s := c.Schedule
	if s == nil || s.ConfigKey() != TransferRecoverScheduleKey || !s.RunsOnStart() || !s.IsLive() || s.IsPreview() {
		t.Errorf("schedule = %+v, want the config key, OnStart and Live", s)
	}
}

// noteHost is a fake run that records the status line.
type noteHost struct {
	*taskstest.FakeRun
	mu    sync.Mutex
	notes []string
}

func newNoteHost(ctx context.Context) (*noteHost, *tasks.RC) {
	h := &noteHost{FakeRun: taskstest.NewFakeRun(ctx, TransferRecoverKind)}
	return h, tasks.NewRC(h)
}

func (h *noteHost) Note(msg string) { h.mu.Lock(); h.notes = append(h.notes, msg); h.mu.Unlock() }

func (h *noteHost) last() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.notes) == 0 {
		return ""
	}
	return h.notes[len(h.notes)-1]
}

// A pass over a journal that a crash left behind settles every row and reports
// it; a second pass finds nothing. The result's fields are the report's.
func TestTransferRecoverTask_RunRecoversAKilledTransfer(t *testing.T) {
	for _, s := range matrixScenarios()[:3] {
		t.Run(s.name, func(t *testing.T) {
			f := &faults{killAt: 5}
			r := s.newRig(t, f)
			_, _ = r.m.Transfer(ctxbg(), books, s.src, s.dst, s.opts)
			if r.journalRows() != 1 {
				t.Fatalf("the killed transfer left %d journal rows, want 1", r.journalRows())
			}
			r.m = r.build(rigOpts{})
			def := TransferRecoverTask(r.m)
			host, rc := newNoteHost(context.Background())

			res, err := runRecoverTask(t, def, rc)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Completed+res.RolledBack != 1 || res.Failed != 0 || res.Conflicts != 0 || res.Blocked != 0 {
				t.Errorf("result = %+v, want exactly one row settled", res)
			}
			if got, want := host.last(), RecoverReport(res).String(); got != want {
				t.Errorf("status line = %q, want %q", got, want)
			}
			s.checkSettled(t, r, "after the recovery task")

			again, err := runRecoverTask(t, def, rc)
			if err != nil || again != (TransferRecoverResult{}) {
				t.Errorf("a second pass = %+v, %v; want nothing to do", again, err)
			}
		})
	}
}

// A pass that cannot finish returns the error, wrapped so it names the library.
// The executor stores no result for a failed run, so the report reaches the
// run's record through the status line.
func TestTransferRecoverTask_RunKeepsTheReportOfAFailedPass(t *testing.T) {
	boom := errors.New("the journal is unreadable")
	m := failingRecover{rep: RecoverReport{Completed: 2, Failed: 1}, err: boom}
	host, rc := newNoteHost(context.Background())
	_, err := runRecoverTask(t, TransferRecoverTask(m), rc)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "library: transfer recovery") {
		t.Fatalf("err = %v, want it to name the library and wrap %v", err, boom)
	}
	if got, want := host.last(), "completed 2, rolled back 0, left 0, blocked 0, conflicts 0, failed 1"; got != want {
		t.Errorf("status line = %q, want %q", got, want)
	}
}

// failingRecover is a Manager whose Recover returns a canned report and error.
type failingRecover struct {
	Manager
	rep RecoverReport
	err error
}

func (f failingRecover) Recover(context.Context) (RecoverReport, error) { return f.rep, f.err }

// The task's context is the run's: a cancelled run reaches Recover.
func TestTransferRecoverTask_RunUsesTheRunContext(t *testing.T) {
	var got context.Context
	m := ctxRecover{got: &got}
	ctx, cancel := context.WithCancel(context.Background())
	_, rc := newNoteHost(ctx)
	if _, err := runRecoverTask(t, TransferRecoverTask(m), rc); err != nil {
		t.Fatal(err)
	}
	cancel()
	if got == nil || got.Err() == nil {
		t.Error("Recover did not get the run's context")
	}
}

type ctxRecover struct {
	Manager
	got *context.Context
}

func (c ctxRecover) Recover(ctx context.Context) (RecoverReport, error) {
	*c.got = ctx
	return RecoverReport{}, nil
}

// Over the schedule harness (5T.9): the kind runs once at boot, then daily
// from its key, folds while a pass is still queued, is fired again by a
// restart, and does nothing once the key is disabled. This is the sequence
// F5-14's parity test compares with HEAD's.
func TestTransferRecoverTask_ScheduleHarness(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	defs := []tasks.Definition{TransferRecoverTask(r.m)}
	day := func(d, h, m int) time.Time { return time.Date(2026, 1, d, h, m, 0, 0, time.UTC) }
	cfg := schedule.Config{Schedules: map[string]tasks.ScheduleConfig{
		TransferRecoverScheduleKey: {Enabled: true, Every: config.Duration(24 * time.Hour)},
	}}
	steps := []schedule.Step{
		{At: day(1, 12, 0)}, // first boot: the clock seeds and the on-start pass runs
		{At: day(1, 20, 0), Finish: []tasks.Kind{TransferRecoverKind}}, // a day has not passed
		{At: day(2, 12, 0)}, // due: one pass
		{At: day(3, 12, 0)}, // due again while the last pass is still queued: folds
		{At: day(4, 12, 0), Finish: []tasks.Kind{TransferRecoverKind}},                           // finished: a new pass
		{At: day(4, 13, 0), Finish: []tasks.Kind{TransferRecoverKind}, Boot: true, NoPoll: true}, // a restart runs the on-start pass again
		{At: day(5, 12, 0), Edit: func(c *schedule.Config) { // disabled by a hot config change
			c.Schedules[TransferRecoverScheduleKey] = tasks.ScheduleConfig{Enabled: false, Every: config.Duration(24 * time.Hour)}
		}},
	}
	got := schedule.Strings(schedule.Run(t, defs, cfg, steps))
	want := []string{
		"2026-01-01T12:00:00Z seed core.library.transfer-recover last_due=2026-01-01T12:00:00Z",
		"2026-01-01T12:00:00Z enqueue core.library.transfer-recover trigger=on_start",
		"2026-01-02T12:00:00Z advance core.library.transfer-recover last_due=2026-01-02T12:00:00Z",
		"2026-01-02T12:00:00Z enqueue core.library.transfer-recover trigger=schedule",
		"2026-01-03T12:00:00Z advance core.library.transfer-recover last_due=2026-01-03T12:00:00Z",
		"2026-01-03T12:00:00Z fold core.library.transfer-recover trigger=schedule",
		"2026-01-04T12:00:00Z advance core.library.transfer-recover last_due=2026-01-04T12:00:00Z",
		"2026-01-04T12:00:00Z enqueue core.library.transfer-recover trigger=schedule",
		"2026-01-04T13:00:00Z advance core.library.transfer-recover last_due=2026-01-04T13:00:00Z",
		"2026-01-04T13:00:00Z enqueue core.library.transfer-recover trigger=on_start",              // the restart
		"2026-01-05T12:00:00Z advance core.library.transfer-recover last_due=2026-01-04T13:00:00Z", // disabled: no enqueue
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sequence:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
