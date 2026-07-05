<!-- file: docs/agent-tasks/bulk-ops-pools/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7a10dca8-e134-4976-88d1-2a1d1ab7cf69 -->
<!-- last-edited: 2026-07-05 -->

# Workstream — bulk + request-driven op pools

Add conservative pools to the request-driven bulk metadata ops and the two remaining N+1 backfill reads. From `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md` and the design spec `docs/specs/2026-07-05-concurrency-parallelization-design.md`.

**Execution mode:** /parallel-sweep — trigger: 4 mechanically-similar tasks (≥3 threshold), disjoint files per the collision matrix, gate = `make ci`. Invocation: TASK-01,02,03,04 all in wave 1.

| Task | Src id | Title | Priority | Effort | Tier | Wave |
|------|--------|-------|----------|--------|------|------|
| TASK-01 | CONC-12 | Parallelize bulkFetchMetadataImpl over req.BookIDs with a conservative request-scoped pool | P3 | M | Sonnet-class | 1 |
| TASK-02 | CONC-13 | Parallelize BatchUpdateMetadata and ImportMetadata per-item DB round-trips | P3 | M | Sonnet-class | 1 |
| TASK-03 | CONC-14 | Parallelize the duration-backfill per-book GetBookFiles loop | P3 | S | Haiku-class | 1 |
| TASK-04 | CONC-15 | Parallelize the itunes path-reconcile per-book GetBookFiles loop | P3 | S | Haiku-class | 1 |

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
