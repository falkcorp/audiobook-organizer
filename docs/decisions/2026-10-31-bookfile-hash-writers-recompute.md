<!-- file: docs/decisions/2026-10-31-bookfile-hash-writers-recompute.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d2c4e91-3a58-4b6f-8e0d-5f1a9c3b7e26 -->
<!-- last-edited: 2026-10-31 -->

# TODO-BF-HASH-WRITERS-RECOMPUTE: when a hash write recomputes its book

## Decision log

Context: `SetBookFileHash` and `UpdateBookFileHashes` committed through
`updateBookFileOver(..., false)` and never recomputed the owning book, on the
comment "Hashes feed no book aggregate". `ComputeBookRuntime`
(`missingDuplicateOf`) and `OwnFolderFiles` (`IsBookFileCopy`) both compare
rows by hash, so a hash written onto the present row that pairs it with a
missing repoint twin (or a copy) changes `Book.Duration` / `Book.FileSize`.

Callers found (gopls references and grep): extract-wav-clips
(`SetBookFileHash`, one call per row, when the row has no hash),
`fileops.WriteTagsSafe` and `fileops.recordHashes` / `book_file_hash_recorder`
(`UpdateBookFileHashes`, one call per rewritten file). backfill-file-hashes
does NOT call either: it stores through `ModifyBookFileDeferAggregates` and
recomputes each book once per chunk (`recomputeBooks`), which is unchanged.

Chosen: the store decides. After the hash-only commit, `hashWriteMovesAggregate`
asks whether any hash value the row moved (old or new, per field that changed)
equals a `FileHash` or `OriginalFileHash` of a sibling live row, read from the
memdb Core projection (Pebble per-book range before memdb publishes). Only then
does the writer recompute, after the stripe is released like every other
`updateBookFileOver` caller. The aggregate depends on hashes only through
equality between two rows, so if no sibling shares a value before or after, no
comparison changed and the stored aggregate is still right. A tag write gives
every chapter a fresh unique digest, so a 50-chapter write-back still costs no
recompute (the reason the skip existed). A read error recomputes.

`SetBookFileHash` also notified only on `uerr == nil`; it now uses
`bookFileApplied` like the other writers, so a write whose fsync failed after
the commit still refreshes the aggregates.

Rejected:

- Always recompute in the store: restores the 50-recomputes-per-write-back cost
  (each decodes every row of the book) that the skip was added to remove.
- Recompute only when the row or a sibling is Missing: wrong, the copy-cluster
  rule pairs two PRESENT rows by `FileHash`.
- A no-recompute variant of the two methods for bulk callers: no bulk caller
  uses them. The one bulk hash writer already has its own deferred writer.
- Index lookup of the hash (`BookFilesWithHash`): it refuses when memdb is not
  serving, which would have to fail open to a recompute anyway, and it is
  cross-book where only the owning book matters.
