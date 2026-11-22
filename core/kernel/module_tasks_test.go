// file: core/kernel/module_tasks_test.go
// version: 1.0.0
// guid: 6a2f9d13-b84c-4e07-95a1-d3c8e0b71f52
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

const cleanupKind tasks.Kind = "arkham.cleanup"

// arkham is a module whose only trace is its ledger: disabled by default, it
// registers nothing and its kind is known by name only.
func arkham() testModule {
	return mod("arkham", nil).with(func(m *module.Manifest) {
		m.DefaultEnabled = false
		m.TaskLedger = []tasks.Kind{cleanupKind}
	})
}

// patroller is gotham with the patrol kind, the starts it may make and the
// Deps its Register saw (set once Register ran).
func patroller(starts []tasks.Kind, more ...func(r *module.Registrar)) (testModule, *module.Deps) {
	var deps module.Deps
	m := mod("gotham", func(r *module.Registrar) error {
		deps = r.Deps()
		r.Tasks(patrol())
		for _, f := range more {
			f(r)
		}
		return r.Err()
	}).with(func(m *module.Manifest) {
		m.TaskLedger = []tasks.Kind{patrolKind}
		m.TaskStarts = starts
		m.Permissions = []rbac.PermissionSpec{{Name: "gotham:patrol", Title: "Patrol"}}
	})
	return m, &deps
}

// TestTaskStarts_DisabledTargetModuleBootsAndRefusesAtRuntime (SEC2-15,
// COR3-10): a TaskStarts entry that names a kind of a DISABLED module is a valid
// entry: the app boots, and the start is refused at run time as held
// (executor.ErrHeld), not at boot.
func TestTaskStarts_DisabledTargetModuleBootsAndRefusesAtRuntime(t *testing.T) {
	g, deps := patroller([]tasks.Kind{cleanupKind})
	c, _ := taskConfig(t, g, arkham())
	k := mustNew(t, c)
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatalf("StartTasks: %v", err)
	}
	_, err := deps.Tasks.Start(t.Context(), cleanupKind, tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if !errors.Is(err, executor.ErrHeld) {
		t.Fatalf("gotham starting the disabled module's kind: %v, want executor.ErrHeld", err)
	}
}

// TestTaskStarts_UnknownKindRefusedAtBoot: an entry no manifest ledger and no
// registration names is ProblemTaskStartUnknown, as is one that is not a kind.
func TestTaskStarts_UnknownKindRefusedAtBoot(t *testing.T) {
	for name, starts := range map[string][]tasks.Kind{
		"unregistered kind":   {"arkham.escape"},
		"not a kind":          {"arkham"},
		"unknown module":      {"blackgate.riot"},
		"known, then unknown": {cleanupKind, "arkham.riot"},
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := patroller(starts)
			c, _ := taskConfig(t, g, arkham())
			if _, err := newK(t, c); !hasKind(err, module.ProblemTaskStartUnknown) {
				t.Fatalf("New = %v, want ProblemTaskStartUnknown", err)
			}
		})
	}
	t.Run("a registered kind of another enabled module is known", func(t *testing.T) {
		g, _ := patroller([]tasks.Kind{"robin.signal"})
		robin := mod("robin", func(r *module.Registrar) error {
			r.Tasks(tasks.Task("robin.signal", tasks.TaskSpec[struct{}, struct{}]{
				Title: "Signal", Effects: tasks.ReadOnly(), Perms: tasks.Permissions{Start: rbac.Need("robin:signal")},
				Resume: tasks.ResumeFromZero, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
			}))
			return r.Err()
		}).with(func(m *module.Manifest) { m.TaskLedger = []tasks.Kind{"robin.signal"} })
		c, _ := taskConfig(t, g, robin)
		mustNew(t, c)
	})
}

// TestTaskStarts_CoreScopeKindRefusedAtBoot (SEC2-15): a module's system actor
// holds the union of its listed kinds' Perms.Start, so a kind that starts under a
// core:* scope may not be listed: it would hand the module core:tasks.manage,
// core:tasks.view or core:admin. The same kind under the module's own scope is
// valid.
func TestTaskStarts_CoreScopeKindRefusedAtBoot(t *testing.T) {
	robin := func(scope rbac.Permission) testModule {
		return mod("robin", func(r *module.Registrar) error {
			r.Tasks(tasks.Task("robin.signal", tasks.TaskSpec[struct{}, struct{}]{
				Title: "Signal", Effects: tasks.ReadOnly(), Perms: tasks.Permissions{Start: rbac.Need(scope)},
				Resume: tasks.ResumeFromZero, Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
			}))
			return r.Err()
		}).with(func(m *module.Manifest) {
			m.TaskLedger = []tasks.Kind{"robin.signal"}
			m.Permissions = []rbac.PermissionSpec{{Name: "robin:signal", Title: "Signal"}}
		})
	}
	for _, scope := range []rbac.Permission{"core:tasks.manage", "core:tasks.view", "core:admin", "core:system.manage"} {
		t.Run(string(scope), func(t *testing.T) {
			g, _ := patroller([]tasks.Kind{"robin.signal"})
			c, _ := taskConfig(t, g, robin(scope))
			_, err := newK(t, c)
			var ve *module.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("New = %v, want a validation error", err)
			}
			found := false
			for _, p := range ve.Problems {
				found = found || (p.Kind == module.ProblemTaskStartUnknown && p.Module == "gotham" && strings.Contains(p.Detail, string(scope)))
			}
			if !found {
				t.Fatalf("no TaskStarts problem naming %s: %v", scope, err)
			}
		})
	}
	t.Run("the module's own scope is valid", func(t *testing.T) {
		g, _ := patroller([]tasks.Kind{"robin.signal"})
		c, _ := taskConfig(t, g, robin("robin:signal"))
		mustNew(t, c)
	})
}

// TestModuleTasks_ActsAsTheModulesSystemActor: Deps.Tasks is bound when the
// executor is built, and a call outside a request starts the module's own kind as
// its system actor, which a spoofed req.Actor does not change.
func TestModuleTasks_ActsAsTheModulesSystemActor(t *testing.T) {
	g, deps := patroller(nil)
	c, _ := taskConfig(t, g)
	k := mustNew(t, c)
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatalf("StartTasks: %v", err)
	}
	ref, err := deps.Tasks.Start(t.Context(), patrolKind, tasks.StartRequest{Mode: tasks.ModeRequestLive,
		Actor: tasks.Actor{UserID: "joker", AuthMethod: "session"}})
	if err != nil {
		t.Fatalf("a module starting its own kind: %v", err)
	}
	v := waitRun(t, k.TaskClient(), ref.ID)
	if want := (tasks.Actor{AuthMethod: "system", System: "gotham"}); v.Actor != want {
		t.Fatalf("the run's actor = %+v, want %+v (the module's system actor)", v.Actor, want)
	}
	if v.State != tasks.StateSucceeded {
		t.Fatalf("run ended %s (%s)", v.State, v.Reason)
	}
}

// TestModuleTasks_NotBoundWithoutAnEngine: an app that builds no executor (no
// kind registered, no run stored) leaves Deps.Tasks answering ErrTasksNotBound.
func TestModuleTasks_NotBoundWithoutAnEngine(t *testing.T) {
	var deps module.Deps
	m := mod("gotham", func(r *module.Registrar) error { deps = r.Deps(); return r.Err() })
	c, _ := taskConfig(t, m)
	mustNew(t, c)
	if _, err := deps.Tasks.Start(t.Context(), patrolKind, tasks.StartRequest{}); !errors.Is(err, module.ErrTasksNotBound) {
		t.Fatalf("Start = %v, want ErrTasksNotBound", err)
	}
	for _, err := range deps.Tasks.Runs(t.Context(), tasks.RunQuery{}) {
		if !errors.Is(err, module.ErrTasksNotBound) {
			t.Fatalf("Runs = %v, want ErrTasksNotBound", err)
		}
	}
}

// TestModuleLanes_Checked: a lane is named in its module's namespace, and a
// kind that lists a lane no enabled module declared is refused at boot, not at
// its first lease.
func TestModuleLanes_Checked(t *testing.T) {
	withLane := func(lane tasks.LaneID) func(*tasks.TaskSpec[struct{}, struct{}]) {
		return func(s *tasks.TaskSpec[struct{}, struct{}]) { s.Lanes = []tasks.LaneID{lane} }
	}
	cases := map[string]struct {
		lanes   []tasks.Lane
		def     tasks.Definition
		wantErr bool
	}{
		"declared and listed":  {lanes: []tasks.Lane{{ID: "gotham.signal"}}, def: patrol(withLane("gotham.signal"))},
		"listed, not declared": {def: patrol(withLane("gotham.signal")), wantErr: true},
		"foreign namespace":    {lanes: []tasks.Lane{{ID: "arkham.cells"}}, def: patrol(), wantErr: true},
		"bare id":              {lanes: []tasks.Lane{{ID: "signal"}}, def: patrol(), wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := mod("gotham", func(r *module.Registrar) error {
				r.Lanes(tc.lanes...)
				r.Tasks(tc.def)
				return r.Err()
			}).with(func(m *module.Manifest) { m.TaskLedger = []tasks.Kind{patrolKind} })
			c, _ := taskConfig(t, m)
			if !tc.wantErr {
				mustNew(t, c)
				return
			}
			if _, err := newK(t, c); !hasKind(err, module.ProblemInvalidRegistration) {
				t.Fatalf("New = %v, want ProblemInvalidRegistration", err)
			}
		})
	}
}

// countingSink is a change journal that counts what the writer hands it.
type countingSink struct{ n atomic.Int64 }

func (s *countingSink) Record(_ context.Context, _ tasks.RunID, ch []tasks.Change) error {
	s.n.Add(int64(len(ch)))
	return nil
}
func (*countingSink) Stands(context.Context, tasks.RunID, string) (bool, error) { return true, nil }

// TestModuleChangeSink_ReceivesTheKindsChanges: the sink a module registers is
// the one its kinds' gated writes record into.
func TestModuleChangeSink_ReceivesTheKindsChanges(t *testing.T) {
	sink := &countingSink{}
	body := func(rc *tasks.RC, _ struct{}) (struct{}, error) {
		return struct{}{}, rc.Writer().Do(rc.Context(), tasks.Write{Name: "gotham.note", Resource: tasks.Res("gotham", "cases"),
			Subject: tasks.ResID("gotham", "cases", "riddler")}, func(context.Context) ([]tasks.Change, error) {
			return []tasks.Change{{Subject: tasks.ResID("gotham", "cases", "riddler"), Field: "status", Old: "open", New: "closed"}}, nil
		})
	}
	m := mod("gotham", func(r *module.Registrar) error {
		r.Resources("cases")
		r.ChangeSink(sink)
		r.Tasks(tasks.Task(patrolKind, tasks.TaskSpec[struct{}, struct{}]{
			Title: "Patrol Gotham", Effects: tasks.Writes(tasks.Res("gotham", "cases")),
			Perms: tasks.Permissions{Start: rbac.Need("gotham:patrol")}, Resume: tasks.ResumeFromZero, Run: body,
		}))
		return r.Err()
	}).with(func(m *module.Manifest) { m.TaskLedger = []tasks.Kind{patrolKind} })
	c, _ := taskConfig(t, m)
	k := mustNew(t, c)
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	ref, err := k.TaskClient().Start(asAdmin(t.Context()), patrolKind, tasks.StartRequest{Mode: tasks.ModeRequestLive})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if v := waitRun(t, k.TaskClient(), ref.ID); v.State != tasks.StateSucceeded {
		t.Fatalf("run ended %s (%s)", v.State, v.Reason)
	}
	if n := sink.n.Load(); n != 1 {
		t.Fatalf("the module's change sink recorded %d changes, want 1", n)
	}
}

// TestModuleHealth_RegisteredAtP7: a component a module declares is registered
// with the health registry and reported through Deps.Health; an ID the module did
// not declare is health.ErrNotOwned, and before P7 the handle is not bound.
func TestModuleHealth_RegisteredAtP7(t *testing.T) {
	var deps module.Deps
	var before error
	m := mod("gotham", func(r *module.Registrar) error {
		deps = r.Deps()
		_, before = deps.Health.Reporter("gotham.signal")
		r.Health(health.Check{ID: "gotham.signal", Title: "Bat-signal"})
		return r.Err()
	})
	c, _ := taskConfig(t, m)
	k := mustNew(t, c)
	if !errors.Is(before, module.ErrHealthNotBound) {
		t.Fatalf("Reporter during Register = %v, want ErrHealthNotBound", before)
	}
	rep, err := deps.Health.Reporter("gotham.signal")
	if err != nil {
		t.Fatalf("Reporter after boot: %v", err)
	}
	rep.Report(health.Result{Status: health.StatusUp})
	var up bool
	for _, comp := range k.healthReg.Snapshot().Components {
		if comp.ID == "gotham.signal" && comp.Result.Status == health.StatusUp {
			up = true
		}
	}
	if !up {
		t.Fatal("the registry does not show gotham.signal Up after the module reported it")
	}
	if _, err := deps.Health.Reporter("arkham.cells"); !errors.Is(err, health.ErrNotOwned) {
		t.Fatalf("Reporter of another module's component = %v, want ErrNotOwned", err)
	}
}

// TestModuleHealth_NoStoreFailsClosed: a component declared in an app with no
// store (no registry) fails the boot instead of reading as never-up.
func TestModuleHealth_NoStoreFailsClosed(t *testing.T) {
	m := mod("gotham", func(r *module.Registrar) error {
		r.Health(health.Check{ID: "gotham.signal", Title: "Bat-signal"})
		return r.Err()
	})
	if _, err := newK(t, testConfig(t, m)); err == nil {
		t.Fatal("New succeeded with a health component and no store")
	}
}
