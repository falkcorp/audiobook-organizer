<!-- file: docs/plans/platform-core/implementation-plan.md -->
<!-- version: 2.1.1 -->
<!-- guid: e0650f4a-4aec-4453-a256-aed84bf5ef1c -->
<!-- last-edited: 2026-11-05 -->

# mediacore — implementation plan (Phase 6, R0–R3)

**Status:** draft for the plan review loop (Phase 7). It replaces the staged plan (S0–S10, T0–T8, H1–H3),
which described the design before the owner decisions D10–D14.

**Inputs.** The spec under `docs/specs/platform-core/`: 03 is normative and defines the release keys R0–R3,
the flip labels F2–F9 and the migration steps M1–M8 (03 §3.8); 09 owns sequencing, verification, rollback
and the owner's part (09 §9.2–§9.4); the 05-* files own each mechanism; 06 §6.4.10 lands each SDK member;
07 §7.19 orders the UI work; 12 sets the split criteria; 13 the toolchain bump; 14 the risks; 04 is the
informative package map keyed by release and flip. The owner decisions are recorded in
[`STATE.md`](STATE.md) (D10–D14) and summarised in [`executive-summary.md`](executive-summary.md).
**Evidence commit:** `748fd787a`; every count is informative and every brief regenerates the counts it
needs at its own base commit (09 §9.3). The task file lists, importer and caller lists included, were computed
at the plan's base, `f7ac5b11e`, a later commit of `main`.

The workstream index for agents is
[`docs/agent-tasks/platform-core/README.md`](../../agent-tasks/platform-core/README.md): one row per task
with its wave, tier, dependencies, files and spec sections. The same rows exist as JSON for the brief
writers.

**Size: 129 rows: 123 agent tasks, each one PR, and 6 owner checkpoints (NOT AGENT WORK),
in 45 waves.**

---

## (a) Goal and scope

**Goal.** ABO becomes *app = core + module `books`* in four strictly ordered releases (09 §9.0, §9.3):
**R0** the hotfixes, **R1** the core runtime with no change to user data, **R2** the one-way cutover of
every core-service family with its UI, as one production release, and **R3** cleanup and the one upgrade
floor. The plan optimises for the shortest path to a finished extraction (D11): code lands dark on `main`,
one flip PR per subsystem switches it on, every merge passes `make ci`, e2e and the named required checks,
and there are no soaks, only an optional one-day smoke run after each deploy.

**Binding owner decisions.** No backward compatibility (D10): no alias, compat window, `Gone` step, legacy
store or HEAD-parity golden is built. No soaks (D11). R2 is one release, with automatic promotion paused
from F3's merge until R2 ships (D12 (a)); API keys and ABS phone sessions survive, web sessions and
one-time tokens reset, task and log history are converted (D12 (b)); the Audiobookshelf-compatible API is
the only frozen contract (D12 (c)); every checkpoint is deleted 7 days after it stops being needed
(D12 (d)). An unconvertible identity row or an unfinished account merge stops the upgrade; there are no
old-URL redirects; mediacore is `github.com/falkcorp/mediacore`, public, Apache-2.0 (D13). ABO keeps
PolyForm Noncommercial; moved code is relicensed at the split (D14).

**In scope.**

| Release / flip | What ships | Spec | Tasks |
|---|---|---|---|
| R0 | the six early fixes, the toolchain bump (first PR), the updater hotfix | 09 §9.4 R0; 05-authn §11.1; 13 §13.2.2; 05-updater §5U.12 | R0-01 … R0-06 |
| R1 | nested module and CI, leaves, composition root, the perimeter with the one log redactor, logging, core storage with strict claims, the one journaled swap, offline restore and reset, crash matrix, deploy tooling and the stop settings, the service registry's explicit list, react-router 8 and the api-client | 09 §9.4 R1; 03 §3.2, §3.8; 06 §6.11.1 step 3; 07 §7.19 steps 1–2 | R1-01 … R1-34 |
| R2, F2 | the foundation PRs, dark, with their kernel wiring and `tasklint`'s exemption ratchet, and the R2 verification corpus and harness | 09 §9.4 R2 (F2 row), §9.3; 05-tasks §5T.8.4 | F2-01 … F2-17 |
| R2, F3 | config and secrets; the Basic gate into core; M2, M7, M8 | 09 §9.4 R2 flips; 05-config §1.12 | F3-01 … F3-07 |
| R2, F4 | identity with `authz`, the events bus, the native settings routes; M3 | 05-authn §11; 05-rbac §3.4, §6 | F4-01 … F4-10 |
| R2, F5 | tasks and the log journal with its query route; conversion groups T2–T7; M4, M5 | 05-tasks §5T.8; 05-logging §5, §11 | F5-01 … F5-16, F5-18, F5-19 |
| R2, F6 | outbox and realtime, the outbox operator routes, the journal's live tail | 05-events §5E.11, §5E.13; 05-realtime §11; 05-logging §5 | F6-01 … F6-05 |
| R2, F7 | integrations with their instance routes, AI jobs, provider gates, library roots, notify; M6 | 05-metadata M1.3, M1.10; 05-ai §10; 05-notify §10; 05-library §5L.10 | F7-01 … F7-10 |
| R2, F8 | backup, the updater move, health, the kernel `/metrics` mount | 05-backup §5B.10; 05-updater; 05-health §5H.10; 05-metrics §13 | F8-01 … F8-04 |
| R2, F9 | the shell (R2 if ready, else R3) | 07 §7.11, §7.19 steps 4–5 | F9-01 … F9-05 |
| R2, last PR | the runbook and the R2 release note; then the freeze ends | 09 §9.2.9, §9.4 R2 | R2-01, R2-02 |
| R3 | deletions, the service registry's last clusters, `legacy-file-cleanup`, `UpgradeFloor{InstallFirst: <R2 tag>}`, the R3 release note | 09 §9.4 R3; 09 §9.2.2; 06 §6.11.1 steps 4, 6 | R3-01 … R3-05 |
| Owner | decisions, production reads, releases and deploys | 09 §9.4 Owner rows; §9.7 | OWNER-01 … OWNER-06 |

What is built in no release is listed in §(j).

---

## (b) Releases and flips: entry and exit criteria

Every PR of every release passes `make ci`, the e2e suite and, from R1, the named required checks of
09 §9.2.1 (`ci.yml` jobs mirroring `make ci`, `e2e`, `extraction-gates`, `vet-darwin`, `vet-integration`,
`unit-lifecycle` on its triggers, `docker-image`, `core-ci`); the tables below add only what is specific to
the release or flip (09 §9.3). Every PR that adds a boot-time durable write extends the crash matrix in the
same PR (09 §9.3; `TestBootCrashMatrix` for a kernel writer, `TestBootCrashMatrix_ABO` for a books one).

| Release / flip | Entry criteria | Exit criteria (done-check) | Rollback |
|---|---|---|---|
| **R0** | R0-01 is the first PR. R0-05 merges only after OWNER-01 has recorded P14. | The six `TestLegacy…` tests and the HEAD half of `TestUserMerge_ScopedAdminKeyRefused` (05-authn §11.1); the R0 updater test (exit 75, kept `.prev`, older tag refused, "local build, not compared", no unattended apply); `TestHealth_UnauthenticatedTokenSetGolden`'s R0 cell (`auto_update_paused` on the authenticated `degraded` field of `GET /api/v1/system/status`, never on `/health`); the pin check; `make ci`; e2e (09 §9.4 R0). OWNER-02 cuts the release and keeps the R0 tag. | `make rollback`: no stored data changes, no checkpoint |
| **R1** | R0 released (OWNER-02); Q-I decided and the R1 freeze kept or overridden (OWNER-01). R1-01 turns the R1 freeze on. | 09 §9.4 R1 Verification: every required check under strict claims; e2e; the ABS conformance suite; the perimeter tests; `TestLegacyManifest_RegistersAsMuxPatterns` (ABS on and off); `TestNativePrefixesNotRedirected`, `TestCollidingNamespacesStillRedirect`; `TestVersionJSONFields`; both image-job runs; both `unit-lifecycle` cells; `TestBootCrashMatrix`, `TestBootCrashMatrix_ABO`; the restore and reset staging tests; `TestR0OpensR1Store`; `TestCheckpointAutoDeletedAfterSevenDays`; every row of the CI classification table; `test_ci_classifier.py`, `test_deploy_wait_ready.py`, `test_make_rollback.py`; the embed check; the UI greps, vitest, Playwright, the image job with `core/web` and a dry-run RC with a clean tree. OWNER-03 reads P12, cuts R1 and deploys; R1-31 ends the freeze. | `make rollback` to R0, nothing lost; refused while a swap is in flight |
| **F2** (R2, dark) | R1 released and deployed (OWNER-03): R2's content lands dark while R1 is deployed (09 §9.3). F2's PRs are outside the freeze. | Builds and boots with nothing switched: no route, schedule or stored row changes (09 §9.4.1). `TestABOMetricsGatherNoDuplicate` (from R1) on every PR; `tasklint` in CI over core and books' task packages with its shrink-only exemption list; `hello.count` runs and `TestDisabledModuleWithRunsBoots` lists held runs while ABO starts nothing of `tasks`; `TestCoreCatalog_FixedFromF2`; `taskstest.StoresSuite`; the parity suites; the R0-written corpus, the overlay, the production-shaped store and the cutover-suite harness committed. | as R1 (nothing user-visible changed) |
| **F3** | Every F2 task merged; F3-01 … F3-06 merged dark; OWNER-04's P1 and P4 recorded (no unmapped env name, no unregistered prefix). | The config oracle and the M2 tests (05-config §1.12.2); `TestRenameTable_Golden`, `TestRenameTable_EveryKeyRegistered`; the deploy-files test; `TestBasicGate_ExemptSetGolden`, `TestBasicGate_EmptyPasswordRefuses`, `TestBasicGate_MoreRestrictiveWins`; `TestFailClosed_MoreRestrictiveWins`; `TestStateDirFallbackMatchesHEAD`; `TestConfigCutover_NoPlaintextSecretOnDisk`; `TestCutoverRefusedByOlderBuilds` with the pre-freeze release's binary; `TestDryRunMatchesLiveRun`; the restore drill; the crash matrix over M2, M7 and M8; the cutover suite in `extraction-gates` green from this PR on. **The R2 freeze starts at its merge.** | `make rollback-restore` to the pre-freeze release (from here on) |
| **F4** | F3 merged; F4-01 … F4-07 merged dark. | `TestIdentityCutover_ReachOracle` (R0 the baseline); the `TestIdentityCutover_*` rows; the five ported early-fix properties and the core half of `TestUserMerge_ScopedAdminKeyRefused`; `TestWorkerKey_Confined`; `TestCutover_NoRawTokenOnDisk`; `TestCutoverStopsOnUnconvertibleIdentity`, `TestCutoverRefusedWhileMergeInProgress`; `TestCutoverRefusedByOlderBuilds` gains its mid-M3 cell; the OAuth callback and start paths served by core; the ABS suite on the migrated corpus. | as F3 |
| **F5** | F4 merged; every conversion group T2–T7 merged dark (F5-01 … F5-14), each having removed its lines from `tasklint`'s exemption list; M4, M5, the journal and its query route dark (F5-15, F5-16, F5-19). | The schedule oracle `TestScheduleParity_AllDefinitions` and `TestCutover_NoCatchUpStorm`; `TestParity_ResumeCancelShutdown`; `TestKindRenameMapCoversLedger` on the F5 build; the dashboard-reference test and `promtool test rules` after the ops renames; `TestABOMetricsGatherNoDuplicate`; G6/G7 (`TestSecondModuleNoAudiobookEdit`); `tasklint`'s exemption list empty; the journal query route with `TestJournal_OtherUsersLinesNeedUsersView` and the route half of `TestJournal_RequiresSystemManage`, HEAD's logs route deleted. | as F3 |
| **F6** | F5 merged; outbox, realtime, the outbox operator routes and the journal's live tail merged dark (F6-01, F6-02, F6-04, F6-05). | The stream tests (05-events outbox set, 05-realtime hub set); the event half of `TestJournal_RequiresSystemManage`; `/api/events` deleted and the SPA on `/api/core/v1/events`, its log view on the live tail; e2e. | as F3 |
| **F7** | F6 merged; F7-01 … F7-08 and F7-10 merged dark. | The search oracle `TestBooksIndexRebuiltAtCutover`; the webhook delivery test; `TestRootsCutover_FromHEADFixture`; the instance routes in place of the class-i `/api/v1/config` handlers; the dashboard-reference test after the AI renames; `internal/plugin` deleted with no importer left. | as F3 |
| **F8** | F7 merged; F8-01 … F8-03 merged dark. | The dashboard-reference test over the kernel `/metrics` mount (no rename); R0's updater tests re-pointed with unchanged expectations, `TestABOUpdaterSourceMatchesHEAD`, `TestApply_RawAssetNoBackup`; `TestBasicGate_ExemptSetGolden` with `/health` exempt; `TestLegacyArchiveRestoreThenMigrate`; `unit-lifecycle`. | as F3 |
| **F9** | F8 merged; F9-01, F9-02, F9-04 and F9-05 merged dark; 07 §7.19 steps 1–2 done. Not ready: it moves to R3 unchanged. | The shell's checks (07 §7.19 step 4: `make build-api && ./audiobook-organizer serve` answers `/` with the placeholder; `TestABOManifestBootsWithShell`); e2e; the image job boots the embedded bundle. | as F3 (in R3: `make rollback` to R2) |
| **R2 last PR** | F8 merged (and F9 when it is in R2). | The runbook (deploy, rollback, smoke run) and the release note assembled from the rename table, the kind-rename map, the deleted routes, the metric renames and 09 §9.6, each owner's text verbatim (09 §9.2.9). OWNER-05 deploys and cuts R2; R2-02 ends the freeze. | `make rollback-restore`: checkpoint or off-host archive offline, plus the pre-freeze release; every write since the upgrade is lost |
| **R3** | R2 released and deployed (OWNER-05). | `make ci`; e2e; the crash matrix over `legacy-file-cleanup`; a pre-R2 store refused at boot with exit 78 (`TestPreR2StoreRefusedByR3`); `TestRestoreBelowUpgradeFloorRefusedAtStaging`; `TestFreshInstallPostFloorBootsTwice`. OWNER-06 deploys. | `make rollback` to R2 |

---

## (c) Critical path and duration

**Assumptions.** Agents run in parallel without limit, bounded only by file disjointness and dependencies
(§(d)); there are no soaks (D11). One agent PR, from brief to merge on a green required-check set with the
35-minute CI budget (09 §9.2.10), takes **0.5 working days at T0–T1, 1 at T2, 1.5 at T3, 2.5 at T4 and 3
for a flip PR**; an owner release checkpoint (cut, deploy) takes 1 day, the optional smoke run 1 more. The
owner turns a checkpoint around the day it is reached. R2's briefs may be executed and their PRs opened
during R1; only the merge waits for OWNER-03.

**The critical path** (30 sequential agent PRs and 4 release checkpoints):

R0-02 (T3, 1.5) → R0-03 (T2, 1) → OWNER-02 (OWNER, 1) → R1-01 (T3, 1.5) → R1-02 (T2, 1) → R1-10 (T3, 1.5) → R1-13 (T4, 2.5) → R1-20 (T4, 2.5) → R1-21 (T4, 2.5) → R1-23 (T4, 2.5) → R1-25 (T4, 2.5) → R1-27 (T2, 1) → R1-28 (T2, 1) → OWNER-03 (OWNER, 1) → F2-09 (T2, 1) → F2-01 (T3, 1.5) → F2-06 (T4, 2.5) → F3-04 (T4, 2.5) → F3-06 (T4, 2.5) → F4-06 (T4, 2.5) → F4-07 (T3, 1.5) → F4-08 (flip, 3) → F5-18 (flip, 3) → F6-03 (flip, 3) → F7-09 (flip, 3) → F8-04 (flip, 3) → F9-05 (T3, 1.5) → F9-02 (T3, 1.5) → F9-03 (flip, 3) → R2-01 (T2, 1) → OWNER-05 (OWNER, 1) → R3-02 (T4, 2.5) → R3-03 (T3, 1.5) → OWNER-06 (OWNER, 1)

| Scenario | Working days |
|---|---|
| Base rates, dependencies only (pipelined execution) | **65** (about 13 weeks) |
| Base rates, with the four optional smoke days | 69 |
| Shell (F9) slips to R3 and runs beside R3-02 | 59 |
| Every T3, T4 and flip PR takes 1.5× its base rate | 92.75 |
| Waves run strictly in lockstep (§(d), each wave as long as its longest PR) | 84 |

R0 and R1 take 23 of these days: R1's storage chain R1-20 → R1-21 → R1-23 → R1-25 is 10 of them,
and the deploy tooling and `unit-lifecycle` close R1. In R2 the path starts with F2-09 → F2-01 (the tasks API
waits a day for the config and health leaf types it names), runs through the dark identity step
(F3-04 → F3-06 → F4-06 → F4-07) and into the serialized flips F4-08 → F5-18 → F6-03 → F7-09 → F8-04 → F9-03,
each a one-PR atomic change (09 §9.2.1), so R2's length is set by the flip PRs, not by the dark work,
which almost all fits beside them. The levers that shorten it are pre-staging R2's PRs during R1, splitting the T4 dark
tasks of F2–F4 further where their files allow, and letting F9 slip to R3.

**The exemption entries keep the conversions parallel.** `tasklint` reads `internal/books/testdata/tasklint_exempt.d/` as one union: F2-17
creates one entry file per conversion row (and one for F5-18), each of the 12 conversion rows deletes only its
own file in its own PR (05-tasks §5T.8.2 conversion rule 3, §5T.8.4), the shrink-only ratchet holds over the union,
and F5-18 requires the directory empty. No two conversion rows share a file, so they share waves.

---

## (d) Wave plan

A wave is a set of tasks that run at the same time. Every task in a wave has all its dependencies merged
in an earlier wave, and no two tasks in a wave touch the same file, where a path ending in `/` is a whole
directory and collides with every path below it. Both properties are checked mechanically by a script
over the JSON inventory, whose checks are numbered as the table cites them: (1) the row schema and the ID
scheme, (2) every dependency in an earlier wave, (3) pairwise file disjointness of the 309 same-wave
task pairs, (4) the 04 coverage of §(h), (5) the gate ownership of §(e), (6) real paths. The last run passed all six. The waves were
computed by list scheduling in dependency order with critical-path priority, so a task joins the earliest
wave after its dependencies whose members it does not collide with.

What makes the plan safe beyond the per-wave rule:

- **Shared hot files serialize by construction.** Every root-package move, rename or creation lists
  `internal/arch/layering_test.go` (its `layerOf` entry) and `scripts/ci/short_test_weights.json` (stale
  weights fail), so no two move PRs share a wave (09 §9.4 R1 CI classification). Every flip lists `main.go`,
  `internal/server/`, `internal/books/module.go`, the kernel's two wiring files and the module member files (most
  also `core/app/options.go`), so no two flips share a wave and a flip never runs beside a task touching those
  paths (09 §9.3: the flips share `main.go`, the kernel wiring and `internal/server`).
- **Shared wiring files have one owner per wave.** R1-13 creates `core/kernel/boot.go` (the boot phases and the
  services build order), `core/kernel/kernel.go` (per-module construction, collision checks, core route mounts)
  and `core/module/manifest.go`, `registrar.go` and `deps.go`. In R1 a row that needs a boot call lists
  `boot.go`; from R2 on a dark row adds only its own `core/kernel/<subsystem>.go` and package files, and its
  stage's wiring row (F2-16 for the foundation, which has no flip) or its flip adds the boot calls, the members and
  the mounts. The same holds for `internal/books/module.go` and `internal/books/services.go`, `core/app/offline.go`,
  the generated `core/openapi/core.json` and client (regenerated by the row that mounts routes),
  `tools/cmd/sdkguard/internal-deps.txt` (regenerated by every move inside `pkg/plugin/sdk`'s closure), the
  `core/coretest/` files and the CI files. The README's shared-file table names each file's owner in every
  wave. Core's own `layerOf` carries 03 R4's whole table from R1-01, its `internal/<capability>http` pattern
  included, so creating a core package R4 names edits no shared guard file.
- **One exemption entry file per conversion row.** `tasklint` (F2-17) runs from F2 over the union of the entry
  files in its exemption directory, which may only shrink; each conversion row deletes only its own entry file
  (§(c)), and F5-18 deletes its own and requires the directory empty.
- **Real files.** Every listed path is a file or directory at the plan's base commit or a path a row creates;
  check 6 fails a placeholder, a directory written without its `/`, a path that an earlier wave removed, and a
  rename's destination used before the rename. Importer and caller lists are computed at the plan's base; a
  brief re-greps at its own base and any further importer is its file too.
- **Move windows.** Every move or rename task (its `04 keys` name a move) runs inside a move window
  announced at least one working day ahead in the tracking issue, naming the 04 rows it freezes; feature PRs
  touching them are held; the window lasts at most two working days (09 §9.2.10).
- **Dark before flip.** Every F2–F9 task except the seven flip PRs lands dark: it changes no behaviour,
  route, schedule or stored row. A dark PR that moves a package verbatim (F2-17, F3-01, F4-02, F4-03, F7-02,
  F7-05, F8-02) only re-points its importers, every caller in the same PR with no forwarding shim (04 §2.1); the cutover steps are built unregistered until their flip
  registers them (05-config §1.12.2 separates "builds" from "ships"). Every intermediate `main` boots and
  passes e2e.
- **Releases stay ordered.** R1-01 waits for the R0 release, every F2 task for the R1 release, every R3
  task for the R2 release (09 §9.3).

| Wave | Releases | Tasks (parallel, file-disjoint) | Longest (days) | Why it is safe |
|---|---|---|---|---|
| W01 | OWNER, R0 | OWNER-01, R0-01, R0-02, R0-04, R0-06 | 1.5 | owner checkpoint OWNER-01: no files, NOT AGENT WORK; it gates the tasks that depend on it; 4 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2). |
| W02 | R0 | R0-03, R0-05 | 1.0 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2). |
| W03 | OWNER | OWNER-02 | 1.0 | owner checkpoint OWNER-02: no files, NOT AGENT WORK; it gates the tasks that depend on it. |
| W04 | R1 | R1-01 | 1.5 | every dependency merged in an earlier wave (check 2); R1-01 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; R1-01 turns the R1 freeze on. |
| W05 | R1 | R1-02, R1-11, R1-22 | 1.0 | 3 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2). |
| W06 | R1 | R1-07, R1-12 | 1.0 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-07 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files. |
| W07 | R1 | R1-10 | 1.5 | every dependency merged in an earlier wave (check 2); R1-10 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files. |
| W08 | R1 | R1-03, R1-32 | 1.5 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2). |
| W09 | R1 | R1-04, R1-09 | 1.0 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-09 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files. |
| W10 | R1 | R1-05, R1-08, R1-13 | 2.5 | 3 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-08 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files. |
| W11 | R1 | R1-14, R1-20, R1-29 | 2.5 | 3 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-20 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; R1-20 is the wave's one owner of the kernel and module wiring files. |
| W12 | R1 | R1-15, R1-21 | 2.5 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-21 is the wave's one owner of the kernel and module wiring files. |
| W13 | R1 | R1-23 | 2.5 | every dependency merged in an earlier wave (check 2); R1-23 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files. |
| W14 | R1 | R1-16, R1-18 | 1.0 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-16 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files. |
| W15 | R1 | R1-33, R1-34 | 1.0 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2). |
| W16 | R1 | R1-17, R1-25 | 2.5 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-17 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files. |
| W17 | R1 | R1-19, R1-24 | 1.5 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-24 is the wave's one owner of the kernel and module wiring files. |
| W18 | R1 | R1-26, R1-27 | 1.0 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-26 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files. |
| W19 | R1 | R1-28, R1-30 | 1.0 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); R1-28 is the wave's one owner of the kernel and module wiring files. |
| W20 | R1 | R1-06 | 1.0 | every dependency merged in an earlier wave (check 2). |
| W21 | OWNER | OWNER-03 | 1.0 | owner checkpoint OWNER-03: no files, NOT AGENT WORK; it gates the tasks that depend on it. |
| W22 | OWNER, R1, F2, F4 | OWNER-04, R1-31, F2-07, F2-08, F2-09, F2-10, F2-12, F4-04, F4-10 | 1.5 | owner checkpoint OWNER-04: no files, NOT AGENT WORK; it gates the tasks that depend on it; 8 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); the R2 PRs here land dark (no route, schedule or stored row changes before their flip); the freeze-off PR merges only after its release is cut. |
| W23 | F2, F3, F4 | F2-01, F2-13, F2-14, F3-01, F4-01 | 2.5 | 5 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); F3-01 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; the R2 PRs here land dark (no route, schedule or stored row changes before their flip). |
| W24 | F2, F3, F4 | F2-02, F2-06, F3-03, F3-05, F4-09 | 2.5 | 5 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); the R2 PRs here land dark (no route, schedule or stored row changes before their flip). |
| W25 | F2, F3, F4, F7 | F2-03, F2-15, F2-17, F3-02, F3-04, F4-02, F7-01 | 2.5 | 7 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); F2-17, F4-02 run inside an announced move window: no other PR in the wave touches the packages they move or rewire or the shared layerOf and shard files; the R2 PRs here land dark (no route, schedule or stored row changes before their flip). |
| W26 | F2, F3, F7, F8 | F2-04, F3-06, F7-04, F7-10, F8-01 | 2.5 | 5 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); the R2 PRs here land dark (no route, schedule or stored row changes before their flip). |
| W27 | F2, F4, F5 | F2-05, F2-11, F4-03, F4-06, F5-16 | 2.5 | 5 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); F4-03 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; the R2 PRs here land dark (no route, schedule or stored row changes before their flip). |
| W28 | F2, F4, F5, F6, F7, F8 | F2-16, F4-05, F5-15, F6-01, F6-02, F7-02, F7-06, F8-03 | 2.5 | 8 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); F7-02 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; the R2 PRs here land dark (no route, schedule or stored row changes before their flip); F2-16 is the wave's one owner of the kernel and module wiring files. |
| W29 | F3, F5, F6, F7, F9 | F3-07, F5-01, F5-02, F5-03, F5-04, F5-05, F5-06, F5-07, F5-08, F5-09, F5-10, F5-11, F5-12, F5-19, F6-04, F7-07, F9-01 | 3.0 | 17 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); F3-07 is the one PR here touching main.go, internal/server/ and books' registration; it merges alone and its siblings rebase after it; F3-07, F5-03 run inside an announced move window: no other PR in the wave touches the packages they move or rewire or the shared layerOf and shard files; the R2 PRs here land dark (no route, schedule or stored row changes before their flip); F3-07's merge starts the R2 freeze; F5-01, F5-02, F5-04, F5-05, F5-06, F5-07, F5-08, F5-09, F5-10, F5-11, F5-12 each delete only their own exemption entry file; F3-07 is the wave's one owner of the kernel and module wiring files. |
| W30 | F4, F5, F6, F7 | F4-07, F5-13, F6-05, F7-03, F7-05 | 2.5 | 5 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); F7-05 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; the R2 PRs here land dark (no route, schedule or stored row changes before their flip); F5-13 each delete only their own exemption entry file. |
| W31 | F4, F5, F7 | F4-08, F5-14, F7-08 | 3.0 | 3 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); F4-08 is the one PR here touching main.go, internal/server/ and books' registration; it merges alone and its siblings rebase after it; F4-08 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; the R2 PRs here land dark (no route, schedule or stored row changes before their flip); F4-08 is the wave's one owner of the kernel and module wiring files. |
| W32 | F8 | F8-02 | 1.5 | every dependency merged in an earlier wave (check 2); F8-02 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; the R2 PRs here land dark (no route, schedule or stored row changes before their flip). |
| W33 | F5 | F5-18 | 3.0 | every dependency merged in an earlier wave (check 2); F5-18 is the one PR here touching main.go, internal/server/ and books' registration; it merges alone and its siblings rebase after it; F5-18 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; F5-18 is the wave's one owner of the kernel and module wiring files. |
| W34 | F6 | F6-03 | 3.0 | every dependency merged in an earlier wave (check 2); F6-03 is the one PR here touching main.go, internal/server/ and books' registration; it merges alone and its siblings rebase after it; F6-03 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; F6-03 is the wave's one owner of the kernel and module wiring files. |
| W35 | F7 | F7-09 | 3.0 | every dependency merged in an earlier wave (check 2); F7-09 is the one PR here touching main.go, internal/server/ and books' registration; it merges alone and its siblings rebase after it; F7-09 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; F7-09 is the wave's one owner of the kernel and module wiring files. |
| W36 | F8 | F8-04 | 3.0 | every dependency merged in an earlier wave (check 2); F8-04 is the one PR here touching main.go, internal/server/ and books' registration; it merges alone and its siblings rebase after it; F8-04 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; F8-04 is the wave's one owner of the kernel and module wiring files. |
| W37 | F9 | F9-05 | 1.5 | every dependency merged in an earlier wave (check 2); the R2 PRs here land dark (no route, schedule or stored row changes before their flip). |
| W38 | F9 | F9-02, F9-04 | 1.5 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); the R2 PRs here land dark (no route, schedule or stored row changes before their flip). |
| W39 | F9 | F9-03 | 3.0 | every dependency merged in an earlier wave (check 2); F9-03 is the one PR here touching main.go, internal/server/ and books' registration; it merges alone and its siblings rebase after it; F9-03 runs inside an announced move window: no other PR in the wave touches the packages it moves or rewires or the shared layerOf and shard files; F9-03 is the wave's one owner of the kernel and module wiring files. |
| W40 | R2 | R2-01 | 1.0 | every dependency merged in an earlier wave (check 2). |
| W41 | OWNER | OWNER-05 | 1.0 | owner checkpoint OWNER-05: no files, NOT AGENT WORK; it gates the tasks that depend on it. |
| W42 | R2, R3 | R2-02, R3-02, R3-04 | 2.5 | 3 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2); the freeze-off PR merges only after its release is cut. |
| W43 | R3 | R3-05 | 1.5 | every dependency merged in an earlier wave (check 2). |
| W44 | R3 | R3-01, R3-03 | 1.5 | 2 PRs, pairwise file-disjoint (check 3); every dependency merged in an earlier wave (check 2). |
| W45 | OWNER | OWNER-06 | 1.0 | owner checkpoint OWNER-06: no files, NOT AGENT WORK; it gates the tasks that depend on it. |

---

## (e) Per-flip gates

These are the tests and checks 09 (and the 05-* files it cites) require before a release or flip
merges. Each is created and owned by exactly one task; later flips extend some of them (the crash matrix,
`TestCutoverRefusedByOlderBuilds`, the dashboard-reference test, `TestBasicGate_ExemptSetGolden`) in the
same PR as the change they cover. Unnamed checks in 09 carry the owning 05 file's test names; a name marked
"(name coined here)" is the plan's. The existing ABS conformance suite (`internal/server/handlers/abs/`,
`internal/server/absauth/`) runs in every `make ci` and needs no owner; F4-08 adds its run on the migrated
corpus.

**Required checks from R1 (09 §9.2.1).** `ci.yml` jobs mirroring root `make ci` (R1-03), `e2e`,
`extraction-gates` (created by R1-02; jobs added by R1-10, R1-23, F2-14 and F2-15), `vet-darwin` and
`vet-integration` (R1-02), `unit-lifecycle` (R1-28; triggers in 09 §9.2.1), `docker-image` (R1-04) and
`core-ci` (R1-01, completed by R1-03). From F3-07 every PR runs the R2 cutover suite; its
production-shaped run executes on `unit-lifecycle`'s triggers and nightly. The crash matrices and the
cutover suite never move to the nightly run (09 §9.2.10).

| Release / flip | Gate tests and checks | Required by | Owning task |
|---|---|---|---|
| R0 (the six early fixes' HEAD-code tests) | `TestLegacySessionsList_NoToken` | 09 §9.4 R0 Verification; 05-authn §11.1 | R0-04 |
| R0 (the six early fixes' HEAD-code tests) | `TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint` | 09 §9.4 R0 Verification; 05-authn §11.1 | R0-02 |
| R0 (the six early fixes' HEAD-code tests) | `TestLegacyAPIKey_RevokedNotReactivated` (in internal/server/handlers/apikeys_earlyfix_test.go) | 09 §9.4 R0 Verification; 05-authn §11.1 | R0-02 |
| R0 (the six early fixes' HEAD-code tests) | `TestLegacyRequireAdmin_HonoursKeyScopes` | 09 §9.4 R0 Verification; 05-authn §11.1 | R0-03 |
| R0 (the six early fixes' HEAD-code tests) | `TestUserMerge_ScopedAdminKeyRefused` | 09 §9.4 R0 Verification; 05-authn §11.1 | R0-03 |
| R0 (the six early fixes' HEAD-code tests) | `TestLegacyBasicGate_EmptyPasswordRefuses` | 09 §9.4 R0 Verification; 05-authn §11.1 | R0-05 |
| R0 (the six early fixes' HEAD-code tests) | `TestLegacyRevokeRacesTouch_NeverUnrevokes` | 09 §9.4 R0 Verification; 05-authn §11.1 | R0-02 |
| R0 (the updater test) | the R0 updater test: `TestApply_RestartExits75`, `TestApply_SwapAtomicAndPrevKept`, `TestCheck_SemverNeverDowngrades`, `TestCheck_DescribeSuffixedCurrentNotCompared`, `TestCheck_ScheduledRunNeverApplies` | 09 §9.4 R0 Verification | R0-06 |
| R0 (13's pin check) | 13's pin check: `scripts/check_toolchain_versions.py` | 09 §9.4.1 row 1 | R0-01 |
| R1 (the perimeter tests) | `TestStage2_RecordsEveryRequest` | 09 §9.4 R1 Verification; 05-http-api §8.1, §12 | R1-17 |
| R1 (the perimeter tests) | `TestPerimeterHonoursBlobOnlyValues` | 09 §9.4 R1 Verification; 05-http-api §8.1, §12 | R1-17 |
| R1 (the perimeter tests) | `TestCSP_DefaultPerOwner` | 09 §9.4 R1 Verification; 05-http-api §8.1, §12 | R1-17 |
| R1 (the perimeter tests) | `TestCompressionExclusionsEqualHEAD` | 09 §9.4 R1 Verification; 05-http-api §8.1, §12 | R1-17 |
| R1 (the perimeter tests) | `TestPerimeter_XAppBuildOnEveryResponse` | 09 §9.4 R1 Verification; 05-http-api §8.1, §12 | R1-17 |
| R1 (manifest and redirects) | `TestLegacyManifest_RegistersAsMuxPatterns` | 09 §9.4 R1 Verification; 09 §9.2.4 | R1-14 |
| R1 (manifest and redirects) | `TestNativePrefixesNotRedirected` | 09 §9.4 R1 Verification; 09 §9.2.4 | R1-14 |
| R1 (manifest and redirects) | `TestCollidingNamespacesStillRedirect` | 09 §9.4 R1 Verification; 09 §9.2.4 | R1-14 |
| R1 (build identity) | `TestVersionJSONFields` | 09 §9.4 R1 Verification | R1-19 |
| R1 (build identity) | `TestBuildID_HeaderEqualsEmbeddedMeta` | 09 §9.4 R1 Verification | R1-19 |
| R1 (image job) | the docker-image job, both runs | 09 §9.4 R1 Verification | R1-04 |
| R1 (unit-lifecycle) | `scripts/tests/test_unit_lifecycle.py` (the two unit-lifecycle cells) | 09 §9.4 R1 Verification; §9.10 | R1-28 |
| R1 (crash matrices) | `TestBootCrashMatrix` | 09 §9.4 R1 Verification; §9.3; §9.10 | R1-21 |
| R1 (crash matrices) | `TestBootCrashMatrix_ABO` | 09 §9.4 R1 Verification; §9.3; §9.10 | R1-23 |
| R1 (restore and reset staging) | the restore and reset staging tests of 05-backup §5B.3.4 and 05-storage §5S.3.15 (`TestInflightBeforeFirstRename`, `TestOfflineResetStagesIntent`, `TestFactoryResetOffline_R1`, `TestSwapSlotExclusive` with the ABO stagers, `TestCLISubcommandsUseOfflineOpen`) | 09 §9.4 R1 Verification | R1-25 |
| R1 (restore and reset staging) | the swap's journal crash tests (`TestRestoreCrashAtEveryJournalStep`, `TestInflightPrecedesFirstRename`) | 09 §9.4 R1 Verification | R1-21 |
| R1 (previous-build and checkpoint) | `TestR0OpensR1Store` | 09 §9.4 R1 Verification; §9.10 | R1-23 |
| R1 (previous-build and checkpoint) | `TestCheckpointAutoDeletedAfterSevenDays` | 09 §9.4 R1 Verification; §9.10 | R1-24 |
| R1 (classification and scripts) | every row of R1's CI classification table | 09 §9.4 R1 Verification; §9.10 | R1-03 |
| R1 (classification and scripts) | genproto-exclude-root | 09 §9.4 R1 Verification; §9.10 | R1-01 |
| R1 (classification and scripts) | `scripts/tests/test_ci_classifier.py` | 09 §9.4 R1 Verification; §9.10 | R1-02 |
| R1 (classification and scripts) | `scripts/tests/test_deploy_wait_ready.py` (exit 78 included) | 09 §9.4 R1 Verification; §9.10 | R1-27 |
| R1 (classification and scripts) | `scripts/tests/test_make_rollback.py` | 09 §9.4 R1 Verification; §9.10 | R1-27 |
| R1 (classification and scripts) | `scripts/ci/check_embed_context.py` (passes on HEAD, fails on a docs/ embed fixture) | 09 §9.4 R1 Verification; §9.10 | R1-04 |
| R1 (classification and scripts) | `scripts/ci/check_commits_build.py` | 09 §9.4 R1 Verification; §9.10 | R1-02 |
| R1 (classification and scripts) | `scripts/ci/short_test_shards.py` (one core shard, stale entries fail) | 09 §9.4 R1 Verification; §9.10 | R1-03 |
| R1 (classification and scripts) | `scripts/tests/test_run_gates.py` | 09 §9.4 R1 Verification; §9.10 | R1-02 |
| R1 (classification and scripts) | `scripts/ci/build_web.sh` | 09 §9.4 R1 Verification; §9.10 | R1-05 |
| R1 (core guards) | `TestCoreImportsOnlyCore` | 09 §9.4 R1 CI classification; §9.4.1 | R1-01 |
| R1 (core guards) | `TestCoreHoldsNoAppKeyNames` | 09 §9.4 R1 CI classification; §9.4.1 | R1-01 |
| R1 (core guards) | `TestBooksMinCoreVersion` | 09 §9.4 R1 CI classification; §9.4.1 | R1-15 |
| R1 (core guards) | `TestNoDirectAppConfigWrites` | 09 §9.4 R1 CI classification; §9.4.1 | R1-11 |
| R1 (UI steps) | ! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts | 09 §9.4 R1 Verification (UI steps) | R1-29 |
| R1 (UI steps) | the dry-run RC build through prerelease.yml with git status --porcelain empty | 09 §9.4 R1 Verification (UI steps) | R1-30 |
| R1 (UI steps) | the image job green with core/web present | 09 §9.4 R1 Verification (UI steps) | R1-30 |
| R1 (legacy track pinned) | `TestNoNewLegacyMigrations` | 09 §9.2.2 | R1-23 |
| R1 (strict claims) | make ci under strict claims | 09 §9.4 R1 Verification; 05-storage §5S.9.4 step 2 | R1-23 |
| R1 (required checks) | vet-darwin | 09 §9.2.1 required-checks table; §9.4 R1 CI classification | R1-02 |
| R1 (required checks) | vet-integration | 09 §9.2.1 required-checks table; §9.4 R1 CI classification | R1-02 |
| R1 (required checks) | `scripts/ci/check_module_skew.py` | 09 §9.2.1 required-checks table; §9.4 R1 CI classification | R1-01 |
| R1 (dashboard-reference test) | `TestDeployReferencedSeriesServed` (the dashboard-reference test) | 09 §9.2.1 extraction-gates; §9.2.7 | R1-10 |
| R1 (dashboard-reference test) | `TestPromRules_Promtool` | 09 §9.2.1 extraction-gates; §9.2.7 | R1-10 |
| F2 (foundation) | `TestABOMetricsGatherNoDuplicate` | 09 §9.4.1 (F2 builds and boots with nothing switched); 04 §8.1 F2; 05-tasks §5T.8.2 Core gate | R1-15 |
| F2 (foundation) | `TestCoreCatalog_FixedFromF2` | 09 §9.4.1 (F2 builds and boots with nothing switched); 04 §8.1 F2; 05-tasks §5T.8.2 Core gate | F2-08 |
| F2 (foundation) | taskstest.StoresSuite with its importer cases (`TestNativeStores_RoundTrip`) | 09 §9.4.1 (F2 builds and boots with nothing switched); 04 §8.1 F2; 05-tasks §5T.8.2 Core gate | F2-02 |
| F2 (R2 corpus and harness) | the fixture corpus written by the R0 binary and its answers | 09 §9.3 item 1; §9.4 R2 Verification; §9.10 | F2-12 |
| F2 (R2 corpus and harness) | the hand-built overlay and the preflight's blocking-row and unfinished-merge corpus | 09 §9.3 item 1; §9.4 R2 Verification; §9.10 | F2-13 |
| F2 (R2 corpus and harness) | the production-shaped store run and the boot-time measurement | 09 §9.3 item 1; §9.4 R2 Verification; §9.10 | F2-14 |
| F2 (R2 corpus and harness) | `TestCutoverOnR0Corpus` | 09 §9.3 item 1; §9.4 R2 Verification; §9.10 | F2-15 |
| F2 (R2 corpus and harness) | `TestCutoverPreservesIDsAndReferences` | 09 §9.3 item 1; §9.4 R2 Verification; §9.10 | F2-15 |
| F2 (R2 corpus and harness) | `TestCutoverLeavesOtherFamiliesByteIdentical` | 09 §9.3 item 1; §9.4 R2 Verification; §9.10 | F2-15 |
| F2 (R2 corpus and harness) | `TestRollbackRestoreBootsPreviousBuild` (the restore drill) | 09 §9.3 item 1; §9.4 R2 Verification; §9.10 | F2-15 |
| F2 (R2 corpus and harness) | `TestCutoverCrashAtEveryChunk` (harness, each step adds its cells) | 09 §9.3 item 1; §9.4 R2 Verification; §9.10 | F2-06 |
| F2 (parity suites) | `TestParity_ResumeCancelShutdown` (05-tasks' resume, cancel and shutdown parity suites) | 09 §9.4 R2 Verification | F2-03 |
| F3 (config oracle and M2) | `TestConfigCutover_EffectiveConfigUnchanged` (the config oracle) | 09 §9.4.1 (F3: the config oracle); 05-config §1.12.2 | F3-06 |
| F3 (config oracle and M2) | `TestRenameTable_Golden` | 09 §9.4.1 (F3: the config oracle); 05-config §1.12.2 | F3-06 |
| F3 (F3 flip gates) | `TestRenameTable_EveryKeyRegistered` | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-07 |
| F3 (F3 flip gates) | `TestDeployFilesUseCanonicalNamesOnly` (the deploy-files test) | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-07 |
| F3 (F3 flip gates) | `TestBasicGate_ExemptSetGolden` | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-07 |
| F3 (F3 flip gates) | `TestBasicGate_MoreRestrictiveWins` (the compose cell of `TestPerimeterHonoursBlobOnlyValues`) | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-07 |
| F3 (F3 flip gates) | `TestBasicGate_EmptyPasswordRefuses` | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-05 |
| F3 (F3 flip gates) | `TestConfigCutover_NoPlaintextSecretOnDisk` | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-07 |
| F3 (F3 flip gates) | `TestCutoverRefusedByOlderBuilds` | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-07 |
| F3 (F3 flip gates) | `TestDryRunMatchesLiveRun` | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-04 |
| F3 (F3 flip gates) | `TestFailClosed_MoreRestrictiveWins` | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-02 |
| F3 (F3 flip gates) | `TestStateDirFallbackMatchesHEAD` | 09 §9.4 R2 Verification; §9.2.5; §9.10 | F3-01 |
| F4 (identity) | `TestIdentityCutover_ReachOracle` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-07 |
| F4 (identity) | `TestIdentityCutover_ABSSurvives` (the ABS conformance suite on the migrated corpus) | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-08 |
| F4 (identity) | `TestIdentityCutover_WorkerKeyStaysConfined` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-06 |
| F4 (identity) | `TestWorkerKey_Confined` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-01 |
| F4 (identity) | `TestCutover_NoRawTokenOnDisk` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-08 |
| F4 (identity) | `TestCutoverRefusedWhileMergeInProgress` (the preflight's refusal corpus) | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-06 |
| F4 (identity) | `TestCutoverStopsOnUnconvertibleIdentity` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-06 |
| F4 (identity) | `TestSessions_ListNeverReturnsToken` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-02 |
| F4 (identity) | `TestAPIKey_EmptyEffectiveKeyCannotMint` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-02 |
| F4 (identity) | `TestAPIKey_RevokedIsTerminal` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-02 |
| F4 (identity) | `TestAdminChecks_HonourKeyScopes` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-02 |
| F4 (identity) | `TestRevokeRacesTouch_NeverUnrevokes` | 09 §9.4 R2 Verification; §9.4.1 (F4); §9.2.6; 05-authn §11.1 | F4-02 |
| F5 (tasks) | `TestScheduleParity_AllDefinitions` (the schedule oracle) | 09 §9.4 R2 Verification; §9.4.1 (F5); 05-tasks §5T.8.2 | F5-14 |
| F5 (tasks) | `TestCutover_NoCatchUpStorm` | 09 §9.4 R2 Verification; §9.4.1 (F5); 05-tasks §5T.8.2 | F5-14 |
| F5 (tasks) | `TestKindRenameMapCoversLedger` | 09 §9.4 R2 Verification; §9.4.1 (F5); 05-tasks §5T.8.2 | F5-18 |
| F5 (tasks) | tasklint's exemption directory empty at the flip | 09 §9.4 R2 Verification; §9.4.1 (F5); 05-tasks §5T.8.2 | F5-18 |
| F6 (stream tests) | the outbox stream tests of 05-events (`TestOutbox_StageCommitsWithBatch`, `TestOutbox_RedeliveryAfterCrashBeforeAck`) | 09 §9.4.1 (F6: the stream tests) | F6-01 |
| F6 (stream tests) | the hub stream tests of 05-realtime (`TestHub_ResumeFromLastEventID`, `TestHub_PerEventPermissionFilter`, `TestHub_RefusalsAreStatusesBeforeStream`) | 09 §9.4.1 (F6: the stream tests) | F6-02 |
| F7 (integrations) | `TestBooksIndexRebuiltAtCutover` (the search oracle) | 09 §9.4.1 (F7); §9.4 R2 Verification | F7-09 |
| F7 (integrations) | the webhook delivery test (`TestWebhook_JSONBodyGolden`, `TestWebhook_SignatureWithTimestamp`) | 09 §9.4.1 (F7); §9.4 R2 Verification | F7-05 |
| F8 (backup, updater, health, metrics) | the dashboard-reference test over the kernel /metrics mount (extension of `TestDeployReferencedSeriesServed`) | 09 §9.4.1 (F8) | F8-04 |
| F9 (shell) | the shell's checks (make build-api && ./audiobook-organizer serve answers / with the placeholder, and `TestABOManifestBootsWithShell`) | 09 §9.4.1 (F9: the shell's checks); 07 §7.19 step 4 | F9-03 |
| R2 (step 3) | the generated-client coverage check (name coined here) | 09 §9.4.1 (07 §7.19 step 3) | R1-30 |
| R3 (floor) | `TestPreR2StoreRefusedByR3` (the exit-78 boot test) | 09 §9.4 R3 Verification; §9.10 | R3-02 |
| R3 (floor) | `TestRestoreBelowUpgradeFloorRefusedAtStaging` | 09 §9.4 R3 Verification; §9.10 | R3-02 |
| R3 (floor) | `TestFreshInstallPostFloorBootsTwice` | 09 §9.4 R3 Verification; §9.10 | R3-02 |
| R3 (floor) | `TestLegacyFileCleanup` (the crash matrix over legacy-file-cleanup's rows) | 09 §9.4 R3 Verification; §9.10 | R3-03 |
| R0 (degraded field; 05-*) | `TestHealth_UnauthenticatedTokenSetGolden` | 05-health §5H.9 (R0 cell), §5H.10 R0 row | R0-06 |
| R1 (atom table and manifest; 05-*) | `TestAtomTable_Golden` | 05-rbac §6.1, §12; 05-http-api §8.1 R1 row | R1-15 |
| R1 (atom table and manifest; 05-*) | `TestManifest_SPermRoutesCarryAtomRequirement` | 05-rbac §6.1, §12; 05-http-api §8.1 R1 row | R1-15 |
| R1 (checkpoint prune; 05-*) | `TestPruneCheckpoints` | 05-storage §5S.3.12 | R1-24 |
| R1 (core version; 05-*) | check_core_version.py | 12 §12.4.1, §12.4.3; 09 §9.4 R1 CI classification | R1-06 |
| R1 (core version; 05-*) | core-checkpoint (release-prod.yml) | 12 §12.4.1, §12.4.3; 09 §9.4 R1 CI classification | R1-06 |
| F2 (runner; 05-*) | `TestDataMigrationResumesExactly` | 05-storage §5S.3.6 | F2-06 |
| F3 (M2 oracle set; 05-*) | `TestConfigCutover_SecretsReadable` | 05-config §1.12.2 (Oracle and tests) | F3-06 |
| F3 (M2 oracle set; 05-*) | `TestConfigCutover_KeyGuard` | 05-config §1.12.2 (Oracle and tests) | F3-06 |
| F3 (M2 oracle set; 05-*) | `TestConfigCutover_QuarantineNotDrop` | 05-config §1.12.2 (Oracle and tests) | F3-06 |
| F3 (M2 oracle set; 05-*) | `TestConfigCutover_GapFillStripped` | 05-config §1.12.2 (Oracle and tests) | F3-06 |
| F3 (M2 oracle set; 05-*) | `TestConfigCutover_Idempotent` | 05-config §1.12.2 (Oracle and tests) | F3-06 |
| F3 (M2 oracle set; 05-*) | `TestConfigCutover_RestoreDrill` | 05-config §1.12.2 (Oracle and tests) | F3-06 |
| F3 (M2 oracle set; 05-*) | `TestConfigCutover_RowsGone` | 05-config §1.12.2 (Oracle and tests) | F3-06 |
| F3 (quarantine commands; 05-*) | `TestQuarantineCommands` | 05-storage §5S.3.13 | F3-04 |
| F4 (identity cutover rows; 05-*) | `TestIdentityCutover_FixtureFromR0` | 05-authn §10; 05-rbac §12 | F4-06 |
| F4 (identity cutover rows; 05-*) | `TestIdentityCutover_Resumable` | 05-authn §10; 05-rbac §12 | F4-06 |
| F4 (identity cutover rows; 05-*) | `TestIdentityCutover_RolesAndKeyScopes` | 05-authn §10; 05-rbac §12 | F4-06 |
| F4 (OIDC paths; 05-*) | `TestOIDC_AppHandoffMintsABSCode` | 05-authn §10, §4.13; 05-http-api §8.1 F4 row | F4-08 |
| F4 (OIDC paths; 05-*) | `TestOAuthCallback_HEADPathServedByCore` | 05-authn §10, §4.13; 05-http-api §8.1 F4 row | F4-08 |
| F4 (OIDC paths; 05-*) | `TestOAuthStart_HEADPathServedByCore` | 05-authn §10, §4.13; 05-http-api §8.1 F4 row | F4-08 |
| F5 (tasks cutover rows; 05-*) | `TestCutover_KindReferencesRewritten` | 05-tasks §5T.8.3, §5T.9 | F5-16 |
| F5 (tasks cutover rows; 05-*) | `TestCutover_RepairsRowsAndMarksImported` | 05-tasks §5T.8.3, §5T.9 | F5-16 |
| F5 (tasks cutover rows; 05-*) | `TestCutover_TasksFixtureFromR0` | 05-tasks §5T.8.3, §5T.9 | F5-16 |
| F5 (tasks cutover rows; 05-*) | `TestCutover_SharedSettingFamilyOnlyRunKeysMove` | 05-tasks §5T.8.3, §5T.9 | F5-16 |
| F5 (journal; 05-*) | `TestCutover_SyslogImported` | 05-logging §5, §11 item 1 | F5-15 |
| F5 (G6/G7; 05-*) | `TestSecondModuleNoAudiobookEdit` (G6/G7) | 06 §6.10; 04 §8.1 F5 | F5-18 |
| F7 (M6 migrations; 05-*) | `TestRootsCutover_FromHEADFixture` | 05-library §5L.10; 05-notify §9; 05-metadata M1.10; 05-ai §10 | F7-08 |
| F7 (M6 migrations; 05-*) | `TestWebhookMigration_InstancesAndRule` | 05-library §5L.10; 05-notify §9; 05-metadata M1.10; 05-ai §10 | F7-08 |
| F7 (M6 migrations; 05-*) | `TestIntegrationsMigration_EveryInstanceAndID` | 05-library §5L.10; 05-notify §9; 05-metadata M1.10; 05-ai §10 | F7-08 |
| F7 (M6 migrations; 05-*) | `TestAIJobMigration_EveryJobAndState` | 05-library §5L.10; 05-notify §9; 05-metadata M1.10; 05-ai §10 | F7-08 |
| F8 (updater move; 05-*) | `TestABOUpdaterSourceMatchesHEAD` | 05-updater §5U.11, §5U.12 (R2 done-check) | F8-02 |
| F8 (updater move; 05-*) | `TestApply_RawAssetNoBackup` | 05-updater §5U.11, §5U.12 (R2 done-check) | F8-02 |
| F8 (updater move; 05-*) | `TestFetchRelease_NamedReleaseOnly` | 05-updater §5U.11, §5U.12 (R2 done-check) | F8-02 |
| F8 (backup; 05-*) | `TestLegacyArchiveRestoreThenMigrate` | 05-backup §5B.10; 05-config §1.12.2 | F8-04 |
| R1 (metrics core cells; 05-*) | `TestNoGlobals` | 05-metrics §12 | R1-13 |
| R1 (metrics core cells; 05-*) | `TestKernelFamiliesGathered` | 05-metrics §12 | R1-13 |
| R1 (metrics core cells; 05-*) | `TestFamilyTaken_ThroughGlobalProvider` | 05-metrics §12 | R1-15 |
| R1 (metrics core cells; 05-*) | `TestContractScrapesServedHandler` | 05-metrics §12 | R1-15 |
| R1 (metrics core cells; 05-*) | `TestStage2_ServerAddressIgnoresHost` | 05-metrics §12 | R1-17 |
| R1 (metrics core cells; 05-*) | `TestHandler_NoDoubleGzip` | 05-metrics §12 | R1-17 |
| R1 (metrics core cells; 05-*) | `TestTrace_NoApikeyInSpanAttributes` | 05-metrics §12 | R1-32 |
| F2 (held runs; 05-*) | `TestDisabledModuleWithRunsBoots` (with AssertModuleInert's held-runs cell) | 06 §6.10, §6.4.8; 05-tasks §5T.4 step 1 | F2-16 |
| F2 (tasklint; 05-*) | tasklint's six rules and its shrink-only exemption ratchet in CI from F2 | 05-tasks §5T.8.4, §5T.9; 09 §9.4 F2 row | F2-17 |
| F2 (tasklint; 05-*) | the guards' self-tests (archtest.ResumeContinueCheckpoints, archtest.RunItemsDeclareParallelism) | 05-tasks §5T.8.4, §5T.9; 09 §9.4 F2 row | F2-17 |
| F5 (M4 cutover rows; 05-*) | `TestCutover_UndecodableRowQuarantined` | 05-tasks §5T.9 | F5-16 |
| F5 (M4 cutover rows; 05-*) | `TestCutover_ResumableCrashMatrix` | 05-tasks §5T.9 | F5-16 |
| F5 (journal route; 05-*) | `TestJournal_RequiresSystemManage` | 05-logging §5, §10 | F5-19 |
| F5 (journal route; 05-*) | `TestJournal_OtherUsersLinesNeedUsersView` | 05-logging §5, §10 | F5-19 |
| F6 (run view; 05-*) | `TestRunView_CurrentRedactedWithoutUsersView` | 05-tasks §5T.9; 05-events (core.task.run-changed.v1) | F6-02 |

---

## (f) Owner checkpoints

Owner checkpoints are not agent work: each has a brief with a NOT AGENT WORK banner. They are kept to what
09's runbook and 12 actually require of the owner.

| ID | Checkpoint | When | Blocks |
|---|---|---|---|
| OWNER-01 | Q-I: how the required checks are enforced (a ruleset with no bypass actors, and merge tooling that never merges with `--admin` past a required check, or the owner's alternative); keep or override the R1 freeze; Q-G thresholds (before R1 wires the readiness report); P14 (Basic gate on with no saved password) | before R0-05 merges and before R1-01 | R0-05, R1-01 |
| OWNER-02 | Cut the R0 production release and keep its tag; deploy; optional smoke run | after R0-01 … R0-06 | R1-01 (which starts the R1 freeze) |
| OWNER-03 | P12 (disk headroom: the first R1 boot takes a checkpoint); cut R1 with its release-note lines; deploy; optional smoke run | after every R1 task but R1-31 | every F2 task, OWNER-04, R1-31 |
| OWNER-04 | P1 (family counts and bytes; any `(unregistered)` prefix), P2, P3, P4 (env names actually set), P5, P7, P9; Q-C (the 15 never-run v2 crons) | after R1 deploys (P2, P3, P5 and P7 read a copy restored with R1's `storage restore-offline --into`), read-only, counts only | F2-14, F3-07, F5-14 |
| OWNER-05 | P12; the runbook's deploy steps 0–8 (merges finished and blocking rows fixed; the off-host archive with `.encryption_key` and `config.yaml`; optional dry run; env renames; deploy with the measured `DEPLOY_READY_TIMEOUT`; webhook receivers; reload rules and dashboards; `secrets export-keyring`; re-send invites); optional smoke run; keep or roll back; cut R2 | after R2-01 | R2-02, every R3 task |
| OWNER-06 | The keyring exported and an off-host `.encryption_key` kept with every pre-R2 archive; cut R3; deploy; optional smoke run | after R3-01 … R3-05 | — |

---

## (g) Top risks and the plan's mitigation

From 14 §14.1 and §14.1.1; each row names the tasks that carry the mitigation.

| Risk (14) | Plan mitigation |
|---|---|
| K20 — gates that gate nothing: merges and RCs were not CI-gated, a production minor auto-promoted every 10 RCs | OWNER-01 settles Q-I before R1-01; R1-02, R1-03, R1-04 build the named required checks; R1-01 and F3-07 turn the freezes on and R1-31, R2-02 turn them off only after the release; no merge with `--admin` past a required check. `/parallel-sweep`'s admin-merge on green is incompatible with Q-I's recommended ruleset and is used only in the mode OWNER-01 approves |
| E14 — R2's blast radius, one release changing every core service | R1 carries the runtime half; every R2 subsystem lands dark first and its flip is one PR that only wires, deletes and switches callers; each flip is the only PR of its wave touching `main.go`, `internal/server/` and books' registration (waves W29, W31, W33, W34, W35, W36, W39) |
| E1, E18 — a one-way migration loses data, with no previous-build oracle | F2-12 has the R0 binary write the corpus and record the answers; F2-13 the labelled overlay; F2-14 the production-shaped store; F2-15 the per-family, ID, reference and byte-identity checks and the restore drill; F2-06 the crash matrix at every chunk; F3-04 the dry run |
| E3 — the shared `idx:` and `setting:` families split wrongly | F2-15's sub-prefix lists generated from `keyfamilies.go` and the census, with the byte-identity check on every R2 PR |
| E4, E10 — secrets lost or plaintext copies left behind | F3-06 keeps HEAD's key guard and quarantines an undecryptable row; F3-03 the keyring export; F3-07 the no-plaintext canary; R1-24 the 7-day sweeper; OWNER-05 steps 1 and 7 |
| K15, E5, E6 — a renamed env name is ignored, a stale value weakens security, an empty store opens | F3-02 refuses a set retired protected or secret name and applies the fail-closed rule; F3-07's deploy-files test; OWNER-04's P4 before F3; OWNER-05 step 3 |
| E7 — the first R2 boot is killed partway | `EXTEND_TIMEOUT` per step in F2-06's runner; R1-28's 12-minute cell; F2-14 measures the boot that sets `DEPLOY_READY_TIMEOUT`; every step resumes |
| E9 — a rollback restore discards writes and revives revoked credentials | R1-27's `make rollback-restore` with its level check, F8-02's `RELEASE=` fetch; R2-01's runbook says fix forward and lists the revocations to repeat |
| E15, E16 — a half-flipped `main` reaches production; schedules stampede | the freezes; R0-06 makes unattended updates check-only; F2-16's start rule keeps ABO's executor off before F5; F5-14's schedule oracle and no-catch-up-storm test |
| E17 — the six early fixes regress in core auth | R0-02 … R0-05 ship them with their HEAD tests; F4-02 and F3-05 port them as property tests |
| K11, K14 — R1 breaks the Docker and web builds; dependency skew between modules | R1-04 (the image job), R1-05 (one web build entry point), R1-30 (the Docker frontend stage, a dry-run RC); R1-01's `check_module_skew.py` and Dependabot directories |
| K24 — feature work collides with the moves | move windows (§(d)); move PRs never share a wave; feature PRs rebase onto a move by its `old ==> new` list (09 §9.2.10) |
| K1 — the extraction stalls with two half-systems | every release ends shippable; dark-then-flip keeps every `main` booting; R1 lands the runtime and storage before any user data moves |

---

## (h) 04 package-stage keys and the tasks that move them

Every package move, creation, rename, merge and deletion 04 keys to a release or flip (04 §2.4, §3, §8.1,
§8.2, §11), with the one task that carries it. "grow" marks a core package that a later flip completes;
"create" with a later key marks the flip where 04 §8.2 counts the package as first existing in full
(`rbac`, `auth` and `ui` have leaf types from R1). Keys the plan adds beyond 04 §8.2 are 04 §3.6's
deletions, 04 §2.4's renames, the file-level moves 04 names in prose (the Basic gate's
`basicauth.go` at F3, the books-internal moves of F5), and four core packages for spec surfaces 04 does not key:
`internal/authz` (05-rbac §3.4) and three capability route packages whose names are coined (`loghttp`,
`outboxhttp`, `integrationhttp`). One 04 row is excluded:
`internal/database/mocks`'s deletion belongs to the optional mock conversion, "none; R1–R2 as the owner
decides" (09 §9.4.1; 04 §4.6 and open item 1); until then R1-26 renames it with the residue.

| 04 stage key | Kind | Package, or source → destination | Task |
|---|---|---|---|
| R1 | create | core module (core/go.mod, empty core/go.sum) | R1-01 |
| R1 | create | `core/openapi/core.json` | R1-13 |
| R1 | create | `internal/books` (composition root) | R1-15 |
| R1 | create | `mediacore/app` | R1-13 |
| R1 | create | `mediacore/archtest` | R1-01 |
| R1 | create | `mediacore/auth` (leaf types) | R1-12 |
| R1 | create | `mediacore/buildid` | R1-01 |
| R1 | create | `mediacore/coretest` | R1-09 |
| R1 | create | `mediacore/coretest/crashmatrix` | R1-21 |
| R1 | create | `mediacore/entity` | R1-09 |
| R1 | create | `mediacore/examples/hello` | R1-13 |
| R1 | create | `mediacore/health` | R1-09 |
| R1 | create | `mediacore/httpapi` | R1-13 |
| R1 | create | `mediacore/internal/httpmw` | R1-17 |
| R1 | create | `mediacore/internal/jsonschemax` | R1-12 |
| R1 | create | `mediacore/internal/openapi` | R1-13 |
| R1 | create | `mediacore/internal/pebble` | R1-20 |
| R1 | create | `mediacore/internal/restoreswap` | R1-21 |
| R1 | create | `mediacore/internal/sdnotify` | R1-16 |
| R1 | create | `mediacore/kernel` | R1-13 |
| R1 | create | `mediacore/library` | R1-08 |
| R1 | create | `mediacore/lifecycle` | R1-07 |
| R1 | create | `mediacore/log` | R1-07 |
| R1 | create | `mediacore/metrics` | R1-10 |
| R1 | create | `mediacore/module` | R1-13 |
| R1 | create | `mediacore/rbac` (leaf types) | R1-12 |
| R1 | create | `mediacore/storage` | R1-20 |
| R1 | create | `mediacore/storage/keys` | R1-01 |
| R1 | create | `mediacore/storage/restorejournal` | R1-20 |
| R1 | create | `mediacore/ui` (leaf) | R1-12 |
| R1 | create | `mediacore/version` | R1-09 |
| R1 | create | npm @mediacore/api-client | R1-30 |
| R1 | move | `internal/arch` → `mediacore/archtest` | R1-01 |
| R1 | move | `internal/boundedcall` → `mediacore/library` | R1-08 |
| R1 | move | `internal/compactprogress` → `mediacore/storage` | R1-20 |
| R1 | move | `internal/config` → `mediacore/log` | R1-07 |
| R1 | move | `internal/database` → `mediacore/storage` | R1-23 |
| R1 | move | `internal/database/dbtest` → `mediacore/coretest` | R1-20 |
| R1 | move | `internal/diag/flight` → `mediacore/health` | R1-09 |
| R1 | move | `internal/diskstats` → `mediacore/library` | R1-08 |
| R1 | move | `internal/httputil` → `mediacore/httpapi` | R1-17 |
| R1 | move | `internal/libroot` → `mediacore/library` | R1-08 |
| R1 | move | `internal/lifecycle` → `mediacore/lifecycle` | R1-07 |
| R1 | move | `internal/logging` → `mediacore/log` | R1-07 |
| R1 | move | `internal/metrics` → `mediacore/internal/pebble` | R1-23 |
| R1 | move | `internal/sdnotify` → `mediacore/internal/sdnotify` | R1-16 |
| R1 | move | `internal/security/pathvalidation` → `mediacore/library` | R1-08 |
| R1 | move | `internal/security/safepath` → `mediacore/library` | R1-08 |
| R1 | move | `internal/server` → `mediacore/httpapi` | R1-17 |
| R1 | move | `internal/server/middleware` → `mediacore/httpapi` | R1-17 |
| R1 | move | `internal/telemetry` → `mediacore/metrics` | R1-10 |
| R1 | move | `internal/util` → `mediacore/library` | R1-08 |
| R1 | rename | `internal/database` → `internal/books/store` | R1-26 |
| R1 | rename | `internal/database/aggtest` → `internal/books/store/aggtest` | R1-26 |
| R1 | rename | `internal/database/dbtest` → `internal/books/store/storetest` | R1-26 |
| R1 | rename | `internal/database/mocks` → `internal/books/store/mocks` | R1-26 |
| R1 | rename | `internal/telemetry/contract` → `internal/telemetrypolicy` | R1-10 |
| F2 | create | `mediacore/archtest/tasklint` | F2-17 |
| F2 | create | `mediacore/audit` | F2-07 |
| F2 | create | `mediacore/config` (section declarations) | F2-09 |
| F2 | create | `mediacore/health/diagnostics` (minimal) | F2-11 |
| F2 | create | `mediacore/integrations/ai` (minimal) | F2-11 |
| F2 | create | `mediacore/integrations/providerhttp` (minimal) | F2-11 |
| F2 | create | `mediacore/internal/executor` | F2-03 |
| F2 | create | `mediacore/internal/scheduler` | F2-04 |
| F2 | create | `mediacore/internal/storagemigrate` | F2-06 |
| F2 | create | `mediacore/internal/taskhttp` | F2-05 |
| F2 | create | `mediacore/storage/datamigrate` | F2-06 |
| F2 | create | `mediacore/tasks` | F2-01 |
| F2 | create | `mediacore/tasks/taskstest` | F2-01 |
| F2 | grow | `mediacore/health` (readiness core) | F2-10 |
| F2 | grow | `mediacore/rbac` (the scope catalog) | F2-08 |
| F2 | move | `tools/cmd/oplint` → `mediacore/archtest/tasklint` | F2-17 |
| F3 | create | `internal/books/cutover` | F3-06 |
| F3 | create | `mediacore/config/configtest` | F3-01 |
| F3 | create | `mediacore/internal/keyedmutex` | F3-03 |
| F3 | create | `mediacore/secrets` | F3-03 |
| F3 | grow | `mediacore/config` (loader, OverrideStore, Importer) | F3-01 |
| F3 | move | `internal/config` → `mediacore/config` | F3-01 |
| F3 | move | `internal/server/middleware` (basicauth.go) → `mediacore/httpapi` | F3-07 |
| F4 | create | `internal/cfaccess` | F4-03 |
| F4 | create | `internal/server/legacyauth` | F4-08 |
| F4 | create | `mediacore/auth` (full package, leaf types since R1) | F4-02 |
| F4 | create | `mediacore/auth/authtest` | F4-02 |
| F4 | create | `mediacore/auth/oidc` | F4-03 |
| F4 | create | `mediacore/auth/password` | F4-02 |
| F4 | create | `mediacore/backup/journal` | F4-04 |
| F4 | create | `mediacore/events` | F4-04 |
| F4 | create | `mediacore/internal/authhttp` | F4-05 |
| F4 | create | `mediacore/internal/authz` | F4-09 |
| F4 | create | `mediacore/internal/modulehttp` | F4-05 |
| F4 | create | `mediacore/internal/storagehttp` | F4-05 |
| F4 | create | `mediacore/internal/uihttp` | F4-05 |
| F4 | create | `mediacore/rbac` (full package, leaf types since R1) | F4-01 |
| F4 | create | `mediacore/rbac/rbactest` | F4-01 |
| F4 | grow | `mediacore/audit` (serving) | F4-08 |
| F4 | move | `internal/auth` → `mediacore/auth` | F4-08 |
| F4 | move | `internal/auth` → `mediacore/rbac` | F4-08 |
| F4 | move | `internal/config` → `mediacore/auth` | F4-02 |
| F4 | move | `internal/oauth` → `mediacore/auth/oidc` | F4-03 |
| F4 | move | `internal/server` → `mediacore/auth` | F4-08 |
| F4 | move | `internal/server/absauth` → `mediacore/audit` | F4-02 |
| F4 | move | `internal/server/absauth` → `mediacore/auth` | F4-02 |
| F4 | move | `internal/server/handlers` → `mediacore/internal/authhttp` | F4-08 |
| F4 | move | `internal/server/middleware` → `mediacore/auth` | F4-08 |
| F4 | move | `internal/util` → `mediacore/auth` | F4-02 |
| F5 | create | `internal/books/contract` | F5-18 |
| F5 | create | `mediacore/internal/loghttp` (name coined here) | F5-19 |
| F5 | create | `mediacore/internal/logjournal` | F5-15 |
| F5 | create | `mediacore/internal/storagetasks` | F5-03 |
| F5 | create | `mediacore/log/logtest` | F5-15 |
| F5 | move | `internal/database` → `internal/books/cutover` | F5-18 |
| F5 | move | `internal/maintenance` → `mediacore/tasks` | F5-18 |
| F5 | move | `internal/operations` → `mediacore/tasks` | F5-18 |
| F5 | move | `internal/operations/childop` → `mediacore/tasks` | F5-18 |
| F5 | move | `internal/operations/freshness` → `mediacore/tasks` | F5-18 |
| F5 | move | `internal/operations/opmode` → `mediacore/tasks` | F5-18 |
| F5 | move | `internal/operations/opswriter` → `mediacore/tasks` | F5-18 |
| F5 | move | `internal/operations/registry` → `internal/activity` | F5-18 |
| F5 | move | `internal/operations/registry` → `internal/books/contract` | F5-18 |
| F5 | move | `internal/operations/registry` → `mediacore/internal/executor` | F5-18 |
| F5 | move | `internal/opsmetrics` → `mediacore/internal/executor` | F5-18 |
| F5 | move | `internal/plugins/maintenance` → `mediacore/internal/storagetasks` | F5-03 |
| F5 | move | `internal/scheduler` → `internal/books` | F5-18 |
| F5 | move | `internal/scheduler` → `mediacore/internal/scheduler` | F5-18 |
| F5 | move | `internal/server` → `internal/books` | F5-18 |
| F5 | move | `internal/server/handlers` → `mediacore/internal/taskhttp` | F5-18 |
| F5 | move | `internal/server/handlers/operations` → `mediacore/internal/taskhttp` | F5-18 |
| F5 | move | `internal/server/handlers/opsv3` → `mediacore/internal/taskhttp` | F5-18 |
| F5 | move | `internal/walkresume` → `mediacore/tasks` | F5-18 |
| F5 | move | `pkg/ops` → `mediacore/tasks` | F5-18 |
| F5 | move | `pkg/ops/opstest` → `mediacore/tasks/taskstest` | F5-18 |
| F5 | move | `pkg/plugin/sdk` → `mediacore/tasks` | F5-18 |
| F5 | rename | `internal/operations/opswriter` → `internal/books/writer` | F5-18 |
| F5 | delete | `internal/logger/mocks` | F5-18 |
| F5 | delete | `internal/maintenance` | F5-18 |
| F5 | delete | `internal/opscatalog` | F5-18 |
| F5 | delete | `internal/plugins` | F5-18 |
| F6 | create | `mediacore/internal/eventhttp` | F6-02 |
| F6 | create | `mediacore/internal/outboxhttp` (name coined here) | F6-04 |
| F6 | create | `mediacore/internal/sse` | F6-02 |
| F6 | create | `mediacore/outbox` | F6-01 |
| F6 | create | `mediacore/realtime` | F6-02 |
| F6 | move | `internal/realtime` → `mediacore/realtime` | F6-03 |
| F7 | create | `mediacore/integrations` | F7-01 |
| F7 | create | `mediacore/integrations/ai/aitest` | F7-03 |
| F7 | create | `mediacore/integrations/ai/openai` | F7-03 |
| F7 | create | `mediacore/integrations/download` | F7-04 |
| F7 | create | `mediacore/integrations/download/deluge` | F7-04 |
| F7 | create | `mediacore/integrations/integrationstest` | F7-01 |
| F7 | create | `mediacore/integrations/metadata` | F7-02 |
| F7 | create | `mediacore/integrations/notify` | F7-05 |
| F7 | create | `mediacore/integrations/notify/mail` | F7-05 |
| F7 | create | `mediacore/integrations/notify/notifytest` | F7-05 |
| F7 | create | `mediacore/integrations/notify/webhook` | F7-05 |
| F7 | create | `mediacore/integrations/providerhttp/providerhttptest` | F7-02 |
| F7 | create | `mediacore/internal/bleveindex` | F7-07 |
| F7 | create | `mediacore/internal/integrationhttp` (name coined here) | F7-10 |
| F7 | create | `mediacore/storage/index` | F7-07 |
| F7 | grow | `mediacore/integrations/ai` | F7-03 |
| F7 | grow | `mediacore/integrations/providerhttp` | F7-02 |
| F7 | grow | `mediacore/library` | F7-06 |
| F7 | move | `internal/ai` → `mediacore/integrations/ai/openai` | F7-09 |
| F7 | move | `internal/ai/aijobs` → `mediacore/integrations/ai` | F7-09 |
| F7 | move | `internal/aidispatch` → `mediacore/integrations/ai` | F7-09 |
| F7 | move | `internal/config` → `mediacore/integrations/ai` | F7-09 |
| F7 | move | `internal/deluge` → `mediacore/integrations/download/deluge` | F7-09 |
| F7 | move | `internal/fileops` → `mediacore/library` | F7-09 |
| F7 | move | `internal/mail` → `mediacore/integrations/notify/mail` | F7-05 |
| F7 | move | `internal/mail/mailtest` → `mediacore/coretest` | F7-05 |
| F7 | move | `internal/metadata` → `mediacore/integrations/providerhttp` | F7-09 |
| F7 | move | `internal/metadata/providerhttp` → `mediacore/integrations/providerhttp` | F7-09 |
| F7 | move | `internal/pathutil` → `mediacore/library` | F7-09 |
| F7 | move | `internal/plugins/webhook` → `mediacore/integrations/notify/webhook` | F7-09 |
| F7 | move | `internal/search` → `mediacore/internal/bleveindex` | F7-09 |
| F7 | move | `internal/search` → `mediacore/storage/index` | F7-09 |
| F7 | move | `internal/security/safehttp` → `mediacore/integrations/providerhttp` | F7-02 |
| F7 | move | `internal/server` → `mediacore/storage/index` | F7-09 |
| F7 | move | `internal/watcher` → `mediacore/library` | F7-09 |
| F7 | delete | `internal/plugin` | F7-09 |
| F8 | create | `mediacore/backup` | F8-01 |
| F8 | create | `mediacore/updater` | F8-02 |
| F8 | grow | `mediacore/health` | F8-03 |
| F8 | grow | `mediacore/health/diagnostics` | F8-03 |
| F8 | move | `internal/backup` → `mediacore/backup` | F8-04 |
| F8 | move | `internal/server` → `mediacore/health` | F8-04 |
| F8 | move | `internal/server` → `mediacore/updater` | F8-04 |
| F8 | move | `internal/server/handlers` → `mediacore/health` | F8-04 |
| F8 | move | `internal/startup` → `mediacore/kernel` | F8-04 |
| F8 | move | `internal/sysinfo` → `mediacore/health` | F8-04 |
| F8 | move | `internal/updater` → `mediacore/updater` | F8-02 |
| F9 | create | hello web package | F9-01 |
| F9 | create | `mediacore/ui` (full package, leaf since R1) | F9-01 |
| F9 | create | npm @mediacore/shell | F9-01 |
| F9 | create | npm @mediacore/ui-kit | F9-01 |
| F9 | move | `internal/server` → `mediacore/ui` | F9-03 |
| R3 | merge | `internal/models` → `internal/dedup/unified` | R3-01 |
| R3 | delete | `internal/bookfiles` | R3-01 |
| R3 | delete | `internal/books/cutover` | R3-02 |
| R3 | delete | `internal/operations/state` | R3-02 |
| R3 | delete | `internal/serviceregistry` | R3-01 |
| R3 | delete | `internal/writeback` | R3-01 |
| R3 | delete | `pkg/plugin/sdk` | R3-01 |
| R3 | delete | `tools/cmd/opsgen` | R3-02 |
| R3 | delete | `tools/cmd/sdkguard` | R3-01 |
| R3 | delete | `web/src/generated/ops.ts` | R3-02 |

---

## (i) How tiers were assigned

| Tier | Model / effort | Rule | Tasks |
|---|---|---|---|
| T0 | Haiku / low | a mechanical edit that a checker proves (a pin bump, a workflow toggle) | 3 |
| T1 | Sonnet / medium | small and fully specified: an import-path move of leaf packages, one handler, a deploy-file value, a release note | 8 |
| T2 | Sonnet / high | multi-file and well specified, moderate blast radius: CI and tooling, move PRs with many importers, contracts that already exist, conversion batches whose shared bodies reroute through their shells | 38 |
| T3 | Opus / high | fresh core code with intricate contracts, security-sensitive rules, the oracles and corpora | 43 |
| T4 | Opus / xhigh–max | anything that can lose or expose data or lock users out: durable boot writers, the store engine, the swap, the one-way cutover steps, the perimeter and auth engines, and every flip PR | 31 |

Every agent task carries its one-line reason in the JSON inventory and its brief. A brief may raise its
tier when its base-commit re-verification finds more than the plan assumed; it never lowers a T4.

---

## (j) What is NOT in this plan

- **Deferred-build items** (spec README "Build scope"; 03 §3.3 Build column): `acquire`,
  `integrations/indexer`, `arrfacade` (only the `/api/v3/` reservation exists), the download
  grab/monitor/pending-recovery pipeline and every adapter but Deluge, the AI usage ledger and cap, the
  notify inbox and per-user channels, the facade-only key and route members, backup encryption, the
  seed-role merge diff, `Online` data migrations, root read shares, and updater hardening beyond the moved
  R0 updater (signed manifests, the boot-attempt counter, automatic rollback, the unit's binary layout).
- **The repo split** (12 §12.8.2): creating `falkcorp/mediacore`, the Apache-2.0 relicensing of moved files,
  `COMMERCIAL.md`, publishing `v1.0.0` and the npm packages, and the ABO cutover PR. It waits for the
  trigger T and SC1–SC10 (12 §12.3); this plan only builds the readiness reports (R1-06).
- **TV/Movies** (10) and **multi-app in one binary** (11).
- **Optional ratchets** (09 §9.4.1 last row): the `config.AppConfig` accessor and façade removal, logger
  injection, and the conversion of the 114 + ~22 tests off `internal/database/mocks` (owner's choice,
  04 open item 1).
- **Follow-ups after R2 that no release gates:** 07 §7.19 step 6 (the remaining `api.ts` sections as books
  routes become native); a books route group moving to `/api/books/v1` when rewritten (09 §9.12 O8);
  moving organizer, versions, quarantine, Deluge import and iTunes transfer onto `library.Transfer`
  (05-library §5L.10 "Order"); storing `download.ItemRef` (05-download phase 2).
- **The storage-efficiency program** (`storage_format` 2, its release B), a separate track (09 §9.0).
- **The per-release normative digests** the spec README's reading order mentions; this rebuild produces
  the plan and the index only.
- **Soaks, staging hosts and previous-build rehearsals** (D11); the optional dry run and smoke runs are
  owner steps inside OWNER-05 and the other release checkpoints.

---

## (k) Conventions

- **IDs** encode the release: `R0-nn`, `R1-nn`, `F2-nn` … `F9-nn` (R2's foundation and flips), `R2-nn` (R2's
  closing PRs), `R3-nn`, and `OWNER-nn` for owner checkpoints. The flips keep their first IDs (F3-07, F4-08,
  F5-18, F6-03, F7-09, F8-04, F9-03); rows added later take the next free number of their label, and F5-17 is
  retired (its `tasklint` work is F2-17).
- **Files** are the main paths a PR creates, modifies or deletes; a path ending in `/` is a whole directory
  (a move, a new package, or the importers a move rewires). The T6 and T7 file groups are listed by file, as the
  conversion briefs computed them at the plan's base; long lists show one line per directory in the README. Every PR also
  adds one `changelog.d/` fragment and, where it closes a tracked item, checks it off in `TODO.md`; these
  line-local edits are not listed and never decide a wave.
- **Briefs** follow `docs/agent-tasks/README.md` and the shape of the dedup-pipeline-hardening briefs:
  worktree off fresh `origin/main` with `REPO=<your clone>`, exact files, re-verify greps with expected
  hit counts, numbered steps, the code contract from the spec, the spec's test names written first,
  acceptance commands with `make ci`, out-of-scope, rollback, and repo rules (file headers bumped,
  conventional commits ending `Co-Authored-By: Claude <noreply@anthropic.com>`, `gh pr merge <n> --rebase`,
  never squash, never `--admin` past a required check). **09 names no release or flip trailer** (09 §9.2.1:
  nothing records a flip), so commits carry none.
- **Concurrency** (CLAUDE.md): a library-scale cutover step is a `DataMigration` with its own
  `tasks.Parallelism` and a bounded worker pool over disjoint key ranges (09 §9.9); conversions keep HEAD's
  concurrency 1:1 and any change of a definition's parallelism is a separate PR (05-tasks §5T.8.2).
- **Drift.** Every brief re-verifies its anchors at its base commit with expected counts; a zero-hit grep
  on an edit target, or a moved count, stops the task and is reported, never patched around; done-check
  counts are generated at the base commit (09 §9.3).

---

## (l) Plan decisions the spec leaves open

Each is a choice inside 09's rules, recorded so a reviewer can overturn it.

1. **Steps built dark, registered by the flip.** M2, M3, M4/M5 and M6 are built and tested in their own
   PRs (F3-06, F4-06, F5-15/F5-16, F7-08) and registered by their flip, which "carries the migration step"
   (09 §9.2.1); 05-config §1.12.2 already distinguishes the PR that builds M2 from the flip that ships it.
2. **Dark PRs of F3–F8 may merge before F3-07.** They change no behaviour and ship in production minors as
   F2's PRs do; 09 puts only F3 and later flips inside the freeze. The lead may hold them until F3-07 merges
   without changing the waves.
3. **Freeze toggles.** R1-01 turns the R1 freeze on, R1-31 turns it off after the R1 release; F3-07 turns the
   R2 freeze on and records the pre-freeze tag in `ci/prefreeze_release.txt`; R2-02 turns it off after the R2
   release (09 §9.2.1 names when, not which PR), all through one workflow-level env value, `RELEASE_FREEZE` in
   `prerelease.yml`, which R1-01 coins (`'R1'`, cleared by R1-31; `'R2'` set by F3-07, cleared by R2-02).
4. **The books Writer contract.** `internal/books/contract` and `internal/books/writer` are created by the
   F5 flip, when their sources leave (03 §3.5.1; 04 §2.4; 06 §6.11.1), while 05-tasks lands T2–T7 dark
   before it. The dark conversions therefore declare the narrow typed-writer interface they call
   (`ModifyBook` and its siblings) in their own package, a consumer interface as 04 §4.6 keeps them, with a
   fake in their tests; F5-18 creates the two packages, wires the implementation over `tasks.Writer` and
   points the converted packages at the shared contract. 05-tasks' conversion rule 3 gives the dark window
   this form: a shared body reaches its writer and stores only through what its shell hands it, HEAD's shell an
   adapter of the same interface over HEAD's writer.
5. **The exemption list is a directory.** `tasklint` and its exemptions land at F2 (F2-17; 05-tasks §5T.8.4).
   The plan writes them as one entry file per row in `internal/books/testdata/tasklint_exempt.d/`, read as one union: each conversion row's file
   lists the books files it converts, F5-18's lists the files whose HEAD definitions no conversion converts (T4's
   among them), and F5-03, whose kinds are core's, has none. A row deletes only its own file; the ratchet fails a
   PR that adds a file or a line to the union; F5-18 requires the directory empty. The directory lives in ABO,
   not in core's `archtest/tasklint`, because it names books' files.
6. **One PR per flip label**, F8 and F7 included, because 09's flips table speaks of "the flip PR"; F7 is
   also one step, M6.
7. **F9 in R2 by default**, decided at R2-01: if F9-03 has not merged when F8-04 has, F9 moves to R3
   unchanged and R2-01 drops it.
8. **Unkeyed 04 rows.** `internal/bookfiles`, `internal/writeback` and the `internal/models` merge go in
   R3-01; `internal/logger/mocks` goes with `retention.go` in F5-18.
9. **`make rollback-restore RELEASE=`** lands with `updater fetch-release` in F8-02; R1-27 lands the
   checkpoint and archive branches.
10. **Where the R2 corpus lives:** `internal/books/testdata/r2corpus/`, with its scripts under
    `scripts/r2corpus/`.
11. **Callers move with the code.** A move rewrites every caller in its own PR, with no forwarding shim (04 §2.1:
    the staying side's references are rewritten in the same PR): F4-02 lists its callers, F7-09 every importer
    of what it moves, F8-04 every first-party `/api/health` caller (09 §9.2.9).
12. **A gate whose last half needs the flip is the flip's.** `TestBooksIndexRebuiltAtCutover` is F7-09's (F7-08
    lands the dark half), `TestLegacyArchiveRestoreThenMigrate` F8-04's (after F8-01's staging half) and
    `TestOIDC_AppHandoffMintsABSCode` F4-08's (after F4-05's core cells). A test that exists from an earlier
    release is owned by the row that creates it and extended later: `TestHealth_UnauthenticatedTokenSetGolden`
    by R0-06 (R1-24, F2-10 and F8-04 extend it), `TestABOMetricsGatherNoDuplicate` by R1-15 (F2-03 extends it).
13. **Leaf types before the tasks API.** F2-01 waits for F2-09 and F2-10 instead of carrying fallback types;
    `tasks.RunViewRedactor`, whose `events.RedactFunc` has its first consumer in the realtime filter, lands
    with F6-02, so F2-01 does not wait for `events`.
14. **Coined paths.** `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/{manifest,registrar,deps}.go`,
    `internal/books/services.go`, `internal/books/config.go`, `internal/books/cutover/stepids.go` (M2–M6's step
    IDs, reserved by F3-06), `internal/books/crash_matrix_test.go`, the route packages
    `core/internal/{loghttp,outboxhttp,integrationhttp}`, `internal/books/testdata/tasklint_exempt.d/` and
    `docs/system/r3-release-note.md` are the plan's names; a brief may rename one before its first PR merges,
    and the inventory follows.

---

## (m) Spec gaps and open questions

**No spec gap forced a SPEC-GATED row.** Every task has the normative text its brief needs. Open
questions the plan carries instead:

| # | Question | Owner | Effect on the plan |
|---|---|---|---|
| 1 | Q-I: how the required checks are enforced, and whether `/parallel-sweep` may admin-merge | owner (OWNER-01) | decides the merge mode of every wave from R1-01 |
| 2 | Q-C: the 15 v2 crons that never ran (delete by default) | owner (OWNER-04) | F5-14's schedule declarations and the release note |
| 3 | Q-G: the split-readiness thresholds | owner (OWNER-01) | R1-06's report only |
| 4 | What `frontend-enabled` does in the shared release workflow at `.github/ghcommon-ref.txt`'s SHA | read by R1-05's agent; BLOCKED to the lead if unreadable | R1-05's release callers |
| 5 | The pre-freeze release tag, known only when F3 merges | F3-07 records it | the restore drill and `TestCutoverRefusedByOlderBuilds` |
