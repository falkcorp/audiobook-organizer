<!-- file: docs/plans/storage-efficiency/TASK-A4.md -->
<!-- version: 1.2.0 -->
<!-- guid: e67a43a5-ec7c-49e3-a081-4ebc3c9bdab7 -->
<!-- last-edited: 2026-10-03 -->

# TASK-A4: `storage_format` stamp, open/init split, pinned Pebble format, `make rollback` guard

Wave W1. Start only after PR #3704 has merged to `main` (#3698 is already
merged); both edit `internal/database/pebble_store.go`. Runs in parallel with
A1, A2, A5 and A6. Model: opus. Reviewers: code-reviewer and
silent-failure-hunter.

## 1. Goal and why

**Goal.**
- Split `newPebbleStore` into an **open** phase (`pebble.Open` plus the
  storage-format checks; it writes nothing) and an **init** phase (markers,
  import-path migration, counters, stamp, sidecar, memdb warmup).
- Write an integer `storage_format` stamp into the main Pebble store and
  mirror it in a sidecar file beside the store. Every open refuses a store
  whose stamp or sidecar is above what the build supports
  (`StorageFormatTooNewError`).
- Every open also refuses a store whose stamp is below the build's, or whose
  `storage_migration` marker is present, with a message that contains
  "start serve to migrate". Only an unexported cut-over open mode, used by
  `RunCutover` in release B (B6a), skips that refusal. In release A no real
  store can be older than the build; the refusal is wired and tested now so
  B6a only adds the caller.
- Decide "empty at open" in the open phase, before any write: an empty store
  is stamped current; a store with data and no stamp reads as format 1.
- Replace `pebble.FormatNewest` with one shared constant,
  `PebbleFormatMajorVersion`, at the main store open and at the raw
  diagnostics open. (A7 applies the same constant to the AI-scan and
  OpenLibrary opens, because A1 and A3 edit those files first.)
- Each build prints the format it supports when run with
  `--print-storage-format`.
- `make rollback` follows one rule: it swaps in `.prev` only when the
  store's stamp is not above the format `.prev` supports. Otherwise it
  refuses, swaps nothing, and prints the restore-from-checkpoint steps,
  naming the `checkpoint_dir` the cut-over recorded.
- The stamp starts at 1, which means today's format. This PR changes no data.

**Why.** Releases B and C convert book history and file rows in place, with no
backward compatibility (design P3). `make rollback` today swaps the binary
with no data check (`Makefile:725-735`). An older binary started on converted
file rows would read every file as fingerprint-less, and its first scan would
erase the fingerprint index (design 9, `pebble_store_bookfiles.go:840`,
`pebble_store.go:5507`; these two are the design's read-only evidence cites
from `d1f069fac`, nothing in this task edits them, and `pebble_store.go:5507`
has since moved to about `:5512`). The guard is what makes "the backup is the only way
back" true. It must ship and be deployed before any format change.

`pebble.Open` ratchets the on-disk format up to whatever the options ask for
and never back (pebble v2.1.7 `open.go:561-562`). With `FormatNewest`, a
Dependabot bump of pebble would silently raise the format and make the
cut-over's own checkpoint unopenable by the previous build. A pinned
constant stops that; raising it is a release of its own.

Two recovery tools are exempt from the guard by name (design 9): the raw
mode of `cmd/diagnostics` (`diagnostics query --raw`, read-only) and
`cmd/pebble-inject-skip` (writes two `setting:` keys outside every family a
cut-over changes; it passes no format version, which pebble resolves to
`FormatMinSupported`, `options.go:1495-1496`, so it can never ratchet the
store). They must keep working on a store the guard refuses.

## 2. ⛔ START HERE (run this first)

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
gh pr view 3704 --json state -q .state   # must print MERGED; if not, stop
git worktree add ../aorg-storage-a4-format-stamp -b feat/storage-a4-format-stamp origin/main
cd ../aorg-storage-a4-format-stamp
npm ci --prefix web
```

- Do NOT run `go work init`.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch.
- Next, run the "already done if" check in `## Idempotency / Rollback` below.
  If it says the work exists, stop and report instead of redoing it.

## Idempotency / Rollback

Already done if both of these hold (worktree root):

```bash
test -f internal/database/storage_format.go && echo "format file present"
grep -c 'print-storage-format' main.go     # the flag is defined in main.go; prints 1 or more when done
```

Both present: stop and report "already done". Only one present: a previous
attempt stopped half way; finish the missing steps instead of starting over.

Rollback: before the first push, remove the worktree and branch, or
`git reset --hard origin/main` inside it. After the PR merges, `git revert`
it. The `storage_format` stamp and the sidecar file are ignored by the
previous build (it never reads `preference:storage_format` or the
`.storage-format` file), so a binary swap back is safe; the pinned Pebble
format equals `FormatNewest` on v2.1.7, so no on-disk format changes either way.

## 3. Read before editing

Line numbers in this section come from `origin/main` at `373ba19d2`. Section 4
re-greps each one; where a number differs, use the grep result.

- `internal/database/pebble_store.go:379-516`. `NewPebbleStore` (`:379`),
  `NewPebbleStoreInMemory` (`:409`), and `newPebbleStore` (`:415-516`): open
  (`:416-424`), the undecodable-marker load (`:433`), the import-path
  migration (`:440`), the `counter:` init loop (`:446-460`), and the warmup
  block (`:462-514`).
- `internal/database/pebble_store.go:5174-5245`. `Reset()` wipes every key
  and re-creates the counters. It wipes the stamp too.
- `internal/database/store.go:1454-1477`. `InitializeStore` (`:1454`):
  `NewPebbleStore` (`:1463`) then `RunMigrations` (`:1474`). Every CLI entry
  point reaches the store through it.
- `internal/database/migration_bookkeeping.go:1-125`. `db_version` is a
  preference (`dbVersionPreferenceKey = "db_version"`, `:26`). The value is a
  JSON `DatabaseVersion{version, updated_at}` (`migrations.go:94`) inside a
  `UserPreference` row at key `preference:db_version`.
- `internal/database/pebble_store_preferences.go:16-100`. The preference
  get, set and list functions. `ListUserPreferences` (`:80-99`) iterates
  `preference:` and decodes each value as `UserPreference`. A raw,
  non-`UserPreference` value under `preference:` would be silently skipped
  there, so the stamp must be a full `UserPreference`.
- `main.go:21-50`. `run()`, and the child-mode sentinel that is checked
  before cobra parses arguments.
- `cmd/root.go:396-410` and `:525-551`. `PersistentPreRun` runs `initConfig`,
  which prints `Using config file: ...` to stdout (`:550`). A cobra flag
  would therefore put noise on stdout, which is why the flag is handled in
  `main.go`.
- `cmd/diagnostics.go:161-240`. `runDiagnosticsQuery` and
  `runRawPebbleQuery` (raw open at `:208-210`).
- `cmd/pebble-inject-skip/main.go:1-63`. Read only; do not edit.
- `internal/testutil/integration.go:40-62`. `SetupIntegration` opens a store
  with `database.NewPebbleStore`.
- `Makefile:725-736`, the `rollback` target. `Makefile:45-51`, the deploy
  variables. `Makefile.local.example:55-60` (deploy variables) and `:82`
  (the deploy copies the running binary to `.prev` on every deploy).
- `docs/system/deploy-and-gpu-ops.md:40-75` (the rollback flow) and
  `docs/system/runbooks.md:110-127` ("Backup and Restore").
- Design `docs/design/2026-10-03-storage-efficiency-design.md`, section 9:
  "Guard, shipped in release A", the restore paragraph, and step 2b (the
  marker and the `.migration-checkpoint` file release B writes).

## 4. Re-verify anchors

Line numbers were re-run on `origin/main` at `373ba19d2` (they had drifted
from `543827ef7` after #3704 grew `pebble_store.go`). If a number moved, use
the grep result, not the number.

1. `grep -n 'opts := &pebble.Options{FormatMajorVersion: pebble.FormatNewest}\|db, err := pebble.Open(path, opts)\|counters := \[\]string{\|store.ensureUndecodableMarkersLoaded()' internal/database/pebble_store.go`
   → `416`, `420`, `433`, `446`, and `5192` (inside `Reset`).
2. `grep -n 'const dbVersionPreferenceKey = "db_version"\|^func databaseVersionPayload\|^func (p \*PebbleStore) setPreferencesAtomic' internal/database/migration_bookkeeping.go` → `26`, `56`, `117`.
3. `grep -n 'type DatabaseVersion struct' internal/database/migrations.go` → `94:`
4. `grep -n 'LowerBound: \[\]byte("preference:")' internal/database/pebble_store_preferences.go` → `84:`
5. `grep -n '^rollback:\|DEPLOY_BIN).prev' Makefile` → `727`, `731`, `733`.
6. `grep -n 'if registry.IsChildMode()' main.go` → `37:`
7. `grep -n 'fmt.Println("Using config file:"' cmd/root.go` → `550:`
8. `grep -n 'func (p \*PebbleStore) Reset' internal/database/pebble_store.go` → `5174:`
9. `grep -n 's, err = NewPebbleStore(path)' internal/database/store.go` → `1463:`
10. `grep -rn 'pebble.Open(' --include='*.go' internal cmd | grep -v _test` →
    - `internal/database/pebble_store.go:420`: the main store. This task.
    - `cmd/diagnostics.go:208`: raw mode. This task pins its format and
      leaves it exempt from the stamp guard.
    - `cmd/pebble-inject-skip/main.go:34`: exempt; no edit.
    - `internal/database/ai_scan_store.go:108` and
      `internal/openlibrary/store.go:33`: A7 pins these. Do not edit.
11. `P=$(go env GOMODCACHE)/github.com/cockroachdb/pebble/v2@v2.1.7; grep -n 'FormatNewest FormatMajorVersion = iota - 1\|^	FormatValueSeparation$' $P/format_major_version.go`
    → `235` (`FormatValueSeparation`) and `240` (`FormatNewest`, the value
    right after it). So `FormatNewest == FormatValueSeparation` in v2.1.7 and
    pinning changes nothing on disk today.
12. `grep -n 'if o.FormatMajorVersion == FormatDefault' $P/options.go` →
    `1495:` (resolved to `FormatMinSupported` on the next line).
13. `grep -n '^	Create(\|^	Rename(' $P/vfs/vfs.go`
    → `97: Create(name string, category DiskWriteCategory) (File, error)`,
    `122: Rename(oldname, newname string) error`. `vfs.WriteCategoryUnspecified`
    exists.
14. `grep -nF 'sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).prev' Makefile.local.example` → `82`, `111`.
    (`-F` because the pattern holds `$(`, which is special in a basic
    regular expression.)
15. `grep -n 'Environment="DATABASE_PATH' deploy/audiobook-organizer.service`
    → `70:`. This is the default path only; prod overrides it. Never
    hard-code it.
16. `grep -n '^## Backup and Restore\|^### Restore\|^## memdb Warmup Recovery' docs/system/runbooks.md`
    → `110`, `121`, `128`. Step 10 inserts `## Storage Format Restore` after
    the restore subsection, before line `128`.
17. `grep -n '^## 2. Rollback flow' docs/system/deploy-and-gpu-ops.md`
    → `36:` (the rollback flow step 10 edits; the example `make rollback`
    command is at `:54`).
18. `grep -n 'DEPLOY_BIN *?=\|DEPLOY_HOST *?=' Makefile` → `46`, `47` (step 7
    adds `DEPLOY_DB ?=` after line `47`).
19. `grep -n 'func initConfig' cmd/root.go` → `525:` (section 3's reason the
    flag is handled in `main.go`; the `Using config file:` print is anchor 7).
20. `grep -n 'func NewPebbleStore\|func NewPebbleStoreInMemory\|func newPebbleStore' internal/database/pebble_store.go`
    → `379`, `409`, `415` (the three functions steps 2-3 split and rewire).
21. `grep -n '^DEPLOY_BIN :=\|^DEPLOY_URL :=' Makefile.local.example`
    → `57`, `60` (step 9 adds `DEPLOY_DB ?=` after `57`).
22. `head -3 Makefile` → `# file: Makefile`, `# version: 2.31.1`, a `# guid:` line.
    The Makefile carries a version header, so the version bump in section 9 applies; if the
    second line no longer starts with `# version:`, skip the bump.
## 5. Steps

1. **`internal/database/storage_format.go` (new).**
   - `const SupportedStorageFormat = 1`. Doc comment: "1 = the format before
     the storage-efficiency program (book_ver full copies, inline file
     signals). Raise it only in the release whose converter changes the
     format. The converter raises the stamp, and the sidecar, to the target
     value before its first legacy delete, not at the end (design 9, step
     2b), so a half-converted store is already too new for the previous
     build."
   - `var buildStorageFormat = SupportedStorageFormat`. Unexported; every
     comparison uses it, so a test can pretend the build supports 2.
   - `const legacyStorageFormat = 1`: the format of a store that has data and
     no stamp.
   - `const PebbleFormatMajorVersion = pebble.FormatValueSeparation`. Doc
     comment: every Pebble open passes this, never `FormatNewest`; `Open`
     ratchets the on-disk format up to the requested version and never back;
     raising this constant is a release of its own, listed as a format change
     in its rollback notes.
   - `const storageFormatPreferenceKey = "storage_format"` and
     `const storageMigrationPreferenceKey = "storage_migration"` (the marker
     B6a writes; this task only checks whether `preference:storage_migration`
     exists).
   - `const StorageFormatSidecarSuffix = ".storage-format"`,
     `func StorageFormatSidecarPath(dbPath string) string { return filepath.Clean(dbPath) + StorageFormatSidecarSuffix }`.
     The sidecar is a sibling of the DB directory, for example
     `/data/audiobooks.pebble.storage-format`, holding one line with one
     integer. It is not inside the DB directory, which Pebble owns.
   - `const StorageMigrationCheckpointSuffix = ".migration-checkpoint"` and
     `func StorageMigrationCheckpointPath(dbPath string) string`. Release B
     writes the cut-over's `checkpoint_dir` there (design 9, step 2b). This
     task only defines the path; `make rollback` reads it.
   - `type StorageFormatTooNewError struct { Path, Source string; Stamp, Supported int }`
     (`Source` is `"stamp"` or `"sidecar"`), with `Error()` returning exactly:
     `storage format <Stamp> (<Source>) in <Path> is newer than this build supports (<Supported>); refusing to open. Restore the pre-migration checkpoint and the binary that match this store: docs/system/runbooks.md#storage-format-restore`.
   - `type StorageMigrationRequiredError struct { Path string; Stamp, Supported int; MarkerPresent bool }`
     with `Error()` returning
     `storage format <Stamp> in <Path> needs migration to <Supported>` (plus
     `; a storage migration is in progress` when `MarkerPresent`) and ending
     `; start serve to migrate`.
   - `func readStorageFormatStamp(db *pebble.DB) (stamp int, present bool, err error)`.
     `db.Get([]byte("preference:storage_format"))`; `pebble.ErrNotFound`
     → `(0, false, nil)`. Decode `UserPreference`, then decode `*Value` as
     `DatabaseVersion` and return `.Version`. A decode failure is an error.
     Never treat it as absent: fail closed.
   - `func storageMigrationMarkerPresent(db *pebble.DB) (bool, error)`.
   - `func storeIsEmpty(db *pebble.DB) (bool, error)`: one iterator with no
     bounds, `First()`; empty when it returns false and `iter.Error()` is
     nil.
   - `func readStorageFormatSidecar(fs vfs.FS, dbPath string) (stamp int, present bool, err error)`.
     Missing file → `(0, false, nil)`. Content that is not one integer is
     returned as an error; the caller logs it at `slog.Error` and goes on
     (init rewrites the sidecar). A sidecar that parses is authoritative for
     the "too new" check.
   - `func writeStorageFormatSidecar(fs vfs.FS, dbPath string, stamp int) error`.
     Write `fmt.Sprintf("%d\n", stamp)` to `<sidecar>.tmp` with
     `fs.Create(name, vfs.WriteCategoryUnspecified)`, then `Sync`, `Close`,
     `fs.Rename` to the sidecar path. Using the store's own `vfs.FS` means
     in-memory tests never touch the real disk.
   - `func (p *PebbleStore) ensureStorageFormatStamp(emptyAtOpen bool) (int, error)`.
     Stamp present → return it. Absent and `emptyAtOpen` → write
     `buildStorageFormat`. Absent and not empty → write `legacyStorageFormat`
     (the open phase already refused this case unless legacy equals the
     build's format). Write with
     `p.setPreferencesAtomic([]preferenceWrite{{Key: storageFormatPreferenceKey, Value: <databaseVersionPayload(n)>}})`
     (`migration_bookkeeping.go:56`, `:117`). Reuse `databaseVersionPayload`;
     do not copy it.
2. **Open phase (`pebble_store.go`).** Add
   `type pebbleOpenMode int` with `openForServe` (the default) and
   `openForCutover` (unexported; B6a's `RunCutover` will be its only
   production caller), and
   `func openPebbleChecked(path string, fs vfs.FS, mode pebbleOpenMode) (*pebble.DB, storageFormatState, error)`:
   - `opts := &pebble.Options{FormatMajorVersion: PebbleFormatMajorVersion}`,
     the test `FS` override, `pebble.Open`.
   - Read the stamp, the marker and the sidecar; compute `emptyAtOpen`. On
     any read error: close and return it.
   - Effective stamp: the stamp if present; else `legacyStorageFormat` if the
     store has data; else `buildStorageFormat` (empty).
   - Stamp or parsed sidecar above `buildStorageFormat` → close, return
     `*StorageFormatTooNewError` (`Source` says which). This applies in both
     modes.
   - `mode == openForServe` and (effective stamp below `buildStorageFormat`
     or marker present) → close, return `*StorageMigrationRequiredError`.
   - Nothing in this function writes to the store.
3. **Init phase.** Move the rest of `newPebbleStore` (`:426-514`, everything after the `pebble.Open` error check) into
   `func initPebbleStore(db *pebble.DB, path string, fs vfs.FS, st storageFormatState) (*PebbleStore, error)`,
   unchanged except: after the counter loop and BEFORE the warmup block,
   call `p.ensureStorageFormatStamp(st.emptyAtOpen)` (close and return on
   error), then write the sidecar with the store's fs (`fs` when non-nil,
   else `vfs.Default`). A sidecar write failure is logged at `slog.Error`
   with the path and the error, and the open continues: the app must not
   refuse to serve over a sidecar, and the Makefile guard refuses a rollback
   when the sidecar is missing, so the failure stays visible. Log once:
   `slog.Info("storage format", "stamp", stamp, "supported", SupportedStorageFormat, "sidecar", sidecarPath)`.
   `newPebbleStore(path, fs)` becomes `openPebbleChecked(path, fs, openForServe)`
   followed by `initPebbleStore`. Keep the comment block above the warmup.
4. **`Reset()` (`pebble_store.go:5174`).** After the batch commit, call
   `p.ensureStorageFormatStamp(true)` and return its error. The wipe deleted
   the stamp, and a reset store is empty.
5. **`cmd/diagnostics.go` raw mode.** In `runRawPebbleQuery` (`:208-210`),
   replace `pebble.FormatNewest` with `database.PebbleFormatMajorVersion`.
   Add a comment: raw mode is exempt from the storage-format guard by design
   (design 9): it only reads, and it must work on a store the guard refuses.
   Change nothing else in the file.
6. **`main.go`.** In `run()`, after the three `Set*` calls and BEFORE the
   child-mode check:
   ```go
   if len(os.Args) == 2 && os.Args[1] == "--print-storage-format" {
       fmt.Println(database.SupportedStorageFormat)
       return 0
   }
   ```
   This copies the `registry.IsChildMode()` pattern of reading `os.Args`
   before cobra parses. Stdout then carries exactly one line, `1`. Add the
   import `github.com/falkcorp/audiobook-organizer/internal/database`.
7. **`Makefile`.**
   - Add `DEPLOY_DB   ?=` next to `DEPLOY_BIN` (`:47`), with a comment: "path
     of the main Pebble store on DEPLOY_HOST (the --db / DATABASE_PATH
     value); required by rollback".
   - Add `@[ -n "$(DEPLOY_DB)" ] || (echo "ERROR: DEPLOY_DB is not set ..."; exit 1)`
     next to the other two checks.
   - In `rollback`, after the `.prev` existence check (`:731`) and before the
     copy (`:732`), run
     `python3 scripts/storage_format_guard.py --db "$(DEPLOY_DB)" --bin "$(DEPLOY_BIN)" --prev "<prev output>" --sidecar "<sidecar content>" --checkpoint "<checkpoint file content>"`
     and stop on a non-zero exit. Collect the three strings with:
     - `ssh $(DEPLOY_HOST) '$(DEPLOY_BIN).prev --print-storage-format 2>/dev/null'`
       (stdout only; ignore the exit code);
     - `ssh $(DEPLOY_HOST) 'cat $(DEPLOY_DB).storage-format 2>/dev/null'`;
     - `ssh $(DEPLOY_HOST) 'cat $(DEPLOY_DB).migration-checkpoint 2>/dev/null'`.
   - Pass `ROLLBACK_IGNORE_FORMAT` through as an environment variable.
8. **`scripts/storage_format_guard.py` (new).** Python, not shell: it
   prints multi-line restore steps and has seven cases (repo rule 4: shell
   only for simple ops under 20 lines). `#` header lines (`# file:`,
   `# version:`, `# guid:`, `# last-edited:`) after the shebang. Standard
   library only.
   - Strip whitespace from every input.
   - `--prev` empty or not all digits → `prev = 1`. Every build before this
     one rejects the unknown flag, and every one of them reads format 1.
     Without this rule, the first rollback after A4 ships would be refused
     for nothing.
   - `--sidecar` empty or not all digits: with `ROLLBACK_IGNORE_FORMAT=1`,
     print a warning and exit 0; otherwise print
     `REFUSING: cannot read the store's storage format from <db>.storage-format. Set ROLLBACK_IGNORE_FORMAT=1 only if you have verified the store is at format 1.`
     and exit 1.
   - `store > prev`: print
     `REFUSING: the previous binary supports storage format <prev>, the store is at <store>. A binary swap cannot go back past a format change; nothing was swapped.`
     then the restore-from-checkpoint steps, and exit 1.
     `ROLLBACK_IGNORE_FORMAT` never overrides this case. The steps:
     1. `sudo systemctl stop audiobook-organizer.service`;
     2. move `<db>` aside and put the checkpoint in its place:
        `<checkpoint_dir>/<basename of db>`, where `checkpoint_dir` is the
        `--checkpoint` value. If it is empty, print
        `checkpoint_dir: none recorded in <db>.migration-checkpoint. Do not pick the newest .migration-backups/ entry; read the storage_migration report.`
        instead of a path;
     3. write the restored store's format into `<db>.storage-format` (the
        value `<bin>.pre-format-<store> --print-storage-format` prints);
     4. install `<bin>.pre-format-<store>` as `<bin>` (never `.prev`). The
        guard prints this step even when no such file exists: A9's
        `scripts/deploy-cutover.sh` is what creates it, and A9 merges after
        this task. Alongside the step the guard prints
        `note: <bin>.pre-format-<store> is saved by scripts/deploy-cutover.sh at deploy time; if it is absent, build the release that matches format <store> and install that binary`;
     5. start, and check the `storage format` log line;
     6. `See docs/system/runbooks.md#storage-format-restore`.
   - Otherwise exit 0 (the Makefile then swaps in `.prev` as today).
   - Case list for the unit tests: (1) prev 1, store 1; (2) prev empty;
     (3) prev is an error string; (4) store above prev, with a checkpoint;
     (5) store above prev, `ROLLBACK_IGNORE_FORMAT=1`, no checkpoint;
     (6) sidecar unreadable; (7) sidecar unreadable with
     `ROLLBACK_IGNORE_FORMAT=1`. Case 4 also asserts the restore steps contain
     the `.pre-format-<store>` step and the "saved by scripts/deploy-cutover.sh"
     note even though no such file exists in the test.
9. **`Makefile.local.example`.** Add a `DEPLOY_DB ?=` example line with a
   placeholder path, plus the same comment as step 7. Bump its header.
10. **Docs.**
    - `docs/system/runbooks.md`: add `## Storage Format Restore` (anchor
      `storage-format-restore`) after "Backup and Restore". It explains:
      - what the stamp and the sidecar are, and that `make rollback` swaps
        only when the store's stamp is not above `.prev`'s format;
      - the restore steps from step 8, with the checkpoint named by
        `<db>.migration-checkpoint` (written by the release B and C
        cut-overs), never "the newest `.migration-backups/` entry", and the
        binary `<bin>.pre-format-<N>` saved by `scripts/deploy-cutover.sh`
        (A9), never `.prev`;
      - that a restore after the app has served discards every write since
        the cut-over and needs the owner's approval (design 11, Q6);
      - that nothing short of the checkpoint restores a converted store.
    - `docs/system/deploy-and-gpu-ops.md`: in the rollback flow, add
      `DEPLOY_DB` to the example command and one step describing the guard
      and `ROLLBACK_IGNORE_FORMAT`.

## 6. Do not touch

- `internal/database/migrations.go` and `migration_bookkeeping.go`, except to
  READ `databaseVersionPayload` and `setPreferencesAtomic`. Do not change
  `db_version` handling.
- The store options in `newPebbleStore` other than the format constant (A7),
  `ScanPrefix`, `CountPrefix` and `KeyCount` (A3). In `pebble_store.go`, touch
  only `newPebbleStore` (and the two new functions it splits into) and
  `Reset`.
- `internal/database/ai_scan_store.go` and `internal/openlibrary/store.go`
  (A7 applies the format constant there; A1 and A3 edit them first).
- `cmd/pebble-inject-skip`. Exempt by design; no edit.
- `internal/database/pebble_store_ops_v2.go` (A5), `internal/server/` (A1,
  A2, A5), `internal/operations/registry/` (A6).
- `database.Store`, `iface_*.go`, `mocks/`. The new functions are unexported
  or package-level.
- No `RunCutover`, checkpoint, marker write or `.migration-checkpoint` write.
  Those are release B (B6a).

## 7. Tests

`internal/database/storage_format_test.go` (new). Use
`NewPebbleStoreInMemory` unless a test says otherwise.

- `TestStorageFormat_FreshStoreStampedAtSupported`. Open, close, read the
  stamp from the raw DB: it equals `SupportedStorageFormat`.
- `TestStorageFormat_DataWithoutStampReadsAsLegacy`. On a temp dir, write one
  `book:x` key with a raw `pebble.Open` (pinned format), close, then
  `NewPebbleStore`. The stamp is now `legacyStorageFormat`.
- `TestStorageFormat_OlderStoreRefusedBeforeAnyWrite`. Same raw setup; set
  `buildStorageFormat = 2` (restore it with `t.Cleanup`). `NewPebbleStore`
  returns an error that `errors.As` matches to `*StorageMigrationRequiredError`
  and whose message ends with `start serve to migrate`. Re-open raw: no `counter:` keys, no stamp, no
  sidecar file. The refused open wrote nothing.
- `TestStorageFormat_MarkerPresentRefused`. Write
  `preference:storage_migration` raw on a stamped store; `NewPebbleStore`
  refuses with `MarkerPresent == true`. `openPebbleChecked(..., openForCutover)`
  on the same dir succeeds and writes nothing (raw key count unchanged).
- `TestStorageFormat_RefusesNewerStamp`. Temp dir, stamp
  `SupportedStorageFormat+1` through the preference layer, close, reopen.
  Assert `errors.As(err, &*StorageFormatTooNewError)`, `Source == "stamp"`,
  the message contains both numbers and
  `docs/system/runbooks.md#storage-format-restore`, and a raw open shows the
  stamp unchanged.
- `TestStorageFormat_RefusesNewerSidecar`. Stamp 1, sidecar file `2\n`.
  Refused with `Source == "sidecar"`.
- `TestStorageFormat_CorruptSidecarIsRewritten`. Sidecar `garbage`; the open
  succeeds and the sidecar reads `1\n` afterwards.
- `TestStorageFormat_UndecodableStampFailsClosed`. Raw key set to
  `"not json"`: the reopen returns an error, not a silent restamp.
- `TestStorageFormat_SidecarWrittenAtOpen`. Temp dir: `<dir>.storage-format`
  contains `1\n`.
- `TestStorageFormat_ResetRestamps`. `Reset()`, then the stamp is present.
- `TestStorageFormat_CurrentStampStoreOpensAndServes`. Anti-over-suppression:
  temp dir; `NewPebbleStore`, create one book, close; reopen with
  `NewPebbleStore` (stamp equals `buildStorageFormat`, sidecar `1\n`, no
  marker). The reopen must succeed with no error, and the book must read back.
  This proves the guard refuses only older, newer and marked stores, never a
  current one.
- `TestStorageFormat_ListUserPreferencesStillDecodes`. `ListUserPreferences`
  includes a `storage_format` entry.
- `TestStorageFormat_PinnedFormatLeavesOnDiskVersionUnchanged`. Temp dir:
  create with raw `pebble.Open` at `PebbleFormatMajorVersion`, close; open and
  close with `NewPebbleStore`; raw open with `FormatMinSupported` and assert
  `db.FormatMajorVersion() == PebbleFormatMajorVersion`.

`internal/testutil` (new test file): `TestSetupIntegration_StoreStampedCurrent`.
The store from `SetupIntegration` carries `preference:storage_format` =
`SupportedStorageFormat`.

`cmd/diagnostics_test.go`: `TestRawPebbleQuery_OpensStoreTheGuardRefuses`.
Write a stamp of `SupportedStorageFormat+1` raw into a temp store, point
`config.AppConfig.DatabasePath` at it (restore in cleanup), and call
`runRawPebbleQuery(1, "preference:")`: no error.

`main_test.go`: `TestRunPrintStorageFormat`. Save and restore `os.Args` and
`os.Stdout`. Use an `os.Pipe`. Replace `executeCmd` with a function that
fails the test. Assert exit code 0, stdout exactly `"1\n"`, and that
`executeCmd` was not called.

`scripts/test_storage_format_guard.py` (new, `unittest`, standard library,
loaded with `importlib` like `scripts/test_ci_remote.py`; CI runs
`python3 -m unittest discover -s scripts -p 'test_*.py'`,
`.github/workflows/ci.yml:310`): one test per case below, asserting the exit
code and the key line of output (`REFUSING`, the `checkpoint_dir` path or
"none recorded", the warning).

Guard script. Also run these by hand and paste the output into the report:

```bash
G="python3 scripts/storage_format_guard.py --db /data/db --bin /usr/local/bin/aorg"
$G --prev 1 --sidecar 1 --checkpoint ""; echo "exit=$?"                       # 0
$G --prev "" --sidecar 1 --checkpoint ""; echo "exit=$?"                      # 0 (old binary => 1)
$G --prev "Error: unknown flag" --sidecar 1 --checkpoint ""; echo "exit=$?"   # 0
$G --prev 1 --sidecar 2 --checkpoint "/data/.migration-backups/1-2-x"; echo "exit=$?"  # 1, REFUSING + steps naming that dir
ROLLBACK_IGNORE_FORMAT=1 $G --prev 1 --sidecar 2 --checkpoint ""; echo "exit=$?"      # 1, still refused, "none recorded"
$G --prev 1 --sidecar "" --checkpoint ""; echo "exit=$?"                      # 1, cannot read
ROLLBACK_IGNORE_FORMAT=1 $G --prev 1 --sidecar "" --checkpoint ""; echo "exit=$?"     # 0 with warning
make -n rollback DEPLOY_HOST=test-host DEPLOY_BIN=/tmp/x DEPLOY_DB=/tmp/db   # shows the guard lines
```

## 8. Verify

```bash
go build ./...
go vet ./internal/database/... ./internal/testutil/... ./cmd/... .
go test -race -count=1 -run 'StorageFormat' ./internal/database/
go test -race -count=1 -run 'StoreStampedCurrent' ./internal/testutil/
go test -race -count=1 -run 'RawPebbleQuery' ./cmd/
go test -race -count=1 -run 'TestRun' .
python3 -m unittest discover -s scripts -p 'test_storage_format_guard.py' -v
go test -race -count=1 ./internal/database/
make lint-errcheck-ratchet
make lint-width
go build -o /tmp/aorg-a4 . && /tmp/aorg-a4 --print-storage-format   # prints 1
```

No `Store` interface changes, so `scripts/check-interface-width.sh` is not
required.

## 9. Deliverables

- Version headers: new Go files use the Go header; the new Python script and
  its test use the `#` header. Bump the version and `last-edited` on `pebble_store.go`,
  `cmd/diagnostics.go`, `main.go`, `main_test.go`, `Makefile` (run
  `head -3 Makefile` first and bump only if a `# version:` line exists; at
  `373ba19d2` it does, anchor 22), `Makefile.local.example`,
  `docs/system/runbooks.md` and `docs/system/deploy-and-gpu-ops.md`. Generate
  guids with `uuidgen | tr A-Z a-z`.
- Fragment `changelog.d/<YYYYMMDD>_storage_a4_format_stamp.md`, no header,
  `### Added`, `####` entries for the stamp, the pinned Pebble format and the
  rollback guard. Mention the new required `DEPLOY_DB` variable.
- Check that `git diff origin/main | grep -nE "ab""k_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}"` prints
  nothing. Use placeholders such as `192.0.2.10` in docs.
- Commit with exactly
  `feat(database): storage_format stamp checked at open; pinned Pebble format; make rollback refuses past a format change`
  (`<type>(<scope>): ...` form), ending with:

  ```
  Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/feat/storage-a4-format-stamp"`
- `gh pr create --base main --head feat/storage-a4-format-stamp`. The body
  lists the two exempt recovery tools and the new `DEPLOY_DB` requirement. Do
  NOT merge.

## 10. Exit criteria and report

- [ ] A store opened by this build carries `preference:storage_format` =
      1, and the sidecar file holds `1`.
- [ ] Emptiness is decided before any write; a store with data and no stamp
      reads as 1; with the build pretending to support 2, it is refused with
      "start serve to migrate" and the refused open writes nothing.
- [ ] A stamp or a sidecar of 2 is refused with the exact message, and the
      refused open writes nothing.
- [ ] A present `storage_migration` marker is refused in serve mode and
      opens in cut-over mode without writes.
- [ ] Opening at the pinned `PebbleFormatMajorVersion` leaves the on-disk
      format version unchanged; a fresh in-memory store and a testutil store
      come out stamped current.
- [ ] Raw diagnostics opens a store the guard refuses.
- [ ] `--print-storage-format` prints `1` and nothing else.
- [ ] The guard swaps only when the store's stamp is not above `.prev`'s
      format; otherwise it refuses and prints the checkpoint restore steps
      (section 7, all 7 cases).
- [ ] The Go tests pass with `-race`; `make lint-errcheck-ratchet` and
      `make lint-width` pass.
- [ ] PR open, not merged. The owner must add `DEPLOY_DB` to
      `Makefile.local` before the next rollback. Say so in the report.

Report:

```
TASK-A4 report
head sha: <sha>
PR: <url>
files changed: <list>
tests: <name> PASS (<time>) ...
guard script cases: <7 lines: args -> exit, message>
exempt by design: cmd/diagnostics.go raw mode (pinned format, no guard), cmd/pebble-inject-skip (no edit)
owner action: add DEPLOY_DB to Makefile.local
not done / deviations: <list or "none">
```
