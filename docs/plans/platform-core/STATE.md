<!-- file: docs/plans/platform-core/STATE.md -->
<!-- version: 1.38.1 -->
<!-- guid: 621bd548-9bb0-45e1-8e7b-31d3f7ef650e -->
<!-- last-edited: 2026-11-05 -->

# Platform-core planning operation — state

Evidence commit (HEAD of main at start): `748fd787a6d922c63dfb3bbb09da578a1418632e`.

| Item | Value |
|---|---|
| Spec branch | `docs/platform-core-spec-v2` (merged, deleted worktree); the 258-commit `docs/platform-core-spec` was closed unmerged as #4359 |
| Spec PR | #4360, rebase-merged into main (last spec commit on main `ac4368bdb`) |
| Spec revision branch | `docs/platform-core-spec-v3`: the D10–D14 revision and review rounds 13–15, cut from main as a separate PR |
| Plan branch | `docs/platform-core-plan`, on main after #4360 |
| Plan PR | #4376, rebase-merged into main |
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
| 5b | Spec revision for D10–D14 + review | done: every spec file revised (no backward compatibility, one cutover release R2 opened by the F2 foundation PRs); consistency pass; review round 13 (BLOCKER 2 / MAJOR 33 / MINOR 35), round 14 verification (1 / 13 / 19), round 15 re-check (0 / 4 / 7), every finding dispositioned and applied; 04 regenerated (v1.15.0, gates pass); see `reviews/spec-rounds-13-15.md`. Plan-gap fixes G1–G17 with review round 16 (0 / 3 / 7, applied); brief-sync gap fixes G18–G21 (05-rbac `CompileInput` and `Requirement.Kind()`, `core.outbox.*` audit actions, the log query's `limit`) and G22 (the library transfer-recovery schedule key); merged as #4368 |
| 6 | Implementation plan | done: `implementation-plan.md`, 134 tasks at merge (now 133 after D15, phase 10; 128 agent briefs under `docs/agent-tasks/platform-core/`, 6 owner checkpoints) in 46 waves; `task-summary.md`; `tools/inventory.json` and `tools/check_waves.py` (all six checks pass) |
| 7 | Plan review loop | done: rounds 1–3, BLOCKERs 4 → 1 → 0 (MAJOR 17 / 12 / 8), every finding applied; see `reviews/plan-rounds-1-3.md` |
| 8 | Plan advisor review | done: READY WITH CHANGES; accepted changes applied (review before T4 and flip merges, re-priced flips, the shell in R3, production-shaped rehearsal, freeze hotfix procedure, execution rules); two simplifications deferred to the owner |
| 9 | Plan confidentiality gate + PR + merge | done: gate PASS (scrub 0, gitleaks 0, manual review of the plan and 25 briefs); #4376 rebase-merged |
| 10 | Owner decisions D15 applied | done in this PR: spec 02 §2.6 and 14 §14.3.2; plan, inventory (133 tasks: R2-02 folded into OWNER-05, R1-31 now R1's executive summary), briefs, task summary and executive summary updated; all six checks pass |

## Artifacts

- `docs/plans/platform-core/raw/00-index.md` … `16-arr-domain-research.md`
- `docs/specs/platform-core/` README + 01–15 (34 files, ~41k lines); 03 is normative
- `docs/plans/platform-core/reviews/`: 12 review rounds, the advisor review, and a dispositions file for each; `spec-rounds-13-15.md` summarises rounds 13–15
- `docs/plans/platform-core/executive-summary.md`: plain-language summary of the revised design
- `changelog.d/2026-11-04_platform_core_spec.md`; supersede marker on `docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`
- `todo.d/2026-11-04_platform_core_followups.md` (out-of-scope defects, incl. the six live security hotfixes of 05-authn §11.1)

## Owner decision D10 (2026-11-04)

No backward compatibility: data formats, storage layouts, key names, HTTP paths, metric names, config keys and env names may change; dashboards and alert rules are updated to match. This supersedes the representation-neutral extraction (A-2) and every HEAD-parity, alias, compat-ledger and Gone-step mechanism. Working assumptions pending owner correction: user data is migrated forward one-way (never dropped); rollback = restore the pre-upgrade backup plus the old binary; third-party client APIs (the Audiobookshelf-compatible surface) keep their wire contract; the six security hotfixes stand; soak count and length are an owner choice. The brief-integration pass was stopped; an impact analysis is running, then the spec, plan and briefs are revised.

## Owner decision D11 (2026-11-04)

No soaks: at most an optional 1-day smoke run. Optimise for the shortest path to the finished extraction (a complete product fixed forward over a long staged project). Fewer, larger releases are fine. Safety kept: backup before migration, restore as rollback, the test suites.

## Owner decision D12 (2026-11-04)

F1: auto-update paused from the first R2 flip until R2 ships, so R2 is one cutover release. F2: API keys and ABS phone-app sessions survive the cutover; web sessions and one-time tokens reset; task and log history kept. F3: the only frozen external contract is the Audiobookshelf-compatible API (verified: unprefixed root routes mounted by `internal/server/wire_abs_routes.go`; the ABS handlers contain no webhook code); everything else may change. F4: the pre-migration checkpoint auto-deletes after 7 days. Releases: R0 hotfixes, R1 core runtime, R2 cutover, R3 cleanup. The spec is being revised by nine parallel revisers on disjoint files.

## Owner decision D13 (2026-11-04)

Q-H: the core module is `github.com/falkcorp/mediacore`. Q-B: the core repository is public. Q-A: licence Apache-2.0. A user (or its keys/identities) that cannot be converted at the R2 cutover stops the upgrade with a report naming the row, fixed or deleted by a CLI command (no quarantine-and-continue for identity). An account merge in progress refuses the R2 migration until it is finished; finished merges become permanent (no undo after R2). No old-URL redirects in the UI shell (`UIDescriptor.LegacyRedirects` and `legacyStorageKeys` removed).

## Owner decision D14 (2026-11-04)

ABO keeps PolyForm Noncommercial 1.0.0 (already its licence) and adds a commercial-licensing notice (`COMMERCIAL.md`, contact through the GitHub repository; no email in the repo). Code moving into mediacore is relicensed to Apache-2.0 at the split (sole copyright holder). Recorded in 12.

## Owner decision D15 (2026-11-05)

Q-I (a): a ruleset on `main` requiring the named checks, no bypass actors, GitHub's merge queue; `/parallel-sweep` never
admin-merges. R1 freeze kept. Q-G accepted as proposed. Q-C: the 15 v2 `Schedule` strings are deleted; the 14 defs a
scheduler `TaskDefinition` fires keep that cadence; `deluge.protected-paths-sync` stays manual. P14: Branch A. Advisor
S2 adopted (no flip-upgrade test; M8 registered once as `books.rewrite`, kept last). Advisor S3 + S5 adopted (the
freeze is the repository variable `RELEASE_FREEZE`, toggled by the owner in OWNER-02, OWNER-03, before F3-08 and in
OWNER-05; R2-02 removed). Recorded in spec 02 §2.6 and OWNER-01.

## Blockers

None. (The #4359 merge refusal was cleared by re-cutting the branch as #4360.)

## Next action

Owner: apply the `main` ruleset with the required checks, no bypass actors and the merge queue (OWNER-01's one open action), and log it in the tracking issue. W01 proceeds meanwhile: R0-05 is unblocked (P14 Branch A). R1-01 waits for the ruleset, R1-35's merge and `RELEASE_FREEZE` set to `R1` (OWNER-02's last step).

## Incidents

- 2026-11-04: one commit (`b45193d9f`) was pushed while the per-commit gate reported hits, because the gate's exit status was hidden by a pipe. All hits were verified false positives (long identifiers; a header name); nothing confidential was pushed; no history rewrite was needed. The gate now runs under `pipefail`, stages only named paths, and blocks on any hit.
- 2026-11-04: the shared disk filled up (a 25 GB Go build cache); it was cleared with `go clean -cache` and nothing was lost.
- 2026-11-04: the plan branch was rebased onto main after #4360 merged (spec trees identical).
- 2026-11-04: the branch was rebased onto 17 new main commits (no overlap), and in-doc commit references were remapped.
- Module-layout experiments: an early batch used a reset that did not undo staged renames; those runs were discarded and re-run cleanly (`raw/00-baseline-build.md`).

## Fallbacks in use

- Subagents use `general-purpose` / `Explore` with explicit model and effort.

COMPLETED: 10 — phases 1–9 and 5b (raw data, spec, spec review, spec advisor, spec gate + #4360, D10–D14 revision + #4368, plan, plan review, plan advisor, plan gate + #4376) and phase 10 (D15 applied, this PR)
REMAINING: 1 — the owner applies the main ruleset and merge queue (OWNER-01), then execution from W01
BLOCKED: 0
