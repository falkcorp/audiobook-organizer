### Added

- **`POST /api/v1/operations/backfill-scan-cache`** makes the per-file scan-cache
  migration something an operator can actually run. It seeds the new file-keyed scan
  cache from the existing book-level stamps and gives single-file books the
  `book_file` row the scan never creates for them. Until it has been run once, every
  `book_file` row reads as "never scanned", so the first scan after deploying the
  file-keyed reader re-reads and re-hashes the whole library — 4–6 hours here, and the
  opposite of what a scan cache is for. Dry-run by default: an apply must be opted
  into with `?dry_run=false`.
