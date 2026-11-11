// file: core/internal/executor/doc.go
// version: 1.0.0
// guid: df169df5-2bec-4c0d-8d44-9d8c78263018
// last-edited: 2026-11-10

// Package executor is core's task executor (05-tasks §5T.3–§5T.7, F2-03):
// the one implementation of tasks.Executor. It is written fresh against
// tasks.RunStore and tasks.RunState, not moved from HEAD's engine, and it is
// dark: nothing outside core imports it, and before the tasks flip (F5) ABO's
// App constructs none, so internal/opsmetrics stays the one owner of the
// <namespace>.ops families there (05-tasks §5T.11 "One owner per family").
//
// What replaces what (HEAD's internal/operations/registry, cited by file
// name only):
//
//	executor.go   registry.go, register.go           New, the def registry, WriteRuns, CAS moves
//	start.go      enqueue.go, dedupe.go, opmode       Start, StartIn, mode default, dedupe, idempotency, actor stamping
//	dispatch.go   dispatcher.go, worker.go            the gates, the claim (fence epoch + 1), worker slots
//	attempt.go    worker.go, run_items.go, progress   the RunHost behind RC/ItemRC, outcome mapping, yield
//	writer.go     pkg/ops writer, write gates         Writer: fence, Preview intents, ChangeSink, marks
//	control.go    cancel.go, cancel_persisted.go      cancel with grace and fence revocation, decide, approve, pause, discard
//	resume.go     resume.go, resume_*.go              the boot sweep: policies, held runs, unknown kinds, restart guard
//	watchdog.go   watchdog.go, strikes                stuck / never_reported / uncheckpointed strikes
//	shutdown.go   shutdown*.go                        bounded drain, interrupted stamping, one systemd extension
//	standdown.go  scan_standdown.go                   stand-down holds, persisted markers, quiesce
//	batch.go      v3 batch runner, chunk ledger       Batch: chunks, ledger, resume
//	fixer.go      v3 fixer runner, approval.go        Fixer: plan rows, approval, apply with marks
//	budget.go     cpu budget, ref locks, lanes        the per-executor CPU semaphore, ref-lock table and lane leases
//	await.go      internal/operations/childop         rc.Await
//	instruments.go internal/opsmetrics                the 18 <ns>.ops instruments
//
// Every batch that carries a run-store write is built inside WriteRuns and
// committed with storage.Sync under storage.RetryOnConflict; every in-process
// lock (the executor's mu, the per-run writer, the ref-lock table, the CPU
// budget) is taken before WriteRuns or after it returns, never inside a
// stage (§5T.7). Nothing here is package-level state (R5): two executors in
// one process share nothing.
package executor
