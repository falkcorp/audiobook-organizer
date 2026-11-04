<!-- file: docs/plans/platform-core/STATE.md -->
<!-- version: 1.17.0 -->
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
| 3 | Spec review loop | distinct BLOCKERs per round: R1 20, R2 10, R3 9, R4 6, R5 5, R6 2, R7 1, R8 0 — rounds 1–6 dispositioned and applied (+ integration passes); round 7 (96 findings: 1 BLOCKER, 31 MAJOR, 64 MINOR) dispositioned and applied (8 cluster revisers + integration pass; 04 regenerated, 213 rows, gates pass); round 8 (80 findings: 0 BLOCKER, 31 MAJOR, 49 MINOR) meets the exit rule; dispositioned (R8-1..R8-6), revisers applying |
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

Round 8 had zero BLOCKERs, so the loop exits. Eight cluster revisers are applying the round-8 dispositions (R8-1: factory reset becomes a kind of the one restore swap). Then an integration pass, then Phase 4 (single fresh max-effort reviewer, since no advisor tool exists).

## Incidents

- 2026-11-04: one commit (`01247778b`) was pushed while the per-commit gate reported hits, because the gate's exit status was hidden by a pipe. All hits were verified false positives (long identifiers; a header name); nothing confidential was pushed; no history rewrite was needed. The gate now runs under `pipefail`, stages only named paths, and blocks on any hit.
- Module-layout experiments: an early batch used a reset that did not undo staged renames; those runs were discarded and re-run cleanly (`raw/00-baseline-build.md`).

## Fallbacks in use

- Subagents use `general-purpose` / `Explore` with explicit model and effort.

COMPLETED: 2 — phase 1 (raw data), phase 2 (spec draft v1)
REMAINING: 7 — phases 3–9 (phase 3: round-8 revision in progress; loop exit rule met)
BLOCKED: 0 —
