// file: core/tasks/taskstest/parity/excluded.go
// version: 1.0.0
// guid: 2e6c15fd-f841-48c5-9d9a-87cce95e157b
// last-edited: 2026-11-10

package parity

// Exclusion is a HEAD test the kit does not port, and why. The list may only
// shrink: a reason that stops being true moves the test into Cases.
type Exclusion struct {
	Head   string // "file.go:TestFunc", as in Case.Head
	Reason string
	// Partial marks a test whose other half IS ported as a Case: the coverage
	// test then requires the Head in both lists.
	Partial bool
}

// Excluded lists every HEAD resume, cancel and shutdown test with no oracle
// in core's executor.
var Excluded = []Exclusion{
	{Head: "resume_test.go:TestResume_RestartResetFailureDoesNotAnnounceQueued",
		Reason: "store-error injection on HEAD's v2 row API (ResetOperationV2ForResume, op_errors_v2); core's row store has no such fault seam"},
	{Head: "resume_test.go:TestResume_RestartResetFailureAtRuntimeLeavesScanQuiesced",
		Reason: "store-error injection on HEAD's v2 row API (failResetForResume, op_errors_v2)"},
	{Head: "resume_test.go:TestResumeRequeue_DropWriteFailureInsertsNoReplacement",
		Reason: "HEAD's requeue inserts a replacement row and retires the old one; core reruns the same run (one row per run), so there is no half-done retire-then-insert to fail; also store-error injection"},
	{Head: "resume_shutdown_roundtrip_test.go:TestMaintenancePolicy_RestartWithoutCheckpointing",
		Reason: "HEAD watchdog 'uncheckpointed' strike ledger; core has no strike ledger"},
	{Head: "resume_supersede_test.go:TestSupersedeStaleQuiesced_GroupsFormerAndCanonicalIDsTogether",
		Reason: "former/canonical definition-ID aliases were removed by D10"},
	{Head: "resume_test.go:TestResume_ReconcileScanRowsAreDroppedThroughTheirDef",
		Reason:  "the former-ID half needs a definition-ID alias (removed by D10); the current-ID half is ported as a case",
		Partial: true},
	{Head: "cancel_row_cas_test.go:TestCancel_StubAlongsideANonQueuedRow",
		Reason: "HEAD's dispatcher claim placeholder (stub handle in r.running) has no core equivalent"},
	{Head: "cancel_row_cas_test.go:TestCancel_StoppedLiveRunSucceedsDespiteAStoreError",
		Reason: "store-error injection on HEAD's v2 row API (failCancelCAS, failRead)"},
	{Head: "cancel_row_cas_test.go:TestCancel_StubDroppedByTheDispatcherPublishesOpTerminal",
		Reason: "stub handle and the dispatcher's post-claim re-read have no core equivalent"},
	{Head: "cancel_row_cas_test.go:TestCancel_PickupGateHonorsAUserCancelItsOwnWriteRaced",
		Reason: "store-error injection on HEAD's v2 row API (failRead) plus HEAD's userCanceled re-check in settleInterrupted; the outcome duplicates the cancel-versus-quiesce cases"},
	{Head: "cancel_row_cas_test.go:TestCancel_StoreErrorWithNoStoppedRunIsReturned",
		Reason: "store-error injection on HEAD's v2 row API (failCancelIf, failCancelCAS)"},
	{Head: "shutdown_stub_handle_test.go:TestShutdown_StubHandleNilCancel_NoPanic",
		Reason: "nil-cancel stub handle is a HEAD dispatcher placeholder; core has none"},
	{Head: "shutdown_stub_handle_test.go:TestCancel_StubHandleNilCancel_NoPanic",
		Reason: "nil-cancel stub handle and queuedCancel flag are HEAD-only; cancel of a queued run is covered by the persisted-row cases"},
	{Head: "dispatch_shutdown_race_test.go:TestExecuteRun_ShutdownBeforeInfiniteRestartCheckWritesNothing",
		Reason: "asserts HEAD's pickup-step ordering (infinite-restart check versus the shutdown gate); the outcome is covered by the shutdown-before-pickup case"},
}
