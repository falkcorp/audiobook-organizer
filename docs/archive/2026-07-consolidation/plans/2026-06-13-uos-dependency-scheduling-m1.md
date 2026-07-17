<!-- file: docs/plans/2026-06-13-uos-dependency-scheduling-m1.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b1e9c34-2f85-4a07-9d61-3c8a5e7f2b40 -->
<!-- last-edited: 2026-06-13 -->

# UOS Dependency Scheduling — M1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development to execute task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Land the core dependency engine for the unified operations system —
prerequisite (`op_completed`) requirements, the `dep_rev` freshness model,
completion recording, a `waiting_deps` parked state with event-driven + sweep
re-evaluation, failure propagation, and a cycle guard. **Additive:** ops without
`Requires` behave exactly as today.

**Architecture:** New `deps.go` (pure evaluator + dep_rev helpers) + new persistence
methods on the op store, integrated at three registry seams: enqueue (park if
unmet), worker-complete (record completion + wake dependents), worker-fail
(propagate failure). M1 ships op-completed requirements only; field-set (M2) and
batching (M3) extend the same machinery.

**Spec:** `docs/specs/2026-06-13-uos-dependency-scheduling-design.md`

**Tech stack:** Go, the existing `internal/operations/registry` package, the Pebble
op store (`internal/database/pebble_store_ops_v2.go`), `OperationV2Row`.

---

## Pre-flight (the implementer does this first, before Task 1)

Read these to pin the real integration points (the plan references them but exact
signatures must be confirmed):
- `internal/operations/registry/registry.go` — `EnqueueOp(ctx, defID, params, opts...)`,
  `EnqueueOptions`/`EnqueueOption`, where the row is created + set to `"queued"`.
- `internal/operations/registry/worker.go` — where status becomes `"completed"`
  (~line 211) and `"failed"` (~line 207); this is where C2/failure-propagation hook.
- `internal/operations/registry/dispatcher.go` — how `"queued"` rows are picked up
  (so `"waiting_deps"` rows are NOT picked up until promoted).
- `internal/database/iface_ops_v2.go` + `pebble_store_ops_v2.go` — `OperationV2Row`
  fields + the op-store interface to extend; the keyspace prefix convention.
- `internal/operations/registry/types.go` — `OperationDef`, `EnqueueOption`.

Confirm: the op-store interface name, how to add fields to `OperationV2Row`
(migration vs additive JSON), and how the registry resolves a subject from params
(there may be no convention yet — if not, Task 3 introduces `SubjectFromParams`).

---

## File structure

| File | Responsibility | New/Modify |
|---|---|---|
| `internal/operations/registry/types.go` | `Subject`, `Requirement`, `RequirementKind`, `OperationDef.Requires`, `WithRequires` option | Modify |
| `internal/operations/registry/deps.go` | requirement evaluator + `dep_rev` helpers + cycle check | Create |
| `internal/operations/registry/deps_test.go` | evaluator/cycle unit tests | Create |
| `internal/database/iface_ops_v2.go` | op-store interface: completion + dep_rev + waiting-deps queries | Modify |
| `internal/database/pebble_store_ops_v2.go` | Pebble impl of the new store methods | Modify |
| `internal/database/pebble_store_ops_v2_test.go` | store round-trip tests | Modify/Create |
| `internal/operations/registry/registry.go` | enqueue: park if unmet; promotion API | Modify |
| `internal/operations/registry/worker.go` | record completion on success; propagate failure | Modify |
| `internal/operations/registry/deps_scheduler.go` | event-driven + sweep re-evaluation loop | Create |

---

## Task 1: Types — `Subject`, `Requirement`, `OperationDef.Requires`, `WithRequires`

**Files:** Modify `internal/operations/registry/types.go`; Test `deps_test.go` (compile-only here).

- [ ] **Step 1: Add the types**

```go
// Subject identifies the entity a requirement/completion is about. v1: book.
type Subject struct {
	Type string // "book"
	ID   string
}

type RequirementKind string

const (
	ReqOpCompleted RequirementKind = "op_completed"
	ReqFieldSet    RequirementKind = "field_set" // M2; type defined now for stability
)

// Requirement is a single prerequisite/condition for an op to become runnable.
type Requirement struct {
	Kind        RequirementKind `json:"kind"`
	OpType      string          `json:"op_type,omitempty"`      // ReqOpCompleted: required op def ID
	Field       string          `json:"field,omitempty"`        // ReqFieldSet: subject field (M2)
	SubjectType string          `json:"subject_type,omitempty"` // override; default = dependent's subject type
	AllFiles    bool            `json:"all_files,omitempty"`    // book subject: require completion for every file
}
```

Add to `OperationDef` (near `DependsOn`, with a comment distinguishing them):

```go
	// Requires are standing prerequisites evaluated before this op runs. Unlike
	// DependsOn (which means "must NOT run concurrently"), Requires means "these
	// must be SATISFIED first". Op-completed requirements only in M1.
	Requires []Requirement
```

Add the enqueue option (mirror the existing `WithPriority`/`WithParent` pattern in
types.go):

```go
// WithRequires adds per-enqueue requirements on top of the def's Requires.
func WithRequires(reqs ...Requirement) EnqueueOption {
	return func(o *EnqueueOptions) { o.Requires = append(o.Requires, reqs...) }
}
```

Add `Requires []Requirement` to the `EnqueueOptions` struct.

- [ ] **Step 2: Build** `go build ./internal/operations/... ` → clean.
- [ ] **Step 3: Commit** `feat(uos): dependency requirement + subject types`.

## Task 2: Op store — completion records + dep_rev + waiting-deps persistence

**Files:** Modify `internal/database/iface_ops_v2.go`, `pebble_store_ops_v2.go`; Test `pebble_store_ops_v2_test.go`.

- [ ] **Step 1: Write failing store round-trip test**

```go
func TestOpCompletionAndDepRev_RoundTrip(t *testing.T) {
	s := newTestOpStore(t) // mirror existing op-store test setup
	sub := database.OpSubject{Type: "book", ID: "b1"}

	// dep_rev starts at 0; bump → 1.
	if got, _ := s.GetDepRev(sub); got != 0 { t.Fatalf("dep_rev0=%d", got) }
	if _, err := s.BumpDepRev(sub); err != nil { t.Fatal(err) }
	if got, _ := s.GetDepRev(sub); got != 1 { t.Fatalf("dep_rev1=%d", got) }

	// record a completion at rev 1, then assert it reads back.
	if err := s.RecordOpCompletion(sub, "acoustid.fingerprint-extract", "", 1); err != nil { t.Fatal(err) }
	rev, ok, _ := s.GetOpCompletion(sub, "acoustid.fingerprint-extract")
	if !ok || rev != 1 { t.Fatalf("completion rev=%d ok=%v", rev, ok) }

	// bump again → completion (rev1) is now stale vs current rev2.
	s.BumpDepRev(sub)
	cur, _ := s.GetDepRev(sub)
	if cur != 2 { t.Fatalf("cur=%d", cur) }
}
```

> NOTE: name the subject type to match what the store package can import without a
> cycle — if `registry.Subject` would cause an import cycle, define the persisted
> shape as `database.OpSubject` here and convert in the registry. Check import
> direction (registry imports database, not vice-versa) and choose accordingly.

- [ ] **Step 2: Run → fails** (methods undefined).
- [ ] **Step 3: Implement the store methods** in `pebble_store_ops_v2.go`, mirroring
the existing Pebble JSON-value + `db.Set/Get` pattern in that file. Keyspaces:
`op:completion:<type>:<id>:<opType>[:<fileID>]` and `op:deprev:<type>:<id>`.
Add to the op-store interface in `iface_ops_v2.go`:

```go
type OpSubject struct{ Type, ID string }

GetDepRev(sub OpSubject) (uint64, error)
BumpDepRev(sub OpSubject) (uint64, error)
RecordOpCompletion(sub OpSubject, opType, fileID string, depRev uint64) error
GetOpCompletion(sub OpSubject, opType string) (depRev uint64, ok bool, err error)
ListFileCompletions(sub OpSubject, opType string) (map[string]uint64, error) // fileID->rev, for AllFiles
ListWaitingDepsOps() ([]OperationV2Row, error)                               // status == "waiting_deps"
```

Add `OperationV2Row` fields: `SubjectType, SubjectID string`, `Requirements string`
(JSON), `ReqSnapshotRev uint64`. Persist them with the row (additive — old rows
read back with zero values, which means "no requirements").

- [ ] **Step 4: Run → passes.** Also run the existing op-store tests (no regression).
- [ ] **Step 5: Commit** `feat(uos): op-store completion records + dep_rev keyspace`.

## Task 3: Requirement evaluator + cycle check (`deps.go`)

**Files:** Create `internal/operations/registry/deps.go`, `deps_test.go`.

- [ ] **Step 1: Write failing tests**

```go
func TestEvaluate_OpCompleted_FreshVsStale(t *testing.T) {
	st := newFakeDepStore() // implements the small DepStore interface (Task 3 defines it)
	sub := Subject{Type: "book", ID: "b1"}
	st.depRev[key(sub)] = 2
	req := Requirement{Kind: ReqOpCompleted, OpType: "fp"}

	// no completion → unmet
	if ok, _ := Satisfied(st, req, sub); ok { t.Fatal("should be unmet without completion") }
	// completion at rev1 but current rev2 → stale → unmet
	st.completion[ckey(sub, "fp")] = 1
	if ok, _ := Satisfied(st, req, sub); ok { t.Fatal("stale completion must not satisfy") }
	// completion at rev2 → satisfied
	st.completion[ckey(sub, "fp")] = 2
	if ok, _ := Satisfied(st, req, sub); !ok { t.Fatal("fresh completion must satisfy") }
}

func TestEvaluate_AllFiles_Coverage(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "b1"}
	st.depRev[key(sub)] = 1
	st.files[sub.ID] = []string{"f1", "f2"}
	req := Requirement{Kind: ReqOpCompleted, OpType: "fp", AllFiles: true}
	st.fileCompletion[ckey(sub, "fp")] = map[string]uint64{"f1": 1} // only f1 done
	if ok, _ := Satisfied(st, req, sub); ok { t.Fatal("partial coverage must not satisfy") }
	st.fileCompletion[ckey(sub, "fp")]["f2"] = 1
	if ok, _ := Satisfied(st, req, sub); !ok { t.Fatal("full coverage must satisfy") }
}

func TestCycleDetection(t *testing.T) {
	defs := map[string][]Requirement{
		"a": {{Kind: ReqOpCompleted, OpType: "b"}},
		"b": {{Kind: ReqOpCompleted, OpType: "a"}},
	}
	if err := CheckRequirementCycle(defs); err == nil { t.Fatal("expected cycle error") }
}
```

- [ ] **Step 2: Run → fails.**
- [ ] **Step 3: Implement.** Define a narrow `DepStore` interface (the subset of the
op store the evaluator needs: `GetDepRev`, `GetOpCompletion`, `ListFileCompletions`,
plus a `BookFiles(bookID) []string`). Implement:
- `Satisfied(store DepStore, req Requirement, sub Subject) (bool, string, error)` —
  op-completed (fresh ≥ current rev), AllFiles (every file covered at ≥ current rev),
  `ReqFieldSet` returns `(false, "field_set not implemented in M1", nil)` for now.
- `AllSatisfied(store, reqs []Requirement, sub Subject) (bool, firstUnmet string, err error)`.
- `CheckRequirementCycle(defReqsByOpType map[string][]Requirement) error` — DFS,
  returns an error naming the cycle.
- [ ] **Step 4: Run → passes;** `gofmt`/`go vet` clean.
- [ ] **Step 5: Commit** `feat(uos): requirement evaluator + cycle detection`.

## Task 4: Enqueue integration — park unmet ops as `waiting_deps`

**Files:** Modify `registry.go`; Test `registry_test.go`.

- [ ] **Step 1: Failing test** — enqueue an op (registered with a `Requires` on an
op-type not yet completed for its subject) and assert its row status is
`"waiting_deps"`, not `"queued"`; the dispatcher does not pick it up.
- [ ] **Step 2: Run → fails.**
- [ ] **Step 3: Implement.** In `EnqueueOp`, after resolving def + options:
  - Combine `def.Requires` + `opts.Requires`.
  - Resolve the op's `Subject` from params (introduce `SubjectFromParams(params) Subject`
    — v1: read `book_id`; empty subject + non-empty requirements is an error unless all
    requirements carry an explicit `SubjectType`).
  - If requirements is empty → existing path (`"queued"`).
  - Else evaluate `AllSatisfied`. If satisfied → `"queued"`. If not → create the row
    with status `"waiting_deps"`, persisting `Requirements` (JSON) + `ReqSnapshotRev`
    (current dep_rev of the subject). Index it for wakeups (Task 5).
  - Run `CheckRequirementCycle` over registered defs once at registry startup (not per
    enqueue) and fail registration of a cycle.
- [ ] **Step 4: Run → passes;** existing registry tests still pass.
- [ ] **Step 5: Commit** `feat(uos): park ops with unmet requirements as waiting_deps`.

## Task 5: Completion recording + dependent wakeup + failure propagation

**Files:** Modify `worker.go`; Create `deps_scheduler.go`; Test `registry_test.go`.

- [ ] **Step 1: Failing test** — enqueue dependent D requiring op-type X for book b1
(parked). Enqueue + complete an X op for b1. Assert D is promoted `waiting_deps →
queued`. Separately: when X **fails**, assert D moves to `"failed"` with reason
containing `unmet dependency`.
- [ ] **Step 2: Run → fails.**
- [ ] **Step 3: Implement.**
  - In `worker.go` on `"completed"`: if the op has a subject, call
    `store.RecordOpCompletion(sub, defID, fileID, currentDepRev(sub))`, then notify the
    scheduler (`deps_scheduler.OnOpCompleted(sub, defID)`).
  - In `worker.go` on `"failed"`: notify `deps_scheduler.OnOpFailed(sub, defID)`.
  - `deps_scheduler.go`:
    - `OnOpCompleted(sub, opType)`: find `waiting_deps` ops whose subject == sub (use
      an in-memory index keyed by `(subjectType, subjectID)` rebuilt from
      `ListWaitingDepsOps()` at startup). Re-evaluate each; promote satisfied ones to
      `"queued"` (and signal the dispatcher).
    - `OnOpFailed(sub, opType)`: fail any `waiting_deps` op hard-requiring `opType` for
      `sub` with reason `unmet dependency: <opType> failed`.
    - `BumpAndReevaluate(sub)`: bump dep_rev + re-evaluate (callable by producers; used
      in M-later).
    - `SweepTick()`: re-evaluate all `waiting_deps` ops (self-heal + future field
      conditions); wire to a low-frequency ticker started with the registry.
- [ ] **Step 4: Run → passes.** Add a restart test: parked ops re-load via
`ListWaitingDepsOps()` and a subsequent completion still promotes them.
- [ ] **Step 5: Commit** `feat(uos): wake/fail dependents on op completion + sweep`.

## Task 6: End-to-end proof + gofmt/vet/full tests

- [ ] **Step 1:** Add an integration test in the registry package: register two test
op defs where `B` has `Requires{ReqOpCompleted, OpType: "A"}`; enqueue B (parks),
enqueue+run A for the same subject, assert B runs and completes. This proves the
whole M1 path with no production op touched.
- [ ] **Step 2:** `gofmt -l internal/operations/registry internal/database` (empty),
`go vet ./internal/operations/... ./internal/database/...` (clean),
`go test ./internal/operations/... ./internal/database/...` (PASS).
- [ ] **Step 3: Commit** `test(uos): end-to-end dependency-ordering integration test`.

---

## Self-review

- **Spec coverage (M1 slice):** types (Task 1), persistence/dep_rev/completion
  (Task 2), evaluator + cycle (Task 3), enqueue parking (Task 4), wake/fail/sweep
  (Task 5), e2e proof (Task 6). field-set evaluator returns "not implemented"
  (M2); batching is M3; dedup migration is M4 — all out of M1 scope by design.
- **Additive guarantee:** ops with no `Requires` skip every new branch → behavior
  identical to today. The `waiting_deps` status is only ever set for ops that opt in.
- **Type consistency:** `Subject`(registry) ↔ `OpSubject`(database) conversion is
  the one boundary to keep straight (Task 2 NOTE) — pick one persisted shape and
  convert at the registry edge to avoid an import cycle.
- **Riskiest seam:** `worker.go` status transitions (Task 5). The implementer must
  confirm the exact transition points (~worker.go:207/211) and that notifying the
  scheduler there can't deadlock the worker (notify async / via a channel, not under
  a held lock).

## Execution

Subagent-driven, one task per subagent, spec-review + quality-review each, final
holistic review. **Do NOT auto-merge/deploy** — this is the core op registry; open a
PR for human review before it reaches prod.
