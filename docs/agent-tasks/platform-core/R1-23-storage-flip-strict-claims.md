<!-- file: docs/agent-tasks/platform-core/R1-23-storage-flip-strict-claims.md -->
<!-- version: 1.1.3 -->
<!-- guid: 74651b7f-033f-42d7-b873-4ecf3f078896 -->
<!-- last-edited: 2026-11-05 -->

# R1-23 — Storage flip: core opens the one Pebble store, books reaches its families only through strict claims on storage.LegacyRange, the storage files re-home into core, the first-boot checkpoint, LegacyCap fixed, books.Module()

| Field | Value |
|---|---|
| ID | `R1-23` |
| Tier | **T4** (Opus / xhigh): the keystone flip: core opens the one store under strict claims for every books family |
| Release / wave | R1 / W15 |
| Depends on | R1-15, R1-20, R1-21, R1-22, R1-04 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-storage §5S.3.4 (strict claims, shadow dial), §5S.3.5 (LegacyCap), §5S.3.7, §5S.9.3, §5S.9.4 step 2; 04 §4.5, §8.4 note 1; 09 §9.2.2, §9.2.3 (checkpoint), §9.4 R1 Content: storage; 03 §3.7 |
| Branch | `agent/platform-core-r1-23-storage-flip-strict-claims` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `internal/database/`
- `core/storage/`
- `core/internal/pebble/`
- `internal/metrics/`
- `main.go`
- `cmd/root.go`
- `internal/books/`: `module.go`, `claims.go`, `crash_matrix_test.go`
- `internal/server/server_lifecycle.go`
- `internal/startup/`
- `.github/workflows/`: `extraction-gates.yml`, `docker-image.yml`
- `core/Makefile`
- `scripts/storage_format_guard.py`
- `tools/cmd/sdkguard/internal-deps.txt`
- `internal/arch/layering_test.go`
- `scripts/ci/short_test_weights.json`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-23" -b agent/platform-core-r1-23-storage-flip-strict-claims origin/main
cd "$REPO/.worktrees/platform-core-r1-23"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `ac4368bdb`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

The keystone storage flip (09 §9.4 R1 "Content: storage"; 05-storage §5S.3.4, §5S.3.5, §5S.3.7, §5S.9.3,
§5S.9.4 step 2; 04 §4.5, §8.4 note 1; 09 §9.2.2, §9.2.3; 03 §3.7):

1. **Core opens the one Pebble store.** The kernel's P2 opens it; books stops opening it; `serve` drops HEAD's
   pre-server store open, migrations and init writers (06 §6.11.1 step 2: "from that PR the kernel opens the store
   and those steps leave `serve`, each to the successor §6.4.3 names"); `main` becomes
   `app.New(ctx, opts, books.Module())` with no store argument (03 §3.5.1 item 1; 06 §6.11.1). `internal/startup`'s
   store-open, migration and encryption steps go to their successors; the rest of `internal/startup` stays ABO until
   F8 (04 §8.1 R1).
2. **Books reaches its families only through strict claims** on `storage.LegacyRange`, every family
   `ClaimReadWrite`, generated from `keyfamilies.go` (which stays ABO; the reserved `c/`/`m/` rows are skipped,
   05-storage §5S.3.4 kernel validation item 2). **Strict from this release**; `core.storage.claims.mode=shadow`
   stays as the emergency dial (logs, counts `<namespace>_storage_claim_violations_total{mode="shadow"}`, allows;
   nothing persisted) (05-storage §5S.9.4 step 2). The full suite and e2e run under strict claims in CI before the
   flip merges.
3. **The storage files re-home into core.** `internal/database`'s engine files (the 12 files of 04 §4.5's first
   row, the cut ones R1-22 prepared: `pebble_store_open.go`, `store_init.go`'s `InitializeStore` part,
   `migrations_runner.go`, plus `pebble_gate.go`, `storage_format.go`, `census*.go`) move to `core/storage` and
   `core/internal/pebble` by `git mv` where 04 §2.2 says "move", replacing R1-20's dark re-implementation where they
   overlap (one implementation, not two). ABO keeps the residue (04 §4.5's other rows) with a thin adapter
   `internal/database/legacyrange.go` mapping the used `GatedDB` subset onto `storage.LegacyRange` (05-storage
   §5S.9.3); `database.Store` splits per 04 §4.5. `internal/metrics`' Pebble collector → `core/internal/pebble`
   (04 key "R1 move internal/metrics -> mediacore/internal/pebble"), **keeping its metric names** (row notes).
4. **Books' legacy migrations** become `LegacyMigration`s 1..`LegacyCap` over the version-source adapter
   (non-atomic, idempotent, run first on books' own claims); `LegacyCap` is fixed at the highest HEAD-style step
   when this PR lands (66 at `748fd787a`; 09 §9.2.2); every later books step is a versioned `Migration` numbered from
   `LegacyCap+1`. HEAD's init-phase writers get the homes of 05-storage §5S.9.3's table (the import-path key
   migration and list-index backfill as books `Migration`s; the journal-clock repair, the ident-index stamp,
   `ReconcileOpsV3WithV2` and `ReconcileVerifiedEmails` in books' start hook, 06 §6.5.2 step 2; no counter seeding).
5. **The first-boot checkpoint:** on a non-empty store the first R1 boot takes the pre-migration checkpoint before
   any `c/` write, then writes the install marker and every owner's level stamp at 0 (09 §9.4 R1 "Migrations";
   09 §9.2.3). No level is raised and the legacy projection stays at HEAD's format 1 (09 §9.2.2 "R0 and R1 raise no
   level").
6. **`--print-storage-levels`** fast exit lands here; set `PROBE_STORAGE_LEVELS=1` in `docker-image.yml` (R1-04's
   wired probe). `scripts/storage_format_guard.py` and `make rollback`'s guard keep working against the legacy
   projection.
7. `core/Makefile`: core's coverage **ratchet floor** starts here ("from the storage move", 09 §9.4 R1
   classification row `coverage-check-short`): measure and commit it.

## Tests this task owns (spec names)

- `TestR0OpensR1Store` (05-storage; 09 §9.10): the R0 binary opens a store the R1 binary wrote and reads every row;
  deleted in R3.
- `TestBootCrashMatrix_ABO` (05-storage §5S.10; 09 §9.4 R1): the R1-21 kit over books' full boot, books' writers
  included, in `internal/books/crash_matrix_test.go` (file name coined here; R3-03 later adds `legacy-file-cleanup`'s
  scenario beside it); runs in `extraction-gates` (add the job, `timeout-minutes` ≤ 30).
- `TestNoNewLegacyMigrations` (09 §9.2.2): the legacy track is pinned at `LegacyCap`.
- `make ci` under strict claims (05-storage §5S.9.4 step 2): the whole root suite runs with the strict mode, and a
  test asserts the mode in force is `strict`.
- Also from 05-storage §5S.10 for this flip: `TestABOClaimsMatchOwnershipTable`, `TestBooksWritesEveryFamily`,
  `TestLegacyClaimsNeverRefuseBlindWrites`, `TestBooksConstructorWritesNothing`, `TestR1BootRepairsOpsAndEmails`,
  `TestLegacyMigrationsReplayIdempotent`, `TestLegacySteps59to66RunFirst`, `TestCheckpointPrecedesEveryWrite` on
  ABO's boot, `TestOrphanedClaimNeedsTakeover`, `TestFirstBootRefusesForeignKeys`.

## Dependency check

R1-15, R1-20, R1-21, R1-22 merged:
`test -d internal/books && test -d core/storage && test -d core/internal/restoreswap && test -f internal/database/pebble_store_open.go && echo ok`.
And R1-04 merged (the image job whose storage-levels probe this row turns on): `test -f .github/workflows/docker-image.yml && echo ok`.

## Re-verify (expected at `ac4368bdb`, plus the predecessors)

```bash
ls internal/database/pebble_store_open.go internal/database/store_init.go internal/database/migrations_runner.go   # R1-22's cut files
grep -c '^\s*Version: *[0-9]' internal/database/migrations.go          # expect 66 at ac4368bdb (last at line 504: Version: 66); LegacyCap = 66 unless a step landed since
ls internal/metrics/pebble_collector.go internal/startup/steps.go       # expect both
grep -n 'internal/metrics' tools/cmd/sdkguard/internal-deps.txt         # expect 1 (regenerate after the move)
grep -n 'print-storage-format' Makefile                               # expect make rollback's guard (Makefile:904 at ac4368bdb; 09 cites :892-917)
```

## Steps

1. Tests first (red), including the strict-claims assertion and `TestR0OpensR1Store`'s harness (it needs the R0 tag's
   binary: build it from the tag in a temp worktree, or download the release asset, as its doc comment states).
2. Generate books' claim list from `keyfamilies.go` (skipping `c/`, `m/`); wire `internal/books/claims.go` and
   `internal/books/module.go` (`Module()` without the store); strict mode default, `shadow` dial.
3. Re-home the engine files into `core/storage`/`core/internal/pebble` (one implementation), add
   `internal/database/legacyrange.go`, split `database.Store`; move the Pebble collector.
4. Books' legacy steps as `LegacyMigration`s up to `LegacyCap`; HEAD's init writers to their homes; remove the
   pre-server steps from `serve`.
5. `--print-storage-levels`; `PROBE_STORAGE_LEVELS=1`; `TestBootCrashMatrix_ABO` job in `extraction-gates`; core
   coverage floor; regenerate `tools/cmd/sdkguard/internal-deps.txt`; update `layerOf` and shard weights.
6. Header bumps; fragment `changelog.d/<today>_storage_flip.md` with the R1 note lines this PR owns
   (`--print-storage-levels`); the PR lists every `old ==> new` path. Every commit builds.

## STOP conditions (data loss or a broken rollback)

- Any write before the checkpoint on a non-empty store; any level raised; the legacy projection changed.
- `TestR0OpensR1Store` cannot pass (R1's rollback to R0 must lose nothing).
- A family books writes is not claimed (strict mode would refuse a live write): list it, never widen a claim to a
  foreign range.
- A Pebble metric family changes name.

## Code contract

`books.Module() module.Module`; books' claims through `storage.LegacyRange` (05-storage §5S.3.4); `--print-storage-levels` prints the level vector as JSON (05-storage §5S.3.7).

## Acceptance

```bash
go test ./... -short -count=1                                 # expect ok, under strict claims
go test ./internal/books/... -run 'TestBootCrashMatrix_ABO|TestNoNewLegacyMigrations|TestR0OpensR1Store' -count=1 -v   # expect PASS (TestR0OpensR1Store builds or downloads the R0 binary as its harness states)
make build-api && ./audiobook-organizer --print-storage-levels --db "$(mktemp -d)/x.pebble"   # expect JSON with every owner at level 0
make sdkguard                                                 # expect pass after regenerating internal-deps.txt
make test-e2e                                                 # expect green under strict claims
make ci && make -C core ci                                    # expect exit 0
```

On the PR: the `docker-image` job green with `PROBE_STORAGE_LEVELS=1`; the `extraction-gates` job with
`TestBootCrashMatrix_ABO` green.

## Out of scope

The checkpoint sweeper and prune (R1-24); offline restore and reset, and deleting HEAD's reset routes (R1-25);
renaming the residue to `internal/books/store` (R1-26).

## Rollback

`make rollback` to R0: R0 ignores the `c/storage/` rows (`TestR0OpensR1Store`). Reverting the PR before release
restores ABO's own open.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Move window (09 §9.2.10).** This task moves or re-homes packages. Before you start, the lead
  announces a move window in the tracking issue at least one working day ahead, naming the 04 rows it
  freezes and its end (at most two working days). If no announcement exists, STOP and ask the lead. The PR
  description lists every `old ==> new` package path, one per line. Every commit of the PR builds:
  `go build ./... && go -C core build ./...` at each commit (`scripts/ci/check_commits_build.py` checks it
  in `extraction-gates`); keep the rename and the import rewrite in one commit, or make each commit build.
  Apply in the same PR the `go fix` modernizers core's `fix-check` requires in the moved files
  (`go -C core fix ./...`, 04 §2.2).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.


## Planner notes

- The row lists `core/Makefile` for the core coverage ratchet floor "from the storage move" (09 §9.4 R1); the brief
  sets it here, as R1-03's brief defers it.
