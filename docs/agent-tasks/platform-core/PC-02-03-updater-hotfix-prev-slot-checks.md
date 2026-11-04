<!-- file: docs/agent-tasks/platform-core/PC-02-03-updater-hotfix-prev-slot-checks.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0b68ba3b-a535-4ca6-ad2e-5690d166bcfc -->
<!-- last-edited: 2026-11-04 -->

# PC-02-03 — Updater hotfix: exit 75, the `.prev` slot, semver with local-build detection, level and floor checks before the swap, both unattended paths check-only

| Field | Value |
|---|---|
| ID | PC-02-03 |
| Tier | **T3 Opus/high.** A wrong swap strands self-updating installs; five refusal paths (not newer, local build, levels, floor, staged restore) and two check-only paths must hold together, and S8 moves this behaviour unchanged into `core/updater`, so its tests become S8's done-check. |
| Wave | W02 (S0, release `S0`) |
| Depends on | PC-02-02 (shares `internal/server/handlers/system/handler.go`; adds `--version-json`). Uses PC-02-01's `database.CheckReader`. |
| Trailer | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §1 item 1, §5 rule 6 |
| Spec | 09 S0 row "Updater hotfix" and its "Done when" updater clause; 05-updater §5U.4 (Check, Apply steps 1–5; the hotfix is "HEAD's pipeline as 09 S0 hotfixes it"), §5U.12 (row `RestartSelf` lists the hotfix exactly); 09 §9.2.2 "Floor release and stepping-stone release" and "Rollback tooling" (the updater runs the same decision in process); 09 §9.1 updater row |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-02-03" -b platform-core/pc-02-03-updater-hotfix origin/main
cd "$REPO/../audiobook-organizer-pc-02-03"
git rebase origin/main
git worktree list
make web-deps
```

Precondition: PC-02-01 and PC-02-02 are merged (`grep -n 'func CheckReader' internal/database/storage_guard.go` and
`grep -n 'version_json\|"ui_build_id"' main.go` each print at least 1 line). Otherwise STOP.

## Exact files

Create: `internal/updater/hotfix_test.go`, `internal/updater/testdata/` (fake-binary scripts and release JSON).
Modify: `internal/updater/updater.go`, `internal/server/scheduler_maintenance_window_op.go`,
`internal/server/handlers/system/handler.go`, `web/src/components/settings/AutoUpdateSection.tsx`.
Also needed (see Planner notes): `internal/updater/scheduler.go`, `internal/server/update_handlers.go`,
`web/src/services/api.ts`, and one test file in `internal/server` for the maintenance-window step.

## Re-verify greps (expected counts at the planning checkout `51a2c2acd`)

```bash
grep -n 'os.Exit(0)' internal/updater/updater.go                                  # 1 hit (line 333)
grep -n 'oldPath := execPath + ".old"' internal/updater/updater.go                # 1 hit (line 252)
grep -n 'os.Remove(oldPath)' internal/updater/updater.go                          # 1 hit (line 278)
grep -n 'latestVersion != u.currentVersion' internal/updater/updater.go           # 1 hit (line 153)
grep -n 'shortSHA != u.currentVersion' internal/updater/updater.go                # 1 hit (line 204)
grep -n 'DownloadAndReplace\|RestartSelf' internal/updater/scheduler.go           # 2 hits (lines 102, 108)
grep -n 'DownloadAndReplace\|RestartSelf' internal/server/scheduler_maintenance_window_op.go  # 2 hits (lines 88, 92)
grep -n 'DownloadAndReplace\|RestartSelf' internal/server/update_handlers.go      # 2 hits (lines 55, 63)
grep -n 'audiobook-organizer-%s-%s' internal/updater/updater.go                   # 1 hit (HEAD's asset name)
grep -n 'golang.org/x/mod' go.mod                                                 # 1 hit (indirect today)
grep -n 'const DegradedMemdbWarming' internal/server/handlers/system/handler.go   # 1 hit
grep -n 'export async function applyUpdate' web/src/services/api.ts               # 1 hit
```

The spec's anchors (`updater.go:153,214-280,278,331-334`, `scheduler_maintenance_window_op.go:72-91`) are at
`748fd787a`; at `51a2c2acd` they are unchanged (`:153`, `:214-284`, `:278`, `:331-335`, `:72-92`). A miss: STOP.

## The behaviour (09 S0 "Updater hotfix", 05-updater §5U.12)

1. **Restart exits 75** (`RestartSelf`), so the committed unit's `Restart=on-failure` restarts the service. Use an
   unexported `var exitProcess = os.Exit` seam for the test.
2. **`.prev` slot**: the swap renames `<exe>` → `<exe>.prev` and `<exe>.new` → `<exe>`, fsyncs the directory, and
   never deletes `.prev`; a failed second rename restores `<exe>` from `.prev` (05-updater §5U.4 step 4). The same
   slot `make rollback` uses.
3. **Semver**: compare with `golang.org/x/mod/semver` (make it a direct dependency in `go.mod`; run `go mod tidy`)
   and offer an update only when the target is strictly newer. A current version carrying a `git describe` suffix,
   `-<n>-g<hex>` or `-dirty` (regex `-[0-9]+-g[0-9a-f]+(-dirty)?$|-dirty$`), is a **local build**: the check sets
   `LocalBuild` and reports the text `local build, not compared`, offering nothing; a manual apply on a local build
   refuses unless the request names the target version explicitly. `dev` stays "no update" as today. The develop
   channel's SHA comparison keeps HEAD's behaviour.
4. **Store checks before the swap** (05-updater §5U.4 step 3, in this order, after the download into `<exe>.new`):
   `database.CheckReader(ctx, config.SecureStateDir(), config.AppConfig.DatabasePath, exe+".new", database.ReaderCheckOptions{})`.
   It runs `<exe>.new --print-storage-levels` (falling back to `--print-storage-format`) against `.storage-levels`
   and `<exe>.new --print-upgrade-floor` against `.storage-applied`, and stats `restore/pending.json`,
   `restore/*/inflight.json`, `restore/*/inflight-checkpoint.json` directly as the service user, never through
   `sudo` (09 §9.2.2 "Rollback tooling"). Any refusal deletes `<exe>.new`, swaps nothing, and returns an error whose
   reason is the token; `upgrade_floor` is reported as `stepping_stone_required` naming the `release` the new binary
   printed (stored in `UpdateInfo.SteppingStone`). An empty floor passes without reading the sidecar; a non-empty
   floor with no sidecar refuses with "boot any release ≥ S0 once". The updater has **no** override.
5. **Both unattended paths are check-only for the whole extraction**: the scheduler's `tick` never calls
   `DownloadAndReplace` or `RestartSelf`, and the maintenance-window op's step 1 only checks. When an update is
   available they log it at INFO and record it as `LastCheck` (the existing surface); no new notification channel.
6. **Health and Settings**: while `config.AppConfig.AutoUpdate.Enabled` is true, `/health`'s `degraded` list carries
   `auto_update_paused` (new const `DegradedAutoUpdatePaused = "auto_update_paused"` next to
   `DegradedMemdbWarming`). `AutoUpdateSection.tsx` shows an info `Alert` with the release-note text "Automatic
   updates now only check for a new release and notify you; apply updates from Settings. A locally built binary is
   not compared with releases.", shows `local build, not compared` when `local_build` is true, and the
   stepping-stone banner text "install <release> and run it until the store has applied what this release needs"
   when `stepping_stone` is set. The manual apply stays behind the credential guard
   (`s.credRoute(... "/update/apply" ...)`, unchanged) and passes `{"version": "<target>"}` when the operator
   confirms a local-build apply.

## Code contract (names coined here unless quoted from the spec)

```go
// UpdateInfo gains (JSON names coined here; S8's Status.LocalBuild and Status.SteppingStone keep the meaning):
LocalBuild    bool   `json:"local_build,omitempty"`
SteppingStone string `json:"stepping_stone,omitempty"`

// Apply entry point; explicitVersion is "" unless the operator named one.
func (u *Updater) Apply(info *UpdateInfo, explicitVersion string) error
func isLocalBuild(version string) bool
var exitProcess = os.Exit           // test seam
var executablePath = os.Executable  // test seam
// Updater gains an unexported apiBase (default HEAD's GitHub API base) so the test serves releases locally.
```

`DownloadAndReplace` becomes `Apply`'s download-and-swap half or is folded into it; keep HEAD's raw asset name
`audiobook-organizer-<GOOS>-<GOARCH>` and user agent `audiobook-organizer/<version>` unchanged (05-updater §5U.12:
`TestABOUpdaterSourceMatchesHEAD` at S8 checks them).

## TDD tests to write first (`internal/updater/hotfix_test.go`; names coined here)

The spec names this "the S0 updater test (fake release server serving HEAD's asset name to a binary in a temp
directory)". One subtest per clause of 09 S0 "Done when"; the fake new binary is a POSIX `sh` script from
`testdata/` that answers `--print-storage-levels`, `--print-storage-format` and `--print-upgrade-floor` with
values the case chooses (skip on Windows):

- `TestHotfix_RestartExits75` — `RestartSelf` calls `exitProcess(75)`.
- `TestHotfix_KeepsPrev` — after `Apply`, `<exe>.prev` holds the old bytes and `<exe>` the new; no `.old` exists.
- `TestHotfix_RefusesOlderOrEqualTag` — `v1.2.0` current, `v1.1.9` and `v1.2.0` latest → no update offered; `Apply` refuses.
- `TestHotfix_LocalBuildNotCompared` — current `v1.2.0-3-gabc1234` and `v1.2.0-dirty` with latest `v1.2.0` or older:
  check reports `local build, not compared`; `Apply(info, "")` refuses; `Apply(info, "v1.2.1")` proceeds.
- `TestHotfix_RefusesLevelsBelowStore` — store sidecar `books 1`, fake binary prints `books 0` → refused
  (`level_newer`), nothing swapped, `<exe>.new` deleted.
- `TestHotfix_SteppingStoneRequiredNothingSwapped` — fake binary prints a floor `{"release":"vX","owners":{"books":66}}`,
  store `.storage-applied` says `books version 60` → error names `stepping_stone_required` and `vX`; `<exe>` unchanged.
- `TestHotfix_EmptyFloorNoSidecarPasses` and `TestHotfix_NonEmptyFloorNoSidecarRefuses`.
- `TestHotfix_RefusesWhileRestoreStaged` — `restore/pending.json` and `restore/x/inflight.json` each refuse.
- `TestHotfix_SchedulerTickNeverApplies` — an available update in window: no download request reaches the fake
  server's asset URL and `exitProcess` is never called.
- In `internal/server`: `TestMaintenanceWindowStep1CheckOnly` — the op's step 1 with an available update makes no
  asset request and does not restart.

## Steps

1. Re-verify. Write the failing tests and the `testdata/` scripts.
2. `updater.go`: seams, semver and local-build rule in `checkStable`, `Apply` with the order download → `CheckReader`
   → swap to `.prev`, `RestartSelf` → 75.
3. `scheduler.go`: delete the apply branch of `tick` (keep the check and the log). `scheduler_maintenance_window_op.go`:
   replace lines 85–96's apply branch with a log line `Update available: <v>; unattended apply is paused (apply from Settings)`.
4. `update_handlers.go`: `applyUpdate` binds an optional `{"version": string}` body and calls `Apply`; a refusal
   answers 409 with `{"reason": "<token>", "stepping_stone": "<release>"}` (status choice coined here) and never
   restarts; success keeps HEAD's response and `go s.updater.RestartSelf()`.
5. `handler.go`: the `auto_update_paused` token. `api.ts` and `AutoUpdateSection.tsx`: the fields, banner and the
   explicit-version confirm.
6. Headers, `changelog.d/` fragment carrying release-note item 1 of 09 S0 "User-visible changes".

## Acceptance

```bash
go test ./internal/updater/ -run 'TestHotfix_' -count=1 -race          # ok  github.com/falkcorp/audiobook-organizer/internal/updater
go test ./internal/server/ -run 'TestMaintenanceWindowStep1CheckOnly' -count=1   # ok
grep -c 'os.Exit(0)' internal/updater/updater.go                       # 0
grep -c '\.old"' internal/updater/updater.go                           # 0
grep -n 'DownloadAndReplace\|RestartSelf' internal/updater/scheduler.go internal/server/scheduler_maintenance_window_op.go | wc -l   # 0
(cd web && npx vitest run src/components/settings)                     # all tests pass
make ci; echo "exit=$?"                                                # exit=0
```

## Invariants and STOP conditions

- Nothing is swapped unless every check of step 4 passed; a refusal leaves `<exe>` byte-identical and removes `.new`.
- `.prev` is never deleted by the updater.
- No unattended code path reaches the swap.
- STOP if `database.CheckReader` is missing, if HEAD's asset name or user agent would change, or if making the
  unattended paths check-only needs a config migration (it must not: 09 S0 "No config data change").

## Out of scope

The core updater, its task kinds and routes (S8); signed manifests, backups, the boot counter (deferred-build);
`ROLLBACK_IGNORE_RESTORE` (the updater has none).

## Rollback

Revert the PR. A rollback to a pre-S0 binary restores HEAD's updater (exit 0, `.old`, any-difference compare).

## Repo rules

- Worktree only; never `go work init`. Headers on every changed file; one `changelog.d/` fragment.
- Every commit ends with `Stage-Release: S0 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; every commit builds.
- `gh pr merge <n> --rebase` after the 09 §9.2.1 required checks (including `unit-lifecycle`, which this PR triggers
  through `internal/updater/**`) pass; never squash or `--admin`.
- Concurrency (CLAUDE.md): no library-scale loop.

## Planner notes

- The inventory row omits files this behaviour needs: `internal/updater/scheduler.go` (the scheduled apply lives in
  `tick`, lines 101–108), `internal/server/update_handlers.go` (the manual apply and the explicit version),
  `web/src/services/api.ts` (`applyUpdate` body and `UpdateInfo` fields), and an `internal/server` test file for the
  maintenance-window step. None is edited by another W02 row.
- The spec does not name the S0 updater test functions; the names above are coined.
