<!-- file: docs/agent-tasks/platform-core/R0-06-updater-hotfix-check-only-exit75.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4a021d38-30be-4d6b-91b6-83afbdadd066 -->
<!-- last-edited: 2026-11-05 -->

# R0-06 — Updater hotfix: exit 75, the kept `.prev` slot, semver with local-build detection, refusal of a release that is not newer, both unattended apply paths check-only with health `auto_update_paused` and a Settings banner

| Field | Value |
|---|---|
| ID | R0-06 |
| Release | R0 (09 §9.4 R0 Content (3)) |
| Tier | **T3** (Opus / high): a wrong swap strands self-updating installs; several refusal paths must hold together |
| Wave | W01 |
| Depends on | none |
| Branch | `fix/r0-06-updater-hotfix` |
| Owner text | 05-updater §5U.12 (the R0 behaviour, row `RestartSelf`), §5U.11 (test names), §5U.4 (comparison and local-build rule), §5U.5 (policy rows "Unattended apply", "Downgrades"); 09 §9.1 updater row; 09 "Out-of-scope defects found" item 3 |

**Gate:** plan, then execute autonomously in your own worktree and PR. You have design latitude inside HEAD's
`internal/updater`; the invariants, tests and STOP conditions below are binding.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/r0-06-updater" -b fix/r0-06-updater-hotfix origin/main
cd "$REPO/.worktrees/r0-06-updater"
git rebase origin/main
npm ci --prefix web
```

## Goal: the R0 behaviour (09 §9.4 R0 Content (3); 05-updater §5U.12)

1. **Exit 75.** A restart after an update exits with status **75**, so the committed unit's `Restart=on-failure`
   restarts it (HEAD exits 0, `updater.go:329-334`, which leaves the service stopped; 09 "Out-of-scope defects"
   item 3).
2. **`.prev` kept.** The previous binary is kept as `<exe>.prev`, the slot `make rollback` uses
   (`Makefile:902`); HEAD renames to `<exe>.old` and deletes it after the swap (`updater.go:267-280`).
3. **Semver.** Versions compare with `golang.org/x/mod/semver`; a release that is **not newer** than the current
   version is refused (never offered, never applied). HEAD treats any `latestVersion != currentVersion` as an update
   (`updater.go:153,204`).
4. **Local build.** A current version with a `git describe` suffix (`-<n>-g<hex>` or `-dirty`; `Makefile:9`) counts
   as a local build: the check reports "local build, not compared", offers nothing and sets the local-build flag;
   a **manual apply refuses unless the operator names the version** to apply. `dev` or another unparsable current
   version offers nothing (HEAD's `!= "dev"` rule, kept).
5. **Check-only unattended.** **Both** unattended apply paths become check-only: the scheduled apply
   (`internal/updater/scheduler.go:75-109`, `tick`) and step 1 of the maintenance-window op
   (`internal/server/scheduler_maintenance_window_op.go:72-91` at 748fd787a and HEAD; the apply call is at `:88`). They check and notify; when the operator asked for
   automatic apply (`auto_update.enabled=true`) the status carries the auto-apply-paused flag, health is Degraded
   **`auto_update_paused`**, and Settings shows a banner.
6. **No store check, no self-test, no download cap, no backup** of the new binary: those are R2's additions or
   deferred (05-updater §5U.4 "exactly two additions", §5U.12). From R1 a swap-slot probe is added by R1-25, not here.

"Notify" in R0: HEAD has no notification service (it arrives with core notify in R2). In R0 a scheduled or
maintenance-window check that finds a newer release records it as the updater's last check (so the Settings page
and `GET /api/v1/update/status` show it) and logs it at info with both versions. Do not build a notification
mechanism.

**Where `auto_update_paused` surfaces.** 05-health §5H.3.1 fixes `/health`'s unauthenticated `degraded` list to a
set that does **not** include `auto_update_paused`, and routes every other token, before F8, to a `degraded` field
on the authenticated `GET /api/v1/system/status` (`settings.manage`). So in R0: (a) `GET /api/v1/update/status`
reports the flag, and (b) `GET /api/v1/system/status` gains a `degraded` string list carrying
`auto_update_paused` while `config.AppConfig.AutoUpdate.Enabled` is true. It must **never** appear in `/health`.

## Files

| Action | Path |
|---|---|
| modify | `internal/updater/updater.go` (semver, local build, not-newer refusal, `.prev`, exit 75, named-version apply, test seams) |
| modify | `internal/updater/scheduler.go` (`tick` check-only) |
| create | `internal/updater/hotfix_test.go` |
| create | `internal/updater/testdata/` (release JSON fixtures for the fake server) |
| modify | `internal/server/scheduler_maintenance_window_op.go` (step 1 check-only) |
| modify | `internal/server/update_handlers.go` (status fields; apply with an optional named version and the refusals) |
| modify | `internal/server/handlers/system/handler.go` (`degraded` field on `GET /system/status`) |
| modify | `web/src/components/settings/AutoUpdateSection.tsx` (banner; local-build note; named-version apply) |
| modify | `go.mod`, `go.sum` (`golang.org/x/mod` becomes a direct requirement; `go mod tidy`) |
| create | `changelog.d/<date>_r0_updater_hotfix.md` (no header) |

See Planner notes for `web/src/services/api.ts` and the existing updater tests.

## Re-verify before editing (expected at the base commit; files unchanged since 748fd787a)

```bash
grep -n 'UpdateAvailable: latestVersion != u.currentVersion' internal/updater/updater.go        # expect 1 hit: 153
grep -n 'UpdateAvailable: shortSHA != u.currentVersion' internal/updater/updater.go             # expect 1 hit: 204
grep -n 'oldPath := execPath + ".old"' internal/updater/updater.go                             # expect 1 hit: 252
grep -n 'os.Remove(oldPath)' internal/updater/updater.go                                       # expect 1 hit: 280
grep -n 'os.Exit(0)' internal/updater/updater.go                                               # expect 1 hit: 333
grep -n 's.updater.DownloadAndReplace(info)' internal/updater/scheduler.go                     # expect 1 hit: 102
grep -n 's.updater.DownloadAndReplace(info)' internal/server/scheduler_maintenance_window_op.go # expect 1 hit: 88
grep -n 'go s.updater.RestartSelf()' internal/server/scheduler_maintenance_window_op.go internal/server/update_handlers.go   # expect 2 hits: :92, :63
grep -n 'func (h \*Handler) GetSystemStatus' internal/server/handlers/system/handler.go         # expect 1 hit: 278
grep -n 'golang.org/x/mod v' go.mod                                                            # expect "golang.org/x/mod v0.42.0 // indirect"
grep -n '"/update/apply"' internal/server/server_lifecycle.go                                  # expect 1 hit: 2104 (credential-guarded; unchanged here)
grep -n 'export interface UpdateInfo' web/src/services/api.ts                                  # expect 1 hit: 5867
```

## Design latitude and the fixed points

You choose the internal shape (helper names, how the fake release server is injected). Fixed points:

- **Comparison** (05-updater §5U.4): normalise both versions to a leading `v`; a current version that
  `semver.IsValid` rejects, or `dev`, is not versioned → no update. Local build = the current version matches
  `-[0-9]+-g[0-9a-f]+(-dirty)?$` or ends in `-dirty`. `UpdateAvailable` is true only when not local, versioned, and
  `semver.Compare(latest, current) > 0`. The `develop` channel keeps HEAD's behaviour for the check (its apply is
  already refused, `updater.go:288-292`), except that it never reports an update on a local build.
- **Status fields** added to `UpdateInfo` (JSON names coined here): `local_build bool`, `auto_apply_paused bool`,
  `note string` (`"local build, not compared"` on a local build, else empty).
- **Apply** (`POST /api/v1/update/apply`, route and credential guard unchanged): optional JSON body
  `{"version": "<tag>"}` (the field name 05-updater §5U.10 uses for the core route). Refusals, each 409 with a
  message, nothing downloaded: a local build with no `version` ("local build: name the version to apply"); a
  named or latest version that is not newer than a non-local current version ("release is not newer than the
  running version"); no update available and no `version` (HEAD's 400 kept). A named version on a local build is
  fetched by tag (the release-by-tag API of the same repository) and applied without the newer-than check, as
  05-updater §5U.4 step 1 allows.
- **Swap**: write `<exe>.new` (mode 0755, HEAD's), rename `<exe>` → `<exe>.prev` (replacing an older `.prev`),
  rename `<exe>.new` → `<exe>`; if the second rename fails, rename `.prev` back and delete `.new`. Never delete
  `.prev`. Fsync the directory after the renames.
- **Restart**: `RestartSelf` exits with **75**. Inject the exit function so a test can observe the code
  (an unexported field is fine; HEAD's tests already construct `&Updater{...}` with unexported fields).
- **Unattended paths**: `Scheduler.tick` and maintenance-window step 1 call `CheckForUpdate` only; on an update they
  log at info ("update available; apply it from Settings") and return. Neither calls `DownloadAndReplace` or
  `RestartSelf` any more (grep proves it). Step 1 keeps writing `maintenance_window_update_completed` as today.
- **Health**: `auto_update_paused` only on authenticated responses (see Goal), never on `/health`.
- **Banner** (`AutoUpdateSection.tsx`): when automatic update is enabled, an MUI `Alert` (severity `info`) reading
  09 §9.4 R0's release-note sentence: "Automatic updates now only check for a new release and notify you; apply
  updates from Settings." When the status says `local_build`, show "Local build, not compared with releases." and a
  version field the apply button sends.

## Tests to write first (spec names copied exactly; 09 §9.4 R0 Verification, 05-updater §5U.11)

All in `internal/updater/hotfix_test.go`, against an `httptest` server serving `testdata/` release JSON and a fake
asset, with the executable path pointed at a temp directory. Each must fail on the base.

| Test | Proves |
|---|---|
| `TestApply_RestartExits75` | `RestartSelf` calls the exit function with 75 (never 0). |
| `TestApply_SwapAtomicAndPrevKept` | After apply, `<exe>` holds the new bytes, `<exe>.prev` holds the old bytes, no `<exe>.new` or `<exe>.old` remains; a pre-existing `.prev` is replaced; a failure of the second rename leaves the old binary at `<exe>`. |
| `TestCheck_SemverNeverDowngrades` | current `v0.223.0`, latest `v0.222.9` or `v0.223.0` → no update; latest `v0.224.0` → update; `0.223.0` without `v` compares the same; current `dev` → no update; apply of a not-newer named version is refused. |
| `TestCheck_DescribeSuffixedCurrentNotCompared` | current `v0.223.0-5-gabc1234` or `v0.223.0-dirty`, latest `v0.223.0` or older (and also newer) → no update offered, `LocalBuild` set, note "local build, not compared"; apply with no version refused; apply of a named version allowed. |
| `TestCheck_ScheduledRunNeverApplies` | A scheduler `tick` with `Enabled: true`, inside the window, and a newer release: the check runs and records `LastCheck`; the fake server sees no asset download; no swap; the exit function is never called; the status reports `AutoApplyPaused`. |

Add (names coined here): `TestMaintenanceWindowStep1_CheckOnly` in `internal/server/` (the op's existing tests are
`maintenance_window_handlers_test.go` and `maintenance_window_outcome_test.go`; put it in a new
`internal/server/scheduler_maintenance_window_update_test.go`, see Planner notes),
proving step 1 never applies; and `TestSystemStatus_AutoUpdatePausedAuthenticatedOnly` in
`internal/server/handlers/system/` proving the token is on `GET /system/status`'s `degraded` and not on
`HealthCheck`'s.

## Invariants

1. No code path outside the manual `POST /update/apply` handler calls `DownloadAndReplace` or `RestartSelf`:
   `grep -rn 'DownloadAndReplace(\|RestartSelf()' internal --include=*.go | grep -v _test.go` lists only
   `updater.go` (definitions) and `update_handlers.go`.
2. A release that is not newer is never offered and never applied on a non-local build.
3. A local build is never compared and never applied without a named version.
4. `.prev` survives every successful apply; a failed apply leaves a runnable `<exe>`.
5. The restart exit code is 75.
6. `auto_update_paused` never appears in `/health`'s `degraded` list.

STOP conditions: an edit-target grep returns 0 hits; `go mod tidy` changes any requirement other than moving
`golang.org/x/mod` to the direct block; a test of the release pipeline shows HEAD's asset name
`audiobook-organizer-<GOOS>-<GOARCH>` must change (out of scope; 05-updater open item 3).

## Acceptance

```bash
go mod tidy && git diff --stat go.mod go.sum                      # only x/mod moves to a direct require
go build ./...                                                    # exit 0
go test -race -count=1 ./internal/updater/ -run 'TestApply_RestartExits75|TestApply_SwapAtomicAndPrevKept|TestCheck_SemverNeverDowngrades|TestCheck_DescribeSuffixedCurrentNotCompared|TestCheck_ScheduledRunNeverApplies'   # ok
go test -race -count=1 ./internal/updater/ ./internal/server/ ./internal/server/handlers/system/   # ok
grep -c 'os.Exit(0)' internal/updater/updater.go                  # expect 0
grep -c '".old"' internal/updater/updater.go                      # expect 0
grep -c 'DownloadAndReplace' internal/updater/scheduler.go internal/server/scheduler_maintenance_window_op.go   # expect 0 and 0
cd web && npm run build && npm test -- --run && cd ..             # exit 0
make ci; echo "exit $?"                                           # expect exit 0
```

The e2e suite and every required check of 09 §9.2.1 are green on the PR on an up-to-date branch (`unit-lifecycle`
does not exist yet in R0).

## Out of scope

- The swap-slot probe `restore_pending`/`restore_inflight` (R1-25), the move into `core/updater` (F8-02), the
  self-test, download cap, checksums, signatures, container refusal, binary-layout migration (deferred, 05-updater
  §5U.3).
- Changing the asset name, the repository slug or the user agent (`TestABOUpdaterSourceMatchesHEAD` in F8 pins them).
- Making `/health` carry any new token.

## Rollback

Revert the commit in a new PR; on a host, `make rollback`. R0 writes no new state (09 §9.4 R0 Rollback).

## Repo rules

- File headers bumped on every modified file (Go, TSX); new files get a header; fragments carry none. `go.mod` and
  `go.sum` have no header.
- Changelog fragment with 09 §9.4 R0's text: "Automatic updates now only check for a new release and notify you;
  apply updates from Settings. A locally built binary is not compared with releases."
- Conventional commit, e.g. `fix(updater): exit 75, keep .prev, semver with local builds, check-only unattended runs`,
  ending `Co-Authored-By: Claude <noreply@anthropic.com>`. No release or flip trailer.
- `gh pr merge <n> --rebase` after every required check is green; never squash, never `--admin`.
- Remove the worktree after merge.

## Planner notes

- **Missing file:** `web/src/services/api.ts` (the `UpdateInfo` interface at `:5867` and `applyUpdate()` at `:5896`,
  which must take the optional version) must change for the banner and the named-version apply, and is not in the
  row's file list. No other W01 row touches it.
- **Existing tests:** `internal/updater/updater_test.go` and `updater_coverage_test.go` construct `Updater` with
  unexported fields; if a new field or a changed `UpdateAvailable` rule breaks one, it is edited in this PR. They
  are not in the row's file list.
- **Extra test file:** `internal/server/scheduler_maintenance_window_update_test.go` for the step-1 test (not in the
  row's file list; no other W01 row touches `internal/server/` test files of that op).
- **Token route ahead of R1-24:** this row adds the authenticated `degraded` field on `GET /api/v1/system/status`
  (carrying only `auto_update_paused`), which 05-health §5H.3.1 says R1 adds; R1-24 ("the split of HEAD's
  fixed-token source with the authenticated degraded field") then extends that field instead of creating it. 09
  §9.4 R0 asks for "health is Degraded `auto_update_paused`" while 05-health keeps it off `/health`; this brief
  reconciles the two by using 05-health's authenticated route.
