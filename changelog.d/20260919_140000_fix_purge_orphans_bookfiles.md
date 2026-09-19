### Fixed

- Deleting a book no longer orphans its `book_file` rows. `DeleteBook` tears down the book row and its indexes but never its file rows, so every hard delete of a book that still owned files left those rows naming a book that no longer exists. `DeleteBook` now refuses such a book with `database.ErrBookOwnsFiles`; move the rows to the owning book first (`MoveBookFilesToBook`). The rows are never deleted.
- The soft-delete purge (`PurgeSoftDeletedBooks`, auto-purge and the purge endpoint) no longer hard-deletes a book that still owns file rows. A dedup-merge loser keeps its own files by design, so every purged loser orphaned them. Such a book now stays soft-deleted (hidden, restorable), is counted in the new `skipped_owns_files` result field, and is named in `errors`. The refusal comes before any side effect: no external-ID tombstones, iTunes removes or tombstone snapshot for a book that stays.
- With `purge_soft_deleted_delete_files` on, the purge no longer removes a file that another book still uses. Duplicate book rows over one file are the commonest dedup merge, and purging the loser deleted the survivor's audio. A path that any `book_file` row or live book still names is now kept, and the reason is reported.
- The archive sweep no longer deletes files from disk and no longer sweeps a book that owns file rows. It used to delete every file of a soft-deleted book older than 30 days, with no protected-path check and no config switch, and then orphan the rows. The sweep is inert today, because its listing excludes soft-deleted books. That is left as is; reviving or retiring it is an owner decision.
- Reconcile's duplicate version-group cleanup keeps any duplicate that owns file rows, and decides this before touching the disk. It reports the count as `skipped_owns_files`.
- A user hard delete of a book that owns file rows is now refused before the hash block or iTunes removes, and returns 409 Conflict rather than 404.

### Added

- `maintenance.orphan-book-files-repoint-plan`, a dry-run-only op with no apply mode. For each existing orphan `book_file` row it plans a target: a duplicate of a row another book owns, the only live book at the file or its directory, the merge survivor recorded on the missing book's tombstone, several possible owners, or unresolved. Nothing is written. Applying a plan is an owner decision.
