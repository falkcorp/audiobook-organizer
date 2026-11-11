// file: core/tasks/taskstest/parity/parity_test.go
// version: 1.0.2
// guid: 5b96825f-95af-4625-bb3f-c9c627503912
// last-edited: 2026-11-10

package parity

import (
	"strings"
	"testing"
	"testing/synctest"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/coretest"
)

// TestParity_ResumeCancelShutdown runs every ported HEAD test against core's
// executor. Each case runs in a synctest bubble: the clock is fake, so the
// settle sleeps and the 30s waits cost nothing and the interleavings repeat.
func TestParity_ResumeCancelShutdown(t *testing.T) {
	for _, c := range Cases() {
		t.Run(c.Name, func(t *testing.T) {
			var skipped string
			synctest.Test(t, func(t *testing.T) {
				defer func() {
					if t.Skipped() {
						skipped = "skipped inside the bubble (executor bug or other; see the log)"
					}
				}()
				c.Run(t, NewKit(t))
			})
			if skipped != "" {
				t.Skip(skipped)
			}
		})
	}
}

// headTests is HEAD's resume, cancel and shutdown test inventory
// (internal/operations/registry, 15 files, 68 tests).
var headTests = []string{
	"resume_test.go:TestResume_DropLeavesInterruptedDropped",
	"resume_test.go:TestResume_AskLeavesInterruptedAsk",
	"resume_test.go:TestResume_RestartReDispatchesWithIncrementedResumeCount",
	"resume_test.go:TestResume_RestartResetFailureDoesNotAnnounceQueued",
	"resume_test.go:TestResume_RestartResetFailureAtRuntimeLeavesScanQuiesced",
	"resume_test.go:TestResume_RestartDoesNotInheritStaleLiveness",
	"resume_test.go:TestResume_RequeueFreshRun",
	"resume_test.go:TestResume_ReconcileScanRowsAreDroppedThroughTheirDef",
	"resume_test.go:TestResume_RowUnderTheTaskNameReconcileScanIsDroppedAsUnknown",
	"resume_test.go:TestResume_PreservesParamsAcrossRestartAndRequeue",
	"resume_test.go:TestResumeRequeue_DropWriteFailureInsertsNoReplacement",
	"resume_shutdown_roundtrip_test.go:TestResume_RealShutdownIsResumedByTheSweep",
	"resume_shutdown_roundtrip_test.go:TestResume_ShutdownTimeoutLeavesQuiescedAndResumable",
	"resume_shutdown_roundtrip_test.go:TestResume_QueuedAtShutdownIsRestartedNotDropped",
	"resume_shutdown_roundtrip_test.go:TestMaintenancePolicy_RestartWithoutCheckpointing",
	"resume_supersede_test.go:TestSupersedeStaleQuiesced_KeepsOnlyTheNewestPerDef",
	"resume_supersede_test.go:TestSupersedeStaleQuiesced_IsPerDefNotGlobal",
	"resume_supersede_test.go:TestSupersedeStaleQuiesced_ALiveRowBeatsEveryQuiescedRow",
	"resume_supersede_test.go:TestSupersedeStaleQuiesced_NeverSupersedesALiveRow",
	"resume_supersede_test.go:TestSupersedeStaleQuiesced_KeepsALoneQuiescedRow",
	"resume_supersede_test.go:TestSupersedeStaleQuiesced_SetParameterizedDefSurvivesALiveRow",
	"resume_supersede_test.go:TestSupersedeStaleQuiesced_GroupsFormerAndCanonicalIDsTogether",
	"resume_supersede_test.go:TestSupersedeStaleQuiesced_SetParameterizedDefStillCollapsesItsOwnBacklog",
	"run_items_resume_test.go:TestRunItems_ResumeFrom_SkipsExactlyThePrefix",
	"run_items_resume_test.go:TestRunItems_ResumeFrom_KeepsAbsoluteProgress",
	"run_items_resume_test.go:TestRunItems_ResumeFrom_BeyondEndRunsNothing",
	"run_items_resume_test.go:TestRunItems_Watermark_StopsAtAGap",
	"run_items_resume_test.go:TestRunItems_Watermark_FailedItemDoesNotAdvance",
	"run_items_resume_test.go:TestRunItems_Watermark_AllCompleteReachesTotal",
	"run_items_resume_test.go:TestRunItems_Watermark_IsAbsoluteAcrossResume",
	"run_items_resume_test.go:TestRunItems_CheckpointEvery_Throttles",
	"run_items_resume_test.go:TestRunItems_NoCheckpointFn_Unchanged",
	"fixer_resume_real_writer_test.go:TestFixerResumeRealWriter",
	"cancel_persisted_test.go:TestCancel_PersistedNonRunningRowBecomesCanceled",
	"cancel_persisted_test.go:TestCancel_TerminalRowIsNotFound",
	"cancel_persisted_test.go:TestCancel_UnknownIDIsNotFound",
	"cancel_requeue_race_test.go:TestCancel_RequeueClaimedByWorkerBeforeTheCancelWriteCancelsTheRun",
	"cancel_row_cas_test.go:TestCancel_LostQueuedCASToThePickupGateCancelsTheQuiescedRow",
	"cancel_row_cas_test.go:TestCancel_WonQueuedCASIsNotOverwrittenByThePickupGate",
	"cancel_row_cas_test.go:TestCancel_LiveHandleAtThePickupGateStillCancelsTheRow",
	"cancel_row_cas_test.go:TestCancel_WaitingDepsPromotedAndClaimedBeforeTheCancelWriteCancelsTheRun",
	"cancel_row_cas_test.go:TestCancel_RunFinishedBeforeTheReReadIsNotFound",
	"cancel_row_cas_test.go:TestCancel_StubAlongsideANonQueuedRow",
	"cancel_row_cas_test.go:TestCancel_BetweenTheRunsFinalStatusAndItsWriteKeepsTheRowCanceled",
	"cancel_row_cas_test.go:TestCancel_StoppedLiveRunSucceedsDespiteAStoreError",
	"cancel_row_cas_test.go:TestCancel_StubDroppedByTheDispatcherPublishesOpTerminal",
	"cancel_row_cas_test.go:TestCancel_PickupGateHonorsAUserCancelItsOwnWriteRaced",
	"cancel_row_cas_test.go:TestCancel_InTheReleaseGapOfACompletedRunKeepsCompleted",
	"cancel_row_cas_test.go:TestCancel_StoreErrorWithNoStoppedRunIsReturned",
	"cancel_row_cas_test.go:TestShutdown_StampedCanceledRunPublishesOpTerminal",
	"cancel_row_cas_test.go:TestCancel_WorkerDoesNotPublishAStaleInterruptedStatus",
	"shutdown_drain_test.go:TestShutdown_WaitsForOpGoroutineToExit",
	"shutdown_cause_test.go:TestShutdownCancelsRunsWithShutdownCause",
	"shutdown_race_test.go:TestShutdownDrainsDepNotifyGoroutines_RealStore",
	"shutdown_terminal_status_test.go:TestShutdown_InterruptedRunIsRecordedResumable",
	"shutdown_terminal_status_test.go:TestShutdown_UserCanceledRunStaysCanceled",
	"shutdown_terminal_status_test.go:TestShutdown_DropPolicyIsRecordedDropped",
	"shutdown_stub_handle_test.go:TestShutdown_StubHandleNilCancel_NoPanic",
	"shutdown_stub_handle_test.go:TestCancel_StubHandleNilCancel_NoPanic",
	"dispatch_shutdown_race_test.go:TestDispatchCycle_ShutdownDuringListClaimsNothing",
	"dispatch_shutdown_race_test.go:TestExecuteRun_ShutdownBeforePickupNeverRuns",
	"dispatch_shutdown_race_test.go:TestExecuteRun_ShutdownBeforeInfiniteRestartCheckWritesNothing",
	"dispatch_shutdown_race_test.go:TestExecuteRun_ShutdownDuringPickupNeverRuns",
	"dispatch_shutdown_race_test.go:TestShutdown_ReleasesBufferedClaimWithoutStatus",
	"dispatch_shutdown_race_test.go:TestShutdown_TimeoutReleasesStubsAndInterruptsRuns",
	"dispatch_shutdown_race_test.go:TestShutdown_TimeoutStampsAUserCanceledRunCanceled",
	"sweeptick_shutdown_race_test.go:TestShutdownDrainsSweepTicker_RealStore",
	"sweeptick_shutdown_race_test.go:TestLeakedRegistrySweepDoesNotPanicOnClosedStore",
}

// TestParity_CoversEveryHeadTest checks that cases and exclusions together
// account for each HEAD test exactly once (a partial exclusion also has its
// case) and that nothing names a test HEAD does not have.
func TestParity_CoversEveryHeadTest(t *testing.T) {
	if len(headTests) != 68 {
		t.Fatalf("inventory has %d tests, want 68", len(headTests))
	}
	known := map[string]bool{}
	for _, h := range headTests {
		if known[h] {
			t.Errorf("duplicate inventory entry %s", h)
		}
		known[h] = true
	}
	cases, full, partial := map[string]int{}, map[string]int{}, map[string]int{}
	for _, c := range Cases() {
		if !known[c.Head] {
			t.Errorf("case %q names unknown HEAD test %q", c.Name, c.Head)
		}
		if c.Run == nil || c.Setup == "" || c.Action == "" || c.Expected == "" || !strings.Contains(c.Head, ".go:Test") {
			t.Errorf("case %q is incomplete", c.Name)
		}
		cases[c.Head]++
	}
	for _, e := range Excluded() {
		if !known[e.Head] {
			t.Errorf("exclusion names unknown HEAD test %q", e.Head)
		}
		if e.Reason == "" {
			t.Errorf("exclusion %q has no reason", e.Head)
		}
		if e.Partial {
			partial[e.Head]++
		} else {
			full[e.Head]++
		}
	}
	for _, h := range headTests {
		switch {
		case full[h] > 1 || partial[h] > 1 || cases[h] > 1 && full[h]+partial[h] == 0:
			t.Errorf("%s is listed more than once", h)
		case partial[h] == 1 && cases[h] != 1:
			t.Errorf("%s is a partial exclusion and needs exactly one case, has %d", h, cases[h])
		case full[h] == 1 && cases[h] != 0:
			t.Errorf("%s is both excluded and a case", h)
		case full[h]+partial[h]+cases[h] == 0:
			t.Errorf("%s is neither a case nor excluded", h)
		}
	}
}

// TestExerciseInstruments (05-metrics §12's executor cell, F2-03): a
// standalone executor with its instruments on records every one of its 18
// instruments into the Provider it is given, and that Provider's gather
// returns no error.
func TestExerciseInstruments(t *testing.T) {
	p := coretest.MetricsProvider(t, app.Options{MetricNamespace: "gotham"})
	if fams := ExerciseInstruments(t, p); len(fams) == 0 {
		t.Fatal("no ops family gathered")
	}
}
