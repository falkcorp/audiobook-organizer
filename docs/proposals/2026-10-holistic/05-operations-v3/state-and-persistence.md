<!-- file: docs/proposals/2026-10-holistic/05-operations-v3/state-and-persistence.md -->
<!-- version: 1.1.0 -->
<!-- guid: 0acb837b-8274-4594-b101-9a6a064ff2dc -->
<!-- last-edited: 2026-10-09 -->

# Operations v3 — run state machine and persisted state

Parent: [`../05-operations-v3.md`](../05-operations-v3.md) §3.2, §3.8.

## 1. The state machine (one table)

`internal/operations/state` holds the only definition. Go code switches on `state.State`
(a typed string); TypeScript gets the same table generated into `web/src/generated/ops.ts`.
It replaces the four classifiers that disagree today (F2):
`database.isTerminalV2Status` (`internal/database/pebble_store_ops_v2.go:1274`),
`registry.isTerminalStatus` (`registry.go:1230`), `registry.IsTerminalStatus`
(`legacy_op_status.go:164`), `registry.IsInterruptedStatus` (`retry.go:29`), and
`web/src/services/api.ts:556` `isOperationTerminal`.

*Round-2 cut (r3): ten states, not twelve.* The first draft had `pending` (deps unmet) and
`superseded` (closed in favor of a successor). `pending` is folded into `queued` with a
`wait_reason` field (`deps`, `ready`, `pause`): readiness (D44, `sdk-api.md` §11.1) already
needed "queued but not dispatchable, with a reason", and three flavours of the same thing
are one state with a field. A run with a wait reason is not in `opv3:q:` until the reason
clears, so the dispatcher does not re-scan it. `superseded` is folded into `dropped` with
`successor_id` set: v3 resume keeps the op id (`sdk-api.md` §5), so no v3 run ever
supersedes another; the state existed only to map legacy `resumeRequeue` rows, and a field
on `dropped` records them without a state the UI must explain. Kept, each on an incident:
`stopping` (the 2026-10-01 fragment apply wrote after cancel, F4), `timed_out` (F3),
`interrupted` (quiesce and deploy resume), `awaiting_decision` (3 defs use ask today).

| state | terminal | holds slot + exclusive key | resumable at boot | retry button | discard button | v2 status it replaces |
|---|---|---|---|---|---|---|
| `queued` (`wait_reason` empty or `deps`/`ready`/`pause`) | no | no | yes | no | no | `queued`, `waiting_deps` |
| `running` | no | yes | → `interrupted{crash}` | no | no | `running` |
| `stopping` | no | **yes, until the goroutine returns** | → `interrupted{crash}` | no | no | (none: v2 wrote a terminal status and freed the slot after 5s) |
| `interrupted` | no | no | per policy | yes | yes | `interrupted_quiesced`, and `running` found at boot |
| `awaiting_decision` | no | no | no (waits) | yes | yes | `interrupted_ask` |
| `succeeded` | yes | no | no | no | yes | `completed` |
| `failed` | yes | no | no | yes (new attempt, same id) | yes | `failed` |
| `canceled` | yes | no | no | yes | yes | `canceled` (user) |
| `timed_out` | yes | no | no | yes | yes | `canceled` with a `timeout:` message |
| `dropped` (`successor_id` optional) | yes | no | no | yes (no when `successor_id` set) | yes | `interrupted_dropped`; with `successor_id`: `interrupted_dropped` + `requeued: original op replaced`, legacy `interrupted_restart` |

`stopping` carries `zombie bool` (the goroutine outlived the abandon grace) and the stop
reason (`user`, `timeout`, `watchdog`, `quiesce`, `shutdown`). When the goroutine returns, the
reason decides the terminal state: user → `canceled`, timeout/watchdog → `timed_out`,
quiesce/shutdown → `interrupted`. The UI shows a zombie as "stopping — still running, writes
refused".

**Transitions** are a closed set; `state.Next(from, event) (to, error)` rejects anything
else, and every store write is a compare-and-set on `(state, attempt, fence_epoch)` — the
generalization of v2's `SetOperationV2StatusIfQueued` (`worker.go:376`) to every transition.

```text
enqueue            → queued{wait_reason: "" | deps | ready}
wait cleared       queued{reason} → queued{}   enters opv3:q:
dispatch (CAS)     queued{} → running          attempt++ , fence_epoch++
return nil         running → succeeded
return err         running → failed
stop requested     running → stopping{reason}  fence revoked immediately
goroutine returns  stopping → canceled | timed_out | interrupted
boot sweep         running|stopping → interrupted{crash}
resume             interrupted → queued        same op id, attempt carries on
ask                interrupted → awaiting_decision (policy Ask)
decide retry       awaiting_decision|failed|canceled|timed_out|dropped → queued
decide drop        awaiting_decision|interrupted → dropped
(migration only)   v2 requeued rows → dropped{successor_id}; no v3 event produces it
```

**Zombie handling (D6).** A run in `stopping` whose goroutine has outlived the abandon
grace keeps its slot and exclusive key until the goroutine returns; it never frees them on
a timer. After 10 minutes in `stopping` the runtime logs at WARN once a minute and the
`ops.zombies` gauge drives the alert (PR 11). The only ways out are the goroutine returning
or a process restart, whose boot sweep maps it to `interrupted{crash}`.

Every transition is appended to `opv3:evt:` (below), so "why is this run in this state" is
answerable from the record instead of from journal lines.

## 2. Keyspace

New keys live under `opv3:`. Every existing key family that hangs off an operation id string
is **kept as is and not re-keyed**, because op ids do not change (ULIDs carry over):
`opchange:`, `opchange_by_book:`, `opchange_undecodable:`, `op_result:`, `opsummary:`,
`opstate:<id>` (v2 checkpoint), `act:op:` (activity by op), `op:completion:`, `op:deprev:`,
`op:batch:` (`internal/database/keyfamilies.go:221-243`).

```text
opv3:run:{op_id}                               → RunRecord (JSON, schema_version)
opv3:q:{999-prio:03d}:{queued_ns:020d}:{op_id} → ""          dispatch order (same encoding as opv2:q:)
opv3:ix:state:{state}:{op_id}                  → ""          census: exact count per state
opv3:ix:def:{def_id}:{queued_ns:020d}:{op_id}  → ""          census: runs of one def, newest last
opv3:ix:done:{completed_ns:020d}:{op_id}       → ""          timeline window
opv3:ix:parent:{parent_id}:{op_id}             → ""          pipeline children
opv3:evt:{op_id}:{ts_ns:020d}:{seq:06d}        → Transition  state-machine audit
opv3:ckpt:{op_id}                              → Checkpoint  phase, task state (rc.Checkpoint), lease table at last write
opv3:snap:{op_id}:{page:06d}                   → []string    frozen item keys, 10k per page (Snapshot sources)
opv3:snap:{op_id}:chunks                       → []int32     chunk start offsets, only when PartitionBy adjusted them
opv3:ledger:{op_id}                            → Ledger      completed chunks: bitmap (Snapshot) or key-range list (AppendOnly)
opv3:intent:{op_id}:{seq:010d}                 → Intent      write-ahead; deleted on commit
opv3:log:{op_id}:{ts_ns:020d}:{seq:010d}       → LogLine     (same shape as opv2:log:)
opv3:sched:{def_id}                            → SchedState  last fire, next fire, last run id, missed
opv3:def:{def_id}                              → DefSnapshot for rows whose def was removed
opv3:meta:counts                               → per-state counters, maintained in the same batch as the ix:state write
```

**Census vs timeline is a property of the key, not of the caller's care.** `ix:state` and
`meta:counts` give exact totals in O(1)/O(n-in-state); `ix:done` gives a time window. The API
returns them from different endpoints with different names (§3).

### 2.1 Chunk ledger and leases (D28a; design in `sdk-api.md` §5)

```go
type Ledger struct {
	SchemaVersion int      `json:"v"`
	ChunkSize     int      `json:"chunk_size"`
	ChunkCount    int      `json:"chunk_count"`         // Snapshot: fixed at freeze; AppendOnly: chunks cut so far
	Done          []byte   `json:"done,omitempty"`      // Snapshot: bitmap, bit c = chunk c complete
	Ranges        []KeyRange `json:"ranges,omitempty"`  // AppendOnly: completed {first,last} key ranges, merged
	Items         struct{ Done, Failed, Skipped int64 } `json:"items"`
	Counters      map[string]int64 `json:"counters"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Leases live in the runner's memory and are copied into opv3:ckpt: with every
// ledger write so a crash report can say which chunks were in flight.
type Lease struct {
	Chunk     int       `json:"chunk"`
	Worker    int       `json:"worker"`
	LeasedAt  time.Time `json:"leased_at"`
	Heartbeat time.Time `json:"heartbeat"`
	ItemIndex int       `json:"item_index"`
	Item      string    `json:"item"` // Label(current item)
}
```

- **One writer.** Only the runner's ledger goroutine writes `opv3:ledger:`, `opv3:ckpt:` and
  the run's `Progress`, in one Pebble batch, at most every 2 s or on each chunk completion
  when those are rarer. Workers never touch the store for bookkeeping.
- **Size.** A bitmap for 1M items at `ChunkSize` 256 is 512 bytes; the lease table is at most
  `Concurrency` entries. `opv3:snap:` pages are the same as before (keys only).
- **Resume.** The boot sweep leaves the ledger alone; the runner's next attempt reads it
  and leases every chunk not marked done, including those in the saved lease table. A
  chunk marked done is never leased again. Progress after a resume starts from the ledger's
  item counts, so `Done` never goes backwards across attempts.
- **Cleanup.** `opv3:snap:`, `opv3:snap:…:chunks` and `opv3:ledger:` are deleted when the
  run reaches a terminal state, in the same batch as the transition; the final counters
  survive in `RunRecord.Progress`.

### RunRecord

```go
type RunRecord struct {
	SchemaVersion int             `json:"v"` // 3
	ID            string          `json:"id"`
	DefID         string          `json:"def_id"`
	Kind          string          `json:"kind"`
	State         state.State     `json:"state"`
	WaitReason    string          `json:"wait_reason,omitempty"` // queued only: deps | ready | pause
	StopReason    string          `json:"stop_reason,omitempty"`
	Zombie        bool            `json:"zombie,omitempty"`
	Mode          string          `json:"mode"` // preview | live
	Trigger       string          `json:"trigger"`
	Params        json.RawMessage `json:"params"`
	ParentID      string          `json:"parent_id,omitempty"`
	PipelineStage string          `json:"pipeline_stage,omitempty"`
	SuccessorID   string          `json:"successor_id,omitempty"` // dropped only; migrated v2 requeue rows
	Attempt       int             `json:"attempt"`
	FenceEpoch    uint64          `json:"fence_epoch"`
	IdemKey       string          `json:"idem_key"`
	Actor         Actor           `json:"actor"`    // user id + auth method
	Approval      *ApprovalRecord `json:"approval,omitempty"`
	Progress      ProgressSnapshot `json:"progress"`
	Result        json.RawMessage `json:"result,omitempty"` // small; large results go to op_result:
	Error         string          `json:"error,omitempty"`
	QueuedAt, StartedAt, CompletedAt, LastProgressAt, LastCheckpointAt *time.Time
	Priority      int             `json:"priority"`
	TraceID, SpanID string
	// Migrated rows only.
	FromV2        *V2Origin       `json:"from_v2,omitempty"` // original status string, high_water, resume counts
}
```

## 3. v2 → v3 status mapping (migration 065)

| v2 `OperationV2Row.Status` | v3 `State` | notes |
|---|---|---|
| `queued` | `queued` | queue key re-created with the same priority and `queued_at` |
| `waiting_deps` | `queued{wait_reason: deps}` | requirements JSON carried over unchanged; not placed in `opv3:q:` until deps clear |
| `running` | `interrupted{crash}` | only possible if v2 died; the boot sweep then applies the def's v3 policy |
| `completed` | `succeeded` | |
| `failed` | `failed` | |
| `canceled` with `error_message` prefix `timeout:` | `timed_out` | prefix written by `worker.go:885` |
| `canceled` otherwise | `canceled` | |
| `interrupted_quiesced` | `interrupted{quiesced}` | |
| `interrupted_ask` | `awaiting_decision` | |
| `interrupted_dropped` with message `requeued: original op replaced` | `dropped{successor_id}` | written by v2 `resumeRequeue` (`internal/operations/registry/resume.go:482-483`); the successor id is only in a slog line, so `SuccessorID` is filled when a newer run of the same def has `queued_at` within 1s of this row's `completed_at`, else left empty |
| `interrupted_dropped` otherwise | `dropped` | |
| `interrupted_restart` | `dropped{successor_id}` | legacy spelling, no longer minted (`registry.go:1290-1294`) |
| `interrupted` (bare, legacy) | `interrupted{legacy}` | |
| anything else | `failed` + `FromV2.Status` = original | counted and logged; the migration reports the count instead of guessing |

Checkpoints: the v2 `opv2:state:` blob is copied into `opv3:ckpt:` as `legacy_state` bytes. A
def still on the v2 adapter reads it unchanged. A def ported to a native v3 kind must, in its
port PR, either ship a decoder for its v2 checkpoint or declare
`LegacyResume: FromZero` (idempotent defs) or `LegacyResume: Drop` (with a log line naming the
run). The port PR's checklist enforces this; `ValidateCatalog` refuses a native def that has
non-terminal migrated runs and no `LegacyResume`.

## 4. Migration, dual-write and rollback

**Migration 065 (Up)** — runs in `database.RunMigrations` (`internal/database/migrations.go:488`):
1. Stream `opv2:op:` in key order; for each row write `opv3:run:` + indexes + `opv3:evt:`
   (one synthetic `migrated` transition) in one batch per 500 rows. Idempotent: a run that
   already exists with the same `FromV2.SourceHash` is skipped.
2. Copy `opv2:state:` → `opv3:ckpt:legacy_state`, `opv2:log:` is **not** copied: the v3 log
   reader falls back to `opv2:log:{op_id}` for migrated runs (read-through), which avoids
   copying the largest family.
3. Write `opv3:meta:counts` from what was written, and log the mapping table counts
   (including "anything else").
4. Leave every `opv2:` key in place.

**Dual-write (shim period, PR 4 → PR 16).** The v3 store writes, in the same Pebble batch as
each `opv3:run:` change, the v2 mirror row (`opv2:op:` in v2 vocabulary through the existing
`stageOpRow`, so the `opv2:open:` / `opv2:done:` timeline index stays correct). New v3-only
states map back: `queued{deps}`→`waiting_deps`, `stopping`→`running`, `timed_out`→`canceled`
(+`timeout:` message), `awaiting_decision`→`interrupted_ask`, `dropped{successor_id}`→`interrupted_restart`,
`interrupted`→`interrupted_quiesced`. Native-v3 runs with a chunk ledger mirror as
`interrupted_dropped` when non-terminal at shutdown, because a v2 binary cannot resume them.
The mirror stays on for **30 days after the last port wave (12H) ships** and never less
than the time to PR 15 (D25).

**Rollback (Down) during the shim period.** Deploy the previous binary. It reads `opv2:` rows
that are current because of the mirror. `opv3:` keys are inert to it (no v2 code scans that
prefix; `keyfamilies.go` gets the new families registered in PR 2 so census tools account for
them). Migration 065's `Down` only resets the schema version; it does not delete `opv3:`
keys, so rolling forward again re-uses them (step 1 is idempotent). What is lost on rollback:
approval records, intents and typed progress of runs made under v3 — they stay in `opv3:`
and reappear on roll-forward.

**After the mirror is removed (PR 16).** Rollback requires `audiobook-organizer ops
export-v2` (shipped in PR 16): writes v2 rows for every v3 run created after the mirror was
switched off. The mirror is removed only after the 30-day soak the owner chose (D25).

**Retiring `opv2:` and v1 keys** is a separate, later change owned by workstream 01 and gated
on that soak: `operation:`, `operationlog:`, `opstate:<id>:params` (v1) and `opv2:*` after
`opv3` has served history for the soak window.

## 5. Size and time (not measured)

The number of `opv2:op:` rows on prod is **not measured** in this workstream (no prod calls,
charter rule 1). PR 0 adds `db-census` coverage for `opv2:` families so the migration's
duration can be estimated before it ships; the migration batches 500 rows and logs progress
every 10k so a slow boot is visible.
