<!-- file: docs/database-by-feature-2026-05-30.md -->
<!-- version: 1.0.0 -->
<!-- guid: b1e8f3d2-7c4a-4b58-a91d-9e2a5c6d8b14 -->
<!-- last-edited: 2026-05-30 -->

# Best-in-Class Database Per Feature

**Companion doc to:** [`database-evaluation-2026-05-30.md`](database-evaluation-2026-05-30.md)

**Question:** "For each thing in audiobook-organizer that needs persistence,
what would the *ideal* storage be if we ignored every constraint we currently
have (pure Go, single binary, etc.)?"

**Why this matters:** the previous eval kept hitting walls because we asked
"which embedded KV is best for everything?" — wrong question. **No single
database is best for everything.** Google doesn't use Bigtable for sessions.
Netflix doesn't use Cassandra for full-text search. Spotify doesn't use their
playlist DB for vector matching against Echo Nest fingerprints. They each
run 10+ specialised stores.

This doc maps each of our workloads to its *category-best* storage —
embedded *or* server, Go *or* not, free *or* commercial. The bottom of the
doc reassembles those choices into three composite architectures (with
increasingly pragmatic constraints) so you can see what the menu actually
looks like.

---

## Method

For each feature I list:
- **Workload shape** — what we actually do with it
- **Volume** — current scale (per memory snapshot + prod stats today)
- **Hot operations** — what the read/write profile looks like
- **Ideal DB (no constraints)** — best-in-class for that exact shape
- **Embedded-Go pragmatic pick** — best option under the pure-Go constraint
- **What sucks if we get it wrong** — failure mode if shape mismatches

---

## Feature-by-feature

### 1. Books / Authors / Series / Narrators / Works (entity graph)

- **Shape:** Relational with many-to-many (book↔narrator, book↔author, series↔book). Heavy JOIN traffic.
- **Volume:** 50K books, 8.8K authors, 21.6K series, ~12K narrators, works set
- **Hot ops:** Find books by series, by author, by narrator; book → author + series + cover lookup; series → ordered books; aggregate counts per author
- **Ideal DB:** **PostgreSQL 16+** — joins, foreign keys, B-tree + GIN indexes, partial indexes, materialised views, JSON column for varied metadata, native FTS via `tsvector`. Decades of query-planner optimisation. Logical replication if we ever go multi-node.
- **Embedded pragmatic:** **`modernc.org/sqlite`** — same query shape, pure Go, single-file. We lose Postgres-specific extensions but get 90% of the value.
- **What sucks if wrong:** This is exactly what we do today on Pebble + memdb — every join is hand-rolled with secondary indexes, N+1 protection by hand, no query planner. We've been paying this tax invisibly for years.

### 2. BookFiles (one-to-many child of Books)

- **Shape:** Relational child table with FK to books, scanned in bulk for fingerprint operations
- **Volume:** 308K files (avg ~6 per book)
- **Hot ops:** `WHERE book_id = ?` (one book's files), `WHERE acoustid_seg0 = ?` (exact fingerprint match), `WHERE fingerprint_status IS NULL` (rescan queue)
- **Ideal DB:** **Same Postgres** as books — FK relationship makes this trivial. Plus expression indexes on lower-cased extension, partial index on `WHERE missing = false`.
- **Embedded pragmatic:** Same SQLite. **One join replaces our `GetBookFilesForIDs` + memdb-index dance.**
- **What sucks if wrong:** N+1 query patterns. We've fixed half a dozen of these.

### 3. AcoustID fingerprints (the painful one)

- **Shape:** **Vector similarity search.** Each file has a 5-min-or-whole-file chromaprint (100KB-1.5MB). Need: exact match (point lookup) + approximate nearest neighbours by Hamming distance + LSH bucketing.
- **Volume:** 88K populated today, target 300K
- **Hot ops:** `find_similar(fp, threshold=0.85)` over 300K candidates; bulk recompute; LSH bucket lookups
- **Ideal DB:** **Qdrant** (Rust, embedded mode or HTTP) or **Milvus** (Go core, cluster). Both are purpose-built vector DBs with:
  - Native HNSW + IVF indexes
  - Hamming distance as a first-class metric
  - Filtering ("find similar fp where book_id != X")
  - Hybrid search (combine vector similarity with metadata predicates)
  - Persistence with snapshot/restore
- **Honourable mention:** **pgvector** (Postgres extension) — if we already run Postgres for everything else, this is a single `CREATE EXTENSION` away. Supports HNSW since v0.5, plenty fast for 300K vectors. But chromaprint fingerprints aren't dense float vectors — they're sparse binary — so pgvector is suboptimal vs Qdrant.
- **Embedded pragmatic:** **None exists.** Closest: hand-roll LSH index on Pebble or Badger (200 lines of Go). `go-hnsw` is dormant. `LanceDB` has Go bindings but they're experimental.
- **What sucks if wrong:** Exactly what we have today — O(N) full-table Hamming compare per query, infeasible at 300K. Disabled `acoustidFuzzyEnabled` because it was un-deployable.

### 4. Book signatures (per-book combined fingerprint)

- **Shape:** Same as #3 but lower volume (50K) and fixed-size 22KB blobs
- **Volume:** 50K
- **Hot ops:** Pairwise compare for dedup; index by author/series for "find duplicate editions"
- **Ideal DB:** **Same as #3** — Qdrant collection or pgvector table
- **Embedded pragmatic:** Same as #3
- **What sucks if wrong:** Same as #3 but smaller scale; we get away with it for now

### 5. External ID map (iTunes PIDs → BookID)

- **Shape:** Tiny KV, point lookup only, write-mostly during import
- **Volume:** 97K entries
- **Hot ops:** `GetBookByITunesPID(pid)`, occasional reverse lookup
- **Ideal DB:** **Redis** (or any KV) — gigabytes of RAM, sub-ms lookups
- **Embedded pragmatic:** **A SQLite table with PK on `pid`.** Or keep in Pebble as today. This workload is genuinely served well by any KV.
- **What sucks if wrong:** Nothing — almost every store handles point lookups well

### 6. Dedup candidates (review queue)

- **Shape:** Relational, status-tracked, paginated
- **Volume:** 14K pending today, can spike to 100K+ during scans
- **Hot ops:** `WHERE status = 'pending' AND layer = 'acoustid' ORDER BY created_at LIMIT 50 OFFSET ?`; status transitions on user action
- **Ideal DB:** **Postgres** — exact fit for "queue table" pattern, can use `SELECT ... FOR UPDATE SKIP LOCKED` for worker coordination
- **Embedded pragmatic:** **SQLite** with index on `(status, layer, created_at)`. Or specialised queue: **NATS JetStream** if we ever do async worker fan-out.
- **What sucks if wrong:** What we have now — JSON marshal/unmarshal per candidate, manual page-cursor logic, no native ORDER BY without sorting in app code

### 7. User accounts / sessions / API keys / auth

- **Shape:** Relational + session expiry; high-churn writes for sessions
- **Volume:** ~10 users typical, sessions in low thousands
- **Hot ops:** Bearer token lookup, session refresh, login
- **Ideal DB:**
  - **Accounts** → Postgres
  - **Sessions** → **Redis** with TTL — designed for this exact shape; auto-expire, sub-ms lookup, no compaction needed
  - **API keys** → Postgres
- **Embedded pragmatic:** Accounts + API keys in SQLite. Sessions in **`buntdb`** (TTL is native, in-process, no Redis needed at our scale).
- **What sucks if wrong:** Pebble does sessions OK but never compacts the deleted entries; over time the keyspace bloats

### 8. Activity log (event stream + digests)

- **Shape:** **Append-only time-series.** Per-event records, then daily digests with aggregate tag counts.
- **Volume:** ~10K events/day, ~3650 digests/year per source
- **Hot ops:** `events WHERE timestamp BETWEEN ? AND ?`; digest by day; filter by tag namespace
- **Ideal DB:** **ClickHouse** — columnar TSDB designed for high-volume event ingest + fast range/aggregate queries. Compression ratios of 10-50× on event logs.
- **Honourable mention:** **TimescaleDB** (Postgres extension) — if we already run Postgres, this gives time-bucket primitives and continuous aggregates in the same engine.
- **Embedded pragmatic:** **SQLite with date-prefix indexed table** (or keep NutsDB as-is). 10K events/day is tiny — any engine handles it. The real value of ClickHouse comes at billions of events.
- **What sucks if wrong:** Slow `GROUP BY day` queries, but at our volume this is fine

### 9. Cover art (binary files)

- **Shape:** Immutable binary blobs, content-addressable (SHA-256), large (50KB-5MB)
- **Volume:** ~50K active + history
- **Hot ops:** Read by SHA-256 hash; periodic dedup walk
- **Ideal DB:** **S3 / MinIO** — object storage is literally designed for this. Content addressing, cheap, optimised for blob reads. Tiered storage for archived covers.
- **Embedded pragmatic:** **Filesystem** with hash-prefix sharding (already what we do: `covers/dedup/{hash}.{ext}`). Don't put binaries in a database.
- **What sucks if wrong:** Database row size blowups, slow scans because every row pulls cover bytes into block cache

### 10. Tag snapshots / change history (for revert)

- **Shape:** Versioned writes with full prior state captured
- **Volume:** ~3 per book per write-back, ~100K snapshots total
- **Hot ops:** `WHERE book_id = ? ORDER BY created_at DESC` for timeline; diff between two snapshots
- **Ideal DB:** **Postgres** with JSONB column for the snapshot body and GIN index on tags. Bi-temporal pattern if we want "value at point in time" queries.
- **Embedded pragmatic:** SQLite with JSON column. Or `dolt` if you wanted git-style branching but **that's overkill** (see prior eval).
- **What sucks if wrong:** Snapshot bloat in main book table; what we have today partially does this

### 11. OpenAI batch jobs

- **Shape:** Job queue with state machine (queued → running → fetching → complete → failed). External ID for OpenAI's batch_id.
- **Volume:** Tens to hundreds of pending batches
- **Hot ops:** Poll all `WHERE status = 'running'`; route results by `metadata.tag`
- **Ideal DB:** **NATS JetStream** or **PostgreSQL with LISTEN/NOTIFY** — both are queue-shaped. NATS gives at-least-once delivery semantics; Postgres gives transactional state changes.
- **Embedded pragmatic:** SQLite table with index on `(status, updated_at)`. Polling loop. We're not at the scale where queue semantics matter.
- **What sucks if wrong:** Lost jobs on crash, duplicate work — but our universal batch poller handles this

### 12. Embedding vectors (existing dedup signal)

- **Shape:** Dense float32 vectors, similarity search
- **Volume:** ~50K (one per book)
- **Hot ops:** `find_nearest(vec, k=20)`
- **Ideal DB:** **pgvector** in the main Postgres — single engine, can JOIN against book metadata in one query. Or Qdrant if we already have it for #3.
- **Embedded pragmatic:** **No good answer.** Same problem as #3 — we hand-roll HNSW or use brute force.
- **What sucks if wrong:** Slow nearest-neighbour at scale

### 13. Library aggregates / filter chip counts

- **Shape:** **OLAP.** "How many books WHERE has_cover = false AND author_id = X?"
- **Volume:** 50K rows, ~30 aggregatable columns
- **Hot ops:** COUNT-WHERE with arbitrary predicate combos; 6 quick-filter presets with cached counts
- **Ideal DB:** **DuckDB** (embedded) or **ClickHouse** (server) — both are columnar OLAP engines with bitmap predicate evaluation. DuckDB in-process is the dream for this shape.
- **Honourable mention:** **`kelindar/column`** — pure-Go-ish (SIMD) columnar with bitmap indexes, in-RAM. 90% of DuckDB's aggregate speed without leaving the binary.
- **Embedded pragmatic:** `kelindar/column` or DuckDB-via-CGO
- **What sucks if wrong:** What we have today — full-table scan with predicate evaluation per row. Mitigated by `stats:library` cache + dirty flag, but the cache misses are slow.

### 14. Full-text search (titles, authors, narrators, series, tags)

- **Shape:** Inverted index with BM25/TF-IDF, fuzzy matching, autocomplete
- **Volume:** Index over ~50K books + author/series text
- **Hot ops:** Type-ahead search, "find books matching `frank herbert`"
- **Ideal DB:** **Meilisearch** or **Typesense** — both are purpose-built embedded-search engines with typo tolerance, faceting, sub-100ms ranking, dead-simple to deploy.
- **Honourable mention:** **Elasticsearch / OpenSearch** if we wanted analytics + log search in the same engine.
- **Embedded pragmatic:** **Bleve** (pure Go) or SQLite **FTS5** (built-in to modernc/sqlite). FTS5 is good enough for our scale and saves a separate engine.
- **What sucks if wrong:** Current substring scan returns no relevance ranking, no typo tolerance, no phrase queries

### 15. iTunes XML metadata cache

- **Shape:** Document-ish; iTunes' XML is hierarchical
- **Volume:** 88K tracks, 11.7K albums; reparsed periodically from XML
- **Hot ops:** Lookup track by PID, album by ID
- **Ideal DB:** **MongoDB** is the document-DB instinct — but really this is just KV with structured values. Postgres with JSONB does the same thing without the operational overhead.
- **Embedded pragmatic:** SQLite with JSON column. Or skip the cache and reparse XML (fast enough at this volume).
- **What sucks if wrong:** XML reparse latency on cold start

### 16. Reading progress / playback events

- **Shape:** Time-series writes per (user, book), aggregate "what % through" reads
- **Volume:** Low — handful of users
- **Hot ops:** `UPDATE progress SET position = ? WHERE user_id = ? AND book_id = ?`; resume position lookup
- **Ideal DB:** **Postgres** for progress (relational), **InfluxDB / ClickHouse** for raw playback event stream if we wanted listening analytics
- **Embedded pragmatic:** SQLite, single table
- **What sucks if wrong:** Lost resume positions on crash

### 17. Playlists

- **Shape:** User-owned ordered collections
- **Volume:** Small
- **Hot ops:** "playlists for user X", "books in playlist Y in order"
- **Ideal DB:** Postgres with composite PK `(playlist_id, position)`
- **Embedded pragmatic:** SQLite, same shape
- **What sucks if wrong:** Race conditions when reordering — Pebble's "no atomic compound key updates" hurts

### 18. Configuration / feature flags / runtime tunables

- **Shape:** Tiny KV
- **Volume:** Dozens of entries
- **Hot ops:** Read on every request (cached); occasional admin write
- **Ideal DB:** **etcd** if we ever go distributed (watch semantics for live config); **Consul** alternative
- **Embedded pragmatic:** SQLite key/value table or just env vars + a JSON file. Don't overthink this.
- **What sucks if wrong:** Nothing — this is a trivial workload

### 19. User preferences (themes, sort orders, column toggles)

- **Shape:** Per-user JSON document
- **Volume:** 1 doc per user
- **Hot ops:** Load on login, save on UI change
- **Ideal DB:** Postgres with JSONB
- **Embedded pragmatic:** SQLite JSON column
- **What sucks if wrong:** Nothing — this is trivial

### 20. Maintenance / temp file tracking

- **Shape:** Filesystem-shadow records with TTL
- **Volume:** A few hundred at a time
- **Hot ops:** "what `.bak-*` files are older than 24h"
- **Ideal DB:** Nothing — `find . -name '*.bak-*' -mtime +1` is the right answer.
- **Embedded pragmatic:** Same — filesystem mtime is the source of truth, don't shadow it in a DB
- **What sucks if wrong:** DB-FS divergence

---

## Composite architectures

### Architecture A — "If we had Google's budget" (best-in-class everywhere)

```
┌──────────────────────────────────────────────────────────────────┐
│  PostgreSQL 16 (canonical)                                       │
│    - books, authors, series, narrators, works, book_files        │
│    - external_id_map, dedup_candidates                           │
│    - users, api_keys, playlists, prefs                           │
│    - reading_progress, tag_snapshots                             │
│    - feature flags                                               │
│    Extensions: pgvector (#3, #12), TimescaleDB (#16)             │
├──────────────────────────────────────────────────────────────────┤
│  Qdrant — chromaprint fingerprint vectors (#3, #4)               │
├──────────────────────────────────────────────────────────────────┤
│  ClickHouse — activity log + playback event analytics (#8, #16)  │
├──────────────────────────────────────────────────────────────────┤
│  Meilisearch — full-text title/author/narrator search (#14)      │
├──────────────────────────────────────────────────────────────────┤
│  Redis — sessions + cached aggregates (#7, #13)                  │
├──────────────────────────────────────────────────────────────────┤
│  S3 / MinIO — cover art + .bak archive (#9)                      │
├──────────────────────────────────────────────────────────────────┤
│  NATS JetStream — OpenAI batch queue + worker fan-out (#11)      │
└──────────────────────────────────────────────────────────────────┘
```

**Operational cost:** 7 distinct stateful services. Backup/restore is a coordinated dance. Schema migrations span engines. Local dev environment is docker-compose with 7 containers.

**When this makes sense:** Multi-tenant SaaS at scale, 100K+ users, dedicated ops team.

### Architecture B — "Single-user prosumer app done well" (pragmatic, mostly embedded)

```
┌──────────────────────────────────────────────────────────────────┐
│  modernc.org/sqlite (canonical)                                  │
│    Everything relational: 90% of features above.                 │
│    FTS5 for full-text search (#14)                               │
│    JSON1 for tag snapshots (#10) and prefs (#19)                 │
├──────────────────────────────────────────────────────────────────┤
│  Pebble or Badger sidecar — fingerprint blobs only (#3, #4)      │
│    With hand-rolled LSH bucket index for similarity              │
├──────────────────────────────────────────────────────────────────┤
│  kelindar/column — in-RAM aggregate cache (#13)                  │
│    Repopulated from SQLite at warmup + on dirty                  │
├──────────────────────────────────────────────────────────────────┤
│  Filesystem — cover art, .bak files (#9, #20)                    │
├──────────────────────────────────────────────────────────────────┤
│  In-process queue (channels + cron) — OpenAI batches (#11)       │
│    State persisted in SQLite                                     │
└──────────────────────────────────────────────────────────────────┘
```

**Operational cost:** Single binary deploy. One SQLite file + one Pebble dir + cover art folder. Backup is `tar`.

**When this makes sense:** Right now, for us. Migration is 2-3 months of measured work with shadow-mode validation per layer.

### Architecture C — "Lift-and-shift, smallest possible diff from today" (status quo plus targeted fixes)

```
┌──────────────────────────────────────────────────────────────────┐
│  Pebble (canonical, unchanged)                                   │
│    Everything we have today                                      │
├──────────────────────────────────────────────────────────────────┤
│  Pebble #2 (NEW) — fingerprint blobs only (#3, #4)               │
│    Same engine, second instance, separate write lock             │
├──────────────────────────────────────────────────────────────────┤
│  Bleve sidecar — full-text search (#14) ONLY                     │
├──────────────────────────────────────────────────────────────────┤
│  Filesystem — cover art (#9, unchanged)                          │
└──────────────────────────────────────────────────────────────────┘
```

**Operational cost:** Adds one Pebble instance and one Bleve directory. No engine change.

**When this makes sense:** If we can't justify the SQLite migration cost. The fingerprint sidecar alone unblocks the LSH work, the Bleve sidecar fixes search, everything else stays painful but functional.

---

## Per-feature decision summary

| # | Feature | Ideal | Embedded pragmatic | Cost of wrong |
|---|---|---|---|---|
| 1 | Books/Authors/Series | Postgres | modernc/sqlite | Hand-rolled joins (current) |
| 2 | BookFiles | Postgres | modernc/sqlite | N+1 queries |
| 3 | AcoustID fingerprints | Qdrant | Pebble+LSH sidecar | Infeasible at scale (current) |
| 4 | Book signatures | Qdrant | Pebble+LSH sidecar | O(N²) compares |
| 5 | iTunes PID map | Redis | SQLite/Pebble | Trivial workload |
| 6 | Dedup candidates | Postgres | SQLite | Manual pagination |
| 7 | Sessions/API keys | Redis + Postgres | buntdb + SQLite | Keyspace bloat (current) |
| 8 | Activity log | ClickHouse | NutsDB or SQLite | Slow group-by |
| 9 | Cover art | S3/MinIO | Filesystem | DB bloat if blob-in-row |
| 10 | Tag snapshots | Postgres JSONB | SQLite JSON | Snapshot bloat |
| 11 | OpenAI batch jobs | NATS JetStream | SQLite | Lost jobs on crash |
| 12 | Embedding vectors | pgvector | LSH sidecar | Slow ANN |
| 13 | Library aggregates | DuckDB | kelindar/column | Slow filter chips (current) |
| 14 | Full-text search | Meilisearch | SQLite FTS5 or Bleve | No relevance ranking (current) |
| 15 | iTunes XML cache | Postgres JSONB | SQLite JSON | XML reparse |
| 16 | Reading progress | Postgres | SQLite | Resume position loss |
| 17 | Playlists | Postgres | SQLite | Reorder races |
| 18 | Config / flags | etcd (if distributed) | SQLite or env | Trivial |
| 19 | User prefs | Postgres JSONB | SQLite JSON | Trivial |
| 20 | Maintenance temp files | Filesystem | Filesystem | DB-FS divergence |

---

## Recommendation

**Adopt Architecture B incrementally.** Start with:

1. **PR 1:** Bleve sidecar for full-text search (#14). Pure additive, no migration.
2. **PR 2:** Pebble fingerprint sidecar (#3, #4) + hand-rolled LSH bucket index. Unblocks the deferred dedup-fuzzy work.
3. **PR 3:** kelindar/column aggregate cache (#13). Replaces stats:library scan, populated from Pebble.
4. **PR 4 (the big one):** Begin modernc/sqlite migration. Start with the leaf tables (sessions, prefs, playlists), validate behaviour, work inward to the book/author/series core. Shadow mode for one release per table.
5. **PR 5:** Cut over remaining Pebble book/author/series tables to SQLite. Pebble retained only for fingerprints + activity log.

If PR 4 looks too expensive, **Architecture C is the fallback** — just the fingerprint sidecar and Bleve, accept that the join-by-hand pain stays.

**Don't** try to do this all at once. Each layer is reversible if validated independently.
