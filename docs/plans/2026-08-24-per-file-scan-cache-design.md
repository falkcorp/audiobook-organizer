<!-- file: docs/plans/2026-08-24-per-file-scan-cache-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e41d7b2-3c95-4a60-b1f7-52d0e9364caf -->
<!-- last-edited: 2026-08-24 -->

# Per-File Scan Cache — Design

**Status:** PROPOSAL. Direction (option A) approved by the user 2026-08-24;
implementation NOT approved. Companion to
`docs/superpowers/specs/2026-08-24-staged-library-scan-design.md` (local, gitignored),
whose closing recommendation ("written unconditionally, keyed by file path") this
document makes concrete.

## Problem

Multi-file audiobooks are re-read and re-hashed on **every** scan. Measured
2026-08-24, second-scan verdict: `skippedUnchanged=0 cacheMiss=1`.

The cause is a **grain mismatch**, not a bug in any one function:

> The scan cache is keyed per **book**. The skip decision is made per **file**,
> during the walk, before any book is known.

For a single-file book those two grains coincide and everything works. For a
multi-file book they diverge the moment `createBookFilesForBook` normalizes
`Book.FilePath` from `segs[0]` to the containing directory — which is correct for
the book and fatal for the cache key.

### Two independent failures. Fixing either alone changes nothing.

1. **Key grain.** `GetScanCacheMap`
   (`internal/database/pebble_store_scancache.go:44`) keys on `book.FilePath` —
   after normalization, the **directory**. The walk emits, and `classifySkipFile`
   (`internal/scanner/scanner.go:539`) looks up, the **segment file** path.
   Grouping makes zero store calls, so it cannot know the row moved. Every lookup
   misses.
2. **Value grain.** `writeBackScanCache` is handed the **directory** to stat, so
   the stored size is the directory inode's (128 bytes observed) rather than the
   segment's. Even with keys aligned, `entry.Size != size`
   (`scanner.go:546`) fails.

A third, related hole: the directory-rooted book branch (`scanner.go:1229`)
never calls `writeBackScanCache` at all, so those books have no cache entry by
construction — and the existing `scanCacheNoRowCount` counter cannot see them.

### What was already fixed, and what it did not fix

#2865 and #2867 made `createBookFilesForBook` return the path the row lives at,
and added recovery for rows normalized by an earlier scan. That fixed **chapter
persistence** and the **no-row counter**. It did **not** make scans skip
anything, and an earlier version of #2865's own prose claimed it did. Corrected
in #2867. Do not reintroduce that claim.

## Decision: key the scan cache on `book_file`

Three options were considered.

| | approach | verdict |
|---|---|---|
| **A** | Key the scan cache on `book_file` | **Chosen** |
| B | Stop normalizing `Book.FilePath` to the directory | Desired end state, not now — see below |
| C | Add a separate "scan key" column on the book | Rejected: fixes the key grain only; still stats a directory |

C is rejected because it addresses one of the two failures. A book-level scan key
still describes one path, and a multi-file book has many.

## B is the intended destination. A must not block it.

The user's stated direction: eventually stop normalizing `Book.FilePath` to the
directory — "just return the files and everything should know how to handle
them". A is a stepping stone, and the **single most important design property of
this spec** is:

> After A, the scan cache does not read `Book.FilePath` at all.

That is what makes B cheap later. Concretely, A must:

- key every cache entry on `book_file.FilePath`, never on `book.FilePath`;
- never call `os.Stat` on a directory in the write-back path;
- keep the book-level rollup (below) derived, so nothing persists a
  book-grain mtime/size that B would have to unwind.

If those three hold, B becomes a change to the *organizer* and the *UI's* notion
of a book's path, and the scan/skip layer is untouched by it.

**B carries a prerequisite that is not in this spec.** Moving a book's files
means telling Deluge the new location, or its seeding torrents break. That is
filed separately as a todo and must land before B, not with it.

## Design

### Schema

Add to `BookFile` (mirroring the existing book-level trio):

```go
LastScanMtime *int64 // unix seconds, from os.Stat of THIS file
LastScanSize  *int64 // bytes, from os.Stat of THIS file
NeedsRescan   *bool  // force re-read regardless of mtime/size
```

Pointers, deliberately — `nil` means "never scanned", which is distinct from
"scanned and measured zero". Note this repo's `omitempty` hazard: with
`GOEXPERIMENT=jsonv2`, `omitempty` **emits** `false`/`0`; use `omitzero` on these
fields or a never-scanned file will serialize as scanned-at-epoch.

### Read path

`GetScanCacheMap` returns `map[filePath]ScanCacheEntry` built from **`book_file`
rows**, not book rows. `classifySkipFile` is unchanged in shape — it already
takes a file path and already compares mtime+size. It simply starts hitting.

The directory-rooted book branch stops being a special case: its member files are
`book_file` rows like any other, so they acquire entries by the same path.

### Write path

`writeBackScanCache` takes the **file** it just processed, stats that file, and
writes the entry keyed by that file's path. It no longer looks a book up by path
at all, which deletes the entire class of failure #2865/#2867 were patching.

The book-level `LastScanMtime`/`LastScanSize` become **derived** (max mtime, sum
size) or are dropped. They must not remain an independently-written source of
truth, or B will have to unwind them.

### Book-level rollup — state this explicitly, do not discover it later

Skipping is per **file**; processing is per **book**. A book with 6 files where 5
are unchanged and 1 changed **must still be reprocessed** — its chapter timeline,
duration and size aggregates are all functions of the whole set.

So the walk skips the 5 unchanged files, the 6th is a cache miss, and that miss
must promote the whole book to "process me". The natural shape is: group first,
then a book is processed if **any** of its files is not skippable. Getting this
wrong in the other direction — processing only the changed file — silently
corrupts the aggregates, which is the failure mode `RecomputeBookAggregates`
already guards against with its partial-data rule.

## Migration and the deploy herd

Every existing `book_file` row reads as "never scanned", so the first scan after
deploy is a **whole-library re-read**. On a library that already takes 4–6 hours
that is the opposite of the intent.

Two options, and this is a user decision:

1. **Backfill** from the existing book-level entries where the book is
   single-file (key and value are already correct there), and cold-start only the
   multi-file population. Cheap, and it covers most rows.
2. **Cold start everything**, once, deliberately scheduled — accepting one long
   scan to get correct state, with the staged-scan work landing first so it is not
   a 6-hour blocking run.

Option 1 is recommended. Note the multi-file population is precisely the one that
is re-read every scan **today**, so cold-starting it costs nothing new.

## Interaction with the staged-scan spec

That spec's item 3 needs a per-book "last scanned at" field and warns the
timestamp must not be written inside the `dbBook != nil` conditional, because the
books an age gate exists to help are exactly the ones with no cache entry. A
subsumes that warning: after A there is no by-path book lookup in the write-back,
so the timestamp cannot be conditional on one. The age gate should be specified
against `book_file` entries, not book rows.

## Test plan

- **Conformance:** memdb vs Pebble for the new `GetScanCacheMap`, per this repo's
  standing rule that two implementations need a conformance test with a working
  selector.
- **The measurement that fails today:** scan twice, assert the second scan reports
  `skippedUnchanged == fileCount`. This is the assertion no existing test makes,
  and it is why the grain mismatch shipped.
- **Rollup:** 6-file book, touch one file, assert the book is reprocessed AND the
  other 5 files are skipped.
- **Never-scanned vs scanned-zero:** assert a `nil` `LastScanMtime` round-trips as
  absent, not as `0` (the `omitzero` hazard above).
- **Mutation:** every new guard, compile-verified. A mutant that does not build
  tests nothing.

## Rollback

The new fields are additive and nullable. Reverting the read path to the
book-level map restores current behaviour without a data migration; the
`book_file` columns can be left in place, unread.

## Open, not decided here

- Whether book-level `LastScanMtime`/`LastScanSize` are derived or deleted.
- Backfill option 1 vs 2.
- Whether `NeedsRescan` stays book-level, file-level, or both (the re-arm logic in
  `writeBackScanCache` currently reasons about a whole book).
