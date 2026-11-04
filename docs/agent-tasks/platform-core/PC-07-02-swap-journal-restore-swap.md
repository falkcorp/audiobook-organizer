<!-- file: docs/agent-tasks/platform-core/PC-07-02-swap-journal-restore-swap.md -->
<!-- version: 1.0.0 -->
<!-- guid: b8c31666-ce07-4bda-8112-2aca435870c3 -->
<!-- last-edited: 2026-11-04 -->

# PC-07-02 — Swap journal and restore swap (dark)

**Full title:** Swap journal and restore swap (dark): `storage/restorejournal` and `core/internal/restoreswap`
with the P0a resume over ABO's staged intent.

| Field | Value |
|---|---|
| ID | PC-07-02 |
| Tier | **T4 (Opus, xhigh to max).** A durable multi-state machine over the live database directory with a failure exit at every state; one wrong step order loses the store or wedges every later boot behind `restore_inflight`. |
| Wave | W07 (stage S2b, release `S2br1`) |
| Depends on | OWNER-06 (current `S2r3` soak row; merge gate only) |
| Trailer | `Stage-Release: S2b 1` on every commit |
| Mode | Dark: `core/` files plus one ABO test-only fixture generator (Step 3); nothing runs the swapper until the kernel wiring (PC-07-03) and the flip (PC-07-05). |
| Normative digest | `docs/plans/platform-core/digests/S2b.md` sections 3, 5.1, 5.7, 5.8 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-07-02 -b feat/pc-07-02-restore-swap origin/main
cd "$REPO/../audiobook-organizer-pc-07-02"
git rebase origin/main
git worktree list
```

PC-07-01 and PC-07-02 are file-disjoint and may run at once. If PC-07-01 has merged, rebase on it and use
`storage`'s types; if not, this PR may depend only on the L0 leaves (`storage/keys`, the new
`storage/restorejournal`) plus the minimum of `storage` named in Step 4, and you then rebase after PC-07-01
merges.

## Goal

1. The L0 leaf `storage/restorejournal`: the kinds, records and intent of the **one** journaled swap
   (05-storage §5S.3.15 code block), with `CreateIntent`, the one atomic create of the one slot.
2. `core/internal/restoreswap` (L1; imports only `storage` and its L0 leaves; imported only by `kernel`):
   the P0a machine for the restore kinds (`KindArchive`, `KindRevert`, `KindCheckpoint`), following
   05-backup §5B.3.4 steps a, b, c, d, e, f, g and x and machine 1 of 05-storage §5S.3.16, including the
   resume of a journal that S1's ABO-local swap (PC-03-06) left in flight.

The reset kind's staging and set-aside are PC-07-07's (`core/internal/restoreswap/reset.go`); this PR leaves
the `KindReset` branch returning a clear "not implemented in this build" error that no caller reaches yet.

## Exact files

Create:

- `core/storage/restorejournal/restorejournal.go` — the leaf, exactly the 05-storage §5S.3.15 block.
- `core/storage/restorejournal/restorejournal_test.go`
- `core/internal/restoreswap/` — `swap.go` (P0a machine), `journal.go` (append-only journal file with fsync),
  `reject.go` (step a), `resume.go`, `restoreswap_test.go`, `testdata/s1-journals/` (fixtures, Step 3).

Create (ABO, test only): `internal/backup/s1_journal_fixtures_test.go` (name coined here; Step 3).

Modify: none. Plus one `changelog.d/` fragment.

## Re-verify before editing

```bash
# base: nested module (PC-01-02)
ls core/go.mod core/storage/keys/keys.go                      # both listed
# base: the S1 ABO-local swap whose journals you must resume (PC-03-06)
ls internal/backup/staged_restore.go internal/backup/restorejournal.go   # both listed
grep -n 'inflight.json\|pending.json' internal/backup/staged_restore.go internal/backup/restorejournal.go | head   # >= 2 hits
# base: nothing creates these yet (expect "No such file")
ls core/storage/restorejournal core/internal/restoreswap
# base: marker
grep -E '^(shipped_stage|release):' ci/stage.txt              # shipped_stage: S2r3; release: S2r3 or UI0
```

If `internal/backup/restorejournal.go` writes a JSON shape that differs from the 05-storage `Intent` and
`Record` fields, STOP and report the differing field names: the S1 swap must already follow 05-backup
§5B.3.4 exactly (09 S1, DATA10-8), and this PR must not paper over a mismatch.

## Code contract

Copy the 05-storage §5S.3.15 `package restorejournal` block **exactly**: `ErrSwapPending` (message
`"storage: a restore or factory reset is already staged or in flight"`), `Intent` (every field and JSON tag),
`Target`, `Row`, `CreateIntent(stateDir string, in Intent) error`, `Kind` (`KindArchive`, `KindRevert`,
`KindCheckpoint`, `KindReset`), `ResetKind` (`ResetStore = "store"`, `ResetFactory = "factory"`), `Record`
(every field), `SidecarIdentity`. The leaf imports only the standard library (03 R4: L0).

`CreateIntent`: temp file in `<stateDir>/restore/` + fsync, `link(2)` to `restore/pending.json` (`EEXIST` →
`ErrSwapPending`, temp removed), remove the temp, fsync the directory; also `ErrSwapPending` while any
`restore/<id>/inflight.json` or `inflight-checkpoint.json` exists.

`restoreswap` API: only `Swapper.StageReset` is named by the spec (PC-07-07 adds it). For the P0a run you
coin the names (mark them "(name coined here)" in the doc comment). It must give the kernel, without the
kernel reading any journal file itself (COR7-10):

- whether this boot is a restore boot, decided from the journal, never from `pending.json` alone;
- the identity recorded at step `sidecars` (the `.storage-format` `SidecarIdentity` and the highest
  `OPTIONS-<n>`), which the kernel passes to storage's open at P2;
- the recorded `db_path`, so P2 can verify it and call the swap-back on a mismatch;
- methods the kernel calls later in the same boot to journal `opened`, `marker_written` and `applied`
  (deleting the in-flight file at `applied`), and `swapped_back` through step x.

## Invariants

1. **One slot.** At most one non-terminal journal; a second `CreateIntent` of any kind gets `ErrSwapPending`.
2. **The in-flight file comes first.** `pending.json` is renamed to `restore/<id>/inflight.json`
   (`inflight-checkpoint.json` for `KindCheckpoint`) after journalling `rechecked` and **before** the first
   live-item rename (step c). Every state from which a live item may have moved holds the in-flight file.
3. **Step a's one rule** reads only `pending.json`: `expires_at` passed, `staged_by_build` ≠ this binary, or
   the live DB directory's highest `OPTIONS-<n>` ≠ `pebble_options` → `restore/<id>/rejected.json`, the live
   store untouched, health token `restore_rejected`.
4. **Every state has a failure exit.** A non-crash failure after the in-flight rename and before the first
   journalled move takes `rechecked --> swapped_back` (step x moves back whatever step c moved, renaming only
   what exists). Step c's state-file copies (`backup.location.json`, `storage.boot.json`) are
   copy-if-exists.
5. **Idempotent steps:** rename only if the source exists and the destination does not; a crash anywhere
   resumes forward to `applied` or into the swap-back.
6. A journal at `failed` (`KindCheckpoint`) is resumed only by a binary other than the one its `failed`
   record names.
7. `restoreswap` imports `storage` and the L0 leaves only, never the reverse; `storage.Admin` gets no swap
   method (archtest checks the edge, 03 R4).

## TDD: tests first (spec names)

- `TestS2bResumesS1Journal` — a journal left in flight by S1's ABO-local swap, at each of its non-terminal
  steps, is resumed by `restoreswap`'s P0a run to a terminal state of machine 1. The fixtures are produced
  by S1's code: write a small generator under `internal/backup` (test-only, `go test -run
  TestWriteS1JournalFixtures -update`) that stages and interrupts S1 swaps on a temp store and copies each
  `restore/` tree into `core/internal/restoreswap/testdata/s1-journals/<step>/`; commit the fixtures. Core
  reads them as plain files (core imports nothing from ABO).
- `TestInflightPrecedesFirstRename` — for each restore kind, a kill after the first live-item rename always
  finds the in-flight file and no `pending.json`. (The `KindReset` case and the `CheckReader` half with
  `ROLLBACK_IGNORE_RESTORE=1` are added by PC-07-07 and PC-07-03.)
- `TestSwapFailureExitBeforeFirstMove` — an error return (`ENOSPC`, `EACCES`) on each operation from the
  in-flight rename to the first journalled move ends at `swapped_back` with the live store in place.
- `TestRestoreCrashAtEveryJournalStep` (05-backup §5B.9) — each crash point resumes to applied, to
  swapped-back, or to untouched live data.
- `TestRestoreWithoutLocationFileS2b` — no `<state_dir>/backup.location.json`: step c copies only what exists
  and step x restores only the copies it saved.
- Leaf tests (names coined here): `TestCreateIntentExclusive` (two goroutines and two processes via
  `os/exec` of the test binary: one wins, the other `ErrSwapPending`; an existing `inflight.json` also
  blocks), `TestIntentJSONMatchesSpec` (marshal every field; tags as the block).

Inject faults through a filesystem seam inside `restoreswap` (an unexported interface over rename, link,
fsync and copy). PC-07-03 replaces it with the labelled `core/internal/statefs` seam the crash matrix
needs; keep the seam small so that swap is mechanical.

## Steps

1. Run the re-verify block; compare the S1 JSON with the spec block field by field.
2. Write the leaf and its tests; `go -C core test ./storage/restorejournal/` green.
3. Write the fixture generator in `internal/backup` (test file only) and commit the fixtures. This is the
   one ABO file this PR may add; it is a `_test.go` file and changes no behaviour.
4. Write the `restoreswap` tests, then the P0a machine: step a (reject), step b (re-check against
   `manifest_sha256` and sizes; journal `rechecked`; rename to in-flight), step c (move live items to
   `previous/`, copy-if-exists state files), step d (staged items into place), step e (rewrite
   `.storage-levels`, `.storage-applied`, books' `.storage-format` from the staged store's records, which
   it reads with storage's read-only open from PC-07-01; if PC-07-01 has not merged, read them through a
   small reader interface the kernel supplies and STOP before merge until PC-07-01 lands), step f (mark
   derived indexes for rebuild: record only; no index exists at S2b), step g (journal `swapped`), step x.
5. Resume: any journal whose in-flight file exists resumes from its last step.
6. Run the acceptance block.

## Acceptance

```bash
go -C core build ./...                                                    # exit 0
go -C core test -race -count=1 ./storage/restorejournal/ ./internal/restoreswap/   # ok, ok
go -C core test -count=1 -run 'TestS2bResumesS1Journal|TestInflightPrecedesFirstRename|TestSwapFailureExitBeforeFirstMove' ./internal/restoreswap/   # ok
go -C core list -deps ./internal/restoreswap | grep 'mediacore/' | sort   # only .../storage, .../storage/keys, .../storage/codec, .../storage/restorejournal, .../internal/pebble, .../internal/restoreswap
go -C core list -deps ./storage/restorejournal | grep 'mediacore/'        # only .../storage/restorejournal
make -C core ci                                                           # exit 0
make ci                                                                   # exit 0
```

`unit-lifecycle` triggers on `core/internal/restoreswap/**`; it must stay green (nothing runs the swapper yet).

## STOP conditions

- The S1 journal shape differs from the spec's `Intent` or `Record`.
- A step needs `restoreswap` to import anything above L1, or `storage` to import `restoreswap`.
- A destructive test would touch a directory the test did not create (fixtures only, CLAUDE.md and the
  workstream ground rules).

## Out of scope

`KindReset` staging and set-aside (PC-07-07); kernel wiring of P0a and the `marker_written` batch
(PC-07-03); the hooks of step h (S4 r2); ABO's restore staging switching to `restorejournal.CreateIntent`
(see Planner notes); `--print-restore-journal` (PC-07-03 wires the flag; the answer comes from here).

## Rollback

Revert the PR. Nothing runs the swapper; the fixture generator is a test.

## Repo rules

- Headers on every new file (Go: `// file:` etc.; fixtures under `testdata/` that are JSON carry none).
- Conventional commits with `Stage-Release: S2b 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin`. Required: `core-ci`, `extraction-gates`,
  `unit-lifecycle` (triggered by `core/internal/restoreswap/**`), `vet-darwin`, `vet-integration`.
- No library-scale loop is added; the swap renames a fixed target list sequentially by design (strict
  ordering is the correctness rule: CLAUDE.md's "do not naively add concurrency").

## Planner notes

- **Spec name for a "(new test)".** The row's "inflight.json exists before the first rename in every kind
  (new test)" is the spec's `TestInflightPrecedesFirstRename` (05-storage §5S.10); the brief uses it.
- **`TestS2bResumesS1Journal` is labelled "(ABO)" in 05-storage and 05-backup,** but this row is core-only.
  The brief runs it in core over committed fixtures produced by S1's ABO code; the ABO run through the real
  kernel P0a can only happen once ABO boots through the kernel, so PC-07-05 should add it as a scenario of
  `TestBootCrashMatrix_ABO`.
- **No row moves ABO's pre-S8 restore staging onto `restorejournal.CreateIntent`.** 05-storage §5S.3.15 says
  both writers (core's `restoreswap` and ABO's `internal/backup`) call the one create and share one type.
  `TestSwapSlotExclusive` (PC-07-07) needs it. The brief for PC-07-07 carries that ABO change; the planner
  may prefer a separate row.
- **One ABO test file added.** The row lists no ABO file; the brief adds `internal/backup/s1_journal_fixtures_test.go`
  so the S1 fixtures come from S1's real code rather than hand-written JSON.
- **The `restoreswap` P0a API is not named by the spec** beyond `Swapper.StageReset`; the brief lets the
  implementer coin names and requires them marked.
