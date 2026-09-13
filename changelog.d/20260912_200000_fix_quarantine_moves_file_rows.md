### Fixed

- Quarantining a book now repoints every one of its `book_file` rows to the
  file's new location under `.failed/`, in the same pass that moves the files.
  Before, only the `books` row moved, so a quarantined book's files read as
  missing while the audio sat under `.failed/`, where the recover-missing-files
  walk never looks. Unquarantine moves the rows back. No row is ever deleted,
  and existing files are never overwritten.
- Each book now gets its own quarantine folder,
  `.failed/<author>/<title> [<book id>]/`, so two copies of the same book can
  no longer share a folder and pick up each other's files.
- Path history is written before each move it describes: the book's entry
  when a pass starts, and each file's entry right before the file is renamed.
  If a history write fails, that move is stopped before it happens, so no file
  ends up under `.failed/` without a record of where it came from.
- A file whose destination is taken keeps its row and is named in the error,
  and the book is not marked quarantined. Running quarantine again resumes the
  same pass. It reuses the source layout and destination folder the first
  pass recorded, even if the title changed since, as long as the book is still
  at that pass's source or destination. A file an interrupted pass already
  moved is picked up only if its history entry and its size (and hash, when
  known) show it is that file. A book folder is picked up only if its move is
  recorded and every file in it matches its row. An unrelated file or folder
  sitting at a destination is never taken over. A folder the pass moves itself
  always carries its rows along, even for a file edited since the last scan.
- A row whose file was already missing before quarantine is left alone and
  logged instead of blocking the quarantine forever.
- Unquarantine picks history by timestamp, so a book quarantined twice goes
  back to where it was before the second quarantine. A row with no per-file
  history that sits in the book's quarantine folder is mapped back by its
  place in that folder. Any other row under `.failed/` with no recorded origin
  keeps the book quarantined rather than being left behind.
- Added a test proving the recover-missing-files walk skips a configured
  database directory that has no leading dot.
