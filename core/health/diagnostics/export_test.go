// file: core/health/diagnostics/export_test.go
// version: 1.0.0
// guid: 20b184c5-2d48-477e-a3f2-dce9e82377db
// last-edited: 2026-11-11

package diagnostics_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// fakeExport records the one call the kind makes of its backend.
type fakeExport struct {
	calls  int
	rc     *tasks.RC
	params diagnostics.ExportParams
	result diagnostics.ExportResult
	err    error
}

func (f *fakeExport) Export(rc *tasks.RC, p diagnostics.ExportParams) (diagnostics.ExportResult, error) {
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

func TestExportTask_Conformance(t *testing.T) {
	def := diagnostics.ExportTask(&fakeExport{})
	taskstest.Conformance(t, def)

	if got, want := def.Kind(), tasks.Kind("core.diagnostics.export"); got != want {
		t.Fatalf("Kind = %q, want %q", got, want)
	}
	if def.Shape() != tasks.ShapeTask {
		t.Fatalf("Shape = %v, want ShapeTask", def.Shape())
	}
	c := tasks.CommonOf(def)
	if c.Title == "" || c.Help == "" {
		t.Errorf("Title %q and Help %q must both be set", c.Title, c.Help)
	}
	if !c.Effects.Declared() || !c.Effects.IsReadOnly() {
		t.Errorf("Effects must be ReadOnly, got read-only=%v declared=%v", c.Effects.IsReadOnly(), c.Effects.Declared())
	}
	if len(c.Effects.ReadSet()) != 0 || len(c.Effects.WriteSet()) != 0 || len(c.Effects.DeleteSet()) != 0 {
		t.Errorf("Effects name no resource, got reads %v writes %v deletes %v",
			c.Effects.ReadSet(), c.Effects.WriteSet(), c.Effects.DeleteSet())
	}
	if c.Resume != tasks.ResumeDrop {
		t.Errorf("Resume = %v, want ResumeDrop", c.Resume)
	}
	if c.Timeout != 30*time.Minute {
		t.Errorf("Timeout = %v, want 30m", c.Timeout)
	}
	if c.Priority != 0 {
		t.Errorf("Priority = %v, want the default (unset)", c.Priority)
	}
	if c.Schedule != nil {
		t.Errorf("Schedule must be none, got key %q", c.Schedule.ConfigKey())
	}
	if c.Perms.Start.Kind() != rbac.KindAllOf || c.Perms.Start.String() != "core:system.manage" {
		t.Errorf("Perms.Start = %v (kind %v), want Need(core:system.manage)", c.Perms.Start, c.Perms.Start.Kind())
	}
	if !c.Perms.Cancel.IsZero() || !c.Perms.Approve.IsZero() || !c.Perms.View.IsZero() || c.Perms.StartableByViewers != "" {
		t.Errorf("only Perms.Start is declared, got %+v", c.Perms)
	}
	if c.Approval != nil || c.Debounce != nil || c.Yields || len(c.NotWhile) != 0 {
		t.Errorf("no approval, debounce, yield or NotWhile is declared, got %+v", c)
	}
}

func TestExportTask_RunDelegatesToBackend(t *testing.T) {
	b := &fakeExport{result: diagnostics.ExportResult{
		File:       "fake-core.diagnostics.export.zip",
		Redactions: map[string]int{"typed": 2, "pattern": 7, "permission": 1},
	}}
	def := diagnostics.ExportTask(b)
	rc := taskstest.NewFakeRun(context.Background(), diagnostics.ExportKind).RC()

	raw, err := runDefinition(t, def, rc, `{"actor_id":"bruce","sections":["core.logs","core.tasks"]}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if b.calls != 1 {
		t.Fatalf("backend called %d times, want 1", b.calls)
	}
	if b.rc != rc {
		t.Errorf("backend got a different run context than the run's")
	}
	if b.params.ActorID != "bruce" || !slices.Equal(b.params.Sections, []string{"core.logs", "core.tasks"}) {
		t.Errorf("params = %+v, want the start request's actor and sections", b.params)
	}
	var got diagnostics.ExportResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("result %s: %v", raw, err)
	}
	if got.File != b.result.File || len(got.Redactions) != 3 ||
		got.Redactions["typed"] != 2 || got.Redactions["pattern"] != 7 || got.Redactions["permission"] != 1 {
		t.Errorf("result = %+v, want %+v", got, b.result)
	}
}

func TestExportTask_RunPropagatesBackendError(t *testing.T) {
	boom := errors.New("zip not writable")
	b := &fakeExport{err: boom}
	rc := taskstest.NewFakeRun(context.Background(), diagnostics.ExportKind).RC()
	raw, err := runDefinition(t, diagnostics.ExportTask(b), rc, "")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if raw != nil {
		t.Errorf("a failed run stores no result, got %s", raw)
	}
}

func TestExportTask_NilBackendFailsRegistration(t *testing.T) {
	def := diagnostics.ExportTask(nil)
	if len(tasks.Problems(def)) == 0 {
		t.Fatal("a nil backend must be a constructor problem so registration fails closed")
	}
}
