### Changed

- **The `backfill-file-hashes` maintenance job now hashes files in parallel.**
  It previously walked every `book_file` in a single serial loop, which made a
  full-library hash backfill (hundreds of thousands of files on a network
  volume) run at a fraction of achievable throughput. It now processes files
  through a bounded worker pool (default 8, override with
  `ABK_HASH_BACKFILL_WORKERS`) in ordered chunks, checkpointing only after each
  chunk completes so resume-after-restart still never skips an un-hashed file.
  Per-file digests and the skip-already-hashed behaviour are unchanged.
