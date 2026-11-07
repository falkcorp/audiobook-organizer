<!-- file: docs/plans/platform-core/STATE.md -->
<!-- version: 1.40.0 -->
<!-- guid: 621bd548-9bb0-45e1-8e7b-31d3f7ef650e -->
<!-- last-edited: 2026-11-06 -->

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

## Execution

| Wave | Rows | Merged (PR, commit) | Notes |
|---|---|---|---|
| W01 | R0-01 | #4385 `98e06bfd2` | toolchain go1.27.3; drift 57 → 58 (a file-header `1.27.2`, not a pin) |
| W01 | R0-02 | #4388 `3169acbd6` | step-8 STOP: 3 tests asserted the defect; lead approved, 5 paths added to the inventory; bootstrap skill documents the 403 |
| W01 | R0-04 | #4387 `d27b893b2` | no drift |
| W01 | R0-06 | #4389 `32c432b12` | CodeQL `go/request-forgery` fixed with a release-tag match at the URL site; the AppConfig read ratchet kept by `config.Snapshot()` |
| W02 | R0-05 | #4391 `d617240ac` | T4 independent review: 0 BLOCKER, 1 MAJOR (blank saved password trimmed, invisible to P14) reverted to the brief, pinned by two rows; first PR through the merge queue |
| W02 | R0-03 | #4393 `1df727a55` | step-6 STOP: 2 master-list tests asserted the defect; lead approved, 1 path added to the inventory |
| W03 | OWNER-02 | — | owner-authorized run 2026-11-06: R0 production release **v0.223.0**, deployed, `RELEASE_FREEZE=R1`; recorded on #4382 |
| W04 | R1-35 | #4394 `7b2560718` | merge queue dropped it twice (concurrency cancel of the queue run), fixed by #4403; step-5 release checks clean |
| W05 | R1-01 | #4397 `88a962140` | owner overrode the 09 §9.2.10 move window ("no waiting"); Dockerfile `core/` COPY left to R1-04 per inventory |
| W06 | R1-02 | #4407 `ef186c16c` | e2e `merge_group` runs never cancelled and chromium-only (same check name as the PR run); ruleset +3 checks after merge |
| W06 | R1-11 | #4405 `2f774e3d8` | 8 writes through `Mutate`; `TestNoDirectAppConfigWrites` AST guard |
| W06 | R1-22 | #4406 `2fdf0b475` | pure move, 8185 decls byte-identical except NewPebbleStore/RunMigrations: 12 slog calls moved to `internal/logger` (owner decision; ratchet at its file ceiling) |
| W07 | R1-12 | #4411 `41042309b` | merged with Woodpecker staticcheck red (SA1029); follow-up #4413 also lets `MethodSessionDelegated` change credentials (owner decision, spec amendment in seams.d) |

Side PRs this period (not plan rows): #4400 `0f5f0d509` (promote dispatch with `GITHUB_TOKEN`), #4401 `cd7578a2c`
(github-common pins → `23ed8a4`, nightly e2e skipped: it starts no server), #4402 `18348c535` (v0.223.0 changelog
collected by hand), #4403 `5c6d4d598` (merge-queue runs never cancelled); github-common #386–#388 (Rust toolchain
input, minimatch override, changelog collection lands through a PR when direct push is refused).

Merge queue: on since 2026-11-05 (repo ruleset 26660812, REBASE, ALLGREEN, no bypass actors). Required checks (11, all
GitHub Actions): Changes, TODO Fragment Headers, Interface Width Ratchet, Errcheck Ratchet, Deadcode Ratchet, Repo
Guards, Mock Freshness, Coverage Floor (PR gate), and from R1-02 `extraction-gates`, `vet-darwin`, `vet-integration`.
`e2e` is **not** required yet (owner decision 2026-11-06): its `pull_request` `paths:` filter leaves docs-only PRs with
no E2E status; R1-05 edits e2e.yml. Woodpecker statuses are not required, so a PR can merge with them red. Enqueue with
the GraphQL `enqueuePullRequest` mutation: `gh pr merge` no longer enqueues.

Move windows (09 §9.2.10): the owner directed on 2026-11-06 that move rows (R1-01, R1-07, R1-10, R1-09, R1-08, …)
start as soon as their dependencies merge, announced on #4382, still running alone over the packages they move.

## Blockers

None for agents. R0 complete (6/6). R1: 6 of 37 rows merged (R1-35, R1-01, R1-02, R1-11, R1-22, R1-12).

## Next action

W07: R1-07 (#4410, move window open) to merge. Then W08 R1-10 (T3, move window) and W09 R1-03 (in progress) / R1-32.
Follow-ups in flight: #4413 (R1-12 follow-up), #4414 (vitest EventSource mock flake).
