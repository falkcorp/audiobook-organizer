# SQL Database Backend Analysis for Audiobook Organizer

**Date:** 2026-03-22
**Scope:** Non-Oracle SQL/relational databases as potential backends
**Current state:** PebbleDB (default KV store) + SQLite3 (opt-in), ~270-method Store interface, 37 migrations, 4,310-line SQLite implementation

---

## Workload Profile Summary

| Dimension | Value |
|-----------|-------|
| Books | ~11K |
| Authors | ~3K |
| Series | ~8.5K |
| External ID mappings | ~97K |
| Total tables | 20+ (books, authors, series, works, narrators, book_authors, book_narrators, book_tags, book_segments, external_id_map, metadata_states, metadata_changes_history, operation_changes, operations, operation_logs, operation_summary_logs, system_activity_log, playlists, sessions, users, etc.) |
| Read/write ratio | ~90/10 (browsing-heavy, bursty writes) |
| Concurrent users | 1-5 (household) |
| Deployment | Self-hosted Linux server, single node |
| FTS requirement | Title, author, narrator search via SQLite FTS5 |
| Key-value patterns | User preferences, scan cache, operation state |
| Complex queries | 10+ table joins, dashboard aggregations, duplicate detection |
| Go driver | Must support `database/sql` interface |

### Migration Complexity Baseline

The 37 existing migrations use pure SQLite SQL: `ALTER TABLE ADD COLUMN`, `CREATE TABLE`, `CREATE INDEX`, `CREATE VIRTUAL TABLE USING fts5`, partial indexes (`WHERE` clause), `INSERT OR IGNORE`, `ON CONFLICT DO NOTHING`, `COALESCE` in index expressions, triggers for FTS sync, and `PRAGMA table_info`. The migrations also include Go-level data transformation (string splitting, backfill loops). Any new database must handle these patterns or require rewriting.

---

## 1. PostgreSQL

### Overview
The gold standard open-source RDBMS. First released 1996, currently v17.x. License: PostgreSQL License (permissive, BSD-like). Battle-tested at every scale from embedded single-user to multi-TB enterprise. The most feature-complete open-source SQL database.

### Go Driver Quality
- **pgx** (`github.com/jackc/pgx/v5`): Pure Go, best-in-class performance, `database/sql` compatible via `pgx/stdlib`. Also has a native interface that bypasses `database/sql` overhead.
- **lib/pq** (`github.com/lib/pq`): Legacy but stable, `database/sql` compatible. In maintenance mode; pgx recommended.
- ORM support: Excellent. GORM, sqlx, sqlc, Ent, Bun all have first-class PostgreSQL support.

### Embeddability
**Cannot run in-process.** Requires a separate server process. However, the PostgreSQL binary is available as a single Docker container, and tools like `embedded-postgres-go` (`github.com/fergusstrange/embedded-postgres`) can download and manage a PostgreSQL instance for testing. For production self-hosting, it is always a separate daemon.

### SQL Compatibility
PostgreSQL SQL is a superset of standard SQL and differs from SQLite in several ways:
- **Type system**: Strict types. SQLite's type affinity ("any column can hold any type") does not apply. All `TEXT`, `INTEGER`, `BOOLEAN`, `DATETIME` columns map cleanly, but `BOOLEAN` is native (not `0/1` integer).
- **`ALTER TABLE ADD COLUMN`**: Supported, nearly identical syntax.
- **`INSERT OR IGNORE`**: Must become `INSERT ... ON CONFLICT DO NOTHING`.
- **`AUTOINCREMENT`**: Becomes `SERIAL` or `GENERATED ALWAYS AS IDENTITY`.
- **`datetime('now')`**: Becomes `NOW()` or `CURRENT_TIMESTAMP` (latter works in both).
- **`PRAGMA`**: Not supported. Schema introspection uses `information_schema` or `pg_catalog`.
- **FTS5**: Not applicable. PostgreSQL has its own `tsvector`/`tsquery` full-text search (see below).
- **Partial indexes**: Fully supported, same syntax.
- **`lower(hex(randomblob(16)))`**: Must become `gen_random_uuid()` or equivalent.

**Migration effort: Medium.** Most DDL translates directly. The 37 migrations need systematic rewriting for syntax differences (INSERT OR IGNORE, AUTOINCREMENT, FTS), but the logic stays the same. Estimate: 2-3 days of focused work.

### Full-Text Search
PostgreSQL's built-in FTS (`tsvector`/`tsquery`) is significantly more powerful than SQLite FTS5:
- Language-aware stemming (English, plus 30+ languages)
- Configurable dictionaries and stop words
- Ranking functions (`ts_rank`, `ts_rank_cd`)
- `GIN` and `GiST` index types for FTS
- Phrase search, prefix matching, boolean operators
- Trigram similarity (`pg_trgm` extension) for fuzzy matching

For an audiobook organizer searching titles/authors/narrators, PostgreSQL FTS is substantially better than FTS5. The `pg_trgm` extension is particularly valuable for handling misspellings in author names.

### Connection Model
Client-server over TCP/Unix sockets. Connection pooling via PgBouncer or built-in pgx pool. The pgx driver has excellent built-in connection pooling (`pgxpool`). For 1-5 users, a pool of 5-10 connections is more than sufficient.

### Self-Hosting Complexity
- **Docker**: `docker run postgres:17` -- trivial.
- **System package**: Available in every Linux distribution's package manager.
- **Config**: `postgresql.conf` and `pg_hba.conf`. For a small single-user app, defaults work. Tuning for 11K rows is unnecessary.
- **Backup**: `pg_dump` for logical, `pg_basebackup` for physical. Mature tooling.
- **Overhead**: ~30-50MB RAM idle. Lightweight for a server.

### Performance at Our Scale
Massively overkill for 11K books. PostgreSQL handles billions of rows; at 10K-100K it will return any query in single-digit milliseconds. The 10+ table joins with aggregations that might take 50ms in SQLite would take <5ms in PostgreSQL. Connection overhead is the only downside vs embedded SQLite.

### Replication/HA
- Streaming replication (built-in): synchronous or async
- Logical replication (built-in): table-level, cross-version
- Patroni, pg_auto_failover: automated HA
- pgBackRest: enterprise backup/restore
- Completely unnecessary for this workload, but available if the app grows.

### Migration from SQLite
- Rewrite 37 migrations for PostgreSQL syntax (2-3 days)
- Rewrite ~4,300 lines of SQLite store implementation
- Replace FTS5 virtual table + triggers with `tsvector` column + GIN index
- Replace `INSERT OR IGNORE` with `ON CONFLICT DO NOTHING`
- Replace `AUTOINCREMENT` with `SERIAL`/`BIGSERIAL`
- Replace `PRAGMA` introspection with `information_schema` queries
- Replace `randomblob()` with `gen_random_uuid()`
- Handle `BOOLEAN` as native type instead of integer

### Pros
1. **Richest SQL feature set**: Window functions, CTEs, JSONB, full-text search, partial indexes, generated columns -- everything you will ever need
2. **Best Go driver ecosystem**: pgx is the gold standard for Go database drivers
3. **Future-proof**: If the app ever grows beyond a household, PostgreSQL scales to enterprise

### Cons
1. **Not embeddable**: Requires a separate server process, increasing deployment complexity
2. **Overkill**: A nuclear reactor to power a flashlight at 11K rows
3. **Migration effort**: Every one of the 37 migrations needs syntax adjustment

### Verdict: **Yes** -- worth implementing
PostgreSQL is the obvious choice if you want a "real" database backend. The migration effort is moderate and the result is a rock-solid foundation. The main question is whether the deployment complexity (separate process) is acceptable for a self-hosted app. If the app already runs in Docker, adding a PostgreSQL container is trivial.

---

## 2. CockroachDB

### Overview
Distributed SQL database with PostgreSQL-compatible wire protocol. Created by ex-Google engineers (inspired by Spanner). First stable release 2017. License: Business Source License (BSL) 1.1 -- free for single-node and small clusters, but not for offering as a managed service. Converts to Apache 2.0 after 3 years.

### Go Driver Quality
- Uses the same **pgx** driver as PostgreSQL (wire-compatible).
- Some PostgreSQL features not supported (see SQL compatibility).
- ORM support: Same as PostgreSQL via GORM, sqlx, etc.

### Embeddability
**Cannot run in-process.** Requires a separate server process. CockroachDB is a large binary (~200MB) designed for distributed deployments. Even single-node mode runs as a daemon.

### SQL Compatibility
CockroachDB supports most PostgreSQL SQL but has notable gaps:
- **No triggers**: The FTS5 sync triggers would need to be handled application-side.
- **No `CREATE VIRTUAL TABLE`**: No FTS5 equivalent built-in.
- **No stored procedures** (limited support added recently).
- **Serial columns**: Uses `SERIAL` but with UUID-based sequences, not sequential integers.
- **Foreign keys**: Supported but with different performance characteristics (distributed checks).
- **`INSERT ... ON CONFLICT DO NOTHING`**: Supported (PostgreSQL syntax).
- **Partial indexes**: Supported.

### Full-Text Search
CockroachDB has **no built-in full-text search** as of 2026. You would need to:
- Implement application-level search with `ILIKE` (slow at scale)
- Use trigram indexes (`pg_trgm` is partially supported)
- Add an external search service (Meilisearch, Typesense)

This is a significant gap for an app that needs title/author/narrator search.

### Connection Model
Client-server over PostgreSQL wire protocol. Same pgx pooling applies.

### Self-Hosting Complexity
- **Docker**: Available but the image is large (~200MB compressed).
- **Single binary**: Yes, `cockroach start-single-node` for non-distributed mode.
- **Config**: More complex than PostgreSQL. Needs certificate setup for production (or `--insecure` flag).
- **Resource usage**: Minimum 2GB RAM recommended. Heavy for a small audiobook app.
- **Backup**: Built-in `BACKUP`/`RESTORE` commands.

### Performance at Our Scale
CockroachDB adds latency overhead for distributed consensus (Raft) even in single-node mode. At 11K rows, queries will be 2-5x slower than PostgreSQL for point lookups due to the distributed architecture overhead. Complex joins are particularly affected. This database is designed for horizontal scaling, not single-node performance.

### Replication/HA
This is CockroachDB's entire raison d'etre. Automatic multi-region replication, Raft consensus, zero-downtime upgrades. Completely unnecessary for a household audiobook app, and the overhead is a net negative.

### Migration from SQLite
Same as PostgreSQL migration effort, **plus**:
- Remove all triggers (reimplement in Go code)
- Find an alternative for full-text search
- Handle distributed sequence behavior for `AUTOINCREMENT` equivalents

**Migration effort: High.**

### Pros
1. **PostgreSQL wire compatibility**: Can reuse pgx driver and most PostgreSQL SQL
2. **Distributed by default**: If you ever need multi-node, it is built in
3. **Automatic data rebalancing**: Survives node failures transparently

### Cons
1. **No full-text search**: Dealbreaker for title/author search
2. **Resource-heavy**: 2GB+ RAM minimum, 200MB binary, for a 11K-row database
3. **Worse single-node performance than PostgreSQL**: Distributed overhead hurts small workloads

### Verdict: **No**
CockroachDB solves a problem this application does not have (global distribution) while missing a feature it does need (full-text search). The resource overhead is unjustifiable for a household app. Use PostgreSQL instead.

---

## 3. MariaDB

### Overview
Community-driven fork of MySQL, created by MySQL's original author (Monty Widenius) after Oracle's acquisition. First release 2009. License: GPLv2. Drop-in replacement for MySQL with additional features (window functions, CTEs, temporal tables, system-versioned tables). Currently v11.x.

### Go Driver Quality
- **go-sql-driver/mysql** (`github.com/go-sql-driver/mysql`): The standard Go MySQL driver. Production-proven, `database/sql` compatible. Works with MariaDB.
- ORM support: GORM, sqlx, sqlc, Ent all support MySQL/MariaDB.
- Quality is good but not at pgx level. The MySQL wire protocol has more quirks.

### Embeddability
**Cannot run in-process.** Requires a separate server daemon (`mariadbd`). There is no embeddable library equivalent to SQLite for MariaDB. (MySQL had `libmysqld` which was removed in 8.0.)

### SQL Compatibility
MariaDB SQL differs from SQLite significantly:
- **`INSERT OR IGNORE`**: Becomes `INSERT IGNORE INTO` (MySQL syntax).
- **`AUTOINCREMENT`**: Becomes `AUTO_INCREMENT` (one word vs two).
- **`TEXT` type**: Works but maximum lengths differ. `MEDIUMTEXT` or `LONGTEXT` for large fields.
- **`BOOLEAN`**: Alias for `TINYINT(1)`, similar to SQLite's integer approach.
- **`datetime('now')`**: Becomes `NOW()`.
- **String comparison**: Case-insensitive by default with `utf8mb4_general_ci` collation (different from SQLite's `BINARY` default).
- **Foreign keys**: InnoDB only. Need to ensure all tables use InnoDB engine.
- **Partial indexes**: **Not supported.** The `WHERE` clause in `CREATE INDEX` (used in migrations 32, 33, 34) has no MariaDB equivalent. Must use full indexes.
- **Expression indexes** (e.g., `COALESCE(marked_for_deletion, 0)` in migration 17): Supported in MariaDB 10.6+ as virtual columns + index.

### Full-Text Search
MariaDB has built-in full-text search on InnoDB tables:
- `FULLTEXT` index type with `MATCH ... AGAINST` syntax
- Natural language mode and boolean mode
- Decent for basic search but less configurable than PostgreSQL's `tsvector`
- No language-specific stemming by default (plugin-based)
- Good enough for title/author search at 11K records

### Connection Model
Client-server over MySQL wire protocol. Connection pooling via ProxySQL, MaxScale, or Go's built-in `database/sql` pool. The `go-sql-driver/mysql` handles connection management well.

### Self-Hosting Complexity
- **Docker**: `docker run mariadb:11` -- simple.
- **System package**: Available in all Linux distributions.
- **Config**: `my.cnf`. Needs more tuning than PostgreSQL for good defaults (InnoDB buffer pool size, etc.).
- **Backup**: `mariadb-dump`, Mariabackup for hot backups.
- **Overhead**: ~100-200MB RAM with InnoDB.

### Performance at Our Scale
Excellent for 11K rows. InnoDB is well-optimized for mixed read/write workloads. Similar performance to PostgreSQL at this scale. The main performance difference vs PostgreSQL would be in complex joins and CTEs, where PostgreSQL's query planner is generally superior.

### Replication/HA
- Binary log replication (built-in): async or semi-sync
- Galera Cluster: synchronous multi-master replication
- MaxScale: proxy-based routing and failover
- MariaDB SkySQL: managed cloud offering

### Migration from SQLite
- Rewrite all 37 migrations for MySQL syntax
- Replace `INSERT OR IGNORE` with `INSERT IGNORE`
- Replace `AUTOINCREMENT` with `AUTO_INCREMENT`
- Replace FTS5 with InnoDB `FULLTEXT` indexes
- Remove partial indexes (use full indexes or generated columns)
- Replace expression indexes with virtual columns + indexes
- Replace `PRAGMA` with `information_schema` queries
- Handle string collation differences (case sensitivity)

**Migration effort: Medium-High.** More syntax differences than PostgreSQL.

### Pros
1. **Familiar MySQL ecosystem**: Huge community, abundant tooling, easy to find help
2. **Built-in full-text search**: InnoDB FULLTEXT indexes cover the search use case
3. **System-versioned tables**: Temporal tables could replace the manual book version history

### Cons
1. **No partial indexes**: Must work around migrations 32, 33, 34
2. **Less capable than PostgreSQL**: Weaker query planner, fewer advanced SQL features
3. **More migration rewriting**: MySQL syntax diverges more from SQLite than PostgreSQL does

### Verdict: **Maybe** -- only if you prefer MySQL ecosystem
MariaDB is a competent database but offers no advantages over PostgreSQL for this use case while requiring more migration work. The only reason to choose it is if the deployer is already running MariaDB and prefers to consolidate.

---

## 4. DuckDB

### Overview
Embeddable analytical SQL database (column-oriented OLAP). First release 2019, rapidly growing. License: MIT. Often described as "SQLite for analytics." Designed for fast analytical queries over medium datasets. Currently v1.x (stable).

### Go Driver Quality
- **go-duckdb** (`github.com/marcboeker/go-duckdb`): CGo-based driver, `database/sql` compatible. Uses DuckDB's C API.
- Also available via ADBC (Arrow Database Connectivity).
- ORM support: Limited. GORM may work via `database/sql` but not officially tested. sqlx works.
- Driver maturity is lower than SQLite or PostgreSQL drivers.

### Embeddability
**Runs in-process** via CGo bindings, similar to how `mattn/go-sqlite3` works. Single file database. This is a major advantage -- same deployment model as SQLite.

### SQL Compatibility
DuckDB supports PostgreSQL-dialect SQL, which differs from SQLite:
- **`INSERT OR IGNORE`**: Not supported. Use `INSERT INTO ... ON CONFLICT DO NOTHING` (PostgreSQL syntax).
- **`AUTOINCREMENT`**: Not supported the same way. DuckDB has sequences.
- **`CREATE VIRTUAL TABLE USING fts5`**: Not supported. DuckDB has its own FTS extension.
- **`ALTER TABLE ADD COLUMN`**: Supported.
- **Triggers**: **Not supported.** The FTS sync triggers must be handled in application code.
- **Foreign keys**: Parsed but **not enforced** (as of 2026).
- **`PRAGMA table_info`**: Partially supported (DuckDB has its own PRAGMAs).
- **Window functions, CTEs**: Excellent support (better than SQLite).
- **Partial indexes**: Not supported.

### Full-Text Search
DuckDB has an FTS extension (`fts`) that provides:
- `PRAGMA create_fts_index('table', 'id_col', 'col1', 'col2', ...)`
- BM25 scoring
- Stemming support
- Reasonable for basic search needs

Less mature than PostgreSQL FTS or SQLite FTS5 but functional.

### Connection Model
In-process, single-writer. Similar to SQLite's concurrency model. Multiple readers, one writer at a time. For a 1-5 user app, this is fine.

### Self-Hosting Complexity
**Same as SQLite** -- embedded in the application binary. No separate server needed. Single database file. This is the simplest possible deployment.

### Performance at Our Scale
DuckDB is column-oriented, optimized for analytical queries (aggregations, scans, GROUP BY). For the dashboard stats queries (`COUNT`, `SUM`, `GROUP BY format`, `GROUP BY library_state`), DuckDB would be 5-50x faster than SQLite. However:
- **Point lookups by ID**: Slower than SQLite (column store overhead for single-row fetches).
- **Insert/update single rows**: Slower than SQLite (batch-oriented design).
- **Small table joins**: Comparable or slightly slower than SQLite for <100K rows.

The workload is predominantly OLTP (browse, lookup, update) with occasional OLAP (dashboard stats). DuckDB is optimized for the opposite ratio.

### Replication/HA
None. DuckDB is a single-file embedded database with no replication features.

### Migration from SQLite
- Rewrite migrations for PostgreSQL-like syntax (DuckDB uses Postgres dialect)
- Remove all triggers (handle FTS updates in application code)
- Replace FTS5 with DuckDB FTS extension
- Handle lack of foreign key enforcement (application-level validation)
- Handle lack of partial indexes
- Replace `INSERT OR IGNORE` with `ON CONFLICT DO NOTHING`

**Migration effort: Medium-High.** Similar to PostgreSQL but with additional gaps (no triggers, no FK enforcement).

### Pros
1. **Embeddable**: Same deployment model as SQLite -- no server process needed
2. **Exceptional analytics performance**: Dashboard aggregations would be blazing fast
3. **Modern SQL dialect**: Better standard SQL compliance than SQLite

### Cons
1. **Wrong workload fit**: Column-oriented OLAP design vs our OLTP-dominant workload
2. **No triggers, no FK enforcement**: Loses data integrity guarantees SQLite provides
3. **Immature Go driver**: CGo-based, less battle-tested than `mattn/go-sqlite3`

### Verdict: **No**
DuckDB is an impressive database for the wrong use case. The audiobook organizer is predominantly OLTP (single-row lookups, inserts, updates) with occasional analytics. DuckDB's column-oriented design makes it slower for the 90% of operations that matter most while being faster for the 10% that are already fast enough in SQLite. The lack of triggers and FK enforcement also reduces data integrity.

---

## 5. rqlite

### Overview
Lightweight distributed SQLite, using Raft consensus for replication. Each node runs a full SQLite instance, and writes are replicated via Raft. First release 2014. License: MIT. Designed for applications that need SQLite's simplicity with fault tolerance.

### Go Driver Quality
- **gorqlite** (`github.com/rqlite/gorqlite`): Official Go client. HTTP-based API, **not** `database/sql` compatible.
- Must use rqlite's HTTP API (JSON over HTTP) rather than standard SQL driver interface.
- ORM support: None. You must use the raw HTTP API or gorqlite client.

### Embeddability
**Cannot run in-process.** rqlite runs as a separate daemon that exposes an HTTP API. You interact with it via HTTP requests, not via a SQL driver. This is a fundamental architectural difference from SQLite.

### SQL Compatibility
rqlite uses SQLite under the hood, so the SQL dialect is identical to SQLite. However:
- All queries go through an HTTP API, not `database/sql`.
- Writes are serialized through Raft consensus.
- Some SQLite features may not work through the HTTP layer.
- **FTS5**: Depends on the SQLite build used by rqlite (usually included).
- The existing SQLite SQL would work unchanged -- but the driver layer is completely different.

### Full-Text Search
Same as SQLite -- FTS5 virtual tables, `MATCH` queries, triggers. If rqlite's embedded SQLite is compiled with FTS5 (it usually is), all existing FTS code works.

### Connection Model
HTTP client-server. Each request is an HTTP POST with SQL in the JSON body. No connection pooling needed (HTTP). Reads can go to any node; writes go to the leader.

### Self-Hosting Complexity
- **Docker**: Available. Single container per node.
- **Single binary**: Yes. `rqlited` is a single Go binary.
- **Config**: Minimal. Specify data directory and join addresses for clustering.
- **Overhead**: ~50MB RAM per node.
- **Clustering**: 3+ nodes for fault tolerance (odd numbers for Raft quorum).

For a single-user app, running 3 rqlite nodes is overkill. Running 1 node provides no benefit over plain SQLite.

### Performance at Our Scale
**Slower than plain SQLite** for all operations:
- Read queries add HTTP round-trip overhead (~1ms per query)
- Write queries add Raft consensus overhead (~5-10ms per write)
- For 11K rows, the overhead is noticeable but not problematic
- Batch imports would be significantly slower due to per-write consensus

### Replication/HA
This is rqlite's purpose:
- Automatic leader election via Raft
- Data replicated to all nodes
- Tolerates minority node failures
- Automatic recovery when nodes rejoin

### Migration from SQLite
- **SQL**: Zero changes needed (same dialect).
- **Driver layer**: Complete rewrite. Replace all `database/sql` calls with HTTP API calls.
- **Store interface**: Every method needs rewriting to use gorqlite instead of `*sql.DB`.
- **Transactions**: rqlite supports "execute" batches but not full ACID transactions across arbitrary queries.

**Migration effort: Very High.** The SQL stays the same but the entire 4,310-line store implementation must be rewritten to use HTTP instead of `database/sql`.

### Pros
1. **Identical SQL dialect**: Zero SQL rewriting needed
2. **Distributed SQLite**: Fault tolerance for SQLite databases
3. **Simple Go binary**: Easy to deploy

### Cons
1. **Not `database/sql` compatible**: Requires complete driver-layer rewrite
2. **HTTP overhead**: Slower than direct SQLite for every operation
3. **Overkill for single-user**: No benefit over plain SQLite unless you need 3+ nodes

### Verdict: **No**
rqlite solves the wrong problem. The audiobook organizer does not need distributed consensus for a household app. The HTTP-only API means rewriting the entire store implementation despite keeping the same SQL. The only scenario where rqlite makes sense is if you need multiple geographically-distributed copies of the library database, which is not a real use case here.

---

## 6. LiteFS / Turso (libSQL)

### Overview
Two related but distinct projects for distributed/replicated SQLite:

**LiteFS** (by Fly.io): A FUSE-based filesystem that intercepts SQLite write operations and replicates them to other nodes. License: Apache 2.0. The application uses SQLite normally; LiteFS handles replication transparently at the filesystem level. As of 2025, Fly.io has deprioritized LiteFS in favor of Turso.

**Turso/libSQL** (by Turso): A fork of SQLite that adds native replication, HTTP access, and embedded replicas. License: MIT. libSQL is the open-source fork; Turso is the managed cloud service. Provides both an embedded library and a server mode.

### Go Driver Quality
**LiteFS**: Uses standard `mattn/go-sqlite3` or `modernc.org/sqlite`. No driver changes needed -- LiteFS is transparent to the application.

**Turso/libSQL**:
- `github.com/tursodatabase/go-libsql`: Official driver, `database/sql` compatible.
- Supports both local embedded mode and remote HTTP mode.
- Can run as an "embedded replica" that syncs from a primary server.
- Driver maturity is moderate -- newer than `mattn/go-sqlite3` but actively developed.

### Embeddability
**LiteFS**: Application uses SQLite in-process. LiteFS is a separate FUSE daemon.

**libSQL embedded mode**: Runs in-process, same as SQLite. The embedded replica feature lets you have a local SQLite-like file that automatically syncs from a remote primary.

### SQL Compatibility
**LiteFS**: Identical to SQLite (it IS SQLite).

**libSQL**: SQLite-compatible with extensions:
- `ALTER TABLE DROP COLUMN` (not in upstream SQLite)
- Native `VECTOR` type for embeddings
- `RANDOM ROWID` for better distribution
- All existing SQLite SQL works unchanged.

### Full-Text Search
Both use SQLite's FTS5. Identical to current implementation. Zero changes needed.

### Connection Model
**LiteFS**: Same as SQLite (in-process, file-based). LiteFS adds a FUSE layer.

**libSQL**: In-process for embedded mode. HTTP for remote/server mode. Embedded replicas combine both -- local reads, remote writes.

### Self-Hosting Complexity
**LiteFS**:
- Requires FUSE support on the host (not available in all containers)
- Needs a Consul instance for leader election (additional dependency)
- Configuration is non-trivial
- Being deprioritized by Fly.io

**Turso/libSQL server (sqld)**:
- Single binary server
- Docker available
- Can run as a standalone server or embedded library
- Simpler than LiteFS but still an additional moving part

**libSQL embedded only** (no replication):
- Same as SQLite -- embedded in application binary
- Drop-in replacement via `go-libsql` driver
- Simplest deployment

### Performance at Our Scale
**LiteFS**: Same as SQLite with ~1-5% overhead for FUSE interception on writes.

**libSQL embedded**: Same as SQLite. The fork maintains performance parity.

**libSQL remote/replica**: Adds network latency for writes (synced to primary), local-speed reads.

### Replication/HA
**LiteFS**: Primary-replica replication. One writer, N readers. Failover requires Consul.

**libSQL/Turso**:
- Embedded replicas sync from a primary `sqld` server
- WAL-based replication
- Eventual consistency for replicas
- Server-to-server replication

### Migration from SQLite
**LiteFS**: **Zero changes.** Application code stays identical. Just add the FUSE daemon.

**libSQL embedded**: Change the driver import from `mattn/go-sqlite3` to `go-libsql`. Minimal code changes (connection string format may differ). SQL stays identical.

**libSQL with replication**: Same driver change plus configuration for sync endpoint.

**Migration effort: Very Low to Low.**

### Pros
1. **Near-zero migration effort**: Same SQL dialect, `database/sql` compatible drivers
2. **Embeddable**: No separate server process needed (embedded mode)
3. **Replication option**: Can add replication later without changing application code

### Cons
1. **LiteFS is being deprioritized**: Uncertain long-term support from Fly.io
2. **libSQL driver maturity**: Newer than `mattn/go-sqlite3`, potentially less battle-tested
3. **Limited value-add for single-user**: Without replication needs, it is just SQLite with a different driver

### Verdict: **Maybe** -- low cost, low risk, modest benefit
libSQL in embedded mode is the lowest-effort option on this list. It provides a SQLite-compatible database with some extensions (DROP COLUMN, vector search) that could be useful in the future. The migration cost is nearly zero. However, the practical benefit over plain SQLite is marginal for a single-user self-hosted app. Worth considering if you want a future path to replication without committing to a full database migration.

---

## 7. YugabyteDB

### Overview
Distributed SQL database built on PostgreSQL's query layer atop a Google Spanner-inspired storage engine. First release 2017. License: Apache 2.0 (fully open source). Designed for global-scale transactional workloads. Compatible with PostgreSQL wire protocol and SQL dialect.

### Go Driver Quality
- Uses **pgx** (same as PostgreSQL) via wire protocol compatibility.
- Some PostgreSQL features may not work identically (see caveats).
- ORM support: Same as PostgreSQL.

### Embeddability
**Cannot run in-process.** YugabyteDB runs as a cluster of `yb-master` and `yb-tserver` processes. Even single-node deployments require multiple daemon processes. The minimum recommended setup uses 3 nodes.

### SQL Compatibility
YugabyteDB's YSQL mode is PostgreSQL-compatible:
- Most PostgreSQL syntax works
- **No partial indexes** in some configurations
- **Triggers**: Supported but with distributed execution caveats
- **FTS**: PostgreSQL's `tsvector`/`tsquery` is supported but may have limitations
- **TOAST** (large value storage): Different implementation

### Full-Text Search
Same as PostgreSQL (`tsvector`/`tsquery`, GIN indexes). Support may lag behind PostgreSQL's latest features.

### Connection Model
Client-server via PostgreSQL wire protocol. Same pgx driver and pooling.

### Self-Hosting Complexity
- **Docker**: Available but requires multiple containers (master + tserver).
- **Resource usage**: Minimum 4GB RAM per node recommended. 3 nodes minimum for production.
- **Config**: Complex. Replication factor, placement policies, tablet splitting.
- **Overhead**: Extremely heavy for a household app.

### Performance at Our Scale
YugabyteDB is designed for millions of rows across distributed clusters. At 11K rows:
- Point lookups: 5-10x slower than PostgreSQL due to distributed storage overhead
- Joins: Significantly slower (potential cross-tablet shuffles even on single node)
- Aggregations: Slower than PostgreSQL
- Write latency: Higher due to Raft consensus (even single-node)

### Replication/HA
World-class: automatic sharding, configurable replication factor, multi-region deployment, zero-downtime upgrades, follower reads. Complete overkill for this use case.

### Migration from SQLite
Same as PostgreSQL migration effort plus distributed system caveats.

**Migration effort: High.**

### Pros
1. **PostgreSQL wire compatibility**: Reuse pgx driver
2. **Automatic sharding and replication**: Scales horizontally
3. **Fully open source** (Apache 2.0)

### Cons
1. **Massive resource overhead**: 12GB+ RAM minimum for a 3-node cluster, for 11K rows
2. **Slower than PostgreSQL at every operation** at this scale
3. **Operational complexity**: Managing a distributed database for a household app

### Verdict: **No**
YugabyteDB is designed for enterprises running global-scale applications. Using it for a household audiobook library with 11K books is like hiring a fleet of 18-wheelers to deliver groceries. The resource overhead alone (12GB+ RAM) makes it impractical for a self-hosted home server.

---

## 8. TiDB

### Overview
Distributed SQL database with MySQL compatibility. Created by PingCAP, inspired by Google Spanner and F1. First release 2015. License: Apache 2.0. Designed for HTAP (hybrid transactional/analytical processing). TiDB is the SQL layer; TiKV is the distributed storage engine.

### Go Driver Quality
- Uses **go-sql-driver/mysql** (same as MariaDB/MySQL).
- Written in Go itself, so the team maintains excellent Go tooling.
- ORM support: Same as MySQL/MariaDB.

### Embeddability
**Cannot run in-process** in the traditional sense. TiDB requires TiKV (storage) and PD (placement driver) components. However, TiDB has a **"TiDB Serverless"** mode and an experimental **embedded mode** that runs a single-node TiDB instance. There is also `go.uber.org/mock`-compatible test support.

For local development, `tiup playground` can spin up a complete cluster, but it runs multiple processes.

### SQL Compatibility
MySQL 5.7/8.0 compatible with extensions:
- Most MySQL syntax works
- **`INSERT IGNORE`**: Supported (MySQL syntax).
- **`AUTO_INCREMENT`**: Supported but non-sequential (distributed sequences).
- **Triggers**: **Not supported.**
- **Foreign keys**: Parsed but **not enforced** (as of TiDB 7.x; enforcement added experimentally in later versions).
- **Partial indexes**: Not supported (MySQL limitation).
- **Window functions, CTEs**: Supported.

### Full-Text Search
TiDB has **no built-in full-text search**. The MySQL `FULLTEXT` index type is parsed but not functional. You would need an external search service.

### Connection Model
Client-server via MySQL wire protocol. Standard MySQL connection pooling applies.

### Self-Hosting Complexity
- **Docker**: `tiup playground` for development; production requires TiDB + TiKV + PD.
- **Minimum 3 components**: TiDB (SQL), TiKV (storage), PD (scheduling). Each needs separate processes.
- **Resource usage**: Minimum 8GB RAM for a minimal cluster.
- **Config**: Complex. Needs tikv.toml, tidb.toml, pd.toml.

### Performance at Our Scale
Similar to YugabyteDB's trade-offs -- designed for large-scale distributed workloads. At 11K rows, single-node PostgreSQL or SQLite will be significantly faster for every operation type.

### Replication/HA
Raft-based replication, automatic sharding, online DDL, HTAP (TiFlash for analytics). Enterprise-grade HA. Completely unnecessary here.

### Migration from SQLite
Same as MariaDB migration plus:
- Remove triggers (not supported)
- Find alternative for FTS (not supported)
- Handle non-enforced foreign keys

**Migration effort: Very High.**

### Pros
1. **MySQL compatible**: Familiar SQL dialect
2. **HTAP**: Can handle both OLTP and OLAP in one system
3. **Written in Go**: Excellent Go ecosystem and tooling

### Cons
1. **No full-text search**: Dealbreaker
2. **No triggers, limited FK enforcement**: Loses data integrity features
3. **Resource-heavy**: 8GB+ RAM minimum for the simplest deployment

### Verdict: **No**
Same problems as CockroachDB and YugabyteDB: massive overhead for a distributed database that adds latency and complexity without any benefit at this scale. The lack of FTS is the immediate dealbreaker.

---

## 9. QuestDB

### Overview
Time-series database optimized for fast ingestion and time-based queries. First release 2014. License: Apache 2.0. Written in Java and C++. Designed for financial data, IoT sensor data, and application metrics. Supports a subset of SQL via PostgreSQL wire protocol.

### Go Driver Quality
- Can use **pgx** via PostgreSQL wire protocol (limited SQL support).
- Also has an InfluxDB Line Protocol (ILP) ingestion client for Go.
- No dedicated Go SQL driver.
- ORM support: None -- QuestDB's SQL subset is too limited for ORMs.

### Embeddability
**Cannot run in-process.** QuestDB runs as a separate Java/C++ server process. Requires JVM for some features.

### SQL Compatibility
QuestDB supports a **very limited subset** of SQL:
- **No `JOIN`**: Only `LATEST ON` for time-series joins. No general-purpose joins.
- **No `INSERT OR IGNORE`**: Not applicable.
- **No `ALTER TABLE ADD COLUMN`**: Columns are added implicitly on first write.
- **No foreign keys**: Not a relational database in the traditional sense.
- **No `CREATE INDEX`**: QuestDB manages its own indexes automatically.
- **No subqueries** in many contexts.
- **Designated timestamp**: Every table must have a timestamp column that defines its time-series ordering.

### Full-Text Search
None. QuestDB has basic `LIKE` and `=` string matching. No full-text search capability.

### Connection Model
Client-server. PostgreSQL wire protocol for queries, ILP for high-speed ingestion.

### Self-Hosting Complexity
- **Docker**: `docker run questdb/questdb` -- simple.
- **Resource usage**: Designed for millions of rows per second ingestion. Moderate RAM usage.
- **Config**: Minimal for basic use.

### Performance at Our Scale
QuestDB excels at time-series aggregations (e.g., "how much did I listen last week?"). For the playback_events table, it would be excellent. For everything else (books, authors, series, joins), it is fundamentally unsuitable.

### Replication/HA
Enterprise-only replication. Open source version is single-node.

### Migration from SQLite
Impossible to migrate the existing schema. QuestDB is not a relational database. It cannot model the 20+ tables with foreign keys and many-to-many relationships that the audiobook organizer uses.

**Migration effort: Not feasible.**

### Pros
1. **Blazing fast time-series queries**: Excellent for playback history and listening stats
2. **Simple ingestion**: ILP protocol is very fast
3. **Automatic indexing**: No manual index management

### Cons
1. **Not a relational database**: Cannot model books/authors/series relationships
2. **No JOINs**: Fundamental incompatibility with the current schema
3. **No FTS, no FK, no transactions**: Missing every feature the app needs

### Verdict: **No**
QuestDB is a time-series database, not a general-purpose RDBMS. It could potentially serve as a secondary store for playback analytics alongside the primary database, but it cannot replace SQLite as the main backend. The lack of joins alone makes it completely unsuitable.

---

## 10. Firebird

### Overview
Open-source RDBMS descended from Borland InterBase. First release 2000 (InterBase lineage goes to 1981). License: IPL (InterBase Public License) and IDPL -- permissive, similar to MPL. Supports both embedded and server modes. Currently v5.x.

### Go Driver Quality
- **firebirdsql** (`github.com/nakagami/firebirdsql`): Pure Go driver, `database/sql` compatible. Written by a single developer. Functional but niche.
- ORM support: Very limited. GORM does not officially support Firebird. sqlx works via `database/sql`.
- Community is small. Issues may go unresolved.

### Embeddability
**Yes -- runs in-process** via the Firebird Embedded library (`fbembed`). The Go driver can connect to both embedded and server modes. Embedded mode uses a shared library (`.so`/`.dll`), similar to SQLite's C library.

However, the embedded Firebird library is a C shared library that must be distributed with the application. It is larger and more complex than SQLite's single `.c/.h` file.

### SQL Compatibility
Firebird SQL is standards-compliant but differs from SQLite:
- **`INSERT OR IGNORE`**: Not supported. Must use `MERGE` statement or `UPDATE OR INSERT`.
- **`AUTOINCREMENT`**: Uses `GENERATORS` (sequences) + triggers, or `GENERATED BY DEFAULT AS IDENTITY` (v3+).
- **`TEXT` type**: Firebird uses `VARCHAR(n)` with a maximum of 32,767 bytes, or `BLOB SUB_TYPE TEXT` for larger values.
- **`ALTER TABLE ADD COLUMN`**: Supported (`ALTER TABLE ... ADD <column>`).
- **`COALESCE`**: Supported.
- **Foreign keys**: Fully supported.
- **Partial indexes**: **Not supported.**
- **`BOOLEAN` type**: Supported natively (v3+).
- **CTEs**: Supported (v2.5+).
- **Window functions**: Supported (v3+).

### Full-Text Search
Firebird has **no built-in full-text search**. You would need to implement search via `LIKE '%term%'` (slow) or use an external FTS engine. There are third-party Firebird UDF libraries that add FTS, but they are obscure and unmaintained.

### Connection Model
Both embedded (in-process, single-user) and client-server (multi-user) modes. The embedded mode is single-connection only -- suitable for this app's 1-5 user model.

### Self-Hosting Complexity
- **Docker**: Available but less common than PostgreSQL/MariaDB.
- **System package**: Available on most Linux distributions but less commonly installed.
- **Config**: `firebird.conf`. Moderate complexity.
- **Community**: Small. Documentation is adequate but not as abundant as PostgreSQL/MySQL.

### Performance at Our Scale
Good for 11K rows. Firebird is a capable RDBMS with a mature query optimizer. Performance is comparable to SQLite for embedded mode at this scale. The embedded library adds ~5-10MB to the application.

### Replication/HA
Firebird has limited replication options:
- Asynchronous replication via third-party tools
- No built-in clustering
- IBReplicator (commercial) for replication
- Not a strong suit

### Migration from SQLite
- Rewrite all 37 migrations for Firebird SQL syntax
- Replace `INSERT OR IGNORE` with `MERGE` or `UPDATE OR INSERT`
- Replace `AUTOINCREMENT` with sequences/identity columns
- Replace `TEXT` with `VARCHAR(n)` or `BLOB SUB_TYPE TEXT`
- Find an alternative for FTS5 (application-level search)
- Remove partial indexes

**Migration effort: High.** Firebird SQL is more different from SQLite than PostgreSQL is, and the smaller community means less tooling support.

### Pros
1. **Embeddable**: Can run in-process like SQLite
2. **Mature**: 40+ year lineage, battle-tested in embedded applications
3. **Standards-compliant SQL**: Good SQL support with CTEs, window functions, etc.

### Cons
1. **No full-text search**: Must implement search externally
2. **Tiny Go community**: Single-developer driver, limited ORM support
3. **Obscure ecosystem**: Finding Firebird expertise is difficult; documentation is sparse compared to PostgreSQL

### Verdict: **No**
Firebird's embeddability is appealing, but the lack of FTS, the tiny Go ecosystem, and the significant migration effort make it a poor choice. If you want an embeddable database, stick with SQLite or consider libSQL. If you want a server database, use PostgreSQL. Firebird occupies an awkward middle ground for this use case.

---

## Comparison Matrix

Databases are scored 1-5 (5 = best) for each dimension.

| Database | Ease of Implementation | Self-Hosting Simplicity | Performance | Future Scalability | Community/Ecosystem | **Total** |
|----------|:-----:|:-----:|:-----:|:-----:|:-----:|:-----:|
| **PostgreSQL** | 3 | 3 | 5 | 5 | 5 | **21** |
| **CockroachDB** | 2 | 2 | 3 | 5 | 3 | **15** |
| **MariaDB** | 2 | 3 | 4 | 4 | 4 | **17** |
| **DuckDB** | 2 | 5 | 3 | 2 | 3 | **15** |
| **rqlite** | 1 | 3 | 2 | 3 | 2 | **11** |
| **LiteFS/Turso (libSQL)** | 5 | 5 | 4 | 3 | 3 | **20** |
| **YugabyteDB** | 2 | 1 | 2 | 5 | 3 | **13** |
| **TiDB** | 2 | 1 | 2 | 5 | 3 | **13** |
| **QuestDB** | 1 | 3 | 1 | 1 | 2 | **8** |
| **Firebird** | 2 | 3 | 4 | 2 | 1 | **12** |

### Scoring Rationale

**Ease of Implementation** (how much existing SQL/code can be reused):
- libSQL (5): Near-zero migration effort, same SQL dialect, `database/sql` compatible
- PostgreSQL (3): Moderate migration effort, systematic but straightforward rewriting
- MariaDB, CockroachDB, DuckDB, YugabyteDB, TiDB, Firebird (2): Significant rewriting plus feature gaps
- rqlite, QuestDB (1): Complete rewrite of driver layer or fundamentally incompatible

**Self-Hosting Simplicity** (ease for a non-DBA to deploy and maintain):
- libSQL, DuckDB (5): Embedded in application binary, zero deployment overhead
- PostgreSQL, MariaDB, rqlite, Firebird, QuestDB (3): Single Docker container, minimal config
- CockroachDB (2): Larger binary, more configuration needed
- YugabyteDB, TiDB (1): Multi-component clusters, complex setup

**Performance for Our Workload** (11K rows, OLTP-dominant, complex joins):
- PostgreSQL (5): Best general-purpose query performance
- libSQL, MariaDB, Firebird (4): Excellent for this scale
- CockroachDB, DuckDB (3): Adequate but suboptimal (distributed overhead / OLAP-oriented)
- rqlite, YugabyteDB, TiDB (2): Added latency for every operation
- QuestDB (1): Cannot model the workload

**Future Scalability** (growth path if the app evolves):
- PostgreSQL, CockroachDB, YugabyteDB, TiDB (5): Scale to millions of records and hundreds of users
- MariaDB (4): Scales well with replication
- libSQL, rqlite (3): Replication options available but limited
- DuckDB, Firebird (2): Limited scaling options
- QuestDB (1): Only scales for time-series, not relational workloads

**Community/Ecosystem** (driver quality, documentation, hiring, tooling):
- PostgreSQL (5): Largest community, best documentation, best Go drivers
- MariaDB (4): Huge MySQL ecosystem
- CockroachDB, YugabyteDB, TiDB, DuckDB, libSQL (3): Growing communities, adequate docs
- rqlite, QuestDB (2): Small communities
- Firebird (1): Tiny community, single-developer Go driver

---

## Final Recommendations

### Tier 1: Worth Implementing

1. **PostgreSQL** (Total: 21) -- The best choice if you are willing to accept a separate server process. Unmatched query capabilities, Go driver quality, and full-text search. The migration effort is moderate and the result is a database backend that will never be the bottleneck. Recommended if the app is typically deployed via Docker Compose.

2. **libSQL/Turso** (Total: 20) -- The best choice if embeddability is a hard requirement. Near-zero migration cost, same SQL dialect as SQLite, and an opt-in path to replication. Recommended as a first step: swap the SQLite driver for libSQL, gain `ALTER TABLE DROP COLUMN` support and vector search, and unlock future replication without any SQL rewriting.

### Tier 2: Conditional

3. **MariaDB** (Total: 17) -- Only worthwhile if the deployment environment already has MariaDB/MySQL and the user prefers consolidation. No technical advantage over PostgreSQL for this workload.

### Tier 3: Not Recommended

4. All others (CockroachDB, DuckDB, rqlite, YugabyteDB, TiDB, QuestDB, Firebird) -- Each fails on one or more critical requirements: no FTS, not `database/sql` compatible, massive resource overhead for a household app, wrong workload model, or tiny ecosystem. None are worth the implementation effort.

### Practical Strategy

The most pragmatic approach would be a two-phase plan:

**Phase 1 (Low effort, immediate):** Replace `mattn/go-sqlite3` with libSQL's `go-libsql` driver. This requires minimal code changes (driver import and connection string), keeps all 37 migrations working unchanged, and unlocks future replication if needed.

**Phase 2 (Medium effort, when justified):** Add PostgreSQL as a third backend option behind the Store interface. This is justified if/when the app needs multi-user access, better full-text search, or is deployed alongside other services that already use PostgreSQL. The existing Store interface abstraction makes this feasible without touching application logic.
