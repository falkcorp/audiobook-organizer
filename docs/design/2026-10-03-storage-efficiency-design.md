<!-- file: docs/design/2026-10-03-storage-efficiency-design.md -->
<!-- version: 0.5.0 -->
<!-- guid: 332dcbd9-73e2-4814-b1a0-723afa60e605 -->
<!-- last-edited: 2026-10-03 -->

# Storage efficiency redesign: store changes, not copies

Status: DRAFT v1, awaiting adversarial review and owner approval. No code has been written.

Evidence base: `.claude/notes/db-optimization-eval-2026-10-03.md` (raw data and
measurements, cited here as R1..R7 and F1..F13) plus three code scouts run on
2026-10-03 against `d1f069fac`. Every `file:line` below was grep-verified by a
scout on that commit. Re-verify before implementing; the plan carries the greps.

## 1. Problem

The main Pebble database holds 174,165,047 keys and 50.6 GB for about 77,000
live books (R1, R4). Three write patterns cause most of it, and each one also
costs time on every write:

1. **Every book save stores a full copy of the previous book.**
   `updateBookLockedMode` writes `book_ver:<id>:<nanos>` = the whole old row,
   signature included, with no check that anything changed
   (`internal/database/pebble_store.go:2812-2816`, `:2742`). Sample of 55
   books: 79.8 copies per book, 11 KB each, 80.4% of the bytes are a signature
   that is identical in 96.7% of copies, and 20.6% of copies differ from their
   neighbour only in `updated_at` (R5). Estimated 25-37 GB.
2. **Every file-record save rewrites the whole record and all its indexes.**
   `updateBookFileLocked` re-marshals the full row (fingerprint, tags,
   transcript), deletes and rewrites every secondary index including up to 129
   fingerprint-index keys, and triggers a book recompute, whether or not
   anything changed (`pebble_store_bookfiles.go:756`, `:840`, `:869`). Startup
   warmup reads 2,436 MB of file rows and discards 1,853 MB of it
   (`memdb_warmup.go:116`).
3. **Operations write one log row per progress update and nothing deletes
   them.** The "distinct message" filter never fires because messages contain
   counters (`reporter_db.go:391`). One scan wrote about 300,935 rows (R6). No
   code deletes `opv2:log:` rows except a manual per-operation discard
   (`pebble_store_ops_v2.go:785`).

Around those sit read paths that scan everything to answer a small question:
the timeline decodes every operation row per call (5.5 s, R4), db-health
iterates all 174M keys (5 min), and Pebble itself runs on library defaults
(8 MB cache, no bloom filters, one compaction at a time; R2).

## 2. Goals and non-goals

Goals, in priority order (owner: "our biggest focus is areas that need optimization"):

- G1. A write that changes nothing writes nothing. A write that changes one
  field stores that field, not the record.
- G2. No request-path operation scans a whole key family to return a count or
  a page.
- G3. Every unbounded key family gets a retention rule.
- G4. Nothing that works today stops working: version restore, merge undo,
  signature recovery, repairs undo, the Audiobookshelf-compatible API.
- G5. Space falls as a consequence. Target after cleanup: main store under
  15 GB logical. This is an estimate until the census (section 9) confirms the
  per-family sizes.

Non-goals:

- Moving data out of Pebble (the metadata proxy was evaluated and dropped by
  the owner on 2026-10-03).
- Changing what the fingerprint index is for. Its key count is addressed only
  in the optional phase (section 12).
- Audio files. Nothing here reads or writes them.

## 3. Design principles

- P1. **One chokepoint per record type.** All writes of a book row go through
  one function that decides: skip, write row, write version entry. Same for
  file rows. A grep ratchet in CI fails any new raw `book:<id>` or
  `book_file:` Set outside the chokepoint.
- P2. **Types cannot carry what they must not wipe.** The repeated production
  incident class is "field was not loaded, got saved as empty" (fingerprints,
  signatures, descriptions). Guards exist for nine book fields and two file
  fields (`pebble_store.go:2753`, `bookfile_merge.go:211`). For data moved to a
  side entry, the field is removed from the main struct, so the bug cannot be
  written. Clearing becomes an explicit call.
- P3. **Cut over. No backward compatibility.** (Owner, 2026-10-03: "cut us
  over, convert the old, purge the old ... we cut off compatibility as long
  as we migrate to the new system.") There are no format flags, no dual-write
  period, no promise that an older build can run against the data, and no
  legacy format kept "forever". Each format change ships as one release that
  writes only the new format, converts every legacy record, verifies the
  conversion, and deletes the legacy records. The code that reads the legacy
  format exists only inside the converter and is deleted in the following
  release. The way back from a bad migration is the pre-migration backup, not
  an older binary. What stays is verification: a conversion that cannot prove
  it reproduced the old data exactly does not purge it (P4).
- P4. **Fail closed, verifiably.** A history entry that cannot be rebuilt
  exactly returns an error naming the entry. It never returns a best guess.
- P5. **Destructive steps follow the standing rule**: dry run, reviewable row
  list, apply by explicit ids, owner approval.
- P6. **Measure before and after.** Phase 0 builds the census; every later
  phase states the number it expects to move and is checked against it.
- P7. **Expensive derived data is never deleted by this work.** Audio
  signatures, fingerprints and transcripts cost processor time to produce;
  the owner keeps book and file records precisely so these survive (owner,
  2026-10-03). No retention rule, prune, conversion or delete path in this
  design removes the current signature, fingerprint or transcript of any book
  or file. Where a record that owns such data is removed, the data is kept
  and stays findable (sections 4.7 and 5.3).

## 4. Book history: change-only versions (replaces full copies)

### 4.1 What consumers need (verified)

| Consumer | Needs | Anchor |
|---|---|---|
| Version list UI | timestamps only; never decodes `data` | `web/src/components/MetadataHistory.tsx:111` |
| Restore to version | a complete Book at an exact nanos key | `pebble_store.go:3225` |
| Tag comparison at a timestamp | 17 scalar fields + author/series ids | `internal/audiobooks/helpers.go:416` |
| Automatic-merge undo | restore by nanos stored in the merge journal | `internal/dedup/auto_resolve.go:421`, `dedup_automerge_journal.go:32` |
| Merge baseline discovery | "first copy newer than baseline", assumes one copy per write | `auto_resolve.go:378`, `merge_journaled.go:133` |
| Signature recovery audit | newest history value of `description` and of the signature | `booksig_recovery_audit.go:108`, `:250` |
| Prune | keep newest N | `pebble_store.go:3320`, `prune_book_snapshots.go:45` |

Two facts shape the design. The nanos in the key is an external identifier
(the merge journal stores it), so the key format stays. And the merge code
depends on a side effect (every write makes a copy), which a no-op skip would
break, so that dependency is replaced with an explicit call.

### 4.2 Format

**Version ids: a per-book sequence number, not a timestamp.** Today the key
is `book_ver:<bookID>:<nanos>` and the wall-clock nanos is the identity. That
has three defects: two writes in the same nanosecond overwrite each other; a
clock step backwards mis-orders history (iteration is by key); and the decimal
nanos is not zero-padded, so ordering only holds while the digit count is
constant. New entries use a new family:

`bookhist:<bookID>:<seq, 10 digits zero-padded>`

`seq` starts at 1 per book and increases by one per entry; the next value
lives in `book_verhead`. The wall-clock time moves into the value (`"t"`), where
it is information, not identity. Consequences:

- Order is exact and collision-free regardless of the clock.
- Keyframe cadence is `seq % 20 == 0`; chain verification is "previous seq".
- The legacy family `book_ver:` is left untouched. An older build after a
  rollback simply does not see new entries; it cannot misread them.
- Every legacy entry is older than every `bookhist:` entry for the same book,
  so a combined listing is "new family newest-first, then legacy
  newest-first".
- External references by time keep working: the merge journal's
  `*PreMergeTS` values and the UI's restore-by-timestamp resolve through
  `ResolveBookVersion(id, nanos)`, which checks the legacy key directly and
  otherwise scans the book's (at most 50) new entries for a matching `"t"`.
  New journal entries store the seq.

The entry with sequence S still means "the book as it was just before write
S".

Value, by first byte:

- `{` : legacy full copy (JSON). Read forever, never written after the flip.
- `0x01` : **keyframe**. Rest is JSON `{"t": <nanos>, "h": <post-hash>, "pin": <reason or
  absent>, "row": <full stored row JSON, signature fields absent>}`.
- `0x02` : **change entry**. Rest is JSON `{"t": <nanos>, "h": <post-hash>, "set": {<json
  key>: <previous raw value>, ...}, "unset": [<json keys that were absent
  before>]}`.

Definitions:

- The **versioned projection** of a row is its stored JSON object
  (`book:<id>` value, which already excludes the six signature fields, see
  `pebble_store.go:2799`) minus the key `updated_at`.
- The **post-hash** of entry T is a 64-bit xxhash of the versioned projection
  of the row as written at T, computed over keys in sorted order.
- A change entry holds every top-level JSON key whose raw bytes differ between
  the row before T and the row written at T. This is a raw-JSON diff, not
  `ChangedBookFields` (which deliberately skips `author`, `authors`, `series`,
  `metadata_provenance` and timestamps, `book_field_render.go:33`). Embedded
  objects and timestamps other than `updated_at` are therefore covered.

Side key, one per book: `book_verhead:<bookID>` = `{"seq": <last sequence used>, "h": <post-hash of the
live row>}`.
Written in the same batch as the row. It lets the writer detect a broken chain
and decide keyframe cadence with one point read.

### 4.3 Reading a version

`reconstruct(id, T)`:

1. Seek the nearest entry K >= T that is a keyframe or a legacy copy. If there
   is none, the base is the live row. Otherwise the base is K's row.
2. For each change entry E with T <= E < K, newest first: check that the
   xxhash of the working state's versioned projection equals `E.h`; if not,
   return `ErrVersionChainBroken{book, at: E}`. Then apply `E.set` and
   `E.unset`.
3. Hydrate author/series display objects the same way `GetBookByID` does, set
   the signature from the signature history if the caller asked for it (4.6).

Cost is bounded by the keyframe interval N = 20: at most 19 small applies.

### 4.4 Writing

In the book chokepoint, with the book's write stripe held:

1. Read the stored row bytes and `book_verhead`.
2. Build the new row bytes. If the versioned projections are equal and the
   signature side entry is unchanged: **no-op**. Write nothing: no row, no
   `updated_at` bump, no version entry, no memdb write-through, no reindex, no
   change notification. Still run the two self-heal index ensures that today
   are unconditional Sets (`bookAtPathKey`, `book:versiongroup:`,
   `pebble_store.go:2876`, `:2960`) as "read, set only if missing".
3. Otherwise compute the diff. If `verhead.h` does not equal the hash of the
   stored row (some write bypassed the chokepoint), or the next seq is a multiple of 20, or
   there is no verhead: write a keyframe of the old row. Else write a change
   entry.
4. Stage row, version entry, verhead, signature side entry and indexes in the
   one batch that exists today.

`ErrSkipBookWrite` stays as a caller-side fast path. The three existing return
sites keep working (`pebble_store_book_aggregates.go:173`).

A new explicit call, `ForceBookRewrite(id)`, covers the case where a caller
truly wants the row and memdb re-written with no change (repair tooling).

### 4.5 Pins (replaces the side-effect dependency)

`PinBookVersion(id, reason) (seq, error)` writes a keyframe of the current
row with `pin` set and returns its sequence number. Restore to a pinned
version is a direct read. Merge code calls it for winner and loser before merging and
stores the returned nanos in the journal fields it already has
(`WinnerPreMergeTS`, `LoserPreMergeTS`). `newestSnapshotNanos` and
`preMergeSnapshotNanos` are deleted.

Existing journal entries point at legacy full copies. Those are self-contained
and stay readable. The conversion step (4.8) treats every nanos referenced by
an unresolved journal entry as pinned.

### 4.6 Signature history

Signatures leave version entries entirely. A new family
`book_sig_ver:<bookID>:<nanos>` stores the previous signature side entry, and
is written only when the side entry's bytes change (57 of 1,738 writes in the
sample, R5). Retention: newest 3 per book. `ClearBookSignature` writes one
before deleting the live side entry.

`booksig_recovery_audit` changes source: signature from `book_sig_ver:`,
description from a new helper `LastHistoryValue(id, jsonKey)` that walks
entries newest-first and returns the first non-null previous value. No
reconstruction is needed for that walk, because a change entry already holds
the previous value of any key it changed.

### 4.7 Retention and deletion

- Prune keeps the newest 50 entries per book plus all pinned keyframes. With
  reverse change entries, deleting the oldest entries never affects newer
  ones, so "keep newest N" stays correct. Entries are never deleted from the
  middle.
- Pins are released when their merge journal entry is finalized or after 90
  days, by the same nightly op.
- `DeleteBook` (hard delete) removes the change history (`bookhist:`, legacy
  `book_ver:`, `book_verhead:`) in its batch; today it leaves it forever
  (`pebble_store.go:3351`). It does **not** remove the signature: the live
  `book_sig:` entry and `book_sig_ver:` history move to
  `book_sig_kept:<bookID>` so the computed signature outlives the row (P7).
  Today `DeleteBook` deletes `book_sig:`. Whether hard delete should exist at
  all for books with computed signals is open question Q6.
- The prune runs nightly. Today it is manual and defaults to dry run.

### 4.8 Converting existing copies (space reclaim only)

Correctness does not depend on conversion: legacy copies are valid keyframes.
Conversion per book, under the book stripe, in one batch:

1. Read legacy entries newest to oldest.
2. For each, compute the change against the next newer state (the next entry's
   row, or the live row for the newest). Because the diff is taken between the
   two stored states directly, the result is exact even if unversioned writes
   happened between them.
3. Keep a keyframe for every 20th entry, for every pinned nanos, and for the
   oldest retained entry. Write change entries for the rest.
4. Before dropping inline signatures, write each distinct consecutive
   signature to `book_sig_ver:` (newest 3).
5. Apply retention (4.7).

The op is resumable (per-book atomic), parallel by book id (disjoint sets),
dry-run by default, and reports per book: entries before, after, bytes before,
after. It also has a verify mode: reconstruct every converted entry and
compare with the legacy copy it replaced, in memory, before the batch commits.
A mismatch skips the book and reports it.

### 4.9 API

- `GET /audiobooks/:id/cow-versions` returns `{timestamp, kind, pinned,
  changed_fields}` per entry, reverse-iterated so cost is O(limit) (today it
  loads every copy first, `pebble_store.go:3159-3193`). `data` is returned
  only with `?include_data=true`, reconstructed. The UI never read `data`.
- Store surface: `ListBookVersions`, `GetBookAtVersion`, `RevertBookToVersion`,
  `PinBookVersion`, `LastHistoryValue`, `PruneBookVersions`.
  `GetBookSnapshots` and `BookSnapshot.Data` are removed once the four callers
  move. New methods go behind a capability interface (`AsCapability`), not the
  wide `Store` interface (width ratchet).

### 4.10 Expected effect (estimate; arithmetic)

Stripped row is about 19.6% of 11.1 KB = 2.2 KB (R5). A change entry for the
commonest changes (duration, path, scan stamps) is 60-300 B. Per book at 50
retained entries: 3 keyframes x 2.2 KB + 47 x 0.2 KB = 16 KB, against 884 KB
measured. For 77k books: about 1.2 GB against 25-37 GB. No-op skip removes
20.6% of writes outright, with their memdb reload (which re-decodes every file
row of the book, `memdb_sync.go:176`) and reindex.

## 5. File records: hot row plus side entries

### 5.1 Split

Stays on the row (`book_file:<bookID>:<fileID>`): identity, paths, hashes,
sizes, scan state, `raw_tags` (read from memdb by the ABS mapper,
`abs/mapper.go:808`), `acoustid_fingerprint_duration_sec`, `acoustid_fp_version`,
`fingerprint_failed_at`, the seven small transcription fields.

Moves out, keyed by file id only so a move between books does not touch it
(`pebble_store_bookfiles.go:2219` re-keys the row):

- `book_file_fp:<fileID>` = 1 version byte + raw fingerprint bytes. No JSON,
  no base64.
- `book_file_cold:<fileID>` = JSON `{failure_reason, failure_detail,
  diagnostic_json, intro_transcription}`.

Both prefixes sort after `book_file;`, so every existing `book_file:` range
scan and the warmup never see them (same reasoning as `book_sig:`,
`pebble_store_booksig.go:67`, and `fpwin:`, `fingerprint_window.go:61`).

### 5.2 Type change

`AcoustIDFingerprint`, `IntroTranscription` and the three failure-text fields
are removed from `BookFile`. Access is explicit:

- `GetBookFileFingerprint(fileID) ([]byte, error)`
- `SetBookFileFingerprint(fileID, fp, durationSec, version)` : one batch writes
  the fp key, the two hot fields on the row, and the fingerprint-index keys.
- `ClearBookFileFingerprint(fileID)` : same batch shape, deletes.
- `GetBookFileCold` / `PatchBookFileCold`.

About 25 reader sites and the writers move to these (list in the scout report;
re-grep `\.AcoustIDFingerprint\b`). `bfPreserveAlways` guards for these fields
are deleted, since a row write can no longer touch them. The merge judge that
drops derived audio fields on a changed hash (`bookfile_merge.go:331`, `:366`)
calls the explicit clear in the same batch.

### 5.3 Legacy rows and lazy migration

Existing rows carry the fields inline. Reads are fallback-first: side entry,
else the inline JSON key decoded through a private legacy struct. The file
chokepoint checks the stored bytes for the legacy keys before any rewrite and,
if present, moves them to side entries in the same batch. So a legacy row can
never be rewritten without its cold data being carried over. A background op
(resumable, parallel by file id, modelled on `MigrateBookSigToSidecar` and
`SweepBookFileSegDrop`) finishes the rest without `UpdateBookFile`.

Delete paths (`deleteBookFileAt`, `DeleteBookFilesForBook`,
`DeleteBookFilesByIDs`) do not drop the side keys (P7). They re-key them to
`book_file_fp_kept:<content hash or fileID>` / `book_file_cold_kept:<...>` with
the file's path and hashes recorded, so a later row for the same audio can
re-adopt the fingerprint and transcript without recomputing. Only
`ClearBookFileFingerprint` (the audio itself changed) and the explicit
reset-all op delete a fingerprint. `WipeByPrefixes` (`maintenance_fixups.go:361`)
is a rollback tool and wipes the side prefixes with the rows.

### 5.4 Changed-detection in the file chokepoint

After merge, compare the new hot row bytes with the stored bytes:

- Equal: write nothing, skip memdb upsert and recompute.
- Row differs but no indexed field differs: write the row only. Today every
  update deletes and rewrites all secondary indexes (`:840`).
- The fingerprint-index keys are written only by `SetBookFileFingerprint` /
  `ClearBookFileFingerprint`, never by an ordinary row write.
- Recompute the book only when `duration`, `file_size`, `book_id`, `missing`
  or `file_path` changed.

### 5.5 Recompute storms

One book showed 654 distinct durations in 697 copies (R5): per-file writes each
recomputing the book. After section 4 each such write costs a 60 B change
entry, not 11 KB, but it still reloads memdb and reindexes. Remaining per-row
loops move to the existing batch variants that recompute once per book
(`UpdateBookFiles`, `BatchUpsert*`, `DeleteBookFilesByIDs`). The plan
inventories the per-row callers; `RecomputeBookAggregates` switches to a slim
decode of the four fields it uses.

### 5.6 Expected effect

Warmup: file-row phase reads 2,436 MB and keeps 583 MB (`memdb_warmup.go:116`).
With cold data out of the range it reads roughly the 583 MB plus tags. File
rows are 82% of a 109 s warmup (`memdb_store.go:156`); expect roughly 40-50 s.
Estimate, to be confirmed by timing on the deployed build.

### 5.7 Signal store: where the cold data lives (owner direction 2026-10-03)

The owner's roadmap is whole-book fingerprints and whole-book transcripts
(the transcripts with timing, for aligning an ebook to the audio position).
Sizing, as estimates with the arithmetic:

- Chromaprint emits about 8 32-bit values per second = 115 KB per audio hour.
  77k books x roughly 10 hours = 770k hours -> about 88 GB raw, and it barely
  compresses. Today's prints cover 120 s per file.
- Speech is about 9,000 words per hour = 55 KB of text per hour -> about
  42 GB of plain text for the library; word-level timing multiplies that
  before compression.

That is two to three times the whole current database, written once and
almost never changed. An LSM rewrites its values during compaction, so
keeping this in the main store would undo this design. Decision: the side
entries of section 5.1 do not live in the main store. They live in a
**signal store**:

- A second Pebble instance, `signals.pebble`, beside the main one, with its
  own cache and compaction settings tuned for large write-once values. Keys:
  `fp:<fileID>`, `cold:<fileID>`, `booksig:<bookID>`, `booksighist:<bookID>:<seq>`,
  later `fpfull:<fileID>` and `transcript:<fileID>`.
- For values past a size threshold (whole-book transcripts with timing), the
  value is a compressed file under a blob directory and the key holds a
  pointer plus checksum. Pebble's own value separation is checked against the
  pinned version before choosing; either way the interface is the same.
- Cross-store ordering replaces the single batch: write the signal (synced),
  then commit the main-store batch that records "has fingerprint, version,
  duration, checksum". A crash between the two leaves an unreferenced signal,
  which is harmless and re-adoptable. A row that claims a signal the store
  does not have is reported as `ErrSignalMissing`, never read as "no
  fingerprint".
- P7 becomes structural: no main-store delete path can reach the signal
  store. Removing a file or book record leaves its signals in place, keyed by
  id and indexed by audio content hash where one exists, for re-adoption.
- Backups and ZFS snapshot policy are set separately for it: write-once data
  snapshots cheaply, unlike a churning LSM.

The fingerprint index (`fpidx:`) stays in the main store for now; it is small
values and many keys, and phase 5 decides its shape.

## 5A. Archive: retired books leave the live keyspace

Books that are soft-deleted or merged away stay in `book:` today, with their
file rows, index entries and history. Every warmup, whole-library scan and
count pays for them. Proposal: an **archive** in a third key space, physically
in the signal store's Pebble instance (`archive:book:<id>`,
`archive:file:<bookID>:<fileID>`, `archive:hist:<bookID>:<seq>`), not a
separate database engine.

- **What moves:** the book row, its file rows, its change history, its
  metadata field state and history. Signals do not move; they are already in
  the signal store under the same ids.
- **When:** only after the record is out of every undo window: retired or
  soft-deleted for longer than the journal retention (90 days), no unreverted
  journal row, no unresolved merge journal entry, no pin. The fragment fixer
  and merge undo read retired books (`retireInto`, `merged_into` chains), so
  nothing inside a window moves.
- **How:** copy to the archive (synced), then delete from the main store in
  one batch, recorded in the operation journal; resumable; dry run, row list
  and owner approval like every destructive step. The copy-then-delete order
  means a crash leaves a duplicate, never a loss; the main-store copy wins
  until its delete commits.
- **Reads:** a narrow `GetArchivedBook(id)` and a restore op. Ordinary reads
  do not fall through to the archive; a lookup that needs to follow a
  `merged_into` link past the archive horizon gets a small forwarding entry
  left in the main store (`book_fwd:<id>` -> survivor id).
- **Hard delete** then means "remove from the archive", is refused while
  signals exist unless the owner passes an explicit flag, and is the only
  place change history is deleted for good.

Whether this is worth doing depends on how many retired rows exist; the
census (section 7) counts them before this phase is scheduled.

## 6. Operation logs and history

- **Progress log throttle.** A progress line is logged when the message
  *shape* changes (digits normalized to `#`), or 30 s have passed since the
  last progress line, or the operation reaches a terminal state. Live
  progress (`UpdateOpProgressV2`, the event bus) is unchanged. Expected: a
  300,935-row scan log becomes a few hundred rows.
- **Retention op** `maintenance.prune-op-history`, nightly, dry-run flag,
  defaults: log rows of terminal ops older than 30 days; whole op records
  (row, state, errors, strikes) older than 180 days; failed ops keep logs 90
  days. 180 days is longer than the 90-day journal retention
  (`cleanup.go:310-338`), so no journal row outlives its op record. The 30-day
  figure is the owner's call (open question Q2).
- **Tail read.** `GetOpLogsV2` reverse-iterates for a limited read; today it
  decodes the whole range then slices (`pebble_store_ops_v2.go:963`).
- **Dead code.** The legacy `operationlog:` family has no writer
  (`sysinfo/service.go:309`); its pruner and reader are removed.

## 7. Request-path scans

- **Timeline.** Add `opv2:done:<completed_nanos>:<opID>` written at the
  terminal transition, backfilled once. A window query is the union of: the
  active index (`opv2:act:`), a ULID seek on `opv2:op:` from the window start
  (queued in window), and a range read of `opv2:done:` from the window start
  (completed in window). This preserves the current window test
  (`:902`) exactly, including a long-running op that started before the
  window. The seven callers of `ListOperationsV2Since` benefit. 5.5 s becomes
  a read of the rows in the window.
- **Census.** `GET /diagnostics/db-census`: per-prefix key count and bytes from
  `db.SSTables(WithProperties)` plus `db.EstimateDiskUsage` per known prefix
  (R7). Metadata only, milliseconds. Prefix list lives in one registry that
  the schema doc is generated from.
- **db-health.** Uses the census. No full iteration. The "AI scans" size
  reports the `aiscan:` prefix, not the whole shared database (today it
  double-reports 50.7 GB).
- **Counts.** `/cache/stats` uses the census estimate. `ScanPrefix` and
  `CountPrefix` use `prefixUpperBound` (their current last-byte increment is
  wrong for a trailing 0xff, `pebble_store.go:4919`).
- **Prune functions** stream in bounded batches; today each builds one
  unbounded batch (`pebble_store_activity.go:139`).

## 8. Pebble configuration

Host: 121 GB RAM, 57 GB free, 48 cores, app RSS 8.5 GB (measured 2026-10-03).

| Option | Now (default) | Proposed | Why |
|---|---|---|---|
| Block cache | 8 MB | 4 GB, config key | 50 GB store; every miss goes to the page cache or disk |
| Bloom filter | none | 10 bits/key, all levels | exact-key misses are common (id probes, cache misses) |
| MemTable | 4 MB | 64 MB | fewer flushes under scan write bursts |
| Compaction concurrency | 1 | 1 to 4 | one large compaction blocks the rest (R3) |
| Manual compact | `parallelize=false` | `true` | 28.5 min full compaction, est. 8-10 min |
| Compression | Snappy | unchanged | ZFS already applies zstd; revisit only with census data |

The option names must be checked against pebble v2.1.7 (go.mod pin); the scout
read v2.1.4 from the module cache. The OpenLibrary store gets the same options
with a smaller cache.

Pebble metrics (cache hit rate, read amplification, L0 sublevels, compaction
debt) are exported to Prometheus so each change is measurable.

Operational, owner's call: stop the weekly scheduled full compaction once
background compaction keeps up (two ops run it today: `maintenance.db-optimize`
and `scheduler.db-optimize`, `plugins/maintenance/db.go:22`,
`scheduler/extra_ops.go:603`), and shorten ZFS snapshot retention on the app
data dataset. A full compaction followed by a snapshot pins a whole extra copy
(F12).

## 9. Other families (retention and duplication)

- **Metadata fetch cache** (owner decisions 2026-10-03: stale is fine with
  periodic refresh; not-found is never cached). Read path serves an entry of
  any age and marks it stale. TTL becomes per provider (a provider whose terms
  limit caching gets a short one). A budgeted refresh op re-fetches the oldest
  entries and keeps the preserve-on-empty guard (`metafetch/cache.go:525`). A
  reaper deletes rows whose book is gone. Empty results are not written, and
  the 90-day known-empty verdict (`iface_metadata.go:74`) is removed. Open
  question Q3 covers the provider-quota consequence.
- **Embeddings.** `emb:v` stores the vector as base64 inside JSON
  (`embedding_store.go:96`) and `emb:c` stores the same vector again
  (`:542`). `emb:v` keeps metadata only and reads the vector from
  `emb:c:<model>:<textHash>`, which it already records. A GC op removes
  `emb:c` rows no `emb:v` references and rows for models no longer configured.
- **Small items.** Batch `RecordMetadataChange` writes per book write (36
  call sites each do a synced Set); delete `metadata_change:` on hard delete;
  `file_prov_hash:` stores a pointer, not a second full copy.

## 10. Rollout (cut-over; supersedes any flag or shadow wording above)

Every release below writes only the new format from its first start. Legacy
data is read by the converter and by nothing else, except the narrow window
in which a converter is still running, when a read that meets an unconverted
record converts it on the spot.

- **Release A, no format change:** Pebble options and metrics; census;
  db-health; timeline index; op-log throttle and tail read. Establishes the
  before-numbers.
- **Release B, book history cut-over:** new history format, pins, no-op skip.
  At first start it takes a backup (a Pebble checkpoint plus a ZFS snapshot,
  named and logged), then runs the converter as a startup operation: per
  book, convert, verify in memory against the legacy copies, write new,
  delete legacy, in one batch. A book that fails verification keeps its
  legacy copies and is listed; the operation ends "converted N, held M" and
  the release is not finished until M is zero or the owner has ruled on each
  held book.
- **Release C, file records and signal store cut-over:** same shape. Backup,
  then move fingerprints, transcripts, diagnostics and book signatures to the
  signal store with a byte-for-byte check, then rewrite each row without
  them.
- **Release D, purge:** operation history retention, fetch-cache reaper,
  legacy fingerprint-index rows, embedding duplicates, history of
  hard-deleted books, the dead `operationlog:` family. Each is a deletion of
  data that has no new-format equivalent, so each keeps dry run, row list and
  owner approval (P5).
- **Release E:** delete the converters and every legacy reader. Then the
  owner deletes the old ZFS snapshots and one parallel compaction returns the
  space.
- **Later, on census data:** archive for retired books; fingerprint-index
  shape.

There is no rollback path by design. Each cut-over release states its backup
name in the operation log, and restoring that backup with the previous
release is the only way back. A converter that is interrupted resumes at the
next start; the application serves requests while it runs.

## 11. Test strategy

- Property test on a real store: random sequences of writes, no-ops, pins,
  signature changes, restores, prunes and a simulated bypass write; an oracle
  keeps full copies; every `reconstruct` must equal the oracle or return
  `ErrVersionChainBroken` (only for the bypass case).
- Cut-at-every-step tests (the pattern from the fragment fixer) for the
  conversion op and the file migration: kill after every staged write, resume,
  compare end state with an uninterrupted run.
- Existing pinned tests are updated in the same PR as the behaviour they pin
  (list in the scout report: 4 store tests, 9 prune tests, 6 audit tests,
  merge journal tests, the snapshot-count assertions in
  `delete_book_files_by_ids_test.go`).
- Reflection tests keep `BookFileCore`, the strip set and the field-class
  table in step.
- A CI grep ratchet for raw row writes outside the chokepoints.
- Benchmarks before and after: book write, file write, warmup, timeline.

## 12. Risks

| Risk | Mitigation |
|---|---|
| A write bypasses the chokepoint and silently corrupts history | post-hash chain detects it; next write lays a keyframe; ratchet prevents new bypasses |
| No-op skip removes a side effect a caller relied on (`updated_at` bump, reindex, memdb resync) | plan task inventories `UpdatedAt` readers and "touch" callers; `ForceBookRewrite` for intentional rewrites |
| Old build runs against new-format data after a rollback | expand/contract; old build fails closed on a tagged entry (JSON decode error), does not misread it |
| Fingerprint, row fields and index keys go out of step | single-batch `SetBookFileFingerprint`; test that kills between stages |
| Merge undo for journal entries written before the change | legacy copies stay readable; referenced nanos are pinned through conversion |
| Estimates are wrong (they rest on a 55-book sample) | census first; each phase re-measured |

## 13. Open questions for the owner

- Q1. Retention for book history: newest 50 entries per book (proposed), or a
  time window?
- Q2. Operation log retention: 30 days of full logs, 180 days of operation
  records (proposed)?
- Q3. Never caching not-found means every candidate pass re-queries providers
  for books they do not have. Google Books allows 1,000 requests a day. The
  refresh budget caps the damage, but those books will consume part of it
  every day. Accept, or allow a short retry delay (hours, not days) that is
  not a cached verdict?
- Q4. Stop the weekly full compaction and shorten ZFS snapshot retention on
  the app data dataset?
- Q6. Hard delete removes a book's computed signature today. Proposed: keep
  the signature, fingerprints and transcripts when a book or file record is
  removed, and let a later record for the same audio re-adopt them. Agree, or
  should hard delete of a book with computed signals be refused outright?
- Q5. Phase 5 (fingerprint index): the index holds an estimated 61-78M keys.
  Fingerprints cover only the first 120 s of each file. Shrink the index, or
  decide first whether it stays?
