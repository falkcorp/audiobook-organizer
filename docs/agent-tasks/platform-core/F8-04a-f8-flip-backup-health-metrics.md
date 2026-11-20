<!-- file: docs/agent-tasks/platform-core/F8-04a-f8-flip-backup-health-metrics.md -->
<!-- version: 1.0.0 -->
<!-- guid: e9dd33db-6f96-4beb-9224-3ff0bde196d2 -->
<!-- last-edited: 2026-11-20 -->

# F8-04a — F8 flip, part a, one PR: backup in core's layout with its routes and books' restore stager on it, health wired with /api/health and /api/v1/health deleted and every caller on /health, the kernel /metrics mount with no rename, the status-topic publisher, InterimExport and app.Options.InterimBackends deleted

| Field | Value |
|---|---|
| ID | F8-04a |
| Release | R2, flip F8, part a (backup, health, metrics). Part b, the updater, is `F8-04b-f8-flip-updater-wired.md` |
| Tier | **T4** (Opus / xhigh–max): a flip PR; it changes the `/health` callers, the Basic-gate exempt set, the metrics mount, the backup owner and books' live restore path |
| Wave | W38 in the plan (alone: it owns `main.go`, `internal/server/`, books' registration and the kernel and module wiring files in W38). **Merge order is not the wave number:** the merge-order ruling below lets it merge before F6-03 if it is green first. Runs inside an announced **move window** |
| Depends on | F3-07, F4-08, F5-18, F8-01, F8-02, F8-03, F8-03b. **Not** F6-03, **not** F7-09 (pre-scope `F8-04-prescope.md` section 5, checked input by input) |
| Owns gate tests | the dashboard-reference test over the kernel `/metrics` mount (extension of `TestDeployReferencedSeriesServed`); `TestLegacyArchiveRestoreThenMigrate` (F8-01 landed its staging half) |
| Spec | 09 §9.4 R2 flips table row F8, §9.6 rows 3 and 19, §9.2.7, §9.2.9; 03 §3.8 F8; 05-backup §5B.10, B1, B7; 05-health §5H.3.3, §5H.10 (row F8); 05-metrics §6, §12, §13; 05-http-api §8.1 (F8 row), §12; 06 §6.4.10 (F8 row); 04 §8.1 F8 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **One atomic PR** (09 §9.2.1): it wires the
subsystems, deletes what they replace and switches every first-party caller in the same PR, so the merged `main` boots and
passes e2e. The R2 freeze is on: do not touch `.github/workflows/prerelease.yml`. This row replaces the backup, health and
metrics half of the old `F8-04-f8-flip-backup-health-metrics.md` (superseded 2026-11-20); that file stays as history.

> F8-03b has merged: the diagnostics bundle is share-safe (`seams.d/F8-03.md`, Round 13). When this row mounts `healthhttp`
> and builds the Exporter, pass the library roots both as `diagnostics.Config.Roots` and as `healthhttp.Deps.Roots`
> (nil roots are fail-closed: every location is written `<path>`). Also set `diagnostics.Config.RoutePatterns` to a
> function returning the router's registered patterns: for each `RouteInfo` from `Router.Routes()` both `ri.Pattern` and
> `strings.TrimSpace(ri.Method + " " + ri.Pattern)`. The list must come from the router, never from request data; it is
> called once per bundle, so keep it one pass over the table. A test builds the real router, sets `RoutePatterns` from it and
> checks a registered `GET /api/...` template survives a bundle while a request path does not.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f8-04a-flip" -b agent/platform-core-f8-04a-flip origin/main
cd "$REPO/.worktrees/pc-f8-04a-flip"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Announce the move window one working day ahead, naming the 04 rows below (09 §9.2.10), and name part a and part b
separately: they have separate windows.

## Coordinator rulings of 2026-11-20 (they bind this row)

- **Merge-order ruling.** F8-04a merges independently of F6-03: whichever is green first after F5-18 goes first, and the
  second rebases the shared lines (blocker 15) and deletes `internal/server/toplevel_auth_chain.go` and its test. This
  amends the 11-19 23:22 ruling ("F8-04a after F6-03"). The restore shutdown frame holds in either order (blocker 16).
  Whichever lands first, F8-04b follows F8-04a.
- **Stager ruling:** books' restore stager moves onto core `backup` (Q2): re-point `internal/books/restore_stager.go`,
  `internal/books/options.go` and the organizer's auto-backup. The 04 key moves `internal/backup` to `mediacore/backup`, and
  core's catalogue already lists `legacy-abo/0` archives (F8-01). This is the largest unlisted item and carries the most
  data-loss risk, so: gate it on `TestLegacyArchiveRestoreThenMigrate` plus the books restore drill tests
  (`internal/books/{live_restore,restore_drill,restore_stager}_test.go`), and **do not merge without a read-only
  prod-copy restore rehearsal** (the owner's standing OK, 11-19): restore a copy of a production archive into a scratch
  directory on a copy of the data, never against the live store, and paste the per-family counts into the PR.
- **Status-publisher ruling:** F8-04a owns the status publisher. `Registry.SetStatusPublisher(health.PublishStatus(<core scope>))` and so the
  only publisher of `core.system.status-changed.v1` (F8-03's follow-up 2, `seams.d/F8-03.md:81`, and TODO-F8-03-BOOT-WIRING,
  `TODO.md:353`). F6-03 drops it from its Goal 2; nothing publishes the topic until this row lands, which is harmless
  because no SPA code reads it.
- **Deluge-moves ruling:** the version-swap and import Deluge moves stay in-process (F6-03's ruling; nothing for this row to do, listed so
  the two briefs agree).

## The 16 blockers the old F8-04 brief did not list, split by part

Counted against the old brief on 2026-11-20 (origin/main `f18ad2686`); *carried* = found 11-19, *new* = since, *changed* = a PR
since changed what the flip must do. Source: the coordinator's F8-04 pre-scope. The pre-scope notes (`.claude/notes/pc-tools/`) are gitignored coordinator scratch, not in a clone; their content is inlined in this brief.

| # | Blocker | Part |
|---|---|---|
| 1 | The split was adopted but never written down (no brief, no inventory row, no plan W38 row): this PR | both |
| 2 | F8-01's hand-offs | **a** |
| 3 | F8-02's hand-offs | b |
| 4 | F8-03's hand-offs | **a** |
| 5 | The updater code contract is wrong (AppID, asset name) | b |
| 6 | `internal/updater` cannot be deleted whole | b |
| 7 | `internal/backup` has 20 importers; books' live restore is one | **a** |
| 8 | F3-07 rewrites the startup table and health tokens | **a** |
| 9 | The reach oracle | **a** (b adds the 3 update routes) |
| 10 | Metrics scope and the shared auth chain | **a** |
| 11 | `request_size.go:45` special-cases `/backup/` | **a** |
| 12 | The acceptance grep has a new non-caller hit | **a** |
| 13 | F4-08 wires half of backup's storage; reuse it | **a** |
| 14 | F6-03 and F8-04 both claimed the status publisher | **a** (status-publisher ruling) |
| 15 | The maintenance-window step 1 may already be gone | b |
| 16 | Backup restore in the SPA relies on hub A's shutdown frame | **a** |

### Blockers of part a, in full

2. **F8-01's hand-offs** *(carried; `seams.d/F8-01.md:15-21`, `:81`, `:160-161`)*.
   - **Close the task path's plain restore before registering `StageRestoreTask`.** `authorizeOptions` returns nil when no
     privileged option is set (`core/backup/authorize.go:85-89`), so any principal that can start the task stages a plain
     restore (slice A1).
   - Compose `backup.Recheck` with the swapper's default check.
   - Build the `Keyring`, `Markers`, `Inspector`, `Stager` and `Progress` adapters; register the `backup` scope and
     `backuphttp.Register`.
   - `Options.PruneJournal` has no implementation (no trim function in `core/backup/journal`; slice A2).
   - The legacy archive's rehash and SHA-256 binding (review m6).
4. **F8-03's hand-offs** *(carried; `TODO.md:353-355`; `seams.d/F8-03.md:78-88`)*.
   - Build the registry with `health.NewRegistryWith` (`buildHealth` is already called at `core/kernel/boot.go:562` with
     `NewRegistry()`); `SetStatusPublisher(health.PublishStatus(<core scope>))`.
   - **`ViewerFor`:** compile with `Interactive` only for a `session` actor, never hand an API-key run the interactive
     checker, and test it with a `token` actor (slice A5).
   - The real `Enqueuer`, `Runs`, `Deps.Retention`, `Deps.Now` and `ExportParams.Perms`.
   - Declare `core:system.status` in the RBAC catalog.
   - Register `core.diagnostics.prune` and its config keys (slice A3; the keys wait for F3-07's `core_sections.go`).
   - Clamp the storage close to the shutdown budget (30 s default against a 25 s budget; TODO-F8-03-CLOSE-TIMEOUT).
   - Reserve `/livez` and `/readyz` in the gin SPA fallback (live-additive: it goes in the fold, not in a dark slice).
7. **`internal/backup` has 20 importers** *(carried)*. Non-test: `internal/books/restore_stager.go:34-35`
   (`backup.StageRestore`), `internal/books/options.go`, `internal/organizer/service.go` (the auto-backup),
   `internal/server/handlers/system/handler.go`, `internal/appdirs/appdirs.go`, `internal/config/config.go`.
   `internal/arch/layering_test.go:322` carries a `{"internal/config", "internal/backup"}` exception that goes with it.
8. **F3-07 rewrites the startup table and the health tokens this row builds on** *(changed by #4765)*:
   `internal/startup/steps.go` drops `loadConfigFromDB` and renames `settingsWrites` to `gapFillStrip` (token
   `gap_fill_strip`); `core/health/startup.go` drops `loadConfigFromDB` from `CoreStartupSteps()`; `core/kernel/health.go` gains a
   `Config` reporter; F3-07 touches `handlers/system/health_tokens_test.go`, `core/httpapi/basicgate.go` and deletes the three
   `internal/server/middleware/basicauth*.go` files. So part a is cut after F3-07 and F8-03's "seven core rows" reads six.
   `internal/startup` has 11 importers.
9. **The reach oracle** *(carried)*. Each deleted gin route becomes R0-only: the 4 backup routes, `GET /api/health`,
   `GET /api/v1/health` and `GET /metrics`. Add them to `reachWantR0Only` (`internal/books/cutover/reach_gate_test.go:369`),
   edit `reach_golden_test.go:85-87` (`/metrics`) and `:135-136` (backup restore), re-pin `reach_baseline_test.go:295-306,374-378`.
   F4-08, F6-03 and F8-04b edit the same files, so the flips merge one at a time.
10. **Metrics scope and the shared auth chain** *(carried)*. The gin `/metrics` accepts any credential, and none at all when
    auth is off (`internal/server/server_lifecycle.go:2772-2783`); the kernel mount requires `core:metrics.read`. So the prod
    scrape key (`deploy/prometheus/scrape-config.yml:39`) must hold that scope after the flip: an **owner check at deploy**.
    The gin handler already serves the attached Provider (`internal/server/server.go:758-770`), so the move is a route move
    over the same gatherer ("no rename" holds). `toplevel_auth_chain.go` serves only `/metrics` and `/api/events`; the
    second of this row and F6-03 to land deletes it.
11. **`internal/server/middleware/request_size.go:45` special-cases `"/backup/"`** *(carried, minor)*. Core routes do not pass
    through gin middleware, so the arm is dead once the gin backup routes go: delete it here.
12. **The acceptance grep has a new non-caller hit** *(new)*: `scripts/tests/test_assemble_r2_notes.py:123` (R2-01a's fixture
    names `/api/health` as a deleted path). Add `':!scripts/tests/test_assemble_r2_notes.py'`. The three `basicauth*.go` hits
    leave with F3-07. At `f18ad2686` the caller grep lists **38** files (the old brief said 45).
13. **F4-08 wires half of backup's storage, and this row reuses it** *(new)*. The F4-08 brief (`:40-47`) registers
    `backup/journal`'s `StorageSpec` for `c/backup/` and runs P4d steps 2 and 3. Your `Markers` and `Keyring` adapters must bind
    **that** journal handle, not open a second one. Open F4-08 follow-ups also land in `core/backup/journal`
    (`todo.d/2026-11-19-f4-08-movefile-mkdir-parent-fsync.md`; #4776 round 4 m1: hand-built in-memory Seams without `Shared`
    fail loudly once `journalfs.From` is wired). Part a's kernel backup adapter waits for F4-08's journal wiring, and its file
    names must not collide with F4-08's new `core/kernel` files.
14. **Status publisher ownership** *(new)*: the status-publisher ruling.
16. **Backup restore in the SPA relies on hub A's shutdown frame** *(new)*. `web/src/hooks/useBackupHandlers.ts:130` ("The
    server tells every connected page it is going down (system.shutdown)"), `App.tsx:146` shows the backdrop. If this row
    lands **before** F6-03 the frame must still be produced. Verified on `main`: core `backup.Options.Restart` is documented as
    `Lifecycle.RequestRestart` (`core/backup/options.go:260-262`, `types.go:239`) -> `RequestRestart` closes the kernel's
    restart channel (`core/kernel/supervisor.go:150-160`) -> the kernel's stop drains the listener through
    `R1Listener.Shutdown`, which calls `s.beforeDrain()` first (`server_lifecycle.go:1746-1752`) -> `beforeDrain` broadcasts
    `system.shutdown` on hub A (`:991-1006`). So the frame survives **as long as G2 wires `Options.Restart` to
    `Lifecycle.RequestRestart` and the SPA's new backups client sends `restart: true` or handles `restart_required`**
    (`core/backup/tasks.go:72,78`): a requirement of slices A6 and G2. If this row lands **after** F6-03 the frame is
    `core.stream.closed.v1{shutdown}`. `web/tests/e2e/backup-restore.spec.ts:79` mocks `/api/v1/backup/...` paths.

### Hot spots shared with F6-03 (blocker 15 of that brief)

`server_lifecycle.go:2711-2712` (one gzip condition naming `/api/health` and `/api/events`); `App.tsx` (the health poll at
`:173`, the stream at `:19,145`); `web/tests/e2e/utils/test-helpers.ts` (the health mocks at `:473,1837`, the EventSource mock at
`:314-356`); `legacy_manifest.go`; `toplevel_auth_chain.go`; the reach tests; every wiring file. Whichever lands second rebases
these lines; neither PR may reformat them. Also: F7-09 replaces `internal/search`, so books' `searchindex` startup step
(declared here over HEAD's `IndexService`, `server_lifecycle.go:172`) is re-pointed by F7-09's F-lib domain.

## Goal (09 §9.4 R2 flips table, row F8; 06 §6.4.10 F8 row)

0. **Module members and kernel wiring**: add `Registrar.StartupSteps`, `Registrar.Diagnostics`, `Registrar.Backup` and
   `Deps.Backup` to `core/module/registrar.go`, `deps.go` and `manifest.go`; in `core/kernel/boot.go` and `kernel.go` call
   F8-03's start functions in `core/kernel/health.go` (`registerCoreComponents` `:134`, `startHealthLoops` `:235`,
   `beginHealthShutdown` `:248`: none is called today), build backup, and mount the backup, health and diagnostics routes;
   regenerate `core/openapi/core.json` and the client. **Service registry** (06 §6.11.1 step 4): delete `system`
   (`registry_wire.go:160`), `dashboard` (`internal/sysinfo/register.go:16`) and `metricsstore` (`registry_wire.go:339`) from
   `serviceDefs` (`registry_wire.go:104`); the `updater`/`updatescheduler` Overrides are F8-04b's.
1. **Backup.** The kernel builds core `backup` (F8-01); `Registrar.Backup` and `Deps.Backup` live; `/api/core/v1/backups*`
   mounted; HEAD's `/api/v1/backup/*` gin routes deleted with the SPA switched; **books' restore stager, `options.go` and the
   organizer's auto-backup re-pointed onto core `backup`** (stager ruling) and `internal/backup` deleted (`F8 move
   internal/backup -> mediacore/backup`); the auto-backup becomes `Deps.Backup.Ensure(ctx, 6h, "pre:books.organize")`
   (05-backup §5B.10). The first `core.backup.create` writes `mediacore-backup/1`; existing archives list as `legacy-abo/0`.
2. **Health.** The kernel mounts `/livez`, `/readyz`, `/api/core/v1/health`, `/api/core/v1/system/*` and the diagnostics
   routes; books' `/health` is served from the registry with ABO's shape (OPS-27); `/api/health` and `/api/v1/health` deleted
   and **every** caller switched to `/health`; `BasicExempt` moves to `/health`; `internal/startup` becomes `health.Startup`
   (`F8 move internal/startup -> mediacore/kernel`), books' `searchindex` a Degraded step; the host parts of `internal/sysinfo`
   replaced by `health/sysinfo` (nil roots; per-root capacity arrives with F7-09); `internal/diagnostics`'s zip half replaced by
   `health/diagnostics` with books' `Contributor` sections, so `diagnostics.InterimExport`, books' backend and
   `app.Options.InterimBackends` are deleted. **Status publisher** (status-publisher ruling): `SetStatusPublisher(health.PublishStatus(...))`.
3. **Metrics.** The kernel `/metrics` mount (`core/kernel/metrics_mount.go`) replaces the gin `/metrics` route with **no
   metric rename**; it requires `core:metrics.read`, while `/livez` and `/readyz` stay Public (`TestInfra_MetricsRequiresScope`).
   The kernel also emits `<namespace>_build_info` (label `version`; one of 05-metrics §6's always-on smoke families; a new
   family, not a rename).

04 keys this PR moves: `F8 move internal/backup -> mediacore/backup`; `F8 move internal/sysinfo -> mediacore/health`;
`F8 move internal/server -> mediacore/health`; `F8 move internal/server/handlers -> mediacore/health`;
`F8 move internal/startup -> mediacore/kernel`. (`F8 move internal/server -> mediacore/updater` is F8-04b's.)

## Invariants

- **I1.** After merge nothing in the repository requests `/api/health` or `/api/v1/health`, and both paths answer 404.
- **I2.** `/health` keeps ABO's shape and its `ready` field; its unauthenticated body carries only the golden token set
  (`TestHealth_UnauthenticatedTokenSetGolden`, with its F8 cells here: books' `/health`, `/readyz`, `GET /api/core/v1/health`).
- **I3.** The Basic gate exempts `/health` in place of the two deleted paths and nothing else changes (`TestBasicGate_ExemptSetGolden`).
- **I4.** `/metrics` serves the same series names as before plus `<namespace>_build_info`; every series a shipped dashboard or
  rule names is served; a plain gather returns no error and no duplicate family (`TestABOMetricsGatherNoDuplicate`).
- **I6.** `TestLegacyArchiveRestoreThenMigrate` passes on the full step list **through the re-pointed books restore stager**.
- **I7.** `unit-lifecycle` passes (this PR changes `main.go` and `internal/backup/**`).
- **I8.** A plain restore through `StageRestoreTask` without an owner proof is refused; a key-started run gets the non-interactive checker.
- **I9.** `/readyz` answers 503 before startup completes and once shutdown begins; a backup restore still shows the SPA's shutdown backdrop.

## Slices (pre-scope section 3; a slice that has merged by your BASE is not redone)

Dark slices that may start on `main` today (each adds `scripts/deadcode-baseline.txt` lines; resolve line by line, sorted):
- **A1 restore closure** (S): `core/backup/authorize.go` plus test, and the legacy archive's rehash and SHA-256 binding (m6). No open PR touches `core/backup/`.
- **A2 journal trim** (M): `core/backup/journal/*`, a trim function `Options.PruneJournal` can call. After A1. **Ask the F4-08 lead first**: F4-08's body wires this journal.
- **A3 diagnostics prune task** (S): `core/health/diagnostics` `PruneTask(...)` over `Prune(root, retention, now)`, plus the tasklint allowlist line in `core/archtest/tasklint/core_test.go`.
- **A4 metrics mount** (S): new `core/kernel/metrics_mount.go` (`Infra("GET /metrics", core:metrics.read, ...)` not called by `boot.go`), a 401/403 test router, the `build_info` gauge constructor and its `TestKernelFamiliesGathered` cell. Dark **only if** the gauge is registered inside the mount function; registered at boot it is live-additive.
- **A5 kernel diagnostics adapter** (M): new `core/kernel/diagnostics_wire.go` (`ViewerFor`, `Enqueuer`, `Runs`, `RoutePatterns`, `Roots`). After F4-08's body is open so file names do not collide.
- **A6 SPA backups client** (M): new `web/src/services/coreBackups/` on `/api/core/v1/backups*` (problem+json, `ApiAuthRedirectError`, types hand-written from `core/internal/backuphttp/routes.go`), unwired; `api.ts` untouched. Must send `restart: true` or handle `restart_required` (blocker 16).
- **A7 split briefs** (docs): this brief, `F8-04b-*.md`, the inventory rows, the plan's W38 and W38b rows, the R2-01 and OWNER-05 "Depends on" lines (landed by the 2026-11-20 amendment PR).

Gated: **G1** config keys and startup rebase after F3-07 (the diagnostics retention keys in `core_sections.go`; books' startup rows onto `gapFillStrip`; `Registrar.StartupSteps` with the `*health.Startup` in a field). **G2** kernel backup adapter after F4-08 (`core/kernel/backup_wire.go`: `Keyring`, `Markers` on F4-08's journal, `Inspector` with `DerivedGeneration` nil, `Stager` (`restoreswap`), `Progress`, `Recheck` composed with the swapper default, `Options.Restart` = `Lifecycle.RequestRestart`; the stage-13 cells of `backuphttp` and `healthhttp`; the RBAC declaration of `core:system.status`). **G3** tasks registration after F5-18 (`core.backup.{create,verify,prune,stage-restore}`, `core.diagnostics.export` native body, `core.diagnostics.prune`; delete `diagnostics.InterimExport`, books' backend and `app.Options.InterimBackends`).

**The fold (after F5-18, beside F6-03):** (1) module members, boot wiring, mount `healthhttp` (with `Roots` from books' current roots as `[]diagnostics.PathRoot`), `backuphttp` and A4; (2) re-point books' restore stager, `options.go` and the organizer's auto-backup, delete `internal/backup`, the `layering_test.go:322` exception and `request_size.go:45`'s arm; (3) `internal/startup` to `health.Startup`, sysinfo host parts, diagnostics zip half; (4) delete `/api/health` and `/api/v1/health`, move `BasicExempt`, switch the caller list, reserve `/livez` and `/readyz`, `TestDeletedHealthPaths404`; (5) delete the gin `/metrics` route and `toplevel_auth_chain.go` if F6-03 landed; (6) SPA backup calls onto A6, the `App.tsx` health poll onto `/health`, e2e mocks, the reach oracle, layering, shard weights, `core.json`, the deadcode prune, the changelog fragment; (7) delete the three services.

Part a's nil inputs are safe, not regressions (pre-scope section 5): `DerivedGeneration` nil means every derived item is stale and never offered on restore, and HEAD's archives carry no derived index (`internal/backup/backup.go:694-738`); nil sysinfo roots lose only the new per-root capacity; nil diagnostics roots write `<path>`.

## Files

| Action | Path |
|---|---|
| modify | wiring this PR owns in W38: `main.go`, `cmd/root.go`, `internal/books/module.go`, `internal/server/registry_wire.go`, `internal/sysinfo/register.go`, `internal/server/server.go`, `core/app/options.go`, `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go` |
| create | `core/kernel/metrics_mount.go`, `core/kernel/backup_wire.go`, `core/kernel/diagnostics_wire.go`, `web/src/services/coreBackups/` |
| modify | `core/backup/` (A1, A2), `core/health/diagnostics/` (A3, the `Contributor` sections) |
| regenerate | `core/openapi/core.json`, `core/web/packages/api-client/src/generated/` |
| modify / delete | `internal/server/` (backup, health and metrics routes and handlers; Basic exempt set; `middleware/request_size.go`; `toplevel_auth_chain.go` if F6-03 landed; tests) |
| delete / move | `internal/backup/`, `internal/startup/`, `internal/sysinfo/` (host parts), `internal/diagnostics/` (zip machinery) |
| modify | `internal/books/` (restore stager, `options.go`, `services.go`), `internal/organizer/` (auto-backup), `internal/appdirs/`, `internal/config/` (the `internal/backup` importers) |
| modify | the reach oracle: `internal/books/cutover/reach_gate_test.go`, `reach_golden_test.go`, `reach_baseline_test.go` |
| modify | `Dockerfile` (verify `HEALTHCHECK` stays on `/health`), `scripts/deploy-wait-ready.sh` (verify `/health`) |
| modify | `web/src/` (backup and health callers; `App.tsx`'s health poll and `App.health.test.tsx`), `web/tests/e2e/` health and backup mocks (`utils/test-helpers.ts`, `backup-restore.spec.ts`) |
| modify | every first-party `/api/health` or `/api/v1/health` caller at BASE: re-run the caller grep, paste the list into the PR description as the checklist. At `f18ad2686` it lists 38 files; `715da70dc` already moved the scripts, docs, deploy skill and `binary-smoke.yml`; `scripts/record_demo.js` and `scripts/run_demo_recording.sh` keep a `/api/health` fallback this PR deletes. Never edit `internal/books/rbacreach/`, `scripts/r2corpus/`, `scripts/tests/test_record_r0_reach.py` (R0 recordings) |
| modify | `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json`, `scripts/deadcode-baseline.txt`; `.mockery.yaml` only if a mocked interface moves |
| modify | `core/archtest/tasklint/core_test.go` (`TestTasklint_Core`, `TestTasklint_CoreGuards`: the F8 backup and diagnostics task kinds the kernel now registers must satisfy tasklint's rules; extend the package list if the new `core/kernel/backup_wire.go` and `diagnostics_wire.go` are not already covered) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected at your base; regenerate and record both values in the PR)

```bash
# 1. Caller grep. Expected at f18ad2686: 38 files before this PR; 0 after.
git grep -n -I -e '/api/health' -e '/api/v1/health' -- . \
  ':!docs/specs' ':!docs/plans' ':!docs/archive' ':!docs/agent-tasks' ':!docs/diagnostics' ':!CHANGELOG.md' ':!TODO.md' \
  ':!changelog.d' ':!.claude/notes' ':!internal/books/rbacreach' ':!scripts/r2corpus' ':!scripts/tests/test_record_r0_reach.py' \
  ':!scripts/tests/test_assemble_r2_notes.py'
# 1b. Module members this PR adds. Expected: 0
grep -rn 'StartupSteps\|func (r \*Registrar) Diagnostics\|func (r \*Registrar) Backup' core/module/ | wc -l
# 1c. F8-03's start functions exist and boot.go calls none. Expected: functions >= 3, calls 0
grep -rnE 'func .*(registerCoreComponents|startHealthLoops|beginHealthShutdown)' core/kernel/health.go | wc -l
grep -rnE 'registerCoreComponents|startHealthLoops|beginHealthShutdown' core/kernel/boot.go core/kernel/kernel.go | wc -l
# 2. The gin routes this PR deletes (backup, health, metrics; the update routes are F8-04b's)
grep -rn '"/backup/\|GET("/metrics"\|"/api/health"\|"/api/v1/health"' --include='*.go' internal/server/ | grep -v _test
# 3. The interim backend to delete. Expected: InterimExport in core/health/diagnostics, InterimBackends in core/app, books' backend
grep -rn 'InterimExport\|InterimBackends' --include='*.go' core/ internal/ main.go | grep -v _test
# 4. The dark packages this PR wires. Expected: each present, none with a production caller
ls core/backup/ core/internal/backuphttp/ core/internal/healthhttp/ core/health/sysinfo/ core/health/diagnostics/
grep -rn 'backup.New(\|backuphttp.Register\|healthhttp.Register\|diagnostics.New(' --include='*.go' . | grep -v _test | grep -v '^./core/\(backup\|internal/backuphttp\|internal/healthhttp\|health\)/' | wc -l   # 0
# 5. The Basic exempt-set golden: TestBasicGate_ExemptSetGolden (F3-07 renames) or TestBasicGate_HEADExemptRules (core/httpapi/basicgate_test.go:225)
grep -rn 'func TestBasicGate_ExemptSetGolden\|func TestBasicGate_HEADExemptRules' --include='*.go' .
# 6. The dashboard-reference test. Expected: 1 (internal/telemetrypolicy/deploy_reference_test.go:50)
grep -rn 'func TestDeployReferencedSeriesServed' --include='*.go' .
# 7. Dependencies merged. Expected: each >= 1
grep -rn 'gap_fill_strip' internal/startup/ | wc -l                     # F3-07
grep -rn 'func (r \*Registrar) Topics' core/module/ | wc -l             # F4-08
grep -rn 'InterimBackends' core/app/options.go | wc -l                  # F5-18 adds the field (F5-18 brief :85, :98); on main today it is a comment
```

## STOP conditions

1. Grep 4 misses a package (an F8 dark row is not merged), grep 6 finds no definition, grep 5 finds neither golden, or grep 1
   lists a caller in a path you cannot change in this PR (report it; do not leave a caller on a deleted path).
2. F3-07, F4-08 or F5-18 is not merged (grep 7).
3. `TestLegacyArchiveRestoreThenMigrate` or a books restore drill test fails after re-pointing the stager, or the prod-copy
   restore rehearsal was not run (stager ruling). Never merge a restore change on green unit tests alone.
4. A plain restore reaches `StageRestoreTask` without an owner proof (A1 not landed).
5. The restore path no longer produces the SPA's shutdown signal (blocker 16: `Options.Restart` not wired to `Lifecycle.RequestRestart`).
6. The `Markers` or `Keyring` adapter opens a second journal handle instead of binding F4-08's (blocker 13).
7. Any caller of the deleted paths remains in `scripts/`, `.github/`, `deploy/`, `web/` or skills.

## Steps (one PR; every commit builds)

1. Run the re-verify block; paste grep 1's list into the PR description as the caller checklist.
2. Write or extend the tests first (below).
3. Module members and the kernel wiring (G1, G2, G3 as they are not yet on `main`); mount backup routes; re-point books' restore
   stager, `options.go` and the auto-backup; delete the gin backup routes, `internal/backup`, the `layering_test.go:322`
   exception and `request_size.go:45`'s arm; switch the SPA's backup calls (A6).
4. Health: call F8-03's start functions from `boot.go`; `NewRegistryWith`; `SetStatusPublisher`; books' `/health` from the registry;
   `Registrar.StartupSteps`; delete `/api/health` and `/api/v1/health`; move `BasicExempt`; switch every caller; reserve `/livez` and
   `/readyz` in the SPA fallback; sysinfo host parts; books `Contributor` sections; delete `InterimExport` and `InterimBackends`.
5. Metrics: mount `/metrics` from the App's Provider with `core:metrics.read`; delete the gin route and (if F6-03 landed)
   `toplevel_auth_chain.go`; register `<ns>_build_info`; extend the dashboard-reference test to scrape the kernel mount.
6. Delete the three services; reach oracle; layering; shard weights; `.mockery.yaml` if needed; `core.json` and the client; deadcode prune.
7. `go build ./... && (cd core && go build ./...)`, then the acceptance block. Headers. Fragment.

## Tests (write first)

- `TestBasicGate_ExemptSetGolden` with `/health` replacing the two deleted paths; `TestHealth_UnauthenticatedTokenSetGolden`'s F8 cells
  (and the `gap_fill_strip` rename); `TestDeletedHealthPaths404` (name coined here: both paths 404 through the full router, `GET /health`
  answers ABO's shape).
- The dashboard-reference test over the kernel `/metrics` mount, with `promtool test rules`; `TestABOMetricsGatherNoDuplicate`;
  `TestInfra_MetricsRequiresScope` (401 without a credential, 403 without `core:metrics.read`, `/livez` and `/readyz` Public);
  `TestKernelFamiliesGathered`'s cell for `<ns>_build_info`.
- `TestLegacyArchiveRestoreThenMigrate` on the full step list, through the **re-pointed** books restore stager; the books restore drill tests.
- A plain restore through `StageRestoreTask` without an owner proof is refused; a key-started run gets the non-interactive checker (`token` actor).
- `/readyz` 503 before startup and after shutdown begins; the backup restore shows the shutdown backdrop (hub A frame if this lands first,
  `closed{shutdown}` otherwise).
- `TestCoreRoutesReplaceGin` (F4-08's) extended with the F8 cell: no gin route at a backup, health or `/metrics` path.
- The reach oracle: `go test ./internal/books/cutover/ -run 'TestReach'`.

## Acceptance

```bash
git grep -n -I -e '/api/health' -e '/api/v1/health' -- . ':!docs/specs' ':!docs/plans' ':!docs/archive' ':!docs/agent-tasks' ':!docs/diagnostics' ':!CHANGELOG.md' ':!TODO.md' ':!changelog.d' ':!.claude/notes' ':!internal/books/rbacreach' ':!scripts/r2corpus' ':!scripts/tests/test_record_r0_reach.py' ':!scripts/tests/test_assemble_r2_notes.py' | wc -l   # 0
grep -rn 'InterimExport\|InterimBackends' --include='*.go' core/ internal/ main.go | wc -l   # 0
ls -d internal/backup internal/startup 2>/dev/null                                            # nothing (internal/updater is F8-04b's)
go test -count=1 -run 'TestBasicGate_ExemptSetGolden|TestHealth_UnauthenticatedTokenSetGolden|TestDeployReferencedSeriesServed|TestABOMetricsGatherNoDuplicate|TestLegacyArchiveRestoreThenMigrate|TestDeletedHealthPaths404|TestInfra_MetricsRequiresScope|TestCoreRoutesReplaceGin' ./...
(cd core && go test -count=1 -run 'TestKernelFamiliesGathered|TestInfra_MetricsRequiresScope' ./...)
promtool test rules deploy/prometheus/alert-rules.test.yml
make build-api && (./audiobook-organizer serve & echo $! > /tmp/f8a.pid; sleep 5; curl -s -o /dev/null -w '%{http_code}\n' localhost:8484/health; curl -s -o /dev/null -w '%{http_code}\n' localhost:8484/api/health; curl -s -o /dev/null -w '%{http_code}\n' localhost:8484/livez; kill $(cat /tmp/f8a.pid))   # 200, 404, 200
make web-build && make web-test && make test-e2e
python3 -m pytest -q scripts/tests/test_unit_lifecycle.py
make ci && make -C core ci
```

The required checks of 09 §9.2.1, the R2 cutover suite included, must be green. **Plus the read-only prod-copy restore rehearsal
of the stager ruling**, with its per-family counts in the PR description.

## Owner checks before part a deploys (one bundled question)

(a) any monitor outside the repo that polls `/api/health` or `/api/v1/health`; (b) the prod scrape key holds `core:metrics.read`
(blocker 10); (c) the move window is announced. Note for the PR: F8-03's follow-up 8 says "retire `/health`", but the old brief and
05-health §5H.3.3 keep it (OPS-27, the `Dockerfile` and `deploy-wait-ready.sh` use it); this row keeps `/health`.

## Out of scope

Any metric rename (none at F8, 09 §9.2.7); the updater (F8-04b); the shell (F9-03); the runbook and release note (R2-01); archive
encryption and signed manifests; `internal/sysinfo`'s library and dashboard parts and `RecentOperations` (stay books').

## Rollback

Before R2 ships: revert the PR (atomic). After R2 ships: R2's rollback, `make rollback-restore` (09 §9.2.1).

## Repo rules

**TODO.md is not edited by this row (rule 25).** The `TODO-F8-03-*` closes (`TODO.md:353-355`: boot wiring, the status publisher, the storage close timeout) go through the PR description under a "TODO check-offs / owner fixes" heading, naming each line this PR completes.

Worktree off fresh `origin/main`; headers on every file; moved files keep their guid; conventional commits ending
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check. The PR
description lists every `old ==> new` package path (09 §9.2.10). One PR per rename: the deleted paths' callers change in this PR. Workflow
edits are pushed with `git push`, never the MCP contents API. No release or flip trailer.
