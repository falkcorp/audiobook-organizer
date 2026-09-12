### Fixed

- Quarantining a book now repoints every one of its `book_file` rows to the
  file's new location under `.failed/`, in the same pass that moves the files.
  Before, only the `books` row moved, so a quarantined book's files read as
  missing while the audio sat under `.failed/`, where the recover-missing-files
  walk never looks. Unquarantine moves the rows back. Rows always match the
  disk, no row is ever deleted, and existing files are never overwritten.
- A file whose destination is already taken keeps its row and is named in the
  error, and the book is not marked quarantined. Running quarantine again
  resumes from where the files actually are: it keeps the original
  destination even if the title changed, and keeps subfolders such as `disc2/`.
  It also repairs a pass interrupted between a file move and its database
  write, instead of failing on the missing source.
- A row whose file was already missing before quarantine is left alone and
  logged instead of blocking the quarantine forever.
- Unquarantine now picks the newest quarantine history by timestamp. A book
  quarantined twice goes back to where it was before the second quarantine,
  not the first.
- Each file move is journaled as a `quarantine_file` path-history row, written
  only after the book row is saved, so a rolled-back pass leaves no stale
  entries.
- Added a test proving the recover-missing-files walk skips a configured
  database directory that has no leading dot.
