### Fixed

- Creating or updating a `BookFile` no longer writes `book_file_acoustid:<segment>`
  index keys for `AcoustIDSeg0..6` values the stored row does not keep (T020 drops
  them from the row). Nothing could ever delete such a key afterwards: the delete
  paths and `dedup.bookfile-seg-drop` find a row's segments in the stored row, so
  the keys outlived the fingerprint they were written for, and the row itself.
  The row and its index keys now come from one definition of the stored form, in
  every writer: create, update, upsert, batch create, PID transfer, the retired-row
  update, and `MoveBookFilesToBook`, which was the one writer that still kept the
  legacy segment columns of a moved row. A legacy row that is updated now loses its
  index keys together with its segments.

### Added

- `maintenance.bookfile-acoustid-index-repair` deletes the orphaned
  `book_file_acoustid:` keys the old writers left behind: keys whose row is gone,
  or exists without that segment. Preview by default (`dry_run=false` deletes);
  each delete is judged again under the single-owner index lock, so a key a retire
  repointed to a live row is kept. Keys whose row still stores the segment are left
  for `dedup.bookfile-seg-drop`; malformed values are reported, never deleted.
  `GetBookFileByAcoustID` stops answering for a deleted key, so run the preview
  first and read `docs/decisions/2026-10-30-bookfile-acoustid-seg-keys.md`.
