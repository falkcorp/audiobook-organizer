- [ ] **TRASH-PURGE-FOLLOWUPS** Four known gaps around purging trashed book
      versions, left open by the trash-window fix (PR #3957):
      (1) Claim the row atomically before any file is deleted. Today
      `CleanupTrashedVersions` re-reads the row and then calls `PurgeVersion`,
      so a restore that lands between the re-read and the purge still loses the
      version's files.
      (2) `handleTrashVersion` accepts rows in `inactive_purged`,
      `blocked_for_redownload` and `swapping_*`; it should refuse them (or
      define what trashing each means) instead of writing them into the trash.
      (3) After a purge the version's `book_file` rows are kept on purpose but
      their `file_path` still points at the deleted files; repoint or mark
      them so nothing reads a path that no longer exists.
      (4) `PurgeVersion` derives the version folder from `book.FilePath`, which
      can be stale; derive it from the version's own `book_file` rows.
