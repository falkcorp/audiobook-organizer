<!-- file: PLAN.md -->
<!-- version: 1.0.0 -->
<!-- guid: a3e90137-2ca9-4148-9870-411a5cfa3283 -->
<!-- last-edited: 2026-09-30 -->

# Per-book scan lock: replace the 409 SCAN_RUNNING gate on the request paths

Branch `feat/scan-per-book-lock`, cut from origin/main at `1ec4cc4e0`.

## 1. Goal

A metadata apply or write-back from the UI must never get "a library scan is
running; try again when it finishes". Today six request handlers refuse with
409 `SCAN_RUNNING` the moment any scan is claimed or running.

Replace that library-wide, no-wait refusal with per-book coordination:

- The scanner locks the **book row(s)** it is working on, one book at a time,
  inside its per-book worker.
- An apply or write-back on **that** book waits for the scanner to move on.
  If the wait exceeds a bound, the work is handed to a durable queued op.
- An apply on **any other** book proceeds at once, with no warning.
- The scanner never blocks on an apply either. A busy book goes to the back of
  the chunk; the scanner only waits for it on the final pass.

Out of scope, and unchanged:

- The WAITING stand-down used by bulk and maintenance ops (inventory in §9).
- `library.scan`'s ConcurrencyKey.

## 2. Verified facts the design rests on

Every fact below was read at `1ec4cc4e0`.

1. **The clobber mechanism.** `saveBookToDatabase` (internal/scanner/scanner.go
   ~3183) merges a rescan with
   `ModifyBook(existing.ID, func(cur) { applyScannerFields(cur, dbBook, locked) })`.
   Scanned values win whenever they are non-empty. `dbBook` is built from tags
   read **before** saveBook runs, in `ProcessBooksParallel` (~1435-1760, via
   `ProcessFileWithTimeout` or `AssembleBookMetadata`). An apply that commits
   between that tag read and the merge is overwritten with the pre-apply tag
   values. Nothing reports it, because the write itself succeeds.
2. **The existing lock guard does not protect applies.** `lockedFieldsForBook`
   honours only user overrides (`OverrideLocked` / `OverrideValue`). An apply
   records `FetchedValue` (`persistFetchedMetadata`,
   `updateFetchedMetadataState`), so applied fields are unguarded.
3. **`MetadataUpdatedAt` is never written by the Pebble store.**
   `UpdateBook` sets only `UpdatedAt`. It cannot serve as a
   "metadata changed" version.
4. **Per-book hold time can reach minutes.** Inside one book the worker does:
   - `ProcessFileWithTimeout`, capped at `processFileTimeout = 120s`
     (process_file.go:80). Normally it takes well under a second, since it
     reads tags, media info and a hash.
   - `ComputeFileHash`.
   - `createBookFilesForBookLimited`, which probes every segment, so a
     500-file directory book takes tens of seconds.
   - `PersistChaptersForBook`.

   So the common case is seconds, but the worst case is past the owner's 60 s
   bound. The handoff in §5.3 is required, not optional.
5. **Auto-organize never overlaps a per-book hold inside one scan.**
   - `AutoOrganizeFn` (the hook that reaches `PerformOrganize` and its
     `VersionGroupLocker`) runs in `scanFolder` only after `processBookChunks`
     has returned for that folder (service.go:607).
   - Folders are scanned sequentially (service.go:383).

   Every per-book lock is released by the worker's defer before
   `ProcessBooksParallel` returns. A test pins this (§7, T9).
6. **The scanner never touches `writeBackPathLocks`.** Everything saveBook
   calls out to stays scanner-internal or hands off async:
   - `handOffLinkedGroup` uses the scanner's version-link stripes.
   - `OnBookScanned` writes an activity row.
   - `OnImportDedup` starts `bgWG.Go` (async).
   - `followSyncIdentityOnVersionLink` stays in-package.
7. **Move case (a) creates a ghost row today.** Suppose an apply renames P to Q
   while P sits in the scan's walk list.
   - `classifySkipBook` cannot stat P, so it counts a re-read.
   - `ProcessFile` fails and the filename fallback runs.
   - saveBook finds no row at P and no hash.
   - `CreateBook` then mints a row for a file that no longer exists.

   The scanner itself never marks rows missing; that is the separate
   `mark-missing-files` op. A red test comes first (T6).
8. **Move case (b) is real.** `ensureLibraryCopy` runs `OrganizeOneBook`, which
   lands the files under root_dir, **before** `CreateOrganizedVersion` writes
   the copy's row. A scan that reaches the new path in that window:
   - finds no row by path;
   - finds the ORIGINAL by content hash, because the copy has the same hash
     before its tags are rewritten;
   - takes the "Promoting organized path" branch, which overlays onto the
     original with `FilePath` = the copy's path;
   - leaves `CreateOrganizedVersion` to create a second row at the same path.

   Today the request gate keeps apply-driven copies out of a scan.
   `library.organize` has its own ConcurrencyKey (`"library.organize"`) and no
   scan coordination, so for organize this gap **already exists** (§8, O2).
9. **The AI-phase re-save is also a merge.** `ai_parse_async.go:715` calls
   `saveBook` again, after the main loop.

## 3. Lock identity and the new table

**New package `internal/scanlock`.** It holds one process-wide keyed-mutex
table, `scanlock.Books`, keyed by **book ID**.

- Entries are refcounted and deleted on the last release, like `pathLocks`.
- **It is never striped.** "Only block if they interact" requires that two
  different books never share a lock.
- Waits are channel-based, so they respect a context.
- It has a trace hook for tests (`+scan:<id>` / `-scan:<id>`, with a holder
  label).

API:

- `LockSet(ctx, ids) (*Hold, error)`: dedupes, sorts, then acquires in order.
- `TryLockSet(ids) (*Hold, bool)`: all or nothing.
- `Hold.Release()`, which is idempotent.
- `Hold.Retain() (done func())`: hands the hold to a background job.
  `Release` then returns only the caller's share.
- `Hold.IDs()`.

**Why a new table instead of an existing one:**

- **`writeBackPathLocks` `book:<id>` keys.** `FinishApplyFileWork`,
  `WriteBackMetadataForBook` and the other metafetch entry points take that
  key themselves, and the table is not reentrant. A handler holding it across
  those calls self-deadlocks. The scanner package also cannot import `server`.
- **Pebble `bookLocks`, the scanner's `lockBookPath` and the version-link
  stripes.** All are **striped** (a fixed array hashed by key). Holding one
  for seconds would block unrelated books that share the stripe, and they are
  documented as leaf locks held across a read-modify-write only.

**Scanner lock set for one book, resolved before the skip decision and the tag
read:**

- The row at `FilePath` (`GetBookByFilePath`).
- For a multi-file book, the row at the containing directory, plus the owner
  of the first segment's book_file row.
- **Every member of each found row's version group**
  (`GetBooksByVersionGroup`).

Locking the version-group members lets an apply lock only its own book ID and
still exclude the scanner from that book's library copy, in either direction:

- The scanner reaching copy S locks {S, X}.
- The scanner reaching original X locks {X, S}.

**The cap.** If a group has more than 32 members, lock only the rows found by
path, and increment a counter. Step 0 measures the largest group in prod
before the cap is fixed.

**Apply lock set:** the book ID the request names. For the per-book loops in
multi-book handlers, that is each book in turn. Nothing else is needed,
because the scanner brings the group.

## 4. Lock order and the no-deadlock argument

- **L0: `scanlock.Books`.** Outermost.
- **L1: `writeBackPathLocks`.** Its keys are ordered `book:` → `vg:` (a leaf) →
  `book:<copy>` → path, as metafetch `lockBook` documents.
- **L2: store and scanner stripes.** These are pebble `bookLocks`,
  `lockBookPath`, the version-link stripes and `ModifyBook`. Innermost.

Rules:

- **R1.** Any L0 acquire that can block is a whole sorted set, taken by a
  goroutine that holds **no** L0 key. Adding a key while holding others is
  only ever `TryLockSet`. If that fails, release everything and restart the
  unit of work with the larger set pre-locked (§5.1).
- **R2.** No goroutine holding an L1 or L2 key ever takes an L0 key.
- **R3.** The scanner never takes L1.
  - Apply holders take L1 only through metafetch, under their L0 hold.
  - `PerformOrganize` and auto-organize never run under an L0 hold (fact 5).

**Why this cannot deadlock:**

- Among L0 holders, only goroutines holding nothing ever wait, so no cycle can
  form inside L0.
- L0 → L1 → L2 is the only nesting direction: R2 forbids the reverse, and
  metafetch documents L1's internal order.
- A Retained hold is owned by the pool job, which then takes L1. That is still
  L0 → L1.

## 5. Changes, in order

### 5.1 Scanner (internal/scanner)

1. **Per-book span.** In the `ProcessBooksParallel` worker, before the
   incremental skip check:
   - resolve the lock set (§3);
   - `TryLockSet`. If busy, append the index to a chunk-local `deferred` list
     and return;
   - after `wg.Wait`, process `deferred` with a blocking `LockSet(ctx)`.

   So a scanner worker never idles on an apply unless nothing else is left.

   **After acquiring:**
   - **Re-resolve** the set. A changed set means release and retry (at most 3
     times, then record a `FileFailure{Stage: lock}` and move on; the next
     scan reconciles).
   - **Stat** `FilePath` and every `SegmentFiles` entry. If any has vanished,
     skip the book: count `vanishedMidScan` and create or modify nothing (move
     case a).
   - **Snapshot** each locked row's guarded values: Title, AuthorID, SeriesID,
     SeriesSequence, Narrator, Language, Publisher, ASIN, plus FilePath. Store
     the snapshot on the `Book` as an unexported `rowSnaps map[id]snapshot`.

   The hold covers the suspicious-file save, the directory-book save and the
   generic save, plus `createBookFiles`, chapters and the scan-cache stamp.
   It is released by `defer` at the end of the worker.
2. **The hold reaches saveBook through `ctx`** (`scanlock.WithHold`).
   saveBook names every row it discovers late and checks it against the hold.
   Late rows are:
   - hash-dup `existing`;
   - `existingByOrgID`;
   - segment-vote `existing`;
   - the raced row after `lockBookPath`;
   - the owner in `checkFileOwnership`.

   If a late row is not held, saveBook returns `errScanLockWiden{ids}` before
   any write. The worker releases everything and restarts the book with those
   IDs added to its pre-lock set. That restart is the only case that re-reads
   tags. It happens only for new files that match an existing row, which is
   move case (b) and genuine imports of a known file.
3. **Changed-since-read wins (the safety net).** In the rescan merge callback,
   for each guarded field whose `cur` value differs from the snapshot for that
   row, keep `cur` and do not overlay the scanned value. Log once per book at
   Info ("kept N fields another writer changed during the scan").

   If `cur.FilePath` differs from the snapshot, return `ErrSkipBookWrite`: the
   row moved under the scan.

   This covers writers that do not take L0 (maintenance ops, AI parse, the
   write-back batcher) and is independent of the lock. With no snapshot
   (a row created during this scan), there is no change to detect.
4. **The AI-phase re-save** (`ai_parse_async.go:715`) takes the same
   per-book L0 set around its saveBook, blocking with R1 respected. It
   reuses the main-loop snapshot carried on the `Book`, so a user apply
   between the main pass and the AI save is kept.
5. **No L0 across auto-organize.** A comment at `AutoOrganizeFn` plus test T9.

### 5.2 Request handlers: replace the 409 gate

Delete these, since they have no callers left after this change:

- `holdScanStandDown` (handlers/metadata/scan_standdown.go);
- `holdScanStandDownForRequest` (server/metadata_scan_standdown.go);
- `SetScanStandDownGate` and its wiring (wire_handlers.go);
- `registry.TryAcquireScanStandDown`, `TryHoldScanStandDown`,
  `ScanStandDownTryGate`, `ScanStandDownRequestGate` and `ErrScanRunning`,
  with their tests.

`scanGate()` / `metadataScanGate` shrink to the blocking gate the ops still
use.

Per handler:

| Handler | New behaviour |
|---|---|
| `apply-metadata` (single) | `LockSet(ctx60s, {id})` before `RenamePreflight` / `ApplyMetadataCandidate`. `Retain` into the pool job, which releases after `FinishApplyFileWork`. Pass `checkpoint=nil`. |
| `write-back` (single) | Lock `{id}` around `WriteBackMetadataForBook`. |
| `fetch-metadata` (single) | The provider fetch runs unlocked. Lock `{id}` only around the apply/write part. |
| `batch-update-metadata`, `bulk-fetch-metadata`, `batch-apply-candidates` | Per book: `TryLockSet({id})`; if busy, defer it to the end of the loop. The deferred books then get a `LockSet` with a shared 60 s budget. Books still busy are handed off (§5.3). candidates Retains per book into its pool job. The per-book `hold.Checkpoint()` beats go. |

### 5.3 Bounded wait, then a durable handoff

**The new op `metadata.apply-when-scanned`** is registered in `server`:

- params: `{kind, book_id, body}`;
- **not** a stand-down holder, so it never parks a scan;
- its ConcurrencyKey is per book: `metadata.apply-when-scanned:<id>`;
- it runs `LockSet(ctx)` with no bound, then the same gin-free core the
  handler runs.

For that, each handler's body is split into `xxxCore(ctx, id, body) (resp, error)`.

**What the handler returns.** When its bound expires, the single handler
enqueues the op and responds **202**:

```
{ queued: true, operation_id, book, message: "The scanner is reading this book right now; your change will be applied as soon as it moves on." }
```

`book` is the current row, so the TS consumers that read `book` keep working.

A multi-book handler returns 200 as today, plus `queued_book_ids` and
`operation_id`. It hands off only the books still busy.

**Why durable.** An in-memory goroutine would lose an apply the UI already
reported as "queued" if the process restarted.

**UI.** Search web/src for anything that treats 409/`SCAN_RUNNING` specially.
There is none today: the refusal surfaced through the generic error path.
The consumers are in `web/src/services/api.ts`:

- ~3702 fetch-metadata;
- ~3762 apply-metadata;
- ~3814 write-back;
- ~3941 bulk-fetch;
- ~4828 batch-apply-candidates.

Make each one accept 202 with `queued` and show an **info** toast, not a
warning or error. The apply dialog closes as it does on success.

## 6. The orderings and move cases: what is guaranteed

Here X is the book being applied, and "file job" means the apply's background
tag write and rename.

| # | Ordering | Handling | Guaranteed? |
|---|---|---|---|
| O-before | The apply (DB write **and** file job) finishes before the scanner reaches X | The scanner reads the new tags, so the merge is consistent. | Yes, **when a tag write happened** (see gap G1). |
| O-before-pending | The apply DB write lands and the file job is still pending when the scanner reaches X | The apply holds L0 {X} from before its DB write until the file job finishes (Retain), so the scanner waits and then reads the new tags. | Yes. |
| O-during | The scanner holds X (it has read tags and not yet merged) when the apply arrives | The apply waits (60 s), or hands off to the op. It never writes while the scanner holds X. Separately, §5.1.3 keeps any unlocked writer's changes. | Yes. |
| O-after | The scanner finished X before the apply | No interaction; the apply proceeds at once. | Yes. |
| Move (a) | The apply renamed P → Q while P is still in the walk list | The scanner re-resolves after locking, stats P, finds it gone, and skips it (`vanishedMidScan`). No ghost row is created and nothing is re-imported. Q is scanned normally if the walk listed it. | Yes (T6 red first). |
| Move (b) | An apply-driven library copy lands on disk before its row exists | The apply holds L0 {X}. The scanner at the copy's path finds X late, by hash, and restarts the book with X pre-locked, which waits for the apply. On the restart the path has row S, and the merge lands on S. | Yes for apply and write-back copies. **Not** for `library.organize` (O2). |

**Gap G1 (pre-existing; this change neither causes it nor fixes it).** When no
tag write happens, the file keeps its old tags. That covers:

- `write_back=false`;
- no file-IO pool;
- a protected book with no copy under `existingCopyOnly`;
- a failed tag write.

The next scan that re-reads the file overlays those old tags onto the applied
row. Multi-file books are re-read on **every** scan (the per-book scan-cache
grain mismatch in the worker comment), so they are hit hardest.

- **Fix option.** Have apply record the applied values as the scanner's
  "do-not-overlay" set: a per-field `applied_at` in metadata_field_state, which
  `applyScannerFields` consults like a lock until a later tag write clears it.
- **Cost.** One field-state column plus the writer (apply, 3 call sites), the
  reader (`lockedFieldsForBook`) and the clearer (`WriteBackMetadataForBook`
  success), about 150 lines plus tests.

**This is the owner's call. It is not in this PR.**

## 7. Tests (all `go test -race`, targeted packages)

- **T1: scanlock unit tests.**
  - Keyed, not striped: two IDs never block each other.
  - Sorted-set acquire.
  - `TryLockSet` is all or nothing.
  - A ctx timeout releases partial acquisitions.
  - Retain/Release refcount.
  - The map is empty after the last release.
- **T2: seam hook.** Add a package-level test hook `afterTagRead(book)` in the
  worker, between the tag read and saveBook, to drive each ordering
  deterministically.
- **T3: O-during.** The hook blocks the scanner holding X. A concurrent apply
  core (fake metafetch) waits. Release: the apply's values survive and the
  apply unblocks. Also assert that an apply on book Y finishes while X is held.
- **T4: O-before-pending.** The apply holds L0 {X} after its DB write, with
  its file job gated. The scanner is deferred to the final pass and waits.
  The file job writes the tags and releases, and the scanner merges the new
  values.
- **T5: the safety net.** An unlocked `ModifyBook` changes Narrator between the
  tag read and the merge. The narrator survives and other fields still
  overlay. A changed FilePath means no write.
- **T6: move (a).** **Red first:** at `1ec4cc4e0`, a walk-listed path deleted
  before processing mints a ghost row. Green: skipped, `vanishedMidScan` = 1,
  no row.
- **T7: move (b).**
  - A copy file whose hash equals X's appears before its row, while L0 {X} is
    held. The scanner restarts with X, waits, and on release finds row S. X's
    FilePath is unchanged and there is exactly one row at the path.
  - Plus a control with L0 not held, documenting the pre-existing organize
    gap as a skipped/`t.Log` case, not asserted.
- **T8: lock-order trace.** One combined trace across `scanlock.Books` and
  `writeBackPathLocks`, with events tagged by goroutine-local holder labels.
  Assert:
  - no `+scan:` event from a holder that currently holds any L1 key;
  - no L1 event in any scanner holder's trace;
  - blocking `+scan:` sets only from holders that hold no L0.

  Driven by a real single apply, a batch-candidates apply and a scan over
  overlapping books.
- **T9: no L0 across auto-organize.** An `AutoOrganizeFn` stub asserts
  `scanlock.Books` has zero held entries when it is called.
- **T10: handlers.**
  - No 409 `SCAN_RUNNING` from any of the six endpoints while a scan is
    registered running.
  - Bound expiry returns 202 with `queued` and enqueues exactly one op.
  - The op applies after the lock frees.
  - Multi-book handlers defer and then queue only the busy book.
- **T11: UI (Vitest).** The api.ts consumers accept 202 with `queued` and show
  an info toast. A grep guard test checks that no UI string contains
  "scan is running" for these paths.

Verification: `go build ./... && go vet` on touched packages, plus targeted
`go test -race`:

- `./internal/scanlock/...`
- `./internal/scanner/...`
- `./internal/server/...` (targeted `-run`)
- `./internal/server/handlers/metadata/...`
- `./internal/operations/registry/...`
- `npm run test -- api` in web.

## 8. Items for the owner (not in this PR unless approved)

- **O1: gap G1** above (applied values reverted by a later re-read when no tag
  write happened). About 150 lines.
- **O2: `library.organize` against a running scan** (move case b for organize
  copies, pre-existing). The fix is for `PerformOrganize`'s per-book path to
  take L0 {source book} before its `VersionGroupLocker` key and hold it
  through `OrganizeOneBook` + `CreateOrganizedVersion`. R2 allows this because
  L0 comes before L1. Batch-save's organize goes the same way. About 60 lines
  plus a test.
- **O3: the write-back batcher** (`wb.Enqueue`) writes tags asynchronously with
  no L0. The §5.1.3 net stops it causing a DB clobber, but it can rewrite a
  file the scanner is reading. The fix is to take L0 {id} per book in the
  batcher worker, about 20 lines.

## 9. WAITING stand-down callers left unchanged, with cost to convert

Every row below parks the running scan for the op's whole apply. Converting
means replacing the hold with per-book `scanlock.LockSet` in the op's per-item
loop, plus the §5.1.3 net already covers stray writes. "Cost" is that change
plus a test.

| Caller | Cost to convert |
|---|---|
| server `metadata.batch-apply-cached` (batch_apply_op.go:351) | Low: per-book loop exists; pass `LockSet` instead of `ScanStandDownCheckpoint`. |
| server `metadata.batch-save` (batch_save_op.go:156) | Medium: organize inside; needs O2 ordering first. |
| server `library.bulk-write-back` (library_writeback_op.go:115) | Low: per-book worker pool. |
| server `library.bulk-metadata-fetch` (metadata_ops.go:611, checkpoints 142/1107/1231) | Low-medium: three checkpoint sites. |
| scheduler `metadata-upgrade` (extra_ops.go:247) and `metadata-refresh` (extra_ops.go:969, checkpoint 1078); metabatch/upgrade.go:291/300 | Low: per-book loop. |
| metafetch isbn.go:384 (checkpoint under the isbn op) | Low. |
| maintenance `metadata-refresh` / `isbn-enrichment` (metadata.go:43/98) | Low. |
| maintenance `bulk-write-back` (write_back.go:62) | Low. |
| maintenance `acquireScanStandDownForApply` (16 ops: author-duplicate-merge, author-id-repair, author-path-link, purge-empty-authors, purge-empty-narrators, split-joined-narrators, series-phantom-repair, fs-regroup-xml, missing-file-repoint, repoint-missing-to-folder-audio, rewrite-path-prefix, recover-missing-files, mark-missing-files, move-book-file-rows, repoint-book-file-rows, itunes-clone-into-library) | Medium to high each: many touch book_file rows and paths across many books at once (author and series merges rewrite thousands of rows). Per-book locking would need each op's write set enumerated up front. Recommend leaving these parked; they are owner-triggered and rare. |
| repairs `AcquireStandDownWaiting` (engine.go:480, version_group_primary_repair.go:493) | Medium: fixer write sets are per group; the lock set is the group members. |
| dedup `series_dedup.go:396` | High: series merges span many books. Leave. |

## 10. Rollback

This is a single PR. Revert it to restore the 409 gate exactly:

- no schema change;
- the new op type is only registered, and a queued instance left after a
  revert fails as "unknown op" in the registry, visible in the ops list;
- the `internal/scanlock` package is self-contained.

A feature flag is not proposed. The old behaviour is the bug the owner wants
gone, and the revert path is clean.

## 11. Step 0 (before code)

- Measure the largest version group in prod (read-only API) to fix the §3 cap.
- Write T6 red and commit it.
