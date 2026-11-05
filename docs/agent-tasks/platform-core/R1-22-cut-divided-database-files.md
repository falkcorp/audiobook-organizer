<!-- file: docs/agent-tasks/platform-core/R1-22-cut-divided-database-files.md -->
<!-- version: 1.1.0 -->
<!-- guid: 1143f20d-1bb7-4a87-a829-58a795a47d5c -->
<!-- last-edited: 2026-11-05 -->

# R1-22 — Mechanical cut of the divided internal/database files (pebble_store.go, store.go, migrations.go) so the storage move is a pure file move

| Field | Value |
|---|---|
| ID | `R1-22` |
| Tier | **T2** (Sonnet / high): mechanical file cut with no behaviour change |
| Release / wave | R1 / W05 |
| Depends on | R1-01 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 04 §4.5 (file-level split), Open items item 5; 05-storage §5S.9.3 |
| Branch | `agent/platform-core-r1-22-cut-divided-database-files` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `internal/database/pebble_store.go`
- `internal/database/store.go`
- `internal/database/migrations.go`
- `internal/database/pebble_store_open.go`
- `internal/database/store_init.go`
- `internal/database/migrations_runner.go`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-22" -b agent/platform-core-r1-22-cut-divided-database-files origin/main
cd "$REPO/.worktrees/platform-core-r1-22"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Cut the three divided `internal/database` files (04 §4.5 "Three files must be divided internally"; 04 "Open
items" item 5: "each needs a mechanical 'cut' PR before its move PR so that the move is a pure file move";
05-storage §5S.9.3) so that R1-23 can re-home the engine parts with `git mv` and no file edit. This PR moves
whole declarations, byte for byte, between files of the same package. No identifier, signature, comment or
behaviour changes; the package compiles to the same thing.

| from | declarations (anchors at `f7ac5b11e`, equal to 04's at `748fd787a`) | to (new file, names fixed here) |
|---|---|---|
| `pebble_store.go` (6,767 lines) | lines 547–1044: from `// NewPebbleStore creates a new PebbleDB store` through the closing brace of `newULID`, i.e. `NewPebbleStore`, `NewPebbleStoreInMemory`, `newPebbleStore`, `pebbleOpenMode` and its constants, `openPebbleChecked`, `initPebbleStore`, `pebbleCloseLog`, `closingContext`, `Close`, `closeStore`, `Checkpoint`, `DB`, `nextID`, `orderedULIDSource` with `newOrderedULIDSource` and `next`, `orderedULIDs`, `newULID` (04 §4.5: `:548-1060` → `internal/pebble`) | `pebble_store_open.go` |
| `store.go` (1,897 lines) | lines 1807–1897: `globalStore`, `globalStoreMu`, `GetGlobalStore`, `SetGlobalStore`, `InitializeStore`, `opsV3Reconciler`, `CloseStore` (04 §4.5: `InitializeStore` → the kernel's open; `globalStore`/`GetGlobalStore` deleted later) | `store_init.go` |
| `migrations.go` (1,425 lines) | lines 72–109: `MigrationFunc` with its idempotency-contract doc, `Migration`, `MigrationRecord`, `DatabaseVersion`; lines 511–576: `RunMigrations` (04 §4.5: the types and the runner → `storage` migrations) | `migrations_runner.go` |

What stays where it is, on purpose: `newUnlinkedULID` and `migrateImportPathKeys` in `pebble_store.go` (session
IDs are identity code; the import-path migration becomes a books legacy step); `getCurrentVersion`/`setVersion`
in `migrations.go` (books' version-source adapter, 04 §4.5); the `migrations` list and the 66 bodies in
`migrations.go`. The `PebbleStore` struct is not edited here (its split is R1-23's).

## Dependency check

R1-01 merged: `test -f core/go.mod && echo ok`. Otherwise STOP.

## Re-verify (expected at `f7ac5b11e`)

```bash
cd internal/database
wc -l pebble_store.go store.go migrations.go             # expect 6767, 1897, 1425
sed -n 547p pebble_store.go                              # expect: // NewPebbleStore creates a new PebbleDB store
sed -n 1042,1044p pebble_store.go                        # expect: func newULID() (string, error) {  /  return orderedULIDs.next()  /  }
sed -n 1807p store.go                                    # expect: // Global store instance — use GetGlobalStore/SetGlobalStore for concurrent access.
sed -n 72p migrations.go                                 # expect: // MigrationFunc represents a migration operation.
sed -n 109p migrations.go                                # expect: }   (end of DatabaseVersion)
sed -n 511,512p migrations.go                            # expect: // RunMigrations applies all pending migrations  /  func RunMigrations(store migrationStore) error {
sed -n 576p migrations.go                                # expect: }
ls pebble_store_open.go store_init.go migrations_runner.go 2>&1 | grep -c 'No such file'   # expect 3
```

The files have not changed since `748fd787a` except where noted by `git log --oneline 748fd787a.. -- <file>`.
If an anchor line differs at your base, locate the same declaration by its `func`/`type` line and use the
same boundaries (the doc comment above the first declaration through the closing brace of the last). If a
declaration listed above no longer exists or has moved to another file, STOP and report.

## Steps

1. For each row of the table, cut the line range out of the source file and paste it unchanged into the new
   file after a header (`// file: internal/database/<new>.go`, `version: 1.0.0`, fresh `guid`,
   `last-edited`) and `package database`. Do the cuts bottom-up within a file so earlier line numbers stay
   valid (in `migrations.go`: 511–576 first, then 72–109).
2. Imports: give each new file exactly the imports its code uses, and remove from each source file the ones
   it no longer uses. With no `goimports` in the toolchain, iterate: `go build ./internal/database/` reports
   `"x" imported and not used` and `undefined: x` per file; fix only import lines. `gofmt -w` the six files.
3. Prove it is a pure move (expect no output from either command):

   ```bash
   cd internal/database
   cat <(git show HEAD:internal/database/pebble_store.go) <(git show HEAD:internal/database/store.go) <(git show HEAD:internal/database/migrations.go) \
     | grep -v -E '^(// (file|version|guid|last-edited):|import|\t"|\)$|$)' | sort > /tmp/before.txt
   cat pebble_store.go store.go migrations.go pebble_store_open.go store_init.go migrations_runner.go \
     | grep -v -E '^(// (file|version|guid|last-edited):|package database|import|\t"|\)$|$)' | sort > /tmp/after.txt
   diff <(grep -v '^package database' /tmp/before.txt) /tmp/after.txt
   ```

   The only acceptable differences are import lines (aliased imports start with a tab and a name; if one
   shows up, confirm it is an import line). Any other difference: undo and redo the cut.
4. Bump the three source files' headers (patch +1). `mocks-check`, `types-check` and the store-width gates do not
   read file names, so they need no change; confirm with `make ci`.
5. Headerless fragment `changelog.d/<today>_database_divided_files_cut.md` (`### Changed`: "Internal: three
   database files split so the storage move can be a pure file move; no behaviour change."). Commit:
   `refactor(database): cut the engine, init and migration-runner parts into their own files`.

## Tests

No new test and no test change: a pure cut. The proof is step 3 plus the unchanged suite.

## Code contract

None: no declaration changes, only the file each lives in.

## Acceptance

```bash
cd internal/database
grep -c '^func NewPebbleStore(\|^func openPebbleChecked(\|^func initPebbleStore(\|^func newULID(' pebble_store_open.go   # expect 4
grep -c '^func InitializeStore(\|^func GetGlobalStore(\|^func CloseStore(' store_init.go                             # expect 3
grep -c '^func RunMigrations(\|^type Migration struct\|^type MigrationFunc' migrations_runner.go                      # expect 3
grep -c '^func NewPebbleStore(\|^func InitializeStore(\|^func RunMigrations(' pebble_store.go store.go migrations.go | paste -sd' '   # expect pebble_store.go:0 store.go:0 migrations.go:0
cd ../.. && go build ./... && go vet ./internal/database/                                                            # expect exit 0
go test ./internal/database/ -short -count=1                                                                          # expect ok
make ci                                                                                                               # expect exit 0
```

## Out of scope

Moving anything into `core/` (R1-23), editing the `PebbleStore` struct, the `rejectOpv2OpRawKey` predicate,
the ops reconcile, the legacy step list, `globalStore`'s deletion.

## Rollback

Revert the commit.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
