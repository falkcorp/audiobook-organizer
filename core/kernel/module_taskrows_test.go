// file: core/kernel/module_taskrows_test.go
// version: 0.1.0
// guid: 9e3c5a17-2f84-4b60-a1d9-7c0b4e82f635
// last-edited: 2026-11-28

package kernel

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

const signalKind tasks.Kind = "robin.signal"

// rowsFixture boots gotham and robin, each with a kind, and stores (never runs:
// the executor is not started) a queued run of each plus a gotham plan with two
// stored rows and a gotham apply run that names it.
type rowsFixture struct {
	k               *Kernel
	gotham, robin   *module.Deps
	patrolRun       tasks.RunID
	signalRun       tasks.RunID
	plan, apply     tasks.RunID
	foreignApply    tasks.RunID // a gotham run whose plan_ref is robin's run
	wantState       json.RawMessage
	robinRowsBefore int
}

func newRowsFixture(t *testing.T) *rowsFixture {
	t.Helper()
	g, gDeps := patroller(nil)
	var rDeps module.Deps
	robin := mod("robin", func(r *module.Registrar) error {
		rDeps = r.Deps()
		r.Tasks(tasks.Task(signalKind, tasks.TaskSpec[struct{}, struct{}]{
			Title: "Signal", Effects: tasks.ReadOnly(), Perms: tasks.Permissions{Start: rbac.Need("robin:signal")},
			Resume: tasks.ResumeFromZero, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
		}))
		return r.Err()
	}).with(func(m *module.Manifest) {
		m.TaskLedger = []tasks.Kind{signalKind}
		m.Permissions = []rbac.PermissionSpec{{Name: "robin:signal", Title: "Signal"}}
	})
	c, _ := taskConfig(t, g, robin)
	k := mustNew(t, c)
	f := &rowsFixture{k: k, gotham: gDeps, robin: &rDeps, wantState: json.RawMessage(`{"book":"year-one"}`)}

	runs := k.tasks.stores.Runs
	insert := func(kind tasks.Kind, planRef tasks.RunID) tasks.RunID {
		id, err := runs.NewID(t.Context())
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		rec := tasks.RunRecord{RunView: tasks.RunView{ID: id, Kind: kind, Mode: tasks.ModeLive, State: tasks.StateQueued,
			Actor: tasks.Actor{AuthMethod: "system", System: "core"}, Trigger: tasks.TriggerManual, Created: time.Now(), PlanRef: planRef},
			Priority: tasks.PriorityNormal, Params: json.RawMessage(`{}`)}
		if err := k.commitTaskBatch(t.Context(), func(b *storage.Batch) error { return runs.Insert(t.Context(), b, rec) }); err != nil {
			t.Fatalf("Insert %s: %v", kind, err)
		}
		return id
	}
	f.patrolRun = insert(patrolKind, "")
	f.signalRun = insert(signalKind, "")
	f.plan = insert(patrolKind, "")
	f.apply = insert(patrolKind, f.plan)
	f.foreignApply = insert(patrolKind, f.signalRun)
	rows := []tasks.Row{
		{ID: "plan:gordon", State: f.wantState},
		{ID: "plan:alfred", State: json.RawMessage(`{"book":"the-long-halloween"}`)},
		{ID: "not-a-plan-row", State: json.RawMessage(`{"book":"never"}`)},
	}
	if err := runs.ReplaceRows(t.Context(), f.plan, "plan:", 2, func(i int) (tasks.Row, error) { return rows[i], nil }); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	if err := runs.ReplaceRows(t.Context(), f.signalRun, "sig:", 1, func(int) (tasks.Row, error) { return tasks.Row{ID: "sig:oracle", State: json.RawMessage(`{}`)}, nil }); err != nil {
		t.Fatalf("robin rows: %v", err)
	}
	for _, err := range runs.Rows(t.Context(), f.signalRun) {
		if err != nil {
			t.Fatalf("robin rows: %v", err)
		}
		f.robinRowsBefore++
	}
	return f
}

func yieldedIDs(t *testing.T, seq func(yield func(tasks.RunRecord, error) bool)) []tasks.RunID {
	t.Helper()
	var ids []tasks.RunID
	seq(func(r tasks.RunRecord, err error) bool {
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		ids = append(ids, r.ID)
		return true
	})
	return ids
}

// An empty query is the module's own kinds, never every kind in the store.
func TestModuleTaskRows_ListIsScopedToTheModulesOwnKinds(t *testing.T) {
	f := newRowsFixture(t)
	got := yieldedIDs(t, f.gotham.TaskRows.List(t.Context(), tasks.RunQuery{}))
	if !slices.Contains(got, f.patrolRun) {
		t.Fatalf("gotham's own run is missing from %v", got)
	}
	if slices.Contains(got, f.signalRun) {
		t.Fatalf("gotham listed robin's run %s: %v", f.signalRun, got)
	}
	named := yieldedIDs(t, f.gotham.TaskRows.List(t.Context(), tasks.RunQuery{Kinds: []tasks.Kind{patrolKind}}))
	if !slices.Contains(named, f.patrolRun) || slices.Contains(named, f.signalRun) {
		t.Fatalf("gotham naming its own kind got %v", named)
	}
}

// A run of another module's kind, and a query naming one, are refused by every
// method, and robin's rows are untouched by the refused write.
func TestModuleTaskRows_RefusesAnotherModulesRuns(t *testing.T) {
	f := newRowsFixture(t)
	tr := f.gotham.TaskRows
	ctx := t.Context()

	for _, err := range tr.List(ctx, tasks.RunQuery{Kinds: []tasks.Kind{signalKind}}) {
		if !errors.Is(err, module.ErrTaskRowsForeign) {
			t.Fatalf("List of robin's kind = %v, want ErrTaskRowsForeign", err)
		}
	}
	for _, err := range tr.List(ctx, tasks.RunQuery{Kinds: []tasks.Kind{patrolKind, signalKind}}) {
		if !errors.Is(err, module.ErrTaskRowsForeign) {
			t.Fatalf("List of an own and a foreign kind = %v, want ErrTaskRowsForeign", err)
		}
	}
	for _, err := range tr.Rows(ctx, f.signalRun) {
		if !errors.Is(err, module.ErrTaskRowsForeign) {
			t.Fatalf("Rows of robin's run = %v, want ErrTaskRowsForeign", err)
		}
	}
	if err := tr.ReplaceRows(ctx, f.signalRun, "sig:", 0, nil); !errors.Is(err, module.ErrTaskRowsForeign) {
		t.Fatalf("ReplaceRows of robin's run = %v, want ErrTaskRowsForeign", err)
	}
	if _, err := tr.PlanOf(ctx, f.signalRun); !errors.Is(err, module.ErrTaskRowsForeign) {
		t.Fatalf("PlanOf robin's run = %v, want ErrTaskRowsForeign", err)
	}
	if _, _, err := tr.PlannedRow(ctx, f.signalRun, "oracle"); !errors.Is(err, module.ErrTaskRowsForeign) {
		t.Fatalf("PlannedRow of robin's run = %v, want ErrTaskRowsForeign", err)
	}
	// An own apply run whose plan_ref names another module's run reads nothing.
	if _, _, err := tr.PlannedRow(ctx, f.foreignApply, "oracle"); !errors.Is(err, module.ErrTaskRowsForeign) {
		t.Fatalf("PlannedRow through a foreign plan = %v, want ErrTaskRowsForeign", err)
	}
	n := 0
	for _, err := range f.k.tasks.stores.Runs.Rows(ctx, f.signalRun) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != f.robinRowsBefore {
		t.Fatalf("robin's run has %d rows after the refused calls, had %d", n, f.robinRowsBefore)
	}
	// And robin reads its own.
	if _, err := f.robin.TaskRows.PlanOf(ctx, f.signalRun); err != nil {
		t.Fatalf("robin's PlanOf of its own run: %v", err)
	}
}

// PlanOf and PlannedRow over the module's own plan and apply runs: the stored
// State byte for byte, only rows under the plan prefix, found=false for a row
// the plan does not hold.
func TestModuleTaskRows_PlannedRowReadsTheApplyRunsPlan(t *testing.T) {
	f := newRowsFixture(t)
	tr := f.gotham.TaskRows
	ctx := t.Context()

	if plan, err := tr.PlanOf(ctx, f.apply); err != nil || plan != string(f.plan) {
		t.Fatalf("PlanOf = %q, %v; want %q", plan, err, f.plan)
	}
	state, found, err := tr.PlannedRow(ctx, f.apply, "gordon")
	if err != nil || !found || string(state) != string(f.wantState) {
		t.Fatalf("PlannedRow(gordon) = %s, %v, %v; want %s", state, found, err, f.wantState)
	}
	if _, found, err := tr.PlannedRow(ctx, f.apply, "joker"); err != nil || found {
		t.Fatalf("PlannedRow(joker) = found %v, %v; want not found", found, err)
	}
	// A row stored outside the plan prefix is not the plan's.
	if _, found, err := tr.PlannedRow(ctx, f.apply, "not-a-plan-row"); err != nil || found {
		t.Fatalf("PlannedRow(not-a-plan-row) = found %v, %v; want not found", found, err)
	}
	// An apply run that names no plan reads nothing, and says why.
	if _, _, err := tr.PlannedRow(ctx, f.patrolRun, "gordon"); err == nil {
		t.Fatal("PlannedRow of a run with no plan_ref succeeded")
	}
	if plan, err := tr.PlanOf(ctx, f.patrolRun); err != nil || plan != "" {
		t.Fatalf("PlanOf(a run with no plan) = %q, %v; want an empty plan", plan, err)
	}
}

// The handle answers ErrTaskRowsNotBound until the kernel builds the store, and
// for ever in an app that builds no engine.
func TestModuleTaskRows_NotBoundWithoutAnEngine(t *testing.T) {
	var deps module.Deps
	m := mod("gotham", func(r *module.Registrar) error { deps = r.Deps(); return r.Err() })
	c, _ := taskConfig(t, m)
	mustNew(t, c)
	if _, err := deps.TaskRows.PlanOf(t.Context(), "r1"); !errors.Is(err, module.ErrTaskRowsNotBound) {
		t.Fatalf("PlanOf = %v, want ErrTaskRowsNotBound", err)
	}
	for _, err := range deps.TaskRows.List(t.Context(), tasks.RunQuery{}) {
		if !errors.Is(err, module.ErrTaskRowsNotBound) {
			t.Fatalf("List = %v, want ErrTaskRowsNotBound", err)
		}
	}
}
