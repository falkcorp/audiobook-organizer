- [ ] **PURGE-MERGE-FOLLOWUPS-4113** purge/merge follow-ups (#4113), two items
      found in review and left out of that PR on purpose:
      (1) `internal/organizer/organizer.go` (the same-version-group branch near
      line 198) refuses with `LibraryCopyExistsError` whenever a holder of the
      book's file hash is in its version group, including a library copy that is
      already inside RootDir, such as an in-root `_copy1` twin. Consider
      applying that library-copy backstop only when the book being organized is
      outside RootDir, so an in-root twin is not refused for a reason that only
      applies to a protected original. Needs a test with an in-root twin and an
      outside-root original.
      (2) The interactive purge and discard paths do work under the merge lock
      that does not need it: building the purge environment (`newPurgeEnv`) and
      the trash-progress listing (`TrashProgress`) both run while the lock taken
      by `merge.TryLockMergeRMWWithin` is held, so a slow store read there keeps
      every merge and combine waiting. Read what can be read before taking the
      lock and re-read only what the lock must protect.
