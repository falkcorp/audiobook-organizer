// file: core/kernel/tasks.go
// version: 1.0.3
// guid: c41f7a09-5d2e-4e83-b6a1-9a0d3f8e7c25
// last-edited: 2026-11-11

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/scheduler"
	"github.com/falkcorp/mediacore/internal/taskhttp"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// phaseP4c is the label of the native stores' build (06 §6.9). Recorded only
// when a store is open.
const phaseP4c = "P4c"

// labelDefUpsert is the crash-matrix region of the definition snapshot
// upsert, the one durable write the tasks wiring adds to the boot
// (crashmatrix.Rows: P7, "tasks").
const labelDefUpsert = "tasks.def.upsert"

// defSnapshotVersion is the version of a definition snapshot's value.
const defSnapshotVersion = 1

// taskOwnerSpec is the storage claim of core's task keyspace c/tasks/
// (05-tasks 5T.6.1): the run, debounce, schedule and idempotency guard
// families are Serialized. Like auditOwnerSpec it is appended to
// storageOwners, so the claim ledger gains the owner at the first P4b of a
// store (storage's own bookkeeping, one write).
func taskOwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}
}

// taskScheduler is what the kernel asks of the scheduler: its goroutine, its
// stop and the window members the executor's lanes read. *scheduler.Scheduler
// is the one production implementation; the stop-order test substitutes a
// recorder, so the order is observed rather than inferred from timing.
type taskScheduler interface {
	Run(ctx context.Context) error
	Stop(ctx context.Context) error
	Members(window string) []tasks.Kind
}

var _ taskScheduler = (*scheduler.Scheduler)(nil)

// kernelTasks is what the tasks wiring holds. The native stores exist from P4c
// whenever a store is open (they only wrap a keyspace and start nothing); the
// executor, the scheduler and the routes exist only when the start rule
// (tasksNeeded) held at P7.
type kernelTasks struct {
	ks     storage.Keyspace
	st     *taskstore.Store
	stores tasks.Stores

	// Set at P7 when the rule holds, never changed after: the goroutines
	// that read them start after the build.
	defs   []tasks.Definition
	exec   tasks.Executor // Run and Shutdown are the lifecycle; the rest is the Client
	sched  taskScheduler
	server *taskhttp.Server
	ops    *metrics.Scope

	mu       sync.Mutex
	started  bool
	stopped  bool
	schedEnd chan struct{} // closed when the scheduler goroutine returns
}

// buildTaskStores is P4c (06 §6.5.1): the native stores over c/tasks/, built
// once the store is migrated. It starts nothing and writes nothing, except
// the runs a test seam asks for (bootseam.Seams.TaskRuns, coretest's
// WithRuns).
func (k *Kernel) buildTaskStores(ctx context.Context) error {
	if k.store == nil || k.store.db == nil {
		return nil
	}
	ks, ok := k.store.db.Keyspace(taskstore.Owner)
	if !ok {
		return fmt.Errorf("kernel: tasks: owner %s has no keyspace: append taskOwnerSpec() to storageOwners", taskstore.Owner)
	}
	k.tasks = &kernelTasks{ks: ks, st: taskstore.New(ks), stores: tasks.NativeStores(ks)}
	return k.seedTaskRuns(ctx)
}

// seedTaskRuns inserts the queued runs bootseam.Seams.TaskRuns names. It is a
// test seam (coretest.WithRuns): the runs exist before P7, so the start rule
// sees them whether or not the module that owns their kind is enabled.
func (k *Kernel) seedTaskRuns(ctx context.Context) error {
	s := bootseam.From(ctx)
	if s == nil || len(s.TaskRuns) == 0 {
		return nil
	}
	now := bootClock(ctx)()
	for _, seed := range s.TaskRuns {
		kind, err := tasks.ParseKind(seed.Kind)
		if err != nil {
			return fmt.Errorf("kernel: seed runs: %w", err)
		}
		for range seed.N {
			id, err := k.tasks.stores.Runs.NewID(ctx)
			if err != nil {
				return fmt.Errorf("kernel: seed runs: %w", err)
			}
			rec := tasks.RunRecord{
				ID: id, Kind: kind, Mode: tasks.ModeLive, State: tasks.StateQueued,
				Actor: tasks.Actor{AuthMethod: "system", System: "core"}, Trigger: tasks.TriggerManual, Created: now,
				Priority: tasks.PriorityNormal, Params: json.RawMessage(`{}`),
			}
			if err := k.commitTaskBatch(ctx, func(b *storage.Batch) error { return k.tasks.stores.Runs.Insert(ctx, b, rec) }); err != nil {
				return fmt.Errorf("kernel: seed runs: %w", err)
			}
		}
	}
	return nil
}

// commitTaskBatch stages into one fresh indexed batch over c/tasks/ and
// commits it with sync.
func (k *Kernel) commitTaskBatch(ctx context.Context, stage func(b *storage.Batch) error) (err error) {
	b := k.tasks.st.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := stage(b); err != nil {
		return err
	}
	return b.Commit(ctx, storage.Sync)
}

// tasksNeeded is the start rule (05-tasks 5T.4 step 1, normative): tasks start
// when any kind is registered (registered counts the definitions every enabled
// module registered; core registers none of its own until F5) or c/tasks/
// holds a run. The run test reads one key under run/, never the whole family.
// In ABO's shape, before F5, both are false: nothing of tasks is built, no
// instrument exists and no route is mounted.
func tasksNeeded(ctx context.Context, registered int, st *taskstore.Store) (bool, error) {
	if registered > 0 {
		return true, nil
	}
	for _, err := range st.Scan(ctx, taskstore.RunPrefix(), nil, 1) {
		if err != nil {
			return false, fmt.Errorf("kernel: tasks: look for stored runs: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// buildTasks is P7's tasks step, after the registrations were checked
// (taskProblems): apply the start rule; when it holds, upsert the definition
// snapshots, then build the executor, the scheduler and the task routes. It
// starts none of them (Run steps (5) and (6) do) and mounts nothing: the
// routes are held by the task server until the router can carry them (see
// the PR notes; httpapi has no raw handler and no auth service yet).
func (k *Kernel) buildTasks(ctx context.Context) error {
	defs := k.taskDefinitions()
	if k.tasks == nil {
		// No store is open: a kind has nowhere to keep its runs. Failing here
		// beats a module whose tasks never run, silently (fail closed).
		for _, e := range k.entries {
			if e.enabled && len(e.rec.Tasks) > 0 {
				return fmt.Errorf("kernel: module %s registers task kinds and no store is open (core.storage.path is not set: kernel.Config.StoragePath)", e.man.ID)
			}
		}
		return nil
	}
	need, err := tasksNeeded(ctx, len(defs), k.tasks.st)
	if err != nil || !need {
		return err
	}
	if err := k.upsertDefinitions(ctx, defs); err != nil {
		return err
	}
	return k.constructTasks(ctx, defs)
}

// snapshotOf is a definition's stored snapshot, def/<kind> (5T.3.11): what a
// disabled module's runs can be described by without its code. The kind is
// the key only, never in the value, because Importer.RenameKind moves the
// snapshot as bytes (taskstore.DefKey).
func snapshotOf(d tasks.Definition) ([]byte, error) {
	c := tasks.CommonOf(d)
	return json.Marshal(struct {
		V     int    `json:"v"`
		Shape string `json:"shape"`
		Title string `json:"title"`
		Help  string `json:"help,omitempty"`
	}{V: defSnapshotVersion, Shape: d.Shape().String(), Title: c.Title, Help: c.Help})
}

// upsertDefinitions writes def/<kind> for each registered definition, one
// batch per registration, only when the stored snapshot differs: a boot over
// an unchanged build writes nothing, and the crash-matrix region fires around
// each write only.
func (k *Kernel) upsertDefinitions(ctx context.Context, defs []tasks.Definition) error {
	for _, d := range defs {
		val, err := snapshotOf(d)
		if err != nil {
			return fmt.Errorf("kernel: tasks: snapshot of %s: %w", d.Kind(), err)
		}
		key := taskstore.DefKey(string(d.Kind()))
		old, found, err := k.tasks.st.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("kernel: tasks: read the snapshot of %s: %w", d.Kind(), err)
		}
		if found && bytes.Equal(old, val) {
			continue
		}
		exit := bootseam.Region(ctx, labelDefUpsert)
		err = k.commitTaskBatch(ctx, func(b *storage.Batch) error { return k.tasks.st.Set(b, key, val) })
		exit()
		if err != nil {
			return fmt.Errorf("kernel: tasks: write the snapshot of %s: %w", d.Kind(), err)
		}
	}
	return nil
}

// executorManifests is every manifest as the executor reads it: whether the
// module is enabled (a disabled module's, and a module storage holds, runs are
// held, 5T.3.11) and the kinds its ledger lists. TaskStarts is F5's.
func (k *Kernel) executorManifests() []executor.Manifest {
	out := make([]executor.Manifest, 0, len(k.entries))
	for _, e := range k.entries {
		out = append(out, executor.Manifest{Module: string(e.man.ID), Enabled: e.enabled, TaskLedger: slices.Clone(e.man.TaskLedger)})
	}
	return out
}

// heldKinds reports a kind of a disabled module: the scheduler does not poll
// its clock.
func heldKinds(ms []executor.Manifest) func(tasks.Kind) bool {
	held := make(map[tasks.Kind]bool)
	mods := make(map[string]bool)
	for _, m := range ms {
		if m.Enabled {
			continue
		}
		mods[m.Module] = true
		for _, k := range m.TaskLedger {
			held[k] = true
		}
	}
	return func(k tasks.Kind) bool { return held[k] || mods[k.Module()] }
}

// constructTasks builds the executor, the scheduler and the task server. The
// executor's WindowMembers closes over the scheduler's Members (the scheduler
// takes the executor as its Starter, so it is built second); Principal is the
// task routes' (taskhttp.Principal), so a Client call acts as the caller its
// context carries.
func (k *Kernel) constructTasks(ctx context.Context, defs []tasks.Definition) error {
	kt := k.tasks
	ops, err := k.provider.Core("ops", executor.Attributes()...)
	if err != nil {
		return fmt.Errorf("kernel: tasks: ops metrics scope: %w", err)
	}
	manifests := k.executorManifests()
	x, err := executor.New(executor.Options{
		Config:      k.coreCfg.executorConfig(),
		Stores:      kt.stores,
		NewBatch:    kt.st.NewBatch,
		Keyspace:    &kt.ks,
		Definitions: defs,
		Manifests:   manifests,
		Catalog:     k.rbacCat,
		Principal:   taskhttp.Principal,
		WindowMembers: func(window string) []tasks.Kind {
			if kt.sched == nil {
				return nil
			}
			return kt.sched.Members(window)
		},
		Health:  k.healthReg,
		Metrics: ops,
		Logger:  k.coreLog("tasks"),
		Now:     bootClock(ctx),
	})
	if err != nil {
		return fmt.Errorf("kernel: tasks: executor: %w", err)
	}
	loc, err := k.coreCfg.location()
	if err != nil {
		return err
	}
	s, err := scheduler.New(scheduler.Options{
		Stores:      kt.stores,
		Starter:     x,
		NewBatch:    kt.st.NewBatch,
		Definitions: defs,
		Config:      schedulerConfig{c: k.coreCfg},
		Poll:        k.coreCfg.schedulePoll(),
		Location:    loc,
		Held:        heldKinds(manifests),
		Metrics:     executor.Schedules(x),
		Logger:      k.coreLog("scheduler"),
		Now:         bootClock(ctx),
	})
	if err != nil {
		return fmt.Errorf("kernel: tasks: scheduler: %w", err)
	}
	srv, err := taskhttp.New(taskhttp.Deps{
		Client:      x,
		Approver:    x,
		Runs:        kt.stores.Runs,
		Definitions: defs,
		Config:      k.coreCfg.store,
		Audit:       k.audit,
		Log:         k.coreLog("taskhttp"),
	})
	if err != nil {
		return fmt.Errorf("kernel: tasks: routes: %w", err)
	}
	kt.defs, kt.exec, kt.sched, kt.server, kt.ops = defs, x, s, srv, ops
	return nil
}

// startTasks is Run steps (5) and (6) (06 §6.5.2): the executor's resume
// sweep, dispatcher and workers, then the scheduler's goroutine. It does
// nothing when the start rule did not hold, and nothing when it already ran.
// Everything it starts stops in stopTasks, which closeCore also calls, so a
// boot that fails after it leaves nothing running.
func (k *Kernel) startTasks(ctx context.Context) error {
	kt := k.tasks
	if kt == nil || kt.exec == nil {
		return nil
	}
	kt.mu.Lock()
	defer kt.mu.Unlock()
	if kt.started || kt.stopped {
		return nil
	}
	if err := kt.exec.Run(ctx); err != nil {
		return fmt.Errorf("kernel: tasks: start the executor: %w", err)
	}
	kt.started = true
	kt.schedEnd = make(chan struct{})
	end := kt.schedEnd
	go func() {
		defer close(end)
		err := kt.sched.Run(ctx)
		if err == nil {
			return
		}
		// Once a stop is under way the only error Run returns is its refusal
		// to start ("Run after Stop", a stop that overtook this goroutine):
		// that is the shutdown, not a fault, so it is not an ERROR. The error
		// is not a sentinel, so it is recognised by the state, not the text;
		// before any stop, any error is a fault.
		kt.mu.Lock()
		stopped := kt.stopped
		kt.mu.Unlock()
		if stopped {
			k.coreLog("scheduler").Debug("kernel: scheduler did not run: tasks already stopped", "err", err)
			return
		}
		k.coreLog("scheduler").Error("kernel: scheduler stopped with an error", "err", err)
	}()
	return nil
}

// stopTasks is the stop order of 05-tasks 5T.4 step 4 (06 §6.5.2): the
// scheduler stops first, so it fires nothing more, then the executor drains.
// It is safe to call again, and before startTasks (the scheduler and executor
// then have nothing to stop).
func (k *Kernel) stopTasks(ctx context.Context) error {
	kt := k.tasks
	if kt == nil || kt.exec == nil {
		return nil
	}
	kt.mu.Lock()
	if kt.stopped {
		kt.mu.Unlock()
		return nil
	}
	kt.stopped = true
	end := kt.schedEnd
	kt.mu.Unlock()
	var errs []error
	if err := kt.sched.Stop(ctx); err != nil {
		errs = append(errs, fmt.Errorf("kernel: tasks: stop the scheduler: %w", err))
	}
	if end != nil {
		select {
		case <-end:
		case <-ctx.Done():
		}
	}
	if err := kt.exec.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("kernel: tasks: drain the executor: %w", err))
	}
	return errors.Join(errs...)
}

// startCoreServices is Run's steps (5), (6) and the audit writer of step (7),
// before the listener opens. The writer is started under a context that
// outlives Run's: Run's context ends when the process is told to stop, and a
// writer bound to it would stop then, before the listener has drained and the
// executor has finished its runs, so every record they emit afterwards would
// be dropped. Only stopAudit ends the writer.
func (k *Kernel) startCoreServices(ctx context.Context) error {
	if err := k.startTasks(ctx); err != nil {
		return err
	}
	if k.audit != nil {
		return startAudit(context.WithoutCancel(ctx), k.audit)
	}
	return nil
}

// auditStopSlice is the longest the kernel lets the audit writer drain at
// stop. The writer has a drain budget of its own (5 s); this is the kernel's,
// below it, so a store that cannot take the queue never costs the module
// workers and hooks that stop after it more than this (05-health §5H.4: each
// stop step has a slice, none is handed the whole remainder).
const auditStopSlice = 3 * time.Second

// stopCoreServices stops what startCoreServices started, in reverse: the
// scheduler, the executor's drain, then the audit writer. The writer stops
// last because it records what the scheduler's and the executor's last
// transitions emit, and while storage is still open, since it drains into it.
// Safe to call twice.
func (k *Kernel) stopCoreServices(ctx context.Context, deadline time.Time) error {
	auditStop := func(context.Context) error { return nil }
	if k.audit != nil {
		auditStop = func(ctx context.Context) error { return stopAudit(ctx, k.audit) }
	}
	return runCoreStops(ctx, deadline, k.stopTasks, auditStop)
}

// runCoreStops is stopCoreServices' order and its budget: the tasks stop
// within what remains of deadline, then the audit writer within the lesser of
// auditStopSlice and what remains after them, so a writer that cannot drain
// leaves the workers and hooks their share.
func runCoreStops(ctx context.Context, deadline time.Time, stopTasks, stopAuditWriter func(context.Context) error) error {
	ctx = context.WithoutCancel(ctx)
	tctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	errs := []error{stopTasks(tctx)}
	slice := time.Now().Add(auditStopSlice)
	if deadline.Before(slice) {
		slice = deadline
	}
	actx, acancel := context.WithDeadline(ctx, slice)
	defer acancel()
	errs = append(errs, stopAuditWriter(actx))
	return errors.Join(errs...)
}

// StartTasks starts the executor and the scheduler without Run, for
// coretest's harness, which never calls App.Run: the same step Run takes
// before it opens the listener. A kernel whose start rule did not hold has
// none, and StartTasks does nothing. Stop is Close's.
func (k *Kernel) StartTasks(ctx context.Context) error { return k.startTasks(ctx) }

// TaskClient is the client over the executor, nil when the start rule did not
// hold. Release F2 only: it is coretest's seam to start and read runs as the
// context's principal; no module is ever handed it (05-tasks 5T.3.10: a
// module's Deps.Tasks is wrapped per module, F5).
func (k *Kernel) TaskClient() tasks.Client {
	if k.tasks == nil || k.tasks.exec == nil {
		return nil
	}
	return k.tasks.exec
}

// TaskRecord is one run's stored record, with the result document and error
// text the Client's read model does not carry. Release F2 only, with
// TaskClient: coretest's RunResult reads it; it applies no permission rule and
// no redaction, so it is never reachable from a request.
func (k *Kernel) TaskRecord(ctx context.Context, id tasks.RunID) (tasks.RunRecord, error) {
	if k.tasks == nil {
		return tasks.RunRecord{}, errors.New("kernel: no task store is open")
	}
	return k.tasks.stores.Runs.Get(ctx, id)
}
