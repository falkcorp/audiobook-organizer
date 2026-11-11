// file: core/integrations/ai/batchpoll_test.go
// version: 1.0.0
// guid: 1a195828-744b-4db6-99d6-c99842ee39a1
// last-edited: 2026-11-11

package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// fakePoll records the one call the kind makes of its backend.
type fakePoll struct {
	calls  int
	rc     *tasks.RC
	params ai.BatchPollParams
	result ai.BatchPollResult
	err    error
}

func (f *fakePoll) PollBatches(rc *tasks.RC, p ai.BatchPollParams) (ai.BatchPollResult, error) {
	f.calls++
	f.rc, f.params = rc, p
	return f.result, f.err
}

// erasedTask is the hidden method the executor reads a definition's body
// through (core/tasks/erased.go); the test runs the body the way the executor
// does.
type erasedTask interface {
	ExecutorRunners() (decode func(json.RawMessage) (any, error),
		task *tasks.TaskSpec[any, json.RawMessage], batch *tasks.BatchSpec[any, any, json.RawMessage], fixer *tasks.FixerSpec[any, any])
}

func runDefinition(t *testing.T, def tasks.Definition, rc *tasks.RC, params string) (json.RawMessage, error) {
	t.Helper()
	e, ok := def.(erasedTask)
	if !ok {
		t.Fatalf("%T does not expose its runners", def)
	}
	decode, task, _, _ := e.ExecutorRunners()
	if task == nil || task.Run == nil {
		t.Fatalf("%s has no task body", def.Kind())
	}
	p, err := decode(json.RawMessage(params))
	if err != nil {
		t.Fatalf("decode params: %v", err)
	}
	return task.Run(rc, p)
}

func TestBatchPollTask_Conformance(t *testing.T) {
	def := ai.BatchPollTask(&fakePoll{})
	taskstest.Conformance(t, def)

	if got, want := def.Kind(), tasks.Kind("core.ai.batch-poll"); got != want {
		t.Fatalf("Kind = %q, want %q", got, want)
	}
	if def.Shape() != tasks.ShapeTask {
		t.Fatalf("Shape = %v, want ShapeTask", def.Shape())
	}
	c := tasks.CommonOf(def)
	if c.Title == "" || c.Help == "" {
		t.Errorf("Title %q and Help %q must both be set", c.Title, c.Help)
	}
	if !c.Effects.Declared() || c.Effects.IsReadOnly() {
		t.Errorf("Effects must be Writes, got read-only=%v declared=%v", c.Effects.IsReadOnly(), c.Effects.Declared())
	}
	wantRef := entity.Ref{Module: "core", Kind: "ai"}
	if got := c.Effects.WriteSet(); len(got) != 1 || got[0] != wantRef {
		t.Errorf("WriteSet = %v, want [%v]", got, wantRef)
	}
	if got := c.Effects.ReadSet(); len(got) != 0 {
		t.Errorf("ReadSet = %v, want none", got)
	}
	if c.Resume != tasks.ResumeFromZero {
		t.Errorf("Resume = %v, want ResumeFromZero", c.Resume)
	}
	if c.Timeout != 10*time.Minute {
		t.Errorf("Timeout = %v, want 10m", c.Timeout)
	}
	if c.Priority != 0 {
		t.Errorf("Priority = %v, want the default (unset)", c.Priority)
	}
	if c.Perms.Start.Kind() != rbac.KindAllOf || c.Perms.Start.String() != "core:integrations.manage" {
		t.Errorf("Perms.Start = %v (kind %v), want Need(core:integrations.manage)", c.Perms.Start, c.Perms.Start.Kind())
	}
	if !c.Perms.Cancel.IsZero() || !c.Perms.Approve.IsZero() || !c.Perms.View.IsZero() || c.Perms.StartableByViewers != "" {
		t.Errorf("only Perms.Start is declared, got %+v", c.Perms)
	}
	s := c.Schedule
	if s == nil {
		t.Fatal("Schedule must be tasks.ScheduleFromConfig(core.ai.schedules.batch_poll).Live()")
	}
	if s.ConfigKey() != "core.ai.schedules.batch_poll" {
		t.Errorf("schedule key = %q, want core.ai.schedules.batch_poll", s.ConfigKey())
	}
	if !s.IsLive() || s.IsPreview() {
		t.Errorf("schedule live=%v preview=%v, want live only", s.IsLive(), s.IsPreview())
	}
	if s.RunsOnStart() || s.Interval() != 0 || len(s.RunParams()) != 0 {
		t.Errorf("the schedule's timing comes from the config key alone, got onStart=%v every=%v params=%s",
			s.RunsOnStart(), s.Interval(), s.RunParams())
	}
	if c.Approval != nil || c.Debounce != nil || c.Yields || len(c.NotWhile) != 0 {
		t.Errorf("no approval, debounce, yield or NotWhile is declared, got %+v", c)
	}
}

func TestBatchPollTask_RunDelegatesToBackend(t *testing.T) {
	b := &fakePoll{result: ai.BatchPollResult{Polled: 5, ApplyStaged: 2, Failed: 1}}
	def := ai.BatchPollTask(b)
	rc := taskstest.NewFakeRun(context.Background(), ai.BatchPollKind).RC()

	raw, err := runDefinition(t, def, rc, "null")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if b.calls != 1 {
		t.Fatalf("backend called %d times, want 1", b.calls)
	}
	if b.rc != rc {
		t.Errorf("backend got a different run context than the run's")
	}
	if b.params != (ai.BatchPollParams{}) {
		t.Errorf("params = %+v, want empty", b.params)
	}
	var got ai.BatchPollResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("result %s: %v", raw, err)
	}
	if got != b.result {
		t.Errorf("result = %+v, want %+v", got, b.result)
	}
	if string(raw) != `{"polled":5,"apply_staged":2,"failed":1}` {
		t.Errorf("stored result = %s, want the polled, apply_staged and failed counts", raw)
	}
}

func TestBatchPollTask_RunPropagatesBackendError(t *testing.T) {
	boom := errors.New("job store unreadable")
	b := &fakePoll{err: boom}
	rc := taskstest.NewFakeRun(context.Background(), ai.BatchPollKind).RC()
	raw, err := runDefinition(t, ai.BatchPollTask(b), rc, "")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if raw != nil {
		t.Errorf("a failed run stores no result, got %s", raw)
	}
}

func TestBatchPollTask_NilBackendFailsRegistration(t *testing.T) {
	def := ai.BatchPollTask(nil)
	if len(tasks.Problems(def)) == 0 {
		t.Fatal("a nil backend must be a constructor problem so registration fails closed")
	}
}
