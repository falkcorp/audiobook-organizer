### Added

- **`maintenance.merge-same-path-dupes`** — merges duplicate book records that
  point at the exact same audio file into one, keeping the record you applied
  metadata to. These duplicates were created when an apply renamed a single-file
  book but left its file row on the old path, so the next scan minted a second
  record at the new path (see the single-file organize fix in the same release).
  The op is deliberately narrow: same exact file only (never same-directory), and
  it merges only when the stored file hash is present and identical across the
  records — a record whose hash disagrees is flagged for review, not merged. It
  uses the safe merge path (losers soft-deleted, external IDs reassigned) and is
  report-only by default; pass `{"apply": true}` to merge.
