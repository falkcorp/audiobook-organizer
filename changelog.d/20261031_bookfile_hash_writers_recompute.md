### Fixed

- `SetBookFileHash` and `UpdateBookFileHashes` (extract-wav-clips, tag
  write-back, `book_file_hash_recorder`) skipped the owning book's aggregate
  recompute on the claim that a hash feeds no aggregate. It does: a missing row
  whose hash matches a present row is a repoint duplicate dropped from
  `Book.Duration`, and rows sharing a `file_hash` are copies of which one counts.
  A hash that paired or un-paired two rows left `Book.Duration` stale until
  something else recomputed the book. Both writers now recompute the book when
  a hash they moved equals a sibling row's hash; unrelated hashes (a tag write
  across a whole book) still cost no recompute. `backfill-file-hashes` is
  unchanged: it stores through `ModifyBookFileDeferAggregates` and recomputes
  each book once per chunk.
