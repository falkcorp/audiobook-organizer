### Added

- **Repairs fixer `consolidation-leftovers` (owner decision 2026-10-05).**
  The 2026-09-06 chapter consolidation moved each chapter's audio into a
  combined book and left the old one-chapter book live. Its `book_file`
  row still names a path that no longer exists and was never marked
  Missing, so the files API reported it present.
  - Plan lists every live book with 1 to 3 rows, at least one not marked
    Missing, whose row paths and book path are all gone from disk
    (`os.Stat` ENOENT only; any other stat error holds the row), and where
    exactly one other live book owns an on-disk file of the same byte size
    under the dead row's series/author folder. Hashes must agree when both
    sides have one, and the twin needs agreeing evidence (equal hash, the
    same chapter number in both file names, or the same duration); two
    equally good candidates hold the row. The combined book must be listed
    by Audiobookshelf (an organized, unquarantined primary). A missing or
    empty library root fails the plan. The disk stats run on a bounded pool of 8.
  - Ambiguous owners, hash disagreements, split owners, a present book
    path, an unsafe scope folder or a stat error are held. Leftovers with
    no match (`no-match`) and iTunes-owned books (`itunes`) are their own
    never-applicable classes. iTunes-owned means a book or row iTunes
    persistent id, an itunes external id, or a file inside the iTunes media
    folder; a row's bare iTunes path reference does not count (owner
    decision 2026-10-06; a fixer-local check, so the other fixers' shared
    `itunesCopyWhy` is unchanged). Doctor Who / Big Finish / Torchwood are held
    by the framework guard.
  - Apply re-plans under the merge lock (with a fresh walk of the scope
    folder), marks each dead row Missing in place (journaled, never
    deleted), then retires the leftover into the combined book through the
    shared `retireInto`: listening state follows as a slice of the combined
    book (the position lands at the chapter's offset; a finished chapter
    never finishes the combined book), external ids follow, the
    version group gets a primary, and the book is soft-deleted with
    `merged_into_book_id` set. Every step reverts with the apply operation.
