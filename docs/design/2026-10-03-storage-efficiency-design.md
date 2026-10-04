<!-- file: docs/design/2026-10-03-storage-efficiency-design.md -->
<!-- version: 1.3.0 -->
<!-- guid: 332dcbd9-73e2-4814-b1a0-723afa60e605 -->
<!-- last-edited: 2026-10-03 -->

# Storage efficiency redesign: store changes, not copies

Status: v1.3, revised after three adversarial reviews (correctness, operations,
simplicity), a red-team workflow and a critic pass, all on 2026-10-03. The
red-team workflow confirmed 45 findings (F01-F43, F45, F46), all applied. Two
were refuted and dropped: F44 ("`DeleteBook` history is not undo data") and
F47 ("the Doctor Who exclusion and the archive rule do not apply"). Awaiting
owner approval. No code has been written.

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
   primary books alone. A code comment from 2026-08-29 puts the whole
   library's snapshot history at about 7.65 GB (`pebble_store.go:3313-3315`);
   the census (section 8) decides which figure is right before release B.
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
  way, `pebble_store_booksig_migrate.go:243`). The ratchet also rejects a raw
  `book_file:` Delete outside the three file-row delete primitives (5.3).
- P2. **Types cannot carry what they must not wipe.** The repeated incident
  class is "field was not loaded, got saved as empty". Guards exist for nine
  book fields and two file fields (`pebble_store.go:2753`,
  `bookfile_merge.go:211`). Data moved out of a row is removed from the row's
  Go type, so the bug cannot be written. Clearing is an explicit call.
- P3. **Cut over. No backward compatibility.** (Owner, 2026-10-03.) No format
  flags, no dual-write period, no legacy format kept readable. Each format
  change is one release that converts every legacy record, verifies the
  conversion, and deletes the legacy records. Legacy-reading code exists only
  inside the converter and is deleted in release E, which is gated on held =
  0 and orphan-listed = 0 for releases B and C, so nothing is left in the
  legacy format when it goes (section 9, release E gate). The way back from a
  bad migration is the pre-migration backup.
- P4. **Verify, then purge; fail closed.** A conversion that cannot prove it
  reproduced the old data keeps the old data and reports it. A history entry
  that cannot be rebuilt exactly returns an error naming the entry.
- P5. **Deleting data that has no new-format equivalent needs the owner**:
  dry run, reviewable list, apply by explicit ids. Lossless conversion does
  not.
- P6. **Measure before and after.** Release A builds the census; every later
  release states the numbers it expects to move.
- P7. **Decode-cost data is never deleted by this work.** File fingerprints
  (whole-file, windowed and whole-book prints alike, 5.2a) and transcripts
  cost processor time to produce, and the owner keeps records so they survive
  (owner, 2026-10-03). No path here deletes a fingerprint or transcript's
  bytes. A path that used to delete them unreferences them instead. Book
  signatures are different: they are assembled from the stored file
  fingerprints with no audio decoding
  (`internal/plugins/acoustid/backfill.go:705`), so they are rebuildable and
  are not covered by P7 (section 5.5).

## 4. Book history: change entries replace full copies

### 4.1 What consumers need (verified)

| Consumer | Needs | Anchor |
|---|---|---|
| Version list UI | timestamps only; never decodes `data` | `web/src/components/MetadataHistory.tsx:111` |
| Restore to version | a complete book at an exact version id | `pebble_store.go:3225` |
| Tag comparison at a time | 17 scalar fields + author/series ids | `internal/audiobooks/helpers.go:416` |
| Tag comparison fallback | when the exact version is missing it must get an error, so it falls back to the activity log | `internal/audiobooks/service_single.go:168-178` |
| ChangeLog revert (`POST /revert-metadata`) | sends an activity timestamp, not a version id; today it fails with "version not found" and must keep failing that way | `web/src/components/ChangeLog.tsx:98-105`, `handlers/metadata/handler.go:732` |
| Merge undo | restore by the pinned id stored in the journal; a released or pruned pin returns `ErrVersionNotFound`, never the oldest retained state | `internal/dedup/auto_resolve.go:421` |
| Merge baseline | "first copy newer than baseline"; assumes one copy per write (replaced by pins, 4.6) | `auto_resolve.go:378`, `merge_journaled.go:133` |
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
empty" is a checkable gate. Gates count with a bounded iterator over the
family, never with the census (section 8), whose sstable-property figures
include tombstones and shadowed versions until compaction.

### 4.3 Format

The entry with id T means "the book as it was just before the write at T".

- First byte `0x01`, **keyframe**: JSON `{"h": <post-hash>, "n": 0, "pin":
  <reason, optional>, "row": <full stored row>}`.
- First byte `0x02`, **change entry**: JSON `{"h": <post-hash>, "n": <int>,
  "set": {<json key>: <previous raw value>}, "unset": [<keys absent
  before>]}`. `n` is the newest entry's `n` + 1; a keyframe (pinned or not)
  resets it to 0.

Definitions:

- **Versioned projection** of a row: its stored JSON object minus one named
  constant set, `unversionedBookKeys`: `updated_at`; the six signature keys
  (rows written before the signature side entry existed can still hold inline
  signatures, `pebble_store_booksig.go:148-157`; the converter in section 9
  finishes that move first and the inline reader is deleted); the derived
  aggregates `duration` and `file_size`, which `RecomputeBookAggregates` owns
  (`pebble_store_book_aggregates.go:50`); and the scan-cache keys
  `last_scan_mtime`, `last_scan_size`, `needs_rescan`
  (`pebble_store_scancache.go:381-391`). R5 shows these keys lead the
  change counts between consecutive copies (duration 1,287, last_scan_mtime
  390, last_scan_size 370, file_size 297, needs_rescan 255); versioning them
  would fill the retained 50 with churn. A reflection test ties every key in
  the set to a `Book` JSON tag, so a renamed field fails CI. Adding a key to
  the set is a spec change.
- **Post-hash** `h`: xxhash64 over the versioned projection of the row as
  written at T, keys sorted, each as length-prefixed key and raw value bytes
  exactly as stored. `h` is what lets a reader detect that some write changed
  a key without recording it. A write that changes only unversioned keys is
  therefore not a bypass and writes no entry.
- A change entry holds every top-level key of the versioned projection whose
  stored bytes differ between the row before T and the row written at T. It
  is a raw-JSON diff, never a decode into `Book` (which would drop keys the
  struct no longer has) and never `ChangedBookFields` (which skips `author`,
  `authors`, `series`, `metadata_provenance` on purpose,
  `book_field_render.go:33`).

There is no per-book head record. The writer's one reverse seek to the newest
`bookhist:` entry (iterator bounded to the `bookhist:<bookID>:` prefix) gives
the last id, its `h` and its `n`. Nothing older is read on a write.

### 4.4 Reading a version

`reconstruct(id, T)` first requires that an entry `bookhist:<id>:<T>` exists
(exact key Get). If it does not, it returns `ErrVersionNotFound{book, T}`,
including when T is older than every retained entry or was pruned. It never
picks a nearby version. `GetBookAtVersion`, `RevertBookToVersion` and
`?include_data=true` all go through this check; today `GetBookAtVersion` is
an exact Get (`pebble_store.go:3200-3205`) and three callers rely on the
refusal (4.1: ChangeLog revert, tag-comparison fallback, merge undo).

Then: find the nearest keyframe K >= T; if none, the base is the live row.
For each change entry E with T <= E < K, newest first: check that the hash
of the working state equals `E.h`, else return `ErrVersionChainBroken{book,
at}`; then apply `set` and `unset`. Nothing is re-hydrated: `author` and
`series` are stored in the row. Keys in `unversionedBookKeys` are omitted
from the reconstructed result.

`reconstruct`, `ListBookVersions(include_data)` and `LastHistoryValue` read
the live row and the book's `bookhist:` range from one Pebble `Snapshot` (or
one indexed-batch read view in the converter), so a concurrent write can
never show up as `ErrVersionChainBroken`. `reconstruct` takes a
`pebble.Reader`, so the converter can pass an indexed batch and the
production code path verifies the staged conversion (section 9).

A restore reconstructs from that snapshot outside the stripe and writes the
row through the locked chokepoint. A whole-row overwrite is intended. A
restore keeps the current live values of the unversioned keys: it never
writes a past `needs_rescan`, scan stamp, `duration` or `file_size`, and it
queues `RecomputeBookAggregates` for the book. It does not touch the
signature (the signature describes the audio, not the metadata).

### 4.5 Writing

In the book chokepoint, with the book's write stripe held:

1. Read the stored row bytes and the newest history entry (id, `h`, `n`).
2. Build the new row bytes and diff the versioned projections.
3. **No change:** write no history entry. In release B the row is still
   written and `updated_at` still bumps, so no caller sees a difference. A
   counter records how often a write would have been skipped entirely,
   split two ways: writes whose row bytes are identical, and writes that
   changed only unversioned keys. Release F needs the first number for the
   full skip; a write in the second group still has to land. The full skip
   (no row, no memdb reload, no reindex, no notification) is a later release,
   after the counter is read on prod and the callers that rely on a write
   happening are inventoried (section 9, release F). When the full skip lands
   it keeps three "ensure" steps that today are side effects of a write: the
   two self-heal index Sets (`pebble_store.go:2876`, `:2960`) and clearing a
   stale undecodable marker (`:2887-2893`), each as "read, write only if
   needed".
4. **Change:** if the newest entry's `h` does not match the stored row (a
   bypass happened), or the newest entry's `n` is 19, write a keyframe of
   the old row. Otherwise write a change entry. Keyframes every 20 exist to
   limit damage: under P4 one unreadable entry makes everything older than it
   unreachable up to the next keyframe. The cadence comes from `n`; the
   chokepoint never walks back through older entries.
5. **Pin reason (optional argument):** when given, the chokepoint lays a
   pinned keyframe of the stored row as it stood just before this write, in
   the same batch, even when the write changes nothing, and returns that
   entry's id (4.6).
6. Row, history entry and indexes commit in one batch. Before release C, the
   signature side entry is in that batch only when its marshalled bytes
   differ from the stored ones; a write that does not change the signature
   does not rewrite it (today `writeBookSigToBatch` re-marshals and Sets the
   ~22 KB sidecar on every `UpdateBook` of a signed book,
   `pebble_store.go:2844`, `pebble_store_booksig.go:190-200`, which alone
   breaks G1). From release C on, the signature is not part of the book
   batch at all; it is written only by `SetBookSignature` /
   `ClearBookSignature` (5.5).

`CreateBook` refuses an id that already exists (today it overwrites,
`pebble_store.go:2520-2532`).

### 4.6 Pins

A pin is a pinned keyframe. There are two ways to lay one:

- Through the chokepoint's pin reason (4.5 step 5). `MergeBooks` passes a pin
  reason on its first write to **every** participant (the winner and all
  losers; the winner is the one `MergeBooks` itself decides, not the
  prediction in `merge_journaled.go:137-141`). It returns a map from book id
  to pre-merge entry id. The journal patch in `merge_journaled.go` stores
  those ids in `WinnerPreMergeTS` / `LoserPreMergeTS`. `newestSnapshotNanos`,
  `preMergeSnapshotNanos` and the per-id baselines are deleted; the merge
  flow makes no standalone pin call. Only a pin laid under the stripe in the
  same batch as the merge's own first write captures the exact pre-merge
  row: a pin taken before `MergeBooks` leaves a window in which a concurrent
  `ModifyBook` lands and is thrown away by the whole-row revert (today's
  baseline lookup has the same window). Note: undo is still a whole-row
  revert, so writes that land after the merge are still lost on undo; that
  is unchanged and out of scope.
- `PinBookVersion(id, reason) (versionID, error)` for callers that have no
  write of their own. It takes the book's write stripe, reads the row and
  the newest entry, assigns the id by the 4.2 rule, and commits the keyframe
  in one batch, so it cannot collide with a concurrent chokepoint write.

Pin release: the merge journal (`dedup:automerge:` keys in the main store,
read through `EmbeddingStore`) has no resolved or finalized state
(`dedup_automerge_journal.go:36-60`), and nothing deletes its entries, so
every entry counts as unresolved. **Pins are never released automatically in
release B.** The nightly prune and the archive (section 6) treat
journal-referenced pins as permanent. Releasing pins needs a later,
owner-approved change that first gives the journal an explicit end state.
When that exists, release takes the stripe, re-reads the entry, and does
nothing if the entry is absent (book deleted or entry pruned); it never
writes an entry it did not just read, so it cannot resurrect a `bookhist:`
key after `DeleteBook`'s range delete (book ids can be caller-supplied and
re-created, `pebble_store.go:2519-2531`).

### 4.7 Retention and deletion

- A nightly prune keeps the newest 50 entries of real change per book
  (proposed, Q1; scan bookkeeping and derived aggregates are not versioned,
  4.3) plus all pinned keyframes. Removing the oldest entries never affects
  newer ones; `n` on the kept entries is unchanged. Entries are never removed
  from the middle. The prune works one book at a time under that book's
  write stripe and follows the concurrency paragraph in section 9.
- The existing manual job `prune-book-snapshots` is deleted in release B. It
  takes no stripe, ignores pins and would race the converter
  (`internal/maintenance/jobs/prune_book_snapshots.go:45`, `:117-150`).
- The per-book endpoint `POST /audiobooks/:id/cow-versions/prune` and the
  history dialog's Prune button (hard-coded keep 5) are also removed in
  release B, for the same reasons: no stripe, no pin check, no dry run
  (`handlers/metadata/handler.go:764-787`,
  `web/src/components/MetadataHistory.tsx:182-193`). `PruneBookSnapshots` is
  deleted from the store and from every interface that declares it
  (`iface_book.go:315`, `metadata/interfaces.go:86`, `maintenance/job.go:266`).
  History is deleted only by the nightly prune (newest 50 plus pins; the
  first run is dry).
- `DeleteBook` (hard delete) removes the book's `bookhist:` range in the same
  stripe-held batch as the row delete. Today it leaves history forever
  (`pebble_store.go:3351`). The existing callers keep working
  (`internal/reconcile/reconcile.go:887`, `itunes_regroup.go:500`,
  `handlers/versions.go:903`). Transcripts: in release B, before the signal
  store exists, `DeleteBook` does not remove the `bookhist:` range of a book
  whose row or history holds `intro_transcription` (`Book.IntroTranscription`,
  `store.go:378-395`). It writes a final keyframe of the row, keeps the range
  (as today's code already does) and counts these books for the owner. From
  release C on, `DeleteBook` first reads the transcript and puts
  `orphan:book:<id>:<sum>` to the signal store with a sync and a read-back
  **before taking any stripe**; then, under the book and owner stripes, it
  re-reads the row, and if the transcript checksum differs from the one
  written it releases the stripes and retries; otherwise it commits the
  delete, range included. No signal-store write happens under a stripe
  (`pebble_store_book_lock.go:30-31`).

### 4.8 API

- `GET /audiobooks/:id/cow-versions` returns `{timestamp, kind, pinned,
  changed_fields}` per entry, reverse-iterated, cost O(limit). `data` is
  returned only with `?include_data=true`, reconstructed.
- Store surface: `ListBookVersions`, `GetBookAtVersion`,
  `RevertBookToVersion`, `PinBookVersion` (ships with the chokepoint in B2),
  `LastHistoryValue`, `PruneBookVersions`, behind a capability interface
  (width ratchet). `GetBookAtVersion` and `RevertBookToVersion` are exact-id
  only (4.4); there is no nearest-time call, and if the owner wants one it is
  a new feature for section 12. `GetBookSnapshots`, `BookSnapshot` and
  `PruneBookSnapshots` are deleted; `PruneBookVersions` is called only by the
  nightly prune op and has no HTTP route.
- `booksig_recovery_audit` uses `LastHistoryValue(id, "description")`, which
  reads change entries and keyframes newest-first. Its signature recovery
  becomes "rebuild from the stored fingerprints"; the prod dry run found zero
  signature wipes (`booksig_recovery_audit.go:42-45`).
- Archive eligibility (section 6) uses "not named as winner or loser in any
  merge journal entry" in place of "no unresolved merge journal entry".

### 4.9 Expected effect (estimate, same denominator both sides)

Stripped row is about 2.2 KB (19.6% of 11.1 KB, R5); a change entry for the
commonest changes is 60-300 B. At 50 retained entries per book: 3 keyframes
x 2.2 KB + 47 x 0.2 KB = 16 KB per book, against 607-884 KB measured for
primary books. For the 41,723 primary books: about 0.7 GB against 25-37 GB.
Non-primary and retired books (81k more rows) were not sampled; the census
measures the whole family before release B, and the plan states what happens
if it contradicts these figures (release A exit).

Bytes per book write for a signed book: today about 22 KB of signature
sidecar (4096 uint32 as base64 plus the mask, rewritten on every save) plus
an 11 KB history copy; after release B, the row plus a change entry of
60-300 B and no sidecar write unless the signature changed; after release C,
no sidecar at all in the book batch.

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
small transcription fields, and the small fingerprint retry state
`FingerprintFailedAt`, `FingerprintFailureReason` and
`FingerprintFailureDetail` (the backfill gate at `acoustid/backfill.go:518`,
the LSH builder, the reason filter at `pebble_store_stats.go:784` and signal
coverage at `signal_coverage.go:192-199` read them, and every attempt sets or
clears them, `backfill.go:572-574`, `:598`; they are not signal data).

Main store, **beside** the file row, one key per signal kind:
`bfsig:<fileID>:<kind>` = `{sum: <xxhash64 of the bytes>, len, version,
duration_sec, file_hash it was computed from}`. The signal reference is
deliberately not a field of the row: the batch upsert, move, PID-transfer and
`UpdateBookFile` paths read whole rows before taking any lock and rewrite
them whole (`pebble_store_book_lock.go:56-61`,
`pebble_store_bookfiles.go:2185-2229`), so a reference on the row could be
written back stale by a batch that started before `SetBookFileFingerprint`
committed. A separate key that only `Set`/`Clear` write cannot. memdb loads
`bfsig:` as a side map for "has a current print". The reference replaces
`acoustid_fp_version` and `acoustid_fingerprint_duration_sec` as the source
of "has a current print": `version` 0 stays legacy (`store.go:944-950`, the
pre-2026-09-19 prints whose bytes are not frames), so `HasCurrentPrint` is
false for them.

Main store, on the book row: `sigref {sum, len, version, built_at,
coverage_pct}`, the book signature reference, written only by the book
chokepoint under the book stripe. `coverage_pct` stays on the row so search
still shows the partial-fingerprint chip without reading the signal store.

Signal store, immutable and content-addressed:

- `fp:<fileID>:<sum>` = 1 format byte + raw fingerprint bytes (no JSON, no
  base64).
- `tx:<fileID>:<sum>` = file intro transcript text; `txb:<bookID>:<sum>` =
  book intro transcript text (`Book.IntroTranscription`, written by
  `maintenance.transcribe-book-intros`, `intro_transcribe.go:1292`). The
  parsed `Transcribed*` scalars, `IntroTranscribedAt` and `TranscribeStatus`
  stay on the book row.
- `booksig:<bookID>:<sum>` = the book signature (all six BookSig fields,
  rebuildable). Immutable and content-addressed like prints; the book row's
  `sigref` names the current one.
- `fpwin:` windowed and whole-file prints (5.2a).
- `orphan:file:<fileID>:<sum>` = `{path, size, file_hash, original_file_hash,
  former book, refs:[{kind, sum, len}]}` and `orphan:book:<bookID>:<sum>` =
  `{..., transcript}`, for signals whose record was removed, so they stay
  findable. `<sum>` is the xxhash64 of the value; the keys are never
  overwritten, so a second delete of a re-created id cannot destroy the
  first record.
- `held:file:<fileID>` = the verbatim stored bytes of a file row the release
  C converter held (section 9, step 2d). Never deleted (P7).

Signal store, mutable (not covered by P7):

- `diag:<fileID>` = the latest failure diagnostic blob
  (`FingerprintDiagnosticJSON`), overwritten in place, deleted by
  `ClearBookFileFingerprint` or by a successful fingerprint. No reference on
  the row and no checksum check; a missing `diag` reads as "no diagnostic".
  `GetBookFileDiagnostics` / `SetBookFileDiagnostics` are plain keyed calls.
- `fpwin_fail:` tombstones (diagnostics, 5.2a).

Large values (whole-book transcripts with timing, whole-book fingerprints)
are stored with Pebble's own value separation. There is no separate blob
directory. The signal store opens with `FormatMajorVersion:
pebble.FormatValueSeparation`, written as that named constant and never as
`FormatNewest` (section 9, Guard), and with
`Experimental.ValueSeparationPolicy` returning `{Enabled: true, MinimumSize:
64<<10, MaxBlobReferenceDepth: 10, TargetGarbageRatio: 1.0}` (pebble v2.1.7
`options.go:747-751`, `:1162-1200`). Signal values are written once and never
overwritten, so they never become garbage and blob rewrites stay off (ratio
1.0). The separated values live in Pebble blob files inside the signal-store
directory, so the checkpoint (`checkpoint.go` copies referenced blob files),
the restore, the pairing check, `ErrorIfNotExists` and `EstimateDiskUsage`
cover them without extra code, and reconcile deals with two stores, not three
kinds of storage. Risk: the option is under `Experimental`; a Pebble upgrade
re-runs the C1 store-open test. A single value must fit in one Pebble batch
(well under 4 GiB); `Put` rejects larger values with an error rather than
truncating.

### 5.2a Windowed and whole-file prints

The `fpwin:` family (`fpwin:f:<file>:<kind>:<slot>`, `fpwin:p:...`,
`fingerprint_window.go:19-25`) holds windowed prints and, on the roadmap,
whole-file prints as `WindowKindWhole` rows. They are fingerprints, so P7
covers them, yet today every `book_file` delete cascade-deletes them
(`commitWithWindowCascade` -> `stageFileWindowCascade` ->
`stageFingerprintWindowDeletes`, `pebble_store_fpwin.go:373-437`),
`DeleteFingerprintWindows` deletes them (`:140-156`) and
`CarryOverFingerprintWindows` deletes each donor's rows after copying
(`:166-176`; the keeper wins on the same kind and slot, so a donor's differing
bytes are lost outright). Rules:

1. The whole `fpwin:` family and `fpwin_fail:` move to the signal store. The
   key gains the checksum: `fpwin:<ref>:<kind>:<slot>:<sum>`. A recompute
   writes a new key and never overwrites. Readers take the newest entry per
   (kind, slot) by `ComputedAt`. No per-window reference goes on the
   main-store row: windows are found by a prefix scan per file ref and file
   ids are never reused.
2. `PutFingerprintWindow` follows 5.3: synced put, read back, compare
   checksum.
3. The `book_file` delete cascade and `DeleteFingerprintWindows` stop deleting
   bytes. They write `orphan:file:` (5.3, Delete) and leave the windows in
   place. Only `fpwin_fail:` tombstones may be dropped.
4. `CarryOverFingerprintWindows` copies the donors' windows under the keeper
   (the keeper still wins on the same kind and slot) and never deletes donor
   rows. Each donor gets an `orphan:file:` record.
5. `WindowsForFile` builds the virtual head row (`fingerprint_window.go:34-35`,
   `legacyHeadWindow` reads `f.AcoustIDFingerprint` at `:205-219`) from
   `GetBookFileFingerprint`. `ErrSignalMissing` / `ErrSignalCorrupt`
   propagate as errors and never become "no head". The window backfill
   planner counts those files and skips them; it does not schedule a head
   decode for them.
6. Whole-file and whole-book prints (`kind=whole`) are rows of this family in
   the signal store. The "separate specs" cross-reference in section 9 points
   here.
7. The reconcile op and the census add these invariants: every window's file
   id has a row or an `orphan:file:` record, and every stored window checks
   out against its key's checksum.

### 5.3 The check: written and right

- **Write:** put the signal (synced), read it back, compare the checksum,
  then commit the main batch that sets the `bfsig:` reference. A crash
  between the two leaves an unreferenced signal, which is harmless. The
  read-back checks the encoding and the checksum, not the disk: a read
  straight after a Set is served from the memtable. What covers the disk is
  the sync itself, the checksum check on every read, and the reconcile op.
  Live writes keep one synced signal put per call; the offline converters
  group-commit (section 9).
- **Replace:** a new fingerprint has a new checksum and therefore a new key.
  The old bytes are never overwritten, so a crash cannot leave a row pointing
  at bytes that are gone.
- **Clear:** unreference in the main store only. The bytes stay (P7).
- **Delete:** the store's file-row delete primitives (`DeleteBookFile`
  `:1340`, `DeleteBookFilesForBook` `:1433` and `DeleteBookFilesByIDs` `:1578`
  in `pebble_store_bookfiles.go`, plus any future primitive; the P1 ratchet
  rejects a raw `book_file:` Delete outside them) read each file's row, its
  `bfsig:` keys and its `fpidx_meta:<fileID>` under the owner stripe. For
  every `bfsig:` key the file has, the primitive writes
  `orphan:file:<fileID>:<sum>` to the signal store, synced and read back.
  Then each of the three primitives stages, in the same main batch as the row
  delete, the deletion of that file's `bfsig:` keys, its `fpidx` keys (the
  ones its `fpidx_meta` names) and its `fpidx_meta`, and commits that one
  batch. The references and the index go with the row in one atomic write;
  the signal bytes stay (P7), covered by the orphan record. A crash between
  the orphan write and the main commit leaves an orphan record for a row that
  still exists, and reconcile clears that record. A batch delete writes all of
  its orphan records first, then commits one main batch. Callers today: `combine_journal.go:850`,
  `itunes_clone_into_library.go:1061`,
  `dedupe_book_file_rows_crossfolder.go:230`, `maintenance_fixups.go:373`.
- **Read:** every read verifies length and checksum against the `bfsig:`
  reference. A mismatch or a missing key returns `ErrSignalMissing` /
  `ErrSignalCorrupt`, never "no fingerprint". Whole-library readers count and
  skip such rows and report the count; they do not abort.
- **Pairing:** both stores carry the same store id and a format stamp. The
  app refuses to start if the signal store is absent after first creation
  (opened with `ErrorIfNotExists`), or if the ids differ. This catches a
  wrong path, an unmounted dataset and a restore of one store without the
  other. The pairing check lives in the shared store-open function with the
  format-stamp check (section 9), so every entry point gets it.
- **Reconcile op**, nightly and on demand, read-only, runs at `NumCPU`
  (section 9, concurrency): every `bfsig:` and `sigref` reference resolves
  and matches; lists rows whose signal is missing or corrupt. Signal keys are
  classified four ways: referenced by a live reference (a `bfsig:` key, a
  book's `sigref`, or a book transcript reference), referenced by an archived
  `zarch:bfsig:` key (section 6, once release G lands), covered by an
  `orphan:file:` / `orphan:book:` record, or unexplained. Only "unexplained" is reported as a
  defect and exported as a metric. Index checks: `fpidx_meta:<fileID>` exists
  if and only if a current fingerprint `bfsig:` exists; `fpidx_meta`'s
  recorded sum equals `bfsig.sum`; every `fpidx` value equals the file's
  current `book_id`. Each mismatch is its own count and metric. Window
  invariants per 5.2a. Invariants exported as metrics: files with a
  fingerprint reference = references that resolve; same for transcripts.
- **Backup:** the two stores live in sibling datasets under one parent, so
  one recursive snapshot is consistent across both. The app's own backup
  (`CreateBackupWithCheckpoint`, used by the backup op and the pre-organize
  auto-backup, `internal/backup/backup.go:428-535`,
  `organizer/service.go:728`) takes both stores and writes
  `audiobooks.pebble/` and `signals.pebble/` into one archive. It checkpoints
  the main store first and the signal store second, each staged beside its
  own store (`checkpointStagingRoot` per store path, `ensureSameDevice` per
  store). Because signals are written before the reference that names them
  and are never deleted, this order guarantees every reference in the
  archived main store resolves in the archived signal store; the other order
  can archive references whose signals are missing. Restore
  (`RestoreBackupIn`, `:741-785`) always restores the main store. It extracts
  the archived signal store only when none exists at the target; an existing
  signal store is kept, because it is a superset of anything an older main
  store references. Restore refuses if the archive has no signal store and
  none exists at the target. The reconcile op runs once after a restore,
  before serving. The signal store is excluded from the full-compaction op;
  write-once data gains nothing from it.

### 5.4 Type change

`AcoustIDFingerprint`, `IntroTranscription`, `FingerprintDiagnosticJSON`,
and the raw version and duration fields are removed from `BookFile`
(`FingerprintFailedAt`, `FingerprintFailureReason` and
`FingerprintFailureDetail` stay, 5.2). `Book.IntroTranscription` and the six
`BookSig*` fields are removed from `Book` (replaced by `txb:` and `sigref`).
Access is explicit:

- `GetBookFileFingerprint(fileID)`, `GetBookFileFingerprintForRow(row)`,
  `SetBookFileFingerprint(fileID, fp, durationSec, version, expectedFileHash)`,
  `ClearBookFileFingerprint(fileID)`.
- `GetBookFileTranscript` / `SetBookFileTranscript`, `GetBookFileDiagnostics`
  / `SetBookFileDiagnostics`.
- `GetBookTranscript` / `SetBookTranscript` / `ClearBookTranscript`
  (`intro_transcribe.go`, `intro_migrate_single_file.go` move to these).
- `GetBookSignature(bookID)` / `SetBookSignature` / `ClearBookSignature`.
  `hydrateBookSig` is deleted from `GetBookByID` ("THE hydration point",
  `pebble_store.go:1344-1355`), `GetBooksByIDs` (up to 10,000 ids per search
  call, `:1366-1373`) and the search hydrate
  (`pebble_store_search_hydrate.go:103-106`), so no book read or search
  touches the signal store. `GetBookSignature` verifies against `sigref`; a
  missing or corrupt entry returns `ErrSignalMissing` / `ErrSignalCorrupt`
  to its caller and queues a rebuild. That error never reaches a book read
  or a search result. Consumers moved onto it: dedup `bookSignature` and its
  conflict cache, the dataset builder, the registry `ReqFieldSet` predicate,
  acoustid synthesize, the recovery audit, and the search payload's coverage
  (which reads `sigref.coverage_pct` from the row).

Cost: 119 occurrences of `.AcoustIDFingerprint` in 40 non-test files move to
these calls. The `bfPreserveAlways` guards for the moved fields are deleted;
a row write can no longer touch them. The merge judge that today clears
derived audio fields when an incoming duration is unknown
(`bookfile_merge.go:288-290`, `:353`) unreferences instead of clearing, and
only when the audio hash actually changed: the upsert then stages deletion of
`bfsig` plus `fpidx` / `fpidx_meta`, all read under the owner stripes as
described in 5.5.

Consumers outside the packages C3 slices: `GET /audiobooks/:id/files`
(`handler_files.go:156-170`) drops `intro_transcription` and
`fingerprint_diagnostic_json` from its fixed key list and adds
`has_fingerprint`, `fp_version` and `transcript_len`, taken from `bfsig:`, so
the request reads nothing from the signal store. The fingerprint-diagnosis
endpoint and the transcript view call `GetBookFileDiagnostics` and
`GetBookFileTranscript` for one file at a time. admindebug's `PATCH
/book-files/:id` builds its writable set by reflection over `BookFile` JSON
tags (`admindebug/handler.go:140-150`); with the reference off the row there
is nothing to protect, and a test asserts no signal-related key is PATCHable.
Readers of `BookFile.UpdatedAt` (ABS `mtimeMs`/`ctimeMs`/`updatedAt`,
`abs/mapper.go:343-344`, `:809`, `:1033`; `handler_files.go:96`, `:164`;
`GetUpdatedAt` in `fingerprint.FileWithFingerprint`) are inventoried by C0
for release F (5.6).

**Held rows (until release E deletes the converter):** the file chokepoint
checks the stored row's raw JSON for any legacy signal key
(`acoustid_fingerprint`, `intro_transcription`,
`fingerprint_diagnostic_json`, `acoustid_fp_version`,
`acoustid_fingerprint_duration_sec`). The last two are on the list because
the converter holds a row that has fingerprint metadata without fingerprint
bytes (section 9, release C); without them, such a row would be decoded and
re-marshalled and its metadata stripped. If it finds one, the write is refused with
`ErrRecordHeld` and counted in a metric shown on the held-list page.
`updateBookFileLocked` decodes the stored row into the struct and marshals
the struct back (`pebble_store_bookfiles.go:766`, `:831`); once the fields
are gone from `BookFile`, the first scan of a held row would otherwise drop
those bytes silently (P7).

**Known defect, owner decision before C3 (Q7):** the LSH Hamming tier
(Tier-0) is dead today. Both candidate reads (`internal/dedup/engine.go:4927`,
`internal/dedup/collectors_acoustid.go:305`) call `GetBookFileByID("",
candID)`, which builds the key `book_file::<id>`; that key never exists, so
the tier has never matched on a real store. The "scan fallback" comment at
`collectors_acoustid.go:302-304` is false and the only test
(`collectors_acoustid_test.go:48`) uses a stub that ignores the book id. C3
must neither hide this behind the new accessor nor fix it in passing. If
revived: `LookupAcoustIDCandidates` gets a variant that returns (fileID,
bookID) pairs from the `fpidx` value it already stores
(`pebble_store.go:5517-5519`); the candidate is read by primary key and its
fingerprint through `GetBookFileFingerprintForRow(row)`: two reads per
candidate, not three, and up to 200 candidates per print across about 605k
prints. The false comment is fixed or removed and a real-store test proves a
candidate pair is emitted. If deleted, the two loops and the stub go.

### 5.5 Indexes and moves

The fingerprint index stays in the main store. Its values are the owning book
id (`pebble_store.go:5518-5520`), so a move between books must update them.
Today that works only because the fingerprint bytes are on the row; the move
path rebuilds the index from them (`pebble_store_bookfiles.go:2205`,
`pebble_store.go:5507`). New rule: when `book_id` changes, the index values
are rewritten from `fpidx_meta:<fileID>`, which already holds every band and
subprint and now also records the sum it was built from. Index keys are
otherwise written only by `Set` / `ClearBookFileFingerprint`.

Locking rule (no new lock, lock order unchanged): `Set`/`Clear(fileID, ...,
expectedFileHash)` read the row to learn its book, take that book's owner
stripe (`lockBookOwners`, the stripe `commitBookFileBatch` takes,
`book_delete_owns_files.go:286-297`), and re-read the row under it. If
`book_id` changed, release and retry. If `file_hash != expectedFileHash`,
refuse with `ErrStaleFingerprint`. Still under the stripe, read the current
`bfsig` and `fpidx_meta` from the committed DB, then stage the old-key
deletes, the new `fpidx` keys, `fpidx_meta` and `bfsig` in one batch, and
commit before releasing. A move, and any path that rewrites `fpidx` values,
reads `fpidx_meta` for each moved file **inside** the `commitBookFileBatch`
locked section, under the source and target owner stripes it already holds,
never before taking them (today `pebble_store.go:5541-5560` reads it from
`s.db` before any lock). Both sides then hold the owning book's stripe, so a
move racing a `Set`, and two `Set`s on one file, serialize. No file stripes
are added: a file stripe must never be held while a book stripe is taken.

Book signatures: setting one follows 5.3. Put `booksig:<bookID>:<sum>` with a
sync and read it back **before** taking the stripe; then set `sigref` in the
book batch under the stripe. `ClearBookSignature` becomes a row-only write
under the stripe that clears `sigref`; the bytes stay. Clearing on an
incomplete fingerprint set stays legal (`acoustid/backfill.go:707-722`); the
signature is rebuildable. The nightly reconcile op covers `sigref` like the
other references.

### 5.6 Changed-detection in the file chokepoint

After merge, compare the file row's **versioned projection**: the stored JSON
object minus `updated_at`, built and compared the same way as 4.3. Stamp
`updated_at` only after the comparison finds a difference (today
`updateBookFileLocked` and both batch upsert paths set `file.UpdatedAt = now`
before marshalling, `pebble_store_bookfiles.go:780`, `:1945`, `:2008`, so a
byte compare could never find two rows equal and a no-change rescan would
rewrite all 777,883 rows). Projection equal: no change. In release C the row
is still written, `updated_at` still bumps and the memdb, stats and
quick-query side effects still run, so no caller sees a difference; a
file-row would-skip counter records how often the write could have been
skipped. The full skip (no row write, no `UpsertBookFileToMemDB`, no
`InvalidateLibraryStats`, no `MarkQuickQueryDirty`, `:869-871`) lands in
release F with the book-side skip, after C0 inventories those side effects
and the `updated_at` readers (5.4). Projection differs but no indexed field
differs: write the row only and do not delete or rewrite the secondary
indexes (today every update deletes and rewrites every secondary index,
`:840`); this ships in C.

Recompute the book only when the fields `RecomputeBookAggregates` actually
reads changed: duration, file size, missing, file path, book id, file hash,
original file hash, original filename
(`internal/dedup/book_runtime.go:129-140`, `book_own_folder.go:259-277`).
Fingerprint duration leaves the row projection: `SetBookFileFingerprint` /
`Clear` trigger the book recompute themselves. The trigger is a hash of
exactly that projection, and a reflection test ties the projection to what
the recompute reads.

**memdb write-through ordering.** Today the move path, the batch upserts and
`UpdateBookFile` write memdb after `commitBookFileBatch` releases the owner
stripes (`pebble_store_bookfiles.go:866`, `:2070`, `:2275`, `:2609`;
`book_delete_owns_files.go:305-307`), and memSync applies calls in arrival
order with no version check (`memdb_sync.go:66-98`). An older copy can
therefore land after a newer one, and today only the next redundant write
repairs it. Changed-detection removes that repair. So C5 gives
`commitBookFileBatch` the rows to write through (upserts and deletes) and
applies them to memdb after a successful commit and before `unlock()`, on the
same path that runs the WAL sync if needed. Every conflicting pair of file
writers shares the owner stripe of the row's book, so memdb then sees writes
in commit order. No post-commit `UpsertBookFileToMemDB` /
`DeleteBookFileFromMemDB` call remains in a file writer. An equal write still
writes nothing, memdb included.

### 5.7 Expected effect

Warmup today reads 4,003 MB of file rows and keeps 936 MB. With signals out,
it reads about the 936 MB. The 110 s file phase should fall substantially;
the figure is measured on the sandbox rehearsal, not predicted, because 61%
of that phase's CPU is block loading, not decoding (`memdb_store.go:156`).

Cost to whole-library readers: `lsh-index-build`
(`plugins/dedup/lsh_index_build.go:198-209`, `NumCPU` workers at `:331`) and
acoustid signature synthesis (`backfill.go:695`) go from one contiguous range
read per book to one verified point read per print in the signal store (about
605k reads on a full rebuild; file ids of one book are not adjacent under
`fp:`). Checksum verification is negligible (xxhash64 over about 5 KB is
under 1 µs); the cost is the I/O, and the signal-store settings in section 8
are chosen for it. These are measured before and after in the C9 rehearsal.
If the slowdown is material, the fix is a sequential scan of the `fp:` prefix
joined against `bfsig:` references, not per-book batching.

## 6. Archive: retired books leave the live key space

The cache loads 123,271 book rows; about 77,000 are live (R4). Roughly 46,000
rows are soft-deleted or merged away and are paid for by every warmup, scan
and count. Release A's census reports the exact count and bytes.

Design, in the main store: retired rows move to key families outside every
live range (`zarch:book:<id>`, `zarch:file:<bookID>:<fileID>`,
`zarch:hist:<bookID>:<id>`) in one batch per book. Same store means the move
is atomic and needs no forwarding entry: `GetArchivedBook(id)` is a point
read, and `merged_into` is already on the row. Archived file rows keep their
`bfsig:` references unchanged (moved to `zarch:bfsig:`), and reconcile reads
them (5.3). The move and the restore run per book under the book's write
stripe (section 9, concurrency).

A book is eligible only when no reader of retired books can need it:

- retired longer than the undo window (the undo journal's own retention
  setting, section 7), no unreverted journal row, not named as winner or
  loser in any merge journal entry (4.6), no pin;
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
  1. **Pack, lossless.** An operation is eligible only when its status is
     `completed`, `failed` or `canceled` and its `CompletedAt` is more than
     one day old. No `interrupted_*` status is ever packed or aged out,
     `interrupted_dropped` included: `RetryInterrupted` (`retry.go:29-35`)
     and the resume paths (`requeueInPlace` ->
     `ResetOperationV2ForResume`, `resume.go:252-300`,
     `pebble_store_ops_v2.go:313-330`) re-queue the **same** id and the rerun
     keeps logging under it, although all four `interrupted_*` statuses carry
     a `CompletedAt` (`:1186-1197`). The three eligible statuses are final
     because nothing moves them back to queued. The status and `CompletedAt`
     are checked again under `opsMu` in the same critical section that reads
     the rows and commits the batch. The batch writes
     `opv2:logpack:<op>:<chunk%06d>` keys, each an independently
     zstd-compressed run of whole log lines of at most about 1 MB raw, in
     log order, recording its first sequence number and line count, so a
     reader can seek to a line without decompressing earlier chunks; and
     `opv2:logdigest:<op>` (line counts by level, first and last timestamps,
     every warning and error verbatim up to a cap, first and last 20 lines).
     The row range is removed with one `DeleteRange` over `[first key read,
     successor(last key read))`, never up to the end of the prefix, in the
     same batch, so the swap is atomic. A row that lands after the read, such
     as a late terminal flush (`reporter_db.go:234-255`, the flush join is
     bounded), stays as a loose row. Readers merge the pack and loose rows in
     key order. If a pack already exists when loose rows are found, the next
     run decodes it, merges the loose rows in key order, rewrites the chunks
     and digest, and removes the rows it read. A pack is never overwritten
     with only the newer rows. Nothing is lost.
  2. **Age out.** After the retention period (proposed, Q2) the pack is
     deleted with a `DeleteRange` over `opv2:logpack:<op>:` and the digest
     stays as long as the operation record. Only `completed`, `failed` and
     `canceled` operations age out. First run is a dry run with a summary for
     the owner; each run is capped.
  Readers (`GetOpLogsV2`, download, detail page) read a pack transparently.
  A tail read is a reverse seek over `opv2:logpack:<op>:` that decompresses
  only the last chunks needed to fill the requested limit; paged reads seek
  to the chunk that holds the start line.
- **Operation records** (row, state, errors, strikes, digest, index entries)
  are removed after 180 days (proposed, Q2), one batch per operation, only
  for `completed`, `failed` and `canceled` operations. Rows in any
  `interrupted_*` status (an `interrupted_ask` can wait on a user for longer
  than 180 days) or any non-terminal status are kept and counted in the run
  summary for the owner.
- **Separate settings.** The undo journal is pruned by the general
  log-retention setting today (`internal/logger/retention.go:27-39`). It gets
  its own setting, the **undo window** that section 6 refers to, so no log
  setting can shorten undo, and the by-book journal index from #3704 is
  pruned in the same batch as the journal row. The undo-journal prune does
  not delete a `book_file_delete` / `book_file_repoint` row until the release
  C ledger scan (section 9) has stored its signal bytes; rows written before
  C are skipped until that scan is complete.
- **Dead family.** `operationlog:` has no writer (`sysinfo/service.go:309`);
  its rows, pruner and reader are removed.

## 8. Request-path scans, census, Pebble settings

- **Timeline.** Two small indexes, staged in the same Pebble batch as the
  operation row by one helper, `stageOpRow(batch, old *OperationV2Row, new
  OperationV2Row)`. The helper sets or deletes `opv2:open:<id>` (while
  `CompletedAt` is nil) and, when `CompletedAt` changes, deletes
  `opv2:done:<old CompletedAt nanos>:<id>` and sets the new done key. Update
  writers call it with `opsMu` held and the old row read under that lock.
  `InsertOperationV2` passes `old=nil` and commits the row, queue, act and
  index keys in one batch (today they are separate synced Sets,
  `pebble_store_ops_v2.go:147-160`). Every other `opv2OpKey` writer
  (`:382`, `:569`, `:586`, `:619`, `:670`, `:687`, and the raw one at `:1309`)
  also goes through the helper, where the index change is empty, so one rule
  covers everything and a CI grep ratchet fails any Set or `pebbleSetJSON` on
  `opv2OpKey` outside `stageOpRow`. A crash can therefore never leave the row
  and its index disagreeing; the stamp-gated startup reconcile only builds
  the indexes the first time. A window query unions open, a ULID seek from
  the window start, and a range read of done from the window start, then
  applies today's predicate to each row (`:902-904`) and de-duplicates by id.
  That is exact for `waiting_deps`, for the `interrupted_*` states that have a
  completion time but are not terminal (`:1190-1194`), and for resumed
  operations.
- **Census.** `GET /diagnostics/db-census`: per-family key count and bytes
  from sstable properties and `EstimateDiskUsage` (R7), cached for a few
  minutes, plus counts the families cannot give: retired books and files,
  history entries per book (distribution), signals referenced and resolved.
  Per-family figures are on-disk entries including tombstones and shadowed
  versions, labelled as such; they move only after compaction. Gates (4.2
  empty legacy family, section 9 step 2e) use bounded iterator counts, never
  the census. The family list is one registry that the schema document is
  generated from; it includes `book:work:`, which stores a full stale book
  copy as its value (`pebble_store.go:2633`; see "separate specs", section 9).
- **db-health** uses the census. No full iteration. The "AI scans" size
  reports its own prefix; today it reports the whole shared store.
- **Counts and prunes.** `/cache/stats` uses the census. `ScanPrefix` and
  `CountPrefix` use the safe upper bound. Prune functions stream in bounded
  batches and use range deletes where a whole prefix goes.
- **Pebble settings**, main store (host: 121 GB RAM, 48 cores, app RSS
  8.5 GB, ZFS ARC about 25 GB; dataset has 6.4 TB free). Exact pebble v2.1.7
  names are in plan A7.

  | Option | Now | Proposed | Why |
  |---|---|---|---|
  | Block cache (`Options.Cache`) | 8 MB | 4 GB, one `pebble.NewCache` shared by the main and OpenLibrary stores | index/filter blocks resident; hit rate from A1 |
  | Bloom filter (`Levels[i].FilterPolicy`) | none | `bloom.FilterPolicy(10)` on levels 0-5; `pebble.NoFilterPolicy` on level 6, set explicitly because levels inherit the policy of the level above (`options.go:479-480`) | a Get for a missing key skips L0-L5 tables without reading them. Get still reads the L6 table: pebble never checks L6 filters on a Get unless `UseL6Filters` is set (`db.go:588-593`, `file_cache.go:655-662`), so filters written into L6 (about 200 MB at 174M keys) would never be read |
  | MemTable (`Options.MemTableSize`) | 4 MB | 64 MB | fewer flushes; raises the large-batch threshold to about 32 MB |
  | Compaction concurrency (`CompactionConcurrencyRange`) | 1 | 1 to 4 | rises above the lower bound only on L0 read-amp, debt, or several waiting manual compactions (`options.go:946-955`) |
  | Manual compaction (`DB.Compact(ctx, nil, []byte{0xff}, parallelize=true)`) | serial (`pebble_store.go:5276`, `ai_scan_store.go:175` pass false) | parallel | pebble splits only at gaps in the in-use key space (`db.go:2011-2031`), so the L5-to-L6 step may still run as one compaction. Measure the speedup on the sandbox; do not assume it |
  | Compression | Snappy | unchanged (ZFS already applies zstd) | |
  | `LBaseMaxBytes`, `TargetFileSizes` | defaults | not proposed yet: measure write amplification (A1) first, then revisit | the eval's F2 raised them; no measurement supports a value |

  The block cache and memtables are allocated off the Go heap (`C.calloc`
  under cgo, which prod builds use, pebble `internal/manual/manual_cgo.go`;
  `Makefile:143`), so `GOMEMLIMIT` does not limit them. The service's cgroup
  cap (`deploy/audiobook-organizer.service` `MemoryMax=12G`, with
  `GOMEMLIMIT=9GiB` and `Restart=on-failure`) must cover: Go heap limit +
  block cache + memtable size x max memtables x open stores (main,
  OpenLibrary, signals) + headroom for the startup migration workers. With a
  4 GB cache this needs about `MemoryMax=18G`; 8.5 GB RSS + 4 GB would
  already exceed 12G and the cgroup would OOM-kill the service into a
  restart loop through a 130 s warmup. Leave `GOMEMLIMIT` unchanged. The
  installed unit is not `deploy/`; the change is installed on the server
  before the cache step.

  The signal store gets its own settings: default (4 KB) data blocks, 10-bit
  bloom filters, a block cache sized to hold the store's index and filter
  blocks (read from the A1 Pebble metrics, not guessed), value separation per
  5.2 so block size does not have to serve whole-book transcripts, no
  scheduled full compaction. Store-open settings come from the environment
  only; the app's saved settings live inside the store they would configure
  (`pebble_store.go:392-397`, `config/persistence.go:814-826`). Pebble metrics
  ship first and run for a day as a baseline; then one setting per deploy,
  each with the metric it should move. Bloom filters reach existing data
  only when it is rewritten. Get never reads L6 filters, and after the full
  compaction most data sits in L6, so the gain is limited to not-found
  lookups that would otherwise read L0-L5 tables. Whether to set
  `primarycache=metadata` on the Pebble dataset once the 4 GB block cache is
  in place (double caching with the ARC) is an owner question (Q8), decided
  by ARC hit rate.

## 9. Releases

Preconditions: PRs #3698, #3699, #3700, #3704 merged before release B (they
edit the same save path and journal; the plan gates individual release A
tasks on the two that touch their files). Free space on each store's dataset
is at least that store's live size: the checkpoint keeps the pre-migration
sstables alive while the conversion's deletes are compacted away, until the
checkpoint is removed in release E.

**Guard, shipped in release A:** a `storage_format` stamp in the main store,
mirrored in a sidecar file beside the store (A4). Every store open refuses a
store whose stamp is above what the build understands
(`StorageFormatTooNewError`, from the stamp or the sidecar). `make
rollback` follows one rule: it swaps in the previous binary only when the
store's stamp is not above the format that binary supports; otherwise it
refuses, swaps nothing, and prints the restore-from-checkpoint steps
(below). Today `make rollback` swaps the binary with no data
restore (`Makefile:725-735`), and an older binary on converted file rows
would read every file as fingerprint-less and erase the fingerprint index
during its first scan (`pebble_store_bookfiles.go:840`,
`pebble_store.go:5507`). The guard is what makes "the backup is the only way
back" true, so the converter raises the stamp **before its first legacy
delete**, not at the end (step 2b). Every store open also refuses a store
whose stamp is older than the build or whose `storage_migration` marker is
present, with the message "start serve to migrate", unless it is the
cut-over itself (below). A store that has no keys when Pebble opens is
checked **before** the counter writes in `newPebbleStore`
(`pebble_store.go:423-437`), stamped current on creation and never
converted; a store that has data but no stamp is legacy.

Every Pebble open (main, signal, OpenLibrary, AI-scan, and the diagnostics
command: `pebble_store.go:393`, `ai_scan_store.go:109`,
`openlibrary/store.go:34`, `cmd/diagnostics.go:209`) passes an explicit
`FormatMajorVersion` constant, today `pebble.FormatValueSeparation`, and
never `FormatNewest`. `Open` ratchets the on-disk format and rewrites the
marker whenever the requested version is higher than the stored one
(pebble v2.1.7 `open.go:561-562`), and a Dependabot bump of the pebble line
in `go.mod` (`7c3ca9dd6` did) would otherwise make the self-taken checkpoint
unopenable by the previous build ("database written in unknown format major
version"). Raising the constant is a release of its own, with no other
format change, listed as a format change in its rollback notes.

**Cut-over procedure, used by releases B and C: a startup migration, inside
the app.** (Owner, 2026-10-03: do it when the app starts, before anything
else, like the database migrations other apps use.) The cut-over is **not**
an entry in `migrations` and has no db-version number; `storage_format` is
its only counter. The versioned runner (`migrations.go:513-545`) records a
version as applied as soon as `Up` returns nil and never re-runs it, which
would make "re-run after a fix" impossible and leave two counters that can
disagree; `getCurrentVersion` returns 0 for a fresh store, so every test and
fresh install would reach the converter; and `InitializeStore` runs the
runner after `NewPebbleStore` has already written counter keys and started
the memdb warmup goroutine (`store.go:1463-1477`,
`pebble_store.go:451-488`), so a conversion there would run under a warmup
that is snapshotting the rows it rewrites. Instead the cut-over is a
separate step, `database.RunCutover`, that only `serve` invokes, after
the config file and flags, logging and TLS settings are loaded and before
`initializeStore` (`cmd/root.go:266`). It opens Pebble itself (`pebble.Open` only: no writes,
no goroutines, no memdb warmup, no counter initialization), runs steps
2a-2e, closes the store, and only then does the normal store open proceed.
`newPebbleStore` is split into open (`pebble.Open` and the stamp check) and
init (undecodable markers, `migrateImportPathKeys`, counters, `NewMemStore`,
`beginMemWarmupBuffering`, warmup) so the shared open path can refuse before
any write. Converters write raw Pebble batches and never call memSync. No
deploy script runs the conversion, no separate tool: deploying the new build
is the migration. Every other entry point (`scan`, `playlist`, `tag`,
`organize`, `diagnostics` in its store-backed mode, `seed`, `dedup_bench`,
child mode, testutil, `cmd/pid-census`) goes through the shared open and
refuses an older or in-progress store.

Two recovery tools are exempt from the stamp guard, by name: `cmd/diagnostics`
raw Pebble mode (`diagnostics query --raw`, `runRawPebbleQuery`,
`cmd/diagnostics.go:207-240`) and `cmd/pebble-inject-skip`. Raw mode only
reads: it iterates one prefix and prints keys and value previews.
`pebble-inject-skip` writes two `setting:transcode_skip_*` keys
(`cmd/pebble-inject-skip/main.go:41-58`), outside every family a cut-over
changes. Neither can ratchet the on-disk format: raw mode passes the pinned
`FormatMajorVersion` (below), and `pebble-inject-skip` passes none, which
pebble resolves to `FormatMinSupported` (v2.1.7 `options.go:1495-1496`), so
it never raises the stored version. Both stay usable on a store the guard
refuses, which is when an operator needs them.

At start `serve` compares the store's `storage_format` stamp with its own:

1. **Stamp current and no `storage_migration` marker:** start normally.
2. **Stamp older, or marker present:** migrate or resume. Before any other
   subsystem starts (no memdb warmup, no scan, no scheduler, no operation
   resume, no API), the app:
   a. opens a minimal read-only status listener on the configured host and
      port with the flag TLS, built from the server's port and TLS settings
      that the cmd layer passes in. It answers `/api/v1/system/version` with
      the real version plus `migrating: true`, and returns JSON with the
      state ("migrating" or "stopped"), the step, progress, held count, the
      stop reason if any, and an ETA. It returns no record contents (no
      paths, titles or ids) and accepts no writes. The config file, flags,
      logging and TLS settings are loaded at this point; what does not exist
      yet is everything that comes from the store or after it: the persisted
      configuration and settings (`loadConfigFromDB`), settings encryption,
      sessions and API keys (they live in the store,
      `pebble_store_auth.go`), and telemetry (`cmd/root.go:266-310`). The
      listener must stay free of anything that needs them. The cut-over deploy's wait step
      (`scripts/deploy-cutover.sh`, plan A9) polls it and fails the deploy on
      `stopped` or timeout. The unit is `Type=simple`
      (`deploy/audiobook-organizer.service:59`), so no systemd start-timeout
      handling is needed;
   b. takes its own backup **only when no marker exists**: a Pebble
      checkpoint of each store, written beside that store on its own dataset,
      `<parent of store dir>/.migration-backups/<from>-<to>-<time>/<store>`.
      This is the `checkpointStagingRoot` rule from
      `internal/backup/backup.go:569`, with its own directory name so the
      24-hour staging sweep (`:541-551`) never reaches it. Before
      checkpointing, the migration calls `backup.ensureSameDevice` (`:577`)
      for each store's checkpoint root and that store's directory; if the
      devices differ it stops in the "migrating" state with the reason and
      never copies, because pebble's `LinkOrCopy` falls back to a full byte
      copy without reporting an error (`vfs.go:435-451`; the codebase has
      met this: `backup.go:546-549`, "a full 14 GB copy"). After
      `Checkpoint` returns, it confirms each checkpoint `.sst` has a link
      count of at least 2. A checkpoint is a set of hard links to files
      Pebble never modifies, so it preserves the exact pre-migration state
      against a converter bug. Then, in one synced batch, it writes the stamp
      at the **target** format and the marker `storage_migration = {from, to,
      checkpoint_dir, started_at}`, and rewrites the A4 sidecar to the target
      value, all before the first legacy delete. The sidecar is rewritten
      first, so a crash between the two leaves the sidecar ahead of the
      stamp (the previous build and `make rollback` refuse, which is safe),
      never behind it. It also writes
      `checkpoint_dir` to a plain file beside the store,
      `<store path>.migration-checkpoint` (written to a temp name, synced,
      renamed), because `make rollback` reads files over ssh and cannot open
      the store, and the marker itself is cleared in 2e. That file stays until
      release E removes the checkpoint. The stamp and sidecar written
      before the first delete are how the previous build and `make rollback`
      see a half-converted store as too new and refuse it. A restart that finds the marker resumes and takes no new
      checkpoint, so there is exactly one checkpoint per cut-over, byte-equal
      to the store before migration. A ZFS snapshot is not required. The
      checkpoint does not protect against losing the pool; the app's backup
      archives, which hold both stores from release C on (5.3), do that. No
      ordering between the two checkpoints is needed here because nothing
      writes during the migration. It is removed only in release E with
      owner sign-off; after normal service resumes it is a read-only
      reference for repairing individual records, not a routine restore
      point (section 11);
   c. converts in groups of N records (start at 1,000) per worker, in one
      per-worker indexed batch (`db.NewIndexedBatch()`; a plain `Batch`
      returns `ErrNotIndexed` on Get, and every production reader is bound to
      the committed `p.db`, so verification must be handed the batch). Per
      book: read the legacy range once, counting every key read (`N_read`)
      separately from parsing; write the new entries; verify by calling the
      same reconstruct function the production read path uses, which takes a
      `pebble.Reader`, passing the batch (memdb is not consulted while
      migrating). For every parsed legacy key, the canonical raw JSON of the
      legacy value (keys sorted, minus `unversionedBookKeys`) must equal
      reconstruct's output byte for byte; decoded structs are never
      compared, because a struct hides any key it no longer has. Delete
      legacy with point Deletes of exactly the keys that were read and
      verified, never a `DeleteRange` over the book's prefix (a prefix range
      can hold unparseable keys and a book id containing `:` sorts inside
      another book's range); held and unparseable keys stay. Before commit,
      assert `N_read = N_verified + N_held`. For release C the signal batch
      is committed with Sync, verified through the production signal
      accessor over the staged main batch, then the main batch is committed
      with Sync: two fsyncs per group, not per record (per record would be
      about 1.5M fsyncs on ZFS for 777k rows). Each batch writes the new form
      and deletes the legacy in one atomic write, so whatever legacy remains
      is unconverted, and a restart (any build that supports the target
      format, including a fixed one) resumes by scanning what remains; there
      is no cursor. Work is computed by a bounded pool (default
      `runtime.NumCPU()`; the B9/C9 rehearsal tunes it against the memory
      ceiling). Workers take disjoint book-id ranges, so no two touch the
      same book; no stripe is needed because nothing else is running;
   d. **held records:** a record that fails verification keeps its legacy
      data and goes on a persisted held list with the reason and the first
      differing field. Unparseable legacy keys (ignored today,
      `pebble_store.go:3242`) and undecodable values count as held; held
      records are revisited on resume and re-held idempotently. **Orphan
      history** (a `book_ver:` range with no `book:` row; `DeleteBook` has
      always left it behind, 4.7) is its own class: it goes on the orphan
      list, is never counted as held, and is not part of the stop rule.
      When a **file** record is held in release C, the converter copies the
      record's full stored value verbatim (no decode) to `held:file:<fileID>`
      in the signal store, reads it back and compares bytes before the record
      goes on the held list; the legacy row stays as it is, and the chokepoint
      refuses writes to it with `ErrRecordHeld` (5.4). The stop rule counts
      only books that have a live row: every 1,000 such records, the migration
      stops if held records exceed 1% of the records processed so far. A
      stop, and a gate failure in 2e, never return an error from
      `RunCutover`: the process stays up in the status-listener state with
      the reason shown and nothing has been lost (a returned error would exit
      `serve`, and with `Restart=on-failure`, `RestartSec=5`,
      `StartLimitBurst=5` the unit would crash-loop and then stay failed). A
      crash restarts under `Restart=on-failure` and resumes. Nothing is ruled
      on in the stopped state; the ways out are deploying a fixed build,
      which resumes from the remaining legacy, or restoring from the
      checkpoint. Both are offline actions. The owner rules on held records
      only after the stamp is current and normal startup has finished,
      through the authenticated op `storage.convert-held` (below);
   e. gates: invariant counts equal before and after (books, files,
      fingerprints, transcripts, signatures), counted with bounded iterators;
      a fingerprint version histogram (for every version value, including 0,
      the number of legacy rows with fingerprint bytes at that version equals
      the number of `bfsig:` references at that version, and the
      `HasCurrentPrint`-true count is equal before and after); legacy family
      count (by key range) equals held plus orphan-listed; a raw count of
      each converted book's `book_ver:<id>:` range after commit equals that
      book's held count; every held file record has a verified `held:file:`
      copy; pins written by the converter = distinct (book, nonzero TS) pairs
      referenced by the merge journal that have a legacy copy, and that
      number plus the no-version list accounts for every journal reference.
      Then the marker is cleared (the stamp is already at the target) and
      normal startup continues: `RunCutover` returns, the store closes, and
      the normal open runs init, so the memdb warmup starts only after the
      stamp is current and reads only converted rows (the C9 rehearsal checks
      that "memdb warmup starting" is logged after the marker-cleared line).
3. **Stamp newer than the build:** refuse to start (the guard above).

**Held and orphan records after startup.** The on-demand op
`storage.convert-held` takes explicit book or file ids and runs the same
converter and verification, following P5: a dry run, a reviewable list of
held ids with reasons and first differing fields, then "reconvert with the
fixed build" or "purge with the loss recorded", applied by explicit ids. For
a book that already has `bookhist:` entries (it kept receiving chokepoint
writes after cut-over), the op anchors the legacy chain on
`reconstruct(oldest new entry)`, never on the live row, and appends the
legacy entries below it. Each orphan range is converted to an orphan record
or purged with the loss recorded, by owner ruling. A purge of a held file
record removes only the main-store row; the `held:file:` copy is never
deleted (P7).

**Concurrency (all new whole-library ops).** Every new nightly or on-demand
op that walks the whole library (history prune 4.7, reconcile 5.3, archive
move and restore 6, `compact-op-logs` 7, `storage.convert-held`, the release D
purges) uses `registry.RunItems` with an explicit `Concurrency`
(`RunItemsOptions.Concurrency` defaults to 0 and is clamped to 1, so an
omitted field is a sequential loop). Use `NumCPU` for CPU-bound or read-only
work (reconcile checksums, the D3 census). Use a stated smaller number, with
the reason, for purges and moves that write heavily. Any counter read by the
`Label` closure is atomic or behind a mutex, because `Label` runs in every
worker. An op that changes `book:`, `book_file:` or `bookhist:` keys while
the app is live (prune, archive move, archive restore, `convert-held`) splits
work by book id and holds that book's write stripe for each per-book batch,
and says so in a code comment. The startup converters are the exception:
nothing else is running, so they need the bounded pool and disjoint ranges
but no stripe.

Because nothing else is running during the cut-over, there are no
mixed-format records, no conversion on read, no temporary legacy readers and
no need to pause repairs. Restoring, before the app has served (or with
owner approval after, section 11), means: stop; replace the store directories
with the directory named in the marker's `checkpoint_dir` (never "the newest
`.migration-backups/` entry"); start `<bin>.pre-format-<N>`, the binary of
the release before the cut-over, which `scripts/deploy-cutover.sh` saved
before it replaced the binary. `.prev` is not that binary: the deploy
template copies the running binary to `.prev` on every deploy
(`Makefile.local.example:82`), so after one routine deploy following the
cut-over `.prev` is a post-cut-over build. (The untracked `Makefile.local`
on the owner's machine has not been checked.) The `.pre-format-<N>` binary
must be able to open the checkpoint, which the pinned `FormatMajorVersion`
guarantees. `make
rollback` applies the rule above: it swaps in `.prev` only when the store's
stamp (read from the A4 sidecar) is not above the format `.prev` supports
(`.prev --print-storage-format`). Otherwise it refuses, swaps nothing, and
prints these restore steps, naming the `checkpoint_dir` the cut-over
recorded (the marker's field, read from `<store path>.migration-checkpoint`;
never the newest `.migration-backups/` entry). The post-deploy check in `Makefile.local.example` (and the
untracked `Makefile.local`, which the owner changes the same way) accepts a
matching version while `migrating` is true and prints the status instead of
"Roll back with: make rollback".

Before each cut-over reaches prod, the same build is started on the rebuilt
rehearsal sandbox (plan A8) against an atomic ZFS clone of prod's store
datasets, with prod's memory limits and with the scheduler, op resume, scans
and outbound fetchers disabled. The old sandbox was torn down 2026-07-18 and
its scripts point at the pre-2026-09-09 dataset and a 16 GB tmpfs, so it
cannot be reused as is. Duration is unknown until the rehearsal measures it.
A lower bound is the bytes the converter must read (census, per family) at
the measured warmup read rate (about 36 MB/s: 4,003 MB in 110 s), plus synced
commits times measured fsync latency; for reference, the last full
compaction took 28.5 min (R3). The owner schedules each cut-over from the
rehearsal figure, not from an estimate (Q3).

**Release A: measure and speed up, no format change.** Pebble metrics (with
write amplification per level); census; db-health; format stamp, pinned
`FormatMajorVersion` and rollback guard; timeline indexes; op-log throttle
and tail read; the cut-over deploy script; the rehearsal sandbox; then Pebble
settings one per deploy, with the unit's `MemoryMax` raised first.

**Release B: book history.** Chokepoint, change entries, `n`, pins (pin
reason on the chokepoint, `PinBookVersion`), monotonic ids,
no-entry-on-no-change with the split would-skip counter, signature sidecar
written only when changed, `CreateBook` refusal, exact-id reads, prune
endpoint and button removed, ratchet. The converter:

- finishes the signature side-entry move for any row still holding inline
  signatures;
- per book, diffs each legacy copy against the next newer state as raw JSON
  objects, minus `unversionedBookKeys`, and sets `n` on every entry with the
  same reset at each keyframe and pin;
- keeps a keyframe for every 20th entry and, as a pin with id equal to the
  legacy nanos, for every nonzero `WinnerPreMergeTS` / `LoserPreMergeTS` in
  every `dedup:automerge:` entry, whatever its age. An entry with a zero TS
  on either side (provisional entries, or a failed patch step,
  `merge_journaled.go:184-200`), or a referenced nanos with no legacy copy
  for that book (for example removed earlier by `prune-book-snapshots`),
  goes on a "journal references with no version" list in the migration
  report. These are not held records: the undo target was already gone;
- deliberately does not carry: inline signature copies (rebuildable),
  historical `updated_at` values (not recorded by the new format);
- keeps every entry. Retention (newest 50 + pins) is not applied by the
  converter; the first nightly prune is a dry run with a summary for the
  owner, because it deletes history that has no other copy (P5);
- lists history of hard-deleted books (orphan ranges) for the owner instead
  of converting it.

**Release C: file records and signal store.** Signal store with pairing and
reconcile; type change; accessors; `bfsig:` keys; index rewrite on move under
the owner stripes; changed-detection on the projection; memdb write-through
before unlock; windowed prints moved; two-store backup and restore. The
converter moves each inline fingerprint, transcript and diagnostic to the
signal store by raw JSON key surgery (never a decode and re-marshal through
the struct), reads each back from the signal store and compares it with the
base64-decoded raw JSON value taken from the legacy row (not with the
converter's own buffer), then rewrites the row without the fields and writes
`bfsig:` in an indexed batch verified through the production signal
accessor, which takes a main-store `pebble.Reader`. `bfsig.version` is copied
verbatim from the raw `acoustid_fp_version` key: an absent key gives 0,
never `fingerprint.PrintEncodingVersion` or any other default, so a legacy
garbage print stays legacy and `HasCurrentPrint` stays false (defaulting it
would promote every pre-2026-09-19 print to current and corrupt fuzzy dedup
library-wide). `duration_sec` is copied verbatim the same way. A row that
has a version or duration key but no `acoustid_fingerprint` bytes is held
with reason "fp metadata without fingerprint bytes". Per-record verification
compares `HasCurrentPrint()` on the legacy row with the same call on the
converted row. Book-level signature side entries move the same way to
`booksig:<bookID>:<sum>` with `sigref` on the row. The converter moves each
book's live `intro_transcription` to `txb:` and every distinct
`intro_transcription` value found in that book's `bookhist:` entries (the
store is content-addressed, so duplicates collapse), then writes
`orphan:book` records for the history ranges release B kept and releases
them; the nightly prune may delete those entries only after this step. It
moves the `fpwin:` / `fpwin_fail:` keys raw (checksum appended), reading each
back before deleting the main-store copy. It also scans undo-ledger rows
(`operation_change`) of ChangeType `book_file_delete` and
`book_file_repoint`: their `OldValue` is a whole serialized `BookFile`
(`dedupe_book_file_rows_crossfolder.go:191-230`,
`repoint_book_file_rows.go:690-703`, "the ENTIRE row") and can be the only
copy of a deleted file's fingerprint or transcript. For each `OldValue` that
holds `acoustid_fingerprint`, `intro_transcription` or
`fingerprint_diagnostic_json`, it puts the bytes under `fp:` / `tx:` /
`diag:` and reads them back; if there is no live row for the file it also
writes `orphan:file:` from the blob. The ledger value is not changed. The C
gate reports ledger-held signals found = signals stored.

**Release D: consolidate and purge.** Op-log pack op; operation record
retention; legacy fingerprint-index rows (written before the era check;
counted against `fpidx_meta:` and current prints first); fetch-cache rows
whose book is gone; the dead `operationlog:` family. Each deletion: dry run,
list or aggregate with exceptions, owner approval, capped runs. A purge
refuses to run when its reference set is empty or implausibly small (cache
not warm, model list not loaded).

**Release E: finish.** Blocked until held = 0 and orphan-listed = 0 for
releases B and C, each resolved by owner ruling (converted, or purged with
the loss recorded); the converter deletion checks this and refuses otherwise,
so no legacy row is ever left in a format nothing can read (P3, P4). Delete
converters and migration-only readers. Owner signs off; checkpoints and old
ZFS snapshots are purged; snapshot schedule paused; one parallel full
compaction; schedule resumed; census recorded as the after-numbers.

**Release F: full no-op skip and recompute storms.** Using the counters from
B and C: skip the whole write when nothing changed, with the ensure steps of
4.5 and the write-through ordering of 5.6. Merge `scanner.go:1069`
`UpdateScanCache` and `:1093` `MarkNeedsRescan` into one store call that sets
the stamp and the final `needs_rescan` value in a single `ModifyBook`, keeping
the file-row mirror (`bookStampDescribesExactlyOneFile`): one book write per
processed file, not two. Move the remaining per-row file loops to the batch
variants that recompute a book once.

**Release G: archive** (section 6).

Separate specs, referenced here and not part of this program: metadata
fetch-cache policy (owner decisions 2026-10-03: stale is fine with periodic
refresh; not-found is never cached); embedding storage (each vector is stored
twice, `embedding_store.go:96`, `:542`); fingerprint-index shape (61-78M keys,
F5); the `book:work:` index, whose value is a full stale book copy
(`pebble_store.go:2633`) and should store the id only, as
`book:versiongroup:` does, or be measured first; whole-book fingerprints and
transcripts (they are `kind=whole` rows of the `fpwin:` family in the signal
store, 5.2a).

## 10. Test strategy

- Property test on a real store: random sequences of writes, unchanged
  writes, writes that change only unversioned keys, pins, restores, prunes,
  a simulated bypass write and non-ASCII and `<>&` values; an oracle keeps
  full copies; every `reconstruct` equals the oracle minus
  `unversionedBookKeys`, or returns `ErrVersionChainBroken` for the bypass
  case only; `n` resets at every keyframe and pruning the oldest entries
  never changes `n` on the kept ones; a write reads exactly one history
  entry.
- Converter tests on fixtures written by the current code: every legacy shape
  found on prod (inline signature, removed fields, a key absent from `Book`
  that must survive reconstruct, unparseable key, a key with a non-numeric
  suffix and a book id containing `:` (both held, both present after
  commit), undecodable value, 800-entry book, version-0 print with the key
  absent, current-version print, version with no bytes), cut at every staged
  write and resumed, end state compared with an uninterrupted run; a staged
  batch that differs from committed state (verification must see the batch);
  a raw post-commit range count. Kill at every staged write, restart, and
  assert exactly one checkpoint exists, byte-equal to the store before
  migration; kill after one converted batch, open with the previous
  `SupportedStorageFormat`, assert `StorageFormatTooNewError` from the stamp
  and from the sidecar; force the 1% stop and assert the process stays up,
  the version endpoint reports migrating, and no new checkpoint is taken on
  restart; `WarmFromPebble` and `beginMemWarmupBuffering` never run while the
  stored stamp is older than the build's; a fresh in-memory store and a
  testutil store come out stamped current and never reach the converter; a
  non-serve entry point given an older stamp refuses and does not convert;
  with `deviceIDFn` (`internal/backup/backup.go:558`) stubbed to report a
  cross-device layout, the migration refuses and converts nothing: it stops
  in the migrating state with the reason, `.migration-backups/` stays empty,
  stamp, marker, sidecar and `.migration-checkpoint` file are unchanged, and
  the legacy key count is unchanged; `diagnostics query --raw` and
  `pebble-inject-skip` still open a store the guard refuses.
- Signal store: kill between signal write and `bfsig` commit; replace; clear;
  move between books; start with a missing or mismatched store; reconcile
  finds a planted missing and a planted corrupt signal; race probes with
  `bookFileBeforeCommitHook` for a stale batch upsert across a `Set`, a move
  across a `Set`, two concurrent `Set`s on one file, and a `Set` refused on a
  `file_hash` change; a held file row written to with `UpdateBookFile` and
  `UpsertBookFile` keeps its stored bytes, returns `ErrRecordHeld`, and
  `held:file:<id>` matches; a `book_file` delete and a `dedupe-book-file-rows
  remove_row_ids` run leave every window's bytes readable; a corrupt or
  missing `booksig:` entry has no effect on `GetBookByID` or search results;
  a one-field edit to a signed book writes no `booksig` key (release B).
- Reflection tests tie the file-row type, the memdb projection, the field
  class table, `unversionedBookKeys` and the recompute projection together.
- Existing tests that pin old behaviour are rewritten in the same PR as the
  behaviour (list in the plan).
- Benchmarks before and after: book write (p50/p99, history entries read per
  write), file write, warmup, timeline, version list, `lsh-index-build`,
  signature synthesis, rows written by a no-change rescan.

## 11. Risks

| Risk | Mitigation |
|---|---|
| A write bypasses the chokepoint | post-hash detects it and the next write lays a keyframe; CI ratchet; `CreateBook` refusal |
| Converter is wrong on some legacy shape | verification through the production reader over an indexed batch, raw-byte compare, point deletes of verified keys only, per-book raw count; held list; rolling 1% stop; sandbox rehearsal on real data |
| Row and signal disagree | immutable content-addressed keys; `bfsig:` beside the row, written only by Set/Clear under the owner stripe; read-back on write; checksum on read; nightly reconcile with index checks; pairing stamp |
| Old binary started on converted or half-converted data | format stamp raised before the first delete; sidecar; pinned Pebble format version; `make rollback` guard; `.pre-format-<N>` binary saved by the deploy script |
| Restore after the app has served loses every write since the cut-over (book edits, merges, applied fixers, undo-journal rows, scan results) and can leave the DB out of step with iTunes library changes already written (`internal/itunes/itl.go`) | Wholesale restore is the response to a failure found during the migration or before normal service resumes. Once the app has served, the policy is fix-forward: `storage.convert-held` on the affected records, using the checkpoint as a read-only source of the pre-migration values. A wholesale restore after serving needs explicit owner approval (Q6), with the list of operations and iTunes writes since the cut-over shown first |
| First purge floods compaction | range deletes; per-run caps; purges after the cut-overs, before the final compaction |
| Estimates rest on a 55-book sample | census in release A; each release re-measured; release A exit states the rule if the census contradicts the estimate |
| Partial new format reaches prod from an intermediate PR | B and C tasks merge to integration branches; main receives the rehearsed tip only inside the deploy window (plan) |

## 12. Open questions for the owner

- Q1. Book history retention: newest 50 entries of real change per book plus
  pins (proposed).
- Q2. Operation logs: packed after 1 day; packs kept 90 days; operation
  records 180 days (proposed; `interrupted_*` and non-terminal rows exempt).
- Q3. Downtime: two offline cut-overs. The rehearsal on the rebuilt sandbox
  gives the real duration before either is scheduled; there is no derived
  estimate before that (the earlier "15-40 minutes" was a placeholder).
- Q4. Stop the weekly scheduled full compaction (two ops run one today,
  `plugins/maintenance/db.go:22`, `scheduler/extra_ops.go:603`) and shorten
  ZFS snapshot retention on the app data dataset. A full compaction followed
  by a snapshot pins a whole extra copy (F12).
- Q5. Fold the per-field metadata history (`metadata_change:`, 36 call sites,
  each its own synced write outside the book's batch) into the new change
  entries, so there is one book history written atomically with the row?
  Cost: provenance fields added to the change entry and 36 call sites moved.
  Proposed as a follow-up release, not part of B.
- Q6. Restore horizon: wholesale checkpoint restore allowed only until the
  first request is served after the cut-over (proposed), or up to N hours;
  after that, fix-forward (section 11).
- Q7. LSH Hamming tier (Tier-0) is dead today (5.4): revive it (two reads per
  candidate, up to 200 candidates per print, about 605k prints; C9 reports
  dedup full-scan wall time, candidate count and signal-store reads per
  print before cut-over) or delete it. Decide before C3.
- Q8. Set `primarycache=metadata` on the Pebble dataset once the 4 GB block
  cache is in place, to stop double caching with the ARC? Deciding metric:
  ARC hit rate before and after.
- Q9. Each orphan `book_ver:` range (history of hard-deleted books) and each
  held record: convert to an orphan record, or purge with the loss recorded
  (P5). Release E is gated on these rulings.
