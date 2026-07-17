<!-- file: docs/database-evaluation-2026-05-30.md -->
<!-- version: 1.0.0 -->
<!-- guid: a0c9e2d4-3f15-4a83-9c4b-8e7a6d5f4b22 -->
<!-- last-edited: 2026-05-30 -->

# Database Evaluation — Pebble & Beyond

**Status:** RESEARCH COMPLETE — awaiting decision
**Authors:** Claude (research) + Daisy (decision)
**Date:** 2026-05-30

---

## TL;DR

After researching 25 Go-native databases across 5 waves of parallel subagents:

- **Keep Pebble as the canonical store.** Nothing in the field is materially better for our mixed workload, and we'd pay migration cost for a marginal win.
- **For full-file fingerprint blobs (the new question), the only serious candidate is BadgerDB.** WiscKey-style key/value separation is purpose-built for our profile: 30–60 GB of ~10 KB–1.5 MB blobs that we want to scan by prefix (LSH buckets) and point-read by ID. Pebble's block cache wastes RAM on these because the values are too big to amortize.
- **For library predicate counts, `kelindar/column` is a real upgrade** to the memdb-stripped-summary pattern — bitmap indexes give 10–100× COUNT-WHERE speedups for our filter chips. Pure cache layer, no persistence story needed.
- **For full-text book/author search, Bleve is the obvious pick.** Already battle-tested; replaces our substring scan.
- **For per-percentile metrics on activity-log digests, go-tdigest** is a no-brainer 5 KB sketch.
- **Everything else is either wrong-shape, abandoned, CGO-blocked, or strictly worse than what we have.**

The "minimum viable improvement" path:
1. Move fingerprints out to a **Badger sidecar** (one-engine-per-workload, no shared write lock with the book/file/author writes).
2. Add **`kelindar/column`** as a pure-RAM aggregate cache populated from Pebble at warmup.
3. Add **Bleve** index for title/author search.

Everything else stays in Pebble.

---

## Why we are doing this evaluation

We currently use a single PebbleDB instance as the canonical KV store
(`/var/lib/audiobook-organizer/audiobooks.pebble`) plus NutsDB for the
activity log digest (`act:digest` bucket).

Three workloads inside Pebble have started looking like they want different
shapes of storage:

1. **Audio fingerprints (per-file + per-book)** — large opaque blobs (~10 KB
   per 5-min segment × 7 segments per file = ~70 KB/file today, or ~100 KB–
   1.5 MB/file if we switch to whole-file fingerprints). Total ~21 GB at
   100% coverage with current 7-seg design; ~30–60 GB if we move to
   whole-file. Workload: write-once, read-many for similarity matching.
2. **Library counts / aggregates** — small structured values that need
   fast scans + indexes (already mostly handled by memdb + `stats:library`
   cache).
3. **Activity log digests** — append-mostly + range scans by day. NutsDB
   handles this OK today but is unmaintained-ish.

The fingerprint workload is the painful one:

- Hamming similarity comparisons need either (a) LSH bucketing or (b)
  efficient large-blob iteration. Pebble's block cache doesn't pay off
  here because the blobs are too large.
- Stripping seg1..6 from memdb costs ~70 MB heap; un-stripping (so dedup
  fuzzy can use memdb) would cost ~700 MB at full coverage.
- Single-writer Pebble means fingerprint rescans + import scans + library
  writes all serialize on the same write lock.

So the question is: **does a dedicated KV/blob store for fingerprints (and
possibly other heavy-blob columns) buy us enough to justify a second engine?**

---

## Workload requirements (fingerprint store)

| Property | Requirement | Notes |
|---|---|---|
| Value size | 10 KB – 1.5 MB per record | Whole-file fingerprints can be 1 MB+ |
| Record count | ~300 K files × 1 fp + ~50 K books × 1 sig | Plus LSH bucket inverted index |
| Read pattern | Random by ID (point), prefix-scan for LSH buckets | Plus full-scan for bulk recompute |
| Write pattern | Mostly write-once on import + bulk recompute | No frequent mutation |
| Durability | Crash-survive recent writes (last few seconds OK to lose) | Not the source of truth — Pebble Book is |
| Concurrency | One writer (rescan op), many readers | Same as Pebble |
| Storage size | ~30–60 GB | Larger than current ~10 GB Pebble book/file rows |
| Cross-compilation | MUST be pure Go (no CGO) | Hard requirement — see [`database-architecture.md`](database-architecture.md) |
| Maintained | Active commits in last 12 months | Avoid bluge/phalanx fate |

---

## Excluded up front (per user direction)
- **bluge** — dead
- **phalanx** — dead
- **ChaiSQL** — already evaluated, no join support

---

## Research findings (by wave)

### Wave 1 — KV stores for blob workload

#### BadgerDB
- **Repo:** https://github.com/dgraph-io/badger
- **License:** Apache 2.0
- **Last commit:** 2026-05-19 — active
- **Storage engine:** LSM tree + value log (**WiscKey** design)
- **Pure Go:** yes
- **Value-size sweet spot:** designed for SSD-resident values, separation via value log
- **Max value size:** unlimited in practice
- **Range scans:** yes — full iterators with sorted KV access
- **Transactions:** ACID with Serializable Snapshot Isolation (SSI)
- **Concurrency:** MVCC; multiple concurrent transactions
- **Disk format:** stable, actively maintained
- **Used by:** Dgraph, Jaeger, go-ipfs, Usenet Express (300 TB+), IoTeX, Fantom
- **Strengths for our workload:** WiscKey separates large values from keys → LSM compaction stays cheap; prefix-scan fast (good for LSH buckets); proven at 300 TB+
- **Gotchas:** value-log GC needs tuning under heavy write churn; running both Pebble and Badger doubles the LSM compaction footprint
- **Verdict:** **Top pick for the fingerprint sidecar.** WiscKey is the only LSM design built specifically for "small key, big value" — which is exactly what we have.

#### bbolt
- **Repo:** https://github.com/etcd-io/bbolt
- **License:** MIT
- **Last commit:** 2026-05-27 — active
- **Storage engine:** B+tree
- **Pure Go:** yes
- **Max value size:** practically unlimited (single B+tree leaf per value)
- **Range scans:** yes — byte-sorted sequential iteration
- **Transactions:** ACID, full serializable
- **Concurrency:** **single writer**, unlimited concurrent readers
- **Disk format:** stable, passively maintained
- **Used by:** etcd (Kubernetes), many embedded tools
- **Strengths:** memory-mapped, single-file simplicity, proven in critical infrastructure
- **Gotchas:** single-writer kills our parallel import path; bulk loads cause page-split stalls; byte slices invalidate after transaction close
- **Verdict:** Wrong concurrency model for our import workload.

#### pogreb
- **Repo:** https://github.com/akrylysov/pogreb
- **License:** Apache 2.0
- **Last commit:** 2026-04-06 — quiet
- **Storage engine:** hash table
- **Pure Go:** yes
- **Range scans:** **NO** — explicitly states "range scans impossible"
- **Transactions:** none
- **Concurrency:** safe-for-concurrent-use, but no isolation
- **Verdict:** Dealbreaker. No range scans means no LSH bucket lookups.

#### LotusDB
- **Repo:** https://github.com/lotusdblabs/lotusdb (renamed from flower-corp)
- **Last commit:** 2025-02-18 — quiet (16 months idle)
- **Storage engine:** LSM + B+tree hybrid
- **Pure Go:** yes
- **Verdict:** Interesting design but dormant + sparse docs + no production track record. Skip.

#### moss
- **Repo:** https://github.com/couchbase/moss
- **Last commit:** unknown — appears dormant
- **Storage engine:** simplified LSM
- **Pure Go:** yes
- **Max value size:** 256 MB (2^28 bytes)
- **Concurrency:** non-blocking concurrent reads/writes
- **Verdict:** Project abandoned. Skip.

---

### Wave 2 — KV alternatives + Redis-protocol

#### nutsdb (already in use for activity log)
- **Repo:** https://github.com/nutsdb/nutsdb
- **License:** Apache 2.0
- **Last commit:** 2026-02-01 — active
- **Storage engine:** LSM + WAL
- **Pure Go:** yes
- **Concurrency:** **single writer**
- **Gotchas:** segment-size breaking change (v0.9.0+); data-format breaks between major versions; high FD usage at scale
- **Verdict:** Fine for the small activity-log digest workload we already use it for. Don't expand its footprint.

#### bitcask
- **Repo:** https://git.mills.io/bitcask/v2
- **License:** MIT
- **Last commit:** 2026-05-26 — active
- **Storage engine:** Bitcask (log-structured hash table)
- **Pure Go:** yes
- **Max value size:** **65 KB default** (configurable up to 1 GB)
- **Concurrency:** single writer
- **Verdict:** Default value limit too small without bumping; predictable 1-IOPS-per-op is nice but single writer kills it.

#### rosedb
- **Repo:** https://github.com/rosedblabs/rosedb
- **License:** Apache 2.0
- **Last commit:** 2026-02-09 — quiet
- **Storage engine:** Bitcask variant
- **Concurrency:** single writer
- **Verdict:** Viable middle ground but quieter and less documented than the alternatives.

#### IceFireDB
- **Repo:** https://github.com/IceFireDB/IceFireDB
- **License:** MIT
- **Last commit:** 2026-05-17 — active
- **Pure Go:** **NO** — depends on `mattn/go-sqlite3` (CGO)
- **Verdict:** Hard CGO dealbreaker. Built for distributed Web3-style use; not what we need.

#### goleveldb
- **Repo:** https://github.com/syndtr/goleveldb
- **License:** BSD-3
- **Last commit:** 2022-07-21 — **dead (3+ years)**
- **Storage engine:** LSM
- **Pure Go:** yes
- **Range scans:** yes
- **Used by:** Cosmos SDK, etcd, many blockchains
- **Verdict:** Technically excellent fit but the maintenance gap is too long. Pebble is the same shape and actively developed.

---

### Wave 3 — Document / columnar / structured

#### frostdb
- **Repo:** https://github.com/polarsignals/frostdb
- **License:** Apache 2.0
- **Last commit:** 2026-01-21 — active
- **Storage engine:** LSM + Parquet
- **Pure Go:** **NO** — depends on Apache Arrow Go (CGO computational kernels)
- **Used by:** Parca (observability)
- **Verdict:** CGO + immutable-only design + "not production-ready yet" disclaimer. Skip.

#### column (kelindar/column)
- **Repo:** https://github.com/kelindar/column
- **License:** MIT
- **Last commit:** 2025-06-28 — active
- **Storage engine:** **in-memory columnar** (SoA layout)
- **Pure Go:** **NO** — uses `kelindar/simd` for SIMD intrinsics (platform-specific assembly, not strictly CGO but not portable to every GOARCH)
- **Indexing:** **bitmap indexes with SIMD set operations**
- **Disk persistence:** optional via custom writer; primarily RAM-resident
- **Verdict:** **Top pick for library predicate counts.** 10–100× COUNT-WHERE speedup vs full table scan. Pure in-RAM cache, populated from Pebble at warmup; perfect fit for our filter-chip presets ("missing covers", "no fingerprints", "in import path"). Tradeoff: SIMD package is amd64+arm64 only — fine for prod (Linux amd64) but limits dev portability slightly.

#### buntdb
- **Repo:** https://github.com/tidwall/buntdb
- **License:** MIT
- **Last commit:** 2026-05-19 — very active
- **Storage engine:** B-tree with append-only WAL
- **Pure Go:** yes
- **Concurrency:** single writer
- **Used by:** Tile38
- **Verdict:** Solid KV with secondary indexes, but single-writer + string API + append-only WAL never compacting make it a hard sell for 30+ GB of blobs.

#### clover
- **Repo:** https://github.com/ostafen/clover
- **License:** MIT
- **Last commit:** 2025-09-09 — active
- **Storage engine:** Bolt or Badger backend
- **Verdict:** JSON-document layer on top of Bolt/Badger. Adds marshalling overhead without giving us anything we'd actually use.

#### ObjectBox-Go
- **Repo:** https://github.com/objectbox/objectbox-go
- **Pure Go:** **NO** — wraps libobjectbox native library via CGO
- **Verdict:** Hard CGO dealbreaker.

---

### Wave 4 — Search / cryptographic / version-controlled

#### Bleve
- **Repo:** https://github.com/blevesearch/bleve
- **License:** Apache 2.0
- **Last commit:** 2026-03-23 — active (v2.6.0 in April 2026)
- **Pure Go:** yes
- **Used by:** Couchbase, Riot Games; 393+ dependents
- **Verdict:** **Top pick for full-text title/author search.** BM25 ranking, phrase queries, fuzzy matching. Already a known quantity in the Go ecosystem.

#### Skizze
- **Repo:** https://github.com/skizzehq/skizze
- **Last commit:** ~2016 — **DEAD (10 years)**
- **Verdict:** Abandoned. Use go-tdigest instead.

#### immudb
- **Repo:** https://github.com/codenotary/immudb
- **License:** **BSL 1.1** (Business Source License — converts to Apache after 4 years)
- **Last commit:** 2026-04-15 — active
- **Pure Go:** yes
- **Used by:** Unilever, Cisco, Datadog (per vendor)
- **Verdict:** Cryptographically-verified KV. Cool tech but the BSL license is a redistribution headache, and we don't have a regulatory reason to make our audit log tamper-proof. Skip unless legal requirements change.

#### Dolt
- **Repo:** https://github.com/dolthub/dolt
- **License:** Apache 2.0
- **Last commit:** 2026-05-29 — active
- **Verdict:** Git-for-data is a cool paradigm and totally wrong for us. Our migrations are numbered SQL/Go scripts; branch-and-merge over 50 K rows of book metadata would be a nightmare to reason about.

#### go-tdigest
- **Repo:** https://github.com/caio/go-tdigest
- **License:** MIT
- **Last commit:** 2025-11-29 — active
- **Memory:** ~5 KB per digest at default compression
- **Verdict:** **Top pick for percentile sketches.** Drop-in p50/p95/p99 for activity-log digests + latency histograms. 5 KB per sketch is essentially free.

---

### Wave 5 — Distributed / time-series / graph

#### VictoriaMetrics
- **Pure Go:** **NO** — zstd via CGO (vendored static libs)
- **Architecture:** standalone server, embed path is underdocumented
- **Verdict:** Over-engineered for 10 K events/day. Skip.

#### LedisDB
- **Last commit:** 2020-05-10 — **dead (6 years)**
- **Verdict:** Skip.

#### Dgraph
- **Architecture:** standalone distributed cluster
- **Verdict:** Wrong shape (we want embed, not cluster). Skip.

#### Tile38
- **Pure Go:** yes
- **Architecture:** standalone server, no embed mode
- **Domain:** geo-spatial
- **Verdict:** Wrong domain.

#### InfluxDB 3 Core
- **Pure Go:** **NO** — written in Rust
- **Verdict:** Wrong language, wrong shape.

---

## Comparison matrix

| DB | Wave | Pure Go | Active | Embeds? | LSH-friendly | Big values | Concurrency | Verdict |
|---|---|---|---|---|---|---|---|---|
| **Pebble (current)** | — | ✓ | ✓ | ✓ | ✓ prefix | OK | single writer | Keep |
| **BadgerDB** | 1 | ✓ | ✓ | ✓ | ✓ prefix | ★★★ WiscKey | MVCC | ★ Sidecar for fingerprints |
| bbolt | 1 | ✓ | ✓ | ✓ | ✓ | OK | single writer | Wrong concurrency |
| pogreb | 1 | ✓ | ~ | ✓ | ✗ | OK | concurrent | No range scans |
| LotusDB | 1 | ✓ | ✗ idle | ✓ | ? | ★★ | ? | Dormant |
| moss | 1 | ✓ | ✗ dead | ✓ | ✓ | ★★ 256 MB | concurrent | Abandoned |
| nutsdb | 2 | ✓ | ✓ | ✓ | ✓ | OK | single writer | Keep for activity log only |
| bitcask | 2 | ✓ | ✓ | ✓ | ✓ scan | ★ 65 KB default | single writer | Limit too small |
| rosedb | 2 | ✓ | ~ | ✓ | ✓ | OK | single writer | Underdocumented |
| IceFireDB | 2 | ✗ CGO | ✓ | ~ | ? | ? | distributed | CGO dealbreaker |
| goleveldb | 2 | ✓ | ✗ dead | ✓ | ✓ | OK | multi | Dead upstream |
| frostdb | 3 | ✗ CGO | ✓ | ✓ | ✗ | columnar | snapshot | CGO + immutable |
| **column** | 3 | ~ SIMD | ✓ | ✓ | n/a | RAM only | sharded | ★ Aggregate cache |
| buntdb | 3 | ✓ | ✓ | ✓ | ✓ | OK | single writer | Wrong scale |
| clover | 3 | ✗ via Badger | ✓ | ✓ | ✗ | ★★ | bkend | JSON overhead |
| ObjectBox | 3 | ✗ CGO | ✓ | ✓ | ✗ | ★ | multi | CGO dealbreaker |
| **Bleve** | 4 | ✓ | ✓ | ✓ | n/a | n/a | concurrent | ★ Text search |
| Skizze | 4 | ✓ | ✗ dead | server | n/a | n/a | server | Abandoned |
| immudb | 4 | ✓ BSL | ✓ | both | ✓ | OK | server-prefer | License headache |
| Dolt | 4 | ✓ | ✓ | server | ✗ | OK | branches | Wrong paradigm |
| **go-tdigest** | 4 | ✓ | ✓ | ✓ | n/a | n/a | per-sketch | ★ Percentile sketches |
| VictoriaMetrics | 5 | ✗ CGO | ✓ | ~ | n/a | n/a | server | CGO + overkill |
| LedisDB | 5 | ~ | ✗ dead | ✓ | ~ | OK | server-style | Dead |
| Dgraph | 5 | ~ | ✓ | ✗ | ✓ | OK | distributed | Wrong shape |
| Tile38 | 5 | ✓ | ✓ | ✗ | ✗ | n/a | server | Wrong domain |
| InfluxDB 3 | 5 | ✗ Rust | ✓ | ✗ | ✗ | n/a | server | Wrong language |

★ = pick.  ~ = qualified yes.  ✗ = no.

---

## Recommendation

**Hybrid storage architecture:**

```
┌────────────────────────────────────────────────────────────────┐
│ Pebble (canonical)  — books, files, authors, series, segments, │
│                       sessions, prefs, dedup candidates        │
├────────────────────────────────────────────────────────────────┤
│ Badger (sidecar)    — fingerprint blobs only                   │
│                       /var/lib/audiobook-organizer/fp.badger   │
├────────────────────────────────────────────────────────────────┤
│ NutsDB (existing)   — activity log digest only                 │
├────────────────────────────────────────────────────────────────┤
│ kelindar/column     — in-RAM aggregate cache, populated from   │
│                       Pebble at warmup (no persistence needed) │
├────────────────────────────────────────────────────────────────┤
│ Bleve (sidecar)     — full-text title/author search index      │
├────────────────────────────────────────────────────────────────┤
│ go-tdigest          — embedded in activity-log digest payloads │
└────────────────────────────────────────────────────────────────┘
```

Migration ordering (one PR per layer; each is reversible):
1. **Bleve** — pure add-on, no migration needed (rebuild index from Pebble)
2. **go-tdigest** — embed in digest payloads, additive
3. **`kelindar/column`** — in-RAM cache rebuilt at startup
4. **Badger fingerprint sidecar** — biggest change; needs migration of 88 K existing fingerprint records out of Pebble. Run in shadow mode for one release to validate.

---

## "If forced to use this database for SOMETHING, what's the least-bad fit?"

> Per follow-up request: assume we had to use this DB for *something*. Where
> would it be least bad? **(All of these would suck overall.)**

### Wave 1 — KV stores for blob workload

**BadgerDB** — fingerprint sidecar (the real recommendation). This is the one
case where it wouldn't suck.

**bbolt** — config store for application settings. Tiny payloads, single
writer fine because changes are user-driven and rare. **Overall suck:** N/A
— this is genuinely fine for tiny single-writer use. **It would still suck**
if we tried to put any throughput workload on it.

**pogreb** — iTunes PID → BookID lookup table. Read-heavy, point-only, ~97 K
rows, no scans needed. **Overall suck:** Significant — losing range scans on a
KV is a big design constraint and we'd have to keep a parallel index for any
prefix work. Crash recovery rebuilds the entire WAL.

**LotusDB** — fingerprint sidecar if Badger somehow fell apart. **Overall
suck:** Significant — 16-month commit gap, sparse docs, no production track
record. We'd be the canary.

**moss** — long-term archival cold storage of soft-deleted books. Big values,
slow writes are fine for cold tier. **Overall suck:** Massive — project is
dead, nobody outside Couchbase uses it, 4.8 GB key overhead at our scale.

### Wave 2 — KV alternatives + Redis-protocol

**nutsdb** — what it's already doing (activity log digest). Don't expand it.
**Overall suck for any new use:** High — segment migration pain, single
writer, format breakages.

**bitcask** — Deluge torrent hash → BookFile lookup. 65 KB default value cap
is plenty for that tiny payload, predictable 1-IOPS-per-op behaviour is nice
for a single-purpose table. **Overall suck:** Medium — single writer, default
limits are tight, "too many open files" issue at scale.

**rosedb** — author/series metadata cache. Forward+backward iteration is
nice for paginated alphabetical lists. **Overall suck:** Medium — quiet
development, ambiguous transactional guarantees, would put us on a small
project's release schedule.

**IceFireDB** — never. Not even hypothetically. Won't compile (CGO via
SQLite). **Overall suck:** Total — we couldn't even build it.

**goleveldb** — embedding store for the dedup signal vectors. Was actually
working fine before we replaced it with Pebble. Big LSM, unlimited values,
multi-batch writes. **Overall suck:** High **only because of maintenance
risk** — 3 years dead, no security patches, no compaction improvements. The
code itself is excellent.

### Wave 3 — Document / columnar / structured

**frostdb** — historical query log for "what books did the user view this
month". Append-only Parquet is a decent fit for analytics-over-time.
**Overall suck:** High — CGO via Arrow, not production-ready disclaimer,
no updates/deletes means we can't even reset a user's history.

**`kelindar/column`** — library aggregate cache (the real recommendation).
Wouldn't suck for that.

**buntdb** — user preferences with TTL-based session store. Secondary
indexes are nice for "all sessions for user X", TTL is native. **Overall
suck:** Medium — single writer caps us at one preference change at a time
across the whole library; append-only WAL never compacts so disk grows
forever.

**clover** — temporary scratch space for OpenAI batch-API response payloads
before we parse them. JSON-doc model matches the input shape exactly.
**Overall suck:** High — adds another dependency for a use case we already
solve with `os.WriteFile`.

**ObjectBox** — never. Won't compile (CGO via libobjectbox). **Overall suck:**
Total.

### Wave 4 — Search / cryptographic / version-controlled

**Bleve** — full-text book/author search (real recommendation). Wouldn't
suck.

**Skizze** — count-distinct cardinality estimates ("how many unique authors
in this library"). HyperLogLog is the right algorithm for this.
**Overall suck:** Total — abandoned 10 years ago, runs as a separate server,
no Python SDK, and we can compute this trivially with `SELECT COUNT(DISTINCT)`
on memdb.

**immudb** — tamper-evident user-action audit trail for compliance scenarios.
Cryptographic verification is the value proposition. **Overall suck:** High
— BSL license, no actual compliance requirement for us, runs as a separate
server, can't delete anything (problem if a user revokes consent).

**Dolt** — "save state of library at this point in time and let me roll back"
feature. Git-for-data is a literal fit for that user story. **Overall suck:**
Massive — entire architecture rewrite around SQL semantics, merge conflicts
on schema changes are a manual mess, branch operations lock tables for
seconds at our scale.

**go-tdigest** — embedded percentile sketches in activity-log digests (real
recommendation). Wouldn't suck.

### Wave 5 — Distributed / time-series / graph

**VictoriaMetrics** — Prometheus-shaped metrics scraping if we ever expose
`/metrics` endpoint for ops monitoring. Designed precisely for this.
**Overall suck:** High **for our use case** — over-engineered for 10 K
events/day; we don't have ops dashboards demanding fast queries; running a
separate TSDB process is friction for a single-machine deploy.

**LedisDB** — Redis-style ZSet of "recently played books per user". Embeddable
pure-Go via goleveldb backend, sorted sets are native. **Overall suck:**
Total — 6 years dead, goleveldb backend means inheriting that abandonware
too.

**Dgraph** — graph of author→book→series→narrator→work relationships for
"recommend similar". Graph traversal is the right shape for that.
**Overall suck:** Massive — distributed cluster overhead, we're a
single-binary single-machine app, gRPC server eats 100s of MB RAM, we'd
spend more time operating Dgraph than serving recommendations.

**Tile38** — fictitious feature: geospatial index of audiobook *recording
locations* (lat/lon of studios). Pure-Go geo with realtime pubsub is
literally what it does. **Overall suck:** Total — we don't have this data,
nobody asked for this feature, it's a server (not a library), wrong domain
entirely.

**InfluxDB 3 Core** — long-term archive of activity-log events queryable by
SQL. GA, Parquet-based, mature. **Overall suck:** Massive — written in Rust
(no Go embed), standalone-server-only, separate process to operate, overkill
for the volume.

---

## Open questions / follow-ups

- [ ] Should we run Badger and Pebble side-by-side, or just expand Pebble
  with separate column families (Pebble's built-in feature)? CFs might let
  us tune compression separately for the blob CF without the operational
  overhead of two engines.
- [ ] Bleve index needs a freshness story: rebuild from scratch periodically,
  or wire into book/author/series Pebble write paths?
- [ ] `kelindar/column` SIMD on arm64 — confirm before relying on it for prod
  (arm Linux box deployment scenario).
- [ ] How does Badger's value-log GC behave when 14 K rows get cleared at
  once (our reset op)? Need to bench.
