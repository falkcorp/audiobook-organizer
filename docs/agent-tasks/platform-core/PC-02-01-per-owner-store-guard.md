<!-- file: docs/agent-tasks/platform-core/PC-02-01-per-owner-store-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: efe4964f-4c5b-4e66-b91f-65b20de9b323 -->
<!-- last-edited: 2026-11-04 -->

# PC-02-01 — Per-owner store guard in `internal/database`: reserved `c/` and `m/` families, install marker, served ledger, sidecars, open stamp and the fast exits

| Field | Value |
|---|---|
| ID | PC-02-01 |
| Tier | **T4 Opus/xhigh–max.** These are the first durable boot writers of the programme. A wrong write order (a `c/` key before `c/storage/install`, a served-ledger `last` committed before the store finished its startup steps, a sidecar listing a step that did not commit) makes the first core-storage boot at S2b halt with `ErrForeignKeys` or misread history on production stores. |
| Wave | W02 (S0, release `S0`) |
| Depends on | PC-01-02 (creates `core/storage/keys`, `core/buildid`, `core/go.mod`), PC-01-05 (creates `.github/workflows/docker-image.yml`) |
| Blocks | PC-02-02, PC-02-04, PC-02-05 (they call what this PR adds) |
| Trailer | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §4.3, §4.4, §4.5, §5 rule 1, §6.8 |
| Spec | 09 §9.2.2 (whole section); 09 S0 rows "Data/keys", "Rollback", "Done when"; 05-storage §5S.3.3 (`storage/keys`, notation, codec), §5S.3.4 rule 4, §5S.3.7 (the guard: every row of its table, `CheckReader`, the probe); 02 N14 |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-02-01" -b platform-core/pc-02-01-store-guard origin/main
cd "$REPO/../audiobook-organizer-pc-02-01"
git rebase origin/main
git worktree list            # this worktree is listed; you are not in the primary checkout
make web-deps                # PC-01-06's entry point; never `go work init` here (CLAUDE.md)
```

Gate: W00 and W01 are merged (`git log origin/main --oneline | grep -c 'Stage-Release: S0 1'` is non-zero and
`ls core/go.mod core/storage/keys/keys.go core/buildid/buildid.go` lists all three). The PR may be opened and made
green at any time after that; it merges in W02's order `{PC-02-01, PC-02-07} → …` (plan §(b)).

## Goal

Ship 09 §9.2.2's one guard mechanism in `internal/database` (S2b moves it into core `storage`): every owner of this
build is served at level 0, every boot writes the records of 09 §9.2.2's table with the `storage/keys` encoding and
05-storage §5S.3.7's exact shapes, the three fast exits print the build's facts, and `scripts/storage_format_guard.py`
implements `CheckReader`'s table for `make rollback` and the `deploy` recipe. S0 raises no level and the legacy
projection (`preference:storage_format` / `<db>.storage-format`) stays at HEAD's format 1, so every pre-guard build
still opens every store this PR writes (09 §9.2.2 "Policy for S0–S10").

## Exact files

Create:
- `internal/database/storage_guard.go` — guard types, level vector, sidecars, fast-exit helpers, `CheckReader`.
- `internal/database/storage_guard_test.go`
- `internal/database/served_ledger.go` — install marker, served ledger, owed duties, foreign-open record, open stamp.
- `internal/database/served_ledger_test.go`
- `internal/database/testdata/s0-store/` — the committed S0-written fixture store (layout in step 11).

Modify:
- `internal/database/keyfamilies.go` — register `c/` and `m/`.
- `internal/database/pebble_store.go` — open phase (pre-open listing, level refusal) and init phase (marker, staging).
- `internal/database/storage_format.go` — only if a helper must be shared; keep `SupportedStorageFormat = 1`.
- `main.go` — fast exits `--print-storage-levels` and `--print-upgrade-floor`; set the binary ID.
- `scripts/storage_format_guard.py`, `scripts/test_storage_format_guard.py` — the `CheckReader` table.
- `.github/workflows/docker-image.yml` — the levels assertion inside the container.

Also needed (see "Planner notes"): `internal/database/store.go` (the served-ledger commit after `RunMigrations`).

## Re-verify greps (run at your base; expected counts measured at the planning checkout `51a2c2acd`)

```bash
grep -n 'const SupportedStorageFormat = 1' internal/database/storage_format.go        # 1 hit (line 50)
grep -n 'func openPebbleChecked' internal/database/pebble_store.go                     # 1 hit (line 620)
grep -n 'func initPebbleStore' internal/database/pebble_store.go                       # 1 hit
grep -n 'writeStorageFormatSidecar(sidecarFS, path, stamp)' internal/database/pebble_store.go  # 1 hit (line 779)
grep -n 'func KeyFamilies' internal/database/keyfamilies.go                            # 1 hit (line 330)
grep -cE '^\s*\{"(c|m)/"' internal/database/keyfamilies.go                              # 0
grep -n '"--print-storage-format"' main.go                                             # 1 hit (line 40)
grep -n 'if err := RunMigrations(s); err != nil' internal/database/store.go            # 1 hit (line 1857)
grep -n 'func getCurrentVersion' internal/database/*.go                                # 1 hit
grep -c 'ROLLBACK_IGNORE_FORMAT' scripts/storage_format_guard.py                       # 8
grep -n 'func Join\|func Str\|func U64' core/storage/keys/keys.go                      # 3 hits (absent at 51a2c2acd; PC-01-02 adds them)
grep -n 'func Binary' core/buildid/buildid.go                                          # 1 hit (PC-01-02)
grep -rn 'c/storage/' --include=*.go internal/ | grep -v _test | wc -l                 # 0
```

The spec cites `storage_format.go:338-352` (fresh-store stamp), `pebble_store.go:651-660` (unreadable sidecar) and
`:776-786` (sidecar rewrite) at `748fd787a`; at `51a2c2acd` they are `storage_format.go:338-361`
(`ensureStorageFormatStamp`), `pebble_store.go:651-659` and `:776-786`, unchanged. A zero-hit edit target or a
moved count: STOP and report.

## Code contract

Types (05-storage §5S.3.7, declared in `internal/database` at S0; names copied from the spec):

```go
type Owner string

// Levels is the supported or stored level per owner. Absent = 0.
type Levels map[Owner]int

type LevelTooNewError struct {
	Owner           Owner
	Store, Supported int
	Source          string // "stamp", "sidecar" or "legacy-stamp"
}

type LegacyGuardProjection struct {
	StampKey, MarkerKey, SidecarSuffix string
	LegacyMaxFormat                    int // 1 at the evidence commit
	// Stage is declared for S2b's move and unused at S0 (no level step exists).
}

type StagingMarker struct {
	ArchiveSHA256 string    `json:"archive_sha256"`
	Nonce         string    `json:"nonce"`
	BinaryID      string    `json:"binary_id"`
	At            time.Time `json:"at"`
}

type Duty string
const DutyJournal Duty = "journal"
type Owed struct{ Since, Detected time.Time }

// UpgradeRequirement mirrors 05-updater §5U.3 (fields and JSON names exactly).
type UpgradeRequirement struct {
	Release string            `json:"release"`
	Owners  map[string]uint64 `json:"owners,omitempty"`
}

type ReaderCheckOptions struct{ IgnoreRestoreStagedBy string } // zero at S0

// CheckReader: 05-storage §5S.3.7 "Reader check". Decides, without opening
// Pebble, whether the binary at exe may open the store as it is now. Reason
// tokens: level_newer, format_newer, upgrade_floor, restore_pending,
// restore_inflight, sidecar_unreadable, cannot_determine.
func CheckReader(ctx context.Context, stateDir, dbPath, exe string, opt ReaderCheckOptions) error
```

Names coined here (no spec name exists): `SupportedLevels() Levels` (the build's vector: `{"books": 0, "core": 0}`),
`UpgradeFloor() UpgradeRequirement` (zero value at S0), `SetBinaryID(id string)` (called once by `main` before
any store opens), `ErrForeignKeys` (rule 4's halt), `ReaderCheckError{Reason, Message string}` (the error
`CheckReader` returns, `Reason` one of the seven tokens).

The S0 owner set is `books` and `core` (05-storage §5S.3.7: books owns the legacy projection; "09 S0's guard writes
`c/storage/served/core/last` too"). Owner set coined from those two rows; no other owner exists at S0.

Keys (05-storage §5S.3.3 "Notation"): every record key is the bytes `c/storage/` followed by
`keys.Join(...)` of its segments, built with `github.com/falkcorp/mediacore/storage/keys`:

| Record | Key segments after `c/storage/` | Value (fields exactly) |
|---|---|---|
| install marker | `Str("install")` | `{install_id, created, build}` |
| served ledger | `Str("served"), Str(<owner>), Str("last")` | `{build, supported, duties, at}` (`duties` omitted at S0: the S0 build performs neither duty) |
| owed duty | `Str("owed"), Str("journal")` | `{since, detected}` |
| foreign-open record | `Str("sidecar_identity")` | `{device, inode, size, mtime_ns, ctime_ns, at, pending}` of `<db>.storage-format`; `pending` holds the same five identity fields |
| open stamp | `Str("pebble_options")` | `{num, pending}` |
| staging marker (written by PC-02-05 only) | `Str("staging")` | `StagingMarker` |
| level stamp (never written at S0 except the fresh-install batch) | `Str("level"), Str(<owner>)` | `{level, by_build, at}` |

Values use 05-storage §5S.3.3's native `codec.JSON[T](1)` encoding: the tag byte `'j'` followed by a JSON object
whose first member is `"v":1`, then the record's members. `core/storage/codec` does not exist at S0, so write a
private encoder producing exactly those bytes; if `core/storage/codec/` exists at your base, use it instead.
Every `build` field holds the binary ID, `buildid.Binary(version)`, never the UI build ID (05-storage §5S.3.7
"Binary ID"). `install_id`: 32 lowercase hex characters from `crypto/rand` (format coined here).

Sidecars (temp file + fsync + rename + directory fsync, as `writeStorageFormatSidecar` does):
- `<db>.storage-levels`: one line `<owner> <level>` per owner, sorted: `books 0` then `core 0`.
- `<db>.storage-applied`: first line `install <install_id> epoch 0` (no `c/storage/epoch` record exists before
  S2b), then one line per owner, sorted: `books version <getCurrentVersion>` and `core version 0`.

Fast exits (09 S0 "Endpoints"; 05-storage §5S.3.7 row "Fast-exit flags"), handled in `main.go` before cobra, like
`--print-storage-format`, printing exactly and exiting 0 without opening anything:
- `--print-storage-levels` → `books 0\ncore 0\n`
- `--print-upgrade-floor` → `{"release":""}\n` (the JSON of the zero `UpgradeRequirement`; readers treat a
  requirement with an empty `release` and no `owners` as "no floor"; exact text coined here, see Planner notes)
- `--print-storage-format` → unchanged (`1`).

Log line (09 §9.2.1 "Soaks"; pinned by PC-02-02's `TestBootLogsBinaryID`): after the served-ledger commit, one INFO
message `boot levels=books=0,core=0` (`<owner>=<level>` pairs, sorted, comma-separated; separator coined here).

## Invariants (each is a test)

1. **Marker first** (05-storage §5S.3.4 rule 4; 09 §9.2.2 table row 1). On a store without `c/storage/install`, the
   guard first verifies `[c/, c0)` and `[m/, m0)` are empty (non-empty → refuse with `ErrForeignKeys` and up to 10
   sample keys, writing nothing), then commits the marker as the first `c/` write. On an empty store
   (`emptyAtOpen`) the same batch also holds `c/storage/level/books` and `c/storage/level/core` at level 0, so a
   crash leaves an unmarked store or a fully stamped one (09 §9.2.2 "Fresh installs").
2. **Nothing before refusal** (09 S0 digest §5 rule 1). A store whose `.storage-levels` sidecar or any
   `c/storage/level/<owner>` stamp is above `SupportedLevels()` is refused with `LevelTooNewError` before any write;
   `max(stamp, sidecar)` per owner decides; a missing store directory and no sidecars passes.
3. **Open stamp** (05-storage row "Pebble open stamp"). Before `pebble.Open` the guard lists the DB directory and
   takes the highest `OPTIONS-<n>`. A number that differs from both the record's `num` and `pending`, a missing
   record, or an unreadable directory is a foreign open. After the open it stages the new highest number as `pending`.
4. **Foreign-open record** (row "Foreign-open record"). Before HEAD's sidecar rewrite at `pebble_store.go:779` the
   guard stats `<db>.storage-format` and compares the full tuple with the record and with `pending`; a difference,
   a missing or unreadable sidecar, or a record without `ctime_ns` is a foreign open. After the rewrite it stages the
   new identity as `pending`.
5. **Owed duty.** At S0 the previous `core` record never lists `journal`, so `c/storage/owed/journal` is owed: written
   once with `since` = the previous `c/storage/served/core/last`'s `at` (zero when there is none) and `detected` =
   now; an entry already present keeps both values (05-storage row "Served ledger"). No `gen` entry: S0 registers no
   derived index.
6. **Served-ledger commit last.** One batch, committed only after `RunMigrations` returned nil in
   `InitializeStore` (09 §9.2.2 row 2: "at S0–S2, after books' store has finished its startup steps"): both
   owners' `last`, `pending` → recorded for the foreign-open record and the open stamp. A boot that fails earlier
   leaves the previous `last` unchanged.
7. **Sidecars after the commit.** `.storage-levels` and `.storage-applied` are rewritten from the stamps and
   bookkeeping after the served-ledger commit (they may lag, never lead). An unreadable or missing sidecar at open
   is rewritten with an error log, never a refusal (09 S0 digest §5 rule 1).
8. **HEAD untouched.** The legacy stamp, marker and `.storage-format` keep HEAD's code and value 1; `openForCutover`
   and `ROLLBACK_IGNORE_FORMAT` keep their behaviour. HEAD-era code paths tolerate `c/` and `m/` keys.
9. **Locks and fsync.** Each guard batch commits with `pebble.Sync` once; no store-wide mutex is held across that
   fsync (repo lesson recorded in `docs/agent-tasks/dedup-pipeline-hardening/TASK-04-*`).

## TDD tests to write first

Spec-named (copy exactly):
- `TestLegacyToleratesNamespacedKeys` — on a store booted once by the guard, seed `c/x` and `m/y`, then run the census
  (`(*PebbleStore).DBCensus`), the retention sweeps, a backup (`backup.CreateBackupWithCheckpoint`), a restore into a
  temp dir and a second boot: no error, both keys survive byte-for-byte, the census files them under `c/` and `m/`.
- `TestS0LedgerKeysRoundTrip` — the install marker precedes every `c/` key: a test-only observer on the guard's
  batch commits (`guardCommitObserver`, name coined here, nil in production) records each committed batch, and the
  first batch holding any `c/` key holds `c/storage/install` and, on a non-empty store, no other `c/` key; every key
  written round-trips through `keys.Split`; every `build` field equals `buildid.Binary(version)` of the test binary.
- `TestReaderCheckMatchesRollbackScript` (05-storage §5S.3.7) — one fixture table, run against `CheckReader` and
  against `scripts/storage_format_guard.py` (invoked with `python3`), same decision and token per case.

New tests (names coined here): `TestGuard_RefusesLevelAboveBuild_WritesNothing`,
`TestGuard_ForeignKeysWithoutMarker_Halts`, `TestGuard_FreshInstallOneBatch`, `TestGuard_OpenStampForeignOpen`,
`TestGuard_SidecarIdentityPendingNotForeign`, `TestGuard_ServedLedgerUnchangedWhenMigrationsFail`,
`TestGuard_SidecarsRewrittenWhenUnreadable`, `TestPrintStorageLevels`, `TestPrintUpgradeFloorEmpty`.

Script cases (`scripts/test_storage_format_guard.py`, 09 S0 "Done when" and §9.2.2's list, each a test method):
compat (a pre-guard `.prev` on a level-0 store passes); restore probe over `restore/` (no `restore/` → pass;
`pending.json` → `restore_pending`; `inflight.json` under any ID → `restore_inflight`; `inflight-checkpoint.json` →
`restore_inflight`); override `ROLLBACK_IGNORE_RESTORE=1` passes a pending intent only and never an in-flight one;
`--print-upgrade-floor` (empty floor + no sidecar → pass; non-empty floor + no sidecar → refuse "boot any release
≥ S0 once"; floor not met → `upgrade_floor`); pre-guard candidate (unknown-flag with empty stdout from
`--print-upgrade-floor` counts as "no floor" only when `--print-storage-levels` also failed, else
`cannot_determine`); unreadable `.storage-levels` with `.storage-format` = 1 → "all owners at 0" (logged) while
still refusing on the probe; no sudoers line (probe stderr starts with `sudo:`) → refuse naming the line; no DB
directory and no sidecars → pass; stamp and sidecar disagree → both values printed. Keep every existing case green.

## Steps

1. Run the re-verify greps. Read 05-storage §5S.3.7 in full before writing code.
2. `keyfamilies.go`: add `{"c/", "core keyspaces (reserved); the S0 store guard writes c/storage/", "internal/database/served_ledger.go"}`
   and `{"m/", "module keyspaces (reserved; no writer before S2b)", "internal/database/keyfamilies.go"}`. Run
   `go test ./internal/database/ -run 'TestKeyFamil' -count=1`.
3. Write the failing tests above.
4. `storage_guard.go`: types, `SupportedLevels`, `UpgradeFloor`, sidecar read/write, the codec encoder, `SetBinaryID`.
5. `served_ledger.go`: record read/stage/commit helpers keyed with `storage/keys`.
6. `pebble_store.go` open phase: pre-open listing (invariant 3), level refusal (invariant 2), foreign-open compare
   (invariant 4) — all before any write; keep `openPebbleChecked`'s "writes nothing" contract.
7. `pebble_store.go` init phase: marker (invariant 1) after the HEAD checks and before any other `c/` write; stage
   the pending identities and the owed duty in one batch after HEAD's sidecar rewrite.
8. `store.go` `InitializeStore`: after `RunMigrations` returns nil, the served-ledger commit (invariant 6), then the
   sidecars (invariant 7), then the `boot levels=` log line. On a `RunMigrations` error nothing of the ledger commits.
9. `CheckReader` in Go, then the same table in `storage_format_guard.py`. New CLI flags (interface coined here; PC-02-04
   and the Makefile call it): `--mode rollback|deploy` (names the candidate in messages; default `rollback`),
   `--prev-levels`, `--prev-levels-rc`, `--prev-floor`, `--prev-floor-rc` (stdout and exit code of
   `<candidate> --print-storage-levels` / `--print-upgrade-floor`), `--levels-sidecar`, `--applied-sidecar`,
   `--db-dir-state present|absent|empty`, `--restore-probe`, `--restore-probe-rc`, and the matching `--*-err`.
   A refusal prints a line starting `REFUSE <token>:` and exits 1. Existing flags and their behaviour stay. The
   probe command the caller runs is 05-storage §5S.3.7's literal
   `sudo -n /usr/bin/find <state_dir> -maxdepth 3 ( -path <state_dir>/restore/pending.json -o -path '<state_dir>/restore/*/inflight.json' -o -path '<state_dir>/restore/*/inflight-checkpoint.json' ) -print`;
   document it in the script's docstring.
10. `main.go`: `database.SetBinaryID(buildid.Binary(version))` (log and continue with `""` only if it errors, never
    exit), then the two fast exits next to `--print-storage-format`.
11. Fixture: a test guarded by `ABO_WRITE_S0_FIXTURE=1` (env name coined) creates a store with the guard, boots it
    twice, and copies it to `internal/database/testdata/s0-store/db/` with its three sidecars as
    `testdata/s0-store/db.storage-levels`, `db.storage-applied`, `db.storage-format`. Commit the output. A second,
    always-on test opens a copy of the fixture with the guard and asserts the marker and ledger read back.
12. `docker-image.yml`: after the ready wait, `docker exec <container> /app/audiobook-organizer --print-storage-levels`
    and assert the output is exactly `books 0` and `core 0` on two lines.
13. Run the acceptance commands. Add a `changelog.d/` fragment. Commit, push, open the PR.

## Acceptance (commands and expected output)

```bash
go build ./... && go -C core build ./...                          # exit 0
go test ./internal/database/ -run 'TestLegacyToleratesNamespacedKeys|TestS0LedgerKeysRoundTrip|TestReaderCheckMatchesRollbackScript|TestGuard_|TestPrint' -count=1 -race
                                                                    # ok  github.com/falkcorp/audiobook-organizer/internal/database
python3 -m unittest scripts/test_storage_format_guard.py -v         # every test "ok"; final line "OK"
go build -o /tmp/abo . && /tmp/abo --print-storage-levels           # books 0
                                                                    # core 0
/tmp/abo --print-upgrade-floor                                      # {"release":""}
/tmp/abo --print-storage-format                                     # 1
ls internal/database/testdata/s0-store/                             # db  db.storage-applied  db.storage-format  db.storage-levels
make ci; echo "exit=$?"                                             # exit=0
```

The `docker-image` required check is green on the PR, its log showing the two level lines from inside the container.

## Out of scope

Raising any level; the `LegacyGuardProjection.Stage` body; `c/storage/epoch`, `c/storage/boot`, claims, the
checkpoint record (all S2b); the staging marker writer (PC-02-05); `--version-json` and the `boot binary_id=` line
(PC-02-02); the Makefile and `deploy` recipe wiring of the new script flags (PC-02-04); the updater's use of
`CheckReader` (PC-02-03); deleting `ROLLBACK_IGNORE_FORMAT`.

## STOP conditions

- A re-verify grep misses, or `core/storage/keys` lacks `Join`/`Split`/`Str`/`U64`.
- Any HEAD family already starts with `c/` or `m/` (grep above prints non-zero).
- Your design needs a `c/` write before the marker, a served-ledger write before `RunMigrations` returns, or a
  change to the legacy stamp's value: stop and report instead.

## Rollback

Revert the PR. Older builds ignore every key and sidecar it wrote (09 S0 "Rollback"); `make rollback` keeps working
through `--print-storage-format`. The committed fixture is test data only.

## Repo rules

- Worktree only; never commit on `main`; never run `go work init` in a checkout or worktree (CLAUDE.md).
- Every new or changed file gets or bumps its 4-line versioned header (`.standards/instructions/file-headers.md`);
  keep existing guids. `changelog.d/` fragments get no header. One fragment per PR.
- Conventional commits; every commit body ends with
  `Stage-Release: S0 1` and then `Co-Authored-By: Claude <noreply@anthropic.com>` (09 §9.2.1 "The trailer"; check (d)
  refuses an untrailered commit). Every commit builds (rebase merges).
- Merge with `gh pr merge <n> --rebase` once the required checks of 09 §9.2.1 are green on an up-to-date branch;
  never squash, never `--admin`.
- Concurrency (CLAUDE.md): the guard runs once per boot over a fixed handful of keys; it adds no library-scale loop.
  The fixture-writing test is sequential by design.

## Planner notes

- The inventory row omits `internal/database/store.go`, which this brief edits for the served-ledger commit (09 §9.2.2
  row 2 places it after books' startup steps, which run in `InitializeStore`). No other W02 row edits `store.go`
  before PC-02-06, which follows this row.
- `TestReaderCheckMatchesRollbackScript` needs a fixture file; this brief keeps it inside `internal/database/testdata/`.
- Plan choices the spec leaves open, coined here and used by PC-02-02/03/04/05: the exact `--print-upgrade-floor`
  text for an empty floor (`{"release":""}`; 05-updater says "an empty output", 05-storage "an empty requirement"),
  the `boot levels=` separator, the owner set `{books, core}`, the `install_id` format and `epoch 0` before S2b.
  PC-01-07's `soak_record.py` must parse the same `boot levels=` text.
- The S0 guard applies 05-storage §5S.3.4 rule 4's empty-range check (it is "whichever build first writes under
  `c/`"), so `TestLegacyToleratesNamespacedKeys` seeds its keys after the marker exists.
- S2b's `codec.JSON[T](1)` must decode the hand-encoded S0 values; `TestS2bOpensStoreWrittenByS0` over the committed
  fixture is the check.
