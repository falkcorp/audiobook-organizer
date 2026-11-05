<!-- file: docs/specs/platform-core/05-tasks.md -->
<!-- version: 1.13.7 -->
<!-- guid: 3472a3a5-c492-47aa-9b0b-17411cfb25d3 -->
<!-- last-edited: 2026-11-05 -->

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
`tasks.PauseStore` and `tasks.StandDownStore` (5T.3.13, R4-1), with one implementation, the **native** one over core's own `c/tasks/` keyspace
(5T.6.1), which every app, `examples/hello` and `coretest` use. An app with pre-core run data moves it into
the native store **once**, in its own cutover step, through `tasks.Importer` (5T.3.13): ABO's is books' tasks
cutover step M4 of the R2 release (5T.8.3), which decodes HEAD's ops families, imports them and deletes them.
Core holds no ABO key name or row format and no storage grant over those families (R4-1, 03 R1(c)). It does
**not** own:
domain writers (modules),
the outbox (`core/outbox`, [05-events](05-events.md), which *uses* tasks for its relay and whose
module-side consumers are task runs, 5T.3.12), SSE transport ([05-realtime](05-realtime.md)),
permission model ([05-rbac](05-rbac.md)), books' family ownership ([05-storage](05-storage.md) §5S.9.1).

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
	Ready           []ReadySignal  // readiness to wait for before dispatch; nil = {SignalStorage}; ReadySignal = health.ComponentID (5T.4)
	Lanes           []LaneID       // lanes items may be handed to (5T.3.9)
	Subjects        SubjectsFunc   // optional per-run write set (5T.3.4)
	Dedupe          DedupePolicy   // queued-run dedupe/merge (v2 DedupeQueuedRuns/MergeQueuedParams)
	Debounce        *Debounce      // v2 Batchable/BatchWindow/BatchMaxWait
	Yields          bool           // may be quiesced by a stand-down (requires ResumeContinue), 5T.3.7
	Notify          Notify
	// There is no alias field (D10): a Kind is the one name its runs, clocks
	// and Requires stamps are stored and labelled under. A rename is a data
	// migration of the owner's that rewrites the stored kind (5T.8.3).
}

// DedupePolicy decides whether a start folds into an existing run of the same
// kind instead of inserting a new one: HEAD's DedupeQueuedRuns,
// DedupeSameActorOnly and MergeQueuedParams (internal/operations/registry/
// types.go:99-125). The zero value is HEAD's default: a start folds into a
// queued or running run of the kind whose params are byte-identical. A start
// that folds inserts nothing and returns the existing run's RunRef.
type DedupePolicy struct {
	AnyParams     bool // fold regardless of params (v2 DedupeQueuedRuns)
	SameActorOnly bool // fold only into a run of the same actor and permission set (v2 DedupeSameActorOnly)
	// Key, when set, replaces the params rule: a start folds only into a QUEUED
	// run whose Key(params) is equal (for example a job ID, 05-ai's batch
	// apply); a running run is never folded into, so a start made while it runs
	// queues behind it under the dispatch conflict rule. "" from Key = never fold.
	Key func(params json.RawMessage) (string, error)
	// Merge may union an incoming start into an existing QUEUED run (v2
	// MergeQueuedParams); never called for a running or claimed run.
	Merge func(existing, incoming json.RawMessage) (json.RawMessage, bool, error)
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

**Dedupe at start.** `Client.Start`, `Stager.StartIn` and `Executor.StartIn` decide a fold from a read of
the kind's unfinished runs inside `WriteRuns` (5T.7). Two concurrent starts can both miss and both queue,
which the kind's `ConcurrencyKey` or write set then serialises, so a kind deduped by `Key` must be idempotent
(05-ai's apply is). A scheduled start folds as 5T.3.8 says.

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
prefix is not the registering module; duplicate kinds; any constructor problem; `Perms.Start` outside `<module>:` or `core:`; a `Perms.Start` satisfiable by
a view-class scope without `Perms.StartableByViewers` (5T.3.7, SEC-9); `Effects.Writes` naming
another module's resource; an undeclared resource kind; `Resume == ResumeContinue` on an `Unordered`
Batch source; a zero `Parallelism` on Batch/Fixer; both `PartitionBy` and `ExclusiveRefs` on one
Batch; a Fixer without `Approval`; a scheduled writer whose schedule says neither `Live()` nor
`Preview()` (v3 rule D8, `pkg/ops/schedule.go:96-118`); a `ScheduleFromConfig` key that the
registering owner has not declared with type `tasks.ScheduleConfig` in its own config namespace, or
that another kind already uses (5T.3.8, R2-7); an `Every` schedule on any kind but
`core.outbox.relay`; a `TaskStarts` entry (5T.3.10) naming a kind that no manifest in the build declares. That
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
type RunID string // opaque; assigned by RunStore.NewID (5T.3.13); an imported run keeps HEAD's op ID (5T.8.3), so app rows that name it still resolve

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
across the re-check and the CAS write ([10](10-tv-movies-proof.md), a design sketch (non-normative) that owns it; COR3-8), so a grab and a module's `ExclusiveRefs` items interlock on one table; there is no
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
| Checkpoint | `rc.Checkpoint(v)` JSON-encodes into the run's checkpoint record (`RunStore.SaveCheckpoint`, 5T.3.13); throttled by `core.tasks.min_checkpoint_interval`; written in the same storage batch as the progress record. Batch/Fixer checkpoint through the store's chunk ledger and item-key snapshot (`SaveChunk`). An imported run (5T.8.3) carries HEAD's checkpoint bytes: the converted kind's `Restore` decodes HEAD's shape, or its cutover converter rewrites it, and a run whose checkpoint neither can read resumes from zero once (counted, WARN). |
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
| start | `Perms.Start`, plus the route floor `rbac.AnyAuthenticated` as 05-rbac defines it (a non-`Confining` scope; a confined credential reaches a definition only when `Perms.Start` names its scope explicitly, SEC-3). `Perms.Start` may not be satisfiable by a view-class scope unless the definition sets `StartableByViewers` (SEC-9: today's `scan.trigger` floor exists because a viewer could otherwise start a `library.view`-only def, `opsv3/handler.go:236-246`; `bulk-apply-preview` is one, 4 h timeout, `internal/server/bulk_apply_preview.go:269,278`). A converted ABO kind's `Perms.Start` is the native scope requirement that 05-rbac's atom table (§6) maps HEAD's def permission to, so the identity cutover's reach oracle (allow and deny before = after, 05-rbac §12) covers op starts too; the auth-disabled principal is decided by 05-rbac §4.11. A HEAD def that declares only `library.view` converts to `rbac.AllOf(books:scan, books:view)`, which keeps the scan floor | closed |
| cancel | the run's actor, or `Perms.Cancel`, or `core:tasks.manage` | closed |
| approve / decide (`awaiting_decision`) | `Perms.Approve` (decide on a run interrupted under ResumeAsk: `Perms.Approve` if set, else `Perms.Start`) **and** a verified method, the same rule as 05-rbac §4.10 (SEC-24): an interactive session, a sign-in through an app-level authenticator extension whose `MethodSpec.Interactive` is true (only `app.Options.Authenticators` may set it, 06 §6.4.5, SEC2-16; ABO's Cloudflare Access is one; a module's `Registrar.AuthMethod` never is), or an API key whose scopes **name** the required scope explicitly (not via `core:admin` or another implying scope; the identity cutover gives a migrated all-atom or admin key each such scope explicitly, so its HEAD approval reach is kept, 05-rbac §6). Refused: a delegated session (`MethodSessionDelegated`) and a forwarded header, which is never identity (current rule, `opsv3/runs.go:395-410`) | closed |
| pause/resume executor, edit schedules | `core:tasks.manage` | closed |

The `core:tasks.view` and `core:tasks.manage` scopes are in the fixed `core:*` catalog that F2 declares, whichever
subsystem is wired ([05-rbac](05-rbac.md) §5), so the identity flip F4 can write them into migrated roles before
the tasks flip F5 serves them. Every approve, decide, cancel-by-other and schedule edit writes an `audit` record, named per [05-rbac](05-rbac.md)
§7.1: `core.task.approve` / `core.task.reject` (approve and decide), `core.task.cancel` (cancel by another
actor); a schedule edit is a write of its config key and is recorded as `core.settings.update`.

**Mode / dry-run contract** (moves `internal/operations/opmode` into the core start path):

1. The start request carries mode in the **envelope**, not in params: `"mode": "preview" | "live"`.
   There is no `dry_run`/`dryRun` spelling (D10): a start body that carries either key at the top level
   or inside `params` is refused with `400 tasks.mode_field_unsupported`, so a stale caller fails
   loudly instead of running in a mode it did not ask for. The first-party callers switch in the PR
   that deletes HEAD's routes, the tasks flip F5 (5T.3.10).
2. Omitted mode → **Preview** for writers, Live for `ReadOnly` defs (owner rule 2026-09-25,
   `dryrun.go:6-10`). Maintenance jobs' per-job default (`ResolveDryRunDefault`, `dryrun.go:60-66`)
   is expressed as `ReadOnly` for the read-only jobs, so the special case disappears.
3. A Preview run gets an intent-recording Writer; its result is `{result, intents[]}`; a Fixer
   Preview is the plan (rows). Params structs carry no `DryRun` field; a converted def reads
   `rc.Mode()` (5T.8.2).
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
// "modules.books.scheduler.schedules.library_scan_full".
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
through `ScheduleFromConfig`. There are no schedule-key aliases (D10). A former interval or cron key
(`core.backup.schedule`, `core.ai.batch_poll_interval`, …) is converted **once** by the config cutover (M2,
05-config's rename table) into the new key's value: a duration `d` → `{enabled: d > 0, every: d}`, so
"0 disables" becomes `enabled: false`. HEAD's 16 `scheduled.<entry>` entries (`config.go:991-1046`) are
converted the same way, each by HEAD's own reading of the entry, into `modules.books.scheduler.schedules.<entry>`,
which books declares for the kind the entry fires (05-config's rename table; 5T.8.2's map names the kind).
Every key M2 writes is registered in the build that runs M2: books declares its `modules.books.scheduler.schedules.*`
keys with its config section from the config flip F3, and every `core.*` section, schedule keys included, is
registered dark from F2 (IMPL-1), although the kinds that read them register at F5 or at their owner's flip. The
one entry whose op becomes a core kind, `db_optimize`, also seeds that owner's key `core.storage.schedules.compact`,
and its books key drives the books half `books.maintenance.aux-store-compact` (5T.8.2). Non-timing tuning keys
are not schedule keys; the rename table moves them like any other key. Spec CI carries a doc-lint: every `ScheduleFromConfig("…")` literal in
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
Conversion (5T.8.2 group T7) turns each of the 15 declared crons into **no schedule** and lists it
in the R2 release note, because none of them ever ran unattended: turning any on is a behaviour
change the owner decides per def (open item 1). The 38 `TaskDefinition`s become
`ScheduleFromConfig` on their target def, with today's interval and enable flag as the key's
default; `RunOnStart` → `.OnStart()`; `IsEnabled` → the key's `enabled` (or `.When` for gates that
are not schedule settings); window membership (`maintenanceOrder`, `internal/scheduler/scheduler.go:197-246`)
→ `Window: "nightly", Order: idx`. `transcode` (enqueues nothing) is deleted; `batch_poller` becomes the
schedule of the core kind `core.ai.batch-poll` ([05-ai](05-ai.md)), registered from F5 by its minimal owner
registration (5T.8.2) and extended by the AI flip F7. The definitions' bodies and these
declarations land dark and are registered by the tasks flip (F5, 5T.8.2), which deletes ABO's scheduler in
the same PR, so no kind ever has two schedulers.

**Durable clock.** One clock per scheduled kind, `tasks.Clock{LastDue, LastFire, LastRun, Missed,
GateClosedAt, ScheduleHash}`, persisted only through `ClockStore` (5T.3.13) under the kind, in
`c/tasks/sched/<kind>` (5T.6.1). An empty `ScheduleHash` (every clock the cutover imports, 5T.8.3) makes the
scheduler derive each poll's next instant from `LastDue` and the current `ScheduleConfig`, HEAD's
interval-clock rule (`interval_clock.go:36-63`), so an imported clock is continued, not re-seeded.
One scheduler goroutine
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

**First boot after the cutover (OPS-7, DATA-6).** The tasks cutover imports every HEAD clock
(the v3 schedule state, the scheduler's settings clocks, the full-sweep clock and the maintenance window's
date) as the clock of the kind it fires (5T.8.3), so the first R2 boot neither fires every due schedule at
once (full scans, paid AI calls) nor skips one that was due: each kind continues from the last `LastDue`
HEAD committed. A clock HEAD never wrote is seeded by the first-sight rule above. `TestCutover_NoCatchUpStorm`
and `TestScheduleParity_AllDefinitions` (5T.9) prove both halves.

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
	// Cancel, Decide, Pause and Unpause act as the actor the kernel stamps from
	// ctx, as Start does (below), and apply the native route's permission rule to
	// that actor (the table below: Cancel = the run's actor or Perms.Cancel;
	// Decide = Approve/Start + verified method; Pause/Unpause =
	// core:tasks.manage).
	Cancel(ctx context.Context, id RunID) error
	Decide(ctx context.Context, id RunID, d Decision) error // Resume | Finish | Drop
	Pause(ctx context.Context, reason string) error
	Unpause(ctx context.Context) error
	// PauseState reads the operator pause marker (any authenticated caller;
	// GET /pause). paused is false when no marker is set.
	PauseState(ctx context.Context) (p Pause, paused bool, err error)
	// Timeline, Log and Discard are the code of GET /timeline,
	// GET /runs/{id}/log and DELETE /runs/{id} (FIN12-3), callable by a module
	// too. Timeline needs core:tasks.view and filters by each def's View.
	Timeline(ctx context.Context, q TimelineQuery) ([]RunView, error)
	// Log reads one run's log page, gated by the def's View, each line redacted
	// by RedactRunView's rule.
	Log(ctx context.Context, id RunID, q PageQuery) iter.Seq2[LogLine, error]
	// Discard deletes one run and every record it owns, with Cancel's
	// permission rule (HEAD's "same permission as cancel"). ErrRunNotFound
	// (404); ErrRunActive (409) while the run is queued, running, waiting or
	// held by a live worker, which only the executor knows (HEAD's rule,
	// operations_v2.go:665-679). The executor stages RunStore.Delete inside
	// WriteRuns, so the delete is checked against the run it read and the
	// resume sweep never sees a half-deleted run.
	Discard(ctx context.Context, id RunID) error
}

// TimelineQuery is Client.Timeline's and GET /timeline's query (FIN12-3).
// HEAD bounds its timeline window on the finish time and keeps unfinished
// runs (the store rule quoted at operations_v2.go:298), which RunFilter's
// Created bound cannot express.
type TimelineQuery struct {
	Kinds  []Kind     // registered kinds; empty = every kind the caller may view
	States []RunState // empty = every state
	From   time.Time  // Finished >= From, unfinished runs included; zero = no bound
	To     time.Time  // Finished < To, unfinished runs included; zero = no bound
	Limit  int        // clamped as HEAD's GetOperationTimeline clamps it (operations_v2.go:237)
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

// RunQuery is Client.Runs' query, GET /runs' and RunStore.List's: Kinds are
// registered Kinds (there are no aliases to resolve, D10); empty = every kind
// the caller may view.
type RunQuery struct {
	Kinds []Kind
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
// core.task.run-changed.v1 catalog entry (05-events 5E.3.1), so the realtime
// stream applies it, and the GET /runs[/{id}] serialiser calls the same
// function. No module redactor exists. A nil who blanks unconditionally.
func RedactRunView(who rbac.Checker, v RunView) RunView

// RunViewRedactor adapts RedactRunView to events.RedactFunc (decode the
// payload as RunView, redact, re-encode; an undecodable payload is returned
// blanked to "{}"). It is the Redact value of the core.task.run-changed.v1
// catalog entry (05-events 5E.3.1; COR3-13).
func RunViewRedactor() events.RedactFunc

// RunState is the state of a run as the executor and the API see it.
// Terminal: succeeded, failed, timed_out, canceled, interrupted_dropped.
// "held" is not a state (5T.3.11). NativeStores stores these values; the
// tasks cutover maps HEAD's stored vocabulary to them once (5T.8.3).
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
// hold a Stager, never the Executor. The service builds b inside WriteRuns,
// which commits it (5T.7, R9-1).
type Stager interface {
	StartIn(ctx context.Context, b *storage.Batch, kind Kind, req StartRequest) (RunRef, error)
	// WriteRuns is the one commit path of a batch that carries a RunStore,
	// ClockStore, PauseStore or StandDownStore write. Each attempt opens a
	// fresh batch with open (the caller's own handle, so the caller stays the
	// initiating owner), calls stage, which performs every read the batch's
	// writes derive from and stages the caller's rows and its run writes (here
	// StartIn) and never commits, and then commits the batch with
	// storage.Sync, all under storage.RetryOnConflict (05-storage §5S.3.2), so
	// a conflict re-stages every participant from fresh reads (5T.7). The
	// checks are the stores' commit-time preconditions; there is no lock.
	// WriteRuns is not re-entrant: called from inside another WriteRuns'
	// stage it returns ErrWriteRunsNested and runs nothing, because a nested
	// commit would repeat on every retry of the outer build.
	WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error
}

// ErrWriteRunsNested: WriteRuns called inside another WriteRuns' stage, a
// programming error; stage the write into the outer batch instead.
var ErrWriteRunsNested = errors.New("tasks: WriteRuns called inside WriteRuns")

// Executor is kernel-only (core/kernel holds it; apps see Client).
type Executor interface {
	Client
	// CancelAs and DecideAs take the actor explicitly (kernel-internal callers:
	// the HTTP route); Client's Cancel and Decide stamp it from ctx. Distinct
	// names, because Go refuses an embedded method redeclared with another
	// signature (as COR3-13 notes for Start).
	CancelAs(ctx context.Context, id RunID, by Actor) error
	Approve(ctx context.Context, id RunID, a ApproveRequest) error
	DecideAs(ctx context.Context, id RunID, d Decision, by Actor) error // Resume | Finish | Drop
	// StartIn stages a run into b (kernel-internal). The outbox relay uses it to
	// enqueue an outbox-triggered run in the same batch as its cursor advance (5T.3.12).
	StartIn(ctx context.Context, b *storage.Batch, kind Kind, req StartRequest) (RunRef, error)
	// WriteRuns is Stager.WriteRuns for kernel-internal builders (the relay,
	// 05-events 5E.4); the executor's own builders use it too (5T.7).
	WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error
	// PauseAs and UnpauseAs stage the pause marker (PauseStore.Set) and commit it
	// inside WriteRuns, as the stand-down marker writes do (5T.3.7; COR9-8);
	// Client's Pause and Unpause call them with the stamped actor.
	PauseAs(reason string, by Actor) error
	UnpauseAs(by Actor) error
	// Lifecycle. Not named Start: Client.Start(ctx, kind, req) is already a
	// method of the embedded interface, and Go refuses the duplicate (COR3-13).
	Run(ctx context.Context) error      // resume sweep, dispatcher, workers (5T.4 step 2)
	Shutdown(ctx context.Context) error // bounded drain, 5T.4
}
```

`/api/core/v1/tasks` (`httpapi`, [05-http-api](05-http-api.md)); every route has a permission (R6). The
"Replaces" column names the HEAD routes the tasks flip deletes (F5, 5T.8.2); none survives as an alias (D10):

| Route | Replaces | Permission |
|---|---|---|
| `GET /definitions`, `GET /definitions/{kind}` | `GET /api/v3/ops/defs[/:id]`, `/api/v1/op-defs[/:id]` | `AnyAuthenticated` + per-def `View` filter |
| `POST /definitions/{kind}/runs` | `POST /api/v3/ops/defs/:id/runs`, `POST /api/v3/ops/runs`, `POST /api/v1/operations/v2`, `POST /api/v1/tasks/:name/run`, `POST /api/v1/maintenance-window/run` (a start of `core.window.nightly`) | per-def `Start`. An `Idempotency-Key` header is copied into `StartRequest.IdempotencyKey`; a duplicate within `core.tasks.idempotency_ttl` returns `200` with the existing `RunView` and `Idempotent-Replayed: true` (the only idempotency store on this route; httpapi keeps none, SIMP2-8) |
| `GET /runs`, `GET /runs/{id}`, `/runs/{id}/rows`, `/runs/{id}/items`, `/runs/{id}/log` | v3 `runs`; `GET /api/v1/operations/v2/:id`, `…/v2/:id/logs/download` | per-def `View`; each `RunView` passes `RedactRunView` |
| `POST /runs/{id}/cancel` | v3 cancel; `DELETE /api/v1/operations/v2/:id` | actor or `Cancel` |
| `DELETE /runs/{id}` (`Client.Discard`) | `DELETE /api/v1/operations/v2/:id/record` | actor or `Cancel`; 409 while active |
| `POST /runs/{id}/approve` | v3 approve | `Approve` + verified method |
| `POST /runs/{id}/decide` `{resume\|finish\|drop}` | `POST /api/v1/operations/v2/:id/retry` and `…/finish` (`raw/08` §7 finish-without-resuming) | `Approve`/`Start` + verified method |
| `GET /counts`, `GET /timeline` | v3 counts/timeline; `GET /api/v1/operations/timeline` | `core:tasks.view` |
| `GET /schedules`, `PATCH /schedules/{kind}` | `/api/v1/tasks`, `PUT /tasks/:name`; `GET /api/v1/maintenance-window/status` (the `core.window.nightly` row) | view / `core:tasks.manage`; PATCH writes the kind's `ScheduleConfig` key through `config` (DB override layer; a key locked by an operator-set layer is refused with 409). The window's hours are the `core.tasks.windows.nightly.*` keys, written through 05-config's settings routes (they replace `PUT /api/v1/maintenance-window/config`) |
| `GET /pause`, `POST /pause`, `POST /unpause` | `GET\|POST /api/v1/operations/pause`, `POST /api/v1/operations/resume` | `core:tasks.view` (GET) / `core:tasks.manage` |
| (no tasks route: events) | `GET /api/v3/ops/events`, `GET /api/v1/operations/events` | not routes of `/api/core/v1/tasks` (FE2-18): the bus topics `core.task.run-changed.v1` and `core.task.log-appended.v1` are streamed by the one realtime endpoint `/api/core/v1/events` ([05-events](05-events.md) 5E.3.2, [05-realtime](05-realtime.md)); topic permission `AnyAuthenticated`, per-envelope visibility = the definition's `View` (COR-20), payload through `RedactRunView` |

**The flip deletes HEAD's run routes and moves their callers in the same PR (D10).** The F5 flip PR
(5T.8.2) deletes every HEAD route the table names, together with the engine behind them, and in that PR
moves the SPA's Activity page, `useOperationsStore` and MaintenanceTab to the native routes ([07](07-ui-shell.md);
the Activity page polls `GET /runs` until the events and realtime flip F6 gives it the realtime client,
[05-realtime](05-realtime.md) §11), and the first-party scripts and agent skills that call them
([09](09-abo-on-core.md) §9.4, R2). No alias or `Deprecation` header exists. The other routes under
`/api/v1/operations` (`stale`, `clear-stale`, `optimize-database`, `sweep-tombstones`, `set-internal-flag`,
`audit-files`, `:id/activity`, `:id/result`, `:id/results`, `:id/class-lists/:class`, `:id/changes`, `:id/undo/preflight`,
`:id/revert`, `reconcile/*`, the version-group maintenance routes; `wire_operations_routes.go:101-112`,
`wire_library_routes.go:48`, `server_lifecycle.go:1952-1965`) are books' domain code and stay books' gin routes; a handler among them
that reads a run reaches it through `Deps.Tasks` (`Client.Get`, `Client.Runs`), never through a HEAD ops row,
and resolves run IDs unchanged, because the cutover keeps every run ID (5T.8.3). Core's run route
`/api/core/v1/tasks/runs/{id}` shares no path with them, so 05-http-api §4.8's collision rule holds with no
special case.

### 5T.3.11 Held runs (disabled modules)

A disabled module registers no kinds ([06](06-app-sdk.md) §6.5.3), but its runs and clocks persist.
**`held` is a computed classification, never a stored state** (COR-13): a run is held when `Kind.Module()`
names a module whose manifest is in the build but disabled; its kinds are known from the module's last
definition snapshots (`c/tasks/def/<kind>`) or from the kind list its manifest carries
(06 `Manifest.TaskLedger`).

| Where | Behaviour |
|---|---|
| Dispatcher | never claims a held run; the run keeps its queue position |
| Resume sweep | skips held runs; their stored state (`queued`, `interrupted_*`, `awaiting_decision`) is untouched |
| Scheduler | does not poll a held kind's clock; on re-enable the kind's `Missed` policy applies (at most one coalesced fire) |
| API | `GET /runs` shows `state: "held"` with the stored state alongside; cancel, approve and decide are refused with `409 tasks.held` (read-only) |
| Re-enable | the next boot registers the kinds; queued runs dispatch and interrupted runs follow their `ResumePolicy`, exactly as after a restart |
| Module removed from the build (no manifest) | not held: its kinds are unknown (5T.5's unknown-kind row) |

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

**Kernel-registered data-migration kinds (COR4-5, COR4-6) — deferred build (SIMP5-7).** No release
R0–R3 declares an `Online` data migration: R3-1 removed the adopt and fold copies it was designed for,
and the R2 cutover's steps (M1–M8) raise a level and so are `BootBlocking`. The `Online` mode therefore lands with its **first consumer**, listed
in 05-storage §5S.3.14's "Deferred-build members" together with everything this paragraph and the
next specify: the kernel-registered `<module>.datamigrate.<name>` and `core.storage.migrate` kinds, the
synthesised definition below, the dispatch write-set exemption (an unexported kernel flag) and the two tests named below. No extraction build registers these
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
// builds it once at P4c (06 §6.5.1) as NativeStores over c/tasks/ (5T.6.1);
// there is no other implementation and no app-supplied store (D10). Core code
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

// RunRecord is the stored form of a run: RunView plus what only the executor
// reads. The native store persists every field.
type RunRecord struct {
	RunView
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
// idempotency record or a notification (OnTerminal, 5T.4). The exceptions
// are Prune and ReplaceRows: maintenance-class methods that commit their own
// chunked batches and never join a multi-owner commit (COR5-12, DATA9-7).
// Every caller that builds such a batch builds it inside Executor.WriteRuns or
// Stager.WriteRuns, which commits it (5T.7, R9-1).
//
// No write may land on a run that changed since it was read: a progress tick
// cannot revert a cancel, and a chunk-ledger commit cannot land on a run that
// went terminal (HEAD's closed-ledger rule). The store guarantees it at commit
// (R6-1, DATA6-1, R7-3): Insert stages Expect(absent) on the run's guard key
// c/tasks/run/<id>; every other method that stages a write derived from a run
// it read — Transition, Progress, SaveCheckpoint, SaveChunk, MarkRow, Strike,
// AppendEvent, AppendIntent, PutItem, AppendLog, Delete — stages GetForUpdate
// on it, and Debounce on its bucket; the run's other keys carry no Expect of
// their own and are written only in a batch that carries the guard Expect
// (5T.6.1). Insert and Transition return ErrRunConflict at staging when their
// read no longer matches want; they never commit, so they never retry:
// WriteRuns re-runs build-and-commit under RetryOnConflict, which re-stages
// every participant (COR8-7), and every other write follows 5T.7's drop rule.
type RunStore interface {
	// Runs, queue, census, history.
	NewID(ctx context.Context) (RunID, error)
	Insert(ctx context.Context, b *storage.Batch, r RunRecord) error // record + queue entry + indexes
	Get(ctx context.Context, id RunID) (RunRecord, error)            // ErrRunNotFound
	// Transition: ErrRunConflict at staging when its read no longer matches
	// want. A commit conflict while want still matches is retried by
	// WriteRuns, which re-stages the whole batch (5T.7, R7-3, COR8-7).
	Transition(ctx context.Context, b *storage.Batch, id RunID, want Expect, next RunRecord) error
	Progress(ctx context.Context, b *storage.Batch, id RunID, done, total int, current string) error
	Queued(ctx context.Context) iter.Seq2[RunRecord, error]     // dispatch order: priority, then enqueue time
	Unfinished(ctx context.Context) iter.Seq2[RunRecord, error] // the resume sweep's input
	List(ctx context.Context, q RunQuery) iter.Seq2[RunRecord, error] // includes terminal history, in RunFilter order
	Counts(ctx context.Context) (map[RunState]int64, error)    // derived from the state index (5T.6.1)

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
	// ReplaceRows makes the run's rows whose IDs begin with prefix exactly the
	// n rows at(0)..at(n-1) (a Fixer plan's rows, a result's class lists):
	// it clears that prefix, then writes the rows in bounded batches that it
	// commits itself (maintenance class, like Prune; HEAD's
	// ReplaceOpRowsPrefix contract, DATA9-7). An empty prefix is refused; n == 0
	// only clears. It is not atomic across batches: a caller that fails clears
	// its prefix again, and a reader checks the row count its summary
	// recorded. Each batch carries the run's guard GetForUpdate and HEAD's
	// closed-ledger rule (refused once the run is terminal). Never called
	// inside WriteRuns.
	ReplaceRows(ctx context.Context, id RunID, prefix string, n int, at func(i int) (Row, error)) error
	Rows(ctx context.Context, id RunID) iter.Seq2[Row, error]
	MarkRow(ctx context.Context, b *storage.Batch, id RunID, rowID string) error

	// Debounce buckets and watchdog strikes.
	Debounce(ctx context.Context, b *storage.Batch, d DebounceBucket) error
	DebounceBuckets(ctx context.Context) iter.Seq2[DebounceBucket, error]
	Strike(ctx context.Context, b *storage.Batch, id RunID, s Strike) error

	// Delete stages the removal of one run and every record it owns (row,
	// queue entry, indexes, pages, checkpoints, chunks, rows, errors); only
	// Client.Discard calls it, inside WriteRuns, after the executor has
	// refused an active run (FIN12-3). ErrRunNotFound; ErrRunActive when the
	// run is not terminal.
	Delete(ctx context.Context, b *storage.Batch, id RunID) error

	// Requires stamps: when a kind last completed for a subject.
	Stamp(ctx context.Context, b *storage.Batch, subject entity.Ref, k Kind, at time.Time) error
	Stamped(ctx context.Context, subject entity.Ref, k Kind) (time.Time, bool, error)

	// Maintenance: commits its own batches (above); a row is deleted only as
	// read, terminal (an Expect on the row). Called by core.tasks.prune (5T.10).
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
	Get(ctx context.Context, k Kind) (Clock, bool, error)
	// Advance stages next if the stored clock still equals prev (absent when
	// had is false), checked at commit; otherwise ErrClockConflict.
	Advance(ctx context.Context, b *storage.Batch, k Kind, prev Clock, had bool, next Clock) error
}

// Pause is the executor's operator pause marker (Executor.PauseAs/UnpauseAs, Client.Pause/Unpause).
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

// Importer writes an app's pre-core task state into the native stores, once,
// from inside that app's own cutover step (ABO: books' M4, 5T.8.3). The kernel
// builds it over c/tasks/ and hands it to the step in module.Importers (06
// §6.4.2; 03 §3.3 "Store interfaces"); the app decodes its own rows, so core
// holds no app key name or row format (03 R1(c)). Every method stages into the
// caller's batch b, which also carries the step's deletes of the source rows,
// so each row moves exactly once and a crash leaves the chunk either wholly
// moved or wholly unmoved (5T.4 "Boot writers"). Nothing is checked against
// executor state: no executor runs before P4 ends.
type Importer interface {
	// Run stages one run with its own ID (HEAD's op ID), its renamed Kind,
	// any RunState (terminal included) and its Seq counters, under
	// Expect(absent) on its guard key, so a resumed step never imports a run
	// twice (ErrRunConflict). A run that was running or stopping when HEAD
	// stopped is imported as StateInterrupted, so its ResumePolicy decides at
	// the first boot's resume sweep (5T.4 step 2).
	Run(ctx context.Context, b *storage.Batch, r RunRecord) error
	// RunData stages records of a run imported in this batch or an earlier
	// one: pages in their seq order, checkpoints, chunks, rows and marks. A
	// run's data may be split over several calls and batches.
	RunData(ctx context.Context, b *storage.Batch, id RunID, d RunData) error
	Clock(ctx context.Context, b *storage.Batch, k Kind, c Clock) error // ScheduleHash empty (5T.3.8)
	Pause(ctx context.Context, b *storage.Batch, p Pause) error
	StandDown(ctx context.Context, b *storage.Batch, s StandDown) error
	Debounce(ctx context.Context, b *storage.Batch, d DebounceBucket) error // pending debounced starts
	Stamp(ctx context.Context, b *storage.Batch, subject entity.Ref, k Kind, at time.Time) error
	// RenameKind stages into b the rewrite of up to limit stored references
	// to from (runs and their kind index, clock, definition snapshot, stamps)
	// as to, continuing after cursor; next is "" when none remain. It is how
	// an owner renames a kind after R2: a BootBlocking data migration of the
	// owner's calls it chunk by chunk (05-storage §5S.3.6); there is no alias
	// (D10).
	RenameKind(ctx context.Context, b *storage.Batch, from, to Kind, cursor string, limit int) (next string, err error)
}

type RunData struct {
	Pages       []PageEntry       // events, intents, item outcomes and log lines
	Checkpoints map[string][]byte // name -> HEAD's checkpoint bytes (5T.3.6)
	Chunks      []Chunk
	Rows        []Row
	Marks       []string // applied row IDs
}

var (
	ErrRunNotFound   = errors.New("tasks: run not found")
	ErrRunConflict   = errors.New("tasks: run changed since read")
	ErrClockConflict = errors.New("tasks: schedule clock changed since read")
	// ErrRunActive: Discard of a run that is not terminal or that a live
	// worker holds (409; FIN12-3).
	ErrRunActive = errors.New("tasks: run is active")
)
```

`RunEvent`, `ItemOutcome`, `LogLine`, `PageQuery`/`PageEntry`, `Chunk`, `DebounceBucket` and `Strike`
are plain data structs with the fields of HEAD's v3 records they replace
(`internal/database/iface_ops_v3.go`), minus ABO types; `Row` is 5T.3.1's. The interfaces are SDK and
frozen by the API-stability gate (03 §3.4): `coretest`'s in-memory storage and any later implementation
run the same suite. The invariants live **above** them, in the executor, so every implementation gets
them: one row per run, transitions only by `Transition`'s compare-and-set, per-run sequence numbers
advanced by the fenced owner in the row's batch, retention never touching a non-terminal run.
`taskstest.StoresSuite(t, func(*testing.T) tasks.Stores)` (the naming rule `<pkg>test.<Interface>Suite`,
SIMP5-9) asserts them and the commit-time preconditions over `NativeStores`, including the write check on
every write (`TestProgressVsCancelKeepsState`, `TestLedgerCommitAfterTerminalRefused`; R6-1), concurrent
transitions and a cancel under progress ticks all committing
(`TestConcurrentTransitionsOfDistinctRunsAllCommit`, `TestCancelCommitsUnderProgressTicks`) and the
closed-ledger drop rule (`TestFixerMarksLandDuringQuiesce`; R7-3). Its importer cases assert the
`Importer`'s exactly-once and crash rules (5T.9), so one suite covers the interface and its importer (03 §3.3).

## 5T.4 Lifecycle

The boot phases are defined once, in [06-app-sdk](06-app-sdk.md) §6.5.1 (R2-8), and the process-wide
start and stop order once, in 06 §6.5.2 (COR-8); this section only says what `tasks` does at its
phases and steps. Nothing in `tasks` writes before 06's pre-migration checkpoint.

0. **Boot phases P0–P4: the cutover writes, `tasks` does not.** `tasks` registers no migration step of
   its own. On a pre-R2 store, books' BootBlocking steps run in P4 behind the pre-migration checkpoint, in
   the R2 order M1, M7, M2–M6, M8 (03 §3.8): books' legacy steps 1..66 (M1, kept until the R3 floor, among them 59–66,
   which write the ops families, `internal/database/migrations.go:479-506,1238-1250,1340-1352,1412-1424`),
   the level step (M7), the config and identity steps (M2, M3), then books' tasks cutover step (M4, 5T.8.3), which writes `c/tasks/` through the `Importer` and
   deletes HEAD's ops rows. The executor first calls the stores at step 2, so these steps always run
   first and no `After` edge is needed. The stores are built at P4c (5T.3.13). BootBlocking data
   migrations of 05-storage run in-process through `tasks.ForEach` with persisted shard cursors in the
   P4 DAG (05-storage §5S.3.6 owns `DataMigration`). `Online` data migrations, which would run as
   executor runs after step 2 under the kernel-registered attribution-only kinds of 5T.3.12, are a
   deferred build (SIMP5-7).
1. **Register** (inside `app.New`, 06 P6–P7): kernel collects module definitions, validates (5T.3.1) and
   upserts def snapshots `c/tasks/def/<kind>`. **Whether `tasks` starts at all.** The kernel builds the executor,
   the scheduler and `taskhttp`'s routes and runs steps 2–4 when any kind is registered, a module's (through
   `Registrar.Tasks`) or one of core's own, **or** the store holds runs in `c/tasks/` (so a disabled module's held
   runs are listed, 5T.3.11); otherwise it starts none of them, mounts no task route and creates no `<ns>.ops`
   instrument (5T.11). The kernel registers each of core's own kinds only from its owner's flip (F5 or later), in
   every build. `module.Registrar.Tasks` exists from F2 (06 §6.4.10), so `hello`, which registers `hello.count`,
   starts them from F2 in `coretest` and in its own binary, and coretest's task cells run there, a disabled hello
   with persisted runs included. In ABO before the tasks flip F5 neither condition holds: ABO registers no kind,
   the kernel registers none of core's own kinds before their owner's flip, and HEAD's runs live in HEAD's own
   families until M4 imports them, so before F5 ABO's App starts nothing of `tasks`, and its first
   `Registrar.Tasks` call is in the F5 PR.
2. **Start** (`Executor.Run`, the executor step of 06 §6.5.2: after module hooks and workers and after
   `Bus.Open`, so events from the resume sweep reach subscribers): `PauseStore.Get` →
   `StandDownStore.List`: a marker that survived the restart means a holder died mid-work, so the runs
   it names stay `interrupted_quiesced` for this boot, the marker is cleared and a WARN names the holder
   (HEAD's boot consult, `internal/operations/registry/resume.go:45-60`; a later marker-free boot
   resumes them; MOD6-4) → resume sweep over `RunStore.Unfinished` (5T.3.6; held runs skipped; the
   runs the cutover imported as `interrupted` are decided here by their `ResumePolicy`) → debounce
   reload → dispatcher (100 ms tick + wake), watchdog (30 s), N workers (`core.tasks.workers`, default
   8). The scheduler goroutine starts in the next 06 step, and the outbox relay starts there from its
   schedule's `OnStart`. Dispatch waits for each def's `Ready` signals through `health.Registry.Wait`,
   the readiness core of [05-health](05-health.md), which lands with `tasks` in F2 (COR4-8): storage reports
   `core.storage`, and books' warm-up reports `books.memdb` through `Registrar.Health` and
   `Deps.Health`, so a book-reading run is never dispatched before the memdb is warm (D44,
   `TestDispatch_WaitsForReadySignal`). Waiting runs hold no worker, key or clock (current warm-up rule,
   `pkg/ops/effects.go:44-57`).
3. **Run**: claim (CAS queued→running, fence epoch+1) → build `RC` → shape runner → terminal CAS.
   After each commit the executor publishes `core.task.run-changed.v1` (state or progress change,
   coalescable per run) and `core.task.log-appended.v1`; once per terminal transition it publishes
   `core.task.finished.v1` (05-events 5E.3.2). The bus publish produces no notification (05-notify
   §4: the only entry point is `Publish`, COR2-3): the executor calls a kernel-wired
   `Options.OnTerminal(ctx, b *storage.Batch, v RunView) error` inside the terminal CAS batch, and the
   kernel binds it to 05-notify's outbox-backed `Publish` for `core.task.finished.v1` (`tasks` does not
   import `notify`, which imports `outbox`, which imports `tasks`). The executor publishes nothing but
   core topics and calls no app code (R1); the SPA reads them on the one realtime stream
   ([05-realtime](05-realtime.md)).
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

**Boot writers (A-8).** Each write into the task families before dispatch starts is named for
05-storage's crash matrix: books' legacy steps 59–66 and books' tasks cutover step M4 in P4 (step 0,
books'; M4 commits each chunk's imports and source deletes in one batch, and resumes from its persisted
cursor); the definition-snapshot upsert of `c/tasks/def/<kind>` at registration (step 1); and, in
`Executor.Run` (step 2), the stand-down marker clear and the resume sweep's transitions (`interrupted`,
`interrupted_dropped`, re-queue). A PR that adds a tasks write before dispatch, or changes one of these,
extends the matrix in the same PR.

## 5T.5 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Invalid definition (any 5T.3.1 rule) | closed: `app.New` fails | a hole in the catalog makes runs vanish silently (current `opRegistrationGate` reason) |
| `Parallelism` zero in `RunItems`/`RunSeq`/`ForEach` | closed: returns `*ItemsError{First: ErrNoParallelism, FirstIndex: -1}` before any item | N8, R7 |
| Start of unknown kind | closed: 404 `tasks.unknown_kind` | there are no aliases (D10) |
| Start without permission / unverified approver | closed: 403 | RBAC |
| `dry_run`/`dryRun` in a start body | closed: 400 `tasks.mode_field_unsupported`, nothing enqueued | a stale caller must not run in a mode it did not ask for (5T.3.7 rule 1) |
| Params do not decode | closed: run `failed` with decode error (not enqueued if validated at start) | — |
| Undecodable stored run record | open: hidden from census/totals, WARN, counted metric | `raw/08` §7 `opsv3-undecodable-census-total` |
| A HEAD ops row the tasks cutover cannot decode (M4, 5T.8.3) | open for the boot, never silent: its raw bytes are copied under `c/storage/quarantine/`, the source row is deleted with the copy in one batch, health reports the count, the step continues | data is migrated forward, never dropped (D10; 05-storage §5S.3.13 owns the quarantine) |
| A HEAD def ID that is neither in the kind-rename map nor a well-formed ID | the run is quarantined as above; a well-formed unmapped ID maps to `books.retired.<id>` (5T.8.2) | history is kept (D12 (b)) |
| Write outside declared effects | closed: `ErrOutsideEffects`, item/run fails | G8 |
| Write after fence revoked | closed: `ErrFenced` | prevents zombie writes |
| Checkpoint write error | closed for `ResumeContinue` (run fails: resume would be unsound), open (WARN) otherwise | today `CheckpointFn` errors are ignored (`run_items.go:318-320`) |
| Watermark persist error inside `RunItems` | same as checkpoint | — |
| Yield without progress/checkpoint | closed: run `failed` with reason | loop guard (`continue_in_place.go:31-41`) |
| Schedule clock read error | open for this poll (skip, metric `schedule_errors`), never "due now" | `interval_clock.go:57-75` |
| Schedule CAS conflict (`ErrClockConflict`) | open: another poll won; no fire | exactly-once |
| Event publish failure after commit | open: run state is durable; UI may lag | bus is notify-only (05-events) |
| Shutdown drain timeout | open: remaining runs stamped interrupted; fences revoked | bounded stop |
| A `Transition` or `Insert` batch fails with `storage.ErrConflict` (a progress tick, another actor, a participant's key) | open: rebuilt and recommitted by `WriteRuns` under `storage.RetryOnConflict` while a fresh read still matches `want`; `ErrRunConflict` only when it no longer does (the dispatcher's claim simply loses; of an executor completion and an HTTP cancel of one run, exactly one commits); attempts exhausted with `want` matching → `storage.ErrConflict`, transient (5T.7) | CAS transitions; unrelated writes must not fail a transition (R7-3, DATA7-2) |
| Any other `RunStore` write finds the run changed since it was read (a cancel, decide, watchdog stop or shutdown landed first: its precondition fails at commit) | open: never re-staged over the old read; on the fresh read a bare progress tick is dropped on any state change, and a chunk-ledger commit, checkpoint, Fixer row or mark, page append, item outcome or strike is re-staged while the run is non-terminal under the same attempt and fence (`stopping` included) and otherwise refused, its chunk redone on resume (5T.7) | a tick must never revert a cancel and nothing may land on a closed ledger, while a quiescing run keeps its marks (R6-1, R7-3, DATA6-1, DATA7-3; HEAD's closed-ledger rule, `pebble_store_ops_v3.go:1655-1666`, `pebble_store_ops_v3_rowmarks.go:54-56`) |
| Stand-down marker present at boot | open: the runs it names stay `interrupted_quiesced` this boot, the marker is cleared, WARN | the holder's work is half-applied; HEAD's boot consult (`resume.go:45-60`; MOD6-4) |
| `WriteRuns` called inside another `WriteRuns` stage | closed: `ErrWriteRunsNested`, nothing run | a nested commit would repeat on every retry of the outer build (COR9-8) |
| `ReplaceRows` fails part-way | open for the caller: the rows already written stay; the caller clears its prefix again (`n == 0`) and reports the failure, and a reader checks the row count its summary recorded | HEAD's `ReplaceOpRowsPrefix` contract (`internal/database/iface_op_rows.go:40-61`); not atomic across batches by design (DATA9-7) |
| Cancel/approve/decide on a held run | closed: `409 tasks.held` | read-only while its module is disabled (5T.3.11) |
| `Client.Discard` of a run that is queued, running, waiting or held by a live worker | closed: `ErrRunActive`, 409, nothing deleted | HEAD's discard refused the same states (`operations_v2.go:665-679`); deleting an active run would race the executor (FIN12-3) |
| Stored run whose kind is neither registered nor held (a removed module, a `books.retired.*` history kind) | open: the resume sweep sets a non-terminal one `interrupted_dropped` (reason `unknown_kind`), as HEAD does (`resume.go:86-96`); terminal ones stay listed as history under their kind, counted, WARN once | boot must not fail on history; `TestKindRenameMapCoversLedger` fails CI before a HEAD def is left unmapped |
| `ForEach` with `Watermark` | closed: `*ItemsError{First: ErrNoRunContext}` before any item | checkpointing needs a run |
| Lane capacity 0 with `NoLocalFallback` | waits (cancellable); never runs in-process | LaneMac rule |

## 5T.6 Persistence: the native store (R4-1)

**One stored format, core's.** The executor and the scheduler persist only through `tasks.Stores`
(5T.3.13), which is always `NativeStores` over `c/tasks/` (D10), with no other implementation and no storage
grant. HEAD's ops
families reach `c/tasks/` once, through books' tasks cutover step (5T.8.3), and the R2 level step (M7,
05-storage) makes every older build refuse the migrated store. Two `c/tasks/` families describe core state
rather than runs:

| Key | Content |
|---|---|
| `c/tasks/def/<kind>` | definition snapshots, for held runs (5T.3.11) |
| `c/tasks/idem/<kind>/<key>` | HTTP start idempotency records (TTL `core.tasks.idempotency_ttl`; SIMP2-8), **Serialized**: `Client.Start` stages the record in the batch of the run's `Insert` under `Expect(absent)`, so of two concurrent starts with one key exactly one commits, and the other's 5T.7 retry re-reads the record and replays the existing run (`200`, `Idempotent-Replayed: true`); expiry deletes a record with an `Expect` on the value it read (DATA8-9) |

### 5T.6.1 Native store (`c/tasks/`)

`tasks.NativeStores` is what every app, `examples/hello` and `coretest` use (SIMP4-3). It lands with
`tasks` in F2, dark in ABO until the tasks flip F5 (5T.8.2): under 03 §3.4 rule (b) `hello` and `coretest` are core
consumers, and `hello.count` runs on it from the PR that lands it (06). Values are `codec.JSON[T](1)` of the
core type.

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
key. `NewID` returns a ULID; an imported run keeps HEAD's op ID, and the guard key's `Expect(absent)` refuses any collision. `Prune`
deletes a terminal run with all its rows in one batch: the guard key by a point `Delete` under its
`Expect`, the run's other families by `DeleteRange`, which storage allows outside a checked range and
refuses inside one (05-storage §5S.3.4, DATA7-1). `TestNativeStores_RoundTrip` and
`taskstest.StoresSuite` cover it.

## 5T.7 Concurrency contract

- `Executor`, `Client`, `RC`, `ItemRC`, `Writer` are goroutine-safe; `RC` may be shared with goroutines
  the run starts **only** if they end before the run returns (a goroutine left behind writes
  nothing: its writes are refused, `ErrItemReturned`/`ErrFenced`).
- Bounded everything: N executor workers; per-module `max_concurrent`; per-run `Parallelism` (with
  the operator cap); executor CPU budget; lanes; zombie cap; one ref-lock table with sorted
  all-or-wait acquisition (no lock-order deadlock). No unbounded goroutine fan-out anywhere in `tasks`.
- One scheduler goroutine for all schedules (current `runV3Loop`, `v3_schedule.go:622`).
- State transitions are CAS on `(state, attempt, fence_epoch)` through `RunStore.Transition` (current
  `TransitionRunV3`, `internal/database/iface_ops_v3.go:400-405`): a commit-time precondition on the
  run's `Serialized` guard key.
- **One build-and-commit path (R9-1).** Every caller that builds a batch carrying a `RunStore`,
  `ClockStore`, `PauseStore` or `StandDownStore` write — the executor's claim and terminal transitions, a
  cancel or decide, the watchdog's stop, the shutdown quiesce, the per-run writer's ticks and ledger
  writes, `Client.Start`, the scheduler's enqueue, `Executor.PauseAs`/`UnpauseAs`, a stand-down's marker
  writes, the outbox relay (05-events 5E.4) and a core service's `Stager.StartIn` — builds it inside
  `WriteRuns`, which commits it under `storage.RetryOnConflict` (bounded, jittered; 5T.3.10). A store's
  own maintenance (`Prune`, `ReplaceRows`) is the exception: it commits its own batches. The cutover
  step's imports (`Importer`) are staged into the step's own batches, which commit before the executor
  starts (5T.4 step 0).
- **A conflict is not a lost precondition (R7-3, DATA7-2).** A batch that stages `Insert` or
  `Transition` can fail with `storage.ErrConflict` while its `want` still holds: a progress tick rewrites
  the run key a cancel of the same run reads, and a participant's own key (clock, cursor, idempotency
  record) can change under it. `WriteRuns` then re-reads the run (`Get`; for `Insert`, its absence), and
  while `want` (state, attempt, fence) still matches it re-stages the **whole** batch, every participant
  included (clock advance, outbox cursor, idempotency record, `OnTerminal` notification), and commits
  again; when the fresh read no longer matches `want` it returns `ErrRunConflict` and stops. It never
  re-commits bytes built from the stale read. Attempts exhausted while `want` still matches surface
  `storage.ErrConflict`, a transient error: the executor retries its own transition on the run's next
  writer turn, and a requested one (cancel, decide) answers its caller with a retryable error. So N runs
  completing at once all commit with exact counts (`TestConcurrentTransitionsOfDistinctRunsAllCommit`),
  and a cancel commits under a stream of progress ticks (`TestCancelCommitsUnderProgressTicks`; both
  `synctest`, cases of `taskstest.StoresSuite`).
- **Every other run write is checked too (R6-1, DATA6-1).** Each `RunStore` write is checked against the
  run it read, at commit (`GetForUpdate` on its guard key, 5T.3.13), as HEAD's single ops mutex checked it
  (`internal/database/pebble_store_ops_v2.go:718-751`; `pebble_store_ops_v3.go:1655-1666`); `ReplaceRows`'
  batches carry the guard check too. **One per-run writer:** the executor funnels a run's own writes
  (ticks, ledger, checkpoints, rows, marks, pages, items, log) through one writer per run, an in-process
  lock that 05-storage §5S.3.2 allows for fairness while correctness stays with the store's check
  (progress is already delivered under one lock, 5T.3.2), so a run's page appends never race its own ticks
  and a lost precondition means another actor changed the run: a cancel, a decide, the watchdog's stop,
  shutdown. On such a conflict the write is never re-staged over the old read; it re-reads the run under
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
  terminal transition, and nothing lands on a closed ledger.
- The executor's in-process locks (the dispatcher's, the ref-lock table, the per-run writer, the
  progress lock) are taken before `WriteRuns` or after it returns, never inside a `stage`.
- Cancellation propagates by context cause to every worker within one item boundary (5T.3.2).
- No package-level mutable state (R5): pause gate, CPU budget, ref-lock table, metrics, def registry
  are fields of the executor instance; two `App`s in one process do not share them.
- `held` is computed from the manifest set at boot; it never changes while the process runs, because
  enablement is restart-bound (06 §6.5.3).

## 5T.8 Migration from ABO

Every ABO definition is converted to a native core definition **before** the tasks flip, and the flip
(F5 of the R2 release, [09](09-abo-on-core.md) §9.4) registers all of them at once, adds books' tasks cutover step and deletes
HEAD's engine in the same PR (D10, D11). `core/tasks` itself, with its executor, scheduler, stores, `Importer`
and `ForEach`, lands earlier, in **F2, the foundation PRs** that open R2 dark before F3 (09 §9.4; the Core
group of 5T.8.2): no books kind is registered on it before F5, and before F5 ABO's build uses only its pure
loop surface (`Parallelism`, `ForEach`, `ItemsError`), through `internal/storagemigrate`'s cutover steps from
F3. There is no adapter, no alias field and no separate floor release
for the tasks code: the flip is the one point where ABO's runs
move from HEAD's engine to core's, and the R2 cutover is the one point where its run data moves.

### 5T.8.1 What existing code becomes

| ABO today | Becomes |
|---|---|
| `internal/operations/registry` (dispatcher, worker, watchdog, resume, shutdown, batch runner, chunk ledger, fence, deps, debounce, pause, stand-down, warm-up) | `mediacore/internal/executor`, a **re-implementation** (an 04 §8.6 row with a generated count; R7-8, MOD7-1) written fresh against `tasks.RunStore` and `tasks.RunState`, because the engine's files use HEAD's run vocabulary (`internal/operations/state`) and v2 row types, so no file moves. The figures are the generated ones of 04 §8.6's registry row (MOD8-7): 30 non-test files, 14 of them on HEAD's v2/v3 row types, 5 of them holding 39 code references to `internal/operations/state`. The ABO package is **deleted by the flip**, with its v1 status bridge (`legacy_op_status.go`) and backfill (`legacy_backfill.go`, `internal/server/legacy_backfill_op.go`), which have nothing left to bridge once no v1 writer exists. The activity mirror (`activity_mirror.go`, `reporter_db.go:37,866`, which writes books' `database.ActivityEntry`) becomes a **books** subscriber on `core.task.log-appended.v1` in `internal/activity`, keeping HEAD's bounded queue and counted drops ([05-events](05-events.md) 5E.13; MOD8-8) |
| `internal/operations/state` | read only by books' tasks cutover step (5T.8.3), which decodes HEAD's stored vocabulary with it (the v2 status table, the V3 constants, `FromV2`); it stays in place as an ABO package that `internal/books/cutover` imports (03 §3.5.1), and is deleted with the HEAD-format readers in R3 (09 §9.4, PC-14-01). Core's state machine over `tasks.RunState` is part of the executor's re-implementation. From the flip (F5) the SPA's Activity page no longer reads `web/src/generated/ops.ts`; it reads the generated tasks client and `RunState` values ([07](07-ui-shell.md)). `tools/cmd/opsgen` (which imports this package), `web/src/generated/ops.ts` and `internal/operations/state` are deleted **together**, in one R3 PR with the HEAD-format readers, so no build holds opsgen without the package it reads; the F5 flip deletes none of the three, and `ops.ts`'s remaining importers (`api.ts`, `operationPolling.ts` and its users) switch to the generated tasks client in the same release ([05-http-api](05-http-api.md) §7, §8.4; [07](07-ui-shell.md) §7.8) |
| `internal/operations` (`progress.go`, `trigger_source.go`, `state.go`, `selection.go`, `types.go`) | `progress.go` and `trigger_source.go` → `tasks`; `state.go`'s v1 checkpoint helpers (`OperationState`, `SaveCheckpoint`, `LoadCheckpoint`, `ClearState`, `state.go:14-100`, 11 books call sites) are replaced by `rc.Checkpoint` in each caller's conversion PR and deleted by the flip; `BulkMetadataFetchParams` (`state.go:27-30`) → `internal/server`, its only user (`metadata_ops.go:196,796,885`); `selection.go`, `types.go` → `internal/books/contract` (04 §3) |
| `pkg/ops` (Task/Batch/Fixer, Effects, Schedule, contracts, RC, Approval, opstest; `Pipeline` dropped, A7) | `core/tasks`, `core/tasks/taskstest` (from `pkg/ops/opstest`), with A1–A7; `pkg/ops/opstest` is a **re-implementation** in `taskstest` over `tasks.RunState` and core's in-memory storage, because its files read HEAD's V3 states and build HEAD's v2/v3 rows; its Book-typed fakes are re-implemented in `internal/books/store/storetest` (04 §8.6 rows; MOD7-1) |
| `pkg/ops/writer.go` typed methods + `internal/operations/opswriter` | run fencing (`opswriter/fence.go`: `ErrFenced`, `WithFence`, `FenceFromContext`) → public `tasks` (04 §3 `opswriter` *Split*); the generic gated write of 5T.3.5 is core code in `mediacore/internal/executor` (MOD7-13); typed `ModifyBook*` over `tasks.Writer`: the contract interface → `internal/books/contract`, the implementation (`writer.go`, `evidence.go`, `history.go`, `intent.go`, `evidence_journal.go`) → `internal/books/writer` (the `opswriter` rename; 04 §2.4, §8.6; MOD6-12) |
| `internal/operations/opmode` | core start path (5T.3.7); deleted by the flip |
| `internal/operations/childop` | `rc.Await` in `tasks`, a **re-implementation** (an 04 §8.6 row; `childop/follow.go` reads HEAD's `state` vocabulary; MOD7-1). Its v2 callers (`plugins/dedup/run_all.go:532`, `label_refinement.go:153`, `plugins/maintenance/optimize.go:73` through `WaitForOp`, `internal/server/server_maintenance_deps.go:1185-1198`) move to `rc.Await` in their conversion PRs; the ABO package is deleted by the flip |
| `internal/walkresume` | `rc.ShouldYield` + `ErrContinueInPlace` + `RunItems` `Watermark` |
| `internal/scheduler` (interval/daily/cron/window/full sweep/v3 loop) | `core/internal/scheduler`, a **re-implementation** (a 04 §8.6 entry; MOD5-4), because its clock encoding (`intervalLastTickPrefix`, RFC3339) and `taskV2DefIDs` are ABO key names and op IDs. The 38 `TaskDefinition`s become `Schedule` declarations on their books or core kinds (group T7); the clock encoding and `taskV2DefIDs` are read once by the cutover (5T.8.3); the ABO package is deleted by the flip |
| `internal/repairs` (Fixer framework) | `tasks.Fixer`; the 24 fixers → books Fixer kinds `books.repairs.<fixer-id>` |
| `internal/maintenance` (`MaintenanceJob`, registry) | jobs → books Task kinds (group T2); the package is deleted by the flip |
| `internal/opscatalog`, `addOpRegistrar`, `serviceregistry` op wiring, `pkg/plugin/sdk` | `module.Registrar.Tasks`; `internal/opscatalog` (with `Tombstones`, which the kind-rename map consumes) is deleted by the flip; every caller of `pkg/plugin/sdk` moves to `core/tasks` and `module.Registrar` in the F5 flip PR, so neither `pkg/plugin/sdk` nor `internal/serviceregistry` has a caller after it; both packages (with `tools/cmd/sdkguard`) are deleted in R3 (PC-20-01) |
| `internal/opsmetrics` | executor metrics (5T.11), a **re-implementation** in `mediacore/internal/executor` (an 04 §8.6 row; `opsmetrics.go` reads HEAD's `state` vocabulary; MOD7-1). The ABO package is deleted by the flip. Its one family core does not emit, `ops.abandoned_calls` (sources `repairs_resolver`, `internal/repairs/guards.go:209`, and `retired_versions_stat`, `internal/plugins/maintenance/retired_versions_fixer.go:240`; `internal/opsmetrics/opsmetrics.go:370-371`), becomes a books observable on a books scope in the flip PR, which repoints both converted callers, so one build never holds two owners of it |
| `internal/lifecycle` | `core/lifecycle` (03 §3.3, L0) |
| `internal/server/handlers/opsv3`, the v2 operations handlers and `internal/server/handlers/scheduler` | `mediacore/internal/taskhttp` (05-http-api §5), a **re-implementation** (an 04 §8.6 row; `opsv3/*` read HEAD's `state` vocabulary; MOD7-1). The HEAD routes 5T.3.10's table names are deleted by the flip; books' domain routes under `/api/v1/operations` stay (5T.3.10) |
| `internal/database` ops storage (`pebble_store_ops_v2.go`, `pebble_store_ops_v3*.go`, `ReconcileOpsV3WithV2`) and HEAD's v1 history reader | the HEAD-format reader of books' tasks cutover step (5T.8.3), in `internal/books/cutover` (03 §3.5.1): it runs `ReconcileOpsV3WithV2` once and decodes the ops families; it is deleted with the other HEAD-format readers in R3 (PC-14-01). The bodies of books' legacy steps 62, 65 and 66 (`pebble_store_plan_legacy.go` included), which only older stores need, stay with books' legacy steps until that same R3 floor (`UpgradeFloor{InstallFirst: R2}`, 05-storage §5S.3.5). Nothing moves into core (R4-1) |

### 5T.8.2 Conversion groups, the flip, and the kind-rename map

Each group is one or more PRs that land **dark**: the converted definitions compile, are registered by
`coretest` and `taskstest.Conformance`, and run in the parity suites against HEAD's behaviour, while the
production build still runs HEAD's engine. Groups T2–T7 are mechanically similar and suit
`/parallel-sweep`, partitioned by package so parallel PRs never touch the same file (CLAUDE.md "Parallel
Subagent Coordination"). A conversion maps HEAD's `Concurrency` 1:1 (`Fixed(n, "<reason>")` from a
non-zero value, `Sequential("<reason>")` from zero); any change of a definition's parallelism is a separate
PR under CLAUDE.md's concurrency rules. A converted body reads `rc.Mode()` instead of a `dry_run` param,
writes `rc.Checkpoint` instead of a v1 checkpoint, and returns its result instead of writing a v1 result or
summary row. The groups are disjoint by definition and sum to HEAD's 233 (34 + 2 + 6 + 48 + 98 + 45). Counts are informative figures from `748fd787a`; HEAD (`ebe365245`) registers the same set
(its op-ID ledger `internal/server/testdata/op_ids.golden` and every definition literal are unchanged since
`748fd787a`). Each group's done-check uses the count generated at its own base commit (A-16, ADV-11).

**Conversion rules (normative for groups T2–T7).**

1. **Effects from an empty `Writes`.** A HEAD definition with an empty `Writes` converts to `Effects` declaring the books resources its body actually writes, found by reading the body
   and every function it calls; a body that writes nothing converts to `tasks.ReadOnly()` with no declared reads, which meets 5T.3.1's `Effects`
   requirement. The
   PR lists, per such definition, the writes it found.
2. **One body, two shells, during the dark window.** From a conversion PR until the F5 flip, each converted
   definition's body is one function shared by two thin shells: HEAD's v2 definition, still registered on HEAD's
   engine in the production build, and the native `tasks` definition, registered only by `coretest`,
   `taskstest.Conformance` and the parity suites. Each shell only adapts its engine's arguments (params, mode,
   progress, checkpoint) to the shared body, so the parity suites test the code production runs. The F5 flip
   deletes the HEAD shells and folds each shared body into its native shell.
3. **HEAD behaviour until the flip.** A dark conversion keeps HEAD's behaviour: the shared body does what
   HEAD's body did, and production runs it on HEAD's engine until the kind's flip PR (F5, or the owner's flip for a
   core kind). The shared body reaches its writer and its stores only through what its shell hands it: the native
   shell `rc.Writer()` and the dependencies its owner declares, HEAD's shell an adapter of the same interface over
   HEAD's writer. So a converted package passes `tasklint`'s `writer-bypass` and `store-capture` and leaves the
   exemption list in its conversion PR (5T.8.4); the other body changes are the three named above (`rc.Mode()`,
   `rc.Checkpoint`, the returned result).
4. **Schedules through a lookup hook.** Books' list functions (one per converted package, returning its
   `[]tasks.Definition`) take a schedule-lookup hook, `func(tasks.Kind) *tasks.Schedule`, and set each kind's
   `Common.Schedule` from it. Books' schedule declarations (group T7) live in `internal/books`, which passes the
   hook at registration, so attaching or changing a schedule touches only `internal/books` and no converted
   package. Only books kinds take the hook; a core kind declares its own schedule in its owner's package.

| Group | Content | Count | Gate |
|---|---|---|---|
| Core (PC-12-01…06), in **F2** (the foundation PRs, 09 §9.4) | `core/tasks` (with `Parallelism`, `RunItems`, `ForEach` and `ItemsError`), the executor, the scheduler, `NativeStores`, `Importer`, `taskhttp`, `taskstest`; `module.Registrar.Tasks`; `hello` (with `hello.count`) and `coretest` (with its task cells) run on them; the window kind `core.window.nightly` (below) is declared here, in `core/tasks`, with the scheduler; the `core.tasks` config section (5T.10) and the `core:tasks.*` scopes (5T.3.7) are declared here; the three minimal owner registrations below are created here. The kernel starts the executor, the scheduler and `taskhttp` when any kind is registered (a module's or core's own) or `c/tasks/` holds runs (5T.4 step 1): `hello` registers one from F2. `tasklint` (5T.8.4) lands here and runs in CI from F2. Dark in ABO: before F5 ABO registers no kind, the kernel registers none of core's own kinds and `c/tasks/` holds no run, so ABO's kernel starts no executor or scheduler, creates none of their `<ns>.ops` instruments (5T.11) and mounts no `taskhttp` route before F5, and ABO calls only the loop surface, from `internal/storagemigrate` (05-storage) at F3 and F4 | — | `taskstest.StoresSuite` with its importer cases, the core rows of 5T.9 |
| T2 | the 34 `MaintenanceJob`s (`internal/maintenance/jobs`) → books Task kinds (`Run(ctx, JobStore, ProgressReporter, dryRun)` `internal/maintenance/job.go:498` → `TaskSpec.Run(rc, p)`; `Policy()` → `Common`) | 34 | `taskstest.Conformance` per kind |
| T3 | 24 repairs fixers → `tasks.Fixer` kinds `books.repairs.<fixer-id>`; the 2 defs `repairs.plan`/`repairs.apply` removed | 24 fixers, 2 defs | Fixer conformance suite |
| T4 | the ops that become **core kinds** (table below), each in its owner's package: 4 maintenance-plugin defs and 2 server defs; the conversions of `core.storage.compact` and `core.storage.census` create `core/internal/storagetasks`, dark (the kernel registers its kinds from F5) | 6 defs → 6 core kinds, plus the new `core.tasks.prune` | the owner file's tests |
| T5 | service-registry plugin defs, per plugin: acoustid 8, dedup 32, metafetch 3, itunes 1, Deluge 4 (through 05-download's client adapter) | 48 | the native shells do not import `pkg/plugin/sdk` (the HEAD shells keep it until F5) |
| T6 | the maintenance plugin's defs not in T3 or T4, in sub-batches of ≤ 15 by file group | 98 | — |
| T7 | the server-package defs not in T4 (`internal/server/*_op*.go`) and `scheduler.metadata-upgrade`; the 38 `TaskDefinition`s → `ScheduleFromConfig` declarations on their kinds (5T.3.8); the 15 v2 crons → none (listed in the release note) | 45 + 38 | `TestScheduleParity_AllDefinitions` |
| **F5 flip** (PC-12-08, with the schedule conversion PC-13-14) | makes ABO's first `Registrar.Tasks` call, so ABO's kernel starts core's executor and scheduler and registers every converted definition on them, and the three core kinds of the minimal owner registrations below; moves every caller of `pkg/plugin/sdk` to `core/tasks` and `module.Registrar` (the package is deleted in R3, 5T.8.1); requires `tasklint`'s exemption list to be empty (5T.8.4); adds books' tasks cutover step M4 with the kind-rename map (5T.8.3); deletes HEAD's engine (5T.8.1: `registry`, `opscatalog`, `maintenance`, `scheduler`, `childop`, `opmode`, `opsmetrics`, the v1 checkpoint helpers, the handlers and HEAD routes of 5T.3.10; not `opsgen`, `ops.ts` or `internal/operations/state`, which go together in R3, 5T.8.1); moves the SPA's Activity page and the first-party callers to the native routes; updates the dashboards and rules whose `def_id` values or series change (5T.11) | every definition | `make ci` and e2e; the parity suites (resume, cancel, shutdown, schedules); the cutover rows of 5T.9; `TestKindRenameMapCoversLedger`; 05-metrics' dashboard-reference test; `tasklint` with an empty exemption list (5T.8.4) |

**HEAD's generic ops: which become core kinds (resolves the T4 gap; SI-21).** `raw/06` §6 classes 24 of
HEAD's 233 definitions `generic`. One rule decides each: a definition becomes a **core** kind only if
everything it reads and writes after R2 is core-owned state, because a core kind may not reach a module
writer (5T.3.12, `TestCoreRun_NeverCallsModuleWriter`, 03 R1); an op over a books family stays a books kind,
whatever its class. Two definitions outside the class are core by the same rule. Checked against HEAD's
definitions (`internal/plugins/maintenance`, `internal/maintenance/jobs`, `internal/server`) and their
targets in 05-storage, 05-logging, 05-metadata, 05-health and 05-ai:

| HEAD def ID | Core kind | Owner (package; spec) | What it covers |
|---|---|---|---|
| `maintenance.db-optimize` | `core.storage.compact` | `core/internal/storagetasks` over `storage.Admin.Compact` ([05-storage](05-storage.md) §5S.4; `core/storage` is L1 and cannot import `tasks`, 03 §3.5 R4) | full compaction of the main store (`db.go:65-80`, `db.Compact(nil, 0xff)`), `ConcurrencyKey` `core.storage.exclusive` (5T.3.4); schedule `core.storage.schedules.compact` (HEAD's `db_optimize` task, nightly window). HEAD's same op also compacts books' separate AI-scan and OpenLibrary stores: that half becomes the new books kind `books.maintenance.aux-store-compact`, a nightly-window member with no imported runs |
| `maintenance.db-census-exact` | `core.storage.census` | `core/internal/storagetasks` over `storage.Admin.Census` (05-storage §5S.4, §5S.9.1) | exact per-family count by claims and keyspaces into `c/storage/census/`; HEAD's `book_ver:` special case stays books code behind the census hook |
| `maintenance.purge-old-logs` | `core.log.journal.prune` | `core/internal/logjournal` ([05-logging](05-logging.md) §5) | the system-log journal by `core.log.journal.retention_days`; HEAD's other half, `operationlog:` rows, is run-log history that M4 imports, so `core.tasks.prune` covers it |
| `maintenance.throttle-reset` | `core.integrations.release-holds` | `integrations/providerhttp` ([05-metadata](05-metadata.md) M2); minimal owner registration created at F2, registered on the executor from F5, full owner at F7 (below) | lists, and in Live releases, the quota, auth and rate-limit holds of every instance (`Registry.ReleaseHold`); `DELETE …/instances/{id}/hold` is its one-instance route |
| `diagnostics.export` | `core.diagnostics.export` | `health/diagnostics` ([05-health](05-health.md)); minimal owner registration created at F2, registered on the executor from F5, full owner at F8 (below) | already declared there; `diagnostics.ai-analyze` stays books (05-health) |
| `maintenance.window` | `core.window.nightly` | `core/tasks`, the one owner: declared at F2 with the scheduler (`core/internal/scheduler` runs it, 5T.3.8) | the window run; its members are kinds with `ScheduleConfig.Window = "nightly"`; a member's owner (`core.storage.compact` among them) only schedules into it and never declares the window kind |
| (the operation-record half of `maintenance.retention-and-hygiene`) | `core.tasks.prune` | `core/tasks`, the executor (5T.6.1, 5T.10) | terminal-run retention by `core.tasks.history_retention` through `RunStore.Prune`; the job's other half, the one-off `book:series:`/`book:author:` dead-prefix sweep, stays the books kind `books.maintenance.retention-and-hygiene`, under which its stored runs are imported |

The other 19 `generic`-class definitions stay **books** kinds (`books.<HEAD ID>` by rule 4 below), because
each one reads or writes a books family: the activity feed, which is books' (05-logging §5:
`maintenance.activity-filter-index-backfill`, `cleanup-activity-log`, `compact-activity-log`,
`nightly-compact-activity-log`, `recompact-activity-digests`); books' operation-change journal
(`opchange-book-index-rebuild`); books' `aijournal:` claim (`prune-ai-journal`; 05-ai, 05-storage
§5S.9.1); books' `met:` claim (`sweep-pebble-metrics-ttl`); library files and folders
(`cleanup-old-backups`, which deletes fileops' `.bak-<unix>` files under the library root, not core backup
archives; `temp-file-cleanup`, `transcode-temp-cleanup`, `reports-prune`, `cleanup-empty-folders`,
`fix-file-modes`); books' entities (`purge-deleted`, `trash-cleanup`, `tombstone-cleanup`); and the
dead-prefix half of `retention-and-hygiene`. `repairs.plan` and `repairs.apply` (also `generic`) are
replaced by T3's per-fixer kinds. Core kinds with no HEAD definition (`core.outbox.relay`,
`core.ai.batch-poll` from HEAD's inline `batch_poller` task, `core.backup.create`/`prune`/`verify`,
`core.updater.check`/`apply`, the auth, audit, diagnostics and library prune kinds) are declared by their
owners' files and need no map entry, except that `core.ai.batch-poll` takes `batch_poller`'s clock.

**Three core kinds whose owners flip after F5: minimal owner registrations (IMPL-5).** F5 deletes HEAD's engine
and scheduler, but the owners of `core.integrations.release-holds` (`integrations/providerhttp`, F7),
`core.ai.batch-poll` (`integrations/ai`, F7) and `core.diagnostics.export` (`health/diagnostics`, F8) land later.
So that every intermediate `main` keeps HEAD's throttle reset, AI batch polling and diagnostics export, and M4
renames HEAD's runs and `batch_poller`'s clock only to kinds the F5 build registers, F2 creates in each owner's
package a **minimal owner registration**: the kind's final `TaskSpec` (name, shape, `Perms`, `Resume`,
`Timeout`, `Parallelism`, `Effects` on the owner's own core resource, and its schedule key, `core.ai.schedules.batch_poll` for the
poll) over a one-method interim backend interface declared by that package: `providerhttp.InterimHolds`,
`ai.InterimPoll`, `diagnostics.InterimExport`. ABO's `main` hands the three backends to the kernel from F5
through `app.Options.InterimBackends`, and the kernel registers the three kinds at P6 only when their
backend is set. Each backend is books code over HEAD's state and writes no module entity under the core kind
(COR2-11): `InterimHolds` lists and, in a Live run, releases HEAD's provider holds; `InterimExport` writes
HEAD's bundle into the run's zip (`ReadOnly`); `InterimPoll` advances HEAD's AI jobs and stages each ready
job's apply as a books run through `tasks.Stager.StartIn`, as 05-ai's poll does, so result handlers run only
under books kinds. The flip that lands each owner **extends** its registration with the native body over the
owner's store and, in the same PR, deletes the interim interface, its `app.Options.InterimBackends` field and
the books backend (D10): F7 for release-holds and the poll, F8 for the export. The kind name, its stored runs,
the poll's clock and the schedule key never change, so no second rename exists; `TestKindRenameMapCoversLedger`
runs against the F5 build, where the three kinds are registered.

The F5 registration **is** the final spec: each row below is copied from the owner's definition (05-metadata M2.4,
05-ai §4, 05-health §5H.3.5), and the owner's flip changes only the `Run` body behind it. Each interim interface's
one method has exactly the kind's `TaskSpec.Run` signature over the kind's final params and result types, which the
owner package declares with the minimal registration at F2, so the interim registration's `Run` is the backend's
method value and the native body later replaces it with the same types:

```go
package providerhttp // F2 to F7
type InterimHolds interface {
	ReleaseHolds(rc *tasks.RC, p ReleaseHoldsParams) (ReleaseHoldsResult, error)
}

package ai // F2 to F7
type InterimPoll interface {
	PollBatches(rc *tasks.RC, p BatchPollParams) (BatchPollResult, error)
}

package diagnostics // F2 to F8
type InterimExport interface {
	Export(rc *tasks.RC, p ExportParams) (ExportResult, error)
}
```

| Kind | Shape | `Effects` | `Resume` | `Timeout` | `Perms` | `Schedule` (key) | Other |
|---|---|---|---|---|---|---|---|
| `core.integrations.release-holds` | Task | `tasks.Writes(tasks.Res("core", "providerhttp"))` | `ResumeDrop` | 1 m | `Start: rbac.Need("core:integrations.manage")` | none (no key) | `Priority: PriorityHigh` (HEAD's throttle reset) |
| `core.ai.batch-poll` | Task | `tasks.Writes(tasks.Res("core", "ai"))` | `ResumeFromZero` | 10 m | `Start: rbac.Need("core:integrations.manage")` | `tasks.ScheduleFromConfig("core.ai.schedules.batch_poll").Live()` | takes `batch_poller`'s clock through M4 |
| `core.diagnostics.export` | Task | `tasks.ReadOnly()` | `ResumeDrop` | 30 m | `Start: rbac.Need("core:system.manage")` | none (no key) | progress per section |

**The kind-rename map (one map, applied once by M4).** Books code, generated at the flip commit from HEAD's op-ID ledger
(`internal/server/testdata/op_ids.golden`, every canonical ID and `FormerIDs` alias HEAD ever registered),
`opscatalog.Tombstones` and `taskV2DefIDs` (`internal/scheduler/maintenance.go:154-175`); core holds none
of it (03 R1(c)). Its rules, first match wins:

1. a HEAD def ID in the table above → that core kind;
2. `repairs.plan` / `repairs.apply` → `books.repairs.<fixer_id>` from the run's `fixer_id` param, a plan
   run as `ModePreview`, an apply run as `ModeLive` with `PlanRef` = the plan run HEAD's params name;
3. a HEAD `FormerIDs` alias → where its canonical ID maps;
4. every other registered HEAD def ID `<id>` → `books.<id>` (the wrapped `maintenance.<job>` IDs included), with
   one run-level split: a `library.scan` run whose params are the full sweep's (`force_update` and
   `include_root_dir` both true, the params HEAD's `library_scan_full` task enqueues, whoever started the run) →
   `books.library.scan-full`, the full sweep's own kind; every other `library.scan` run → `books.library.scan`;
5. a tombstoned ID, `operations.backfill-legacy-status` (deleted by the flip) and any other well-formed ID
   found in a store → `books.retired.<id>`: history only, never registered (5T.5's unknown-kind row);
6. an ID that fails the kind grammar (5T.3.1) → its run is quarantined (5T.5).

A scheduler task name maps to the kind it fires through `taskV2DefIDs`, then rule 1 or 4 (`batch_poller` →
`core.ai.batch-poll`; `transcode` fires nothing and its clock is dropped), with one exception: `library_scan_full`,
which `taskV2DefIDs` points at `library.scan`, maps to `books.library.scan-full`, a new books kind with no HEAD definition of its own (T7), so its clock
(`library_scan_full_last_run`) and its `scheduled.library_scan_full` entry reach that kind and not
`books.library.scan`. **One schedule per kind stands:** `books.library.scan` carries `library_scan`'s schedule and
`books.library.scan-full` carries the full sweep's (`modules.books.scheduler.schedules.library_scan_full`); the two
share one body, one `ConcurrencyKey` and the scan stand-down key, so they never run at once.
`TestKindRenameMapCoversLedger` counts the ledger line `library.scan` once, mapped to `books.library.scan`; the
run-level split is checked by `TestCutover_TasksFixtureFromR0`. M4 applies the map to every
stored reference to a def ID: runs, clocks, debounce buckets, `Requires` stamps (all inside the retired ops
claims), and the books rows **outside** those claims that store a def ID. Those are exactly the activity
feed's families (05-storage §5S.9.1 row 15), because HEAD's activity mirror writes the def ID into every
operation entry (`internal/operations/registry/reporter_db.go:848-870`):

| Family | What M4 rewrites |
|---|---|
| `act:audit:`, `act:batch:`, `act:change:`, `act:debug:`, `act:info:`, `act:system:` (tier primary rows) | an entry's `type` when it is a HEAD def ID, its `details.def_id`, and its `def:<id>` tag |
| `act:digest:` (daily-digest rollups) | each digest item's `type` and `def:<id>` tag, as above |
| `act:typ:` (filter index by type) | an entry keyed by a HEAD def ID is re-keyed to the kind (delete and put in one batch, with its primary row) |

`act:op:` (by run ID), `act:src:` (by HEAD's plugin name, which is kept as stored), `act:bk:` and `act:lvl:`
need nothing. Rows that store a run ID need nothing anywhere, because run IDs are kept. This list is the
"rewritten in place by M4" entry of 05-storage §5S.9.1 (DATA-5), and
`TestCutoverLeavesOtherFamiliesByteIdentical` excludes exactly that column's list (these families, and the
keys M3 and M6 write in place, which their owners name there) beside the retired claims.
`TestCutover_KindReferencesRewritten` scans the whole R0-written store for HEAD def IDs and fails if one sits
in a books family outside the retired claims that this table does not name. HEAD's 16 `scheduled.<entry>` keys reach the keys of
the kinds they fire through M2 (5T.3.8). After R2 a rename is an owner's data migration through `Importer.RenameKind` (5T.3.13).

### 5T.8.3 The tasks cutover (M4)

Books' BootBlocking cutover step for the ops families (books code in `internal/books/cutover`, 03 §3.5.1), step M4
of the R2 migration list (03 §3.8; [09](09-abo-on-core.md) §9.4), carried by the tasks flip F5, after the level step (M7),
the config step (M2) and the identity step (M3), and before M5, M6 and the compaction M8 (M7 runs before any
family row is deleted). It runs behind the
mandatory pre-migration checkpoint (05-storage §5S.3.12), in P4, before the executor starts (5T.4 step
0); it receives `tasks.Importer` in `module.Importers` (06 §6.4.2); the step type, its dry-run mode
(per-family counts and quarantined rows, nothing written) and the quarantine are 05-storage §5S.3.13's. Order:

1. HEAD's every-boot repair `ReconcileOpsV3WithV2` (`internal/database/store.go:1860-1870`) runs once,
   so every v3 record agrees with its v2 row before anything is decoded.
2. In chunks, each chunk one batch holding the `Importer` calls and the deletes of exactly the source rows
   it imported, with a persisted cursor per family (resumable; 05-storage's crash matrix):
   - **runs:** v3 records, falling back to the v2 row for a run without a projection (open item 5), with
     their trails, intents, items, log, checkpoints, chunk ledgers, item-key snapshots, and the Fixer rows
     and marks: `opv2:row:` (read with its index `opv2:rowix:`) → `c/tasks/row/`, and `opv3:mark:` and
     `opv3:rowmark:` → `c/tasks/mark/` (5T.6.1). They are plan and resume state, not derived data (DATA-3):
     an imported `awaiting_decision` plan run (rule 2 of the map: `books.repairs.<fixer_id>`, `ModePreview`)
     keeps the rows its approver decides on, and an interrupted apply run keeps HEAD's rule that a row counts
     as applied only while its mark exists (`pebble_store_ops_v3_rowmarks.go:13-30`), so it never re-applies a
     row it already applied. Open runs import them with the run; a terminal run's rows and marks convert with
     its history. 05-storage §5S.9.1 row 13 names these four families as imported, not deleted. HEAD's v1
     history (`operation:` with its `operationlog:` lines, results, summaries and statuses) is imported
     as terminal runs with their log pages (D12 (b): task-run and log history are kept). Every run keeps
     its HEAD op ID; its kind goes through the map; `Priority` 0/1/2 → Low/Normal/High
     (`internal/operations/registry/types.go:329-331`). A v1 checkpoint (`operations.SaveCheckpoint`'s
     family, DATA-15) of an open run becomes that run's checkpoint named `v1`, read once by the converted
     kind's `Restore`; one with no open run is deleted;
   - **states**, HEAD's stored vocabulary → `RunState`:

     | HEAD (`state.V3`; the v2 status through `state.FromV2`) | Imported as |
     |---|---|
     | `queued` (`WaitReason` `""`, `ready`, `pause`) / `queued` with `WaitReason` `deps` | `queued` / `waiting_deps` |
     | `running`; `stopping` with `StopReason` `shutdown` | `interrupted` (HEAD stopped mid-run; the resume policy decides, 5T.4 step 2) |
     | `stopping` with `StopReason` `user` / `quiesce` / `timeout` / `watchdog` | `canceled` / `interrupted_quiesced` / `timed_out` / `failed` (reason `watchdog`) |
     | `interrupted` (`InterruptedWhy` `crash`, `legacy`) / with `quiesced` | `interrupted` / `interrupted_quiesced` |
     | `awaiting_decision`, `succeeded`, `failed`, `timed_out`, `canceled` | the same |
     | `dropped` | `interrupted_dropped` |
     | a v1 status | its terminal state; a non-terminal v1 row → `interrupted_dropped` (reason `v1_unfinished`), because no v1 runner exists |

   - **clocks:** HEAD's v3 schedule state (`SchedState`, `iface_ops_v3.go:281-290`: `LastDue`, `LastFire`,
     `Missed`, `GateClosedAt`); the scheduler's settings clocks (an RFC3339 last-due time per task under
     `intervalLastTickPrefix`, `internal/scheduler/interval_clock.go:50-60`) and the full-sweep clock
     (`library_scan_full_last_run`, `internal/scheduler/full_sweep.go:81,117`) as `LastDue` of `books.library.scan-full`; and
     `maintenance_window_last_run`, a local `YYYY-MM-DD` date (`maintenance.go:53,70-71`), as `LastDue` =
     `LastRun` = **local** midnight of that date (`time.ParseInLocation`, `time.Local`), so a window that ran
     on day D is not run again on D west of UTC (MOD7-3, MOD8-9). `ScheduleHash` is empty (5T.3.8);
   - **markers and buckets:** the operator pause marker (`pauseSettingKey`, `pauseMarker`,
     `internal/operations/registry/pause_gate.go:52-60`) → `Pause`; the scan stand-down marker
     (`registry.scan_standdown`, `scan_standdown.go:27,106-110,557-605`) → one `StandDown` for the converted
     scan kind's key; the v2 debounce buckets → `Debounce` (each holds a pending start);
   - **stamps:** HEAD's completion and dependency-revision stamps for books and book-file subjects
     (`pebble_store_ops_v2.go:1188`), library-scale, → `Stamp` with the subject `books/books` or
     `books/book_files` and the renamed kind.
3. Not imported, deleted: watchdog strikes, the census row `opv3:meta:counts` (native `Counts` is derived),
   the v2 and v3 definition rows (registration rebuilds `c/tasks/def/`). Of the shared `setting:` family
   only the run keys above move; HEAD's other setting rows are byte-identical before and after.
4. Books' claims over the ops families end with the flip, so a stray writer the conversion missed is
   refused by strict claims at runtime (05-storage §5S.3.4).

A row the step cannot decode is quarantined and reported, never dropped (5T.5). Every run, clock and stamp
it imports is counted and checked against the source (5T.9). Rollback is the offline restore of the
pre-migration checkpoint (or the owner's off-host archive) plus the pre-freeze release's binary, the last
production release before F3 merged (`.prev` after R2 deploys; 05-storage, 09): runs started after the upgrade
are lost with every other post-upgrade write.

### 5T.8.4 `archtest/tasklint` (from `tools/cmd/oplint`)

`tools/cmd/oplint` moves to `core/archtest/tasklint` (04 §6) with its module path a parameter. It lands at **F2**,
with the tasks engine (the Core group of 5T.8.2), with its six rules and its exemption list, and runs in CI from F2. HEAD's seven rules
are defined over HEAD's v3 registration model (`ops.Bundle`, `Ops(Deps)`, `internal/opscatalog`), which core does
not have: registration is only through `module.Registrar.Tasks` (5T.3.1). They map onto core's model as follows:

| HEAD rule | `tasklint` rule | Over core's model |
|---|---|---|
| `unbundled-def` | `unregistered-kind` | every `TaskSpec`, `BatchSpec` or `FixerSpec` value built in a module package (a `tasks.Task`, `tasks.Batch` or `tasks.Fixer` call) is returned by that module's `Registrar.Tasks` registration, directly or through a list function it passes there; from F2 until the F5 flip, when ABO makes no `Registrar.Tasks` call, a books list function (5T.8.2 conversion rule 4) that returns it counts, and F5 passes every such function to `Registrar.Tasks` |
| `unregistered-bundle` | — (dropped) | core has no bundles and no catalog: a kind is registered exactly when `Registrar.Tasks` receives it, which `unregistered-kind` already checks |
| `store-in-deps` | `store-capture` | a definition's `Run` (or `Source`, `Item`, `Finish`, `Candidates`, `Load`, `Evaluate`, `Apply`) captures no store except through `tasks.Writer` (`rc.Writer()`) or the dependencies its owner declared for it |
| `writer-bypass` | `writer-bypass` | unchanged in meaning: a definition's body writes a module entity only through `tasks.Writer` |
| `missing-help`, `sequential-empty-reason`, `deletes-owner-review` | the same names | unchanged in meaning, read from `Common.Help`, `tasks.Sequential("<reason>")` and the definition's `Effects` |

**Where it runs.** `tasklint` runs over `core/...` (core's own kinds, `hello`, `coretest`) and over books' task
packages, `internal/books/...` and every ABO package whose definitions books' `Registrar.Tasks` call registers,
as a test in each module (apps run it from their own tests, 03 §3.3 `archtest` row).

**The dark-conversion ratchet.** HEAD's not-yet-converted task packages would fail `writer-bypass` and
`store-capture`, so those two rules run as a ratchet: a checked-in exemption list, created at F2 with every
not-yet-converted books task package on it, may only shrink (a PR that adds an entry fails). Each dark conversion PR
(groups T2–T7) removes the packages it converts (5T.8.2 conversion rule 3), and the F5 flip requires the list to be
empty: any entry still present fails F5's gate. The other four of `tasklint`'s six rules have no exemption list and
apply from F2.

## 5T.9 Tests (core must ship)

Rows marked **(ABO)** run in the root module: the cutover rows over 09 §9.3's fixture corpus, which the R0
binary writes, plus its separate, labelled, hand-built overlay for the cases R0's API cannot write (for this
file: the corrupt and undecodable ops rows of `TestCutover_UndecodableRowQuarantined`), each overlay case
listed there; and the parity rows against HEAD's behaviour; the rest are
core's. Every row is part of `make ci`; there is no previous-build test and no `PREV_REF`.

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
| `TestMode_OmittedIsPreviewForWriters`, `TestStart_DryRunFieldRefused400`, `TestPreview_WriterRecordsIntentOnly` | dry-run |
| `TestApprove_RequiresVerifiedMethodAndPermission` (table: session, app-level interactive extension (CF Access), module method (refused), key naming the scope, key implying it via `core:admin` (refused), delegated session, forwarded header), `TestFixer_PlanRefAgeAndRowsOnly` | approval (SEC-24) |
| `TestStart_ViewOnlyDefRefusedAtRegister`, `TestStart_ConvertedViewDefKeepsScanFloor` | SEC-9 |
| `TestSchedule_EnqueueAndClockOneBatch` (crash injection between), `TestSchedule_NoBootStampede`, `TestSchedule_CoalesceMissed`, `TestSchedule_DSTSpringForwardFallBack` (synctest, fake zone) | schedules |
| `TestScheduleFromConfig_UndeclaredKeyRefused`, `TestScheduleFromConfig_ForeignNamespaceRefused` (a core kind on `modules.x.…`, a module kind on another module's or a `core.` key, a key without the `.schedules.` segment, a hyphenated segment, a `duration`-typed key, one key on two kinds), `TestScheduleFromConfig_OwnerKeyAccepted` (`core.backup.schedules.create`, `modules.books.scheduler.schedules.library_scan_full`), `TestScheduleFromConfig_HotReload`, `TestEvery_RefusedExceptRelay` | SIMP-6, R2-7 |
| `TestShutdown_DrainBoundedAndStampsInterrupted`, `TestShutdown_UninterruptibleExtendsOnceOnlyUnderNotify` | shutdown |
| `TestHeld_DisabledModuleRunsUntouchedAndBootSucceeds`, `TestHeld_KindsKnownWhileDisabled`, `TestHeld_MutationsRefused409` | COR-13 |
| `TestOutboxTriggered_StartInSameBatchAsCursor` (crash between), `TestOutboxTriggered_ModeFromConsumerOption` (no `Live()`/`Preview()` → registration fails; unset `Tx.StartRun` mode refused) | COR-9 hand-off, COR2-5 |
| `TestCoreRun_NeverCallsModuleWriter` (archtest: a `core.` kind's body reaches no module-registered write callback; module writes only under module kinds) | COR2-11 |
| `TestLockRefs_CtxFromRCJoinsTable`, `TestLockRefs_PlainCtxRefused` | COR2-16 |
| `TestModuleSystemActor_CannotStartUndeclaredKind` (foreign kind not in `TaskStarts` → 403; listed kind needing `core:system.manage` → 403), `TestTaskStarts_DisabledTargetModuleBootsAndRefusesAtRuntime` | SEC2-15, COR3-10 |
| `TestRunView_CurrentRedactedWithoutUsersView` (same `RunView` over `GET /runs` and the native stream, through `RunViewRedactor`) | COR2-2, COR3-13 |
| `TestStart_IdempotencyKeyHeaderReplays` (second POST → `200`, same run, `Idempotent-Replayed: true`), `TestOutboxTriggered_WritesNoIdemRecord` | SIMP2-8, SIMP3-8 |
| `TestOnTerminal_NotificationStagedInTerminalBatch` (crash between → neither or both) | COR2-3 |
| `taskstest.StoresSuite` over `NativeStores`: CAS transitions (every executor edge), the commit-time write check on every write, one row per run, per-run sequences, terminal-only `Prune`, every write staged into the caller's batch (a discarded batch leaves nothing; `Prune` and `ReplaceRows` excepted, they commit their own); suite name per the `<pkg>test.<Interface>Suite` rule (SIMP5-9) | R4-1, R6-1 |
| `TestProgressVsCancelKeepsState` and `TestLedgerCommitAfterTerminalRefused`, cases of `taskstest.StoresSuite` (`synctest`: a progress tick races an HTTP cancel and the run stays `stopping` whatever the order, because a tick read before the cancel's commit fails with `ErrRunConflict`; a `SaveChunk` racing a terminal transition writes no ledger, checkpoint or mark once the run is terminal) | R6-1, DATA6-1 |
| `TestConcurrentTransitionsOfDistinctRunsAllCommit` (N runs complete at once; every terminal transition commits and `Counts` is exact) and `TestCancelCommitsUnderProgressTicks` (`synctest`: an HTTP cancel races a stream of progress ticks of the same run and commits; no tick reverts it), cases of `taskstest.StoresSuite` | R7-3, DATA7-2 |
| `TestPruneUnderConcurrentCompletions` (`synctest`, a case of `taskstest.StoresSuite`: `Prune` deletes terminal runs while N other runs complete; every chunk commits under `RetryOnConflict`, `Counts` stays exact, and a row that is no longer terminal on the re-read is skipped) | DATA8-8 |
| `TestTransitionVsCancelOneWins` (`synctest`: an executor completion and an HTTP `runs/{id}/cancel` race on one run; exactly one commits, the other gets `ErrRunConflict`; the same for two `ClockStore.Advance` calls on one clock) | R5-4, DATA5-1 |
| `TestStart_ConcurrentSameIdempotencyKeyOneRun` (two concurrent HTTP starts with one `Idempotency-Key`: one run is inserted, the other replays it with `Idempotent-Replayed: true`) | DATA8-9 |
| `TestScheduleParity_AllDefinitions` **(ABO, fake clock)** (for each of HEAD's 38 `TaskDefinition`s, config enabled and disabled: core's scheduler over the converted declarations and HEAD's scheduler produce the same enqueue, start-run, maintenance-window reachability and clock-advance sequence; transcode and series-normalize never start themselves) | OPS8-4, DATA5-8 |
| `TestFixerMarksLandDuringQuiesce` (`synctest`, a case of `taskstest.StoresSuite`: a Fixer row applies while a stand-down quiesces the run to `stopping`; its mark commits, and on resume that row is skipped) | R7-3, DATA7-3 |
| `TestTimelineLogDiscard` (`GET /timeline` returns HEAD's rows for a finished-at window with in-flight runs included; `GET /runs/{id}/log` returns the run's lines; `DELETE /runs/{id}` answers 409 for a running run and, for a terminal run, removes every key of the run, and the next resume sweep does not resurrect it) and its **(ABO)** parity case against HEAD's timeline handler on the R0 fixture | FIN12-3 |
| `TestStandDown_MarkerAtBootDefersQuiescedRun` (a hold's marker survives a crash; the next boot leaves the quiesced run `interrupted_quiesced`, clears the marker and warns; the boot after resumes it) | MOD6-4 |
| `TestNativeStores_RoundTrip` (every `RunStore`, `ClockStore`, `PauseStore` and `StandDownStore` method over `c/tasks/`; `hello.count` on a `coretest` App whose only module is `hello`) | SIMP4-3 |
| `TestParity_ResumeCancelShutdown` **(ABO)** (HEAD's resume, cancel and shutdown suites, ported as behaviour oracles: each case's HEAD outcome is the expected outcome of the converted definition on core's executor) | G1, PC-12-03 |
| `TestUnknownKindRun_DroppedAtResume` (a non-terminal run of an unregistered, unheld kind → `interrupted_dropped`, reason `unknown_kind`; a terminal one stays listed; boot succeeds) | 5T.5 |
| the importer cases of `taskstest.StoresSuite` (over `NativeStores`: an imported run keeps its ID, kind, state and `Seq`; a second import of one ID fails with `ErrRunConflict`; `RunData` lands only on an imported run; `CrashClone` at every point of an import batch leaves the chunk wholly imported or wholly not; `RenameKind` rewrites runs, clock, snapshot and stamps and is idempotent) | 5T.3.13 |
| `TestKindRenameMapCoversLedger` **(ABO)** (every line of HEAD's `op_ids.golden` and every `opscatalog.Tombstones` key maps to exactly one kind by 5T.8.2's rules; every target is a registered kind of the flip build (the three minimal owner registrations included) or a `books.retired.*` kind; no two HEAD canonical IDs map to one registered kind; the core targets are exactly 5T.8.2's table) | 5T.8.2, SI-21 |
| `TestCutover_TasksFixtureFromR0` **(ABO)** (a store written by the R0 binary, with queued, waiting, running, stopping (each reason), interrupted, quiesced, awaiting-decision and terminal runs of every shape, v1 history, checkpoints, ledgers, rows, marks, pages, clocks, the pause and stand-down markers, debounce buckets and stamps: after M4 every source row is gone or quarantined, every run is present with HEAD's ID, the renamed kind and 5T.8.3's state, every count and cross-reference matches the source, a full-sweep `library.scan` run is present under `books.library.scan-full` and any other under `books.library.scan`, and the first boot's resume sweep acts per policy) | 5T.8.3 |
| `TestCutover_CheckpointsResume` **(ABO)** (every converted `ResumeContinue` kind resumes from a HEAD-written checkpoint, `v1` checkpoints included, or is listed as resuming from zero once) | 5T.3.6 |
| `TestCutover_NoCatchUpStorm` **(ABO, fake clock)** (first boot after M4: no kind fires before its next due instant from HEAD's clock; the window clock, imported with `TZ` west of UTC after a run at 23:30 local, is not due again that day) | OPS-7, MOD7-3 |
| `TestCutover_KindReferencesRewritten` **(ABO)** (every row of 5T.8.2's activity-family table that stored a HEAD def ID names the renamed kind afterwards in `type`, `details.def_id` and its `def:` tag, and `act:typ:` holds the re-keyed entry and not the old one; a scan of the R0 corpus finds no HEAD def ID in any other family outside the retired claims; `Requires` stamps are honoured for the renamed kind) | 5T.8.2, DATA-5 |
| `TestCutover_RepairsRowsAndMarksImported` **(ABO)** (an R0 store with a repairs plan run awaiting decision and an apply run interrupted mid-way: after M4 no `opv2:row:`, `opv2:rowix:`, `opv3:mark:` or `opv3:rowmark:` row is left; the plan run shows every row it held; the apply run, resumed, applies only the rows that had no mark) | 5T.8.3, DATA-3 |
| `TestCutover_SharedSettingFamilyOnlyRunKeysMove` **(ABO)** (every `setting:` row other than the moved run keys is byte-identical before and after) | 5T.8.3 |
| `TestCutover_UndecodableRowQuarantined` **(ABO)** (a corrupt ops row, from the hand-built overlay, is copied under `c/storage/quarantine/`, deleted at its source in the same batch, counted in health; the step completes) | 5T.5 |
| `TestCutover_ResumableCrashMatrix` **(ABO)** (`CrashClone` at every chunk boundary and inside a chunk: the resumed step imports each row exactly once) | A-8, 05-storage crash matrix |
| `TestDispatch_WaitsForReadySignal` (a `books.*` run that reads books is not dispatched before `books.memdb` reports Up) | COR4-8 |
| (deferred with the `Online` mode, SIMP5-7) 05-storage's `TestModuleOnlineMigrationRunsAsModuleKind` and `TestOnlineMigrationYieldsPastTimeout`, over a test module fixture (5T.3.12); one name each, the owner's (COR5-17) | COR4-5, COR4-6 |
| `TestPerRunSeq_NoReissueAfterCrash` (`CrashClone` between row write and commit) | DATA-19 |
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
| `core.tasks.schedules.prune` | `tasks.ScheduleConfig` (declared by `core/tasks` for `core.tasks.prune`, R2-7) | `{"enabled": true, "every": "24h"}` | yes (re-read every poll) | — |
| `core.<subsystem>.schedules.<name>` / `modules.<id>.<subsystem>.schedules.<name>` (declared by each kind's owner; 5T.3.8, R2-7) | `tasks.ScheduleConfig` | declared per kind by its owner | yes (re-read every poll) | declared by the owner (e.g. 05-updater's apply schedule is `ProtectAdmin`) |
| `modules.<id>.tasks.max_concurrent` | int | 0 = unlimited | yes | — |

The `core.tasks` section (every row above except the per-owner schedule keys, which their owners declare) is
registered dark from F2, the foundation PRs (09 §9.4), as every core section is, so that M2 at F3 converts
HEAD's `operation_log_retention_days` and `maintenance.*` values into registered keys (05-config's rename
table; IMPL-1); the executor and scheduler read them from F5.

There is no separate history window: history is the run store's terminal rows (the runs the cutover
imported included), aged out by `history_retention` through `RunStore.Prune`, which the core kind
`core.tasks.prune` calls on its schedule (SIMP2-12). HEAD's `scheduled.*` and `maintenance.*` values reach
these keys and the schedule keys once, through the config cutover (M2, 05-config's rename table).

## 5T.11 Metrics

Per-`App` meter ([05-metrics](05-metrics.md)), named under the app's metric namespace `<ns>`
(`app.Options.MetricNamespace`, 06; default `mediacore`; 05-metrics owns the naming rule). Names may change
(D10); this file keeps HEAD's instrument names (`internal/opsmetrics/opsmetrics.go:11-19,216-250`) on the
core scope `<ns>.ops`, because renaming them buys nothing, so ABO, which passes `audiobook_organizer`,
keeps its series names. Label **values** change with the kinds: `def_id` is the run's kind, a closed set
(registered kinds only), and HEAD's `plugin` label is replaced by `module`, the kind's owning module. The
tasks flip PR (5T.8.2) updates every dashboard panel and rule that reads them (`deploy/grafana/dashboards/*.json`,
`deploy/prometheus/{alert-rules,recording-rules,alert-rules.test}.yml`; the stall alert joins
`progress_age` with `progress_timeout` on `def_id` and `module`), and 05-metrics' dashboard-reference test
(every series those files name is served) is in that PR's gate (05-metrics).

| Instrument | Type | Labels | At HEAD |
|---|---|---|---|
| `<ns>.ops.runs` | counter | `def_id`, `outcome` (`started`, `completed`, `failed`, `canceled`, `timed_out`, `dropped`) | same name; `def_id` values are kinds |
| `<ns>.ops.run.duration` | histogram (s); default boundaries HEAD's (`internal/telemetry/views.go:42`) | `def_id`, `outcome` | same name |
| `<ns>.ops.items` | counter | `def_id`, `outcome` | same name |
| `<ns>.ops.inflight` | gauge | `def_id`, `module` | `plugin` label → `module` |
| `<ns>.ops.progress_age` | gauge (s) | `def_id`, `module` | `plugin` label → `module` |
| `<ns>.ops.progress_timeout` | gauge (s) | `def_id`, `module` | `plugin` label → `module` |
| `<ns>.ops.zombies` | gauge | — | same |
| `<ns>.ops.schedule_lag` | histogram (s); default boundaries HEAD's (`views.go:58`) | `def_id` | same name |
| `<ns>.ops.schedule_missed` | counter | `def_id` | same name |
| `<ns>.ops.queue_depth` | gauge | `module`, `priority` | new |
| `<ns>.ops.dispatch_deferred` | counter | `reason` (key, writeset, ready, module_cap, zombie_cap, standdown, paused, held) | new |
| `<ns>.ops.cpu_slots_in_use` | gauge | — | new |
| `<ns>.ops.lane_in_use` | gauge | `lane` | new |
| `<ns>.ops.ref_lock_wait` | histogram (s) | `def_id` | new |
| `<ns>.ops.held_runs` | gauge | `module` | new |
| `<ns>.ops.yields` | counter | `def_id` | new |
| `<ns>.ops.schedule_errors` | counter | `def_id` | new |
| `<ns>.ops.undecodable_records` | counter | `family` (a closed set the native store declares) | new |

Prometheus series follow 05-metrics' rule (`<ns>_ops_runs_total`, `<ns>_ops_run_duration_seconds`, …).
The two histograms default to HEAD's bucket lists (`0.1 … 86400`, `views.go:42`; `1 … 86400`,
`views.go:58`), declared as plain numbers through `Scope.View`, so `le` values do not change;
`ref_lock_wait` takes 05-metrics' default for a core histogram.

**One owner per family.** The executor owns the run families and core's scheduler both schedule
families. **From F2 until F5 the dark executor and scheduler create no `<ns>.ops` instrument in ABO's App**
(05-metrics §13): ABO's kernel either does not construct them or builds them with their instruments off, and
`internal/opsmetrics` stays the one owner of those names until the F5 PR, which wires the executor's
instruments and deletes `opsmetrics` together. A second owner would put two samples of one series (HEAD's
attribute-less `ops_zombies`, for one) on every scrape and fail the whole `/metrics` answer;
`TestABOMetricsGatherNoDuplicate` (05-metrics) runs on every PR from F2 and decides. `hello` and `coretest`
create the instruments from F2, since they have no `opsmetrics`. HEAD's other emitters of ops series are
deleted by the flip:

- `internal/opsmetrics` (5T.8.1). Its one family core does not emit, `ops_abandoned_calls` with its two
  `source` values, becomes a books observable in the flip PR.
- HEAD's client_golang run families in `internal/metrics` (`metrics.go:18-47,260-284,428-442`:
  `operations_{started,completed,failed,canceled}_total`, `operation_duration_seconds`,
  `op_items_processed`, `op_items_total`, recorded from the registry worker, `worker.go:611,1204,1293-1297`).
  They duplicate the executor's families and their `type` label held HEAD def IDs, which no longer exist.
  The flip PR repoints their dashboard panels and rules at the executor's series
  (`operation_duration_seconds` → `ops_run_duration_seconds` by `def_id`; the `op_items_processed` stall
  rule → the `ops_progress_age_seconds` / `ops_progress_timeout_seconds` stall alert).

The HTTP families `http_server_*` are 05-http-api's and 05-metrics': core's stage 2 records every request,
gin's included; no tasks route changes that.

## 5T.12 Open items

1. Owner decision per def for the 15 never-evaluated v2 crons (notably `deluge.protected-paths-sync`
   `*/30`): enable as a real schedule, or delete the declaration. Default in this spec: delete.
2. Should consistency-sensitive readers (reports) be gated against writers of the same resource?
   Today they are not.
3. `aijobs` (2 durable OpenAI batch types): task shape or separate durable type? Owned by 05-ai.
4. Remote lane protocol promotion to core when a second app needs remote workers (e.g. remote
   transcode for TV/Movies).
5. UNKNOWN: whether v2 rows written before the v3 projection existed were back-projected; the tasks
   cutover's v2-row fallback (5T.8.3) covers both cases, but its run time on a production-shaped store
   depends on the answer (measured on the production-shaped store of 09 §9.3).
6. (Closed: 06 has `Registrar.ChangeSink`, `Manifest.TaskLedger`, `Manifest.TaskStarts` checked at P7,
   and `Deps.Tasks` typed `tasks.Client`.)
7. (Closed by 05-rbac v1.4 §6.1 and its Tasks row: `core:tasks.manage` exists, may cancel any run and
   edit schedules, and the approval-method rule of 5T.3.7 (SEC-24) is stated there; SEC-9 and SEC-3 use
   its `PermissionSpec.Class` and `Confining`.)
8. (Closed by D10: rows 9, 11, 12, 13a and 13 of 05-storage §5S.9.1 are moved into `c/tasks/` by books'
   tasks cutover step and deleted (5T.8.3); `core/tasks` still has no grant over them, because books
   decodes its own rows. The post-DAG reconcilers have no tasks step.)
9. (Closed: which HEAD ops become core kinds, their `core.*` names and owners is 5T.8.2's table, checked
   by `TestKindRenameMapCoversLedger`; SI-21.)

## Objections to 03

None open. HEAD's task routes, `/api/v3/ops/*` and HEAD's run streams are deleted, and the Activity page
moves to core's task routes, in the tasks flip F5 (03 §3.8; 5T.3.10, 5T.8.2), because F5 deletes HEAD's engine;
the page polls `GET /runs` until the events and realtime flip F6 gives it the realtime client.

Earlier items: none open. 03 v1.4.0 adopted both v1.1.0 items: R7's wording (pass-through, `ForEach`, one
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

Advisor (2026-11-04):

- A-3 / ADV-S2 — books' legacy run families keep HEAD's `opsMu` discipline: no ops family is `CheckedWrites` and no books method stages an `Expect` on a legacy row; the lock spans the commit through the new optional `legacy.TasksWriteLock` and the one build-and-commit path `Executor.WriteRuns` / `Stager.WriteRuns` (5T.3.10, 5T.3.13, 5T.6.2, 5T.7); `CheckedWrites`/`Serialized` checks apply to the native `c/tasks/` keyspace only, and the `Transition`/`Insert` retry rule is kept for it; deleted: the guard-row `CheckedWrites` from T0+T1 with its count-only release, 14-day soak and `TestT0T1RunRowsCheckedWrites` (DATA7-15), `ErrBlindWrite` and `ErrNotSerialized` rows, the per-guard-row rules (R6-1, R7-3, DATA8-2 `Guarded`, DATA8-8 per-key guards, the per-run staging handle) and the step 62/65/66 guard-write conversions; `TestLegacyRunLockSpansCommit` and the `ErrRunsNotLocked` row added.
- A-2 / ADV-S1 — books' extension family removed from tasks: a field HEAD's records lack reads as its zero value (`Yields`, `ScheduleHash`; the next instant derives from `LastDue`), a record HEAD cannot hold is refused (`ErrStoreUnsupported`); deleted `ExtStale`/`ExtOrphan`, `legacy_ref`, `legacy.Extension.Put/Delete` uses, `mediacore_tasks_store_extension_stale_total`, `TestSideRecordStaleAfterPrevWrite`; adapted defs' `Perms.Start` decided as HEAD does for legacy roles and `abk_` keys (no key extension records).
- A-5 / ADV-S4 — 5T.11: the executor records HEAD's run instruments under HEAD's names in the app's namespace; the five `operations_*` families stay books', recorded by a bus subscriber; aliases, `TestS5_FullAliasSetBoots` and the alias-collision rule (OPS8-1) deleted.
- A-4 / ADV-S3 — the `origins.txt` clause and the deferred-identifier rationale removed; 04 §8.6 rows stay as planning information.
- A-6 / ADV-S5 — floor-release text: the S8 updater makes the hotfix's own-floor check; the signed manifest's copy is deferred-build.
- A-8 / ADV-4 — 5T.4 "Boot writers": legacy steps 59–66, the definition-snapshot upsert, `Recover`, the stand-down marker clear and the resume sweep named for the crash matrix.
- ADV-11 — 5T.8.2: literal counts are informative, generated at each batch's base commit.
- Cross-cluster (R-F, 05-ai) — `DedupePolicy` defined (HEAD's three v2 fields plus `Key`: fold into a queued run with an equal key, e.g. a job ID); "Dedupe at start" paragraph.
- G1-7 — T0+T1: HEAD's resume, cancel and shutdown suites run against both engines; the 1:1 parallelism mapping is kept, and any parallelisation is a separate PR.
- integration pass (advisor): 5T.11 tables books' run-event recorder (`operations_*`, `operation_duration_seconds`, `op_items_*`); 09 §9.2.1 row 5 cited; 10 cited as a design sketch.

Round 9:

- R9-1 / COR9-1 / DATA9-1 — `legacy.TasksWriteLock` deleted: the run store implements the cross-owner `legacy.WriteLock` (one lock name `runs`, HEAD's `opsMu`, asserted by the kernel on `Stores.Runs`), and `Executor.WriteRuns`/`Stager.WriteRuns` are uses of `storage.CommitLocked` (05-storage owns both): `WriteRuns(ctx, open, stage)` opens a fresh batch per attempt and commits it through `CommitLocked`, so `stage` never commits; a write outside the hold (`storage.LocksHeld` false) fails with `legacy.ErrRunsNotLocked`.
- R9-1 / DATA9-2 — 5T.6.2 "One store instance per process": `legacy.Stores.Tasks` is called once and memoised; opsbridge, `childop` and the books ticker share that instance and its `opsMu`; `TestLegacyStoresAreSingletons`.
- R9-1 / DATA9-7 — `ReplaceOpRowsPrefix`'s lock rule stated once, in 5T.6.2: `RunStore.PutRow` replaced by the maintenance-class `ReplaceRows(ctx, id, prefix, n, at)`, which books implements as HEAD's lock-free chunked `ReplaceOpRowsPrefix`, exempt from `legacy.ErrRunsNotLocked` and never inside `WriteRuns`; 5T.7 names it the one unchecked books write; error row; `TestLegacyReplaceRowsTakesNoRunLock`.
- COR9-8 — the lock is asserted on `Stores.Runs`; `Clocks`, `Pause` and `StandDown` share its hold, so `Executor.Pause`/`Unpause` and the stand-down marker writes commit inside `WriteRuns`, and the books ticker commits through `storage.CommitLocked`; HEAD's lock-free pause and clock writes stated; re-entrance defined (`ErrWriteRunsNested`, refused, since a joined nested commit would repeat on the outer retry); suite and `TestLegacyRunLockSpansCommit` extended.
- R9-2 / COR9-2 — 5T.3.10: the T0+T1 PR moves `GET /api/v1/operations/v2/{id}` and the four `…/{id}/{activity,changes,result,results}` routes to core as ledger-listed aliases with HEAD's `library.view`; the `GET /runs` row no longer lists `operations/v2/:id`; one owner per conflict set; T0+T1 row and done-check. (Superseded by R10-1: the five routes stay books' at every stage; the move text, route row and done-check are deleted.)
- R9-3 / MOD9-1 — 5T.11 "One owner per family" (05-metrics §13 item 1 the owner text): at S5 the T0+T1 PR shrinks `opsmetrics` to `ops.abandoned_calls`; the executor owns the run families and core's scheduler both schedule families; until T7 the v3 loop reports each tick's due time and fold count through the new optional tasks-client capability `legacy.TasksScheduleTicks.ReportScheduleTick`, so no ABO `RegisterDefID` is needed; the false "same scope" sentence replaced; 5T.8.1 row; `TestABOMetricsGatherNoDuplicateOrMissing`.
- R9-3 / MOD9-2 — `plugin` keeps HEAD's values through the new `Common.LegacyPlugin`, which opsbridge sets; series identity (label values) is part of the golden and of the T0+T1 done-check.
- MOD9-8 — 5T.11 states the scope `<ns>.ops` (native apps' series are `mediacore_ops_*`) and that the S5 golden diff is additions only (nine new instruments), not empty.
- OPS9-16 — `TestUpgradeFloor_RefusedBelowBooks66` renamed `TestUpgradeFloor_RefusedBelowLegacyCap` in the T8 row and 5T.9.
- Cross-cluster (R-E, lead-approved) — `counterMu` is not listed as an identity lock in 5T.3.13.
- integration pass (round 9): 5T.3.10 and 5T.3.13: new `legacy.LockRuns` (`"tasks.runs"`), the run store's one lock name, used by `WriteRuns`; the lock list matches 05-storage (identity mutexes, `opsMu`, `configBlobSaveMu`; `counterMu` is not one) and `CommitLocked(ctx, b, d, stage, locks...)` with `LockRank`; the former run-store lock identifier is no longer named.

Round 10:

- R10-1 / COR10-1 — 5T.3.10: the "(core aliases, no native path…)" route row and the R9-2 move text deleted; new paragraph "The five operations routes stay books'": all five `GET /api/v1/operations/…` routes stay books' gin routes with HEAD's handlers and `library.view` at every stage, their handlers reading runs through `Deps.Tasks` from T0+T1; core's run route `/api/core/v1/tasks/runs/{id}` is not an alias; 5T.8.1 handlers row, T0+T1 row and done-check (`TestS5_OperationsComponentStaysBooks`).
- R10-3 / MOD10-7 — 5T.8.1 and 5T.11: `ops_abandoned_calls` changes owner in one PR, the T6 sub-batch PR converting `retired_versions_fixer.go`, which creates books' observable for both `source` values (`repairs_resolver`, `retired_versions_stat`) and deletes `opsmetrics`' instrument; new "One owner at every stage" table for every ops family.
- R10-3 / MOD10-3 / COR10-3 — 5T.3.10 compat: an alias request is recorded by core's stage 2 with 05-http-api §4.2's alias `http_route`; every request that reaches gin, the five books routes included, by the mount's `otelgin`, never both.
- R10-4 / COR10-2 / DATA10-1 — 5T.3.10 `WriteRuns`, 5T.3.13, 5T.6.2, 5T.7, error row, suite and `TestLegacyRunLockSpansCommit`: the hold is marked on the batch by `CommitLocked` and checked with `storage.LocksHeld(b, opsMu)`; a write staged inside a hold into a second batch is refused.
- DATA10-3 — 5T.3.13: `opsMu` ranks last in 05-storage §5S.3.2's one total order (`TestLockRanksTotalOrder`); no `stage` calls a books method that takes `opsMu` itself; the executor's in-process locks are taken before `WriteRuns` or after it, never inside a `stage`.
- COR10-5 — 5T.3.10 and 5T.3.13: the kernel calls `WriteLocks([]string{legacy.LockRuns})` at P4c and hands the executor `RunLocks []storage.Lock`; the executor never imports `legacy`, names a lock or calls `WriteLocks`.
- MOD10-6 — 5T.11: `ops.run.duration` and `ops.schedule_lag` default boundaries are HEAD's lists (`views.go:42,58`), overridable by `metrics.Options.HistogramBuckets`; `TestHistogramBucketsPinned` in the T0+T1 done-check.
- R10-8 (consistency with 05-updater) — 5T.6.2 and T8 row: the S8 updater's floor check is 05-updater 5U.4 step 3.
- integration pass (round 10): `WriteRuns` comment: `storage.LocksHeld(b, l)`; `legacy.ErrRunsNotLocked` wraps `storage.ErrNotLocked`; 5T.3.13 cites the executor's outer-lock ranks 50–80; one store instance per App (COR10-10); 5T.11 boundaries overridable through `app.Options.MetricViews`. Already as requested: the "(core aliases, no native path…)" row is gone; HEAD's buckets are the executor's defaults; 5T.8.1 names the T6 PR.

Round 11:

- VER11-3 (R11-3) — 5T.3.10: the compat sentence no longer makes any `/api/v1/operations*` route a core alias; the aliases are `/api/v3/ops/*`, `/api/v1/op-defs` and `/api/v1/tasks`; the table's "Replaces" column drops every `/api/v1/operations` path; new paragraph "No `/api/v1/operations*` route is a core alias": all 38 HEAD routes stay books' gin routes, the formerly listed ones calling `Deps.Tasks`; T0+T1 done-check and the package row restated.
- VER11-3 (R11-3) — 5T.3.10 `Client`: `Cancel`, `Decide`, `Pause`, `Unpause` (actor stamped from ctx, the native route's permission rule) and `PauseState`, so books' legacy handlers reach runs through the client; `Executor`'s actor-taking methods renamed `CancelAs`, `DecideAs`, `PauseAs`, `UnpauseAs` (an embedded method cannot be redeclared with another signature); 5T.3.13 references follow.
- VER11-1 (R11-1) — 5T.3.10 compat paragraph: otelgin and stage 2 write into one instrument set.

Final verification:

- FIN12-3 — 5T.3.10 `Client` gains `Timeline` (new `TimelineQuery`, finished-at window with unfinished runs), `Log` and `Discard` (new `ErrRunActive`, 409); `RunStore.Delete` staged inside `WriteRuns`; the R11-3 paragraph names the three routes; error row and `TestS5_OperationsTimelineLogDiscardParity` added.
- FIN12-4 — 5T.3.10: `/maintenance-window/*` dropped from the schedules row's "Replaces"; the three routes are kept as books' gin routes on `Deps.Tasks`.

D10–D12 revision (2026-11-04): owner decisions D10 (no backward compatibility), D11 (no soaks, one flip per
subsystem) and D12 applied:

- Deleted: books' legacy task store (former 5T.6.2, with its lock discipline, run-state and clock-encoding
  tables, G5 rollback rules, one-writer and one-scheduler rules, v1 carve-outs and the floor and
  stepping-stone release); the `legacy` members `TasksRecoverer`, `TasksScheduleTicks`, `LockRuns`,
  `ErrRunsNotLocked`, the run store's `legacy.WriteLock`, `RunLocks` and `CommitLocked` use; `StoredKind`,
  `ListQuery`, `ErrStoreUnsupported`; `Common.FormerKinds`, `LegacyDefID`, `LegacyPlugin`;
  `app.Options.LegacyTaskAliases` and `ProblemLegacyTaskAlias`; the op-ID ledger gate; opsbridge and the
  T0+T1 adapter release; the books-side legacy-stream bridge; the `/api/v3/ops`, `/api/v1/op-defs`,
  `/api/v1/tasks` aliases and the R11-3 "operations routes stay books'" rules; the T8 floor release; the
  metric hand-over choreography, HEAD-golden and gather-parity tests; every previous-build and legacy-store
  test; the `dry_run` compatibility spelling.
- Rewritten: 5T.1 and 5T.3.13 (native store only, plus the new `tasks.Importer`); 5T.3.8 (schedule keys
  converted once by M2; first boot after the cutover instead of rollback); 5T.3.10 (WriteRuns without a
  lock; native `DELETE /runs/{id}` and `GET /pause`; HEAD's run routes deleted by the flip, callers moved in
  the same PR); 5T.4, 5T.5, 5T.6, 5T.7 (native store only); 5T.8 (every definition converted dark, one
  flip F5; disjoint conversion groups summing to HEAD's 233; the resolved T4 list of core kinds with names
  and owners; the one kind-rename map; new 5T.8.3, the tasks cutover M4); 5T.9 (cutover and parity rows
  over an R0-written fixture); 5T.10 (`core.tasks.schedules.prune`); 5T.11 (HEAD's instrument names kept,
  `def_id` values become kinds, `plugin` → `module`, HEAD's duplicate client_golang families deleted with
  dashboards repointed in the flip PR).
- HEAD op catalog read at `ebe365245` (`internal/opscatalog`, the plugin `OperationDefs()` lists, the op-ID
  ledger): unchanged since `748fd787a`. Consistency pass: the R2 step order M1, M7, M2–M6, M8 stated in 5T.4 and 5T.8.3, HEAD's 16
  `scheduled.<entry>` keys mapped to `modules.books.scheduler.schedules.<entry>` (`db_optimize` also to
  `core.storage.schedules.compact`), D12's answers cited as D12 (a)–(d), the route-deletion objection settled for F5,
  the approval reach of migrated all-atom keys kept (05-rbac §6), and leftover removed names dropped.

Round 13 (2026-11-04): IMPL-1, IMPL-2, IMPL-4, IMPL-5, IMPL-8, XFILE-2, XFILE-4, DATA-3, DATA-5, CONFORM-1.
`core/tasks` (executor, scheduler, stores, `Importer`, `ForEach`) lands dark in F2, the foundation PRs, with the
`core.tasks` section and `core:tasks.*` scopes; the three core kinds whose owners flip later are registered at
F5 by F2's minimal owner registrations over interim backends (`app.Options.InterimBackends`) and extended at
F7/F8; F5 no longer deletes opsgen, which goes with `ops.ts` and `internal/operations/state` in one R3 PR; M4
imports the Fixer rows and marks by family name; from F2 to F5 ABO's App creates no executor `ops` instrument
(5T.11, `TestABOMetricsGatherNoDuplicate`); and its in-place rewrite of the activity families is listed
for the byte-identity test; the fixture corpus gains its hand-built overlay; rollback names the pre-freeze
release.

Round 16 (2026-11-05): plan-gap fixes G1 (5T.4 step 1 and the Core group row: `Registrar.Tasks`, `hello.count` and coretest's task cells land at F2; the kernel starts the executor, scheduler and `taskhttp` only when an enabled module registered a kind, so ABO starts nothing before its first call at F5), G2 (5T.8.2: the three interim interfaces' one-method signatures and the three kinds' full `TaskSpec` fields, copied from 05-metadata M2.4, 05-ai §4 and 05-health §5H.3.5), G3 (new 5T.8.4: `tasklint`'s rules over core's model, `unregistered-kind`, `store-capture`, `unregistered-bundle` dropped; where it runs; the `writer-bypass`/`store-capture` exemption ratchet emptied by F5), G4 (rename map rule 4 and the scheduler-name rule: `books.library.scan-full` with its own schedule and clock; `TestCutover_TasksFixtureFromR0` cell), G6 (core-kinds table: the storage kinds live in `core/internal/storagetasks`), G10 (5T.8.1 and the F5 row: `pkg/plugin/sdk` callers move at F5, the package is deleted in R3), G12 (5T.8.2 conversion rules 1–4: `Effects` from an empty `Writes`, one body behind two shells, HEAD bodies until the flip, the schedule-lookup hook), G15 (`core.window.nightly` has one owner, `core/tasks`, declared at F2; members only schedule into it).

Round 16 (2026-11-05): review fixes X1 (5T.8.4, the Core group and F5 rows, conversion rule 3: `tasklint` lands at F2 with its exemption list holding every not-yet-converted books task package; each dark conversion PR removes its packages, whose shared bodies reach writer and stores only through their shells; F5 requires the list empty), X2 (5T.4 step 1, Core group row: `tasks` starts when any kind is registered, a module's or core's own, or `c/tasks/` holds runs; core's own kinds register from their owner's flip; `TestNativeStores_RoundTrip` boots with hello), X4 (T4: the storage-kind conversions create `core/internal/storagetasks` dark; T5's gate names the native shells; rule 1 writes `tasks.ReadOnly()`).
