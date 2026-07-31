<!-- file: changelog.d/20260730_090000_abs-sync-syncfile-crossbook-repoint.md -->
<!-- version: 1.0.0 -->
<!-- guid: c833c7d0-e839-4739-a601-7eab4bdf367d -->
<!-- last-edited: 2026-07-30 -->

### Fixed

- **Per-file sync `ino` now follows a file across a book-id change (abs-sync,
  PR #2074 follow-up).** `sync_file` entries are keyed `(bookID, fileID)`, and
  the existing `RepointSyncFile` could only move an entry's `fileID` within one
  book — it could not carry an entry to a DIFFERENT book. Two paths move a
  `BookFile` to a different book without changing the file itself:
  `CombineBooks` (absorbed books' files are reassigned onto the survivor) and
  the scanner's untagged-move / hash-duplicate version-link (the book itself
  gets a new ULID). Neither carried the file's durable `ino` forward, so an
  offline client's cached download URL (`/api/items/{itemId}/file/{ino}`)
  could go stale after either operation, requiring a re-download of an
  already-downloaded book. Item-level identity and progress/bookmarks were
  unaffected (they key on the item, not the file).
  - New `database.(*PebbleStore).RepointSyncFileToBook(oldBookID, newBookID,
    fileID)` (`internal/database/pebble_store_syncfile.go`) moves a sync_file
    entry across books in a single atomic Pebble batch, preserving the
    sync-file ID. Idempotent (a re-run after a successful move is a no-op).
    Collision rule: if the destination book already has its OWN entry for
    that `fileID` (independently minted), the destination's existing identity
    wins and the source is left untouched — never silently reassign which
    syncFileID answers for a (book, file) pair a client may already be
    resolving against. Guarded by the existing `syncFileMintMu` mutex idiom;
    covered by a `-race` concurrent-repoint test asserting a single
    consistent outcome.
  - New `merge.FollowFileMove(db, oldBookID, newBookID, fileIDs)`
    (`internal/merge/sync_follow.go`) is wired into `CombineBooks`'s main
    file-move loop and into `attachVirtualFile`'s cross-book reattach branch
    (both in `internal/merge/service.go`) — the two places CombineBooks
    reassigns a `BookFile` row's owning book.
  - `merge.FollowBookIDChange` (already the hook for the scanner's
    hash-duplicate version-link path) now also carries every sync_file
    registered on the superseded book onto the new book id, gated behind the
    same "old book had a syncID" check as the existing identity/progress
    carry — a book with no client-visible identity could not have had a
    client-cached file URL either. No `internal/scanner/scanner.go` changes
    were needed for this path: it already calls `FollowBookIDChange`.
  - **Left for a follow-up:** `internal/dedup/book_dedup.go`'s `MergeBooks`
    hard-delete path (`merge.FollowMergeWithStore` call around line 456) does
    not move `BookFile` rows across books at all today (files stay attached
    to whichever book id the dedup engine chose to keep), so no file-ino
    carry was needed there for this gap. If that ever changes, it will need
    the same `FollowFileMove` wiring — flagged here rather than edited, since
    `internal/dedup/book_dedup.go` may have parallel work in flight.
