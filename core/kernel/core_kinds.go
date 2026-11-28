// file: core/kernel/core_kinds.go
// version: 1.0.0
// guid: 2e8b5c47-d913-4a60-b7f2-1c9a4e6d8035
// last-edited: 2026-11-22

package kernel

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/internal/scheduler"
	"github.com/falkcorp/mediacore/internal/storagetasks"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// coreModuleID is the module core's own task kinds belong to: the namespace of
// core.storage.compact, core.window.nightly and the rest, the owner of the
// resource kinds they write, and the system actor that starts them on their
// schedule.
const coreModuleID module.ID = "core"

// storageKeyspaceOwner is storage's own bookkeeping owner, c/storage/, where
// the census is kept (storage keeps the constant unexported).
const storageKeyspaceOwner storage.Owner = "core/storage"

// InterimBackends are the three late-owner kinds' backends (05-tasks 5T.8.2):
// each kind is registered over its backend only when the app supplies one, so
// an app with no provider holds, no AI batch jobs or no diagnostics export
// registers none of the three. F7 and F8 move each backend into its owner and
// delete the field. app.InterimBackends mirrors it.
type InterimBackends struct {
	Holds  providerhttp.InterimHolds // core.integrations.release-holds
	Poll   ai.InterimPoll            // core.ai.batch-poll
	Export diagnostics.InterimExport // core.diagnostics.export
}

// tasksFlipOn is the one predicate of the F5 tasks switch (Config.TasksBoot):
// core registers its own kinds and the interim backends' kinds only when it
// holds, and the start rule (tasksNeeded) then starts the executor and the
// scheduler over them. It reads nothing but the config the app gave: not the
// identity flip, which stands alone.
func (k *Kernel) tasksFlipOn() bool { return k.cfg.TasksBoot }

// taskEntries are the enabled modules' registrations and, when the tasks switch
// registered them, core's own, which come first. Core's is a synthetic entry (no
// module of the app): its Manifest names only its ID and its kinds, so every P7
// check a module's kinds pass applies to core's through the same code.
func (k *Kernel) taskEntries() []*entry {
	if k.core == nil {
		return k.entries
	}
	return append([]*entry{k.core}, k.entries...)
}

// registerCoreKinds is core's own Register, run at the end of P6 when the tasks
// switch is on: the kinds F5-03 converted (core.storage.*, core.tasks.prune,
// core.log.journal.prune), one core.window.<name> per core.tasks.windows entry,
// and the late-owner kinds of the backends the app supplied. They go through a
// real module.Registrar for module "core", so what registers a core kind is a
// Registrar.Tasks call like any module's, which is what tasklint reads
// (archtest/tasklint) and P7 checks.
//
// It builds each kind over what P4 opened, and fails the boot when that is
// missing: with the switch on and no store there is nowhere to run a kind, and
// failing here is better than a switch that silently does nothing (fail closed).
// Describe opens no store and does not call it (boot runs it at P6 only for a real boot).
func (k *Kernel) registerCoreKinds(ctx context.Context) error {
	events := k.eventsTaskDefinitions()
	if !k.tasksFlipOn() && len(events) == 0 {
		return nil
	}
	if k.tasks == nil || k.store == nil || k.store.db == nil {
		return fmt.Errorf("kernel: the tasks switch or the events flip is on and no store is open (core.storage.path is not set: kernel.Config.StoragePath)")
	}
	// The events flip's kinds come first (the relay), then the tasks switch's.
	defs := events
	if k.tasksFlipOn() {
		cfg, err := k.coreConfig()
		if err != nil {
			return err
		}
		sks, ok := k.store.db.Keyspace(storageKeyspaceOwner)
		if !ok {
			return fmt.Errorf("kernel: tasks: the store has no %s keyspace", storageKeyspaceOwner)
		}
		lks, ok := k.store.db.Keyspace(logjournal.Owner)
		if !ok {
			return fmt.Errorf("kernel: tasks: the store has no %s keyspace", logjournal.Owner)
		}
		now := bootClock(ctx)
		admin := k.store.db.Admin()
		defs = append(defs,
			storagetasks.CompactTask(admin, storagetasks.Options{Now: now}),
			storagetasks.CensusTask(admin, storagetasks.NewCensusStore(sks), storagetasks.Options{Now: now}),
			executor.PruneDefinition(k.tasks.stores.Runs, func() time.Duration { return cfg.tasks.Get().HistoryRetention.Std() }, now),
			logjournal.PruneDefinition(logjournal.New(lks), func() logjournal.Settings { return *cfg.journal.Get() }, now),
		)
		defs = append(defs, k.windowDefinitions(cfg)...)
		if b := k.cfg.Interim.Holds; b != nil {
			defs = append(defs, providerhttp.ReleaseHoldsTask(b))
		}
		if b := k.cfg.Interim.Poll; b != nil {
			defs = append(defs, ai.BatchPollTask(b))
		}
		if b := k.cfg.Interim.Export; b != nil {
			defs = append(defs, diagnostics.ExportTask(b))
		}
	}

	r := module.NewRegistrar(module.RegistrarConfig{Context: ctx, Deps: module.Deps{Module: coreModuleID}})
	r.Resources(coreResourceKinds(defs)...)
	r.Tasks(defs...)
	if err := r.Err(); err != nil {
		return fmt.Errorf("kernel: core's own task registration: %w", err)
	}
	man := module.Manifest{ID: coreModuleID, TaskLedger: make([]tasks.Kind, 0, len(defs))}
	for _, d := range defs {
		man.TaskLedger = append(man.TaskLedger, d.Kind())
	}
	k.core = &entry{man: man, enabled: true, rec: r.Close()}
	return nil
}

// windowDefinitions is one core.window.<name> definition per core.tasks.windows
// entry, in name order. Its body is the scheduler's RunWindow, bound when
// constructTasks builds the scheduler: the scheduler takes the executor as its
// Starter, so it exists only after the executor that registers these. A window
// run that starts first (it cannot: the scheduler fires it, and the executor
// accepts starts only after both are built) fails instead of running nothing.
func (k *Kernel) windowDefinitions(cfg *coreConfig) []tasks.Definition {
	names := make([]string, 0, len(cfg.tasks.Get().Windows))
	for name := range cfg.tasks.Get().Windows {
		names = append(names, name)
	}
	slices.Sort(names)
	kt := k.tasks
	defs := make([]tasks.Definition, 0, len(names))
	for _, name := range names {
		defs = append(defs, scheduler.WindowDefinition(name, func(rc *tasks.RC) error {
			run := kt.runWindow.Load()
			if run == nil {
				return fmt.Errorf("kernel: window %s ran before the scheduler was built", name)
			}
			return (*run)(rc)
		}))
	}
	return defs
}

// coreResourceKinds are the resource kinds core's own definitions name in the
// module "core": what Registrar.Resources declares for them, derived from the
// definitions' Effects so a new core kind cannot name a resource nobody declared
// (P7 refuses that for a module, and core's go through the same check).
func coreResourceKinds(defs []tasks.Definition) []string {
	var kinds []string
	for _, d := range defs {
		e := tasks.CommonOf(d).Effects
		for _, r := range slices.Concat(e.ReadSet(), e.WriteSet()) {
			if r.Module == string(coreModuleID) && !slices.Contains(kinds, r.Kind) {
				kinds = append(kinds, r.Kind)
			}
		}
	}
	slices.SortFunc(kinds, cmp.Compare[string])
	return kinds
}
