<!-- file: docs/specs/platform-core/05-tasks.md -->
<!-- version: 1.9.1 -->
<!-- guid: 3472a3a5-c492-47aa-9b0b-17411cfb25d3 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Tasks (`core/tasks`)

Evidence commit `748fd787a`. Raw inputs: [`raw/06-tasks-ops.md`](../../plans/platform-core/raw/06-tasks-ops.md)
(the inventory every count below comes from), [`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md),
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md) §7 (ops decision digests),
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §7.
Names follow [03-architecture](03-architecture.md) §3.6; layer per R4 (03 v1.2.0) is **L4**
(`events`, `outbox`, `tasks`, `health`, `updater` are peers; `entity` and `lifecycle` are L0).

## 5T.0 Decision: N8 and N9 confirmed, with seven amendments

[02-decision-record](02-decision-record.md) N8 (parallelism is a required typed property) and N9
(one task system: the v3 authoring model on the production v2 executor) are **confirmed**. The
evidence for N9 is unchanged: v2 runs every op in production, v3 has the better authoring model and
zero native definitions (`internal/opscatalog/catalog.go:56`, `raw/06` §0). Reading the v3 code
directly forced seven amendments, each of which is a place where copying `pkg/ops` as-is would carry
a defect or dead weight into core:

| # | Amendment | Evidence | Why |
|---|---|---|---|
| A1 | `pkg/ops.Concurrency`'s zero value is **not** carried over: `tasks.Parallelism` has no valid zero value. | `pkg/ops/batch.go:123` (`concCPU concKind = iota // the zero value: an omitted Concurrency is CPU()`), `pkg/ops/batch.go:31-33` | v3 made the omission *parallel* instead of *sequential*, which fixes the v2 trap (`run_items.go:155-157` clamps 0 to 1) but still lets an author omit the decision. N8 asks that the decision be stated. |
| A2 | `ResumePolicy` is **required** (zero refused), not defaulted by shape. | v2 refuses zero at boot (`internal/operations/registry/types.go:59`, `ValidateOpDef` `registry.go:747`); v3 defaults by kind (`pkg/ops/runtime.go:73-82`: Task→Drop, Batch→Continue, Fixer→Ask) | 162 of 233 ops drop on restart (`raw/06` §6.2). Whether an interrupted run is lost is a data-correctness decision; a shape default hides it. |
| A3 | The v3 `Writer` (hard-typed to `database.Book`/`BookFile`/`BookAuthor`) becomes a generic gated-write primitive; typed writers move to the module that owns the type. | `pkg/ops/writer.go:46-92`, `SubjectType book/book_file` `pkg/ops/writer.go:112-118` | Core may not import app types (R1). The *gate* (fence, preview, journal, evidence) is generic; `ModifyBook` is not. |
| A4 | A resource is the L0 reference `entity.Ref{Module, Kind, ID}` (03 §3.3; `tasks.Resource` is only an alias of it) and `ConcurrencyKey` is namespaced (gate G8). | v2 `type Resource string` `types.go:384`; v3 `type Resource string` `pkg/ops/effects.go:16-39`; `ConcurrencyKey string` `types.go:97` | Two modules declaring `"files"` would serialise each other today; one reference type across tasks, outbox, audit and acquire (COR-17). |
| A5 | `LaneMac` becomes a module-declared lane; core has no audio concept. | `pkg/ops/lanes.go:12-15`, `pkg/ops/definition.go:105-107` | Lanes are a generic capacity class; "audio decode" is ABO's. |
| A6 | Subprocess isolation (`OperationDef.Isolate`) and `Synchronous` are **not** carried over. | 0 non-test defs set `Isolate: true` or `Synchronous: true` (grep at HEAD); `itunes.import` notes "DISABLED ... Pebble is single-writer" (`raw/06` §1.1) | A mode nothing can use is dead weight and a second code path in the executor. |
| A7 | The `Pipeline` shape (`KindPipeline`, `pkg/ops/definition.go:42-43`) is **not** carried over. | The only `ops.Pipeline(` calls at HEAD are lint testdata (`tools/cmd/oplint/testdata/src/.../unbundled_def_nobundle/nobundle.go:11`) and one adapter test (`internal/operations/registry/v3_adapter_test.go:356`) | Same rule as A6: a shape with no definition is a second runner to maintain. A future DAG definition is an additive API change (SIMP-21). |

What is kept verbatim in concept: the three shapes Task/Batch/Fixer (`pkg/ops/definition.go:35-41`),
`Effects` and the write-set gate, `Schedule` as the single timer declaration (`pkg/ops/schedule.go:17-32`),
`CancelContract` (`pkg/ops/contract.go:10-48`), `Approval` (`pkg/ops/approval.go:18-27`), the run
context `RC` replacing v2's `Reporter` + type-asserted side interfaces (`pkg/ops/rc.go:113-118`), the
chunk-ledger Batch runner (`internal/operations/registry/batch_runner.go`), the ten-state v3 machine
with compare-and-set transitions (`internal/operations/state/v3.go:20-31,187`), v2's dispatcher gates,
watchdog, resume and shutdown (`raw/06` §5), `ErrContinueInPlace` (`continue_in_place.go:66`), and
the preview-by-default rule (`internal/operations/opmode/dryrun.go:1-25`).

## 5T.1 Purpose & scope

`core/tasks` is the **only** durable background-work system in every app: definition, enqueue,
dispatch, execution, progress, checkpoint/resume, cancel, approval, schedule, history. It owns the
`/api/core/v1/tasks` API and the persistence **interfaces** `tasks.RunStore`, `tasks.ClockStore`,
`tasks.PauseStore` and `tasks.StandDownStore` (5T.3.13, R4-1), with a **native** implementation over core's own `c/tasks/`
keyspace, the one new apps, `examples/hello` and `coretest` use (5T.6.1). An app with pre-core run data
passes its own **legacy** implementation of the same interfaces through `legacy.Stores.Tasks`
([06](06-app-sdk.md) §6.4.11; core's legacy package, outside the API promise, R6-3): ABO's is books code over ABO's existing ops families in their existing format,
so during the extraction the previous build reads every run the new build wrote (5T.6.2). Core holds no
ABO key name or row format and no storage grant over those families (R4-1). It does **not** own:
domain writers (modules),
the outbox (`core/outbox`, [05-events](05-events.md), which *uses* tasks for its relay and whose
module-side consumers are task runs, 5T.3.12), SSE transport ([05-realtime](05-realtime.md)),
permission model ([05-rbac](05-rbac.md)), legacy-family ownership ([05-storage](05-storage.md) §5S.9.1).

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
| Shutdown: ops drain budget 10 s, HTTP 30 s; the code comment assumes a unit `TimeoutStopSec` of 30 s, but the committed unit sets none (only `deploy/local.conf.example:33` does) | `internal/server/server_lifecycle.go:611,582,174-178`; `deploy/audiobook-organizer.service` ([05-health](05-health.md) 5H.2) |
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
	ShapeTask  Shape = iota + 1 // one function, runs once
	ShapeBatch                  // source + per-item function; the runner owns the loop
	ShapeFixer                  // plan -> approve -> apply over rows
	// There is no Pipeline shape (A7).
)

// Definition is opaque; build it with Task, Batch or Fixer.
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

	Effects Effects      // required: ReadOnly(...) or Writes(...)
	Perms   Permissions  // required: Perms.Start at least
	Resume  ResumePolicy // required (A2): zero value refused

	Cancel          CancelContract // zero = shape default (5T.3.6)
	ConcurrencyKey  Key            // zero = Key(Kind)
	NotWhile        []Kind         // v2 DependsOn: must not be running
	Priority        Priority       // zero = PriorityNormal
	Timeout         time.Duration  // per attempt; 0 = shape default; > 24h refused
	ProgressTimeout time.Duration  // watchdog gap; 0 = derived (5T.3.6)
	Schedule        *Schedule      // the ONE timer declaration (5T.3.8); modules use ScheduleFromConfig
	Approval        *Approval      // required for ShapeFixer
	Ready           []ReadySignal  // readiness to wait for before dispatch; nil = {SignalStorage}; ReadySignal = health.ComponentID (lands at S5, 5T.4)
	Lanes           []LaneID       // lanes items may be handed to (5T.3.9)
	Subjects        SubjectsFunc   // optional per-run write set (5T.3.4)
	Dedupe          DedupePolicy   // queued-run dedupe/merge (v2 DedupeQueuedRuns/MergeQueuedParams)
	Debounce        *Debounce      // v2 Batchable/BatchWindow/BatchMaxWait
	Yields          bool           // may be quiesced by a stand-down (requires ResumeContinue), 5T.3.7
	Notify          Notify
	FormerKinds     []Kind // renames and legacy IDs (v2 aliases.go / FormerIDs); resolved on every read
	// LegacyDefID is the def ID older builds register for this kind. When the
	// app supplies legacy stores (5T.6.2), runs, clocks and Requires stamps of
	// the kind are STORED under it (its StoredKind, 5T.3.13), so an older build
	// resumes and shows them; with the native stores it is only an alias. It is
	// implicitly a FormerKinds entry. Required for every adapted v2 def and every
	// native successor of one (T2-T7); "" for a kind no older build has (its runs
	// are stored under Kind itself, 5T.6 rule 3).
	LegacyDefID     Kind
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
	Source        func(rc *RC, p P) (Source[T], error)
	Item          func(ic *ItemRC, p P, item T) error // MUST be idempotent
	Finish        func(rc *RC, p P, s Summary) (R, error)
	Parallelism   Parallelism // required (N8); FromParam allowed here
	PartitionBy   func(T) string             // one key per item: same key -> same worker, in order
	ExclusiveRefs func(T) []entity.Ref       // many refs per item: overlapping items never run at once (5T.3.4)
	ChunkSize     int                        // 0 = 256
	ItemTimeout   time.Duration              // clock starts after the pause gate and ref locks
	OnItemError   ErrorMode                  // zero = Collect (count failed, continue)
	Label         func(T) string             // pure function of the item; runs in workers
}

type FixerSpec[P, C any] struct {
	Common // Effects must be Writes(...); Approval required
	Candidates  func(rc *RC, p P) (Source[C], error)
	Load        func(rc *RC, p P, rowID string) (C, bool, error)
	Evaluate    func(ic *ItemRC, p P, c C) (Row, error) // read-only; runs at plan AND apply
	Apply       func(ic *ItemRC, w Writer, p P, row Row) error
	Parallelism Parallelism
	PartitionBy func(Row) string // optional; Row.Subjects always act as ExclusiveRefs at apply
	AfterApply  func(rc *RC, applied []string) error
	Guards      []Guard
}

func Task[P, R any](kind Kind, s TaskSpec[P, R]) Definition
func Batch[P, T, R any](kind Kind, s BatchSpec[P, T, R]) Definition
func Fixer[P, C any](kind Kind, s FixerSpec[P, C]) Definition
```

`Source[T]`, `SourceOrder` (`Snapshot`/`AppendOnly`/`Unordered`), `DirtySet`, `Row`, `Guard`,
`Summary`, `Total` keep their v3 meaning (`pkg/ops/batch.go:197-259`, `pkg/ops/fixer.go:20-80`),
with `Row.Subjects []entity.Ref` instead of book subjects. `DirtySet`'s `prefix` is a key inside the
calling module's `storage.Keyspace` ([05-storage](05-storage.md)), never a raw prefix.

Registration is only through `module.Registrar` ([06-app-sdk](06-app-sdk.md) §6.4.3):

```go
// 06 §6.4.3: r.Tasks(defs ...tasks.Definition)
//            r.Resources(kinds ...string)   // resource kinds this module owns (G8)
//            r.Lanes(lanes ...tasks.Lane)
//            r.ChangeSink(s tasks.ChangeSink)
// core-only (kernel): windows (5T.3.8), the relay's Every schedule, PrioritySystem
```

The kernel rejects (boot fails before listeners open, `startup` class Fatal): a kind whose module
prefix is not the registering module; duplicate kinds or `FormerKinds` colliding with any live kind;
any constructor problem; `Perms.Start` outside `<module>:` or `core:`; a `Perms.Start` satisfiable by
a view-class scope without `Perms.StartableByViewers` (5T.3.7, SEC-9); `Effects.Writes` naming
another module's resource; an undeclared resource kind; `Resume == ResumeContinue` on an `Unordered`
Batch source; a zero `Parallelism` on Batch/Fixer; both `PartitionBy` and `ExclusiveRefs` on one
Batch; a Fixer without `Approval`; a scheduled writer whose schedule says neither `Live()` nor
`Preview()` (v3 rule D8, `pkg/ops/schedule.go:96-118`); a `ScheduleFromConfig` key that the
registering owner has not declared with type `tasks.ScheduleConfig` in its own config namespace, or
that another kind already uses (5T.3.8, R2-7); an `Every` schedule on any kind but
`core.outbox.relay`; a `LegacyDefID` that is not in the op-ID ledger (5T.8.2) or that two kinds
declare; a `TaskStarts` entry (5T.3.10) naming a kind that no manifest in the build declares. That
last check runs at **P7** (06 §6.5.1), against the registered kinds **plus every manifest's
`TaskLedger`**, disabled modules' manifests included, so disabling module B never fails the boot of a
module A that lists one of B's kinds (G1, COR3-10); a start of such a kind while B is disabled is
refused at runtime with `404 tasks.unknown_kind` (06 O3).

### 5T.3.2 Parallelism (N8), `RunItems` and `ForEach`

```go
// Parallelism is a declared worker count with its reason. Its zero value is
// INVALID: Validate returns ErrNoParallelism, Batch/Fixer registration fails,
// and RunItems/RunSeq/ForEach return before touching an item.
type Parallelism struct {
	kind   parKind // 0 = unset
	n      int
	reason string
	field  string // FromParam
	max    int
}

func CPU() Parallelism                                 // runtime.NumCPU() workers sharing the executor CPU budget
func Fixed(n int, reason string) Parallelism           // n workers, clamped to [1, MaxFixed]; reason non-empty
func Sequential(reason string) Parallelism             // one worker; reason non-empty, shown in the def list
func FromParam(field string, def, max int) Parallelism // Batch/Fixer only: read from run params, clamped [1, min(max, MaxFixed)]

const MaxFixed = 64 // v3 MaxNetworkWorkers, pkg/ops/batch.go:132-134

// ErrNoParallelism is the single sentinel for an undeclared parallelism (03 R7's name).
var ErrNoParallelism = errors.New("tasks: parallelism not declared (use CPU, Fixed or Sequential)")

// ErrNoRunContext: ForEach was asked for a Watermark without an RC-derived ctx.
var ErrNoRunContext = errors.New("tasks: watermark needs a run context")

func (p Parallelism) Validate() error // nil or the bare ErrNoParallelism
func (p Parallelism) String() string  // "cpu", "fixed(4): <reason>", "sequential: <reason>"
```

**One bounded-count constructor (SIMP-17).** The former `Fixed(n)` ("exactly n") and `IO(n)`
("1 ≤ n ≤ MaxIO") differed only by a clamp, so v1 has one: `Fixed(n, reason)`, where the reason says
what bounds `n` (a provider's rate budget, disk-bound I/O, a remote API's connection limit). There is
no `tasks.Network`; [10](10-tv-movies-proof.md) uses `Fixed`. 05-storage's `DataMigration` takes a
`tasks.Parallelism` rather than a parallel `Shards` type, with its runner at L4 (SIMP-17; owned by
05-storage).

**CPU budget.** Every `CPU()` worker acquires one slot from an executor-wide semaphore
(`core.tasks.cpu_slots`, default `runtime.NumCPU()`) per item and releases it when `fn` returns.
Today eight executor workers (`register.go:102`) may each run a `NumCPU` pool (~45 sites,
`raw/06` §2), i.e. up to 8×NumCPU CPU-bound goroutines; the budget turns that into fair sharing.
`Fixed` and `Sequential` take no CPU slot. Cost: a CPU-bound run is slower in wall clock while
another CPU-bound run is active; this is the intended trade. The operator dial
`core.tasks.kinds[<kind>].parallelism_cap` (5T.10) lowers any declared count at run time without
changing a definition (OPS-17: an operational lever that touches no persisted shape). Task kinds
contain dots and hyphens, which the config path grammar refuses (05-config `Path`,
`[a-z][a-z0-9_]{0,47}` per segment), so the dial is **one** map-typed key, `core.tasks.kinds`
(`map[tasks.Kind]tasks.KindDial`, `KindDial{ParallelismCap int}`; env value JSON), not one key per kind
(COR3-11).

```go
// RunItems runs fn over items inside a run. It is the in-body loop for Task
// shapes; Batch/Fixer use their runner. Semantics in the table below.
func RunItems[T any](rc *RC, par Parallelism, items []T,
	fn func(ic *ItemRC, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error)

// RunSeq is RunItems over a stream whose length may be unknown. It cannot
// take Watermark (no stable index) unless Key is set.
func RunSeq[T any](rc *RC, par Parallelism, items iter.Seq[T], total Total,
	fn func(ic *ItemRC, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error)

// ForEach is the same loop for code that holds no *RC (COR-23): a library call
// reached through an API that takes only a ctx (e.g. an indexer pool's fan-out,
// 05-indexer; an acquire decision pass, 10). Same worker shape, cancellation,
// error shape, Label and ExclusiveRefs rules. When ctx descends from an RC
// context, ForEach joins that run's CPU budget, pause gate, ref-lock table and
// liveness; otherwise it is a plain bounded pool. Watermark is refused
// (ErrNoRunContext): checkpointing needs a run.
func ForEach[T any](ctx context.Context, par Parallelism, items []T,
	fn func(ctx context.Context, item T) error, opts ...ItemsOptions[T]) (ItemsResult, error)

type ItemsOptions[T any] struct {
	Label           func(T) string       // pure; receives only the item (cannot read counters)
	OnError         ErrorMode            // FailFast or Collect; zero = FailFast
	ItemTimeout     time.Duration
	ExclusiveRefs   func(T) []entity.Ref // overlapping items never run at the same time (5T.3.4)
	Watermark       string               // non-empty: checkpoint the contiguous-completion watermark
	                                     // under this name and resume from it (requires a stable item order)
	CheckpointEvery int                  // 0 = 64 items
	ProgressBase    int                  // absolute offset when items is a slice of a larger set
	ProgressTotal   int                  // absolute denominator
	Key             func(T) string       // RunSeq + Watermark: stable key instead of index
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

// ItemsError is the ONLY non-nil error type RunItems/RunSeq/ForEach return.
type ItemsError struct {
	First      error // the first item error to COMPLETE (completion order, both modes)
	FirstIndex int   // -1 when no item ran (ErrNoParallelism, ErrNoRunContext)
	Errs       []error // every item error observed before the loop stopped, completion order
	Cause      error   // context.Cause(rc.Context()) when the run was stopped, else nil
}

func (e *ItemsError) Error() string
func (e *ItemsError) Unwrap() []error // First, Errs and Cause: errors.Is(err, context.Canceled) and errors.Is(err, ErrNoParallelism) work

// ItemRC is one item's context. Executor-owned counters: callers keep none.
type ItemRC struct{ /* unexported */ }

func (ic *ItemRC) Context() context.Context // derived from the run ctx; carries ItemTimeout
func (ic *ItemRC) Count(name string, delta int64) // atomic; folded into ItemsResult/Summary
func (ic *ItemRC) Skip(reason string)
func (ic *ItemRC) Log() *slog.Logger
func (ic *ItemRC) Writer() Writer
func (ic *ItemRC) Lane(id LaneID) (LaneLease, error)
func (ic *ItemRC) LockRefs(refs ...entity.Ref) (release func(), err error) // 5T.3.4
```

| Behaviour | Contract (identical sequential and parallel) | Fixes |
|---|---|---|
| Worker shape | Fixed set of `min(par.Workers(), len)` goroutines (started with `wg.Go`) pulling the next index from an atomic cursor; no goroutine per item | `run_items.go:335-358` spawns per item |
| Cancellation | Each worker checks `ctx.Err()` **before taking** an index and **again before calling** `fn`; once the run ctx is done no new `fn` starts. Items never started count `NotStarted`. In-flight items see their ctx canceled with the run's cause | `run_items.go:336-345` random `select`; the per-op guard in `docs/decisions/2026-10-31-lsh-backfill-cancel-guard.md` becomes unnecessary |
| Pause | Workers park on the **executor's** pause gate reached through `rc`, before the item clock starts | global `pause_gate.go:197-199` |
| Exclusion | With `ExclusiveRefs`, the worker takes the executor's keyed ref locks for the item (sorted, all-or-wait, cancellable) after the pause gate and before the item clock; items whose ref sets overlap therefore never run concurrently, in this run or against any other run locking the same refs. Fixer apply does the same with `Row.Subjects` (COR-7: a season pack and an overlapping single episode cannot import into one slot at once) | `PartitionBy` alone takes one key per item |
| Label | `Label(item)` is called in the worker before `fn` for "current item"; progress text is rendered by core from atomic counters (`done/total`, counters), never by the caller | `run_items.go:45-54,268` race; the CLAUDE.md note about `Label` closures becomes structurally impossible except via deliberately captured mutable state, which `-race` in `coretest` catches |
| Progress | Monotonic atomic completion count; delivered to the reporter in completion order under one lock (current `progressMu` property, `run_items.go:271-285`) | — |
| FailFast | First error stops dispatch; in-flight items finish (their ctx canceled with `ErrItemsStopped`); return `*ItemsError` with `First` = first to complete, `Errs` = all observed | `run_items.go:149` vs `:340-342` mismatch; sequential bare error `:316` |
| Collect | All items run; `*ItemsError` iff any failed | — |
| Run canceled | `*ItemsError{Cause: context.Cause(ctx)}` (+ any item errors) | `ctx.Err()` loses `ErrShutdown` cause |
| Undeclared parallelism | `*ItemsError{First: ErrNoParallelism, FirstIndex: -1}` before any item; one return shape for every failure (COR-23). `Validate` and registration report the bare sentinel | doc said bare, table said wrapped |
| Watermark | Contiguous-completion prefix (v2 `completionTracker`, `run_items.go:114-137`) checkpointed via `rc.Checkpoint` under `Watermark` name, serialised; restored automatically on resume; failed items never advance it | caller-wired `ResumeFrom`/`CheckpointStateFn` |
| Liveness | Every completed item touches liveness; the watchdog never kills a progressing loop | — |
| Stand-down hold | If the run holds a stand-down (5T.3.7), each item renews it and a lost hold fails the item before its write | `run_items.go:263-267` |

`archtest.RunItemsDeclareParallelism(t, pkgs...)` (03 R7's name) replaces
`internal/plugins/runitems_concurrency_lint_test.go`. For the parallelism argument of
`RunItems`/`RunSeq`/`ForEach` and the `Parallelism` field of `BatchSpec`/`FixerSpec` it accepts:
(a) a call to a `tasks` constructor; (b) a `tasks.Parallelism`-typed variable, parameter or field
whose every assignment in the analysed packages is (a); (c) a field of an options struct marked
`//mediacore:required` whose owner calls `Parallelism.Validate` when the options are constructed
(e.g. 05-indexer's search options). (b) and (c) admit pass-through, so core's own
`RunItems(rc, o.Parallelism, …)` is legal (COR-23). It flags `tasks.Parallelism{}` literals, a zero
`var`, and `Sequential("")` / `Fixed(n, "")`. 03 R7 (v1.4.0) states the same rule.

### 5T.3.3 Run context

```go
type RunID string // opaque; assigned by RunStore.NewID (5T.3.13); books' legacy store keeps HEAD's op ID, so a run keeps its ID across builds

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
func (rc *RC) LockRefs(refs ...entity.Ref) (release func(), err error) // 5T.3.4
func (rc *RC) Canceled() bool

// The causes an RC context is canceled with (context.Cause). All wrap
// context.Canceled or context.DeadlineExceeded so existing errors.Is checks hold.
var (
	ErrCanceled  error // operator cancel
	ErrShutdown  error // process shutdown; re-exported from core/lifecycle (03 §3.3, L0)
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

### 5T.3.4 Effects, resources, write sets, concurrency keys, ref locks (G8)

```go
// Resources are entity.Ref values (03 §3.3, L0; COR-17). A Ref with ID == ""
// means the whole kind. tasks adds helpers and a readability alias only; it
// defines no reference type of its own.
type Resource = entity.Ref // alias, not a new type (SIMP-16 option)

func Res(module, kind string) entity.Ref
func ResID(module, kind, id string) entity.Ref
func Overlaps(a, b entity.Ref) bool // the dispatch overlap rule below
func FormatRef(r entity.Ref) string // "books/book_files" or "books/book_files#<id>"

// LockRefs is rc.LockRefs for code that holds only a ctx (COR2-16): when ctx
// descends from an RC it takes that executor's ref-lock table (same sorted
// all-or-wait rule); otherwise it returns ErrNoRunContext.
func LockRefs(ctx context.Context, refs ...entity.Ref) (release func(), err error)

type Effects struct{ /* unexported */ }

func ReadOnly(reads ...entity.Ref) Effects
func Writes(ws ...entity.Ref) Effects
func (e Effects) AlsoReads(rs ...entity.Ref) Effects
func (e Effects) AlsoDeletes(rs ...entity.Ref) Effects // flagged for owner review by archtest

// SubjectsFunc narrows a run's write set to concrete IDs at enqueue time
// (e.g. one book merge writes books/books#a and #b; a season-pack import writes
// every episode it covers). Returned refs must be within Effects.Writes kinds.
type SubjectsFunc func(params json.RawMessage) ([]entity.Ref, error)

// Key is a namespaced mutual-exclusion key "<module>.<name>". A module may use
// keys in its own namespace and keys in "core." (e.g. "core.storage.exclusive",
// held by compaction and backup).
type Key string
```

Dispatch conflict rule (Gate 3b generalised): a queued run conflicts with a running run iff some
ref in one write set overlaps some ref in the other, where `{M,K,""}` overlaps every `{M,K,*}` and
`{M,K,id}` overlaps `{M,K,id}` and `{M,K,""}`. Reads are declarative (shown in the UI, used by
preview/audit), not gated — the same as today (`dispatcher.go:163-178`); an open item asks whether
readers of consistency-sensitive reports should gate. A run whose `ConcurrencyKey` is held stays
queued (Gate 3, `dispatcher.go:143-153`); zombies keep their key until their goroutine exits
(current D6). The kernel-registered data-migration kinds (5T.3.12) are outside this rule: their
`Effects` are attribution-only and they serialise only on their `ConcurrencyKey` (COR4-5).

**Ref locks (COR-7).** The dispatch gate sees only write sets known at enqueue. Work that learns its
subjects while running (an RSS sync deciding what to grab, a search task, an import of one download
that covers many entities) serialises on `rc.LockRefs(refs...)` / `ic.LockRefs(refs...)`: one
keyed-lock table per executor (per `App`, R5), acquired in sorted `FormatRef` order all-or-wait,
cancellable through the caller's context, released by `release()` or when the attempt ends; a zombie's
locks are released only when its goroutine exits, as for `ConcurrencyKey`. A ref lock grants no write:
the write set is still `Effects`. `ExclusiveRefs` and Fixer `Row.Subjects` use the same table, so a
grab inside a search task and one inside rss-sync that cover an overlapping entity never interleave
their check-then-write: 10's `Engine.Grab(ctx, l, d)` takes the caller's `RefLocker` `l` (an `*RC` or
`*ItemRC`, so `l.LockRefs(refs...)` is this table) over its `Refs` (sorted by `FormatRef`) and holds them
across the re-check and the CAS write ([10](10-tv-movies-proof.md), owned there; COR3-8), so a grab and a module's `ExclusiveRefs` items interlock on one table; there is no
second grab mutex (COR2-16). A coarser option with no new code is a shared `ConcurrencyKey` such as
`core.acquire.grab.<module>`, already legal under the key rule.

### 5T.3.5 Writer: the generic gated write

```go
// Writer is the run's only write path. Modules wrap it in typed writers
// (books: ModifyBook, ModifyBookFile, RepointBookFile, ModifyBookAuthors —
// today's pkg/ops/writer.go:46-92 methods: their contract interface moves to
// internal/books/contract and their implementation to internal/books/writer,
// the opswriter rename; 04 §2.4, §8.6; MOD6-12).
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
	Name     string     // "books.modify_book"
	Resource entity.Ref // must be inside the def's Effects.Writes
	Subject  entity.Ref // the entity written (ID set)
}

type Change struct {
	Subject  entity.Ref
	Field    string
	Old, New string
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
| Checkpoint | `rc.Checkpoint(v)` JSON-encodes into the run's checkpoint record (`RunStore.SaveCheckpoint`, 5T.3.13); throttled by `core.tasks.min_checkpoint_interval`; written in the same storage batch as the progress record. Batch/Fixer checkpoint through the store's chunk ledger and item-key snapshot (`SaveChunk`). A batch (T2–T7) that changes a kind's checkpoint shape keeps decoding the previous shape, or the kind resumes from zero once after a rollback, a consequence listed in 09 §9.2.1's rollback table (OPS4-11). |
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

	// StartableByViewers is a reviewed reason allowing Start to be satisfied by
	// a view-class scope (05-rbac PermissionSpec.Class). Empty = refused (SEC-9).
	StartableByViewers string
}

type Approval struct {
	PlanRequired bool          // Live start must name a Preview run (plan_ref) of the same kind
	MaxPlanAge   time.Duration // 0 = 7 days (pkg/ops/approval.go:29-30)
}
```

| Action | Who | Fail mode |
|---|---|---|
| list/get defs and runs | `Perms.View` per def; rows/results of runs the caller may not view are omitted (current per-viewer filter, `internal/diagnostics/service.go:138-150`, `sysinfo` `OperationReadCheck`) | closed |
| start | `Perms.Start`, plus the route floor `rbac.AnyAuthenticated` as 05-rbac defines it (a non-`Confining` scope; a confined credential reaches a definition only when `Perms.Start` names its scope explicitly, SEC-3). `Perms.Start` may not be satisfiable by a view-class scope unless the definition sets `StartableByViewers` (SEC-9: today's `scan.trigger` floor exists because a viewer could otherwise start a `library.view`-only def, `opsv3/handler.go:236-246`; `bulk-apply-preview` is one, 4 h timeout, `internal/server/bulk_apply_preview.go:269,278`). An adapted legacy def's `Perms.Start` is the one legacy-atom requirement value `rbac.LegacyAtom(atom)` (05-rbac §6.3; the same value a legacy route's `s.perm` check uses, COR5-4), which each checker resolves its own way: **(a)** a role principal, and a key with an extension record, through `Catalog.LegacyReach(atom)` (R4-3), not the atom's expansion, so a def declaring `settings.manage` (the old op-def default) requires `core:admin`; **(b)** a legacy `abk_` key **without** an extension record passes iff the atom is in the key's stored atoms **and** its owner's role checker satisfies `LegacyReach(atom)`: HEAD's `intersect(role atoms, key atoms)` (`internal/server/middleware/auth.go:364-365`), with the row rule's dominance on the role side, so a key scoped `["settings.manage"]` keeps starting the ops it starts at HEAD (SEC5-7); **(c)** the auth-disabled principal through the atom's expansion (05-rbac §4.11), because HEAD's op start checks pass every request with auth off (`internal/server/wire_ops_v3_routes.go:96-98`; an `auth_off: pass` site, SEC5-9), so with auth off such a def starts as it does at HEAD and no auth-off tightening is recorded for op starts (SEC5-8). An in-handler `auth.Can(ctx, atom)` site inside a legacy handler or an adapted def's code is not a `Perms.Start`: it becomes `rbac.LegacyCheck(atom, readSide)` with its entry from the reach snapshot's in-handler list (05-rbac §3.1, §6.3; 05-http-api §8.3), which the auth-disabled principal never satisfies, because HEAD's `auth.Can` returns false with auth off (SEC5-9, SEC5-14). Adapted legacy defs declaring only `library.view` translate to `rbac.AllOf(books:scan, books:view)`, which keeps the floor | closed |
| cancel | the run's actor, or `Perms.Cancel`, or `core:tasks.manage` | closed |
| approve / decide (`awaiting_decision`) | `Perms.Approve` (decide on a run interrupted under ResumeAsk: `Perms.Approve` if set, else `Perms.Start`) **and** a verified method, the same rule as 05-rbac §4.10 (SEC-24): an interactive session, a sign-in through an app-level authenticator extension whose `MethodSpec.Interactive` is true (only `app.Options.Authenticators` may set it, 06 §6.4.5, SEC2-16; ABO's Cloudflare Access is one; a module's compat `Registrar.AuthMethod` never is), or an API key whose scopes **name** the required scope explicitly (not via `core:admin` or another implying scope). Refused: a legacy full-scope key (`LegacyFullScope`), a delegated session (`MethodSessionDelegated`), and a forwarded header, which is never identity (current rule, `opsv3/runs.go:395-410`) | closed |
| pause/resume executor, edit schedules | `core:tasks.manage` | closed |

Every approve, decide, cancel-by-other and schedule edit writes an `audit` record, named per [05-rbac](05-rbac.md)
§7.1: `core.task.approve` / `core.task.reject` (approve and decide), `core.task.cancel` (cancel by another
actor); a schedule edit is a write of its config key and is recorded as `core.settings.update`.

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
5. A run started from an outbox message runs in the mode its consumer declares: `ConsumeIntoTask[Raw]`
   requires exactly one of `outbox.Live()`/`outbox.Preview()` at registration (the outbox form of
   D8), and `outbox.Tx.StartRun` refuses an unset `StartRequest.Mode` ([05-events](05-events.md)
   5E.4, COR2-5). The rule-2 default never applies to `Trigger: Outbox`. The relay's own nudge start
   is `ModeLive` (5E.5.3).

**Stand-down** (generalises the `library.scan` special case, `dispatcher.go:155-161`): a def that
declares `Common.Yields = true` (meaning: it is `ResumeContinue` and safe to quiesce) can be stood
down by `rc.StandDown(key)` from another run: running holders of `key` are quiesced
(`stopping{quiesce}` → `interrupted_quiesced`, re-queued when the hold is released), new runs of
`key` stay queued while the hold lives; the hold is a lease renewed by every `Writer.Do`, lost
after `core.tasks.standdown_lease` (default 2 m) without renewal. While a hold lives its marker is
persisted through `Stores.StandDown` (5T.3.13; MOD6-4): staged when the hold is taken, re-staged on
renewal at most once a second (HEAD throttles the same write, `scan_standdown.go:257-258`), cleared
when the last holder releases. A marker found at boot keeps the runs it quiesced from resuming that
boot (5T.4 step 2), because the holder's work is half-applied: HEAD's restart behaviour, kept.

### 5T.3.8 Schedules: one mechanism, durable clock

```go
type Schedule struct{ /* unexported */ }

// ScheduleFromConfig is the standard schedule (SIMP-6). Its timing is a config
// value of type ScheduleConfig at key, which the kind's owner declares in its
// own config schema with a default; tasks re-reads it every poll.
// The one key rule (R2-7): key has the form
//   "<owner>.<subsystem>.schedules.<name>"
// where <owner> is "core" for a core kind and "modules.<id>" for a kind of
// module <id> (the registering owner only), and every segment matches the
// config grammar [a-z][a-z0-9_]{0,47} (no hyphens). The key must be declared
// by that owner with type ScheduleConfig, and no other kind may use it.
// Anything else fails registration. Examples: "core.ai.schedules.batch_poll",
// "core.acquire.schedules.rss_sync", "core.updater.schedules.check",
// "modules.books.scan.schedules.full_sweep".
func ScheduleFromConfig(key string) *Schedule

// ScheduleConfig is the value type of every schedule key. Exactly one of
// Every, DailyAt, Cron and Window is set when Enabled is true.
type ScheduleConfig struct {
	Enabled bool          `json:"enabled"`
	Every   config.Duration `json:"every,omitempty"`  // >= core.tasks.schedule_poll; a string such as "24h" in defaults and files (05-config)
	DailyAt string        `json:"daily_at,omitempty"` // "HH:MM" in core.tasks.timezone
	Cron    string        `json:"cron,omitempty"`     // 5-field, in core.tasks.timezone
	Window  string        `json:"window,omitempty"`   // member of a maintenance window
	Order   int           `json:"order,omitempty"`    // position inside Window
}

// Every is a fixed, non-configurable schedule reserved for the outbox relay's
// ensure-running tick (core.outbox.relay, 05-events 5E.5.3). The kernel
// refuses it on every other kind, core or module.
func Every(d time.Duration) *Schedule

func (s *Schedule) OnStart() *Schedule
func (s *Schedule) When(gate func() bool) *Schedule // a non-timing gate re-read each poll (e.g. updater mode)
func (s *Schedule) Params(p any) *Schedule
func (s *Schedule) Live() *Schedule
func (s *Schedule) Preview() *Schedule
func (s *Schedule) Missed(p MissedPolicy) *Schedule

type MissedPolicy uint8

const (
	Coalesce MissedPolicy = iota // fire once for any number of missed instants; count them (current countDue, v3_schedule.go:501)
	SkipMissed                   // do not fire for instants missed while down; wait for the next
)

// Window is declared by core config (core.tasks.windows.<name>); kinds join it
// with ScheduleConfig.Window. The window runs as core.window.<name>: members in
// Order, once per window opening.
type Window struct {
	Name       string
	Start, End int // hour 0-23, wraps midnight
}
```

**One mechanism, one key rule (SIMP-6, R2-7).** Durable work at a cadence of one minute or more is a
task with a `Schedule`, and its timing is one `ScheduleConfig` key of the form
`<owner>.<subsystem>.schedules.<name>` (`core.<subsystem>.schedules.<name>` for core kinds,
`modules.<id>.<subsystem>.schedules.<name>` for module kinds; segments `[a-z][a-z0-9_]{0,47}`, no
hyphens) that the kind's owner declares in its **own** config section and passes to
`ScheduleFromConfig`. Every scheduled kind, core or module, is therefore overridden the same way and
listed by `GET /schedules`. There is no central `core.schedules.` namespace (the round-1 form is
withdrawn): the key sits inside the owner's own subsystem section (`core.backup.schedules.create`,
`core.integrations.schedules.health`, `core.diagnostics.schedules.prune`, and
`core.log.schedules.journal_prune`, declared by the L4 `internal/logjournal` package that owns the
journal-prune kind, since L0 `log` cannot import `tasks`). A key is a schedule key by
its form, its **type** (`tasks.ScheduleConfig`, not `duration` or a cron string) and its registration
through `ScheduleFromConfig`. A former interval or cron key (`core.backup.schedule`,
`core.ai.batch_poll_interval`, …) becomes an alias of the new key with a `Convert` (05-config §1.3.6)
from the old value: a duration `d` → `{enabled: d > 0, every: d}`, so "0 disables" becomes
`enabled: false`. The 38 `scheduled.<task>` entries become `modules.books.<subsystem>.schedules.<name>`
keys with aliases for the compatibility window ([09](09-abo-on-core.md) §9.2.1). Non-timing tuning
keys stay where they are. Spec CI carries a doc-lint: every `ScheduleFromConfig("…")` literal in
`docs/specs/platform-core/*.md` must match this form and name a key that the same file's config table
declares with type `tasks.ScheduleConfig`, so a schedule key that would fail registration fails
review first (SIMP2-1).

**What is not a task.** The rule governs **durable work**: durable work at a cadence of one minute or
more is a task. In-process ticks shorter than `core.tasks.schedule_poll` (1 m) are not tasks; they are
`Registrar.Worker`s or kernel `lifecycle.Component`s. Library-internal timers that schedule no work of
their own (the OTel periodic reader, `core.metrics.otlp.interval` 60 s, and the batch span processor,
[05-metrics](05-metrics.md)) are not scheduled work and are not listed (SIMP3-9). The ticks below are
listed so the boundary is explicit, and a new one is added to this list (with its reason when it is not
sub-minute) or it is a review finding: health probes and the kernel heartbeat ([05-health](05-health.md) 5H.4); the
search-index reconcile loop ([05-storage](05-storage.md)); `providerhttp` limiter housekeeping
([05-metadata](05-metadata.md)); and, the one exception above a minute, [05-library](05-library.md)'s
15-minute watcher fallback poll (reason: it runs only while the platform watcher is unavailable, is
per root and holds no durable work, so a missed tick loses nothing; SIMP2-1). The outbox intake scan
is **not** on this list: it runs inside the relay's task run ([05-events](05-events.md) 5E.5.3), whose
heartbeat it is (COR2-5).

**What replaces v2 cron strings.** They are dead metadata (5T.2). Core has no string-cron field on a
definition; a cron string exists only as an operator-set `ScheduleConfig.Cron`, which is evaluated.
Migration (5T.8 batch T7) converts each of the 15 declared crons into **no schedule** and records it
in the migration ledger, because none of them ever ran unattended: turning any on is a behaviour
change the owner decides per def (open item 1). The 38 `TaskDefinition`s become
`ScheduleFromConfig` on their target def, with today's interval and enable flag as the key's
default; `RunOnStart` → `.OnStart()`; `IsEnabled` → the key's `enabled` (or `.When` for gates that
are not schedule settings); window membership (`maintenanceOrder`, `internal/scheduler/scheduler.go:197-246`)
→ `Window: "nightly", Order: idx`. `transcode` (enqueues nothing) is deleted; `batch_poller` becomes a
schedule on an AI-owned task ([05-ai](05-ai.md)).

**Durable clock.** One clock per scheduled kind, `tasks.Clock{LastDue, LastFire, LastRun, Missed,
GateClosedAt, ScheduleHash}`, persisted only through `ClockStore` (5T.3.13) under the kind's
`StoredKind`. The native store keeps it in `c/tasks/sched/<kind>` (5T.6.1). Books' legacy store keeps a
kind's clock where HEAD keeps it, in HEAD's encoding, and the fields that encoding lacks in its
extension family (5T.6.2); when an older build advanced that clock while rolled back, the extension
reads stale (`ExtStale`) and its fields take their defaults (`Missed` 0, `GateClosedAt` zero; an unknown
`ScheduleHash` re-derives the next instant from `LastDue` without firing) until the scheduler's next
`Advance` re-stamps it. One scheduler goroutine
polls every `core.tasks.schedule_poll` (1 m, current `intervalPollInterval`,
`internal/scheduler/interval_clock.go:52-56`). A due fire is **one storage batch**:
`ClockStore.Advance` (`LastDue` advanced, checked at commit against the clock the poll read) +
`RunStore.Insert` of the queued run. A crash between clock advance and enqueue therefore cannot lose or
duplicate a fire (today the interval clock is a settings write separate from enqueue,
`interval_clock.go:46-60`); both stores stage into the one batch of the one Pebble store (participant
binding, 05-storage §5S.3.2). The clock advances on every due
check whether or not the gate let it enqueue (current semantics, `interval_clock.go:36-45`).
First sight of a schedule seeds `last_due = now` and does not fire (no boot stampede,
`interval_clock.go:57-63`). `schedule_hash` changes when the effective `ScheduleConfig` changes; a
changed `Every` keeps `last_due`. A schedule whose def already has an active run of the same mode
folds into it (current `EnqueueOp` rule for scheduled defs, `registry.go:1055-1060`). DST: `DailyAt`
and `Cron` compute the next instant in the zone; a skipped wall time fires at the first instant
after, a repeated one fires once. A held kind's clock (5T.3.11) is not polled; on re-enable its
`Missed` policy applies.

**Rollback (OPS-7, DATA-6).** With books' legacy store the clock a build advances is the clock its
predecessor reads, so a rollback neither fires every due schedule at once on stale clocks nor
double-runs one after the roll-forward, with no mirror and no boot-time adoption: each build continues
from the last `LastDue` the other committed. Pebble admits one process at a time, so two builds never
advance a clock concurrently. Until T7 the 38 `TaskDefinition`s are still ticked by ABO's scheduler,
never by core's; from T0+T1 to T7 nothing else schedules them, so that ticker must keep running
through the whole interval (5T.6.2, `TestOneSchedulerPerKind`, DATA5-8).

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

// RunFilter is what a run listing filters on besides the kind. The zero value
// lists every run, newest first (Created descending, then RunID).
type RunFilter struct {
	States []RunState // empty = every state
	Parent RunID      // non-empty = only the children of this run
	Since  time.Time  // Created >= Since; zero = no bound
	After  RunID      // keyset cursor: continue after this run in listing order
	Limit  int        // 0 = 100; values above 1000 are clamped to 1000
}

// RunQuery is Client.Runs' query (and GET /runs'): Kinds are registered
// Kinds, former kinds accepted and resolved by the executor; empty = every
// kind the caller may view.
type RunQuery struct {
	Kinds []Kind
	RunFilter
}

// ListQuery is RunStore.List's query: the executor rewrites a RunQuery's
// Kinds to the StoredKinds they are stored under (every alias and former
// kind included), so an implementation never resolves aliases (StoredKind,
// 5T.3.13; SIMP6-7).
type ListQuery struct {
	Kinds []StoredKind
	RunFilter
}

type StartRequest struct {
	Mode           ModeRequest     // Unset | Preview | Live
	Params         json.RawMessage
	Priority       Priority
	PlanRef        RunID  // Fixer / Approval.PlanRequired
	Parent         RunID
	Actor          Actor  // on Client (module handle) overwritten by the kernel, see below
	Trigger        Trigger
	IdempotencyKey string // dedupe window core.tasks.idempotency_ttl. Only the HTTP start route sets it
	                      // (copying the Idempotency-Key header, SIMP2-8); the outbox relay leaves it
	                      // empty, because its same-batch cursor commit is the guarantee (5T.3.12, SIMP3-8)
}

// ErrModeUnset: a kernel-internal start (StartIn, outbox Tx.StartRun) with
// Mode unset. Only the HTTP route and Client.Start apply the rule-2 default
// (5T.3.7); automation always states its mode (COR2-5).
var ErrModeUnset = errors.New("tasks: start mode not stated")

// RunView is the read model of one run: what Client.Get/Runs return, what
// GET /runs[/{id}] serialise, and the payload of core.task.run-changed.v1.
type RunView struct {
	ID       RunID
	Kind     Kind
	Mode     Mode
	State    RunState
	Held     bool   // computed at read time, never stored (5T.3.11)
	Reason   string // fixed token for interrupted_*/failed/timed_out/canceled states ("" otherwise)
	Actor    Actor  // the stamped actor
	Trigger  Trigger
	Parent   RunID
	PlanRef  RunID
	Created  time.Time
	Started  time.Time // zero until first dispatch
	Finished time.Time // zero until terminal
	Done     int    // completed items (Batch/Fixer/RunItems; Task: last reported)
	Total    int    // 0 = unknown
	Current  string // current item label; may name a user, so blanked by RedactRunView (below)
}

// RedactRunView is the ONE redaction of a run's user-identifying text (COR2-2).
// It blanks Current (and any free error text a later version adds) unless who
// holds core:users.view, the canonical successor of HEAD's users.manage gate on
// labels and error text (opsv3/events.go:609-616, view.go:59-64): 05-rbac maps
// users.manage to core:users.manage, which implies core:users.view, and
// core:users.view is the scope that already gates reading user identities
// (05-authn GET /users). tasks sets it as the Redact function of the
// core.task.run-changed.v1 catalog entry (05-events 5E.3.1), so every realtime
// surface (native stream and legacy projections) applies it, and the
// GET /runs[/{id}] serialiser calls the same function. No module redactor exists.
// A nil who blanks unconditionally (the S5 legacy-stream bridge, 5T.4 step 3).
func RedactRunView(who rbac.Checker, v RunView) RunView

// RunViewRedactor adapts RedactRunView to events.RedactFunc (decode the
// payload as RunView, redact, re-encode; an undecodable payload is returned
// blanked to "{}"). It is the Redact value of the core.task.run-changed.v1
// catalog entry (05-events 5E.3.1; COR3-13).
func RunViewRedactor() events.RedactFunc

// RunState is the state of a run as the executor and the API see it.
// Terminal: succeeded, failed, timed_out, canceled, interrupted_dropped.
// "held" is not a state (5T.3.11). NativeStores stores these values; books'
// legacy store keeps HEAD's stored vocabulary (internal/operations/state,
// which stays in ABO) and maps each RunState to it (5T.6.2 "Run states";
// R6-2), so a RunState value is never assumed to be a stored string.
type RunState string

const (
	StateQueued              RunState = "queued"
	StateWaitingDeps         RunState = "waiting_deps"         // Requires / NotWhile not yet satisfied
	StateRunning             RunState = "running"
	StateStopping            RunState = "stopping"             // Reason: cancel | quiesce | shutdown
	StateInterrupted         RunState = "interrupted"          // resume policy decides at next boot (5T.3.6)
	StateInterruptedQuiesced RunState = "interrupted_quiesced" // stood down; re-queued when the hold is released (5T.3.7)
	StateAwaitingDecision    RunState = "awaiting_decision"    // ResumeAsk
	StateSucceeded           RunState = "succeeded"
	StateFailed              RunState = "failed"
	StateTimedOut            RunState = "timed_out"
	StateCanceled            RunState = "canceled"
	StateInterruptedDropped  RunState = "interrupted_dropped"  // ResumeDrop, or Decide(Drop)
)

func (s RunState) Terminal() bool

// Actor stamping (COR-21). On Client.Start — the module handle,
// module.Deps.Tasks — the kernel overwrites req.Actor with the principal in
// ctx (a request) or, outside a request, with the calling module's system
// actor; a module therefore cannot start a run as another actor, and
// Perms.Start is checked against the stamped actor. Only kernel-internal
// callers (Executor.Start/StartIn: the HTTP route, the scheduler, the outbox
// relay) set Actor themselves.
//
// The module system actor (SEC2-15) may start exactly the module's own kinds
// plus the kinds listed in its Manifest.TaskStarts []tasks.Kind (06; a
// reviewed list of foreign and core kinds, each validated at P7 against the
// registered kinds plus every manifest's TaskLedger, 5T.3.1; COR3-10).
// Its permission set is the union of Perms.Start of those kinds, minus
// core:admin and every AuthRequired scope, which it never holds. A start of
// any other kind, or of a listed kind whose Perms.Start needs an excluded
// scope, is refused with 403 tasks.actor_not_permitted.

// Stager is the kernel-provided handle a core service gets when it must start
// a module kind built for it (05-ai's batch poll starting
// <module>.ai.batch-apply.<name>, acquire starting <module>.acquire.import;
// COR2-11, 5T.3.12). It is the ONE name for that starter in every file
// (COR3-8); Executor.StartIn is the relay's only. StartIn stages
// the run into b (the service's own batch) and the kernel stamps req.Actor with
// the system actor of the module that owns kind (SEC2-15 permission set), never
// a core or caller identity; req.Mode must be set (ErrModeUnset). Core services
// hold a Stager, never the Executor.
type Stager interface {
	StartIn(ctx context.Context, b *storage.Batch, kind Kind, req StartRequest) (RunRef, error)
}

// Executor is kernel-only (core/kernel holds it; apps see Client).
type Executor interface {
	Client
	Cancel(ctx context.Context, id RunID, by Actor) error
	Approve(ctx context.Context, id RunID, a ApproveRequest) error
	Decide(ctx context.Context, id RunID, d Decision, by Actor) error // Resume | Finish | Drop
	// StartIn stages a run into b (kernel-internal). The outbox relay uses it to
	// enqueue an outbox-triggered run in the same batch as its cursor advance (5T.3.12).
	StartIn(ctx context.Context, b *storage.Batch, kind Kind, req StartRequest) (RunRef, error)
	Pause(reason string, by Actor) error
	Unpause(by Actor) error
	// Lifecycle. Not named Start: Client.Start(ctx, kind, req) is already a
	// method of the embedded interface, and Go refuses the duplicate (COR3-13).
	Run(ctx context.Context) error      // resume sweep, dispatcher, workers (5T.4 step 2)
	Shutdown(ctx context.Context) error // bounded drain, 5T.4
}
```

`/api/core/v1/tasks` (`httpapi`, [05-http-api](05-http-api.md)); every route has a permission (R6):

| Route | Replaces | Permission |
|---|---|---|
| `GET /definitions`, `GET /definitions/{kind}` | `GET /api/v3/ops/defs[/:id]`, `/api/v1/op-defs` | `AnyAuthenticated` + per-def `View` filter |
| `POST /definitions/{kind}/runs` | `POST /api/v3/ops/defs/:id/runs`, `POST /api/v3/ops/runs`, `POST /api/v1/operations/v2` | per-def `Start`. An `Idempotency-Key` header is copied into `StartRequest.IdempotencyKey`; a duplicate within `core.tasks.idempotency_ttl` returns `200` with the existing `RunView` and `Idempotent-Replayed: true` (the only idempotency store on this route; httpapi keeps none, SIMP2-8) |
| `GET /runs`, `GET /runs/{id}`, `/runs/{id}/rows`, `/runs/{id}/items`, `/runs/{id}/log` | v3 `runs`, v1 `operations/v2/:id` | per-def `View`; each `RunView` passes `RedactRunView` |
| `POST /runs/{id}/cancel` | v3 cancel, `DELETE /api/v1/operations/v2/:id` | actor or `Cancel` |
| `POST /runs/{id}/approve` | v3 approve | `Approve` + verified method |
| `POST /runs/{id}/decide` `{resume\|finish\|drop}` | v1 retry/finish routes (`retry.go:70`, `finish.go:73`; `raw/08` §7 finish-without-resuming) | `Approve`/`Start` + verified method |
| `GET /counts`, `GET /timeline` | v3 counts/timeline | `core:tasks.view` |
| `GET /schedules`, `PATCH /schedules/{kind}` | `/api/v1/tasks`, `PUT /tasks/:name`, `/maintenance-window/*` | view / `core:tasks.manage`; PATCH writes the kind's `ScheduleConfig` key through `config` (DB override layer; a key locked by an operator-set layer is refused with 409) |
| `POST /pause`, `POST /unpause` | `/api/v1/operations/pause|resume` | `core:tasks.manage` |
| (no tasks route: events) | `GET /api/v3/ops/events`, `/api/v1/operations/events` | not routes of `/api/core/v1/tasks` (FE2-18): the bus topics `core.task.run-changed.v1` and `core.task.log-appended.v1` are bridged one-to-one to realtime streams ([05-events](05-events.md) 5E.3.2, [05-realtime](05-realtime.md)), which serve the legacy paths; topic permission `AnyAuthenticated`, per-envelope visibility = the definition's `View` (COR-20), payload through `RedactRunView` |

Compat: `/api/v3/ops/*` and `/api/v1/operations*`/`/op-defs`/`/tasks` are aliases onto the same
handlers with a `Deprecation` header and log line for the compatibility window, defined once in
[09](09-abo-on-core.md) §9.2.1 and implemented by `compat` (SIMP-7). Alias handlers translate legacy
def IDs through `FormerKinds`/`LegacyDefID`, and render the legacy response body through 05-http-api's
`LegacyPath.Shape` (there is no `WithLegacyShape`; FE3-6).

### 5T.3.11 Held runs (disabled modules)

A disabled module registers no kinds ([06](06-app-sdk.md) §6.5.3), but its runs, clocks and ledger
lines persist. **`held` is a computed classification, never a stored state** (COR-13): a run is held
when `Kind.Module()` names a module whose manifest is in the build but disabled, or when its stored def
ID is a `FormerKinds` or `LegacyDefID` entry of such a module — known from the module's last definition snapshots
(`c/tasks/def/<kind>`) or from the task-ID ledger its manifest carries (06 `Manifest.TaskLedger`).

| Where | Behaviour |
|---|---|
| Dispatcher | never claims a held run; the run keeps its queue position |
| Resume sweep | skips held runs; their stored state (`queued`, `interrupted_*`, `awaiting_decision`) is untouched |
| Scheduler | does not poll a held kind's clock; on re-enable the kind's `Missed` policy applies (at most one coalesced fire) |
| Op-ID ledger gate (5T.4 step 1) | a ledger line or stored kind that resolves to a disabled module is **resolved** (held), never fatal |
| API | `GET /runs` shows `state: "held"` with the stored state alongside; cancel, approve and decide are refused with `409 tasks.held` (read-only) |
| Re-enable | the next boot registers the kinds; queued runs dispatch and interrupted runs follow their `ResumePolicy`, exactly as after a restart |
| Module removed from the build (no manifest) | not held: its kinds are unknown (hidden from census, counted, WARN; 5T.5), and the release that removes a module must tombstone its ledger lines |

`coretest.AssertModuleInert` (06) includes a disabled module with queued, interrupted and
`awaiting_decision` runs and a scheduled kind, and asserts boot succeeds and none of them moves.

### 5T.3.12 Outbox-triggered runs

A module never consumes outbox messages inside the relay's run (COR-9). Its only consumer forms are
`outbox.ConsumeIntoTask` and `outbox.ConsumeIntoTaskRaw` ([05-events](05-events.md) 5E.4): the relay
turns each message into a run of a kind **owned by the consuming module**, with
`Trigger: Outbox`, and stages that run with `Executor.StartIn` in the same batch as the consumer's
cursor advance, so the hand-off is effectively once. The relay sets no `StartRequest.IdempotencyKey`
and writes no start-idempotency record: a seq is never reissued (05-events 5E.5.2), so a
`"<consumer>:<topic>:<seq>"` key could never repeat and the record would guard nothing on a
possibly library-scale path (SIMP3-8). From then on the work passes every gate a
manual run passes: `Effects` and the write set (G8), the dispatch conflict rule and ref locks, the
fence, cancel, progress and the watchdog. Dispatch is FIFO within a priority, so runs start in
message order per partition; completion order across runs of one key is guaranteed only when the
kind's `ConcurrencyKey` or `SubjectsFunc` serialises them. A failed run is the run's outcome (its
`ResumePolicy`, its history), not a redelivery. The run's mode is the one its consumer declares
(`outbox.Live()`/`outbox.Preview()`, 5T.3.7 rule 5), and a consumer's `outbox.Where` filter acks
unmatched messages without starting anything (COR2-10), so a consumer never creates runs for other
owners' messages.

**Module writes run only under module kinds (COR2-11).** A core run never invokes a module callback
that writes module entities: the core kind cannot declare the module's resources (5T.3.1), so its
writes would either be refused (`ErrOutsideEffects`) or bypass G8. Where core logic must trigger
module writes (acquire's import of a completed download, an AI batch result, a derived-index build,
and, in the deferred build below, a module's `Online` data migration), the module side is a **module** kind built by a core helper that
carries the module's own `Effects` and `Subjects`: `acquire.ImportTask`, `ai.BatchApplyTask`
(`<module>.ai.batch-apply.<name>`), `datamigrate.DerivedBuildTask` (package `storage/datamigrate`, L4, because `storage` (L1) cannot name a
`tasks` type; `<module>.derived.<name>.build`, handed to `Registrar.DerivedIndex(name, hooks, build)` —
`storage.DerivedHooks` carries only `Snapshot` and `Load`, no build; with `Name` module-relative, a `Title`, `Effects`, `Perms.Start`, a `Timeout` defaulting to 24 h and the
rebuild's `Parallelism`; [05-storage](05-storage.md) §5S.3.9, COR3-7), and (deferred) the kernel-registered
`<module>.datamigrate.<name>` below. Core starts it: from a core outbox consumer with `outbox.Tx.StartRun`
in the relay batch (05-events 5E.4), or from a core service through the kernel-provided `tasks.Stager`
(5T.3.10), which stamps the owning module's system actor. The
dispatch conflict rule, ref locks and fence then apply as for any module run, without exception in
the extraction builds (the attribution-only exemption below is deferred).

**Kernel-registered data-migration kinds (COR4-5, COR4-6) — deferred build (SIMP5-7).** No stage
S0–S10 declares an `Online` data migration: R3-1 removed the adopt and fold copies it was designed for,
and the named later consumers (books' storage-efficiency release B, a per-family cutover) raise a
level and so are `BootBlocking`. The `Online` mode therefore lands with its **first consumer**, listed
in 05-storage §5S.3.14's "Deferred-build members" together with everything this paragraph and the
next specify: the kernel-registered `<module>.datamigrate.<name>` and `core.storage.migrate` kinds, the
synthesised definition below, the dispatch write-set exemption (an unexported kernel flag, so no
identifier of `tasks` is deferred) and the two tests named below. No extraction build registers these
kinds or carries the exemption; `BootBlocking` data migrations (5T.4 step 0) and
`datamigrate.DerivedBuildTask` are built and unaffected. The design is kept at proof depth so it lands
additively. When built: for every `Online` data migration
(05-storage §5S.3.6) the kernel registers one definition: `<module>.datamigrate.<name>` for a module
owner, `core.storage.migrate` for a core owner. `<name>` must match the task-kind segment grammar
`[a-z][a-z0-9-]*` (registration fails otherwise, so the kind always passes `ParseKind`). The
synthesised definition is fixed here:

| Field | Value |
|---|---|
| Shape | Task |
| `Title` | the migration's `Description` |
| `Effects` | `Writes(...)` of the migration's declared `Writes`, for attribution only (G8 refusal of other writes, audit, UI): an unexported kernel flag exempts the kind from the dispatch write-set conflict rule, and no per-shard `Subjects` are derived (a shard is a key range, not an entity) |
| `ConcurrencyKey` | `<module>.datamigrate` (`core.storage.migrate` for core), so two migrations of one owner never overlap |
| `Resume` | `ResumeContinue`: the persisted shard plan and cursors (`c/storage/dm/<id>`) are the checkpoint |
| `Timeout` | 24 h; the runner checks `rc.ShouldYield()` at every chunk boundary and returns `ErrContinueInPlace` after persisting its cursors, so a multi-day rewrite never ends `timed_out` |
| `Perms.Start` | `core:system.manage` (manual retry only; never in a module system actor's set, 5T.3.10) |
| Parallelism | `DataMigration.Parallelism` |
| `TaskLedger` | implicitly in the owner's ledger, and kept there after the migration is removed, so held and history classification keep working (5T.3.11) |

Owner writes are protected by storage, not by dispatch: 05-storage accepts an `Online` migration only
over families its owner declares `Serialized`, and every step write commits with a `GetForUpdate`/
`Expect` precondition against the bytes the step read (or their absence); a lost precondition re-reads
the item and retries. So no writer, task run or not (a gin handler, a hook, a worker), is ever
overwritten by the rewrite, and the rewrite blocks no module task for its duration. Both properties
are asserted by 05-storage's tests, the owner's names, run over a test module fixture rather than
`hello` (which declares no data migration and no `Serialized` family): `TestModuleOnlineMigrationRunsAsModuleKind`
(a concurrent HTTP-path write to a row under the rewrite survives; a concurrent module task writer of
the same subject is not blocked; two migrations of one owner never overlap) and
`TestOnlineMigrationYieldsPastTimeout` (COR5-17); both are deferred with the mode.

### 5T.3.13 Stores (R4-1)

```go
// Stores is everything the executor and the scheduler persist. The kernel
// builds it once at P4c (06 §6.5.1): from legacy.Stores.Tasks when a module
// supplies it (ABO: books code, 5T.6.2; the constructor is described below),
// else NativeStores over c/tasks/ (5T.6.1). Core code
// reaches runs, clocks, the pause marker and stand-down markers only through
// these interfaces and never names a stored key.
type Stores struct {
	Runs      RunStore
	Clocks    ClockStore
	Pause     PauseStore
	StandDown StandDownStore // MOD6-4
}

// NativeStores is core's implementation over the c/tasks/ keyspace (5T.6.1).
func NativeStores(ks storage.Keyspace) Stores

// StoredKind is the def ID a run, clock or stamp is stored under: the kind's
// LegacyDefID when the stores are legacy and it is set, else the Kind. The
// executor resolves it; implementations never resolve aliases.
type StoredKind string

// RunRecord is the stored form of a run: RunView plus what only the executor
// reads. An implementation persists every field; a field its primary row has
// no place for goes to its extension record (5T.6.2), whose zero-value default
// names no access and starts no work.
type RunRecord struct {
	RunView
	StoredKind StoredKind
	Priority   Priority
	Params     json.RawMessage
	Result     json.RawMessage
	Error      string // fixed token plus redacted text
	Attempt    int
	FenceEpoch uint64
	Yields     int
	Seq        RunSeqs // per-run numbering of events, intents and log lines (DATA-19)
}

type RunSeqs struct{ Event, Intent, Log uint64 }

// Expect is the compare-and-set precondition of a transition (5T.7).
type Expect struct {
	State      RunState
	Attempt    int
	FenceEpoch uint64
}

// RunStore persists runs. Every write method that takes b STAGES into the
// caller's *storage.Batch: the implementation binds its own handle to b
// (participant binding, 05-storage §5S.3.2), so one commit carries a run row
// together with a clock advance, an outbox cursor (05-events 5E.4), an
// idempotency record or a notification (OnTerminal, 5T.4). The one exception
// is Prune (and the optional legacy.TasksRecoverer below): maintenance that
// commits its own chunked batches and never joins a multi-owner commit
// (COR5-12).
//
// EVERY write is checked at commit (R6-1, DATA6-1, R7-3), never by a lock
// held between calls, and the check is per GUARD ROW, not per written key.
// A run's guard rows are the keys its precondition names: native
// c/tasks/run/<id>; books the opv3:run: record AND the paired opv2:op: row,
// plus the global census row opv3:meta:counts in an Insert or Transition
// batch (5T.6.2). Insert stages Expect(absent) on the run; every other method
// that stages a write derived from a run it read — Transition, Progress,
// SaveCheckpoint, SaveChunk, PutRow, MarkRow, Strike, AppendEvent,
// AppendIntent, PutItem, AppendLog — stages GetForUpdate on that run's guard
// rows, and Debounce stages GetForUpdate on its bucket. Every other key a
// method writes (queue, indexes, pages, checkpoints, ledger, rows, marks,
// strikes) is legal only in a batch that carries the run's guard Expect; it
// carries no Expect of its own. So a write whose run changed since it was
// read fails the whole batch and is never re-staged over the old read. In
// particular a progress tick cannot revert a cancel, and a chunk-ledger
// commit cannot land on a run that went terminal (HEAD's closed-ledger rule).
// Insert and Transition return ErrRunConflict at staging when their
// GetForUpdate read no longer matches want; they never commit, so they never
// retry. The caller that builds the batch re-runs build-and-commit under
// storage.RetryOnConflict (5T.7 names those callers), which re-stages every
// participant (COR8-7); every other write follows 5T.7's drop rule. An implementation may therefore keep runs only in families that are
// Serialized: for the native store the guard keys of 5T.6.1, and for a
// legacy store every family it writes or annotates with an extension record,
// declared Serialized in the claims table (05-storage §5S.9.1; R5-4).
type RunStore interface {
	// Runs, queue, census, history.
	NewID(ctx context.Context) (RunID, error)
	Insert(ctx context.Context, b *storage.Batch, r RunRecord) error // record + queue entry + indexes (+ books' census row)
	Get(ctx context.Context, id RunID) (RunRecord, error)            // ErrRunNotFound
	// Transition: ErrRunConflict at staging when its read no longer matches
	// want. A commit conflict while want still matches is retried by the
	// caller that builds the batch, which re-stages the whole batch (5T.7,
	// R7-3, COR8-7).
	Transition(ctx context.Context, b *storage.Batch, id RunID, want Expect, next RunRecord) error
	Progress(ctx context.Context, b *storage.Batch, id RunID, done, total int, current string) error
	Queued(ctx context.Context) iter.Seq2[RunRecord, error]     // dispatch order: priority, then enqueue time
	Unfinished(ctx context.Context) iter.Seq2[RunRecord, error] // the resume sweep's input
	List(ctx context.Context, q ListQuery) iter.Seq2[RunRecord, error] // includes terminal history, in ListQuery order
	Counts(ctx context.Context) (map[RunState]int64, error) // native: derived from the state index (5T.6.1)

	// Per-run pages: transition trail, Preview intents, item outcomes, log.
	AppendEvent(ctx context.Context, b *storage.Batch, id RunID, seq uint64, e RunEvent) error
	AppendIntent(ctx context.Context, b *storage.Batch, id RunID, seq uint64, w Write, c []Change) error
	PutItem(ctx context.Context, b *storage.Batch, id RunID, key string, o ItemOutcome) error
	AppendLog(ctx context.Context, b *storage.Batch, id RunID, seq uint64, l LogLine) error
	Pages(ctx context.Context, id RunID, q PageQuery) iter.Seq2[PageEntry, error]

	// Resume state: checkpoints, the Batch/Fixer chunk ledger and item-key
	// snapshot, Fixer rows and their applied marks.
	SaveCheckpoint(ctx context.Context, b *storage.Batch, id RunID, name string, v []byte) error
	Checkpoint(ctx context.Context, id RunID, name string) ([]byte, bool, error)
	SaveChunk(ctx context.Context, b *storage.Batch, id RunID, c Chunk) error
	Chunks(ctx context.Context, id RunID) iter.Seq2[Chunk, error]
	PutRow(ctx context.Context, b *storage.Batch, id RunID, r Row) error
	Rows(ctx context.Context, id RunID) iter.Seq2[Row, error]
	MarkRow(ctx context.Context, b *storage.Batch, id RunID, rowID string) error

	// Debounce buckets and watchdog strikes.
	Debounce(ctx context.Context, b *storage.Batch, d DebounceBucket) error
	DebounceBuckets(ctx context.Context) iter.Seq2[DebounceBucket, error]
	Strike(ctx context.Context, b *storage.Batch, id RunID, s Strike) error

	// Requires stamps: when a kind last completed for a subject.
	Stamp(ctx context.Context, b *storage.Batch, subject entity.Ref, k StoredKind, at time.Time) error
	Stamped(ctx context.Context, subject entity.Ref, k StoredKind) (time.Time, bool, error)

	// Maintenance: commits its own batches (above); each delete stages an
	// Expect on the terminal row it read, so a row is deleted only as read.
	Prune(ctx context.Context, before time.Time, limit int) (deleted int, err error) // terminal runs only, chunked
}

// Clock is one scheduled kind's durable clock (5T.3.8).
type Clock struct {
	LastDue, LastFire, LastRun time.Time
	Missed                     int
	GateClosedAt               time.Time
	ScheduleHash               string
}

type ClockStore interface {
	Get(ctx context.Context, k StoredKind) (Clock, bool, error)
	// Advance stages next if the stored clock still equals prev (absent when
	// had is false), checked at commit; otherwise ErrClockConflict.
	Advance(ctx context.Context, b *storage.Batch, k StoredKind, prev Clock, had bool, next Clock) error
}

// Pause is the executor's operator pause marker (Executor.Pause/Unpause).
type Pause struct {
	Reason string
	By     Actor
	At     time.Time
}

type PauseStore interface {
	Get(ctx context.Context) (Pause, bool, error)
	Set(ctx context.Context, b *storage.Batch, p *Pause) error // nil clears
}

// StandDown is the persisted marker of one live stand-down hold (5T.3.7). It
// exists only so that the next boot can tell that a holder died mid-work:
// the resume sweep then leaves the runs it quiesced interrupted_quiesced for
// that boot (HEAD's boot consult, internal/operations/registry/resume.go:45-60).
type StandDown struct {
	Key      Key
	Holder   RunID
	Quiesced []RunID
	Expires  time.Time // lease expiry when last staged
}

// StandDownStore persists stand-down markers (MOD6-4).
type StandDownStore interface {
	List(ctx context.Context) iter.Seq2[StandDown, error]
	Set(ctx context.Context, b *storage.Batch, key Key, s *StandDown) error // nil clears
}

var (
	ErrRunNotFound   = errors.New("tasks: run not found")
	ErrRunConflict   = errors.New("tasks: run changed since read")
	ErrClockConflict = errors.New("tasks: schedule clock changed since read")
)
```

`RunEvent`, `ItemOutcome`, `LogLine`, `PageQuery`/`PageEntry`, `Chunk`, `DebounceBucket` and `Strike`
are plain data structs with the fields of HEAD's v3 records they replace
(`internal/database/iface_ops_v3.go`), minus ABO types; `Row` is 5T.3.1's. The interfaces are SDK and
frozen by the API-stability gate (03 §3.4), the trade R4-1 names. The invariants live **above** them,
in the executor, so every implementation gets them: one row per run, transitions only by `Transition`'s
compare-and-set, per-run sequence numbers advanced by the fenced owner in the row's batch, retention
never touching a non-terminal run. `taskstest.StoresSuite(t, func(*testing.T) tasks.Stores)` (the
05-storage §5S.3.11 naming rule `<pkg>test.<Interface>Suite`, SIMP5-9) asserts them and the
commit-time preconditions; core runs it over `NativeStores`, and ABO runs it over its legacy store with
HEAD fixtures (5T.9). The suite covers only the frozen contract, including the commit-time check on
every write (`TestLegacyProgressVsCancelKeepsState`, `TestLedgerCommitAfterTerminalRefused`; R6-1), the
retry of a transition whose `want` still holds (`TestConcurrentTransitionsOfDistinctRunsAllCommit`,
`TestCancelCommitsUnderProgressTicks`) and the closed-ledger drop rule (`TestFixerMarksLandDuringQuiesce`;
R7-3).
`tasks.Stores` carries no legacy-only member: every method above has a meaning for the native store
(SIMP5-4).

**Legacy-only identifiers live in package `legacy` (R6-3, SIMP6-2, SIMP6-6).** What exists only to
serve an app's pre-core run store is not in `tasks`: it is in core's one legacy package (06 §6.4.11),
which the API-stability gate excludes as it excludes `kernel` (12 §12.4.2), so deleting it at the tasks
cutover is not a breaking change. The constructor is the field `legacy.Stores.Tasks`, of type
`func(legacy.Handles) (tasks.Stores, error)` (06 §6.4.11 owns it): the kernel calls it once at P4c with
the supplying module's OWN handles (`legacy.Handles`: its legacy range over its own claims and its
extension family), as for every other legacy constructor, with no grant (R4-1); it is called also while
that module is disabled, so held runs stay readable (5T.3.11); the returned stores read and write only
inside those handles. The one optional capability is this file's:

```go
package legacy // github.com/falkcorp/mediacore/legacy: outside the API promise (R6-3)

// TasksRecoverer is an OPTIONAL capability of a legacy tasks.RunStore (it
// was tasks.Recoverer; former names: 05-storage §5S.3.11; SIMP7-8 names it after its
// owner package in Go initialism form): a store whose previous binaries can
// leave records inconsistent implements it, and Recover runs once per boot
// inside Executor.Run before the resume sweep (5T.4 step 2), commits its own
// batches and stages an Expect on every row it rewrites (R6-1), committing
// each chunk under storage.RetryOnConflict, which re-reads each row's guard
// and skips a row a live write has synced since detection (DATA8-8). It is
// detected once, by its owner: the executor, which cannot import this
// package (legacy imports tasks), asserts the store it is handed against an
// unexported interface with the identical method set (the kernel asserts
// only the capabilities 03 §3.3 lists; SIMP7-5, SIMP8-3). The mirror test (03 §3.3, R7-7) checks
// that the two are mutually assignable: internal/executor's export_test.go
// exports a test-only alias of the mirror, and legacy_mirror_test.go in
// package executor_test, which may import legacy, asserts both directions
// (COR7-2, SIMP7-1). The native store does not implement it. Because a
// failed assertion is silent, a store that needs it asserts it at compile
// time (books: var _ legacy.TasksRecoverer, 5T.6.2).
type TasksRecoverer interface {
	Recover(ctx context.Context) error
}
```

Its conformance case follows the one-legacy-suite-per-store-interface pattern (03 §3.3, SIMP6-6):
`legacytest.TasksRunStoreSuite(t, newStores, plantStale)` runs a case per capability the
implementation has; for `TasksRecoverer`, a record `plantStale` makes inconsistent is repaired by
`Recover` before `Unfinished` yields it, and a second `Recover` writes nothing. ABO runs it over books'
store with HEAD fixtures beside `taskstest.StoresSuite` (`TestLegacyRunStoreRecovers` is that case's ABO
instance).

## 5T.4 Lifecycle

The boot phases are defined once, in [06-app-sdk](06-app-sdk.md) §6.5.1 (R2-8), and the process-wide
start and stop order once, in 06 §6.5.2 (COR-8); this section only says what `tasks` does at its
phases and steps. Nothing in `tasks` writes before 06's pre-migration checkpoint.

0. **Boot phases P0–P4: no tasks writes.** `tasks` has no migration step, no adopt step and no
   post-DAG reconciler (the legacy store keeps HEAD's format, 5T.6.2). Books legacy steps 59–66, which
   write the ops families (`internal/database/migrations.go:479-506,1238-1250,1340-1352,1412-1424`),
   run in P4b with books' own handle on its own claims (05-storage §5S.3.4; no grant, R4-1); the
   executor first calls the stores at step 2, so those steps always run first and no `After` edge is
   needed. The stores are built at P4c (5T.3.13). BootBlocking data migrations of 05-storage run
   in-process through `tasks.ForEach` with persisted shard cursors in the P4 DAG (05-storage §5S.3.6
   owns `DataMigration`). `Online` data migrations, which would run as executor runs after step 2
   under the kernel-registered attribution-only kinds of 5T.3.12, are a deferred build (SIMP5-7): no
   extraction build has one.
1. **Register** (inside `app.New`, 06 P6–P7): kernel collects module definitions, validates (5T.3.1), upserts
   def snapshots `c/tasks/def/<kind>` (with `FormerKinds`, a core kind's including the aliases the app supplies through `app.Options.LegacyTaskAliases`, 5T.8.2), and runs the op-ID ledger gate (current
   `opCatalogGate`, `internal/startup/steps.go:110-111`): a ledger line that resolves to no def,
   alias, tombstone **or held module** (5T.3.11) fails boot.
2. **Start** (`Executor.Run`, the executor step of 06 §6.5.2: after module hooks and workers and after
   `Bus.Open`, so events from the resume sweep reach subscribers): `PauseStore.Get` →
   `Recover`, when `Stores.Runs` implements the optional `legacy.TasksRecoverer` (books' legacy store runs
   HEAD's every-boot `ReconcileOpsV3WithV2` there, `internal/database/store.go:1860-1870`, ABO code
   unchanged, writing only its own families before the resume sweep acts on them; the native store
   does not implement it). From S2b until this executor lands (S5) the same repair has another home:
   books' start hook runs `ReconcileOpsV3WithV2` before it builds HEAD's ops registry (05-storage
   §5S.9.3, COR5-7), and it moves to `Recover` in the T0+T1 PR → `StandDownStore.List`: a marker
   that survived the restart means a holder died mid-work, so the runs it names stay
   `interrupted_quiesced` for this boot, the marker is cleared and a WARN names the holder (HEAD's boot
   consult, `internal/operations/registry/resume.go:45-60`; a later marker-free boot resumes them;
   MOD6-4) → resume sweep over `RunStore.Unfinished` (5T.3.6; held runs skipped) → debounce reload → dispatcher (100 ms tick +
   wake), watchdog (30 s), N workers (`core.tasks.workers`, default 8). The scheduler goroutine starts
   in the next 06 step, and the outbox relay starts there from its schedule's `OnStart`. Dispatch waits
   for each def's `Ready` signals through `health.Registry.Wait`, the readiness core of
   [05-health](05-health.md) that lands **with `tasks` at S5** (COR4-8): storage reports
   `core.storage`, and books' warm-up reports `books.memdb` through `Registrar.Health` and
   `Deps.Health`, so from the first core-executor release a book-reading run is not dispatched before
   the memdb is warm (D44, `TestDispatch_WaitsForReadySignal`). Waiting runs hold no worker, key or
   clock (current warm-up rule, `pkg/ops/effects.go:44-57`).
3. **Run**: claim (CAS queued→running, fence epoch+1) → build `RC` → shape runner → terminal CAS.
   After each commit the executor publishes `core.task.run-changed.v1` (state or progress change,
   coalescable per run) and `core.task.log-appended.v1`; once per terminal transition it publishes
   `core.task.finished.v1` (05-events 5E.3.2). The bus publish produces no notification (05-notify
   §4: the only entry point is `Publish`, COR2-3): the executor calls a kernel-wired
   `Options.OnTerminal(ctx, b *storage.Batch, v RunView) error` inside the terminal CAS batch, and the
   kernel binds it to 05-notify's outbox-backed `Publish` for `core.task.finished.v1` (`tasks` does not
   import `notify`, which imports `outbox`, which imports `tasks`). **Legacy-stream bridge (FE2-3,
   COR3-9, FE3-12, SEC3-9):** the executor publishes nothing but core topics; core never calls ABO
   (R1). From T0+T1 until the realtime legacy projections land (09 S6), **books** registers a bus
   subscriber on `core.task.run-changed.v1` and `core.task.log-appended.v1` (`Registrar.Subscribe`,
   [05-realtime](05-realtime.md) §4.9) that feeds all three legacy streams: hub A's `Send*`
   (`operation.*`), `registry.EventHub` (stream B, `op.*` names) and the v3 broker's `run` events
   (stream C). It forwards **only** runs whose kind has a `LegacyDefID`, translated back to that def ID,
   so HEAD's per-event filters (stream B's `opEventFilter`, `internal/server/handlers/operations_v2.go:1084-1104`;
   stream C's re-check, `internal/server/handlers/opsv3/events.go:569-618`) see exactly what they see
   today; a kind without one (every new `core.*` kind, e.g. auth's prune tasks) is never forwarded.
   Before stream A it applies `RedactRunView(nil, v)`. S6 removes the bridge when 05-realtime's
   projections take over.
4. **Shutdown** (06 §6.5.2 order: scheduler stop, then executor drain; the slice comes from the one
   budget in [05-health](05-health.md) 5H.4): flip `shuttingDown` → release claimed-not-started →
   cancel every run with `ErrShutdown` (the relay run included: it acks nothing further) → wait up to
   `min(core.tasks.shutdown_drain, remaining budget)` (default **10 s**, current value
   `server_lifecycle.go:611`) → stamp the rest `interrupted` (resume policy decides next boot),
   revoke their fences (late writes refused with `ErrFenced`) → stop owned goroutines → return
   before storage closes. If an `Uninterruptible` run is in flight **and** the process runs under
   systemd with `NOTIFY_SOCKET` set, the executor asks once via `EXTEND_TIMEOUT_USEC`
   (`internal/sdnotify/sdnotify.go:144`) for at most `core.tasks.shutdown_extend_max` (60 s) beyond
   the budget; that extension is the only way past `core.shutdown.budget`. Docker and launchd offer
   no extension, so there the run is stamped and fenced when its slice ends. It never waits unbounded.

## 5T.5 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Invalid definition (any 5T.3.1 rule) | closed: `app.New` fails | a hole in the catalog makes runs vanish silently (current `opRegistrationGate` reason) |
| `Parallelism` zero in `RunItems`/`RunSeq`/`ForEach` | closed: returns `*ItemsError{First: ErrNoParallelism, FirstIndex: -1}` before any item | N8, R7 |
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
| Schedule CAS conflict (`ErrClockConflict`) | open: another poll won; no fire | exactly-once |
| Event publish failure after commit | open: run state is durable; UI may lag | bus is notify-only (05-events) |
| Shutdown drain timeout | open: remaining runs stamped interrupted; fences revoked | bounded stop |
| Legacy store's extension record whose fingerprint no longer matches its row (`ExtStale`: an older build rewrote the run row or clock while rolled back), or whose row is gone (`ExtOrphan`) | open: stale, each extension field (all Derived) at its stated default, counted; the next store write of that row re-stamps it and it reads `ExtCurrent` again; never `Suspended`, because no task field is Widening (05-storage §5S.3.11, DATA5-4); an orphan is ignored and deleted, by a conditional `legacy.Extension.Delete`, by the next write or `Prune` (5T.6.2, DATA7-8) | R4-1; no task extension field grants access or starts work |
| The store's recorded backend for `core/tasks` differs from this build's choice (a build without the app's legacy store on a legacy store, or the reverse) | closed: boot fails with `ErrLegacyStoreMissing` naming `core/tasks` (05-storage §5S.3.11) | an empty native keyspace must never stand in for the legacy runs (R4-1) |
| `Transition` or `Insert` batch fails with `storage.ErrConflict` (census row, a progress tick, another actor) | open: rebuilt and recommitted under `storage.RetryOnConflict` while a fresh read still matches `want`; `ErrRunConflict` only when it no longer does (the dispatcher's claim simply loses; of an executor completion and an HTTP cancel of one run, exactly one commits); attempts exhausted with `want` matching → `storage.ErrConflict`, transient (5T.7) | CAS transitions; unrelated writes must not fail a transition (R7-3, DATA7-2) |
| Any other `RunStore` write loses its precondition at commit (the run changed since it was read: a cancel, decide, watchdog stop or shutdown landed first) | open: never re-staged over the old read; on the fresh read a bare progress tick is dropped on any state change, and a chunk-ledger commit, checkpoint, Fixer row or mark, page append, item outcome or strike is re-staged while the run is non-terminal under the same attempt and fence (`stopping` included) and otherwise refused, its chunk redone on resume (5T.7) | a tick must never revert a cancel and nothing may land on a closed ledger, while a quiescing run keeps its marks (R6-1, R7-3, DATA6-1, DATA7-3; HEAD's closed-ledger rule, `pebble_store_ops_v3.go:1655-1666`, `pebble_store_ops_v3_rowmarks.go:54-56`) |
| A write or `DeleteRange` to one of books' guard rows (`opv3:run:`, `opv2:op:`, `opv3:meta:counts`) with no `Expect` on its key | closed: `ErrBlindWrite` (count-only in the first T0+T1 release, DATA6-12) | the guard rows are `CheckedWrites` from T0+T1; the other per-run families are `Serialized` and `Guarded` (no per-key `Expect`, no stripe) and written only under the guard `Expect` (5T.6.2, R7-3, DATA8-2) |
| Stand-down marker present at boot | open: the runs it names stay `interrupted_quiesced` this boot, the marker is cleared, WARN | the holder's work is half-applied; HEAD's boot consult (`resume.go:45-60`; MOD6-4) |
| A legacy family a store method stages a precondition on is not `Serialized` (`storage.ErrNotSerialized` from `Expect`) | closed: the store's conformance suite and `TestABOClaimsMatchOwnershipTable` fail in CI; at runtime the write fails, never degrades to a blind write | R5-4 |
| Cancel/approve/decide on a held run | closed: `409 tasks.held` | read-only while its module is disabled (5T.3.11) |
| Ledger line resolving to nothing (not held) | closed: boot fails | a rename without an alias strands stored rows (current `opCatalogGate`) |
| Stored run whose def ID resolves to no kind, alias, ledger line or held module (a newer build's kind after a rollback; a removed module) | open: the resume sweep sets a non-terminal one `interrupted_dropped` (reason `unknown_kind`), as HEAD does (`resume.go:86-96`); terminal ones are hidden from census, counted, WARN once | a rollback between core builds must not fail boot (5T.6 rule 3) |
| `ForEach` with `Watermark` | closed: `*ItemsError{First: ErrNoRunContext}` before any item | checkpointing needs a run |
| Lane capacity 0 with `NoLocalFallback` | waits (cancellable); never runs in-process | LaneMac rule |

## 5T.6 Persistence: one interface, two implementations (R4-1)

**One stored format per family, and no core code over ABO's.** The executor and the scheduler persist
only through `tasks.Stores` (5T.3.13). Which implementation serves an install is fixed per store: the
kernel records the choice for `core/tasks` in the claim ledger's `backends` entry, and a build whose
choice differs fails boot with `ErrLegacyStoreMissing` (05-storage §5S.3.11), because a build that lost
the legacy store would otherwise serve an empty native keyspace. The only legitimate change is a
cutover (below). There is no storage grant, no copy between implementations, no mirror, no P4d
reconcile (the one every-boot repair is HEAD's own, run by the legacy store's `Recover` at Run, 5T.4
step 2) and no tasks Teach build. Two `c/tasks/` families are core's whichever implementation serves
runs, because they describe core state rather than runs, and no older build reads them:

| Key | Content |
|---|---|
| `c/tasks/def/<kind>` | definition snapshots (with `FormerKinds`, `LegacyDefID`), for held runs (5T.3.11) |
| `c/tasks/idem/<kind>/<key>` | HTTP start idempotency records (TTL `core.tasks.idempotency_ttl`; SIMP2-8), **Serialized**: `Client.Start` stages the record in the batch of the run's `Insert` under `Expect(absent)`, so of two concurrent starts with one key exactly one commits, and the other's 5T.7 retry re-reads the record and replays the existing run (`200`, `Idempotent-Replayed: true`); expiry deletes a record with an `Expect` on the value it read (DATA8-9) |

### 5T.6.1 Native store (`c/tasks/`): apps without legacy runs

`tasks.NativeStores` is what new apps, `examples/hello` and `coretest` use (SIMP4-3). It lands with
`tasks` at S5: under 03 §3.4 rule (b) `hello` and `coretest` are core consumers, and `hello.count` runs
from S5 (06). Values are `codec.JSON[T](1)` of the core type.

| Key | Content |
|---|---|
| `c/tasks/run/<run_id>` | `RunRecord`, the run's guard key (Serialized: every `RunStore` write of the run checks it at commit, R6-1, R7-3) |
| `c/tasks/q/<priority>/<enqueued>/<run_id>` | dispatch queue entry, written only with its run row; `PrioritySystem` sorts first |
| `c/tasks/ix/kind/<kind>/<run_id>`, `c/tasks/ix/state/<state>/<run_id>`, `c/tasks/ix/done/<finished>/<run_id>` | `List`, the resume sweep and retention |
| `c/tasks/page/<run_id>/{evt,intent,item,log}/<seq or key>` | per-run pages |
| `c/tasks/ckpt/<run_id>/<name>`, `c/tasks/chunk/<run_id>/<n>`, `c/tasks/row/<run_id>/<row_id>`, `c/tasks/mark/<run_id>/<row_id>` | resume state, Fixer rows and marks |
| `c/tasks/debounce/<kind>/<bucket>` | debounce bucket (Serialized: `Debounce` checks it at commit) |
| `c/tasks/strike/<run_id>/<n>` | watchdog strikes |
| `c/tasks/sched/<kind>` | `Clock` (Serialized) |
| `c/tasks/pause` | `Pause` |
| `c/tasks/standdown/<key>` | `StandDown` marker (MOD6-4) |
| `c/tasks/req/<module>/<kind>/<id>/<taskkind>`, `c/tasks/req-rev/<taskkind>/<module>/<kind>/<id>` | `Requires` stamps and their reverse index |

The families marked Serialized are exactly the guard keys, those a method writes under a commit-time
precondition (the run row, which `Insert` expects absent and every other run write expects unchanged
since read, R6-1; the debounce bucket; the clock; and the core family `c/tasks/idem/` of 5T.6, which
`Client.Start` expects absent, DATA8-9), the native form of R5-4. In a core keyspace storage
checks a `Serialized` sub-prefix per key like a `CheckedWrites` claim (05-storage §5S.3.2), so no other
family is `Serialized`: the queue, indexes, pages, resume state, strikes and stamps carry no `Expect` of
their own and are written only in the batch that carries their run's guard `Expect` (R7-3). There is no
census row (DATA7-2): `Counts` counts the keys under each `c/tasks/ix/state/<state>/` prefix, which the
transition batch already rewrites and history retention bounds, so two runs' transitions never share a
key. `NewID` returns a ULID; the native run store does not implement `legacy.TasksRecoverer`. `Prune`
deletes a terminal run with all its rows in one batch: the guard key by a point `Delete` under its
`Expect`, the run's other families by `DeleteRange`, which storage allows outside a checked range and
refuses inside one (05-storage §5S.3.4, DATA7-1). `TestNativeStores_RoundTrip` and
`taskstest.StoresSuite` cover it.

### 5T.6.2 Books' legacy store (ABO code, not core)

The books module supplies `legacy.Stores.Tasks` (06 §6.4.11, §6.11.6), implemented by an ABO package below the books
composition root (`internal/books/taskstore`, name open; 04, R4-5). It implements `tasks.Stores` with
HEAD's own ops storage code over books' own claims — the families 05-storage §5S.9.1 rows 9, 11, 12, 13
and 13a list — in HEAD's format. Nothing moves into core and nothing is re-implemented there, so no
second copy of the ops-format code exists at any stage (SIMP4-2), and HEAD's format invariants hold by
construction because it is HEAD's code: every run write commits the v3 record **and** its paired v2 row
in one batch (`internal/database/pebble_store_ops_v3.go:28-45`, owner decision D25), which is what every
older build reads. This subsection specifies ABO code; no key name it touches appears in core.

**What changes in HEAD's code: the compare-and-set mechanism (R5-4, DATA5-1, R6-1).** HEAD serialises
**every** run write — transitions, progress, checkpoint, ledger, result and resume-count writes — with a
mutex around read, compare and its **own** commit (`opsMu`: `TransitionRunV3`,
`internal/database/pebble_store_ops_v3.go:1029-1041`; `UpdateOpProgressV2`,
`pebble_store_ops_v2.go:718-751`; `CommitLedgerV3`, which also refuses a terminal run,
`pebble_store_ops_v3.go:1655-1666`). Behind the interface each method stages into the caller's batch, so
the mutex no longer spans the commit; the store instead stages `GetForUpdate` on what it read, and the
precondition is checked at commit: for every method that writes from a run it read (5T.3.13's list),
the run's **guard rows**, the `opv3:run:` record **and** the paired `opv2:op:` row, and in an `Insert` or
`Transition` batch also HEAD's global census row `opv3:meta:counts` (`pebble_store_ops_v3.go:100`, read
and rewritten by every state change, `:243-248,423-439,461-473`), the one non-run key in such a batch;
for `Advance`, the clock key; for `Debounce`, the bucket. HEAD's terminal check in `CommitLedgerV3`
becomes that `Expect`, so a ledger commit that races a terminal transition fails at commit instead of
writing onto a closed run, and a progress tick read before an HTTP cancel cannot write `Status=running`
back into the `opv2:op:` row (the row `ReconcileOpsV3WithV2` rebuilds the v3 record from, so the cancel
would be undone at the next boot and by an older build after a rollback; DATA6-1). Two consequences
are handled where they arise, not by narrowing the check: two **different** runs' state changes both
rewrite the census row, and a progress tick rewrites the `opv2:op:` row that a cancel, decide, watchdog
stop or shutdown quiesce of the same run also reads. Both make a `Transition` or `Insert` batch fail with
`storage.ErrConflict` although its `want` still holds; it is then rebuilt and recommitted under
`storage.RetryOnConflict`, and `ErrRunConflict` is returned only when the fresh read no longer matches
`want` (5T.7, DATA7-2).

**Write checking is per guard row (R7-3, DATA7-1).** `Expect` is admitted only in `Serialized` ranges, so
every family this store writes or annotates with an extension record is `Serialized` in 05-storage
§5S.9.1: row 9's clock, pause and stand-down keys and its clock sub-prefix, row 11, and the row-13
families `Insert`, `Transition` and the run's other writes touch. **`CheckedWrites` applies only to the
three guard rows** `opv3:run:`, `opv2:op:` (row 11) and `opv3:meta:counts` (row 13), from T0+T1, so
storage refuses any blind write to them (`ErrBlindWrite`) rather than relying on each method. Every other
per-run row-11/13 family (`opv3:prog:`, `opv3:ckpt:`, the ledger, snapshots, row marks, op rows and their
index, pages, queue and index rows) is `Serialized` and **`Guarded`** (`storage.Claim.Guarded`, 05-storage §5S.3.4, DATA8-2): an
`Expect` on its keys is refused (`ErrNotSerialized`), its writes and its `DeleteRange` take no commit
stripe, and they are legal only in a batch that carries the run's guard `Expect`, whose stripe every legal
writer holds and which therefore serializes them. Storage does not check the batch rule, because the family has no
key of its own to check; taskstore enforces it by structure: it writes those families only through one
per-run staging handle whose constructor stages the guard `GetForUpdate`, and
`TestOpsFamiliesWrittenOnlyByTaskstore` asserts that no other code path writes them. **Two per-key
guards have no run** (DATA8-8): `op:batch:<opType>:<subjectType>:<subjectID>`, the debounce bucket
(`internal/database/pebble_store_ops_v2.go:1554-1567`), and `opv3:sched:`, HEAD's v3 schedule state
(`keyfamilies.go:303`) served as `ClockStore`. They are `Serialized`, not `Guarded`, with an `Expect`
per key, like native `c/tasks/debounce/` and `c/tasks/sched/`: `Debounce` and `Advance` stage
`GetForUpdate` on the key they read, outside the per-run staging handle. `DeleteRange` is
refused inside a `CheckedWrites` claim and allowed, with no `Expect` and no stripe, inside a `Guarded`
one (05-storage §5S.3.4): HEAD's range deletes on a terminal transition (snapshots, marks, row marks,
`pebble_store_ops_v3.go:285-300`), on a discard (`pebble_store_ops_v2.go:938-942,1056-1060`), in step 62's
hollow sweep (`pebble_store_op_rows.go:76-90`) and in `ReplaceOpRowsPrefix` (`:125`) all cover
`Guarded` families, and a guard row is deleted only by a point `Delete` under its `Expect`. So
`ReplaceOpRowsPrefix`, which HEAD commits without `opsMu` so as not to stall other writers
(`pebble_store_op_rows.go:110-114`), stages one guard `Expect`, not one per written key, and takes no
other writer's stripes for its fsync: the `opv2:row:` and `opv2:rowix:` keys it writes are `Guarded`, so
only the guard key's stripe is held (`TestGuardedWritesTakeNoStripes`, 05-storage's). The T0+T1 PR, which moves the bodies of books' legacy steps 62, 65
and 66 into taskstore (below), converts their guard-row writes to writes that stage an `Expect` on the
row they rewrite (absent for a new row) and routes their other writes through the staging handle, and
converts `Recover`'s and `Prune`'s the same way, so no blind writer of a guard row is left. Both
`Prune` and `Recover` rewrite the census row `opv3:meta:counts` (`del` → `count(prev, -1)`,
`pebble_store_ops_v3.go:309-325`; `pebble_store_ops_v3_reconcile.go:75`), so each commits every chunk
under `storage.RetryOnConflict`, re-reading each row's guard: `Prune` skips a row that is no longer
terminal, `Recover` one a live write has synced since detection (DATA8-8,
`TestPruneUnderConcurrentCompletions`); the first
T0+T1 release records violations count-only under 05-storage's discovery rule for a new flag (DATA6-12),
the method-level checks protect the data meanwhile, and the strict release follows only after zero
persisted blind-write violations on the guard rows for ≥ 14 days of production (05-storage D-S2,
DATA7-15). `TestABOClaimsMatchOwnershipTable` cross-checks each family served through `tasks.Stores`
against that rule; `TestLegacyRunTransitionVsCancelOneWins` (`synctest`, books' store: an executor
completion and an HTTP cancel of one run race; exactly one commits, the other's fresh read no longer
matches its `want` and it gets `ErrRunConflict`), `TestLegacyProgressVsCancelKeepsState`,
`TestLedgerCommitAfterTerminalRefused`, `TestConcurrentTransitionsOfDistinctRunsAllCommit` and
`TestCancelCommitsUnderProgressTicks` (5T.9) prove the result.

**Package direction (R5-2, MOD5-1, R6-2).** Imports run `internal/books/taskstore` → the remaining store
package, never the reverse (an ABO guard assertion), so the split is no import cycle. Taskstore also
imports `internal/operations/state` (HEAD's run-state vocabulary and transition machine, used from six
of its files), which **stays in ABO** next to it (5T.8.1; MOD6-1, MOD6-9). In the T0+T1 PR
taskstore takes, besides HEAD's ops storage files: the bodies of books' legacy steps 62 and 65, step
66's body (`pebble_store_plan_legacy.go`, which writes ops rows and so belongs to the one writer of
those families), and the ops part of `InitializeStore` (it becomes `Recover`). The remaining store
exports what taskstore uses: `RecoverPebbleClosed`, `PrefixEnd`, `PrefixUpperBound`, `OpChangeKey` and
the version-source adapter (04 §8.6 gate E's name table, MOD7-8); HEAD's `errStoreClosing` is declared in
`pebble_gate.go`, which goes to core `storage` at S2b, so taskstore matches `storage`'s exported closed
error (`storage.ErrClosed`, 05-storage §5S.3.1) instead of reaching into the residue (04 §8.6); the
residue keeps HEAD's pure ops-family key predicate (`rejectOpv2OpRawKey`), which its own raw-write path
needs and which taskstore may call (04 §8.6 gate E, MOD7-8); `opsV2TimelineTrusted`
moves onto the taskstore type. Books' legacy step list keeps its order by calling steps 62, 65 and 66
through funcs `internal/books` wires from taskstore. Every capability assertion on a moved method
(`store.(hollowOpsSweeper)`, `store.(opsV3Reconciler)`, `internal/database/migrations.go:1238-1244`,
`store.go:1867`) becomes a direct call, because once the methods leave `*PebbleStore` an assertion fails
silently: step 62 and 66 would log "nothing to do" and be recorded as applied without running, and the
every-boot reconcile would be skipped. `TestLegacyStepPreconditionsResolve` **(ABO)** asserts that
every legacy step reaches its body for the production store type (no "nothing to do" path; an
external test in `internal/books`, which imports both the residue's step list and taskstore, so it
compiles once the split lands, MOD6-2), and `var _ legacy.TasksRecoverer = (*taskstore.RunStore)(nil)`
plus `TestLegacyRunStoreRecovers` (a stale v3 record beside its v2 row is rebuilt on the next boot)
pin the optional capability.

**Run states (R6-2, MOD6-1).** Books' store keeps HEAD's stored vocabulary: it writes and reads
`state.V3` values in the `opv3:run:` record and the v2 status in the `opv2:op:` row through HEAD's
`state.FromV2`/`ToV2` (`internal/operations/state/v3.go:289,344`), and keeps HEAD's in-store legality
check (`state.InferEvent`/`state.Next`, `v3.go:187,261`), so a core transition HEAD's machine refuses fails
the write instead of storing a state an older build cannot follow; `taskstest.StoresSuite`'s transition
matrix runs every executor edge over books' store, so a disagreement fails CI. Every stored form reads
as exactly one `RunState`, and a write uses the first form its row lists
(`internal/operations/state/v3.go:23-50`):

| `tasks.RunState` | Stored by books' store (`state.V3` and its sub-field) |
|---|---|
| `queued` | `queued`, `WaitReason` `""`, `ready` or `pause` |
| `waiting_deps` | `queued`, `WaitReason` `deps` |
| `running` | `running` |
| `stopping` | `stopping` with `StopReason` `user` ↔ Reason `cancel`, `quiesce` ↔ `quiesce`, `shutdown` ↔ `shutdown`; a row an older build stopped with `timeout` or `watchdog` reads as `stopping` with that token as `Reason` |
| `interrupted` | `interrupted`, `InterruptedWhy` `crash` or `legacy` |
| `interrupted_quiesced` | `interrupted`, `InterruptedWhy` `quiesced` |
| `awaiting_decision` | `awaiting_decision` |
| `succeeded`, `failed`, `timed_out`, `canceled` | the same strings |
| `interrupted_dropped` | `dropped` |

| Interface | Books' implementation (HEAD code, HEAD format) |
|---|---|
| `RunStore` records, queue, indexes, census, trails, intents, items, log | HEAD's native v3 run path with the paired v2 rows (`internal/database/iface_ops_v3.go:110` `RunRecord`, `internal/database/iface_ops_v2.go:39` `OperationV2Row`); `RunState` ↔ `state.V3` by the table above and the v2 status through HEAD's `state.FromV2`/`ToV2`; `Priority` Low/Normal/High ↔ 0/1/2 (`internal/operations/registry/types.go:329-331`), `PrioritySystem` ↔ 3, which sorts first in HEAD's `999-priority` queue key (`internal/database/pebble_store_ops_v2.go:63`); `NewID` is HEAD's op-ID generator |
| checkpoints, chunk ledger, item-key snapshot, Fixer rows and marks, debounce, strikes | HEAD's v3 checkpoint, ledger, snapshot, row and row-mark methods and the v2 batch-bucket and strike methods; per-run pages are written where HEAD reads them |
| `Stamp`/`Stamped` | for `books/books` and `books/book_files` subjects: HEAD's completion and dependency-revision stamps (`pebble_store_ops_v2.go:1188`), library-scale and never copied; any other subject: the extension family below |
| `List` (history) | v3 rows, falling back to the v2 row for a run without a projection (open item 5), merged with the v1 history HEAD's v1 reader returns, as ordinary terminal `RunRecord`s with no marker (HEAD's v1 reader carries none, and `RunRecord` has no legacy field; SIMP6-7): the results, summaries and statuses written by `CreateOperationResult`, `SaveOperationSummaryLog` and `UpdateOperationStatus`, and the v1 log. The v1 checkpoint family that `operations.SaveCheckpoint` writes is not history: the store neither reads nor writes it (SIMP4-7) |
| `Prune` | HEAD's terminal-run retention, plus the v1 log; never a non-terminal row |
| `Recover` (the optional `legacy.TasksRecoverer`, 5T.3.13) | HEAD's every-boot `ReconcileOpsV3WithV2` (`internal/database/store.go:1860-1870`), with one change: each rewrite stages an `Expect` on the guard row it read and writes its other families through the per-run staging handle (the guard rows are `CheckedWrites`, above). It rebuilds v3 records a previous binary left stale against their v2 rows and writes only those families; a failure is logged and the next boot retries, as at HEAD (`store.go:1864-1866`) |
| `ClockStore` | HEAD's v3 schedule state (`SchedState`, `iface_ops_v3.go:281-290`) for v3 kinds; for the 38 `TaskDefinition`s, the scheduler's settings clocks (an RFC3339 last-due time per task under the setting prefix `intervalLastTickPrefix`, `internal/scheduler/interval_clock.go:50-60`, plus the full-sweep clock `library_scan_full_last_run`, RFC3339, `internal/scheduler/full_sweep.go:81,117`), task name ↔ def ID by `taskV2DefIDs` (`internal/scheduler/maintenance.go:154-175`); and, as the clock of the maintenance-window kind, HEAD's `maintenance_window_last_run` setting (`maintenance.go:53,71`) with HEAD's **date codec**: a `YYYY-MM-DD` local calendar date (`time.Parse("2006-01-02")` on read, `time.Now().Format("2006-01-02")` on write), which `Get` reads as `LastDue` = `LastRun` = **local** midnight of that date (`time.ParseInLocation("2006-01-02", v, time.Local)`) and `Advance` writes as `next.LastRun.In(time.Local).Format("2006-01-02")`, HEAD's local date (`maintenance.go:70`). HEAD itself parses the value in UTC and compares only its formatted date with the local `today` (`maintenance.go:127-128`), so a UTC-midnight `Get` compared in local time would read a run on D as D−1 west of UTC and run the window twice on D; with both halves in local time any zone-consistent comparison is right, and a pre-T7 build reads the marker it expects and does not run the window a second time that day after a rollback (MOD7-3, MOD8-9; `taskstest.StoresSuite` covers the codec over books' store, including a case run with `TZ` west of UTC: `Advance` at 23:30 local, `Get`, due check the same day → not due). Source (MOD5-4): the key prefixes and keys, the RFC3339 and date encode/decode are cut out of `interval_clock.go`, `full_sweep.go` and `maintenance.go` into taskstore, and `taskV2DefIDs` into books as data passed to taskstore and to the books ticker; the tick logic stays with the books ticker (deleted at T7) and is **re-implemented**, not moved, in `core/internal/scheduler` (5T.8.1), so no ABO setting key or op ID travels into core |
| `PauseStore` | HEAD's operator pause marker: the setting key `pauseSettingKey` and the `pauseMarker` struct (`internal/operations/registry/pause_gate.go:52-60`) are cut out into taskstore; the gate logic goes to the executor without them (MOD5-4) |
| `StandDownStore` | HEAD's persisted scan stand-down marker (MOD6-4): the setting key `registry.scan_standdown` and the `scanStandDownMarker` JSON codec (`{holder_op_id, scan_op_id, lease_expiry_unix_nano}`, a blank value meaning cleared; `internal/operations/registry/scan_standdown.go:27,106-110,557-605`) are cut out into taskstore and serve the library-scan stand-down key with one quiesced run, exactly HEAD's singleton; a marker for any other key, or with more than one quiesced run, is a standalone record in books' extension family, which no older build reads (an older build stands down only the scan). The gate logic goes to the executor without them |

**Native-only data: one extension family (R4-1, SIMP4-9).** A field HEAD's row has no place for — a
clock's `Missed`, `GateClosedAt`, `LastRun`, `LastFire` and `ScheduleHash`, a run field HEAD's records
lack, a stamp of a non-book subject — is never written additively into a legacy row: HEAD decodes rows
into typed structs with no catch-all field and drops unknown members on its next rewrite
(`pebble_store_ops_v2.go:336-380`), and extra content in a legacy row is the class of change SEC4-3
showed unsafe. Books' store keeps such fields in a **books-owned extension family that no older build
reads** (05-storage §5S.9.1 lists it), under 05-storage §5S.3.11's extension-record rule: one record per
legacy row, keyed by that row's key, holding no field the row holds, written in the same batch as the
row, carrying `legacy_ref` = `{key, fp}` (the one name, DATA5-16). There is no default projection
(05-storage §5S.3.11): each task `Extension` declares its `Hash` over the whole `opv3:run:` record or
clock row it annotates, because no per-tick field lives there (progress goes to `opv3:prog:` and the v2
row) and every field of it can change what the extension's fields mean (DATA6-14). Every field of a task extension record is **Derived** and
none is **Widening** (05-storage §5S.3.11's classes): none grants access, widens a permission or starts
work. Books' extension family is a `Serialized` module-keyspace range, which storage checks per key
(05-storage §5S.3.2), so every task extension write states its `Expect` (DATA7-8): a record `Put`
(`legacy.Extension.Put`) stages an `Expect` on the record bytes it read (`seen`; zero = no record) and
carries the guard `Expect` on the legacy row the store read (R5-4, above); a record delete is
`legacy.Extension.Delete(ctx, b, legacyKey, entryID, seen)`, a conditional delete that stages an `Expect`
on the record it read (05-storage owns the signature; R7-3). On read:

- `ExtCurrent` (fingerprint matches): the extension applies;
- `ExtStale` (an older build rewrote the row while rolled back): each field takes its stated default
  (`Missed` 0, `GateClosedAt` zero, an unknown `ScheduleHash` re-derives the next instant from
  `LastDue` without firing; run fields their zero value), counted
  (`mediacore_tasks_store_extension_stale_total`) and logged once. Because the record has no Widening
  field it is never marked `Suspended` (05-storage §5S.3.11, DATA5-4): the next write of that row
  through the store re-stamps it under the current fingerprint and it reads `ExtCurrent` again, so no
  admin step exists or is needed, and no health token counts a task record (contrast 05-authn and
  05-rbac, whose Widening fields carry access, need an audited confirmation and report their own
  suspended tokens; SIMP7-6 deleted the generic `storage_extension_suspended`);
- `ExtOrphan` (row absent): the extension is ignored, and deleted (the conditional delete above) by the
  next write of that key or by `Prune`.

**One writer per family; one scheduler per kind (DATA4-5).** From T0+T1 every runtime write of these
families goes through books' store — HEAD's per-family staging functions — enforced by an ABO archtest,
`TestOpsFamiliesWrittenOnlyByTaskstore`, the ops instance of 09's umbrella ratchet
`TestLegacyFamiliesWrittenOnlyByLegacyStores` (09 §9.2.1, §9.2.3 rule 4; successor of `TestOpsV2RowWritesGoThroughStageOpRow`,
`raw/08` §7): the only other writers are books' legacy steps in P4b and the v1 carve-outs below. This
replaces round 3's storage-enforced `Exclusive` grant with a package-structure rule checked in ABO CI
(SIMP4-2 cost (b)). ABO's `internal/scheduler` keeps ticking the 38 `TaskDefinition`s from T0+T1 to T7,
enqueuing through `Deps.Tasks` and advancing their settings clocks through the books store's
`ClockStore`; core's scheduler polls only kinds that declare a `Schedule`, and no adapted kind declares
one until T7 converts its `TaskDefinition` and deletes the books ticker entry in the same PR, so no kind
ever has two schedulers and no clock write is ever refused (`TestOneSchedulerPerKind`). The converse
holds too and is the reason the ticker is never stopped early: from T0+T1 to T7 the books ticker is the
**only** scheduler of the 38 definitions (library scans, the maintenance window, retention), so any
statement that books' scheduler stops at T0+T1 would leave them unscheduled — the silent-stop class
`internal/scheduler/interval_clock.go:116-140` documents (DATA5-8). 05-storage §5S.9.1 row 9 states the
same rule. The T0+T1 done-check proves it with two checks (5T.8.2; OPS8-4), not with "every one of the
38 definitions fires on staging", which cannot pass at HEAD parity: transcode and series-normalize are
manual-only by construction (`IsEnabled` false, interval 0, `internal/scheduler/tasks.go:553-554,708-709`),
the ticker runs only enabled definitions (`internal/scheduler/scheduler.go:335`), some definitions are
switched off by config, and a weekly interval makes the wait a week:

- **Normative: `TestScheduleParity_AllDefinitions`** (root CI, fake clock). For each of the 38 definitions,
  with its config enabled and disabled, `cur` (T0+T1: core scheduler plus the books ticker) and HEAD
  produce the same sequence: enqueue or no enqueue, start-run, maintenance-window reachability and clock
  advance, from the definition's `IsEnabled`, interval and `RunOnStart`. A manual-only definition never
  starts itself.
- **Smoke: the staging boot.** It logs the scheduler's table (name, enabled, interval, next due). Every
  enabled definition with an interval of 24 h or less fires within one interval on the scrubbed,
  egress-denied staging copy (09 §9.3, R7-6); the rest are listed with their next due time.

**Rules that make a rollback a binary swap (G5).**

1. **One row per run.** A run is never in two stores, so no build can double-execute it. After a
   rollback the previous build resumes, retries or shows every run in the format it wrote; HEAD's Retry
   re-queues the same row (`internal/operations/registry/retry.go:24-50`), which is correct rather than
   a hazard (DATA3-4). Runs of a kind with a `LegacyDefID` are stored under that def ID.
2. **Native-only fields live only in the extension family** and read stale (defaults), never trusted,
   once their row changed underneath them (above); no rule of this file depends on an extension
   surviving a rollback.
3. **Kinds no older build knows.** Their runs are stored under `Kind` itself. An older build skips them
   in dispatch (`dispatcher.go:113-120`) and marks a resumable one `interrupted_dropped` at boot with
   reason "unknown def at startup" (`resume.go:86-96`); an older **core** build does the same (5T.5).
   After roll-forward that run shows as dropped and is restarted by its schedule or by hand, a
   consequence listed in 09 §9.2.1's rollback table for the stage that adds such kinds (OPS4-11).
4. **Core-owned `c/tasks/` families are invisible to an older build.** Definition snapshots are
   rewritten at the next boot's registration and idempotency records only expire; neither depends on a
   legacy row, so neither can go stale against one (DATA4-8).
5. **No concurrent writers across builds.** Pebble admits one process at a time; within the process the
   one-writer rule above holds.

**Sequence numbers (DATA-19, consumer side).** The per-run counters that number a run's events,
intents and log lines live in the run record (`RunRecord.Seq`) and advance in the batch that writes the
row they number, by the run's fenced owner; a crash loses only uncommitted rows and never reissues a
committed number. Nothing in `tasks` uses a shared `storage.Sequence`.

**History.** Terminal runs are the same rows: `GET /runs` reads them through `RunStore.List`. They are
never rewritten and are deleted only by `RunStore.Prune` under `core.tasks.history_retention` (an older
build's own retention deletes the same terminal rows; neither deletes a non-terminal row).

**v1 families with live books writers** (05-storage §5S.9.1 rows 12 and 13 state the owner; this table
repeats what this file needs). They stay books' own read-write families, written by books code outside
the store until each writer goes native:

| Family | Handling |
|---|---|
| v1 results, summaries and statuses — **live books writers** at `748fd787a`: `CreateOperationResult` (`internal/database/pebble_store_operations.go:438-445`; 15 sites in 9 files, e.g. `internal/server/metadata_ops.go:391,440,1056,1105`), `SaveOperationSummaryLog` (`internal/maintenance/jobs/backfill_book_files.go:197`, `recompute_book_aggregates.go:265`, `backfill_file_hashes.go:1440`), `UpdateOperationStatus` (`internal/operations/registry/legacy_op_status.go:244`, `legacy_backfill.go:218`) | books carve-outs, read-write (DATA2-12); read by the books store's `List` as terminal history. Each result and summary writer moves to the run's result or summary in the batch that makes its job native (T2, T5, T7). The two `UpdateOperationStatus` sites stay ABO code in opsbridge (5T.8.1, MOD7-2): they only mirror a terminal status onto a v1 row whose creator stamped `legacy_op_id`, so they write nothing once every creator is native, and they are deleted with opsbridge at T8; `TestLegacyOpsWriters` ratchets the 20 sites down to zero, after which `Prune` clears the rest |
| v1 log | read by `List`; deleted by `Prune`; never written |
| v1 checkpoints — checkpoints and cursors of **live** books jobs: `operations.SaveCheckpoint` (`internal/operations/state.go:60`, which goes to `internal/books/contract` at S5, not to `tasks`; 5T.8.1, MOD7-2) from `internal/itunes/service/importer.go:626,636,725,732`, `internal/dedup/drain_stale.go:324`, `internal/maintenance/jobs/recompute_book_aggregates.go:230`, `internal/maintenance/jobs/backfill_file_hashes.go:1315`, `internal/server/metadata_ops.go:1326`; the hash-backfill chunk journal (`backfill_file_hashes.go:722-775`); the metadata-upgrade cursor (`internal/metabatch/upgrade.go:162,199`); deletes in `retention_and_hygiene.go:339-374` | **books carve-out, read-write** (DATA-15); neither read nor written by the store (SIMP4-7). Each writer moves to `rc.Checkpoint` or a books-keyspace cursor in the batch that makes its job native (T2, T5, T7); `TestLegacyOpsWriters` ratchets the write sites down to zero |
| v2 and v3 definition rows | left to older builds; the store reads neither (core's snapshots are `c/tasks/def/`) |
| the operation-change journal | books' `ChangeSink`; not tasks |

**No level step during the extraction; the floor release (OPS4-5, DATA4-18, DATA4-19).** The extraction
changes no stored format of these families, so `core/tasks` registers no migration step, no adopt step
and no `RaisesLevel` step, and T0–T8 leave the store's level where it is (the level guard serves books'
own format changes and later cutovers, 05-storage). The only store-side bound is books' legacy track,
named with two distinct terms:

- the **floor release** is the first release that ships T8: it deletes the ABO code only books' legacy
  steps 62, 65 and 66 call (`MigrateOpsV2ToV3`, `SweepHollowOperationsV2` and the store methods behind
  them) and declares books' `UpgradeFloor{Version: LegacyCap, InstallFirst: <stepping-stone release>}`
  (05-storage §5S.3.5; `LegacyCap` is the highest HEAD-style books step at the S2b flip, 66 at the
  evidence commit). Every release before it keeps that code;
- the **stepping-stone release** (`InstallFirst`, and the `release` of the signed manifest's
  `min_upgrade_from`, [05-updater](05-updater.md) 5U.3) is the newest production tag that exists when
  the T8 PR merges. It carries every books step up to `LegacyCap` and the code they call, and contains
  no T8 deletion; root CI's `TestStepSteppingStoneValid` asserts that the tag exists, predates the T8
  commit, registers books `LegacyCap` and prints an empty `--print-upgrade-floor`.

A store below `LegacyCap` is refused by the floor release at boot with `ErrUpgradeFloor{InstallFirst}`
(exit 78). The floor release ships in an S5 release, before any signed release manifest exists (05-updater
5U.12), so the guard that keeps it off such a store is the binary's own floor: the 09 S0 updater hotfix
runs `<exe>.new --print-upgrade-floor` and compares it with the store's `<db>.storage-applied` before the
swap, refusing with `stepping_stone_required` and naming the release the new binary prints, and the
`deploy` recipe runs the same pair before it copies the binary (09 S0); the S8 updater also checks the
signed manifest's copy (05-updater 5U.4, 5U.5; OPS6-1, OPS3-7). A pre-S0 updater swaps any newer binary
unchecked (`internal/updater/updater.go:214-280`), so the floor release's note and the Docker last-line
message name the stepping stone.

**Deferred: cutover.** Moving runs from books' legacy store to the native store and deleting the legacy
families is **out of the extraction**. It is a later, per-family, one-way, checkpointed step behind the
single per-owner level guard, decided on measured data (02 decision 13); it is the first point at which
core reads books' rows, so it re-introduces a read-delete grant for `core/tasks` (05-storage §5S.3.13,
deferred-build members), records `native` in `backends`, and is its own floor release. How it treats
library-scale families (the `Requires` stamps, per-run pages, history) is decided then; nothing in
T0–T8 prepares for it.

## 5T.7 Concurrency contract

- `Executor`, `Client`, `RC`, `ItemRC`, `Writer` are goroutine-safe; `RC` may be shared with goroutines
  the run starts **only** if they end before the run returns (a goroutine left behind writes
  nothing: its writes are refused, `ErrItemReturned`/`ErrFenced`).
- Bounded everything: N executor workers; per-module `max_concurrent`; per-run `Parallelism` (with
  the operator cap); executor CPU budget; lanes; zombie cap; one ref-lock table with sorted
  all-or-wait acquisition (no lock-order deadlock). No unbounded goroutine fan-out anywhere in `tasks`.
- One scheduler goroutine for all schedules (current `runV3Loop`, `v3_schedule.go:622`).
- State transitions are CAS on `(state, attempt, fence_epoch)` through `RunStore.Transition`'s
  commit-time precondition (current `TransitionRunV3`, `internal/database/iface_ops_v3.go:400-405`),
  whichever store implementation serves the install; the precondition replaces HEAD's `opsMu` and is
  admitted because the run's guard rows are `Serialized` (R5-4, 5T.6.2).
- **A conflict is not a lost precondition (R7-3, DATA7-2).** A batch that stages `Insert` or `Transition`
  can fail with `storage.ErrConflict` while its `want` still holds: on books' store every state change of
  every run rewrites the census row `opv3:meta:counts`, and a progress tick rewrites the `opv2:op:` row a
  cancel of the same run reads. So whoever builds such a batch (the executor's claim and terminal
  transitions, a cancel or decide, the watchdog's stop, the shutdown quiesce, `Client.Start`, the
  scheduler's enqueue, an outbox consumer's `StartIn`) builds **and** commits it inside
  `storage.RetryOnConflict` (bounded, jittered): each attempt re-reads the run (`Get`; for `Insert`, its
  absence), and while `want` (state, attempt, fence) still matches it re-stages the **whole** batch, every
  participant included (clock advance, outbox cursor, idempotency record, `OnTerminal` notification), and
  commits again; when the fresh read no longer matches `want` it returns `ErrRunConflict` and stops. It
  never re-commits bytes built from the stale read. Attempts exhausted while `want` still matches surface
  `storage.ErrConflict`, a transient error: the executor retries its own transition on the run's next
  writer turn, and a requested one (cancel, decide) answers its caller with a retryable error. So N runs
  completing at once all commit with exact counts (`TestConcurrentTransitionsOfDistinctRunsAllCommit`),
  and a cancel commits under a stream of progress ticks (`TestCancelCommitsUnderProgressTicks`; both
  `synctest`, cases of `taskstest.StoresSuite` over both stores).
- **Every other run write is checked at commit too (R6-1, DATA6-1).** HEAD serialises progress,
  checkpoint, ledger, result and resume-count writes on the same `opsMu`
  (`internal/database/pebble_store_ops_v2.go:718-751`; `pebble_store_ops_v3.go:1655-1666`), so each
  `RunStore` write stages `GetForUpdate` on the guard rows of the run it read (5T.3.13), in both
  implementations. **One per-run writer:** the executor funnels a run's own writes (ticks, ledger,
  checkpoints, rows, marks, pages, items, log) through one writer per run, an in-process lock that
  05-storage §5S.3.2 allows for fairness while correctness stays with the commit-time check (progress is
  already delivered under one lock, 5T.3.2), so a run's page appends never race its own ticks and a lost
  precondition means another actor changed the run: a cancel, a decide, the watchdog's stop, shutdown.
  On such a conflict the write is never re-staged over the old read; it re-reads the run under
  `storage.RetryOnConflict` and applies **HEAD's closed-ledger rule** to the fresh read
  (`pebble_store_ops_v3.go:1664-1666`: a ledger commit is refused only on a terminal run;
  `pebble_store_ops_v3_rowmarks.go:54-56`: a row mark only on a missing or terminal run). A **bare
  progress tick** is dropped when the fresh read shows any state change (the next tick carries current
  numbers) and is otherwise re-staged; a **chunk-ledger commit, checkpoint, Fixer row or mark, page
  append, item outcome or strike** is re-staged while the fresh read shows the run **non-terminal under
  the same `Attempt` and `FenceEpoch`**, `stopping` included, and is otherwise refused, so its chunk
  counts as uncommitted and is redone on resume, as after a crash (`ResumeContinue` already tolerates
  that). A run that a stand-down quiesce or a shutdown moved to `stopping` therefore still takes the marks
  of the rows its Fixer already applied, and those rows are skipped on resume rather than applied twice
  (marks are resume evidence written as each row's apply returns; `TestFixerMarksLandDuringQuiesce`,
  DATA7-3). A **debounce** merge re-reads its bucket and retries. So a tick never reverts a cancel or
  terminal transition, nothing lands on a closed ledger, and an older build after a rollback never finds
  a cancelled run back at `running`.
- Cancellation propagates by context cause to every worker within one item boundary (5T.3.2).
- No package-level mutable state (R5): pause gate, CPU budget, ref-lock table, metrics, def registry
  are fields of the executor instance; two `App`s in one process do not share them.
- `held` is computed from the manifest set at boot; it never changes while the process runs, because
  enablement is restart-bound (06 §6.5.3).

## 5T.8 Migration from ABO

### 5T.8.1 What existing code becomes

| ABO today | Becomes |
|---|---|
| `internal/operations/registry` (dispatcher, worker, watchdog, resume, shutdown, batch runner, chunk ledger, fence, deps, debounce, pause, stand-down, warm-up) | `mediacore/internal/executor` is a **re-implementation** (an 04 §8.6 row with a generated count; R7-8, MOD7-1), written fresh against `tasks.RunStore` and `tasks.RunState`: the engine's files use HEAD's run vocabulary from `internal/operations/state` (`state.StopQuiesce`, `InterruptedQuiesced`, `TimeoutPrefix`, `state.ToV2` on a `database.OperationV2Row`, `resume.go:554-555`) and HEAD's v2 row types (`OperationV2Row`, `OpsV2Store`, `TransitionRequest`), which R6-2 keeps in ABO, so no import-path change compiles and no file moves. The figures are the generated ones of 04 §8.6's registry row (MOD8-7): the engine has 30 non-test files, 14 of them use HEAD's v2/v3 run-row types, and 5 of them hold 39 code references (comments excluded) to `internal/operations/state`. The **ABO files stay at their path** as the v2 adapter `opsbridge` until T8 (5T.8.2), and no `origins.txt` line is written for them (12 §12.8.1). The pause gate's logic is re-implemented while its setting key and marker struct (`pause_gate.go:52-60`) go to taskstore's `PauseStore` (MOD5-4), and the stand-down's logic likewise while its setting key `registry.scan_standdown` and marker codec (`scan_standdown.go:27,106-110,557-605`) go to taskstore's `StandDownStore` (MOD6-4). ABO legacy formats in these files stay ABO (MOD7-2): `legacy_op_status.go` (the v1 status bridge, `propagateLegacyOpStatus`) and `legacy_backfill.go` (`BackfillLegacyOpStatus`) **stay in opsbridge**; from T0+T1 the bridge is driven by a books bus subscriber on `core.task.finished.v1` (forwarding only kinds with a `LegacyDefID`, as the legacy-stream bridge of 5T.4 step 3 does; [05-events](05-events.md) 5E.13) and the backfill by its server op (`internal/server/legacy_backfill_op.go:81`), both calling books' store's v1 methods directly, so HEAD's type-asserted optional capabilities (`legacyOpStore`, `legacy_op_status.go:21-28`; `legacyBackfillStore`) are gone (R5-2); both are post-commit and best-effort, as HEAD's `publishOpTerminal` path is, and are deleted with opsbridge at T8. The activity mirror (`activity_mirror.go`, `reporter_db.go:37,866`, which writes books' `database.ActivityEntry`) becomes a **books** subscriber on `core.task.log-appended.v1`, keeping HEAD's bounded queue and counted drops; it is permanent books behaviour, so the T0+T1 PR moves it (with the mirror part of `reporter_db.go`) out of opsbridge into `internal/activity`, which already owns `ActivityEntry` writing and outlives T8 ([05-events](05-events.md) 5E.13; MOD8-8) |
| `internal/operations/state` | **stays in ABO** as books' legacy run-state package, next to `internal/books/taskstore` (it may be renamed under `internal/books/`, never moved to core; R6-2, MOD6-1): the v2 status table, the V3 constants, the transition machine, `Classify*` and `FromV2`/`ToV2`, which taskstore uses to keep HEAD's stored values (5T.6.2 "Run states"). Core's executor state machine over `tasks.RunState` (files of `mediacore/internal/executor`, MOD7-13) is part of the executor's **re-implementation** (an 04 §8.6 entry), not a move. `tools/cmd/opsgen` keeps reading this package, so `web/src/generated/ops.ts` (`OperationV2Status`, `STATUS_PROPS`, `RUN_STATE_PROPS`) stays byte-identical because its input does not change; opsgen is **not** repointed at `mediacore/tasks`, whose `RunState` values are not HEAD's stored strings (e.g. `interrupted_dropped` vs `dropped`) and which holds no v2 table |
| `internal/operations` (`progress.go`, `trigger_source.go`, `state.go`, `selection.go`, `types.go`) | only `progress.go` and `trigger_source.go` → `tasks`; `state.go`'s v1 checkpoint helpers (`OperationState`, `SaveCheckpoint`, `LoadCheckpoint`, `ClearState`, `state.go:14-100`, 11 books call sites) → `internal/books/contract` until their T2/T5/T7 writers are gone, and `BulkMetadataFetchParams` (`state.go:27-30`) → `internal/server`, its only user (`metadata_ops.go:196,796,885`), so no legacy-only identifier enters the frozen `tasks` API (R6-3, R7-8, MOD7-2); `selection.go`, `types.go` → `internal/books/contract` (04 §3) |
| `pkg/ops` (Task/Batch/Fixer, Effects, Schedule, contracts, RC, Approval, opstest; `Pipeline` dropped, A7) | `core/tasks`, `core/tasks/taskstest` (from `pkg/ops/opstest`), with A1–A7; the whole of `pkg/ops/opstest` is a **re-implementation** in `taskstest` over `tasks.RunState` and core's in-memory storage, because `conformance.go` and `run.go` read HEAD's V3 states and `faults.go`, `store.go` and `world.go` build HEAD's v2/v3 rows, so no file of it moves by an import-path change; its Book-typed fakes are re-implemented in `internal/books/store/storetest` (04 §8.6 rows; MOD7-1) |
| `pkg/ops/writer.go` typed methods + `internal/operations/opswriter` | run fencing (`opswriter/fence.go`: `ErrFenced`, `WithFence`, `FenceFromContext`) → public `tasks` (04 §3 `opswriter` *Split*); the generic gated write of 5T.3.5 is core code in files of `mediacore/internal/executor` (MOD7-13); typed `ModifyBook*` over `tasks.Writer`: the contract interface → `internal/books/contract`, the implementation (`writer.go`, `evidence.go`, `history.go`, `intent.go`, `evidence_journal.go`) → `internal/books/writer` (the `opswriter` rename; 04 §2.4, §8.6; MOD6-12) |
| `internal/operations/opmode` | core start path (5T.3.7) |
| `internal/operations/childop` | `rc.Await` in `tasks`, written fresh, a **re-implementation** (an 04 §8.6 row; `childop/follow.go` reads HEAD's `state` vocabulary; MOD7-1). The ABO package **stays at its path**, as opsbridge-side code over taskstore's `OperationV2Row`, until its last ABO user is gone (R8-6, MOD8-2): v2 `OperationDef` bodies that receive a `Reporter`, not an `*RC`, and stay v2 until their batch — `plugins/dedup/run_all.go:532` and `label_refinement.go:153` (T5), `plugins/maintenance/optimize.go:73` through `WaitForOp` (`internal/server/server_maintenance_deps.go:1185-1198`; T6, T7) — and opsbridge's own `v3_adapter.go:480`, so it is deleted with opsbridge at T8. No v2 body is rewritten onto `rc.Await` before its batch |
| `internal/walkresume` | `rc.ShouldYield` + `ErrContinueInPlace` + `RunItems` `Watermark` |
| `internal/scheduler` (interval/daily/cron/window/full sweep/v3 loop) | `core/internal/scheduler` is a **re-implementation** (a 04 §8.6 entry, not a move or a copy; MOD5-4): no file of `internal/scheduler` moves into core, because its clock encoding (`intervalLastTickPrefix`, RFC3339) and `taskV2DefIDs` are ABO key names and op IDs. At T0+T1 those two parts are cut out into taskstore's `ClockStore` and books (5T.6.2); the rest of the ABO path keeps ticking the 38 definitions through `Deps.Tasks` and that `ClockStore` until T7, when their bodies become `Schedule` declarations in `books` and the ABO path is deleted |
| `internal/repairs` (Fixer framework) | `tasks.Fixer`; the 24 fixers → `books` Fixer defs |
| `internal/maintenance` (`MaintenanceJob`, registry) | deleted after T2; jobs → `books` Task defs |
| `internal/opscatalog`, `pkg/plugin/sdk`, `addOpRegistrar`, `serviceregistry` op wiring | `module.Registrar.Tasks` |
| `internal/opsmetrics` | executor metrics (5T.11), a **re-implementation** in `mediacore/internal/executor`, its one core destination; nothing of it is public `tasks` (an 04 §8.6 row; `opsmetrics.go` reads HEAD's `state` vocabulary; MOD7-1, MOD8-2). The ABO package **stays at its path**, as opsbridge-side code, until its last ABO user is gone (R8-6): opsbridge's registry files (T8), the ABO scheduler's v3 loop (`internal/scheduler/v3_schedule.go:444,447`, T7) and the `RegisterAbandoned` callers `internal/repairs/guards.go:209` and `internal/plugins/maintenance/retired_versions_fixer.go:240`; it is deleted with opsbridge at T8. Its run instruments that the v2 engine alone recorded leave in the T0+T1 PR with their alias rows (5T.11). Books' abandoned-call gauges move, when their callers convert (T3, T6), to a books-registered observable through the public `metrics` scope (05-metrics), not to a core `RegisterAbandoned` with ABO names |
| `internal/lifecycle` | `core/lifecycle` (03 §3.3, L0) |
| `internal/server/handlers/opsv3`, v1 operations handlers | `mediacore/internal/taskhttp` (05-http-api §5), a **re-implementation** (an 04 §8.6 row; `opsv3/*` read HEAD's `state` vocabulary; MOD7-1) |
| `internal/database` ops storage (`pebble_store_ops_v2.go`, `pebble_store_ops_v3*.go`, `ReconcileOpsV3WithV2`) and HEAD's v1 history reader | **stays in ABO** as books' legacy `tasks.Stores` (`internal/books/taskstore`, 5T.6.2; `ReconcileOpsV3WithV2`, the ops part of `InitializeStore`, is its `Recover`, the v1 reader feeds its `List`), with the bodies of legacy steps 62, 65 and 66 (`pebble_store_plan_legacy.go` included) moved in the same T0+T1 PR; imports run taskstore → the remaining store, never the reverse (R5-2, MOD5-1); nothing moves into core and core has no copy (R4-1, SIMP4-2). The code only books' legacy steps 62, 65 and 66 call is deleted by the floor release (T8) |

### 5T.8.2 Batches

Every batch is one or more PRs, each green on `make ci` in both module dirs; each keeps every kind
working (adapter first, native later). Def IDs map to kinds by prefixing the owning module:
`dedup.full-scan` → `books.dedup.full-scan` with `FormerKinds: ["dedup.full-scan"]`; core-owned
generic ops get `core.*` kinds. **A core-owned kind never spells an ABO op ID in core code (MOD6-6):**
core declares `core.diagnostics.export` with no `FormerKinds`, and the app supplies each core kind's
legacy aliases from its own op-ID ledger through `app.Options.LegacyTaskAliases`
(`map[tasks.Kind][]tasks.Kind`, 06 owns the field; books passes
`{"core.diagnostics.export": ["diagnostics.export"]}` and one entry per T4 kind). The kernel adds them
to the named core kind's former kinds at P7, before the op-ledger gate; a key that is not a registered
`core.*` kind, or a value a module registers or lists in its `TaskLedger`, fails boot
(`ProblemLegacyTaskAlias`, 06). The field is needed from the T4 batch (an S5 release), whose `core.*`
kinds are the first with ABO former IDs, not only from `core.diagnostics.export` at S8; R1(c)'s
`TestCoreHoldsNoAppKeyNames` and 04 §2.1 (h) fail a `FormerKinds` entry outside `core.` in a non-test `core/` file (a core kind renamed within `core.` keeps its own `FormerKinds`). The op-ID ledger (`internal/server/op_id_aliases_test.go`) is the
check that no stored row strands. The last column is this file's input to 09's batch → stage map
(OPS-12); 09 assigns the stage.

| Batch | Content | Count | Gate to finish | Behaviour change / rollback |
|---|---|---|---|---|
| T0 + T1 (one release) | `core/tasks` + executor + scheduler land; ABO adapter `opsbridge` (the `internal/operations/registry` path re-implemented in place as the v2 adapter, not a new package; MOD5-11) wraps every v2 `OperationDef` as a core Task with `LegacyDefID` = its def ID (`Run(ctx, params, Reporter)` called with a `Reporter` implemented over `RC`; legacy `registry.RunItems` re-implemented over `tasks.RunItems` with `Fixed(n, "legacy Concurrency")` from its non-zero `Concurrency`, or `Sequential("legacy zero Concurrency")`); `/api/v3/ops` aliases; books' legacy `tasks.Stores` (`internal/books/taskstore`: HEAD's ops store code and v1 history reader behind the interfaces, 5T.6.2) supplied as `legacy.Stores.Tasks`, with every runtime write of the ops families routed through it; ABO's scheduler keeps ticking the 38 `TaskDefinition`s through `Deps.Tasks` and the store's `ClockStore` until T7; the readiness core of `health` with `books.memdb` (5T.4 step 2); the books-side legacy-stream bridge (5T.4 step 3); the activity mirror moved into `internal/activity` (MOD8-8); the five `operations_*` client_golang vectors of `internal/metrics` (`metrics.go:20-47`), their helpers `IncOperation{Started,Completed,Failed,Canceled}` and `ObserveOperationDuration` (`:351-358`) and their calls in the registry worker (`worker.go:611,1204,1293-1297`) deleted in the PR that adds their alias rows (5T.11, OPS8-1) | all 233 defs run on core | op-ID ledger green; resume/cancel/shutdown suites green on core; `taskstest.StoresSuite` green on both implementations; `TestOpsFamiliesWrittenOnlyByTaskstore`, `TestOneSchedulerPerKind`, `TestDispatch_WaitsForReadySignal`, `TestLegacyRunTransitionVsCancelOneWins`, `TestLegacyProgressVsCancelKeepsState`, `TestLedgerCommitAfterTerminalRefused`, `TestConcurrentTransitionsOfDistinctRunsAllCommit`, `TestCancelCommitsUnderProgressTicks`, `TestFixerMarksLandDuringQuiesce`, `TestStandDown_MarkerAtBootDefersQuiescedRun`, `TestLegacyStepPreconditionsResolve`, `TestLegacyRunStoreRecovers`; the guard rows' `CheckedWrites` (5T.6.2) with zero persisted blind-write violations for ≥ 14 days of production in the count-only release before the strict one, and `TestT0T1RunRowsCheckedWrites` green in both modes (DATA7-15); `TestScheduleParity_AllDefinitions` (normative), and the staging smoke: every enabled definition with an interval of 24 h or less fires within one interval, the rest listed with their next due time (DATA5-8, OPS8-4; 5T.6.2 "One writer per family"; 09 §9.3, R7-6); the five `operations_*` client_golang families deleted with their aliases added in this PR, and `TestS5_FullAliasSetBoots` (5T.11, OPS8-1); G5 rollback test (5T.9) | yes: executor swap (CPU budget, cancellation fix) over the **same** stored format; no level step, no Teach build; rollback = binary swap under the G5 rules of 5T.6 |
| T2 | 34 `MaintenanceJob`s → native `books.maintenance.*` Tasks (mechanical: `Run(ctx, JobStore, ProgressReporter, dryRun)` `internal/maintenance/job.go:498` → `TaskSpec.Run(rc, p)`; `Policy()` → `Common`); their v1 checkpoint writers move to `rc.Checkpoint` and their v1 result and summary writers to the run's result (`TestLegacyOpsWriters` ratchet) | 34 | `internal/maintenance` deleted | no, when kind, params and effects are unchanged (binary swap); a v1-checkpoint → `rc.Checkpoint` move resumes from zero once after a rollback, listed per job in 09 §9.2.1's rollback table (OPS4-11) |
| T3 | 24 repairs fixers → `tasks.Fixer`; `repairs.plan/apply` removed | 24 (+2 defs) | Fixer conformance suite | no (binary swap) |
| T4 | Generic ops → `core.*` (activity-log cleanup/compaction family, backups prune, db optimize/census, temp/trash/reports cleanup, throttle reset, tombstone cleanup, retention-and-hygiene, metrics TTL sweep): the `generic` class rows of `raw/06` §6, each re-checked in [04-package-mapping](04-package-mapping.md) (some, e.g. `opchange-book-index-rebuild`, are book-bound despite the class) | ≤ 24 | owner per kind recorded in 04 | no; kind renames ride `FormerKinds`, a core kind's ABO ID through `app.Options.LegacyTaskAliases` (5T.8.2, MOD6-6) (binary swap) |
| T5 | Plugin defs native, per plugin: acoustid 8, dedup 32, metafetch 3, iTunes 7 (with the 4 `importer.go` v1 checkpoint writers), Deluge 4 (→ 05-download adapter tasks) | 54 | `pkg/plugin/sdk` unused | no (binary swap) |
| T6 | Maintenance plugin's 104 defs, in sub-batches of ≤ 15 by file group | 104 | — | no (binary swap) |
| T7 | Server-package ops (`internal/server/*_op*.go`, incl. `metadata_ops.go`'s v1 checkpoint writer) and `scheduler.metadata-upgrade` (its v1 cursor) → `books`; 38 TaskDefinitions → `ScheduleFromConfig`, each conversion deleting its books ticker entry in the same PR (`TestOneSchedulerPerKind`); 15 v2 crons → none (ledger) | ~49 + 38 | `internal/scheduler` deleted from ABO | yes: schedule keys move (aliases for the window); crons deleted (none ever ran) |
| T8 (the **floor release**, 5T.6.2) | Delete `OperationDef` and `opsbridge` (the `internal/operations/registry` path: its v2 engine files, which the re-implemented executor replaced at T0+T1, and the v1 status bridge and backfill it kept; MOD7-1, MOD7-2; the activity mirror already left for `internal/activity` at T0+T1, MOD8-8), the ABO `internal/operations/childop` and `internal/opsmetrics`, whose last ABO user opsbridge was (5T.8.1, MOD8-2), v2 RunItems; delete `runitems_concurrency_lint_test.go` (replaced by `archtest.RunItemsDeclareParallelism`); delete the ABO code only books' legacy steps 62, 65 and 66 call (`MigrateOpsV2ToV3`, `SweepHollowOperationsV2` and their store methods) and declare books' `UpgradeFloor{Version: LegacyCap, InstallFirst: <stepping-stone release>}` (05-storage §5S.3.5; DATA2-2). The first release that contains T8 **is** the floor release; the stepping-stone release is the newest production tag when the T8 PR merges and contains none of it (OPS4-5) | — | grep: no `registry.` import in ABO; `TestUpgradeFloor_RefusedBelowBooks66`; `TestStepSteppingStoneValid` (root CI) | no format change (binary swap); a boundary only for stores below `LegacyCap`: boot refuses them with `ErrUpgradeFloor{InstallFirst}` (exit 78); the S0 updater hotfix and the `deploy` recipe refuse to swap it in by comparing its `--print-upgrade-floor` with `<db>.storage-applied`, and the S8 updater also checks the signed manifest (05-updater 5U.4, 5U.5; OPS6-1, OPS3-7); a pre-S0 updater swaps it unchecked, so the release note and the Docker last-line message name the stepping stone (DATA4-18) |

Batches T2–T7 are mechanically similar and suit `/parallel-sweep`, partitioned by package so
parallel PRs never touch the same file (CLAUDE.md "Parallel Subagent Coordination").

## 5T.9 Tests (core must ship)

Rows marked **(ABO)** run in the root module against books' legacy store (5T.6.2); the rest are core's.
The prev-binary one-format rows (`TestLegacyFormat_*`, `TestUnknownKindRun_*`, `TestClock_InPlace_*`,
`TestReqStamps_InPlace_HonouredOnPrev`) are part of the root `make ci` requirement, with `prev` built
from `PREV_REF` and cached by tag (09 §9.2.1, OPS4-6), so every Go PR runs them rather than only the
nightly rehearsal.

| Test | Proves |
|---|---|
| `TestParallelism_ZeroInvalid`, `TestBatch_ZeroParallelismRefusedAtRegister`, `TestRunItems_ZeroParallelismFailsBeforeFirstItem` (asserts `*ItemsError` and `errors.Is(err, ErrNoParallelism)`) | N8, one return shape |
| `TestFixed_ClampAndReasonRequired` | SIMP-17 |
| `TestRunItems_CancelStopsDispatch` (synctest: cancel while 3 of 8 in flight; assert no `fn` starts after cancel, `NotStarted` exact) | raw/06 §2 cancel fix |
| `TestRunItems_ErrorShapeSameSeqAndPar` (table: Sequential vs CPU, FailFast/Collect; same `*ItemsError` fields) | doc/behaviour mismatch fix |
| `TestRunItems_LabelGetsItemOnly_RaceFree` (`-race`, 64 workers) and `TestRunItems_CountersAtomic` | Label contract |
| `TestRunItems_WatermarkResume` (kill at random point, resume, every item done ≥ 1, prefix exact) | watermark |
| `TestRunItems_CPUBudgetShared` (two CPU runs ≤ cpu_slots concurrent fn), `TestParallelismCap_LowersDeclared` (a `core.tasks.kinds` entry for `books.dedup.full-scan`, set by file and by JSON env) | budget, operator cap (COR3-11) |
| `TestRunItems_PauseParksBeforeItemClock` (synctest) | pause/item timeout |
| `TestForEach_JoinsRunBudgetWhenCtxFromRC`, `TestForEach_WatermarkRefused` | COR-23 |
| `TestExclusiveRefs_OverlapNeverConcurrent` (`-race`; pack + single episode), `TestLockRefs_CrossRunSerialises`, `TestLockRefs_SortedNoDeadlock` | COR-7 |
| `TestArchtest_PassThroughParallelismAccepted` (typed field, required options field) and `_ZeroLiteralFlagged` | R7 pass-through |
| `TestResume_PolicyMatrix` (4 policies × {shutdown, crash, timeout, cancel}) | resume |
| `TestContinueInPlace_RequiresProgressAndCheckpoint`, `TestContinueInPlace_SameRunIDFreshTimeout` | yield |
| `TestCancel_GraceThenFenceRevoked`, `TestZombieCapBlocksModule` | cancel |
| `TestWriteSet_ResourceOverlapMatrix` (multi-ref sets), `TestConcurrencyKey_NamespaceEnforced` | G8 |
| `TestMode_OmittedIsPreviewForWriters`, `TestMode_ConflictingSpellings400`, `TestPreview_WriterRecordsIntentOnly` | dry-run |
| `TestApprove_RequiresVerifiedMethodAndPermission` (table: session, app-level interactive extension (CF Access), module compat method (refused), key naming the scope, key implying it via `core:admin`, legacy full-scope key, delegated session, forwarded header), `TestFixer_PlanRefAgeAndRowsOnly` | approval (SEC-24) |
| `TestStart_ViewOnlyDefRefusedAtRegister`, `TestStart_LegacyViewDefKeepsScanFloor` | SEC-9 |
| `TestSchedule_EnqueueAndClockOneBatch` (crash injection between), `TestSchedule_NoBootStampede`, `TestSchedule_CoalesceMissed`, `TestSchedule_DSTSpringForwardFallBack` (synctest, fake zone) | schedules |
| `TestScheduleFromConfig_UndeclaredKeyRefused`, `TestScheduleFromConfig_ForeignNamespaceRefused` (a core kind on `modules.x.…`, a module kind on another module's or a `core.` key, a key without the `.schedules.` segment, a hyphenated segment, a `duration`-typed key, one key on two kinds), `TestScheduleFromConfig_OwnerKeyAccepted` (`core.backup.schedules.create`, `modules.books.scan.schedules.full_sweep`), `TestScheduleFromConfig_HotReload`, `TestEvery_RefusedExceptRelay` | SIMP-6, R2-7 |
| `TestShutdown_DrainBoundedAndStampsInterrupted`, `TestShutdown_UninterruptibleExtendsOnceOnlyUnderNotify` | shutdown |
| `TestHeld_DisabledModuleRunsUntouchedAndBootSucceeds`, `TestHeld_LedgerGateResolves`, `TestHeld_MutationsRefused409` | COR-13 |
| `TestOutboxTriggered_StartInSameBatchAsCursor` (crash between), `TestOutboxTriggered_ModeFromConsumerOption` (no `Live()`/`Preview()` → registration fails; unset `Tx.StartRun` mode refused) | COR-9 hand-off, COR2-5 |
| `TestCoreRun_NeverCallsModuleWriter` (archtest: a `core.` kind's body reaches no module-registered write callback; module writes only under module kinds) | COR2-11 |
| `TestLockRefs_CtxFromRCJoinsTable`, `TestLockRefs_PlainCtxRefused` | COR2-16 |
| `TestModuleSystemActor_CannotStartUndeclaredKind` (foreign kind not in `TaskStarts` → 403; listed kind needing `core:system.manage` → 403), `TestTaskStarts_DisabledTargetModuleBootsAndRefusesAtRuntime` | SEC2-15, COR3-10 |
| `TestRunView_CurrentRedactedWithoutUsersView` (same `RunView` over `GET /runs`, the native stream and projection C, through `RunViewRedactor`) | COR2-2, COR3-13 |
| `TestStart_IdempotencyKeyHeaderReplays` (second POST → `200`, same run, `Idempotent-Replayed: true`), `TestOutboxTriggered_WritesNoIdemRecord` | SIMP2-8, SIMP3-8 |
| `TestOnTerminal_NotificationStagedInTerminalBatch` (crash between → neither or both) | COR2-3 |
| `TestS5Bridge_LegacyStreamsShowCoreRuns` (S5 build: streams A, B and C show live progress for a run of a kind with a `LegacyDefID`, executed by the core executor; FE4-10), `TestS5Bridge_RepairsRunHiddenFromViewer` (a Repairs run reaches no `library.view` subscriber of B; a kind with no `LegacyDefID` reaches no stream; stream A's `Current` blank) | FE2-3, COR3-9, FE3-12, SEC3-9 |
| `taskstest.StoresSuite` over `NativeStores` (core) and over books' legacy store with HEAD fixtures **(ABO)**: CAS transitions (every executor edge, so books' in-store legality check agrees, 5T.6.2), a commit-time precondition on every write, one row per run, per-run sequences, terminal-only `Prune`, every write staged into the caller's batch (a discarded batch leaves nothing; `Prune` excepted, it commits its own); suite name per 05-storage §5S.3.11 (SIMP5-9) | R4-1, SIMP4-2, R6-1, R6-2 |
| `TestLegacyProgressVsCancelKeepsState` and `TestLedgerCommitAfterTerminalRefused`, cases of `taskstest.StoresSuite`, so they run over both implementations (`synctest`: a progress tick read before an HTTP cancel commits after it → `ErrRunConflict`, the run stays `stopping`, and on books' store the `opv2:op:` row still says so after `Recover`; a `SaveChunk` read before a terminal transition → `ErrRunConflict`, no ledger, checkpoint or mark written) | R6-1, DATA6-1 |
| `TestConcurrentTransitionsOfDistinctRunsAllCommit` (N runs complete at once; every terminal transition commits, books' census row and native `Counts` exact) and `TestCancelCommitsUnderProgressTicks` (`synctest`: an HTTP cancel races a stream of progress ticks of the same run and commits; no tick reverts it), cases of `taskstest.StoresSuite` over both implementations | R7-3, DATA7-2 |
| `TestPruneUnderConcurrentCompletions` (`synctest`, a case of `taskstest.StoresSuite`: `Prune` deletes terminal runs while N other runs complete; every chunk commits under `RetryOnConflict`, books' census row stays exact, and a row that is no longer terminal on the re-read is skipped) | DATA8-8 |
| `TestStart_ConcurrentSameIdempotencyKeyOneRun` (two concurrent HTTP starts with one `Idempotency-Key`: one run is inserted, the other replays it with `Idempotent-Replayed: true`) | DATA8-9 |
| `TestScheduleParity_AllDefinitions` **(ABO, root CI, fake clock)** (for each of the 38 `TaskDefinition`s, config enabled and disabled: `cur`'s core scheduler plus books ticker and HEAD produce the same enqueue, start-run, maintenance-window reachability and clock-advance sequence; transcode and series-normalize never start themselves) | OPS8-4, DATA5-8 |
| `TestS5_FullAliasSetBoots` **(ABO)** (S5's whole metric-alias table, with every collector `metrics.Register()` still registers, passes `AddAliases`; no `operations_*` or engine-recorded `ops.*` family is live, 5T.11) | OPS8-1, MOD8-2 |
| `TestFixerMarksLandDuringQuiesce` (`synctest`, a case of `taskstest.StoresSuite`: a Fixer row applies while a stand-down quiesces the run to `stopping`; its mark commits, and on resume that row is skipped) | R7-3, DATA7-3 |
| `legacytest.TasksRunStoreSuite` **(ABO, over books' store)**: the `legacy.TasksRecoverer` case (5T.3.13), beside `taskstest.StoresSuite` | R6-3, SIMP6-6 |
| `TestStandDown_MarkerAtBootDefersQuiescedRun` (a hold's marker survives a crash; the next boot leaves the quiesced run `interrupted_quiesced`, clears the marker and warns; the boot after resumes it) and its **(ABO)** twin over HEAD's `registry.scan_standdown` row with a HEAD-written fixture | MOD6-4 |
| `TestLegacyTaskAliases` (06 owns it: a run HEAD stored as `diagnostics.export` resolves to `core.diagnostics.export` through `app.Options.LegacyTaskAliases`; a key that is not a registered `core.*` kind, or a value a module registers or lists in its `TaskLedger`, fails boot with `ProblemLegacyTaskAlias`), extended to one T4 kind | MOD6-6 |
| `TestLegacyRunTransitionVsCancelOneWins` **(ABO)** (`synctest`, books' store: an executor completion and an HTTP `runs/{id}/cancel` race on one run; exactly one commits, the other gets `ErrRunConflict`; the same for two `ClockStore.Advance` calls on one clock) | R5-4, DATA5-1 |
| `TestLegacyStepPreconditionsResolve` **(ABO)** (every books legacy step, 62, 65 and 66 included, reaches its body for the production store type; none can log "nothing to do" and be recorded as applied), `TestLegacyRunStoreRecovers` **(ABO)** (a stale v3 record beside its v2 row is rebuilt by `Recover` before the resume sweep; the run store asserts `legacy.TasksRecoverer` at compile time) | R5-2, MOD5-1, SIMP5-4, R6-3 |
| `TestNativeStores_RoundTrip` (every `RunStore`, `ClockStore`, `PauseStore` and `StandDownStore` method over `c/tasks/`; `hello.count` on a zero-module `coretest`) | SIMP4-3 |
| `TestLegacyStoreMissingFailsBoot` (a store recorded as legacy booted without `LegacyStores.Tasks`, and the reverse → `ErrLegacyStoreMissing`) | R4-1 |
| `TestOpsFamiliesWrittenOnlyByTaskstore` **(ABO)**, the ops instance of the umbrella `TestLegacyFamiliesWrittenOnlyByLegacyStores` (09 §9.2.1) (AST: every runtime write of the ops, clock, pause and stand-down families is inside books' store; legacy steps and the v1 carve-outs excepted; inside the store, every write of a non-guard ops family goes through the per-run staging handle that stages the guard `Expect`, 5T.6.2, R7-3) | R4-1, DATA4-5 |
| `TestOneSchedulerPerKind` **(ABO)** (no kind has both a core `Schedule` and an ABO scheduler entry; the ABO scheduler's clock writes go through `ClockStore`) | DATA4-5 |
| `TestDispatch_WaitsForReadySignal` (S5 build: a `books.*` run that reads books is not dispatched before `books.memdb` reports Up) | COR4-8 |
| `TestLegacyFormat_RoundTripWithHEAD` **(ABO)** (fixture store from HEAD: a HEAD-written run is resumed by core; a core-written run, checkpoint, clock and stamp are resumed and honoured by HEAD's reader; then core again over the same store) | G5, R4-1 |
| `TestSideRecordStaleAfterPrevWrite` **(ABO)** (HEAD rewrites a run row and advances a clock that carry extensions; on roll-forward both extensions read `ExtStale`, their fields at the stated defaults, no fire, neither `Suspended`; the next store write re-stamps them to `ExtCurrent`; an extension whose row was deleted is ignored and swept) | R4-1, DATA4-8, SIMP4-9, DATA5-4 |
| `TestLegacyFormat_RetryOnPrevRunsOnce` **(ABO)** (a core run interrupted, Retry on HEAD, roll forward: one row, one execution) | 5T.6 rule 1, DATA3-4 |
| `TestUnknownKindRun_DroppedByPrevVisibleAfterRollForward` **(ABO)** | 5T.6 rule 3 |
| `TestClock_InPlace_RollbackNoStampedeNoDoubleFire` **(ABO)** (new → old → new over one fixture store) | OPS-7, DATA-6 |
| `TestReqStamps_InPlace_HonouredOnPrev` **(ABO)** (a stamp written on cur is honoured on prev; R-test row) | OPS2-9 |
| `TestLegacySteps59to66_RunBeforeExecutorStart` (store at books v63, a pre-floor build), `TestUpgradeFloor_RefusedBelowBooks66` (floor-release build; store below `LegacyCap`) | DATA2-2, DATA4-19 |
| (deferred with the `Online` mode, SIMP5-7) 05-storage's `TestModuleOnlineMigrationRunsAsModuleKind` and `TestOnlineMigrationYieldsPastTimeout`, over a test module fixture (5T.3.12); one name each, the owner's (COR5-17) | COR4-5, COR4-6 |
| `TestLegacyHistory_ReadInPlaceUndecodableHidden` | history |
| `TestPerRunSeq_NoReissueAfterCrash` (`CrashClone` between row write and commit) | DATA-19 |
| `TestLegacyOpsWriters` **(ABO)** (successor of `TestOpStateWriters`; ratchet: the count of v1 checkpoint, result, summary and status write sites only shrinks, each with its target batch T2/T5/T7) | DATA-15, DATA2-12 |
| `TestNoGlobals_Tasks` (R5 AST), `archtest.ResumeContinueCheckpoints`, `archtest.RunItemsDeclareParallelism` self-tests | guards |
| `taskstest.Conformance(t, defs...)` — modules run it over their own defs (re-implemented from `pkg/ops/opstest/conformance.go`, 04 §8.6) | per-module |

## 5T.10 Config keys

`Protection` levels are 05-config's (§1.3.1; SEC2-8); `—` means `ProtectNone` (the section's write
permission, `core:settings.manage` for `core.*`, suffices). Nothing here reaches credentials, paths or
URLs; a schedule key's protection is declared by its owner with the key.

| Key | Type | Default | Hot reload | Protection |
|---|---|---|---|---|
| `core.tasks.workers` | int | 8 | no (restart) | — |
| `core.tasks.system_workers` | int | 2 (reserved for `PrioritySystem`) | no | — |
| `core.tasks.cpu_slots` | int | `NumCPU` | yes (resizes semaphore for new acquisitions) | — |
| `core.tasks.kinds` | `map[tasks.Kind]tasks.KindDial` (`{parallelism_cap: int}` per kind; env value is JSON) | `{}` = no caps | yes (next item dispatch; OPS-17 operational dial; COR3-11) | — |
| `core.tasks.shutdown_drain` | duration | 10s (a slice of `core.shutdown.budget`, 05-health) | yes | — |
| `core.tasks.shutdown_extend_max` | duration | 60s (systemd only) | yes | — |
| `core.tasks.min_checkpoint_interval` | duration | 30s | yes | — |
| `core.tasks.max_yields` | int | 1000 | yes | — |
| `core.tasks.zombie_cap` | int | 4 per module | yes | — |
| `core.tasks.standdown_lease` | duration | 2m | yes | — |
| `core.tasks.schedule_poll` | duration | 1m | no | — |
| `core.tasks.timezone` | IANA zone | server local | no | — |
| `core.tasks.windows.<name>.start` / `.end` | hour | `nightly`: 1 / 4 (current `maintenance.*` window, `config.go:3003-3005`) | yes | — |
| `core.tasks.windows.<name>.enabled` | bool | true | yes | — |
| `core.tasks.idempotency_ttl` | duration | 7d (HTTP start route only) | yes | — |
| `core.tasks.history_retention` | duration | 90d | yes | — |
| `core.<subsystem>.schedules.<name>` / `modules.<id>.<subsystem>.schedules.<name>` (declared by each kind's owner; 5T.3.8, R2-7) | `tasks.ScheduleConfig` | declared per kind by its owner | yes (re-read every poll) | declared by the owner (e.g. 05-updater's apply schedule is `ProtectAdmin`) |
| `modules.<id>.tasks.max_concurrent` | int | 0 = unlimited | yes | — |

There is no `core.tasks.legacy_history_window`: history is the run store's terminal rows (books' legacy
store: the ops families' own), aged out by `history_retention` through `RunStore.Prune` (SIMP2-12).

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
| `mediacore_tasks_dispatch_deferred_total` | counter | `reason` (key, writeset, ready, module_cap, zombie_cap, standdown, paused, held) |
| `mediacore_tasks_progress_age_seconds` | gauge | `kind` |
| `mediacore_tasks_zombies` | gauge | `module` |
| `mediacore_tasks_cpu_slots_in_use` | gauge | — |
| `mediacore_tasks_lane_in_use` | gauge | `lane` |
| `mediacore_tasks_ref_lock_wait_seconds` | histogram | `kind` |
| `mediacore_tasks_held_runs` | gauge | `module` |
| `mediacore_tasks_yields_total` | counter | `kind` |
| `mediacore_tasks_schedule_lag_seconds` | histogram | `kind` |
| `mediacore_tasks_schedule_missed_total` | counter | `kind` |
| `mediacore_tasks_schedule_errors_total` | counter | `kind` |
| `mediacore_tasks_undecodable_records_total` | counter | `family` (a closed set each store implementation declares) |
| `mediacore_tasks_store_extension_stale_total` | counter | `record` (`run`, `clock`, `stamp`); counts `ExtStale` reads (task records are never `Suspended`, DATA5-4) |

**One name mechanism (MOD8-2, OPS8-1).** HEAD's series names are kept for the compatibility window (09
§9.2.1) only by the gather-time alias rows of 05-metrics §13, never by parallel emission. An alias row
and a live family with its name cannot coexist (`AddAliases` fails closed with `ErrAliasConflict`), so
the PR that adds an alias deletes the ABO instrument it replaces:

- **T0+T1 PR:** the five `audiobook_organizer_operations_*` client_golang families of `internal/metrics`
  (with their helpers and their calls in the registry worker; 5T.8.2), and the OTel instruments of ABO's
  `opsmetrics` (`opsmetrics.go:216-250`) whose only recorder was the v2 engine: `ops.runs`,
  `ops.run.duration`, `ops.items`, `ops.inflight`, `ops.progress_age`, `ops.progress_timeout` and
  `ops.zombies`. `_operation_duration_seconds` aliases the histogram `mediacore_tasks_run_duration_seconds`,
  not a counter.
- **Later:** an instrument whose ABO recorder lives on keeps its ABO emission and gets no alias until that
  recorder converts: `ops.schedule_lag` and `ops.schedule_missed` (the ABO ticker, `v3_schedule.go:444,447`,
  until T7, whose PR deletes them and adds their aliases), and `ops.abandoned_calls`, which books keeps
  under its own name through its own observable (5T.8.1), so it has no alias.

`TestS5_FullAliasSetBoots` (09 S5; the stage instance of the seeded contract test) asserts that S5's whole
metric-alias table plus every legacy collector `metrics.Register()` still registers passes `AddAliases`.

## 5T.12 Open items

1. Owner decision per def for the 15 never-evaluated v2 crons (notably `deluge.protected-paths-sync`
   `*/30`): enable as a real schedule, or delete the declaration. Default in this spec: delete.
2. Should consistency-sensitive readers (reports) be gated against writers of the same resource?
   Today they are not.
3. `aijobs` (2 durable OpenAI batch types): task shape or separate durable type? Owned by 05-ai.
4. Remote lane protocol promotion to core when a second app needs remote workers (e.g. remote
   transcode for TV/Movies).
5. UNKNOWN: whether v2 rows written before the v3 projection existed were back-projected; books'
   legacy store's v2-row fallback in `List` covers both cases, but its cost depends on the answer.
6. (Closed: 06 has `Registrar.ChangeSink`, `Manifest.TaskLedger`, `Manifest.TaskStarts` checked at P7,
   and `Deps.Tasks` typed `tasks.Client`.)
7. (Closed by 05-rbac v1.4 §6.1 and its Tasks row: `core:tasks.manage` exists, may cancel any run and
   edit schedules, and the approval-method rule of 5T.3.7 (SEC-24) is stated there; SEC-9 and SEC-3 use
   its `PermissionSpec.Class` and `Confining`.)
8. (Closed by R4-1: rows 9, 11, 12, 13a and 13 of 05-storage §5S.9.1 stay books' with **no**
   `core/tasks` grant; books serves them through its legacy `tasks.Stores` (5T.6.2), and §5S.9.1 lists
   books' extension family. The post-DAG reconcilers have no tasks step.)

## Objections to 03

None open. 03 v1.4.0 adopted both v1.1.0 items: R7's wording (pass-through, `ForEach`, one
return shape, COR-23) and the `[a-z][a-z0-9-]*` task-kind segment grammar (COR-26).

## Out-of-scope defects found

1. `deluge.path-update` subscribes to `book.relocated` (`internal/plugins/deluge/path_update.go:49-53`),
   but nothing publishes that event and the registry never invokes `EventSubscription.Handler`
   (only `triggersToNames` reads `Triggers`, `registry.go:882,2330`): the Deluge path update never
   fires on relocation. Addressed by design in [05-events](05-events.md) (outbox-triggered task);
   the current behaviour is a bug independent of the extraction.
2. `OperationDef.Cancellable`'s comment claims the Cancel API rejects `false` (`types.go:52`);
   `Registry.Cancel` never reads it.
3. `RunItems` doc comment (`run_items.go:149`) contradicts the parallel return value (`:340-342,362-364`).

## Review history

Round 1 findings applied to this file (v1.1.0):

- COR-1 — outbox-triggered runs staged by `Executor.StartIn`; `Registrar.ChangeSink` still requested from 06 (open item 6).
- COR-7 — ref locks (`LockRefs`), `ExclusiveRefs` for multi-ref items, Fixer `Row.Subjects` exclusion, multi-ref write-set overlap.
- COR-8 — lifecycle steps reference 06 §6.5.2 as the single start/stop order; executor starts after `Bus.Open`.
- COR-9 — 5T.3.12: module outbox consumers are task runs that pass the write gate, ref locks and fence.
- COR-13 — 5T.3.11: computed `held` classification; ledger gate resolves held kinds; dispatcher, resume and scheduler skip them.
- COR-17 / SIMP-16 — `Resource` is now the alias `= entity.Ref`; resources, subjects and changes are `entity.Ref`; open item on `EntityRef` closed.
- COR-19 — layer stated as L4 per 03 v1.2.0.
- COR-20 — task topics `core.task.run-changed.v1`, `core.task.log-appended.v1`, `core.task.finished.v1`; topic permission and per-def visibility.
- COR-23 — pass-through parallelism accepted by the AST rule; `ForEach` for callers without `*RC`; one error name and one return shape.
- COR-26 — task-kind segment grammar kept; objection asks 03 to adopt it.
- DATA-2 — legacy-family ownership deferred to 05-storage §5S.9.1; claims needed listed.
- DATA-6 — boot reconcile of legacy runs lacking `migrated_to`; newer legacy clocks adopted at boot.
- DATA-15 — `opstate:` is a live books carve-out with a writer ratchet; history is aging-out, not read-only.
- DATA-19 — per-run sequence numbers written by the fenced owner in the row's batch; no shared allocator.
- DATA-27 — per-run adoption batches, page families read through, over-cap runs dropped with notice; boot never fails on adoption.
- OPS-7 — 05-tasks model chosen and owned here: clock and pause mirrors, reconcile, compat level and C5 contract referenced by 09.
- OPS-12 — batch table gains a behaviour-change/rollback column as input to 09's stage map.
- OPS-13 — shutdown premise corrected; drain is a slice of the one 05-health budget; extension only under systemd.
- OPS-17 — `core.tasks.kinds.<kind>.parallelism_cap` operator dial.
- SEC-3 — start route floor follows 05-rbac `AnyAuthenticated`/`Confining`.
- SEC-9 — view-class `Start` refused without `StartableByViewers`; legacy view-only defs keep the scan floor.
- SEC-24 — approval method rule aligned with 05-rbac (named scope, no legacy full-scope key, no delegated session).
- SIMP-6 — `ScheduleFromConfig` and `ScheduleConfig` as the one schedule mechanism and key form; `Interval` removed; sub-minute ticks listed.
- SIMP-7 — compat aliases and legacy windows reference 09 §9.2.1.
- SIMP-17 — `IO` merged into `Fixed(n, reason)`; `MaxFixed`; no `tasks.Network`.
- SIMP-21 — `Pipeline` shape dropped (amendment A7).

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-G request (COR-21) — `RunView` and `RunState` defined (states as used across this file); kernel stamps `StartRequest.Actor` on `Client.Start`.
- integration pass: R-H request (OPS-7) — §5T.6: the adopting boot records the adopt-step-applied record; only contract C5 raises the level (to 4).
- integration pass: Objections to 03 removed: both adopted by 03 v1.4.0 (R7 text, task-kind grammar).
- integration pass: Verified already present (R-F/R-D requests): `ScheduleFromConfig`, `Fixed(n, reason)`, `Resource = entity.Ref`, no `tasks.Network`, SEC-9 start floor with `AllOf(books:scan, books:view)`.

Round 2:

- R2-7 / COR2-6 / SIMP2-1 — one schedule-key rule: `ScheduleFromConfig` accepts any key of the registering owner of the form `<owner>.<subsystem>.schedules.<name>` (segments `[a-z][a-z0-9_]{0,47}`) declared with type `tasks.ScheduleConfig`, one kind per key; the `core.schedules.`-only rule is withdrawn; old interval/cron keys become `Convert` aliases; spec doc-lint; `Every` reserved for the relay.
- R2-8 / COR2-7 — 5T.4 references 06 §6.5.1 for boot phases; nothing writes before the checkpoint; BootBlocking data migrations via `tasks.ForEach`, only `Online` ones as executor runs.
- COR2-2 — `RedactRunView` (blanks `Current` without `core:users.view`) set as the catalog entry's `Redact` and used by `GET /runs`.
- COR2-3 — terminal notification staged through the kernel-wired `OnTerminal` hook into 05-notify's `Publish`; the bus publish alone notifies nobody.
- COR2-5 — outbox-run mode comes from the consumer's required `Live()`/`Preview()`; relay nudge is Live; intake scan removed from the non-task list.
- COR2-10 — outbox `Where` filter referenced in 5T.3.12.
- COR2-11 — rule: a core run never invokes module write callbacks; module kinds built by core helpers carry the writes.
- COR2-16 — `tasks.LockRefs(ctx, …)`; 10's grab named `Engine.Grab` on the executor ref-lock table.
- R2-7 (R-E) — `ScheduleConfig.Every` is `config.Duration`, so string defaults ("24h") decode; journal-prune example `core.log.schedules.journal_prune` owned by `logjournal`.
- COR2-11 (R-F) — `tasks.Stager`: kernel-provided `StartIn` for core services that stamps the owning module's system actor.
- DATA2-2 — `core/tasks#1` declares `After: books#66`; T8 keeps the legacy-step store methods until the minimum-upgrade-from release, then `ErrUpgradeFloor`.
- DATA2-12 — `op_result:`, `opsummary:`, `operation:` become books read-write carve-outs; `TestLegacyOpsWriters`.
- DATA2-13 — C5 runs a final copy-and-mark and clock adoption before raising the level; reconcile code deleted only from a build that refuses pre-C5 stores.
- DATA2-24 — tasks reconcile placed at 05-storage's post-DAG reconcilers step (d) in P4.
- DATA2-29 — T0+T1 row raises no level; `opv2:op:` read-write for copy-and-mark; bookkeeping key `c/storage/ks/core/tasks/applied/1`, step `core/tasks#1`.
- FE2-3 — legacy `registry.EventHub` bridge from T0+T1 until the S6 projections.
- FE2-18 — route table states that tasks has no `events` route.
- OPS2-2 — C5 subject to `RequiresSelfServed` (no contract on an auto-applied first boot).
- OPS2-9 — `req-migrate` copies only; new stamps mirrored to legacy keys; legacy delete and fallback removal move to C5.
- OPS2-22 / SIMP2-14 — T0+T1 row: adopt-step record, declares reader level 5 (4 before the integration-pass renumbering), adopt step stamps only `Floor` 4.
- SEC2-8 — Protection column in 5T.10.
- SEC2-15 — module system actor: own kinds plus `Manifest.TaskStarts`, never `core:admin`/`AuthRequired`.
- SEC2-16 — approval's verified method names app-level authenticator extensions only.
- SIMP2-8 — the task-start route copies `Idempotency-Key`; replay returns the existing `RunView`.
- SIMP2-12 — `core.tasks.legacy_history_window` removed.
- SIMP2-15 — verified: 05-tasks already owns `parallelism_cap` (09's spelling is 09's fix).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): §5T.6 and T0+T1 row: `core/tasks#1` stamps `Floor` 4 (Teach build S4 r3); S5 declares reader level 5; C5 raises `core` to 5; test row updated (09 v1.2.0 renumbering).
- integration pass (round 2): Audit names per 05-rbac §7.1; reconcile cites 06 P4d step 4; `module.Registrar.Tasks`.

Round 3:

- R3-1 — §5T.6 rewritten: runs, checkpoints, pages, clocks, pause marker and `book`/`book_file` `Requires` stamps read and written in place in the ops families' legacy format through one logged `core/tasks` legacy-format grant (adapter = HEAD's opv2/opv3 store code as `legacystore`); core-only `c/tasks/` keeps only native data; removed `core/tasks#1`, the `Floor` 4 stamp, the tasks Teach build, copy-and-mark/`migrated_to`, boot reconcile, clock/pause/stamp mirrors, `req-migrate`, `c/tasks/adopt/`, adoption errors/metric/tests and C5; added `Common.LegacyDefID`, G5 rules 1–5, "no level step" and "Deferred: cutover".
- COR3-5 — module `Online` data migrations run as kernel-registered module kinds `<module>.datamigrate.<name>` (5T.4 step 0, 5T.3.12).
- COR3-7 — derived build kind via `storage.DerivedBuildTask` (module-relative name, Title, Effects, Perms, 24 h timeout, Parallelism) in 5T.3.12.
- COR3-8 — `tasks.Stager` is the one core→module starter; AI kind `<module>.ai.batch-apply.<name>`; `Engine.Grab(ctx, l, d)` takes the caller's `RefLocker`.
- COR3-9 / FE3-12 / SEC3-9 — S5 legacy-stream bridge is a books bus subscriber feeding A, B and C, forwarding only kinds with a `LegacyDefID` under that ID, stream A redacted with a nil checker; tests.
- COR3-10 — `TaskStarts` checked at P7 against registered kinds plus every manifest's `TaskLedger`; disabled target refused at runtime.
- COR3-11 — operator dial is one map key `core.tasks.kinds` (`KindDial{ParallelismCap}`).
- COR3-13 — `Executor` lifecycle methods are `Run`/`Shutdown`; `RunViewRedactor() events.RedactFunc` added.
- COR3-15 — stale "still to add to 06" line deleted; open item 6 closed.
- DATA3-4 — superseded by R3-1 (no copy, no `migrated_to`); one row per run makes HEAD's same-row Retry safe; additive-field default rule and tests from its evidence.
- DATA3-10 / DATA3-11 / OPS3-20 — superseded by R3-1: no C5, no `req-migrate`, no reconcile-code deletion rule.
- OPS3-7 — T8: no C5 dependency; only the books-66 floor, checked by the updater against the store's recorded state.
- FE3-6 — aliases render legacy bodies through 05-http-api `LegacyPath.Shape`; no `WithLegacyShape`.
- SIMP3-8 — the outbox relay sets no `IdempotencyKey` and writes no `c/tasks/idem/` record; "second guard" removed.
- SIMP3-9 — "What is not a task" scoped to durable work; library-internal timers (OTel reader, batch span processor) excluded.

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): handlers move to `core/internal/taskhttp` (was `tasksapi`); the derived build helper is `datamigrate.DerivedBuildTask` (L4), `storage.DerivedHooks` has no build; open items 7 and 8 closed (satisfied by 05-rbac and 05-storage v1.4).

Round 4:

- R4-1 / SIMP4-2 — `tasks.RunStore`, `ClockStore`, `PauseStore` and `Stores` (5T.3.13) are the only persistence path; `NativeStores` over `c/tasks/` in core; books supplies its legacy implementation (HEAD's ops store code, unchanged, in ABO) through `app.Options.LegacyStores.Tasks` with its own claims; grant, `legacystore`, `legacyhist`-in-core and every ABO key name removed from core text; `ErrLegacyStoreMissing`; cutover re-introduces a read-delete grant.
- SIMP4-3 — 5T.6.1 native store table (runs, queue, indexes, census, pages, resume state, debounce, strikes, clocks, pause, stamps), landing at S5; `TestNativeStores_RoundTrip`.
- SIMP4-7 — the books store's history reads the v1 results, summaries, statuses and log; the v1 checkpoint family is neither read nor written by it.
- SIMP4-9 / DATA4-8 — native-only fields only in the books extension family under 05-storage's extension-record rule (`legacy_fp`, suspension, orphan sweep); "current again on roll-forward" deleted; `TestSideRecordStaleAfterPrevWrite`; suspension metric.
- DATA4-5 — every runtime write of the ops families through books' store from T0+T1 (`TestOpsFamiliesWrittenOnlyByTaskstore`); ABO's scheduler ticks the 38 definitions until T7 through `ClockStore`, one scheduler per kind (`TestOneSchedulerPerKind`); T8 row aligned with 5T.6.
- OPS4-5 / DATA4-18 — "minimum-upgrade-from release" replaced by the floor release (first T8 release: deletes the legacy-step code, declares the floor) and the stepping-stone release (newest production tag at the T8 merge, `InstallFirst`); `TestStepSteppingStoneValid`; pre-S0 updaters named.
- DATA4-19 — the floor is `UpgradeFloor{Version: LegacyCap}`.
- COR4-5 — kernel-registered data-migration kinds are attribution-only (no per-shard `Subjects`, outside the write-set conflict rule, `ConcurrencyKey` `<module>.datamigrate`); owner writes protected by `Serialized` + preconditions; test reworded.
- COR4-6 — synthesised data-migration definition specified (Title, Resume, 24 h Timeout with yield, `Perms.Start`, parallelism, implicit `TaskLedger`, `<name>` grammar); `TestOnlineMigrationYieldsPastTimeout`.
- COR4-8 — dispatch waits through the readiness core of `health`, landing with `tasks` at S5 (`core.storage`, `books.memdb`); `TestDispatch_WaitsForReadySignal`.
- FE4-10 — bridge test is `TestS5Bridge_LegacyStreamsShowCoreRuns`, worded for a `LegacyDefID` kind.
- OPS4-6 — prev-binary one-format tests are in the root `make ci` requirement.
- OPS4-11 — checkpoint-shape and unknown-kind rollback consequences point to 09 §9.2.1's table.

Integration pass (round 4):

- integration pass (round 4): §5T.3.13 and 5T.6.2/T0+T1: `module.Manifest.LegacyStores.Tasks` (never `app.Options`), `LegacyStoresFunc` takes `storage.LegacyHandles` like every other constructor; dispatch conflict rule excludes the kernel-registered data-migration kinds; adapted op defs translate their atom through `Catalog.LegacyReach` (`settings.manage` → `core:admin`); `TestOpsFamiliesWrittenOnlyByTaskstore` is an instance of 09's umbrella ratchet.

Round 5:

- R5-4 / DATA5-1 — `RunStore` comment: an implementation keeps preconditioned and extension-annotated families `Serialized`; 5T.6.2 states the CAS change (HEAD's `opsMu` → staged `GetForUpdate`/`Expect`), lists the rows 05-storage §5S.9.1 marks `Serialized` (not `CheckedWrites`), native table note, error row, 5T.7 bullet; `TestLegacyRunTransitionVsCancelOneWins`.
- R5-2 / MOD5-1 — 5T.6.2 "Package direction": taskstore → remaining store only; legacy steps 62/65/66 bodies and the ops part of `InitializeStore` move to taskstore; capability assertions become direct calls; `TestLegacyStepPreconditionsResolve`; 5T.8.1 row.
- SIMP5-4 — `RunStore.Recover` removed from the frozen interface; optional `tasks.Recoverer` detected at P4c, asserted at compile time by books' store, `TestLegacyRunStoreRecovers`; no other legacy-only member in `tasks.Stores`.
- COR5-12 — `Prune` and `Recover` commit their own batches; the "every write stages into the caller's batch" rule states the exception.
- COR5-11 — "no boot reconcile" reworded to "no P4d reconcile" (HEAD's reconcile is `Recover`).
- COR5-7 (reference) — 5T.4 step 2: from S2b to S4 books' start hook runs `ReconcileOpsV3WithV2`; it moves to `Recover` at T0+T1.
- DATA5-8 (DATA4-5 residue) — the books ticker is the only scheduler of the 38 definitions from T0+T1 to T7 and is never stopped early (5T.3.8, 5T.6.2); T0+T1 done-check "every one of the 38 definitions fires on the staging store within one interval".
- DATA5-4 — task extension fields are all Derived, none Widening: stale reads take defaults, are never `Suspended`, and the next write re-stamps them; metric renamed `mediacore_tasks_store_extension_stale_total`; error row and test reworded.
- DATA5-16 — `legacy_fp` → `legacy_ref` = `{key, fp}` (whole-row projection).
- MOD5-4 — books' `ClockStore`/`PauseStore` source named (key prefix, RFC3339 codec, `taskV2DefIDs`, pause key and marker cut into taskstore/books); `core/internal/scheduler` is a re-implementation, not a move or copy.
- MOD5-11 — `opsbridge` is the `internal/operations/registry` path re-implemented in place, not a new package.
- COR5-4 / SEC5-7 / SEC5-8 / SEC5-9 — adapted defs' `Perms.Start` is `rbac.LegacyAtom(atom)`: roles via `LegacyReach`; a legacy `abk_` key without extension by HEAD's intersect semantics; the auth-disabled principal via the expansion (HEAD's op checks pass with auth off, an `auth_off: pass` site), so no op-start tightening.
- SIMP5-7 — kernel-registered data-migration kinds, the dispatch exemption and their tests are a deferred build (05-storage §5S.3.14); 5T.3.12 and 5T.4 step 0 reworded.
- COR5-17 — the data-migration tests use 05-storage's names over a test module fixture, not hello.
- SIMP5-9 — `taskstest.StoreSuite` → `taskstest.StoresSuite` (05-storage naming rule); `TestNativeStores_RoundTrip` kept.
- integration pass (round 5): 5T.3.7 permissions table — in-handler `auth.Can` sites become `rbac.LegacyCheck(atom, readSide)` (denied to the auth-disabled principal), distinct from adapted defs' `rbac.LegacyAtom` (R-D request; SEC5-9, SEC5-14); 5T.6.2 — taskstore matches `storage.ErrClosed` instead of reaching HEAD's `errStoreClosing` through the residue (04 §8.6).

Round 6:

- R6-1 / DATA6-1 — every `RunStore` write is checked at commit (`GetForUpdate` on the run read: books' `opv3:run:` record and paired `opv2:op:` row, native `c/tasks/run/<id>`), not only `Transition`/`Insert`/`Advance`; on `ErrRunConflict` a progress tick is dropped, a ledger commit/checkpoint/row/mark is refused unless the run is still `running` under the same attempt and fence (5T.7); books' row 11 `CheckedWrites` from T0+T1 with steps 62/65/66, `Recover` and `Prune` converted; error rows; `TestLegacyProgressVsCancelKeepsState`, `TestLedgerCommitAfterTerminalRefused` in `taskstest.StoresSuite` and the T0+T1 gate.
- R6-2 / MOD6-1 / MOD6-9 — `internal/operations/state` stays in ABO next to taskstore (taskstore imports it); 5T.6.2 "Run states" maps each `RunState` to HEAD's stored `state.V3` form and keeps HEAD's in-store legality check; core's executor machine is a re-implementation; opsgen keeps its HEAD input and is not repointed, so `ops.ts` stays byte-identical for that reason (5T.8.1).
- R6-3 / SIMP6-2 / SIMP6-6 — `tasks.Recoverer` → `legacy.TaskRecoverer` and `tasks.LegacyStoresFunc` → the field `legacy.Stores.Tasks` over `legacy.Handles` (06 §6.4.11), outside the API promise; the executor calls the capability through an unexported twin checked by `export_test.go`; `legacytest.TasksRunStoreSuite` with the recoverer case; 5T.1's stale `app.Options` route corrected.
- OPS6-1 — floor-release guard reworded: the S0 updater hotfix and the `deploy` recipe compare the new binary's `--print-upgrade-floor` with `<db>.storage-applied` before the swap; the S8 updater also checks the signed manifest (5T.6.2, T8 row).
- MOD6-4 — `tasks.StandDownStore` (`Stores.StandDown`): books over HEAD's `registry.scan_standdown` row and codec, native `c/tasks/standdown/<key>`; the marker is persisted while a hold lives and consulted at boot (5T.3.7, 5T.4 step 2); error row and `TestStandDown_MarkerAtBootDefersQuiescedRun`.
- MOD6-6 — core kinds never spell an ABO op ID: their legacy aliases come from `app.Options.LegacyTaskAliases`, merged at P7; a `FormerKinds` entry outside `core.` in core fails R1(c)/(h); `TestLegacyTaskAliases` (06) extended to a T4 kind.
- MOD6-12 — `books/taskwrite` replaced by `internal/books/contract` (interface) and `internal/books/writer` (implementation).
- MOD6-2 — `TestLegacyStepPreconditionsResolve` is an external test in `internal/books`, so it compiles after the split.
- SIMP6-7 — `RunQuery`, `RunFilter` and `ListQuery` defined (`RunStore.List` takes stored kinds); the undocumented `Legacy` marker and HEAD's non-existent `legacy: true` removed.
- DATA6-14 — task extensions declare `Hash` over the whole `opv3:run:` record or clock row (no per-tick field there); "default guarded projection" removed.

Round 7:

- R7-3 / DATA7-1 — write checking is per guard row: `CheckedWrites` only on `opv3:run:`, `opv2:op:`, `opv3:meta:counts`; every other run family `Serialized` only and written only through taskstore's per-run staging handle under the guard `Expect` (`TestOpsFamiliesWrittenOnlyByTaskstore`); `DeleteRange` refused in a checked range, allowed in a `Serialized`-only one; native `Serialized` limited to guard keys (run, debounce bucket, clock); `RunStore` comment, 5T.6.1, 5T.6.2, error rows.
- R7-3 / DATA7-2 — `Insert`/`Transition` batches rebuilt under `storage.RetryOnConflict` while `want` matches, `ErrRunConflict` only when it does not; `c/tasks/count/` deleted, native `Counts` from `c/tasks/ix/state/`; one per-run writer (5T.7); `TestConcurrentTransitionsOfDistinctRunsAllCommit`, `TestCancelCommitsUnderProgressTicks`.
- R7-3 / DATA7-3 — drop predicate is HEAD's closed-ledger rule (non-terminal under the same `Attempt`/`FenceEpoch`, `stopping` included); only a bare tick dropped on any state change; `TestFixerMarksLandDuringQuiesce`.
- R7-3 / DATA7-8 — task extension writes state their `Expect` (`Put`'s `seen`; conditional `legacy.Extension.Delete(ctx, b, …, seen)` for orphans).
- DATA7-15 — T0+T1 gate: zero persisted blind-write violations on the guard rows for ≥ 14 days before the strict release; `TestT0T1RunRowsCheckedWrites` in both modes.
- R7-8 / MOD7-1 — registry engine, `childop`, `opsmetrics`, `opsv3` and `opstest`'s state-using files are re-implementations (04 §8.6 rows); executor written fresh, ABO registry files stay as opsbridge until T8 with no `origins.txt` line; T8 row reworded.
- R7-8 / MOD7-2 — new `internal/operations` row: v1 checkpoint helpers → `internal/books/contract`, `BulkMetadataFetchParams` → `internal/server`, only `progress.go`/`trigger_source.go` → `tasks`; `legacy_op_status.go`/`legacy_backfill.go` stay in opsbridge (books subscriber on `core.task.finished.v1`, backfill op; direct calls, no capability assertion); activity mirror a books subscriber on `core.task.log-appended.v1`; v1 families table updated.
- R7-8 / MOD7-3 — `ClockStore` row: `maintenance_window_last_run` is the maintenance-window kind's clock with HEAD's `YYYY-MM-DD` date codec; full-sweep key named.
- MOD7-8 (c) — the residue keeps the pure ops-family key predicate taskstore may call and exports `RecoverPebbleClosed`, `PrefixEnd`, `PrefixUpperBound`, `OpChangeKey` and the version-source adapter.
- MOD7-13 — `core/internal/executor/state` and `…/writer` replaced by "files of `mediacore/internal/executor`"; fencing (`fence.go`) → public `tasks`, opswriter's other files → `internal/books/writer`.
- COR7-2 / SIMP7-1 / R7-7 — the recoverer mirror test is `internal/executor`'s external `legacy_mirror_test.go` over an `export_test.go` alias.
- SIMP7-5 — the capability is detected once, by the executor; "records what it found" deleted.
- SIMP7-8 — `legacy.TaskRecoverer` → `legacy.TasksRecoverer` (Go initialism of the owner package).
- SIMP7-6 — `storage_extension_suspended` reference replaced (owners' own tokens).
- R7-6 / OPS7-3 — T0+T1 schedule done-check runs on the scrubbed, egress-denied staging copy with schedules running.
- integration pass (round 7): `TasksRecoverer` comment cites 05-storage for former names (it said "then legacy.TasksRecoverer"); 5T.8.1 `pkg/ops` row and 5T.9: the whole of `pkg/ops/opstest` is re-implemented in `taskstest`, its Book fakes in `internal/books/store/storetest` (R-H request); `Extension.Delete` signature, `maintenance_window_last_run` codec and MOD7-13 executor files checked: already applied.

Round 8:

- COR8-7 — `RunStore` comment and `Transition` doc: `Insert`/`Transition` return `ErrRunConflict` at staging and never retry; the caller that builds the batch re-runs build-and-commit under `RetryOnConflict` (5T.7).
- DATA8-2 — 5T.6.2: the per-run non-guard families are `Serialized` and `Guarded` (no per-key `Expect`, no stripe; `DeleteRange` with no stripe), so `ReplaceOpRowsPrefix` holds only the guard's stripe, as the kept sentence says.
- DATA8-8 — `op:batch:` and `opv3:sched:` are per-key guards outside the per-run handle; `Prune` and `Recover` commit each chunk under `RetryOnConflict`, re-reading each row's guard; `TasksRecoverer` comment; `TestPruneUnderConcurrentCompletions`.
- DATA8-9 — `c/tasks/idem/` is `Serialized`; `Client.Start` stages it under `Expect(absent)`, the 5T.7 retry replays; expiry deletes with an `Expect`; `TestStart_ConcurrentSameIdempotencyKeyOneRun`.
- MOD8-9 — `ClockStore`'s date codec: `Get` returns local midnight, `Advance` writes the local date; `StoresSuite` case with `TZ` west of UTC.
- OPS8-4 — "every one of the 38 definitions fires on staging" replaced by `TestScheduleParity_AllDefinitions` (normative) and a staging smoke, in 5T.6.2 and the T0+T1 gate.
- OPS8-1 — the T0+T1 PR deletes the five `operations_*` client_golang families, their helpers and worker calls with their aliases; `TestS5_FullAliasSetBoots`; `_operation_duration_seconds` aliases the duration histogram.
- R8-6 / MOD8-2 — `childop` and `opsmetrics` ABO files stay at their path as opsbridge-side code until their last ABO user (listed) is gone and are deleted with opsbridge at T8; `opsmetrics`' one core destination is `mediacore/internal/executor`; books' abandoned gauges get their own observable; 5T.11 keeps one name mechanism (05-metrics §13 aliases, no parallel emission), each alias landing in the PR that deletes the ABO instrument it replaces.
- MOD8-7 — 5T.8.1 registry row: the stale "11 of the 19 files, about 90 references" replaced by a pointer to 04 §8.6's generated figures.
- MOD8-8 — the activity mirror moves at T0+T1 into `internal/activity`, so T8's deletion of opsbridge does not take it (5T.8.1, T0+T1 and T8 rows).
- SIMP8-3 — `TasksRecoverer` comment: "the kernel asserts only the capabilities 03 §3.3 lists".
- integration pass (round 8): 5T.6.2 names the flag `storage.Claim.Guarded` (R-B request; `idem/` `Serialized` (DATA8-9), the per-key guards for `op:batch:` and `opv3:sched:` and the `Prune`/`Recover` retry (DATA8-8) were already applied); 5T.8.1 registry row states 04 §8.6's generated figures (30 engine files, 14 using run-row types, 39 code references to `state` in 5 files; R-H request).
