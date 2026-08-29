### Added

- **Bulk prune of copy-on-write book version snapshots.** A new
  `prune-book-snapshots` maintenance job prunes version history across the whole
  library instead of one book at a time, keeping the newest `keep_count` (default
  10) snapshots per book. Measured on production: `book_ver:` holds 7.65 GB, of
  which 85.4% is a `book_sig_v1` copy that is unchanged in 96% of edits.
