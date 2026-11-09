<!-- file: docs/plans/platform-core/STATE.md -->
<!-- version: 1.52.0 -->
<!-- guid: 621bd548-9bb0-45e1-8e7b-31d3f7ef650e -->
<!-- last-edited: 2026-11-09 -->

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
| W07 | R1-07 | #4410 `f1f9936ca` | move window: internal/lifecycle and internal/logging into core (core/lifecycle, core/log); R1-12 follow-up #4413 `a206c56f8` merged alongside |
| W08 | R1-10 | #4430 `58456d7de` | move window; T3 with an independent Opus review: 0 BLOCKER, 0 MAJOR, 8 MINOR (served /metrics identical apart from the instance id); 3 MINORs fixed on the branch, 4 filed as todo.d; `.errcheck-baseline` 770 → 726 (earned by earlier PRs, R1-10 net zero); deployed 2026-11-07 10:10 |
| W09 | R1-03 | #4417 `73dd3dbb7` | per-module `make ci`: core ratchets, the core/4 shard, core in nightly, CodeQL and Woodpecker; core's errcheck exempts `errors.AsType` (matched at root by #4434) |
| W09 | R1-32 | #4436 `fbda477b0` | T3: independent review, verification review and re-verification, last verdict 0 BLOCKER / 0 MAJOR; 3 MINORs go to a follow-up PR before R1-36 touches core/log; deployed 2026-11-07 |
| W10 | R1-04 | #4438 `9f4534d5b` | docker-image PR job and the embed-context check; the first real image build ran in CI |
| W10 | R1-09 | #4437 `3caa19fff` | move window: core leaves (version 0.1.0, entity, coretest's injected logger) and the flight recorder into core/health |
| W11 | R1-05 | #4442 `a2489b830` | one web build entry point (`scripts/ci/build_web.sh`, `make web-deps` stamp); Dependabot `/core/web` entry deferred to R1-30 |
| W11 | R1-08 | #4444 `6e4bab4da` | move window: library leaves into core/library; split into a pure-move commit and a rewrite commit to pass the 90% rename-similarity move gate; deployed 2026-11-07 16:31 |
| W11 | R1-29 | #4443 `916303c18` | react-router-dom 7 to react-router 8.5.1 across 144 files; deployed 2026-11-07 16:31 |
| W11 | R1-36 | #4445 `1f3e980dd` | T3: independent review, re-verification and delta review, last verdict 0 BLOCKER / 0 MAJOR; CodeQL fixed in code (ABO-side `SanitizeLogValue` barrier kept at flagged sites; flight recorder quotes outside values with `%q`, a recorded deviation from brief step 7); deployed 2026-11-07 16:55 |
| W12 | R1-13 | #4450 `80077b7ea` | T4: independent review NOT MERGEABLE (1 BLOCKER: disabled modules' `/api/<id>/` and core's `/api/core/` never claimed; 3 MAJOR: shutdown budget overrun, native subtree routes, Go type names in 400 details; 10 MINOR), all fixed with tests; verification review mergeable plus a Router registration race fixed before merge; coined `Deps.RequestRestart` (owner asleep, recommended option); deadcode ratchet now runs per module and counts core's exported SDK surface as live |
| W13 | R1-14 | #4452 `31af8a05c` | T3: independent review found 1 BLOCKER (the "/" fallback ran gin handlers the mux never admitted, e.g. `GET /files/..`), fixed with a root handler admitting only GET/HEAD "/"; staticcheck S1021 fixed; dropped from the merge queue once by a Repo Guards timeout (slow runner disk cleanup, fixed by #4454 `6abf6e9dd`); deployed 2026-11-07 22:40 |
| W14 | R1-37 | #4458 `30906a80b` | T3: coretest in-process harness, client and `RoutesHavePermissions`. The check-commits-build move gate failed on a 73 %-similarity logcapture move, fixed by splitting a pure-move commit (`7642abc8f`) from the rewrite. The gate's per-commit fresh worktree had pushed a 33-commit PR past its timeout, fixed by #4461 `216401f74` (one reused worktree); deployed 2026-11-08 |
| W15 | R1-20 | #4453 `77f89c4f6` | T4: core storage engine, dark. The first independent review found 4 BLOCKERs and the verification review found more, all fixed. The final round made a preset filesystem win over `OpenOptions.FS` and renamed the identity key `dir_dev` to `dir_fsid`. 48 commits. The unsharded check-commits-build hit its 30-minute cap, so #4463 `7a88eb352` split it into 4 shards. The same PR made dashboard-reference survive a GitHub release CDN outage (504s): cached tarball, retry, then a module-proxy build of promtool. Deployed 2026-11-08 |
| W16 | R1-15 | #4467 `f464fabdc` | T4: composition root. Two review rounds; the verification round passed all 16 first-round items with 10 of 10 mutation probes caught. The HNSW snapshot export moved off the stop path, with a reconcile-on-boot replacing the keys sidecar and a 10 % tombstone ceiling. Re-cut as a new PR (#4455 closed) after the rebase onto R1-20. A shard runner crash on non-UTF-8 test output was fixed by #4464 `2cea11c6c`. One pre-existing gap (a crash that leaves the embedding row count unchanged is never reconciled) goes to a follow-up with a clean-stop marker. Deployed 2026-11-08 |
| W17 | R1-16 | #4470 `4881ead74` | T3: sdnotify moved into core; READY, STOPPING and EXTEND_TIMEOUT_USEC (only while starting) from the kernel. Reviewed MERGEABLE; re-cut onto main after R1-15 (r3). Deployed 2026-11-08: the Type=notify unit reached active on READY |
| W18 | R1-33 | #4474 `0ac2cf1dc` | T1: stop settings. Compose stop_grace_period 60s and launchd ExitTimeOut 60 against a 25 s core budget plus 10 s store drain; `ABO_CORE_SHUTDOWN_BUDGET` read at serve start (invalid value refuses to start, naming the variable). Re-cut twice (#4456, #4471) for the R1-15 wire and an R1-16 conflict. Deployed 2026-11-08 |
| W19 | R1-34 | #4477 `c78a9f681` | T3: explicit ServiceDefs lists replace init() registration. Independent review proved production parity (identical def and resolution-order dumps, base vs head); follow-ups pinned the resolution order, made the link test require every ServiceDefs package in the list, removed the vestigial Container.Include and bannered two stale architecture docs. Re-cut for R1-15 and R1-16 (#4472 closed). Deployed 2026-11-08 |
| W20 | R1-21 | #4480 `6dc509670` | T4: journaled restore swap (P0a) in core. Reviewed MERGEABLE; test-gap commits pin the Lstat, directory check and mode-first order, refuse device queries on an unknown filesystem and fail closed on a filesystem neither real nor in-memory. Re-cut onto main after the R1-15 conflict (#4466 closed). Not wired into the books boot yet. Deployed 2026-11-08 |
| W21 | R1-18 | #4485 `7d7a3474c` | T3: logging stages into core (redaction, file output, metrics bridge). Deviations: `subsystem` is a log attribute, not a message prefix (grep `subsystem=db-optimize`); context attributes follow the record's own; `kernel.Config.LogDir` is separate from R1-21's StateDir. Two CI rounds folded into the introducing commits (op-context attrs on the stdout default, one `database.Store` reference, metrics contract with a new `sink` key); #4481 and #4484 closed. Deployed 2026-11-08 |
| W22 | R1-17 | #4487 `9be9adc96` | T4: perimeter into core stages (peer, request ID, telemetry, recovery, access log, headers, CORS, cross-origin, compression) plus gin leaves. Two review rounds and a delta review (MERGEABLE, 0 blockers); deviation 16 gin half fixed (Basic gate 401 keeps `Vary: Origin` under gzip); deviation 15 accepted until F3. Reconciled with R1-18's Activity tap (access lines stay off the feed). Re-cut so every commit is green (#4478 closed). Deployed 2026-11-08 |
| W23 | R1-19 | #4488 `af4432e3c` | T2: build identity: `--version-json`, `--print-upgrade-floor`, `app.BinaryID`, UI build-ID meta and the stale-build banner; the docker-image job's `PROBE_BUILD_ID` probe is on and passed on the real image. Re-cut for R1-17 (#4482 closed). Deployed 2026-11-08; `--version-json` verified on the deployed binary |
| W24 | R1-30 | #4495 `f6f57bc7f` | T2: `core/web` npm workspace with `@mediacore/api-client` 0.1.0, consumed by `web/` through `file:`; both Docker frontend stages build it. Three CI rounds: CI's frontend job now builds the package first (`scripts/ci/ensure_core_web.sh`), minimatch overridden to 11.x under eslint only (GHSA-g3hv-75wg-m249), four permissive dev-dependency licences named in `allow-dependencies-licenses`, ruff format (#4483, #4491, #4494 closed). Deployed 2026-11-08 |
| W25 | R1-06 | #4498 `de0ab0a1d` | T2: report-only core version check in core's merge gate, `core-checkpoint` job in the production release, `make -C core readiness` with the SC scripts, and a nightly split-readiness fold-in through the CI bot app (not a PAT). Draft #4489 closed after the re-cut |
| W26 | R1-23 | #4504 `552d57c4f` | T4: storage flip: the kernel opens the one store at P2 and takes the pre-migration checkpoint before the first write; books reaches its rows only through strict claims (`ABO_CORE_STORAGE_CLAIMS_MODE=shadow` is the emergency dial; scans stay clipped either way). The review's six unregistered key families are covered by a registry-independent claim-violation guard. Re-cut three times (#4493, #4501 closed). Merged 2026-11-09 |
| W27 | R1-24 | #4507 `c8421370a` | T3: checkpoint sweeper, `prune-checkpoints` and authenticated storage tokens; deletes the plaintext-secrets copy the first R1 boot leaves, which ends the deploy hold after R1-23. Creates `core/app/offline.go` and `cmd/offline.go` ahead of R1-25's `OfflineCommands`. Re-cut onto main after R1-23 (#4500, #4502 and #4505 closed). Merged 2026-11-09 |

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

None for agents. R0 complete (6/6). R1: 32 of 37 rows merged (R1-35, R1-01, R1-02, R1-11, R1-22, R1-12, R1-07, R1-10, R1-03, R1-32, R1-04, R1-09, R1-05, R1-08, R1-29, R1-36, R1-13, R1-14, R1-37, R1-20, R1-15, R1-16, R1-33, R1-34, R1-21, R1-18, R1-17, R1-19, R1-30, R1-06, R1-23, R1-24).

Owner 2026-11-07: no owner gates and no burn-in periods; finish the refactor as fast as possible. Woodpecker runs only staticcheck and test-native-taglib from #4439 on; PRs gate on the GitHub test shards.

## Next action

W13 merged 2026-11-07: R1-14 (legacy mount and manifest). In progress in parallel: R1-15 (T4, composition root; its STOP on the measured 51–77 s production shutdown was answered by moving the HNSW snapshot export off the stop path and closing SSE streams before the drain), R1-20 (T4, storage engine, dark; review NOT MERGEABLE with 4 BLOCKERs, being fixed), R1-21 (T4, stacked on R1-20), R1-37 (T3, coretest harness) and R1-33 (T1, draft #4456, waits on R1-15 for its serve wire). Next after R1-15: R1-16, R1-17, R1-18, R1-34. In plain terms: the old web routes now run behind the new framework's router; nothing users see has changed.

W14 to W27 merged 2026-11-08 and 2026-11-09: R1-37, R1-20, R1-15, R1-16, R1-33, R1-34, R1-21, R1-18, R1-17, R1-19, R1-30, R1-06, R1-23 and R1-24. In flight as of 2026-11-09 11:30 EST:
- R1-25 (T4, offline restore and reset) is on round 7; its earlier PRs #4510, #4516 and #4518 are superseded.
- R1-27 is #4519 (draft, stacked on R1-25), then R1-28 is #4520 (draft, stacked on R1-27).
- R1-26 waits on R1-25.
- R1-31 (executive summary) last.

The R1-15 follow-up (HNSW clean-stop marker) merged as #4512 `0aa7d519c` on 2026-11-09 at 07:40 EST and was deployed at 07:45; it supersedes #4496, #4506 and #4476, and ports the earlier work onto core storage. It is not a plan row, so the R1 count is unchanged. The deploy hold between R1-23 and R1-24 is over: both are merged.

R1-23's deferred step 3 (engine re-home into core, the `database.Store` split, the Pebble collector move) and its kernel write gate (D14) become follow-up rows; its review confirmed no remaining R1 row depends on them. In plain terms: the server now starts, stops, logs and serves its web surface through the new framework, and reports its build identity; the new storage engine is built but not switched on yet.

Side PRs 2026-11-07 (not plan rows): CI flake fixes #4427, #4428, #4429, #4431 (memdb/Pebble listing tie-break), #4432;
CI bot app migration #4423 and github-common repin #4426 `6e7958c05`; Woodpecker cut to unique jobs #4439.

Side PR 2026-11-09 (not a plan row): #4508 `df3bd0449` (CI retry and prefetch: the R0 archive download retries transient errors, the Dockerfiles' curl calls fail and retry on HTTP errors, and the R0 archive is prefetched so `TestR0OpensR1Store` makes no CI download).
