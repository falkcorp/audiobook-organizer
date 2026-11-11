// file: core/integrations/providerhttp/holds_test.go
// version: 1.0.0
// guid: d72dd282-21d8-49ca-b1be-eaa8526fe6fa
// last-edited: 2026-11-11

package providerhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// fakeHolds records the one call the kind makes of its backend.
type fakeHolds struct {
	calls  int
	rc     *tasks.RC
	params providerhttp.ReleaseHoldsParams
	result providerhttp.ReleaseHoldsResult
	err    error
}

func (f *fakeHolds) ReleaseHolds(rc *tasks.RC, p providerhttp.ReleaseHoldsParams) (providerhttp.ReleaseHoldsResult, error) {
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

func TestReleaseHoldsTask_Conformance(t *testing.T) {
	def := providerhttp.ReleaseHoldsTask(&fakeHolds{})
	taskstest.Conformance(t, def)

	if got, want := def.Kind(), tasks.Kind("core.integrations.release-holds"); got != want {
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
	wantRef := []entity.Ref{{Module: "core", Kind: "providerhttp"}}
	if got := c.Effects.WriteSet(); len(got) != 1 || got[0] != wantRef[0] {
		t.Errorf("WriteSet = %v, want %v", got, wantRef)
	}
	if got := c.Effects.ReadSet(); len(got) != 0 {
		t.Errorf("ReadSet = %v, want none", got)
	}
	if c.Resume != tasks.ResumeDrop {
		t.Errorf("Resume = %v, want ResumeDrop", c.Resume)
	}
	if c.Timeout != time.Minute {
		t.Errorf("Timeout = %v, want 1m", c.Timeout)
	}
	if c.Priority != tasks.PriorityHigh {
		t.Errorf("Priority = %v, want PriorityHigh", c.Priority)
	}
	if c.Schedule != nil {
		t.Errorf("Schedule must be none, got key %q", c.Schedule.ConfigKey())
	}
	if c.Perms.Start.Kind() != rbac.KindAllOf || c.Perms.Start.String() != "core:integrations.manage" {
		t.Errorf("Perms.Start = %v (kind %v), want Need(core:integrations.manage)", c.Perms.Start, c.Perms.Start.Kind())
	}
	if !c.Perms.Cancel.IsZero() || !c.Perms.Approve.IsZero() || !c.Perms.View.IsZero() || c.Perms.StartableByViewers != "" {
		t.Errorf("only Perms.Start is declared, got %+v", c.Perms)
	}
	if c.Approval != nil || c.Debounce != nil || c.Yields || len(c.NotWhile) != 0 {
		t.Errorf("no approval, debounce, yield or NotWhile is declared, got %+v", c)
	}
}

func TestReleaseHoldsTask_RunDelegatesToBackend(t *testing.T) {
	b := &fakeHolds{result: providerhttp.ReleaseHoldsResult{Listed: 3, Released: 2}}
	def := providerhttp.ReleaseHoldsTask(b)
	rc := taskstest.NewFakeRun(context.Background(), providerhttp.ReleaseHoldsKind).RC()

	raw, err := runDefinition(t, def, rc, `{}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if b.calls != 1 {
		t.Fatalf("backend called %d times, want 1", b.calls)
	}
	if b.rc != rc {
		t.Errorf("backend got a different run context than the run's")
	}
	if b.params != (providerhttp.ReleaseHoldsParams{}) {
		t.Errorf("params = %+v, want empty", b.params)
	}
	var got providerhttp.ReleaseHoldsResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("result %s: %v", raw, err)
	}
	if got != b.result {
		t.Errorf("result = %+v, want %+v", got, b.result)
	}
	if string(raw) != `{"listed":3,"released":2}` {
		t.Errorf("stored result = %s, want the listed and released counts", raw)
	}
}

func TestReleaseHoldsTask_RunPropagatesBackendError(t *testing.T) {
	boom := errors.New("gate store unreadable")
	b := &fakeHolds{err: boom}
	rc := taskstest.NewFakeRun(context.Background(), providerhttp.ReleaseHoldsKind).RC()
	raw, err := runDefinition(t, providerhttp.ReleaseHoldsTask(b), rc, "")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if raw != nil {
		t.Errorf("a failed run stores no result, got %s", raw)
	}
}

func TestReleaseHoldsTask_NilBackendFailsRegistration(t *testing.T) {
	def := providerhttp.ReleaseHoldsTask(nil)
	if len(tasks.Problems(def)) == 0 {
		t.Fatal("a nil backend must be a constructor problem so registration fails closed")
	}
}
