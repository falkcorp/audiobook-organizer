// file: core/tasks/taskstest/parity/cases_supersede.go
// version: 1.0.0
// guid: 3b5b6f86-9128-4cae-8a62-cfad0e820088
// last-edited: 2026-11-10

package parity

import (
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// wantStates asserts each run reaches its state.
func wantStates(k *Kit, want map[tasks.RunID]tasks.RunState) {
	k.T.Helper()
	for id, s := range want {
		k.WaitState(id, s)
	}
}

func supersedeCases() []Case {
	const f = "resume_supersede_test.go:"
	return []Case{
		{
			Name: "KeepsOnlyTheNewestQuiescedPerKind", Head: f + "TestSupersedeStaleQuiesced_KeepsOnlyTheNewestPerDef",
			Setup:    "three interrupted_quiesced runs of one kind (ID order is age)",
			Action:   "boot",
			Expected: "the newest is resumed and succeeds; the two older end interrupted_dropped; the body runs once",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				a, b, c := k.Plant(p.Kind, tasks.StateInterruptedQuiesced), k.Plant(p.Kind, tasks.StateInterruptedQuiesced), k.Plant(p.Kind, tasks.StateInterruptedQuiesced)
				k.Boot(Defs(p.Def()))
				wantStates(k, map[tasks.RunID]tasks.RunState{c: tasks.StateSucceeded, a: tasks.StateInterruptedDropped, b: tasks.StateInterruptedDropped})
				wantCalls(t, k, p, 1)
			},
		},
		{
			Name: "SupersessionIsPerKindNotGlobal", Head: f + "TestSupersedeStaleQuiesced_IsPerDefNotGlobal",
			Setup:    "one interrupted_quiesced run for each of two kinds",
			Action:   "boot",
			Expected: "both are resumed and succeed; neither is dropped",
			Run: func(t *testing.T, k *Kit) {
				p, q := k.Probe("gotham.patrol"), k.Probe("gotham.stakeout")
				a, b := k.Plant(p.Kind, tasks.StateInterruptedQuiesced), k.Plant(q.Kind, tasks.StateInterruptedQuiesced)
				k.Boot(Defs(p.Def(), q.Def()))
				wantStates(k, map[tasks.RunID]tasks.RunState{a: tasks.StateSucceeded, b: tasks.StateSucceeded})
			},
		},
		{
			Name: "ALiveRunBeatsEveryQuiescedRun", Head: f + "TestSupersedeStaleQuiesced_ALiveRowBeatsEveryQuiescedRow",
			Setup:    "an older live run (queued, or running) and a newer interrupted_quiesced run of the same kind",
			Action:   "boot",
			Expected: "the quiesced run ends interrupted_dropped; the live run is kept and the body runs once",
			Run: func(t *testing.T, k *Kit) {
				for _, live := range []tasks.RunState{tasks.StateQueued, tasks.StateRunning} {
					k.Sub(string(live), func(t *testing.T, k *Kit) {
						p := k.Probe("gotham.patrol")
						l := k.Plant(p.Kind, live)
						q := k.Plant(p.Kind, tasks.StateInterruptedQuiesced)
						k.Boot(Defs(p.Def()))
						wantStates(k, map[tasks.RunID]tasks.RunState{q: tasks.StateInterruptedDropped, l: tasks.StateSucceeded})
						wantCalls(t, k, p, 1)
					})
				}
			},
		},
		{
			Name: "NeverSupersedesALiveRun", Head: f + "TestSupersedeStaleQuiesced_NeverSupersedesALiveRow",
			Setup:    "a queued and a running run of one kind and a queued run of another; none quiesced",
			Action:   "boot",
			Expected: "every run is resumed or dispatched on its own policy and succeeds; none is interrupted_dropped",
			Run: func(t *testing.T, k *Kit) {
				p, q := k.Probe("gotham.patrol"), k.Probe("gotham.stakeout")
				a, b, c := k.Plant(p.Kind, tasks.StateQueued), k.Plant(p.Kind, tasks.StateRunning), k.Plant(q.Kind, tasks.StateQueued)
				k.Boot(Defs(p.Def(), q.Def()))
				wantStates(k, map[tasks.RunID]tasks.RunState{a: tasks.StateSucceeded, b: tasks.StateSucceeded, c: tasks.StateSucceeded})
			},
		},
		{
			Name: "KeepsALoneQuiescedRun", Head: f + "TestSupersedeStaleQuiesced_KeepsALoneQuiescedRow",
			Setup:    "a single interrupted_quiesced run",
			Action:   "boot",
			Expected: "it is resumed, not dropped",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("gotham.patrol")
				a := k.Plant(p.Kind, tasks.StateInterruptedQuiesced)
				k.Boot(Defs(p.Def()))
				k.WaitState(a, tasks.StateSucceeded)
				wantCalls(t, k, p, 1)
			},
		},
		{
			Name: "MergingKindKeepsItsQuiescedRunBesideAQueuedOne", Head: f + "TestSupersedeStaleQuiesced_SetParameterizedDefSurvivesALiveRow",
			Setup:    "an interrupted_quiesced run and a queued run of one kind, with and without a queued-params merge policy",
			Action:   "boot",
			Expected: "with the merge policy both run; without it the quiesced run is dropped and the queued one runs",
			Run: func(t *testing.T, k *Kit) {
				k.Sub("merge", func(t *testing.T, k *Kit) {
					p := k.Probe("wayne.apply")
					a, b := k.Plant(p.Kind, tasks.StateInterruptedQuiesced), k.Plant(p.Kind, tasks.StateQueued)
					k.Boot(Defs(p.Def(MergeQueued())))
					wantStates(k, map[tasks.RunID]tasks.RunState{a: tasks.StateSucceeded, b: tasks.StateSucceeded})
				})
				k.Sub("plain", func(t *testing.T, k *Kit) {
					p := k.Probe("wayne.apply")
					a, b := k.Plant(p.Kind, tasks.StateInterruptedQuiesced), k.Plant(p.Kind, tasks.StateQueued)
					k.Boot(Defs(p.Def()))
					wantStates(k, map[tasks.RunID]tasks.RunState{a: tasks.StateInterruptedDropped, b: tasks.StateSucceeded})
				})
			},
		},
		{
			Name: "MergingKindStillCollapsesItsOwnBacklog", Head: f + "TestSupersedeStaleQuiesced_SetParameterizedDefStillCollapsesItsOwnBacklog",
			Setup:    "three interrupted_quiesced runs of a kind with a queued-params merge policy",
			Action:   "boot",
			Expected: "only the newest is kept and runs; the older two end interrupted_dropped",
			Run: func(t *testing.T, k *Kit) {
				p := k.Probe("wayne.apply")
				a, b, c := k.Plant(p.Kind, tasks.StateInterruptedQuiesced), k.Plant(p.Kind, tasks.StateInterruptedQuiesced), k.Plant(p.Kind, tasks.StateInterruptedQuiesced)
				k.Boot(Defs(p.Def(MergeQueued())))
				wantStates(k, map[tasks.RunID]tasks.RunState{c: tasks.StateSucceeded, a: tasks.StateInterruptedDropped, b: tasks.StateInterruptedDropped})
				wantCalls(t, k, p, 1)
			},
		},
	}
}
