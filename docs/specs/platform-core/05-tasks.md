<!-- file: docs/specs/platform-core/05-tasks.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3472a3a5-c492-47aa-9b0b-17411cfb25d3 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Tasks (`core/tasks`)

Evidence commit `748fd787a`. Raw inputs: [`raw/06-tasks-ops.md`](../../plans/platform-core/raw/06-tasks-ops.md)
(the inventory every count below comes from), [`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md),
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md) §7 (ops decision digests),
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §7.
Names follow [03-architecture](03-architecture.md) §3.6; layer per R4 is layer 3
(`events`, `outbox`, `tasks`, `health`, `updater` are peers).

## 5T.0 Decision: N8 and N9 confirmed, with six amendments

[02-decision-record](02-decision-record.md) N8 (parallelism is a required typed property) and N9
(one task system: the v3 authoring model on the production v2 executor) are **confirmed**. The
evidence for N9 is unchanged: v2 runs every op in production, v3 has the better authoring model and
zero native definitions (`internal/opscatalog/catalog.go:56`, `raw/06` §0). Reading the v3 code
directly forced six amendments, each of which is a place where copying `pkg/ops` as-is would carry
a defect into core:

| # | Amendment | Evidence | Why |
|---|---|---|---|
| A1 | `pkg/ops.Concurrency`'s zero value is **not** carried over: `tasks.Parallelism` has no valid zero value. | `pkg/ops/batch.go:123` (`concCPU concKind = iota // the zero value: an omitted Concurrency is CPU()`), `pkg/ops/batch.go:31-33` | v3 made the omission *parallel* instead of *sequential*, which fixes the v2 trap (`run_items.go:155-157` clamps 0 to 1) but still lets an author omit the decision. N8 asks that the decision be stated. |
| A2 | `ResumePolicy` is **required** (zero refused), not defaulted by shape. | v2 refuses zero at boot (`internal/operations/registry/types.go:59`, `ValidateOpDef` `registry.go:747`); v3 defaults by kind (`pkg/ops/runtime.go:73-82`: Task→Drop, Batch→Continue, Fixer→Ask) | 162 of 233 ops drop on restart (`raw/06` §6.2). Whether an interrupted run is lost is a data-correctness decision; a shape default hides it. |
| A3 | The v3 `Writer` (hard-typed to `database.Book`/`BookFile`/`BookAuthor`) becomes a generic gated-write primitive; typed writers move to the module that owns the type. | `pkg/ops/writer.go:46-92`, `SubjectType book/book_file` `pkg/ops/writer.go:112-118` | Core may not import app types (R1). The *gate* (fence, preview, journal, evidence) is generic; `ModifyBook` is not. |
| A4 | `Resource` is `{Module, Kind, ID}` and `ConcurrencyKey` is namespaced (gate G8). | v2 `type Resource string` `types.go:384`; v3 `type Resource string` `pkg/ops/effects.go:16-39`; `ConcurrencyKey string` `types.go:97` | Two modules declaring `"files"` would serialise each other today. |
| A5 | `LaneMac` becomes a module-declared lane; core has no audio concept. | `pkg/ops/lanes.go:12-15`, `pkg/ops/definition.go:105-107` | Lanes are a generic capacity class; "audio decode" is ABO's. |
| A6 | Subprocess isolation (`OperationDef.Isolate`) and `Synchronous` are **not** carried over. | 0 non-test defs set `Isolate: true` or `Synchronous: true` (grep at HEAD); `itunes.import` notes "DISABLED ... Pebble is single-writer" (`raw/06` §1.1) | A mode nothing can use is dead weight and a second code path in the executor. |

What is kept verbatim in concept: the four shapes (Task/Batch/Fixer/Pipeline, `pkg/ops/definition.go:35-47`),
`Effects` and the write-set gate, `Schedule` as the single timer declaration (`pkg/ops/schedule.go:17-32`),
`CancelContract` (`pkg/ops/contract.go:10-48`), `Approval` (`pkg/ops/approval.go:18-27`), the run
context `RC` replacing v2's `Reporter` + type-asserted side interfaces (`pkg/ops/rc.go:113-118`), the
chunk-ledger Batch runner (`internal/operations/registry/batch_runner.go`), the ten-state v3 machine
with compare-and-set transitions (`internal/operations/state/v3.go:20-31,187`), v2's dispatcher gates,
watchdog, resume and shutdown (`raw/06` §5), `ErrContinueInPlace` (`continue_in_place.go:66`), and
the preview-by-default rule (`internal/operations/opmode/dryrun.go:1-25`).

## 5T.1 Purpose & scope

`core/tasks` is the **only** durable background-work system in every app: definition, enqueue,
dispatch, execution, progress, checkpoint/resume, cancel, approval, schedule, history. It owns
`c/tasks/` storage and the `/api/core/v1/tasks` API. It does **not** own: domain writers (modules),
the outbox (`core/outbox`, [05-events](05-events.md), which *uses* tasks for its relay), SSE
transport ([05-realtime](05-realtime.md)), permission model ([05-rbac](05-rbac.md)).

In scope: everything in `raw/06` §0's four generations (v1 rows, v2 `registry`, v2.5
`maintenance.MaintenanceJob`, v3 `pkg/ops`), the scheduler (`internal/scheduler`), `internal/repairs`
as the Fixer shape, `internal/walkresume`, `internal/operations/{childop,opmode,opswriter,state}`,
`internal/opsmetrics`, `internal/opscatalog`, `pkg/plugin/sdk`. Out of scope: `internal/ai/aijobs`
(durable OpenAI Batch-API jobs; [05-ai](05-ai.md) decides whether they become tasks), the
fingerprint remote-worker *protocol* (stays in `books`, see 5T.3.9).

## 5T.2 Current state at HEAD

| Fact | Anchor |
|---|---|
| Four generations coexist; v2 is the executor; v3 has 0 native defs | `raw/06` §0; `internal/opscatalog/catalog.go:56` |
| 233 registered defs = 199 `OperationDef` literals + 34 `MaintenanceJob`s wrapped as defs (`maintenance.`+id); 1 more literal is an excluded stub | `raw/06` §6 method note, §6.2; `internal/server/maintenance_job_op.go:146` |
| By class: audiobook 199, generic 24, iTunes 7, Deluge 4 | `raw/06` §6.2 |
| By resume: drop 162, restart(checkpoint) 51, requeue 18, ask 3 | `raw/06` §6.2 |
| 38 scheduler `TaskDefinition`s (36 enqueue a def; `transcode` enqueues nothing; `batch_poller` calls a func) | `raw/06` §6.1; `internal/scheduler/tasks.go:529,1554` |
| 24 repairs fixers registered by the maintenance plugin | `internal/plugins/maintenance/plugin.go:53-60` (24 `repairsReg.Register` calls) |
| v2 `OperationDef.Schedule *string` is never evaluated as a timer (15 defs carry one) | `internal/operations/registry/types.go:171`; `internal/scheduler/tasks.go:1046-1050`; `raw/06` §6.2 |
| `deluge.protected-paths-sync` declares `*/30 * * * *` and therefore never runs unattended | `raw/06` §6.1 note (d) |
| `OperationDef.Cancellable` is persisted but never read by `Cancel` | `types.go:52`; only use `registry.go:900` |
| `RunItems` zero `Concurrency` is sequential (clamp) | `internal/operations/registry/run_items.go:155-157` |
| `RunItems` `Label(i,total)` runs inside workers; the pre-work call is unlocked | `run_items.go:45-54,268`; post-work under `progressMu` `run_items.go:281-285` |
| Doc says "first item error" on `ErrModeFail`; parallel returns `errors.Join` | `run_items.go:149` vs `:340-342,362-364`; sequential returns bare first error `:316` |
| Parallel cancellation is dispatcher-only: `select` between `cancelCtx.Done()` and a free slot picks at random, and `runOne` never checks `ctx` before `fn` | `run_items.go:336-345,348-350`; worked around per op (`docs/decisions/2026-10-31-lsh-backfill-cancel-guard.md`, `raw/08` §7) |
| The v3 Batch runner, by contrast, uses a fixed worker set and checks `workCtx` per chunk and per item | `batch_runner.go:784-805,831-833` |
| Operator pause gate is a package global because `RunItems` is a free function | `pause_gate.go:197-199` |
| Scan stand-down is hard-coded to `library.scan` in the dispatcher | `dispatcher.go:155-161` (Gate 3.5) |
| Write-set gate is write/write only, string resources | `dispatcher.go:163-178` |
| Event triggers: `deluge.path-update` declares `Triggers: book.relocated`; nothing publishes `book.relocated` and the registry only persists trigger names | `internal/plugins/deluge/path_update.go:49-53`; `registry.go:882,2330`; grep for `"book.relocated"` finds only the declaration |
| Executor workers: 8 | `internal/operations/registry/register.go:102` |
| Shutdown: ops drain budget 10 s, HTTP 30 s, unit `TimeoutStopSec` 30 s | `internal/server/server_lifecycle.go:611,582,176` |
| `ResumeRestart` AST guard (21-entry allowlist) | `internal/arch/resume_restart_guard_test.go`; `raw/08` §7 |
| Repo-wide `RunItems` concurrency lint | `internal/plugins/runitems_concurrency_lint_test.go` (`raw/06` §1.6) |
| opv2/opv3 dual-write in one batch so the previous binary can be redeployed | `internal/database/pebble_store_ops_v3.go:28-45` |
| v3 approve route asks only `library.view`; def `Approver` checked inside; verified auth method required | `internal/server/handlers/opsv3/handler.go:228-248`; `opsv3/runs.go:401-431` |
| `pkg/ops` imports `internal/auth` and `internal/database` | `pkg/ops/definition.go:20`; `pkg/ops/writer.go` |
| Registration through `init()` globals (`maintenance.Register` panics on duplicate, `addOpRegistrar` slice) | `internal/maintenance/registry.go:19-24`; `raw/07` §5.1 B, C |
| Metrics are OTel instruments named `audiobook_organizer.ops.*` | `internal/opsmetrics/opsmetrics.go:212-250` |

## 5T.3 Go API

Package `github.com/falkcorp/mediacore/tasks`. Implementation in `core/internal/executor` (dispatcher,
workers, watchdog, resume, batch runner) and `core/internal/scheduler`. Apps import only `tasks`.

### 5T.3.1 Kinds, shapes, definitions

```go
package tasks

// Kind is "<moduleID>.<name>". name is one or more dot-separated segments of
// [a-z][a-z0-9-]*; ':' is never allowed (it is the storage separator, as in
// v2 ValidateOpDef). Example: "books.dedup.full-scan", "core.backup.run".
type Kind string

func ParseKind(s string) (Kind, error)
func (k Kind) Module() string // text before the first '.'

type Shape uint8

const (
	ShapeTask     Shape = iota + 1 // one function, runs once
	ShapeBatch                     // source + per-item function; the runner owns the loop
	ShapeFixer                     // plan -> approve -> apply over rows
	ShapePipeline                  // DAG of child definitions
)

// Definition is opaque; build it with Task, Batch, Fixer or Pipeline.
// Constructors never panic: problems are collected and reported by
// module.Registrar at registration, which fails app.New (fail-closed).
type Definition interface {
	Kind() Kind
	Shape() Shape
	spec() *spec // unexported: only package tasks implements Definition
}

type Common struct {
	Title string // required
	Help  string // 1-3 sentences; archtest warns when empty

	Effects Effects     // required: ReadOnly(...) or Writes(...)
	Perms   Permissions // required: Perms.Start at least
	Resume  ResumePolicy // required (A2): zero value refused

	Cancel          CancelContract // zero = shape default (5T.3.6)
	ConcurrencyKey  Key            // zero = Key(Kind)
	NotWhile        []Kind         // v2 DependsOn: must not be running
	Priority        Priority       // zero = PriorityNormal
	Timeout         time.Duration  // per attempt; 0 = shape default; > 24h refused
	ProgressTimeout time.Duration  // watchdog gap; 0 = derived (5T.3.6)
	Schedule        *Schedule      // the ONE timer declaration (5T.3.8)
	Approval        *Approval      // required for ShapeFixer
	Ready           []ReadySignal  // readiness to wait for before dispatch; nil = {SignalStorage}
	Lanes           []LaneID       // lanes items may be handed to (5T.3.9)
	Subjects        SubjectsFunc   // optional per-run write set (5T.3.4)
	Dedupe          DedupePolicy   // queued-run dedupe/merge (v2 DedupeQueuedRuns/MergeQueuedParams)
	Debounce        *Debounce      // v2 Batchable/BatchWindow/BatchMaxWait
	Yields          bool           // may be quiesced by a stand-down (requires ResumeContinue), 5T.3.7
	Notify          Notify
	FormerKinds     []Kind // renames and legacy IDs (v2 aliases.go / FormerIDs)
}

type Priority uint8

const (
	PriorityLow Priority = iota + 1
	PriorityNormal
	PriorityHigh
	// PrioritySystem is core-only (kernel refuses it on module kinds). Such
	// runs dispatch on core.tasks.system_workers reserved workers, so core
	// plumbing (outbox relay, scheduler window runs, updater) is never starved
	// by a saturated executor.
	PrioritySystem
)

type TaskSpec[P, R any] struct {
	Common
	Run func(rc *RC, p P) (R, error)
}

type BatchSpec[P, T, R any] struct {
	Common
	Source      func(rc *RC, p P) (Source[T], error)
	Item        func(ic *ItemRC, p P, item T) error // MUST be idempotent
	Finish      func(rc *RC, p P, s Summary) (R, error)
	Parallelism Parallelism // required (N8); FromParam allowed here
	PartitionBy func(T) string
	ChunkSize   int           // 0 = 256
	ItemTimeout time.Duration // clock starts after the pause gate
	OnItemError ErrorMode     // zero = Collect (count failed, continue)
	Label       func(T) string // pure function of the item; runs in workers
}

type FixerSpec[P, C any] struct {
	Common // Effects must be Writes(...); Approval required
	Candidates  func(rc *RC, p P) (Source[C], error)
	Load        func(rc *RC, p P, rowID string) (C, bool, error)
	Evaluate    func(ic *ItemRC, p P, c C) (Row, error) // read-only; runs at plan AND apply
	Apply       func(ic *ItemRC, w Writer, p P, row Row) error
	Parallelism Parallelism
	PartitionBy func(Row) string
	AfterApply  func(rc *RC, applied []string) error
	Guards      []Guard
}

type PipelineSpec[P any] struct {
	Common
	Stages []Stage
}

func Task[P, R any](kind Kind, s TaskSpec[P, R]) Definition
func Batch[P, T, R any](kind Kind, s BatchSpec[P, T, R]) Definition
func Fixer[P, C any](kind Kind, s FixerSpec[P, C]) Definition
func Pipeline[P any](kind Kind, s PipelineSpec[P]) Definition
```

`Source[T]`, `SourceOrder` (`Snapshot`/`AppendOnly`/`Unordered`), `DirtySet`, `Row`, `Guard`,
`Stage`, `Summary`, `Total` keep their v3 meaning (`pkg/ops/batch.go:197-259`, `pkg/ops/fixer.go:20-80`,
`pkg/ops/pipeline.go:24`), with `Row.Subjects []Resource` instead of book subjects. `DirtySet`'s
`prefix` is a key inside the calling module's `storage.Keyspace` ([05-storage](05-storage.md)), never a raw prefix.

Registration is only through `module.Registrar` ([06-app-sdk](06-app-sdk.md)):

```go
// in module.Registrar (06 §6.4.3): r.Task(defs ...tasks.Definition)
// additions this spec needs in 06 (open item 7):
//                           r.Resources(kinds ...string)   // resource kinds this module owns
//                           r.Lanes(lanes ...tasks.Lane)
//                           r.ChangeSink(s tasks.ChangeSink)
// core-only (kernel): windows (5T.3.8)
```

The kernel rejects (boot fails before listeners open, `startup` class Fatal): a kind whose module
prefix is not the registering module; duplicate kinds or `FormerKinds` colliding with any live kind;
any constructor problem; `Perms.Start` outside `<module>:` or `core:`; `Effects.Writes` naming
another module's resource; an undeclared resource kind; `Resume == ResumeContinue` on an `Unordered`
Batch source; a zero `Parallelism` on Batch/Fixer; a Fixer without `Approval`; a scheduled writer
whose schedule says neither `Live()` nor `Preview()` (v3 rule D8, `pkg/ops/schedule.go:96-118`).

### 5T.3.2 Parallelism (N8) and `RunItems`

```go
// Parallelism is a declared worker count with its reason. Its zero value is
// INVALID: Validate returns ErrParallelismUnset, Batch/Fixer registration
// fails, and RunItems returns ErrParallelismUnset before touching an item.
type Parallelism struct {
	kind   parKind // 0 = unset
	n      int
	reason string
	field  string // FromParam
	max    int
}

func CPU() Parallelism                       // runtime.NumCPU() workers sharing the executor CPU budget
func Fixed(n int) Parallelism                // exactly n (n >= 1), e.g. a provider's rate budget
func IO(n int) Parallelism                   // n network/disk-bound workers, 1 <= n <= MaxIO
func Sequential(reason string) Parallelism   // one worker; reason non-empty, shown in the def list
func FromParam(field string, def, max int) Parallelism // Batch/Fixer only: read from run params, clamped [1,max]

const MaxIO = 64 // v3 MaxNetworkWorkers, pkg/ops/batch.go:132-134

var ErrParallelismUnset = errors.New("tasks: parallelism not declared (use CPU, Fixed, IO or Sequential)")

func (p Parallelism) Validate() error
func (p Parallelism) String() string // "cpu", "fixed(4)", "io(8)", "sequential: <reason>"
```

**CPU budget.** Every `CPU()` worker acquires one slot from an executor-wide semaphore
(`core.tasks.cpu_slots`, default `runtime.NumCPU()`) per item and releases it when `fn` returns.
Today eight executor workers (`register.go:102`) may each run a `NumCPU` pool (~45 sites,
`raw/06` §2), i.e. up to 8×NumCPU CPU-bound goroutines; the budget turns that into fair sharing.
`IO`, `Fixed` and `Sequential` take no CPU slot. Cost: a CPU-bound run is slower in wall clock
while another CPU-bound run is active; this is the intended trade.

```go
// RunItems runs fn over items inside a run. It is the in-body loop for Task
// shapes; Batch/Fixer use their runner. Semantics in the table below.
func RunItems[T any](rc *RC, par Parallelism, items []T,
	fn func(ic *ItemRC, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error)

// RunSeq is RunItems over a stream whose length may be unknown. It cannot
// take Watermark (no stable index) unless Key is set.
func RunSeq[T any](rc *RC, par Parallelism, items iter.Seq[T], total Total,
	fn func(ic *ItemRC, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error)

type ItemsOptions[T any] struct {
	Label       func(T) string // pure; receives only the item (cannot read counters)
	OnError     ErrorMode      // FailFast or Collect; zero = FailFast
	ItemTimeout time.Duration
	Watermark   string         // non-empty: checkpoint the contiguous-completion watermark
	                           // under this name and resume from it (requires a stable item order)
	CheckpointEvery int        // 0 = 64 items
	ProgressBase    int        // absolute offset when items is a slice of a larger set
	ProgressTotal   int        // absolute denominator
	Key         func(T) string // RunSeq + Watermark: stable key instead of index
}

type ErrorMode uint8

const (
	FailFast ErrorMode = iota // stop dispatching on the first error
	Collect                   // run every item
)

type ItemsResult struct {
	Done, Failed, Skipped, NotStarted int64
	Counters  map[string]int64 // folded from ItemRC.Count
	Watermark int              // absolute contiguous-completion prefix
}

// ItemsError is the ONLY non-nil error type RunItems/RunSeq return.
type ItemsError struct {
	First      error // the first item error to COMPLETE (completion order, both modes)
	FirstIndex int
	Errs       []error // every item error observed before the loop stopped, completion order
	Cause      error   // context.Cause(rc.Context()) when the run was stopped, else nil
}

func (e *ItemsError) Error() string
func (e *ItemsError) Unwrap() []error // Errs plus Cause: errors.Is(err, context.Canceled) works

// ItemRC is one item's context. Executor-owned counters: callers keep none.
type ItemRC struct{ /* unexported */ }

func (ic *ItemRC) Context() context.Context // derived from the run ctx; carries ItemTimeout
func (ic *ItemRC) Count(name string, delta int64) // atomic; folded into ItemsResult/Summary
func (ic *ItemRC) Skip(reason string)
func (ic *ItemRC) Log() *slog.Logger
func (ic *ItemRC) Writer() Writer
func (ic *ItemRC) Lane(id LaneID) (LaneLease, error)
```

| Behaviour | Contract (identical sequential and parallel) | Fixes |
|---|---|---|
| Worker shape | Fixed set of `min(par.Workers(), len)` goroutines (started with `wg.Go`) pulling the next index from an atomic cursor; no goroutine per item | `run_items.go:335-358` spawns per item |
| Cancellation | Each worker checks `ctx.Err()` **before taking** an index and **again before calling** `fn`; once the run ctx is done no new `fn` starts. Items never started count `NotStarted`. In-flight items see their ctx canceled with the run's cause | `run_items.go:336-345` random `select`; the per-op guard in `docs/decisions/2026-10-31-lsh-backfill-cancel-guard.md` becomes unnecessary |
| Pause | Workers park on the **executor's** pause gate reached through `rc`, before the item clock starts | global `pause_gate.go:197-199` |
| Label | `Label(item)` is called in the worker before `fn` for "current item"; progress text is rendered by core from atomic counters (`done/total`, counters), never by the caller | `run_items.go:45-54,268` race; the CLAUDE.md note about `Label` closures becomes structurally impossible except via deliberately captured mutable state, which `-race` in `coretest` catches |
| Progress | Monotonic atomic completion count; delivered to the reporter in completion order under one lock (current `progressMu` property, `run_items.go:271-285`) | — |
| FailFast | First error stops dispatch; in-flight items finish (their ctx canceled with `ErrItemsStopped`); return `*ItemsError` with `First` = first to complete, `Errs` = all observed | `run_items.go:149` vs `:340-342` mismatch; sequential bare error `:316` |
| Collect | All items run; `*ItemsError` iff any failed | — |
| Run canceled | `*ItemsError{Cause: context.Cause(ctx)}` (+ any item errors) | `ctx.Err()` loses `ErrShutdown` cause |
| Watermark | Contiguous-completion prefix (v2 `completionTracker`, `run_items.go:114-137`) checkpointed via `rc.Checkpoint` under `Watermark` name, serialised; restored automatically on resume; failed items never advance it | caller-wired `ResumeFrom`/`CheckpointStateFn` |
| Liveness | Every completed item touches liveness; the watchdog never kills a progressing loop | — |
| Stand-down hold | If the run holds a stand-down (5T.3.7), each item renews it and a lost hold fails the item before its write | `run_items.go:263-267` |

`archtest.RunItemsParallelism(t, pkgs...)` (core `archtest`) replaces
`internal/plugins/runitems_concurrency_lint_test.go`: it flags `tasks.Parallelism{}` literals,
`var x tasks.Parallelism` declarations reaching a `RunItems`/`RunSeq`/`BatchSpec` argument, and
`Sequential("")`. See "Objections to 03" for why R7's wording is adjusted.

### 5T.3.3 Run context

```go
type RunID string // opaque; adopted legacy runs keep their v2/v3 id verbatim

type Mode uint8

const (
	ModePreview Mode = iota + 1
	ModeLive
)

type RC struct{ /* unexported */ }

func (rc *RC) Context() context.Context // canceled on cancel, timeout, quiesce, shutdown (cause tells which)
func (rc *RC) RunID() RunID
func (rc *RC) Kind() Kind
func (rc *RC) Mode() Mode
func (rc *RC) Trigger() Trigger // Manual, Schedule, Window, OnStart, Outbox, Parent, Resume
func (rc *RC) Actor() Actor     // who started it; system actor for schedules
func (rc *RC) Log() *slog.Logger // run_id, kind attached (05-logging child logger)
func (rc *RC) Progress() *Progress
func (rc *RC) Phase(name string) (end func())
func (rc *RC) Writer() Writer // nil for ReadOnly defs; intent-recording in Preview
func (rc *RC) Checkpoint(v any) error
func (rc *RC) Restore(v any) (bool, error)
func (rc *RC) Resumed() bool
func (rc *RC) Deadline() time.Time   // this attempt's Timeout deadline
func (rc *RC) ShouldYield() bool     // true when Deadline - now < YieldMargin (5T.3.6)
func (rc *RC) Enqueue(kind Kind, params any, opts ...StartOption) (RunRef, error) // child, parent-linked
func (rc *RC) Await(ref RunRef) (Outcome, error) // relays child liveness to this run
func (rc *RC) StandDown(key Key) (Hold, error)   // 5T.3.7
func (rc *RC) Canceled() bool

// The causes an RC context is canceled with (context.Cause). All wrap
// context.Canceled or context.DeadlineExceeded so existing errors.Is checks hold.
var (
	ErrCanceled  error // operator cancel
	ErrShutdown  error // process shutdown; re-exported from core/lifecycle (Objections to 03)
	ErrQuiesced  error // stand-down or pause-for-requeue
	ErrTimedOut  error // attempt Timeout
	ErrAbandoned error // grace expired after a cancel; fence revoked
	ErrItemsStopped error // FailFast sibling stop (item ctx only)
)

// ErrContinueInPlace: return it (after Checkpoint) to be re-queued under the
// SAME RunID with a fresh Timeout. Honoured only for Resume == ResumeContinue,
// only if this attempt reported progress past its starting point AND
// checkpointed since it started; otherwise the run fails with a message saying
// which proof was missing (current rule, continue_in_place.go:31-41).
var ErrContinueInPlace = errors.New("tasks: yielded before deadline; continue in place")
```

### 5T.3.4 Effects, resources, write sets, concurrency keys (G8)

```go
// Resource names shared state a run reads or writes. ID == "" means the whole kind.
type Resource struct {
	Module string // declaring module, or "core"
	Kind   string // declared via Registrar.Resources
	ID     string
}

func Res(module, kind string) Resource
func (r Resource) With(id string) Resource
func (r Resource) String() string // "books/book_files" or "books/book_files#<id>"

type Effects struct{ /* unexported */ }

func ReadOnly(reads ...Resource) Effects
func Writes(ws ...Resource) Effects
func (e Effects) AlsoReads(rs ...Resource) Effects
func (e Effects) AlsoDeletes(rs ...Resource) Effects // flagged for owner review by archtest

// SubjectsFunc narrows a run's write set to concrete IDs at enqueue time
// (e.g. one book merge writes books/books#a and #b). Returned resources must be
// within Effects.Writes kinds.
type SubjectsFunc func(params json.RawMessage) ([]Resource, error)

// Key is a namespaced mutual-exclusion key "<module>.<name>". A module may use
// keys in its own namespace and keys in "core." (e.g. "core.storage.exclusive",
// held by compaction and backup).
type Key string
```

Dispatch conflict rule (Gate 3b generalised): a queued run conflicts with a running run iff both
write an overlapping resource, where `{M,K,""}` overlaps every `{M,K,*}` and `{M,K,id}` overlaps
`{M,K,id}` and `{M,K,""}`. Reads are declarative (shown in the UI, used by preview/audit), not
gated — the same as today (`dispatcher.go:163-178`); an open item asks whether readers of
consistency-sensitive reports should gate. A run whose `ConcurrencyKey` is held stays queued
(Gate 3, `dispatcher.go:143-153`); zombies keep their key until their goroutine exits (current D6).

### 5T.3.5 Writer: the generic gated write

```go
// Writer is the run's only write path. Modules wrap it in typed writers
// (books: ModifyBook, ModifyBookFile, RepointBookFile, ModifyBookAuthors —
// today's pkg/ops/writer.go:46-92 methods move to books/taskwrite).
type Writer interface {
	// Do performs one gated write. It refuses with ErrFenced when the run is
	// canceled, abandoned, timed out or lost its stand-down lease; in Preview
	// it records the intent, does NOT call fn, and returns ErrPreview. In Live
	// it records a write-ahead intent, calls fn, then records the returned
	// changes to the module's ChangeSink (if any) and the run's evidence.
	Do(ctx context.Context, w Write, fn func(ctx context.Context) ([]Change, error)) error
	MarkRowApplied(ctx context.Context, rowID string)          // Fixer evidence (v3 semantics)
	RowApplied(ctx context.Context, rowID string) (bool, error) // via ChangeSink.Stands
}

type Write struct {
	Name     string   // "books.modify_book"
	Resource Resource // must be inside the def's Effects.Writes
	Subject  Resource // the entity written (ID set)
}

type Change struct {
	Subject       Resource
	Field         string
	Old, New      string
}

// ChangeSink is a module's journal (books: the operation-change journal and
// undo, raw/12 §8). Registered via Registrar; optional.
type ChangeSink interface {
	Record(ctx context.Context, run RunID, changes []Change) error
	// Stands reports whether every change a marker names is still in effect
	// (the v3 RowApplied evidence rule, pkg/ops/writer.go:75-91).
	Stands(ctx context.Context, run RunID, rowID string) (bool, error)
}

var (
	ErrFenced           error // write refused: run no longer holds its permit
	ErrPreview          error // preview: intent recorded, not performed
	ErrChangedSincePlan error // Fixer row re-evaluated differently
	ErrOutsideEffects   error // Write.Resource not declared in Effects.Writes (fail-closed)
)
```

### 5T.3.6 Resume, checkpoints, timeouts, cancellation, watchdog

```go
type ResumePolicy uint8 // zero = unset, refused at registration

const (
	ResumeContinue ResumePolicy = iota + 1 // Task: Restore checkpoint; Batch/Fixer: chunk ledger. v2 Restart, v3 Continue
	ResumeFromZero                         // same RunID, from zero; idempotent work. v2 Requeue
	ResumeDrop                             // interrupted -> dropped. v2 Drop
	ResumeAsk                              // interrupted -> awaiting_decision. v2 Ask
)

type CancelContract struct{ /* unexported */ }

func AtItemBoundaries() CancelContract                         // Batch/Fixer default
func Cooperative(grace time.Duration) CancelContract           // Task default: Cooperative(30*time.Second)
func Uninterruptible(reason string, budget time.Duration) CancelContract // listed at WARN on boot
```

| Topic | Rule |
|---|---|
| Checkpoint | `rc.Checkpoint(v)` JSON-encodes into `c/tasks/ckpt/<run>`; throttled by `core.tasks.min_checkpoint_interval`; written in the same storage batch as the progress record. Batch/Fixer checkpoint through the chunk ledger (`c/tasks/ledger/`, `c/tasks/snap/`). |
| `ResumeContinue` requires a checkpoint | (a) static: `archtest.ResumeContinueCheckpoints(t, pkgs...)` — successor of `internal/arch/resume_restart_guard_test.go` — requires every Task-shape def with `ResumeContinue` to reach `.Checkpoint(` or a `RunItems`/`RunSeq` with `Watermark` from its `Run`; Batch/Fixer pass by construction; the 21-entry allowlist is carried over and may only shrink. (b) runtime: an attempt of a `ResumeContinue` Task that ran longer than `min_checkpoint_interval` × 3 without a checkpoint records an `uncheckpointed` watchdog strike (existing strike kind, `raw/06` §5). |
| Resume at boot | v2 `resumeAfterStartup` (`resume.go:35`) per policy; infinite-restart guard (`worker.go:1412`) kept; a yield is not an attempt for that guard but `core.tasks.max_yields` caps consecutive yields per run (default 1000) as a backstop. |
| Yield | `rc.ShouldYield()` is true when `Deadline − now < max(10% of Timeout, 1 min)`; the author checkpoints and returns `ErrContinueInPlace` (5T.3.3). Replaces `internal/walkresume` (`walkresume.go:5-17`, `ErrYield`). |
| Timeout | Per attempt. Defaults Task 30 m, Batch 24 h, Fixer 6 h (`pkg/ops/definition.go:86-88`); cap 24 h. Timeout is **terminal** (`timed_out`), never re-queued (current rule, `continue_in_place.go:18-24`). |
| Cancel | `stopping{cancel}` → run ctx canceled with `ErrCanceled`; after the contract's grace, the fence is revoked (`ErrAbandoned`), the row ends `canceled`, the goroutine is counted a zombie; per-module zombie cap `core.tasks.zombie_cap` (v2 abandoned cap 4/plugin, `registry.go:235`) blocks new dispatch for that module when reached. |
| Watchdog | Batch/Fixer/RunItems are self-reporting. Task: must report progress at least every `ProgressTimeout` (default `Timeout/6`, min 1 m; v3 rule `pkg/ops/task.go:15-16`) or declare `Uninterruptible`. Strikes `stuck`, `never_reported`, `uncheckpointed` kept. |

### 5T.3.7 Permissions, preview/dry-run, approval, stand-down

```go
// Each field is an rbac.Requirement (05-rbac §3.1; zero value invalid there,
// so "zero" below means "field omitted, default applied by tasks").
type Permissions struct {
	Start   rbac.Requirement // required; own-namespace or core scopes only (05-rbac §4 R3 rule)
	Cancel  rbac.Requirement // omitted = Start. The run's own actor may always cancel it.
	Approve rbac.Requirement // required iff Approval != nil (always for Fixer)
	View    rbac.Requirement // omitted = rbac.Need("core:tasks.view")
}

type Approval struct {
	PlanRequired bool          // Live start must name a Preview run (plan_ref) of the same kind
	MaxPlanAge   time.Duration // 0 = 7 days (pkg/ops/approval.go:29-30)
}
```

| Action | Who | Fail mode |
|---|---|---|
| list/get defs and runs | `Perms.View` per def; rows/results of runs the caller may not view are omitted (current per-viewer filter, `internal/diagnostics/service.go:138-150`, `sysinfo` `OperationReadCheck`) | closed |
| start | `Perms.Start`, plus the route floor `rbac.AnyAuthenticated` (today's `scan.trigger` floor, `opsv3/handler.go:236-243`, is replaced by the per-def check, not kept as a second atom) | closed |
| cancel | the run's actor, or `Perms.Cancel`, or `core:tasks.manage` | closed |
| approve / decide (`awaiting_decision`) | `Perms.Approve` (decide on a run interrupted under ResumeAsk: `Perms.Approve` if set, else `Perms.Start`) **and** a verified auth method (`auth.Method` interactive session or an issued API credential; a forwarded header is never identity — current rule `opsv3/runs.go:407-408`) | closed |
| pause/resume executor, edit schedules | `core:tasks.manage` | closed |

Every approve, decide, cancel-by-other and schedule edit writes an `audit` record ([05-rbac](05-rbac.md)).

**Mode / dry-run contract** (moves `internal/operations/opmode` into the core start path):

1. The start request carries mode in the **envelope**, not in params: `"mode": "preview" | "live"`.
   For compatibility, `"dry_run"`/`"dryRun"` booleans are accepted at the top level and, for legacy
   kinds, inside `params` (lifted out before decode). Disagreeing spellings → `400 tasks.mode_conflict`
   (current rule, `dryrun.go:41-58`).
2. Omitted mode → **Preview** for writers, Live for `ReadOnly` defs (owner rule 2026-09-25,
   `dryrun.go:6-10`). Maintenance jobs' per-job default (`ResolveDryRunDefault`, `dryrun.go:60-66`)
   is expressed as `ReadOnly` for the read-only jobs, so the special case disappears.
3. A Preview run gets an intent-recording Writer; its result is `{result, intents[]}`; a Fixer
   Preview is the plan (rows). Params structs no longer carry `DryRun` fields; adapted legacy defs
   receive the mode re-injected into their params (5T.8).
4. A scheduled writer runs in the mode its `Schedule` declares (`Live()`/`Preview()`).

**Stand-down** (generalises the `library.scan` special case, `dispatcher.go:155-161`): a def that
declares `Common.Yields = true` (meaning: it is `ResumeContinue` and safe to quiesce) can be stood
down by `rc.StandDown(key)` from another run: running holders of `key` are quiesced
(`stopping{quiesce}` → `interrupted_quiesced`, re-queued when the hold is released), new runs of
`key` stay queued while the hold lives; the hold is a lease renewed by every `Writer.Do`, lost
after `core.tasks.standdown_lease` (default 2 m) without renewal.

### 5T.3.8 Schedules: one mechanism, durable clock

```go
type Schedule struct{ /* unexported */ }

func Every(d time.Duration) *Schedule                   // fixed interval
func Interval(get func() time.Duration) *Schedule       // config-driven; re-read every poll; <= 0 = off
func DailyAt(hhmm string) *Schedule                     // in core.tasks.timezone
func Cron(expr string) *Schedule                        // 5-field, in core.tasks.timezone
func InWindow(window string, order int) *Schedule       // member of a named maintenance window

func (s *Schedule) OnStart() *Schedule
func (s *Schedule) When(enabled func() bool) *Schedule  // gate re-read each poll
func (s *Schedule) Params(p any) *Schedule
func (s *Schedule) Live() *Schedule
func (s *Schedule) Preview() *Schedule
func (s *Schedule) Missed(p MissedPolicy) *Schedule

type MissedPolicy uint8

const (
	Coalesce MissedPolicy = iota // fire once for any number of missed instants; count them (current countDue, v3_schedule.go:501)
	SkipMissed                   // do not fire for instants missed while down; wait for the next
)

// Window is declared by core config (core.tasks.windows.<name>); modules join
// it with InWindow. The window runs as core.window.<name>: members in order,
// once per window opening.
type Window struct {
	Name       string
	Start, End int // hour 0-23, wraps midnight
}
```

**What replaces v2 cron strings.** They are dead metadata (5T.2). Core has no string-cron field;
`Schedule` is the only timer. Migration (5T.8 batch T7) converts each of the 15 declared crons into
**no schedule** and records it in the migration ledger, because none of them ever ran unattended:
turning any on is a behaviour change the owner decides per def (open item 1). The 38
`TaskDefinition`s become `Schedule`s on their target def: config-driven intervals use
`Interval(func(){ cfg... })`; `RunOnStart` → `.OnStart()`; `IsEnabled` → `.When(...)`; window
membership (`maintenanceOrder`, `internal/scheduler/scheduler.go:197-246`) → `InWindow("nightly", idx)`;
per-task window toggles → `When`. `transcode` (enqueues nothing) is deleted; `batch_poller` becomes
an `Every(5*time.Minute)` schedule on an AI-owned task ([05-ai](05-ai.md)).

**Durable clock.** One record per scheduled kind at `c/tasks/sched/<kind>`:
`{last_due, last_fire, last_run, missed, gate_opened_at, schedule_hash}`. One scheduler goroutine
polls every `core.tasks.schedule_poll` (1 m, current `intervalPollInterval`,
`internal/scheduler/interval_clock.go:46-55`). A due fire is **one storage batch**: CAS the
`sched` record (`last_due` advanced) + insert the queued run row + queue index. A crash between
clock advance and enqueue therefore cannot lose or duplicate a fire (today the interval clock is a
settings write separate from enqueue, `interval_clock.go:46-60`). The clock advances on every due
check whether or not the gate let it enqueue (current semantics, `interval_clock.go:36-45`).
First sight of a schedule seeds `last_due = now` and does not fire (no boot stampede,
`interval_clock.go:57-63`). `schedule_hash` changes when the declaration changes; a changed
`Every` keeps `last_due`. A schedule whose def already has an active run of the same mode folds
into it (current `EnqueueOp` rule for scheduled defs, `registry.go:1055-1060`). DST: `DailyAt` and
`Cron` compute the next instant in the zone; a skipped wall time fires at the first instant after,
a repeated one fires once.

### 5T.3.9 Lanes

```go
type LaneID string // "<module>.<name>", e.g. "books.audio-decode"

type Lane struct {
	ID              LaneID
	Title           string
	Capacity        func() int // current capacity (e.g. connected remote workers); re-read on lease
	NoLocalFallback bool       // work declared for this lane must not run in-process if no capacity
}

type LaneLease interface {
	Release()
	Context() context.Context // canceled if the lane revokes the lease
}
```

`ic.Lane(id)` blocks (cancellably) until capacity allows; a def may only lease lanes it lists in
`Common.Lanes`. `pkg/ops.LaneMac` (`lanes.go:12-15`) becomes the books-declared lane
`books.audio-decode` with `NoLocalFallback: true`; its remote lease protocol
(`internal/fingerprint/workerapi`, `internal/server/handlers/fpworker/handler.go:7-17`) stays in
`books` (one consumer; §3.4 promotion rule) and implements `Capacity`.

### 5T.3.10 Executor, starter and HTTP API

```go
// Client is what modules get as module.Deps.Tasks (06 §6.4.4) to start and observe runs by Kind
// (cross-module by Kind string, never by importing the other module, R3).
type Client interface {
	Start(ctx context.Context, kind Kind, req StartRequest) (RunRef, error)
	Get(ctx context.Context, id RunID) (RunView, error)
	Runs(ctx context.Context, q RunQuery) iter.Seq2[RunView, error]
}

type StartRequest struct {
	Mode           ModeRequest     // Unset | Preview | Live
	Params         json.RawMessage
	Priority       Priority
	PlanRef        RunID  // Fixer / Approval.PlanRequired
	Parent         RunID
	Actor          Actor
	Trigger        Trigger
	IdempotencyKey string // dedupe window core.tasks.idempotency_ttl; the outbox relay always sets it
}

// Executor is kernel-only (core/kernel holds it; apps see Client).
type Executor interface {
	Client
	Cancel(ctx context.Context, id RunID, by Actor) error
	Approve(ctx context.Context, id RunID, a ApproveRequest) error
	Decide(ctx context.Context, id RunID, d Decision, by Actor) error // Resume | Finish | Drop
	Pause(reason string, by Actor) error
	Unpause(by Actor) error
	Start(ctx context.Context) error    // lifecycle
	Shutdown(ctx context.Context) error // bounded drain, 5T.4
}
```

`/api/core/v1/tasks` (`httpapi`, [05-http-api](05-http-api.md)); every route has a permission (R6):

| Route | Replaces | Permission |
|---|---|---|
| `GET /definitions`, `GET /definitions/{kind}` | `GET /api/v3/ops/defs[/:id]`, `/api/v1/op-defs` | `AnyAuthenticated` + per-def `View` filter |
| `POST /definitions/{kind}/runs` | `POST /api/v3/ops/defs/:id/runs`, `POST /api/v3/ops/runs`, `POST /api/v1/operations/v2` | per-def `Start` |
| `GET /runs`, `GET /runs/{id}`, `/runs/{id}/rows`, `/runs/{id}/items`, `/runs/{id}/log` | v3 `runs`, v1 `operations/v2/:id` | per-def `View` |
| `POST /runs/{id}/cancel` | v3 cancel, `DELETE /api/v1/operations/v2/:id` | actor or `Cancel` |
| `POST /runs/{id}/approve` | v3 approve | `Approve` + verified method |
| `POST /runs/{id}/decide` `{resume\|finish\|drop}` | v1 retry/finish routes (`retry.go:70`, `finish.go:73`; `raw/08` §7 finish-without-resuming) | `Approve`/`Start` + verified method |
| `GET /counts`, `GET /timeline` | v3 counts/timeline | `core:tasks.view` |
| `GET /schedules`, `PATCH /schedules/{kind}` | `/api/v1/tasks`, `PUT /tasks/:name`, `/maintenance-window/*` | view / `core:tasks.manage` |
| `POST /pause`, `POST /unpause` | `/api/v1/operations/pause|resume` | `core:tasks.manage` |
| events | `GET /api/v3/ops/events`, `/api/v1/operations/events` | realtime topics `core.task.*.v1` ([05-events](05-events.md), [05-realtime](05-realtime.md)) |

Compat: `/api/v3/ops/*` and `/api/v1/operations*`/`/op-defs`/`/tasks` are aliases onto the same
handlers with a `Deprecation` header and log line for two minor releases (02 §2.5 Q5), then 410.
Alias handlers translate legacy def IDs through `FormerKinds`.

## 5T.4 Lifecycle

1. **Register** (inside `app.New`): kernel collects module definitions, validates (5T.3.1), upserts
   def snapshots `c/tasks/def/<kind>`, rejects an op-ID ledger entry that resolves to nothing
   (current `opCatalogGate`, `internal/startup/steps.go:110-111`).
2. **Start** (`App.Run`): restore pause marker → copy the non-terminal legacy set on first boot
   of the new release (5T.6) → resume sweep (5T.3.6) → debounce reload → dispatcher (100 ms tick + wake),
   watchdog (30 s), N workers (`core.tasks.workers`, default 8), scheduler goroutine. Dispatch waits
   for each def's `Ready` signals from the health registry ([05-health](05-health.md)); waiting
   runs hold no worker, key or clock (current warm-up rule, `pkg/ops/effects.go:44-57`).
3. **Run**: claim (CAS queued→running, fence epoch+1) → build `RC` → shape runner → terminal CAS.
   Events `core.task.created/progress/log/finished.v1` are published on the bus after each commit.
4. **Shutdown** (reverse order, inside the app's stop budget, [05-health](05-health.md) §stop budget):
   stop scheduler → flip `shuttingDown` → release claimed-not-started → cancel every run with
   `ErrShutdown` → wait up to `core.tasks.shutdown_drain` (default **10 s**, current value
   `server_lifecycle.go:611`) → stamp the rest `interrupted` (resume policy decides next boot),
   revoke their fences (late writes refused with `ErrFenced`) → stop owned goroutines → return
   before storage closes. If an `Uninterruptible` run is in flight, the executor asks the
   supervisor for more time once via `sdnotify` `EXTEND_TIMEOUT_USEC` (`internal/sdnotify/sdnotify.go:144`)
   bounded by `min(remaining budget, core.tasks.shutdown_extend_max)` (default 60 s); it never
   waits unbounded. Shutdown never blocks HTTP drain (they run in the order of 03 §3.7, HTTP first).

## 5T.5 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Invalid definition (any 5T.3.1 rule) | closed: `app.New` fails | a hole in the catalog makes runs vanish silently (current `opRegistrationGate` reason) |
| `Parallelism` zero in `RunItems` | closed: returns `*ItemsError{First: ErrParallelismUnset}` before any item | N8 |
| Start of unknown kind (after alias resolution) | closed: 404 `tasks.unknown_kind` | — |
| Start without permission / unverified approver | closed: 403 | RBAC |
| Mode conflict | closed: 400, nothing enqueued | `dryrun.go:41-58` |
| Params do not decode | closed: run `failed` with decode error (not enqueued if validated at start) | — |
| Undecodable stored run record | open: hidden from census/totals, WARN, counted metric | `raw/08` §7 `opsv3-undecodable-census-total` |
| Write outside declared effects | closed: `ErrOutsideEffects`, item/run fails | G8 |
| Write after fence revoked | closed: `ErrFenced` | prevents zombie writes |
| Checkpoint write error | closed for `ResumeContinue` (run fails: resume would be unsound), open (WARN) otherwise | today `CheckpointFn` errors are ignored (`run_items.go:318-320`) |
| Watermark persist error inside `RunItems` | same as checkpoint | — |
| Yield without progress/checkpoint | closed: run `failed` with reason | loop guard (`continue_in_place.go:31-41`) |
| Schedule clock read error | open for this poll (skip, metric `schedule_errors`), never "due now" | `interval_clock.go:57-75` |
| Schedule CAS conflict | open: another poll won; no fire | exactly-once |
| Event publish failure after commit | open: run state is durable; UI may lag | bus is notify-only (05-events) |
| Shutdown drain timeout | open: remaining runs stamped interrupted; fences revoked | bounded stop |
| Active-set copy fails, or the set exceeds the atomic limits | closed: boot halts before listeners; pre-copy backup restorable | G2/G5; a split copy could run one run in two executors |
| Lane capacity 0 with `NoLocalFallback` | waits (cancellable); never runs in-process | LaneMac rule |

## 5T.6 Persistence keyspace `c/tasks/`

All keys inside the core `tasks` keyspace ([05-storage](05-storage.md)); JSON values unless noted.

| Key | Content | From |
|---|---|---|
| `c/tasks/run/<id>` / `c/tasks/prog/<id>` | cold record / hot progress fields (split kept for write amplification) | `opv3:run:`/`opv3:prog:` (`pebble_store_ops_v3.go:44-52`) |
| `c/tasks/q/<999-prio>/<ts>/<id>` | dispatch queue | `opv2:q:` |
| `c/tasks/ix/state/<state>/<id>`, `ix/kind/<kind>/<id>`, `ix/done/<ns>/<id>`, `ix/parent/<p>/<c>` | indexes | `opv3:ix:*` |
| `c/tasks/meta/counts`, `c/tasks/meta/pause` | exact census; pause marker | `opv3:meta:counts`; pause settings key |
| `c/tasks/evt/<id>/<seq>` | state-transition audit trail | `opv3:evt:` |
| `c/tasks/ckpt/<id>`, `ledger/<id>`, `snap/<id>/<page>`, `intent/<id>/<seq>`, `item/<id>/<outcome>/<key>` (cap 10 000/run), `log/<id>/<ts>/<seq>`, `mark/<id>/<row>`, `row/<id>/<row>`, `rowix/...` | run state | `opv3:*`, `opv2:row:` |
| `c/tasks/sched/<kind>` | durable clock | `opv3:sched:`, settings `scheduled_task_last_tick.*` |
| `c/tasks/def/<kind>` | definition snapshots | `opv2:def:`/`opv3:def:` |
| `c/tasks/debounce/<kind>/<subject>` | debounce buckets | `op:batch:` |
| `c/tasks/req/<module>/<kind>/<id>/<taskkind>`, `c/tasks/req-rev/...` | `Requires` completion stamps and revisions, namespaced subjects | `op:completion:{type}:{id}:{opType}`, `op:deprev:` (`pebble_store_ops_v2.go:1188`) |
| `c/tasks/strike/<kind>/<id>` | watchdog strikes | `opv2:strike:` |
| `c/tasks/idem/<kind>/<key>` | start idempotency records (TTL) | new |

Writers: a single staging function per family with an AST ratchet, as today
(`TestOpsV2RowWritesGoThroughStageOpRow`, `raw/08` §7) — `core/internal/executor/store_ratchet_test.go`.

**Legacy records.** Coordinated with [05-storage](05-storage.md) S5 and §5S.9.1: the 42 ops
families (`operation:`, `operationlog:`, `opstate:`, `opsummary:`, `op:`+3, `opv2:`+14, `opv3:`+19) are
claimed by `core/tasks` and **read in place**; new rows go only to `c/tasks/`. This spec makes two
bounded exceptions to "never by copy", each justified below: the non-terminal run set (resume must
continue under one executor) and the `Requires` completion stamps (standing state that never ages
out). Both fit 05-storage's mechanisms (atomic adoption within `core.storage.adopt.max_atomic_*`, and a
resumable `DataMigration`, §5S.3.6).

| Family | Size class | Handling |
|---|---|---|
| Non-terminal runs (opv2 active set `opv2:act:`, `interrupted_*`, `waiting_deps`, v3 `awaiting_decision`) with their `opv2:state:`/`opv3:ckpt:`/`ledger:`/`snap:`/`rowmark:`/`opv2:row:` | small: bounded by the active set (tens), well under the atomic-adoption limits | **Copied** (exception 1) by core migration `core.tasks.adopt-active` on first boot, in one batch: copied into `c/tasks/` keeping the RunID and kind via `FormerKinds`; in the same batch the original v2 row is set to `interrupted_dropped` with field `migrated_to` (an unknown JSON field the previous binary ignores), so a rollback to the previous release never resumes a run the new release already owns (fail toward not double-applying). If the set exceeds the atomic limits the migration fails boot rather than splitting (a split would let one run exist in two executors). Pre-migration backup is mandatory ([05-backup](05-backup.md)). |
| Terminal history (`opv2:op/log/err/done/open`, `opv3:run/prog/evt/log/item/ix:*`) | large | **Read in place**, read-only, by `core/internal/executor/legacyhist` through the v3 projection (every v2 write since 05-PR4 staged it, `pebble_store_ops_v3.go:28-45`), falling back to `opv2:op:` for rows without a projection (UNKNOWN whether pre-05-PR4 rows were back-projected). Merged into `GET /runs` results with `legacy: true`. Never written. Pruned by `core.tasks.legacy-prune` after the compat window. |
| `opv2:def:`, `opv3:def:`, `opv3:sched:` | small | adopted into `c/tasks/def/`, `c/tasks/sched/` (clock continuity: `last_due` carried over, as `seedFromLegacyClock` does today, `v3_schedule.go:329`) |
| Interval clocks in settings (`scheduled_task_last_tick.*`, `interval_clock.go:46`), full-sweep stamp, maintenance-window last run | small | adopted into `c/tasks/sched/` by task name → kind map (`taskV2DefIDs`, `internal/scheduler/maintenance.go:154`) |
| `op:batch:` (debounce buckets) | small (pending subjects only) | copied with the active set (same batch, same justification) |
| `op:completion:`, `op:deprev:` (`Requires` stamps, `pebble_store_ops_v2.go:1188`) | potentially library-scale (subjects × kinds) | **Exception 2**: read-through — a miss in `c/tasks/req/` falls back to the legacy key (subjects `book`/`book_file` read as `books/books`, `books/book_files`); new stamps go to `c/tasks/req/`; a resumable `DataMigration` `core.tasks.req-migrate` (05-storage §5S.3.6, `Shards` required) moves the rest and then the fallback is removed. Copy is justified because these stamps never age out, so read-in-place would be permanent. |
| v1 `operation:`, `operationlog:`, `opsummary:`, `op_result:` | legacy | read in place by `legacyhist` (v1 reader) or dropped after the window; never written |
| v1 `opstate:<op>:params` | live chunk journal of `backfill-file-hashes` (`raw/05` row `opstate:`) | claimed by `core/tasks` (05-storage §5S.9.1); until T2 migrates that job to a checkpoint, `core/tasks` exposes one narrow legacy helper (`legacy.OpStateParams(run)`) that the job's adapter uses, so the write stays inside the claim owner (R8) |
| `opchange:` | books' change journal | claimed by `books` (not tasks) |

The opv2↔opv3 dual-write mirror (owner decision D25, until "05-PR16") ends at the copy of the
active set: from then on, core writes only `c/tasks/`. Rollback compatibility (G5) is preserved by the
copy-and-mark rule above plus the read-only treatment of history; the previous release sees every
run it knew, in a terminal or dropped state. `Requires` stamps and runs created after the upgrade are
invisible to the previous release; at worst it re-runs a prerequisite, which is idempotent by contract.

## 5T.7 Concurrency contract

- `Executor`, `Client`, `RC`, `ItemRC`, `Writer` are goroutine-safe; `RC` may be shared with goroutines
  the run starts **only** if they end before the run returns (a goroutine left behind writes
  nothing: its writes are refused, `ErrItemReturned`/`ErrFenced`).
- Bounded everything: N executor workers; per-module `max_concurrent`; per-run `Parallelism`;
  executor CPU budget; lanes; zombie cap. No unbounded goroutine fan-out anywhere in `tasks`.
- One scheduler goroutine for all schedules (current `runV3Loop`, `v3_schedule.go:622`).
- State transitions are CAS on `(state, attempt, fence_epoch)` (current `TransitionRunV3`,
  `internal/database/iface_ops_v3.go:400-405`).
- Cancellation propagates by context cause to every worker within one item boundary (5T.3.2).
- No package-level mutable state (R5): pause gate, CPU budget, metrics, def registry are fields
  of the executor instance; two `App`s in one process do not share them.

## 5T.8 Migration from ABO

### 5T.8.1 What existing code becomes

| ABO today | Becomes |
|---|---|
| `internal/operations/registry` (dispatcher, worker, watchdog, resume, shutdown, batch runner, chunk ledger, fence, deps, debounce, pause, stand-down, warm-up) | `core/internal/executor` (cleaned of `database`, `auth`, `config.AppConfig`, `logger` imports, `raw/06` §7.1) |
| `internal/operations/state` | `core/internal/executor/state` |
| `pkg/ops` (Task/Batch/Fixer/Pipeline, Effects, Schedule, contracts, RC, Approval, opstest) | `core/tasks`, `core/tasks/taskstest` (from `pkg/ops/opstest`), with A1–A6 |
| `pkg/ops/writer.go` typed methods + `internal/operations/opswriter` | gate → `core/internal/executor/writer`; typed `ModifyBook*` → `books/taskwrite` over `tasks.Writer` |
| `internal/operations/opmode` | core start path (5T.3.7) |
| `internal/operations/childop` | `rc.Await` |
| `internal/walkresume` | `rc.ShouldYield` + `ErrContinueInPlace` + `RunItems` `Watermark` |
| `internal/scheduler` (interval/daily/cron/window/full sweep/v3 loop) | `core/internal/scheduler`; the 38 `TaskDefinition` bodies → `Schedule` declarations in `books` |
| `internal/repairs` (Fixer framework) | `tasks.Fixer`; the 24 fixers → `books` Fixer defs |
| `internal/maintenance` (`MaintenanceJob`, registry) | deleted after T2; jobs → `books` Task defs |
| `internal/opscatalog`, `pkg/plugin/sdk`, `addOpRegistrar`, `serviceregistry` op wiring | `module.Registrar.Task` |
| `internal/opsmetrics` | executor metrics (5T.10) |
| `internal/lifecycle` | `core/lifecycle` (Objections to 03) |
| `internal/server/handlers/opsv3`, v1 operations handlers | `core/internal/tasksapi` |

### 5T.8.2 Batches

Every batch is one or more PRs, each green on `make ci` in both module dirs; each keeps every kind
working (adapter first, native later). Def IDs map to kinds by prefixing the owning module:
`dedup.full-scan` → `books.dedup.full-scan` with `FormerKinds: ["dedup.full-scan"]`; core-owned
generic ops get `core.*` kinds. The op-ID ledger (`internal/server/op_id_aliases_test.go`) is the
check that no stored row strands.

| Batch | Content | Count | Gate to finish |
|---|---|---|---|
| T0 | `core/tasks` + executor + scheduler land; ABO adapter `internal/opsbridge` wraps every v2 `OperationDef` as a core Task (`Run(ctx, params, Reporter)` called with a `Reporter` implemented over `RC`; legacy `registry.RunItems` re-implemented over `tasks.RunItems` with `Parallelism` derived from its non-zero `Concurrency`, or `Sequential(marker reason)`); `/api/v3/ops` aliases | all 233 defs run on core | op-ID ledger green; resume/cancel/shutdown suites green on core |
| T1 | `core.tasks.adopt-active` + `core.tasks.req-migrate` + `legacyhist` | — | G5 rollback test (5T.9) |
| T2 | 34 `MaintenanceJob`s → native `books.maintenance.*` Tasks (mechanical: `Run(ctx, JobStore, ProgressReporter, dryRun)` `internal/maintenance/job.go:498` → `TaskSpec.Run(rc, p)`; `Policy()` → `Common`) | 34 | `internal/maintenance` deleted |
| T3 | 24 repairs fixers → `tasks.Fixer`; `repairs.plan/apply` removed | 24 (+2 defs) | Fixer conformance suite |
| T4 | Generic ops → `core.*` (activity-log cleanup/compaction family, backups prune, db optimize/census, temp/trash/reports cleanup, throttle reset, tombstone cleanup, retention-and-hygiene, metrics TTL sweep): the `generic` class rows of `raw/06` §6, each re-checked in [04-package-mapping](04-package-mapping.md) (some, e.g. `opchange-book-index-rebuild`, are book-bound despite the class) | ≤ 24 | owner per kind recorded in 04 |
| T5 | Plugin defs native, per plugin: acoustid 8, dedup 32, metafetch 3, iTunes 7, Deluge 4 (→ 05-download adapter tasks) | 54 | `pkg/plugin/sdk` unused |
| T6 | Maintenance plugin's 104 defs, in sub-batches of ≤ 15 by file group | 104 | — |
| T7 | Server-package ops (`internal/server/*_op*.go`) and `scheduler.metadata-upgrade` → `books`; 38 TaskDefinitions → Schedules; 15 v2 crons → none (ledger) | ~49 + 38 | `internal/scheduler` deleted from ABO |
| T8 | Delete `OperationDef`, `opsbridge`, v2 RunItems, opv2 writes; delete `runitems_concurrency_lint_test.go` (replaced by `archtest.RunItemsParallelism`) | — | grep: no `registry.` import in ABO |

Batches T2–T7 are mechanically similar and suit `/parallel-sweep`, partitioned by package so
parallel PRs never touch the same file (CLAUDE.md "Parallel Subagent Coordination").

## 5T.9 Tests (core must ship)

| Test | Proves |
|---|---|
| `TestParallelism_ZeroInvalid`, `TestBatch_ZeroParallelismRefusedAtRegister`, `TestRunItems_ZeroParallelismFailsBeforeFirstItem` | N8 |
| `TestRunItems_CancelStopsDispatch` (synctest: cancel while 3 of 8 in flight; assert no `fn` starts after cancel, `NotStarted` exact) | raw/06 §2 cancel fix |
| `TestRunItems_ErrorShapeSameSeqAndPar` (table: Sequential vs CPU, FailFast/Collect; same `*ItemsError` fields) | doc/behaviour mismatch fix |
| `TestRunItems_LabelGetsItemOnly_RaceFree` (`-race`, 64 workers) and `TestRunItems_CountersAtomic` | Label contract |
| `TestRunItems_WatermarkResume` (kill at random point, resume, every item done ≥ 1, prefix exact) | watermark |
| `TestRunItems_CPUBudgetShared` (two CPU runs ≤ cpu_slots concurrent fn) | budget |
| `TestRunItems_PauseParksBeforeItemClock` (synctest) | pause/item timeout |
| `TestResume_PolicyMatrix` (4 policies × {shutdown, crash, timeout, cancel}) | resume |
| `TestContinueInPlace_RequiresProgressAndCheckpoint`, `TestContinueInPlace_SameRunIDFreshTimeout` | yield |
| `TestCancel_GraceThenFenceRevoked`, `TestZombieCapBlocksModule` | cancel |
| `TestWriteSet_ResourceOverlapMatrix`, `TestConcurrencyKey_NamespaceEnforced` | G8 |
| `TestMode_OmittedIsPreviewForWriters`, `TestMode_ConflictingSpellings400`, `TestPreview_WriterRecordsIntentOnly` | dry-run |
| `TestApprove_RequiresVerifiedMethodAndPermission`, `TestFixer_PlanRefAgeAndRowsOnly` | approval |
| `TestSchedule_EnqueueAndClockOneBatch` (crash injection between), `TestSchedule_NoBootStampede`, `TestSchedule_CoalesceMissed`, `TestSchedule_DSTSpringForwardFallBack` (synctest, fake zone) | schedules |
| `TestShutdown_DrainBoundedAndStampsInterrupted`, `TestShutdown_UninterruptibleExtendsOnce` | shutdown |
| `TestAdoptActive_CopyAndMark_PreviousBinaryDoesNotResume` (fixture DB from HEAD; run the old reader over the migrated DB) | G5 |
| `TestReqStamps_ReadThroughThenDataMigration` | exception 2 |
| `TestLegacyHistory_ReadInPlaceUndecodableHidden` | history |
| `TestNoGlobals_Tasks` (R5 AST), `archtest.ResumeContinueCheckpoints`, `archtest.RunItemsParallelism` self-tests | guards |
| `taskstest.Conformance(t, defs...)` — modules run it over their own defs (from `pkg/ops/opstest/conformance.go`) | per-module |

## 5T.10 Config keys

| Key | Type | Default | Hot reload |
|---|---|---|---|
| `core.tasks.workers` | int | 8 | no (restart) |
| `core.tasks.system_workers` | int | 2 (reserved for `PrioritySystem`) | no |
| `core.tasks.cpu_slots` | int | `NumCPU` | yes (resizes semaphore for new acquisitions) |
| `core.tasks.shutdown_drain` | duration | 10s | yes |
| `core.tasks.shutdown_extend_max` | duration | 60s | yes |
| `core.tasks.min_checkpoint_interval` | duration | 30s | yes |
| `core.tasks.max_yields` | int | 1000 | yes |
| `core.tasks.zombie_cap` | int | 4 per module | yes |
| `core.tasks.standdown_lease` | duration | 2m | yes |
| `core.tasks.schedule_poll` | duration | 1m | no |
| `core.tasks.timezone` | IANA zone | server local | no |
| `core.tasks.windows.<name>.start` / `.end` | hour | `nightly`: 1 / 4 (current `maintenance.*` window, `config.go:3003-3005`) | yes |
| `core.tasks.windows.<name>.enabled` | bool | true | yes |
| `core.tasks.idempotency_ttl` | duration | 7d | yes |
| `core.tasks.history_retention` | duration | 90d | yes |
| `core.tasks.legacy_history_window` | duration | two minor releases (set at release) | no |
| `modules.<id>.tasks.max_concurrent` | int | 0 = unlimited | yes |
| `modules.<id>.schedules.<kind-suffix>.enabled` / `.interval` | bool / duration | per def | yes (read by `When`/`Interval`) |

## 5T.11 Metrics

Per-`App` registry ([05-metrics](05-metrics.md)); `kind` is a closed label set (registered kinds only,
current `opsmetrics.RegisterDefID`, `internal/opsmetrics/defids.go:24`).

| Name | Type | Labels |
|---|---|---|
| `mediacore_tasks_runs_total` | counter | `kind`, `shape`, `outcome`, `trigger` |
| `mediacore_tasks_run_duration_seconds` | histogram | `kind`, `outcome` |
| `mediacore_tasks_items_total` | counter | `kind`, `outcome` (done/failed/skipped/not_started) |
| `mediacore_tasks_inflight` | gauge | `kind` |
| `mediacore_tasks_queue_depth` | gauge | `module`, `priority` |
| `mediacore_tasks_dispatch_deferred_total` | counter | `reason` (key, writeset, ready, module_cap, zombie_cap, standdown, paused) |
| `mediacore_tasks_progress_age_seconds` | gauge | `kind` |
| `mediacore_tasks_zombies` | gauge | `module` |
| `mediacore_tasks_cpu_slots_in_use` | gauge | — |
| `mediacore_tasks_lane_in_use` | gauge | `lane` |
| `mediacore_tasks_yields_total` | counter | `kind` |
| `mediacore_tasks_schedule_lag_seconds` | histogram | `kind` |
| `mediacore_tasks_schedule_missed_total` | counter | `kind` |
| `mediacore_tasks_schedule_errors_total` | counter | `kind` |
| `mediacore_tasks_undecodable_records_total` | counter | `family` |

The ABO OTel names `audiobook_organizer.ops.*` (`opsmetrics.go:212-250`) are emitted in parallel
for one minor release for dashboard continuity, then removed (09).

## 5T.12 Open items

1. Owner decision per def for the 15 never-evaluated v2 crons (notably `deluge.protected-paths-sync`
   `*/30`): enable as a real `Schedule`, or delete the declaration. Default in this spec: delete.
2. Should consistency-sensitive readers (reports) be gated against writers of the same resource?
   Today they are not.
3. `aijobs` (2 durable OpenAI batch types): task shape or separate durable type? Owned by 05-ai.
4. Remote lane protocol promotion to core when a second app needs remote workers (e.g. remote
   transcode for TV/Movies).
5. UNKNOWN: whether opv2 rows written before the v3 projection existed were back-projected; the
   `legacyhist` fallback covers both cases, but its cost depends on the answer.
6. `Resource` vs the core `EntityRef{Module, Kind, ID}` (02 decision 6): if 06 places `EntityRef`
   in a layer ≤ 3, `tasks.Resource` becomes an alias of it.
7. [06-app-sdk](06-app-sdk.md) §6.4.3 must add `Registrar.Resources`, `Registrar.Lanes` and
   `Registrar.ChangeSink` (used by 5T.3.4, 5T.3.9, 5T.3.5).
8. 06 §6.4.3 also lists `Registrar.Schedule(s ...tasks.Schedule)`, a second place to declare a timer.
   This spec keeps `Common.Schedule` as the **only** timer declaration (v3's rule,
   `pkg/ops/definition.go:94`, and the lesson of the dead v2 cron strings); `Registrar.Schedule`
   should be dropped from 06, or defined as sugar that sets `Common.Schedule` on the module's own
   definition and fails registration if the definition already has one.
9. [05-rbac](05-rbac.md) §6.1 must add `core:tasks.manage` (pause, cancel others' runs, edit schedules);
   every other scope used here (`core:tasks.view`, `core:system.view`, `core:system.manage`,
   `core:admin`) is already in its catalog.

## Objections to 03

1. **R7 wording.** "Construction fails at registration" is achievable for Batch/Fixer, whose
   `Parallelism` is part of the definition, but not for a `RunItems` call inside a Task body, which
   is a runtime call. The mechanism here is: the zero value is unconstructible except by an explicit
   `tasks.Parallelism{}`/`var` (flagged by `archtest.RunItemsParallelism`), and such a call fails
   closed before its first item. Proposed R7 text: "Batch/Fixer parallelism is validated at
   registration; in-body `RunItems` parallelism is a required positional argument with no valid zero
   value, checked by `archtest.RunItemsParallelism` and refused at call time."
2. **Missing leaf package `lifecycle`.** `ErrShutdown`/`IsShutdown` are needed by core packages in
   layers below `tasks` (`storage`, `library`, `integrations/*` must tell a shutdown cancel from an
   operator cancel, as `internal/lifecycle/shutdown.go:16-29` exists to do). Proposed: add public
   `lifecycle` to §3.3 at layer 0 (stdlib only); `tasks.ErrShutdown` re-exports it.
3. **Task-kind grammar.** §3.6 shows `<moduleID>.<name>`; this spec allows dots inside `<name>`
   (`books.dedup.full-scan`) so that 233 existing IDs map by prefixing. Clarification, not a change.

## Out-of-scope defects found

1. `deluge.path-update` subscribes to `book.relocated` (`internal/plugins/deluge/path_update.go:49-53`),
   but nothing publishes that event and the registry never invokes `EventSubscription.Handler`
   (only `triggersToNames` reads `Triggers`, `registry.go:882,2330`): the Deluge path update never
   fires on relocation. Addressed by design in [05-events](05-events.md) (outbox-triggered task);
   the current behaviour is a bug independent of the extraction.
2. `OperationDef.Cancellable`'s comment claims the Cancel API rejects `false` (`types.go:52`);
   `Registry.Cancel` never reads it.
3. `RunItems` doc comment (`run_items.go:149`) contradicts the parallel return value (`:340-342,362-364`).
