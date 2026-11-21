// file: core/internal/scheduler/params_func_test.go
// version: 0.1.0
// guid: 4a7c2e91-6d3b-4f08-9c15-b8e0d2a6f753
// last-edited: 2026-11-21

package scheduler

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// ParamsFunc (F5-14): HEAD's acoustid_online_lookup read its limit, and
// candidate_fetch its mark-dirty flag, from config at each fire; a settings
// change reached the next run without a restart. ParamsFunc builds the
// params at each start, on every trigger.

const lookup tasks.Kind = "gotham.oracle-lookup"

const lookupKey = "modules.gotham.oracle.schedules.lookup"

type lookupParams struct {
	Limit int `json:"limit"`
}

// lookupTask is a read-only kind whose params carry a limit.
func lookupTask(s *tasks.Schedule) tasks.Definition {
	return tasks.Task(lookup, tasks.TaskSpec[lookupParams, struct{}]{
		Title: string(lookup), Effects: tasks.ReadOnly(), Resume: tasks.ResumeDrop,
		Perms: tasks.Permissions{Start: rbac.Need("gotham:run")}, Schedule: s,
		Run: func(*tasks.RC, lookupParams) (struct{}, error) { return struct{}{}, nil },
	})
}

// paramsOf is the params of each run of k, oldest first.
func paramsOf(t *testing.T, f *fixture, k tasks.Kind) []string {
	t.Helper()
	var out []string
	for _, r := range runsOf(t, f.st, k) {
		out = append(out, string(r.Params)+" "+string(r.Trigger))
	}
	return out
}

// TestParamsFunc_ReadAtEachStart: the on_start run, each interval run and
// the window's run get the value the func returns at that start.
func TestParamsFunc_ReadAtEachStart(t *testing.T) {
	limit := 5
	f := newFixture(t, []tasks.Definition{
		lookupTask(tasks.ScheduleFromConfig(lookupKey).OnStart().ParamsFunc(func() any { return lookupParams{Limit: limit} })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{lookupKey: bothCfg(6*time.Hour, 3)}, windows: nightly()})
	f.boot(day(1, 12, 0))
	f.finish(lookup, tasks.StateSucceeded)
	limit = 7
	f.poll(day(1, 18, 0))
	f.finish(lookup, tasks.StateSucceeded)
	limit = 9
	f.poll(day(2, 1, 0)) // the interval is due again as the window opens
	f.finish(lookup, tasks.StateSucceeded)
	limit = 11
	f.windowRun(day(2, 1, 1))
	got := paramsOf(t, f, lookup)
	want := []string{`{"limit":5} on_start`, `{"limit":7} schedule`, `{"limit":9} schedule`, `{"limit":11} window`}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("runs %v, want %v", got, want)
	}
}

// TestParamsFunc_EncodeErrorAdvancesAlone: a value that does not encode is a
// refused start: the clock advances alone, the error is counted once, no run
// is created, and the next poll does not retry the instant.
func TestParamsFunc_EncodeErrorAdvancesAlone(t *testing.T) {
	f := newFixture(t, []tasks.Definition{
		lookupTask(tasks.ScheduleFromConfig(lookupKey).ParamsFunc(func() any { return func() {} })),
	}, fixtureOpts{schedules: map[string]tasks.ScheduleConfig{lookupKey: every(time.Hour)}})
	f.boot(day(1, 0, 0))
	f.poll(day(1, 0, 0))
	f.poll(day(1, 1, 0))
	f.poll(day(1, 1, 30))
	f.expect(
		"01-01T00:00 seed gotham.oracle-lookup last_due=01-01T00:00",
		"01-01T01:00 advance gotham.oracle-lookup last_due=01-01T01:00",
		"01-01T01:00 failed gotham.oracle-lookup schedule",
	)
	if n := f.rec.errCount(lookup); n != 1 {
		t.Fatalf("schedule_errors = %d, want 1", n)
	}
	if rs := runsOf(t, f.st, lookup); len(rs) != 0 {
		t.Fatalf("%d runs from params that do not encode", len(rs))
	}
	if !refused(fmt.Errorf("start: %w", tasks.ErrScheduleParams)) || refused(errors.New("disk: input/output error")) {
		t.Fatal("ErrScheduleParams must be classified as a refusal")
	}
}
