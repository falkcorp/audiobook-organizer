<!-- file: docs/plans/storage-efficiency/TASK-A4.md -->
<!-- version: 1.0.0 -->
<!-- guid: e67a43a5-ec7c-49e3-a081-4ebc3c9bdab7 -->
<!-- last-edited: 2026-10-03 -->

# TASK-A4: `storage_format` stamp and the `make rollback` guard

Wave W1, in parallel with A1, A2, A5 and A6. Model: opus. Reviewers:
code-reviewer and silent-failure-hunter.

## 1. Goal and why

**Goal.**
- Write an integer `storage_format` stamp into the main Pebble store. Every
  build refuses to open a store whose stamp is higher than the build
  supports.
- Each build prints the format it supports when run with
  `--print-storage-format`.
- `make rollback` refuses to install a previous binary that supports less
  than the store's stamp.
- The stamp starts at 1, which means today's format. This PR changes no data.

**Why.** Releases B and C convert book history and file rows in place, with no
backward compatibility (design P3). `make rollback` today swaps the binary
with no data check (`Makefile:725-735`). An older binary started on converted
file rows would read every file as fingerprint-less, and its first scan would
erase the fingerprint index (design 9, `pebble_store_bookfiles.go:840`,
`pebble_store.go:5507`). The guard is what makes "the backup is the only way
back" true. It must ship and be deployed before any format change.

## 2. Setup

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
git worktree add ../aorg-storage-a4-format-stamp -b feat/storage-a4-format-stamp origin/main
cd ../aorg-storage-a4-format-stamp
npm ci --prefix web
```

- Do NOT run `go work init`.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch.

## 3. Read before editing

- `internal/database/pebble_store.go:389-470`. `newPebbleStore`: open,
  marker load, import-path migration, the `counter:` init loop (`:423`), and
  the warmup goroutine.
- `internal/database/pebble_store.go:5100-5136`. `Reset()` wipes every key
  and re-creates the counters. It wipes the stamp too.
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
- `Makefile:725-736`, the `rollback` target. `Makefile:45-51`, the deploy
  variables.
- `docs/system/deploy-and-gpu-ops.md:40-75` (the rollback flow) and
  `docs/system/runbooks.md:110-127` ("Backup and Restore").

## 4. Re-verify anchors

1. `grep -n 'opts := &pebble.Options{FormatMajorVersion: pebble.FormatNewest}\|db, err := pebble.Open(path, opts)\|counters := \[\]string{' internal/database/pebble_store.go`
   → `393`, `397`, `423`, and `5120` (inside `Reset`).
2. `grep -n 'const dbVersionPreferenceKey = "db_version"' internal/database/migration_bookkeeping.go` → `26:`
3. `grep -n 'type DatabaseVersion struct' internal/database/migrations.go` → `94:`
4. `grep -n 'LowerBound: \[\]byte("preference:")' internal/database/pebble_store_preferences.go` → `84:`
5. `grep -n 'func (p \*PebbleStore) SetUserPreference' internal/database/pebble_store_preferences.go` → `34:`
6. `grep -n '^rollback:\|DEPLOY_BIN).prev' Makefile` → `727`, `731`, `733`.
7. `grep -n 'if registry.IsChildMode()' main.go` → `37:`
8. `grep -n 'fmt.Println("Using config file:"' cmd/root.go` → `550:`
9. `grep -n 'func (p \*PebbleStore) Reset' internal/database/pebble_store.go` → `5102:`
10. `grep -rn 'pebble.Open(' --include='*.go' internal cmd | grep -v _test` →
    - `internal/database/pebble_store.go:397`: the main store. Guarded by
      this task.
    - `internal/database/ai_scan_store.go:108` and
      `internal/openlibrary/store.go:33`: other stores, out of scope.
    - `cmd/diagnostics.go:208` and `cmd/pebble-inject-skip/main.go:34`: raw
      opens of the main DB that bypass the check. **Out of scope** for this
      task. List them in the PR body as known bypasses. They are operator
      tools run by hand.
11. `P=$(go env GOMODCACHE)/github.com/cockroachdb/pebble/v2@v2.1.7; grep -n '^	Create(\|^	Rename(' $P/vfs/vfs.go`
    → `97: Create(name string, category DiskWriteCategory) (File, error)`,
    `122: Rename(oldname, newname string) error`. `vfs.WriteCategoryUnspecified`
    exists (used at `vfs.go:399`).
12. `grep -n 'Environment="DATABASE_PATH' deploy/audiobook-organizer.service`
    → `70:`. This is the default path only; prod overrides it. Never
    hard-code it.

The design's anchor `Makefile:725-735` holds (the target starts at `:725`
with its `##` comment).

## 5. Steps

1. **`internal/database/storage_format.go` (new).**
   - `const SupportedStorageFormat = 1`. Doc comment: "1 = the format before
     the storage-efficiency program (book_ver full copies, inline file
     signals). Raise it only in the release whose converter changes the
     format; the converter advances the stamp."
   - `const storageFormatPreferenceKey = "storage_format"`.
   - `const StorageFormatSidecarSuffix = ".storage-format"` and
     `func StorageFormatSidecarPath(dbPath string) string { return filepath.Clean(dbPath) + StorageFormatSidecarSuffix }`.
     The sidecar is a sibling file next to the DB directory, for example
     `/data/audiobooks.pebble.storage-format`. It is not inside the DB
     directory, which Pebble owns.
   - `type StorageFormatTooNewError struct { Path string; Stamp, Supported int }`
     with `Error()` returning exactly:
     `storage format <Stamp> in <Path> is newer than this build supports (<Supported>); refusing to open. Restore the snapshot and build that match this store: docs/system/runbooks.md#storage-format-restore`.
   - `func readStorageFormatStamp(db *pebble.DB) (stamp int, present bool, err error)`.
     `db.Get([]byte("preference:storage_format"))`; `pebble.ErrNotFound`
     → `(0, false, nil)`. Decode `UserPreference`, then decode `*Value` as
     `DatabaseVersion` and return `.Version`. A decode failure is an error.
     Never treat it as absent: fail closed.
   - `func (p *PebbleStore) ensureStorageFormatStamp() (int, error)`. If the
     stamp is present, return it. If it is absent, write
     `SupportedStorageFormat` with
     `p.setPreferencesAtomic([]preferenceWrite{{Key: storageFormatPreferenceKey, Value: <databaseVersionPayload(SupportedStorageFormat)>}})`
     (`migration_bookkeeping.go:55`, `:113`), then return it. Reuse
     `databaseVersionPayload`; do not copy it.
   - `func writeStorageFormatSidecar(fs vfs.FS, dbPath string, stamp int) error`.
     Write `fmt.Sprintf("%d\n", stamp)` to `<sidecar>.tmp` with
     `fs.Create(name, vfs.WriteCategoryUnspecified)`, then `Sync`, then
     `Close`, then `fs.Rename` to the sidecar path. Using the store's own
     `vfs.FS` means in-memory tests never touch the real disk.
2. **`newPebbleStore` (`pebble_store.go`).**
   - Right after `pebble.Open` succeeds (`:397-400`) and BEFORE
     `ensureUndecodableMarkersLoaded`:
     ```go
     stamp, present, err := readStorageFormatStamp(db)
     if err != nil { db.Close(); return nil, fmt.Errorf("read storage format: %w", err) }
     if present && stamp > SupportedStorageFormat {
         db.Close()
         return nil, &StorageFormatTooNewError{Path: path, Stamp: stamp, Supported: SupportedStorageFormat}
     }
     ```
     Nothing may write to the store before this check.
   - After the `counter:` init loop (`:423-437`; `nextID("preference")` needs
     `counter:preference`) and BEFORE the warmup block, call
     `p.ensureStorageFormatStamp()`, and close and return on error. Then
     write the sidecar with the store's fs: `opts.FS` when it is non-nil,
     otherwise `vfs.Default`. A sidecar write failure is logged at
     `slog.Error` with the path and the error, and the open continues. The
     app must not refuse to serve over a sidecar. The Makefile refuses a
     rollback when the sidecar is missing (step 5), so the failure stays
     visible.
   - Log once:
     `slog.Info("storage format", "stamp", stamp, "supported", SupportedStorageFormat, "sidecar", sidecarPath)`.
3. **`Reset()` (`pebble_store.go:5102`).** After the batch commit, call
   `p.ensureStorageFormatStamp()` and return its error. The wipe deleted the
   stamp, and a reset store is in today's format.
4. **`main.go`.** In `run()`, after the three `Set*` calls and BEFORE the
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
5. **`Makefile`.**
   - Add `DEPLOY_DB   ?=` next to `DEPLOY_BIN` (`:47`), with a comment: "path
     of the main Pebble store on DEPLOY_HOST (the --db / DATABASE_PATH
     value); required by rollback".
   - In `rollback`, after the `.prev` existence check (`:731`) and before the
     copy (`:732`), add a guard that runs
     `scripts/storage_format_guard.sh "<prev output>" "<sidecar content>"`.
     Collect the two strings with:
     - `ssh $(DEPLOY_HOST) '$(DEPLOY_BIN).prev --print-storage-format 2>/dev/null'`
       (keep stdout only; ignore the exit code);
     - `ssh $(DEPLOY_HOST) 'cat $(DEPLOY_DB).storage-format 2>/dev/null'`.
   - Add `@[ -n "$(DEPLOY_DB)" ] || (echo "ERROR: DEPLOY_DB is not set ..."; exit 1)`
     next to the other two checks.
   - Pass `ROLLBACK_IGNORE_FORMAT` through to the script as an environment
     variable.
6. **`scripts/storage_format_guard.sh` (new, under 20 lines, `set -eu`).**
   Arguments: `$1` is the previous binary's output and `$2` is the sidecar
   content.
   - Strip whitespace from both.
   - If `$1` is empty or not all digits, set `prev=1`. Every build before
     this one rejects the unknown flag, and every one of them reads format 1.
     Without this rule, the first rollback after A4 ships would be refused
     for nothing.
   - If `$2` is empty or not all digits: when `ROLLBACK_IGNORE_FORMAT=1`,
     print a warning and exit 0; otherwise print
     `REFUSING: cannot read the store's storage format from <path>. Set ROLLBACK_IGNORE_FORMAT=1 only if you have verified the store is at format 1.`
     and exit 1.
   - If `prev < store`: print
     `REFUSING: previous binary supports storage format <prev>, the store is at <store>. A binary swap cannot go back past a format change. Follow docs/system/runbooks.md#storage-format-restore.`
     and exit 1. `ROLLBACK_IGNORE_FORMAT` never overrides this case.
   - Otherwise exit 0.
   - Give it a `# file:` / `# version:` / `# guid:` / `# last-edited:` header.
7. **`Makefile.local.example`.** Add a `DEPLOY_DB ?=` example line with a
   placeholder path, plus the same comment as step 5. Bump its header.
8. **Docs.**
   - `docs/system/runbooks.md`: add a section `## Storage Format Restore`
     (anchor `storage-format-restore`) after "Backup and Restore". It must
     explain:
     - what the stamp is;
     - that `make rollback` refuses past it;
     - the restore steps: stop the service; roll back or clone the recursive
       ZFS snapshot named in the pre-migration marker file (releases B and C
       write it); install the build recorded with that snapshot; start; check
       the `storage format` log line;
     - that nothing short of the snapshot restores a converted store.
   - `docs/system/deploy-and-gpu-ops.md`: in the rollback flow, add
     `DEPLOY_DB` to the example command and add one step describing the
     guard and `ROLLBACK_IGNORE_FORMAT`.

## 6. Do not touch

- `internal/database/migrations.go` and `migration_bookkeeping.go`, except to
  READ `databaseVersionPayload` and `setPreferencesAtomic`. Do not change
  `db_version` handling. (The plan listed `migrations.go`; it is not needed.)
- The store options in `newPebbleStore` (that is A7), `ScanPrefix`,
  `CountPrefix` and `KeyCount` (that is A3, a later wave). In
  `pebble_store.go`, touch only `newPebbleStore` and `Reset`.
- `internal/database/pebble_store_ops_v2.go` (A5), `internal/server/` (A1,
  A2, A5), `internal/operations/registry/` (A6).
- `database.Store`, `iface_*.go`, `mocks/`. The new methods are unexported or
  package-level.
- `cmd/diagnostics.go` and `cmd/pebble-inject-skip`. Known bypasses, listed
  in the PR.

## 7. Tests

`internal/database/storage_format_test.go` (new). Every test uses
`NewPebbleStoreInMemory` unless it says otherwise.

- `TestStorageFormat_FreshStoreStampedAtSupported`. Open, close, then read
  the stamp from the raw DB. Assert it equals `SupportedStorageFormat`.
- `TestStorageFormat_ExistingUnstampedStoreGetsStamp`. Create a store, delete
  the `preference:storage_format` key, close, reopen. Assert the stamp is
  back.
- `TestStorageFormat_RefusesNewerStamp`. Use a real temp dir with
  `NewPebbleStore(t.TempDir()+"/db")`. Write a stamp of
  `SupportedStorageFormat+1` through the preference layer, close, reopen.
  Assert:
  - `errors.As(err, &*StorageFormatTooNewError)`;
  - the message contains both numbers and
    `docs/system/runbooks.md#storage-format-restore`;
  - the stamp value is unchanged afterwards. Open the dir with
    `pebble.Open` directly to check: the refused open must not write.
- `TestStorageFormat_UndecodableStampFailsClosed`. Set the raw key to
  `"not json"`. Assert the reopen returns an error, not a silent restamp.
- `TestStorageFormat_SidecarWrittenAtOpen`. With `NewPebbleStore` on a temp
  dir, assert the file `<dir>.storage-format` contains `1\n`.
- `TestStorageFormat_ResetRestamps`. Call `Reset()`, then assert the stamp is
  present.
- `TestStorageFormat_ListUserPreferencesStillDecodes`. Assert
  `ListUserPreferences` includes a `storage_format` entry. This proves the
  value is a valid `UserPreference`.

`main_test.go`:

- `TestRunPrintStorageFormat`. Save and restore `os.Args` and `os.Stdout`.
  Use an `os.Pipe`. Replace `executeCmd` with a function that fails the test.
  Assert exit code 0, stdout exactly `"1\n"`, and that `executeCmd` was not
  called.

Guard script. Run these by hand and paste the output into the report:

```bash
sh scripts/storage_format_guard.sh "1" "1"; echo "exit=$?"     # 0
sh scripts/storage_format_guard.sh "" "1"; echo "exit=$?"      # 0 (old binary => 1)
sh scripts/storage_format_guard.sh "Error: unknown flag" "1"; echo "exit=$?"   # 0
sh scripts/storage_format_guard.sh "1" "2"; echo "exit=$?"     # 1, REFUSING
ROLLBACK_IGNORE_FORMAT=1 sh scripts/storage_format_guard.sh "1" "2"; echo "exit=$?"   # 1, still refused
sh scripts/storage_format_guard.sh "1" ""; echo "exit=$?"      # 1, cannot read
ROLLBACK_IGNORE_FORMAT=1 sh scripts/storage_format_guard.sh "1" ""; echo "exit=$?"    # 0 with warning
make -n rollback DEPLOY_HOST=test-host DEPLOY_BIN=/tmp/x DEPLOY_DB=/tmp/db   # shows the guard lines
```

## 8. Verify

```bash
go build ./...
go vet ./internal/database/... .
go test -race -count=1 -run 'StorageFormat' ./internal/database/
go test -race -count=1 -run 'TestRun' .
go test -race -count=1 ./internal/database/
make lint-errcheck-ratchet
make lint-width
go build -o /tmp/aorg-a4 . && /tmp/aorg-a4 --print-storage-format   # prints 1
```

No `Store` interface changes, so `scripts/check-interface-width.sh` is not
required.

## 9. Deliverables

- Version headers: new Go files use the Go header; the new shell script uses
  the `#` header. Bump the version and `last-edited` on `pebble_store.go`,
  `main.go`, `main_test.go`, `Makefile` (if it carries a header; check the
  top of the file), `Makefile.local.example`, `docs/system/runbooks.md` and
  `docs/system/deploy-and-gpu-ops.md`. Generate guids with
  `uuidgen | tr A-Z a-z`.
- Fragment `changelog.d/<YYYYMMDD>_storage_a4_format_stamp.md`, no header,
  `### Added`, `####` entries for the stamp and the rollback guard. Mention
  the new required `DEPLOY_DB` variable.
- Check that `git diff origin/main | grep -nE 'abk_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}'` prints
  nothing. Use placeholders such as `192.0.2.10` in docs.
- Commit, for example
  `feat(database): storage_format stamp checked at open; make rollback refuses older formats`,
  ending with:

  ```
  Co-Authored-By: <model name> <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/feat/storage-a4-format-stamp"`
- `gh pr create --base main --head feat/storage-a4-format-stamp`. The body
  lists the known raw-open bypasses and the new `DEPLOY_DB` requirement. Do
  NOT merge.

## 10. Exit criteria and report

- [ ] A store opened by this build carries `preference:storage_format` =
      1, and the sidecar file holds `1`.
- [ ] A store stamped 2 is refused with the exact message, and the refused
      open writes nothing.
- [ ] `--print-storage-format` prints `1` and nothing else.
- [ ] The guard script behaves as in section 7, all 7 cases.
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
known bypasses: cmd/diagnostics.go:208, cmd/pebble-inject-skip/main.go:34
owner action: add DEPLOY_DB to Makefile.local
not done / deviations: <list or "none">
```
