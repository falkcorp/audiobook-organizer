### Fixed

- **Deleting a series from the UI could strand its books.** Both series delete
  endpoints — `DELETE /series/:id` and `POST /series/bulk-delete` — decided
  whether a series was empty using the counter that fills in the badge next to a
  series, which deliberately skips books in the trash and non-primary (duplicate)
  versions. Those books still hold the `series_id`, so a series whose books were
  all trashed, or all alternate versions, counted as zero and was deleted out
  from under them, leaving books pointing at a series that no longer exists and a
  name that cannot be recovered. This is the same defect fixed in the weekly
  prune; the interactive path still had it. Both endpoints now count references
  in every book state, and refuse to delete at all if that count is unavailable
  rather than falling back to the filtered one.
