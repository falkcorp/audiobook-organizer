<!-- file: docs/agent-tasks/platform-core/F8-04-f8-flip-backup-health-metrics.md -->
<!-- version: 1.1.5 -->
<!-- guid: 9c425807-35e2-4fe7-9289-5de6f5478ebe -->
<!-- last-edited: 2026-11-05 -->

# F8-04 — F8 flip, one PR: backup in core's layout with its routes, the updater move wired, health wired with /api/health and /api/v1/health deleted and every caller on /health, the kernel /metrics mount with no rename, InterimExport and app.Options.InterimBackends deleted

| Field | Value |
|---|---|
| ID | F8-04 |
| Release | R2, flip F8 (the flip PR) |
| Tier | **T4** (Opus / xhigh–max): every flip PR is T4; this one changes the `/health` callers, the Basic-gate exempt set, the metrics mount and the backup owner |
| Wave | W38 (alone: the only PR of its wave; it owns `main.go`, `internal/server/`, books' registration and the kernel and module wiring files in W38). Runs inside an announced **move window** |
| Depends on | F7-09, F8-01, F8-02, F8-03 |
| Owns gate tests | the dashboard-reference test over the kernel `/metrics` mount (extension of `TestDeployReferencedSeriesServed`); `TestLegacyArchiveRestoreThenMigrate` (F8-01 landed its staging half) |
| Spec | 09 §9.4 R2 flips table row F8, §9.6 rows 3 and 19, §9.2.7 (F8 moves the mount only), §9.2.9 (one PR per rename, every caller); 03 §3.8 F8; 05-backup §5B.10, B1, B7; 05-updater §5U.10, §5U.12; 05-health §5H.3.3, §5H.10 (row F8); 05-metrics §6, §12, §13; 05-http-api §8.1 (F8 row), §12; 06 §6.4.10 (F8 row); 04 §8.1 F8 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **One atomic PR** (09 §9.2.1): it wires
the subsystems, deletes what they replace and switches every first-party caller in the same PR, so the merged `main`
boots and passes e2e. It merges alone in its wave. The R2 freeze is on (since F3-08, which merged before F3-07): do not touch
`.github/workflows/prerelease.yml`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f8-04-flip" -b agent/platform-core-f8-04-flip origin/main
cd "$REPO/.worktrees/pc-f8-04-flip"
git rebase origin/main
```

Announce the move window one working day ahead naming the 04 rows below (09 §9.2.10).

## Goal (09 §9.4 R2 flips table, row F8; 06 §6.4.10 F8 row)

0. **Module members and kernel wiring** (06 §6.4.10 F8 row): add `Registrar.StartupSteps`, `Registrar.Diagnostics`,
   `Registrar.Backup` and `Deps.Backup` to `core/module/registrar.go`, `deps.go` and `manifest.go`; in
   `core/kernel/boot.go` and `kernel.go` call F8-03's start functions in `core/kernel/health.go` (heartbeat, watchdog
   ping, the remaining core components), build backup and the wired updater, and mount the backup, updater, health and
   diagnostics routes; add `updater fetch-release` to the offline commands in `core/app/offline.go` (05-updater §5U.4);
   regenerate `core/openapi/core.json` and the client under `core/web/packages/api-client/src/generated/`. **Service
   registry** (06 §6.11.1 step 4): delete from `internal/books/services.go` the `ServiceDefs` of updater,
   updatescheduler, system, dashboard and metricsstore.
1. **Backup.** The kernel builds core `backup` (F8-01); `Registrar.Backup` and `Deps.Backup` live; the
   `/api/core/v1/backups*` routes mounted; HEAD's `/api/v1/backup/*` gin routes deleted with the SPA switched;
   `internal/backup` moved/deleted per 04 (`F8 move internal/backup -> mediacore/backup`); the organizer's
   auto-backup becomes `Deps.Backup.Ensure(ctx, 6h, "pre:books.organize")` (05-backup §5B.10). The first
   `core.backup.create` writes `mediacore-backup/1`; existing archives list as `legacy-abo/0` (§9.6 row 19).
2. **Updater.** ABO's `main` sets `app.Options.Updater` with `github.New("falkcorp", "audiobook-organizer")`,
   `BinaryName: "audiobook-organizer"` and the default `RawBinary` namer (F8-02 built the options constructor);
   `internal/updater`'s `scheduler.go` and `register.go`, the `updatescheduler` service, the maintenance-window
   step 1 and its setting `maintenance_window_update_completed` deleted; `/api/v1/update/*` deleted; the SPA on
   `/api/core/v1/updater*` (05-updater §5U.10, §5U.12).
3. **Health.** The kernel mounts `/livez`, `/readyz`, `/api/core/v1/health`, `/api/core/v1/system/*` and the
   diagnostics routes; books' `/health` is served from the registry with ABO's shape (OPS-27); `/api/health` and
   `/api/v1/health` deleted and **every** caller switched to `/health` in this PR; `BasicExempt` moves to `/health`
   (`TestBasicGate_ExemptSetGolden` updated, 05-http-api §12); `internal/startup` becomes `health.Startup`
   (04: `F8 move internal/startup -> mediacore/kernel`), books' `searchindex` a books Degraded step; the host parts
   of `internal/sysinfo` replaced by `health/sysinfo` (its library parts stay); `internal/diagnostics`'s zip machinery
   replaced by `health/diagnostics` with books `Contributor` sections; `health/diagnostics` extends the
   registration of `core.diagnostics.export` with the bundle (09 §9.4 R2 F8 row), so `diagnostics.InterimExport`,
   books' backend and `app.Options.InterimBackends` itself are deleted (06 §6.4.10 F8 row).
4. **Metrics.** The kernel `/metrics` mount (`core/kernel/metrics_mount.go`) replaces the gin `/metrics` route,
   with **no metric rename** (09 §9.2.7; 05-metrics §6); the dashboard-reference test runs over the kernel mount.
   The mount requires `core:metrics.read` while `/livez` and `/readyz` stay Public (05-http-api §12,
   `TestInfra_MetricsRequiresScope`). The kernel also emits the `<namespace>_build_info` gauge (label `version`;
   09 §9.10, "from R2"; one of 05-metrics §6's always-on smoke families). It is a new family, not a rename.

04 keys this PR moves: `F8 move internal/backup -> mediacore/backup`; `F8 move internal/server -> mediacore/updater`;
`F8 move internal/sysinfo -> mediacore/health`; `F8 move internal/server -> mediacore/health`;
`F8 move internal/server/handlers -> mediacore/health`; `F8 move internal/startup -> mediacore/kernel`.

## Invariants

- **I1.** After merge, nothing in the repository requests `/api/health` or `/api/v1/health` (code, scripts, CI,
  e2e, skills, docs), and both paths answer 404.
- **I2.** `/health` keeps ABO's shape and its `ready` field; its unauthenticated body carries only the golden token
  set (`TestHealth_UnauthenticatedTokenSetGolden`, R0-06's test, gains its F8 cells here: books' `/health`, `/readyz`
  and `GET /api/core/v1/health`).
- **I3.** The Basic gate exempts `/health` in place of the two deleted paths and nothing else changes in its exempt
  set (`TestBasicGate_ExemptSetGolden`).
- **I4.** `/metrics` serves the same series names as before the PR, plus the new `<namespace>_build_info`; every
  series a shipped dashboard or rule names
  is served (the dashboard-reference test, extension of `TestDeployReferencedSeriesServed`); a plain gather
  returns no error and no duplicate family (`TestABOMetricsGatherNoDuplicate`).
- **I5.** R0's updater behaviour holds through the wired core updater (`TestABOUpdaterSourceMatchesHEAD`,
  `TestApply_RawAssetNoBackup`, R0's tests re-pointed).
- **I6.** `TestLegacyArchiveRestoreThenMigrate`, owned here, passes on the full step list: F8-01's staging half plus
  the next boot migrating the restored `legacy-abo/0` store through every registered cutover step, with the corpus'
  per-family counts.
- **I7.** `unit-lifecycle` passes (this PR changes `main.go`, `internal/backup/**`, `internal/updater/**`).

## Files

| Action | Path |
|---|---|
| modify | wiring this PR owns in W38: `main.go`, `cmd/root.go`, `internal/books/module.go`, `internal/books/services.go`, `core/app/options.go`, `core/app/offline.go`, `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go` |
| regenerate | `core/openapi/core.json`, `core/web/packages/api-client/src/generated/` |
| modify / delete | `internal/server/` (backup, update, health, metrics routes and handlers; Basic exempt set; tests) |
| delete / move | `internal/backup/`, `internal/updater/`, `internal/startup/`, `internal/sysinfo/` (host parts), `internal/diagnostics/` (zip machinery) |
| modify | `internal/organizer/` (auto-backup through `Deps.Backup.Ensure`) |
| create | `core/kernel/metrics_mount.go` |
| modify | `Dockerfile` (verify `HEALTHCHECK` stays on `/health`), `scripts/deploy-wait-ready.sh` (verify `/health`), `deploy/` (dashboards and rules only if a mount label changes; none expected) |
| modify | `web/src/` (backup, updater, health callers; `App.tsx`'s health poll and `App.health.test.tsx`) |
| modify | every other first-party `/api/health` or `/api/v1/health` caller at the plan's base (09 §9.2.9): `internal/activity/writer_test.go`; `web/tests/e2e/book-detail.spec.ts`, `files-history.spec.ts`, `import-paths.spec.ts`, `metadata-provenance.spec.ts`, `utils/setup-modes.spec.ts`, `utils/test-helpers.ts`; `.github/workflows/binary-smoke.yml`; `scripts/TEST-README.md`, `scripts/api_examples.sh`, `scripts/e2e_test.sh`, `scripts/record_demo.js`, `scripts/run_demo_recording.sh`, `scripts/test-api-endpoints.py`; `docs/developer-guide.md`, `docs/system/deploy-and-gpu-ops.md`; `.claude/skills/deploy/SKILL.md` |
| modify | `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json`; `.mockery.yaml` only if a mocked interface moves (then name it in the PR) |
| create | one `changelog.d/` fragment (headerless) |

Re-grep at your base (grep 1): any further caller is this PR's file too. Dated incident records under
`docs/diagnostics/` describe history, not requests, and are left as written.

## Re-verify before editing (expected results at your base)

Evidence counts are from HEAD `ac4368bdb`; regenerate them at your base and record both in the PR.

```bash
# 1. Every caller of the two deleted paths, outside the spec/plan/archive docs.
#    [evidence: web/src/App.tsx:165 and App.health.test.tsx; 7 web/tests/e2e files; .github/workflows/binary-smoke.yml:85;
#     scripts/run_demo_recording.sh, test-api-endpoints.py, api_examples.sh, e2e_test.sh, record_demo.js, TEST-README.md;
#     docs/developer-guide.md, docs/system/deploy-and-gpu-ops.md; internal/server tests and static_nonembed.go;
#     internal/server/middleware/basicauth.go:34 (deleted by F3-07 at your base); internal/activity/writer_test.go;
#     .claude/skills/deploy/SKILL.md]
grep -rn -I -e '/api/health' -e '/api/v1/health' . \
  | grep -v -E '^./(docs/specs|docs/plans|docs/archive|docs/agent-tasks|docs/diagnostics|node_modules|web/node_modules)|CHANGELOG|^./TODO.md|changelog.d/'
# 1b. Module members this PR adds. Expected: 0
grep -rn 'StartupSteps\|func (r \*Registrar) Diagnostics\|func (r \*Registrar) Backup' core/module/ | wc -l
# 1c. F8-03's start functions, not yet called from boot.go (read their names in health.go first and grep boot.go
#     for each). Expected: >= 1 functions, and no call of them in boot.go
grep -rn '^func ' core/kernel/health.go | wc -l
grep -rn 'Heartbeat\|Watchdog' core/kernel/boot.go | wc -l

# 2. The gin routes this PR deletes. [evidence: wire_system_routes.go:59-66 backup; server_lifecycle.go:2102-2104 update;
#    server_lifecycle.go:1746 /metrics; the three /health routes near server_lifecycle.go:1749-1755]
grep -rn '"/backup/\|"/update/\|GET("/metrics"\|"/api/health"\|"/api/v1/health"' --include='*.go' internal/server/ | grep -v _test

# 3. The interim backend to delete. Expected: InterimExport in core/health/diagnostics, InterimBackends in core/app,
#    books' export backend in internal/
grep -rn 'InterimExport\|InterimBackends' --include='*.go' core/ internal/ main.go | grep -v _test

# 4. The dark packages this PR wires. Expected: each present
ls core/backup/ core/updater/ core/updater/github/ core/internal/healthhttp/ core/health/sysinfo/

# 5. The Basic exempt-set golden. Expected: 1 test definition
grep -rn 'func TestBasicGate_ExemptSetGolden' --include='*.go' .

# 6. The dashboard-reference test. Expected: 1 definition (R1-10's)
grep -rn 'func TestDeployReferencedSeriesServed' --include='*.go' .
```

STOP if grep 4 misses a package (an F8 dark row is not merged), grep 5 or 6 finds no definition, or grep 1 lists a
caller in a repository path you cannot change in this PR (report it; do not leave a caller on a deleted path).

## Steps

1. Run the re-verify block; paste the grep 1 list into the PR description as the caller checklist.
2. Write or extend the tests first (section below).
3. Wire backup in the kernel and books (Registrar `Backup`, `Deps.Backup`), mount its routes, delete the gin backup
   routes and `internal/backup`, switch the SPA's backup calls, change the organizer's auto-backup to
   `Ensure(ctx, 6h, "pre:books.organize")`.
4. Wire the updater (`app.Options.Updater` in `main.go` from F8-02's ABO options constructor), mount its routes,
   delete `internal/updater` (scheduler, registration), the maintenance-window step 1 and its setting, and
   `/api/v1/update/*`; switch the SPA (`SettingsGeneral.tsx` at the evidence commit).
5. Wire health: call F8-03's start functions from `boot.go`; kernel probes and routes; books' `/health` from the
   registry; `Registrar.StartupSteps` with books'
   steps (searchindex Degraded); `books.memdb` already pushes (F5); delete `/api/health` and `/api/v1/health`; move
   `BasicExempt` to `/health`; switch every caller of grep 1; re-implement sysinfo host parts on `health/sysinfo`,
   keeping books' dashboard parts; books `Contributor` sections for diagnostics; delete `InterimExport`, books'
   backend and `app.Options.InterimBackends`.
6. Metrics: `core/kernel/metrics_mount.go` mounts `/metrics` from the App's Provider with
   `core:metrics.read`; delete the gin `/metrics` route; keep every series name; extend the dashboard-reference
   test to scrape the kernel mount. Register the kernel's `<namespace>_build_info` gauge (label `version`, value 1)
   and add it to `TestKernelFamiliesGathered`'s family list.
7. Delete the five `ServiceDefs` from `internal/books/services.go`; add `updater fetch-release` to
   `core/app/offline.go`; regenerate `core/openapi/core.json` and the client; update `internal/arch/layering_test.go`
   (deleted and moved packages), shard weights, `.mockery.yaml` if needed.
8. `go build ./... && (cd core && go build ./...)`, then the acceptance block. Bump headers. Fragment.

## Code contract

No new public API beyond F8-01/02/03's. ABO's wiring, per 05-updater §5U.3:

```go
opts.Updater = &updater.Options{
	AppID:      "audiobook-organizer",               // equal to the binary's --version-json "app"
	BinaryName: "audiobook-organizer",
	Source:     github.New("falkcorp", "audiobook-organizer"),
	// AssetName nil: the default RawBinary(BinaryName)
}
```

(use the constructor F8-02 added and tested with `TestABOUpdaterSourceMatchesHEAD`, not a second literal).

## Tests (write first; spec names copied exactly)

- `TestBasicGate_ExemptSetGolden`: golden updated so `/health` replaces `/api/health` and `/api/v1/health`.
- `TestHealth_UnauthenticatedTokenSetGolden` (R0-06's test): its F8 cells added, books' `/health`, `/readyz` and
  `GET /api/core/v1/health`.
- The dashboard-reference test over the kernel `/metrics` mount (extension of `TestDeployReferencedSeriesServed`;
  this row owns the extension), with `promtool test rules`.
- `TestABOMetricsGatherNoDuplicate` (unchanged; must stay green).
- `TestABOUpdaterSourceMatchesHEAD`, `TestApply_RawAssetNoBackup` through the wired options.
- `TestLegacyArchiveRestoreThenMigrate` (owned here): extend F8-01's staging half to the migrated store on the full
  step list.
- `TestInfra_MetricsRequiresScope` (05-http-api §12): on the native role store, `/metrics` without a credential
  answers 401 and with a key lacking `core:metrics.read` answers 403; `/livez` and `/readyz` stay Public.
- `TestKernelFamiliesGathered` (05-metrics §12, a `kernel` test): its cell for `<namespace>_build_info`, which the
  one zero-module `coretest` boot's scrape now carries.
- Extend `TestCoreRoutesReplaceGin` (05-http-api §12; F4-08's) with the F8 cell: no gin route remains at a path F8's
  native routes replace (backup, update, health and `/metrics`).
- `TestDeletedHealthPaths404` (name coined here): `GET /api/health` and `GET /api/v1/health` answer 404 through the
  full router; `GET /health` answers ABO's shape.

## Acceptance (commands and expected output)

```bash
# No caller of the deleted paths. Expected: 0
grep -rn -I -e '/api/health' -e '/api/v1/health' . \
  | grep -v -E '^./(docs/specs|docs/plans|docs/archive|docs/agent-tasks|docs/diagnostics|node_modules|web/node_modules)|CHANGELOG|^./TODO.md|changelog.d/' | wc -l

# Interim backends gone. Expected: 0
grep -rn 'InterimExport\|InterimBackends' --include='*.go' core/ internal/ main.go | wc -l

# Deleted packages gone. Expected: nothing
ls -d internal/backup internal/updater internal/startup 2>/dev/null

# Gate tests. Expected: ok, no FAIL
go test -count=1 -run 'TestBasicGate_ExemptSetGolden|TestHealth_UnauthenticatedTokenSetGolden|TestDeployReferencedSeriesServed|TestABOMetricsGatherNoDuplicate|TestABOUpdaterSourceMatchesHEAD|TestLegacyArchiveRestoreThenMigrate|TestDeletedHealthPaths404|TestInfra_MetricsRequiresScope|TestCoreRoutesReplaceGin' ./...
(cd core && go test -count=1 -run 'TestKernelFamiliesGathered|TestInfra_MetricsRequiresScope' ./...)
promtool test rules deploy/prometheus/alert-rules.test.yml

# The binary answers. Expected: /health 200 with "ready"; /api/health 404; /livez 200
make build-api && (./audiobook-organizer serve & echo $! > /tmp/f8.pid; sleep 5; \
  curl -s -o /dev/null -w '%{http_code}\n' localhost:8484/health; \
  curl -s -o /dev/null -w '%{http_code}\n' localhost:8484/api/health; \
  curl -s -o /dev/null -w '%{http_code}\n' localhost:8484/livez; kill $(cat /tmp/f8.pid))

# Frontend. Expected: build ok, vitest green; e2e green
make web-build && make web-test && make test-e2e

# Unit lifecycle (this PR changes main.go and internal/backup, internal/updater). Expected: both cells pass
python3 -m pytest -q scripts/tests/test_unit_lifecycle.py   # runs fully on the unit-lifecycle CI job

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

The required checks of 09 §9.2.1, the R2 cutover suite included, must be green on the PR.

## Out of scope

- Any metric rename (none at F8, 09 §9.2.7).
- The shell (F9-03), the runbook and release note (R2-01).
- Archive encryption, signed manifests and other deferred-build members.
- `internal/sysinfo`'s library and dashboard parts and `RecentOperations` (stay books').

## Rollback

Before R2 ships: revert the PR (it is atomic). After R2 ships: R2's rollback, `make rollback-restore` (09 §9.2.1).

## Repo rules

- Worktree off fresh `origin/main`; headers bumped on every file; moved files keep their guid.
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
  never squash; never `--admin` past a required check. The PR description lists every `old ==> new` package path
  (09 §9.2.10). No release or flip trailer (09 §9.2.1).
- One PR per rename: the deleted paths' callers change in this PR, never later (09 §9.2.9).
- Workflow edits are pushed with `git push`, never the MCP contents API.

## Planner notes

- **One caller file outside the row, by choice.** At the plan's HEAD a dated incident record,
  `docs/diagnostics/2026-08-24-scan-adds-no-new-books-root-cause.md`, names `/api/health`. The brief treats it as
  history, not a first-party caller (09 §9.2.9), and excludes `docs/diagnostics/` from the caller grep; if the planner
  reads 09 §9.2.9 as covering it, add it to the row.
