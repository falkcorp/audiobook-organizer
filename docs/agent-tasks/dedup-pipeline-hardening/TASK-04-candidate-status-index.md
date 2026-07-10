<!-- file: docs/agent-tasks/dedup-pipeline-hardening/TASK-04-candidate-status-index.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0f846f9b-215c-4875-8e7d-bf36d94781c9 -->
<!-- last-edited: 2026-07-10 -->

# TASK-04 — Status secondary index over dedup candidates; de-magic the both_unmatched limit (INIT-2 T4)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). EXCEPTIONS: T3's 387k-backlog drain and T6's CONS-10 prod drain are prod-data mutations -> dry-run FIRST, then a real AskUserQuestion apply gate. — This task's index backfill op writes only rebuildable index rows (no user-visible data), so it stays in the autonomous lane; do NOT run it on prod in this task.
**File-ownership:** INIT-2 OWNS all structural edits to `internal/database/embedding_store.go` — this task is that owner. Never run concurrently with any INIT-1 wave touching this file. Do NOT edit `internal/dedup/engine.go` here (TASK-03/05's lane).

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · store-index + op-plugin subagent · **Why:** transactional index maintenance across three write paths plus a backfill op — integration logic, not mechanical · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-pipeline-hardening-candidate-status-index" -b agent/dedup-pipeline-hardening-candidate-status-index origin/main
cd "$REPO/.worktrees/dedup-pipeline-hardening-candidate-status-index"
git rebase origin/main
```

## Goal

Kill the full-table scan behind every status-filtered candidate list: add a `dedup:s:` status
secondary index over dedup candidates, maintained transactionally in the SAME pebble batch as
the record on every create / status change / delete; make `ListCandidates` use it when a
status filter is set AND the built-flag is set (fail-open to today's full scan otherwise);
ship an idempotent backfill op `dedup.build-candidate-status-index` AND register it in
`internal/plugins/dedup/plugin.go`'s central ops slice; and de-magic the
`filter.Limit = 1_000_000` literal on the `both_unmatched` handler path by renaming it to a
named const `bothUnmatchedScanLimit` — **the ceiling itself stays UNCONDITIONALLY** (spec §C4:
`ListCandidates` treats `limit <= 0` as 50, so "no limit" silently truncates the triage view
to 50 rows, and any cap below the candidate population — ~387k pending pre-drain — truncates
too; the perf win comes from the indexed read, never from shrinking the limit). REUSE the
existing patterns exactly: the `dedup:e:` entity index in the same file (key shape,
batch-maintenance), the `isbnIndexBuiltFlagKey` Settings-flag activation pattern, and the
existing `candidateWriteOpts` commit option. PebbleDB-primary discipline: this is a
PebbleStore feature end-to-end (candidates live only in `EmbeddingStore`; there is no memdb
twin for this store).

## Background (verify before editing)

- `ListCandidates(f CandidateFilter)` iterates the whole `dedup:r:` prefix and applies every
  filter (EntityType/Status/Layer/Min-MaxSimilarity/Band) in Go, then sorts by similarity and
  paginates. With ~400k rows this is the dedup tab's dominant cost.
- The `both_unmatched` handler path sets `filter.Limit = 1_000_000` because the match signal
  lives on the Book, not the candidate — the handler MUST keep its in-handler Book-side
  filtering; your change narrows the store-returned set to status-matching rows (triage
  default `pending`), it does not move the Book check into the store. The `1_000_000` value
  is CORRECT (a ceiling ≥ the max candidate population); its only defect is being a bare
  magic literal. You rename it, you never remove or shrink it.
- Write paths that must maintain the index (all in `embedding_store.go`, all already
  batch-based, all committing with `candidateWriteOpts = pebble.NoSync`):
  `UpsertCandidateNew` (new-pair insert AND the existing-pair update branch — the update
  branch can change `Status`; delete the old-status row when it does),
  `UpdateCandidateStatus` (delete old row, set new row), `DeleteCandidate` (delete row).
  PR #1855 lesson (verbatim constraint): never hold a store-wide mutex across a `pebble.Sync`
  fsync — you are NOT adding any new commit; index rows join the EXISTING batches only.
- Activation pattern to mirror: `isbnIndexBuiltFlagKey = "book_isbn_index_v1_done"` +
  `IsISBNIndexBuilt()`/`SetISBNIndexBuilt()` in `pebble_store_isbn_index.go`, and the op that
  builds it. New flag: `dedup_candidate_status_index_v1_done` on the EmbeddingStore's
  settings access (find how the dedup plugin reads/writes Settings —
  `internal/plugins/dedup/drain_stale.go` uses `p.store.SetSetting`/`p.isFlagSet`; mirror that
  in the new op).
- Fallback semantics (spell-out): flag unset OR `f.Status == ""` ⇒ exactly today's full-scan
  path, byte-for-byte behavior (including sort + pagination). Flag set AND status filter ⇒
  iterate `dedup:s:<status>:`, point-read each `dedup:r:` record, apply the REMAINING filters
  in Go, then the same sort + pagination. A dangling index row (record missing) is skipped
  silently — never an error.
- Both existing scans stay correct during rollout: rows written before the backfill lack
  index entries, which is why the flag gates the read path.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  # Edit target: full-scan list (~embedding_store.go:665, 1 hit)
  grep -n 'func (s \*EmbeddingStore) ListCandidates' internal/database/embedding_store.go
  # Edit targets: the three write paths + write opts (>=4 hits)
  grep -n 'func (s \*EmbeddingStore) UpsertCandidateNew\|func (s \*EmbeddingStore) UpdateCandidateStatus\|func (s \*EmbeddingStore) DeleteCandidate\|candidateWriteOpts = pebble.NoSync' internal/database/embedding_store.go
  # Copy-from source: entity index key shape + prefixes (>=2 hits)
  grep -n 'dedupEntityPfx\|func dedupEntityKey' internal/database/embedding_store.go
  # Edit target: forced limit (handler.go:~175, 1 hit)
  grep -n 'filter.Limit = 1_000_000' internal/server/handlers/dedup/handler.go
  # Copy-from source: flag pattern (>=2 hits)
  grep -n 'isbnIndexBuiltFlagKey\|func (p \*PebbleStore) IsISBNIndexBuilt' internal/database/pebble_store_isbn_index.go
  # Copy-from source: op registration + flag helpers in the dedup plugin (>=2 hits)
  grep -n 'drainStaleDef\|isFlagSet\|SetSetting' internal/plugins/dedup/drain_stale.go
  # Edit target: the central ops slice your new def MUST be appended to (>=2 hits)
  grep -n 'buildISBNIndexDef\|RegisterOp' internal/plugins/dedup/plugin.go
  # Copy-from source: bounded worker-pool op loop (>=1 hit)
  grep -n 'registry.RunItems' internal/plugins/acoustid/backfill.go
  ```
  Zero hits on any edit-target grep at execution time = STOP and report.

## Step-by-step

1. Run the anchor greps. In `internal/database/embedding_store.go`, add
   `const dedupStatusIdxPfx = "dedup:s:"` next to `dedupEntityPfx` and
   `func dedupStatusIdxKey(status string, id int64) []byte` mirroring `dedupEntityKey`'s
   `%016x` id encoding.
2. Maintain the index in all three write paths (see Background — including the
   status-change-on-update branch of `UpsertCandidateNew`). Same batch, same
   `candidateWriteOpts` commit; no new locks, no new commits.
3. Add the flag helpers on EmbeddingStore (or reuse the store's existing settings access —
   whichever the dedup plugin already reaches; do not invent a second settings mechanism) and
   the indexed read path in `ListCandidates` per the Fallback semantics above.
4. NEW `internal/plugins/dedup/build_candidate_index.go`: op
   `dedup.build-candidate-status-index` — full `dedup:r:` scan writing `dedup:s:` rows in
   bounded batches with progress reporting (mirror the drain-stale op's registration shape;
   if iterating candidate PAGES, reuse the drain's paging constant pattern rather than a raw
   unbounded loop), idempotent (re-run overwrites the same presence-only keys), sets the flag
   at the end via the same `SetSetting` call shape drain-stale uses. Cancellable via ctx.
   **Then register it:** append `buildCandidateStatusIndexDef()` to the central ops slice in
   `internal/plugins/dedup/plugin.go` (the `ops []sdk.OperationDef` slice looped through
   `r.RegisterOp` — `buildISBNIndexDef()` is the sibling entry to mirror). Ops are NOT
   self-registering: without this append the op exists on disk but is un-runnable.
5. In `internal/server/handlers/dedup/handler.go`, rename the bare literal: declare a named
   const `bothUnmatchedScanLimit = 1_000_000` (doc comment: "ceiling ≥ the max candidate
   population; ListCandidates treats limit <= 0 as 50, so this must never be removed or set
   below the population — see spec §C4") and change the assignment to
   `filter.Limit = bothUnmatchedScanLimit`. The ceiling stays UNCONDITIONALLY — do NOT delete
   it, do NOT set "no limit" (that returns 50 rows), do NOT lower it (100_000 < ~387k
   pre-drain pending would truncate the triage set). Keep the in-handler Book-side filter +
   its pagination exactly as-is.
6. Purely additive elsewhere: no signature changes on the store interface, no edits to
   `engine.go`, no reordering beyond gofmt.
7. NEW `internal/database/embedding_store_status_index_test.go`:
   (a) parity — same fixture, flag set vs unset, `ListCandidates(status="pending")` returns
   identical rows/totals/order; (b) maintenance — create → status change → delete leaves
   exactly the expected `dedup:s:` rows (iterate the prefix and assert); (c) dangling index
   row is skipped without error; (d) flag-unset fallback still honors all filters
   (anti-over-suppression for the read path: no row silently disappears when the index is
   active — parity test (a) is the proof).
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.
9. Run the gate.

## How to test

```bash
make ci
go test ./internal/database/... ./internal/plugins/dedup/... ./internal/server/handlers/dedup/... -short
```

Caveat (verbatim): staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck
to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "dedupStatusIdxPfx" internal/database/embedding_store.go` hits (index keyspace present)
- [ ] `grep -n 'filter.Limit = 1_000_000' internal/server/handlers/dedup/handler.go` returns 0 hits — satisfied ONLY by the rename to `bothUnmatchedScanLimit`; verify the ceiling survives: `grep -n 'bothUnmatchedScanLimit = 1_000_000' internal/server/handlers/dedup/handler.go` hits AND `grep -n 'filter.Limit = bothUnmatchedScanLimit' internal/server/handlers/dedup/handler.go` hits (deleting or lowering the limit fails this criterion)
- [ ] `grep -n 'buildCandidateStatusIndexDef' internal/plugins/dedup/plugin.go` hits (op registered in the central slice — the op file merely existing does NOT satisfy this)
- [ ] Parity test (a) green: indexed result == full-scan result on the same fixture (no row lost when the index is active — anti-over-suppression)
- [ ] Maintenance test (b) green: status change moves the index row; delete removes it
- [ ] Tests green; vet/lint clean on changed files (`make ci` exits 0).
- [ ] File headers bumped on every changed file (`grep -n "last-edited: " <file>` shows 2026-07-10 or later).

## Commit message

```
perf(dedup): status secondary index over candidates; named both_unmatched ceiling (INIT-2 T4)

dedup:s: presence rows maintained in the same NoSync batches as dedup:r:
records (PR #1855 lesson: no new fsync, no lock across commit); ListCandidates
uses the index when the built-flag is set, fail-open to the full scan
otherwise. New idempotent backfill op dedup.build-candidate-status-index
mirrors the isbn-index flag pattern and is registered in plugin.go's ops
slice. both_unmatched's 1_000_000 ceiling kept unconditionally, renamed to
bothUnmatchedScanLimit (limit<=0 means 50 in ListCandidates — never delete it).

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-pipeline-hardening-candidate-status-index
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "dedupStatusIdxPfx" internal/database/embedding_store.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the read path returns to the full scan (index rows become inert dead keys, optionally prefix-deleted later); no user-visible data is touched — `dedup:s:` rows and the Settings flag are the only writes and both are rebuildable.
