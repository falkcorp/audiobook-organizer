<!-- file: docs/plans/platform-core/STATE.md -->
<!-- version: 1.7.0 -->
<!-- guid: 621bd548-9bb0-45e1-8e7b-31d3f7ef650e -->
<!-- last-edited: 2026-11-04 -->

# Platform-core planning operation — state

Evidence commit (HEAD of main at start): `748fd787a6d922c63dfb3bbb09da578a1418632e`.

| Item | Value |
|---|---|
| Spec branch | `docs/platform-core-spec` |
| Spec PR | (not opened) |
| Plan branch | `docs/platform-core-plan` (not created) |
| Plan PR | (not opened) |
| plan-op plugin | not installed; following the prompt's §5 manually |
| Advisor mechanism | (TBD) |

## Phases

| # | Phase | Status |
|---|---|---|
| 1 | Raw data gathering | done (17 raw files incl. raw/00-baseline-build.md) |
| 2 | Design spec | done (draft v1): 34 files under docs/specs/platform-core/ |
| 3 | Spec review loop | R1: 192 (20 BLOCKER); R2: 162 (10); R3: 130 (9) — R3 resolution R3-1 replaces the dual-format rollback machinery with one-stored-format-per-family adapters (cutover deferred); 8 revisers applying; round 4 next |
| 4 | Spec advisor review | pending |
| 5 | Spec confidentiality gate + PR + merge | pending |
| 6 | Implementation plan | pending |
| 7 | Plan review loop | pending |
| 8 | Plan advisor review | pending |
| 9 | Plan confidentiality gate + PR + merge | pending |

## Artifacts

- `docs/plans/platform-core/raw/00-index.md` … `16-arr-domain-research.md`
- `docs/specs/platform-core/` README + 01–15 (34 files, ~18k lines); 03 is normative
- `todo.d/2026-11-04_platform_core_followups.md` (out-of-scope defects, incl. 3 live security hotfix items)

## Blockers

(none)

## Next action

Round 3 revision (8 revisers; rules: `reviews/spec-round-3-dispositions.md`, R3-1…R3-5). Then integration pass, then round 4 review (7 fresh reviewers). Exit criterion unchanged: zero BLOCKERs and every MAJOR dispositioned.

## Incidents

- 2026-11-04: one commit (`01247778b`) was pushed while the per-commit gate reported hits, because the gate's exit status was hidden by a pipe. All hits were verified false positives (long identifiers; a header name); nothing confidential was pushed; no history rewrite was needed. The gate now runs under `pipefail`, stages only named paths, and blocks on any hit.
- Module-layout experiments: an early batch used a reset that did not undo staged renames; those runs were discarded and re-run cleanly (`raw/00-baseline-build.md`).

## Fallbacks in use

- Subagents use `general-purpose` / `Explore` with explicit model and effort.

COMPLETED: 2 — phase 1 (raw data), phase 2 (spec draft v1)
REMAINING: 7 — phases 3–9
BLOCKED: 0 —
