### Added

- **`maintenance.missing-file-repair`** — deletes `book_file` rows whose bytes are
  gone, so downloads stop 404ing on rows that point at nothing.

  It deliberately does NOT repair everything. A row is only deleted when its book
  keeps at least one surviving file. Measured on a 120-book sample, 5 books had
  *every* row dead; deleting those would leave the book with no files at all,
  turning a wrong index into a lost book. Those books are skipped and named in the
  report for a human to look at.

  A book with any path that cannot be stat'd is skipped entirely, including its
  confirmed-missing siblings — "I could not tell" is not "it is gone", and one
  unmounted share must not present a whole tree as deletable.

  **Dry run by default.** `{"apply": true}` is required to delete anything, and
  `max_deletes` caps the blast radius of a single run. Every deleted path is
  logged before the delete so the change is reconstructible from the operation log.
