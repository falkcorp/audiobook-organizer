<!-- file: docs/specs/platform-core/05-tasks.md -->
<!-- version: 1.6.0 -->
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
`/api/core/v1/tasks` API and the persistence **interfaces** `tasks.RunStore`, `tasks.ClockStore` and
`tasks.PauseStore` (5T.3.13, R4-1), with a **native** implementation over core's own `c/tasks/`
keyspace, the one new apps, `examples/hello` and `coretest` use (5T.6.1). An app with pre-core run data
passes its own **legacy** implementation of the same interfaces through `app.Options`
([06](06-app-sdk.md)): ABO's is books code over ABO's existing ops families in their existing format,
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
| start | `Perms.Start`, plus the route floor `rbac.AnyAuthenticated` as 05-rbac defines it (a non-`Confining` scope; a confined credential reaches a definition only when `Perms.Start` names its scope explicitly, SEC-3). `Perms.Start` may not be satisfiable by a view-class scope unless the definition sets `StartableByViewers` (SEC-9: today's `scan.trigger` floor exists because a viewer could otherwise start a `library.view`-only def, `opsv3/handler.go:236-246`; `bulk-apply-preview` is one, 4 h timeout, `internal/server/bulk_apply_preview.go:269,278`). An adapted legacy def's `Perms.Start` is the one legacy-atom requirement value `rbac.LegacyAtom(atom)` (05-rbac §6.3; the same value a legacy route's `s.perm` check uses, COR5-4), which each checker resolves its own way: **(a)** a role principal, and a key with an extension record, through `Catalog.LegacyReach(atom)` (R4-3), not the atom's expansion, so a def declaring `settings.manage` (the old op-def default) requires `core:admin`; **(b)** a legacy `abk_` key **without** an extension record passes iff the atom is in the key's stored atoms **and** its owner's role checker satisfies `LegacyReach(atom)`: HEAD's `intersect(role atoms, key atoms)` (`internal/server/middleware/auth.go:364-365`), with the row rule's dominance on the role side, so a key scoped `["settings.manage"]` keeps starting the ops it starts at HEAD (SEC5-7); **(c)** the auth-disabled principal through the atom's expansion (05-rbac §4.11), because HEAD's op start checks pass every request with auth off (`internal/server/wire_ops_v3_routes.go:96-98`; an `auth_off: pass` site, SEC5-9), so with auth off such a def starts as it does at HEAD and no auth-off tightening is recorded for op starts (SEC5-8). Adapted legacy defs declaring only `library.view` translate to `rbac.AllOf(books:scan, books:view)`, which keeps the floor | closed |
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
after `core.tasks.standdown_lease` (default 2 m) without renewal.

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

// RunState is the stored state of a run. Terminal: succeeded, failed,
// timed_out, canceled, interrupted_dropped. "held" is not a state (5T.3.11).
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
// builds it once at P4c (06 §6.5.1): from module.Manifest.LegacyStores.Tasks
// when a module supplies one (ABO: books code, 5T.6.2), else NativeStores over
// c/tasks/ (5T.6.1). Core code reaches runs, clocks and the pause marker only
// through these interfaces and never names a stored key.
type Stores struct {
	Runs   RunStore
	Clocks ClockStore
	Pause  PauseStore
}

// LegacyStoresFunc is the type of module.Manifest.LegacyStores.Tasks (06
// §6.4.2). The kernel calls it once at P4c with the declaring module's OWN
// handles (storage.LegacyHandles: its Legacy range over its own claims and its
// extension family), as for every other LegacyStores constructor: no grant is
// involved (R4-1). It is called also while that module is disabled, so held
// runs stay readable (5T.3.11). The returned stores read and write only inside
// h.
type LegacyStoresFunc func(h storage.LegacyHandles) (Stores, error)

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
// is Prune (and the optional Recoverer below): maintenance that commits its
// own chunked batches and never joins a multi-owner commit (COR5-12).
// Preconditions (Transition, Insert's uniqueness) are checked at commit by
// GetForUpdate/Expect over the implementation's own Serialized range, never by
// a lock held between calls. An implementation may therefore keep runs only in
// families that are Serialized: c/tasks/ for the native store, and for a
// legacy store every family it writes under a commit-time precondition or
// annotates with an extension record, declared Serialized in the claims table
// (05-storage §5S.9.1; R5-4).
type RunStore interface {
	// Runs, queue, census, history.
	NewID(ctx context.Context) (RunID, error)
	Insert(ctx context.Context, b *storage.Batch, r RunRecord) error // record + queue entry + indexes + census
	Get(ctx context.Context, id RunID) (RunRecord, error)            // ErrRunNotFound
	Transition(ctx context.Context, b *storage.Batch, id RunID, want Expect, next RunRecord) error // ErrRunConflict
	Progress(ctx context.Context, b *storage.Batch, id RunID, done, total int, current string) error
	Queued(ctx context.Context) iter.Seq2[RunRecord, error]     // dispatch order: priority, then enqueue time
	Unfinished(ctx context.Context) iter.Seq2[RunRecord, error] // the resume sweep's input
	List(ctx context.Context, q RunQuery) iter.Seq2[RunRecord, error] // includes terminal history; Legacy marks rows an older run system wrote
	Counts(ctx context.Context) (map[RunState]int64, error)

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

	// Maintenance: commits its own batches (above).
	Prune(ctx context.Context, before time.Time, limit int) (deleted int, err error) // terminal runs only, chunked
}

// Recoverer is an OPTIONAL capability of a RunStore (SIMP5-4): a legacy store
// whose previous binaries can leave records inconsistent implements it, and the
// executor calls Recover once per boot before the resume sweep (5T.4 step 2). It
// commits its own batches. The kernel detects it by type assertion on
// Stores.Runs at P4c; the native store does not implement it. It is not part of
// the frozen RunStore contract, so it goes with the legacy store at that
// family's cutover (05-storage §5S.3.11 "Legacy capabilities"). Because an
// assertion that fails is silent, a legacy store that needs it asserts it at
// compile time (books: var _ tasks.Recoverer, 5T.6.2) and its owner tests it.
type Recoverer interface {
	Recover(ctx context.Context) error
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
HEAD fixtures (5T.9). The suite covers only the frozen contract; `Recoverer` is tested by its
implementor. `tasks.Stores` carries no other legacy-only member: every method above has a meaning
for the native store (SIMP5-4).

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
   def snapshots `c/tasks/def/<kind>` (with `FormerKinds`), and runs the op-ID ledger gate (current
   `opCatalogGate`, `internal/startup/steps.go:110-111`): a ledger line that resolves to no def,
   alias, tombstone **or held module** (5T.3.11) fails boot.
2. **Start** (`Executor.Run`, the executor step of 06 §6.5.2: after module hooks and workers and after
   `Bus.Open`, so events from the resume sweep reach subscribers): `PauseStore.Get` →
   `Recover`, when `Stores.Runs` implements the optional `tasks.Recoverer` (books' legacy store runs
   HEAD's every-boot `ReconcileOpsV3WithV2` there, `internal/database/store.go:1860-1870`, ABO code
   unchanged, writing only its own families before the resume sweep acts on them; the native store
   does not implement it). From S2b until this executor lands (S5) the same repair has another home:
   books' start hook runs `ReconcileOpsV3WithV2` before it builds HEAD's ops registry (05-storage
   §5S.9.3, COR5-7), and it moves to `Recover` in the T0+T1 PR → resume sweep over
   `RunStore.Unfinished` (5T.3.6; held runs skipped) → debounce reload → dispatcher (100 ms tick +
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
| Legacy store's extension record whose fingerprint no longer matches its row (`ExtStale`: an older build rewrote the run row or clock while rolled back), or whose row is gone (`ExtOrphan`) | open: stale, each extension field (all Derived) at its stated default, counted; the next store write of that row re-stamps it and it reads `ExtCurrent` again; never `Suspended`, because no task field is Widening (05-storage §5S.3.11, DATA5-4); an orphan is ignored and deleted by the next write or `Prune` (5T.6.2) | R4-1; no task extension field grants access or starts work |
| The store's recorded backend for `core/tasks` differs from this build's choice (a build without the app's legacy store on a legacy store, or the reverse) | closed: boot fails with `ErrLegacyStoreMissing` naming `core/tasks` (05-storage §5S.3.11) | an empty native keyspace must never stand in for the legacy runs (R4-1) |
| `RunStore.Transition` precondition lost at commit | open: `ErrRunConflict`; the caller re-reads (the dispatcher's claim simply loses; of an executor completion and an HTTP cancel of one run, exactly one commits) | CAS transitions (5T.7) |
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
| `c/tasks/idem/<kind>/<key>` | HTTP start idempotency records (TTL `core.tasks.idempotency_ttl`; SIMP2-8), staged in the batch of the run's `Insert` |

### 5T.6.1 Native store (`c/tasks/`): apps without legacy runs

`tasks.NativeStores` is what new apps, `examples/hello` and `coretest` use (SIMP4-3). It lands with
`tasks` at S5: under 03 §3.4 rule (b) `hello` and `coretest` are core consumers, and `hello.count` runs
from S5 (06). Values are `codec.JSON[T](1)` of the core type.

| Key | Content |
|---|---|
| `c/tasks/run/<run_id>` | `RunRecord` (Serialized: `Transition` and `Insert` check at commit) |
| `c/tasks/q/<priority>/<enqueued>/<run_id>` | dispatch queue entry, written only with its run row; `PrioritySystem` sorts first |
| `c/tasks/ix/kind/<kind>/<run_id>`, `c/tasks/ix/state/<state>/<run_id>`, `c/tasks/ix/done/<finished>/<run_id>` | `List`, the resume sweep and retention |
| `c/tasks/count/<state>` | census counters (Serialized) |
| `c/tasks/page/<run_id>/{evt,intent,item,log}/<seq or key>` | per-run pages |
| `c/tasks/ckpt/<run_id>/<name>`, `c/tasks/chunk/<run_id>/<n>`, `c/tasks/row/<run_id>/<row_id>`, `c/tasks/mark/<run_id>/<row_id>` | resume state, Fixer rows and marks |
| `c/tasks/debounce/<kind>/<bucket>`, `c/tasks/strike/<run_id>/<n>` | debounce buckets, watchdog strikes |
| `c/tasks/sched/<kind>` | `Clock` (Serialized) |
| `c/tasks/pause` | `Pause` |
| `c/tasks/req/<module>/<kind>/<id>/<taskkind>`, `c/tasks/req-rev/<taskkind>/<module>/<kind>/<id>` | `Requires` stamps and their reverse index |

The families marked Serialized are exactly those a method writes under a commit-time precondition
(the run row for `Insert`'s absence and `Transition`'s compare-and-set, the census, the clock), the
native form of R5-4; the others are written only in the batch of a precondition-checked row. `NewID` returns a ULID; the
native run store does not implement `Recoverer`; `Prune` deletes a terminal run with all its rows in
one batch. `TestNativeStores_RoundTrip` and `taskstest.StoresSuite` cover it.

### 5T.6.2 Books' legacy store (ABO code, not core)

The books module passes `module.Manifest.LegacyStores.Tasks` (06 §6.4.2, §6.11.6), implemented by an ABO package below the books
composition root (`internal/books/taskstore`, name open; 04, R4-5). It implements `tasks.Stores` with
HEAD's own ops storage code over books' own claims — the families 05-storage §5S.9.1 rows 9, 11, 12, 13
and 13a list — in HEAD's format. Nothing moves into core and nothing is re-implemented there, so no
second copy of the ops-format code exists at any stage (SIMP4-2), and HEAD's format invariants hold by
construction because it is HEAD's code: every run write commits the v3 record **and** its paired v2 row
in one batch (`internal/database/pebble_store_ops_v3.go:28-45`, owner decision D25), which is what every
older build reads. This subsection specifies ABO code; no key name it touches appears in core.

**What changes in HEAD's code: the compare-and-set mechanism (R5-4, DATA5-1).** HEAD's run CAS is a
mutex around read, compare and its **own** commit (`opsMu` in `TransitionRunV3`,
`internal/database/pebble_store_ops_v3.go:1029-1041`). Behind the interface the method stages into the
caller's batch, so the mutex no longer spans the commit; the store instead stages
`GetForUpdate`/`Expect` on the run row (and, for `Advance`, on the clock key) and the precondition is
checked at commit. `Expect` is admitted only in `Serialized` ranges, so every family this store
writes under a precondition or annotates with an extension record is `Serialized` in 05-storage
§5S.9.1: row 9's clock and pause keys and its clock sub-prefix, row 11, the queue and index
sub-prefixes of row 13 that `Insert`/`Transition` touch — declared `Serialized`, **not** `CheckedWrites`,
because books' legacy steps 62, 65 and 66 still write row 11 blind in P4b. `TestABOClaimsMatchOwnershipTable`
cross-checks each family served through `tasks.Stores` against that rule, and
`TestLegacyRunTransitionVsCancelOneWins` (`synctest`, books' store: an executor completion and an HTTP
cancel of one run race; exactly one commits, the other gets `ErrRunConflict`) proves the result.

**Package direction (R5-2, MOD5-1).** Imports run `internal/books/taskstore` → the remaining store
package, never the reverse (an ABO guard assertion), so the split is no import cycle. In the T0+T1 PR
taskstore takes, besides HEAD's ops storage files: the bodies of books' legacy steps 62 and 65, step
66's body (`pebble_store_plan_legacy.go`, which writes ops rows and so belongs to the one writer of
those families), and the ops part of `InitializeStore` (it becomes `Recover`). The remaining store
exports what taskstore uses (`recoverPebbleClosed`, `prefixEnd`, `errStoreClosing`) and replaces
`rejectOpv2OpRawKey` with its own pure key predicate over the ops family prefixes; `opsV2TimelineTrusted`
moves onto the taskstore type. Books' legacy step list keeps its order by calling steps 62, 65 and 66
through funcs `internal/books` wires from taskstore. Every capability assertion on a moved method
(`store.(hollowOpsSweeper)`, `store.(opsV3Reconciler)`, `internal/database/migrations.go:1238-1244`,
`store.go:1867`) becomes a direct call, because once the methods leave `*PebbleStore` an assertion fails
silently: step 62 and 66 would log "nothing to do" and be recorded as applied without running, and the
every-boot reconcile would be skipped. `TestLegacyStepPreconditionsResolve` **(ABO)** asserts that
every legacy step reaches its body for the production store type (no "nothing to do" path), and
`var _ tasks.Recoverer = (*taskstore.RunStore)(nil)` plus `TestLegacyRunStoreRecovers` (a stale v3
record beside its v2 row is rebuilt on the next boot) pin the optional capability.

| Interface | Books' implementation (HEAD code, HEAD format) |
|---|---|
| `RunStore` records, queue, indexes, census, trails, intents, items, log | HEAD's native v3 run path with the paired v2 rows (`internal/database/iface_ops_v3.go:110` `RunRecord`, `internal/database/iface_ops_v2.go:39` `OperationV2Row`); `RunState` ↔ `state.V3` and the v2 status through HEAD's `state.FromV2`/`ToV2` (`internal/operations/state/v3.go:289,344`); `Priority` Low/Normal/High ↔ 0/1/2 (`internal/operations/registry/types.go:329-331`), `PrioritySystem` ↔ 3, which sorts first in HEAD's `999-priority` queue key (`internal/database/pebble_store_ops_v2.go:63`); `NewID` is HEAD's op-ID generator |
| checkpoints, chunk ledger, item-key snapshot, Fixer rows and marks, debounce, strikes | HEAD's v3 checkpoint, ledger, snapshot, row and row-mark methods and the v2 batch-bucket and strike methods; per-run pages are written where HEAD reads them |
| `Stamp`/`Stamped` | for `books/books` and `books/book_files` subjects: HEAD's completion and dependency-revision stamps (`pebble_store_ops_v2.go:1188`), library-scale and never copied; any other subject: the extension family below |
| `List` (history) | v3 rows, falling back to the v2 row for a run without a projection (open item 5), merged with the v1 history HEAD's v1 reader returns (`legacy: true`): the results, summaries and statuses written by `CreateOperationResult`, `SaveOperationSummaryLog` and `UpdateOperationStatus`, and the v1 log. The v1 checkpoint family that `operations.SaveCheckpoint` writes is not history: the store neither reads nor writes it (SIMP4-7) |
| `Prune` | HEAD's terminal-run retention, plus the v1 log; never a non-terminal row |
| `Recover` (the optional `tasks.Recoverer`, 5T.3.13) | HEAD's every-boot `ReconcileOpsV3WithV2` (`internal/database/store.go:1860-1870`), unchanged: it rebuilds v3 records a previous binary left stale against their v2 rows and writes only those families; a failure is logged and the next boot retries, as at HEAD (`store.go:1864-1866`) |
| `ClockStore` | HEAD's v3 schedule state (`SchedState`, `iface_ops_v3.go:281-290`) for v3 kinds; for the 38 `TaskDefinition`s, the scheduler's settings clocks (an RFC3339 last-due time per task under the setting prefix `intervalLastTickPrefix`, `internal/scheduler/interval_clock.go:50-60`, plus the full-sweep and maintenance-window clocks), task name ↔ def ID by `taskV2DefIDs` (`internal/scheduler/maintenance.go:154-175`). Source (MOD5-4): the key prefix and the RFC3339 encode/decode are cut out of `interval_clock.go` into taskstore, and `taskV2DefIDs` into books as data passed to taskstore and to the books ticker; the tick logic stays with the books ticker (deleted at T7) and is **re-implemented**, not moved, in `core/internal/scheduler` (5T.8.1), so no ABO setting key or op ID travels into core |
| `PauseStore` | HEAD's operator pause marker: the setting key `pauseSettingKey` and the `pauseMarker` struct (`internal/operations/registry/pause_gate.go:52-60`) are cut out into taskstore; the gate logic goes to the executor without them (MOD5-4) |

**Native-only data: one extension family (R4-1, SIMP4-9).** A field HEAD's row has no place for — a
clock's `Missed`, `GateClosedAt`, `LastRun`, `LastFire` and `ScheduleHash`, a run field HEAD's records
lack, a stamp of a non-book subject — is never written additively into a legacy row: HEAD decodes rows
into typed structs with no catch-all field and drops unknown members on its next rewrite
(`pebble_store_ops_v2.go:336-380`), and extra content in a legacy row is the class of change SEC4-3
showed unsafe. Books' store keeps such fields in a **books-owned extension family that no older build
reads** (05-storage §5S.9.1 lists it), under 05-storage §5S.3.11's extension-record rule: one record per
legacy row, keyed by that row's key, holding no field the row holds, written in the same batch as the
row, carrying `legacy_ref` = `{key, fp}` (the one name, DATA5-16), whose `fp` covers the whole row: the
default guarded projection, since no task row field is a volatile usage timestamp and every one can
change what the extension's fields mean. Every field of a task extension record is **Derived** and
none is **Widening** (05-storage §5S.3.11's classes): none grants access, widens a permission or starts
work. Each record `Put` also carries the `Expect` on the legacy row the store read, which its
`Serialized` family admits (R5-4, above). On read:

- `ExtCurrent` (fingerprint matches): the extension applies;
- `ExtStale` (an older build rewrote the row while rolled back): each field takes its stated default
  (`Missed` 0, `GateClosedAt` zero, an unknown `ScheduleHash` re-derives the next instant from
  `LastDue` without firing; run fields their zero value), counted
  (`mediacore_tasks_store_extension_stale_total`) and logged once. Because the record has no Widening
  field it is never marked `Suspended` (05-storage §5S.3.11, DATA5-4): the next write of that row
  through the store re-stamps it under the current fingerprint and it reads `ExtCurrent` again, so no
  admin step exists or is needed and health's `storage_extension_suspended` never counts a task record
  (contrast 05-authn and 05-rbac, whose Widening fields carry access and need an audited confirmation);
- `ExtOrphan` (row absent): the extension is ignored, and deleted by the next write of that key or by `Prune`.

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
same rule, and the T0+T1 done-check includes "every one of the 38 definitions fires on the staging
store within one interval" (5T.8.2).

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
| v1 results, summaries and statuses — **live books writers** at `748fd787a`: `CreateOperationResult` (`internal/database/pebble_store_operations.go:438-445`; 15 sites in 9 files, e.g. `internal/server/metadata_ops.go:391,440,1056,1105`), `SaveOperationSummaryLog` (`internal/maintenance/jobs/backfill_book_files.go:197`, `recompute_book_aggregates.go:265`, `backfill_file_hashes.go:1440`), `UpdateOperationStatus` (`internal/operations/registry/legacy_op_status.go:244`, `legacy_backfill.go:218`) | books carve-outs, read-write (DATA2-12); read by the books store's `List` (`legacy: true`). Each writer moves to the run's result or summary in the batch that makes its job native (T2, T5, T7); `TestLegacyOpsWriters` ratchets the 20 sites down to zero, after which `Prune` clears the rest |
| v1 log | read by `List`; deleted by `Prune`; never written |
| v1 checkpoints — checkpoints and cursors of **live** books jobs: `operations.SaveCheckpoint` (`internal/operations/state.go:60`) from `internal/itunes/service/importer.go:626,636,725,732`, `internal/dedup/drain_stale.go:324`, `internal/maintenance/jobs/recompute_book_aggregates.go:230`, `internal/maintenance/jobs/backfill_file_hashes.go:1315`, `internal/server/metadata_ops.go:1326`; the hash-backfill chunk journal (`backfill_file_hashes.go:722-775`); the metadata-upgrade cursor (`internal/metabatch/upgrade.go:162,199`); deletes in `retention_and_hygiene.go:339-374` | **books carve-out, read-write** (DATA-15); neither read nor written by the store (SIMP4-7). Each writer moves to `rc.Checkpoint` or a books-keyspace cursor in the batch that makes its job native (T2, T5, T7); `TestLegacyOpsWriters` ratchets the write sites down to zero |
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
(exit 78), and an S0+ updater refuses to apply the floor release to it (05-updater 5U.4, OPS3-7). A
pre-S0 updater swaps any newer binary unchecked (`internal/updater/updater.go:214-280`), so the floor
release's note and the Docker last-line message name the stepping stone.

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
  admitted because the run families are `Serialized` (R5-4, 5T.6.2).
- Cancellation propagates by context cause to every worker within one item boundary (5T.3.2).
- No package-level mutable state (R5): pause gate, CPU budget, ref-lock table, metrics, def registry
  are fields of the executor instance; two `App`s in one process do not share them.
- `held` is computed from the manifest set at boot; it never changes while the process runs, because
  enablement is restart-bound (06 §6.5.3).

## 5T.8 Migration from ABO

### 5T.8.1 What existing code becomes

| ABO today | Becomes |
|---|---|
| `internal/operations/registry` (dispatcher, worker, watchdog, resume, shutdown, batch runner, chunk ledger, fence, deps, debounce, pause, stand-down, warm-up) | `core/internal/executor` (cleaned of `database`, `auth`, `config.AppConfig`, `logger` imports, `raw/06` §7.1); the pause gate's logic moves, its setting key and marker struct (`pause_gate.go:52-60`) go to taskstore's `PauseStore` (MOD5-4). The path itself stays in ABO as the v2 adapter `opsbridge` until T8 (5T.8.2) |
| `internal/operations/state` | the v3 machine → `core/internal/executor/state`; the v2 ↔ v3 status mapping (`FromV2`/`ToV2`) stays in ABO with books' legacy store (5T.6.2) |
| `pkg/ops` (Task/Batch/Fixer, Effects, Schedule, contracts, RC, Approval, opstest; `Pipeline` dropped, A7) | `core/tasks`, `core/tasks/taskstest` (from `pkg/ops/opstest`), with A1–A7 |
| `pkg/ops/writer.go` typed methods + `internal/operations/opswriter` | gate → `core/internal/executor/writer`; typed `ModifyBook*` → `books/taskwrite` over `tasks.Writer` |
| `internal/operations/opmode` | core start path (5T.3.7) |
| `internal/operations/childop` | `rc.Await` |
| `internal/walkresume` | `rc.ShouldYield` + `ErrContinueInPlace` + `RunItems` `Watermark` |
| `internal/scheduler` (interval/daily/cron/window/full sweep/v3 loop) | `core/internal/scheduler` is a **re-implementation** (a 04 §8.6 entry, not a move or a copy; MOD5-4): no file of `internal/scheduler` moves into core, because its clock encoding (`intervalLastTickPrefix`, RFC3339) and `taskV2DefIDs` are ABO key names and op IDs. At T0+T1 those two parts are cut out into taskstore's `ClockStore` and books (5T.6.2); the rest of the ABO path keeps ticking the 38 definitions through `Deps.Tasks` and that `ClockStore` until T7, when their bodies become `Schedule` declarations in `books` and the ABO path is deleted |
| `internal/repairs` (Fixer framework) | `tasks.Fixer`; the 24 fixers → `books` Fixer defs |
| `internal/maintenance` (`MaintenanceJob`, registry) | deleted after T2; jobs → `books` Task defs |
| `internal/opscatalog`, `pkg/plugin/sdk`, `addOpRegistrar`, `serviceregistry` op wiring | `module.Registrar.Tasks` |
| `internal/opsmetrics` | executor metrics (5T.10) |
| `internal/lifecycle` | `core/lifecycle` (03 §3.3, L0) |
| `internal/server/handlers/opsv3`, v1 operations handlers | `core/internal/taskhttp` (05-http-api §5) |
| `internal/database` ops storage (`pebble_store_ops_v2.go`, `pebble_store_ops_v3*.go`, `ReconcileOpsV3WithV2`) and HEAD's v1 history reader | **stays in ABO** as books' legacy `tasks.Stores` (`internal/books/taskstore`, 5T.6.2; `ReconcileOpsV3WithV2`, the ops part of `InitializeStore`, is its `Recover`, the v1 reader feeds its `List`), with the bodies of legacy steps 62, 65 and 66 (`pebble_store_plan_legacy.go` included) moved in the same T0+T1 PR; imports run taskstore → the remaining store, never the reverse (R5-2, MOD5-1); nothing moves into core and core has no copy (R4-1, SIMP4-2). The code only books' legacy steps 62, 65 and 66 call is deleted by the floor release (T8) |

### 5T.8.2 Batches

Every batch is one or more PRs, each green on `make ci` in both module dirs; each keeps every kind
working (adapter first, native later). Def IDs map to kinds by prefixing the owning module:
`dedup.full-scan` → `books.dedup.full-scan` with `FormerKinds: ["dedup.full-scan"]`; core-owned
generic ops get `core.*` kinds. The op-ID ledger (`internal/server/op_id_aliases_test.go`) is the
check that no stored row strands. The last column is this file's input to 09's batch → stage map
(OPS-12); 09 assigns the stage.

| Batch | Content | Count | Gate to finish | Behaviour change / rollback |
|---|---|---|---|---|
| T0 + T1 (one release) | `core/tasks` + executor + scheduler land; ABO adapter `opsbridge` (the `internal/operations/registry` path re-implemented in place as the v2 adapter, not a new package; MOD5-11) wraps every v2 `OperationDef` as a core Task with `LegacyDefID` = its def ID (`Run(ctx, params, Reporter)` called with a `Reporter` implemented over `RC`; legacy `registry.RunItems` re-implemented over `tasks.RunItems` with `Fixed(n, "legacy Concurrency")` from its non-zero `Concurrency`, or `Sequential("legacy zero Concurrency")`); `/api/v3/ops` aliases; books' legacy `tasks.Stores` (`internal/books/taskstore`: HEAD's ops store code and v1 history reader behind the interfaces, 5T.6.2) passed as `module.Manifest.LegacyStores.Tasks`, with every runtime write of the ops families routed through it; ABO's scheduler keeps ticking the 38 `TaskDefinition`s through `Deps.Tasks` and the store's `ClockStore` until T7; the readiness core of `health` with `books.memdb` (5T.4 step 2); the books-side legacy-stream bridge (5T.4 step 3) | all 233 defs run on core | op-ID ledger green; resume/cancel/shutdown suites green on core; `taskstest.StoresSuite` green on both implementations; `TestOpsFamiliesWrittenOnlyByTaskstore`, `TestOneSchedulerPerKind`, `TestDispatch_WaitsForReadySignal`, `TestLegacyRunTransitionVsCancelOneWins`, `TestLegacyStepPreconditionsResolve`, `TestLegacyRunStoreRecovers`; every one of the 38 definitions fires on the staging store within one interval (DATA5-8); G5 rollback test (5T.9) | yes: executor swap (CPU budget, cancellation fix) over the **same** stored format; no level step, no Teach build; rollback = binary swap under the G5 rules of 5T.6 |
| T2 | 34 `MaintenanceJob`s → native `books.maintenance.*` Tasks (mechanical: `Run(ctx, JobStore, ProgressReporter, dryRun)` `internal/maintenance/job.go:498` → `TaskSpec.Run(rc, p)`; `Policy()` → `Common`); their v1 checkpoint writers move to `rc.Checkpoint` and their v1 result and summary writers to the run's result (`TestLegacyOpsWriters` ratchet) | 34 | `internal/maintenance` deleted | no, when kind, params and effects are unchanged (binary swap); a v1-checkpoint → `rc.Checkpoint` move resumes from zero once after a rollback, listed per job in 09 §9.2.1's rollback table (OPS4-11) |
| T3 | 24 repairs fixers → `tasks.Fixer`; `repairs.plan/apply` removed | 24 (+2 defs) | Fixer conformance suite | no (binary swap) |
| T4 | Generic ops → `core.*` (activity-log cleanup/compaction family, backups prune, db optimize/census, temp/trash/reports cleanup, throttle reset, tombstone cleanup, retention-and-hygiene, metrics TTL sweep): the `generic` class rows of `raw/06` §6, each re-checked in [04-package-mapping](04-package-mapping.md) (some, e.g. `opchange-book-index-rebuild`, are book-bound despite the class) | ≤ 24 | owner per kind recorded in 04 | no; kind renames ride `FormerKinds` (binary swap) |
| T5 | Plugin defs native, per plugin: acoustid 8, dedup 32, metafetch 3, iTunes 7 (with the 4 `importer.go` v1 checkpoint writers), Deluge 4 (→ 05-download adapter tasks) | 54 | `pkg/plugin/sdk` unused | no (binary swap) |
| T6 | Maintenance plugin's 104 defs, in sub-batches of ≤ 15 by file group | 104 | — | no (binary swap) |
| T7 | Server-package ops (`internal/server/*_op*.go`, incl. `metadata_ops.go`'s v1 checkpoint writer) and `scheduler.metadata-upgrade` (its v1 cursor) → `books`; 38 TaskDefinitions → `ScheduleFromConfig`, each conversion deleting its books ticker entry in the same PR (`TestOneSchedulerPerKind`); 15 v2 crons → none (ledger) | ~49 + 38 | `internal/scheduler` deleted from ABO | yes: schedule keys move (aliases for the window); crons deleted (none ever ran) |
| T8 (the **floor release**, 5T.6.2) | Delete `OperationDef`, `opsbridge`, v2 RunItems and the v2 registry code nothing has called since T0+T1; delete `runitems_concurrency_lint_test.go` (replaced by `archtest.RunItemsDeclareParallelism`); delete the ABO code only books' legacy steps 62, 65 and 66 call (`MigrateOpsV2ToV3`, `SweepHollowOperationsV2` and their store methods) and declare books' `UpgradeFloor{Version: LegacyCap, InstallFirst: <stepping-stone release>}` (05-storage §5S.3.5; DATA2-2). The first release that contains T8 **is** the floor release; the stepping-stone release is the newest production tag when the T8 PR merges and contains none of it (OPS4-5) | — | grep: no `registry.` import in ABO; `TestUpgradeFloor_RefusedBelowBooks66`; `TestStepSteppingStoneValid` (root CI) | no format change (binary swap); a boundary only for stores below `LegacyCap`: boot refuses them with `ErrUpgradeFloor{InstallFirst}` (exit 78) and an S0+ updater refuses to apply the release (05-updater 5U.4, OPS3-7); a pre-S0 updater swaps it unchecked, so the release note and the Docker last-line message name the stepping stone (DATA4-18) |

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
| `taskstest.StoresSuite` over `NativeStores` (core) and over books' legacy store with HEAD fixtures **(ABO)**: CAS transitions, commit-time preconditions, one row per run, per-run sequences, terminal-only `Prune`, every write staged into the caller's batch (a discarded batch leaves nothing; `Prune` excepted, it commits its own); suite name per 05-storage §5S.3.11 (SIMP5-9) | R4-1, SIMP4-2 |
| `TestLegacyRunTransitionVsCancelOneWins` **(ABO)** (`synctest`, books' store: an executor completion and an HTTP `runs/{id}/cancel` race on one run; exactly one commits, the other gets `ErrRunConflict`; the same for two `ClockStore.Advance` calls on one clock) | R5-4, DATA5-1 |
| `TestLegacyStepPreconditionsResolve` **(ABO)** (every books legacy step, 62, 65 and 66 included, reaches its body for the production store type; none can log "nothing to do" and be recorded as applied), `TestLegacyRunStoreRecovers` **(ABO)** (a stale v3 record beside its v2 row is rebuilt by `Recover` before the resume sweep; the run store asserts `tasks.Recoverer` at compile time) | R5-2, MOD5-1, SIMP5-4 |
| `TestNativeStores_RoundTrip` (every `RunStore`, `ClockStore` and `PauseStore` method over `c/tasks/`; `hello.count` on a zero-module `coretest`) | SIMP4-3 |
| `TestLegacyStoreMissingFailsBoot` (a store recorded as legacy booted without `LegacyStores.Tasks`, and the reverse → `ErrLegacyStoreMissing`) | R4-1 |
| `TestOpsFamiliesWrittenOnlyByTaskstore` **(ABO)**, the ops instance of the umbrella `TestLegacyFamiliesWrittenOnlyByLegacyStores` (09 §9.2.1) (AST: every runtime write of the ops, clock and pause families is inside books' store; legacy steps and the v1 carve-outs excepted) | R4-1, DATA4-5 |
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
| `taskstest.Conformance(t, defs...)` — modules run it over their own defs (from `pkg/ops/opstest/conformance.go`) | per-module |

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

The ABO OTel names `audiobook_organizer.ops.*` (`opsmetrics.go:212-250`) are emitted in parallel
for the compatibility window (09 §9.2.1) for dashboard continuity, then removed.

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
