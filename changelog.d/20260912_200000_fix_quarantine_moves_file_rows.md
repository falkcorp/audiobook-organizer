### Fixed

- Quarantining a book now repoints every one of its `book_file` rows to the
  file's new location under `.failed/`, in the same pass that moves the files.
  Before, only the `books` row moved, so a quarantined book's files read as
  missing while the audio sat under `.failed/`, where the recover-missing-files
  walk never looks. Unquarantine moves the rows back. A file that cannot move
  keeps its row and is named in the error, and existing files are never
  overwritten. Each file move is journaled as a `quarantine_file` path-history
  row so the reverse move knows where each file came from.
- Added a test proving the recover-missing-files walk skips a configured
  database directory that has no leading dot.
