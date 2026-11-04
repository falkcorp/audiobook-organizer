<!-- file: docs/agent-tasks/platform-core/PC-17-02-updater-moved-raw-asset-pipeline.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0c0c84b7-a147-4b3e-b41f-069271c700cf -->
<!-- last-edited: 2026-11-04 -->

# PC-17-02 — Updater moved into `core/updater` with HEAD's raw-asset pipeline, parameterised by the release source

| Field | Value |
|---|---|
| ID | PC-17-02 |
| Title | Updater moved into core/updater with HEAD's raw-asset pipeline, parameterised by the release source |
| Tier | **T3 — Opus / high.** A move, not a rebuild: S0's hotfixed behaviour must pass its own test unchanged through the moved code, and a wrong slug or asset name silently disables every update. |
| Wave | W17 (stage S8, release `S8`) |
| Depends on | PC-17-01 (OWNER-20, with its recorded reading of `reusable-release.yml`, is upstream of it) |
| Trailer | `Stage-Release: S8 1` |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §3 (row `updater`), §4 (05-updater 5U.3), §5 rules 1–4, §7 S8 |
| Owning spec | 05-updater §5U.3, §5U.4, §5U.6–§5U.12; 09 S8 "Preconditions", "Updater"; 09 S0 "Hotfixes" (the behaviour being moved) |

**Gate.** Merges only after the `S7` soak row exists. Before you start, read the S7 stage record for OWNER-20's
raw-asset reading and quote it in the PR description; if the stage record has no reading, STOP and report (no agent
may read the shared release workflow).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-17-02" -b feat/pc-17-02-updater-move origin/main
cd "$REPO/../abo-pc-17-02"
git worktree list
make web-deps
```

## Goal

Move the S0-hotfixed `internal/updater/updater.go` into `core/updater` (+ `core/updater/github`, the release source),
**keeping HEAD's pipeline**: download HEAD's raw asset `audiobook-organizer-<GOOS>-<GOARCH>` straight to `<exe>.new`,
exit 75 on restart, the one `.prev` slot shared with `make rollback`, semver with local-build detection, the level and
floor checks against the store's sidecars, the in-process restore-slot check through `CheckReader`, and check-only
unattended mode. **Exactly two additions:** the `core.updater.max_download` size cap and the `--version-json`
self-test of the new binary. The repository slug, binary name and user agent come from `app.Options.Updater`, which
ABO's `main.go` fills; nothing in core names ABO. `scheduler.go`, `SchedulerStarterAdapter` and `register.go`'s
`init()` are **deleted, not moved**; the check runs as the task `core.updater.check` on the durable clock.

## Files

Create: `core/updater/` (with `core/updater/github/`), `core/internal/updaterhttp/` (handlers for the native routes of
5U.10; mounting and aliases are PC-17-04's).

Modify:
- `internal/updater/` — `updater.go` moved; `scheduler.go` and `register.go` deleted; the S0 test
  (`internal/updater/hotfix_test.go`, from PC-02-03) re-pointed at `core/updater` with unchanged expectations.
- `main.go` — passes `app.Options.Updater` (slug, binary name, default `RawBinary` namer).
- `internal/server/` — gin's `/api/v1/update/*` handlers call the moved updater until PC-17-04 replaces them with
  aliases; `scheduler_maintenance_window_op.go` keeps no update step; `registry_wire.go` and `server.go` lose the
  `updatescheduler` wiring.

## Re-verify before editing

Counts at `748fd787a` (equal at this plan's HEAD). The S0 hotfix (PC-02-03) changed `updater.go` since; re-run these
at your base and STOP on a zero-hit edit target.

```bash
git grep -n 'repo:           "falkcorp/audiobook-organizer"' -- internal/updater/updater.go   # 1 (:71; the slug this PR parameterises)
git grep -n '"User-Agent", "audiobook-organizer/"' -- internal/updater/updater.go | wc -l      # 4 (:115, :168, :239, :301)
git grep -n 'wantName := fmt.Sprintf("audiobook-organizer-%s-%s"' -- internal/updater/updater.go   # 1 (:319)
ls internal/updater/register.go internal/updater/scheduler.go                                  # both exist (both are deleted)
test -f internal/updater/hotfix_test.go && echo ok                                              # PC-02-03's S0 test; missing = STOP
M=github.com/falkcorp/audiobook-organizer
git grep -l "\"$M/internal/updater\"" -- '*.go' | grep -v _test.go                             # 2: internal/server/registry_wire.go, internal/server/server.go
git grep -n 'protected.GET("/update/status"\|protected.POST("/update/check"\|"/update/apply"' -- internal/server/server_lifecycle.go   # 3 (:2102-2104)
git grep -n 'func CheckReader\|func ReaderCheck' -- core/storage | head -2                      # >= 1 (S2b)
git grep -n 'auto_update' -- internal/config/config.go | head -3                                # the keys aliased to core.updater.*
```

## Steps

1. Read 05-updater in full (§5U.3 Go API and its deferred-build table, §5U.4 lifecycle, §5U.5 policy, §5U.6 errors,
   §5U.8 config, §5U.10 HTTP, §5U.11 tests, §5U.12 migration and its "S8 done-check" paragraph); 09 S8 "Updater".
2. **Tests first:** re-point the S0 test at `core/updater` and make it fail for the right reason (package missing);
   write `TestABOUpdaterSourceMatchesHEAD` in ABO's root module.
3. Move `updater.go` into `core/updater`; the GitHub release source into `core/updater/github`, parameterised by slug
   and base URL (`TestGithubSource_ParameterisedSlugAndBaseURL`). Keep every S0 rule unchanged. Add only the size cap
   (`TestApply_OversizeAborts`) and the self-test (the new binary must print exactly
   `{app, version, commit, ui_build_id, binary_id}` with `app == AppID`). The S0 test's one fixture change: its fake
   asset answers `--version-json` with `app == AppID` and `version ==` the target (05-updater §5U.12; VER11 note).
4. Tasks: `core.updater.check` (`Schedule: tasks.ScheduleFromConfig("core.updater.schedules.check").When(mode >=
   ModeCheck).Live()`), writes `c/updater/seen` `{version, at}` and stages `core.update.found.v1` through
   `notify.Service.Publish` **in the same batch**, so a version notifies once across restarts; `core.updater.apply`
   declares **no** `Schedule` and runs only from the admin route behind the credential guard; `core.updater.rollback`
   keeps the replaced binary as `<exe>.rolled-back`. The check's durable clock lives in books' legacy `ClockStore`.
5. `binary_not_writable`: probe the executable's directory through an `os.Root`; refuse apply and report Degraded
   instead of failing half-way (the committed unit's root-owned binary directory).
6. Nil `*Options` = no updater at all (no service, tasks, routes or component; `TestPolicy_NilOptionsNoRoutesNoTasks`).
7. Config: `auto_update*` → `core.updater.*` and `core.updater.schedules.check` with window aliases;
   `core.updater.mode` is ProtectAdmin.
8. `main.go`: `Updater: &updater.Options{AppID: "abo", BinaryName: "audiobook-organizer", Source:
   github.New(<slug>, ...)}` with the default `RawBinary("audiobook-organizer")` namer — use the exact `AppID` value
   ABO's `app.Options.AppID` already has at your base (`git grep -n 'AppID:' main.go`).
9. Delete `scheduler.go`, `SchedulerStarterAdapter`, `register.go`; delete the maintenance-window update step and the
   `maintenance_window_update_completed` setting use if still present.
10. **Do not build** any deferred-build member: archive download or extraction, `checksums.txt`, the pre-update backup
    (`core.updater.require_backup`, `backup.Runner`), the container refusal, `restart_mode=exec`, signed manifests,
    the boot-attempt counter, `updater.Boot` at P−1, the unit's binary-layout migration, the real-RC
    `--updater-check`, R-test cell (d).

## Code contract (05-updater §5U.3; copy exactly)

```go
type Options struct {
	AppID string; BinaryName string; Source Source
	AssetName func(r Release, goos, goarch string) string // default RawBinary(BinaryName)
	DefaultMode Mode
}
type UpgradeRequirement struct { Release string `json:"release"`; Owners map[string]uint64 `json:"owners,omitempty"` }
type Mode uint8 // ModeOff (default), ModeCheck
type Source interface {
	Latest(ctx context.Context, ch Channel) (Release, error)
	Open(ctx context.Context, a Asset) (rc io.ReadCloser, size int64, err error)
}
func RawBinary(binary string) func(Release, string, string) string // "<binary>-<goos>-<goarch>"
func New(opts Options, d Deps) (*Updater, error)
func (u *Updater) Check(ctx context.Context) (Status, error)
func (u *Updater) Apply(rc *tasks.RC, version string) error   // only from the core.updater.apply task body
func (u *Updater) Rollback(rc *tasks.RC) error                 // only from core.updater.rollback
// Status carries LocalBuild, AutoApplyPaused, SteppingStone, DisabledReason ("off", "dev_build", "binary_not_writable")
```

## Tests to write first (05-updater §5U.11 and 09 S8 Done when)

- The S0 updater test through the moved code, expectations unchanged: exit 75, kept `.prev`, refusal of an older tag,
  "local build, not compared", refusal of a binary whose levels are below the store's and of one whose floor the store
  does not meet, and neither unattended path applies.
- `TestApply_RawAssetNoBackup`, `TestABOUpdaterSourceMatchesHEAD` (ABO root CI: the options ABO's `main` builds give
  HEAD's API path for the repository, HEAD's raw asset name for every GOOS/GOARCH of goreleaser's matrix and HEAD's
  user agent; S0's fake-server test runs through them with only the base URL injected).
- `TestCheck_SemverNeverDowngrades`, `TestCheck_DevBuildNeverAvailable`, `TestCheck_DescribeSuffixedCurrentNotCompared`,
  `TestCheck_ScheduledRunNeverApplies`, `TestGithubSource_ParameterisedSlugAndBaseURL`, `TestRawBinary_MatchesHEAD`,
  `TestApply_OversizeAborts`, `TestApply_FailedDownloadDeletesNew`, `TestApply_SelfTestFailsKeepsOldBinary`,
  `TestSelfTest_VersionJSONFieldList`, `TestApply_ReaderRefusesNewBinary`, `TestApply_SteppingStoneRequired`,
  `TestApply_StoreStateErrorRefuses`, `TestApply_EmptyFloorSkipsSidecar`, `TestApply_SwapAtomicAndPrevKept`,
  `TestApply_RestartExits75`, `TestRollback_ReaderRefusesPrev`, `TestRollback_SwapsPrevBack`,
  `TestApply_BinaryDirNotWritableRefusedAndDegraded`, `TestApply_RefusedWhileResetPending`,
  `TestApply_CheckpointJournalAdmitsCandidate`, `TestCheck_NotifiesOncePerVersionAcrossRestart`,
  `TestPolicy_NilOptionsNoRoutesNoTasks`, `TestConfig_ModeIsProtectAdmin`, `TestApply_OnlyPathIsManualTask`,
  `TestNoGlobals_Updater`.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                       # exit 0
(cd core && go test ./updater/... ./internal/updaterhttp/... -race -count=1)        # all "ok"
go test ./internal/updater/... ./internal/server/... -race -count=1                 # all "ok" (the re-pointed S0 test included)
go test . -run 'TestABOUpdaterSourceMatchesHEAD' -count=1 -v | grep -E '^--- (PASS|FAIL)'   # --- PASS (root package; move the test to the package where ABO builds its Options if that is not main)
test ! -e internal/updater/scheduler.go && test ! -e internal/updater/register.go && echo deleted   # deleted
git grep -n 'falkcorp/audiobook-organizer\|audiobook-organizer-' -- core/updater | grep -v _test.go | wc -l   # 0 (no ABO name in core)
git grep -n 'checksums.txt\|require_backup\|restart_mode' -- core/updater | grep -v _test.go | wc -l   # 0 (deferred-build)
make ci                                                                             # exit 0
make -C core ci                                                                     # exit 0
```

`unit-lifecycle` triggers on this PR (`internal/updater/**`).

## Out of scope

Every deferred-build member listed in step 10; mounting the native routes and the `/api/v1/update/*` aliases
(PC-17-04); the UI's update calls (PC-17-05); any change to the release pipeline.

## Rollback

Binary swap. S8 adds `c/updater/seen` and the `core.updater.check` clock, which no older build reads; `.prev` is
still the one slot `make rollback` uses.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on new or changed files; one headerless `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S8 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- `main.go` is also edited by PC-17-04; the chain orders them (plan §(d)).
- `TestABOUpdaterSourceMatchesHEAD` is "ABO, root CI"; its package depends on where ABO builds `updater.Options`
  (main or `internal/books`). The acceptance line assumes `main`.
