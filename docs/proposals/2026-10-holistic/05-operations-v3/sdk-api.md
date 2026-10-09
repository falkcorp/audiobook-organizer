<!-- file: docs/proposals/2026-10-holistic/05-operations-v3/sdk-api.md -->
<!-- version: 1.2.0 -->
<!-- guid: b4575f26-f7c3-4fb6-a442-b74a31581e88 -->
<!-- last-edited: 2026-10-09 -->

# Operations v3 — SDK API sketch (`pkg/ops`)

This is a design sketch, not code. The Go blocks show the surface a plugin author sees and
the contracts the runtime enforces. Names are proposals. Parent doc:
[`../05-operations-v3.md`](../05-operations-v3.md) §3.

Package layout:

| package | role |
|---|---|
| `pkg/ops` | the SDK: definitions, run context, writer, progress, schedule, effects. Owns its own types; nothing is a type alias onto the runtime (v2's `pkg/plugin/sdk` is 662 lines of aliases onto `internal/operations/registry`). |
| `pkg/ops/opstest` | deterministic runner, fake store, fault injection. |
| `internal/operations/registry` | the executor ("runtime"): queue, dispatcher, workers, watchdog, resume, stand-down, fence. **Stays at this path** and is evolved in place, not rewritten, so the 122 `RunItems` call sites and every importer keep compiling during the shim. |
| `internal/operations/state` | the typed run state machine; source of the generated TypeScript. |
| `internal/operations/opswriter` | the fenced, journaling Writer; `internal/repairs.Writer` becomes a thin wrapper over it. |
| `internal/opscatalog` | the ONE list of op bundles linked into the binary. |
| `tools/cmd/oplint` | rewritten as a `go/analysis` checker and added to `make ci` (today it is in neither `make ci` nor any workflow, and the Makefile passes it `./internal/plugins/...`, which it cannot parse; run on the directory it reports 538 violations). |

---

## 1. Definitions

There are four kinds. Each is a constructor returning an `ops.Definition`. The kind decides
the defaults, so the common case needs very few fields.

```go
package ops

// Definition is what the catalog holds. It is opaque: authors build one with
// Task, Batch, Fixer or Pipeline, never with a struct literal, so a default can
// change without editing 234 call sites.
type Definition interface {
	ID() string
	Kind() Kind
	spec() *baseSpec // unexported: only this package can implement Definition
}

type Kind int

const (
	KindTask     Kind = iota + 1 // one function, runs once
	KindBatch                    // a source of items and a per-item function; the runner owns the loop
	KindFixer                    // plan -> approve -> apply over rows (generalized internal/repairs)
	KindPipeline                 // a DAG of child definitions with typed hand-off
)

// Common is the part every kind shares. Only ID, Title and Effects are required.
type Common struct {
	// Title is shown in the UI. Required.
	Title string
	// Help is the 1-3 sentence description shown in the def list and the
	// generated form. oplint warns when empty.
	Help string
	// Effects declares what the op reads and writes. Required: ReadOnly() or
	// Writes(...). It drives the write-set gate, the preview default, the
	// default permission and whether a Writer is handed out at all.
	Effects Effects
	// Exclusive names the mutual-exclusion key. Default: the def ID (what 90%
	// of v2 defs spell out by hand). Ops that must serialize with another op
	// name a shared key. library.scan keeps exactly one key ("library.scan").
	Exclusive string
	// Priority defaults to PriorityNormal.
	Priority Priority
	// Timeout defaults by kind: Task 30m, Batch 24h, Fixer plan 2h / apply 6h.
	Timeout time.Duration
	// Permission defaults to auth.PermLibraryView for ReadOnly ops and
	// auth.PermSettingsManage for ops that write. Override only to widen
	// access deliberately (a test pins every override).
	Permission auth.Permission
	// Schedule is the ONE place a timer is declared. Nil = manual/child only.
	Schedule *Schedule
	// Resume overrides the kind's default resume policy (see §5).
	Resume *ResumePolicy
	// Cancel overrides the kind's cancel contract (see §4).
	Cancel *CancelContract
	// Notify defaults to NotifyAlert for manual runs and NotifyActivity for
	// scheduled ones (today a context flag every op reads by hand, TriggerSource).
	Notify Notify
	// FormerIDs: renames keep working (carried over from v2 aliases.go).
	FormerIDs []string
	// Uses lists remote lanes this op may hand items to (see §9). Audio
	// decoding is allowed ONLY on the Mac lane.
	Uses []Lane
}
```

### 1.1 Task

```go
// TaskSpec is a single function. P is the params struct; use NoParams when there are none.
type TaskSpec[P any, R any] struct {
	Common
	Run func(rc *RC, p P) (R, error)
}

func Task[P any, R any](id string, s TaskSpec[P, R]) Definition

// NoParams and NoResult are the empty types.
type NoParams struct{}
type NoResult struct{}
```

A Task's liveness is its own business: it must call `rc.Progress().Set(...)` or
`rc.Progress().Tick()` at least every `Common.Timeout/6` (default floor 5m), or declare
`Cancel: ops.Uninterruptible("reason", budget)`. That is the v2 `LivenessManual` /
`LivenessNone` pair, collapsed into one rule the runtime can check.

### 1.2 Batch

```go
// BatchSpec is a source of items plus a per-item function. The runner owns the
// loop: concurrency, cancel points, progress, checkpoints, resume, partitioning,
// pause, stand-down renewal and per-item timeouts. The op cannot forget any of them.
type BatchSpec[P any, T any, R any] struct {
	Common
	// Source produces the items. See Source below: its Order decides whether a
	// chunk-ledger resume is legal (§5).
	Source func(rc *RC, p P) (Source[T], error)
	// Item does the work for one item. It must be idempotent: a resume re-runs
	// every item of a chunk that was not recorded complete, including items of
	// that chunk that had already finished (§5).
	Item func(rc *ItemRC, p P, item T) error
	// Finish runs once after every item settled (also after a cancel, with
	// rc.Canceled() true). It builds the result from the counters.
	Finish func(rc *RC, p P, s Summary) (R, error)

	// Concurrency defaults to CPU() — NOT 1. Sequential is a choice with a reason.
	Concurrency Concurrency
	// PartitionBy, when set, routes every item with the same key to the same
	// worker, in source order. Use it for apply paths that must never touch one
	// row from two workers (CLAUDE.md "partition into disjoint sets"). How it
	// composes with chunks is in §5.2.
	PartitionBy func(T) string
	// ChunkSize is the number of items leased to a worker at a time (§5).
	// Default 256; opstest runs each def with small sizes as well. Tune it
	// down for slow items (an item that takes minutes) so a crash re-runs
	// little, and up for sub-millisecond items so the ledger write rate stays low.
	ChunkSize int
	// ItemTimeout bounds one item; 0 = none. The clock starts after the pause
	// gate, as in v2 run_items.go.
	ItemTimeout time.Duration
	// OnItemError: Continue (default, counted as failed) or Stop.
	OnItemError ErrorPolicy
	// Label names an item for the "currently working on" line. It receives the
	// item only — no counters — so it cannot race (v2 Label read shared tallies).
	Label func(T) string
}

func Batch[P any, T any, R any](id string, s BatchSpec[P, T, R]) Definition

// Concurrency is a declared worker count with its reason.
type Concurrency struct{ n int; reason string; kind concKind }

func CPU() Concurrency                         // runtime.NumCPU(); CPU-bound default
func Network(n int, why string) Concurrency    // fixed small pool for a rate-limited backend
func Sequential(why string) Concurrency        // 1; the reason is shown in the def list
func FromParam(field string, def, max int) Concurrency // operator-tunable, clamped

// Source yields items. Order is a promise the runtime relies on for resume.
type Source[T any] struct {
	// Items is the full item list, or nil when Pages is used.
	Items []T
	// Pages streams items for sources too large to hold (book files).
	Pages func(ctx context.Context, after string, limit int) (items []T, next string, err error)
	// Key returns an item's stable id (book id, file id). Required: the
	// snapshot, the chunk ledger and the journal are all keyed by it.
	Key func(T) string
	// Order says whether a chunk-ledger resume is sound for this source.
	Order SourceOrder
	// Total, when the source cannot cheaply count, may be Unknown(); progress
	// then shows a rate and no percentage instead of a fake 0/0.
	Total Total
}

type SourceOrder int

const (
	// Snapshot: the runner freezes the item KEYS at start (opv3:snap:), cuts
	// them into chunks and resumes over that frozen chunk table. Always safe.
	// Default for Items sources; required when PartitionBy is set (§5.2).
	Snapshot SourceOrder = iota + 1
	// AppendOnly: new items only ever sort AFTER existing ones, so a cursor
	// resume cannot skip anything. Chunks are cut from the stream as pages
	// arrive and the ledger records completed KEY RANGES, not chunk numbers
	// (§5.3). The author asserts it; opstest checks it with an
	// insert-below-cursor fault.
	AppendOnly
	// Unordered: items can appear below the cursor (the activity digest tier,
	// which writes backdated keys). Resume restarts from zero; the def must
	// make Item idempotent. Declaring this is what v2's
	// activityNonResumableTiers did by hand.
	Unordered
)
```

### 1.3 Fixer (trial → approve → apply)

This is `internal/repairs` lifted into the SDK, with the parts every fixer repeats moved into
the framework: Plan and Replan become one `Evaluate`, the fingerprint is computed from the
row's declared inputs, and concurrency comes from the Batch runner.

```go
// FixerSpec plans rows, lets a person approve some of them, then applies
// exactly those, re-checking each against the plan.
type FixerSpec[P any, C any] struct {
	Common // Effects must be Writes(...); Fixers are never ReadOnly
	// Candidates lists what to look at (cheap: ids and core fields).
	Candidates func(rc *RC, p P) (Source[C], error)
	// Load re-reads one candidate by row id for apply-time re-evaluation.
	Load func(rc *RC, p P, rowID string) (C, bool, error)
	// Evaluate decides one candidate. Read-only: it gets no Writer. It is used
	// at plan time AND at apply time, so "what the plan said" and "what apply
	// checks" cannot drift (v2 fixers implement Plan and Replan separately).
	Evaluate func(rc *ItemRC, p P, c C) (Row, error)
	// Apply writes one approved row through w. w is fenced and journals.
	Apply func(rc *ItemRC, w Writer, p P, row Row) error
	// PartitionBy: rows sharing a key apply in order on one worker (a
	// version group, a parent book). Default: Row.ID.
	PartitionBy func(Row) string
	// AfterApply runs once with every applied subject (index refresh,
	// follow-up op enqueue). Gets a non-cancelable context, as v2 does.
	AfterApply func(rc *RC, applied []string) error
	// Guards add to the framework guards (iTunes paths, owner-manual
	// franchises, user locks). Each returns a skip reason or "".
	Guards []Guard
}

func Fixer[P any, C any](id string, s FixerSpec[P, C]) Definition

// Row is what a person approves. Inputs is what the fingerprint hashes.
type Row struct {
	ID       string            // stable across plans of the same state
	Subjects []Subject         // every book/file the row reads or may write
	Title    string
	Current  map[string]string // display
	Proposed map[string]string // display
	Reason   string
	Risk     Risk
	Class    string
	Evidence []string
	// Inputs are the values the decision was made from. The framework
	// fingerprints them (sorted, canonical JSON). Apply refuses a row whose
	// fresh Inputs fingerprint differs: changed_since_plan.
	Inputs map[string]string
	// Hold marks a row apply will never write, with a reason. Transient holds
	// (index not built yet) set Retry: the framework fingerprints Inputs
	// without the hold, which replaces v2's hand-computed RetryFingerprint.
	Hold  string
	Retry bool
	// OwnerOnly rows are skipped unless the apply carries an owner grant
	// for this row (v2 OwnerApplicable).
	OwnerOnly bool
	// State is fixer-private data stored with the plan (v2 Row.State).
	State json.RawMessage
}
```

A Fixer registers **two** run shapes under one ID: `<id>` with `mode=preview` (the plan) and
`<id>` with `mode=live` plus an `Approval` (the apply). There is no separate `repairs.plan` /
`repairs.apply` dispatcher op and no per-fixer HTTP route; the generated API serves both.

### 1.4 Pipeline (parent/child)

```go
// PipelineSpec runs child definitions as a DAG. The parent's liveness and
// progress are derived from its children (childop.Follow, built in), so a
// parent is never reaped while a child is healthy, and goes quiet when a child
// wedges.
type PipelineSpec[P any] struct {
	Common // Effects = union of children; computed, not declared
	Stages []Stage[P]
}

type Stage[P any] struct {
	Name string
	// Def is the child definition to run.
	Def Definition
	// After lists stage names that must SUCCEED first. Empty = starts at once.
	After []string
	// Params builds the child's params from the pipeline params and the
	// results of the stages it waits on (typed hand-off: results are decoded
	// into the child's R type, not passed as raw JSON).
	Params func(p P, results Results) (any, error)
	// OnFail: StopPipeline (default) | ContinueOthers.
	OnFail StageFailure
}

func Pipeline[P any](id string, s PipelineSpec[P]) Definition
```

*Round-2 cut (r3).* The first draft also had `PerSubject`/`FanOut` (one child run per
subject) and `Gate` (approval before a stage). Both are removed. The three pipelines that
exist at HEAD (`maintenance.window`, `maintenance.library-optimize`, `dedup.run-all`) are
linear chains of whole-library children, and workstream 02 chose a `Batch` over a dirty set
for `identification.advance` precisely to avoid ~11k child rows (parent §3.9). A stage that
needs a person's approval is a Fixer, and a Fixer's Live run can only be started from an
approved plan (§6), so a pipeline can contain a Fixer *plan* stage and must stop there; no
separate gate type is needed. Fan-out over subjects, if ever needed, is a `Batch` whose
`Item` calls `rc.Enqueue`/`rc.Await`.

### 1.5 Sources and budgets for workstream 02 (`DirtySet`, `Budget`)

```go
// DirtySet is a Source over a durable per-subject dirty set (02's
// idx:sidx:dirty:<subjectID> pattern). Pages drains it 256 keys at a time;
// Order is Unordered (a re-mark can land below the cursor) so resume is
// ResumeFromZero, which is cheap because processed entries are gone.
func DirtySet[T any](prefix string, load func(ctx context.Context, ids []string) ([]T, error)) Source[T]

// Budget names an EXISTING provider limiter (internal/metadata/throttle_registry.go)
// so the dispatcher can see contention between ops that share it. It is a
// rate budget: calls per second and in-flight concurrency. It does NOT model
// a calendar quota such as Google Books' 1,000 calls per day; workstream 02
// keeps its day-quota planner, and a def that needs one declares Budget for
// the rate and calls 02's planner for the day.
func Budget(provider string) BudgetRef
```

Rules the runner enforces for a `DirtySet` source (closing 02 §6's three gaps):
1. **Mark epochs, compare-and-delete.** Each dirty entry carries a mark epoch set by the
   marker. The runner reads the epoch with the page, hands the item to `Item`, and after
   `Item` succeeds deletes the entry **only if its epoch is unchanged**. An item re-marked
   while in flight keeps its newer epoch, stays dirty, and is drained on the next page or
   run. A plain delete after processing would lose that re-mark.
2. **One page is one chunk.** The `DirtySet` page size and the D28a `ChunkSize` default are
   both 256, and a Batch over a `DirtySet` leases each page as exactly one chunk (§5.3,
   `Unordered` row): the lease table shows the page's first key as the chunk id. With
   `PartitionBy` (02 PR 14 partitions by book id) the subject id is the key, which is
   unique per entry, so partition-major grouping is the identity and `ValidateCatalog`
   allows `PartitionBy` on a `DirtySet` as the one exception to the Snapshot-only rule.
3. **Names match 02 PR 14.** 02's driver is already written as `Source.Pages` /
   `Item` / `Finish` with `PartitionBy: bookID` on the v2 adapter; the wave-12F port is a
   constructor swap (`ops.Batch(...)` with `Source: ops.DirtySet(...)`), nothing in the
   body changes.

Per-subject dependencies that outlive one pipeline run (v2 `Requires` / `DepsScheduler`,
`op:deprev:` / `op:completion:`) stay, renamed:

```go
// After declares a standing prerequisite for a subject: "do not run X for
// book B until Y has completed for B since B last changed". Same semantics and
// same keyspace as v2 ReqOpCompleted / ReqFieldSet.
func AfterFor(defID string, sub SubjectType) Requirement
func FieldSet(field string) Requirement
```

---

## 2. The run context

```go
// RC is what a run receives. It replaces v2's Reporter (8 methods + four
// side-interfaces discovered by type assertion: OpID, TouchLiveness,
// SetResult, InvalidateLibraryStats).
type RC struct{ /* unexported */ }

func (rc *RC) Context() context.Context // canceled on cancel, timeout, quiesce, shutdown
func (rc *RC) OpID() string             // always set; no "" fallback
func (rc *RC) DefID() string
func (rc *RC) Mode() Mode               // Preview or Live; framework-resolved
func (rc *RC) Trigger() Trigger         // Manual | Scheduled | Child | Resume
func (rc *RC) Actor() Actor             // user id + verified auth method
func (rc *RC) Log() *slog.Logger        // op_id, def_id, phase pre-attached; mirrored to op log + activity
func (rc *RC) Progress() *Progress
func (rc *RC) Phase(name string) func() // declares and closes a phase (typed progress)
func (rc *RC) Writer() Writer           // nil for ReadOnly defs; record-only in Preview
func (rc *RC) Checkpoint(v any) error   // Tasks only; Batch/Fixer checkpoint automatically
func (rc *RC) Restore(v any) (bool, error)
func (rc *RC) Enqueue(def Definition, params any, opts ...EnqueueOpt) (RunRef, error) // child with parent link
func (rc *RC) Await(ref RunRef) (Outcome, error) // follows the child, relays its liveness
func (rc *RC) StandDown() (Hold, error) // pause library.scan for a write phase (v2 HoldScanStandDown)
func (rc *RC) Canceled() bool

// ItemRC is RC plus the item's own deadline and counters.
type ItemRC struct {
	*RC
}

func (rc *ItemRC) Count(counter string, delta int64) // atomic, merged by the runner
func (rc *ItemRC) Skip(reason string)                // counted under skipped:<reason>
func (rc *ItemRC) Touch()                            // liveness inside one long item (v2 TouchLiveness)

type Mode int

const (
	Preview Mode = iota + 1 // writes are recorded as intents and refused
	Live
)
```

**Preview is structural, not a flag the op reads.** In Preview the Writer records each
intended write as a plan line and returns `ErrPreview` without touching the store. An op
that writes outside the Writer is caught by `oplint` (see §11). The v2 rule "omitted mode =
preview" (`internal/operations/opmode/dryrun.go`) becomes the framework default for every
def whose Effects include a write; `dry_run` / `dryRun` params disappear from 186 sites
(`grep -rn --include='*.go' -i '"dry_run"' internal | grep -v _test | wc -l`).

---

## 3. The Writer: fenced, journaled, no delete

```go
// Writer is the only write path an op gets. Every method:
//   1. checks the fence (canceled / abandoned / lost stand-down lease -> ErrFenced);
//   2. writes an INTENT row (opv3:intent:) before touching the store;
//   3. performs the write inside the store's critical section, capturing the
//      "previous" value there (never from the caller's pre-IO snapshot);
//   4. writes the HISTORY row (opchange: / metadata history) only after the
//      write committed;
//   5. clears the intent.
// A crash between 2 and 5 leaves an intent: the recovery report lists those
// subjects as "may be partially applied" instead of the ledger claiming a
// change that never landed (the ledger-before-write class) or saying nothing.
type Writer interface {
	ModifyBook(ctx context.Context, bookID string, fn func(*database.Book) error) (Changed, error)
	ModifyBookFile(ctx context.Context, fileID string, fn func(*database.BookFile) error) (Changed, error)
	RepointBookFile(ctx context.Context, fileID, newPath string, check RepointCheck) error
	ModifyCredits(ctx context.Context, bookID string, fn CreditsFn) (Changed, error)
	ModifyTags(ctx context.Context, bookID string, fn TagsFn) (Changed, error)
	LockFields(ctx context.Context, bookID string, fields ...string) error
	// Effect wraps a write the typed surface does not cover (a file reflink, a
	// settings key, an index rebuild). The op names the effect and its subject;
	// the Writer applies steps 1, 2 and 5 around fn, and the op returns the
	// history rows fn produced so the Writer records them after fn returns.
	Effect(ctx context.Context, e EffectSpec, fn func() ([]HistoryRow, error)) error
}

// There is deliberately no DeleteBook / DeleteBookFile. Deleting requires a
// def that declares Effects: Writes(..., Deletes(ResBooks)) and uses
// rc.Deleter(), which oplint flags for owner review (standing rule: never
// delete book_file rows; repoint them).

var (
	ErrFenced           = errors.New("ops: run is fenced (canceled, abandoned or lease lost); write refused")
	ErrPreview          = errors.New("ops: preview mode; write recorded, not performed")
	ErrChangedSincePlan = errors.New("ops: row changed since the plan; refused") // returned from a Fixer Apply fn
)

// Subjects and risk, used by Row (§1.3).
type Subject struct{ Type SubjectType; ID string }
func Books(ids ...string) []Subject
func BookFiles(ids ...string) []Subject

type Risk int
const (
	RiskLow Risk = iota + 1
	RiskMedium
	RiskHigh
)
```

**Fencing.** Each run attempt gets a fence epoch (`uint64`) stored on its run handle and in
the run record. `Cancel`, a timeout, abandonment and a lost stand-down lease all revoke it.
The Writer checks it on every call, so a goroutine that ignores its context
(F4, F5) can keep computing but can no longer write. The exclusive key and write-set slot
stay held until that goroutine actually returns (the run sits in `stopping` with
`zombie=true`), so a second run cannot overlap the zombie. v2 frees the slot after 5s
(`internal/operations/registry/worker.go:553-590`).

---

## 4. Cancel contract

```go
// CancelContract replaces v2's `Cancellable bool`, which nothing verified
// (library.size-refresh declares true and its work takes no ctx).
type CancelContract struct{ kind cancelKind; reason string; budget time.Duration }

// Default for Batch and Fixer: the runner checks between items and the Writer
// checks per write. Nothing to declare.
func AtItemBoundaries() CancelContract

// Default for Task: rc.Context() is honored; opstest.CancelAt verifies the
// Task returns within Grace after cancel.
func Cooperative(grace time.Duration) CancelContract

// For genuinely atomic work (one Pebble compaction call). The budget is the
// watchdog budget too. Listed at WARN on every boot, as v2 LivenessNone is.
func Uninterruptible(reason string, budget time.Duration) CancelContract
```

---

## 5. Resume: chunk leasing (owner decision D28a)

**Invariant.** *Every item is processed at least once; no completed chunk is processed twice.*
The op author writes only `Item`. Chunking, leasing, the ledger, counters, partitioning,
progress and resume belong to the runner. This replaces the contiguous-prefix watermark of
the first draft (v2's `completionTracker`), which forced a resume to redo every item above
the lowest unfinished one even when 7 of 8 workers had run far ahead of it.

### 5.1 Chunks, leases, ledger

- **Chunks.** The runner cuts the source into chunks of `ChunkSize` items (default 256).
  For a `Snapshot` source the keys are frozen at start into `opv3:snap:{op}:{page}` and chunk
  *c* is the key range `[c·size, (c+1)·size)` of that frozen order, so the chunk table is a
  pure function of the snapshot and never depends on the worker count. Chunk boundaries are
  adjusted for partitions as in §5.2.
- **Leases.** A free chunk is leased to a worker. A lease is `{chunk, worker_id, leased_at,
  heartbeat_at, item_index}`; the worker heartbeats after every item (and `rc.Touch()`
  inside a long item) by updating its lease in the runner's in-memory lease table. Leases
  live in one process: a lease is **never reassigned while its owner goroutine is alive**,
  because the runner cannot kill a goroutine and two owners would break the invariant. A
  worker whose heartbeat is older than `ItemTimeout` (or `Timeout/6` when `ItemTimeout` is
  0) is reported as stuck in progress and to the watchdog, which is what trips the run's
  `ProgressTimeout` today; the way out is cancel or restart, and on restart the chunk is
  simply unfinished.
- **Ledger.** Completed chunks are recorded in a per-run ledger, `opv3:ledger:{op}`: a bitmap
  for `Snapshot` sources (one bit per chunk; 1M items at 256 per chunk is 4,096 bits), a
  completed-key-range list for `AppendOnly` sources (§5.3). A chunk is "complete" only when
  every one of its items returned from `Item` (ok, failed-and-continued, or skipped); an
  item error with `OnItemError: Stop` leaves the chunk incomplete. **All ledger writes are
  serialized by the runner:** workers send completions to one ledger goroutine, which folds
  them into the bitmap and writes the ledger, the lease table and the progress snapshot in
  one Pebble batch, at most every 2 s or on every completion when completions are rarer than
  that. There is no concurrent writer of the ledger, so there is nothing for a worker to
  race on.
- **Counters and Label.** Per-item counters (`ok`, `failed`, `skipped:<reason>`, op-defined
  via `rc.Count`) are runner-owned atomics, merged into the ledger batch. `Label(T)` is
  called by the worker for its own current item and stored on its lease; it receives the
  item only and reads nothing shared, so the v2 Label race (CLAUDE.md) has nothing to race on.
- **Resume.** On resume (same op id, next attempt) the runner reads the ledger, marks every
  chunk whose bit is unset as free, including chunks that were in flight at the crash, and
  leases them again. Items of a partially finished chunk are run again; `Item` idempotence
  covers that. A chunk whose bit is set is never leased again. `ResumeContinue` therefore
  means "lease the unfinished chunks"; there is no prefix and no watermark.
- **Finish.** When the ledger is full, `Finish` runs once with the `Summary` (§7). After a
  cancel, `Finish` runs with the partial summary and `rc.Canceled()` true, as before.

### 5.2 PartitionBy composes with chunks: partition-major chunking

Chosen: **partition first, then cut chunks that never split a partition.** With
`PartitionBy` set, the snapshot freezes keys grouped by partition key (a stable sort by key,
so source order is preserved inside a partition). Chunks are then cut every `ChunkSize`
items, but a boundary is pushed forward to the next partition edge, so a chunk holds whole
partitions; a partition larger than `ChunkSize` becomes one oversized chunk on its own. The
chunk boundaries are stored with the snapshot (`opv3:snap:{op}:chunks` → `[]int32` start
offsets) because they are no longer arithmetic. Each chunk is executed sequentially by its
holder, so every item of a partition runs on one worker, in source order, and any free
worker can take any free chunk.

Rejected: **hash partitions to workers** (`hash(key) mod W`). Chunk membership would then
depend on `W`, so a resume after a config change, on a smaller machine, or with `FromParam`
tuned differently would re-run or skip the wrong items; a slow partition would pin one
worker while the others idle, because chunks could not be stolen; and the ledger would need
one bitmap per worker. Partition-major chunking keeps the ledger a function of the snapshot
alone and keeps work stealing.

`PartitionBy` requires `Order: Snapshot`; `ValidateCatalog` refuses it with `AppendOnly` or
`Unordered`, because grouping needs the whole key set. The one exception is a `DirtySet`
source whose partition key is the entry key itself (§1.5). Fixer apply uses this path with
`Row.ID` as the default partition key.

### 5.3 How each `Order` interacts with chunks

| Order | chunk table | ledger | resume |
|---|---|---|---|
| `Snapshot` | frozen keys in `opv3:snap:`; chunk = fixed range (or partition-adjusted range) | bitmap | re-lease unset chunks; never re-lease a set chunk |
| `AppendOnly` | chunks are cut from the cursor stream in arrival order: the runner takes `ChunkSize` items from `Pages` and leases them as a chunk `{first_key, last_key}`; no frozen snapshot | completed-key-range list `[{first,last}]`, merged when adjacent | re-read `Pages` from the cursor of the lowest incomplete range's `first_key`; every item whose key falls inside a completed range is skipped without calling `Item`; items after the old tail are new and are chunked as they arrive. Ranges, not chunk numbers, so a deletion between runs cannot shift chunk membership |
| `Unordered` | chunks are cut from the stream as for `AppendOnly`; for a `DirtySet` one 256-key page is one chunk (§1.5) | none | `ResumeFromZero`: the stream is read again from the start and every item re-runs (for a `DirtySet`, only entries still dirty); `ValidateCatalog` refuses `ResumeContinue` |

```go
type ResumePolicy int

const (
	// ResumeContinue: Batch/Fixer-apply re-lease the chunks the ledger does
	// not record as complete (§5.1), including the ones that were in flight.
	// Tasks resume with rc.Restore(). Default for Batch with Snapshot/AppendOnly sources.
	ResumeContinue ResumePolicy = iota + 1
	// ResumeFromZero: start over in place (same op id). Requires idempotent
	// work. Default for Batch with an Unordered source and for Fixer plans.
	ResumeFromZero
	// ResumeDrop: the interrupted run ends as dropped. Default for Tasks.
	ResumeDrop
	// ResumeAsk: wait in awaiting_decision for a person. Default for Fixer
	// applies and any def with Approval.
	ResumeAsk
)
```

Differences from v2, each answering a finding:
- A resume **keeps the op id** in every policy. v2 `ResumeRequeue` minted a new row and
  closed the old one `interrupted_dropped` (`resume.go:482-497`), which moved the anchor that `opchange:`,
  `op_result:` and activity rows hang off (memory: v1 retirement note, `bulk_fetch_metadata`).
- The chunk ledger is written by the runner; the op cannot opt out of it by forgetting
  `CheckpointStateFn`, and cannot write it wrong, because the op never sees it.
- `Unordered` sources cannot select `ResumeContinue`; `ValidateCatalog` refuses it.
- `library.scan` keeps its own checkpoint format and resume rules (parent §3.8); it is not
  a chunked Batch.

---

## 6. Approval

```go
// Approval says a Live run needs a person's decision recorded first.
type Approval struct {
	// PlanRequired: the Live request must name a Preview run of the same def
	// (plan_ref) whose result holds the rows; only listed row ids are applied,
	// each re-evaluated and fingerprint-checked.
	PlanRequired bool
	// MaxPlanAge refuses a plan older than this (default 7 days).
	MaxPlanAge time.Duration
	// Approver must hold this permission AND have authenticated with a
	// verified method (session, api key, verified Cloudflare Access JWT).
	// The unsigned Cloudflare email header is never an identity.
	Approver auth.Permission
}

// Every Fixer has Approval{PlanRequired: true}. Any other def that writes may
// opt in. Bulk metadata apply should (see 05 §3.6).
```

The approval record (plan op id, the digest of the selected row ids, approver user id, auth
method, time) is stored on the Live run record, so "who approved this apply, from which
plan" is answerable from the run alone.

---

## 7. Progress (typed)

```go
// Progress is structured, not (current, total, message).
type Progress struct{ /* unexported */ }

func (p *Progress) Set(done int64, total Total) // Tasks only; Batch is automatic
func (p *Progress) Tick()                        // liveness without a number
func (p *Progress) Note(msg string)              // free-text status line

type Total struct{ n int64; known bool }

func Known(n int64) Total
func Unknown() Total

// Snapshot is what the API and SSE carry. For a Batch/Fixer the runner fills
// it from the chunk ledger and the lease table (§5); a Task fills Done/Total
// itself and has no Chunks or Workers.
type ProgressSnapshot struct {
	Phase     string           `json:"phase"`
	Phases    []string         `json:"phases"`       // declared order
	Done      int64            `json:"done"`         // items settled (ok + failed + skipped)
	Total     *int64           `json:"total"`        // null = unknown, never 0/0
	Unit      string           `json:"unit"`         // "books", "files", "rows"
	Failed    int64            `json:"failed"`       // items whose Item returned an error
	Counters  map[string]int64 `json:"counters"`     // ok, failed, skipped:<reason>, op-defined
	Chunks    *ChunkProgress   `json:"chunks"`       // Batch/Fixer only
	Workers   []WorkerProgress `json:"workers"`      // one row per worker, Batch/Fixer only
	RatePerS  float64          `json:"rate_per_s"`   // items/s over the last 30 s
	ETA       *time.Duration   `json:"eta"`          // only when Total known
	Current   string           `json:"current"`      // Label(item) of the most recently started item
	UpdatedAt time.Time        `json:"updated_at"`
}

type ChunkProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`   // for AppendOnly: chunks cut so far
	Size  int `json:"size"`    // ChunkSize in effect
}

type WorkerProgress struct {
	Worker    int       `json:"worker"`     // 0..Concurrency-1
	Chunk     int       `json:"chunk"`      // -1 when idle
	Item      string    `json:"item"`       // Label(current item)
	ItemIndex int       `json:"item_index"` // position inside the chunk
	ChunkSize int       `json:"chunk_size"` // items in this chunk (partition-adjusted)
	Heartbeat time.Time `json:"heartbeat"`
	Stuck     bool      `json:"stuck"`      // heartbeat older than the item budget (§5.1)
}

// Summary is what Finish receives: the final counters, read from the ledger.
type Summary struct {
	Done, Total, Failed, Skipped int64
	Counters                     map[string]int64 // op-defined via rc.Count, plus skipped:<reason>
	ChunksDone, ChunksTotal      int
	Canceled                     bool
}

func (s Summary) Counter(name string) int64
```

The ops UI renders `Workers` as one row per worker (chunk, item, items into the chunk,
heartbeat age, a "stuck" marker) under the run's progress bar, and `Chunks.Done/Total` next
to `Done/Total`. This is the owner's "status is always visible" requirement (D28).

---

## 8. Schedule

```go
// Schedule is the single timer declaration. It replaces three things in v2:
// OperationDef.Schedule (a cron string nothing evaluates), the TaskScheduler's
// TaskDefinition (31 hand-written entries), and the taskV2DefIDs name map.
type Schedule struct{ /* unexported */ }

func Every(d time.Duration) *Schedule              // durable interval clock (v2 interval_clock.go)
func DailyAt(hhmm string) *Schedule                // server-local (v2 daily_at.go)
func Cron(expr string) *Schedule                   // parsed and validated at registration
func InMaintenanceWindow(order int) *Schedule      // member of the window pipeline, in order

// Modifiers.
func (s *Schedule) OnStart() *Schedule
func (s *Schedule) When(enabled func() bool) *Schedule // config gate, re-read each tick
func (s *Schedule) Params(p any) *Schedule              // params for scheduled runs (mode is Live only if stated)
func (s *Schedule) Live() *Schedule                     // scheduled writes must say so explicitly
```

A schedule that fires while the def's previous run is queued or running coalesces into it
(v2's cron dedupe, `registry.go:848`). Missed fires are counted (`ops_schedule_missed_total`)
and at most one catch-up run is enqueued.

---

## 9. Lanes (where work may run)

```go
// Lane names an executor class. In-process is implicit.
type Lane string

const (
	// LaneMac: remote workers on the Macs (the fpworker protocol). The ONLY
	// lane allowed to decode audio (fingerprinting, transcription). A def that
	// decodes must declare it and must not fall back to in-process decoding.
	LaneMac Lane = "mac"
)
```

v2's subprocess isolation (`Isolate`, `internal/operations/registry/subprocess.go`, 327
lines, wired in `cmd/child_mode.go`) has zero users and is not carried forward.

---

## 10. Effects

```go
type Effects struct{ reads, writes, deletes []Resource; readOnly bool }

func ReadOnly(reads ...Resource) Effects
func Writes(ws ...Resource) Effects            // reads default to the same set
func (e Effects) AlsoReads(rs ...Resource) Effects
func Deletes(rs ...Resource) Resource          // wraps; see Writer note

// Resources: v2's set (ResBooks, ResBookFiles, ResAuthors, ResSeries,
// ResReviewItems, ResEmbeddings, ResOperations, ResCatalog) plus the ones v2
// ops write without a name today: ResSettings, ResActivity, ResFiles (bytes
// on disk), ResExternalIDs, ResUserState, ResMetadataCache, ResFieldStates,
// ResIndexes.
```

Every def that writes declares its write set (v2: 27 of 234 do, census `writes` column), so the dispatcher's
write-set gate covers all writers, not an opt-in subset.

---

## 11. Registration

```go
// Bundle is what a plugin package exports. One per package.
type Bundle struct {
	Plugin string
	Defs   []Definition
	// Requires lists services the bundle needs at startup; the catalog fails
	// boot if one is missing instead of registering a def that will panic.
	Requires []ServiceKey
}

// internal/opscatalog/catalog.go — the only list. cmd/server imports it.
var All = []func(Deps) ops.Bundle{
	acoustid.Ops,
	dedup.Ops,
	deluge.Ops,
	itunesplugin.Ops,
	maintenance.Ops,
	metafetch.Ops,
	library.Ops,   // was internal/server addOpRegistrar files
	scheduler.Ops, // was ExtraOpsRegistrar
	// ...
}
```

### 11.1 Domain packages, narrow dependencies, readiness

**Domain packages.** Workstream 07 splits `internal/plugins/maintenance` (78k lines, 103 ops
per 07) by domain. Each domain package has this shape and nothing else is needed to register:

```go
// internal/plugins/bookfiles/ops.go  (one of the domain packages carved out of maintenance)
package bookfiles

// Deps lists exactly what this package's ops call. Each field is a narrow capability
// interface declared HERE, next to its consumer, never database.Store.
type Deps struct {
	Files  BookFileRows   // GetBookFilesForBook, RepointBookFile (no delete method exists)
	Books  BookReader     // GetBookByID, IterateBookIDs
	Scan   ops.StandDown  // framework-provided
}

type BookFileRows interface {
	GetBookFilesForBook(ctx context.Context, bookID string) ([]database.BookFile, error)
	RepointBookFile(ctx context.Context, fileID, newPath string) error
}

func Ops(d Deps) ops.Bundle {
	return ops.Bundle{
		Plugin: "maintenance", // plugin namespace kept, so op ids do not change (R18)
		Defs:   []ops.Definition{markMissing(d), repointMissing(d) /* ... */},
	}
}
```

`opscatalog` builds each package's `Deps` from the concrete store once
(`bookfiles.Deps{Files: store, Books: store}`); a missing method is a **compile error in the
catalog**, not a runtime type assertion. Moving an op between domain packages never changes
its ID, because the ID string is in the def, not derived from the package path.

**Never `database.Store`.** `oplint` rejects a `Deps` field (or any closure-captured value
inside a def) whose type is `database.Store` or an interface embedding it. This removes the
pattern 07 measured, in which op-side type assertions pinned to `Store` grew from 3 to 14 through
the `indexedStore` wrapper, and so lets 07 delete that wrapper: an op that needs an index
capability names the narrow interface, and the catalog passes whichever concrete type
implements it.

**Readiness.** `Effects` gains `NeedsReady ops.Readiness` with values `ReadyNone` (pure
housekeeping that does not read the store), `ReadyStore` (default for any def whose `Deps`
include a store interface) and `ReadyIndexes` (reads memdb indexes or search). The
dispatcher holds such runs in `queued` with reason `waiting_ready:<signal>` until the
readiness state from 07's PRs R2-R4 reports the signal. That covers about 130-200 s after a
restart, per 07. Schedules that fire during warmup coalesce instead of piling up. The
readiness wait shows in the run's progress and does not count toward `ProgressTimeout`.

**Permissions are mandatory.** `ValidateCatalog` refuses a def with an empty `Permission`.
The census (04 §2.2) found every surviving `maintenance.*` twin declares none, which would
leave deleting ops like purge-deleted behind only the generic `scan.trigger` route permission.
A def that is meant to be open to editors says so explicitly.

Failure points, in order:
1. **Build (`make ci`):** `oplint` reports any call to `ops.Task/Batch/Fixer/Pipeline` whose
   result is not reachable from the package's `Ops` bundle, any package that defines a bundle
   but is not in `opscatalog.All`, any write call on a store inside a `Run`/`Item`/`Apply`
   that bypasses the Writer, and `Concurrency: Sequential("")`.
2. **Test:** `TestCatalogLinkedIntoBinary` (generalizes
   `internal/plugins/plugins_wiring_test.go:23`) runs `go list -deps ./cmd/...` and requires
   every bundle package.
3. **Startup:** `ValidateCatalog` refuses to start on a duplicate ID, an invalid schedule, an
   `Unordered` source with `ResumeContinue`, `PartitionBy` on a non-`Snapshot` source, a
   Fixer without `Writes`, a native def without `Permission` (D1: adapted v2 defs get
   `settings.manage` from 08 PR X2 until ported), a scheduled writer whose schedule has
   neither `.Live()` nor an explicit preview note (D8), or a ledger ID (the embedded
   `op_ids.golden`) that resolves to no def, alias or tombstone.

---

## 12. Test harness (`pkg/ops/opstest`)

```go
// World is an in-memory store + fake clock + seeded scheduler.
func NewWorld(t testing.TB, opts ...WorldOpt) *World

func (w *World) Books() *FakeBooks           // implements the read interfaces ops use
func (w *World) Run(def ops.Definition, params any, opts ...RunOpt) *Outcome

type RunOpt func(*runCfg)

func Live() RunOpt                         // default is Preview, as in prod
func Seed(n int64) RunOpt                  // completion order of concurrent items
func Workers(n int) RunOpt                 // default GOMAXPROCS, never 1
func ChunkSize(n int) RunOpt               // override the def's ChunkSize (Conformance also runs 1, 3 and the default)
func CancelAt(item int) RunOpt             // cancel when item n starts
func CrashAt(item int) RunOpt              // stop the world after item n; Outcome.Resume() continues
func CrashMidChunk(chunk, item int) RunOpt // stop the world while chunk c is leased and item i of it is done;
                                           // Resume() must re-run chunk c from its first item, never re-run
                                           // any chunk the ledger recorded, run every item >= 1 time and the
                                           // items of chunk c <= 2 times, and leave Done == Total
func InsertBelowCursor(item any) RunOpt    // proves an AppendOnly claim or fails it
func AbandonAt(item int) RunOpt            // item goroutine ignores ctx; asserts fenced writes
func LoseLeaseAt(item int) RunOpt          // the scan stand-down lease, not a chunk lease

type Outcome struct {
	State    state.State
	Result   json.RawMessage
	Writes   []WriteRecord   // in commit order
	History  []HistoryRow    // in commit order
	Intents  []Intent        // unresolved at end
	Progress []ops.ProgressSnapshot
	Plan     []ops.Row       // Fixer preview rows / recorded intents
}

func (o *Outcome) Resume(opts ...RunOpt) *Outcome

// Assertions every op gets for free by calling opstest.Conformance(t, def, params):
//   - Preview performs zero writes;
//   - every History row follows its Write (ledger-after-write);
//   - no Write after the fence is revoked (CancelAt, AbandonAt, LoseLeaseAt);
//   - crash + resume (CrashAt and CrashMidChunk at several chunks) processes
//     every item at least once and re-runs no chunk the ledger recorded;
//     with PartitionBy, every item of one partition ran on one worker in
//     source order; for AppendOnly, InsertBelowCursor fails the claim;
//   - progress Done is monotone and ends equal to Total when Total is known;
//     Chunks.Done ends equal to Chunks.Total;
//   - runs under -race with Workers >= 4 and ChunkSize in {1, 3, default}.
func Conformance(t *testing.T, def ops.Definition, params any, opts ...ConformanceOpt)
```

---

## 13. Generated surfaces

- `GET /api/v3/ops/defs` — every def: id, title, help, kind, effects, schedule (with the
  next fire time), exclusive key, permission, approval, params JSON Schema (from the `P` struct
  and `ops:` tags), result schema.
- `POST /api/v3/ops/runs` — `{def, params, mode, plan_ref?, rows?}`; params validated against
  the schema before anything is queued (v2: `ParamsSchema` exists, set by 0 defs).
- `web/src/generated/ops.ts` — `RunState` union, `isTerminal`, `isResumable`, `isActive`
  generated from `internal/operations/state` (`go generate`), so the four disagreeing
  classifiers (F2) become one.
- `web/src/components/ops/OpForm.tsx` renders a form from the schema; the Operations page
  gets "Run…" for any def the user may trigger, and Fixers render in the Repairs lane from the
  same def metadata.
