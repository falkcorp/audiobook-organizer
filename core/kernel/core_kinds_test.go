// file: core/kernel/core_kinds_test.go
// version: 1.0.0
// guid: 8b4f1d26-c07e-4a93-b5d8-2e6a91f0c347
// last-edited: 2026-11-22

package kernel

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/internal/storagetasks"
	"github.com/falkcorp/mediacore/tasks"
)

// holdsStub, pollStub and exportStub are the three interim backends; none runs.
type holdsStub struct{}

func (holdsStub) ReleaseHolds(*tasks.RC, providerhttp.ReleaseHoldsParams) (providerhttp.ReleaseHoldsResult, error) {
	return providerhttp.ReleaseHoldsResult{}, nil
}

type pollStub struct{}

func (pollStub) PollBatches(*tasks.RC, ai.BatchPollParams) (ai.BatchPollResult, error) {
	return ai.BatchPollResult{}, nil
}

type exportStub struct{}

func (exportStub) Export(*tasks.RC, diagnostics.ExportParams) (diagnostics.ExportResult, error) {
	return diagnostics.ExportResult{}, nil
}

// coreOwnKinds are the kinds core registers with the switch on and no backend:
// the four F5-03 converted kinds and the nightly window (the one window
// core.tasks.windows declares by default).
var coreOwnKinds = []tasks.Kind{
	storagetasks.KindCompact, storagetasks.KindCensus, executor.KindPrune, logjournal.KindPrune,
	tasks.WindowKind("nightly"),
}

// registeredKinds is every kind the booted kernel's executor was given, sorted.
func registeredKinds(t *testing.T, k *Kernel) []tasks.Kind {
	t.Helper()
	if k.tasks == nil {
		t.Fatal("the kernel built no task stores")
	}
	var out []tasks.Kind
	for _, d := range k.tasks.defs {
		out = append(out, d.Kind())
	}
	slices.Sort(out)
	return out
}

func sortedKinds(ks ...tasks.Kind) []tasks.Kind {
	out := slices.Clone(ks)
	slices.Sort(out)
	return out
}

// TestTasksBoot_Off_RegistersNothing: without the switch core registers no
// kind, so a boot whose modules register none and find no stored run builds and
// starts neither the executor nor the scheduler, as before the flip. The hello
// example and today's ABO boot are this case.
func TestTasksBoot_Off_RegistersNothing(t *testing.T) {
	c, _ := taskConfig(t)
	c.Interim = InterimBackends{Holds: holdsStub{}, Poll: pollStub{}, Export: exportStub{}}
	k := mustNew(t, c)
	if k.core != nil {
		t.Fatalf("core registered %d kinds with the switch off", len(k.core.rec.Tasks))
	}
	if k.tasks == nil || k.tasks.exec != nil || k.tasks.sched != nil {
		t.Fatalf("an executor or scheduler was built with the switch off: %+v", k.tasks)
	}
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatalf("StartTasks: %v", err)
	}
}

// TestTasksBoot_On_RegistersCoreKinds: with the switch on core's own kinds are
// registered through a core Registrar, checked like a module's and given to the
// executor and the scheduler, which then start. No interim kind is registered
// when no backend is supplied.
func TestTasksBoot_On_RegistersCoreKinds(t *testing.T) {
	c, _ := taskConfig(t)
	c.TasksBoot = true
	k := mustNew(t, c)
	if got, want := registeredKinds(t, k), sortedKinds(coreOwnKinds...); !slices.Equal(got, want) {
		t.Fatalf("registered kinds = %v, want %v", got, want)
	}
	if k.tasks.exec == nil || k.tasks.sched == nil {
		t.Fatal("the switch is on and no executor or scheduler was built")
	}
	if got := k.core.man.ID; got != coreModuleID {
		t.Fatalf("core entry ID = %q", got)
	}
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatalf("StartTasks: %v", err)
	}
	// The scheduler discovered the nightly window and its members: the three
	// scheduled core kinds.
	if got := k.tasks.sched.Members("nightly"); len(got) == 0 {
		t.Error("the nightly window has no members")
	}
}

// TestTasksBoot_InterimBackends_EachAlone: each late-owner kind is registered
// only over its own backend, and only that one.
func TestTasksBoot_InterimBackends_EachAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		b    InterimBackends
		kind tasks.Kind
	}{
		"holds":  {InterimBackends{Holds: holdsStub{}}, providerhttp.ReleaseHoldsKind},
		"poll":   {InterimBackends{Poll: pollStub{}}, ai.BatchPollKind},
		"export": {InterimBackends{Export: exportStub{}}, diagnostics.ExportKind},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := taskConfig(t)
			c.TasksBoot, c.Interim = true, tc.b
			k := mustNew(t, c)
			if got, want := registeredKinds(t, k), sortedKinds(append(slices.Clone(coreOwnKinds), tc.kind)...); !slices.Equal(got, want) {
				t.Fatalf("registered kinds = %v, want %v", got, want)
			}
		})
	}
	t.Run("all three", func(t *testing.T) {
		c, _ := taskConfig(t)
		c.TasksBoot = true
		c.Interim = InterimBackends{Holds: holdsStub{}, Poll: pollStub{}, Export: exportStub{}}
		k := mustNew(t, c)
		want := sortedKinds(append(slices.Clone(coreOwnKinds), providerhttp.ReleaseHoldsKind, ai.BatchPollKind, diagnostics.ExportKind)...)
		if got := registeredKinds(t, k); !slices.Equal(got, want) {
			t.Fatalf("registered kinds = %v, want %v", got, want)
		}
	})
}

// TestTasksBoot_On_NoStoreFailsBoot: the switch with nowhere to keep runs is a
// boot error, not a switch that does nothing (fail closed).
func TestTasksBoot_On_NoStoreFailsBoot(t *testing.T) {
	c, _ := taskConfig(t)
	c.TasksBoot, c.StoragePath = true, ""
	_, err := newK(t, c)
	if err == nil || !strings.Contains(err.Error(), "no store is open") {
		t.Fatalf("New = %v, want a refusal naming the missing store", err)
	}
}

// TestTasksBoot_OnlyCoreRegisters_ModuleKindsStillJoin: the core registration and
// a module's coexist: the module's kind and core's are one definition set, each
// kind registered once.
func TestTasksBoot_CoreAndModuleKinds(t *testing.T) {
	g, _ := patroller(nil)
	c, _ := taskConfig(t, g)
	c.TasksBoot = true
	k := mustNew(t, c)
	want := sortedKinds(append(slices.Clone(coreOwnKinds), patrolKind)...)
	if got := registeredKinds(t, k); !slices.Equal(got, want) {
		t.Fatalf("registered kinds = %v, want %v", got, want)
	}
}

// TestTasksBoot_CoreKindsRun: the registered core kinds run over what the boot
// opened. The census kind writes the record under c/storage/ through its sink
// (storage keeps the keyspace owner unexported), and the nightly window's body
// is the scheduler's RunWindow, which starts its members: a window that ran
// before the scheduler was bound would fail here.
func TestTasksBoot_CoreKindsRun(t *testing.T) {
	c, _ := taskConfig(t)
	c.TasksBoot = true
	k := mustNew(t, c)
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatalf("StartTasks: %v", err)
	}
	cl := k.TaskClient()
	start := func(kind tasks.Kind) tasks.RunView {
		t.Helper()
		ref, err := cl.Start(t.Context(), kind, tasks.StartRequest{Mode: tasks.ModeRequestLive})
		if err != nil {
			t.Fatalf("Start %s: %v", kind, err)
		}
		v := waitRun(t, cl, ref.ID)
		if v.State != tasks.StateSucceeded {
			t.Fatalf("%s ended %s (%s)", kind, v.State, v.Reason)
		}
		return v
	}

	start(storagetasks.KindCensus)
	sks, ok := k.store.db.Keyspace(storageKeyspaceOwner)
	if !ok {
		t.Fatal("no core/storage keyspace")
	}
	rec, found, err := storagetasks.NewCensusStore(sks).Latest(t.Context())
	if err != nil || !found || len(rec.Partitions) == 0 {
		t.Fatalf("census record = %+v found=%v err=%v, want a recorded census", rec, found, err)
	}

	start(executor.KindPrune)
	start(logjournal.KindPrune)
	start(tasks.WindowKind("nightly"))
}
