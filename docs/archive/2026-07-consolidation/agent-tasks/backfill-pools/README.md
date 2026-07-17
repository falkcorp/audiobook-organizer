<!-- file: docs/agent-tasks/backfill-pools/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: a7ec7f2e-4374-4507-877a-24710f63f093 -->
<!-- last-edited: 2026-07-05 -->

# Workstream — backfill worker pools

Add bounded worker pools (registry.RunItems) to the embarrassingly-parallel whole-library backfills, each in its own file. From `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md` and the design spec `docs/specs/2026-07-05-concurrency-parallelization-design.md`.

**Execution mode:** /parallel-sweep — trigger: 4 mechanically-similar tasks (≥3 threshold), disjoint files per the collision matrix, gate = `make ci`. Invocation: TASK-01,02,03,04 all in wave 1.

| Task | Src id | Title | Priority | Effort | Tier | Wave |
|------|--------|-------|----------|--------|------|------|
| TASK-01 | CONC-5 | Parallelize dedup.embed-scan sync path with a rate-limited worker pool | P2 | M | Sonnet-class | 1 |
| TASK-02 | CONC-6 | Parallelize the startup embedding backfill loops over books and authors | P2 | M | Sonnet-class | 1 |
| TASK-03 | CONC-7 | Parallelize the tag-backfill ExtractMetadata loop with a CPU-sized pool | P2 | M | Sonnet-class | 1 |
| TASK-04 | CONC-8 | Add book-lookup memoization then parallelize mine_gold_labels and dataset_backfill | P2 | L | Sonnet-class | 1 |

## Wave table

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01, TASK-02, TASK-03, TASK-04 | none | disjoint file sets (see collision table) |

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

These tasks touch **disjoint files** and all run concurrently in wave 1 — no same-file collision.

See [ORCHESTRATION.md](../ORCHESTRATION.md) (one level up) for the coordinator + worker protocol.
