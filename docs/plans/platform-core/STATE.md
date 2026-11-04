<!-- file: docs/plans/platform-core/STATE.md -->
<!-- version: 1.29.0 -->
<!-- guid: 621bd548-9bb0-45e1-8e7b-31d3f7ef650e -->
<!-- last-edited: 2026-11-04 -->

# Platform-core planning operation — state

Evidence commit (HEAD of main at start): `748fd787a6d922c63dfb3bbb09da578a1418632e`.

| Item | Value |
|---|---|
| Spec branch | `docs/platform-core-spec-v2` (merged, deleted worktree); the 258-commit `docs/platform-core-spec` was closed unmerged as #4359 |
| Spec PR | #4360, rebase-merged into main (last spec commit on main `ac4368bdb`) |
| Plan branch | `docs/platform-core-plan`, on main after #4360 |
| Plan PR | (not opened) |
| plan-op plugin | not installed; following the prompt's §5 manually |
| Advisor mechanism | no `advisor` tool exists; a single fresh reviewer at max effort (`reviews/spec-advisor-review.md`) |

## Phases

| # | Phase | Status |
|---|---|---|
| 1 | Raw data gathering | done (17 raw files incl. raw/00-baseline-build.md) |
| 2 | Design spec | done: 34 files under docs/specs/platform-core/ (~41k lines) |
| 3 | Spec review loop | done: 8 rounds of 7 antagonistic lenses, distinct BLOCKERs 20 → 10 → 9 → 6 → 5 → 2 → 1 → 0; every finding dispositioned (`reviews/spec-round-N-dispositions.md`) |
| 4 | Spec advisor review | done: READY WITH CHANGES; all findings accepted, simplifications adopted (A-1..A-17); then confirmation rounds 9–12 with BLOCKERs 3 → 2 → 1 → 0 |
| 5 | Spec confidentiality gate + PR + merge | done: gate PASS; #4359 (258 commits) was refused by GitHub (`This branch can't be rebased`), so the same tree was re-cut as 5 logical commits in #4360, re-gated (scrub 0, gitleaks 0) and rebase-merged |
| 6 | Implementation plan | in progress: 20 cross-file spec conflicts resolved (03, 05-*, 09, 12; 04 regenerated as v1.11.4); `implementation-plan.md` and the workstream index written (153 rows: 120 agent PRs, 33 OWNER checkpoints, 22 waves); briefs being written |
| 7 | Plan review loop | pending |
| 8 | Plan advisor review | pending |
| 9 | Plan confidentiality gate + PR + merge | pending |

## Artifacts

- `docs/plans/platform-core/raw/00-index.md` … `16-arr-domain-research.md`
- `docs/specs/platform-core/` README + 01–15 (34 files, ~41k lines); 03 is normative
- `docs/plans/platform-core/reviews/`: 12 review rounds, the advisor review, and a dispositions file for each
- `changelog.d/2026-11-04_platform_core_spec.md`; supersede marker on `docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`
- `todo.d/2026-11-04_platform_core_followups.md` (out-of-scope defects, incl. the six live security hotfixes of 05-authn §11.1)

## Blockers

None. (The #4359 merge refusal was cleared by re-cutting the branch as #4360.)

## Next action

Phase 6: write one brief per inventory row in `docs/agent-tasks/platform-core/` (nine writers, disjoint waves), then link them from the index. Open spec gap: the H1–H3 cutovers have no stage tables in 09 §9.13 (and H1's storage layout is undecided), so those briefs are SPEC-GATED.

## Incidents

- 2026-11-04: one commit (`b45193d9f`) was pushed while the per-commit gate reported hits, because the gate's exit status was hidden by a pipe. All hits were verified false positives (long identifiers; a header name); nothing confidential was pushed; no history rewrite was needed. The gate now runs under `pipefail`, stages only named paths, and blocks on any hit.
- 2026-11-04: the shared disk filled up (a 25 GB Go build cache); it was cleared with `go clean -cache` and nothing was lost.
- 2026-11-04: the plan branch was rebased onto main after #4360 merged (spec trees identical).
- 2026-11-04: the branch was rebased onto 17 new main commits (no overlap), and in-doc commit references were remapped.
- Module-layout experiments: an early batch used a reset that did not undo staged renames; those runs were discarded and re-run cleanly (`raw/00-baseline-build.md`).

## Fallbacks in use

- Subagents use `general-purpose` / `Explore` with explicit model and effort.

COMPLETED: 5 — phases 1–5 (raw data, spec, review loop, advisor review, gate + PR #4360 merged)
REMAINING: 4 — phase 6 (briefs), phase 7 (plan review), phase 8 (plan advisor), phase 9 (gate + PR #2)
BLOCKED: 0
