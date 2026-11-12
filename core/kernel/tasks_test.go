// file: core/kernel/tasks_test.go
// version: 1.0.0
// guid: a2d94c7e-1b53-4f08-9e6a-3c7d0f5b8e12
// last-edited: 2026-11-11

package kernel

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// taskConfig is a Config over an in-memory store, for mods.
func taskConfig(t *testing.T, mods ...module.Module) (Config, *logcapture.Capture) {
	t.Helper()
	lc := logcapture.New(t)
	return Config{AppID: "gotham", Logger: lc.Logger(), Modules: mods,
		StoragePath: "/gotham/storage/main.pebble", StorageFS: storage.MemFS()}, lc
}

// patrolKind is the kind the tests' module registers.
const patrolKind tasks.Kind = "gotham.patrol"

// patrol is a read-only task in gotham's namespace, started by gotham:patrol.
func patrol(mutate ...func(*tasks.TaskSpec[struct{}, struct{}])) tasks.Definition {
	s := tasks.TaskSpec[struct{}, struct{}]{
		Title:   "Patrol Gotham",
		Effects: tasks.ReadOnly(),
		Perms:   tasks.Permissions{Start: rbac.Need("gotham:patrol")},
		Resume:  tasks.ResumeFromZero,
		Run:     func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
	}
	for _, m := range mutate {
		m(&s)
	}
	return tasks.Task(patrolKind, s)
}

// gotham is a module that registers defs, with ledger as its TaskLedger.
func gotham(ledger []tasks.Kind, defs ...tasks.Definition) testModule {
	return mod("gotham", func(r *module.Registrar) error {
		r.Tasks(defs...)
		return r.Err()
	}).with(func(m *module.Manifest) { m.TaskLedger = ledger })
}

// holdsAll is a checker that holds every permission.
type holdsAll struct{}

func (holdsAll) Has(rbac.Permission) bool        { return true }
func (holdsAll) Satisfies(rbac.Requirement) bool { return true }
func (holdsAll) Confined() bool                  { return false }
func (holdsAll) Concrete() []rbac.Permission     { return []rbac.Permission{rbac.CoreAdmin} }

// asAdmin is ctx acting as a session principal that may do anything.
func asAdmin(ctx context.Context) context.Context {
	return auth.WithPrincipal(ctx, &auth.Principal{Method: auth.MethodSession, Perms: holdsAll{}})
}

// opsFamilies counts the families of the <ns>_ops_ scope the kernel gathers.
func opsFamilies(t *testing.T, k *Kernel) int {
	t.Helper()
	prefix := strings.ReplaceAll(strings.TrimSuffix(k.scope.Prefix(), ".kernel"), ".", "_") + "_ops_"
	ss, err := metricstest.Gather(k.provider.Gatherer(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	return len(ss)
}

// taskKeys lists every key of c/tasks/ (relative), in order.
func taskKeys(t *testing.T, k *Kernel) []string {
	t.Helper()
	ks, ok := k.store.db.Keyspace(taskstore.Owner)
	if !ok {
		t.Fatal("the store has no c/tasks/ keyspace")
	}
	var out []string
	for kv, err := range ks.ScanPrefix(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(kv.Key))
	}
	return out
}

// assertNoTasks is ABO's shape before F5: no store of runs in use beyond the
// stores' construction, no executor, no scheduler, no route, no <ns>_ops_
// family, no key in c/tasks/.
func assertNoTasks(t *testing.T, k *Kernel) {
	t.Helper()
	if k.tasks != nil && (k.tasks.exec != nil || k.tasks.sched != nil || k.tasks.server != nil || k.tasks.ops != nil) {
		t.Errorf("tasks were built: exec %v, scheduler %v, routes %v, ops scope %v", k.tasks.exec != nil, k.tasks.sched != nil, k.tasks.server != nil, k.tasks.ops != nil)
	}
	if k.TaskClient() != nil {
		t.Error("TaskClient is not nil")
	}
	rec := httptest.NewRecorder()
	k.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/core/v1/tasks/runs", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/core/v1/tasks/runs = %d, want 404 (no task route is mounted)", rec.Code)
	}
	if n := opsFamilies(t, k); n != 0 {
		t.Errorf("%d <ns>_ops_ families are gathered, want none", n)
	}
	if k.store != nil && k.store.db != nil {
		if keys := taskKeys(t, k); len(keys) != 0 {
			t.Errorf("c/tasks/ holds %d keys, want none: %q", len(keys), keys)
		}
	}
}

// TestKernelTasks_NothingStartsWithoutKindsOrRuns is ABO's shape before F5 (05-tasks
// 5T.4 step 1): a zero-module boot, and a boot whose only module registers
// no kind, over an empty store, start nothing of tasks.
func TestKernelTasks_NothingStartsWithoutKindsOrRuns(t *testing.T) {
	t.Run("zero modules, no store", func(t *testing.T) {
		k := mustNew(t, testConfig(t))
		assertNoTasks(t, k)
	})
	t.Run("zero modules, an empty store", func(t *testing.T) {
		c, _ := taskConfig(t)
		assertNoTasks(t, mustNew(t, c))
	})
	t.Run("a module that registers no kind, an empty store", func(t *testing.T) {
		c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}))
		k := mustNew(t, c)
		assertNoTasks(t, k)
		// The run, kind and ledger are known without anything starting.
		if err := k.StartTasks(t.Context()); err != nil {
			t.Fatalf("StartTasks with nothing built: %v", err)
		}
	})
	t.Run("a disabled module with a ledger and no run", func(t *testing.T) {
		m := gotham([]tasks.Kind{patrolKind}).with(func(m *module.Manifest) { m.DefaultEnabled = false })
		c, _ := taskConfig(t, m)
		assertNoTasks(t, mustNew(t, c))
	})
}

// waitRun polls until the run is terminal and returns its view.
func waitRun(t *testing.T, c tasks.Client, id tasks.RunID) tasks.RunView {
	t.Helper()
	ctx, cancel := context.WithTimeout(asAdmin(t.Context()), 30*time.Second)
	defer cancel()
	for {
		v, err := c.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if v.State.Terminal() {
			return v
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run %s is still %s", id, v.State)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestKernelTasks_StartsWhenAKindIsRegistered: a registered kind builds the
// executor, the scheduler and the routes, writes the definition snapshot
// (without the kind in its value), and the run it is started with succeeds
// once Run's step (5) has started the executor.
func TestKernelTasks_StartsWhenAKindIsRegistered(t *testing.T) {
	c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()))
	k := mustNew(t, c)
	kt := k.tasks
	if kt == nil || kt.exec == nil || kt.sched == nil || kt.server == nil || kt.ops == nil {
		t.Fatalf("not built: %+v", kt)
	}
	if n := len(kt.server.Routes()); n == 0 {
		t.Error("the task server has no routes")
	}
	val, found, err := kt.st.Get(t.Context(), taskstore.DefKey(string(patrolKind)))
	if err != nil || !found {
		t.Fatalf("definition snapshot: found %v, err %v", found, err)
	}
	if bytes.Contains(val, []byte(patrolKind)) || bytes.Contains(val, []byte("gotham")) {
		t.Errorf("the snapshot %s embeds the kind (RenameKind moves it as bytes)", val)
	}
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatalf("StartTasks: %v", err)
	}
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatalf("a second StartTasks: %v", err)
	}
	ref, err := k.TaskClient().Start(asAdmin(t.Context()), patrolKind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerManual})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if v := waitRun(t, k.TaskClient(), ref.ID); v.State != tasks.StateSucceeded {
		t.Fatalf("run ended %s (%s), want succeeded", v.State, v.Reason)
	}
}

// TestKernelTasks_SnapshotWrittenOncePerChange: a boot over a store that holds
// the current snapshot writes nothing, so the matrix's one region fires only
// when a definition is new or changed.
func TestKernelTasks_SnapshotWrittenOncePerChange(t *testing.T) {
	c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()))
	k := mustNew(t, c)
	before := taskKeys(t, k)
	if err := k.upsertDefinitions(t.Context(), k.tasks.defs); err != nil {
		t.Fatal(err)
	}
	if after := taskKeys(t, k); !slices.Equal(before, after) {
		t.Errorf("a second upsert changed c/tasks/: %q -> %q", before, after)
	}
}

// TestKernelTasks_StartsWhenRunsAreHeld: no kind is registered (the module is
// disabled) but c/tasks/ holds runs of its kind, so tasks start, and the runs
// list as held and are never dispatched (05-tasks 5T.3.11).
func TestKernelTasks_StartsWhenRunsAreHeld(t *testing.T) {
	m := gotham([]tasks.Kind{patrolKind}, patrol()).with(func(m *module.Manifest) { m.DefaultEnabled = false })
	c, _ := taskConfig(t, m)
	ctx := bootseam.With(t.Context(), &bootseam.Seams{TaskRuns: []bootseam.TaskRunSeed{{Kind: string(patrolKind), N: 3}}})
	k, err := New(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close(context.Background()) })
	if k.tasks == nil || k.tasks.exec == nil {
		t.Fatal("stored runs did not start the tasks")
	}
	if len(k.tasks.defs) != 0 {
		t.Errorf("%d definitions registered by a disabled module", len(k.tasks.defs))
	}
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got []tasks.RunView
	for v, err := range k.TaskClient().Runs(asAdmin(t.Context()), tasks.RunQuery{Kinds: []tasks.Kind{patrolKind}}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if len(got) != 3 {
		t.Fatalf("%d runs listed, want 3", len(got))
	}
	for _, v := range got {
		if !v.Held || v.State != tasks.StateQueued || !v.Started.IsZero() {
			t.Errorf("run %s: held %v, state %s, started %v; want held, queued, never started", v.ID, v.Held, v.State, v.Started)
		}
	}
}

// TestKernelTasks_LedgerMissingKindRefused: a kind registered but absent from
// the module's TaskLedger is a ProblemTaskLedger.
func TestKernelTasks_LedgerMissingKindRefused(t *testing.T) {
	c, _ := taskConfig(t, gotham(nil, patrol()))
	_, err := New(t.Context(), c)
	if !hasKind(err, module.ProblemTaskLedger) {
		t.Fatalf("New = %v, want ProblemTaskLedger", err)
	}
}

// TestKernelTasks_ForeignKindRefused: a kind in another module's namespace,
// registered or listed in the ledger, is a ProblemTaskKindNamespace; so is a
// kind two manifests list (ProblemDuplicateTaskKind).
func TestKernelTasks_ForeignKindRefused(t *testing.T) {
	joker := tasks.Task("joker.laugh", tasks.TaskSpec[struct{}, struct{}]{
		Title: "Laugh", Effects: tasks.ReadOnly(), Perms: tasks.Permissions{Start: rbac.Need("gotham:patrol")}, Resume: tasks.ResumeFromZero,
		Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
	})
	t.Run("registered", func(t *testing.T) {
		c, _ := taskConfig(t, gotham([]tasks.Kind{"joker.laugh"}, joker))
		_, err := New(t.Context(), c)
		if !hasKind(err, module.ProblemTaskKindNamespace) {
			t.Fatalf("New = %v, want ProblemTaskKindNamespace", err)
		}
	})
	t.Run("ledger entry", func(t *testing.T) {
		c, _ := taskConfig(t, gotham([]tasks.Kind{"joker.laugh"}))
		_, err := New(t.Context(), c)
		if !hasKind(err, module.ProblemTaskKindNamespace) {
			t.Fatalf("New = %v, want ProblemTaskKindNamespace", err)
		}
	})
	t.Run("not a kind", func(t *testing.T) {
		c, _ := taskConfig(t, gotham([]tasks.Kind{"gotham"}))
		_, err := New(t.Context(), c)
		if !hasKind(err, module.ProblemTaskKindNamespace) {
			t.Fatalf("New = %v, want ProblemTaskKindNamespace", err)
		}
	})
	t.Run("the same ledger twice", func(t *testing.T) {
		c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind, patrolKind}))
		_, err := New(t.Context(), c)
		if !hasKind(err, module.ProblemDuplicateTaskKind) {
			t.Fatalf("New = %v, want ProblemDuplicateTaskKind", err)
		}
	})
	t.Run("registered twice", func(t *testing.T) {
		c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol(), patrol()))
		_, err := New(t.Context(), c)
		if !hasKind(err, module.ProblemDuplicateTaskKind) {
			t.Fatalf("New = %v, want ProblemDuplicateTaskKind", err)
		}
	})
}

// TestKernelTasks_RegistrationRules: the rest of P7's registration checks.
func TestKernelTasks_RegistrationRules(t *testing.T) {
	cases := []struct {
		name string
		def  tasks.Definition
		kind module.ProblemKind
	}{
		{"PrioritySystem on a module kind", patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) { s.Priority = tasks.PrioritySystem }), module.ProblemInvalidRegistration},
		{"Start under another module's scope", patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) {
			s.Perms.Start = rbac.Need("joker:laugh")
		}), module.ProblemPermissionNamespace},
		{"an undeclared resource kind read", patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) {
			s.Effects = tasks.ReadOnly(tasks.Res("gotham", "signal"))
		}), module.ProblemInvalidRegistration},
		{"a write to another module's resource", patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) {
			s.Effects = tasks.Writes(tasks.Res("joker", "gag"))
		}), module.ProblemInvalidRegistration},
		{"a schedule key no section declares", patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) {
			s.Schedule = tasks.ScheduleFromConfig("modules.gotham.tasks.schedules.patrol")
		}), module.ProblemInvalidRegistration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, tc.def))
			if _, err := New(t.Context(), c); !hasKind(err, tc.kind) {
				t.Fatalf("New = %v, want a %v problem", err, tc.kind)
			}
		})
	}
	t.Run("resources declared, reads and writes of its own accepted", func(t *testing.T) {
		m := mod("gotham", func(r *module.Registrar) error {
			r.Resources("signal")
			r.Tasks(patrol(func(s *tasks.TaskSpec[struct{}, struct{}]) {
				s.Effects = tasks.Writes(tasks.Res("gotham", "signal")).AlsoReads(tasks.Res("batcave", "log"))
			}))
			return r.Err()
		}).with(func(m *module.Manifest) { m.TaskLedger = []tasks.Kind{patrolKind} })
		c, _ := taskConfig(t, m)
		mustNew(t, c)
	})
	t.Run("kinds with no store open", func(t *testing.T) {
		cfg := testConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()))
		if _, err := New(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "no store is open") {
			t.Fatalf("New = %v, want a refusal naming the missing store", err)
		}
	})
}

// TestBatch_ZeroParallelismRefusedAtRegister (05-tasks 5T.9): the
// registration-time half of F2-01's constructor rule: a Batch that declares
// no parallelism is refused at P7, wrapping tasks.ErrNoParallelism.
func TestBatch_ZeroParallelismRefusedAtRegister(t *testing.T) {
	batch := tasks.Batch(patrolKind, tasks.BatchSpec[struct{}, string, struct{}]{
		Title: "Patrol the rooftops", Effects: tasks.ReadOnly(), Perms: tasks.Permissions{Start: rbac.Need("gotham:patrol")}, Resume: tasks.ResumeFromZero,
		Source: func(*tasks.RC, struct{}) (tasks.Source[string], error) {
			return tasks.Source[string]{}, errors.New("not reached")
		},
		Item: func(*tasks.ItemRC, struct{}, string) error { return nil },
	})
	c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, batch))
	_, err := New(t.Context(), c)
	if !hasKind(err, module.ProblemInvalidRegistration) || !errors.Is(err, tasks.ErrNoParallelism) {
		t.Fatalf("New = %v, want an invalid registration wrapping ErrNoParallelism", err)
	}
}

// TestStart_ViewOnlyDefRefusedAtRegister (05-tasks 5T.9, SEC-9): a definition
// whose Start is a view-class scope is refused at registration unless it states
// a reason. The catalog holds core's scopes at F2, so the rule is enforced for
// them; a module's own view scopes need its manifest's permissions (F4).
func TestStart_ViewOnlyDefRefusedAtRegister(t *testing.T) {
	viewOnly := func(s *tasks.TaskSpec[struct{}, struct{}]) { s.Perms.Start = rbac.Need("core:tasks.view") }
	c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol(viewOnly)))
	_, err := New(t.Context(), c)
	if !hasKind(err, module.ProblemInvalidRegistration) || !strings.Contains(err.Error(), "view-class") {
		t.Fatalf("New = %v, want a view-class refusal", err)
	}
	reviewed := func(s *tasks.TaskSpec[struct{}, struct{}]) {
		viewOnly(s)
		s.Perms.StartableByViewers = "a read-only report anyone who can see tasks may refresh"
	}
	c, _ = taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol(reviewed)))
	mustNew(t, c)
}

// TestKernelTasks_DefaultsOnlyConfig: the executor, the scheduler and the
// routes read core.tasks.* and the schedule keys from the core sections at
// their declared defaults.
func TestKernelTasks_DefaultsOnlyConfig(t *testing.T) {
	c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol()))
	k := mustNew(t, c)
	cfg := k.coreCfg
	ec := cfg.executorConfig()
	if ec.Workers != 8 || ec.SystemWorkers != 2 || ec.ShutdownDrain != 10*time.Second || ec.IdempotencyTTL != 168*time.Hour {
		t.Errorf("executor config = %+v, want core.tasks' defaults", ec)
	}
	if w, ok := cfg.window("nightly"); !ok || w.Start != 1 || w.End != 4 || !w.Enabled {
		t.Errorf("window nightly = %+v, %v; want 1-4 enabled", w, ok)
	}
	got, err := cfg.schedule("core.tasks.schedules.prune")
	if err != nil || !got.Enabled || time.Duration(got.Every) != 24*time.Hour {
		t.Errorf("core.tasks.schedules.prune = %+v, %v; want every 24h", got, err)
	}
	if _, err := cfg.schedule("core.tasks.workers"); err == nil {
		t.Error("a key that is not a schedule key was read as one")
	}
	if isSched, owner, ok := cfg.declared("core.backup.schedules.create"); !isSched || owner != "core" || !ok {
		t.Errorf("declared(core.backup.schedules.create) = %v %q %v", isSched, owner, ok)
	}
	if _, _, ok := cfg.declared("modules.gotham.tasks.schedules.patrol"); ok {
		t.Error("a module key is declared before module settings exist")
	}
}

// TestKernelBoot_F2CallsInOrder: the boot calls of the F2 foundation, each
// where 06 §6.5.1 puts it: the audit store built in P4a before P4b's data
// migrations ran; the scope catalog declared; the core sections listed; the
// health registry holding core.storage and core.secrets Up; the data-migration
// runner invoked at P4b; the native task stores at P4c; the audit instruments
// after P5.
func TestKernelBoot_F2CallsInOrder(t *testing.T) {
	// A data migration visits what a store already holds: a first boot makes
	// the store and one row, the second runs the migration over it.
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40}) // P4a measures free space on a store that exists
	first, _ := taskConfig(t, mod("gotham", nil).with(func(m *module.Manifest) {
		m.Storage = storage.Spec{Keyspace: true}
	}))
	first.StorageFS = storage.FSOf(mem)
	k1, err := New(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	ks, _ := k1.store.db.Keyspace("gotham")
	if err := ks.Set(t.Context(), []byte("item/robin"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := k1.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	c, lc := taskConfig(t)
	c.StorageFS = first.StorageFS
	verified := false // set by the step, on the runner goroutine, before New returns
	m := mod("gotham", nil).with(func(m *module.Manifest) {
		m.Storage = storage.Spec{Keyspace: true}
		m.DataMigrations = []datamigrate.DataMigration{{
			ID: "gotham.recount", Owner: "gotham", Description: "recount the signals",
			Source:      []storage.Range{{Start: []byte("m/gotham/item/"), End: []byte("m/gotham/item0")}},
			Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking, Compat: storage.Additive,
			Step: func(context.Context, storage.KV, *datamigrate.StepWriter) error {
				if !verified {
					verified = true
					lc.Logger().Debug("data migration step ran (P4b)")
				}
				return nil
			},
		}}
	})
	c.Modules = []module.Module{m}
	k := mustNew(t, c)

	if !verified {
		t.Error("the data-migration runner did not run at P4b")
	}
	msgs := lc.Messages()
	audit, migrated := -1, -1
	for i, s := range msgs {
		switch {
		case strings.HasPrefix(s, "kernel: audit store built"):
			audit = i
		case s == "data migration step ran (P4b)":
			migrated = i
		}
	}
	if audit < 0 || migrated < 0 || audit > migrated {
		t.Errorf("audit store built at log line %d, P4b's data migration at %d; the store is built before P4b", audit, migrated)
	}
	if k.audit == nil || k.auditSc == nil {
		t.Errorf("audit store %v, its metrics scope %v", k.audit, k.auditSc)
	}
	if k.rbacCat == nil {
		t.Fatal("the scope catalog was not declared")
	}
	if _, ok := k.rbacCat.Lookup(rbac.CoreAdmin); !ok {
		t.Error("the catalog lacks core:admin")
	}
	for _, p := range []config.Path{"core.tasks.workers", "core.audit.schedules.prune", "core.log.level"} {
		if _, ok := k.coreCfg.store.Lookup(p); !ok {
			t.Errorf("the core sections lack %s", p)
		}
	}
	got := states(k.healthReg)
	for _, id := range []health.ComponentID{componentStorage, componentSecrets} {
		if s, ok := got[id]; !ok || s.Result.Status != health.StatusUp {
			t.Errorf("health component %s = %+v (present %v), want Up", id, s, ok)
		}
	}
	if k.reporters.Storage == nil || k.reporters.Secrets == nil {
		t.Error("the kernel dropped its health reporters")
	}
	if k.tasks == nil || k.tasks.stores.Runs == nil {
		t.Error("the native task stores were not built at P4c")
	}
	if k.dataMig == nil {
		t.Error("the data-migration runner was not kept")
	}
	phases, err := bootPhases(k)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"P1", "P2", "P4a", "P4b", "P4c", "P5", "P6", "P7", "P8"}; !slices.Equal(phases, want) {
		t.Errorf("phases = %v, want %v", phases, want)
	}
	// The owners the boot registered include core's own two.
	owners := k.storageOwners()
	for _, want := range []storage.Owner{auditOwnerID, taskstore.Owner} {
		if !slices.ContainsFunc(owners, func(o storage.OwnerSpec) bool { return o.Owner == want }) {
			t.Errorf("storage owner %s is not registered", want)
		}
	}
}
