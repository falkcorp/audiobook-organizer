### Fixed

- Combining books no longer destroys the absorbed books, and every combine can
  be undone. `merge.Service.CombineBooks` (used by `POST /audiobooks/combine`
  and by approved review-queue `combine` and `duplicate-of` items, which apply
  for real in production) used to HARD-delete every absorbed book. It now
  soft-deletes them and writes a combine journal
  (`merge:combine-journal:<ULID>`, `internal/merge/combine_journal.go`) before
  it changes anything. If the journal cannot be written, the combine is refused.
  The journal records file-row moves and each file's disc/track numbers before
  the combine (including the survivor's own files, which the multidisc apply
  renumbers), the rows the combine created, external-ID mappings, the ABS sync
  redirect, per-user listening progress on both books, and any survivor
  title/narrator/author override with its lock rows.
- The soft-deleted shell's `FilePath` is cleared, because that path now
  belongs to a file the survivor owns. `PurgeSoftDeletedBooks` with
  delete-files enabled removes a purged single-file book's `FilePath` from disk,
  so a shell that kept its path would have had the survivor's audio deleted when
  it was purged. The journal keeps the original path for undo.
- Combining a book that is already soft-deleted is now refused with
  `merge.SoftDeletedInputError` (409), the same as `MergeBooks`.

### Added

- `POST /api/v1/merge/undo/:journal_id` reverses a combine under the merge
  lock. It restores the absorbed books and their `FilePath`, moves their file
  rows and sync_file inos back, and restores disc/track numbers. It deletes the
  file rows the combine created, moves external IDs back, clears the ABS
  redirect (new `PebbleStore.ClearSyncMerge`), restores listening progress, and
  rolls back an override. Survivor progress is left alone if the user has
  listened since, and the response says so. The endpoint checks everything
  before it writes anything. It returns 409 with every reason, and writes
  nothing, if the library changed after the combine: a file moved, deleted or
  re-merged; an absorbed book purged or restored; a mapping reassigned; or the
  overridden metadata edited. `GET /api/v1/merge/combine-journal?limit=N`
  lists journals newest-first. `POST /audiobooks/combine` now returns
  `journal_id`.
