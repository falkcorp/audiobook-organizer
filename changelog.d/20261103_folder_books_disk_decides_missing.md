### Fixed

- Folder-sized books repair: whether a file is missing is now decided by looking
  at the disk, not by the library's "missing" flag. The flag goes stale in both
  directions, so an apply could create a book for a file that was already gone,
  and could leave a file that is still there hidden on the retired folder row.
  A file that is gone from disk now stays on the retired folder row and never
  becomes a book, and a file that is present always gets its own book with the
  file recorded as present. If a file cannot be checked (a permission error, or
  its library folder looks unmounted or empty), the row is listed as unreadable
  and nothing is applied to it. Plans made before this change show as changed
  since plan and need a re-run.
