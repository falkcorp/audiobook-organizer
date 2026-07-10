<!-- file: docs/specs/2026-07-10-dedup-pipeline-hardening-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a5b4797-eba3-40c2-a079-7dba596ee462 -->
<!-- last-edited: 2026-07-10 -->

# Deduplication Pipeline Hardening (INIT-2) — Design Spec

**Status:** Draft <!-- flip to: Approved — ready for implementation planning, at Gate 2 -->
**Scope:** Go backend only — `internal/database` (store getters, candidate index), `internal/dedup` (tiers, emission gates, emit() sharding), `internal/plugins/dedup` (index-build op), `internal/server/handlers/dedup` (list path). Prod ops: one gated drain run.
**Parent task:** INIT-2 (`.claude/notes/2026-07-10-remaining-work-master-plan.md`)

---

## Motivation

Three independent, grounded defects keep the dedup tab wrong and slow:

1. **Two of the three book-dedup tiers are dead.** `GetFolderDuplicatesCore()` and
   `GetDuplicateBooksByMetadataCore(threshold)` in `internal/database/pebble_store.go` are
   documented no-op stubs (`return nil, nil` — the doc comment at the stub itself says
   "known-unimplemented stub on both storage backends today"). No MemStore (memdb) twin exists
   either. Consequence: tiers 2 (folder) and 3 (metadata-fuzzy) of `ScanBookDuplicates`
   (`internal/dedup/book_dedup.go` — calls at the `folderGroupsCore, err :=` and
   `metadataGroupsCore, err :=` lines) and the second consumer
   `internal/audiobooks/service_single.go` (`svc.store.GetFolderDuplicatesCore()`) always get
   zero groups. The downstream fuzzy logic (`metadataPairSimilarity`,
   `applyTranscriptionMetadataTiebreaker` in `book_dedup.go`) exists but is never fed.
2. **Exact-layer candidate explosion (#1512).** ~387k pending exact-layer candidates were
   emitted before the CONS-16 (duration-ms) / CONS-17 (title-leak) importer fixes and the
   Jul-1 guard sweep. `DrainStaleCandidates` (`internal/dedup/drain_stale.go`) exists to
   re-apply today's guard chain and soft-reclassify stale rows to `"stale-drain"`, wrapped by
   op `dedup.drain-stale` (`internal/plugins/dedup/drain_stale.go`) — but it has never been
   applied on prod, and its guard-chain parity with `upsertExactCandidate` must be re-verified
   before the run (guards were added after drain_stale.go was written on 2026-07-03).
3. **Two hotspots.** (a) `EmbeddingStore.ListCandidates`
   (`internal/database/embedding_store.go`) is a full `dedup:r:` prefix scan filtered in Go —
   every list call touches every candidate row; the `both_unmatched` triage view forces
   `filter.Limit = 1_000_000` (`internal/server/handlers/dedup/handler.go`). With ~400k rows
   this is the dedup tab's dominant latency. (b) The full-scan `emit()` closure
   (`internal/dedup/engine.go`, CONC-3) runs its whole body under one `sync.Mutex` guarding
   four maps + a counter, with fallback `GetBookByID`/`GetBookFiles` calls **under the lock** —
   NumCPU workers serialize behind one mutex on every emission.

**Goal:** all three book-dedup tiers produce candidates, the exact-layer backlog is drained
under an explicit human gate, and candidate listing + full-scan emission stop being O(all-rows)
/ single-mutex.

## Goals

- Real `GetFolderDuplicatesCore` + `GetDuplicateBooksByMetadataCore` on BOTH backends
  (PebbleStore scan path AND MemStore memdb fast path) + a functional MockStore hook.
- Tier-3 fuzzy grouping guarded against O(N²) via the existing normalized-title/author
  bucketing pattern (PR #1451's index-then-point-read shape, never all-pairs).
- Guard-chain parity between `upsertExactCandidate` and `DrainStaleCandidates`, then one
  gated prod drain of the ~387k backlog (dry-run report → AskUserQuestion → apply).
- A `status`-keyed secondary index over dedup candidates so `ListCandidates` with a status
  filter is O(matching), plus an idempotent index-build op; the `both_unmatched` path keeps an
  explicit bounded ceiling ≥ the candidate population (a bare/zero limit is impossible —
  `ListCandidates` treats `limit <= 0` as 50, so "no limit" silently truncates to 50 rows) but
  the full-table SCAN behind it is replaced by the indexed read once the index is active.
  Note on timing: `pending` ≈ the whole table until T6 drains it, so the index's selectivity
  win is realized post-T6 (see C4 and M3).
- `emit()` sharded: per-book lookups batched/out from under the lock, pair-key check-then-set
  sharded so workers on different shards never contend; `-race` covered.

## Non-goals (v1)

- Label-quality / mining-rule fixes (`rules.go`, ms-sec normalization, calibration) — INIT-1.
- Any embedding/LSH/scoring-formula change — the unified scorer is out of scope.
- A general-purpose query planner for candidates — exactly one new index dimension (status),
  because status is the high-selectivity filter every hot path uses (pending vs the rest).
- AcoustID emission gating — `CollectExactAcoustID` stays intentionally ungated
  (`.github/copilot-instructions.md` §hasPlausibleAudio: the match is itself audio evidence).
- Hard-deleting any candidate row — drains stay soft-reclassify (`stale-drain`), auditable.

## Decisions (locked during design)

1. **Stub getters are implemented HERE, not INIT-9** — INIT-9 only cross-references.
2. **Both backends or nothing:** each getter ships PebbleStore + MemStore twin in the same PR
   (store-getter fidelity discipline), gated by the FULL `go test ./... -short` — a subset
   run hides mocks that set the OLD func and silently return 0 items.
3. **Tier-3 bucketing over pairwise:** group by normalized (author, title-prefix) buckets and
   compare only within buckets — losing alternative (all-pairs `metadataPairSimilarity` over
   44k books ≈ 10⁹ comparisons) rejected; that is the exact O(N²) shape PR #1451/#1857 fixed
   twice already in the ISBN path.
4. **Drain is soft + versioned:** re-use `staleDrainStatus = "stale-drain"` and bump
   `drainStaleDoneFlag` v1→v2 when guard criteria change (the flag's own doc comment mandates
   this) — losing alternative (hard delete) rejected: irreversible, breaks the audit trail.
5. **Status index only, maintained transactionally:** index rows are written in the SAME
   pebble batch as the candidate record (`UpsertCandidateNew` / `UpdateCandidateStatus` /
   `DeleteCandidate` already batch), committed with the existing `candidateWriteOpts`
   (`pebble.NoSync`) — never a second fsync, never a store-wide mutex held across a sync
   commit (PR #1855 lesson).
6. **emit() sharding preserves per-pair atomicity:** the emitted-key check-then-set moves to
   a shard selected by pair-key hash, so the same pair always lands on the same shard-mutex —
   losing alternative (one `sync.Map`) rejected: check-then-set + counter + three book caches
   need a mutex anyway; sharding keeps the invariant proof local.
7. **Gate (verbatim, from the master plan):** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per
   task). EXCEPTIONS: T3's 387k-backlog drain and T6's CONS-10 prod drain are prod-data
   mutations -> dry-run FIRST, then a real AskUserQuestion apply gate.
   **Reconciliation (explicit, so no one schedules two apply gates):** the gate's two named
   drains are the SAME single operation — the ~387k #1512 backlog IS the CONS-10 drain, and
   `dedup.drain-stale` is the one op that executes it. This design splits the work as
   code-parity (T3, autonomous, ZERO prod mutation) vs the ONE human-gated apply run (T6).
   T3's gate exception is therefore satisfied vacuously; the drain apply runs exactly once,
   in T6, and is never run twice.
8. **File ownership (verbatim):** INIT-2 OWNS all structural edits to
   `internal/dedup/engine.go` and `internal/database/embedding_store.go`. INIT-1 rebases its
   single engine.go touch on top AFTER INIT-2's engine.go waves merge. Never schedule
   INIT-1+INIT-2 waves touching engine.go concurrently.

## Data model

No new persisted value types. One new Pebble keyspace (presence-only index rows) and one new
Settings flag:

```go
// internal/database/embedding_store.go — additions (names normative)

// dedupStatusIdxPfx is the status secondary index over dedup candidates:
//   dedup:s:<status>:<%016x id> -> (empty value)
// Maintained in the SAME batch as the dedup:r: record on every create /
// status change / delete, committed with candidateWriteOpts (NoSync — #19).
const dedupStatusIdxPfx = "dedup:s:"

// dedupStatusIdxKey builds one index row key.
func dedupStatusIdxKey(status string, id int64) []byte

// candidateStatusIndexBuiltFlagKey marks the one-time backfill op complete
// (Settings, mirrors isbnIndexBuiltFlagKey = "book_isbn_index_v1_done").
const candidateStatusIndexBuiltFlagKey = "dedup_candidate_status_index_v1_done"
```

### Persistence

- `dedup:s:<status>:<id-hex16>` → empty — status secondary index (NEW, rebuildable).
- `dedup:r:<id-hex16>` → candRec JSON — unchanged.
- `dedup:e:<type>:<entityID>:<id-hex16>` → empty — unchanged (the existing entity index this
  design mirrors).
- Settings `dedup_candidate_status_index_v1_done` = "true" — index-built flag (NEW).
- Settings `dedup_stale_drain_v2_done` — drain done-flag after the v1→v2 bump (T3).

## Components

### C1. Folder-duplicates getter (`internal/database/pebble_store.go`, `memdb_reads.go`, `mock_store.go`) — T1

Replace the stub `func (p *PebbleStore) GetFolderDuplicatesCore() ([][]BookCore, error)`:

- **Semantics:** groups of ≥2 primary, not-marked-for-deletion books whose normalized title
  matches AND whose files share one parent directory (the "M4B + MP3 in one folder" case) —
  the semantics `internal/audiobooks/service_single.go` and `book_dedup.go` tier 2 already
  document at their call sites.
- **Pebble path:** delegate to the memdb fast path when `p.UseMemDB && p.mem() != nil`
  (mirror `GetBooksBySeriesIDCore`'s delegation shape exactly); scan fallback pages
  `GetAllBooksCore` and buckets by `(normalizedTitle, parentDir)` — one pass, O(N) memory of
  (title,dir)→[]BookCore. **Never** a per-book `GetBooksByTitleInDir` fan-out (that is the
  O(N²) shape).
- **MemStore twin:** `func (m *MemStore) GetFolderDuplicatesCore() ([][]BookCore, error)` in
  `memdb_reads.go`, same bucketing over the memdb book table.
- **MockStore:** add `GetFolderDuplicatesCoreFunc` hook (mirror the existing
  `GetDuplicateBooksByMetadataFunc` shape) so consumer tests can inject groups.
- **Fail-open:** errors return `nil, err`; both existing consumers already log-and-continue
  (`slog.Warn("folder dedup failed"...)` / `slog.Warn("folder duplicate detection failed"...)`).
- Parent-dir derivation: a book with files in >1 distinct parent dirs contributes its FIRST
  file's dir only if all dirs equal, else it is skipped for tier 2 (mirrors
  `parentDirForBook`'s "different parents ⇒ unknown" convention in `engine.go`).

### C2. Metadata-fuzzy getter (`pebble_store.go`, `memdb_reads.go`, `mock_store.go`) — T2

Replace the stub `GetDuplicateBooksByMetadataCore(threshold float64)`:

- **Semantics:** groups of ≥2 books whose (author, title) are fuzzy-similar at ≥ `threshold`
  (caller passes `metadataBorderlineFloor = 0.80` from `book_dedup.go`).
- **O(N²) guard (Decision 3):** bucket books by normalized author + first significant title
  token; run pairwise similarity ONLY within buckets; cap bucket size (const
  `metadataFuzzyBucketCap = 200`, skip-with-log above it). Reuse the existing similarity
  helper the tier already applies downstream rather than inventing a second metric — the
  store returns candidate GROUPS, similarity refinement stays in
  `metadataPairSimilarity`/tiebreaker.
- Same backend-pair + MockStore-hook + fail-open rules as C1.

### C3. Exact-layer emission gates + drain parity (`internal/dedup/engine.go`, `drain_stale.go`, `internal/plugins/dedup/drain_stale.go`) — T3

- Root-cause pass: verify every exact emitter routes through `upsertExactCandidate` (the
  documented single chokepoint: primary-version gate → `identifiersConflict` →
  `isBoilerplateTitle` → `hasKnownShortDuration` → `isPartVsWholeMismatch`) and that
  `checkExactTitle`/`checkExactISBN` still apply `hasPlausibleAudio` on both sides. Emission
  pair-dedupe already exists (`UpsertCandidateNew` canonicalizes + point-reads `dedupPairKey`)
  — confirm, do not rebuild.
- Close any gap found (additive guard at the chokepoint only), with anti-over-suppression
  tests (a known-good dup pair must still emit).
- **Drain parity:** `DrainStaleCandidates`' gate chain must equal the chokepoint's chain,
  gate-for-gate, same order; extend its reason buckets for any newly covered gate; bump
  `drainStaleDoneFlag` `dedup_stale_drain_v1_done` → `..._v2_done` (its doc comment mandates
  the bump when criteria change, and it lets the T6 prod apply run even if a v1 apply ever
  completed).
- The prod drain itself is NOT this component — it is C6/T6, human-gated.

### C4. Candidate status index (`embedding_store.go`, `internal/plugins/dedup/build_candidate_index.go` NEW, `internal/plugins/dedup/plugin.go` [op registration], `handlers/dedup/handler.go`) — T4

- Write/move/delete `dedup:s:` rows inside the existing batches of `UpsertCandidateNew`
  (create + layer-precedence update path when status changes), `UpdateCandidateStatus`
  (delete old-status row, set new-status row), `DeleteCandidate` (delete row). Same
  `candidateWriteOpts` commit — no new fsync (Decision 5).
- `ListCandidates`: when `f.Status != ""` AND `IsCandidateStatusIndexBuilt()`, iterate
  `dedup:s:<status>:` and point-read `dedup:r:` per hit (O(matching)); all other filters
  still apply in Go. When the flag is unset or status is empty: today's full scan, unchanged
  (fail-open to correctness).
- Backfill op `dedup.build-candidate-status-index` (NEW file in `internal/plugins/dedup/`),
  mirroring the `dedup.build-isbn-index` op + `isbnIndexBuiltFlagKey` flag pattern
  (`pebble_store_isbn_index.go`): idempotent, re-runnable, sets the flag at the end. Index
  writes are additive/rebuildable — NOT a prod-data mutation in the gate's sense (it touches
  no user-visible row), so it stays in the autonomous lane; still dry-runnable via its count
  report before flag-set. **Registration:** dedup ops are registered from the central
  definition slice in `internal/plugins/dedup/plugin.go` (looped through `r.RegisterOp`), NOT
  by self-init — the new `buildCandidateStatusIndexDef()` MUST be appended there or the op is
  un-runnable despite existing on disk.
- Handler: `both_unmatched` keeps in-handler Book-side filtering (the match signal lives on
  the Book — the store cannot pre-filter it) AND keeps an explicit bounded ceiling on
  `filter.Limit`. Deleting the limit is NOT possible: `ListCandidates` treats `limit <= 0` as
  50 (default page), so a bare call returns 50 rows, silently truncating the triage view; and
  any cap below the candidate population (~387k pending pre-drain) truncates too. The limit
  therefore stays ≥ the max candidate population (a named const, e.g.
  `bothUnmatchedScanLimit = 1_000_000`), UNCONDITIONALLY — on the indexed path it bounds the
  status-matching set, on the fallback full-scan path (flag unset, or `status` empty — note
  the handler never defaults `status`; it is only set from the `status` query param) it
  prevents converting a bounded-but-large materialization into behavior drift. The perf win
  comes from the store's indexed read path narrowing the SCAN, not from shrinking the limit.
- **Selectivity timing (be honest about when the win lands):** until T6 drains the backlog,
  `pending` ≈ the entire candidate table, so a `status=pending` indexed read gains ~no
  selectivity — and point-reading ~387k `dedup:r:` records one-by-one is SLOWER than the
  single sequential prefix scan it replaces. T4 ships the write path + flag-gated read path
  (dormant in prod until the backfill op runs); prod activation is deliberately deferred to
  T6, AFTER the drain, when `pending` is small and the index pays off (see C6 step and M3).

### C5. emit() sharding (`internal/dedup/engine.go`) — T5

- Replace the single `var mu sync.Mutex` + four maps + counter with an `emitShards [16]`
  array of `{mu sync.Mutex; emitted map[string]struct{}}` selected by FNV-hash of the
  canonical pair key — same pair ⇒ same shard ⇒ check-then-set atomicity preserved
  (Decision 6).
- Per-book state (`booksByID`, `boilerplateBookCache`, `parentDirCache`) becomes read-mostly.
  `booksByID` and `boilerplateBookCache` ARE pre-seeded from the `books` slice today — keep
  that. `parentDirCache` is NOT: it starts empty and is filled per book via `GetBookFiles`
  (partly primed inside the RunItems callback, partly lazily under the emit lock) — it must
  be BUILT, and building it costs a `GetBookFiles` store read per scan book. That store-read
  work is exactly what must stay OFF the hot lock: do it as an explicit pre-pass before the
  worker pool starts, or keep the per-book in-callback priming — either way NEVER under a
  pair-shard lock; the residual lazy-fallback lookups (`GetBookByID`/`GetBookFiles` for
  LSH-returned books outside the scan slice) move to their own dedicated `sync.Mutex`-guarded
  cache OUTSIDE the emit shard lock, so a store read never blocks emissions on other shards.
- `identifierGateDrops` becomes `atomic.Int64`.
- Keep the surrounding `registry.RunItems` pool untouched (the parallel sibling pattern —
  `internal/plugins/acoustid/backfill.go` — is already in place here; this task only fixes
  the lock, not the pool).
- REQUIRED: a `-race` test driving ≥2 workers over pairs colliding on the same pair key and
  on different keys (extend the existing `engine_fullscan_layer1_parallel_test.go` shape).

### C6. Prod drain run (ops, no code) — T6

- Sequence: deploy HEAD with T3 merged → `dedup.drain-stale` `apply=false` → present the
  reason-bucket report → **AskUserQuestion** (a text reply does not count) → `apply=true` →
  re-run dry-run + `ListCandidates(status="pending")` count to confirm shrink.
- Uses the op's own checkpointing (`drainStaleCheckpointID`) for resume; the v2 done-flag
  from T3 prevents double-apply.
- **Post-drain index activation (named, owned step — completes C4):** AFTER apply + shrink
  verification, run `dedup.build-candidate-status-index` on prod to set the built-flag and
  activate T4's indexed read path — this is the deliberate activation C4 defers ("Selectivity
  timing"), and it happens HERE, not before the drain. Never run the build op pre-drain: with
  `pending` ≈ the whole table, the indexed `status=pending` read (point-read per row) is
  SLOWER than the sequential scan it replaces. The build op writes only rebuildable index
  rows (autonomous lane per C4), so it needs no apply gate of its own.

## Migration / integration

- Getters: signatures already exist on the `Store` interface (`iface_book.go`) and all
  consumers already call them — implementing the bodies is drop-in; zero call-site changes.
- Status index: old rows lack index entries until the backfill op runs; `ListCandidates`
  fail-opens to the full scan until the flag is set (exactly the `IsISBNIndexBuilt()`
  activation pattern). Handler change (the `bothUnmatchedScanLimit` rename) lands in the same
  PR as the store change (same task) so the named ceiling never points at a missing index.
- emit() sharding changes no signatures and no persisted data.

## Milestones

- **M1 — Tiers revived (T1, T2).** Additive: dead paths start returning groups; no existing
  behavior changes (tier 1 hash groups win dedupe priority in `addGroups` ordering already).
  Read-only and non-persisting (`ScanBookDuplicates` returns an ephemeral `BookScanResult`,
  never writes candidates) — no gate needed. Note the only off-switch is PR revert +
  redeploy: there is no runtime flag, so if the revived tiers surface a flood of
  low-confidence groups in the UI, expect a revert, not a toggle.
- **M2 — Exact-layer bounded (T3).** Additive guards + drain parity + v2 flag; no data
  touched.
- **M3 — Hotspots removed (T4, T5).** Index is additive + flag-gated; sharding is
  behavior-preserving (verified by `-race` + candidate-count parity on a fixture scan).
- **M4 — THE behavior-changing milestone (T6).** Prod drain apply — gated by dry-run report +
  **AskUserQuestion** (default **off**: nothing runs without the human gate).

Each milestone is independently shippable and additive until M4.

## Files modified

| File | Change |
|---|---|
| `internal/database/pebble_store.go` | T1, T2: replace both stub getter bodies (delegate-to-memdb + paged scan fallback) |
| `internal/database/memdb_reads.go` | T1, T2: NEW MemStore twins `GetFolderDuplicatesCore` / `GetDuplicateBooksByMetadataCore` |
| `internal/database/mock_store.go` | T1: add `GetFolderDuplicatesCoreFunc` hook; T2: keep/verify `GetDuplicateBooksByMetadataFunc` |
| `internal/database/pebble_store_folder_dups_test.go` | T1: NEW tests (both backends) |
| `internal/database/pebble_store_metadata_dups_test.go` | T2: NEW tests (both backends, bucket-cap, threshold) |
| `internal/dedup/engine.go` | T3: chokepoint gate audit/additions; T5: emit() shard rewrite |
| `internal/dedup/drain_stale.go` | T3: gate-parity + new reason buckets |
| `internal/plugins/dedup/drain_stale.go` | T3: done-flag v1→v2 |
| `internal/dedup/engine_emit_shard_race_test.go` | T5: NEW `-race` test |
| `internal/database/embedding_store.go` | T4: `dedup:s:` index maintenance + indexed `ListCandidates` path |
| `internal/database/embedding_store_status_index_test.go` | T4: NEW tests |
| `internal/plugins/dedup/build_candidate_index.go` | T4: NEW backfill op |
| `internal/plugins/dedup/plugin.go` | T4: append `buildCandidateStatusIndexDef()` to the central ops slice (without this the op is un-runnable — see C4) |
| `internal/server/handlers/dedup/handler.go` | T4: rename the bare `filter.Limit = 1_000_000` literal to the named const `bothUnmatchedScanLimit`; the ceiling itself stays UNCONDITIONALLY (see C4) |

## Testing

| Test | Asserts |
|---|---|
| `TestPebbleGetFolderDuplicatesCore` | same-title+same-dir grouped; different-dir not; single-book title not; marked-for-deletion excluded |
| `TestMemStoreGetFolderDuplicatesCore` | memdb twin parity with the Pebble scan on the same fixture |
| `TestGetDuplicateBooksByMetadataCoreThreshold` | pairs ≥ threshold grouped, < threshold not; bucket cap skips oversized buckets with a log, never hangs |
| `TestScanBookDuplicatesTiers2and3Fed` | with injected MockStore hooks, tier-2/3 groups reach `BookScanResult` (regression: stubs starved them) |
| `TestUpsertExactCandidateGateParityWithDrain` | drain gate chain == chokepoint chain (table-driven pairs rejected by each gate get the same verdict in both) |
| `TestExactEmitHappyPathSurvives` | anti-over-suppression: a known-good dup pair still emits after T3 |
| `TestListCandidatesStatusIndexParity` | indexed status query == full-scan result on the same fixture; unset flag falls back to scan |
| `TestCandidateStatusIndexMaintenance` | create→status-change→delete leaves exactly the right `dedup:s:` rows |
| `TestEmitShardRace` (`-race`) | 2+ workers, colliding + disjoint pair keys: no race, no double-emit, counts equal serial run |

## Rollback

**Gate (verbatim):** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). EXCEPTIONS: T3's
387k-backlog drain and T6's CONS-10 prod drain are prod-data mutations -> dry-run FIRST, then a
real AskUserQuestion apply gate.

- T1/T2 getters are additive revivals — revert the PR and the tiers return to empty (today's
  behavior); no data written.
- T3 guards are forward-only code — revert the PR; the v2 flag simply never gets consulted.
- T4 index is flag-gated dormant until the backfill op sets
  `dedup_candidate_status_index_v1_done`; instant disable = unset the flag (ListCandidates
  fail-opens to the full scan); full revert = revert PR + optional prefix-delete of `dedup:s:`.
- T5 sharding — revert the PR restores the single-mutex emit(); no persisted state involved.
- T6 drain writes ONLY soft status reclassifications (`stale-drain`, never a delete) — the
  data survives intact and auditable. **But be explicit: T6 is roll-forward only.** No
  status-restore op exists today (grep of `internal/plugins/dedup` finds only the drain op);
  recovery would require BUILDING a new restore op that routes through
  `UpdateCandidateStatus` (so T4's `dedup:s:` index stays in sync with the records), itself
  dry-run + AskUserQuestion gated. Do not represent recovery as an existing instant inverse.
  Nothing runs without the AskUserQuestion gate.

## Open questions (resolved — recorded for the plan)

1. ~~Where does the master plan's `service_single.go:251` consumer live (internal/dedup has no
   such file)?~~ → `internal/audiobooks/service_single.go` — `svc.store.GetFolderDuplicatesCore()`
   inside `GetDuplicateBooks`; verified by grep this session.
2. ~~Is there a MemStore to twin (scout found no `mem_store*.go`)?~~ → Yes: `type MemStore struct`
   in `internal/database/memdb_store.go`, read methods in `memdb_reads.go`, reached via
   `p.mem()`; the scout's glob missed the `memdb_*` naming.
3. ~~Does drain_stale.go implement CONS-10?~~ → File is tagged DEDUP-1 / CONS-16 / CONS-17 (not
   CONS-10) and reclassifies to `"stale-drain"` (not `dismissed`); the master plan's CONS-10
   label is treated as the drain-the-backlog intent, executed via this op.
4. ~~One index or many for T4?~~ → Status only (Decision 5); band/similarity stay in-Go filters
   on the status-narrowed set.
