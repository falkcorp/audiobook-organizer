<!-- file: docs/agent-tasks/platform-core/PC-14-01-t8-floor-release-deletions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 343ba464-257c-498a-868a-4501e9627ef6 -->
<!-- last-edited: 2026-11-04 -->

# PC-14-01 — T8, the floor release: delete OperationDef, opsbridge, childop, opsmetrics and the code only legacy steps 62/65/66 call; declare books' UpgradeFloor

| Field | Value |
|---|---|
| ID | PC-14-01 |
| Tier | **T3 Opus/high** — the one release that bounds skips: a wrong `InstallFirst` tag, or a deletion of code an older store still needs, strands production stores |
| Wave | W14 (release `floor`; one PR) |
| Depends on | OWNER-17 (the `S5r5` soak row) |
| Trailer | `Stage-Release: floor 1` |
| Marker | `release: floor` in `ci/stage.txt`; `shipped_stage:` stays `S5` (`floor` is never a `shipped_stage:` value) |
| Digest | `docs/plans/platform-core/digests/S5.md` §5 table row T8, §6 "T8 adds the floor", §7 "T8" |
| Owning spec | 05-tasks §5T.8.2 row T8, §5T.6.2 "No level step during the extraction; the floor release"; 09 §9.2.2 "Floor release and stepping-stone release"; 09 §9.4.1 T8 row; 02 N14; 05-storage §5S.3.5 upgrade-floor row and its runner row; 05-backup §5B.3.4 |

## Goal

Ship the floor release. Delete, in one PR: `OperationDef`; opsbridge (the `internal/operations/registry` path: its v2
engine files, the v1 status bridge and backfill); ABO's `internal/operations/childop` and `internal/opsmetrics`; v2
`RunItems`; `internal/plugins/runitems_concurrency_lint_test.go`; books' legacy steps 1..`LegacyCap` and every store
method only they call (`MigrateOpsV2ToV3`, `SweepHollowOperationsV2`, `StripLegacyPlanRows` and the methods behind
them); `storage.LegacyMigration`, `storage.Spec.Legacy`, the runner's legacy phase and `TestNoNewLegacyMigrations`.
Declare books' `UpgradeFloor{Version: LegacyCap, InstallFirst: <stepping-stone release>}`. Keep `storage.UpgradeFloor`,
`storage.LegacyGuardProjection` and the version-source adapter.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin --tags
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-14-01" -b feat/pc-14-01-floor-release origin/main
cd "$REPO/../audiobook-organizer-pc-14-01"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Modify (deletions mostly): `internal/operations/registry/` (deleted), `internal/operations/childop/` (deleted),
`internal/opsmetrics/` (deleted), `internal/books/store/` (legacy steps and the methods only they call; the step list),
`internal/books/module.go` (`UpgradeFloor` in books' manifest `Storage`), `core/storage/` (`LegacyMigration`,
`Spec.Legacy`, the legacy phase, `TestNoNewLegacyMigrations` deleted), `internal/plugins/runitems_concurrency_lint_test.go`
(deleted), `ci/stage.txt` (`release: floor`).

Also touched (not in the inventory row; see Planner notes): `internal/books/taskstore/` (bodies of steps 62, 65, 66 live
there since PC-12-05), any remaining importer of the deleted packages, and the root-CI test file for
`TestStepSteppingStoneValid` (location coined here: `internal/books/stepping_stone_test.go`).

## Re-verify greps

At the plan checkout (= `748fd787a` for these paths) the counts are pre-S5; at this wave's base W12/W13 have removed
most users. Run at your base and record both values in the PR description.

```bash
grep -rln 'internal/operations/registry"' --include=*.go . | grep -v '^./internal/operations/registry/' | wc -l   # plan checkout: 403 (with tests); at base: expect only files this PR deletes or rewires — list them
grep -rln 'internal/operations/childop"\|internal/opsmetrics"' --include=*.go . | grep -v _test | grep -v '^./internal/operations/\|^./internal/opsmetrics/' | wc -l   # at base after PC-13-13: expect 0
grep -n "^type OperationDef struct" internal/operations/registry/types.go                     # expect 1
test -f internal/plugins/runitems_concurrency_lint_test.go && echo present                   # expect present
grep -rn "func (.*) MigrateOpsV2ToV3\|func (.*) SweepHollowOperationsV2\|func (.*) StripLegacyPlanRows" --include=*.go internal | grep -v _test | wc -l   # expect 3 (in internal/books/taskstore since PC-12-05)
grep -rn "LegacyMigration\b" core/storage --include=*.go | grep -v _test | wc -l              # expect >= 1 (to delete)
grep -rn "func TestNoNewLegacyMigrations" --include=*.go . | wc -l                           # expect 1
grep -rn "UpgradeFloor" core/storage --include=*.go | grep -v _test | wc -l                   # expect >= 1 (kept)
grep -rn "LegacyGuardProjection" core/storage --include=*.go | grep -v _test | wc -l           # expect >= 1 (kept)
grep -n "^release: S5r5$" ci/stage.txt                                                        # expect 1 (OWNER-17's predecessor)
grep -rn "archtest.RunItemsDeclareParallelism\|func RunItemsDeclareParallelism" core --include=*.go | wc -l   # expect >= 1 (the replacement guard exists)
git tag --list 'v*' --sort=-creatordate | head -3                                            # the newest production tags; the first is the stepping-stone candidate
```

`LegacyCap` is the highest HEAD-style books step at the S2b flip (66 at `748fd787a`; `grep -c "Version: *[0-9]*," internal/database/migrations.go` = 66 there). Use the `LegacyCap` constant, never a literal (09 §9.2.2).

## Invariants

1. **Floor declared, never a marker.** Books' manifest declares `UpgradeFloor{Version: LegacyCap, InstallFirst:
   "<tag>"}`; `ci/stage.txt` gets `release: floor` and keeps `shipped_stage: S5`.
2. **Stepping stone is honest** (09 §9.2.2): `<tag>` is the newest **production** tag that exists when this PR merges;
   it carries every books step up to `LegacyCap` and the code they call, contains no T8 deletion, and prints an empty
   `--print-upgrade-floor`. If a newer production tag appears before merge, update `<tag>` and rerun
   `TestStepSteppingStoneValid`.
3. **Delete only what loses its last consumer.** Every deleted symbol has zero remaining references; the deadcode ratchet
   (`lint-deadcode-ratchet` in `make ci`) does not rise. Keep `storage.UpgradeFloor`, `LegacyGuardProjection`, the
   version-source adapter, `internal/operations/state`, `tools/cmd/opsgen` (and `web/src/generated/ops.ts` byte-identical),
   `internal/activity`'s mirror (it outlives T8).
4. **Refusals.** A store below `LegacyCap` is refused at boot with `ErrUpgradeFloor{InstallFirst}`, exit 78, last stderr
   line `install <release> first`; an archive below the floor is refused at restore staging before extraction; an empty
   store is exempt (a fresh install records books' 1..`LegacyCap` as applied).
5. **No format change.** Rollback is a binary swap to any release at or above the stepping stone.

## Steps

1. Write/extend the tests below first.
2. Delete opsbridge, `OperationDef`, `childop`, `opsmetrics`, v2 `RunItems` and `runitems_concurrency_lint_test.go`
   (its role is `archtest.RunItemsDeclareParallelism`). Fix every remaining importer (there should be none after W13;
   any you find is a W13 leftover: STOP and report instead of converting a definition here).
3. Delete books' legacy step list and bodies (1..`LegacyCap`, including steps 62, 65, 66 in taskstore) and the store
   methods only they call (`MigrateOpsV2ToV3`, `SweepHollowOperationsV2`, `StripLegacyPlanRows`, and the helpers behind
   them). Keep any method that runtime code also calls.
4. In `core/storage`: delete `LegacyMigration`, `Spec.Legacy`, the runner's legacy phase and `TestNoNewLegacyMigrations`.
5. Declare `UpgradeFloor` in books' manifest per invariant 1; confirm `--print-upgrade-floor` prints it.
6. Add `TestStepSteppingStoneValid` (root CI; 05-storage test table): the `InstallFirst` tag exists, predates the T8
   commit, registers books `LegacyCap` and prints an empty `--print-upgrade-floor` (build the tag in a temp directory
   outside the checkout; never `go work init`).
7. `ci/stage.txt`: `release: floor`.
8. Release note and Docker last line (09 §9.2.2): name the stepping stone; tell macOS users to `launchctl bootout` the
   job and Docker users to read the last line of `docker logs` and stop the container. Draft them in the PR description
   for the owner (release notes are prepended to the generated body).
9. `make compat-rehearsal` (required: this PR changes `release:`), `make ci`, `make -C core ci`.

## Code contract

```go
// 05-storage §5S.3.5 (books' manifest Storage)
UpgradeFloor{Version: LegacyCap, InstallFirst: "<stepping-stone tag>"}
// boot refusal
ErrUpgradeFloor{InstallFirst} // exit code 78
```

## Tests (row done-tests; names from the spec)

- "no `registry.` import in ABO": `grep -rln 'internal/operations/registry"' --include=*.go . | wc -l` → 0 (04 §8.3
  registry count 0).
- `TestUpgradeFloor_RefusedBelowLegacyCap` (floor build; a store below books' `LegacyCap` is refused).
- `TestStepSteppingStoneValid` (root CI).
- `TestRestoreBelowUpgradeFloorRefusedAtStaging` (05-backup test table: an archive below the floor is refused before
  extraction; a failed host-loss restore leaves no fresh store).
- Also keep green: `TestFreshInstallPostFloorBootsTwice` (05-storage), the S0 updater test's `stepping_stone_required`
  case, `scripts/test_storage_format_guard.py`'s `--print-upgrade-floor` cases.

## Acceptance

```bash
grep -rln 'internal/operations/registry"' --include=*.go . | wc -l                     # expect 0
test ! -e internal/operations/registry && test ! -e internal/operations/childop && test ! -e internal/opsmetrics && echo ok   # expect ok
test ! -e internal/plugins/runitems_concurrency_lint_test.go && echo ok                  # expect ok
grep -rn "LegacyMigration\b\|TestNoNewLegacyMigrations" --include=*.go core internal | wc -l   # expect 0
grep -n "^release: floor$\|^shipped_stage: S5$" ci/stage.txt | wc -l                     # expect 2
D=$(mktemp -d) && go build -o "$D/abo" . && "$D/abo" --print-upgrade-floor   # prints books' LegacyCap and the stepping-stone tag (not empty)
go test ./... -run 'TestUpgradeFloor_RefusedBelowLegacyCap|TestStepSteppingStoneValid|TestRestoreBelowUpgradeFloorRefusedAtStaging|TestFreshInstallPostFloorBootsTwice' -count=1 2>&1 | grep -c '^ok'   # expect >= 3 packages ok, no FAIL
git diff --exit-code origin/main -- web/src/generated/ops.ts && echo unchanged          # expect unchanged
make compat-rehearsal      # exit 0
make -C core ci            # exit 0
make ci                    # exit 0
```

## Out of scope

- Any task conversion left over from W13 (STOP and report it).
- Native cutover of the ops families (deferred build), level steps, the `Online` data-migration mode.
- Deploying or verifying the stepping stone on production (OWNER-18, P13).

## STOP conditions

- An importer of opsbridge, `childop` or `opsmetrics` remains that is not a pure deletion.
- No production tag satisfies `TestStepSteppingStoneValid` (for example the newest tag already contains a T8 deletion).
- A legacy step body is also called by runtime code and cannot be cleanly separated.

## Rollback

Binary swap to any release at or above the stepping stone (09 §9.2.1 W14 row). Releases older than the stepping stone are
refused by the store guard and the updater (`ErrUpgradeFloor`, `stepping_stone_required`). A revert PR is the exact
inverse diff.

## Repo rules

- File headers on every changed file; `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: floor 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; required checks include
  `compat-rehearsal` (this PR changes `release:`); `gh pr merge <n> --rebase`, never squash, never `--admin`;
  `check_soak.py` needs OWNER-17's current `S5r5` row ≥ 14 days old.
- The owner verifies the stepping-stone choice at merge (digest S5 §8); hand off to OWNER-18 with the release-note text.

## Planner notes

- The row's done-tests omit `TestFreshInstallPostFloorBootsTwice`, which digest S5 §7 and 05-storage list for the floor
  build; this brief keeps it as a must-stay-green test.
- The row lists `internal/books/store/` for the legacy-step deletions, but since PC-12-05 the bodies of steps 62, 65 and 66
  and the methods only they call live in `internal/books/taskstore/`, which the row does not list.
