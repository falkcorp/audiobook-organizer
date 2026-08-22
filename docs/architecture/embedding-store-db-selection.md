<!-- file: docs/architecture/embedding-store-db-selection.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7a97ba03-e0f0-46dd-aa57-bf8826a5af25 -->
<!-- last-edited: 2026-08-22 -->

# Embedding Store: Database Selection Rationale

**Decision date:** 2026-05-11
**Context:** Migrating `embeddings.db` (SQLite CGo sidecar) to a CGo-free,
embedded KV store that shares the process with the main `audiobooks.pebble` DB.

--- ****

## What the embedding store actually holds

Two logically separate concerns live in `embeddings.db`:
****
1. **Vector store + text-hash cache** — float32 embedding vectors keyed by
   `(entityType, entityID)`, plus a content-hash cache keyed by `(model, textHash)`
   that short-circuits repeated OpenAI calls for identical text.

2. **DedupCandidate CRUD** — pairs of potentially-duplicate entities, with
   layer precedence (`exact > llm > embedding`), similarity scores, LLM verdicts,
   human review status, and secondary indexes for pagination and filtering.

The store is hit on every dedup scan (potentially tens of thousands of calls per
maintenance run) and on every metadata fetch that goes through the embedding scorer.

---

## Candidates evaluated

### PebbleDB (`cockroachdb/pebble/v2`) ✅ chosen

PebbleDB is already in `go.mod` as the primary application database. The main
`audiobooks.pebble` DB instance is shared with the embedding store via a `DB()`
accessor, so no new process or file descriptor is needed.

**Why PebbleDB wins for this use case:**

- **Already present.** Zero new dependencies. The binary size, license, and
  security-review surface don't change.

- **MVCC concurrent reads and writes.** The dedup engine reads vectors while
  simultaneously upserting candidates from multiple goroutines. PebbleDB's
  MVCC model handles this without explicit locking at the application layer.
  NutsDB and BuntDB serialize writes through a single goroutine or mutex.

- **Atomic batch writes across keys.** `UpsertCandidate` must atomically update
  both the record (`dedup:r:<id>`) and the pair-uniqueness index
  (`dedup:p:<type>:<a>:<b>`). PebbleDB's `*Batch` commits both in one fsync.
  NutsDB's transactions are single-bucket; cross-bucket atomicity requires its
  own batch API that has looser consistency guarantees.

- **Mutable records.** `UpdateCandidateStatus` and `UpdateCandidateLLM` modify
  fields in place. PebbleDB supports arbitrary in-place writes. NutsDB is
  append-only: every update appends a new log entry and old entries are only
  reclaimed on compaction. For frequently-updated dedup candidates (status
  flips during review), this creates unbounded log growth until the next merge.

- **No TTL needed.** NutsDB's primary advantage is per-entry TTL expiry, which
  is irrelevant here — embeddings and dedup decisions are meant to live forever.

- **Prefix scan for ListByType and ListCandidates.** PebbleDB range iterators
  with `LowerBound`/`UpperBound` support efficient prefix scans. NutsDB's
  range API is key-ordered within a single bucket and less ergonomic for
  multi-prefix namespacing. BuntDB supports range scans but only via string
  indexes, not arbitrary byte prefixes.

- **Production-proven at scale.** PebbleDB is the storage engine behind
  CockroachDB. At 100K embeddings (the rough production estimate), its LSM
  compaction and bloom-filter-backed point lookups outperform BuntDB's
  in-memory sorted index.

---

### NutsDB (`nutsdb/nutsdb`) ❌

NutsDB is already in `go.mod` (used for the `ActivityStorer` and `MetricsStorer`
namespaces) and was the first alternative considered.

**Why NutsDB was rejected:**

- **Append-only WAL for mutable records.** NutsDB is architected for time-series
  and event-log workloads where entries are written once and expired by TTL.
  Dedup candidates are written once and then updated many times (status changes,
  LLM verdicts, human reviews). Each update appends a full copy of the record.
  The reclaim pass runs only on explicit compaction. Under normal usage this means
  the on-disk file grows without bound until the next maintenance window.

- **Single-writer goroutine model.** NutsDB serializes all writes through an
  internal goroutine. The dedup engine's concurrent goroutines (book scanner,
  author scanner, LLM reviewer) would queue behind each other. PebbleDB's MVCC
  allows true concurrent writes.

- **No cross-bucket atomic writes.** Atomic writes across multiple keys in
  different buckets require NutsDB's `Merge` API, which is designed for
  compaction, not transactional multi-key updates. The dedup pair-index update
  (`dedup:r:*` + `dedup:p:*`) must be atomic to prevent index corruption on
  crash. PebbleDB's `*Batch.Commit` provides this natively.

- **Already used for a different purpose.** NutsDB's strength in this codebase
  is TTL-expiring activity logs and sliding-window metrics. Mixing embedding
  vectors (large binary blobs, no TTL) into the same NutsDB instance risks
  amplifying its append-only growth problem and muddying the separation of
  concerns.

- **Good fit, wrong workload.** NutsDB is the right choice for the `ActivityStorer`
  (append-heavy, TTL-expired) and `MetricsStorer` (time-bucketed counters, evict
  after 7 days). It is the wrong choice for embeddings (large blobs, mutable,
  no TTL) and dedup candidates (mutable CRUD with multi-key atomicity).

---

### BuntDB (`tidwall/buntdb`) ❌

BuntDB is an in-memory KV store (with optional WAL persistence) written in pure Go.

**Why BuntDB was rejected:**

- **In-memory primary structure.** BuntDB keeps its entire index in memory.
  The production embedding corpus (estimated 50K–200K book+author vectors at
  ~6 KB each for 1536-dim float32) would consume 300 MB–1.2 GB of heap just
  for the index, before GC overhead. PebbleDB's LSM keeps cold data on disk and
  uses block cache for hot pages.

- **No binary key support.** BuntDB keys and values are strings. Storing raw
  float32 vector blobs requires base64 encoding, adding ~33% size overhead and
  encoding/decoding CPU cost on every read. PebbleDB is a byte-native store.

- **Single-writer lock.** BuntDB uses a `sync.RWMutex` for all writes. Concurrent
  `UpsertCandidate` calls from the dedup engine's goroutine pool would serialize.

- **No prefix-range scan.** BuntDB's `AscendRange` requires a declared string
  index and works on full string comparison. Prefix scanning (e.g., all
  `dedup:r:` keys to enumerate candidates) requires scanning the full keyspace
  and filtering in the application layer. PebbleDB's `IterOptions.LowerBound` /
  `UpperBound` gives O(log N) seek directly to the prefix.

- **WAL durability is opt-in.** By default, BuntDB persists on a best-effort
  schedule. Dedup candidates include irreplaceable human review decisions; the
  embedding cache embeds quota cost. Both require durable writes. PebbleDB uses
  WAL + fsync by default (`pebble.Sync`).

---

### LanceDB / sqlite-vec / usearch ❌ (vector-only stores)

These were evaluated for the vector storage component only (not dedup candidates).
All three are ANN (approximate nearest neighbor) stores optimized for similarity
search.

**Why they were rejected:**

- **We already have chromem-go.** The `ChromemEmbeddingStore` (using
  `philippgille/chromem-go` with HNSW indexing) handles ANN search. Adding a
  second vector store alongside chromem-go would be redundant.

- **Vectors still need to be read back.** `dedup/engine.go` calls
  `embedStore.Get("book", bookID)` and reads `existing.Vector` before passing it
  to `chromemStore.FindSimilar`. The vector store is not purely write-once;
  it's also used as a lookup cache. sqlite-vec and usearch are optimized for ANN
  query, not keyed point lookups.

- **CGo dependency.** LanceDB (Rust FFI), sqlite-vec (CGo), and usearch (CGo)
  all reintroduce the CGo dependency that was the primary motivation for this
  migration.

- **Overkill for the dedup candidate store.** The dedup candidates aren't vectors —
  they're structured records. None of these stores supports CRUD with layer
  precedence and pagination.

---

## Summary

| Property | PebbleDB | NutsDB | BuntDB | LanceDB/vec |
|---|---|---|---|---|
| Already in go.mod | ✅ | ✅ | ❌ | ❌ |
| CGo-free | ✅ | ✅ | ✅ | ❌ |
| Mutable records (no WAL blowup) | ✅ | ❌ | ✅ | N/A |
| MVCC concurrent writes | ✅ | ❌ | ❌ | N/A |
| Cross-key atomic batch | ✅ | partial | ❌ | N/A |
| Disk-backed (not in-memory) | ✅ | ✅ | opt-in | ✅ |
| Prefix range scan | ✅ | partial | ❌ | ❌ |
| Binary key/value | ✅ | ✅ | ❌ | ✅ |
| No TTL required | ✅ | wastes WAL | ✅ | ✅ |

PebbleDB is the unambiguous choice: it already exists in the process, handles
mutable records without WAL amplification, supports atomic multi-key batches,
and provides MVCC concurrency that matches the dedup engine's goroutine model.
