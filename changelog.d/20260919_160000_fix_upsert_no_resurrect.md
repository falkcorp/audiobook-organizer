### Fixed

- An upsert can no longer recreate a `book_file` row that has been deleted. Deleting a row now leaves a per-ID tombstone (`book_file_gone:<id>`). Both `BatchUpsertBookFiles` and the single-row `UpsertBookFile` check it:
  - A row naming a deleted ID is refused. The batch form refuses that row alone, with a reason, and commits the rest. The single-row form returns `ErrBookFileRowDeleted`.
  - A row naming a live ID updates that row, even when another row shares its path. Production still has duplicate-path rows, and refusing them would repeat on every backfill. The one exception: if the row's iTunes ID is held by a different live row, it is refused (`ErrBookFilePIDConflict`).
  - A row with an unknown ID, such as the fresh IDs the scanner assigns, or with no ID at all is matched by path or iTunes ID as before.
  
  Checking an ID is point reads only, never a full table scan. This closes the case where the tag or duration backfill, holding rows across a long run, wrote back a row that dedupe had just deleted.
