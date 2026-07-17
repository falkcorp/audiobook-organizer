<!-- file: docs/specs/fable5-spec-memory-db-optimization.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c8e1a4f-7b2d-4e9a-9c3f-6d0b8e2a5f7c -->

# SPEC 3: Memory & Database Optimization

Scope correction up front — **the review brief's premises were partially stale** (verified
against code this review):

1. `embeddings.db` (SQLite) **no longer exists**: embeddings live in PebbleDB
   (`emb:v:<entityType>:<entityID>` records, `emb:c:<model>:<textHash>` content cache,
   `internal/database/embedding_store.go`). The "eliminate embeddings SQLite" priority is
   already done; what remains is compression (§3).
2. The "disabled 69GB cache warm-up" is, in current code, the **memdb warm-up — enabled by
   default** (`UseMemDB = true`, `pebble_store.go:225`, async at :271-283) and already
   stripped (`memdb_strip.go`: Description, BookSig*, AcoustIDFingerprint, diagnostics
   removed before insert). The 69GB incident predates the stripping; the *pattern* fix
   shipped. What remains is preventing regression (§5) and the remaining big consumers.
3. `ai_scans.db` is also PebbleDB-backed now (`ai_scan_store.go`, `aiscan:` prefix in
   shared mode).
4. What **does** remain on SQLite: the legacy `sqlite_store_*.go` Store implementation
   (~7,938 lines) opened unconditionally at startup via `database.go:20`
   (`sql.Open("sqlite3", …)`) — findings MED-4.

Prod disk reality check (2026-06-09, `/var/lib/audiobook-organizer/`):
`audiobooks.pebble` = **11GB** (not the 20–40GB estimated below); stale leftovers from
completed migrations still on disk — `embeddings.db` 1.8GB sparse/924MB, `activity.db`
842MB/140MB, `metrics.db`+wal/shm, `audiobooks.chai/` — all last written 2026-05-11.
TASK-022 must include archiving/deleting these dead files (~1GB+ reclaimed, and removes
the confusion of dead stores sitting next to live ones).

Current steady-state RSS model at ~50K books / ~308K files (agent audit, spot-checked):

| Component | RSS | Notes |
|---|---|---|
| go-memdb (stripped projections) | ~2.4–2.7GB | books ~2GB dominated by retained AcoustIDSeg0–6 strings + remaining Book fields |
| chromem (in-RAM vectors) | ~600MB | 50K × 3072-dim float32; hydrated once at startup from Pebble |
| Pebble block cache / runtime | ~100–300MB | |
| **Modeled baseline** | **~3.3GB** | bottom-up estimate |
| **Prod observed (2026-06-09)** | **~7.0GB steady / 8.9GB peak + 2.2GB swap peak** | systemd `MemoryCurrent` at 9-day uptime; unit accounting at restart. The ~3.7GB gap between model and reality (Go heap slack, Pebble caches/compaction, goroutine/op buffers, untracked allocations) is itself a finding — TASK-023's telemetry should attribute it before further optimization claims are made |

## 1. PebbleDB schema audit — results

Full prefix inventory is in the agent audit (45+ prefixes; secondary indexes are already
minimal ~26B id-pointers — good). Optimization-relevant observations:

- **Dead prefixes:** `book:series:<id>`, `book:author:<id>` (replaced by memdb queries in
  Task 3.4) — confirm zero live readers, then add a one-off sweep to delete remaining keys.
- **Fat values on hot paths:** `book:<id>` (2–4KB JSON) is deserialized in full by several
  count/ID-only paths. The memdb layer already shields most reads; remaining direct-Pebble
  iterators (e.g. backfills, stats fallbacks) should use the existing stripped decode or a
  field-subset unmarshal. Estimated win: latency/GC on scans, not resident RSS — measure
  before investing (§6).
- **`BookFile.AcoustIDSeg0–6`** are *deprecated* (whole-file migrated, `store.go:685-694`)
  yet still stored in every `book_file:` value AND retained in memdb (only Seg1–6
  diagnostics stripped; Seg0+ retained for the disabled tier-2 fuzzy path). Once SPEC 1's
  LSH index lands, segments have **zero** readers → strip from memdb entirely (~550–900MB
  RSS, the single biggest memory win available) and drop from Pebble values via lazy
  rewrite-on-touch + background sweep. This is the headline item.
- `operation:`/`operationlog:` rows (1–3KB) accumulate unboundedly — add retention sweep
  to maintenance (cheap, disk-only).

## 2. Eliminate SQLite — remaining plan

Target: delete `internal/database/sqlite_store_*.go` and the `database/sql` +
`mattn/go-sqlite3` dependency.

1. Inventory callers: `grep -rn "sqlite_store\|DB\b" internal/database/database.go` plus
   interface-dispatch audit — which constructors can still select the SQLite Store, and
   does any prod config path reach it? (Expected: no; prod is PebbleDB-only.)
2. Parity check: for each method implemented only in sqlite_store (none expected; PebbleDB
   is the canonical impl), port or delete.
3. Remove `sql.Open` from startup; gate the legacy activity-log SQLite reader (if the
   legacy activity import path still reads an old `.db`) behind an explicit migration
   command rather than runtime linkage.
4. Effort M, savings: ~8K lines, one CGO dependency (faster builds, smaller binary,
   no lock-contention trap), zero runtime RSS change.

## 3. Embedding compression (PebbleDB, already migrated)

Current: `emb:v:` record stores raw float32 LE blob (3072-dim → 12,288B) in JSON-wrapped
rec, ~12.5KB/book → ~625MB disk for 50K + ~600MB RAM in chromem.

- **Disk:** float16 quantization (halves to 6,144B) + zstd block compression on the blob
  before JSON wrap. Cosine ranking loss at float16 is negligible for a 0.85/0.95
  threshold regime. Combined ≈ 3.5–4× reduction (~160–180MB). Product quantization (PQ)
  would reach 10×+ but adds an index/codebook lifecycle that is not justified at 625MB on
  a ZFS pool — recommend **float16+zstd now, PQ never unless corpus 10×es**. (The brief's
  "300MB raw" assumed 1536-dim; actual model is 3072-dim — verified
  `chromem_embedding_store.go:19-20`.)
- **RAM (chromem):** store float16 in Pebble, dequantize to float32 on hydrate (RAM
  unchanged, startup I/O halved), OR switch chromem collection to float16 if supported —
  investigate in-task; do not commit to RAM savings here.
- Migration: versioned flag `emb_f16_v1_done`; dual-read (accept v0 float32 and v1
  float16 by header byte), write v1; background re-encode op; rollback = keep dual-read.

## 4. NutsDB and Bleve evaluation

- **NutsDB** (activity 7-tier buckets + metrics, `nuts_activity_store.go`): functioning;
  key formats (`uint64-nano:ulid`) are lexicographic and would map 1:1 onto Pebble
  prefixes (`act:<tier>:<timekey>` …). Migration is *straightforward but not free*
  (compaction/TTL re-implementation, 256MB segment semantics differ). Recommendation:
  **migrate** — it removes an entire storage engine (operational simplicity is the owner's
  stated goal), and the activity store already has a Store interface with two backends
  (SQL legacy + NutsDB) so a third (Pebble) slot exists by design. Effort L. Do it after
  the higher-ROI items; hot-deployable with a cutover op (dual-write window + backfill,
  flag `activity_pebble_v1_done`).
- **Bleve** (scorch, `internal/search/bleve_index.go`): 60–100MB index, English analyzers,
  field boosts, used only by `/api/v1/audiobooks/search`; library listing already runs on
  memdb. Pebble prefix scans cannot replace stemmed full-text + relevance ranking, and
  SQLite FTS5 would *re-add* SQLite against the owner's direction. **Keep Bleve.** Only
  action: make index rebuild incremental/batched (exists: `IndexBookBatch`) and document
  rebuild-from-Pebble as the recovery path.

## 5. GC-pressure / large-object cache audit

- The 69GB-class pattern (caching full API-response/full-struct objects) has one remaining
  guardrail gap: nothing *prevents* a new field added to `Book` from ballooning memdb
  again. Add a startup metric: per-memdb-table approximate bytes (sampled marshal of N
  rows × count) exported via the existing metrics store + a CI-adjacent soft assert in a
  test (e.g. stripped Book sample ≤ 4KB).
- No other full-object caches found (agent sweep: playlist evaluator is per-query;
  stats cache is a single 1–5KB Pebble value with the dirty-flag pattern from PR #1072).
- Chromem hydration: one-shot goroutine, add WaitGroup join on Stop (findings MED-8).

## 6. Prioritized optimization list (effort vs savings)

| # | Item | Effort | Savings | Window |
|---|---|---|---|---|
| 1 | Strip AcoustIDSeg0–6 from memdb after LSH lands (depends SPEC 1 LSH) | S | **~550–900MB RSS (~25–35%)** | hot deploy |
| 2 | Drop seg fields from `book_file:` Pebble values (lazy + sweep, flag `bookfile_seg_drop_v1_done`) | M | ~200–400MB disk; faster file scans | hot deploy |
| 3 | Embedding float16+zstd (`emb_f16_v1_done`) | M | ~450MB disk; ~½ hydrate I/O | hot deploy, dual-read rollback |
| 4 | Legacy SQLite store removal | M | 8K LOC, CGO dep, drift risk | hot deploy (no data) |
| 5 | memdb size telemetry + regression assert | S | prevents 69GB-class recurrence | hot deploy |
| 6 | operation/operationlog retention sweep | S | unbounded growth stopped | hot deploy |
| 7 | Dead-prefix sweep (`book:series:`, `book:author:`) | S | small disk; schema hygiene | hot deploy |
| 8 | NutsDB → Pebble activity/metrics migration | L | −1 storage engine | hot deploy w/ dual-write window |
| 9 | Bleve | — | none — keep | — |

Nothing here requires a production maintenance window; items 2/3/8 need their backfill op
to complete before flag-flip (repo rule). Items with <10% impact (7) are included only as
hygiene riders on adjacent tasks per the brief's >10% threshold for full migration plans —
full before/after key schema + rollback are specified in the implementation plan for items
1–3 and 8.
