<!-- file: docs/agent-tasks/acoustid-consolidation/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 19cfbd09-ff52-4e17-a607-d9fa1fc14c61 -->
<!-- last-edited: 2026-07-05 -->

# Workstream — acoustid backfill consolidation

Delete the serial server-side AcoustID backfill duplicate and route startup fingerprinting to the already-parallel plugin op. From `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md` and the design spec `docs/specs/2026-07-05-concurrency-parallelization-design.md`.

**Execution mode:** SINGLE-AGENT (strong model) — trigger: judgment work (delete/redirect a code path with caller wiring + a shared helper to check); ⚠ review-critical, coordinator line-review before merge.

| Task | Src id | Title | Priority | Effort | Tier | Wave |
|------|--------|-------|----------|--------|------|------|
| TASK-01 | CONC-9 | Delete the serial server-side AcoustID backfill and route startup to the parallel plugin op | P2 | M | Opus-class | 1 |

## Wave table

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01 | none | single task |

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
