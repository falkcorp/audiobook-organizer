<!-- file: docs/agent-tasks/itunes-import/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e4c5837-1484-4266-8c1a-15981df6739f -->
<!-- last-edited: 2026-07-05 -->

# Workstream — itunes import concurrency

Parallelize the two whole-library import passes in importer.go: file-organize (I/O) and metadata-enrich (rate-limited, circuit-breaker). From `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md` and the design spec `docs/specs/2026-07-05-concurrency-parallelization-design.md`.

**Execution mode:** SERIAL WAVES (coordinator-driven) — trigger: both tasks edit internal/itunes/service/importer.go (collision table row 2); enrich (TASK-02) serializes after organize (TASK-01).

| Task | Src id | Title | Priority | Effort | Tier | Wave |
|------|--------|-------|----------|--------|------|------|
| TASK-01 | CONC-10 | Parallelize organizeImportedBooks file-organize + UpdateBook over imported books | P2 | M | Sonnet-class | 1 |
| TASK-02 | CONC-11 | Parallelize enrichImportedBooks metadata fetch with a bounded pool preserving the rate-limit circuit-breaker | P3 | L | Sonnet-class | 2 |

## Wave table

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01 | none | single task |
| 2 | TASK-02 | wave 1 merged + siblings rebased | shares `internal/itunes/service/importer.go` with wave 1 |

## Ground rules

- Go only, in the files named by each task; every change is additive (parallelize an existing loop; identical output to the serial version).
- Reuse `registry.RunItems` (`internal/operations/registry/run_items.go`) — do NOT hand-roll a worker pool or add a new concurrency constant.
- Build + test gate for every task in this workstream:
  ```bash
  make ci
  ```
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee.
- Every pool change ships a `go test -race` proving no data race on the shared state the brief names.

## Collision / wave note

**Every task here edits `internal/itunes/service/importer.go`.** They MUST run in different waves (each serialized after the previous merges) — running them in parallel would produce a same-file merge conflict on every rebase cycle.

See [ORCHESTRATION.md](../ORCHESTRATION.md) (one level up) for the coordinator + worker protocol.
