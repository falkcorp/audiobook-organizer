<!-- file: docs/agent-tasks/platform-core/PC-21-01-hardening-cutover-machinery.md -->
<!-- version: 1.0.0 -->
<!-- guid: 42b2767c-3b58-472c-8a51-649636a13446 -->
<!-- last-edited: 2026-11-04 -->

# PC-21-01 — Hardening-cutover machinery: TestHardeningCutoverRefusedByEveryOlderRelease, the fresh-verified-archive precondition, checkpoint deletion with forced compaction, soak_record's H owner table

| Field | Value |
|---|---|
| ID | `PC-21-01` |
| Tier | T4 Opus/xhigh–max — one-way steps: a gap lets an older build misread a raised store, or lets a cutover run with no way back |
| Wave | W21 — H1–H3, ships in the first H release |
| Depends on | `OWNER-30` (current `S10` soak row) |
| Priority / effort | P1 / XL |
| Spec | 09 §9.13 (hardening cutovers, "A fresh verified archive first", the H rows' done-checks); 09 §9.2.1 "Soaks" (`<levels>`, `<since>`, H pass criteria); 05-storage §5S.3.13 ("What a cutover leaves on disk" items 1–4, the rules table), §5S.3.12, §5S.3.14 (deferred-build members), §5S.3.7 (guard, `CheckReader`); 05-config §1.12.5; 05-authn §11.2 |
| Digest | `docs/plans/platform-core/digests/H1-H3.md` §2, §3, §4, §5 rules 1–8, §7 |
| `Stage-Release:` | `H1 1` (the plan lands H1 first; if the owner schedules another H release first, use that release's `<H> 1`, 09 §9.2.1 "The trailer") |

## Gate

- Every `H…` trailer needs a current predecessor row: `S10`'s (OWNER-30) for the first H release (09 §9.2.1 check (b)).
- This PR registers **no** cutover and raises **no** level. It lands machinery that is inert until an H flip registers
  a cutover, so it can merge before H1's stage table exists. Anything that would register a real cutover, raise a
  level or delete a legacy row: STOP (that is PC-21-02/03/04).

## Goal

Build, once, the parts every hardening cutover shares, so each H flip only declares its own data migration:

1. **The refusal harness** `TestHardeningCutoverRefusedByEveryOlderRelease`: every release tag from S0 to the one before
   the cutover, booted on a store stamped at a cutover's raised levels, refuses at boot **and** in `CheckReader`
   (09 §9.13).
2. **Two preconditions that defer, never fail, H1 and H2** (05-storage §5S.3.13 items 3 and 4): no swap set-aside
   present (`storage.ErrCutoverResidue`, config `core.storage.cutover.sweep_set_asides`), and a fresh verified archive
   of this store in the backup catalogue (`storage.ErrCutoverNoArchive`, config `core.storage.cutover.archive_max_age`).
3. **The done-phase clearing of old copies** for H1 and H2 (items 1 and 2): every checkpoint of this store with its
   record and `.migration-checkpoint`, then a forced compaction of the rewritten key ranges and a WAL rotation.
4. **`soak_record.py`'s fixed H owner table** (09 §9.2.1): `H2` → `books`, `core`; `H3` → `books`; `H2H3` → `books`,
   `core`; `H1` → undecided, refused until its stage table decides (see Planner notes).

## Invariants (each has a test below)

- I1. A cutover step that has an unmet archive or residue precondition is **deferred**: health `storage_step_pending`
  naming the missing archive or the set-aside paths, the error in the log, the store served at its old levels, no
  checkpoint taken for it (05-storage §5S.3.12: a deferred step does not count), every existing checkpoint left in place,
  and the boot's `boot levels=<vector>` line shows the old vector. Never a boot failure.
- I2. "Fresh verified archive" means all of: manifest `InstallID` equals `c/storage/install`; its level stamps equal the
  pre-cutover vector; created within `core.storage.cutover.archive_max_age` before the cutover boot; a passing
  `VerifyOpen` result (`<main>.verify.json`) written **after** the archive was created (05-storage §5S.3.13 item 4).
  `CreatedAt` is read only as a freshness bound. The check reads the catalogue from the backup directory's sidecars; no
  open store or backup service is needed, and `storage` never imports `backup`: the **kernel** supplies the check to the
  step at 06 P4b.
- I3. Set-asides are: `<db parent>/.mediacore-restore/<id>/{previous,failed}/`, `<state_dir>/restore/<id>/previous/` and
  every beside-itself destination a reset recorded (05-storage §5S.3.15). With `sweep_set_asides=true` the cutover boot
  deletes them **first**, then runs; without it the step is deferred until the kernel's sweep after
  `core.storage.swap.keep_previous` removes them.
- I4. Clearing runs only in the cutover's `done` phase, after `raised` and after the rewrite has committed and verified;
  it is idempotent and resumable (a crash mid-clear finishes at the next boot); afterwards no checkpoint of this store,
  no `.migration-checkpoint`, and no SST or WAL segment under the DB directory holds the pre-cutover bytes of the
  rewritten ranges. Nothing is deleted before `raised` (05-storage §5S.3.13 "Phases").
- I5. Every guard-aware build from S0 declares both owners `books` and `core` (09 §9.13), so a store with either owner
  above that build's supported level is refused (`level_newer`) at boot and by `CheckReader` — the one decision for
  open, `make rollback` and the updater (05-storage §5S.3.7).
- I6. The soak expected vector for an H release is the predecessor row's `<levels>` with exactly the table's owners
  strictly above and every other owner unchanged; both `--print-storage-levels` and `<db path>.storage-levels` must
  equal it; `<since>` is the later of the deploy and the first `boot levels=` line showing the raised vector; no PR
  writes levels (`ci/stage.txt` has no `levels:` line).

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>
TASK=pc-21-01-cutover-machinery
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-$TASK" -b "agent/$TASK" origin/main
cd "$REPO/../audiobook-organizer-$TASK"
git rebase origin/main
git worktree list
npm ci --prefix web                     # or `make web-deps`
git rev-parse --short HEAD              # record: the base commit
```

Do not run `go work init` (CLAUDE.md).

## Files

| Action | Path | Purpose |
|---|---|---|
| create | `core/storage/cutover.go` | `ErrCutoverResidue`, `ErrCutoverNoArchive`, the two preconditions' evaluation, the done-phase clearing |
| create | `core/storage/cutover_test.go` | the core tests below |
| modify | `core/storage/guard.go` | deferral reporting (`storage_step_pending`) and the `boot levels=` vector, if the guard owns them at base |
| modify | `core/kernel/` (the P4b boot step file) | supply the archive-catalogue check to storage (I2) |
| modify | the file that declares `core.storage.*` config keys (R5) | `core.storage.cutover.sweep_set_asides`, `core.storage.cutover.archive_max_age` |
| modify | the crash-matrix kit (`TestBootCrashMatrix`, 06 §6.5.1 writer table) | the clearing's deletes are boot-time durable writes |
| create | `internal/books/hardening_refusal_test.go` | the refusal harness (ABO root module) |
| modify | `scripts/soak_record.py`, `scripts/tests/test_soak_record.py` | the fixed H owner table and its tests |
| modify | `scripts/tests/test_check_soak.py` | the four H cases if any is missing (R7) |
| modify | `docs/specs/platform-core/05-storage.md` §5S.3.14 | delete the table entries this PR builds ("the PR that builds a member deletes its entry") |
| create | `changelog.d/<date>_hardening_machinery.md` | fragment |

## Re-verify at your base commit (all absent at HEAD 9b1545c1a; created by the predecessors named)

```bash
# R1 the guard and CheckReader (PC-07-01). Expected: >= 1 each.
grep -rn 'func CheckReader' core/internal/pebble/ | wc -l
grep -rn 'level_newer' core/ --include='*.go' | grep -v _test.go | wc -l
# R2 the data-migration phase machine (PC-12-06: core/storage/datamigrate). Expected: >= 1 hit for each phase name.
for p in folding folded raised deleting done; do echo "$p $(grep -rn "\"$p\"" core/storage/datamigrate --include='*.go' | grep -v _test.go | wc -l)"; done
# R3 the pre-migration checkpoint records (05-storage §5S.3.12) and RequiresSelfServed.
grep -rn '\.migration-checkpoint\|mediacore-checkpoints' core/ --include='*.go' | grep -v _test.go | wc -l   # expected: >= 2
grep -rn 'RequiresSelfServed\|storage_step_pending' core/ --include='*.go' | grep -v _test.go | wc -l       # expected: >= 2
# R4 the backup catalogue and the verify sidecar (PC-17-01; 05-backup §5B.3.5).
grep -rn 'verify.json\|VerifyOpen' core/backup --include='*.go' | grep -v _test.go | wc -l                 # expected: >= 2
# R5 where core.storage.* keys are declared, and that the two new keys do not exist yet.
grep -rn '"core.storage.swap.keep_previous"\|core\.storage\.swap\.keep_previous' core/ --include='*.go' | head -3   # the declaring file
grep -rn 'cutover.sweep_set_asides\|cutover.archive_max_age' core/ --include='*.go' | wc -l                # expected: 0
# R6 the set-aside records of the one swap (05-storage §5S.3.15: Record.From / Record.To).
grep -rn 'From *string\|To *string' core/internal/restoreswap core/internal/restorejournal --include='*.go' 2>/dev/null | head -4   # expected: >= 2
# R7 the soak tooling (PC-01-07) and the H cases.
grep -n 'levels' scripts/soak_record.py | wc -l                                                            # expected: >= 1
grep -n -E 'H2H3|\bH1\b' scripts/ci/check_soak.py | wc -l                                                  # expected: >= 1 (grammar includes H IDs)
grep -n -i -E 'h1 flip|levels:|revert' scripts/tests/test_check_soak.py | wc -l                            # record; see step 6
# R8 the crash-matrix instance.
grep -rn 'func TestBootCrashMatrix(' core/ --include='*_test.go' | wc -l                                   # expected: 1
# R9 no cutover is registered anywhere yet.
grep -rn 'RaisesLevel: *[1-9]' core/ internal/ --include='*.go' | grep -v _test.go | wc -l                 # expected: 0
```

STOP and report if R1, R2, R4, R6 or R8 shows 0, or R9 shows non-zero: the machinery would rest on members the spec
assumes but the base lacks.

## Steps

1. Run the re-verify block; record the base commit and every count in the PR description.
2. **Errors and keys.** In `core/storage/cutover.go` declare `ErrCutoverResidue` and `ErrCutoverNoArchive` (names fixed
   by 05-storage §5S.3.14). Declare the two keys exactly as 05-storage §5S.3.14 states: `core.storage.cutover.sweep_set_asides`
   (bool, `ProtectOperator`, default `false`) and `core.storage.cutover.archive_max_age` (duration, `ProtectOperator`,
   default `24h`).
3. **Preconditions (I1–I3).** Add the evaluation that a cutover step uses before it starts. How a cutover marks itself as
   one of the credential-removing cutovers (H1, H2) is your design: one declaration on the cutover, logged at boot, named
   in the PR description; any new exported identifier is coined and listed in the PR for the spec owner. The archive check
   is a function the kernel hands storage at P4b (I2); the residue check enumerates set-asides from the swap journal's
   recorded destinations (R6). Unmet → deferred exactly as `RequiresSelfServed` defers (R3's mechanism), never fatal.
4. **Clearing (I4).** Add the done-phase function: delete every checkpoint of **this** store (records whose `install_id`
   equals the live store's, under `core.storage.checkpoint_dir` or `<db parent>/.mediacore-checkpoints/`) with its
   `<name>.json` and `.migration-checkpoint`, by idempotent removes with directory fsync; then force a compaction of the
   given key ranges and rotate the WAL so no obsolete SST or WAL segment remains. Extend `TestBootCrashMatrix` and the
   writer table (06 §6.5.1) for these deletes in this PR.
5. **Harness (I5).** In `internal/books/hardening_refusal_test.go` write `TestHardeningCutoverRefusedByEveryOlderRelease`:
   enumerate the production release tags from the `S0` row's landing commit in `ci/soak.txt` up to the newest tag before
   the PR's base; build each (cached by tag, as the prev-build one-format tests in `extraction-gates` are, 09 §9.2.1
   table); for each owner vector in its case table — `{books: +1}` and `{books: +1, core: +1}` now, each H flip adding its
   exact vector — stamp a fixture store at that vector through core's test helpers and assert (a) the old binary's boot
   exits non-zero with the level refusal and writes nothing, and (b) `CheckReader` with the old binary as candidate refuses
   with `level_newer`. Under `-short` (which `make ci` uses) it runs only the newest tag and logs that it did so; the full
   run is a required check of every H flip PR.
6. **Soak tooling (I6).** In `scripts/soak_record.py` add the fixed table `H2: books, core`, `H3: books`,
   `H2H3: books, core`, and `H1` as undecided: `--release H1` refuses with "H1's owner set is not decided (09 §9.13 H1 stage
   table)". Make sure the `<since>` rule for H releases uses the first `boot levels=` line that shows the raised vector;
   add it if PC-01-07 did not. In `scripts/tests/test_check_soak.py` add any of the four H cases of the H1–H3 digest §7 that
   R7 shows missing (an `H1 flip` PR admitted after `S10`'s row; a `levels:` line refused; an exact revert of an `H1 flip`
   admitted only before a production tag contains it); the deferred-boot `<since>` case goes in `test_soak_record.py`.
7. Delete from 05-storage §5S.3.14's first row the members this PR built (`storage.ErrCutoverResidue`,
   `storage.ErrCutoverNoArchive`, the two config keys, the catalogue check); leave the grant members (no consumer here).
8. Tests first (below), then "Acceptance".

## Code contract (names fixed by the spec)

```go
// core/storage — deferred-build members this PR builds (05-storage §5S.3.14)
var ErrCutoverResidue   error // a swap set-aside exists; the step is deferred (05-storage §5S.3.13 item 3)
var ErrCutoverNoArchive error // no fresh verified archive of this store; the step is deferred (item 4)
// config keys (05-storage §5S.3.14): core.storage.cutover.sweep_set_asides  bool      ProtectOperator  default false
//                                    core.storage.cutover.archive_max_age   duration  ProtectOperator  default 24h
// health token: storage_step_pending; boot INFO line: boot levels=<vector> (09 §9.2.1)
```
Soak row (09 §9.2.1): `<release> <landing commit> <binary_id> <commit> <since, UTC> <levels> <evidence SHA-256>`.

## Tests to write first (TDD)

- `TestH1H2_DeferredWithoutFreshVerifiedArchive` (09 §9.13): over a synthetic cutover registered by a test module, each
  of no archive, a stale one, an unverified one (no `verify.json`, or one written before the archive's creation) and
  another install's defers the step with `storage_step_pending` and leaves every checkpoint in place; a fresh verified one
  lets it run.
- `TestH1H2_DeferredWhileSwapSetAsideExists` (09 §9.13): a restore's `previous/` or `failed/` directory, or a reset's
  beside-itself destination, defers the step with `storage_step_pending` naming the paths until swept; with
  `core.storage.cutover.sweep_set_asides=true` the cutover boot deletes them first and runs.
- `TestCutoverClearsOldCopies` (name coined here): a canary planted before a fixture upgrade (so a retained checkpoint
  holds it), a synthetic cutover that rewrites its range, then the done phase; a byte scan of every file under the DB
  directory and `.mediacore-checkpoints/` finds no canary, and no checkpoint or `.migration-checkpoint` of the store
  remains; a crash at every point of the clearing resumes to the same end state.
- `TestHardeningCutoverRefusedByEveryOlderRelease` (09 §9.13) with the two generic vectors.
- `scripts/tests/test_soak_record.py`: H2/H3/H2H3 expected vectors; `--release H1` refused; a deferred boot does not
  start `<since>`; a build/store vector mismatch refuses.

## Acceptance (commands and expected output)

```bash
go test ./core/storage/... -run '^(TestH1H2_DeferredWithoutFreshVerifiedArchive|TestH1H2_DeferredWhileSwapSetAsideExists|TestCutoverClearsOldCopies)$' -count=1 -v 2>&1 | grep -c -- '--- PASS'   # expected: >= 3
go test ./core/... -run '^TestBootCrashMatrix$' -count=1 2>&1 | grep -c '^FAIL'                                # expected: 0
go test ./internal/books/ -run '^TestHardeningCutoverRefusedByEveryOlderRelease$' -count=1 -v 2>&1 | grep -c -- '--- PASS'   # expected: 1 (full run, not -short)
python3 -m pytest scripts/tests/test_soak_record.py scripts/tests/test_check_soak.py -q 2>&1 | tail -1         # expected: "N passed" and no "failed"
grep -rn 'RaisesLevel: *[1-9]' core/ internal/ --include='*.go' | grep -v _test.go | wc -l                      # expected: 0 (still no cutover registered)
grep -c '^levels:' ci/stage.txt                                                                                 # expected: 0
make -C core ci; echo "core ci exit=$?"                                                                         # expected: core ci exit=0
make ci; echo "make ci exit=$?"                                                                                 # expected: make ci exit=0
```

## Out of scope (STOP if you find yourself doing it)

- Registering any cutover, raising any level, deleting any legacy row, building `storage.Grant` and the other grant
  members, `storage.UpgradeFloor.Requires`, backup encryption (each lands with its first consumer, 05-storage §5S.3.14,
  05-backup §5B.3.9).
- H1's owner entry in the soak table (waits for H1's stage table).
- Any edit of `ci/soak.txt`, `ci/stage.txt` levels, or a production store.

## Rollback

Revert PR (no state was written: nothing is registered). After release: binary swap; the machinery is inert without a
registered cutover, so no row and no floor exist.

## Repo rules

- **File headers** bumped or added; fragment without header; the spec edit bumps 05-storage's header and adds a review-
  history line naming this PR.
- **Commits:** conventional (`feat(storage): …`), trailer `Stage-Release: H1 1` (see the table above) on every commit,
  ending with `Co-Authored-By: Claude <noreply@anthropic.com>`.
- **PR:** `extraction-gates`, `core-ci`, `docker-image`, `vet-darwin`, `vet-integration` green on an up-to-date branch;
  `gh pr merge <n> --rebase`; never squash, never `--admin` past a required check.
- **Concurrency (CLAUDE.md):** the clearing walks checkpoint directories, not a library-scale collection; the harness's
  per-tag builds may run with a bounded pool sized to `runtime.NumCPU()`, never unbounded.
- Destructive paths run on fixtures only, never on a store the agent did not create.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`; never run `soak_record.py` against a host.
- No hostnames, IPs, paths or secrets in commits, PRs or fixtures.

## Planner notes

- **Files missing from the inventory row:** `core/kernel/` (the archive-catalogue check is supplied by the kernel at 06 P4b,
  05-storage §5S.3.13 item 4), the file declaring `core.storage.*` config keys, the crash-matrix kit,
  `scripts/tests/test_check_soak.py` (the H cases of the H1–H3 digest §7), and the 05-storage §5S.3.14 table edit.
- **Trailer:** `H1 1` assumes H1 lands first; 09 §9.13 leaves the order to the owner.
- **H1's soak-table entry** cannot be written until H1's stage table decides whether H1 raises `core` (09 §9.13: "as 05-authn
  and 05-rbac decide"); the brief makes `--release H1` refuse until then.
- **Grant members** (`storage.Grant`, `GrantMode`, `Spec.Grants`, `ErrGrantNotOwned`, `module.ProblemGrant`, the ledger
  field `grants`, `mediacore_storage_grant_ops_total`, `storage_grants.golden`) share 05-storage §5S.3.14's row with this
  PR's members, but this PR has no consumer for them; they land with the first cutover that reads books' rows from core.
