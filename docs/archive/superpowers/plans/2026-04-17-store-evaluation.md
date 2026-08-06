<!-- file: docs/superpowers/plans/2026-04-17-store-evaluation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3418392a-9849-4ade-a89a-6828159ae1fb -->
<!-- last-edited: 2026-04-16 -->

# 4.7: Per-Workload Store Evaluation — Research Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task-by-task.

**Backlog item:** 4.7 — Per-workload store evaluation
**Spec:** None — this plan is self-contained.
**Depends on:** Nothing (can run in parallel with all other work)
**Prior art:** PostgreSQL recommended as next step (noted in project memory `project_session_state.md`)

## Overview

The audiobook organizer currently uses PebbleDB (Go-native LSM key-value store) for all metadata CRUD and SQLite for the embedding/dedup vector store. This plan produces a **decision document** evaluating alternative storage backends for each distinct workload in the system. The deliverable is not code migration but a structured analysis with benchmarks, migration cost estimates, and a recommended path forward.

## Workloads to evaluate

1. **Metadata CRUD** — Book, Author, Series, BookFile, BookVersion, User, Playlist, Tag, Session (PebbleDB today)
2. **Vector search** — Embedding storage + approximate nearest neighbor queries (SQLite + chromem-go today)
3. **Full-text search** — Library search, smart playlist evaluation (Bleve today, via `internal/search/`)
4. **Activity log / time-series** — Unified activity log, operation history (PebbleDB today)
5. **External ID map** — iTunes PID ↔ book ID mappings, ~97K entries (PebbleDB today)
6. **Configuration / state** — App config, checkpoints, feature flags (PebbleDB today)

## Candidate stores

- **PostgreSQL** — Relational, JSONB, full-text search, pgvector for embeddings
- **CockroachDB** — PostgreSQL-compatible, distributed (free tier for evaluation)
- **SQLite (expanded)** — Use SQLite for metadata CRUD (replacing PebbleDB), keeping it for embeddings
- **PebbleDB (status quo)** — Keep current architecture, optimize indexes
- **Meilisearch / Typesense** — Dedicated search engines (replace Bleve)
- **pgvector** — PostgreSQL extension for vector similarity (replace chromem-go)

---

### Task 1: Benchmark harness + workload definitions (1 PR)

**Goal:** Create a benchmark harness that can run the same workload against multiple store backends and produce comparable results.

**Files:**
- Create: `internal/benchmark/harness.go` — benchmark runner with timing, throughput, and latency percentile reporting
- Create: `internal/benchmark/workloads.go` — workload definitions
- Create: `internal/benchmark/harness_test.go` — verify harness works with PebbleDB

Workload definitions:
- [ ] **W1: Metadata CRUD mix** — 70% reads (GetBookByID, GetAllBooks with pagination), 20% updates (UpdateBook), 10% creates (CreateBook). 10K operations.
- [ ] **W2: Vector search** — Upsert 5K embeddings (384-dim), then run 1K FindSimilar queries with varying thresholds and maxResults.
- [ ] **W3: Full-text search** — Index 10K books, run 500 queries mixing simple terms, field filters, boolean operators.
- [ ] **W4: Activity log append + range query** — Append 50K activity entries, then query by time range (last hour, last day, last week) and by book ID.
- [ ] **W5: External ID lookup** — 100K point lookups by PID, 1K reverse lookups by book ID.
- [ ] **W6: Concurrent mixed** — All of the above running simultaneously from 10 goroutines.

Metrics to capture per workload:
- [ ] Throughput (ops/sec)
- [ ] Latency: p50, p95, p99
- [ ] Memory usage (peak RSS)
- [ ] Disk usage after workload completes
- [ ] Error rate under concurrent load

**Acceptance criteria:**
- [ ] Harness runs all 6 workloads against PebbleDB and produces a JSON + text report
- [ ] Results are reproducible (within 10% variance across 3 runs)
- [ ] Harness is backend-agnostic (accepts a `Store` interface)

---

### Task 2: PostgreSQL backend adapter + benchmarks (1 PR)

**Goal:** Implement a PostgreSQL-backed Store adapter (enough methods for the benchmark workloads) and run benchmarks.

**Files:**
- Create: `internal/benchmark/pg_store.go` — PostgreSQL Store adapter for benchmark workloads
- Create: `internal/benchmark/pg_store_test.go`
- Create: `internal/benchmark/docker-compose.yml` — PostgreSQL 16 + pgvector for local testing

Implementation scope (benchmark only, not production):
- [ ] Book CRUD: CreateBook, GetBookByID, GetAllBooks, UpdateBook, DeleteBook
- [ ] Embedding: Upsert, FindSimilar using pgvector (`<=>` cosine distance operator)
- [ ] Activity log: append (INSERT), range query (WHERE created_at BETWEEN)
- [ ] External ID: point lookup (SELECT by PID), reverse lookup (SELECT by book_id)
- [ ] Use `pgx` driver with connection pooling (10 connections)
- [ ] Schema: one migration file for benchmark tables

Benchmarks to run:
- [ ] W1-W6 against PostgreSQL, same parameters as PebbleDB baseline
- [ ] Additional: pgvector ANN recall accuracy at different `ef_search` values (compare against chromem-go HNSW)

**Acceptance criteria:**
- [ ] All 6 workloads produce results for PostgreSQL
- [ ] Results are stored in the same JSON format as the PebbleDB baseline
- [ ] pgvector recall rate measured (% of true top-10 neighbors returned)

---

### Task 3: SQLite-expanded backend adapter + benchmarks (1 PR)

**Goal:** Benchmark SQLite as a unified store (metadata CRUD + embeddings in one database).

**Files:**
- Create: `internal/benchmark/sqlite_store.go` — SQLite Store adapter for benchmark workloads
- Create: `internal/benchmark/sqlite_store_test.go`

Implementation scope:
- [ ] Book CRUD using `github.com/mattn/go-sqlite3` with WAL mode
- [ ] Embedding CRUD reusing existing `EmbeddingStore` patterns
- [ ] Activity log: same table structure as PebbleDB but in SQLite
- [ ] External ID: direct table lookup
- [ ] Connection mode: `?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000`
- [ ] Benchmark concurrent access (SQLite's main weakness)

Benchmarks to run:
- [ ] W1-W6 against SQLite, same parameters
- [ ] Additional: W6 (concurrent mixed) with varying goroutine counts (1, 5, 10, 20) to find the concurrency ceiling

**Acceptance criteria:**
- [ ] All 6 workloads produce results for SQLite
- [ ] Concurrency scaling curve documented (throughput vs goroutine count)

---

### Task 4: Evaluation criteria + scoring matrix (1 PR)

**Goal:** Define evaluation criteria, weight them, and produce a scoring matrix comparing all backends.

**Files:**
- Create: `docs/superpowers/specs/2026-04-17-store-evaluation-results.md` — the decision document

Evaluation criteria (with weights):
- [ ] **Performance** (30%) — throughput and latency from benchmarks
- [ ] **Operational complexity** (20%) — deployment, backup, monitoring, upgrades
- [ ] **Migration cost** (20%) — lines of code to change, risk of data loss, time estimate
- [ ] **Scalability** (10%) — can it handle 100K+ books, 500K+ embeddings?
- [ ] **Feature richness** (10%) — built-in full-text search, JSON support, vector search, triggers
- [ ] **Community / ecosystem** (10%) — Go driver quality, documentation, long-term viability

Scoring:
- [ ] Score each backend 1-5 on each criterion
- [ ] Compute weighted total
- [ ] Separate recommendation per workload (e.g., "use PostgreSQL for metadata, keep chromem-go for vectors")

Sections in the decision document:
- [ ] Executive summary (one paragraph)
- [ ] Current architecture diagram
- [ ] Benchmark results table (all backends, all workloads)
- [ ] Scoring matrix
- [ ] Per-workload recommendation
- [ ] Migration path: phased plan if switching (which workload to migrate first)
- [ ] Risk assessment: what could go wrong during migration
- [ ] Decision: recommended approach + rationale

**Acceptance criteria:**
- [ ] Decision document is complete with all sections
- [ ] Benchmark data is included (not just opinions)
- [ ] At least one concrete recommendation per workload
- [ ] Migration cost estimate includes LOC changes and time

---

### Task 5: Migration cost analysis (1 PR)

**Goal:** For the recommended backend(s), estimate the concrete migration effort.

**Files:**
- Modify: `docs/superpowers/specs/2026-04-17-store-evaluation-results.md` — add migration section

Analysis to perform:
- [ ] Count all `Store` interface methods in `internal/database/store.go` — how many need reimplementation?
- [ ] Count all PebbleDB-specific code paths (key encoding, prefix scans, iterators)
- [ ] Identify data migration path: PebbleDB → new backend (export/import tool needed?)
- [ ] Identify feature gaps: anything PebbleDB does that PostgreSQL/SQLite cannot (prefix iteration, atomic batch writes)
- [ ] Estimate time: S/M/L per interface method, sum total
- [ ] Identify the migration order: which workloads to migrate first (lowest risk) vs last (highest coupling)
- [ ] Rollback plan: can we run both backends in parallel during migration? (dual-write approach)

**Acceptance criteria:**
- [ ] Every Store interface method is listed with its migration complexity (S/M/L)
- [ ] Total estimated effort in developer-days
- [ ] Migration order is justified
- [ ] Rollback plan is described

---

### Estimated effort

| Task | Size | Depends on |
|------|------|------------|
| 1 (harness + workloads) | M | -- |
| 2 (PostgreSQL adapter) | L | 1 |
| 3 (SQLite adapter) | M | 1 |
| 4 (evaluation + scoring) | M | 1, 2, 3 |
| 5 (migration analysis) | M | 4 |
| **Total** | ~5 PRs, L overall | |

### Critical path

Task 1 must be done first. Tasks 2 and 3 can run in parallel. Task 4 depends on benchmark results from 2 and 3. Task 5 depends on the recommendation from task 4.

### Constraints

- Benchmarks should run on the production server (<server>) to reflect real hardware. Also run on a dev machine for comparison.
- PostgreSQL benchmarks require Docker (for the PostgreSQL + pgvector container). The benchmark harness should detect if Docker is unavailable and skip gracefully.
- This is a research deliverable — no production code changes. The benchmark adapters are throwaway code; they do NOT need to implement the full Store interface, only enough for the benchmark workloads.
- The decision document should be reviewed by the project owner before any migration work begins.
