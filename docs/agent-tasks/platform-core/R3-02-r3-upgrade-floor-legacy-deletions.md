<!-- file: docs/agent-tasks/platform-core/R3-02-r3-upgrade-floor-legacy-deletions.md -->
<!-- version: 1.1.6 -->
<!-- guid: c967232d-f699-4787-8455-5badc8f3fca6 -->
<!-- last-edited: 2026-11-05 -->

# R3-02 — The R3 floor: UpgradeFloor{InstallFirst: <R2 tag>}, books' legacy steps 1..66 and the code only they call, the HEAD-format readers, internal/books/cutover and TestR0OpensR1Store deleted, and together tools/cmd/opsgen, web/src/generated/ops.ts and internal/operations/state

| Field | Value |
|---|---|
| ID | R3-02 |
| Release | R3 |
| Tier | **T4** (Opus / xhigh–max): the upgrade floor refuses stores at boot and at restore staging; a wrong floor either bricks a valid store or lets an unmigrated one boot with its readers deleted |
| Wave | W41 (beside F9-05; file-disjoint) |
| Depends on | OWNER-05 (R2 released and deployed) |
| Spec | 09 §9.2.2 ("The R3 floor", "Fresh installs"), §9.4 R3; 05-storage §5S.3.5 (`UpgradeFloor`, `ErrUpgradeFloor`, runner rules "Atomicity" and "Upgrade floor"), §5S.4 steps 3 and 6, §5S.9.3, §5S.10; 05-backup §5B.3.4 step 4; 05-tasks §5T.8.1 (the `internal/operations/state` row); 07 §7.8 (opsgen, `ops.ts`, `operationPolling`); 03 §3.7 ("one floor, in R3"), §3.8 R3; 06 §6.4.10 R3, §6.5.1 "Releases" R3 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Starts only after OWNER-05 is recorded
(the R2 release cut and deployed). The R2 production release tag is needed in step 2; take it only from OWNER-05's
tracking-issue record (its `decision: kept (R2 release <tag>, cut from <rc tag>)` line), never from `gh release list`
or `main`'s newest RC: a later R2 patch release would otherwise give a different tag from the one R3-04's release note
names. If the record names no production tag, STOP.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-r3-02-floor" -b agent/platform-core-r3-02-floor origin/main
cd "$REPO/.worktrees/pc-r3-02-floor"
git rebase origin/main
```

## Goal

R3 deletes the code that only a pre-R2 store needs and declares books' one upgrade floor, so a store that skipped
R2 is refused before anything reads it with deleted code (09 §9.2.2; 05-storage §5S.3.5):

1. Books' manifest declares
   `UpgradeFloor{Owner: "books", Version: LegacyCap, Requires: <the step refs of M2, M3, M4, M5, M6>, InstallFirst: <R2 tag>}`
   (05-storage §5S.3.5, the one floor; `InstallFirst` holds the R2 release's tag, written into books' declaration when
   R2 is tagged; 09 §9.2.2). That tag is the production release OWNER-05 cut with `release-prod.yml --ref <RC tag>`
   from the RC tag it deployed, so it names the deployed commit (order M4); read it from OWNER-05's tracking-issue
   record, not from `main`'s newest RC.
2. Delete books' legacy steps 1..`LegacyCap` (66 at `748fd787a`) and every store method only they call (including
   `MigrateOpsV2ToV3`), the HEAD-format readers (the read-only decoders books kept for its cutover steps), the
   whole of `internal/books/cutover` (M2–M6, the M7 and M8 step definitions, `m4legacy`), and `TestR0OpensR1Store`
   (09 §9.4 R3; 05-storage §5S.9.3).
3. In core storage delete `storage.LegacyMigration`, `Spec.Legacy`, the runner's legacy phase and
   `TestNoNewLegacyMigrations`; `UpgradeFloor` and books' version-source adapter stay (05-storage §5S.3.5
   "Atomicity", SIMP7-7).
4. In one change, delete `tools/cmd/opsgen`, `web/src/generated/ops.ts` and `internal/operations/state`, after
   moving books' `operationPolling` and its remaining importers onto the generated `TaskRunView` states and dropping
   `ops.ts` from `services/api` (07 §7.8; 05-tasks §5T.8.1; XFILE-2).

R3 runs no migration step and changes no stored data (09 §9.4 R3 "Migrations"); this PR changes code only.

## Invariants (each has a test below)

- **I-floor-1.** A non-empty store whose books applied version is below `LegacyCap`, or on which any of M2–M6 is not
  done, is refused at storage lifecycle step 3 (05-storage §5S.4), **before** the checkpoint and before any write:
  `ErrUpgradeFloor`, exit **78**, last stderr line exactly `install <R2 tag> first` (the R2 release's tag, e.g. the tag
  OWNER-05 cut). The store directory is
  byte-identical afterwards.
- **I-floor-2.** An empty store (fresh install, first boot after a factory reset) is exempt; its fresh-install
  batch records books' 1..`LegacyCap` as applied and every step ID named in `UpgradeFloor.Requires` (M2–M6) as
  done **whether or not the build registers it**, so the second and every later boot pass (IMPL-7).
- **I-floor-3.** Restore staging refuses an archive below the floor before extracting anything, with
  `ErrUpgradeFloor` naming the release to install first, and a failed host-loss restore leaves no fresh store
  (05-backup §5B.3.4 step 4, DATA3-9).
- **I-floor-4.** Under launchd (`XPC_SERVICE_NAME` set) the binary sleeps 60 s before exiting 78; under systemd the
  unit's `RestartPreventExitStatus=78` (from R1) stops restarts; `deploy-wait-ready.sh` reports `ExecMainStatus=78`
  as "install the release the log names first", not as a crash loop (09 §9.2.2, OPS6-5).
- **I-floor-5.** `--print-upgrade-floor` prints the floor without opening anything (05-storage §5S.3.7,
  `TestPrintUpgradeFloor`).
- **I-del.** After the deletions no package imports `internal/books/cutover`, `internal/operations/state` or the
  deleted legacy-step code; `go build ./...` and `(cd core && go build ./...)` pass; nothing outside the deleted set
  changes behaviour.

## Files

| Action | Path |
|---|---|
| modify | `internal/books/` (manifest: the floor; delete the legacy steps, their store methods and HEAD-format readers; the tests below) |
| delete | `internal/books/cutover/` |
| modify | `internal/books/store/` (delete the HEAD-format decoders and the methods only the legacy steps call) |
| delete | `internal/operations/state/` |
| delete | `tools/cmd/opsgen/` |
| delete | `web/src/generated/ops.ts` |
| modify | `core/storage/` (delete `LegacyMigration`, `Spec.Legacy`, the legacy phase, `TestNoNewLegacyMigrations`; ensure the fresh-install batch records `Requires` step IDs) |
| modify | `scripts/deploy-wait-ready.sh` (only if its exit-78 message differs from I-floor-4) |
| modify | `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json` (drop deleted packages) |
| modify | `Makefile` (`generate-ops`, `ops-check` and `ops-check` in `ci:`), `.github/workflows/ci.yml` (the `make ops-check` step and the `web/src/generated/*` classifier case comment), `.github/workflows/extraction-gates.yml` (the R2 cutover-suite and restore-drill jobs) |
| modify | the books web files that import `generated/ops`: `web/src/modules/books/legacy/` (`operationPolling.ts` and its tests, 07 §7.11) and `web/src/modules/books/legacyApi.ts` (the legacy remainder of `services/api.ts`). If F9 slipped to R3 and has not merged before this PR, these are still `web/src/utils/operationPolling.ts` and `web/src/services/api.ts`: edit them there instead and say so in the PR |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
M=github.com/falkcorp/audiobook-organizer
# 1. Books' cutover package exists. Expected: a directory listing (M2..M6 steps, M7/M8 definitions).
ls internal/books/cutover/

# 2. Importers of internal/books/cutover outside internal/books. Expected: 0 (books' module.go/steps registration
#    is inside internal/books). A hit elsewhere: STOP.
grep -rl --include='*.go' "\"$M/internal/books/cutover" . | grep -v '^./internal/books/'

# 3. LegacyCap and the floor machinery. Expected: LegacyCap defined once (value 66 unless R1-23 measured otherwise);
#    UpgradeFloor and ErrUpgradeFloor present in core/storage; no floor declared yet.
grep -rn 'LegacyCap *=' --include='*.go' internal/ core/
grep -rn 'type UpgradeFloor struct\|type ErrUpgradeFloor struct' core/storage/
grep -rn 'UpgradeFloor{' --include='*.go' internal/books/ | grep -v _test.go      # expected: 0 lines

# 4. The legacy track. Expected: storage.LegacyMigration, Spec.Legacy and TestNoNewLegacyMigrations each found.
grep -rn 'type LegacyMigration struct\|Legacy  *\[\]LegacyMigration\|func TestNoNewLegacyMigrations' --include='*.go' core/ internal/

# 5. TestR0OpensR1Store. Expected: exactly 1 definition.
grep -rn 'func TestR0OpensR1Store' --include='*.go' .

# 6. opsgen inputs and consumers. Expected: internal/operations/state imported only by internal/books/cutover
#    (05-tasks §5T.8.1), by tools/cmd/opsgen and by its own tests. [evidence checkout: 47 importing files, before F5-18]
grep -rl --include='*.go' "\"$M/internal/operations/state\"" . | grep -v '^./internal/operations/state/'
grep -rln "generated/ops" web/src/      # expected: ops.ts's importers (services/api, operationPolling and tests)

# 7. Exit-78 plumbing from R1. Expected: >=1 hit each (launchd sleep, unit setting, deploy script branch).
grep -rn 'XPC_SERVICE_NAME' --include='*.go' . core/
grep -n 'RestartPreventExitStatus=78' deploy/*.service
grep -n '78' scripts/deploy-wait-ready.sh

# 8. The fresh-install batch rule for Requires (IMPL-7). Expected: code in core/storage that records the step IDs of
#    every declared UpgradeFloor.Requires as done in the fresh-install batch. 0 hits: you implement it (step 3).
grep -rn 'Requires' core/storage/*.go | grep -i -E 'fresh|install|floor'
```

STOP and report if: grep 2 finds an importer outside `internal/books/`; grep 3 finds `LegacyCap` defined more than
once or `UpgradeFloor`/`ErrUpgradeFloor` absent from core; grep 5 finds 0 or more than 1 definition; grep 6 finds a
Go importer of `internal/operations/state` other than `internal/books/cutover`, `tools/cmd/opsgen` and its own
tests; grep 7's `XPC_SERVICE_NAME` or `RestartPreventExitStatus=78` is absent (those are R1's, not this row's).

## Steps

1. **Write the tests first** (section below) against the R2 corpus fixtures; they fail until step 2.
2. **Declare the floor** in books' manifest (`Manifest.Storage.UpgradeFloor`, 06 §6.4.10 R3):
   `Owner: "books"`, `Version: LegacyCap`, `Requires:` the `StepRef`s of M2, M3, M4, M5 and M6 **as constants**
   (copy them from `internal/books/cutover/stepids.go`, the one place F3-06 reserved M2–M6's IDs, before deleting
   the package, into a small books file such as `internal/books/floor.go`, name coined here, with a comment that the
   steps are deleted and only their IDs remain), `InstallFirst:` the R2 production release tag (the one OWNER-05
   recorded as cut from its deployed RC tag; if the record names no production tag, STOP), written as a string
   literal in the declaration (`InstallFirst: "<R2 tag>"`, not through a constant), because R3-04's acceptance greps
   that literal under `internal/books/` and checks that its release note names the same tag. Keep `LegacyCap` defined
   where it is if books' version adapter still uses it.
3. **Core storage.** Delete `LegacyMigration`, `Spec.Legacy`, the runner's legacy phase and
   `TestNoNewLegacyMigrations` (05-storage §5S.3.5 "Atomicity"). If grep 8 found no implementation of the
   fresh-install rule, add it at §5S.4 step 6: the one fresh-install batch writes a `done` record under
   `c/storage/dm/<id>` for every step ID named in any declared `UpgradeFloor.Requires`, registered or not. Keep
   `UpgradeFloor`, `ErrUpgradeFloor`, the step-3 check and `--print-upgrade-floor`. Update core's own tests that
   used the legacy phase (hello has none; `coretest` fixtures may).
4. **Books deletions.** Delete books' legacy steps 1..`LegacyCap`, then every function `go vet`/`deadcode` shows as
   reachable only from them (the store methods, `MigrateOpsV2ToV3`, HEAD-format readers in `internal/books/store/`).
   Delete `internal/books/cutover/` and its registration in books' module (the step list passed to
   `Manifest.Cutover` / `DataMigrations`), and books' importer calls that only the cutover made. Delete the R2
   cutover-suite tests and the restore drill (`TestCutoverOnR0Corpus`, `TestCutoverPreservesIDsAndReferences`,
   `TestCutoverLeavesOtherFamiliesByteIdentical`, `TestRollbackRestoreBootsPreviousBuild`) and the cutover
   scenarios of `TestBootCrashMatrix_ABO`, because they exercise deleted code; keep `internal/books/testdata/r2corpus/`
   (it is the pre-R2 store fixture of the new tests). Delete `TestR0OpensR1Store`.
5. **opsgen, ops.ts, state, together** (07 §7.8): move `operationPolling` (wherever it lives at your base) and its
   remaining books importers from `ops.ts`'s enums onto the generated `TaskRunView` states (the shell's Activity
   page did the same at F5); drop the `ops.ts` re-export from `services/api`; delete `web/src/generated/ops.ts`,
   `tools/cmd/opsgen` and `internal/operations/state`. Remove `generate-ops` and `ops-check` from `Makefile` (target,
   `.PHONY`, `ci:` prerequisite) and the `make ops-check` step from `.github/workflows/ci.yml`; keep the
   `web/src/generated/*` classifier case only if another generated file still lives there.
6. **CI wiring.** Remove the cutover-suite and restore-drill jobs from `.github/workflows/extraction-gates.yml`;
   keep `TestBootCrashMatrix_ABO`. Leave `ci/prefreeze_release.txt` untouched.
7. **Deploy script.** If `scripts/deploy-wait-ready.sh` already prints the exit-78 message of I-floor-4, leave it;
   otherwise change only that message and its test in `scripts/tests/test_deploy_wait_ready.py`.
8. Layering and shard files: drop the deleted packages.
9. Run the acceptance block. Bump headers. Add a `changelog.d/` fragment.

## Code contract (05-storage §5S.3.5, verbatim)

```go
type UpgradeFloor struct {
	Owner        Owner
	Version      int       // the minimum applied version
	Requires     []StepRef // data migrations that must be done (R3: books' cutover steps, §5S.3.13)
	InstallFirst string    // the tag of the release to install first (the R2 release's tag), named in the refusal
}

type ErrUpgradeFloor struct {
	Owner        Owner
	Have, Floor  int
	Missing      []StepRef
	InstallFirst string
}
```

The refusal's last stderr line is `install ` + `InstallFirst` + ` first`; the process exits 78 (06 §6.6).

## Tests (write first; spec names copied exactly)

| Test | Where | Proves |
|---|---|---|
| `TestPreR2StoreRefusedByR3` | `internal/books/` | R3 refuses an R1 store (the R0-written corpus of `internal/books/testdata/r2corpus/` booted through R1's shape) and an R2 store whose cutover did not finish (a copy with M7 applied and M4 not done), before any write: exit 78, last line `install <R2 tag> first`, store byte-identical (I-floor-1) |
| `TestRestoreBelowUpgradeFloorRefusedAtStaging` | `internal/books/` (the books manifest drives core staging) | an archive below the floor is refused before extraction; a failed host-loss restore leaves no fresh store (I-floor-3) |
| `TestFreshInstallPostFloorBootsTwice` | `internal/books/` | a build with books' floor that registers none of M2–M6 creates a store and boots it a second time without `ErrUpgradeFloor`; the same after a factory reset (I-floor-2) |
| `TestFactoryResetPostFloorBoots` | `internal/books/` | (05-storage §5S.3.15 reset tests; DATA7-7) after a factory reset on a post-floor build, the next boot is a fresh install whose one batch records books' 1..`LegacyCap` and every M2–M6 step ID as done, so it passes the floor at that boot and every later one (I-floor-2) |
| `TestUpgradeFloorRefusesBeforeAnyWrite` | `core/storage/` (if not already present from R1) | version floor and `Requires` refusal, byte-identical store |
| `TestPrintUpgradeFloor` | `core/storage/` or ABO root (if not already present) | `--print-upgrade-floor` prints books' floor without opening anything (I-floor-5) |
| `TestFloorLaunchdSleepsBeforeExit78` (name coined here) | where the exit-78 path lives | with `XPC_SERVICE_NAME` set and an injected clock, the refusal sleeps 60 s before exit 78; without it, no sleep (I-floor-4) |

## Acceptance (commands and expected output)

```bash
M=github.com/falkcorp/audiobook-organizer
# Deleted paths are gone. Expected output: nothing
ls -d internal/books/cutover internal/operations/state tools/cmd/opsgen web/src/generated/ops.ts 2>/dev/null

# No importer left. Expected output: 0
grep -rl --include='*.go' -e "\"$M/internal/books/cutover" -e "\"$M/internal/operations/state\"" . | wc -l
grep -rln 'generated/ops' web/src | wc -l                       # expected: 0

# The legacy track is gone and the floor is declared. Expected: 0, 0, 1
grep -rn 'type LegacyMigration struct' --include='*.go' core/ | wc -l
grep -rn 'func TestNoNewLegacyMigrations\|func TestR0OpensR1Store' --include='*.go' . | wc -l
grep -rn 'UpgradeFloor{' --include='*.go' internal/books/ | grep -v _test.go | wc -l

# Builds. Expected: exit 0
go build ./... && (cd core && go build ./...)

# The floor tests. Expected: ok, no FAIL
go test -count=1 -run 'TestPreR2StoreRefusedByR3|TestRestoreBelowUpgradeFloorRefusedAtStaging|TestFreshInstallPostFloorBootsTwice|TestFactoryResetPostFloorBoots' ./internal/books/...
(cd core && go test -count=1 -run 'TestUpgradeFloorRefusesBeforeAnyWrite|TestPrintUpgradeFloor' ./storage/...)

# The binary prints its floor. Expected: one line naming books, LegacyCap, M2..M6 and the R2 tag
make build-api && ./audiobook-organizer --print-upgrade-floor

# Crash matrix (ABO) still green without the cutover scenarios. Expected: ok
go test -count=1 -run TestBootCrashMatrix_ABO ./...

# Deploy script test. Expected: OK
python3 -m pytest -q scripts/tests/test_deploy_wait_ready.py

# Frontend. Expected: build succeeds, vitest green
make web-build && make web-test

# Full local gate and core gate. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- R3-01's deletions and R3-03's `legacy-file-cleanup` start hook.
- The R3 release note (R3-04).
- Any change to the level guard, the level stamps or M7's targets (R3 raises nothing, 09 §9.2.2).
- `ci/prefreeze_release.txt` and `scripts/ci/fetch_prefreeze_binary.sh` (leave them).

## Rollback

Revert the commit: the floor disappears and the deleted code returns. R3 changes no stored data, so `make rollback`
to R2 needs no restore (09 §9.4 R3 "Rollback"). A store an R3 build refused was never written (I-floor-1).

## Repo rules

- Worktree off fresh `origin/main`; headers bumped on every changed file; new files get a fresh header.
- Conventional commit, for example `feat(storage): declare the R3 upgrade floor and delete the pre-R2 readers`,
  ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`, never squash, never
  `--admin` past a required check; no release or flip trailer (09 §9.2.1).
- Every PR that adds or changes a boot-time durable write extends the crash matrix in the same PR (09 §9.3); this
  PR removes writers (the legacy steps, the cutover) and must leave `TestBootCrashMatrixCoversEveryWriter` green.
- CLAUDE.md concurrency rules apply to any loop you touch over library-scale collections (none expected here).

