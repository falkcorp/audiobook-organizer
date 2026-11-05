<!-- file: docs/agent-tasks/platform-core/F8-02-updater-move-into-core.md -->
<!-- version: 1.1.2 -->
<!-- guid: dd9d9131-a311-46ff-ac6c-d78aac9f8d12 -->
<!-- last-edited: 2026-11-05 -->

# F8-02 — The R0 updater moved into core/updater and core/updater/github, parameterised by release source, with `updater fetch-release` and `make rollback-restore RELEASE=<pre-freeze tag>`

| Field | Value |
|---|---|
| ID | F8-02 |
| Release | R2, flip label F8 (dark PR) |
| Tier | **T3** (Opus / high): a move of the R0 updater with an offline fetch; the fetch decides which binary a rollback across R2 installs |
| Wave | W33 (alone; file-disjoint). Runs inside an announced **move window** (09 §9.2.10) |
| Depends on | F2-05 (tasks), F7-05 (`notify.Service`), F7-02 (`providerhttp.Client`), F4-04 (`events.Bus`), F2-10 (the `health` registry): the four types 05-updater §5U.3's `Deps` names |
| Spec | 05-updater §5U.3 (Go API), §5U.4 (Check, Apply, "Fetching a named release", rollback), §5U.9 (metrics), §5U.10 (routes), §5U.11 (tests), §5U.12 (migration, "R2 done-check"); 05-storage §5S.3.12 (row `make rollback-restore`); 09 §9.2.1 ("After an R2 hotfix"); 04 §8.1 F8; 06 §6.4.5 (`app.OfflineCommands`) |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** no route is mounted, the
gin `/api/v1/update/*` handlers keep serving HEAD's R0 behaviour, nothing in ABO's `main` passes
`app.Options.Updater` (F8-04 wires it). The one user-visible addition allowed here is the offline command
`updater fetch-release` and the `RELEASE=` branch of `make rollback-restore` (plan §(l) item 9).

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f8-02-updater" -b agent/platform-core-f8-02-updater origin/main
cd "$REPO/.worktrees/pc-f8-02-updater"
git rebase origin/main
```

Announce the move window in the tracking issue one working day ahead, naming the 04 row
`internal/updater -> mediacore/updater` (09 §9.2.10).

## Goal

Move HEAD's `internal/updater/updater.go`, **as R0 hotfixed it**, into `core/updater` with the release source as its
one new parameter (A-6), add the `github` source adapter, the task bodies (`core.updater.check`,
`core.updater.apply`, `core.updater.rollback`), the native routes (unmounted), `Updater.FetchRelease` and its offline
command, and the `RELEASE=` branch of `make rollback-restore`. Exactly two additions to HEAD's pipeline: the
`core.updater.max_download` cap and the `--version-json` self-test (05-updater §5U.4). Only `updater.go` moves;
`scheduler.go` and `register.go` stay in ABO until F8-04 deletes them (05-updater §5U.12).

## Invariants

- **I1. Same behaviour as R0.** R0's updater tests, re-pointed at `core/updater`, pass with unchanged expectations
  (exit 75, `.prev` kept, the semver and local-build rule, check-only scheduled runs); the only fixture change is
  that the fake asset answers `--version-json` (05-updater §5U.12 "R2 done-check").
- **I2. No hard-coded app.** Nothing in `core/` names `falkcorp/audiobook-organizer` or `audiobook-organizer`
  (`TestCoreHoldsNoAppKeyNames` and the archtest stay green); ABO supplies the slug, binary name and asset namer.
- **I3. The fetch installs nothing.** `FetchRelease` writes only `<dest>` through `<dest>.part`, never touches
  `<exe>` or `<exe>.prev`, opens no store, restarts nothing, refuses `latest`, a URL and a path.
- **I4. Dark.** No `/api/core/v1/updater*` route is reachable from ABO; `/api/v1/update/*` answers exactly as before.
- **I5. History.** `updater.go` reaches `core/updater` by `git mv` so rename detection holds for the repo split
  (09 §9.2.10, A-4); `scripts/ci/check_commits_build.py` runs on this PR (a rename into `core/`).

## Files

| Action | Path |
|---|---|
| move (`git mv`) then edit | `internal/updater/updater.go` → `core/updater/updater.go` |
| create | `core/updater/` (`Options`, `Mode`, `Channel`, `Source`, `Release`, `Asset`, `RawBinary`, `Status`, `Updater`, `Deps`, `ReaderCheck`, `Restarter`; the three task definitions; tests) |
| create | `core/updater/github/` (`New`, `Option`, `BaseURL`, `TokenSecret`; tests) |
| create | `core/internal/updaterhttp/` (the four routes of §5U.10, not mounted) |
| create | `core/app/offline_updater.go` (`updater fetch-release --version <tag> --out <path>` through `app.OfflineCommands`) |
| modify | `internal/updater/` (keep `scheduler.go`, `register.go` and the gin-facing type working over the moved code, or keep a thin ABO adapter; R0's tests move to `core/updater` with the fixture change) |
| modify | `Makefile` (`rollback-restore`: the `RELEASE=` branch) |
| modify | `internal/arch/layering_test.go` (`layerOf` for `core/updater*`, `core/internal/updaterhttp`), `scripts/ci/short_test_weights.json` (if a weight names `internal/updater`) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
M=github.com/falkcorp/audiobook-organizer
# 1. HEAD's updater files. Expected: register.go scheduler.go updater.go and their tests [evidence: 5 files]
ls internal/updater/
# 2. Its importers. Expected: internal/server files only [evidence: registry_wire.go, server.go]
grep -rl --include='*.go' "\"$M/internal/updater\"" . | grep -v '^./internal/updater/'
# 3. The hard-coded slug and agent. Expected: lines 36/71 (slug), 115/168/239/301 (user agent), 318-319 (asset)
grep -n 'falkcorp/audiobook-organizer\|"audiobook-organizer/"\|audiobook-organizer-%s-%s' internal/updater/updater.go
# 4. R0's updater tests. Expected: each of the five R0 test names found once
grep -rn -E 'func (TestApply_RestartExits75|TestApply_SwapAtomicAndPrevKept|TestCheck_SemverNeverDowngrades|TestCheck_DescribeSuffixedCurrentNotCompared|TestCheck_ScheduledRunNeverApplies)\(' internal/updater/
# 5. make rollback-restore exists from R1-27 and has no RELEASE branch yet. Expected: target found; RELEASE 0 hits in it
grep -n '^rollback-restore:' Makefile
sed -n '/^rollback-restore:/,/^$/p' Makefile | grep -c 'RELEASE'
# 6. The Deps types exist in core (F7-02, F2-10, F4-04, F7-05). Expected: >=1 hit each
grep -rn 'type Client struct' core/integrations/providerhttp/ ; grep -rn 'type Registry struct' core/health/
grep -rn 'type Bus struct' core/events/ ; grep -rn 'type Service struct' core/integrations/notify/ 2>/dev/null
# 7. Offline command registration point. Expected: app.OfflineCommands and Command found
grep -rn 'func OfflineCommands\|OfflineCommands' core/app/*.go | head -5
```

STOP and report if grep 4 misses any R0 test, if grep 5 shows `rollback-restore` absent (R1-27 owns it), or if grep 6
misses any of the four `Deps` types (each dependency is merged before this wave, so a miss means a dependency changed
its API).

## Steps

1. Write the tests first (below), copying R0's five tests into `core/updater` with the one fixture change.
2. `git mv internal/updater/updater.go core/updater/updater.go`; commit this rename alone first inside the PR
   (`check_commits_build.py` requires each commit to build: make the first commit the rename **plus** the minimal
   package/import edits that make both modules build).
3. Rewrite the moved file to the §5U.3 API: `Options` (AppID, BinaryName, Source, AssetName, DefaultMode),
   semver compare with `golang.org/x/mod/semver`, `LocalBuild`, `.prev` slot, exit through `Restarter` (75),
   `ReaderCheck` on `<exe>` for the swap slot, the writable-directory probe through `os.Root`, the
   `max_download` cap, the `--version-json` self-test (`{app, version, commit, ui_build_id, binary_id}`, 10 s,
   empty env except `PATH`). No archive, checksum, backup, container or exec step (deferred-build, §5U.3).
4. `core/updater/github`: `New(owner, repo, opts...)` over the GitHub releases API with `BaseURL` and `TokenSecret`
   (a `core/secrets` name, never a value); HTTP through `Deps.HTTP` (`providerhttp`).
5. Task definitions on `core/tasks` (05-updater §5U.4): `core.updater.check` (schedule from
   `core.updater.schedules.check`, `When(mode >= ModeCheck)`, writes `c/updater/seen` and stages
   `core.update.found.v1` in the same batch), `core.updater.apply` (no `Schedule`, `ConcurrencyKey: core.updater`,
   `Perms.Start: core:admin`, `Cancel: Cooperative(30s)` until the swap), `core.updater.rollback`. Registered only
   when `app.Options.Updater` is non-nil (`TestPolicy_NilOptionsNoRoutesNoTasks`).
6. `core/internal/updaterhttp`: the four routes of §5U.10, built but mounted only by the kernel when
   `Options.Updater` is set (F8-04 sets it).
7. `FetchRelease(ctx, version, dest)` and `core/app/offline_updater.go`: the `updater fetch-release --version <tag>
   --out <path>` command; compiled-in cap 512 MiB (it reads no store).
8. ABO side: keep `internal/updater`'s scheduler and registration compiling against the moved code (or a thin
   adapter in `internal/updater` that preserves HEAD's gin-facing calls), so `/api/v1/update/*` behaves as before.
9. `Makefile` `rollback-restore`: with `RELEASE=<tag>`, run `<current binary> updater fetch-release --version
   $(RELEASE) --out <prev path>.fetched` on the host, compare its `--print-storage-levels` with the source's levels
   (the checkpoint's `from_vector`, or the archive's levels) and install it as `.prev` only when equal; refuse
   otherwise and keep the existing `.prev`. Without `RELEASE`, when `.prev`'s levels differ, refuse and print the tag
   to pass (from the checkpoint's `from_release` or the archive's producing release). Extend
   `scripts/tests/test_make_rollback.py` (R1-27's test) with the `RELEASE=` cells.
10. Metrics (05-updater §5U.9): `mediacore_updater_checks_total`, `mediacore_updater_update_available`,
   `mediacore_updater_apply_total`, `mediacore_updater_rollbacks_total` and `mediacore_updater_download_bytes_total`,
   with §5U.9's labels and closed `outcome` values, on the core `updater` metrics scope (namespace from
   `app.Options.MetricNamespace`). This adds that scope: add its `testdata/series.golden` under `core/updater/`,
   asserted by `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5).
11. Layering, shard weights, headers, `changelog.d/` fragment; acceptance.

## Code contract (05-updater §5U.3, verbatim excerpts)

```go
type Options struct {
	AppID       string
	BinaryName  string
	Source      Source
	AssetName   func(r Release, goos, goarch string) string // default: RawBinary(BinaryName)
	DefaultMode Mode
}
type Source interface {
	Latest(ctx context.Context, ch Channel) (Release, error)
	Open(ctx context.Context, a Asset) (rc io.ReadCloser, size int64, err error)
}
func RawBinary(binary string) func(Release, string, string) string
func New(opts Options, d Deps) (*Updater, error)
func (u *Updater) Status() Status
func (u *Updater) Check(ctx context.Context) (Status, error)
func (u *Updater) Apply(rc *tasks.RC, version string) error
func (u *Updater) Rollback(rc *tasks.RC) error
func (u *Updater) FetchRelease(ctx context.Context, version, dest string) error
type ReaderCheck func(ctx context.Context, dbPath, exe string) error
type Restarter interface{ Restart(ctx context.Context, reason string) error }

package github
func New(owner, repo string, opts ...Option) updater.Source
func BaseURL(u string) Option
func TokenSecret(name string) Option
```

`Deps` is §5U.3's struct (`HTTP`, `Restarter`, `Health`, `Events`, `Notify`, `Store`, `ReaderCheck`, `Log`); copy it
exactly.

## Tests (write first; spec names copied exactly)

All of 05-updater §5U.11 in `core/updater` and `core/updater/github`, among them: `TestCheck_SemverNeverDowngrades`,
`TestCheck_DevBuildNeverAvailable`, `TestCheck_DescribeSuffixedCurrentNotCompared`,
`TestCheck_ScheduledRunNeverApplies`, `TestGithubSource_ParameterisedSlugAndBaseURL`, `TestRawBinary_MatchesHEAD`,
`TestApply_RawAssetNoBackup`, `TestApply_OversizeAborts`, `TestApply_FailedDownloadDeletesNew`,
`TestApply_SelfTestFailsKeepsOldBinary`, `TestSelfTest_VersionJSONFieldList`, `TestApply_SwapAtomicAndPrevKept`,
`TestApply_RestartExits75`, `TestRollback_ReaderRefusesPrev`, `TestRollback_SwapsPrevBack`,
`TestApply_BinaryDirNotWritableRefusedAndDegraded`, `TestFetchRelease_NamedReleaseOnly`,
`TestFetchRelease_FailureLeavesNothing`, `TestApply_RefusedWhileResetPending`,
`TestApply_RefusedWhileOfflineRestoreInFlight`, `TestCheck_NotifiesOncePerVersionAcrossRestart`,
`TestPolicy_NilOptionsNoRoutesNoTasks`, `TestConfig_ModeIsProtectAdmin`, `TestApply_OnlyPathIsManualTask`,
`TestNoGlobals_Updater`.

`TestUpdaterSeries_Golden` (name coined here): `metricstest.AssertSeries` over the `updater` scope's provider against
`core/updater/testdata/series.golden` (05-metrics §12): the five §5U.9 families with their types and label sets.

In ABO's root module: `TestABOUpdaterSourceMatchesHEAD` (05-updater §5U.12): build the `updater.Options` ABO's
`main` will pass (put the constructor in an ABO package, for example `internal/updater`, so F8-04 only wires it) and
assert HEAD's API path for the repository, HEAD's raw asset name for each GOOS/GOARCH of `.goreleaser.yml`'s build
matrix and HEAD's user agent `audiobook-organizer/<ver>`; run R0's fake-server test through those options with only
the base URL injected.

In `scripts/tests/test_make_rollback.py`: the `RELEASE=` cells (name the new test functions as you like; mark them
coined).

## Acceptance (commands and expected output)

```bash
# Gate tests. Expected: ok, no FAIL
(cd core && go test -count=1 ./updater/... ./internal/updaterhttp/...)
go test -count=1 -run 'TestABOUpdaterSourceMatchesHEAD' ./internal/updater/...
(cd core && go test -count=1 -run 'TestApply_RawAssetNoBackup|TestFetchRelease_NamedReleaseOnly|TestUpdaterSeries_Golden' ./updater/...)

# R0's five tests now live in core. Expected: 5
grep -rh -o -E 'func (TestApply_RestartExits75|TestApply_SwapAtomicAndPrevKept|TestCheck_SemverNeverDowngrades|TestCheck_DescribeSuffixedCurrentNotCompared|TestCheck_ScheduledRunNeverApplies)\(' core/updater/ | sort -u | wc -l

# No app name in core. Expected: 0
grep -rn 'falkcorp/audiobook-organizer\|audiobook-organizer' core/updater core/internal/updaterhttp core/app/offline_updater.go | wc -l

# Dark: the gin update routes are unchanged and no core updater route is mounted in ABO. Expected: ok
go test -count=1 ./internal/server/... -run 'Update'

# The offline command exists. Expected: usage text naming --version and --out
make build-api && ./audiobook-organizer updater fetch-release --help

# Makefile recipe tests. Expected: all passed
python3 -m pytest -q scripts/tests/test_make_rollback.py

# Each commit builds (a rename into core/; the script lands with R1-02). Read its usage, then run it over
# origin/main..HEAD. Expected: exit 0
python3 scripts/ci/check_commits_build.py --help

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Wiring `app.Options.Updater` in `main.go`, mounting the routes, deleting `/api/v1/update/*`, `scheduler.go`,
  `register.go` and the maintenance-window step (all F8-04).
- Archive download, checksums, signed manifests, the boot-attempt counter, automatic rollback, exec restart,
  pre-update backup, container refusal (deferred-build, 05-updater §5U.3).
- Changing the release pipeline.

## Rollback

Revert the PR: `internal/updater/updater.go` returns, `fetch-release` and the `RELEASE=` branch disappear. No stored
data is written (dark; `c/updater/seen` is written only once F8-04 wires the check task).

## Repo rules

- Worktree off fresh `origin/main`; headers bumped on every file; `git mv` keeps the moved file's guid.
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
  never squash; never `--admin`. PR description lists `internal/updater/updater.go ==> core/updater/updater.go`
  (09 §9.2.10). No release or flip trailer (09 §9.2.1).
- Move window announced; feature PRs touching `internal/updater` are held until this merges.

