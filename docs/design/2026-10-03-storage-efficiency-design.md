<!-- file: docs/design/2026-10-03-storage-efficiency-design.md -->
<!-- version: 1.1.0 -->
<!-- guid: 332dcbd9-73e2-4814-b1a0-723afa60e605 -->
<!-- last-edited: 2026-10-03 -->

# Storage efficiency redesign: store changes, not copies

Status: v1.0, revised after three adversarial reviews (correctness, operations,
simplicity). Awaiting owner approval. No code has been written.

Evidence: `.claude/notes/db-optimization-eval-2026-10-03.md` (measurements
R1-R7, findings F1-F13), three code scouts and three design reviews run on
2026-10-03 against `d1f069fac`. Every `file:line` here was checked on that
commit; the implementation plan carries the grep that re-verifies each one.

## 1. Problem

The main Pebble store holds 174,165,047 keys and 50.6 GB (R1). The in-memory
cache loads 123,271 book rows and 777,872 file rows at startup (prod warmup
log, 2026-10-03 18:04). Three write patterns cause most of the size, and each
also costs time on every write:

1. **Every book save stores a full copy of the previous book**, signature
   included, with no check that anything changed
   (`internal/database/pebble_store.go:2812-2816`, `:2742`). Sample of 55
   books: 79.8 copies per book, 11 KB each; 80.4% of the bytes are a signature
   that is identical in 96.7% of copies; 20.6% of copies differ from their
   neighbour only in `updated_at` (R5). Estimated 25-37 GB for the 41,723
   primary books alone.
2. **Every file-record save rewrites the whole record and all its indexes**,
   including up to 129 fingerprint-index keys, and triggers a book recompute,
   whether or not anything changed (`pebble_store_bookfiles.go:756`, `:840`,
   `:869`). The startup warmup reads 4,003 MB of file rows and discards
   3,067 MB of it; 3,017 MB of that is fingerprints. The file-row phase is 110
   of the 134 seconds (prod warmup log, 2026-10-03 18:04).
3. **Operations write one log row per progress update and nothing removes
   them.** Messages contain counters, so the duplicate filter never fires
   (`internal/operations/registry/reporter_db.go:391`). One scan wrote about
   300,935 rows (R6). Only a manual per-operation discard deletes them
   (`pebble_store_ops_v2.go:785`).

Read paths scan everything to answer small questions: the timeline decodes
every operation row per call (5.5 s, R4), db-health iterates all 174M keys
(about 5 minutes), and Pebble runs on library defaults (8 MB cache, no bloom
filters, one compaction at a time; R2).

## 2. Goals and non-goals

Goals, in priority order (owner: "our biggest focus is areas that need
optimization"; "if we can't get it to shrink that's fine"):

- G1. A write that changes nothing writes nothing. A write that changes one
  field stores that field.
- G2. No request-path operation scans a whole key family to return a count or
  a page.
- G3. Every unbounded key family gets a retention or consolidation rule.
- G4. Restore to a version, merge undo, repairs undo and the
  Audiobookshelf-compatible API keep working.
- G5. Size falls as a consequence. The census (section 8) replaces today's
  estimates before any size target is stated.

Non-goals: a metadata proxy (evaluated and dropped by the owner 2026-10-03);
audio files (nothing here touches them); compatibility with older builds (P3).

## 3. Principles

- P1. **One chokepoint per record type.** Every write of a book row goes
  through one function that decides: skip, write row, write history entry.
  Same for file rows. A CI grep ratchet fails any raw `book:` or `book_file:`
  Set outside the chokepoints. The patterns include `"book:" +` and
  `[]byte("book:"`, not only `Sprintf` (the signature migration writes that
  way, `pebble_store_booksig_migrate.go:243`).
- P2. **Types cannot carry what they must not wipe.** The repeated incident
  class is "field was not loaded, got saved as empty". Guards exist for nine
  book fields and two file fields (`pebble_store.go:2753`,
  `bookfile_merge.go:211`). Data moved out of a row is removed from the row's
  Go type, so the bug cannot be written. Clearing is an explicit call.
- P3. **Cut over. No backward compatibility.** (Owner, 2026-10-03.) No format
  flags, no dual-write period, no legacy format kept readable. Each format
  change is one release that converts every legacy record, verifies the
  conversion, and deletes the legacy records. Legacy-reading code exists only
  inside the converter and is deleted in the next release. The way back from
  a bad migration is the pre-migration backup.
- P4. **Verify, then purge; fail closed.** A conversion that cannot prove it
  reproduced the old data keeps the old data and reports it. A history entry
  that cannot be rebuilt exactly returns an error naming the entry.
- P5. **Deleting data that has no new-format equivalent needs the owner**:
  dry run, reviewable list, apply by explicit ids. Lossless conversion does
  not.
- P6. **Measure before and after.** Release A builds the census; every later
  release states the numbers it expects to move.
- P7. **Decode-cost data is never deleted by this work.** File fingerprints
  and transcripts cost processor time to produce, and the owner keeps records
  so they survive (owner, 2026-10-03). No path here deletes a fingerprint or
  transcript's bytes. A path that used to delete them unreferences them
  instead. Book signatures are different: they are assembled from the stored
  file fingerprints with no audio decoding
  (`internal/plugins/acoustid/backfill.go:705`), so they are rebuildable and
  are not covered by P7 (section 5.5).

## 4. Book history: change entries replace full copies

### 4.1 What consumers need (verified)

| Consumer | Needs | Anchor |
|---|---|---|
| Version list UI | timestamps only; never decodes `data` | `web/src/components/MetadataHistory.tsx:111` |
| Restore to version | a complete book at an exact version id | `pebble_store.go:3225` |
| Tag comparison at a time | 17 scalar fields + author/series ids | `internal/audiobooks/helpers.go:416` |
| Merge undo | restore by the nanos stored in the merge journal | `internal/dedup/auto_resolve.go:421` |
| Merge baseline | "first copy newer than baseline"; assumes one copy per write | `auto_resolve.go:378`, `merge_journaled.go:133` |
| Signature recovery audit | newest history value of `description`; signature | `booksig_recovery_audit.go:108`, `:250` |

### 4.2 Version ids

The id stays a nanosecond timestamp, with two changes: it is forced to
increase (`id = max(now, newest id for this book + 1)`), and it is written
zero-padded to 20 digits. That removes the one real defect of today's keys (a
clock step backwards mis-orders history) and keeps one notion of identity:
the merge journal's `WinnerPreMergeTS` / `LoserPreMergeTS` and the
restore-by-timestamp API keep working with no lookup layer. A per-book
sequence number was considered; it needs a second id beside the timestamp
everywhere a version is referenced, and it restarts at 1 if a book id is ever
re-created.

New family: `bookhist:<bookID>:<id>`. The legacy family `book_ver:` is
converted and emptied by the cut-over (section 9), so "legacy family is
empty" is a checkable gate.

### 4.3 Format

The entry with id T means "the book as it was just before the write at T".

- First byte `0x01`, **keyframe**: JSON `{"h": <post-hash>, "pin": <reason,
  optional>, "row": <full stored row>}`.
- First byte `0x02`, **change entry**: JSON `{"h": <post-hash>, "set": {<json
  key>: <previous raw value>}, "unset": [<keys absent before>]}`.

Definitions:

- **Versioned projection** of a row: its stored JSON object minus
  `updated_at` and minus the six signature keys. (Rows written before the
  signature side entry existed can still hold inline signatures,
  `pebble_store_booksig.go:148-157`; the converter in section 9 finishes that
  move first and the inline reader is deleted.)
- **Post-hash** `h`: xxhash64 over the versioned projection of the row as
  written at T, keys sorted, each as length-prefixed key and raw value bytes
  exactly as stored. `h` is what lets a reader detect that some write changed
  a key without recording it.
- A change entry holds every top-level key whose stored bytes differ between
  the row before T and the row written at T. It is a raw-JSON diff, never a
  decode into `Book` (which would drop keys the struct no longer has) and
  never `ChangedBookFields` (which skips `author`, `authors`, `series`,
  `metadata_provenance` on purpose, `book_field_render.go:33`).

There is no per-book head record. The writer's one reverse seek to the newest
`bookhist:` entry gives the last id and its `h`.

### 4.4 Reading a version

`reconstruct(id, T)`: find the nearest keyframe K >= T; if none, the base is
the live row. For each change entry E with T <= E < K, newest first: check
that the hash of the working state equals `E.h`, else return
`ErrVersionChainBroken{book, at}`; then apply `set` and `unset`. Nothing is
re-hydrated: `author` and `series` are stored in the row, and `updated_at` is
omitted from the result because history does not record it.

A restore writes the reconstructed row through the chokepoint. It does not
touch the signature (the signature describes the audio, not the metadata).

### 4.5 Writing

In the book chokepoint, with the book's write stripe held:

1. Read the stored row bytes and the newest history entry.
2. Build the new row bytes and diff the versioned projections.
3. **No change:** write no history entry. In release B the row is still
   written and `updated_at` still bumps, so no caller sees a difference. A
   counter records how often a write would have been skipped entirely. The
   full skip (no row, no memdb reload, no reindex, no notification) is a
   later release, after the counter is read on prod and the callers that rely
   on a write happening are inventoried (section 9, release F). When the full
   skip lands it keeps three "ensure" steps that today are side effects of a
   write: the two self-heal index Sets (`pebble_store.go:2876`, `:2960`) and
   clearing a stale undecodable marker (`:2887-2893`), each as "read, write
   only if needed".
4. **Change:** if the newest entry's `h` does not match the stored row (a
   bypass happened), or 19 change entries have been written since the last
   keyframe, write a keyframe of the old row. Otherwise write a change entry.
   Keyframes every 20 exist to limit damage: under P4 one unreadable entry
   makes everything older than it unreachable up to the next keyframe.
5. Row, history entry, signature side entry and indexes commit in the one
   batch that exists today.

`CreateBook` refuses an id that already exists (today it overwrites,
`pebble_store.go:2520-2532`).

### 4.6 Pins

`PinBookVersion(id, reason) (versionID, error)` writes a keyframe of the
current row with `pin` set. Merge code pins winner and loser before merging
and stores the returned ids in the journal fields it already has.
`newestSnapshotNanos` and `preMergeSnapshotNanos` are deleted. This replaces
a dependency on a side effect (today the lookup returns 0 if the merge never
wrote that book). Pins are released when their journal entry is finalized or
after the undo window.

### 4.7 Retention and deletion

- A nightly prune keeps the newest 50 entries per book plus all pinned
  keyframes. Removing the oldest entries never affects newer ones. Entries
  are never removed from the middle.
- The existing manual job `prune-book-snapshots` is deleted in release B. It
  takes no stripe, ignores pins and would race the converter
  (`internal/maintenance/jobs/prune_book_snapshots.go:45`, `:117-150`).
- `DeleteBook` (hard delete) removes the book's `bookhist:` range in its
  batch. Today it leaves history forever (`pebble_store.go:3351`). The
  existing callers keep working (`internal/reconcile/reconcile.go:887`,
  `itunes_regroup.go:500`, `handlers/versions.go:903`). If the row carries a
  transcript (`Book.IntroTranscription` and the `Transcribed*` fields,
  `store.go:378-395`), the delete first writes it to the signal store under
  `orphan:book:<id>` (P7).

### 4.8 API

- `GET /audiobooks/:id/cow-versions` returns `{timestamp, kind, pinned,
  changed_fields}` per entry, reverse-iterated, cost O(limit). `data` is
  returned only with `?include_data=true`, reconstructed.
- Store surface: `ListBookVersions`, `GetBookAtVersion`,
  `RevertBookToVersion`, `PinBookVersion`, `LastHistoryValue`,
  `PruneBookVersions`, behind a capability interface (width ratchet).
  `GetBookSnapshots` and `BookSnapshot` are deleted.
- `booksig_recovery_audit` uses `LastHistoryValue(id, "description")`, which
  reads change entries and keyframes newest-first. Its signature recovery
  becomes "rebuild from the stored fingerprints"; the prod dry run found zero
  signature wipes (`booksig_recovery_audit.go:42-45`).

### 4.9 Expected effect (estimate, same denominator both sides)

Stripped row is about 2.2 KB (19.6% of 11.1 KB, R5); a change entry for the
commonest changes is 60-300 B. At 50 retained entries per book: 3 keyframes
x 2.2 KB + 47 x 0.2 KB = 16 KB per book, against 607-884 KB measured for
primary books. For the 41,723 primary books: about 0.7 GB against 25-37 GB.
Non-primary and retired books (81k more rows) were not sampled; the census
measures the whole family before release B.

## 5. File records and the signal store

### 5.1 Decision

Fingerprints, transcripts, failure diagnostics and book signatures move out
of the main store into a second Pebble instance, the **signal store**
(`signals.pebble`). Owner, 2026-10-03: "I'm ok losing the atomic write to get
signatures and fingerprints out of the main database so we can tune it and
the new database better. We just have to have a check that it's written and
right." The roadmap is whole-book fingerprints and timed whole-book
transcripts (ebook alignment), estimated at tens of GB each, written once.

The trade is explicit: a file row and its fingerprint no longer commit in one
batch. Section 5.3 is the check that replaces it.

### 5.2 Layout

Main store, on the file row: identity, paths, hashes, sizes, scan state,
`raw_tags` (read from memdb by the ABS mapper, `abs/mapper.go:808`), the seven
small transcription fields, and one **signal reference** per signal kind:
`{sum: <xxhash64 of the bytes>, len, version, duration_sec}`. The reference
fields replace `acoustid_fp_version` and `acoustid_fingerprint_duration_sec`
as the source of "has a current print"; they are writable only through the
calls in 5.4.

Signal store, immutable and content-addressed:

- `fp:<fileID>:<sum>` = 1 format byte + raw fingerprint bytes (no JSON, no
  base64).
- `tx:<fileID>:<sum>` = transcript text; `diag:<fileID>:<sum>` = failure
  diagnostics.
- `booksig:<bookID>` = the signature side entry (rebuildable; overwritten in
  place).
- `orphan:file:<fileID>` / `orphan:book:<bookID>` = `{path, size, hashes,
  former book}` for signals whose record was removed, so they stay findable.

A value past a size threshold (whole-book transcripts with timing) is stored
as a compressed, checksummed file under a blob directory and the key holds
the pointer. Whether Pebble's own value separation can do this at the pinned
version is checked in the plan; the interface is the same either way.

### 5.3 The check: written and right

- **Write:** put the signal (synced), read it back, compare the checksum,
  then commit the main batch that sets the row's reference. A crash between
  the two leaves an unreferenced signal, which is harmless.
- **Replace:** a new fingerprint has a new checksum and therefore a new key.
  The old bytes are never overwritten, so a crash cannot leave a row pointing
  at bytes that are gone.
- **Clear:** unreference in the main store only. The bytes stay (P7).
- **Read:** every read verifies length and checksum against the row's
  reference. A mismatch or a missing key returns `ErrSignalMissing` /
  `ErrSignalCorrupt`, never "no fingerprint". Whole-library readers count and
  skip such rows and report the count; they do not abort.
- **Pairing:** both stores carry the same store id and a format stamp. The
  app refuses to start if the signal store is absent after first creation
  (opened with `ErrorIfNotExists`), or if the ids differ. This catches a
  wrong path, an unmounted dataset and a restore of one store without the
  other.
- **Reconcile op**, nightly and on demand: every reference resolves and
  matches; lists rows whose signal is missing or corrupt; counts unreferenced
  signals. Invariants exported as metrics: files with a fingerprint reference
  = references that resolve; same for transcripts.
- **Backup:** the two stores live in sibling datasets under one parent, so
  one recursive snapshot is consistent across both. The signal store is
  excluded from the full-compaction op; write-once data gains nothing from
  it.

### 5.4 Type change

`AcoustIDFingerprint`, `IntroTranscription`, the three failure-text fields,
and the raw version and duration fields are removed from `BookFile`. Access
is explicit:

- `GetBookFileFingerprint(fileID)`, `SetBookFileFingerprint(fileID, fp,
  durationSec, version)`, `ClearBookFileFingerprint(fileID)`.
- `GetBookFileTranscript` / `SetBookFileTranscript`, `GetBookFileDiagnostics`
  / `SetBookFileDiagnostics`.

Cost: 119 occurrences of `.AcoustIDFingerprint` in 40 non-test files move to
these calls. The `bfPreserveAlways` guards for the moved fields are deleted;
a row write can no longer touch them. The merge judge that today clears
derived audio fields when an incoming duration is unknown
(`bookfile_merge.go:288-290`, `:353`) unreferences instead of clearing, and
only when the audio hash actually changed.

### 5.5 Indexes and moves

The fingerprint index stays in the main store. Its values are the owning book
id (`pebble_store.go:5518-5520`), so a move between books must update them.
Today that works only because the fingerprint bytes are on the row; the move
path rebuilds the index from them (`pebble_store_bookfiles.go:2205`,
`pebble_store.go:5507`). New rule: when `book_id` changes, the index values
are rewritten from `fpidx_meta:<fileID>`, which already holds every band and
subprint. Index keys are otherwise written only by `Set` /
`ClearBookFileFingerprint`.

Book signatures: `ClearBookSignature` on an incomplete fingerprint set stays
legal (`acoustid/backfill.go:707-722`); the signature is rebuildable.

### 5.6 Changed-detection in the file chokepoint

After merge, compare the new row bytes with the stored bytes. Equal: write
nothing. Row differs but no indexed field differs: write the row only (today
every update deletes and rewrites every secondary index, `:840`). Recompute
the book only when the fields `RecomputeBookAggregates` actually reads
changed: duration, file size, missing, file path, book id, file hash,
original file hash, original filename, fingerprint duration
(`internal/dedup/book_runtime.go:129-140`, `book_own_folder.go:259-277`). The
trigger is a hash of exactly that projection, and a reflection test ties the
projection to what the recompute reads.

### 5.7 Expected effect

Warmup today reads 4,003 MB of file rows and keeps 936 MB. With signals out,
it reads about the 936 MB. The 110 s file phase should fall substantially;
the figure is measured on the sandbox rehearsal, not predicted, because 61%
of that phase's CPU is block loading, not decoding (`memdb_store.go:156`).

## 6. Archive: retired books leave the live key space

The cache loads 123,271 book rows; about 77,000 are live (R4). Roughly 46,000
rows are soft-deleted or merged away and are paid for by every warmup, scan
and count. Release A's census reports the exact count and bytes.

Design, in the main store: retired rows move to key families outside every
live range (`zarch:book:<id>`, `zarch:file:<bookID>:<fileID>`,
`zarch:hist:<bookID>:<id>`) in one batch per book. Same store means the move
is atomic and needs no forwarding entry: `GetArchivedBook(id)` is a point
read, and `merged_into` is already on the row.

A book is eligible only when no reader of retired books can need it:

- retired longer than the undo journal's retention, no unreverted journal
  row, no unresolved merge journal entry, no pin;
- no file with an iTunes persistent id or iTunes path (`itunes/pid_integrity.go:143-169`);
- not a member of a version group (`version_group_primary_revive.go:188-199`);
- not the target of another book's `merged_into` unless that book is archived
  with it (`merge/pending_repair.go:178`, `undo/current.go:99`);
- no file present on disk, or a path tombstone stays in the live family, so
  the scanner does not re-import it;
- not in trash with a restore pending.

The archive is its own release after the cut-overs, with dry run, list and
owner approval per run, a cap per run, and a restore op proven on the
sandbox. Signals are untouched; they are keyed by id in the signal store.

## 7. Operation logs and history

- **Progress log throttle.** A progress line is logged when the message shape
  changes (digits normalized), or 30 s have passed since the last one, or the
  operation ends. `Log()` calls are untouched. The watchdog reads progress
  touches, not log rows (`reporter_db.go:355-360`), so liveness is unaffected.
- **Nightly consolidation** `maintenance.compact-op-logs` (owner, 2026-10-03):
  1. **Pack, lossless.** One day after an operation ends, its
     `opv2:log:<op>:*` rows are replaced by `opv2:logpack:<op>` (the whole log,
     zstd) and `opv2:logdigest:<op>` (line counts by level, first and last
     timestamps, every warning and error verbatim up to a cap, first and last
     20 lines). The row range is removed with one `DeleteRange` in the same
     batch. Nothing is lost.
  2. **Age out.** After the retention period (Q2) the pack is deleted and the
     digest stays as long as the operation record. First run is a dry run
     with a summary for the owner; each run is capped.
  Readers (`GetOpLogsV2`, download, detail page) read a pack transparently and
  tail-read without decoding the whole log.
- **Operation records** (row, state, errors, strikes, digest, index entries)
  are removed after 180 days, one batch per operation.
- **Separate settings.** The undo journal is pruned by the general
  log-retention setting today (`internal/logger/retention.go:27-39`). It gets
  its own setting so no log setting can shorten undo, and the by-book journal
  index from #3704 is pruned in the same batch as the journal row.
- **Dead family.** `operationlog:` has no writer (`sysinfo/service.go:309`);
  its rows, pruner and reader are removed.

## 8. Request-path scans, census, Pebble settings

- **Timeline.** Two small indexes maintained at every write of an operation
  row, including the raw one at `pebble_store_ops_v2.go:1309`:
  `opv2:open:<op>` while `CompletedAt` is nil, and
  `opv2:done:<completed_nanos>:<op>` whenever `CompletedAt` is set or changes
  (the old key is removed). A window query unions open, a ULID seek from the
  window start, and a range read of done from the window start, then applies
  today's predicate to each row (`:902-904`) and de-duplicates by id. That is
  exact for `waiting_deps`, for the `interrupted_*` states that have a
  completion time but are not terminal (`:1190-1194`), and for resumed
  operations. A reconcile at startup rebuilds both indexes if their stamp is
  missing.
- **Census.** `GET /diagnostics/db-census`: per-family key count and bytes
  from sstable properties and `EstimateDiskUsage` (R7), cached for a few
  minutes, plus counts the families cannot give: retired books and files,
  history entries per book (distribution), signals referenced and resolved.
  The family list is one registry that the schema document is generated from.
- **db-health** uses the census. No full iteration. The "AI scans" size
  reports its own prefix; today it reports the whole shared store.
- **Counts and prunes.** `/cache/stats` uses the census. `ScanPrefix` and
  `CountPrefix` use the safe upper bound. Prune functions stream in bounded
  batches and use range deletes where a whole prefix goes.
- **Pebble settings**, main store (host: 121 GB RAM, 48 cores, app RSS
  8.5 GB, ZFS ARC about 25 GB; dataset has 6.4 TB free):

  | Option | Now | Proposed |
  |---|---|---|
  | Block cache | 8 MB | 4 GB, shared by the main and OpenLibrary stores |
  | Bloom filter | none | 10 bits per key |
  | MemTable | 4 MB | 64 MB |
  | Compaction concurrency | 1 | 1 to 4 |
  | Manual compaction | serial | parallel |
  | Compression | Snappy | unchanged (ZFS already applies zstd) |

  The signal store gets its own settings: large blocks, a small cache, no
  scheduled full compaction. Store-open settings come from the environment
  only; the app's saved settings live inside the store they would configure
  (`pebble_store.go:392-397`, `config/persistence.go:814-826`). Pebble metrics
  ship first and run for a day as a baseline; then one setting per deploy,
  each with the metric it should move. Option names are verified against
  pebble v2.1.7 in the plan. Bloom filters reach existing data only when it
  is rewritten, so the single full compaction at the end of the program
  (section 9) is what applies them everywhere.

## 9. Releases

Preconditions: PRs #3698, #3699, #3700, #3704 merged (they edit the same save
path and journal). Free space on the datasets recorded.

**Guard, shipped in release A:** a `storage_format` stamp in the main store.
Every build refuses to open a store whose stamp is newer than it understands,
and `make rollback` refuses when the previous binary predates the stamp and
prints the restore steps instead. Today `make rollback` swaps the binary with
no data restore (`Makefile:725-735`), and an older binary on converted file
rows would read every file as fingerprint-less and erase the fingerprint
index during its first scan (`pebble_store_bookfiles.go:840`,
`pebble_store.go:5507`). The guard is what makes "the backup is the only way
back" true.

**Cut-over procedure, used by releases B and C: a startup migration, inside
the app.** (Owner, 2026-10-03: do it when the app starts, before anything
else, like the database migrations other apps use.) The conversions are
registered in the existing migration runner (`internal/database/migrations.go`),
which already runs synchronously at open before the server serves. No deploy
script, no separate tool: deploying the new build is the migration.

At start the app compares the store's `storage_format` stamp with its own:

1. **Stamp current:** start normally.
2. **Stamp older:** before any other subsystem starts (no scan, no scheduler,
   no operation resume, no API), the app:
   a. opens a minimal status listener that answers every request with
      "migrating", the step and the progress, so the deploy health check, the
      UI and the operator can see it. It must also keep systemd from timing
      the start out (start timeout extended or notified while migrating);
   b. takes its own backup: a Pebble checkpoint of each store into a
      dedicated directory, `migration-backups/<from>-<to>-<time>/`. A
      checkpoint is a set of hard links to files Pebble never modifies, so it
      preserves the exact pre-migration state against a converter bug. The
      existing staging sweep that deletes checkpoints after 24 hours
      (`internal/backup/backup.go:541-551`) does not cover this directory; it
      is removed only in release E with owner sign-off. A ZFS snapshot is not
      required. The checkpoint does not protect against losing the pool; the
      existing backups do that;
   c. per record, in one batch: read legacy, write new, verify by running the
      production read path over the staged batch, delete legacy. Work is
      computed by about 8 workers; a cursor makes a restart resume;
   d. **held records:** a record that fails verification keeps its legacy
      data and goes on a persisted held list with the reason and the first
      differing field. Unparseable legacy keys (ignored today,
      `pebble_store.go:3242`) and undecodable values count as held. If more
      than 1% of the first 1,000 records are held, the migration stops, the
      app stays in the "migrating" state with the reason shown, and nothing
      has been lost. The owner rules on each held record: re-run after a
      fix, or purge with the loss recorded;
   e. gates: invariant counts equal before and after (books, files,
      fingerprints, transcripts, signatures); legacy family count equals the
      held count; then the stamp is advanced and normal startup continues.
3. **Stamp newer than the build:** refuse to start (the guard above).

Because nothing else is running, there are no mixed-format records, no
conversion on read, no temporary legacy readers and no need to pause repairs.
Restoring means: stop, replace the store directories with the checkpoint,
start the previous build.

Before each cut-over reaches prod, the same build is started on the sandbox
against a copy of prod. That rehearsal gives the real duration (first
estimate 15-40 minutes), the census and the invariant counts.

**Release A: measure and speed up, no format change.** Pebble metrics;
census; db-health; format stamp and rollback guard; timeline indexes; op-log
throttle and tail read; then Pebble settings one per deploy.

**Release B: book history.** Chokepoint, change entries, pins, monotonic ids,
no-entry-on-no-change with the would-skip counter, `CreateBook` refusal,
ratchet. The converter:

- finishes the signature side-entry move for any row still holding inline
  signatures;
- per book, diffs each legacy copy against the next newer state as raw JSON
  objects, minus `updated_at` and the signature keys;
- keeps a keyframe for every 20th entry and for every id referenced by an
  unresolved merge journal entry (as a pin, id equal to the legacy nanos);
- deliberately does not carry: inline signature copies (rebuildable),
  historical `updated_at` values (not recorded by the new format);
- keeps every entry. Retention (newest 50 + pins) is not applied by the
  converter; the first nightly prune is a dry run with a summary for the
  owner, because it deletes history that has no other copy (P5);
- lists history of hard-deleted books for the owner instead of converting it.

**Release C: file records and signal store.** Signal store with pairing and
reconcile; type change; accessors; index rewrite on move; changed-detection.
The converter moves each inline fingerprint, transcript and diagnostic to the
signal store by raw JSON key surgery (never a decode and re-marshal through
the struct), reads each back and compares bytes, then rewrites the row with
the reference. Book-level signature side entries move the same way.

**Release D: consolidate and purge.** Op-log pack op; operation record
retention; legacy fingerprint-index rows (written before the era check;
counted against `fpidx_meta:` and current prints first); fetch-cache rows
whose book is gone; the dead `operationlog:` family. Each deletion: dry run,
list or aggregate with exceptions, owner approval, capped runs. A purge
refuses to run when its reference set is empty or implausibly small (cache
not warm, model list not loaded).

**Release E: finish.** Delete converters. Owner signs off; old ZFS snapshots
are purged; snapshot schedule paused; one parallel full compaction; schedule
resumed; census recorded as the after-numbers.

**Release F: full no-op skip and recompute storms.** Using the counter from
B: skip the whole write when nothing changed, with the ensure steps of 4.5.
Move the remaining per-row file loops to the batch variants that recompute a
book once.

**Release G: archive** (section 6).

Separate specs, referenced here and not part of this program: metadata
fetch-cache policy (owner decisions 2026-10-03: stale is fine with periodic
refresh; not-found is never cached); embedding storage (each vector is stored
twice, `embedding_store.go:96`, `:542`); fingerprint-index shape (61-78M keys,
F5); whole-book fingerprints and transcripts (they use the signal store
defined here).

## 10. Test strategy

- Property test on a real store: random sequences of writes, unchanged
  writes, pins, restores, prunes, a simulated bypass write and non-ASCII and
  `<>&` values; an oracle keeps full copies; every `reconstruct` equals the
  oracle minus `updated_at` and signature keys, or returns
  `ErrVersionChainBroken` for the bypass case only.
- Converter tests on fixtures written by the current code: every legacy shape
  found on prod (inline signature, removed fields, unparseable key,
  undecodable value, 800-entry book), cut at every staged write and resumed,
  end state compared with an uninterrupted run.
- Signal store: kill between signal write and row commit; replace; clear;
  move between books; start with a missing or mismatched store; reconcile
  finds a planted missing and a planted corrupt signal.
- Reflection tests tie the file-row type, the memdb projection, the field
  class table and the recompute projection together.
- Existing tests that pin old behaviour are rewritten in the same PR as the
  behaviour (list in the plan).
- Benchmarks before and after: book write, file write, warmup, timeline,
  version list.

## 11. Risks

| Risk | Mitigation |
|---|---|
| A write bypasses the chokepoint | post-hash detects it and the next write lays a keyframe; CI ratchet; `CreateBook` refusal |
| Converter is wrong on some legacy shape | verification through the production reader; held list; 1% stop; sandbox rehearsal on real data |
| Row and signal disagree | immutable content-addressed keys; read-back on write; checksum on read; nightly reconcile; pairing stamp |
| Old binary started on converted data | format stamp; `make rollback` guard |
| Restore loses work done since the cut-over | the migration runs before the app serves, so the window is the migration itself |
| First purge floods compaction | range deletes; per-run caps; purges after the cut-overs, before the final compaction |
| Estimates rest on a 55-book sample | census in release A; each release re-measured |

## 12. Open questions for the owner

- Q1. Book history retention: newest 50 entries per book plus pins.
- Q2. Operation logs: packed after 1 day; packs kept 90 days; operation
  records 180 days.
- Q3. Downtime: two offline cut-overs. The sandbox rehearsal gives the real
  duration before either is scheduled; first estimate 15-40 minutes each.
- Q4. Stop the weekly scheduled full compaction (two ops run one today,
  `plugins/maintenance/db.go:22`, `scheduler/extra_ops.go:603`) and shorten
  ZFS snapshot retention on the app data dataset. A full compaction followed
  by a snapshot pins a whole extra copy (F12).
- Q5. Fold the per-field metadata history (`metadata_change:`, 36 call sites,
  each its own synced write outside the book's batch) into the new change
  entries, so there is one book history written atomically with the row?
  Cost: provenance fields added to the change entry and 36 call sites moved.
  Proposed as a follow-up release, not part of B.
