<!-- file: changelog.d/20260828_204000_safe_split_book_batches.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0d66af87-c8a5-4f1b-82a5-c8988b1e0bfc -->
<!-- last-edited: 2026-08-28 -->

### Added

- Reviewed split-book candidate IDs can now be preflighted and queued as one
  durable bulk operation. The request defaults to a dry run, rejects overlap,
  and snapshots candidates before queueing.

### Fixed

- A split-book candidate is retained when a merge is incomplete, rather than
  disappearing after a partial file-reassignment failure.
