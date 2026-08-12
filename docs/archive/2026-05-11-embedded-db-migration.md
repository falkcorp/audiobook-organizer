# Embedded Database Migration Research
<!-- version: 1.0.0 -->
<!-- last-edited: 2026-05-11 -->

## TL;DR

| Store | Current | Recommendation | Rationale |
|---|---|---|---|
| `audiobooks.pebble` (main library) | PebbleDB | **Keep PebbleDB** | Already pure-Go, production-proven at 200K+ keys, complex multi-index batches needed |
| `activity.db` | SQLite (CGo) | **→ NutsDB** | Log-structured writes match append-heavy pattern; eliminates CGo |
| `metrics.db` | SQLite (CGo) | **→ NutsDB** | Low volume, same library as activity.db, eliminates CGo |
| `ai_scans.db` | PebbleDB sidecar | **Fold into main pebble** | Already Pebble; merge under `aiscan:*` prefix to eliminate sidecar instance |
| `embeddings.db` | SQLite (CGo) + chromem-go overlay | **Fold SQL tables into pebble; keep chromem-go** | Dedup candidates + embedding cache → pebble keys; chromem-go stays for ANN queries |
| `library.bleve` | Bleve/scorch | **Keep** | No replacement needed |
| LanceDB | — | **Reject** | No Go SDK; requires Python/Rust sidecar — not embedded |
| BuntDB | — | **Reject** | Unresolved AOF corruption (#72, 4 years open); deadlock bug (#74); stale maintenance |

**Net result after migration:** zero CGo dependencies (SQLite gone entirely), three fewer sidecar DB files, one NutsDB for append logs, PebbleDB + chromem-go for everything else.

---

## What Each Database Actually Does (from codebase mapping)

### audiobooks.pebble — Main Library Store

- **~200K+ keys**: 10,891 books, 2,970 authors, 8,507 series, 97K+ external_id mappings, operations, sessions, playlists, preferences
- **Key pattern**: text prefix + ID (`book:<id>`, `author:<id>`, `ext_id:<source>:<id>`)
- **Multi-key atomic batches**: every create/update writes primary key + 2-4 index keys in one `pebble.Batch` with `pebble.Sync`
- **Range scans**: `NewIter(SeekPrefixGE)` for list operations; secondary-index pattern (index key → primary key → value get)
- **PebbleDB strengths here**: LSM compaction, MVCC reads never block writes, `pebble.Batch` atomicity across multiple keys, proven at this scale

**Why NutsDB cannot replace this:**
- NutsDB issue #255: reads in the same write tx don't see that tx's writes — this breaks the "write primary + write index + read back to confirm" patterns throughout `pebble_store.go`
- NutsDB has no equivalent to `pebble.Batch` for multi-key atomicity across multiple keys in different buckets
- Single-writer lock in NutsDB vs PebbleDB's concurrent MVCC

### activity.db — Append-heavy Event Log

- **Schema**: single `activity_log` table, 13+ indexed columns, ~100K–1M rows over time
- **Write pattern**: high-frequency INSERT during scan/organize (hundreds per minute); `ActivityBatcher` coalesces debug-tier entries
- **Read pattern**: paginated timeline (ORDER BY timestamp DESC), filter by tier/type/level/source/op_id/book_id
- **Compaction**: `CompactByDay` aggregates old entries into daily digests; `Prune` hard-deletes by tier+age

**NutsDB fit**: Log-structured write path is a natural match for append-heavy INSERTs. Time-prefixed keys (`YYYYMMDDTHHMMSSZ:<ulid>`) enable `tx.RangeScan(bucket, start, end)` for timeline queries. Per-tier isolation via buckets maps cleanly to the existing tier filter.

### metrics.db — Cache Stats Snapshots

- **Schema**: `cache_stats_history`, ~86K rows at steady state (10 caches × 288 snapshots/day × 30 days)
- **Write pattern**: batched INSERT every 5 minutes (very low rate)
- **Read pattern**: time-range query for dashboard trend chart
- **Retention**: 30-day prune on each tick

**NutsDB fit**: Trivial workload. Time-keyed entries + bucket-per-cache-name. Use NutsDB TTL (set 30-day expiry on each write) to replace explicit prune logic.

### ai_scans.db — AI Batch Job Records

- **Already PebbleDB** (not SQLite as previously assumed)
- **Key schema**: `scan:<id>`, `scan_phase:<scanID>:<phaseType>`, `scan_result:<scanID>:<resultID:06d>`, `counter:scan`, `counter:scan_result`
- **Volume**: low (one scan per user action)
- **CRUD**: standard create/read/update/delete with range iteration for list operations

**Recommendation**: merge into `audiobooks.pebble` under `aiscan:*` prefix. Same Pebble code, same key patterns, eliminates a second Pebble instance and its WAL/manifest overhead. The `AIScanStore` interface can be added to the main `Store` composition.

### embeddings.db — Vectors + Dedup Candidates

- **SQLite tables**: `embeddings` (float32 BLOB + metadata, ~14-15K rows) and `dedup_candidates` (similarity pairs, status tracking)
- **chromem-go overlay** (`chromem_embedding_store.go`): wraps chromem-go for ANN similarity queries; SQLite `embeddings` table still handles the embedding cache (text_hash → vector, avoids re-calling OpenAI API)

**Important correction**: The comment in `chromem_embedding_store.go` claims "HNSW-based approximate nearest neighbor search" — **this is wrong**. chromem-go v0.7.0 uses exhaustive brute-force cosine scan. HNSW is on the roadmap but not implemented.

**Recommendation**:
- Move `dedup_candidates` table → `pebble` under `dedup:candidate:<id>` prefix
- Move `embeddings` cache rows → `pebble` under `embedding:cache:<textHash>:<model>` prefix
- chromem-go stays as the ANN query engine (already integrated, pure Go, works at 10-15K scale)
- Fix the HNSW comment

**If corpus grows beyond ~20K vectors and query latency becomes an issue**, upgrade path is `github.com/coder/hnsw` (pure Go HNSW, production-proven at Coder Inc.) — not LanceDB (no Go SDK).

---

## Library Assessment

### NutsDB (Recommended for activity.db + metrics.db)

- **v1.1.0** (Dec 2025), **v2.0.0-pre** (Feb 2026). Apache-2.0. 3,566 stars, actively maintained.
- Pure Go, zero CGo, zero external dependencies
- Log-structured (segment files, 256 MB default), in-memory index
- **Two RAM modes**: `HintKeyValAndRAMIdxMode` (keys+values in RAM, default) vs `HintKeyAndRAMIdxMode` (keys only in RAM, values on disk) — **use `HintKeyAndRAMIdxMode` for activity.db at scale**
- ACID transactions: single writer, concurrent readers (`sync.RWMutex`). `db.View()` / `db.Update()` / `db.Begin()`
- Range scan: `tx.RangeScan(bucket, start, end)`, `tx.PrefixScan(bucket, prefix, offset, limit)`, `Iterator` with `Seek()`
- Compaction: **must configure `MergeInterval`** (default 0 = disabled); without it, segment files grow forever
- TTL: per-key TTL supported (`tx.Put(bucket, key, value, ttl)`)
- **Issue #255**: reads within a write transaction don't see that tx's own uncommitted writes. Mitigation: never read a key in the same tx that just wrote it — use separate read transactions for confirmations.

### BuntDB (Rejected)

- **Last commit: September 2024.** Effectively unmaintained.
- **Issue #72 (open 4+ years)**: Production AOF corruption — values silently truncated mid-file on clean shutdown, resulting in "invalid database" on next open. Root cause never diagnosed.
- **Issue #74 (open 4+ years)**: Deadlock under concurrent access.
- All values stored in heap as Go strings — 1M rows ≈ 300+ MB heap continuously.
- **Do not use.**

### chromem-go (Keep for embeddings ANN)

- **v0.7.0** (Sep 2024). MPL-2.0. 941 stars.
- Pure Go, zero CGo, zero external processes
- Brute-force cosine scan (O(n)) — NOT HNSW. At 15K vectors × 1536 dims: ~20-40ms/query. Acceptable for background dedup.
- Metadata: `map[string]string` per document, exact-match filtering
- Persistence: gob file per collection; optional gzip; no WAL (tolerable for dedup re-scan on crash)
- Already integrated at `internal/database/chromem_embedding_store.go`

### LanceDB (Rejected)

- **No Go SDK.** Go caller would need HTTP REST against a running LanceDB server or a Python/Rust sidecar.
- Violates "integrated databases" requirement.
- **Do not use.**

---

## Migration Plan (ordered by ROI)

### Phase 1: Fix the wrong comment (10 minutes)
- `internal/database/chromem_embedding_store.go`: change "HNSW-based approximate nearest neighbor search" → "exhaustive cosine-similarity search (brute-force O(n))"

### Phase 2: Replace activity.db and metrics.db with NutsDB (medium effort)
- Add `github.com/nutsdb/nutsdb` dependency
- Implement `NutsActivityStore` satisfying the existing concrete interface
  - Bucket-per-tier (change, debug, audit, digest)
  - Key: `<timestamp-ns>:<ulid>` — enables `RangeScan` for time-range queries
  - `HintKeyAndRAMIdxMode` to keep values on disk
  - `SyncEnable: true` if durability required; false for high-throughput
  - `MergeInterval: 6 * time.Hour`
- Implement `NutsMetricsStore` satisfying cache stats interface
  - Bucket-per-cache-name; time-keyed entries; per-entry TTL = 30 days (replaces prune cron)
- One-time migration: `SELECT * FROM activity_log ORDER BY timestamp ASC` → bulk NutsDB write
- Remove `go-sqlite3` from go.mod once embeddings.db also migrated (Phase 3)

### Phase 3: Fold embeddings.db into main PebbleDB + keep chromem-go (medium effort)
- Move `dedup_candidates` rows → pebble keys: `dedup:candidate:<id>`, `dedup:status:<status>:<id>`, `dedup:pair:<a>:<b>`
- Move `embeddings` cache rows → pebble keys: `embedding:cache:<textHash>:<model>`
- Add `EmbeddingCacheStore` and `DedupCandidateStore` sub-interfaces to `Store` composition
- Implement in `pebble_store.go` (same patterns as existing entities)
- chromem-go keeps its separate `.chromem` directory for ANN data — no change to that path

### Phase 4: Fold ai_scans.db into main PebbleDB (low effort)
- `ai_scans.db` is already PebbleDB with the same key/value patterns as `audiobooks.pebble`
- Add `AIScanStore` sub-interface to the main `Store` interface
- Implement in `pebble_store.go` under `aiscan:*` key prefix (mirrors existing `AIScanDBStore` logic)
- Open only one Pebble instance; eliminate `ai_scans.db` sidecar

---

## What We're NOT Doing

- **Not replacing `audiobooks.pebble`** with NutsDB or BuntDB. PebbleDB is the right database for the main library store — LSM, atomic multi-key batches, MVCC, proven at 200K+ keys. NutsDB's read-your-writes bug (issue #255) and lack of multi-bucket batch atomicity would require architectural rework for zero gain.
- **Not adopting LanceDB**. No Go SDK; requires external process.
- **Not adopting BuntDB**. Unresolved AOF corruption. Stale maintenance. Heap-only storage.
- **Not replacing library.bleve**. Full-text search with English stemming, boosted fields, numeric range, boolean facets — Bleve does this well and there's no compelling alternative in pure Go.
