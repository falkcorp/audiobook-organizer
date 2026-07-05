<!-- file: docs/agent-tasks/dedup-engine/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 368ccd37-d50a-47db-8b66-318b3b454e9b -->
<!-- last-edited: 2026-07-05 -->

# Workstream — dedup engine scan concurrency

Parallelize the four whole-library scan loops inside internal/dedup/engine.go — the confirmed prod bottleneck and its neighbors. From `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md` and the design spec `docs/specs/2026-07-05-concurrency-parallelization-design.md`.

**Execution mode:** SERIAL WAVES (coordinator-driven) — trigger: all four tasks edit internal/dedup/engine.go (collision table row 1); wave N+1 starts only after wave N merges and siblings rebase.

| Task | Src id | Title | Priority | Effort | Tier | Wave |
|------|--------|-------|----------|--------|------|------|
| TASK-01 | CONC-1 | Shard BookSignatureScan's O(n²) pairwise loop across a bounded worker pool | P1 | L | Opus-class | 1 |
| TASK-02 | CONC-2 | Parallelize FullScan's unified-scoring pass with a bounded worker pool | P1 | M | Sonnet-class | 2 |
| TASK-03 | CONC-3 | Parallelize AcoustIDScan's per-book loop with a bounded pool and guard its four shared maps | P2 | L | Sonnet-class | 3 |
| TASK-04 | CONC-4 | Parallelize FullScan main-pass Layer-1 checks while keeping Layer-2 embedding batching serial | P2 | L | Sonnet-class | 4 |

## Wave table

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01 | none | single task |
| 2 | TASK-02 | wave 1 merged + siblings rebased | shares `internal/dedup/engine.go` with wave 1 |
| 3 | TASK-03 | wave 2 merged + siblings rebased | shares `internal/dedup/engine.go` with wave 2 |
| 4 | TASK-04 | wave 3 merged + siblings rebased | shares `internal/dedup/engine.go` with wave 3 |

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

**Every task here edits `internal/dedup/engine.go`.** They MUST run in different waves (each serialized after the previous merges) — running them in parallel would produce a same-file merge conflict on every rebase cycle.

See [ORCHESTRATION.md](../ORCHESTRATION.md) (one level up) for the coordinator + worker protocol.
