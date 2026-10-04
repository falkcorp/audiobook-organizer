<!-- file: docs/plans/storage-efficiency/TASK-A5.md -->
<!-- version: 1.1.0 -->
<!-- guid: 10c73217-17fc-4334-a120-d0c82a7c9e9b -->
<!-- last-edited: 2026-10-03 -->

# TASK-A5: Timeline indexes (`opv2:open:`, `opv2:done:`), startup reconcile, `GetOpLogsV2` tail read

Wave W1. Start only after PR #3704 has merged to `main`: it edits
`internal/server/server_lifecycle.go`, which step 12 edits. Runs in parallel
with A1, A2, A4 and A6. Model: opus. Reviewer: code-reviewer, with an
equivalence probe (old scan against new, on random operation histories; a
batch failure injected between staging and commit must leave old and new
agreeing).

## 1. Goal and why

**Goal.** Answer `ListOperationsV2Since` from two small indexes instead of
decoding every operation row:
- `opv2:open:<op>` exists while an operation has no completion time;
- `opv2:done:<completed_nanos>:<op>` exists once it has one.

Every write of an `opv2:op:` row keeps both indexes correct. A startup
reconcile builds and repairs them. The result must be identical to today's
for every history: same rows, same order, same limit. In the same task, make
`GetOpLogsV2` with a limit read only the tail.

**Why.** `ListOperationsV2Since` (`pebble_store_ops_v2.go:855`) decodes every
`opv2:op:` row on every call. Measured at 5.46-5.95 s per call, seven times
(eval R4, F4), and it grows by about 1,780 operations a day.
`GetOpLogsV2(op, limit)` decodes every log row of the op and then keeps the
last `limit`. One `library.scan` has about 300,935 log rows (R6).

**Scope of the speed target.** The result is sorted by `StartedAt`, so every
row in the window must still be read. A window that holds all history (R4
used `since=129600m`, 90 days) stays slow until operation retention exists
(release D). The target is under 100 ms for `since` up to 24 h. State the
measured numbers for both a 24 h and a 90 d window in the report.

## 2. Setup

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
gh pr view 3704 --json state -q .state   # must print MERGED; if not, stop
git worktree add ../aorg-storage-a5-timeline-index -b perf/storage-a5-timeline-index origin/main
cd ../aorg-storage-a5-timeline-index
npm ci --prefix web
```

- Do NOT run `go work init`.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch.

## 3. Read before editing

- `internal/database/pebble_store_ops_v2.go`, ALL of it (1,485 lines). In
  particular:
  - `:1-16`: the key schema comment, which you extend;
  - `:86-94`: `pebbleSetJSON`;
  - `:140-160`: `InsertOperationV2`;
  - `:212-445`: the status writers;
  - `:495-690`: the read-modify-write writers;
  - `:708-795`: `DeleteOperationV2`;
  - `:847-967`: `ListOperationsV2Since` and `GetOpLogsV2`;
  - `:1144-1215`: why `interrupted_*` rows have a `CompletedAt` but are not
    terminal;
  - `:1230-1310`: `RepairOpsV2MissingCompletedAt` and
    `stampCompletedAtIfPhantom`, the "collect without the lock, re-check
    under the lock" pattern you reuse;
  - `:1366-1400`: `PromoteToQueued`.
- `internal/database/iface_ops_v2.go:39`. `OperationV2Row`: `ID`, `Status`,
  `QueuedAt time.Time`, `StartedAt *time.Time`, `CompletedAt *time.Time`.
- `internal/database/pebble_store_atpath_index.go:60-90` and `:186-260`. The
  precedent for a one-time startup index build gated by a sentinel key
  (`system:backfill:book_atpath_index_v1_done`), using a bounded worker pool
  (`bookAtPathBackfillWorkers = runtime.NumCPU()`, `:88`).
- `internal/server/server_lifecycle.go:1033-1066` (how that build is started
  after memdb warmup) and `:1697-1710` (capability resolution).
- `internal/server/handlers/operations_v2.go:154-200` and `:284-367`. The
  timeline handler: `timelineScanBound = 5000`, `matched`, `scan_capped`.
  Their meaning depends on the store sorting the whole window before it
  truncates. Keep that.
- `internal/server/reconcile_ops_index.go:95-115`. It calls
  `ListOperationsV2Since(time.Time{}, ...)` with the ZERO time.
- `$(go env GOMODCACHE)/github.com/oklog/ulid/v2@v2.1.2/ulid.go:400-470`.
  `Timestamp`, `SetTime`.

## 4. Re-verify anchors

1. `grep -n 'opv2OpKey(' internal/database/pebble_store_ops_v2.go | grep -E 'Set|pebbleSetJSON'`
   → exactly these 12 write sites: `151, 269, 339, 382, 436, 569, 586, 619,
   654, 670, 687, 812`.
2. `grep -n 'return p.pebbleSetJSON(opv2OpKey(id), &row) == nil\|if err := p.pebbleSetJSON(opv2OpKey(id), &row); err != nil' internal/database/pebble_store_ops_v2.go`
   → `654`, `1309` (stampCompletedAtIfPhantom), `1392` (PromoteToQueued).
   With anchor 1, the **write sites are: 151, 269, 339, 382, 436, 569, 586,
   619, 654, 670, 687, 812, 1309, 1392.**
3. `grep -n 'opv2OpKey(id), opv2ActKey(id), opv2StateKey(id)' internal/database/pebble_store_ops_v2.go`
   → `547` (SweepHollowOperationsV2) and `776` (DeleteOperationV2). These
   are the **delete sites**.
4. `grep -n 'batch.DeleteRange(prefix, prefixEnd(prefix), nil)' internal/database/pebble_store_ops_v2.go`
   → `789`. The design cited `:785`; the actual line is `:789`.
5. `grep -rn 'opv2OpKey(\|"opv2:op:' --include='*.go' internal cmd | grep -v _test.go | grep -v pebble_store_ops_v2.go | wc -l`
   → `0`. No other non-test file writes `opv2:op:`.
6. `grep -n 'inWindow := row.CompletedAt == nil' internal/database/pebble_store_ops_v2.go`
   → `902`. This is the predicate (design `:902-904`).
7. `grep -n 'func (p \*PebbleStore) ListOperationsV2Since\|func (p \*PebbleStore) GetOpLogsV2' internal/database/pebble_store_ops_v2.go`
   → `855`, `939`.
8. `grep -n 'result = result\[len(result)-limit:\]' internal/database/pebble_store_ops_v2.go` → `964`.
9. `grep -n 'interrupted_quiesced" (registry.go:1263' internal/database/pebble_store_ops_v2.go`
   → `1191`. The design cited `:1190-1194`; holds.
10. `grep -n 'ListOperationsV2Since(time.Time{}' internal/server/reconcile_ops_index.go` → `109:`
11. `grep -n 'scope\["scan_capped"\] = len(rows) >= timelineScanBound' internal/server/handlers/operations_v2.go` → `367:`
12. `grep -n 's.bgWG.Go("book-atpath-backfill"' internal/server/server_lifecycle.go` → `1039:`
13. `grep -n 'const bookAtPathBackfillKey' internal/database/pebble_store_atpath_index.go`
    → `63: const bookAtPathBackfillKey = "system:backfill:book_atpath_index_v1_done"`
14. `grep -n 'func (p \*PebbleStore) backfillBookAtPath' internal/database/pebble_store_atpath_index.go` → `230:`
15. `U=$(go env GOMODCACHE)/github.com/oklog/ulid/v2@v2.1.2; grep -n '^func Timestamp\|^func (id \*ULID) SetTime' $U/ulid.go`
    → `445: func Timestamp(t time.Time) uint64`,
    `460: func (id *ULID) SetTime(ms uint64) error`.
16. `grep -n 'opv2:op:<op_id>' docs/database-pebble-schema.md` → `348:`

## 5. Steps

### 5.1 Keys and the index delta: new file `internal/database/pebble_store_ops_v2_timeline.go`

1. Key builders:
   - `func opv2OpenKey(opID string) []byte` → `"opv2:open:" + opID`.
   - `func opv2DoneKey(completedAt time.Time, opID string) []byte` →
     `fmt.Sprintf("opv2:done:%020d:%s", completedNanos(completedAt), opID)`.
   - `func completedNanos(t time.Time) int64` returns `t.UnixNano()`,
     clamped to 0 when it is negative (pre-1970). Zero-padded to 20 digits,
     byte order is time order.
   - `func parseOpv2DoneKey(k []byte) (nanos int64, opID string, ok bool)`.
     The fixed layout is: prefix (10 bytes), 20 digits, `:`, then the id.
   - `const opsV2TimelineIndexStamp = "system:backfill:opv2_timeline_index_v1_done"`.
     It follows the atpath sentinel naming (`pebble_store_atpath_index.go:63`).
2. `func stageOpRow(b *pebble.Batch, prev, next *OperationV2Row) error`
   (the plan and design 8 call it `stageOpRow(batch, old, new)`; use that
   name).
   `prev` is the row as stored before this write; nil means none or unknown.
   `next` is the row being written; nil means the row is being deleted. Rules:
   - `prev` had a `CompletedAt`, and `next` is nil, or has no `CompletedAt`,
     or a different `completedNanos`: Delete `opv2DoneKey(prev)`.
   - `prev` had no `CompletedAt`, and `next` is nil or has one: Delete
     `opv2OpenKey`.
   - `next` has no `CompletedAt` and (`prev` is nil or `prev` had one): Set
     `opv2OpenKey` with an empty value.
   - `next` has a `CompletedAt` and (`prev` is nil, or `prev` had none, or the
     nanos differ): Set `opv2DoneKey(next)` with an empty value.
   - Otherwise, **stage nothing.** `UpdateOpProgressV2` runs on every
     progress tick. When `CompletedAt` does not change, the index must cost
     zero writes.
3. `func (p *PebbleStore) commitOpV2Row(prev *OperationV2Row, row *OperationV2Row, extra func(*pebble.Batch) error) error`.
   It marshals `row`, opens a batch, Sets `opv2OpKey(row.ID)`, calls
   `stageOpRow(b, prev, row)`, calls `extra(b)` when it is
   non-nil (queue and active keys), and commits with `pebble.Sync`. Guard it
   with `recoverPebbleClosed`.

### 5.2 Every write site (in `pebble_store_ops_v2.go`)

4. Before each mutation, capture `prev := row`, a struct copy. Mutations
   assign new pointers to `CompletedAt` and never write through the old one,
   so the copy is safe. Check each site to confirm. Then:
   - `:151` `InsertOperationV2`. Read the existing row first
     (`pebbleGetJSON`). `prev` = that row if `ID != ""`, else nil. Replace
     the three separate Sets (row, queue key, act key) with one
     `commitOpV2Row(prev, &row, extra)`, where `extra` stages the queue and
     act keys when `row.Status == "queued"`. Same keys as today, now atomic.
   - `:269` `UpdateOperationV2Status`, `:339` `ResetOperationV2ForResume`,
     `:436` `SetOperationV2StatusIfQueued`. These already use a batch. Add
     `stageOpRow(batch, &prev, &row)` before the commit.
   - `:382` `SetOperationV2Result`. Use `commitOpV2Row(&prev, &row, nil)`.
     The delta is empty, but the call is uniform.
   - `:569, :586, :619, :654, :670, :687, :812` (IncrementResumeCount,
     MarkManualRetry, UpdateOpProgress, SetOpQueuedProgress, UpdateOpPhase,
     UpdateOpCheckpoint, UpdateOperationV2Params). Replace
     `p.pebbleSetJSON(opv2OpKey(id), &row)` with
     `p.commitOpV2Row(&prev, &row, nil)`. Each one already holds the row it
     read; capture `prev` right after the read.
   - `:1309` `stampCompletedAtIfPhantom`. Use
     `p.commitOpV2Row(&prev, &row, nil) == nil`. This one moves the row from
     open to done.
   - `:1392` `PromoteToQueued`. Replace the row Set and the separate queue
     Set (`:1398`) with one `commitOpV2Row(&prev, &row, extra)` that stages
     the queue key.
5. Delete sites:
   - `:547` `SweepHollowOperationsV2`. The row did not decode, so its
     `CompletedAt` is unknown. Add `opv2OpenKey(id)` to the deleted keys, and
     delete any done key for that id: iterate the `opv2:done:` keys only (no
     values), and stage a Delete for each key whose id part
     (`parseOpv2DoneKey`) equals the swept id. The reconcile does not run on
     every boot (5.4), so it cannot be relied on to clean up. Sweep runs once,
     from a migration (`migrations.go:1256`), so a key-only pass over the done
     index is acceptable there. A stale done key is harmless to readers in any
     case: the reader point-gets each id and skips not-found rows (step 8.4).
   - `:776` `DeleteOperationV2`. Add `opv2OpenKey(id)` to `keys`. When the
     row decoded and `row.CompletedAt != nil`, also add
     `opv2DoneKey(*row.CompletedAt, id)`. Stage both through
     `stageOpRow(batch, &row, nil)`. This is the only operation-record delete
     today (the plan's "record pruner's delete"; its caller is the registry's
     discard, `registry.go:1272`). Say in its doc comment that any future
     record pruner (release D, task D2) must delete through
     `stageOpRow(batch, old, nil)` in the same batch as the row.
6. After this, `pebbleSetJSON` must have no caller with `opv2OpKey`.
   `grep -n 'pebbleSetJSON(opv2OpKey' internal/database/pebble_store_ops_v2.go`
   must print nothing. Keep `pebbleSetJSON` itself; other families use it.
   Make it a CI ratchet, not a one-off check: add
   `TestOpsV2RowWritesGoThroughStageOpRow` to the new test file. It reads
   every non-test `.go` file in `internal/database` and fails on any line
   that Sets an `opv2OpKey(` key (`.Set(opv2OpKey(`, `pebbleSetJSON(opv2OpKey(`)
   outside `commitOpV2Row` and the batch-based writers that call `stageOpRow`
   before their commit (list them by function name in the test, with a
   comment saying a new writer must be added there and must call
   `stageOpRow`). A Go test runs in `make ci` with no Makefile edit; A4
   edits the `Makefile` in this wave.

### 5.3 Reader

7. Factor out of `ListOperationsV2Since` without changing behaviour:
   - `func opV2InWindow(row *OperationV2Row, since time.Time) bool`, the
     exact predicate at `:902-904`. Move its long comment with it.
   - `func opV2TimelineLess(a, b *OperationV2Row) bool`, the comparator at
     `:913-929`, with ONE addition: when `StartedAt` and `QueuedAt` are both
     equal, order by `ID` descending. `sort.Slice` is not stable, so tie
     order was undefined before. Fixing it makes the equivalence test
     deterministic, and it changes no defined behaviour.
   - `func (p *PebbleStore) listOperationsV2SinceScan(since time.Time, limit int) ([]OperationV2Row, error)`,
     today's full scan, using the two helpers above. **Kept** in production
     code: it is the fallback while the index stamp is absent, and it is the
     oracle for the equivalence test.
8. `func (p *PebbleStore) listOperationsV2SinceIndexed(since time.Time, limit int) ([]OperationV2Row, error)`:
   1. Iterate `opv2:open:` and collect the ids.
   2. Iterate `opv2:done:` from `opv2:done:<completedNanos(since):020d>` to
      `prefixUpperBound([]byte("opv2:done:"))` (`embedding_store.go:2342`),
      and collect the ids. If `since` is zero or before 1970, start at the
      prefix.
   3. ULID seek. If `since` is zero or `since.Add(-time.Minute)` is before
      1970, the lower bound is `"opv2:op:"`. Otherwise build a
      `var u ulid.ULID`, call `u.SetTime(ulid.Timestamp(since.Add(-time.Minute)))`,
      and use the bound `"opv2:op:" + u.String()`. Iterate to the prefix end,
      decode each row from the iterator value, and keep it. The one-minute
      margin covers an id minted slightly before its `QueuedAt`. This leg
      exists only for the predicate's `QueuedAt` clause: rows whose
      `CompletedAt` is before `since` (clock skew). Op ids are ULIDs
      (`registry.go:915`, `batch.go:312`, `resume.go:497`).
   4. Point-get every collected id not already decoded in leg 3, with
      `pebbleGetJSON`. Skip not-found, undecodable and empty-`ID` rows, the
      same as the scan's `continue`.
   5. Apply `opV2InWindow` to every row. Dedupe by id. Sort with
      `opV2TimelineLess`. Truncate to `limit`.
9. `ListOperationsV2Since`. Keep the signature, the `limit <= 0` → 200
   default and the `recoverPebbleClosed` guard. Do a point `Get` of
   `opsV2TimelineIndexStamp`. If the stamp is present, use the indexed path;
   otherwise use `listOperationsV2SinceScan`. Do NOT add a field to the
   `PebbleStore` struct to cache this: that means editing
   `pebble_store.go`, which A4 owns in this wave. One point `Get` per call is
   cheap.
10. Update the doc comment of `ListOperationsV2Since` and the key-schema
    comment at `:1-16` with the two new families and the stamp.

### 5.4 Startup reconcile

11. `type OpsV2TimelineReconcileResult struct { Rows, Missing, Orphans, Fixed int; Duration time.Duration }`.
    Add `func (p *PebbleStore) ReconcileOpsV2TimelineIndex(ctx context.Context) (OpsV2TimelineReconcileResult, error)`
    in the new file. It runs only when `opsV2TimelineIndexStamp` is absent
    (design 8): every writer stages the index in the row's own batch (steps
    4-5) and the ratchet (step 6) keeps it that way, so once built the index
    cannot drift. One case can still leave rows without index keys: a
    rollback to a pre-A5 binary that then writes or updates rows. For that,
    the reconcile also runs when the environment variable
    `AORG_OPSV2_TIMELINE_RECONCILE=1` is set at startup. Document it (step
    14) as a one-time step on the first start after rolling forward past A5.
    - **Pass 1, find missing keys, no lock.** One producer goroutine owns a
      single iterator over `opv2:op:` and sends copied (key, value) pairs
      over a bounded channel to `runtime.NumCPU()` workers. Each worker
      decodes the row (skipping undecodable rows and rows with no id; those
      are `SweepHollowOperationsV2`'s job), computes the desired index key
      (open or done), and checks it with `p.db.Get`. A missing key adds the
      id to the candidate list (mutex-guarded slice). Counters are atomics.
      This is the CLAUDE.md "bounded worker pool" rule. The precedent is
      `backfillBookAtPath` (`pebble_store_atpath_index.go:206-230`). It is
      not `registry.RunItems`, so there is no `Concurrency` field to forget.
    - **Pass 2, find orphan keys, no lock, same pool shape.**
      - Over `opv2:open:`: a key is an orphan when its row is missing,
        undecodable, or has `CompletedAt != nil`.
      - Over `opv2:done:`: a key is an orphan when its row is missing or
        undecodable, has `CompletedAt == nil`, or has a different
        `completedNanos` than the key.
    - **Pass 3, fix under the lock, chunked.** In chunks of 500, take
      `p.opsMu`, re-read each candidate row, re-check it, and stage into one
      batch: Set the desired key, Delete a stale open key, Delete each
      confirmed orphan. Commit with `pebble.Sync`, then release the lock
      before the next chunk. Re-checking under the lock is the same rule as
      `RepairOpsV2MissingCompletedAt` (`:1249-1294`): a row can change
      between the scan and the fix.
    - Check `ctx.Err()` between chunks and inside the producers.
    - When all three passes finish without error and `ctx` is not done, Set
      `opsV2TimelineIndexStamp` (value `"1"`, `pebble.Sync`).
    - **Resumable** means: a crash or shutdown at any point loses nothing.
      Every committed chunk is correct on its own, readers stay on the scan
      until the stamp exists, and the next boot runs again. No cursor key:
      the passes are read-mostly and idempotent, and a cursor would skip rows
      a pre-A5 binary changed between attempts. That is the same argument as
      the atpath backfill (`pebble_store_atpath_index.go:225-229`).
    - Log one line at the end:
      `slog.Info("opsv2-timeline-reconcile", "rows", ..., "missing", ..., "orphans", ..., "fixed", ..., "duration_ms", ...)`.
12. **`internal/server/server_lifecycle.go`.** Directly after the
    `book-atpath-backfill` block (ending at `:1066`), add
    `s.bgWG.Go("opsv2-timeline-reconcile", func() { ... })` with the same
    shape:
    - return early on `s.bgCtx.Err()`;
    - resolve `resolveOpsV2TimelineReconciler(s.Ops())` through
      `database.AsCapability`;
    - log a warning and return if it fails;
    - return early, with one `slog.Debug` line, when the stamp is present
      and `AORG_OPSV2_TIMELINE_RECONCILE` is not `1` (expose a small
      `OpsV2TimelineIndexBuilt() (bool, error)` on the capability for the
      check);
    - wait for `WaitForWarmup` or `s.bgCtx.Done()`;
    - call `ReconcileOpsV2TimelineIndex(s.bgCtx)` and log a warning on error.

    Add the interface
    `opsV2TimelineReconciler { WaitForWarmup(); ReconcileOpsV2TimelineIndex(ctx context.Context) (database.OpsV2TimelineReconcileResult, error) }`
    and `resolveOpsV2TimelineReconciler`, next to `bookAtPathBackfiller`
    (`:1697-1710`).

### 5.5 `GetOpLogsV2` tail read

13. With `limit > 0`: use the same bounds, `iter.Last()`, then walk with
    `iter.Prev()`. Decode each row, skip undecodable rows (as today), and
    stop after `limit` decoded rows. Reverse the slice so the result is in
    ascending `created_at` order, exactly as today. With `limit <= 0`: no
    change.

### 5.6 Docs

14. `docs/database-pebble-schema.md`, operations table (`:347-354`):
    - add rows for `opv2:open:<op_id>` and
      `opv2:done:<completed_nanos:020d>:<op_id>`, both with empty values;
    - mention `system:backfill:opv2_timeline_index_v1_done`, that the
      reconcile runs only while it is absent, and the
      `AORG_OPSV2_TIMELINE_RECONCILE=1` one-time step after rolling forward
      past A5 (do not edit `docs/system/runbooks.md`; A4 edits it in this
      wave);
    - change the `opv2:op:` row text "No status index exists" to say which
      reads still scan (`ListWaitingDepsOps`, `ListResumableOperationsV2`,
      the repairs) and that the timeline now uses the indexes;
    - bump the header.
15. `internal/server/handlers/operations_v2.go`: **no change.** The plan
    listed it, but `matched` and `scan_capped` keep their meaning because the
    store still sorts the whole window before truncating.

## 6. Do not touch

- `internal/database/pebble_store.go`, including the `PebbleStore` struct
  (A4 owns it in W1).
- `internal/database/keyfamilies.go`. A2 pre-registers `opv2:open:` and
  `opv2:done:`, so you need no edit there.
- `internal/server/server.go` (A1), `handlers/diagnostics.go` and
  `wire_media_routes.go` (A2), `internal/operations/registry/` (A6).
- `ListWaitingDepsOps`, `ListResumableOperationsV2`, `ListActiveOperationsV2`,
  `RepairOpsV2MissingCompletedAt`'s scan. Out of scope.
- `database.Store`, `iface_ops_v2.go` and the other `iface_*.go` files,
  `mocks/`. The reconcile method is reached by capability.
- The predicate's meaning, and the handler's `matched` / `scan_capped`
  contract.

## 7. Tests

File: `internal/database/pebble_store_ops_v2_timeline_test.go` (new). Use
`NewPebbleStoreInMemory`.

- `TestOpsV2Timeline_EquivalenceRandomHistories`, the property test. Run
  seeds 1..200 with `math/rand/v2` (`rand.New(rand.NewPCG(seed, 0))`). Per
  seed, use a fresh store and `base := time.Now().UTC()`.
  - Create about 150 ops. The id is `ulid.MustNew(ulid.Timestamp(queuedAt), entropy)`,
    with `queuedAt` drawn from [base−30d, base]. The mix of histories, drawn
    at random, must include:
    - `queued` → `running` → `completed`/`failed`/`canceled`, with a
      `completedAt` passed explicitly and drawn from [queuedAt, base]. In 5%
      of cases the `completedAt` is BEFORE `queuedAt` (clock skew);
    - `waiting_deps` → `PromoteToQueued` → running → terminal, and some left
      in `waiting_deps`;
    - `interrupted_quiesced`, `interrupted_ask` and `interrupted_restart`
      written with a non-nil `completedAt`. These are not terminal but have a
      completion time (`:1186-1194`);
    - resumed ops: terminal → `ResetOperationV2ForResume` → `running` →
      terminal again with a later `completedAt`;
    - long-running ops queued 20-30 days before `base` and still running, or
      completed inside the last hour;
    - `SetOperationV2StatusIfQueued(id, "canceled")`;
    - interleaved `UpdateOpProgressV2`, `UpdateOpPhaseV2`,
      `UpdateOperationV2Params`, `SetOperationV2Result` and
      `IncrementResumeCountV2`;
    - `DeleteOperationV2` on about 5% of terminal ops;
    - about 10 "legacy" rows written raw with `p.db.Set(opv2OpKey(id), json, pebble.Sync)`
      and no index keys, simulating pre-A5 data.
  - Then call `ReconcileOpsV2TimelineIndex(ctx)` and assert the stamp
    exists.
  - For each `since` in {zero time, base−30d, base−48h, base−1h, base+1h},
    and each `limit` in {1, 7, 200, 5000}, assert that the ids, in order, of
    `listOperationsV2SinceScan(since, limit)` equal those of
    `listOperationsV2SinceIndexed(since, limit)`. Also assert that
    `ListOperationsV2Since` equals the indexed result.
  - Then apply 100 more random writes (no reconcile) and compare again. This
    proves the writers keep the index without reconcile.
  - On failure, print the seed and the first differing position.
- `TestOpsV2Timeline_FallbackScanUntilStamp`. A raw legacy row completed
  inside the window and with no index key. Before reconcile,
  `ListOperationsV2Since` returns it (scan path). After reconcile it still
  returns it (indexed path).
- `TestOpsV2Timeline_ReconcileRepairsMissingAndOrphans`. Plant:
  - a completed row with no done key;
  - an open key for a completed row;
  - a done key with the wrong nanos;
  - open and done keys for a deleted row.

  Reconcile. Assert the exact key sets under `opv2:open:` and `opv2:done:`.
  A second reconcile reports `Fixed == 0`, `Missing == 0` and `Orphans == 0`.
- `TestOpsV2Timeline_ProgressWritesStageNoIndexKeys`. Count the keys under
  `opv2:open:` and `opv2:done:` before and after 1,000 `UpdateOpProgressV2`
  calls on a running op. Assert equal. Also unit-test
  `stageOpRow` with `prev == next` (same `CompletedAt`) on a
  fresh batch: assert `batch.Count() == 0`.
- `TestOpsV2Timeline_FailedCommitLeavesRowAndIndexAgreeing`. Add an
  unexported test hook `opsV2BeforeCommitHook func() error` in the new file,
  called by `commitOpV2Row` and by the batch-based writers just before
  `Commit`; a non-nil error aborts the commit and is returned. With the hook
  failing on every Nth write of a random history (seeded), the scan and the
  indexed read agree after every step.
- `TestOpsV2Timeline_ReconcileOnlyWhenStampMissingOrForced`. With the stamp
  present, the lifecycle gate skips the reconcile; with
  `t.Setenv("AORG_OPSV2_TIMELINE_RECONCILE", "1")` it runs and repairs a
  planted raw row with no index key.
- `TestOpsV2Timeline_SweepHollowRemovesDoneKey`. A hollow row with a planted
  done key: after `SweepHollowOperationsV2`, no index key names it.
- `TestOpsV2RowWritesGoThroughStageOpRow` (step 6).
- `TestOpsV2Timeline_ResumeMovesDoneToOpen`. Complete at T1 (done key T1),
  reset (no done key, open key present), complete at T2 (done key T2 only).
- `TestOpsV2Timeline_DeleteRemovesIndexKeys`. Delete a completed op and an
  open op; assert no index keys remain for either.
- `TestOpsV2Timeline_ZeroSinceReturnsAll`. `since = time.Time{}` returns
  every row, up to the limit, identical to the scan.
- `TestGetOpLogsV2_TailMatchesFull`. 1,200 rows, with 5 undecodable raw
  values interleaved (`p.db.Set` of `"x"` under `opv2:log:<op>:...`). For
  `limit` in {1, 10, 1000, 1195, 5000, 0}, assert the result equals a test
  helper `getOpLogsV2Full` that reproduces the old code (full scan, then the
  last N).
- `BenchmarkListOperationsV2Since` with 50,000 ops, 2,000 in the last 24 h:
  sub-benchmarks `scan_24h`, `indexed_24h`, `scan_90d`, `indexed_90d`. Put
  the numbers in the report.

Existing tests: run the whole `internal/database` and `internal/server`
packages. Before reconcile, existing tests exercise the scan path, which is
unchanged apart from the tie-break. If an existing test fails, report it
with the test name. Do not edit its assertions to match.

## 8. Verify

```bash
go build ./...
go vet ./internal/database/... ./internal/server/...
go test -race -count=1 -run 'OpsV2Timeline|GetOpLogsV2' ./internal/database/
go test -race -count=1 ./internal/database/
go test -race -count=1 ./internal/server/ ./internal/server/handlers/
go test -run '^$' -bench 'BenchmarkListOperationsV2Since' -benchtime 3x ./internal/database/
make lint-errcheck-ratchet
make lint-width
```

No `Store` interface changes, so `scripts/check-interface-width.sh` is not
required.

## 9. Deliverables

- Version headers: the new Go files get fresh headers
  (`uuidgen | tr A-Z a-z`). Bump the version and `last-edited` on
  `pebble_store_ops_v2.go`, `server_lifecycle.go` and
  `docs/database-pebble-schema.md`.
- Fragment `changelog.d/<YYYYMMDD>_storage_a5_timeline_index.md`, no header,
  `### Changed`, `####` entries for the timeline indexes and the log tail
  read, with the benchmark numbers.
- Check that `git diff origin/main | grep -nE "ab""k_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}"` prints
  nothing.
- Commit, for example
  `perf(ops): timeline reads open/done indexes; startup reconcile; op-log tail read`,
  ending with:

  ```
  Co-Authored-By: <model name> <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/perf/storage-a5-timeline-index"`
- `gh pr create --base main --head perf/storage-a5-timeline-index`. Do NOT
  merge. A reviewer runs the equivalence probe before merge.

## 10. Exit criteria and report

- [ ] `grep -n 'pebbleSetJSON(opv2OpKey' internal/database/pebble_store_ops_v2.go`
      prints nothing. All 14 write sites and both delete sites maintain the
      index.
- [ ] The equivalence test passes for all 200 seeds, both before and after
      the extra writes.
- [ ] The benchmark shows the indexed 24 h window under 100 ms on 50,000 ops.
- [ ] On the first boot the reconcile runs once and sets the stamp; on the
      next boot it does not run unless `AORG_OPSV2_TIMELINE_RECONCILE=1`.
- [ ] `TestOpsV2RowWritesGoThroughStageOpRow` passes and fails when a raw
      `pebbleSetJSON(opv2OpKey(...))` is added (show the failing run).
- [ ] All packages listed in section 8 pass with `-race`.
- [ ] PR open, not merged.

Report:

```
TASK-A5 report
head sha: <sha>
PR: <url>
files changed: <list>
write sites converted: <14 line refs>; delete sites: <2>
equivalence: 200/200 seeds PASS (<time>)
benchmark: scan_24h <ms>, indexed_24h <ms>, scan_90d <ms>, indexed_90d <ms>
other tests: <pkg> ok (<time>) ...
not done / deviations: <list or "none">
```
