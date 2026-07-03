<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-03-booksig-recovery-audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 34f759cc-a00c-4e92-b4d4-fd132421fe0b -->
<!-- last-edited: 2026-07-03 -->

# TASK-03 — BookSig/Description recovery audit from `book_ver:` snapshots (dry-run op)

**Priority:** P0 · **Effort:** M · **Recommended subagent:** Opus · **Wave:** 2 · **Depends on:** TASK-02

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-03-booksig-recovery-audit" -b agent/cr-03-booksig-recovery-audit origin/main
cd "$REPO/.worktrees/cr-03-booksig-recovery-audit"
git rebase origin/main
```

**Before starting:** confirm TASK-02 (memdb preserve guard on `UpdateBook`) has
already merged to `origin/main` and this worktree includes it (`git log
--oneline -5 -- internal/database/pebble_store.go` should show its commit).
TASK-02 stops the *future* wipe; this task only reads existing history and
does not touch `UpdateBook` itself, but it shares `pebble_store.go` so it must
be based on top of TASK-02 to avoid a painful rebase, and because sizing "how
much damage already happened" is only meaningful once the leak is closed.

## Goal

Build a **read-only, dry-run** maintenance op that scans all books, finds the
ones whose current row is missing `Description` and/or `BookSigV1` while an
older `book_ver:` CoW snapshot for that same book still has them, and reports
counts + concrete examples. This sizes the blast radius of the STOR-1 memdb
full-replacement wipe (docs/consultancy/01-storage-architecture.md) using the
exact recovery mechanism the advisor pass identified: `UpdateBook`'s
pre-overwrite CoW snapshot preserves the un-wiped book, so damage is
recoverable **provided it is recovered before STOR-2's snapshot pruning is
ever enabled.**

This task's acceptance criteria stop at a dry-run report. Building/wiring an
apply mode is optional scaffolding (owner-gated, off by default) — see
"Apply mode" below. **Do not run apply mode against prod without the owner's
explicit greenlight**, exactly like the M0 purge and CONS-10 precedents.

## Background (verify before editing)

- The consultancy finding (STOR-1, `docs/consultancy/01-storage-architecture.md`)
  and its advisor addendum are the source of this task:
  > Because `GetBookByID` is Pebble-direct, `UpdateBook` reads the full
  > unstripped old row before overwriting and writes it to the `book_ver:` CoW
  > snapshot. Existing wipe damage is therefore recoverable from snapshots —
  > the severity of *permanent* loss is bounded, but only while those
  > snapshots survive. Sequence any prod recovery of wiped Description/BookSig
  > fields before implementing STOR-2's snapshot pruning.

- **Re-verify every anchor below before writing code** — the consultancy doc
  is dated 2026-07-02 and line numbers drift:
  ```bash
  grep -n "func (p \*PebbleStore) UpdateBook\|book_ver:\|func (p \*PebbleStore) GetBookSnapshots\|func (p \*PebbleStore) GetBookAtVersion\|func (p \*PebbleStore) PruneBookSnapshots\|func (p \*PebbleStore) GetAllBooks\b\|func (p \*PebbleStore) ListBookIDs\b" internal/database/pebble_store.go
  ```
  Confirmed at time of writing (audiobook-organizer-roadmap-tasks worktree,
  2026-07-03):
  - `UpdateBook` — `internal/database/pebble_store.go:2664`. It snapshots the
    **old** (pre-overwrite) book JSON to key `book_ver:<id>:<unixnano>`
    (`:2688-2701`) before writing the new row to `book:<id>`. This is the
    recovery source.
  - `GetBookSnapshots(id string, limit int) ([]BookSnapshot, error)` —
    `:3039`. Prefix-scans `book_ver:<id>:`, returns newest-first, `limit<=0`
    means "all". Reuse this — do not hand-roll another `book_ver:` iterator.
  - `GetBookAtVersion(id string, ts time.Time) (*Book, error)` — `:3080`.
    Point-reads one snapshot by exact timestamp. Reuse this for the apply path.
  - `PruneBookSnapshots(id string, keepCount int) (int, error)` — `:3109`.
    **Do not call this from the new op.** It exists but per STOR-2/advisor
    sequencing this task must not enable or trigger pruning in any form.
  - `GetAllBooks(limit, offset int) ([]Book, error)` — `:1391`. In prod
    (`UseMemDB=true`) this routes through the **memdb-stripped** projection
    (STOR-1's `stripBookForMemdb`, `internal/database/memdb_strip.go:29`),
    so the "current" book fetched via `GetAllBooks` will *always* show
    `Description`/`BookSigV1` as nil regardless of whether the underlying
    Pebble row actually has them. **Do not use `GetAllBooks` to read the
    "current" row for this audit** — it would report false positives for
    every book, memdb-stripped or not. Use `GetBookByID(id)` instead, which
    is Pebble-direct (`:1691`, confirmed by the advisor pass) and reflects
    the true on-disk state. Use `ListBookIDs() ([]string, error)` (`:1532`)
    or `GetAllBooks` (memdb-backed, fine for *enumeration only*) purely to
    get the list of book IDs to iterate, then call `GetBookByID` per ID for
    the actual field check.
  - Book fields: `Description *string` (`internal/database/store.go:133`),
    `BookSigV1 *string` (`:202`), `BookSigBuiltAt *time.Time` (`:204`),
    `BookSigV1Mask *string` (`:205`). The consultancy report's recommended
    damage-sizing signal is: **`BookSigBuiltAt` set (non-nil) but `BookSigV1`
    nil** — this means a signature was built at some point but the current
    row no longer carries it, i.e. it was wiped after being computed. Also
    separately count/report `Description == nil` cases (Description has no
    "was it ever set" marker field, so those are reported as raw counts +
    samples, not cross-checked against a built-at marker).
  - Maintenance-op pattern to copy verbatim: read
    `internal/plugins/maintenance/duration_backfill.go` in full. It is the
    closest existing analog: `sdk.OperationDef` with a `DryRun bool` param
    defaulting to `true`, paginated `GetAllBooks(pageSize, offset)` scan,
    time-batched heartbeat logging (`logInterval`), and a dry-run branch that
    only counts + samples with **zero writes**. Follow its shape, not its
    duration-specific logic.
  - Plugin registration: `internal/plugins/maintenance/plugin.go` — add the
    new op's `...Def()` method to the `defs := []sdk.OperationDef{...}` slice
    inside `func (p *Plugin) Register(r sdk.Registry) error` (currently
    `:32-100+`; re-verify with
    `grep -n "func (p \*Plugin) Register" internal/plugins/maintenance/plugin.go`).
    Add it near `p.durationBackfillDef()` / `p.titleBackfillDef()` under a new
    `// --- booksig/description recovery audit ---` comment group.

## Step-by-step

1. Re-run all `grep` commands in "Background" and confirm every anchor before
   writing any code. If TASK-02's preserve-guard is not yet present in
   `UpdateBook`, STOP and escalate — this task assumes it merged first.
2. Create `internal/plugins/maintenance/booksig_recovery_audit.go` modeled on
   `duration_backfill.go`:
   - Define `bookSigRecoveryAuditParams struct { DryRun bool \`json:"dryRun"\` }`
     defaulting to `true` in the `Run` function (safe default, same pattern).
   - Define an `sdk.OperationDef` (e.g. `ID: "maintenance.booksig-recovery-audit"`)
     with `DefaultPriority: sdk.PriorityLow`, `Capabilities:
     []sdk.Capability{sdk.CapLibraryRead}` for the dry-run path (read-only —
     no `CapLibraryWrite` needed unless you implement apply mode; if you do,
     gate the write capability declaration behind whether apply mode exists
     in the def, or simply keep this task read-only and ship apply as a
     documented follow-up — see "Apply mode" below for the recommended
     minimal-scope choice).
   - Enumerate all book IDs via `ListBookIDs()` (or paginated `GetAllBooks`
     purely for ID enumeration — either is fine since IDs are memdb-safe;
     memory-bounded either way per existing convention: page in batches of
     500, do not load the whole ID list into one huge unbounded slice if
     `ListBookIDs` returns everything at once — check its actual signature
     first; if it already returns `[]string` for the whole library in one
     call, that is the existing convention (books are ~50K, ~a few MB of
     strings) and is acceptable, but do NOT additionally materialize full
     `Book` structs for the whole library at once — fetch full books one ID
     at a time or in small batches via `GetBookByID`).
   - Per ID: `GetBookByID(id)` for the current row. If `Description == nil`
     OR (`BookSigBuiltAt != nil AND BookSigV1 == nil`), it's a candidate for
     recovery — call `GetBookSnapshots(id, 0)` (all snapshots, newest-first)
     and scan for the newest snapshot where the missing field(s) are
     *present*. Record: book ID, title, which field(s) missing, whether a
     recoverable snapshot was found, and the snapshot's timestamp if found.
   - Time-batch heartbeat logging exactly like `duration_backfill.go`'s
     `heartbeat` closure (15s interval, small example ring buffer, ≤5
     examples per heartbeat) — do not log per-book at INFO level across 50K
     books.
   - Dry-run report (the only mode this task must ship): total books
     scanned; count with `Description` missing; count with `BookSigV1`
     missing-but-was-built; of each, count with a recoverable snapshot found
     vs. not found (i.e. wiped-and-then-the-snapshot-itself-later-pruned, or
     never had one, e.g. new books); a handful of concrete examples
     (`book_id`, title, missing field(s), recoverable snapshot timestamp or
     "no recoverable snapshot").
   - Respect `ctx.Err()` in the per-ID loop (same convention as
     `duration_backfill.go`) so the op is cancellable.
3. **Apply mode (owner-gated, minimal scope):** implement it only if trivial
   to add without expanding this task's blast radius. If implemented:
   - Require `params.DryRun == false` explicitly (never default to apply).
   - For each recoverable book, copy only the missing field(s)
     (`Description`, `BookSigV1`, and its siblings `BookSigV1Mask`,
     `BookSigSegments`, `BookSigBuiltAt`, `BookSigCoveragePct` if restoring
     the signature) from the newest snapshot that has them onto the current
     book, then call `UpdateBook` — this itself writes yet another CoW
     snapshot of the (still-partially-wiped) pre-restore state, which is
     harmless and consistent with existing behavior.
   - Batch writes conservatively (e.g. one `UpdateBook` call per book, no bulk
     bypass) since this is a low-frequency, one-time healing op, not a
     hot path.
   - If apply mode feels like scope creep, ship dry-run only and note apply
     as a documented follow-up in the op's `Description` string and in this
     brief's PR description — that satisfies this task's acceptance criteria,
     which stop at the dry-run report regardless.
4. Register the new op in `internal/plugins/maintenance/plugin.go`'s
   `Register` method (see Background for the exact insertion point).
5. Add `internal/plugins/maintenance/booksig_recovery_audit_test.go` modeled
   on `duration_backfill_test.go`'s structure (or the lighter
   `title_backfill_test.go` harness if it's a better fit — check both,
   re-verify with `grep -n "newTestPlugin\|newReextractPlugin\|database.MockStore" internal/plugins/maintenance/*_test.go`).
   Cover:
   - A book with `Description == nil` and a snapshot containing a
     non-nil `Description` → reported as recoverable with correct
     snapshot timestamp.
   - A book with `BookSigBuiltAt` set, `BookSigV1` nil, and a snapshot
     with `BookSigV1` present → reported recoverable.
   - A book with `BookSigBuiltAt` nil (signature never built) → NOT
     flagged as a BookSig-missing candidate (it isn't missing anything;
     it never had one).
   - A book with `Description == nil` and NO snapshot at all (new book,
     never updated) → reported as missing but NOT recoverable (no
     snapshot exists).
   - `DryRun: true` (default) → op returns `nil` with a summary log/progress
     update, and the mock store records **zero** `UpdateBook` calls.
6. Bump the file header (version bump + `last-edited`) on every file you
   touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/plugins/maintenance/... -run BookSigRecoveryAudit -count=1
go test ./internal/plugins/maintenance/... -count=1
go vet ./internal/plugins/maintenance/...
```

## Acceptance criteria

- [ ] New op `maintenance.booksig-recovery-audit` (or equivalent ID) is
      registered and runnable via the existing UOS op-registry path.
- [ ] Dry-run mode (default) never calls `UpdateBook`, `PruneBookSnapshots`,
      or any other write path — verified by a mock-store assertion in tests.
- [ ] Report distinguishes `Description`-missing from `BookSigV1`-missing
      (using the `BookSigBuiltAt`-set-but-`BookSigV1`-nil signal), and for
      each, distinguishes recoverable (a snapshot with the field present
      exists) from not-recoverable (no such snapshot).
- [ ] `GetBookByID` (Pebble-direct) is used for the current-row check, NOT
      `GetAllBooks`'s memdb-stripped projection — verified by code reading
      and/or a test proving a memdb-stripped-but-actually-intact book is not
      falsely flagged.
- [ ] The op never calls `PruneBookSnapshots` or otherwise deletes/reduces
      `book_ver:` snapshots — this task's whole point is preserving the
      recovery source until STOR-2 pruning is separately approved.
- [ ] Iteration is memory-bounded: full-`Book` structs are not all held in
      memory simultaneously for the whole library (paginated/batched, per
      existing `duration_backfill.go` convention).
- [ ] Tests cover all four scenarios in step 5; `go test
      ./internal/plugins/maintenance/...` is green; `go vet` is clean.
- [ ] File headers bumped on every changed file.
- [ ] **Acceptance stops here.** Applying recovered fields to prod (even if
      apply mode is implemented per step 3) requires the owner's explicit
      greenlight and is NOT part of this task's Definition of Done — do not
      run apply mode against the production database as part of this task.

## Commit message

```
feat(maintenance): add dry-run BookSig/Description recovery audit from book_ver snapshots (STOR-1/STOR-2)

UpdateBook's memdb-stripped-Book full-replacement bug (STOR-1) has silently
wiped Description and BookSigV1 dedup signatures on every Book touched by
GetAllBooks-then-UpdateBook read-modify-write paths (reconcile, migrations,
quarantine, merge). The advisor pass confirmed the damage is recoverable from
UpdateBook's own book_ver: CoW snapshots, but only before any snapshot
pruning runs. Add a read-only dry-run op that sizes existing damage and
identifies which books have a recoverable snapshot, so recovery can be
sequenced before STOR-2 pruning work ever starts. Apply mode is intentionally
owner-gated and out of this task's acceptance scope.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-03-booksig-recovery-audit
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/plugins/maintenance/booksig_recovery_audit.go` already exists
and is registered in `plugin.go`'s `Register`, this task is done — verify
with `grep -rn "booksig-recovery-audit\|BookSigRecoveryAudit" internal/plugins/maintenance/`.

If, when you check, `UpdateBook` no longer routes reads through
`GetBookByID`/the memdb-stripped path at all (e.g. TASK-02 or a later
refactor changed the read path so Description/BookSigV1 are never stripped
before `UpdateBook` sees them), the underlying STOR-1 wipe may already be
fully closed for *future* writes — but this task is about **past** damage
already committed to disk, which still needs sizing regardless of whether
the leak is closed going forward. Do not skip this task solely because
TASK-02 merged; only skip it if the audit itself already exists.

Rollback = revert the commit. The new op is additive (a new `OperationDef`
registration) and read-only in its required (dry-run) mode, so reverting is
safe with no data-migration concerns. If apply mode was implemented and run,
rollback of *data* changes it made would itself need to go through
`RevertBookToVersion`/`GetBookAtVersion` against the pre-apply snapshot —
another reason apply mode is out of this task's required scope.
