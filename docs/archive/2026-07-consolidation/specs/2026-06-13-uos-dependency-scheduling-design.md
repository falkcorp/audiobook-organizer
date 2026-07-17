<!-- file: docs/specs/2026-06-13-uos-dependency-scheduling-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f8b1a52-9c47-4e60-bd13-7a2e5c8f4d09 -->
<!-- last-edited: 2026-06-13 -->

# Unified Operations System — Dependency & Condition Scheduling

## Motivation

Today the unified operations system (UOS) can run, serialize, schedule (cron), and
event-trigger operations, but it cannot express **"run this op only after these
other things are done / these fields are populated."** Concretely:

- `OperationDef.DependsOn []string` is **misnamed** — it means "op def IDs that must
  NOT be running concurrently with this op" (anti-concurrency), the *inverse* of a
  prerequisite. We keep it as-is and add a real prerequisite concept beside it.
- There is no way to say "dedup for book B requires fingerprinting + metadata for B
  to have completed first," so dedup-on-import is wired as an **eager
  `CheckBook` goroutine** (`internal/importer/service.go`) that fires before the
  book is even fingerprinted — meaning whole-book-signature matching can't run there
  at all (the signature doesn't exist yet).
- Bulk imports spawn **N per-book operations** instead of batching, flooding the
  queue.

This spec adds a **systemd-inspired dependency, condition, and batching layer** to
the UOS so ordering is *declarative and correct everywhere*, not hand-wired per
caller. Dedup is the motivating consumer, but the mechanism is general.

### systemd mapping (the mental model)

| systemd | this design |
|---|---|
| `After=` / `Before=` | implied ordering from a satisfied `Requires` |
| `Requires=` | hard prerequisite — op-completed requirement (fail dependent if it fails) |
| `Wants=` | soft prerequisite (later milestone) |
| `ExecCondition=` / `Condition*=` | `fieldSet` condition — skip/park until true |
| `ConditionPathExists=` / `AssertPathExists=` | path condition (deferred, M-later) |

## Goals

- Declare, per op, **prerequisites** that must be satisfied before it runs:
  - **op-completed**: op-type X has completed for this subject *since the subject
    last changed*.
  - **field-populated**: a named field on the subject is non-empty.
- Allow these to be declared **statically** in the `OperationDef` *and* **added at
  enqueue time**.
- **Re-trigger on change**: when a subject's relevant data changes, its prerequisites
  become unsatisfied again (freshness, not "ever").
- **Batch** burst enqueues of the same batchable op-type into one operation.
- Survive restart (parked ops, completion records, and revisions persist).
- Migrate dedup-on-import off the eager hook onto declarative requirements.

## Non-goals (v1)

- Path/file conditions (`ConditionPathExists`) — deferred.
- Soft `Wants` dependencies — deferred (v1 is hard `Requires` only).
- True file-level dependency subjects — v1 subjects are **book**-scoped, with an
  all-files-of-book aggregator for per-file prerequisites.
- Cross-machine / distributed scheduling — single-process registry as today.

## Decisions (locked during design)

1. **Declaration:** both def-level defaults (`OperationDef.Requires`) and
   per-enqueue additions (`WithRequires(...)` option).
2. **Freshness:** "since subject last changed" via a dedicated per-subject
   **`dep_rev`** counter (not `Book.UpdatedAt`, which churns for unrelated reasons).
3. **v1 conditions:** op-completed prerequisites **and** field-populated conditions.
   Path conditions deferred.
4. **Batching:** time-window **debounce** — batchable ops bucket by op-type and
   dispatch one batched op after the window quiesces.
5. **Prereq failure:** a hard `Requires` whose prerequisite op fails terminally
   **fails the dependent** with reason `unmet dependency: <opType> failed`. (Soft
   `Wants` that proceeds anyway is a later addition.)
6. **Subject granularity:** v1 subject = **book**. Per-file ops record completion
   keyed to their **book**; a book-level op-completed requirement is satisfied only
   when the op has completed for **all** of the book's files at the current `dep_rev`.
7. **`dep_rev` bump triggers:** a dedicated per-subject counter bumped by the
   specific events that matter (file content/hash change, metadata apply), emitted
   by the producers of those changes.

## Data model

### Subject

```go
// Subject identifies the entity a requirement / completion is about.
type Subject struct {
    Type string // "book" (v1); "file" reserved
    ID   string
}
```

A running op's subject is derived from its params (e.g. `book_id`). Ops that don't
operate on a subject (global maintenance, cron sweeps) have an empty subject and
cannot be required-on (they may still *have* requirements that are global).

### Requirement

```go
type RequirementKind string

const (
    ReqOpCompleted RequirementKind = "op_completed" // op-type X done for subject @ current dep_rev
    ReqFieldSet    RequirementKind = "field_set"    // named field non-empty on subject
)

type Requirement struct {
    Kind    RequirementKind
    OpType  string // for ReqOpCompleted: the required op def ID
    Field   string // for ReqFieldSet:  the subject field that must be non-empty
    // Subject defaults to the dependent op's own subject. A non-empty SubjectType
    // override lets an op require something about a related subject (rare in v1).
    SubjectType string
    AllFiles    bool // ReqOpCompleted + book subject: require completion for ALL files of the book
}
```

### OperationDef additions

```go
// Dependencies & conditions. Optional.
Requires []Requirement // standing prerequisites for every enqueue of this op
// Batching. Optional.
Batchable   bool          // coalesce burst enqueues of this op type
BatchWindow time.Duration // debounce window; 0 with Batchable => default (e.g. 5s)
BatchMaxWait time.Duration // hard cap so a steady trickle still dispatches; 0 => default (e.g. 60s)
```

`EnqueueOption` gains `WithRequires(reqs ...Requirement)` (additive to the def's).

### Persistence (Pebble keyspaces in the op store)

- `op:completion:<subjectType>:<subjectID>:<opType>` → `{dep_rev, file_id?, completed_at}`
  (one row per (subject, op-type); for `AllFiles` requirements we store per-file
  completion rows `...:<opType>:<fileID>` and the requirement checks coverage).
- `op:deprev:<subjectType>:<subjectID>` → `uint64` current revision (default 0).
- Parked ops use the existing `OperationV2Row` with a new status **`waiting_deps`**
  plus a serialized `requirements` blob and a snapshot of `dep_rev` at enqueue.

`OperationV2Row` gains: `Status` value `"waiting_deps"`, `SubjectType`, `SubjectID`,
`Requirements string` (JSON), `RequirementsSnapshotRev uint64`.

## Components

### C1. Requirement evaluation (`internal/operations/registry/deps.go`)

Pure-ish evaluator: `Satisfied(store, req, subject) (bool, reason string, error)`.
- `ReqOpCompleted` (non-AllFiles): a completion row exists for `(subject, opType)`
  with `dep_rev >= currentDepRev(subject)`.
- `ReqOpCompleted` + `AllFiles`: every file of the book has a completion row for
  `opType` at `>= currentDepRev`. (Files enumerated via the store.)
- `ReqFieldSet`: load the subject (book) and check the named field non-empty. A
  small allow-list maps field names → accessors (`book_sig_v1`,
  `acoustid_fingerprint`, `metadata_source_hash`, …) so this stays type-safe.

`AllSatisfied(store, reqs, subject)` returns `(ready bool, firstUnmet string)`.

### C2. Completion recording (`registry/worker.go`)

When an op transitions to `completed`, if it has a subject, write/refresh
`op:completion:<…>:<opType>` with the **current** `dep_rev` of the subject (and the
file id when the op is file-scoped). This is the signal `ReqOpCompleted` reads.

### C3. dep_rev bumping (`registry/deps.go` + producers)

`BumpDepRev(store, subject)` increments `op:deprev:<…>`. Called by the events that
change a subject's content/metadata:
- file content/hash change (scanner / writeback),
- metadata apply (metadata pipeline).
Producers call a thin `registry.BumpDepRev(book)` (or publish a `subject.changed`
event the registry subscribes to — see C5). Bumping invalidates prior completions
for that subject (they now have a lower `dep_rev`), so dependents re-require.

### C4. Scheduler integration (`registry/registry.go`, `dispatcher.go`)

- **Enqueue:** if an op has any requirements, evaluate them. If all satisfied →
  normal `queued`. Else → persist as `waiting_deps` with its requirements + the
  current snapshot rev.
- **Readiness re-evaluation:**
  - **Event-driven:** after every op `completed` (C2) and every `dep_rev` bump
    (C3) / `subject.changed` event, re-evaluate `waiting_deps` ops whose subject or
    required op-type matches. Satisfied → move to `queued`. (Index parked ops by
    `(subjectType, subjectID)` and by required `opType` for O(matches) wakeups.)
  - **Periodic sweep:** a low-frequency tick re-evaluates all `waiting_deps` ops to
    catch `field_set` conditions satisfied by non-op events and to self-heal missed
    wakeups.
- **Failure propagation:** when a required op fails terminally, any `waiting_deps`
  op that hard-requires it (`ReqOpCompleted` on that op-type for that subject) is
  moved to `failed` with `unmet dependency: <opType> failed`.
- **Cycle / starvation guards:** detect a requirement cycle at enqueue (DFS over
  def-level `Requires` by op-type) and reject with a clear error; a parked op past a
  max-wait (configurable, e.g. 24h) is failed with `dependency timeout`.

### C5. Change events (optional wiring)

Reuse the existing event bus (`Triggers`/`EventSubscription`). Producers publish
`subject.changed{type,id,reason}`; the registry subscribes and calls `BumpDepRev`
+ re-evaluation. Direct `BumpDepRev` calls are the fallback where an event is
overkill.

### C6. Batching (`registry/batch.go`)

For `Batchable` op defs, `EnqueueOp` does not immediately create a row; it adds the
subject to a per-op-type **bucket** with a debounce timer (`BatchWindow`, capped by
`BatchMaxWait`). When the timer fires, one `OperationV2Row` is created whose params
carry the collected subject set (`{"subjects": [...]}`); the op's `Run` iterates the
set. Requirements on a batched op are evaluated **per subject** at dispatch — only
subjects whose requirements are met are included; the rest stay bucketed (or re-park).
Persistence: the bucket is journaled (`op:batch:<opType>` → pending subject set) so a
restart mid-window doesn't drop subjects.

## Migration: dedup-on-import (M4)

Replace the eager `CheckBook` goroutine in `internal/importer/service.go` with a
declarative enqueue:

```go
// on import, instead of: go dedupEngine.CheckBook(book)
registry.EnqueueOp(ctx, "dedup.check-book", params(book),
    registry.WithRequires(
        Requirement{Kind: ReqOpCompleted, OpType: "acoustid.fingerprint-extract", AllFiles: true},
        Requirement{Kind: ReqFieldSet, Field: "book_sig_v1"},
        Requirement{Kind: ReqOpCompleted, OpType: "<metadata-backfill op id>"},
    ),
)
```

The dedup op type is `Batchable` so a 50-book import produces one batched dedup pass
once each book's fingerprint + signature + metadata are ready. Whole-book-signature
matching now works because the signature is guaranteed populated by the
`fieldSet` requirement.

> Exact op IDs (`dedup.check-book` as an op vs the engine method; the metadata
> backfill op id) are pinned during M4 against the real registry.

## Milestones

- **M1 — core dependency engine + persistence + scheduler.** `Requirement`/`Subject`
  types, `OperationDef.Requires`, `WithRequires`, completion recording, `dep_rev`,
  `waiting_deps` state + event-driven & sweep re-evaluation, failure propagation,
  cycle guard. Op-completed requirements only. Additive — no existing op changes
  behavior. Wire ONE real dependency end-to-end as proof (e.g. a test op requiring a
  fingerprint completion).
- **M2 — field-populated conditions.** `ReqFieldSet` + the field allow-list +
  periodic sweep coverage.
- **M3 — batching.** `Batchable`/`BatchWindow`, the bucket + debounce dispatch,
  journaled buckets, per-subject requirement gating at dispatch.
- **M4 — migrate dedup-on-import.** Drop the `CheckBook` goroutine; enqueue
  `dedup.check-book` with `Requires`; make it batchable; verify ordering on prod.

Each milestone is independently shippable and additive until M4 (which changes the
import path behavior and is the one to validate carefully on prod).

## Testing

- C1 evaluator unit tests: op-completed (fresh vs stale `dep_rev`), AllFiles
  coverage (partial vs full), field-set (set/unset), unknown field rejected.
- C2/C3: completion row written on complete with correct `dep_rev`; bump
  invalidates a prior completion (dependent re-parks).
- C4: enqueue with unmet reqs → `waiting_deps`; completing the prereq moves it to
  `queued` (event-driven) and the sweep also promotes it; prereq failure fails the
  dependent with the right reason; cycle rejected at enqueue; restart re-loads parked
  ops and re-evaluates.
- C6: 50 rapid enqueues of a batchable op → one dispatched op carrying 50 subjects;
  a subject with unmet reqs is excluded and stays bucketed; restart mid-window keeps
  the bucket.
- M4 integration: import N books → exactly one dedup op runs, after fingerprint +
  signature + metadata, with the signature populated.

## Rollback

M1–M3 are additive: ops without `Requires`/`Batchable` behave exactly as today, so
the feature is dormant until a def opts in. M4 is the only behavior change; it sits
behind keeping the old `CheckBook` path available under a config flag
(`DedupOnImportViaScheduler`, default off until validated) so we can revert the
import wiring instantly.

## Open questions (resolved — recorded for the plan)

1. ~~Declaration site~~ → both (def + enqueue).
2. ~~Freshness~~ → dedicated `dep_rev` counter, "since changed".
3. ~~Condition types v1~~ → op-completed + field-set.
4. ~~Batching~~ → time-window debounce.
5. ~~Prereq failure~~ → fail the dependent (hard Requires).
6. ~~Subject granularity~~ → book v1, all-files-of-book aggregator.
7. ~~dep_rev source~~ → dedicated counter bumped by content/metadata producers.
