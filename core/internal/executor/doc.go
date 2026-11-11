// file: core/internal/executor/doc.go
// version: 1.1.0
// guid: df169df5-2bec-4c0d-8d44-9d8c78263018
// last-edited: 2026-11-10

// Package executor is core's task executor (05-tasks §5T.3–§5T.7, F2-03):
// the one implementation of tasks.Executor. It is written fresh against
// tasks.RunStore and tasks.RunState, not moved from HEAD's engine, and it is
// dark: nothing outside core imports it, and before the tasks flip (F5) ABO's
// App constructs none, so HEAD's opsmetrics package stays the one owner of the
// <namespace>.ops families there (05-tasks §5T.11 "One owner per family").
//
// What replaces what (HEAD's task engine, cited by file name only: the
// registry package unless another package is named):
//
//	executor.go    registry.go, register.go, types.go   New, the def registry, WriteRuns, CAS moves
//	start.go       registry.go's enqueue, queued_merge.go, deps.go, approval.go
//	               Start, StartIn, mode default, dedupe, idempotency, plan checks, actor stamping
//	dispatch.go    dispatcher.go, worker.go, pause_gate.go, warmup_gate.go, abandoned.go
//	               the gates, the claim (fence epoch + 1), worker slots, zombies, rc.Await
//	attempt.go     worker.go, run_items.go, continue_in_place.go, live_tracker.go
//	               the RunHost behind RC/ItemRC, checkpoints, outcome mapping, yields
//	writer.go      the opswriter package's writer.go, fence.go, intent.go, evidence.go
//	               Writer: fence, Preview intents, ChangeSink, marks
//	control.go     registry.go's cancel and pause, finish.go
//	               cancel with grace and fence revocation, decide, approve, pause, discard
//	resume.go      resume.go, retry.go                   the boot sweep: policies, held runs, unknown kinds, restart guard
//	watchdog.go    watchdog.go                           stuck / never_reported / uncheckpointed strikes
//	shutdown.go    registry.go's Shutdown                bounded drain, interrupted stamping, one systemd extension
//	standdown.go   scan_standdown.go, scan_standdown_hold.go   stand-down holds, persisted markers, quiesce
//	batch.go       batch.go, batch_runner.go, chunk_ledger.go, batch_hooks.go   Batch: chunks, ledger, resume
//	fixer.go       preview.go and the v3 apply path      Fixer: plan rows, approval, apply with marks
//	budget.go      run_items.go's CPU budget and ref locks   the per-executor CPU semaphore, ref-lock table, lane leases
//	instruments.go the opsmetrics package                the 18 <ns>.ops instruments
//
// Every batch that carries a run-store write is built inside WriteRuns and
// committed with storage.Sync under storage.RetryOnConflict; every in-process
// lock (the executor's mu, the per-run writer, the ref-lock table, the CPU
// budget) is taken before WriteRuns or after it returns, never inside a
// stage (§5T.7). Nothing here is package-level state (R5): two executors in
// one process share nothing.
package executor
