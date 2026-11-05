<!-- file: docs/agent-tasks/platform-core/F4-11-revocation-journal-integrity-reapply.md -->
<!-- version: 1.0.2 -->
<!-- guid: 9ad8da00-7eab-4236-a8f3-a0c03568e674 -->
<!-- last-edited: 2026-11-05 -->

# F4-11 — Revocation journal integrity and boot re-apply, dark: the authenticated journal format behind a derive-key seam, `backup.location.json` and `c/backup/served_boot`, the P4d step 2 re-apply and the P4d step 3 restore-hook runner as kernel functions F4-08 calls, the `revocation_journal_*` health tokens

| Field | Value |
|---|---|
| ID | F4-11 |
| Release / flip | R2, identity flip F4 (dark PR; F4-08 calls both functions from boot) |
| Tier | **T4** (Opus, xhigh–max): an authenticated append-only journal and the boot classifier that keeps revocations revoked across restores; a mistake here silently re-activates revoked credentials (SEC-16) |
| Wave | W28 |
| Depends on | F4-04, F3-03, F4-02, F2-07 (each merged on `main` before you start) |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-11-journal-integrity
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Never run `go work init` (CLAUDE.md; 03 §3.2 item 1).

## Goal

F4-04 landed `backup/journal`'s types and a plain file-backed `Append`. This row makes the journal what 05-backup
§5B.3.6 says it is, and builds the boot-time half that reads it. Everything stays **dark**: nothing calls the two kernel
functions until F4-08 wires them into boot.

1. **Authenticated format** (05-backup §5B.3.6 rows "Where" and "Format and integrity"): the file
   `<backup dir>/<prefix>revocations.journal`, mode 0600, JSON lines. Line 0 is the header
   `{seq: 0, install_id, app, started_at, kid, base_seq, base_mac, mac}`. Every later line is a `journal.Revocation`
   plus `kid` and `mac`, with `seq` = previous + 1 and
   `mac = HMAC-SHA256(DeriveKeyFor(kid, "core.backup/revocations/v1"), prev_mac ‖ canonical line without mac)`.
   `backup/journal` must not import `secrets` (05-backup §5B.3.2 import list), so the keys come through a seam the
   kernel injects (below). Verification walks the chain and reports where it breaks.
2. **`backup.location.json` and `c/backup/served_boot`** (rows "Where", "Head in the store", "Content marker"): the
   one writer of `<state_dir>/backup.location.json` = `{backup_dir, journal, db_path, install_id, store: {epoch,
   dir_dev, dir_ino}, served_boot: {n, nonce}, head: {seq, mac}}` (paths absolute), with one mutex across every
   read-modify-write, temp file + fsync + rename; the committed-head anchor (rewritten after callers commit: at the next
   `Append`, on an in-process one-minute tick, at shutdown; never ahead of the store; frozen while a restore journal is
   between `swapped` and `applied`); the content marker `c/backup/served_boot` = `{n, nonce}` written with
   `GetForUpdate`, `n` = max(store's `n`, file's `n`) + 1, a fresh random nonce, the same pair then written into the
   file. A method that hands `Create`'s new pair to the component (F8-01 calls it; `Create` never writes the file).
3. **P4d step 2** (05-backup §5B.3.6 "Boot re-apply"; 06 §6.5.1 P4 (d) item 2; 05-storage §5S.4 step 11 (b)) as one
   kernel function in `core/kernel/restore_reapply.go`: the journal move when the effective directory differs (not on a
   restore boot before `applied`); **(0)** install check (rename aside to `<name>.other-install-<ts>`, new journal with a
   `gap` line, nothing re-applied, `revocation_journal_reset`); **(1)** `head_before`; **(2)** the one classifier (restore
   boot / replaced store by (a) older content marker, (b) head below the anchor, (c) other generation or directory
   identity **with** journal evidence / relocated / same store); **(3)** re-apply every verified line after
   `head_before` through auth's and rbac's stores, skipping hook lines of an aborted restore; **(4)** replaced store:
   hooks with `KindExternal`, `Previous` nil, `Revocations` nil, and a `restore` line `{kind: external, boundary:
   head_before}`; **(5)** bookkeeping lines in their own batches, then (except on a restore boot) `served_boot` and the
   file; chain break with `head_before` below the last revocation line fails closed through auth's restore fallback
   (every API key `needs_confirmation`, every credential epoch bumped) and reports `revocation_journal_broken`.
4. **P4d step 3** (06 §6.5.1 P4d 3; 05-backup §5B.3.2 `RestoreHook`, `RestoreContext`) as one kernel function in
   `core/kernel/restore_hooks.go`: a registry of restore hooks run in the order auth, rbac, audit, each with the same
   `RestoreContext` (`Boundary`, `Revocations` per row "Coverage at restore", `Previous` per its comment, `Counts`
   carried forward); after the hooks, journal `hooks_done`, one `marker_written` batch writing `c/backup/served_boot`,
   then `backup.location.json`, then journal `applied` and `inflight.json` removed. A `restore` line without its
   `c/backup/restore_hooked/<seq>` record is resumed by the next boot, never duplicated, unless a `restore_aborted` line
   names it or its restore journal reads `swapped_back`.
5. **Health tokens** (05-backup §5B.8 last paragraph; 05-health §5H.3.1): `revocation_journal_unwritable` (degraded),
   `revocation_journal_broken` (degraded), `revocation_journal_reset`, and `info` `revocation_journal_colocated` (the
   journal's filesystem device equals the store's). They appear only on authenticated endpoints (05-health §5H.3.1).
   This row computes them and returns them from the two functions and from the journal component; F4-08 registers them
   on the kernel-side health component (F2-10's registry).

## Spec

- 05-backup §5B.3.2 (the `journal` package block; `ServedBoot`, `Head`, `RestoreKind`, `RestoreContext`,
  `RestoreHook`, the restore-journal step names), §5B.3.6 (every row: Where, Format and integrity, Head in the store,
  Content marker, Writers, Restores without hooks, Append failure, Boot re-apply, Coverage at restore, Retention),
  §5B.5 (error rows for a broken chain, another install's journal, colocation), §5B.8 (tokens), §5B.9 (tests).
- 06 §6.5.1 P4 (d) items 2 and 3, the P4d rows of the boot-writers table, P0a (the restore journal decides "restore
  boot"; `restore_aborted`), §6.10 (`TestRevocationStagesJournalHead`, `TestJournalReapplyVerifiesWithKeyring`,
  `TestJournalReapplyUsesOverriddenBackupDir`, `TestRestoreBootWritesMarkersAfterHooks`).
- 05-storage §5S.4 step 11 (b), §5S.3.4 rule 4 (a restored marker-less store inherits the install's ID), §5S.10
  `TestPostDAGReconcilerOrder`; "Offline open" item 4 (offline revoking writes append; the offline open re-applies
  nothing).
- 05-config §2.3 (`Store.DeriveKey`, `ActiveDeriveID`, `DeriveKeyFor`, `ErrKeyUnavailable`).
- 05-authn §4.14 (auth's restore fallback and `needs_confirmation`), 05-rbac §7.1 (rbac's and audit's hooks), §12 (`TestAudit_RestoreCopiesForwardRecords`, `TestAudit_CopyForwardThenWriteNoOverwrite`).
- SEC-16 (05-backup §5B.3.6 opening sentence; §5B.3.7).

## Files

| Action | Path |
|---|---|
| modify / create | `core/backup/journal/` (format, chain verification, the location file and anchor, the content marker, the derive-key seam) |
| create | `core/kernel/restore_reapply.go` (P4d step 2) |
| create | `core/kernel/restore_hooks.go` (P4d step 3 and the hook registry) |
| create | `core/kernel/restore_reapply_test.go` |
| create | `core/rbac/restore_hook.go` (rbac's `journal.RestoreHook`: 05-rbac §7.1 "Restore hooks"; 05-backup §5B.3.6) |
| create | `core/audit/restore_hook.go` (audit's `journal.RestoreHook`: 05-rbac §7.1 "Restore hooks"; 05-backup §5B.3.6) |

## Code contract

Copied from 05-backup §5B.3.2 (already in `core/backup/journal` from F4-04; do not change these signatures):

```go
type Head struct {
	Seq uint64
	MAC string
}

type ServedBoot struct {
	N     uint64 `json:"n"`
	Nonce string `json:"nonce"`
}

type RevocationJournal interface {
	Append(ctx context.Context, b *storage.Batch, r Revocation) error
	Gap(b *storage.Batch, since time.Time) error
}

type RestoreHook func(ctx context.Context, rc RestoreContext) (counts map[string]int, err error)
```

The spec names no type for the seam, the two kernel functions or their results. Coin them (all names coined here),
for example:

```go
// package journal: the kernel adapts *secrets.Store to this; journal never imports secrets.
type Keys interface {
	ActiveDeriveID() string
	DeriveKeyFor(kid, info string) ([]byte, error) // ErrKeyUnavailable-style error for a retired or unknown kid
}

// package kernel (unexported): F4-08 calls these from boot at P4d steps 2 and 3.
func reapplyRevocations(ctx context.Context, in reapplyInput) (reapplyResult, error)
func runRestoreHooks(ctx context.Context, in restoreHooksInput) error
```

`reapplyResult` carries at least the classification (restore boot, replaced, relocated, same store), `head_before`,
the number of lines re-applied, the coverage (`Revocations` or nil) for step 3 and the health tokens raised. Keep the
inputs explicit (the journal component, the auth and rbac stores from P4c, `c/storage/install`, `c/storage/epoch`, the
restore journal records, the effective `core.backup.dir` after P4d step 1, the state directory), so tests drive them
without a booted App.

## Invariants

- I1. A revocation recorded in the journal after a store's head is never lost by a restore or a file copy: it is
  re-applied, or every API key falls to `needs_confirmation` and every epoch is bumped (SEC-16, SEC3-10).
- I2. Deleting, reordering, editing, truncating or forging a line breaks the chain; a journal of another install is
  never replayed (SEC2-9, DATA4-15).
- I3. Every comparison uses `head_before`, read before anything is appended this boot, and the seq of the last
  **revocation** line; `gap` and `restore` lines never count (SEC4-5).
- I4. Classification is by store identity and content, never by head lag alone; a relocated store runs no hook
  (DATA4-7, DATA5-7, DATA6-5). 05-backup §5B.3.6 (2) is the one classifier; add no other signal.
- I5. On a restore boot `served_boot` and `backup.location.json` are written only after the hooks commit, the anchor is
  frozen between `swapped` and `applied`, and a swap-back leaves the original store classified "same store" (DATA6-3,
  DATA7-5, R7-4).
- I6. `backup.location.json` has one writer in a running process, under one mutex; `Create` hands its pair to the
  component (DATA7-14). The file is compared only when its `db_path` equals the resolved `core.storage.path` and its
  `install_id` equals the store's marker (SEC6-8).
- I7. An `Append` failure never blocks the revocation: the batch records `c/backup/journal_gap/<since>` and health is
  `revocation_journal_unwritable` until the next successful append writes a closing `gap` line.
- I8. `backup/journal` imports only `entity`, `storage`, `storage/restorejournal` and the standard library (05-backup
  §5B.3.2); the derive key comes through `Keys`.
- I9. The journal file is 0600; every write is fsynced before the caller commits; the location file is written by temp
  file + fsync + rename + directory fsync.

## Re-verify at BASE

```bash
git grep -n 'type RevocationJournal interface' -- core/backup/journal | wc -l        # expect 1 (F4-04)
git grep -n 'type ServedBoot struct\|type RestoreContext struct' -- core/backup/journal | wc -l   # expect 2 (F4-04)
git grep -n 'func StorageSpec' -- core/backup/journal | wc -l                         # expect 1 (F4-04)
git grep -n 'func (s \*Store) DeriveKeyFor\|func (s \*Store) ActiveDeriveID' -- core/secrets | wc -l   # expect 2 (F3-03)
git grep -n 'func PostRestore' -- core/auth | wc -l                                   # expect >= 1 (F4-02)
ls core/rbac/restore_hook.go core/audit/restore_hook.go 2>/dev/null | wc -l           # expect 0 (this row creates both)
git grep -n 'reapplyRevocations\|runRestoreHooks' -- core/kernel | wc -l              # expect 0
git grep -n 'backup.location.json' -- core | wc -l                                    # expect 0 outside comments (this row writes the file)
(cd core && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./backup/journal | grep -c secrets)   # expect 0
```

If F4-02 has no `PostRestore`, STOP: the runner has nothing to call for auth. rbac's and audit's hook bodies
(05-rbac §7.1 "Restore hooks") are this row's: step 6 writes both.

## STOP conditions

1. `backup/journal` would need to import `secrets`, `config`, `auth` or `rbac`.
2. The classifier needs a signal 05-backup §5B.3.6 (2) does not list.
3. Anything here would run at boot before F4-08 wires it (this row edits no `boot.go`, `kernel.go` or `main.go`).
4. A test needs a booted ABO App (the `TestBootCrashMatrix_ABO` cells for these writers are F4-08's).
5. The authenticated format would change F4-04's `Append` or `Gap` signature.

## Steps

1. Write the tests below first, in `core/kernel/restore_reapply_test.go` and `core/backup/journal/*_test.go`.
2. `backup/journal`: `Keys`; header writing on first use (with `install_id`, `app`, `kid`); per-line `kid`/`mac`; a
   verifier that walks the chain (from the header, or from `base_seq`/`base_mac` after retention) and reports the first
   broken seq; file mode 0600; `Gap` writes the `gap` line when the next append succeeds.
3. The location file and anchor: one component that owns the file under one mutex, the one-minute tick (an in-process
   ticker, stopped at shutdown), the shutdown rewrite, the freeze while a restore journal is between `swapped` and
   `applied`, and the method that takes `Create`'s pair. The content-marker write with `GetForUpdate`.
4. `core/kernel/restore_reapply.go`: P4d step 2 in the order of Goal item 3, including the journal move (copy, verify the
   whole chain, fsync, rename, update the record; a failed verification keeps the old file and reports
   `revocation_journal_broken`), the `HooksOwed` case of row "Restores without hooks" (append the `restore` line with
   that boundary, re-apply after it, run the hooks with `Revocations` from it) and the colocation check.
5. `core/kernel/restore_hooks.go`: the hook registry (auth, rbac, audit, in that order), `RestoreContext` built per row
   "Coverage at restore", `Previous` per 05-backup §5B.3.2's comment (read-only native auth and rbac stores over
   `storage.Admin.OpenReadOnly` of the pre-swap directory; nil for `KindExternal` and host loss), `restore_hooked/<seq>`
   records, `hooks_done`, the `marker_written` batch, the location file, `applied`, `inflight.json` removed.
6. `core/rbac/restore_hook.go` and `core/audit/restore_hook.go`: each builds that component's `journal.RestoreHook`
   function (05-rbac §7.1 "Restore hooks"; 05-backup §5B.3.6): rbac re-applies `role.unassigned`, `role.narrowed` and `role.deleted` through `rbac.Store`
   (05-rbac §7.1), taking the most restrictive of the restored and pre-swap state when `rc.Previous` is set; audit's
   hook copies forward every pre-swap record above `rc.AuditHighWater`, calling `Sequence.AdvancePast` before the
   copy, then appends the journal and restore records (05-rbac §7.1 "audit's hook"). F2-07 left this body to this row.
   Both are dark: step 5's registry runs them, nothing wires them until F4-08.
7. Headers, one `changelog.d/` fragment, Acceptance.

## Tests first

Spec names copied exactly (05-backup §5B.9; 06 §6.10; 05-storage §5S.10). Each runs dark: tests call the two kernel
functions over a store and state directory built in the test.

- `TestPostRestoreReappliesRevocations` — a key revoked after the archive stays revoked; a journal gap → keys need
  confirmation.
- `TestJournalReapplyVerifiesWithKeyring` / `TestJournalReapplyUsesOverriddenBackupDir` — with `core.backup.dir` set
  only in the stored settings, P4d step 2 reads (or first moves) the journal from that directory and verifies every line
  with the keyring opened at P4b; the replaced-store case reads `head_before` before the gap line is appended.
- `TestRevocationJournal_TruncationDetected` — deleted, edited, reordered or truncated lines break coverage → keys need
  confirmation.
- `TestJournalOfOtherInstallNotReplayed` — after a factory reset or a deleted DB directory, the old install's journal is
  set aside, a new one starts with a `gap` line, nothing is re-applied.
- `TestRevocationStagesJournalHead` — with the identity flip and before the backup flip, a key revoke commits with
  `c/backup/journal_head` staged through `GetForUpdate`, and an append failure commits with its `journal_gap/` record.
  The kernel half of this row (registering `journal.StorageSpec()` and declaring `core.backup.dir`) is F4-08's wiring;
  here the test drives the journal component directly.
- `TestOfflineRevocationJournaled` — an offline revoking write (for example `users reset-password` bumping an epoch)
  appends as the serving path does, with the head staged; the offline open re-applies nothing, runs no hook and appends
  no `restore` line.
- `TestReplacedStoreRunsHooks` — an older archive's DB directory copied into place is detected by its content marker or
  its store generation with journal evidence, also when its head equals the journal's end: tail re-applied, hooks run
  once with `KindExternal` and `Revocations` nil, keys need confirmation.
- `TestRestoreBootWritesMarkersAfterHooks` — a staged restore with an intact journal appends no `gap` line and needs no
  key confirmation; `served_boot` and `backup.location.json` are written only after step 3's hooks commit; a failing
  hook that swaps back leaves the original store "same store" at the next boot; the committed-head anchor equals the
  original store's head after the swap-back; a boot failing at P6 after `applied` is read as the same store.
- `TestPostDAGReconcilerOrder` — reconcilers run in 06's order after the DAG; the journal re-apply sees the overrides
  and the keyring (this row's cell: the re-apply function refuses to run without the attached overrides and the open
  keyring; F4-08 extends it over the real boot).
- `TestCommittedHeadAnchorDetectsInPlaceCopy` — a DB directory copied file by file over the live one (same directory
  identity, same marker) after a later revocation reads replaced because `head_before` is below the anchor; the anchor
  never leads the store (a revocation whose commit failed does not move it).
- `TestBootReapply_ChainBreakFailsClosed` (05-backup §5B.9; this row owns it) — a broken chain
  with the store's head below the journal's end sets every key `needs_confirmation` and bumps every epoch.
- `TestJournalReapplyWithUISetBackupDir` (05-backup §5B.9, the third name of the row above; this row owns it) — the
  same with the value written to the stored settings layer, as the Settings write path stores it (no route and no
  booted App: F4-05's routes land later).
- `TestAudit_RestoreCopiesForwardRecords` (05-rbac §12) — records above the manifest's audit high-water mark survive a
  restore (SEC-17), driven through `core/audit/restore_hook.go` with a pre-swap store built in the test.
- `TestAudit_CopyForwardThenWriteNoOverwrite` (05-rbac §12) — after a restore with copied records, the next records
  get fresh `seq`s and no copied record or index row changes (SEC2-12).
- `TestJournalChainSurvivesKidRotation` (name coined here) — lines written under `d1` then `d2` verify as one chain;
  a retired `kid` named by a retained line makes verification report the line, not panic.
- `TestLocationFileModeAndAtomicity` (name coined here) — the journal is 0600; a crash between the temp file and the
  rename leaves the previous `backup.location.json` intact.

Not this row's (F4-08 owns them): `TestBootPhaseOrder_NoWriteBeforeCheckpoint`, `TestRestoreHooksWriteNativeRows`,
`TestCrashBetweenSwapAndHooksResumesHooks`, and the `TestBootCrashMatrix_ABO` cells for the new boot writers. F8-01 owns
`TestLocationFileWritersSerialized` and `TestJournalNamePerApp` (it calls this row's method for `Create`'s pair).

## Acceptance

```bash
(cd core && go vet ./backup/... ./kernel/... ./rbac/... ./audit/...)                              # exit 0
(cd core && go test -race -count=1 ./backup/journal/... ./kernel/... ./rbac/... ./audit/...)      # ok
(cd core && go test -count=1 -run 'TestPostRestoreReappliesRevocations|TestJournalReapplyVerifiesWithKeyring|TestJournalReapplyUsesOverriddenBackupDir|TestRevocationJournal_TruncationDetected|TestJournalOfOtherInstallNotReplayed|TestRevocationStagesJournalHead|TestOfflineRevocationJournaled|TestReplacedStoreRunsHooks|TestRestoreBootWritesMarkersAfterHooks|TestPostDAGReconcilerOrder|TestCommittedHeadAnchorDetectsInPlaceCopy|TestBootReapply_ChainBreakFailsClosed|TestJournalReapplyWithUISetBackupDir|TestJournalChainSurvivesKidRotation|TestLocationFileModeAndAtomicity|TestAudit_RestoreCopiesForwardRecords|TestAudit_CopyForwardThenWriteNoOverwrite' -v ./backup/journal/... ./kernel/... ./rbac/... ./audit/... | grep -c '^--- PASS')   # expect 17
(cd core && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./backup/journal | sort)
#   expected: only github.com/falkcorp/mediacore/{backup/journal,entity,storage,storage/restorejournal} and what storage imports
git grep -n 'reapplyRevocations\|runRestoreHooks' -- core/kernel/boot.go core/kernel/kernel.go | wc -l   # expect 0 (dark: F4-08 wires them)
make -C core ci                                                                                   # exit 0
make ci                                                                                           # exit 0
```

## Out of scope

Wiring the functions into boot, registering `journal.StorageSpec()` and the health component, declaring
`core.backup.dir`, and the crash-matrix cells (F4-08). Archive creation and `Create`'s own `served_boot` and
`JournalSeq` writes, the restore staging and routes (F8-01). Auth's hook body, `PostRestore` (F4-02). Retention
pruning of the journal (05-backup §5B.3.6 "Retention", with backup's catalogue, F8-01).

## Rollback

Revert the PR; nothing calls the new code until F4-08.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer (09 §9.2.1); `gh pr merge <n> --rebase`,
never squash, never `--admin`. Concurrency (CLAUDE.md): the re-apply walks the journal tail in batches; it is ordered
by seq and must stay sequential (each batch writes the new head), so do not parallelise it, and say so in a comment.

```
feat(core/backup): authenticated revocation journal and the boot re-apply, dark

Co-Authored-By: Claude <noreply@anthropic.com>
```
