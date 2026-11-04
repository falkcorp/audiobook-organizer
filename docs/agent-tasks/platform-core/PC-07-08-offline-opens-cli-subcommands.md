<!-- file: docs/agent-tasks/platform-core/PC-07-08-offline-opens-cli-subcommands.md -->
<!-- version: 1.0.0 -->
<!-- guid: cf6d230b-83ef-4133-9a73-6ac0569a7fa4 -->
<!-- last-edited: 2026-11-04 -->

# PC-07-08 — Offline opens for every ABO write subcommand

**Full title:** Offline opens: `app.OpenOffline` for every ABO write subcommand; `seed` refused on an empty
store; staging marker through `app.Offline`.

| Field | Value |
|---|---|
| ID | PC-07-08 |
| Tier | **T3 (Opus, high).** An offline open must never launder a foreign open: its close batch stages the owed entries a serve boot would stage before it promotes its own open stamp and sidecar identity. Getting that order wrong silently drops a coverage gap. |
| Wave | W07 (stage S2b, release `S2br1`) |
| Depends on | PC-07-06 |
| Trailer | `Stage-Release: S2b 1` |
| Normative digest | `docs/plans/platform-core/digests/S2b.md` sections 4.4, 5.9 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-07-08 -b feat/pc-07-08-offline-opens origin/main
cd "$REPO/../audiobook-organizer-pc-07-08"
git rebase origin/main
git worktree list
```

Fixtures only: every test opens a store the test created.

## Goal (09 S2b "Offline opens"; 05-storage §5S.4 "Offline open"; 06 §6.4.5, §6.4.6)

Every ABO subcommand that writes the store opens it through `app.OpenOffline`: `scan`, `playlist`, `tag`,
`organize`, `diagnostics cleanup-invalid`, `staging-scrub`, `staging-restore`'s target, and the `Isolate`
child mode. `diagnostics query` and the `bench`-tagged `dedup-bench` open read-only through it. `seed` is
refused with "start `serve` once first". `diagnostics reserved-prefs` stays HEAD's break-glass direct open
(and from here also repairs core's reserved records); `pebble-inject-skip`, `reserved-prefs` and `pid-census`
remain foreign opens. `staging-restore` writes the marker through `Offline.WriteStagingMarker`;
`staging-scrub` reads it through `ReadStagingMarker`.

## Exact files

Create: `core/app/offline.go`, `core/app/offline_test.go`, `cmd/offline_open_test.go`.

Modify: `cmd/` (`root.go` for scan, playlist, tag, organize; `diagnostics.go` for cleanup-invalid and query;
`seed.go`; `child_mode.go`; `dedup_bench.go` (`//go:build bench`)), `cmd/staging_restore.go`,
`cmd/staging_scrub.go`, `scripts/tests/test_staging_scrub.py`, and the doc that describes `seed` (see Step 6).
Not in the row (Planner notes): `core/kernel/offline.go` (`kernel.OpenOffline` and its close batch, 06
§6.4.6), `internal/books/store/` (remove the path-based `InitializeStore` that PC-07-05 kept for the CLI),
`core/coretest/crashmatrix/` and both crash-matrix instances (the offline-open scenarios), 06 §6.5.1's writer
table (offline close-batch labels).

## Re-verify before editing

```bash
grep -E '^(shipped_stage|release):' ci/stage.txt                      # shipped_stage: S2b / release: S2br1
grep -n 'initializeStore(' cmd/*.go | grep -v _test                    # HEAD: root.go:120,152,177,213,280 seed.go:95 child_mode.go:71 dedup_bench.go:106 (+ staging_*.go at base)
grep -n 'database.InitializeStore(' cmd/diagnostics.go                 # HEAD: :121 (ensureDiagnosticsStore)
grep -n 'pebble.Open(' cmd/diagnostics.go cmd/pebble-inject-skip/main.go   # HEAD: diagnostics.go:262 (reserved-prefs), pebble-inject-skip/main.go:35 — both stay
grep -n 'NewPebbleStore' cmd/pid-census/main.go                        # HEAD: :53 — stays a foreign open
grep -n '^//go:build bench' cmd/dedup_bench.go                         # 1 hit
ls cmd/staging_restore.go cmd/staging_scrub.go scripts/tests/test_staging_scrub.py   # PC-02-05
grep -n 'InitializeStore' internal/books/store/store.go                # >= 1 (kept by PC-07-05 for the CLI)
```

`serve` (root.go:280 at HEAD) no longer opens a store after PC-07-05; if it still does, STOP.

## Code contract (06 §6.4.5, copy exactly)

```go
func OpenOffline(ctx context.Context, opts Options, readOnly bool, mods ...module.Module) (*Offline, error)
type Offline struct{ /* unexported: kernel.Offline */ }
func (o *Offline) Storage(id module.ID) (module.StorageHandles, error)
func (o *Offline) Close() error
func (o *Offline) StagingMarker() (m storage.StagingMarker, ok bool, err error)
func (o *Offline) WriteStagingMarker(archiveSHA256 string) error
func ReadStagingMarker(ctx context.Context, opts Options) (m storage.StagingMarker, ok bool, err error)
func OfflineCommands(opts Options, mods ...module.Module) []Command
type Command struct {
	Name  string
	Short string
	Long  string
	Run   func(ctx context.Context, args []string, stdout io.Writer) error
}
func BinaryID(opts Options) (string, error) // delegates to buildid.Binary
```

`Offline.Storage` returns the module's handles as P4c builds them except `Maintenance` is nil.
`OpenOffline` refuses: a store with a pending step (`ErrOfflinePendingSteps`, naming the steps), an empty store
(every `Seed` step is pending), a staged or in-flight swap of any kind (the four restore/reset tokens). It
checks the guard, the floor and the claims, and computes the boot verdicts. `OfflineCommands` at S2b returns
only `storage restore-checkpoint` (the secrets and users commands are absent until their owners land).

## Invariants

1. **No laundering (R8-2).** When the open finds a foreign open, a missing or unreadable record, or a behind
   lineage, its close batch first stages the owed entries a serve boot would stage (`journal`, `gen` with
   `since` = the previous `core` record's `at`, `detected` = the offline open's time), and only then promotes its
   own open stamp and sidecar identity.
2. A clean offline write-and-close leaves the next serve boot reading no foreign open and owing nothing; a crash
   before the close reads as a foreign open (machine 4's documented fail-closed window).
3. `readOnly` writes nothing, not even an `OPTIONS-<n>`.
4. `WriteStagingMarker` stages `c/storage/staging` into the close batch; `Close` commits it, then writes the
   sidecar `<db>.staging`, so a crash between them leaves a key without a sidecar, which `staging-scrub` refuses.
5. `staging-scrub` calls `ReadStagingMarker` (read-only, writes nothing) before any read-write open, and refuses
   unless both halves agree on hash, nonce and `binary_id` and that `binary_id` equals its own `app.BinaryID`.

## TDD: tests first (spec names)

- `TestOfflineOpenIsNotForeign` (core): write + clean close → next serve owes nothing; unjournaled revoking write
  → owes `journal`; crash before close → foreign; pending step, empty store, staged or in-flight swap of any kind
  → refused; `Maintenance` nil; `readOnly` writes nothing.
- `TestOfflineOpenAfterForeignOpenKeepsEvidence` (core): a pre-guard open (separately: a behind lineage; an
  unreadable `core` record), then an offline write, then serve: `journal` and `gen` owed with the `since` and
  `detected` above.
- `TestCLISubcommandsUseOfflineOpen` (ABO, `cmd/offline_open_test.go`): an AST walk over `cmd/` asserting that
  every write subcommand reaches the store only through `app.OpenOffline`, that no file under `cmd/` (outside
  the allowed foreign-open files `cmd/diagnostics.go`'s reserved-prefs path, `cmd/pebble-inject-skip/`,
  `cmd/pid-census/`) calls `pebble.Open`, `NewPebbleStore` or `InitializeStore`, and that `seed` refuses an
  empty store with the text `start \`serve\` once first`.
- `TestStagingRestoreWritesMarker` and `TestStagingScrubRefusesUnmarkedStore` (ABO): extend to go through
  `app.Offline`.
- `scripts/tests/test_staging_scrub.py` S2b cases (09 §9.3): the scrub opens through `app.OpenOffline` with the
  archive's binary on a fixture that `cur` still has to migrate, and is refused with `cur`.
- Crash-matrix scenarios (both instances): an offline open followed by `serve`, and one killed before its close
  batch.

## Steps

1. Re-verify. Read 06 §6.4.6 ("offline open" procedure) and 05-storage §5S.4 "Offline open" step by step.
2. Write the core tests, then `kernel.OpenOffline` (core/kernel/offline.go) and `app/offline.go`.
3. Rewire each subcommand: build `app.Options` the way `serve` does, call `app.OpenOffline(ctx, opts, readOnly,
   books.Module())`, take `Storage("books")`, construct the books store over the handles, `defer off.Close()`
   and check its error. Read-only: `diagnostics query`, `dedup-bench`.
4. `seed`: keep the command; before anything, return an error whose message contains ``start `serve` once
   first`` when `OpenOffline` returns `ErrOfflinePendingSteps` on an empty store; update `Long`.
5. Staging: `staging-restore` writes the marker through `WriteStagingMarker`; `staging-scrub` reads through
   `ReadStagingMarker`; extend the Python test.
6. Docs: update the `seed` help text, and add one line to `QUICKSTART.md`'s development section (or the doc
   that `grep -rln 'audiobook-organizer seed' docs README.md QUICKSTART.md` finds outside `docs/archive/`; HEAD
   finds only an archived backlog, so add the line to `QUICKSTART.md`).
7. Delete the path-based `InitializeStore` that PC-07-05 kept for the CLI. `cmd/pid-census` keeps the
   foreign-open constructor PC-07-05 gave it (`OpenPathForeign`, coined there); do not route it through
   `OpenOffline`.
8. Crash-matrix scenarios and writer-table rows; acceptance.

## Acceptance

```bash
go build ./... && go build -tags bench ./... && go -C core build ./...                     # exit 0
go -C core test -race -count=1 -run 'TestOfflineOpen' ./app/ ./kernel/                    # ok
go test -count=1 -run 'TestCLISubcommandsUseOfflineOpen|TestStagingRestoreWritesMarker|TestStagingScrubRefusesUnmarkedStore' ./cmd/   # ok
python3 -m pytest -q scripts/tests/test_staging_scrub.py                                   # passed, 0 failed
grep -rn 'InitializeStore' --include=*.go cmd internal | grep -v _test                     # no output
make bench-check && make -C core ci && make ci                                             # exit 0
```

`unit-lifecycle` triggers on `cmd/**`; it must stay green.

## STOP conditions

- `serve` still opens a store at your base (PC-07-05 incomplete).
- A subcommand needs a migration to run offline (refusal is the contract; do not add a migrate path).
- The no-laundering order cannot be expressed as one close batch.

## Out of scope

Secrets and users offline commands (S3, S4); `diagnostics reserved-prefs` behaviour beyond also repairing
core's reserved records (05-storage); core's `storage restore-checkpoint` command wiring into ABO's cobra tree
beyond listing it.

## Rollback

Revert: the CLI returns to its own open (a foreign open per CLI write on the next serve boot).

## Repo rules

- Headers on every new or changed file (Python test: `#` header; bump it).
- Every commit `Stage-Release: S2b 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n>
  --rebase`; never squash; never `--admin`.
- Every boot-time durable write added (the offline close batch counts) extends both crash-matrix instances and
  06 §6.5.1's table in this PR.

## Planner notes

- **Missing from the row:** `core/kernel/offline.go` (`kernel.OpenOffline`, which `app.OpenOffline` wraps),
  `internal/books/store/` (drop the CLI's path-based open), `core/coretest/crashmatrix/` and the two instances,
  and the 06 §6.5.1 writer-table rows.
- **Timing against 09.** 09 S2b requires offline opens "from this stage's PR", that is the flip (PC-07-05). See
  PC-07-05's note; the plan's order leaves a three-PR window.
