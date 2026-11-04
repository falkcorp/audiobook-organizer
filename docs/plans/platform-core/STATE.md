<!-- file: docs/plans/platform-core/STATE.md -->
<!-- version: 1.22.0 -->
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
| 3 | Spec review loop | distinct BLOCKERs per round: R1 20, R2 10, R3 9, R4 6, R5 5, R6 2, R7 1, R8 0 — rounds 1–6 dispositioned and applied (+ integration passes); round 7 (96 findings: 1 BLOCKER, 31 MAJOR, 64 MINOR) dispositioned and applied (8 cluster revisers + integration pass; 04 regenerated, 213 rows, gates pass); round 8 (80 findings: 0 BLOCKER, 31 MAJOR, 49 MINOR) meets the exit rule; dispositioned (R8-1..R8-6) and applied (8 cluster revisers + integration pass; 04 v1.8.0 regenerated, 213 rows, gates pass) |
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

Phase 4 advisor review done (`reviews/spec-advisor-review.md`: READY WITH CHANGES, 1 BLOCKER, 6 MAJOR, 5 MINOR, 9 simplifications). All accepted and all simplifications adopted (`reviews/spec-advisor-dispositions.md`, A-1..A-17). Headline: a representation-neutral extraction (A-2) where legacy stores keep HEAD formats through S10, and hardening moves to post-S10 cutovers. The round-8 design is recoverable at commit 2a54afb5f. Applied by 8 cluster revisers plus an integration pass (09 v1.9.1, 04 v1.9.0 regenerated, informative). Confirmation round 9 (5 lenses): 71 findings, 4 BLOCKER (3 distinct), 27 MAJOR, 40 MINOR (`reviews/spec-round-9-dispositions.md`, R9-1..R9-11). All three blockers are HEAD-parity gaps where core now drives HEAD code (commit-time locks, cross-owner route conflict, duplicate metric family). No deleted machinery comes back. Applied by 8 revisers plus an integration pass (09 v1.10.1, 04 v1.10.0 regenerated). Confirmation round 10 (same 5 lenses) is running; exit when zero BLOCKERs. Then Phase 5. Changelog fragment and supersede marker are already committed.

## Incidents

- 2026-11-04: one commit (`01247778b`) was pushed while the per-commit gate reported hits, because the gate's exit status was hidden by a pipe. All hits were verified false positives (long identifiers; a header name); nothing confidential was pushed; no history rewrite was needed. The gate now runs under `pipefail`, stages only named paths, and blocks on any hit.
- Module-layout experiments: an early batch used a reset that did not undo staged renames; those runs were discarded and re-run cleanly (`raw/00-baseline-build.md`).

## Fallbacks in use

- Subagents use `general-purpose` / `Explore` with explicit model and effort.

COMPLETED: 3 — phase 1 (raw data), phase 2 (spec draft v1), phase 3 (8 review rounds)
REMAINING: 6 — phases 4–9 (phase 4: confirmation round 10 running)
BLOCKED: 0 —
