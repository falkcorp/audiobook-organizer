<!-- file: docs/agent-tasks/platform-core/F8-04b-f8-flip-updater-wired.md -->
<!-- version: 1.0.0 -->
<!-- guid: 121f0c43-87cd-4e5d-9859-ab5dba749968 -->
<!-- last-edited: 2026-11-20 -->

# F8-04b — F8 flip, part b, one PR: the core updater wired (app.Options.Updater, routes, found-notification), /api/v1/update/* deleted, the SPA on /api/core/v1/updater*, internal/updater reduced to the options constructor

| Field | Value |
|---|---|
| ID | F8-04b |
| Release | R2, flip F8, part b (the updater). Part a is `F8-04a-f8-flip-backup-health-metrics.md` |
| Tier | **T4** (Opus / xhigh–max): a flip PR; the updater replaces the running binary |
| Wave | W38b in the plan (after F8-04a, alone in its wave: it owns `main.go`, `internal/server/`, books' registration and the kernel and module wiring files). Runs inside its own announced **move window** |
| Depends on | F8-04a (the shared wiring files and `core.json` are owned in series), F7-09 (**soft**, see below), F8-02, F8-03. The inventory lists F7-09 so the plan keeps the order; the brief tolerates its absence |
| Spec | 09 §9.4 R2 flips table row F8, §9.6 row 19; 05-updater §5U.3, §5U.10, §5U.12, §5U.13; 05-health §5H.10; 06 §6.4.10 (F8 row); 04 §8.1 F8 |

Provenance:The pre-scope notes (`.claude/notes/pc-tools/`) are gitignored coordinator scratch, not in a clone; their content is inlined in this brief. The `F8-04-prescope.md` and R2-01 pre-scope references below name those notes.

**Gate:** plan, then execute autonomously in a worktree with its own PR; one atomic PR (09 §9.2.1). The R2 freeze is on: do not
touch `.github/workflows/prerelease.yml`. This row replaces the updater half of the old `F8-04-f8-flip-backup-health-metrics.md`
(superseded 2026-11-20).

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f8-04b-flip" -b agent/platform-core-f8-04b-flip origin/main
cd "$REPO/.worktrees/pc-f8-04b-flip"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F8-04a is merged. Announce the move window one working day ahead, naming the 04 row below (09 §9.2.10). Owner prerequisites
before this PR merges: the O-5 asset answer is **given** (below), and the rollback helper bundle is reinstalled with `stage_bin` set
(`seams.d/F8-02.md`, "Owner-hands"; #4790; OWNER-05 hands item).

## Rulings and code-contract corrections of 2026-11-20 (they bind this row)

- **AppID is `"abo"`, not `"audiobook-organizer"`** (`internal/updater/adapter.go:59`, `internal/books/options.go:47`). The old brief's
  code contract (`AppID: "audiobook-organizer"`, `AssetName` nil, the default `RawBinary` namer) is wrong; **do not write a second
  literal**: wire the options through the constructor F8-02 built and tested with `TestABOUpdaterSourceMatchesHEAD`.
- **The updater asset name follows `FetchOptions`** (owner ruling **O-5**, 2026-11-20 01:20: goreleaser's raw asset name,
  `internal/updater.FetchOptions()`, `adapter.go:101-113`). The default `RawBinary` namer asks for `audiobook-organizer-<GOOS>-<GOARCH>`,
  an asset no release carries (05-updater §5U.13 item 3: releases `v0.223.0` to `v0.232.1-rc.4` publish
  `audiobook-organizer_Linux_x86_64` and siblings). The core apply task uses the `FetchOptions` namer. The 05-updater text
  (§5U.4 step 2, the release-pipeline paragraph, the asset-contract test row, the two §5U.12 rows, §5U.13 item 3) was amended to O-5 by the
  pre-scope PR #4807; verify it still matches, then list `TODO.md:157` under "TODO check-offs / owner fixes" in the PR description. If the answer has not been recorded in the decisions
  log, STOP and ask.
- **F7-09 is a soft dependency** (pre-scope `F8-04-prescope.md` section 5). `Deps.HTTP` and `Deps.Notify` are both nil-tolerant: no HTTP
  falls back to the source's own client (`core/updater/updater.go:230`; `core/updater/github/github.go:118-136, 141-145`: an https-only
  redirect guard and a 30 s header timeout); no Notify means no found notification (`core/updater/tasks.go:253`). HEAD's
  `internal/updater` sends no notification at all, so an updater wired with nil deps keeps HEAD's behaviour. **If F7-09 has not merged,
  wire with `Deps.HTTP == nil` and `Deps.Notify == nil` (the nil-HTTP/Notify fallback)** and say so in the PR: what is missing is only
  the spec's COR2-3 notification and outbound HTTP through `providerhttp` (`05-updater.md:158,252-254`). If F7-09 has merged, inject
  `Notify`, `HTTP` and the library roots (about three hook lines in a new `core/kernel/updater.go`). Fallback option C (wire it in part a
  with nil deps) is the coordinator's call only if F7-09 slips past about 11-27.
- **Open spec item (Q6): which `integrations.Ref` the updater's `providerhttp.Client` uses is undefined** (`Registry.Client(ref, policy)`,
  `core/integrations/providerhttp/registry.go:192`). Recorded as 05-updater §5U.13 item 7 with a recommendation (a core-owned fixed ref
  `core.updater`, `Disabled` and `LANAllow` read from the `core.updater` section). It must be settled before `Deps.HTTP` is set from
  F7-09's registry; with nil `Deps.HTTP` it does not block this PR.
- **Merge order** (merge-order ruling, shared with the F6-03 and F8-04a briefs): F8-04b follows F8-04a. The version-swap and import Deluge moves
  stay in-process (not this row's business).

## The 16 blockers, part b's share (full table in the F8-04a brief)

1. **The split** was never written down: this brief, the inventory rows and the plan's W38 and W38b rows are slice A7. `#4791` and `#4793`
   are already titled "F8-04b".
3. **F8-02's hand-offs** *(carried; `TODO.md:160`; `seams.d/F8-02.md`, "Left for F8-04")*: `updater.FoundTopicSpec()` into
   `kernel.coreTopics`; a `core/updater` storage owner for `c/updater/seen`; hand the served App an `updater.Options`; register the
   updater as a health probe (`Deps.Health` is unused); delete `Updater.Install` (`Deprecated:`, one call with `//lint:ignore SA1019`) with
   the gin path. The old brief's greps `FoundTopicSpec`, `c/updater` and `Install` found 0.
5. **The updater code contract is wrong** *(carried; owner ruled)*: AppID and asset name, above.
6. **`internal/updater` cannot be deleted whole** *(carried)*. `CoreOptions`, `FetchOptions` and `ReleasedAsset`
   (`adapter.go:93-113`) feed `books.OfflineOptions()` for `updater fetch-release`, and `TestABOUpdaterSourceMatchesHEAD` (one of the old
   brief's own gate tests) lives in `internal/updater/adapter_test.go:94`. **Move them first** (slice 8b-4: out of `internal/updater/adapter.go`
   into `internal/books`, after F3-07, behaviour-neutral), or amend the acceptance: the old `ls -d internal/updater` expecting nothing is
   replaced by "no `scheduler.go`, no `register.go`, no gin handler left; the options constructor lives in `internal/books`".
9. **The reach oracle** (part b's share): the 3 `/api/v1/update/*` gin routes become R0-only. Add them to `reachWantR0Only`
   (`internal/books/cutover/reach_gate_test.go:369`), edit `reach_golden_test.go:161` (update apply), re-pin `reach_baseline_test.go`.
   F8-04a, F6-03 and F4-08 edit the same files; one at a time.
15. **The maintenance-window step 1 may already be gone** *(new)*. The F5-18 brief deletes `scheduler_maintenance_window_op.go`
    (`F5-18-tasks-flip-engine-deleted.md:177`) and no native maintenance-window kind carries the update check (grep `CheckUnattended` and
    `maintenance_window_update_completed`: only the v2 op, `internal/updater`, config and the cutover key list). **Verify at your base**
    (Q8): if F5-18 deleted the op file, drop the old brief's Goal 2 deletion of step 1; what remains is the setting key in
    `internal/config/persistence.go:1349` and `internal/books/cutover/head_legacy_keys.go`, which is F3-07's area (the legacy-key list).

## Goal

1. **Updater.** ABO's `main` sets `app.Options.Updater` from the F8-02 options constructor (`AppID` `"abo"`; source `github.New("falkcorp",
   "audiobook-organizer")`; asset name per `FetchOptions`; `BinaryName: "audiobook-organizer"`); the kernel adds
   `updater.FoundTopicSpec()` to `coreTopics()`, the `core/updater` storage owner for `c/updater/seen`, the health probe, and mounts
   `updaterhttp.Register` (`/api/core/v1/updater*`); delete `internal/updater`'s `scheduler.go` and `register.go`, the `updatescheduler`
   service and the `updater` container Overrides (`internal/server/server.go:237-238`, `:976-977`), `Updater.Install`, and
   `/api/v1/update/*` (`server_lifecycle.go:3117-3119`); the SPA moves from `api.ts:5948-5973` and `SettingsGeneral.tsx` onto
   `/api/core/v1/updater*` (05-updater §5U.10, §5U.12) using the unwired `web/src/services/coreUpdater/` from #4793.
2. **Roots into diagnostics and sysinfo** (the 8b scope of the R2-01 pre-scope, only if F7-09 has merged): hand the library roots to
   `health/sysinfo` for per-root capacity.
3. `updater fetch-release` is already in `core/app/offline_updater.go` (F8-02's seam; verify, do not add it again).

04 keys this PR moves: `F8 move internal/server -> mediacore/updater`.

## Slices (R2-01 pre-scope section 3.2 and `F8-04-prescope.md`)

- **8b-1** `versionInfo` json-tag test (#4791, OPEN, head `9a6d5d6c2`): `core/updater/selftest.go` accessor plus a new
  `main_versioninfo_test.go`. Closes the `TODO.md:164` tag test. Dark.
- **8b-2** SPA updater client (#4793, OPEN, head `330ae22b9`): `web/src/services/coreUpdater/`, unwired; types hand-written because
  `core/openapi/core.json` has no updater path until this PR regenerates it. Disjoint from part a.
- **8b-3** kernel updater adapter: new `core/kernel/updater.go` (options from config, `FoundTopicSpec` into `coreTopics`, the `c/updater/`
  owner, the probe, the `updaterhttp.Register` call), with one-line hooks for F7-09. Branch only; keep it separate from F7-09's
  `core/kernel/{integrations,notify,...}.go`.
- **8b-4** updater options move (blocker 6), after F3-07.
- **8b-5** maintenance-window step 1 and setting removal (blocker 15), fold-time.

## Invariants

- **I5.** R0's updater behaviour holds through the wired core updater: `TestABOUpdaterSourceMatchesHEAD`, `TestApply_RawAssetNoBackup`
  (`core/updater/apply_test.go:126`), R0's tests re-pointed. The asset the apply task asks for is one a release carries.
- **I10.** With `Deps.HTTP` and `Deps.Notify` nil the updater still checks, applies and restarts as HEAD does (the nil fallback is tested).
- **I11.** `/api/v1/update/*` answers the legacy not-found; no first-party caller of it remains.

## Files

| Action | Path |
|---|---|
| modify | wiring this PR owns in W38b: `main.go`, `cmd/root.go`, `internal/books/module.go`, `internal/books/options.go`, `internal/server/server.go`, `internal/server/registry_wire.go`, `core/app/options.go`, `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go` |
| create | `core/kernel/updater.go` |
| modify | `core/updater/` (8b-1 accessor if #4791 has not merged), `core/app/offline_updater.go` (verify only) |
| modify / delete | `internal/updater/` (`scheduler.go`, `register.go` deleted; options constructor moved to `internal/books`), `internal/server/` (the 3 update routes and handlers, tests) |
| regenerate | `core/openapi/core.json`, `core/web/packages/api-client/src/generated/` |
| modify | `web/src/services/api.ts` (`:5948-5973`), `web/src/components/SettingsGeneral.tsx`, `web/src/services/coreUpdater/` |
| modify | the reach oracle: `internal/books/cutover/reach_gate_test.go`, `reach_golden_test.go`, `reach_baseline_test.go` |
| modify | `docs/specs/platform-core/05-updater.md` (O-5 is already applied to §5U.4 step 2, the release-pipeline paragraph, the asset-contract test row, the two §5U.12 rows and §5U.13 item 3 by the pre-scope PR #4807; this PR verifies they still match and only adds item 7's decision if F7-09 settled it) |
| modify | `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json`, `scripts/deadcode-baseline.txt` |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing

```bash
grep -rn 'AppID' internal/updater/adapter.go internal/books/options.go | head           # "abo" (adapter.go:59, options.go:47)
grep -rn 'func FetchOptions' internal/updater/ | wc -l                                  # 1 (adapter.go:101-113)
grep -rn 'func FoundTopicSpec' core/updater/ | wc -l                                    # 1
ls core/updater/ core/updater/github/ core/internal/updaterhttp/                        # present, no production caller
grep -rn 'updaterhttp.Register' --include='*.go' . | grep -v _test | grep -v '^./core/internal/updaterhttp/' | wc -l   # 0
ls internal/server/scheduler_maintenance_window_op.go 2>/dev/null | wc -l               # 0 if F5-18 deleted it (blocker 15)
grep -rn 'CheckUnattended\|maintenance_window_update_completed' --include='*.go' . | grep -v _test   # what remains of step 1
grep -rn 'func TestABOUpdaterSourceMatchesHEAD' --include='*.go' .                      # internal/updater/adapter_test.go:94 (move it with the constructor)
git grep -n -e '/api/v1/update' -- . ':!docs' ':!CHANGELOG.md' ':!TODO.md' ':!changelog.d' ':!.claude/notes' ':!internal/books/rbacreach' ':!scripts/r2corpus' ':!scripts/tests/test_record_r0_reach.py'   # the caller list; 0 after
grep -rn 'func (r \*Registry) Client' core/integrations/providerhttp/registry.go          # :192 (the ref the updater uses is open item 7)
```

## STOP conditions

1. F8-04a is not merged, or the O-5 answer is not recorded, or the 05-updater O-5 text (see the Files table) no longer matches `FetchOptions`.
2. The apply task asks for an asset name no release carries (I5): compare against a real release's asset list, never against HEAD's name.
3. The kernel sets `Deps.HTTP` before 05-updater §5U.13 item 7 has a decided `integrations.Ref`.
4. A caller of `/api/v1/update/*` remains in `scripts/`, `.github/`, `deploy/`, `web/` or skills.
5. `internal/updater` is deleted whole (blocker 6): `CoreOptions`, `FetchOptions` and `ReleasedAsset` must move first.

## Steps

1. Re-verify; record the F7-09 state (merged or the nil fallback) and the F5-18 state (blocker 15) in the PR.
2. Tests first (below); then slice 8b-4 (move the constructor and its test into `internal/books`), then wire, delete, switch the SPA.
3. Verify the 05-updater O-5 text and list `TODO.md:157` for check-off in the PR description; leave §5U.13 item 7 open unless F7-09's B3 settled it.
4. `go build ./... && (cd core && go build ./...)`, acceptance, headers, fragment.

## Tests

`TestABOUpdaterSourceMatchesHEAD` (moved with the constructor, asserts `AppID` `"abo"` and the `FetchOptions` asset names per GOOS/GOARCH),
`TestApply_RawAssetNoBackup` through the wired options, a nil-`Deps.HTTP`/`Deps.Notify` apply test (I10), `TestLegacyUpdateRoutes404`
(name coined here), the `FoundTopicSpec` cell in the core topic catalog test, the reach oracle (`go test ./internal/books/cutover/ -run
'TestReach'`), `TestCoreRoutesReplaceGin` (F4-08's) extended with the update paths, vitest for `coreUpdater` and `SettingsGeneral`.

## Acceptance

```bash
go test -count=1 -run 'TestABOUpdaterSourceMatchesHEAD|TestLegacyUpdateRoutes404|TestCoreRoutesReplaceGin' ./...
(cd core && go test -count=1 ./updater/... ./internal/updaterhttp/...)
git grep -n -e '/api/v1/update' -- . ':!docs' ':!CHANGELOG.md' ':!TODO.md' ':!changelog.d' ':!.claude/notes' ':!internal/books/rbacreach' ':!scripts/r2corpus' ':!scripts/tests/test_record_r0_reach.py' | wc -l   # 0
ls internal/updater/scheduler.go internal/updater/register.go 2>/dev/null | wc -l          # 0
make web-build && make web-test && make test-e2e
python3 -m pytest -q scripts/tests/test_unit_lifecycle.py
make ci && make -C core ci
```

## Out of scope

Backup, health, metrics (F8-04a); archive downloads and checksums, signed manifests, the container refusal (deferred-build, 05-updater §5U.3);
the `providerhttp` registry itself (F7-09).

## Rollback

Before R2 ships: revert the PR. After R2 ships: `make rollback-restore` (09 §9.2.1), which needs the helper bundle and `stage_bin`.

## Repo rules

**TODO.md is not edited by this row (rule 25).** Lines `TODO.md:157-165` (the updater items) belong to this row; list each one this PR completes in the PR description under a "TODO check-offs / owner fixes" heading instead of editing the file. (This replaces the earlier steps that said to close `TODO.md:157` in the PR.)

Worktree off fresh `origin/main`; headers on every file; conventional commits ending `Co-Authored-By: Claude <noreply@anthropic.com>`;
`gh pr merge <n> --rebase` alone in its wave, never squash, never `--admin`; the PR description lists the `old ==> new` path (09 §9.2.10);
one PR per rename; no release or flip trailer.
