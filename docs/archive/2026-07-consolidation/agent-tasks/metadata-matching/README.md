<!-- file: docs/agent-tasks/metadata-matching/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 59ee5025-d4d9-4f08-8ba9-d2a35250f7d1 -->
<!-- last-edited: 2026-07-10 -->

# Workstream — Metadata Matching Pipeline (INIT-3)

Make match scoring tunable (config extraction with value-identical defaults + Settings UI +
read-only calibration harness), unify the two conflicting duration scorers, parallelize the bulk
metadata fetch within provider rate limits, close the cache TOCTOU window, and implement
author/series resolution (metadata history REUSES the already-shipping `MetadataChangeRecord`
subsystem — review descoped the history build to retiring a dead stub). From INIT-3 of
`.claude/notes/2026-07-10-remaining-work-master-plan.md` (tasks T1–T6) and
`docs/specs/2026-07-10-metadata-matching-design.md` (components C1–C8). Taskboard:
`docs/plans/2026-07-10-metadata-matching.md`.

**Initiative gate (verbatim, applies to every task):** PLAN -> EXECUTE AUTONOMOUSLY
(worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero
behavior change until an operator tunes them.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | INIT-3-T2 | Unify the two duration-scoring functions | P1 | M | Sonnet-class | 1 |
| TASK-02 | INIT-3-T1 | Extract scoring literals into MetadataScoringConfig | P1 | L | Sonnet-class | 2 |
| TASK-03 | INIT-3-T1 | Settings UI for the new scoring knobs | P2 | M | Sonnet-class | 3 |
| TASK-04 | INIT-3-T1 | Read-only scoring calibration harness op | P2 | L | Sonnet-class | 3 |
| TASK-05 | INIT-3-T3 | Parallelize bulk metadata fetch (rate-limit-aware) ⚠ | P1 | L | Sonnet-class ⚠ | 1 |
| TASK-06 | INIT-3-T4 | Author/series ID resolution + history-stub retirement ⚠ | P2 | M | Sonnet-class ⚠ | 1 |
| TASK-07 | INIT-3-T5 | TOCTOU cache SourceHash validation | P2 | M | Sonnet-class | 1 |
| TASK-08 | INIT-3-T6 | Token-set fuzzy upgrade (OPTIONAL) | P3 | M | Sonnet-class | 1 |

## Ground rules

- Scope: Go backend (`internal/metafetch/`, `internal/config/`, `internal/server/`,
  `internal/metadata/`, `internal/matcher/`, `internal/plugins/metafetch/`) + React frontend
  (`web/src/`, TASK-03 only). Briefs are standalone-mode: each owns worktree + branch + PR;
  non-review-critical briefs also self-merge with `gh pr merge --rebase`. **TASK-05 and TASK-06
  (⚠ review-critical) contain NO merge command in ANY mode** — they stop at the open PR;
  coordinator/human line-by-line review is a hard merge precondition. Under a coordinated sweep
  the coordinator owns push/PR/merge for every task.
- Build + test gate for every task in this workstream:
  ```bash
  make ci
  ```
  staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you
  changed; the merge gate is Minimal CI green.
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a
  starting point, not a guarantee. Zero hits = STOP and report.
- Conventional commits; version headers bumped on every touched file; never commit to main.

## Collision / wave note

**TASK-01 and TASK-02 both edit `internal/metafetch/service_scoring.go` (and its test file).**
They MUST run in different waves (TASK-02 serialized after TASK-01 merges) — running them in
parallel would produce a same-file merge conflict on every rebase cycle. Resolution:
`serialize: wave1=TASK-01, wave2=TASK-02`.

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01, TASK-05, TASK-06, TASK-07, TASK-08 | none | disjoint file sets (see collision table in the plan) |
| 2 | TASK-02 | wave 1 merged + siblings rebased | shares `service_scoring.go` + test with TASK-01 |
| 3 | TASK-03, TASK-04 | TASK-02 merged (config fields are the contract) | disjoint from each other (web/src vs internal/plugins/metafetch) |

Execution modes (from the plan): W1 = /parallel-sweep (5 independent tasks ≥3 threshold, disjoint
files); W2 = SINGLE-AGENT (1 task, <3 threshold); W3 = SERIAL WAVES coordinator-driven (2 tasks,
<3 threshold; may dispatch concurrently — disjoint files).

Cross-initiative caution: TASK-05's `internal/server/metadata_ops.go` — confirm no concurrent
INIT-9 / INIT-10 wave touches that file before dispatch. TASK-08 is optional (P3); dropping it
blocks nothing.

The coordinator protocol (verbatim) is embedded in
[`docs/plans/2026-07-10-metadata-matching.md`](../../plans/2026-07-10-metadata-matching.md) — the
plan's copy is authoritative for this workstream.
