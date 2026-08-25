### Fixed

- **Diagnosed why new books never appear:** the scan finds and ingests them
  correctly, but every ingested row is written `is_primary_version=false` and placed
  alone in its own version group. A one-member group whose only member is non-primary
  has no primary at all, and the library page requests primary-only by default — so
  **16,460 books are in the database and unreachable from the default view.** The
  write-up in `docs/diagnostics/` also records three further defects visible in the
  same rows (one book per track, folder name used as author, filename used as title)
  and clears two dead ends so they are not re-walked.
