<!-- file: docs/decisions/2026-10-30-bookfile-acoustid-seg-keys.md -->
<!-- version: 1.0.0 -->
<!-- guid: c7e2a5b9-4d13-4f86-9a0e-2b5d8f1c6e37 -->
<!-- last-edited: 2026-10-30 -->

# TODO-BF-ACOUSTID-SEG-KEYS-NOT-PERSISTED: index only what the row stores

## Decision log

Context: every book_file create and update wrote one `book_file_acoustid:<seg>`
key per `AcoustIDSeg0..6` value on the caller's struct, while the stored row has
dropped those columns since T020 (`marshalBookFileDropSegs`). The delete paths
and `SweepBookFileSegDrop` find a row's segments in the stored row, so nothing
could ever delete those keys. The TODO offered two fixes: stop writing keys for
values the row does not store, or persist the values.

Chosen: stop writing the keys, plus a one-off repair op. Rejected alternatives:

- **Persist the segments.** Undoes T020 (about 200-400 MB of Pebble space, and
  the memdb strip), and makes `SweepBookFileSegDrop` fight every writer.
- **Keep the index, but derive the key from the stored fingerprint** (Seg0 is
  `DeriveSeg0(AcoustIDFingerprint)`) so deletes can recompute it. Only the fpcalc
  path is derivable; the ffmpeg fallback's Seg1..6 are not, and it would need a
  backfill of the whole library. A larger design change than the TODO asked for;
  worth its own item if the exact-match index is wanted back.

What changed, item by item:

- **One definition of "what the row stores"**: `bookFileStoredForm`. The row is
  marshalled from it (`marshalBookFileDropSegs`) and the index writers
  (`writeBookFileSecondaryIndexes`, and the retired-row branch of
  `writeBookFileSecondaryIndexesFor`) derive their keys from it, so the two
  cannot drift apart again. Today it clears all seven segments, so no
  `book_file_acoustid:` key is written.
- **`singleOwnerKeysOf` is unchanged.** The retire, un-retire and rehome writers
  feed it rows read from Pebble, and a legacy row still stores its segments and
  still has its keys; those must keep moving with the row.
- **`moveBookFilesToBookBulkAttempt` now writes the stored form.** It was the one
  row writer that marshalled the whole struct, so a moved legacy row kept its
  segments. With the index derived from the stored form that would have left a
  row holding a segment with no key. It is the same T020 invariant as every other
  write, so I fixed it in this change rather than leaving an inconsistency; it is
  the only line outside the brief's named functions. Consequence:
  `TestCAS_SweepBookFileSegDrop_FollowsAMovedRow` used a real move to produce a
  moved legacy row; it now stages that move by hand.
- **The repair lives in `database` (store primitives) and `maintenance` (the
  op).** The scan, judge and delete need raw key access and the single-owner
  index mutex, which only the store has; the op composes them with
  `registry.RunItems` (`Concurrency: runtime.NumCPU()`), which `database` cannot
  import. The store side is a capability resolved with `database.AsCapability`,
  so `database.Store` and its mocks are untouched.
- **Page, do not collect.** Each key embeds a whole segment string, so the index
  is scanned 2048 entries at a time; only a page is in memory.
- **Judge twice.** The pool judges a page lock-free to find candidates. The delete
  judges each candidate again from committed state inside the
  `commitBookFileBatchVerified` verify hook, under `lockSingleOwnerIndex`, which
  every writer of these keys takes around its apply. That closes the stale-index
  race (a retire repointing a key between the judgement and the delete) without
  an `indexCAS` retry loop, because the decision and the apply are one step. A
  retire that had read a key the repair then deletes fails its own
  `indexCAS.verify` and re-stages.
- **Orphan = row gone, or row exists without that segment.** A ref whose row
  moved is followed by `book_file_id:`. A key whose row still stores the segment
  is live and left for `dedup.bookfile-seg-drop`. A value that is not
  `<bookID>:<fileID>` is reported (warning in the summary) and never deleted:
  nothing can judge it.
- **Preview by default** (owner rule 2026-09-25): `{}` and `dry_run=true` delete
  nothing; only `dry_run=false` does.

## What the owner should read before running it with dry_run=false

`GetBookFileByAcoustID` answers from this index. Callers: `dedup`
`CollectExactAcoustID` and the engine's exact tier, and
`deluge.discovery`'s intro-segment check. Findings:

- The dedup exact tier and the engine read `AcoustIDSeg*` off rows that come
  from Pebble reads or memdb. The stored row has none and memdb strips them, so
  those probes already ask for an empty segment; they never hit the index in
  production for rows written after T020. Behaviour-neutral for them.
- `fingerprintBookFile` (acoustid backfill) is the only producer of segment
  values. On the fpcalc path it sets `AcoustIDSeg0 = DeriveSeg0(raw)` over the
  120 s analysis window; the Deluge check computes `FileHeadSegment` over 300 s.
  Those strings differ for any file longer than 120 s, so the Deluge exact probe
  could not match a key written by the fpcalc path either. On the ffmpeg
  fallback path (fpcalc missing) Seg0 is the 300 s head segment and the Deluge
  probe could match; that is the one real behaviour this change turns off. The
  fuzzy fallback beside it reads `AcoustIDSeg*` from memdb rows (stripped), so
  it does not catch it.
- Preview first. Its count is the number of keys that stop answering.

Not done, noted for later: nothing rebuilds an exact-match index from the stored
fingerprint (see the rejected alternative above), and
`GetBookFileByAcoustIDFuzzy` and the dedup exact tier still read
`AcoustIDSeg*` fields that no stored row carries.
