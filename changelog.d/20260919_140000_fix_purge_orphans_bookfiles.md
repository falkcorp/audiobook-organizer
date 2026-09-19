### Fixed

- Deleting a book no longer orphans its `book_file` rows. `DeleteBook` removes the book row and its indexes but never its file rows. Before this fix, every hard delete of a book that still owned files left those rows pointing at a book that no longer exists. `DeleteBook` now refuses such a book with `database.ErrBookOwnsFiles`. To remove it, move the rows to the book that should own them first (`MoveBookFilesToBook`). The rows are never deleted.
- That refusal is race-free. `DeleteBook`'s row count and its commit hold the book's new owner lock, and every `book_file` writer takes the same lock at commit:
  - Create, batch create, batch upsert and move check that the target book still exists. A row whose book is gone is refused on its own and every other row in the batch is written. The batch writers return `*BookFileRowsRefusedError`, which lists the refused rows and missing books, and the iTunes sync, duration backfill and tag backfill log those and carry on instead of dropping or aborting the whole batch.
  - Every row a writer rewrites or moves is re-checked under the lock before the write: a prior iTunes-ID owner, a matched upsert row, a move source, update, patch and scan-cache stamps. A row deleted meanwhile is never written back. Two concurrent moves can't leave one row under two books, and a delete racing a move deletes the row wherever the move put it.
  - The disk flush (fsync) now runs after the lock is released, so a large batch no longer blocks single-book writers for the length of the flush.
- The soft-delete purge no longer hard-deletes a book that still owns file rows. A dedup-merge loser keeps its own files by design, so before this fix every purged loser orphaned them. Such a book now stays soft-deleted (hidden and restorable). The refusal happens before any side effect. It is counted in `skipped_owns_files` and listed in `skipped_owns_files_ids`, not in `errors`, and the nightly purge reports the count once per run.
- With `purge_soft_deleted_delete_files` on, the purge no longer removes a file that another book still uses. Duplicate book rows over one file are the commonest dedup merge, and purging the loser used to delete the survivor's audio. The check now looks at every `book_file` row at the path (`BookFilesAtPath`) and at every live book at the path, and keeps the file if the answer is incomplete. The old single-valued path index could miss a row that still pointed at the file.
- Reconcile's duplicate version-group cleanup keeps any duplicate that owns file rows. It checks this before touching the disk, logs a duplicate as removed only once past that check, and counts the rest in `skipped_owns_files`.
- A user's permanent delete of a book that owns file rows is refused before any hash block or iTunes remove. The server returns 409, and the book page and the library now say how many file rows the book owns and what to do instead.
- If an organize rollback cannot delete the book row it created, it now keeps the files it wrote, because the surviving row may point at them.

### Removed

- The archive sweep (`maintenance.archive-sweep`, `scheduler.archive-sweep`, and the `archive_sweep` scheduled task). It duplicated the purge without the purge's safeguards: it deleted every file of a soft-deleted book after a hardcoded 30 days, with no protected-path check and no config switch, and it orphaned the rows. Its listing excluded soft-deleted books, so it never ran in practice.
- The `{"delete": true}` mode of `maintenance.orphan-book-files-cleanup`, because it deleted `book_file` rows. The op is now report-only, and a request with `delete` set is refused with a pointer to the repoint plan.

### Added

- `maintenance.orphan-book-files-repoint-plan`, a dry-run op with no apply mode. For each existing orphan `book_file` row it plans a target. Each row falls into one of these buckets:
  - a duplicate of a row that another book already owns, using every row at the path;
  - a duplicate of another orphan already planned onto the same book;
  - the only live book at the file or its directory;
  - the merge survivor recorded on the missing book's tombstone;
  - several possible owners;
  - unresolved.
  
  The full plan is stored as the op's result (`GET /api/v1/operations/<id>/result`), and the log says where. When the `book_atpath` index is not built, the live-book-at-path check is skipped and the log says so.
