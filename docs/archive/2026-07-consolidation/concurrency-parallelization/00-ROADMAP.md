<!-- file: docs/concurrency-parallelization/00-ROADMAP.md -->
<!-- version: 1.0.0 -->
<!-- guid: 334fbe0c-c953-40ef-99fc-90053336cb1f -->
<!-- last-edited: 2026-07-05 -->

# Concurrency Parallelization — Roadmap

Initiative index for the 5 workstreams that turn `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md` into
execution-ready work. Design rationale + locked decisions: [`docs/specs/2026-07-05-concurrency-parallelization-design.md`](../specs/2026-07-05-concurrency-parallelization-design.md).
Fan-out plan + buckets: [`../agent-tasks/BREAKDOWN-2026-07-05.md`](../agent-tasks/BREAKDOWN-2026-07-05.md).
Implementation plan with the full dependency graph: [`../plans/2026-07-05-concurrency-parallelization.md`](../plans/2026-07-05-concurrency-parallelization.md).

> Terminology note: **Rank** below is workstream execution order (0 = do first),
> distinct from a task's **plan-op Tier** (S/M/L output size) and its **model tier**
> (Haiku/Sonnet/Opus-class). All three appear in this package; do not conflate them.

## Workstreams by rank

| Rank | Workstream | What | Priority | Tasks | Execution mode |
|---|---|---|---|---|---|
| 1 | [`dedup-engine/`](../agent-tasks/dedup-engine/) | dedup engine scan concurrency | P1 | 4 | SERIAL WAVES (coordinator-driven) |
| 2 | [`backfill-pools/`](../agent-tasks/backfill-pools/) | backfill worker pools | P2 | 4 | /parallel-sweep |
| 3 | [`acoustid-consolidation/`](../agent-tasks/acoustid-consolidation/) | acoustid backfill consolidation | P2 | 1 | SINGLE-AGENT (strong model) |
| 4 | [`itunes-import/`](../agent-tasks/itunes-import/) | itunes import concurrency | P2 | 2 | SERIAL WAVES (coordinator-driven) |
| 5 | [`bulk-ops-pools/`](../agent-tasks/bulk-ops-pools/) | bulk + request-driven op pools | P3 | 4 | /parallel-sweep |

## Sequencing

- **Across workstreams:** every workstream touches a disjoint file set, so all 5 can run
  concurrently. Rank orders them by value/risk when capacity is limited — do WS-1
  (the confirmed prod incident) first.
- **Within a workstream:** collision-bound workstreams (WS-1 `engine.go`, WS-4
  `importer.go`) serialize their tasks into waves; the rest run their tasks in one
  parallel wave. See each workstream's `README.md` wave table and `orchestration.md`.

## Deferred (not in this initiative)

- **Bucket 2 (needs design first):** AutoResolveCertain, applyRegroupPlan — correctness-constrained; see the BREAKDOWN.
- **Bucket 3 (operational / low value):** PurgeStaleCandidates, One-time migrations/backfills.
