- [ ] **TODO-PURGE-KEPT-FILES-ORPHANED** A file kept on an `inactive_purged` version
      because a trashed book, or a lingering row of a purged version, still names it
      (`database.BookVersion.PurgeKept`, reported as `KeptFiles` by the nightly trash
      cleanup) is never revisited. Once the trashed book is hard-deleted or the
      lingering row is gone, nothing looks at the file again and it stays on disk with
      no owner. Fix: a sweep (or a step of the nightly cleanup) that re-asks the owner
      checks for each path in `PurgeKept` of purged versions and removes the ones no
      row or book names any more, then drops them from `PurgeKept`.
