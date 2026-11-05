<!-- file: docs/agent-tasks/platform-core/F3-04-storage-cutover-machinery-dryrun-quarantine.md -->
<!-- version: 1.0.0 -->
<!-- guid: e1287955-3c4d-4b21-b3f5-bebfca0452a8 -->
<!-- last-edited: 2026-11-05 -->

# F3-04 — Storage cutover machinery: the level step, the quarantine record, the read-only preflight hook, M8's store-rewrite swap kind, `storage migrate --dry-run`, `storage quarantine list|export|purge`

| Field | Value |
|---|---|
| ID | F3-04 |
| Release / flip | R2, config flip F3 (dark PR) |
| Tier | **T4** (Opus, xhigh–max): the level step, quarantine and dry run guard every one-way step |
| Wave | W24 |
| Depends on | F2-06 |
| Owns gate tests | `TestDryRunMatchesLiveRun`, `TestQuarantineCommands` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f3-04-cutover-machinery
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F2-06 is merged. If not, stop.

## Goal

Build, in core and unregistered, every mechanism the R2 cutover steps run on that is not the runner itself (F2-06
built `storage/datamigrate` and `internal/storagemigrate`):

1. the **level step** procedure of 05-storage §5S.3.7 as a reusable `Breaking` versioned step with no writes of its
   own, with books' legacy projection staged through `LegacyGuardProjection.Stage`, and core's own half of M7;
2. the **quarantine record** `storage.QuarantineRecord` under `c/storage/quarantine/<step-id>/<n>`, with sealing of
   rows under a step's `Sensitive` prefixes (never kept raw), the health token and the metric;
3. the **read-only cutover preflight hook** in the kernel's P4a, run only on a store whose cutover has not started,
   and the `storage.ErrCutoverBlocked` refusal with exit 78;
4. M8's **store-rewrite swap kind** `restorejournal.KindRewrite` run in process by `core/internal/restoreswap`
   (compact, copy into a fresh directory, journaled swap, delete), with the phase derived from M8's own journals and
   `storage.ErrRewriteSwappedBack`;
5. the offline commands `storage migrate --dry-run [--id <id>] [--from-archive <file>] [--estimate]` with its
   scratch area, and `storage quarantine list | export --out <file> | purge --step <id> --confirm`.

Nothing is registered for ABO here: books' M7 and M8 definitions are F3-06's, registration is F3-07's.

## Spec

- 05-storage §5S.3.5 (versioned steps, `Breaking`, `RaisesLevel`), §5S.3.6 (`DataMigration` fields `Cutover`,
  `Sensitive`, `Residual`, `Preflight`, `FixBlocker`, `DeleteBlocker`; `StepWriter.Quarantine`, `Batch`; the
  **Dry run** bullet), §5S.3.7 (levels, `LevelTooNewError`, `LegacyGuardProjection`, the level step procedure, exit
  78), §5S.3.12 ("Scratch copies", "Free space", "When"), §5S.3.13 (principles, importer rules, **Quarantine**,
  `QuarantineRecord` and the four errors, **Cutover blockers** "When", **The list** rows M7 and M8, **M8: the
  rewrite**, Tests), §5S.3.15 (`restorejournal.Intent.StepID`, `Record.StepID`, `KindRewrite`), §5S.3.16 machine 1,
  §5S.4 steps 5a–9, §5S.5 rows, §5S.8 (`mediacore_storage_quarantine_rows`), §5S.10 (`TestDryRunMatchesLiveRun`,
  `TestDryRunScratchModeAndSweep`, `TestQuarantineCommands`).
- 05-backup §5B.3.4 "M8's store rewrite is `KindRewrite` of this swap" rules (1)–(5); `TestRewriteSwapBackMovesNoCheckpoint`.
- 05-config §2.4 step 1 (the dry run's throw-away keyring: `secrets.Open` with a `KeyFile` in the scratch directory).
- 06 §6.4.5 `OfflineCommands` (`storage migrate --dry-run` opens the live store read-only for its copy;
  `storage quarantine list|export` read-only, `purge` read-write through `kernel.OpenOffline`); 06 §6.5.1 P4 (a) and
  the boot-writer table (M7's sidecar and stamp, M8's phases).
- 09 §9.2.3 ("Quarantine", "Dry run"), §9.4 R2 Verification.

## Files

| Action | Path |
|---|---|
| create | `core/storage/level_step.go` |
| create | `core/storage/quarantine.go` (`QuarantineRecord`, the four errors, the quarantine keyspace and sequence, the sealing seam) |
| create | `core/storage/preflight.go` (the "cutover started" predicate, the blocker report renderer) |
| create | `core/storage/dryrun.go` (scratch-area copy, the dry-run driver, the report) |
| create | `core/internal/restoreswap/rewrite.go` (the `KindRewrite` swap and the M8 phase engine) |
| create | `core/app/offline_storage.go` (`storage migrate --dry-run`, `storage quarantine …`) |
| modify | `core/kernel/migrate.go` (P4a preflight before the headroom check and checkpoint; M8's in-process swap; exit 78 mapping) |
| create | tests beside each |

## Re-verify at BASE

```bash
grep -rn 'type DataMigration struct' core/storage/datamigrate/ | wc -l          # expect 1 (F2-06)
grep -rn 'Preflight\b\|Preflight ' core/storage/datamigrate/*.go | wc -l       # expect >= 1 (the field)
grep -rn 'func (w \*StepWriter) Quarantine' core/storage/datamigrate/ | wc -l  # expect 1 (see STOP 1)
grep -rn 'func (w \*StepWriter) Batch' core/storage/datamigrate/ | wc -l       # expect 1
grep -rn 'KindRewrite' core/storage/restorejournal/ | wc -l                     # expect >= 1 (R1-20; see STOP 2)
grep -rn 'StepID' core/storage/restorejournal/ | wc -l                          # expect >= 2 (Intent and Record)
grep -rn 'type LegacyGuardProjection struct' core/storage/ | wc -l             # expect 1 (R1-20)
grep -rn 'LevelTooNewError' core/storage/ | wc -l                               # expect >= 1
grep -rn 'func OpenOffline' core/kernel/ | wc -l                                # expect 1 (R1-25)
grep -rn 'mediacore-checkpoints' core/ | wc -l                                  # expect >= 1 (R1-23/R1-24 checkpoint dir)
ls core/storage/level_step.go core/storage/quarantine.go core/storage/preflight.go core/storage/dryrun.go core/internal/restoreswap/rewrite.go 2>/dev/null | wc -l   # expect 0
```

## STOP conditions

1. `StepWriter.Quarantine` or `StepWriter.Batch` is missing or a stub in F2-06's `datamigrate`: the sealing must happen
   in the runner's chunk batch, and `core/storage/datamigrate/` and `core/internal/storagemigrate/` are not in this
   row's files. Report.
2. `restorejournal.KindRewrite` or the `StepID` fields are missing: `core/storage/restorejournal/` is not in this row's
   files. Report.
3. Anything here would run in ABO's boot before F3-07 (a level raise, a preflight, a quarantine write, an M8 swap).
4. The dry run writes anything to the live DB directory or the state directory's keyring.

## Steps

1. Write the tests below first.
2. **Level step** (`level_step.go`; §5S.3.7 procedure): `new = max(stored, RaisesLevel)`; equal → stamp nothing; else
   write `.storage-levels` (and for books `.storage-format` = `LegacyMaxFormat + 1` and, for a books `Breaking` raise,
   the marker) with fsync, then commit the stamp batch (with `LegacyGuardProjection.Stage` when the projection
   changes) alone, before any chunk; on a commit error rewrite the sidecars from the committed stamps. The marker is
   cleared at §5S.4 step 9 of the boot that leaves no books `Breaking` step pending. Provide a constructor for core's
   own half of M7 (`core` raised), unregistered.
3. **Quarantine** (`quarantine.go`): `QuarantineRecord` exactly as below; key `c/storage/quarantine/<step-id>/<n>`,
   `n` from the keyspace's `quarantine` `Sequence`; a row under one of the step's `Sensitive` prefixes is sealed as the
   secret record `core.storage.quarantine.row-<n>` through the sealer and the record names it in `Secret` with an
   empty `Value`. The sealer is F3-03's `secrets.Importer` (05-config §2.3 hands it to storage's quarantine sealing);
   `storage` (L1) cannot import `secrets` (L2), so the runner holds it, and any storage-side seam is a one-method
   interface (name coined here). Health `storage_quarantine` (warning; count per step, `sensitive`,
   `orphaned_refs`), carried only on authenticated routes; `mediacore_storage_quarantine_rows{step, sensitive}`.
4. **Preflight** (`preflight.go`, `kernel/migrate.go`): before the headroom check and the checkpoint (06 §6.5.1 P4 (a),
   §5S.4 step 5a), if and only if the cutover **has not started** (books' level is below M7's target **and** no cutover
   step has a progress record in `c/storage/dm/`), build the cutover's importers without writing and call every
   pending cutover step's `Preflight`; any blocker → `storage.ErrCutoverBlocked`, exit 78, nothing written (no
   checkpoint, no marker, no step), the report (step, key, family, reason, fix; never a value) in the boot log, last
   line naming `storage cutover-blockers`. A resuming boot skips it. On a staged-restore boot a refusal is 05-backup's
   step x.
5. **M8 rewrite** (`restoreswap/rewrite.go`, `kernel/migrate.go`): phases 1 compact (flush, `storage.Admin.Compact`
   over the whole range, `parallelize`), 2 copy (every live key into `<db parent>/<db name>.rewrite.tmp/` by SST
   ingest, same format pin and options; fsync; rename to `<db name>.rewrite/`; fsync parent; state `copied`), 3 swap
   (`KindRewrite` in process: close, set aside, move into place, rewrite sidecars, reopen; `c/storage/epoch` keeps its
   `epoch` with the new directory identity; one intent slot, `ErrSwapPending` otherwise; in-flight file before the
   first rename), 4 delete (the set-aside directory at once; M8 `done`; journal `applied`). Phase derivation from the
   `KindRewrite` journals **whose `StepID` is this M8's own**, newest first: none → phase 3; `swapped`/`opened` → phase
   4 without a second swap; `swapped_back` → restart from phase 1 once; the two newest both `swapped_back` →
   `storage.ErrRewriteSwappedBack`, exit 78, nothing staged. A swap-back moves no checkpoint and starts no clock.
   Free space (§5S.3.12): a boot whose list holds M8 adds the store's size once more. The API by which books' M8
   definition (F3-06) invokes this engine is not named by the spec: coin it (mark it "(name coined here)" in the code
   comment and the PR) and keep its ID rule `books.rewrite-after-<step name>`.
6. **Dry run** (`dryrun.go`, `offline_storage.go`): refuse while another process holds the DB lock; open the live
   store **ReadOnly** (no WAL flush, no ratchet, no write); hard-link SSTs and copy WAL, MANIFEST, OPTIONS into a new
   directory of the scratch area `.mediacore-checkpoints/scratch/` (under `core.storage.checkpoint_dir` when set; area
   and directory 0700, files 0600, free-space precheck), or extract `--from-archive <file>` there with R1's offline
   extractor; open it as a full DB; run the preflight first, then every pending step in DAG order with `Verify`, M2
   sealing with a throw-away keyring created in the scratch directory (`secrets.Open` with a `KeyFile` there; until
   F3-07 wires the keyring, a test fake); M8 rewrites into a second scratch directory and swaps nothing; report the
   blockers first, then per step counts per outcome, held items, would-quarantine keys and reasons, bytes written, the
   first N keys per outcome and duration; delete the scratch whatever the outcome. `--estimate` is the old in-batch
   discard mode, labelled an estimate.
7. **Quarantine commands** (`offline_storage.go`): `list` (keys, reasons, sizes, never values; read-only open),
   `export --out <file>` (values, sensitive ones decrypted through the unsealer seam, mode 0600, refused if the file
   exists), `purge --step <id> --confirm` (read-write through `kernel.OpenOffline`; deletes one step's records and their
   sealed secrets in one batch; audited `core.storage.quarantine_purge`; health clears when empty).
8. Extend core's crash matrix over hello with M8's phases and the level step's sidecar and stamp writes; add the
   boot-writer rows. Headers, one `changelog.d/` fragment, Acceptance.

## Code contract (05-storage §5S.3.13; copy exactly)

```go
type QuarantineRecord struct {
	Key       []byte    `json:"key"`
	Value     []byte    `json:"value"`
	Secret    string    `json:"secret"`
	Reason    string    `json:"reason"`
	Step      string    `json:"step"`
	Sensitive bool      `json:"sensitive"`
	Build     string    `json:"build"`
	At        time.Time `json:"at"`
}

var (
	ErrImportConflict     = errors.New("storage: an imported record already exists with other content")
	ErrImportRejected     = errors.New("storage: the importer refused the record")
	ErrCutoverBlocked     = errors.New("storage: the cutover is blocked; see storage cutover-blockers")
	ErrRewriteSwappedBack = errors.New("storage: the store rewrite swapped back twice; restore the cutover checkpoint (storage restore-offline, make rollback-restore)")
)
```

If `ErrImportConflict` or `ErrImportRejected` already exist at BASE (F2-06), keep them where they are and add only
the missing two. `Build` holds the binary ID (`app.BinaryID`), never a UI build ID.

## Tests first

`TestDryRunMatchesLiveRun` (gate: counts from `--dry-run` equal a live run's, the quarantine report included; the live
directory's SST and MANIFEST file names and sizes unchanged; no ratchet ran), `TestQuarantineCommands` (gate: `list`
never shows a value; a sensitive row is stored only as a sealed secret record; `export` writes values, sensitive ones
decrypted, mode 0600; `purge --step --confirm` deletes one step's records and their sealed secrets, audited; health
clears when empty), `TestDryRunScratchModeAndSweep`, `TestRaiseWritesSidecarBeforeStamp`,
`TestRaiseCommitErrorRewritesSidecar`, `TestRaiseNeverLowersLevel` (extend if R1-20 has them),
`TestCutoverRewriteLeavesNoOldFile` (over hello: no file of the old directory remains, the generation is unchanged,
the attempt's checkpoint restores), `TestRewriteSwapBackMovesNoCheckpoint` (05-backup), the M8 cells of
`TestCutoverCrashAtEveryChunk` that do not need books (a kill at `swapped`, one swap-back, two consecutive swap-backs,
a second M8 ignoring the first's `applied` journal), `TestPreflight_RunsOnlyBeforeCutoverStarts` (name coined here: a
fake pending step whose `Preflight` returns a blocker refuses with exit 78 and writes nothing; the same store with a
`c/storage/dm/` progress record or a raised level skips the preflight).

## Invariants

- No quarantine record holds a raw value of a `Sensitive` row; no plaintext under `c/`.
- The level step commits before any cutover chunk; no step lowers a level.
- A preflight refusal writes nothing at all, so the pre-freeze release still opens the store.
- The dry run never writes the live directory or the state directory's keyring.
- M8 never loops: at most one restart after a swap-back.

## Acceptance

```bash
(cd core && go vet ./storage/... ./internal/restoreswap/... ./kernel/... ./app/...)               # exit 0
(cd core && go test -race -count=1 ./storage/... ./internal/restoreswap/... ./kernel/... ./app/...)   # ok
(cd core && go test -count=1 -run 'TestDryRunMatchesLiveRun|TestQuarantineCommands' ./...)          # PASS
(cd core && go test -count=1 -run 'TestBootCrashMatrix' ./...)                                      # PASS
go test -count=1 -run 'TestBootCrashMatrix_ABO' ./...                                                # PASS, unchanged
make -C core ci                                                                                      # exit 0
make ci                                                                                              # exit 0
```

## Out of scope

Books' M2, M7 and M8 definitions (F3-06); registering them and wiring the keyring sealer (F3-07); `storage
cutover-blockers` (F4-06); the archive extractor of core's own backup layout (F8-01).

## Rollback

Revert the PR; nothing is registered, so no store is touched.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(core/storage): level step, sealed quarantine, cutover preflight, KindRewrite, dry run (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Planner notes

- **Same-wave secrets.** Quarantine sealing and `quarantine export` need F3-03's keyring, which runs in the same wave;
  this brief uses a seam and fakes, and F3-07 wires the real sealer. Consider listing F3-03 as a dependency if the
  lead prefers real-keyring tests here.
- **Runner and journal files.** If F2-06 did not build `StepWriter.Quarantine` with sealing in the chunk batch, or
  R1-20 did not land `KindRewrite` and `StepID` in `core/storage/restorejournal/`, this row needs
  `core/storage/datamigrate/`, `core/internal/storagemigrate/` or `core/storage/restorejournal/` (STOP 1, STOP 2).
- **M8's invocation API** is not named by the spec; F3-04 coins it and F3-06 consumes it.
