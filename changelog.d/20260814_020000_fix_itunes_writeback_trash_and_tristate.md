### Fixed

- The iTunes writeback preview no longer offers books that are in the trash.
  A soft-deleted book keeps its iTunes persistent ID (deletion is restorable),
  and both the mapping listing and the preview returned it, so metadata for a
  deleted book was eligible to be written back into the iTunes library. Fixed on
  both routes into the preview — the listing and the explicit `book_ids` request,
  which reads through a different store method and needed its own check.

- Two database methods ignored the `UseMemDB` flag and dispatched to the
  in-memory query layer on publication alone, `ListBooksByITunesPID` and
  `ListSoftDeletedBooks`. Their Pebble fallbacks were therefore unreachable in
  any store with memdb up, including one that had explicitly turned memdb off.
  `ListSoftDeletedBooks` is the method the orphan-file cleanup fails closed on,
  so its fallback is worth having reachable and now has a conformance test.

### Changed

- The tri-state deletion filter (exclude trashed / require trashed / require
  live) is now stated once in `includeByDeletionState` instead of being written
  out separately in the summaries path and in `GetAllBooksCore`. Passing a
  non-boolean value for the `marked_for_deletion` filter key used to disable the
  trash exclusion entirely and return both live and deleted rows; it is now
  treated as no filter at all.

- `Book.IsSoftDeleted()` is exported, so packages outside `internal/database`
  have somewhere to call instead of restating the rule.
